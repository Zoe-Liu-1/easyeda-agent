package app

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

type dcTextMove struct {
	AttributeID string      `json:"attributeId"`
	ParentID    string      `json:"parentId"`
	Key         string      `json:"key"`
	Value       string      `json:"value"`
	FromBBox    *layoutBBox `json:"fromBBox,omitempty"`
	ToBBox      *layoutBBox `json:"toBBox,omitempty"`
	FromAnchor  *checkPoint `json:"fromAnchor,omitempty"`
	ToAnchor    *checkPoint `json:"toAnchor,omitempty"`
	DeltaX      float64     `json:"deltaX"`
	DeltaY      float64     `json:"deltaY"`
}

type dcPairPlan struct {
	Net       string      `json:"net"`
	LabelA    string      `json:"labelA"`
	LabelB    string      `json:"labelB"`
	Distance  float64     `json:"distance"`
	Status    string      `json:"status"` // already-clear | move-proposed | unresolved
	Path      string      `json:"path,omitempty"`
	BlockersA []string    `json:"blockersA,omitempty"`
	BlockersB []string    `json:"blockersB,omitempty"`
	Move      *dcTextMove `json:"move,omitempty"`
	Note      string      `json:"note,omitempty"`
}

type dcPlanReport struct {
	SchemaVersion int          `json:"schemaVersion"`
	DocumentID    string       `json:"documentId"`
	MaxDistance   float64      `json:"maxDistance"`
	MaxMove       float64      `json:"maxMove"`
	Pairs         []dcPairPlan `json:"pairs"`
	ProposalCount int          `json:"proposalCount"`
}

type dcBlocker struct {
	Kind     string
	ID       string
	ParentID string
	Label    string
}

func newSchDirectConnectPlanCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var jsonOut bool
	var out string
	var maxDistance, maxMove float64
	cmd := &cobra.Command{
		Use:   "direct-connect-plan",
		Short: "Propose small text/attribute moves that enable direct label connections (read-only)",
		Long: `Read the active page's labels, wires, parts, visible text and native attribute anchors.
For same-net labels on different wire islands, find blocked orthogonal L paths and
try moving a blocking part Designator/Value attribute by small 5-unit grid offsets.
The command only reports proposals; it never calls modify, wire, connect or save.
Power/ground/rail labels are excluded from direct-connect candidates.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
				"includeBBox": true, "includePins": true, "includePinNets": false, "includePagePrimitives": true,
			})
			if err != nil {
				return err
			}
			if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
				return fmt.Errorf("direct-connect-plan: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
			}
			comps, err := parseLayoutComps(geom.Result)
			if err != nil {
				return err
			}
			wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
			if err != nil {
				return err
			}
			vt, err := requestAction(pinned, "schematic.visible_text.bboxes", win, nil)
			if err != nil {
				return err
			}
			if vt.Context != nil && vt.Context.DocumentUUID != "" && vt.Context.DocumentUUID != doc {
				return fmt.Errorf("direct-connect-plan: visible text came from %q, expected %q", vt.Context.DocumentUUID, doc)
			}
			var items struct {
				Items []visibleTextItem `json:"items"`
			}
			raw, _ := json.Marshal(vt.Result)
			if err := json.Unmarshal(raw, &items); err != nil {
				return fmt.Errorf("direct-connect-plan: parse visible text: %w", err)
			}
			anchors := parseVisibleAttributeAnchors(geom.Result)
			sheet := sheetBBoxOf(comps)
			titleBlock, _ := titleBlockKeepoutWithSource(sheet)
			pairs := planDirectConnectTextMoves(items.Items, comps, wires, anchors, sheet, titleBlock, maxDistance, maxMove)
			report := dcPlanReport{SchemaVersion: 1, DocumentID: doc, MaxDistance: maxDistance, MaxMove: maxMove, Pairs: pairs}
			for _, p := range pairs {
				if p.Move != nil {
					report.ProposalCount++
				}
			}
			if jsonOut || out != "" {
				b, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return err
				}
				b = append(b, '\n')
				if out != "" {
					return os.WriteFile(out, b, 0644)
				}
				_, err = stdout.Write(b)
				return err
			}
			renderDirectConnectPlan(report, stdout)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit structured JSON")
	cmd.Flags().StringVar(&out, "out", "", "write structured JSON to file")
	cmd.Flags().Float64Var(&maxDistance, "max-distance", 250, "maximum Manhattan distance between label anchors")
	cmd.Flags().Float64Var(&maxMove, "max-move", 40, "maximum text move per axis, in 5-unit steps")
	return cmd
}

func parseVisibleAttributeAnchors(result map[string]any) map[string]checkPoint {
	out := map[string]checkPoint{}
	pp, _ := result["pagePrimitives"].(map[string]any)
	attrs, _ := pp["attributes"].([]any)
	for _, raw := range attrs {
		m, _ := raw.(map[string]any)
		id, _ := m["primitiveId"].(string)
		x, okx := numberField(m["X"])
		y, oky := numberField(m["Y"])
		if id != "" && okx && oky {
			out[id] = checkPoint{X: x, Y: y}
		}
	}
	return out
}

func numberField(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok && !math.IsNaN(f) && !math.IsInf(f, 0)
}

func planDirectConnectTextMoves(items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	anchors map[string]checkPoint, sheet, titleBlock *layoutBBox, maxDistance, maxMove float64) []dcPairPlan {
	find, wireIndex := wireTreeFinder(wires)
	type label struct {
		it   visibleTextItem
		root int
		x, y float64
	}
	var labels []label
	for _, it := range items {
		if it.Kind != "attribute" || it.Key != "Name" || it.ComponentType != "wire" ||
			it.ParentID == "" || it.BBox == nil || strings.TrimSpace(it.Value) == "" ||
			isRailLabelNet(it.Value) || (it.Visible != nil && !*it.Visible) {
			continue
		}
		idx, ok := wireIndex[it.ParentID]
		if !ok {
			continue
		}
		labels = append(labels, label{it, find(idx),
			(it.BBox.MinX + it.BBox.MaxX) / 2, (it.BBox.MinY + it.BBox.MaxY) / 2})
	}
	var out []dcPairPlan
	for i := 0; i < len(labels); i++ {
		for j := i + 1; j < len(labels); j++ {
			a, b := labels[i], labels[j]
			if a.root == b.root || a.it.Value != b.it.Value {
				continue
			}
			dist := math.Abs(a.x-b.x) + math.Abs(a.y-b.y)
			if dist > maxDistance {
				continue
			}
			pair := dcPairPlan{Net: a.it.Value, LabelA: a.it.ID, LabelB: b.it.ID, Distance: dist}
			paths := [][][2]float64{
				{{a.x, a.y}, {b.x, a.y}, {b.x, b.y}},
				{{a.x, a.y}, {a.x, b.y}, {b.x, b.y}},
			}
			blockers := make([][]dcBlocker, 2)
			for pi, path := range paths {
				blockers[pi] = directPathBlockers(path, a.root, b.root, a.it.ID, b.it.ID,
					items, comps, wires, find, wireIndex)
			}
			if len(blockers[0]) == 0 || len(blockers[1]) == 0 {
				pair.Status = "already-clear"
				pair.Path = "h-v"
				if len(blockers[1]) == 0 {
					pair.Path = "v-h"
				}
				out = append(out, pair)
				continue
			}
			pair.BlockersA = blockerLabels(blockers[0])
			pair.BlockersB = blockerLabels(blockers[1])
			move, pathName := chooseTextMove(paths, blockers, a.root, b.root, a.it.ID, b.it.ID,
				items, comps, wires, anchors, sheet, titleBlock, maxMove)
			if move != nil {
				pair.Status = "move-proposed"
				pair.Path = pathName
				pair.Move = move
			} else {
				pair.Status = "unresolved"
				pair.Note = "no single part Designator/Value move on a 5-unit grid clears an L path"
			}
			out = append(out, pair)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Net != out[j].Net {
			return out[i].Net < out[j].Net
		}
		return out[i].LabelA < out[j].LabelA
	})
	return out
}

func blockerLabels(bs []dcBlocker) []string {
	var out []string
	for _, b := range bs {
		out = append(out, b.Label)
	}
	return out
}

func directPathBlockers(points [][2]float64, ra, rb int, aID, bID string,
	items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	find func(int) int, wireIndex map[string]int) []dcBlocker {
	var out []dcBlocker
	add := func(b dcBlocker) {
		for _, x := range out {
			if x.Kind == b.Kind && x.ID == b.ID {
				return
			}
		}
		out = append(out, b)
	}
	for k := 0; k+1 < len(points); k++ {
		p, q := points[k], points[k+1]
		if p == q {
			continue
		}
		for _, c := range comps {
			if c.ComponentType == "sheet" {
				continue
			}
			if c.BBox != nil && c.ComponentType == schLayoutPartType && schSegmentCrossesBox(p[0], p[1], q[0], q[1], *c.BBox) {
				name := c.Designator
				if name == "" {
					name = c.ID
				}
				add(dcBlocker{Kind: "part", ID: c.ID, Label: "part:" + name})
			} else if c.BBox != nil && isSchMarker(c.ComponentType) &&
				schSegmentCrossesBox(p[0], p[1], q[0], q[1], *c.BBox) {
				add(dcBlocker{Kind: "marker", ID: c.ID, Label: "marker:" + markerLabel(c)})
			}
			for _, pin := range c.Pins {
				if pointOnSegment2(pin.X, pin.Y, p, q) {
					add(dcBlocker{Kind: "pin", ID: c.ID + ":" + pin.Number, Label: "pin:" + c.Designator + "." + pin.Number})
				}
			}
		}
		for _, it := range items {
			if it.BBox == nil || it.ID == aID || it.ID == bID {
				continue
			}
			if schSegmentCrossesBox(p[0], p[1], q[0], q[1], *it.BBox) {
				add(dcBlocker{Kind: "text", ID: it.ID, ParentID: it.ParentID,
					Label: "text:" + textLabel(it)})
			}
		}
		for _, w := range wires {
			idx, ok := wireIndex[w.ID]
			if ok && (find(idx) == ra || find(idx) == rb) {
				continue
			}
			for _, s := range labelWireSegments(w) {
				if segmentsCross2(p, q, [2]float64{s[0], s[1]}, [2]float64{s[2], s[3]}) {
					add(dcBlocker{Kind: "wire", ID: w.ID, Label: "wire:" + w.ID})
				}
			}
		}
	}
	return out
}

func chooseTextMove(paths [][][2]float64, blockers [][]dcBlocker, ra, rb int, aID, bID string,
	items []visibleTextItem, comps []layoutComp, wires []schGroupWire, anchors map[string]checkPoint,
	sheet, titleBlock *layoutBBox, maxMove float64) (*dcTextMove, string) {
	candidates := map[string]visibleTextItem{}
	for _, bs := range blockers {
		for _, b := range bs {
			if b.Kind != "text" {
				continue
			}
			for _, it := range items {
				if it.ID == b.ID && it.Kind == "attribute" && it.ComponentType == "part" &&
					(it.Key == "Designator" || it.Key == "Value") && it.BBox != nil {
					candidates[it.ID] = it
				}
			}
		}
	}
	if len(candidates) == 0 {
		return nil, ""
	}
	var best *dcTextMove
	bestCost := math.Inf(1)
	bestPath := ""
	for _, it := range candidates {
		for _, d := range []float64{5, 10, 15, 20, 25, 30, 35, 40} {
			if d > maxMove {
				break
			}
			for _, delta := range [][2]float64{{d, 0}, {-d, 0}, {0, d}, {0, -d}, {d, d}, {d, -d}, {-d, d}, {-d, -d}} {
				moved := it
				b := *it.BBox
				b.MinX += delta[0]
				b.MaxX += delta[0]
				b.MinY += delta[1]
				b.MaxY += delta[1]
				moved.BBox = &b
				if !textBoxClear(moved, it.ID, items, comps, wires, sheet, titleBlock) {
					continue
				}
				for pi, path := range paths {
					if len(directPathBlockers(path, ra, rb, aID, bID, replaceText(items, moved), comps, wires,
						wireTreeFind(wires), wireIndexMap(wires))) != 0 {
						continue
					}
					cost := math.Abs(delta[0]) + math.Abs(delta[1])
					if cost < bestCost {
						m := &dcTextMove{AttributeID: it.ID, ParentID: it.ParentID, Key: it.Key, Value: it.Value,
							FromBBox: it.BBox, ToBBox: &b, DeltaX: delta[0], DeltaY: delta[1]}
						if pt, ok := anchors[it.ID]; ok {
							m.FromAnchor = &checkPoint{X: round2(pt.X), Y: round2(pt.Y)}
							m.ToAnchor = &checkPoint{X: round2(pt.X + delta[0]), Y: round2(pt.Y + delta[1])}
						}
						best, bestCost, bestPath = m, cost, map[bool]string{true: "v-h", false: "h-v"}[pi == 1]
					}
				}
			}
		}
	}
	return best, bestPath
}

func replaceText(items []visibleTextItem, moved visibleTextItem) []visibleTextItem {
	out := make([]visibleTextItem, len(items))
	copy(out, items)
	for i := range out {
		if out[i].ID == moved.ID {
			out[i] = moved
		}
	}
	return out
}

func textBoxClear(moved visibleTextItem, originalID string, items []visibleTextItem, comps []layoutComp,
	wires []schGroupWire, sheet, titleBlock *layoutBBox) bool {
	if moved.BBox == nil || sheet == nil {
		return false
	}
	usable := schUsableArea(*sheet)
	if moved.BBox.MinX < usable.MinX || moved.BBox.MaxX > usable.MaxX ||
		moved.BBox.MinY < usable.MinY || moved.BBox.MaxY > usable.MaxY {
		return false
	}
	if titleBlock != nil && boxesOverlap2(*moved.BBox, *titleBlock) {
		return false
	}
	for _, c := range comps {
		if c.BBox != nil && c.ComponentType != "sheet" && boxesOverlap2(*moved.BBox, *c.BBox) {
			return false
		}
		for _, pin := range c.Pins {
			if pin.X >= moved.BBox.MinX-0.5 && pin.X <= moved.BBox.MaxX+0.5 &&
				pin.Y >= moved.BBox.MinY-0.5 && pin.Y <= moved.BBox.MaxY+0.5 {
				return false
			}
		}
	}
	for _, it := range items {
		if it.ID != originalID && it.BBox != nil && boxesOverlap2(*moved.BBox, *it.BBox) {
			return false
		}
	}
	for _, w := range wires {
		for _, s := range labelWireSegments(w) {
			if schSegmentCrossesBox(s[0], s[1], s[2], s[3], *moved.BBox) {
				return false
			}
		}
	}
	return true
}

func boxesOverlap2(a, b layoutBBox) bool {
	return a.MinX <= b.MaxX && b.MinX <= a.MaxX && a.MinY <= b.MaxY && b.MinY <= a.MaxY
}

func labelWireSegments(w schGroupWire) [][4]float64 {
	if len(w.ObservedSegments) > 0 {
		return w.ObservedSegments
	}
	var out [][4]float64
	for i := 0; i+3 < len(w.Points); i += 2 {
		out = append(out, [4]float64{w.Points[i], w.Points[i+1], w.Points[i+2], w.Points[i+3]})
	}
	return out
}

func pointOnSegment2(px, py float64, a, b [2]float64) bool {
	cross := (b[0]-a[0])*(py-a[1]) - (b[1]-a[1])*(px-a[0])
	return math.Abs(cross) <= 0.5 &&
		px >= math.Min(a[0], b[0])-0.5 && px <= math.Max(a[0], b[0])+0.5 &&
		py >= math.Min(a[1], b[1])-0.5 && py <= math.Max(a[1], b[1])+0.5
}

func segmentsCross2(a, b, c, d [2]float64) bool {
	o := func(p, q, r [2]float64) float64 {
		return (q[0]-p[0])*(r[1]-p[1]) - (q[1]-p[1])*(r[0]-p[0])
	}
	o1, o2, o3, o4 := o(a, b, c), o(a, b, d), o(c, d, a), o(c, d, b)
	if ((o1 > 0.5 && o2 < -0.5) || (o1 < -0.5 && o2 > 0.5)) &&
		((o3 > 0.5 && o4 < -0.5) || (o3 < -0.5 && o4 > 0.5)) {
		return true
	}
	return (math.Abs(o1) <= 0.5 && pointOnSegment2(c[0], c[1], a, b)) ||
		(math.Abs(o2) <= 0.5 && pointOnSegment2(d[0], d[1], a, b)) ||
		(math.Abs(o3) <= 0.5 && pointOnSegment2(a[0], a[1], c, d)) ||
		(math.Abs(o4) <= 0.5 && pointOnSegment2(b[0], b[1], c, d))
}

func wireTreeFind(wires []schGroupWire) func(int) int {
	f, _ := wireTreeFinder(wires)
	return f
}

func wireIndexMap(wires []schGroupWire) map[string]int {
	_, m := wireTreeFinder(wires)
	return m
}

func textLabel(it visibleTextItem) string {
	v := strings.TrimSpace(it.Value)
	if v == "" {
		v = it.ID
	}
	return v
}

func renderDirectConnectPlan(rep dcPlanReport, w io.Writer) {
	fmt.Fprintf(w, "direct-connect-plan: %d pair(s), %d text-move proposal(s) (document %s)\n",
		len(rep.Pairs), rep.ProposalCount, rep.DocumentID)
	for _, p := range rep.Pairs {
		switch p.Status {
		case "already-clear":
			fmt.Fprintf(w, "  [clear] %s %s ↔ %s distance=%.0f path=%s\n", p.Net, p.LabelA, p.LabelB, p.Distance, p.Path)
		case "move-proposed":
			fmt.Fprintf(w, "  [move] %s %s ↔ %s distance=%.0f path=%s\n", p.Net, p.LabelA, p.LabelB, p.Distance, p.Path)
			fmt.Fprintf(w, "         move %s %s by (%+.0f,%+.0f)\n", p.Move.Key, p.Move.AttributeID, p.Move.DeltaX, p.Move.DeltaY)
		default:
			fmt.Fprintf(w, "  [blocked] %s %s ↔ %s distance=%.0f\n", p.Net, p.LabelA, p.LabelB, p.Distance)
			if len(p.BlockersA) > 0 {
				fmt.Fprintf(w, "         h-v blockers: %s\n", strings.Join(p.BlockersA, ", "))
			}
			if len(p.BlockersB) > 0 {
				fmt.Fprintf(w, "         v-h blockers: %s\n", strings.Join(p.BlockersB, ", "))
			}
			if p.Note != "" {
				fmt.Fprintf(w, "         %s\n", p.Note)
			}
		}
	}
}
