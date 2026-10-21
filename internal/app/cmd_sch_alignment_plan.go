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

type alignmentPlanReport struct {
	SchemaVersion  int                 `json:"schemaVersion"`
	DocumentID     string              `json:"documentId"`
	Net            string              `json:"net,omitempty"`
	Plans          []alignmentPlanItem `json:"plans"`
	Ready          int                 `json:"ready"`
	RewireRequired int                 `json:"rewireRequired"`
	Blocked        int                 `json:"blocked"`
}

type alignmentPlanItem struct {
	ID                      string                  `json:"id"`
	Net                     string                  `json:"net"`
	Status                  string                  `json:"status"` // ready | rewire-required | blocked
	Axis                    string                  `json:"axis"`
	ShiftX                  float64                 `json:"shiftX,omitempty"`
	ShiftY                  float64                 `json:"shiftY,omitempty"`
	BendReduction           int                     `json:"bendReduction"`
	MoveDesignators         []string                `json:"moveDesignators"`
	MoveComponentIds        []string                `json:"moveComponentIds"`
	MoveWireIds             []string                `json:"moveWireIds,omitempty"`
	MoveMarkerIds           []string                `json:"moveMarkerIds,omitempty"`
	MoveLabelIds            []string                `json:"moveLabelIds,omitempty"`
	SharedWireIds           []string                `json:"sharedWireIds,omitempty"`
	SharedLabelIds          []string                `json:"sharedLabelIds,omitempty"`
	Blockers                []string                `json:"blockers,omitempty"`
	IndependentBlockers     []string                `json:"independentBlockers,omitempty"`
	IndependentBlockerPlans []alignmentBlockerPlan  `json:"independentBlockerPlans,omitempty"`
	BoundaryPlans           []alignmentBoundaryPlan `json:"boundaryPlans,omitempty"`
	Steps                   []alignmentPlanStep     `json:"steps"`
	At                      *checkPoint             `json:"at,omitempty"`
	BBox                    *layoutBBox             `json:"bbox,omitempty"`
	Note                    string                  `json:"note,omitempty"`
}

type alignmentPlanStep struct {
	Order         int       `json:"order"`
	Kind          string    `json:"kind"`
	Action        string    `json:"action,omitempty"`
	Designators   []string  `json:"designators,omitempty"`
	PrimitiveIds  []string  `json:"primitiveIds,omitempty"`
	Net           string    `json:"net,omitempty"`
	Points        []float64 `json:"points,omitempty"`
	DX            float64   `json:"dx,omitempty"`
	DY            float64   `json:"dy,omitempty"`
	Required      bool      `json:"required"`
	ExecutableNow bool      `json:"executableNow"`
	Note          string    `json:"note,omitempty"`
}

type alignmentBoundaryPlan struct {
	Net                 string                 `json:"net"`
	Status              string                 `json:"status"` // proposed | blocked
	DeleteWireIds       []string               `json:"deleteWireIds,omitempty"`
	FixedPins           []string               `json:"fixedPins,omitempty"`
	MovedPins           []string               `json:"movedPins,omitempty"`
	Wires               []alignmentPlannedWire `json:"wires,omitempty"`
	LabelIds            []string               `json:"labelIds,omitempty"`
	Blockers            []string               `json:"blockers,omitempty"`
	IndependentBlockers []string               `json:"independentBlockers,omitempty"`
}

type alignmentPlannedWire struct {
	Net    string    `json:"net"`
	Points []float64 `json:"points"`
}

type alignmentBlockerPlan struct {
	InitialDesignators []string    `json:"initialDesignators"`
	Status             string      `json:"status"` // ready | review-required | blocked
	MoveDesignators    []string    `json:"moveDesignators,omitempty"`
	MoveComponentIds   []string    `json:"moveComponentIds,omitempty"`
	MoveWireIds        []string    `json:"moveWireIds,omitempty"`
	MoveMarkerIds      []string    `json:"moveMarkerIds,omitempty"`
	MoveLabelIds       []string    `json:"moveLabelIds,omitempty"`
	SharedWireIds      []string    `json:"sharedWireIds,omitempty"`
	DX                 float64     `json:"dx,omitempty"`
	DY                 float64     `json:"dy,omitempty"`
	BBox               *layoutBBox `json:"bbox,omitempty"`
	Blockers           []string    `json:"blockers,omitempty"`
}

type alignmentVision struct {
	Items []visibleTextItem `json:"items"`
}

func newSchAlignmentPlanCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var jsonOut bool
	var out, netFilter string
	cmd := &cobra.Command{
		Use:   "alignment-plan",
		Short: "Plan rigid group moves that remove unnecessary H-V-H/V-H-V jogs (read-only)",
		Long: `Read the active page and emit read-only plans derived from sch-check alignment-opportunity findings.
Each plan contains the complete movable functional group, a translation vector, owned wires,
markers, labels, shared boundary wires, blockers, independent prerequisite blocker plans, and
orthogonal boundary-reconnect drafts. A plan with shared boundary wires stays rewire-required;
the draft is recomputed from fresh geometry and is never executable in this command.
The command never calls modify, group.move, wire, connect, delete or save.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
				"includeBBox": true, "includePins": true, "includePinNets": true, "includePagePrimitives": true,
			})
			if err != nil {
				return err
			}
			if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
				return fmt.Errorf("alignment-plan: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
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
				return fmt.Errorf("alignment-plan: visible text came from %q, expected %q", vt.Context.DocumentUUID, doc)
			}
			var items alignmentVision
			raw, _ := json.Marshal(vt.Result)
			if err := json.Unmarshal(raw, &items); err != nil {
				return fmt.Errorf("alignment-plan: parse visible text: %w", err)
			}
			sheet := sheetBBoxOf(comps)
			titleBlock, _ := titleBlockKeepoutWithSource(sheet)
			candidates := alignmentOpportunityCandidates(items.Items, comps, wires, sheet, titleBlock)
			report := buildAlignmentPlanReport(doc, netFilter, candidates, comps, items.Items, wires, sheet, titleBlock)
			if out != "" {
				b, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return err
				}
				b = append(b, '\n')
				if err := os.WriteFile(out, b, 0644); err != nil {
					return fmt.Errorf("write alignment plan: %w", err)
				}
				fmt.Fprintf(stderr, "alignment plan written to %s (%d plan(s))\n", out, len(report.Plans))
				return nil
			}
			if jsonOut {
				b, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return err
				}
				_, err = stdout.Write(append(b, '\n'))
				return err
			}
			renderAlignmentPlan(stdout, report)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit structured JSON")
	cmd.Flags().StringVar(&out, "out", "", "write structured JSON to a file")
	cmd.Flags().StringVar(&netFilter, "net", "", "only plan opportunities for this net")
	return cmd
}

func buildAlignmentPlanReport(doc, netFilter string, candidates []alignmentCandidate, comps []layoutComp,
	items []visibleTextItem, wires []schGroupWire, sheet, titleBlock *layoutBBox) alignmentPlanReport {
	report := alignmentPlanReport{SchemaVersion: 1, DocumentID: doc, Net: netFilter, Plans: []alignmentPlanItem{}}
	partIDs := map[string]string{}
	for _, c := range comps {
		if c.Designator != "" {
			partIDs[c.Designator] = c.ID
		}
	}
	for _, c := range candidates {
		if netFilter != "" && c.Net != netFilter {
			continue
		}
		item := alignmentPlanItem{
			ID:  fmt.Sprintf("alignment-%03d", len(report.Plans)+1),
			Net: c.Net, Axis: c.Axis, ShiftX: 0, ShiftY: 0, BendReduction: 2,
			MoveDesignators:     append([]string(nil), c.MoveDesignators...),
			MoveWireIds:         append([]string(nil), c.MoveWireIDs...),
			MoveMarkerIds:       append([]string(nil), c.MoveMarkerIDs...),
			MoveLabelIds:        append([]string(nil), c.MoveLabelIDs...),
			SharedWireIds:       append([]string(nil), c.SharedWireIDs...),
			Blockers:            append([]string(nil), c.Blockers...),
			IndependentBlockers: append([]string(nil), c.IndependentBlockers...),
			At:                  &c.At, BBox: &c.MovedBBox,
		}
		if c.Axis == "x" {
			item.ShiftX = c.Shift
		} else {
			item.ShiftY = c.Shift
		}
		var componentIDs []string
		for _, d := range c.MoveDesignators {
			if id := partIDs[d]; id != "" {
				componentIDs = append(componentIDs, id)
			}
		}
		sort.Strings(componentIDs)
		item.MoveComponentIds = componentIDs
		item.BoundaryPlans, item.SharedLabelIds = alignmentBoundaryPlans(c, comps, items, wires, sheet, titleBlock)
		for _, bp := range item.BoundaryPlans {
			for _, blocker := range bp.Blockers {
				item.Blockers = appendUniqueString(item.Blockers, "boundary:"+blocker)
			}
			for _, blocker := range bp.IndependentBlockers {
				item.IndependentBlockers = appendUniqueString(item.IndependentBlockers, blocker)
			}
		}
		blockerCandidate := c
		blockerCandidate.IndependentBlockers = append([]string(nil), item.IndependentBlockers...)
		item.IndependentBlockerPlans = alignmentBuildBlockerPlans(blockerCandidate, comps, items, wires, sheet, titleBlock)
		for _, blocker := range alignmentPrimaryMoveBlockers(c, comps, items, wires, sheet, titleBlock) {
			item.Blockers = appendUniqueString(item.Blockers, blocker)
		}
		switch {
		case len(item.Blockers) > 0:
			item.Status = "blocked"
			item.Note = "先处理 blockers；independentBlockers 可尝试作为单独组前置移动"
			report.Blocked++
		case len(item.SharedWireIds) > 0:
			item.Status = "rewire-required"
			item.Note = "移动后必须重建 sharedWireIds 的边界连接"
			report.RewireRequired++
		default:
			item.Status = "ready"
			item.Note = "组内没有外部阻塞或共享边界线"
			report.Ready++
		}
		item.Steps = alignmentPlanSteps(item, c)
		report.Plans = append(report.Plans, item)
	}
	return report
}

func alignmentPrimaryMoveBlockers(c alignmentCandidate, comps []layoutComp, items []visibleTextItem,
	wires []schGroupWire, sheet, titleBlock *layoutBBox) []string {
	if sheet == nil {
		return []string{"sheet-geometry-unavailable"}
	}
	movedComp := map[string]bool{}
	movedParent := map[string]bool{}
	for _, designator := range c.MoveDesignators {
		found := false
		for _, comp := range comps {
			if comp.Designator != designator {
				continue
			}
			if comp.ID == "" || comp.BBox == nil {
				return []string{"group-geometry-unavailable"}
			}
			movedComp[comp.ID] = true
			movedParent[comp.ID] = true
			found = true
			break
		}
		if !found {
			return []string{"group-geometry-unavailable"}
		}
	}
	movedWire := map[string]bool{}
	for _, id := range c.MoveWireIDs {
		movedWire[id] = true
		movedParent[id] = true
	}
	for _, id := range c.MoveMarkerIDs {
		movedComp[id] = true
		movedParent[id] = true
	}
	movedText := map[string]bool{}
	for _, id := range c.MoveLabelIDs {
		movedText[id] = true
	}
	for _, it := range items {
		if it.ID != "" && movedParent[it.ParentID] {
			movedText[it.ID] = true
		}
	}
	if !groupMoveClear(
		[2]float64{candidateShiftX(c), candidateShiftY(c)},
		movedComp, movedWire, visibleTextIDList(movedText), items, comps, wires, sheet, titleBlock,
	) {
		return []string{"group-move-geometry-collision"}
	}
	return nil
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func alignmentBuildBlockerPlans(c alignmentCandidate, comps []layoutComp, items []visibleTextItem,
	wires []schGroupWire, sheet, titleBlock *layoutBBox) []alignmentBlockerPlan {
	if len(c.IndependentBlockers) == 0 {
		return nil
	}
	byDesignator := map[string]layoutComp{}
	for _, comp := range comps {
		if comp.Designator != "" {
			byDesignator[comp.Designator] = comp
		}
	}
	handled := map[string]bool{}
	var plans []alignmentBlockerPlan
	for _, initial := range c.IndependentBlockers {
		if handled[initial] {
			continue
		}
		comp, ok := byDesignator[initial]
		if !ok || comp.BBox == nil {
			plans = append(plans, alignmentBlockerPlan{
				InitialDesignators: []string{initial}, Status: "blocked",
				Blockers: []string{"blocker-geometry-unavailable"},
			})
			continue
		}
		group := alignmentExpandLocalGroup(comps, []string{initial}, nil, "", *comp.BBox)
		for _, d := range group {
			handled[d] = true
		}
		bbox, ok := alignmentPartsBBox(comps, group)
		if !ok {
			plans = append(plans, alignmentBlockerPlan{
				InitialDesignators: []string{initial}, Status: "blocked",
				MoveDesignators: group, Blockers: []string{"group-geometry-unavailable"},
			})
			continue
		}
		moveWires, sharedWires, moveMarkers, moveLabels := alignmentOwnedResources(comps, wires, items, group)
		var componentIDs []string
		for _, d := range group {
			if id := byDesignator[d].ID; id != "" {
				componentIDs = append(componentIDs, id)
			}
		}
		sort.Strings(componentIDs)
		dx, dy, blockers := alignmentFindBlockerShift(
			bbox, c.MovedBBox, group, c.MoveDesignators, comps, items, wires,
			moveWires, moveMarkers, moveLabels, c.Axis, sheet, titleBlock,
		)
		plan := alignmentBlockerPlan{
			InitialDesignators: []string{initial}, MoveDesignators: group,
			MoveComponentIds: componentIDs, MoveWireIds: moveWires,
			MoveMarkerIds: moveMarkers, MoveLabelIds: moveLabels, SharedWireIds: sharedWires,
			BBox: &bbox, DX: dx, DY: dy,
		}
		if len(sharedWires) > 0 {
			blockers = append(blockers, "shared-wire-reconnect-required")
		}
		if len(blockers) > 0 {
			plan.Status = "blocked"
			for _, blocker := range blockers {
				plan.Blockers = appendUniqueString(plan.Blockers, blocker)
			}
			sort.Strings(plan.Blockers)
		} else if !alignmentBlockerMoveIsLocal(dx, dy) {
			plan.Status = "review-required"
			plan.Blockers = []string{"blocker-move-exceeds-local-radius"}
		} else {
			plan.Status = "ready"
		}
		plans = append(plans, plan)
	}
	sort.Slice(plans, func(i, j int) bool {
		return strings.Join(plans[i].InitialDesignators, ",") < strings.Join(plans[j].InitialDesignators, ",")
	})
	return plans
}

func alignmentBlockerMoveIsLocal(dx, dy float64) bool {
	const (
		maxAxis = 50.0
		maxL1   = 60.0
	)
	return math.Max(math.Abs(dx), math.Abs(dy)) <= maxAxis &&
		math.Abs(dx)+math.Abs(dy) <= maxL1
}

func alignmentPartsBBox(comps []layoutComp, designators []string) (layoutBBox, bool) {
	want := map[string]bool{}
	for _, d := range designators {
		want[d] = true
	}
	var box layoutBBox
	have := false
	for _, c := range comps {
		if !want[c.Designator] || c.BBox == nil {
			continue
		}
		if !have {
			box, have = *c.BBox, true
			continue
		}
		box.MinX, box.MinY = math.Min(box.MinX, c.BBox.MinX), math.Min(box.MinY, c.BBox.MinY)
		box.MaxX, box.MaxY = math.Max(box.MaxX, c.BBox.MaxX), math.Max(box.MaxY, c.BBox.MaxY)
	}
	return box, have
}

func alignmentFindBlockerShift(bbox, mainMoved layoutBBox, blockerGroup, mainGroup []string,
	comps []layoutComp, items []visibleTextItem, wires []schGroupWire,
	moveWires, moveMarkers, moveLabels []string, mainAxis string,
	sheet, titleBlock *layoutBBox) (float64, float64, []string) {
	excluded := map[string]bool{}
	for _, d := range blockerGroup {
		excluded[d] = true
	}
	for _, d := range mainGroup {
		excluded[d] = true
	}
	movedComp := map[string]bool{}
	for _, d := range blockerGroup {
		for _, comp := range comps {
			if comp.Designator == d && comp.ID != "" {
				movedComp[comp.ID] = true
				break
			}
		}
	}
	for _, id := range moveMarkers {
		movedComp[id] = true
	}
	movedWire := map[string]bool{}
	movedParent := map[string]bool{}
	for _, id := range moveWires {
		movedWire[id] = true
		movedParent[id] = true
	}
	for _, id := range moveMarkers {
		movedParent[id] = true
	}
	for id := range movedComp {
		movedParent[id] = true
	}
	movedText := map[string]bool{}
	for _, id := range moveLabels {
		movedText[id] = true
	}
	for _, it := range items {
		if it.ID != "" && movedParent[it.ParentID] {
			movedText[it.ID] = true
		}
	}
	lastBlockers := []string{"no-clear-blocker-move"}
	for _, d := range alignmentBlockerMoveDeltas(mainAxis) {
		moved := translateLayoutBBox(bbox, [2]float64{d.dx, d.dy})
		if boxesOverlap2(moved, mainMoved) {
			continue
		}
		blocked := false
		var blockers []string
		if titleBlock != nil && boxesOverlap2(moved, *titleBlock) {
			blocked = true
			blockers = append(blockers, "title-block")
		}
		if sheet != nil && !boxInside2(moved, *sheet) {
			blocked = true
			blockers = append(blockers, "sheet-boundary")
		}
		for _, comp := range comps {
			if comp.BBox == nil || excluded[comp.Designator] ||
				(comp.ComponentType != "" && comp.ComponentType != schLayoutPartType) {
				continue
			}
			if boxesOverlap2(*comp.BBox, moved) {
				blocked = true
				label := comp.Designator
				if label == "" {
					label = comp.ID
				}
				blockers = append(blockers, label)
			}
		}
		if sheet == nil {
			blocked = true
			blockers = append(blockers, "sheet-geometry-unavailable")
		} else if !groupMoveClear(
			[2]float64{d.dx, d.dy}, movedComp, movedWire, visibleTextIDList(movedText),
			items, comps, wires, sheet, titleBlock,
		) {
			blocked = true
			blockers = append(blockers, "group-move-geometry-collision")
		}
		if !blocked {
			return d.dx, d.dy, nil
		}
		sort.Strings(blockers)
		lastBlockers = blockers
	}
	return 0, 0, lastBlockers
}

type alignmentDelta struct {
	dx, dy float64
}

func alignmentBlockerMoveDeltas(mainAxis string) []alignmentDelta {
	const (
		step    = 5.0
		maxMove = 200.0
	)
	var out []alignmentDelta
	seen := map[alignmentDelta]bool{}
	add := func(dx, dy float64) {
		key := alignmentDelta{dx: dx, dy: dy}
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, key)
	}
	for radius := step; radius <= maxMove; radius += step {
		// Prefer leaving the main wire corridor on its perpendicular axis, then
		// try the movement axis. Remaining same-distance diagonal/ring positions
		// follow before any larger displacement.
		if mainAxis == "y" {
			add(-radius, 0)
			add(radius, 0)
			add(0, -radius)
			add(0, radius)
		} else {
			add(0, -radius)
			add(0, radius)
			add(-radius, 0)
			add(radius, 0)
		}
		type ringItem struct {
			delta alignmentDelta
			l1    float64
		}
		var ring []ringItem
		for dx := -radius; dx <= radius; dx += step {
			for dy := -radius; dy <= radius; dy += step {
				if math.Max(math.Abs(dx), math.Abs(dy)) != radius {
					continue
				}
				if dx == 0 || dy == 0 {
					continue
				}
				ring = append(ring, ringItem{
					delta: alignmentDelta{dx: dx, dy: dy},
					l1:    math.Abs(dx) + math.Abs(dy),
				})
			}
		}
		sort.Slice(ring, func(i, j int) bool {
			if ring[i].l1 != ring[j].l1 {
				return ring[i].l1 < ring[j].l1
			}
			if ring[i].delta.dx != ring[j].delta.dx {
				return ring[i].delta.dx < ring[j].delta.dx
			}
			return ring[i].delta.dy < ring[j].delta.dy
		})
		for _, item := range ring {
			add(item.delta.dx, item.delta.dy)
		}
	}
	return out
}

type alignmentBoundaryPin struct {
	Key           string
	X, Y          float64
	Rotation      float64
	RotationKnown bool
	Moved         bool
}

func alignmentBoundaryPlans(c alignmentCandidate, comps []layoutComp, items []visibleTextItem,
	wires []schGroupWire, sheet, titleBlock *layoutBBox) ([]alignmentBoundaryPlan, []string) {
	if len(c.SharedWireIDs) == 0 {
		return nil, nil
	}
	group := map[string]bool{}
	for _, d := range c.MoveDesignators {
		group[d] = true
	}
	shared := map[string]bool{}
	for _, id := range c.SharedWireIDs {
		shared[id] = true
	}
	anchorsByNet := map[string][]alignmentBoundaryPin{}
	seen := map[string]bool{}
	for _, w := range wires {
		if !shared[w.ID] {
			continue
		}
		for _, comp := range comps {
			if comp.ComponentType != "" && comp.ComponentType != schLayoutPartType {
				continue
			}
			for _, pin := range comp.Pins {
				net := strings.TrimSpace(pin.Net)
				if net == "" || !wireTouchesPoint(w, pin.X, pin.Y) {
					continue
				}
				key := comp.Designator + ":" + pin.Number + ":" + net
				if seen[key] {
					continue
				}
				seen[key] = true
				anchorsByNet[net] = append(anchorsByNet[net], alignmentBoundaryPin{
					Key: comp.Designator + "." + pin.Number, X: pin.X, Y: pin.Y,
					Rotation: pin.Rotation, RotationKnown: pin.RotationKnown,
					Moved: group[comp.Designator],
				})
			}
		}
	}
	var labelIDs []string
	for _, it := range items {
		if shared[it.ParentID] && it.ID != "" {
			labelIDs = append(labelIDs, it.ID)
		}
	}
	sort.Strings(labelIDs)
	netNames := make([]string, 0, len(anchorsByNet))
	for net := range anchorsByNet {
		netNames = append(netNames, net)
	}
	sort.Strings(netNames)
	var plans []alignmentBoundaryPlan
	for _, net := range netNames {
		anchors := anchorsByNet[net]
		var fixed, moved []alignmentBoundaryPin
		var fixedNames, movedNames []string
		for _, a := range anchors {
			if a.Moved {
				moved = append(moved, a)
				movedNames = append(movedNames, a.Key)
			} else {
				fixed = append(fixed, a)
				fixedNames = append(fixedNames, a.Key)
			}
		}
		sort.Strings(fixedNames)
		sort.Strings(movedNames)
		bp := alignmentBoundaryPlan{
			Net: net, FixedPins: fixedNames, MovedPins: movedNames,
			DeleteWireIds: append([]string(nil), c.SharedWireIDs...), Status: "proposed",
		}
		if len(fixed) == 0 || len(moved) == 0 {
			bp.Status = "blocked"
			bp.Blockers = []string{"no-cross-boundary-anchors"}
			plans = append(plans, bp)
			continue
		}
		var unknownRotation []string
		for _, anchor := range anchors {
			if !anchor.RotationKnown {
				unknownRotation = append(unknownRotation, "pin-rotation-unavailable:"+anchor.Key)
			}
		}
		if len(unknownRotation) > 0 {
			sort.Strings(unknownRotation)
			bp.Status = "blocked"
			bp.Blockers = unknownRotation
			plans = append(plans, bp)
			continue
		}
		options := alignmentBoundaryWireOptions(c, net, fixed, moved)
		var bestSegs [][4]float64
		var bestBlockers []string
		var bestIndependent []string
		bestLen := math.Inf(1)
		for _, segs := range options {
			blockers, independent := alignmentBoundaryBlockers(
				segs, net, c, comps, items, group, wires, sheet, titleBlock,
			)
			length := alignmentSegmentsLength(segs)
			if bestSegs == nil || len(blockers) < len(bestBlockers) || (len(blockers) == len(bestBlockers) && length < bestLen) {
				bestSegs, bestBlockers, bestIndependent, bestLen = segs, blockers, independent, length
			}
		}
		if len(bestSegs) == 0 {
			bp.Status = "blocked"
			bp.Blockers = []string{"no-orthogonal-boundary-route"}
			plans = append(plans, bp)
			continue
		}
		for _, seg := range bestSegs {
			bp.Wires = append(bp.Wires, alignmentPlannedWire{Net: net, Points: seg[:]})
		}
		bp.Blockers = bestBlockers
		bp.IndependentBlockers = bestIndependent
		if len(bp.Blockers) > 0 {
			bp.Status = "blocked"
		}
		plans = append(plans, bp)
	}
	return plans, labelIDs
}

func alignmentBoundaryWireOptions(c alignmentCandidate, net string, fixed, moved []alignmentBoundaryPin) [][][4]float64 {
	anchors := make([]alignmentBoundaryPin, 0, len(fixed)+len(moved))
	anchors = append(anchors, fixed...)
	for _, a := range moved {
		a.X += candidateShiftX(c)
		a.Y += candidateShiftY(c)
		anchors = append(anchors, a)
	}
	if net == c.Net {
		if c.Axis == "y" {
			return [][][4]float64{alignmentBusWires(anchors, true, c.TargetCoord)}
		}
		return [][][4]float64{alignmentBusWires(anchors, false, c.TargetCoord)}
	}
	// Secondary nets usually have one fixed and one moved pin. Enumerate both L
	// orientations, including multi-anchor hub trees, so a blocked first choice
	// does not hide a clear route.
	const maxSecondaryOptions = 32
	var out [][][4]float64
	seen := map[string]bool{}
	for _, hub := range fixed {
		var leaves []alignmentBoundaryPin
		for _, anchor := range anchors {
			if math.Abs(anchor.X-hub.X) <= alignmentTolerance &&
				math.Abs(anchor.Y-hub.Y) <= alignmentTolerance {
				continue
			}
			leaves = append(leaves, anchor)
		}
		for _, segs := range alignmentTreeOptions(hub, leaves) {
			key := alignmentSegmentsKey(segs)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, segs)
			if len(out) >= maxSecondaryOptions {
				return out
			}
		}
	}
	return out
}

func alignmentTreeOptions(hub alignmentBoundaryPin, leaves []alignmentBoundaryPin) [][][4]float64 {
	options := [][][4]float64{{}}
	for _, leaf := range leaves {
		choices := alignmentRoutesBetween(hub, leaf)
		if len(choices) == 0 {
			return nil
		}
		next := make([][][4]float64, 0, len(options)*len(choices))
		for _, existing := range options {
			for _, choice := range choices {
				combined := append(append([][4]float64(nil), existing...), choice...)
				next = append(next, alignmentDedupeSegments(combined))
			}
		}
		options = next
		if len(options) > 32 {
			options = options[:32]
		}
	}
	return options
}

func alignmentSegmentsKey(segs [][4]float64) string {
	var b strings.Builder
	for _, s := range segs {
		fmt.Fprintf(&b, "%.3f,%.3f,%.3f,%.3f;", s[0], s[1], s[2], s[3])
	}
	return b.String()
}

func alignmentBusWires(anchors []alignmentBoundaryPin, horizontal bool, trunk float64) [][4]float64 {
	if len(anchors) == 0 {
		return nil
	}
	var out [][4]float64
	var joins []float64
	for _, a := range anchors {
		lead, ok := alignmentOutwardLead(a)
		if !ok {
			return nil
		}
		out = append(out, [4]float64{a.X, a.Y, lead.X, lead.Y})
		if horizontal {
			if math.Abs(lead.Y-trunk) > alignmentTolerance {
				out = append(out, [4]float64{lead.X, lead.Y, lead.X, trunk})
			}
			joins = append(joins, lead.X)
		} else {
			if math.Abs(lead.X-trunk) > alignmentTolerance {
				out = append(out, [4]float64{lead.X, lead.Y, trunk, lead.Y})
			}
			joins = append(joins, lead.Y)
		}
	}
	if len(joins) == 0 {
		return nil
	}
	lo, hi := joins[0], joins[0]
	for _, v := range joins[1:] {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if hi-lo > alignmentTolerance {
		if horizontal {
			out = append(out, [4]float64{lo, trunk, hi, trunk})
		} else {
			out = append(out, [4]float64{trunk, lo, trunk, hi})
		}
	}
	return alignmentDedupeSegments(out)
}

const alignmentBoundaryLead = 5.0

func alignmentOutwardLead(pin alignmentBoundaryPin) (alignmentBoundaryPin, bool) {
	dx, dy, ok := alignmentOutwardVector(pin)
	if !ok {
		return alignmentBoundaryPin{}, false
	}
	pin.X += dx * alignmentBoundaryLead
	pin.Y += dy * alignmentBoundaryLead
	return pin, true
}

func alignmentOutwardVector(pin alignmentBoundaryPin) (float64, float64, bool) {
	if !pin.RotationKnown {
		return 0, 0, false
	}
	r := math.Mod(math.Mod(pin.Rotation, 360)+360, 360) / 90
	if math.Abs(r-math.Round(r)) > alignmentTolerance {
		return 0, 0, false
	}
	switch int(math.Round(r)) % 4 {
	case 0:
		return 1, 0, true
	case 1:
		return 0, 1, true
	case 2:
		return -1, 0, true
	case 3:
		return 0, -1, true
	}
	return 0, 0, false
}

func alignmentRoutesBetween(a, b alignmentBoundaryPin) [][][4]float64 {
	aLead, aok := alignmentOutwardLead(a)
	bLead, bok := alignmentOutwardLead(b)
	if !aok || !bok {
		return nil
	}
	leadA := [4]float64{a.X, a.Y, aLead.X, aLead.Y}
	leadB := [4]float64{b.X, b.Y, bLead.X, bLead.Y}
	var middles [][][4]float64
	if math.Abs(aLead.X-bLead.X) <= alignmentTolerance || math.Abs(aLead.Y-bLead.Y) <= alignmentTolerance {
		middles = append(middles, [][4]float64{{aLead.X, aLead.Y, bLead.X, bLead.Y}})
	} else {
		middles = append(middles,
			[][4]float64{{aLead.X, aLead.Y, bLead.X, aLead.Y}, {bLead.X, aLead.Y, bLead.X, bLead.Y}},
			[][4]float64{{aLead.X, aLead.Y, aLead.X, bLead.Y}, {aLead.X, bLead.Y, bLead.X, bLead.Y}},
		)
	}
	var out [][][4]float64
	for _, middle := range middles {
		combined := append([][4]float64{leadA}, middle...)
		combined = append(combined, leadB)
		out = append(out, alignmentDedupeSegments(combined))
	}
	return out
}

func alignmentSegmentsLength(segs [][4]float64) float64 {
	total := 0.0
	for _, s := range segs {
		total += math.Abs(s[2]-s[0]) + math.Abs(s[3]-s[1])
	}
	return total
}

func alignmentDedupeSegments(segs [][4]float64) [][4]float64 {
	seen := map[[4]float64]bool{}
	var out [][4]float64
	for _, s := range segs {
		key := s
		if key[0] > key[2] || (key[0] == key[2] && key[1] > key[3]) {
			key = [4]float64{key[2], key[3], key[0], key[1]}
		}
		if seen[key] || (math.Abs(s[0]-s[2]) <= alignmentTolerance && math.Abs(s[1]-s[3]) <= alignmentTolerance) {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}

func alignmentBoundaryBlockers(segs [][4]float64, net string, c alignmentCandidate,
	comps []layoutComp, items []visibleTextItem, group map[string]bool,
	wires []schGroupWire, sheet, titleBlock *layoutBBox) ([]string, []string) {
	blocked := map[string]bool{}
	independent := map[string]bool{}
	shared := map[string]bool{}
	for _, id := range c.SharedWireIDs {
		shared[id] = true
	}
	movedWires := map[string]bool{}
	for _, id := range c.MoveWireIDs {
		movedWires[id] = true
	}
	movedMarkers := map[string]bool{}
	for _, id := range c.MoveMarkerIDs {
		movedMarkers[id] = true
	}
	wireNets := alignmentWireNetMap(comps, wires)
	for _, seg := range segs {
		if sheet != nil && (!pointInBox2(seg[0], seg[1], *sheet, alignmentTolerance) ||
			!pointInBox2(seg[2], seg[3], *sheet, alignmentTolerance)) {
			blocked["sheet-boundary"] = true
		}
		if titleBlock != nil && schSegmentCrossesBox(seg[0], seg[1], seg[2], seg[3], *titleBlock) {
			blocked["title-block"] = true
		}
		for _, comp := range comps {
			if comp.BBox == nil || (comp.ComponentType != "" && comp.ComponentType != schLayoutPartType) {
				continue
			}
			box := *comp.BBox
			if group[comp.Designator] {
				if c.Axis == "y" {
					box = translateLayoutBBox(box, [2]float64{0, c.Shift})
				} else {
					box = translateLayoutBBox(box, [2]float64{c.Shift, 0})
				}
			}
			if schguard.SegmentEntersBody(
				schguard.Point{X: seg[0], Y: seg[1]},
				schguard.Point{X: seg[2], Y: seg[3]},
				schguard.BBox{MinX: box.MinX, MinY: box.MinY, MaxX: box.MaxX, MaxY: box.MaxY},
			) {
				label := comp.Designator
				if label == "" {
					label = comp.ID
				}
				if label != "" {
					blocked["part:"+label] = true
				}
			}
			for _, pin := range comp.Pins {
				px, py := pin.X, pin.Y
				if group[comp.Designator] {
					px, py = px+candidateShiftX(c), py+candidateShiftY(c)
				}
				if strings.TrimSpace(pin.Net) == net ||
					!pointNearSegment2(px, py, [2]float64{seg[0], seg[1]}, [2]float64{seg[2], seg[3]}, alignmentTolerance) {
					continue
				}
				label := comp.Designator
				if label == "" {
					label = comp.ID
				}
				pinLabel := label + "." + pin.Number
				blocked["pin:"+pinLabel] = true
				if label != "" && !group[comp.Designator] && !alignmentActivePart(comp) {
					independent[label] = true
				}
			}
		}
		for _, comp := range comps {
			if !isSchMarker(comp.ComponentType) || !comp.AnchorAvailable {
				continue
			}
			px, py := comp.X, comp.Y
			if movedMarkers[comp.ID] {
				px, py = px+candidateShiftX(c), py+candidateShiftY(c)
			}
			if strings.TrimSpace(comp.Net) == net ||
				!pointNearSegment2(px, py, [2]float64{seg[0], seg[1]}, [2]float64{seg[2], seg[3]}, alignmentTolerance) {
				continue
			}
			label := comp.ID
			if label == "" {
				label = comp.Net
			}
			blocked["marker:"+label] = true
		}
		for _, it := range items {
			if it.BBox == nil || shared[it.ParentID] {
				continue
			}
			box := *it.BBox
			if group[alignmentDesignatorByPartID(comps, it.ParentID)] ||
				movedWires[it.ParentID] || movedMarkers[it.ParentID] {
				box = translateLayoutBBox(box, [2]float64{candidateShiftX(c), candidateShiftY(c)})
			}
			if schSegmentCrossesBox(seg[0], seg[1], seg[2], seg[3], box) {
				label := strings.TrimSpace(it.Value)
				if label == "" {
					label = it.ID
				}
				blocked["text:"+label] = true
			}
		}
		for _, w := range wires {
			if shared[w.ID] {
				continue // this primitive is deleted and rebuilt by the plan
			}
			dx, dy := 0.0, 0.0
			if movedWires[w.ID] {
				dx, dy = candidateShiftX(c), candidateShiftY(c)
			}
			for _, existing := range labelWireSegments(w) {
				moved := [4]float64{existing[0] + dx, existing[1] + dy, existing[2] + dx, existing[3] + dy}
				if !alignmentSegmentsConflict(seg, moved) {
					continue
				}
				wnet := wireNets[w.ID]
				if wnet == net && !alignmentSegmentsOverlap(seg, moved) {
					continue
				}
				label := w.ID
				if wnet != "" {
					label += ":" + wnet
				} else {
					label += ":net-unknown"
				}
				blocked["wire:"+label] = true
				owners := alignmentWireOwnerParts(comps, w, group)
				blockerCanMove := len(owners) > 0
				for _, owner := range owners {
					if alignmentActivePart(owner) {
						blockerCanMove = false
					}
				}
				for _, owner := range owners {
					label := owner.Designator
					if label == "" {
						label = owner.ID
					}
					if label != "" {
						blocked["part:"+label] = true
						if blockerCanMove {
							independent[label] = true
						}
					}
				}
			}
		}
	}
	out := make([]string, 0, len(blocked))
	for item := range blocked {
		out = append(out, item)
	}
	sort.Strings(out)
	ind := make([]string, 0, len(independent))
	for item := range independent {
		ind = append(ind, item)
	}
	sort.Strings(ind)
	return out, ind
}

func alignmentDesignatorByPartID(comps []layoutComp, id string) string {
	if id == "" {
		return ""
	}
	for _, comp := range comps {
		if comp.ID == id {
			return comp.Designator
		}
	}
	return ""
}

func visibleTextIDList(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for id := range values {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func alignmentWireOwnerParts(comps []layoutComp, w schGroupWire, group map[string]bool) []layoutComp {
	var out []layoutComp
	for _, c := range comps {
		if group[c.Designator] || (c.ComponentType != "" && c.ComponentType != schLayoutPartType) {
			continue
		}
		for _, pin := range c.Pins {
			if wireTouchesPoint(w, pin.X, pin.Y) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

func alignmentWireNetMap(comps []layoutComp, wires []schGroupWire) map[string]string {
	out := map[string]string{}
	for _, w := range wires {
		nets := map[string]bool{}
		if strings.TrimSpace(w.Net) != "" {
			nets[strings.TrimSpace(w.Net)] = true
		}
		for _, c := range comps {
			if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
				continue
			}
			for _, pin := range c.Pins {
				if strings.TrimSpace(pin.Net) != "" && wireTouchesPoint(w, pin.X, pin.Y) {
					nets[strings.TrimSpace(pin.Net)] = true
				}
			}
		}
		if len(nets) == 1 {
			for net := range nets {
				out[w.ID] = net
			}
		}
	}
	return out
}

func alignmentSegmentsConflict(a, b [4]float64) bool {
	if segmentsCross2(
		[2]float64{a[0], a[1]}, [2]float64{a[2], a[3]},
		[2]float64{b[0], b[1]}, [2]float64{b[2], b[3]},
	) {
		return true
	}
	return alignmentSegmentsOverlap(a, b)
}

func alignmentSegmentsOverlap(a, b [4]float64) bool {
	ah := math.Abs(a[1]-a[3]) <= alignmentTolerance
	bh := math.Abs(b[1]-b[3]) <= alignmentTolerance
	av := math.Abs(a[0]-a[2]) <= alignmentTolerance
	bv := math.Abs(b[0]-b[2]) <= alignmentTolerance
	if ah && bh && math.Abs(a[1]-b[1]) <= alignmentTolerance {
		return math.Min(math.Max(a[0], a[2]), math.Max(b[0], b[2]))-
			math.Max(math.Min(a[0], a[2]), math.Min(b[0], b[2])) > alignmentTolerance
	}
	if av && bv && math.Abs(a[0]-b[0]) <= alignmentTolerance {
		return math.Min(math.Max(a[1], a[3]), math.Max(b[1], b[3]))-
			math.Max(math.Min(a[1], a[3]), math.Min(b[1], b[3])) > alignmentTolerance
	}
	return false
}

func alignmentPlanSteps(item alignmentPlanItem, candidate alignmentCandidate) []alignmentPlanStep {
	var steps []alignmentPlanStep
	order := 0
	add := func(kind, action string, designators, ids []string, dx, dy float64, required, executable bool, note string) {
		order++
		steps = append(steps, alignmentPlanStep{
			Order: order, Kind: kind, Action: action, Designators: designators,
			PrimitiveIds: ids, DX: dx, DY: dy, Required: required,
			ExecutableNow: executable, Note: note,
		})
	}
	if len(item.IndependentBlockerPlans) > 0 {
		for _, blocker := range item.IndependentBlockerPlans {
			ids := append([]string(nil), blocker.MoveComponentIds...)
			ids = append(ids, blocker.MoveWireIds...)
			ids = append(ids, blocker.MoveMarkerIds...)
			ids = append(ids, blocker.MoveLabelIds...)
			sort.Strings(ids)
			add("move-independent-blockers", "schematic.group.move", blocker.MoveDesignators, ids,
				blocker.DX, blocker.DY, true, blocker.Status == "ready",
				"独立阻塞组前置移动；之后必须重新读取主组和边界几何")
		}
	} else if len(item.IndependentBlockers) > 0 {
		add("move-independent-blockers", "", item.IndependentBlockers, nil, 0, 0, true, false,
			"先为这些独立块选择落点并整体移动；当前 planner 不猜测目标坐标")
	}
	moveIDs := append([]string(nil), item.MoveComponentIds...)
	moveIDs = append(moveIDs, item.MoveWireIds...)
	moveIDs = append(moveIDs, item.MoveMarkerIds...)
	moveIDs = append(moveIDs, item.MoveLabelIds...)
	sort.Strings(moveIDs)
	add("move-owned-group", "schematic.group.move", nil, moveIDs,
		candidateShiftX(candidate), candidateShiftY(candidate), true,
		item.Status == "ready", "专属导线和标志随组平移；导线标签由 group.move 重建")
	if len(item.BoundaryPlans) > 0 {
		order++
		steps = append(steps, alignmentPlanStep{
			Order: order, Kind: "delete-shared-boundary", Action: "schematic.primitives.delete",
			PrimitiveIds: append([]string(nil), item.SharedWireIds...), Required: true,
			ExecutableNow: false, Note: "先删除整根共享 wire primitive；重建必须覆盖固定脚和移动后的全部边界脚",
		})
		for _, bp := range item.BoundaryPlans {
			for _, wire := range bp.Wires {
				order++
				steps = append(steps, alignmentPlanStep{
					Order: order, Kind: "create-boundary-wire", Action: "schematic.wire.create",
					Net: wire.Net, Points: append([]float64(nil), wire.Points...),
					Required: true, ExecutableNow: false,
					Note: "边界重连草案；应用前仍需对最终移动几何重新验证",
				})
			}
		}
		if len(item.SharedLabelIds) > 0 {
			order++
			steps = append(steps, alignmentPlanStep{
				Order: order, Kind: "restore-shared-labels", PrimitiveIds: append([]string(nil), item.SharedLabelIds...),
				Required: true, ExecutableNow: false,
				Note: "共享线是删除重建；其可见网络名必须在重建后的线上恢复且重新检查重叠",
			})
		}
	} else if len(item.SharedWireIds) > 0 {
		add("rewire-shared-boundary", "", nil, item.SharedWireIds, 0, 0, true, false,
			"共享线触及固定器件，不能随组移动；必须按移动后的真实端点重新生成边界线")
	}
	add("verify-check", "sch check", nil, nil, 0, 0, true, item.Status == "ready",
		"要求没有新增 overlap/crossing/net/click failures")
	add("verify-bridge", "sch bridge-check", nil, nil, 0, 0, true, item.Status == "ready",
		"确认移动和边界重连没有断开或桥接")
	add("save-readback", "sch save", nil, nil, 0, 0, true, item.Status == "ready",
		"保存后重载并重新读取几何、连接和渲染")
	return steps
}

func candidateShiftX(c alignmentCandidate) float64 {
	if c.Axis == "x" {
		return c.Shift
	}
	return 0
}

func candidateShiftY(c alignmentCandidate) float64 {
	if c.Axis == "y" {
		return c.Shift
	}
	return 0
}

func renderAlignmentPlan(w io.Writer, report alignmentPlanReport) {
	fmt.Fprintf(w, "alignment-plan: %d plan(s) — %d ready, %d rewire-required, %d blocked\n",
		len(report.Plans), report.Ready, report.RewireRequired, report.Blocked)
	for _, p := range report.Plans {
		fmt.Fprintf(w, "  %-12s %-5s %-20s shift=(%g,%g)  %s\n",
			p.ID, p.Status, p.Net, p.ShiftX, p.ShiftY, stringsJoinNonEmpty(p.MoveDesignators))
		if len(p.Blockers) > 0 {
			fmt.Fprintf(w, "    blockers: %s\n", stringsJoinNonEmpty(p.Blockers))
		}
		if len(p.SharedWireIds) > 0 {
			fmt.Fprintf(w, "    shared boundary wires: %s\n", stringsJoinNonEmpty(p.SharedWireIds))
		}
		for _, blocker := range p.IndependentBlockerPlans {
			fmt.Fprintf(w, "    independent blocker: %-16s %-7s shift=(%g,%g) group=%s\n",
				stringsJoinNonEmpty(blocker.InitialDesignators), blocker.Status,
				blocker.DX, blocker.DY, stringsJoinNonEmpty(blocker.MoveDesignators))
		}
		for _, boundary := range p.BoundaryPlans {
			fmt.Fprintf(w, "    boundary: %-20s %-8s wires=%d labels=%d\n",
				boundary.Net, boundary.Status, len(boundary.Wires), len(boundary.LabelIds))
			if len(boundary.Blockers) > 0 {
				fmt.Fprintf(w, "      blockers: %s\n", stringsJoinNonEmpty(boundary.Blockers))
			}
		}
		for _, step := range p.Steps {
			state := "planned"
			if !step.ExecutableNow {
				state = "not-executable-yet"
			}
			fmt.Fprintf(w, "    %d. %-26s %s\n", step.Order, step.Kind, state)
		}
	}
}

func stringsJoinNonEmpty(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	out := values[0]
	for _, value := range values[1:] {
		out += "," + value
	}
	return out
}
