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

// dcMarkerMove is a read-only proposal for the second layer of direct-connect
// planning: translate a netflag/netport together with its single-segment stub.
// The marker keeps its rotation (power/ground body direction is unchanged); only
// its anchor and the stub are re-routed on an orthogonal L. The far (host)
// endpoint never moves, so the electrical attachment stays where it was.
type dcMarkerMove struct {
	MarkerID        string       `json:"markerId"`
	ComponentType   string       `json:"componentType"`
	Net             string       `json:"net"`
	FromAnchor      *checkPoint  `json:"fromAnchor"`
	ToAnchor        *checkPoint  `json:"toAnchor"`
	DeltaX          float64      `json:"deltaX"`
	DeltaY          float64      `json:"deltaY"`
	StubWireID      string       `json:"stubWireId"`
	Host            *checkPoint  `json:"host"`
	NewStubPath     [][2]float64 `json:"newStubPath"`
	NewMarkerBBox   *layoutBBox  `json:"newMarkerBBox"`
	AttachedTextIDs []string     `json:"attachedTextIds,omitempty"`
}

type dcMarkerSkip struct {
	MarkerID string `json:"markerId"`
	Net      string `json:"net"`
	Reason   string `json:"reason"`
}

type dcPairPlan struct {
	Net         string         `json:"net"`
	LabelA      string         `json:"labelA"`
	LabelB      string         `json:"labelB"`
	Distance    float64        `json:"distance"`
	Status      string         `json:"status"` // already-clear | move-proposed | marker-move-proposed | unresolved
	Path        string         `json:"path,omitempty"`
	PathPoints  [][2]float64   `json:"pathPoints,omitempty"`
	BlockersA   []string       `json:"blockersA,omitempty"`
	BlockersB   []string       `json:"blockersB,omitempty"`
	Move        *dcTextMove    `json:"move,omitempty"`
	MarkerMove  *dcMarkerMove  `json:"markerMove,omitempty"`
	MarkerSkips []dcMarkerSkip `json:"markerSkips,omitempty"`
	Note        string         `json:"note,omitempty"`
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
try two read-only resolutions on a small 5-unit grid:
  1. move a blocking part Designator/Value attribute;
  2. translate a blocking netflag/netport together with its single-segment stub,
     re-routing only that stub on an orthogonal L while the far endpoint stays put.
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
				if p.Move != nil || p.MarkerMove != nil {
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
		it     visibleTextItem
		root   int
		ax, ay float64 // attachment point on the wire tree (route endpoint)
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
		ax, ay, ok := labelAttachmentPoint(it, wires, wireIndex, find)
		if !ok {
			continue
		}
		labels = append(labels, label{it: it, root: find(idx), ax: ax, ay: ay})
	}
	var out []dcPairPlan
	for i := 0; i < len(labels); i++ {
		for j := i + 1; j < len(labels); j++ {
			a, b := labels[i], labels[j]
			if a.root == b.root || a.it.Value != b.it.Value {
				continue
			}
			dist := math.Abs(a.ax-b.ax) + math.Abs(a.ay-b.ay)
			if dist > maxDistance {
				continue
			}
			pair := dcPairPlan{Net: a.it.Value, LabelA: a.it.ID, LabelB: b.it.ID, Distance: dist}
			paths := dedupeDCPaths([][][2]float64{
				{{a.ax, a.ay}, {b.ax, a.ay}, {b.ax, b.ay}},
				{{a.ax, a.ay}, {a.ax, b.ay}, {b.ax, b.ay}},
			})
			if len(paths) == 0 {
				continue
			}
			blockers := make([][]dcBlocker, len(paths))
			for pi, path := range paths {
				blockers[pi] = directPathBlockers(path, a.root, b.root, a.it.ID, b.it.ID,
					items, comps, wires, find, wireIndex)
			}
			clearIdx := -1
			for pi := range paths {
				if len(blockers[pi]) == 0 {
					clearIdx = pi
					break
				}
			}
			if clearIdx >= 0 {
				pair.Status = "already-clear"
				pair.Path = dcPathName(paths[clearIdx])
				pair.PathPoints = paths[clearIdx]
				out = append(out, pair)
				continue
			}
			pair.BlockersA = blockerLabels(blockers[0])
			if len(blockers) > 1 {
				pair.BlockersB = blockerLabels(blockers[1])
			}
			move, pathIdx := chooseTextMove(paths, blockers, a.root, b.root, a.it.ID, b.it.ID,
				items, comps, wires, anchors, sheet, titleBlock, maxMove)
			if move != nil {
				pair.Status = "move-proposed"
				pair.Path = dcPathName(paths[pathIdx])
				pair.PathPoints = paths[pathIdx]
				pair.Move = move
			} else {
				// Text moves are less invasive than re-routing a stub, so they win
				// whenever they work. The marker layer is the fallback for blockers
				// owned by a netflag/netport (its visible Name or its body).
				markerMove, markerIdx, skips := chooseMarkerMove(paths, blockers, a.it.Value,
					a.it.ParentID, b.it.ParentID, a.it.ID, b.it.ID,
					items, comps, wires, sheet, titleBlock, maxMove)
				pair.MarkerSkips = skips
				if markerMove != nil {
					pair.Status = "marker-move-proposed"
					pair.Path = dcPathName(paths[markerIdx])
					pair.PathPoints = paths[markerIdx]
					pair.MarkerMove = markerMove
				} else {
					pair.Status = "unresolved"
					pair.Note = "no single part-attribute move or netflag/netport stub move on a 5-unit grid clears an L path"
				}
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

func cleanDCPath(p [][2]float64) [][2]float64 {
	out := make([][2]float64, 0, len(p))
	for _, q := range p {
		if len(out) > 0 {
			last := out[len(out)-1]
			if math.Abs(q[0]-last[0]) < 0.001 && math.Abs(q[1]-last[1]) < 0.001 {
				continue
			}
		}
		out = append(out, q)
	}
	return out
}

func dedupeDCPaths(paths [][][2]float64) [][][2]float64 {
	var out [][][2]float64
	seen := map[string]bool{}
	for _, p := range paths {
		p = cleanDCPath(p)
		if len(p) < 2 {
			continue
		}
		var b strings.Builder
		for _, q := range p {
			fmt.Fprintf(&b, "%.2f,%.2f;", q[0], q[1])
		}
		key := b.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out
}

// dcPathName describes the orthogonal shape: "h", "v", "h-v" or "v-h".
func dcPathName(p [][2]float64) string {
	segments := 0
	first := ""
	for i := 0; i+1 < len(p); i++ {
		dx := p[i+1][0] - p[i][0]
		dy := p[i+1][1] - p[i][1]
		if math.Abs(dx) < 0.5 && math.Abs(dy) < 0.5 {
			continue
		}
		dir := "h"
		if math.Abs(dx) < 0.5 {
			dir = "v"
		}
		segments++
		if segments == 1 {
			first = dir
		} else {
			return first + "-" + dir
		}
	}
	if segments == 1 {
		return first
	}
	return "path"
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
				// A pin exactly at the route's start/end is already part of the
				// island being joined; that is the connection point, not an
				// obstacle. Pins anywhere else on the path are still blockers.
				if pointEquals2(pin.X, pin.Y, points[0]) ||
					pointEquals2(pin.X, pin.Y, points[len(points)-1]) {
					continue
				}
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
	sheet, titleBlock *layoutBBox, maxMove float64) (*dcTextMove, int) {
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
		return nil, -1
	}
	var best *dcTextMove
	bestCost := math.Inf(1)
	bestIdx := -1
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
						best, bestCost, bestIdx = m, cost, pi
					}
				}
			}
		}
	}
	return best, bestIdx
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

// chooseMarkerMove is the second, more conservative layer. It only considers a
// blocker owned by a netflag/netport that sits on a single-segment stub whose
// far endpoint is a real pin. The marker is translated rigidly (rotation, and
// therefore power/ground body direction, never changes) and the stub is
// re-routed as a two-segment orthogonal L from the untouched host pin.
//
// Every candidate is checked against: sheet/title-block bounds, part and marker
// bodies, pins, visible text (its own Name moves with it), other wires (touch =
// short), and finally the requested direct-connect path itself. Nothing is
// written; a returned move is only a proposal.
func chooseMarkerMove(paths [][][2]float64, blockers [][]dcBlocker, pairNet, aWireID, bWireID, aID, bID string,
	items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox, maxMove float64) (*dcMarkerMove, int, []dcMarkerSkip) {
	byID := make(map[string]layoutComp, len(comps))
	for _, c := range comps {
		byID[c.ID] = c
	}
	// A marker's own stub often blocks the route even when its body does not.
	// Map single-segment stub wires back to their owning netflag/netport so a
	// wire blocker can still nominate the marker that can move it.
	stubOwner := map[string]string{}
	for _, c := range comps {
		if c.ComponentType != "netflag" && c.ComponentType != "netport" {
			continue
		}
		if stub, reason := stubOfMarker(c, wires); stub != nil && reason == "" {
			stubOwner[stub.WireID] = c.ID
		}
	}
	candidateIDs := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, bs := range blockers {
		for _, b := range bs {
			id := ""
			switch b.Kind {
			case "text":
				id = b.ParentID
			case "marker":
				id = b.ID
			case "wire":
				id = stubOwner[b.ID]
			}
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			candidateIDs = append(candidateIDs, id)
		}
	}

	var skips []dcMarkerSkip
	var best *dcMarkerMove
	bestCost := math.Inf(1)
	bestIdx := -1
	for _, id := range candidateIDs {
		c, ok := byID[id]
		if !ok {
			continue
		}
		if c.ComponentType != "netflag" && c.ComponentType != "netport" {
			// Only symbol-style markers own a stub that can be re-routed safely.
			// netlabel/short_symbol blockers are reported as a reason, while plain
			// part text is handled (or not) by the first layer.
			if isSchMarker(c.ComponentType) {
				skips = append(skips, dcMarkerSkip{MarkerID: c.ID, Net: c.Net,
					Reason: "marker-type-not-movable:" + c.ComponentType})
			}
			continue
		}
		// A marker already carrying the pair's net is not an obstacle worth
		// moving here: its island should be joined by the same direct-connect
		// reasoning, not pushed aside.
		if c.Net != "" && c.Net == pairNet {
			continue
		}
		own := markerOwnedTextItems(items, c.ID)
		if !hasVisibleMarkerName(own) {
			skips = append(skips, dcMarkerSkip{MarkerID: c.ID, Net: c.Net, Reason: "no-visible-name"})
			continue
		}
		stub, reason := stubOfMarker(c, wires)
		if stub == nil {
			skips = append(skips, dcMarkerSkip{MarkerID: c.ID, Net: c.Net, Reason: "not-a-single-segment-stub:" + reason})
			continue
		}
		if wireTreeSize(wires, stub.WireID) != 1 {
			skips = append(skips, dcMarkerSkip{MarkerID: c.ID, Net: c.Net, Reason: "shared-or-branched-wire-tree"})
			continue
		}
		if _, ok := pinAt(comps, stub.HostX, stub.HostY); !ok {
			skips = append(skips, dcMarkerSkip{MarkerID: c.ID, Net: c.Net, Reason: "stub-host-is-not-a-pin"})
			continue
		}

		found := false
		for _, d := range []float64{5, 10, 15, 20, 25, 30, 35, 40} {
			if d > maxMove {
				break
			}
			for _, dir := range [][2]float64{{-1, 0}, {1, 0}, {0, 1}, {0, -1}, {-1, -1}, {-1, 1}, {1, -1}, {1, 1}} {
				delta := [2]float64{dir[0] * d, dir[1] * d}
				newX, newY := c.X+delta[0], c.Y+delta[1]
				for elbow := 0; elbow < 2; elbow++ {
					segs := markerStubPath(stub.HostX, stub.HostY, newX, newY, elbow)
					if segs == nil || markerStubLength(segs) > destaggerStubMaxLen {
						continue
					}
					if !markerMoveClear(c, delta, stub, segs, items, comps, wires, sheet, titleBlock) {
						continue
					}
					proposal := &dcMarkerMove{
						MarkerID: c.ID, ComponentType: c.ComponentType, Net: c.Net,
						FromAnchor: &checkPoint{X: round2(c.X), Y: round2(c.Y)},
						ToAnchor:   &checkPoint{X: round2(newX), Y: round2(newY)},
						DeltaX:     delta[0], DeltaY: delta[1],
						StubWireID:  stub.WireID,
						Host:        &checkPoint{X: round2(stub.HostX), Y: round2(stub.HostY)},
						NewStubPath: segs,
					}
					if c.BBox != nil {
						b := translateLayoutBBox(*c.BBox, delta)
						proposal.NewMarkerBBox = &b
					}
					for _, it := range own {
						proposal.AttachedTextIDs = append(proposal.AttachedTextIDs, it.ID)
					}
					simItems, simComps, simWires := simulateMarkerMove(c, *proposal, items, comps, wires)
					clearedIdx := -1
					for pi, path := range paths {
						if markerMoveClearsPath(path, aWireID, bWireID, aID, bID, simItems, simComps, simWires) {
							clearedIdx = pi
							break
						}
					}
					if clearedIdx < 0 {
						continue
					}
					cost := math.Abs(delta[0]) + math.Abs(delta[1])
					if cost < bestCost {
						best, bestCost, bestIdx = proposal, cost, clearedIdx
					}
					found = true
				}
			}
		}
		if !found {
			skips = append(skips, dcMarkerSkip{MarkerID: c.ID, Net: c.Net, Reason: "no-clear-stub-move"})
		}
	}
	return best, bestIdx, skips
}

func markerOwnedTextItems(items []visibleTextItem, markerID string) []visibleTextItem {
	var out []visibleTextItem
	for _, it := range items {
		if it.ParentID == markerID && it.BBox != nil {
			out = append(out, it)
		}
	}
	return out
}

func hasVisibleMarkerName(items []visibleTextItem) bool {
	for _, it := range items {
		if it.Key == "Name" && strings.TrimSpace(it.Value) != "" &&
			(it.Visible == nil || *it.Visible) {
			return true
		}
	}
	return false
}

func wireTreeSize(wires []schGroupWire, id string) int {
	find, idx := wireTreeFinder(wires)
	i, ok := idx[id]
	if !ok {
		return 0
	}
	root := find(i)
	n := 0
	for j := range wires {
		if find(j) == root {
			n++
		}
	}
	return n
}

func pinAt(comps []layoutComp, x, y float64) (string, bool) {
	for _, c := range comps {
		if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
			continue
		}
		for _, p := range c.Pins {
			if math.Abs(p.X-x) <= 0.5 && math.Abs(p.Y-y) <= 0.5 {
				return c.ID + ":" + p.Number, true
			}
		}
	}
	return "", false
}

// markerStubPath builds an orthogonal route from the untouched host pin to the
// new marker anchor. A straight segment is used when the two points already
// share an axis; otherwise the caller may try both elbows.
func markerStubPath(fx, fy, ax, ay float64, elbow int) [][2]float64 {
	if math.Abs(fx-ax) < 0.5 && math.Abs(fy-ay) < 0.5 {
		return nil
	}
	if math.Abs(fx-ax) < 0.5 || math.Abs(fy-ay) < 0.5 {
		return [][2]float64{{fx, fy}, {ax, ay}}
	}
	if elbow == 0 {
		return [][2]float64{{fx, fy}, {ax, fy}, {ax, ay}}
	}
	return [][2]float64{{fx, fy}, {fx, ay}, {ax, ay}}
}

func markerStubLength(segs [][2]float64) float64 {
	total := 0.0
	for i := 0; i+1 < len(segs); i++ {
		total += math.Abs(segs[i+1][0]-segs[i][0]) + math.Abs(segs[i+1][1]-segs[i][1])
	}
	return total
}

func translateLayoutBBox(b layoutBBox, d [2]float64) layoutBBox {
	return layoutBBox{MinX: b.MinX + d[0], MinY: b.MinY + d[1], MaxX: b.MaxX + d[0], MaxY: b.MaxY + d[1]}
}

func inflateLayoutBBox(b layoutBBox, m float64) layoutBBox {
	return layoutBBox{MinX: b.MinX - m, MinY: b.MinY - m, MaxX: b.MaxX + m, MaxY: b.MaxY + m}
}

func boxInside2(b, area layoutBBox) bool {
	const eps = 0.5
	return b.MinX >= area.MinX-eps && b.MaxX <= area.MaxX+eps &&
		b.MinY >= area.MinY-eps && b.MaxY <= area.MaxY+eps
}

func positiveOverlap(a, b layoutBBox) bool {
	ix, iy, hit := overlapExtent(a, b)
	return hit && math.Min(ix, iy) > 1.0
}

func pointInBox2(x, y float64, b layoutBBox, eps float64) bool {
	return x >= b.MinX-eps && x <= b.MaxX+eps && y >= b.MinY-eps && y <= b.MaxY+eps
}

// trimSegmentAtPoints pulls a segment endpoint inward when it lands on a known
// connection point (host pin or new marker anchor). Without this, every stub
// would "touch" the host part's own bbox at its start and its own marker body at
// the anchor, rejecting all candidates.
func trimSegmentAtPoints(s [4]float64, cuts []checkPoint, trim float64) *[4]float64 {
	a := [2]float64{s[0], s[1]}
	b := [2]float64{s[2], s[3]}
	length := math.Hypot(b[0]-a[0], b[1]-a[1])
	if length <= 1.0 {
		return nil
	}
	u := [2]float64{(b[0] - a[0]) / length, (b[1] - a[1]) / length}
	for _, c := range cuts {
		if math.Abs(a[0]-c.X) <= 0.5 && math.Abs(a[1]-c.Y) <= 0.5 {
			a[0] += u[0] * trim
			a[1] += u[1] * trim
		}
		if math.Abs(b[0]-c.X) <= 0.5 && math.Abs(b[1]-c.Y) <= 0.5 {
			b[0] -= u[0] * trim
			b[1] -= u[1] * trim
		}
	}
	if math.Hypot(b[0]-a[0], b[1]-a[1]) <= 0.5 {
		return nil
	}
	out := [4]float64{a[0], a[1], b[0], b[1]}
	return &out
}

func markerMoveClear(c layoutComp, delta [2]float64, stub *destaggerStub, segs [][2]float64,
	items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox) bool {
	if c.BBox == nil || sheet == nil {
		return false
	}
	// A pure "no bbox overlap" move can land a name 0.1 unit from another label:
	// technically clear, unreadable in practice. Require a small visible gap.
	// Wire-to-wire contact stays at zero tolerance (touching = short).
	const clearance = 2.0
	usable := schUsableArea(*sheet)
	newBox := translateLayoutBBox(*c.BBox, delta)
	newBoxGrown := inflateLayoutBBox(newBox, clearance)
	if !boxInside2(newBox, usable) {
		return false
	}
	if titleBlock != nil && positiveOverlap(newBox, *titleBlock) {
		return false
	}
	for _, o := range comps {
		if o.ID == c.ID || o.BBox == nil || o.ComponentType == "sheet" {
			continue
		}
		if positiveOverlap(newBoxGrown, *o.BBox) {
			return false
		}
	}
	// Marker-check only compares marker bodies with parts/markers. A body can
	// still swallow a wire label; judge it against visible text as well.
	for _, it := range items {
		if it.ParentID == c.ID || it.BBox == nil {
			continue
		}
		if positiveOverlap(newBoxGrown, *it.BBox) {
			return false
		}
	}
	for _, w := range wires {
		if w.ID == stub.WireID {
			continue
		}
		for _, s := range labelWireSegments(w) {
			if schSegmentCrossesBox(s[0], s[1], s[2], s[3], newBoxGrown) {
				return false
			}
		}
	}

	movedOwn := map[string]visibleTextItem{}
	for _, it := range items {
		if it.ParentID != c.ID || it.BBox == nil {
			continue
		}
		m := it
		b := translateLayoutBBox(*it.BBox, delta)
		m.BBox = &b
		movedOwn[it.ID] = m
		if !boxInside2(b, usable) {
			return false
		}
		if titleBlock != nil && positiveOverlap(b, *titleBlock) {
			return false
		}
		bGrown := inflateLayoutBBox(b, clearance)
		for _, o := range comps {
			if o.ID == c.ID || o.BBox == nil || o.ComponentType == "sheet" {
				continue
			}
			if positiveOverlap(bGrown, *o.BBox) {
				return false
			}
		}
		for _, other := range items {
			if other.ID == it.ID || other.ParentID == c.ID || other.BBox == nil {
				continue
			}
			if positiveOverlap(bGrown, *other.BBox) {
				return false
			}
		}
		for _, o := range comps {
			for _, pin := range o.Pins {
				px, py := pin.X, pin.Y
				if o.ID == c.ID {
					// The marker's own pin travels with it; judge the moved pin,
					// not the stale original anchor.
					px, py = px+delta[0], py+delta[1]
				}
				if pointInBox2(px, py, b, clearance) {
					return false
				}
			}
		}
		for _, w := range wires {
			if w.ID == stub.WireID {
				continue
			}
			for _, s := range labelWireSegments(w) {
				if schSegmentCrossesBox(s[0], s[1], s[2], s[3], bGrown) {
					return false
				}
			}
		}
	}

	newX, newY := c.X+delta[0], c.Y+delta[1]
	cuts := []checkPoint{{X: stub.HostX, Y: stub.HostY}, {X: newX, Y: newY}}
	for i := 0; i+1 < len(segs); i++ {
		s := [4]float64{segs[i][0], segs[i][1], segs[i+1][0], segs[i+1][1]}
		trimmed := trimSegmentAtPoints(s, cuts, 1.0)
		if trimmed == nil {
			continue
		}
		for _, o := range comps {
			if o.BBox == nil || o.ComponentType == "sheet" {
				continue
			}
			box := *o.BBox
			if o.ID == c.ID {
				box = newBox
			}
			if schSegmentCrossesBox(trimmed[0], trimmed[1], trimmed[2], trimmed[3],
				inflateLayoutBBox(box, clearance)) {
				return false
			}
		}
		for _, it := range items {
			b := it.BBox
			if b == nil {
				continue
			}
			if m, ok := movedOwn[it.ID]; ok {
				b = m.BBox
			}
			if schSegmentCrossesBox(trimmed[0], trimmed[1], trimmed[2], trimmed[3],
				inflateLayoutBBox(*b, clearance)) {
				return false
			}
		}
		for _, o := range comps {
			for _, pin := range o.Pins {
				if math.Abs(pin.X-stub.HostX) < 0.5 && math.Abs(pin.Y-stub.HostY) < 0.5 {
					continue
				}
				if pointNearSegment2(pin.X, pin.Y, [2]float64{trimmed[0], trimmed[1]},
					[2]float64{trimmed[2], trimmed[3]}, clearance) {
					return false
				}
			}
		}
		for _, w := range wires {
			if w.ID == stub.WireID {
				continue
			}
			for _, ws := range labelWireSegments(w) {
				if segmentsCross2([2]float64{s[0], s[1]}, [2]float64{s[2], s[3]},
					[2]float64{ws[0], ws[1]}, [2]float64{ws[2], ws[3]}) {
					return false
				}
			}
		}
	}
	return true
}

func simulateMarkerMove(c layoutComp, mv dcMarkerMove, items []visibleTextItem,
	comps []layoutComp, wires []schGroupWire) ([]visibleTextItem, []layoutComp, []schGroupWire) {
	delta := [2]float64{mv.DeltaX, mv.DeltaY}
	simItems := make([]visibleTextItem, len(items))
	for i, it := range items {
		simItems[i] = it
		if it.ParentID == c.ID && it.BBox != nil {
			b := translateLayoutBBox(*it.BBox, delta)
			simItems[i].BBox = &b
		}
	}
	simComps := make([]layoutComp, len(comps))
	for i, o := range comps {
		simComps[i] = o
		if o.ID != c.ID {
			continue
		}
		if o.BBox != nil {
			b := translateLayoutBBox(*o.BBox, delta)
			simComps[i].BBox = &b
		}
		simComps[i].X += delta[0]
		simComps[i].Y += delta[1]
		pins := make([]layoutPin, len(o.Pins))
		copy(pins, o.Pins)
		for k := range pins {
			pins[k].X += delta[0]
			pins[k].Y += delta[1]
		}
		simComps[i].Pins = pins
	}
	simWires := make([]schGroupWire, len(wires))
	copy(simWires, wires)
	var pts []float64
	var segs [][4]float64
	for i, p := range mv.NewStubPath {
		pts = append(pts, p[0], p[1])
		if i > 0 {
			q := mv.NewStubPath[i-1]
			segs = append(segs, [4]float64{q[0], q[1], p[0], p[1]})
		}
	}
	for i := range simWires {
		if simWires[i].ID == mv.StubWireID {
			simWires[i] = schGroupWire{ID: mv.StubWireID, Points: pts, ObservedSegments: segs}
		}
	}
	return simItems, simComps, simWires
}

func markerMoveClearsPath(path [][2]float64, aWireID, bWireID, aID, bID string, items []visibleTextItem,
	comps []layoutComp, wires []schGroupWire) bool {
	find, wireIndex := wireTreeFinder(wires)
	ia, okA := wireIndex[aWireID]
	ib, okB := wireIndex[bWireID]
	if !okA || !okB {
		return false
	}
	return len(directPathBlockers(path, find(ia), find(ib), aID, bID, items, comps, wires, find, wireIndex)) == 0
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

func pointEquals2(px, py float64, p [2]float64) bool {
	return math.Abs(px-p[0]) <= 0.5 && math.Abs(py-p[1]) <= 0.5
}

func pointNearSegment2(px, py float64, a, b [2]float64, margin float64) bool {
	_, _, d := closestPointOnSegment(px, py, a[0], a[1], b[0], b[1])
	return d <= margin
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
	fmt.Fprintf(w, "direct-connect-plan: %d pair(s), %d proposal(s) (document %s)\n",
		len(rep.Pairs), rep.ProposalCount, rep.DocumentID)
	for _, p := range rep.Pairs {
		switch p.Status {
		case "already-clear":
			fmt.Fprintf(w, "  [clear] %s %s ↔ %s distance=%.0f path=%s\n", p.Net, p.LabelA, p.LabelB, p.Distance, p.Path)
		case "move-proposed":
			fmt.Fprintf(w, "  [move] %s %s ↔ %s distance=%.0f path=%s\n", p.Net, p.LabelA, p.LabelB, p.Distance, p.Path)
			fmt.Fprintf(w, "         move %s %s by (%+.0f,%+.0f)\n", p.Move.Key, p.Move.AttributeID, p.Move.DeltaX, p.Move.DeltaY)
		case "marker-move-proposed":
			fmt.Fprintf(w, "  [marker] %s %s ↔ %s distance=%.0f path=%s\n", p.Net, p.LabelA, p.LabelB, p.Distance, p.Path)
			fmt.Fprintf(w, "         move %s %s by (%+.0f,%+.0f); re-route stub %s from host (%.0f,%.0f)\n",
				p.MarkerMove.ComponentType, p.MarkerMove.MarkerID, p.MarkerMove.DeltaX, p.MarkerMove.DeltaY,
				p.MarkerMove.StubWireID, p.MarkerMove.Host.X, p.MarkerMove.Host.Y)
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
			for _, s := range p.MarkerSkips {
				fmt.Fprintf(w, "         marker %s (%s): %s\n", s.MarkerID, s.Net, s.Reason)
			}
		}
	}
}
