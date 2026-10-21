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
	"github.com/zhoushoujianwork/easyeda-agent/internal/schguard"
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

// dcPartMove is the third, most invasive read-only proposal: translate one part
// together with every wire tree and marker that is rigidly attached to it.
// Shared trees (wired to a non-member pin), pin-to-pin contacts and incomplete
// expansions are refused, so the move cannot tear a far connection.
type dcPartMove struct {
	PartID     string      `json:"partId"`
	Designator string      `json:"designator"`
	FromAnchor *checkPoint `json:"fromAnchor"`
	ToAnchor   *checkPoint `json:"toAnchor"`
	DeltaX     float64     `json:"deltaX"`
	DeltaY     float64     `json:"deltaY"`
	WireIDs    []string    `json:"wireIds,omitempty"`
	FlagIDs    []string    `json:"flagIds,omitempty"`
	TextIDs    []string    `json:"textIds,omitempty"`
	NewBBox    *layoutBBox `json:"newBBox,omitempty"`
}

type dcPartSkip struct {
	PartID     string `json:"partId"`
	Designator string `json:"designator"`
	Reason     string `json:"reason"`
}

// dcGroupMove moves a small functional block (2-4 parts connected by shared
// wire trees) as one rigid body. This is the safe answer when a single-part
// move would tear an inter-part net.
type dcGroupMove struct {
	PartIDs     []string    `json:"partIds"`
	Designators []string    `json:"designators"`
	DeltaX      float64     `json:"deltaX"`
	DeltaY      float64     `json:"deltaY"`
	WireIDs     []string    `json:"wireIds,omitempty"`
	FlagIDs     []string    `json:"flagIds,omitempty"`
	TextIDs     []string    `json:"textIds,omitempty"`
	UnionBBox   *layoutBBox `json:"unionBBox,omitempty"`
}

type dcGroupSkip struct {
	PartIDs     []string `json:"partIds"`
	Designators []string `json:"designators"`
	Reason      string   `json:"reason"`
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
	PartMove    *dcPartMove    `json:"partMove,omitempty"`
	PartSkips   []dcPartSkip   `json:"partSkips,omitempty"`
	GroupMove   *dcGroupMove   `json:"groupMove,omitempty"`
	GroupSkips  []dcGroupSkip  `json:"groupSkips,omitempty"`
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
	var out, playbookOut, pair string
	var maxDistance, maxMove float64
	cmd := &cobra.Command{
		Use:   "direct-connect-plan",
		Short: "Propose small text, marker or rigid part-group moves that enable direct label connections (read-only)",
		Long: `Read the active page's labels, wires, parts, visible text and native attribute anchors.
For same-net labels on different wire islands, find blocked orthogonal L paths and
try three read-only resolutions on a small 5-unit grid:
  1. move a blocking part Designator/Value attribute;
  2. translate a blocking netflag/netport together with its single-segment stub,
     re-routing only that stub on an orthogonal L while the far endpoint stays put.
  3. translate a blocking part with the wire trees, markers and labels that are
     rigidly attached to its pins. Shared trees, incomplete expansions and
     pin-to-pin contacts are refused.
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
				if p.Move != nil || p.MarkerMove != nil || p.PartMove != nil || p.GroupMove != nil {
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
			if playbookOut != "" {
				project := ""
				if geom.Context != nil {
					project = geom.Context.ProjectUUID
				}
				pb, perr := directConnectPlaybook(report.Pairs, pair, project, doc)
				if perr != nil {
					return perr
				}
				b, merr := json.MarshalIndent(pb, "", "  ")
				if merr != nil {
					return merr
				}
				if werr := os.WriteFile(playbookOut, append(b, '\n'), 0644); werr != nil {
					return fmt.Errorf("write direct-connect playbook: %w", werr)
				}
				fmt.Fprintf(stderr, "direct-connect playbook written to %s (apply with easyeda sch apply %s --yes)\n", playbookOut, playbookOut)
				return nil
			}
			renderDirectConnectPlan(report, stdout)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit structured JSON")
	cmd.Flags().StringVar(&out, "out", "", "write structured JSON to file")
	cmd.Flags().StringVar(&playbookOut, "playbook", "", "write a guarded playbook for selected already-clear label pairs")
	cmd.Flags().StringVar(&pair, "pair", "", "with --playbook, select LABEL_A,LABEL_B instead of all already-clear pairs")
	cmd.Flags().Float64Var(&maxDistance, "max-distance", 250, "maximum Manhattan distance between label anchors")
	cmd.Flags().Float64Var(&maxMove, "max-move", 40, "maximum text move per axis, in 5-unit steps")
	return cmd
}

func directConnectPlaybook(pairs []dcPairPlan, pairArg, project, doc string) (*playbook, error) {
	wanted := map[string]bool{}
	for _, id := range strings.Split(pairArg, ",") {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = true
		}
	}
	var clear []dcPairPlan
	for _, p := range pairs {
		if p.Status != "already-clear" || len(p.PathPoints) < 2 {
			continue
		}
		if len(wanted) > 0 && !(wanted[p.LabelA] && wanted[p.LabelB]) {
			continue
		}
		clear = append(clear, p)
	}
	if len(clear) == 0 {
		return nil, fmt.Errorf("direct-connect playbook: no selected already-clear pair; run the JSON plan without --playbook and inspect move-required pairs first")
	}
	timeout, retry := 120, 0
	cont := false
	confirm := true
	pb := &playbook{
		Version: 1, RequireFullExecution: true,
		Meta:     playbookMeta{Name: "verified direct-connect", Project: project, Doc: doc},
		Defaults: stepPolicy{TimeoutSec: &timeout, Retry: &retry, ContinueOnError: &cont},
	}
	for i, p := range clear {
		pb.Steps = append(pb.Steps, playbookStep{
			ID:   fmt.Sprintf("direct-connect-%03d", i+1),
			Name: fmt.Sprintf("%s: direct-connect %s and %s", p.Net, p.LabelA, p.LabelB),
			Run:  "sch direct-connect-apply",
			Flags: map[string]any{
				"doc": doc, "pair": p.LabelA + "," + p.LabelB,
				"apply": true, "save": true,
			},
			Confirm: &confirm,
		})
	}
	if errs := preflight(pb, nil); len(errs) > 0 {
		return nil, fmt.Errorf("direct-connect playbook failed preflight: %v", errs)
	}
	return pb, nil
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
	return planDirectConnectTextMovesWithMaze(items, comps, wires, anchors,
		sheet, titleBlock, maxDistance, maxMove, true)
}

// planDirectConnectTextMovesWithoutMaze isolates the text/marker/part/group
// proposal layers in tests. Production always enables the obstacle-aware maze,
// but a clear multi-bend route correctly wins over any invasive move.
func planDirectConnectTextMovesWithoutMaze(items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	anchors map[string]checkPoint, sheet, titleBlock *layoutBBox, maxDistance, maxMove float64) []dcPairPlan {
	return planDirectConnectTextMovesWithMaze(items, comps, wires, anchors,
		sheet, titleBlock, maxDistance, maxMove, false)
}

func planDirectConnectTextMovesWithMaze(items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	anchors map[string]checkPoint, sheet, titleBlock *layoutBBox, maxDistance, maxMove float64,
	allowMaze bool) []dcPairPlan {
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
	chipIslands := wireIslandActiveChips(comps, wires)
	partInfo := buildPartComponents(comps, wires)
	wireOwners := wireOwnerMap(comps, wires)
	var out []dcPairPlan
	for i := 0; i < len(labels); i++ {
		for j := i + 1; j < len(labels); j++ {
			a, b := labels[i], labels[j]
			if a.root == b.root || a.it.Value != b.it.Value {
				continue
			}
			if labelsJoinDifferentChips(chipIslands, a.root, b.root) {
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
			paths = filterDCPaths(paths, sheet, titleBlock)
			if len(paths) == 0 {
				pair.Status = "unresolved"
				pair.Note = "both L paths leave the usable sheet or cross the title-block keep-out"
				out = append(out, pair)
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
			if clearIdx < 0 && allowMaze {
				if maze, ok := dcMazePath([2]float64{a.ax, a.ay}, [2]float64{b.ax, b.ay},
					a.root, b.root, a.it.ID, b.it.ID, items, comps, wires, find, wireIndex,
					sheet, titleBlock, maxDistance*2); ok {
					paths = append(paths, maze)
					blockers = append(blockers, directPathBlockers(maze, a.root, b.root,
						a.it.ID, b.it.ID, items, comps, wires, find, wireIndex))
					clearIdx = len(paths) - 1
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
					partMove, partIdx, partSkips := choosePartMove(paths, blockers, a.it.ParentID, b.it.ParentID,
						a.it.ID, b.it.ID, items, comps, wires, sheet, titleBlock, maxMove)
					pair.PartSkips = partSkips
					if partMove != nil {
						pair.Status = "part-move-proposed"
						pair.Path = dcPathName(paths[partIdx])
						pair.PathPoints = paths[partIdx]
						pair.PartMove = partMove
					} else {
						groupMove, groupPath, groupPathName, groupSkips := chooseGroupMove(
							blockers, a.it.ParentID, b.it.ParentID, a.it.ID, b.it.ID,
							items, comps, wires, sheet, titleBlock, maxMove, partInfo, wireOwners)
						pair.GroupSkips = groupSkips
						if groupMove != nil {
							pair.Status = "group-move-proposed"
							pair.Path = groupPathName
							pair.PathPoints = groupPath
							pair.GroupMove = groupMove
						} else {
							pair.Status = "unresolved"
							pair.Note = "no single part-attribute move, netflag/netport stub move, single-part move, or 2-4 part block move on a 5-unit grid clears an L path"
						}
					}
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

func dcMazePath(start, end [2]float64, ra, rb int, aID, bID string,
	items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	find func(int) int, wireIndex map[string]int, sheet, titleBlock *layoutBBox,
	maxLength float64) ([][2]float64, bool) {
	clear := func(p, q [2]float64) bool {
		if titleBlock != nil && schSegmentCrossesBox(p[0], p[1], q[0], q[1], *titleBlock) {
			return false
		}
		if sheet != nil && (!pointInBox2(p[0], p[1], *sheet, alignmentTolerance) ||
			!pointInBox2(q[0], q[1], *sheet, alignmentTolerance)) {
			return false
		}
		return len(directPathBlockers([][2]float64{p, q}, ra, rb, aID, bID,
			items, comps, wires, find, wireIndex)) == 0
	}
	return dcFindOrthogonalPath(start, end, sheet, titleBlock, clear, maxLength)
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
		if len(out) >= 2 {
			a, b := out[len(out)-2], out[len(out)-1]
			if (math.Abs(a[0]-b[0]) < 0.001 && math.Abs(b[0]-q[0]) < 0.001) ||
				(math.Abs(a[1]-b[1]) < 0.001 && math.Abs(b[1]-q[1]) < 0.001) {
				out[len(out)-1] = q
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

// filterDCPaths drops routes that leave the usable sheet or cross the title
// block. A direct wire must obey the same boundary rules as any other wire.
func filterDCPaths(paths [][][2]float64, sheet, titleBlock *layoutBBox) [][][2]float64 {
	if sheet == nil {
		return paths
	}
	usable := schUsableArea(*sheet)
	var out [][][2]float64
	for _, p := range paths {
		ok := true
		for i := 0; i+1 < len(p); i++ {
			if !pointInBox2(p[i][0], p[i][1], usable, 0.5) ||
				!pointInBox2(p[i+1][0], p[i+1][1], usable, 0.5) {
				ok = false
				break
			}
			if titleBlock != nil && schSegmentCrossesBox(p[i][0], p[i][1], p[i+1][0], p[i+1][1], *titleBlock) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, p)
		}
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
				if c.BBox != nil && pin.RotationKnown {
					leadStart, leadEnd, ok := schguard.PinLeadInterval(
						schguard.Point{X: pin.X, Y: pin.Y}, pin.Rotation,
						schguard.BBox{MinX: c.BBox.MinX, MinY: c.BBox.MinY, MaxX: c.BBox.MaxX, MaxY: c.BBox.MaxY},
					)
					if ok && schguard.SegmentTouchesPinLead(
						schguard.Point{X: p[0], Y: p[1]}, schguard.Point{X: q[0], Y: q[1]},
						leadStart, leadEnd, schguard.Point{X: pin.X, Y: pin.Y},
					) {
						add(dcBlocker{Kind: "pin-lead", ID: c.ID + ":" + pin.Number,
							Label: "pin-lead:" + c.Designator + "." + pin.Number})
					}
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
		ownText := map[string]bool{}
		for _, it := range own {
			ownText[it.ID] = true
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
		for _, delta := range dcDeltaCandidates(maxMove) {
			newX, newY := c.X+delta[0], c.Y+delta[1]
			for elbow := 0; elbow < 2; elbow++ {
				segs := markerStubPath(stub.HostX, stub.HostY, newX, newY, elbow)
				if segs == nil || markerStubLength(segs) > destaggerStubMaxLen {
					continue
				}
				// The first stub segment must leave the host pin along the
				// pin's outward direction (the original stub's direction).
				// A perpendicular first step is what `connect_pin` rejects;
				// a folded route must go out first, then bend.
				if !firstSegmentMatchesDir(segs, stub.Dir) {
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
					if simulatedPathClear(path, aWireID, bWireID, aID, bID, simItems, simComps, simWires) {
						if !pathClearanceOK(path, map[string]bool{c.ID: true}, ownText, delta, comps, items) {
							continue
						}
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
		if !found {
			skips = append(skips, dcMarkerSkip{MarkerID: c.ID, Net: c.Net, Reason: "no-clear-stub-move"})
		}
	}
	return best, bestIdx, skips
}

// choosePartMove is the third layer. It reuses expandGroupAttachments — the
// same rigid-body expansion that sch group-move uses — so a part is moved only
// with wire trees that genuinely terminate at its pins. A shared tree, a
// suspected half-move, or a pin-to-pin contact refuses the candidate outright.
func choosePartMove(paths [][][2]float64, blockers [][]dcBlocker, aWireID, bWireID, aID, bID string,
	items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox, maxMove float64) (*dcPartMove, int, []dcPartSkip) {
	byID := make(map[string]layoutComp, len(comps))
	for _, c := range comps {
		byID[c.ID] = c
	}
	// A wire can block the route while its owner's body sits off the path.
	// Map each wire to every part pin it touches so the part move layer still
	// gets a chance to carry that wire out of the way.
	wireOwners := wireOwnerMap(comps, wires)
	var candidateIDs []string
	seen := map[string]bool{}
	for _, bs := range blockers {
		for _, b := range bs {
			if b.ID == "" {
				continue
			}
			var ids []string
			switch b.Kind {
			case "part":
				ids = []string{b.ID}
			case "wire":
				ids = wireOwners[b.ID]
			default:
				continue
			}
			for _, id := range ids {
				if seen[id] {
					continue
				}
				seen[id] = true
				candidateIDs = append(candidateIDs, id)
			}
		}
	}
	if len(candidateIDs) == 0 {
		return nil, -1, nil
	}
	var flags []schGroupFlag
	for _, c := range comps {
		if isSchMarker(c.ComponentType) && c.AnchorAvailable {
			flags = append(flags, schGroupFlag{ID: c.ID, X: c.X, Y: c.Y})
		}
	}

	var skips []dcPartSkip
	var best *dcPartMove
	bestCost := math.Inf(1)
	bestIdx := -1
	for _, id := range candidateIDs {
		c, ok := byID[id]
		if !ok || (c.ComponentType != "" && c.ComponentType != schLayoutPartType) {
			continue
		}
		label := c.Designator
		if label == "" {
			label = c.ID
		}
		if c.BBox == nil || len(c.Pins) == 0 {
			skips = append(skips, dcPartSkip{PartID: c.ID, Designator: label, Reason: "no-bbox-or-pins"})
			continue
		}
		if pinTouchesOtherPart(c, comps) {
			skips = append(skips, dcPartSkip{PartID: c.ID, Designator: label,
				Reason: "pin-to-pin-contact"})
			continue
		}
		var memberPins [][2]float64
		for _, p := range c.Pins {
			memberPins = append(memberPins, [2]float64{p.X, p.Y})
		}
		var otherPins [][2]float64
		for _, o := range comps {
			if o.ID == c.ID || (o.ComponentType != "" && o.ComponentType != schLayoutPartType) {
				continue
			}
			for _, p := range o.Pins {
				otherPins = append(otherPins, [2]float64{p.X, p.Y})
			}
		}
		exp := expandGroupAttachments(groupExpandInput{
			MemberPins: memberPins, OtherPins: otherPins, Wires: wires, Flags: flags,
		})
		if len(exp.Suspects) > 0 {
			skips = append(skips, dcPartSkip{PartID: c.ID, Designator: label,
				Reason: "incomplete-attachment-expansion"})
			continue
		}
		if exp.SharedTrees > 0 {
			skips = append(skips, dcPartSkip{PartID: c.ID, Designator: label,
				Reason: "shared-wire-tree"})
			continue
		}
		if len(exp.WireIDs) > 16 || partGroupExtent(c, exp.WireIDs, wires) > 400 {
			skips = append(skips, dcPartSkip{PartID: c.ID, Designator: label,
				Reason: "attachment-group-too-large"})
			continue
		}
		movedComp := map[string]bool{c.ID: true}
		for _, fid := range exp.FlagIDs {
			movedComp[fid] = true
		}
		movedWire := map[string]bool{}
		for _, wid := range exp.WireIDs {
			movedWire[wid] = true
		}
		badFlag := ""
		for _, fid := range exp.FlagIDs {
			f, ok := byID[fid]
			if !ok {
				continue
			}
			for _, w := range wires {
				if movedWire[w.ID] {
					continue
				}
				for _, s := range labelWireSegments(w) {
					if pointOnSegment2(f.X, f.Y, [2]float64{s[0], s[1]}, [2]float64{s[2], s[3]}) {
						badFlag = fid
						break
					}
				}
				if badFlag != "" {
					break
				}
			}
			if badFlag != "" {
				break
			}
		}
		if badFlag != "" {
			skips = append(skips, dcPartSkip{PartID: c.ID, Designator: label,
				Reason: "flag-stub-not-in-group:" + badFlag})
			continue
		}
		var movedTextIDs []string
		for _, it := range items {
			if it.ParentID != "" && (it.ParentID == c.ID || movedComp[it.ParentID] || movedWire[it.ParentID]) {
				movedTextIDs = append(movedTextIDs, it.ID)
			}
		}
		movedText := map[string]bool{}
		for _, id := range movedTextIDs {
			movedText[id] = true
		}

		found := false
		for _, delta := range dcDeltaCandidates(maxMove) {
			if !partMoveClear(c, delta, movedComp, movedWire, movedTextIDs, items, comps, wires, sheet, titleBlock) {
				continue
			}
			mv := &dcPartMove{
				PartID: c.ID, Designator: label,
				FromAnchor: &checkPoint{X: round2(c.X), Y: round2(c.Y)},
				ToAnchor:   &checkPoint{X: round2(c.X + delta[0]), Y: round2(c.Y + delta[1])},
				DeltaX:     delta[0], DeltaY: delta[1],
			}
			for _, wid := range exp.WireIDs {
				mv.WireIDs = append(mv.WireIDs, wid)
			}
			for _, fid := range exp.FlagIDs {
				mv.FlagIDs = append(mv.FlagIDs, fid)
			}
			mv.TextIDs = append(mv.TextIDs, movedTextIDs...)
			if c.BBox != nil {
				b := translateLayoutBBox(*c.BBox, delta)
				mv.NewBBox = &b
			}
			simItems, simComps, simWires := simulatePartMove(*mv, items, comps, wires)
			clearedIdx := -1
			for pi, path := range paths {
				if simulatedPathClear(path, aWireID, bWireID, aID, bID, simItems, simComps, simWires) {
					if !pathClearanceOK(path, movedComp, movedText, delta, comps, items) {
						continue
					}
					clearedIdx = pi
					break
				}
			}
			if clearedIdx < 0 {
				continue
			}
			cost := math.Abs(delta[0]) + math.Abs(delta[1])
			if cost < bestCost {
				best, bestCost, bestIdx = mv, cost, clearedIdx
			}
			found = true
		}
		if !found {
			skips = append(skips, dcPartSkip{PartID: c.ID, Designator: label,
				Reason: "no-clear-part-move"})
		}
	}
	return best, bestIdx, skips
}

// chooseGroupMove is the shared-tree answer: move a 2-4 part component as one
// rigid body. Larger components (the 10-part LDO channels) are reported, not
// silently dragged across the sheet.
func chooseGroupMove(blockers [][]dcBlocker, aWireID, bWireID, aID, bID string,
	items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox, maxMove float64,
	info partComponentInfo, wireOwners map[string][]string) (*dcGroupMove, [][2]float64, string, []dcGroupSkip) {
	byID := make(map[string]layoutComp, len(comps))
	for _, c := range comps {
		byID[c.ID] = c
	}
	roots := map[int]bool{}
	for _, bs := range blockers {
		for _, b := range bs {
			var ids []string
			switch b.Kind {
			case "part":
				ids = []string{b.ID}
			case "wire":
				ids = wireOwners[b.ID]
			}
			for _, id := range ids {
				if r, ok := info.RootOf[id]; ok {
					roots[r] = true
				}
			}
		}
	}
	var flags []schGroupFlag
	for _, c := range comps {
		if isSchMarker(c.ComponentType) && c.AnchorAvailable {
			flags = append(flags, schGroupFlag{ID: c.ID, X: c.X, Y: c.Y})
		}
	}

	var skips []dcGroupSkip
	var best *dcGroupMove
	bestCost := math.Inf(1)
	var bestPath [][2]float64
	bestPathName := ""
	for root := range roots {
		partIDs := info.PartsOf[root]
		if len(partIDs) < 2 {
			continue
		}
		designators := make([]string, 0, len(partIDs))
		var memberPins [][2]float64
		valid := true
		for _, id := range partIDs {
			c, ok := byID[id]
			if !ok || c.BBox == nil || len(c.Pins) == 0 {
				valid = false
				break
			}
			d := c.Designator
			if d == "" {
				d = c.ID
			}
			designators = append(designators, d)
			for _, p := range c.Pins {
				memberPins = append(memberPins, [2]float64{p.X, p.Y})
			}
		}
		if !valid {
			skips = append(skips, dcGroupSkip{PartIDs: partIDs, Designators: designators,
				Reason: "no-bbox-or-pins"})
			continue
		}
		if len(partIDs) > 4 {
			skips = append(skips, dcGroupSkip{PartIDs: partIDs, Designators: designators,
				Reason: fmt.Sprintf("functional-block-too-large:%d-parts", len(partIDs))})
			continue
		}
		var otherPins [][2]float64
		for _, o := range comps {
			if info.RootOf[o.ID] == root || (o.ComponentType != "" && o.ComponentType != schLayoutPartType) {
				continue
			}
			for _, p := range o.Pins {
				otherPins = append(otherPins, [2]float64{p.X, p.Y})
			}
		}
		exp := expandGroupAttachments(groupExpandInput{
			MemberPins: memberPins, OtherPins: otherPins, Wires: wires, Flags: flags,
		})
		if len(exp.Suspects) > 0 {
			skips = append(skips, dcGroupSkip{PartIDs: partIDs, Designators: designators,
				Reason: "incomplete-attachment-expansion"})
			continue
		}
		if exp.SharedTrees > 0 {
			skips = append(skips, dcGroupSkip{PartIDs: partIDs, Designators: designators,
				Reason: "shared-wire-tree"})
			continue
		}
		if groupExtent(partIDs, exp.WireIDs, byID, wires) > 500 {
			skips = append(skips, dcGroupSkip{PartIDs: partIDs, Designators: designators,
				Reason: "functional-block-too-large-extent"})
			continue
		}
		movedComp := map[string]bool{}
		for _, id := range partIDs {
			movedComp[id] = true
		}
		for _, id := range exp.FlagIDs {
			movedComp[id] = true
		}
		movedWire := map[string]bool{}
		for _, id := range exp.WireIDs {
			movedWire[id] = true
		}
		var movedTextIDs []string
		for _, it := range items {
			if it.ParentID != "" && (movedComp[it.ParentID] || movedWire[it.ParentID]) {
				movedTextIDs = append(movedTextIDs, it.ID)
			}
		}
		found := false
		for _, delta := range dcDeltaCandidates(maxMove) {
			if !groupMoveClear(delta, movedComp, movedWire, movedTextIDs,
				items, comps, wires, sheet, titleBlock) {
				continue
			}
			simItems, simComps, simWires := simulateGroupMove(partIDs, exp.FlagIDs, exp.WireIDs,
				delta, items, comps, wires)
			okPath, path, name := simulatedPathsClear(simItems, simComps, simWires,
				aID, bID, sheet, titleBlock)
			if !okPath {
				continue
			}
			cost := math.Abs(delta[0]) + math.Abs(delta[1]) + 2*float64(len(partIDs))
			if cost < bestCost {
				mv := &dcGroupMove{
					PartIDs: append([]string(nil), partIDs...), Designators: append([]string(nil), designators...),
					DeltaX: delta[0], DeltaY: delta[1],
					WireIDs: append([]string(nil), exp.WireIDs...),
					FlagIDs: append([]string(nil), exp.FlagIDs...),
					TextIDs: append([]string(nil), movedTextIDs...),
				}
				if b := groupUnionBBox(partIDs, byID, delta); b != nil {
					mv.UnionBBox = b
				}
				best, bestCost, bestPath, bestPathName = mv, cost, path, name
			}
			found = true
		}
		if !found {
			skips = append(skips, dcGroupSkip{PartIDs: partIDs, Designators: designators,
				Reason: "no-clear-group-move"})
		}
	}
	return best, bestPath, bestPathName, skips
}

func groupExtent(partIDs []string, wireIDs []string, byID map[string]layoutComp,
	wires []schGroupWire) float64 {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, id := range partIDs {
		c, ok := byID[id]
		if !ok || c.BBox == nil {
			continue
		}
		minX = math.Min(minX, c.BBox.MinX)
		minY = math.Min(minY, c.BBox.MinY)
		maxX = math.Max(maxX, c.BBox.MaxX)
		maxY = math.Max(maxY, c.BBox.MaxY)
	}
	want := map[string]bool{}
	for _, id := range wireIDs {
		want[id] = true
	}
	for _, w := range wires {
		if !want[w.ID] {
			continue
		}
		for i := 0; i+1 < len(w.Points); i += 2 {
			minX = math.Min(minX, w.Points[i])
			maxX = math.Max(maxX, w.Points[i])
			minY = math.Min(minY, w.Points[i+1])
			maxY = math.Max(maxY, w.Points[i+1])
		}
	}
	if math.IsInf(minX, 1) || math.IsInf(maxX, -1) {
		return math.Inf(1)
	}
	return math.Max(maxX-minX, maxY-minY)
}

func groupUnionBBox(partIDs []string, byID map[string]layoutComp, delta [2]float64) *layoutBBox {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, id := range partIDs {
		c, ok := byID[id]
		if !ok || c.BBox == nil {
			continue
		}
		minX = math.Min(minX, c.BBox.MinX+delta[0])
		minY = math.Min(minY, c.BBox.MinY+delta[1])
		maxX = math.Max(maxX, c.BBox.MaxX+delta[0])
		maxY = math.Max(maxY, c.BBox.MaxY+delta[1])
	}
	if math.IsInf(minX, 1) {
		return nil
	}
	return &layoutBBox{MinX: minX, MinY: minY, MaxX: maxX, MaxY: maxY}
}

func pinTouchesOtherPart(c layoutComp, comps []layoutComp) bool {
	for _, o := range comps {
		if o.ID == c.ID || (o.ComponentType != "" && o.ComponentType != schLayoutPartType) {
			continue
		}
		for _, p := range c.Pins {
			for _, q := range o.Pins {
				if pointsClose(p.X, p.Y, q.X, q.Y, 0.5) {
					return true
				}
			}
		}
	}
	return false
}

func wireTouchesPoint(w schGroupWire, x, y float64) bool {
	for _, s := range labelWireSegments(w) {
		if pointOnSegment(x, y, s[0], s[1], s[2], s[3], schGroupEps) {
			return true
		}
	}
	return false
}

func wireOwnerMap(comps []layoutComp, wires []schGroupWire) map[string][]string {
	out := map[string][]string{}
	for _, c := range comps {
		if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
			continue
		}
		for _, p := range c.Pins {
			for _, w := range wires {
				if !wireTouchesPoint(w, p.X, p.Y) {
					continue
				}
				found := false
				for _, id := range out[w.ID] {
					if id == c.ID {
						found = true
						break
					}
				}
				if !found {
					out[w.ID] = append(out[w.ID], c.ID)
				}
			}
		}
	}
	return out
}

// partComponentInfo groups parts connected through wire trees. A 2-4 part
// group is a candidate for a rigid functional-block move; a 10-part LDO
// channel is too large to nudge for one label.
type partComponentInfo struct {
	RootOf  map[string]int
	PartsOf map[int][]string
}

func buildPartComponents(comps []layoutComp, wires []schGroupWire) partComponentInfo {
	var ids []string
	var parts []layoutComp
	for _, c := range comps {
		if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
			continue
		}
		ids = append(ids, c.ID)
		parts = append(parts, c)
	}
	parent := make([]int, len(ids))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	wireParts := make([][]int, len(wires))
	for wi, w := range wires {
		for pi, c := range parts {
			for _, p := range c.Pins {
				if wireTouchesPoint(w, p.X, p.Y) {
					wireParts[wi] = append(wireParts[wi], pi)
					break
				}
			}
		}
	}
	wfind, _ := wireTreeFinder(wires)
	trees := map[int][]int{}
	for wi := range wires {
		r := wfind(wi)
		trees[r] = append(trees[r], wi)
	}
	for _, wis := range trees {
		first := -1
		for _, wi := range wis {
			for _, pi := range wireParts[wi] {
				if first < 0 {
					first = pi
				} else {
					union(first, pi)
				}
			}
		}
	}
	info := partComponentInfo{RootOf: map[string]int{}, PartsOf: map[int][]string{}}
	for pi, id := range ids {
		r := find(pi)
		info.RootOf[id] = r
		info.PartsOf[r] = append(info.PartsOf[r], id)
	}
	for r := range info.PartsOf {
		sort.Strings(info.PartsOf[r])
	}
	return info
}

// partGroupExtent bounds how much circuitry one small nudge may drag along.
// A part wired into a page-wide bus is technically a rigid group, but moving
// that whole bus for a 5-unit readability tweak is not a local adjustment.
func partGroupExtent(c layoutComp, wireIDs []string, wires []schGroupWire) float64 {
	if c.BBox == nil {
		return math.Inf(1)
	}
	minX, minY := c.BBox.MinX, c.BBox.MinY
	maxX, maxY := c.BBox.MaxX, c.BBox.MaxY
	want := map[string]bool{}
	for _, id := range wireIDs {
		want[id] = true
	}
	for _, w := range wires {
		if !want[w.ID] {
			continue
		}
		for i := 0; i+1 < len(w.Points); i += 2 {
			minX = math.Min(minX, w.Points[i])
			maxX = math.Max(maxX, w.Points[i])
			minY = math.Min(minY, w.Points[i+1])
			maxY = math.Max(maxY, w.Points[i+1])
		}
	}
	return math.Max(maxX-minX, maxY-minY)
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

// dcDeltaCandidates enumerates the full 5-unit grid inside ±maxMove on each
// axis. The old eight equal-magnitude directions could never express a small
// asymmetric nudge such as (+40,-15), which is exactly what a marker or part
// often needs to slip past a neighbouring body/text band.
func dcDeltaCandidates(maxMove float64) [][2]float64 {
	const step = 5.0
	n := int(math.Floor(maxMove / step))
	out := make([][2]float64, 0, (2*n+1)*(2*n+1)-1)
	for i := -n; i <= n; i++ {
		for j := -n; j <= n; j++ {
			if i == 0 && j == 0 {
				continue
			}
			out = append(out, [2]float64{float64(i) * step, float64(j) * step})
		}
	}
	sort.Slice(out, func(a, b int) bool {
		ca := math.Abs(out[a][0]) + math.Abs(out[a][1])
		cb := math.Abs(out[b][0]) + math.Abs(out[b][1])
		if ca != cb {
			return ca < cb
		}
		axisA := out[a][0] == 0 || out[a][1] == 0
		axisB := out[b][0] == 0 || out[b][1] == 0
		if axisA != axisB {
			return axisA
		}
		if out[a][0] != out[b][0] {
			return out[a][0] < out[b][0]
		}
		return out[a][1] < out[b][1]
	})
	return out
}

func firstSegmentMatchesDir(path [][2]float64, dir string) bool {
	if len(path) < 2 {
		return false
	}
	dx := path[1][0] - path[0][0]
	dy := path[1][1] - path[0][1]
	switch dir {
	case "up":
		return math.Abs(dx) < 0.5 && dy > 0.5
	case "down":
		return math.Abs(dx) < 0.5 && dy < -0.5
	case "left":
		return math.Abs(dy) < 0.5 && dx < -0.5
	case "right":
		return math.Abs(dy) < 0.5 && dx > 0.5
	}
	return true
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

// anyOverlap is for boxes that were already inflated by the desired clearance:
// any positive intersection means the real gap is smaller than that clearance.
func anyOverlap(a, b layoutBBox) bool {
	ix, iy, hit := overlapExtent(a, b)
	return hit && ix > 0 && iy > 0
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
		if anyOverlap(newBoxGrown, *o.BBox) {
			return false
		}
	}
	// Marker-check only compares marker bodies with parts/markers. A body can
	// still swallow a wire label; judge it against visible text as well.
	for _, it := range items {
		if it.ParentID == c.ID || it.BBox == nil {
			continue
		}
		if anyOverlap(newBoxGrown, *it.BBox) {
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
			if anyOverlap(bGrown, *o.BBox) {
				return false
			}
		}
		for _, other := range items {
			if other.ID == it.ID || other.ParentID == c.ID || other.BBox == nil {
				continue
			}
			if anyOverlap(bGrown, *other.BBox) {
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
			if o.ID == c.ID {
				// The stub is part of this marker: its anchor sits just below
				// the body. Judge self-contact at zero clearance, never with the
				// external 2-unit gap that would reject every folded route on a
				// compact power flag.
				if schSegmentCrossesBox(trimmed[0], trimmed[1], trimmed[2], trimmed[3], newBox) {
					return false
				}
				continue
			}
			if schSegmentCrossesBox(trimmed[0], trimmed[1], trimmed[2], trimmed[3],
				inflateLayoutBBox(*o.BBox, clearance)) {
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

// partMoveClear validates a rigid part-group translation. The moved set is the
// part, its attached flags, those flags' labels, the attached wire trees and
// the net labels on those wires. Everything outside that set is an obstacle.
func groupMoveClear(delta [2]float64, movedComp, movedWire map[string]bool,
	movedTextIDs []string, items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox) bool {
	if sheet == nil {
		return false
	}
	const clearance = 2.0
	usable := schUsableArea(*sheet)
	movedText := map[string]bool{}
	for _, id := range movedTextIDs {
		movedText[id] = true
	}
	dx, dy := delta[0], delta[1]

	for _, o := range comps {
		if !movedComp[o.ID] || o.BBox == nil {
			continue
		}
		nb := translateLayoutBBox(*o.BBox, delta)
		if !boxInside2(nb, usable) {
			return false
		}
		if titleBlock != nil && positiveOverlap(nb, *titleBlock) {
			return false
		}
		nbG := inflateLayoutBBox(nb, clearance)
		for _, u := range comps {
			if movedComp[u.ID] || u.BBox == nil || u.ComponentType == "sheet" {
				continue
			}
			if anyOverlap(nbG, *u.BBox) {
				return false
			}
			for _, pin := range u.Pins {
				if pointInBox2(pin.X, pin.Y, nb, clearance) {
					return false
				}
			}
			for _, mp := range o.Pins {
				px, py := mp.X+dx, mp.Y+dy
				if pointInBox2(px, py, *u.BBox, 0.5) {
					return false
				}
				for _, up := range u.Pins {
					if math.Abs(px-up.X) <= 0.5 && math.Abs(py-up.Y) <= 0.5 {
						return false
					}
				}
			}
		}
		for _, it := range items {
			if movedText[it.ID] || it.BBox == nil {
				continue
			}
			if anyOverlap(nbG, *it.BBox) {
				return false
			}
		}
		for _, w := range wires {
			if movedWire[w.ID] {
				continue
			}
			for _, s := range labelWireSegments(w) {
				if schSegmentCrossesBox(s[0], s[1], s[2], s[3], nbG) {
					return false
				}
				for _, mp := range o.Pins {
					if pointNearSegment2(mp.X+dx, mp.Y+dy,
						[2]float64{s[0], s[1]}, [2]float64{s[2], s[3]}, 0.5) {
						return false
					}
				}
			}
		}
	}

	for _, it := range items {
		if !movedText[it.ID] || it.BBox == nil {
			continue
		}
		nb := translateLayoutBBox(*it.BBox, delta)
		if !boxInside2(nb, usable) {
			return false
		}
		if titleBlock != nil && positiveOverlap(nb, *titleBlock) {
			return false
		}
		nbG := inflateLayoutBBox(nb, clearance)
		for _, u := range comps {
			if movedComp[u.ID] || u.BBox == nil || u.ComponentType == "sheet" {
				continue
			}
			if anyOverlap(nbG, *u.BBox) {
				return false
			}
			for _, pin := range u.Pins {
				if pointInBox2(pin.X, pin.Y, nb, clearance) {
					return false
				}
			}
		}
		for _, other := range items {
			if other.ID == it.ID || movedText[other.ID] || other.BBox == nil {
				continue
			}
			if anyOverlap(nbG, *other.BBox) {
				return false
			}
		}
		for _, w := range wires {
			if movedWire[w.ID] {
				continue
			}
			for _, s := range labelWireSegments(w) {
				if schSegmentCrossesBox(s[0], s[1], s[2], s[3], nbG) {
					return false
				}
			}
		}
	}

	for _, w := range wires {
		if !movedWire[w.ID] {
			continue
		}
		for _, s := range labelWireSegments(w) {
			seg := [4]float64{s[0] + dx, s[1] + dy, s[2] + dx, s[3] + dy}
			if !pointInBox2(seg[0], seg[1], usable, 0) || !pointInBox2(seg[2], seg[3], usable, 0) {
				return false
			}
			if titleBlock != nil && schSegmentCrossesBox(seg[0], seg[1], seg[2], seg[3], *titleBlock) {
				return false
			}
			for _, u := range comps {
				if movedComp[u.ID] || u.BBox == nil || u.ComponentType == "sheet" {
					continue
				}
				if schSegmentCrossesBox(seg[0], seg[1], seg[2], seg[3],
					inflateLayoutBBox(*u.BBox, clearance)) {
					return false
				}
				for _, pin := range u.Pins {
					if pointNearSegment2(pin.X, pin.Y, [2]float64{seg[0], seg[1]},
						[2]float64{seg[2], seg[3]}, 0.5) {
						return false
					}
				}
			}
			for _, it := range items {
				if movedText[it.ID] || it.BBox == nil {
					continue
				}
				if schSegmentCrossesBox(seg[0], seg[1], seg[2], seg[3],
					inflateLayoutBBox(*it.BBox, clearance)) {
					return false
				}
			}
			for _, o := range wires {
				if movedWire[o.ID] {
					continue
				}
				for _, os := range labelWireSegments(o) {
					if segmentsCross2([2]float64{seg[0], seg[1]}, [2]float64{seg[2], seg[3]},
						[2]float64{os[0], os[1]}, [2]float64{os[2], os[3]}) {
						return false
					}
				}
			}
		}
	}
	return true
}

func partMoveClear(c layoutComp, delta [2]float64, movedComp, movedWire map[string]bool,
	movedTextIDs []string, items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox) bool {
	if c.BBox == nil {
		return false
	}
	return groupMoveClear(delta, movedComp, movedWire, movedTextIDs, items, comps, wires, sheet, titleBlock)
}

func simulateGroupMove(partIDs, flagIDs, wireIDs []string, delta [2]float64,
	items []visibleTextItem, comps []layoutComp, wires []schGroupWire) ([]visibleTextItem, []layoutComp, []schGroupWire) {
	movedComp := map[string]bool{}
	for _, id := range partIDs {
		movedComp[id] = true
	}
	for _, id := range flagIDs {
		movedComp[id] = true
	}
	movedWire := map[string]bool{}
	for _, id := range wireIDs {
		movedWire[id] = true
	}
	simItems := make([]visibleTextItem, len(items))
	for i, it := range items {
		simItems[i] = it
		if it.BBox != nil && (movedComp[it.ParentID] || movedWire[it.ParentID]) {
			b := translateLayoutBBox(*it.BBox, delta)
			simItems[i].BBox = &b
		}
	}
	simComps := make([]layoutComp, len(comps))
	for i, o := range comps {
		simComps[i] = o
		if !movedComp[o.ID] {
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
	for i, w := range wires {
		simWires[i] = w
		if !movedWire[w.ID] {
			continue
		}
		pts := make([]float64, len(w.Points))
		for k := 0; k+1 < len(w.Points); k += 2 {
			pts[k] = w.Points[k] + delta[0]
			pts[k+1] = w.Points[k+1] + delta[1]
		}
		segs := make([][4]float64, len(w.ObservedSegments))
		copy(segs, w.ObservedSegments)
		for k := range segs {
			segs[k][0] += delta[0]
			segs[k][1] += delta[1]
			segs[k][2] += delta[0]
			segs[k][3] += delta[1]
		}
		simWires[i] = schGroupWire{ID: w.ID, Points: pts, ObservedSegments: segs}
	}
	return simItems, simComps, simWires
}

func simulatePartMove(mv dcPartMove, items []visibleTextItem, comps []layoutComp,
	wires []schGroupWire) ([]visibleTextItem, []layoutComp, []schGroupWire) {
	return simulateGroupMove([]string{mv.PartID}, mv.FlagIDs, mv.WireIDs,
		[2]float64{mv.DeltaX, mv.DeltaY}, items, comps, wires)
}

func simulatedPathClear(path [][2]float64, aWireID, bWireID, aID, bID string, items []visibleTextItem,
	comps []layoutComp, wires []schGroupWire) bool {
	find, wireIndex := wireTreeFinder(wires)
	ia, okA := wireIndex[aWireID]
	ib, okB := wireIndex[bWireID]
	if !okA || !okB {
		return false
	}
	return len(directPathBlockers(path, find(ia), find(ib), aID, bID, items, comps, wires, find, wireIndex)) == 0
}

// simulatedPathsClear recomputes both label attachment points from simulated
// geometry. A group move can carry a label's own wire, so the original path
// endpoints are no longer valid; this is the path check for that case.
func simulatedPathsClear(simItems []visibleTextItem, simComps []layoutComp, simWires []schGroupWire,
	aTextID, bTextID string, sheet, titleBlock *layoutBBox) (bool, [][2]float64, string) {
	var aItem, bItem *visibleTextItem
	for i := range simItems {
		if simItems[i].ID == aTextID {
			aItem = &simItems[i]
		}
		if simItems[i].ID == bTextID {
			bItem = &simItems[i]
		}
	}
	if aItem == nil || bItem == nil {
		return false, nil, ""
	}
	find, wireIndex := wireTreeFinder(simWires)
	ax, ay, okA := labelAttachmentPoint(*aItem, simWires, wireIndex, find)
	bx, by, okB := labelAttachmentPoint(*bItem, simWires, wireIndex, find)
	if !okA || !okB {
		return false, nil, ""
	}
	ia, okA := wireIndex[aItem.ParentID]
	ib, okB := wireIndex[bItem.ParentID]
	if !okA || !okB {
		return false, nil, ""
	}
	paths := dedupeDCPaths([][][2]float64{
		{{ax, ay}, {bx, ay}, {bx, by}},
		{{ax, ay}, {ax, by}, {bx, by}},
	})
	paths = filterDCPaths(paths, sheet, titleBlock)
	for _, path := range paths {
		if len(directPathBlockers(path, find(ia), find(ib), aTextID, bTextID,
			simItems, simComps, simWires, find, wireIndex)) == 0 {
			return true, path, dcPathName(path)
		}
	}
	return false, nil, ""
}

// pathClearanceOK rejects a proposal whose new wire would run within 2 units of
// the moved part/marker or its labels. Zero-tolerance blocking prevents overlaps;
// this keeps the result readable instead of a wire grazing a body edge.
func pathClearanceOK(path [][2]float64, movedComp, movedText map[string]bool, delta [2]float64,
	comps []layoutComp, items []visibleTextItem) bool {
	const clearance = 2.0
	for i := 0; i+1 < len(path); i++ {
		p, q := path[i], path[i+1]
		if p == q {
			continue
		}
		for _, c := range comps {
			if !movedComp[c.ID] || c.BBox == nil {
				continue
			}
			b := translateLayoutBBox(*c.BBox, delta)
			if schSegmentCrossesBox(p[0], p[1], q[0], q[1], inflateLayoutBBox(b, clearance)) {
				return false
			}
		}
		for _, it := range items {
			if !movedText[it.ID] || it.BBox == nil {
				continue
			}
			b := translateLayoutBBox(*it.BBox, delta)
			if schSegmentCrossesBox(p[0], p[1], q[0], q[1], inflateLayoutBBox(b, clearance)) {
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
		case "part-move-proposed":
			fmt.Fprintf(w, "  [part] %s %s ↔ %s distance=%.0f path=%s\n", p.Net, p.LabelA, p.LabelB, p.Distance, p.Path)
			fmt.Fprintf(w, "         move part %s %s by (%+.0f,%+.0f) with %d wire(s), %d flag(s)\n",
				p.PartMove.Designator, p.PartMove.PartID, p.PartMove.DeltaX, p.PartMove.DeltaY,
				len(p.PartMove.WireIDs), len(p.PartMove.FlagIDs))
		case "group-move-proposed":
			fmt.Fprintf(w, "  [group] %s %s ↔ %s distance=%.0f path=%s\n", p.Net, p.LabelA, p.LabelB, p.Distance, p.Path)
			fmt.Fprintf(w, "         move block %s by (%+.0f,%+.0f) with %d wire(s), %d flag(s)\n",
				strings.Join(p.GroupMove.Designators, "+"), p.GroupMove.DeltaX, p.GroupMove.DeltaY,
				len(p.GroupMove.WireIDs), len(p.GroupMove.FlagIDs))
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
			for _, s := range p.PartSkips {
				fmt.Fprintf(w, "         part %s (%s): %s\n", s.Designator, s.PartID, s.Reason)
			}
			for _, s := range p.GroupSkips {
				fmt.Fprintf(w, "         group %s: %s\n", strings.Join(s.Designators, "+"), s.Reason)
			}
		}
	}
}
