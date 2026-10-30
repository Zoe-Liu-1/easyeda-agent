package app

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	alignmentMinShift     = 10.0
	alignmentTolerance    = 0.5
	alignmentExpandRadius = 500.0
	alignmentMaxGroup     = 16
	alignmentMaxAnchored  = 4
)

type alignmentSegment struct {
	Index      int
	WireID     string
	Net        string
	X0, Y0     float64
	X1, Y1     float64
	Horizontal bool
	Vertical   bool
}

type alignmentCandidate struct {
	Axis                  string
	Net                   string
	Shift                 float64
	TargetCoord           float64
	WireID                string
	MoveDesignators       []string
	StationaryDesignators []string
	AllSegments           []alignmentSegment
	MoveSegments          map[int]bool
	MoveWireIDs           []string
	MoveMarkerIDs         []string
	MoveLabelIDs          []string
	SharedWireIDs         []string
	// AnchoredDesignators are members of the discovered functional island that
	// must stay in place. They are boundary anchors, not discarded members:
	// every wire that used to connect them to the moving subset becomes a
	// SharedWireIDs entry and is rebuilt by the boundary planner.
	AnchoredDesignators []string
	OriginalBBox        layoutBBox
	MovedBBox           layoutBBox
	At                  checkPoint
	Blockers            []string
	IndependentBlockers []string
}

func alignmentFinalizeCandidate(c alignmentCandidate, items []visibleTextItem, comps []layoutComp,
	wires []schGroupWire, sheet, titleBlock *layoutBBox, frames []schFrameSpec) (alignmentCandidate, bool) {
	expanded := alignmentExpandLocalGroup(comps, c.MoveDesignators, c.StationaryDesignators, c.Net, c.OriginalBBox)
	if len(expanded) == 0 {
		return alignmentCandidate{}, false
	}
	c.MoveDesignators = expanded
	c = alignmentFinalizeRigidSubset(c, items, comps, wires, sheet, titleBlock, frames)

	// A functional island is not automatically one indivisible rigid body.
	// Connector/header pins, test points and other boundary parts frequently make
	// one attachment immovable while the rest of the local circuit can still be
	// translated. When the rigid proposal is unsafe, keep those parts as anchors
	// and let the existing shared-boundary reconnect planner rebuild every net
	// that crosses the new boundary. This search is deterministic and bounded.
	if len(c.Blockers) == 0 || len(c.MoveDesignators) <= 1 {
		return c, true
	}
	rigid := c
	best := c
	for len(best.AnchoredDesignators) < alignmentMaxAnchored &&
		alignmentBlockersAllowAnchorDecomposition(best.Blockers) {
		var stepBest alignmentCandidate
		haveStep := false
		for _, designator := range best.MoveDesignators {
			if designator == "" {
				continue
			}
			trial := best
			trial.MoveDesignators = removeAlignmentDesignator(trial.MoveDesignators, designator)
			if len(trial.MoveDesignators) == 0 {
				continue
			}
			trial.StationaryDesignators = appendUniqueString(trial.StationaryDesignators, designator)
			trial.AnchoredDesignators = appendUniqueString(trial.AnchoredDesignators, designator)
			trial = alignmentFinalizeRigidSubset(trial, items, comps, wires, sheet, titleBlock, frames)
			if !haveStep || alignmentRepairCandidateBetter(trial, stepBest) {
				stepBest, haveStep = trial, true
			}
		}
		if !haveStep || len(stepBest.Blockers) >= len(best.Blockers) {
			break
		}
		best = stepBest
	}
	if len(best.Blockers) == 0 {
		return best, true
	}
	// A partial split that merely changes the blocker set is not a usable plan.
	// Keep the original rigid candidate so the report names the actual blocker
	// instead of presenting an arbitrary anchored subset as progress.
	return rigid, true
}

func alignmentFinalizeRigidSubset(c alignmentCandidate, items []visibleTextItem, comps []layoutComp,
	wires []schGroupWire, sheet, titleBlock *layoutBBox, frames []schFrameSpec) alignmentCandidate {
	c.Blockers = nil
	c.IndependentBlockers = nil
	bbox, ok := alignmentGroupBBox(comps, nil, nil, c.MoveDesignators)
	if !ok {
		c.Blockers = appendUniqueString(c.Blockers, "group-geometry-unavailable")
		return c
	}
	c.OriginalBBox = bbox
	if c.Axis == "y" {
		c.MovedBBox = translateLayoutBBox(bbox, [2]float64{0, c.Shift})
	} else {
		c.MovedBBox = translateLayoutBBox(bbox, [2]float64{c.Shift, 0})
	}
	c.MoveWireIDs, c.SharedWireIDs, c.MoveMarkerIDs, c.MoveLabelIDs =
		alignmentOwnedResources(comps, wires, items, c.MoveDesignators)
	exclude := append([]string(nil), c.MoveDesignators...)
	exclude = append(exclude, c.StationaryDesignators...)
	c.Blockers, c.IndependentBlockers = alignmentTargetBlockers(comps, c.MoveDesignators, exclude, c.MovedBBox)
	if len(c.MoveDesignators) > alignmentMaxGroup {
		c.Blockers = append(c.Blockers, fmt.Sprintf("group-too-large:%d", len(c.MoveDesignators)))
	}
	if titleBlock != nil && boxesOverlap2(c.MovedBBox, *titleBlock) {
		c.Blockers = append(c.Blockers, "title-block")
	}
	if sheet != nil && !boxInside2(c.MovedBBox, *sheet) {
		c.Blockers = append(c.Blockers, "sheet-boundary")
	}
	for _, blocker := range alignmentMoveBlockers(c, items, comps, wires, sheet, titleBlock, frames) {
		c.Blockers = appendUniqueString(c.Blockers, blocker)
	}
	if len(c.SharedWireIDs) > 0 {
		boundaryPlans, _ := alignmentBoundaryPlans(c, comps, items, wires, sheet, titleBlock, frames)
		for _, bp := range boundaryPlans {
			for _, blocker := range bp.Blockers {
				c.Blockers = appendUniqueString(c.Blockers, "boundary:"+blocker)
			}
			for _, blocker := range bp.IndependentBlockers {
				c.IndependentBlockers = appendUniqueString(c.IndependentBlockers, blocker)
			}
		}
	}
	sort.Strings(c.Blockers)
	sort.Strings(c.IndependentBlockers)
	return c
}

func removeAlignmentDesignator(values []string, drop string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != drop {
			out = append(out, value)
		}
	}
	return out
}

func alignmentRepairCandidateBetter(a, b alignmentCandidate) bool {
	if len(a.Blockers) != len(b.Blockers) {
		return len(a.Blockers) < len(b.Blockers)
	}
	if len(a.AnchoredDesignators) != len(b.AnchoredDesignators) {
		return len(a.AnchoredDesignators) < len(b.AnchoredDesignators)
	}
	if len(a.SharedWireIDs) != len(b.SharedWireIDs) {
		return len(a.SharedWireIDs) < len(b.SharedWireIDs)
	}
	if len(a.MoveDesignators) != len(b.MoveDesignators) {
		return len(a.MoveDesignators) > len(b.MoveDesignators)
	}
	return strings.Join(a.MoveDesignators, ",") < strings.Join(b.MoveDesignators, ",")
}

func alignmentBlockersAllowAnchorDecomposition(blockers []string) bool {
	for _, blocker := range blockers {
		if blocker == "group-move-geometry-collision" ||
			strings.HasPrefix(blocker, "frame:") ||
			strings.HasPrefix(blocker, "boundary:") {
			return true
		}
	}
	return false
}

func alignmentMoveBlockers(c alignmentCandidate, items []visibleTextItem, comps []layoutComp,
	wires []schGroupWire, sheet, titleBlock *layoutBBox, frames []schFrameSpec) []string {
	movedComp := map[string]bool{}
	movedParent := map[string]bool{}
	for _, designator := range c.MoveDesignators {
		for _, comp := range comps {
			if comp.Designator != designator {
				continue
			}
			if comp.ID != "" {
				movedComp[comp.ID] = true
				movedParent[comp.ID] = true
			}
			break
		}
	}
	for _, id := range c.MoveMarkerIDs {
		movedComp[id] = true
		movedParent[id] = true
	}
	movedWire := map[string]bool{}
	for _, id := range c.MoveWireIDs {
		movedWire[id] = true
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

	var blockers []string
	if sheet != nil && !groupMoveClear(
		[2]float64{candidateShiftX(c), candidateShiftY(c)},
		movedComp, movedWire, visibleTextIDList(movedText),
		alignmentCollisionTextItems(items, c), comps, alignmentCollisionWires(wires, c), sheet, titleBlock,
	) {
		blockers = append(blockers, "group-move-geometry-collision")
	}
	blockers = append(blockers, alignmentFrameBlockers(c, items, comps, wires, frames)...)
	sort.Strings(blockers)
	return blockers
}

// Labels whose parent shared wire is deleted and regenerated cannot be tested at
// their stale coordinates. This must happen before groupMoveClear sees them.
func alignmentCollisionTextItems(items []visibleTextItem, c alignmentCandidate) []visibleTextItem {
	shared := map[string]bool{}
	for _, id := range c.SharedWireIDs {
		shared[id] = true
	}
	out := make([]visibleTextItem, 0, len(items))
	for _, it := range items {
		if shared[it.ParentID] {
			continue
		}
		out = append(out, it)
	}
	return out
}

func alignmentCollisionWires(wires []schGroupWire, c alignmentCandidate) []schGroupWire {
	shared := map[string]bool{}
	for _, id := range c.SharedWireIDs {
		shared[id] = true
	}
	out := make([]schGroupWire, 0, len(wires))
	for _, wire := range wires {
		if shared[wire.ID] {
			continue
		}
		out = append(out, wire)
	}
	return out
}

func alignmentFrameBlockers(c alignmentCandidate, items []visibleTextItem, comps []layoutComp,
	wires []schGroupWire, frames []schFrameSpec) []string {
	if len(frames) == 0 {
		return nil
	}
	group := map[string]bool{}
	for _, designator := range c.MoveDesignators {
		group[designator] = true
	}
	movedWire := map[string]bool{}
	for _, id := range c.MoveWireIDs {
		movedWire[id] = true
	}
	movedMarker := map[string]bool{}
	for _, id := range c.MoveMarkerIDs {
		movedMarker[id] = true
	}
	movedParent := map[string]bool{}
	for i := range comps {
		if group[comps[i].Designator] && comps[i].ID != "" {
			movedParent[comps[i].ID] = true
		}
	}
	for _, id := range c.MoveWireIDs {
		movedParent[id] = true
	}
	for _, id := range c.MoveMarkerIDs {
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
	blocked := map[string]bool{}
	for i := range comps {
		comp := &comps[i]
		if !group[comp.Designator] || comp.BBox == nil {
			continue
		}
		frame := componentFrame(comp, frames)
		if frame == nil {
			continue
		}
		movedBox := translateLayoutBBox(*comp.BBox, [2]float64{candidateShiftX(c), candidateShiftY(c)})
		if !boxInside2(movedBox, frame.Rect) {
			blocked[fmt.Sprintf("frame:%s:body-outside:%s", frame.ID, comp.Designator)] = true
		}
		for _, pin := range comp.Pins {
			x, y := pin.X+candidateShiftX(c), pin.Y+candidateShiftY(c)
			if !pointInBox2(x, y, frame.Rect, alignmentTolerance) {
				blocked[fmt.Sprintf("frame:%s:pin-outside:%s.%s", frame.ID, comp.Designator, pin.Number)] = true
			}
		}
	}
	for _, w := range wires {
		if !movedWire[w.ID] {
			continue
		}
		frame := alignmentOwnerFrameForWire(comps, w, frames)
		if frame == nil {
			continue
		}
		for _, seg := range labelWireSegments(w) {
			if !segmentInsideFrame(
				seg[0]+candidateShiftX(c), seg[1]+candidateShiftY(c),
				seg[2]+candidateShiftX(c), seg[3]+candidateShiftY(c),
				frame.Rect, alignmentTolerance,
			) {
				blocked[fmt.Sprintf("frame:%s:wire-outside:%s", frame.ID, w.ID)] = true
				break
			}
		}
	}
	for i := range comps {
		marker := &comps[i]
		if !movedMarker[marker.ID] || marker.BBox == nil || !marker.AnchorAvailable {
			continue
		}
		for _, frame := range frames {
			if !pointInBox2(marker.X, marker.Y, frame.Rect, alignmentTolerance) {
				continue
			}
			movedBox := translateLayoutBBox(*marker.BBox, [2]float64{candidateShiftX(c), candidateShiftY(c)})
			if !boxInside2(movedBox, frame.Rect) {
				blocked[fmt.Sprintf("frame:%s:marker-outside:%s", frame.ID, marker.ID)] = true
			}
			break
		}
	}
	for _, it := range items {
		if !movedText[it.ID] || !schPositiveBBox(it.BBox) {
			continue
		}
		frame := alignmentOwnerFrameForTextItem(comps, wires, it, frames)
		if frame == nil {
			continue
		}
		movedBox := translateLayoutBBox(*it.BBox, [2]float64{candidateShiftX(c), candidateShiftY(c)})
		if !boxInside2(movedBox, frame.Rect) {
			label := it.Value
			if label == "" {
				label = it.Content
			}
			if label == "" {
				label = it.ID
			}
			blocked[fmt.Sprintf("frame:%s:text-outside:%s", frame.ID, label)] = true
		}
	}
	out := make([]string, 0, len(blocked))
	for item := range blocked {
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func alignmentOwnerFrameForWire(comps []layoutComp, wire schGroupWire, frames []schFrameSpec) *schFrameSpec {
	owners := map[string]schFrameSpec{}
	for i := range comps {
		comp := &comps[i]
		if comp.BBox == nil || (comp.ComponentType != "" && comp.ComponentType != schLayoutPartType) {
			continue
		}
		frame := componentFrame(comp, frames)
		if frame == nil {
			continue
		}
		for _, pin := range comp.Pins {
			if wireTouchesPoint(wire, pin.X, pin.Y) {
				owners[frame.ID] = *frame
				break
			}
		}
	}
	if len(owners) != 1 {
		return nil
	}
	for _, frame := range owners {
		copy := frame
		return &copy
	}
	return nil
}

func alignmentOwnerFrameForTextItem(comps []layoutComp, wires []schGroupWire,
	item visibleTextItem, frames []schFrameSpec) *schFrameSpec {
	switch item.ParentType {
	case "part":
		for i := range comps {
			if comps[i].ID == item.ParentID {
				return componentFrame(&comps[i], frames)
			}
		}
	case "wire":
		for _, wire := range wires {
			if wire.ID == item.ParentID {
				return alignmentOwnerFrameForWire(comps, wire, frames)
			}
		}
	case "netflag", "netport", "netlabel":
		for _, comp := range comps {
			if comp.ID != item.ParentID || !comp.AnchorAvailable {
				continue
			}
			for _, frame := range frames {
				if pointInBox2(comp.X, comp.Y, frame.Rect, alignmentTolerance) {
					copy := frame
					return &copy
				}
			}
		}
	}
	return nil
}

func alignmentOpportunityFindings(items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox) []checkFinding {
	return alignmentOpportunityFindingsWithFrames(items, comps, wires, sheet, titleBlock, nil)
}

func alignmentOpportunityFindingsWithFrames(items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox, frames []schFrameSpec) []checkFinding {
	candidates := alignmentOpportunityCandidatesWithFrames(items, comps, wires, sheet, titleBlock, frames)
	findings := make([]checkFinding, 0, len(candidates))
	for _, candidate := range candidates {
		findings = append(findings, alignmentFinding(candidate))
	}
	return findings
}

func alignmentOpportunityCandidates(items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox) []alignmentCandidate {
	return alignmentOpportunityCandidatesWithFrames(items, comps, wires, sheet, titleBlock, nil)
}

func alignmentOpportunityCandidatesWithFrames(items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox, frames []schFrameSpec) []alignmentCandidate {
	best := map[string]alignmentCandidate{}
	segs := alignmentSegments(wires)
	if len(segs) < 3 {
		return nil
	}
	adj := alignmentSegmentAdjacency(segs)
	find, wireIndex := wireTreeFinder(wires)
	chipIslands := wireIslandActiveChips(comps, wires)
	for connectorIndex, connector := range segs {
		if wi, ok := wireIndex[connector.WireID]; ok && len(chipIslands[find(wi)]) > 1 {
			continue
		}
		// A wire's storage orientation says nothing about which side is
		// offset. Evaluate both translations for every trunk so a V-H-V with
		// differently-long end leads is not silently reduced to an X-only
		// candidate (or missed when the host stores the trunk right-to-left).
		for _, axis := range []string{"x", "y"} {
			for _, candidate := range alignmentCandidatesForConnector(
				comps, segs, adj, connectorIndex, axis,
			) {
				candidate, ok := alignmentFinalizeCandidate(candidate, items, comps, wires, sheet, titleBlock, frames)
				if !ok {
					continue
				}
				if candidate.Axis != alignmentPreferredMovementAxis(comps, candidate) {
					continue
				}
				candidate.WireID = connector.WireID
				key := alignmentOpportunityKey(candidate)
				if prior, ok := best[key]; !ok || alignmentCandidateBetter(candidate, prior) {
					best[key] = candidate
				}
			}
		}
	}
	out := make([]alignmentCandidate, 0, len(best))
	for _, candidate := range best {
		out = append(out, candidate)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Net != out[j].Net {
			return out[i].Net < out[j].Net
		}
		return strings.Join(out[i].MoveDesignators, ",") < strings.Join(out[j].MoveDesignators, ",")
	})
	return out
}

func alignmentFinding(c alignmentCandidate) checkFinding {
	blocked := ""
	if len(c.Blockers) > 0 {
		blocked = "；当前有阻塞项 " + strings.Join(c.Blockers, ",") + "，需先作为独立组移开或改写方案"
		if len(c.IndependentBlockers) > 0 {
			blocked += "（可独立前置移动：" + strings.Join(c.IndependentBlockers, ",") + "）"
		}
	}
	anchored := ""
	if len(c.AnchoredDesignators) > 0 {
		anchored = "；保留 " + strings.Join(c.AnchoredDesignators, "/") +
			" 作为边界锚点并重建共享线"
	}
	return checkFinding{
		Type: "alignment-opportunity", Level: "info", MarkerNet: c.Net,
		WirePrimitiveId: c.WireID, At: &c.At, BBox: &c.MovedBBox,
		AlignmentAxis: c.Axis, ShiftX: func() float64 {
			if c.Axis == "x" {
				return c.Shift
			}
			return 0
		}(),
		ShiftY: func() float64 {
			if c.Axis == "y" {
				return c.Shift
			}
			return 0
		}(),
		BendReduction: 2, MoveDesignators: append([]string(nil), c.MoveDesignators...),
		MoveWireIds: append([]string(nil), c.MoveWireIDs...), MoveMarkerIds: append([]string(nil), c.MoveMarkerIDs...),
		MoveLabelIds: append([]string(nil), c.MoveLabelIDs...), SharedWireIds: append([]string(nil), c.SharedWireIDs...),
		AnchorDesignators: append([]string(nil), c.AnchoredDesignators...),
		Blockers:          append([]string(nil), c.Blockers...), IndependentBlockers: append([]string(nil), c.IndependentBlockers...),
		Message: fmt.Sprintf("主走线 %s 存在可直线化的偏轴折返；候选将 %s 沿 %s 平移 %.0f，把两段同向主干合为一条直线并减少 2 个拐角%s%s",
			c.Net, strings.Join(c.MoveDesignators, "/"), axisLabel(c.Axis), c.Shift, anchored, blocked),
	}
}

func alignmentPreferredMovementAxis(comps []layoutComp, c alignmentCandidate) string {
	coreX, coreY, ok := alignmentCoreCenter(comps, c.StationaryDesignators)
	if !ok {
		return ""
	}
	moveX := (c.OriginalBBox.MinX + c.OriginalBBox.MaxX) / 2
	moveY := (c.OriginalBBox.MinY + c.OriginalBBox.MaxY) / 2
	if math.Abs(moveX-coreX) >= math.Abs(moveY-coreY) {
		return "y"
	}
	return "x"
}

func alignmentCoreCenter(comps []layoutComp, designators []string) (float64, float64, bool) {
	seen := map[string]bool{}
	for _, d := range designators {
		seen[d] = true
	}
	maxPins := 0
	for _, c := range comps {
		if seen[c.Designator] && (c.ComponentType == "" || c.ComponentType == schLayoutPartType) && len(c.Pins) > maxPins {
			maxPins = len(c.Pins)
		}
	}
	if maxPins < 4 {
		return 0, 0, false
	}
	var x, y float64
	n := 0
	for _, c := range comps {
		if !seen[c.Designator] || c.BBox == nil ||
			(c.ComponentType != "" && c.ComponentType != schLayoutPartType) || len(c.Pins) != maxPins {
			continue
		}
		x += (c.BBox.MinX + c.BBox.MaxX) / 2
		y += (c.BBox.MinY + c.BBox.MaxY) / 2
		n++
	}
	if n == 0 {
		return 0, 0, false
	}
	return x / float64(n), y / float64(n), true
}

func alignmentCandidateBetter(a, b alignmentCandidate) bool {
	if len(a.Blockers) != len(b.Blockers) {
		return len(a.Blockers) < len(b.Blockers)
	}
	if len(a.AnchoredDesignators) != len(b.AnchoredDesignators) {
		return len(a.AnchoredDesignators) < len(b.AnchoredDesignators)
	}
	if len(a.MoveDesignators) != len(b.MoveDesignators) {
		return len(a.MoveDesignators) > len(b.MoveDesignators)
	}
	return math.Abs(a.Shift) < math.Abs(b.Shift)
}

func alignmentOpportunityKey(c alignmentCandidate) string {
	// A physical trunk can be observed through several flat segments or wire
	// primitives. Keep the best proposal per net/axis, not one plan per segment;
	// otherwise the same rigid move appears twice with different shift targets.
	return c.Net + "|" + c.Axis
}

func axisLabel(axis string) string {
	if axis == "x" {
		return "X"
	}
	return "Y"
}

func alignmentCandidatesForConnector(comps []layoutComp, segs []alignmentSegment,
	adj [][]int, connectorIndex int, axis string) []alignmentCandidate {
	connector := segs[connectorIndex]
	ends := [2]checkPoint{{X: connector.X0, Y: connector.Y0}, {X: connector.X1, Y: connector.Y1}}
	armAxis := "horizontal"
	if connector.Horizontal {
		armAxis = "vertical"
	}
	arms := [2][]int{}
	for endIndex, point := range ends {
		for _, ni := range adj[connectorIndex] {
			a := segs[ni]
			if armAxis == "horizontal" && !a.Horizontal {
				continue
			}
			if armAxis == "vertical" && !a.Vertical {
				continue
			}
			if !alignmentSegmentTouchesPoint(a, point.X, point.Y) {
				continue
			}
			arms[endIndex] = append(arms[endIndex], ni)
		}
	}
	var out []alignmentCandidate
	for _, first := range arms[0] {
		for _, second := range arms[1] {
			if first == second {
				continue
			}
			// The connector endpoint is what touches this arm, while the far
			// endpoint carries the actual offset to be removed. Raw wire
			// segments are not orientation-normalized, so endpoint 0 may be
			// either the inner or outer end. Always select the endpoint opposite
			// the shared connector before comparing arm coordinates.
			p0 := alignmentSegmentOuterPoint(segs[first], ends[0])
			p1 := alignmentSegmentOuterPoint(segs[second], ends[1])
			if axis == "y" && math.Abs(p0.Y-p1.Y) < alignmentMinShift {
				continue
			}
			if axis == "x" && math.Abs(p0.X-p1.X) < alignmentMinShift {
				continue
			}
			if alignmentPinOrMarkerAt(comps, ends[0], alignmentTolerance) ||
				alignmentPinOrMarkerAt(comps, ends[1], alignmentTolerance) {
				continue
			}
			coordA, coordB := p0.X, p1.X
			if axis == "y" {
				coordA, coordB = p0.Y, p1.Y
			}
			sideA := alignmentCollectSide(adj, first, connectorIndex)
			sideB := alignmentCollectSide(adj, second, connectorIndex)
			if len(sideA) == 0 || len(sideB) == 0 || alignmentSidesOverlap(sideA, sideB) {
				continue
			}
			partsA := alignmentPartsOnSegments(comps, segs, sideA)
			partsB := alignmentPartsOnSegments(comps, segs, sideB)
			if len(partsA) == 0 || len(partsB) == 0 {
				continue
			}
			var move, stationary []string
			var moveSegments map[int]bool
			var allSegments = map[int]bool{}
			for idx := range sideA {
				allSegments[idx] = true
			}
			for idx := range sideB {
				allSegments[idx] = true
			}
			aPins := alignmentMaxPinCount(comps, partsA)
			bPins := alignmentMaxPinCount(comps, partsB)
			aCore, bCore := aPins >= 4, bPins >= 4
			var shift, targetCoord float64
			switch {
			case aCore && !bCore && len(partsB) >= 1 && len(partsA) <= 3:
				move, stationary = partsB, partsA
				moveSegments = sideB
				targetCoord = coordA
				shift = coordA - coordB
			case bCore && !aCore && len(partsA) >= 1 && len(partsB) <= 3:
				move, stationary = partsA, partsB
				moveSegments = sideA
				targetCoord = coordB
				shift = coordB - coordA
			case aCore && bCore && aPins > bPins && len(partsA) < len(partsB):
				move, stationary = partsB, partsA
				moveSegments = sideB
				targetCoord = coordA
				shift = coordA - coordB
			case aCore && bCore && bPins > aPins && len(partsB) < len(partsA):
				move, stationary = partsA, partsB
				moveSegments = sideA
				targetCoord = coordB
				shift = coordB - coordA
			default:
				continue
			}
			net := alignmentCommonNet(comps, segs, allSegments)
			if net == "" {
				continue
			}
			if len(move) == 1 {
				if isPowerClusterGround(net) {
					continue
				}
				if isRailLabelNet(net) {
					if !alignmentSideHasMarker(comps, segs, moveSegments) {
						continue
					}
				} else if !alignmentSinglePartMoveEligible(comps, move[0]) {
					continue
				}
			}
			bbox, ok := alignmentGroupBBox(comps, segs, moveSegments, move)
			if !ok {
				continue
			}
			moved := bbox
			if axis == "y" {
				moved = translateLayoutBBox(moved, [2]float64{0, shift})
			} else {
				moved = translateLayoutBBox(moved, [2]float64{shift, 0})
			}
			out = append(out, alignmentCandidate{
				Axis: axis, Net: net, Shift: shift, TargetCoord: targetCoord,
				MoveDesignators: move, StationaryDesignators: stationary,
				AllSegments: segs, MoveSegments: moveSegments, OriginalBBox: bbox, MovedBBox: moved,
				At: checkPoint{X: round2((ends[0].X + ends[1].X) / 2), Y: round2((ends[0].Y + ends[1].Y) / 2)},
			})
		}
	}
	return out
}

func alignmentSegments(wires []schGroupWire) []alignmentSegment {
	var out []alignmentSegment
	for _, w := range wires {
		raw := labelWireSegments(w)
		for _, s := range raw {
			horizontal := math.Abs(s[1]-s[3]) <= alignmentTolerance
			vertical := math.Abs(s[0]-s[2]) <= alignmentTolerance
			if horizontal == vertical {
				continue
			}
			out = append(out, alignmentSegment{
				Index: len(out), WireID: w.ID, Net: w.Net,
				X0: s[0], Y0: s[1], X1: s[2], Y1: s[3],
				Horizontal: horizontal, Vertical: vertical,
			})
		}
	}
	return out
}

func alignmentSegmentAdjacency(segs []alignmentSegment) [][]int {
	adj := make([][]int, len(segs))
	for i := range segs {
		for j := i + 1; j < len(segs); j++ {
			if alignmentSegmentsTouch(segs[i], segs[j]) {
				adj[i] = append(adj[i], j)
				adj[j] = append(adj[j], i)
			}
		}
	}
	return adj
}

func alignmentSegmentsTouch(a, b alignmentSegment) bool {
	for _, p := range [][2]float64{{a.X0, a.Y0}, {a.X1, a.Y1}} {
		if pointOnSegment(p[0], p[1], b.X0, b.Y0, b.X1, b.Y1, alignmentTolerance) {
			return true
		}
	}
	for _, p := range [][2]float64{{b.X0, b.Y0}, {b.X1, b.Y1}} {
		if pointOnSegment(p[0], p[1], a.X0, a.Y0, a.X1, a.Y1, alignmentTolerance) {
			return true
		}
	}
	return false
}

func alignmentSegmentTouchesPoint(s alignmentSegment, x, y float64) bool {
	return pointOnSegment(x, y, s.X0, s.Y0, s.X1, s.Y1, alignmentTolerance)
}

func alignmentSegmentPoint(s alignmentSegment, end int) checkPoint {
	if end == 0 {
		return checkPoint{X: s.X0, Y: s.Y0}
	}
	return checkPoint{X: s.X1, Y: s.Y1}
}

func alignmentSegmentOuterPoint(s alignmentSegment, connector checkPoint) checkPoint {
	if pointsClose(s.X0, s.Y0, connector.X, connector.Y, alignmentTolerance) {
		return checkPoint{X: s.X1, Y: s.Y1}
	}
	if pointsClose(s.X1, s.Y1, connector.X, connector.Y, alignmentTolerance) {
		return checkPoint{X: s.X0, Y: s.Y0}
	}
	// Callers only pass adjacent segments, so this is an invariant failure
	// fallback rather than the normal path. Use the row's second endpoint to
	// keep the failure deterministic if malformed geometry slips through.
	return alignmentSegmentPoint(s, 1)
}

func alignmentCollectSide(adj [][]int, start, blocked int) map[int]bool {
	out := map[int]bool{}
	stack := []int{start}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == blocked || out[cur] {
			continue
		}
		out[cur] = true
		for _, next := range adj[cur] {
			if next != blocked && !out[next] {
				stack = append(stack, next)
			}
		}
	}
	return out
}

func alignmentSidesOverlap(a, b map[int]bool) bool {
	for idx := range a {
		if b[idx] {
			return true
		}
	}
	return false
}

func alignmentPartsOnSegments(comps []layoutComp, segs []alignmentSegment, indexes map[int]bool) []string {
	seen := map[string]bool{}
	for _, c := range comps {
		if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
			continue
		}
		for _, p := range c.Pins {
			for idx := range indexes {
				if alignmentSegmentTouchesPoint(segs[idx], p.X, p.Y) {
					seen[c.Designator] = true
					break
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func alignmentMaxPinCount(comps []layoutComp, designators []string) int {
	seen := map[string]bool{}
	for _, d := range designators {
		seen[d] = true
	}
	maxPins := 0
	for _, c := range comps {
		if !seen[c.Designator] || (c.ComponentType != "" && c.ComponentType != schLayoutPartType) {
			continue
		}
		if len(c.Pins) > maxPins {
			maxPins = len(c.Pins)
		}
	}
	return maxPins
}

func alignmentCommonNet(comps []layoutComp, segs []alignmentSegment, indexes map[int]bool) string {
	nets := map[string]bool{}
	for idx := range indexes {
		if net := strings.TrimSpace(segs[idx].Net); net != "" {
			nets[net] = true
		}
	}
	for _, c := range comps {
		if isSchMarker(c.ComponentType) && c.Net != "" && c.AnchorAvailable {
			for idx := range indexes {
				if alignmentSegmentTouchesPoint(segs[idx], c.X, c.Y) {
					nets[strings.TrimSpace(c.Net)] = true
					break
				}
			}
			continue
		}
		if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
			continue
		}
		for _, pin := range c.Pins {
			if strings.TrimSpace(pin.Net) == "" {
				continue
			}
			for idx := range indexes {
				if alignmentSegmentTouchesPoint(segs[idx], pin.X, pin.Y) {
					nets[strings.TrimSpace(pin.Net)] = true
					break
				}
			}
		}
	}
	if len(nets) != 1 {
		return ""
	}
	for net := range nets {
		return net
	}
	return ""
}

func alignmentPinOrMarkerAt(comps []layoutComp, p checkPoint, eps float64) bool {
	for _, c := range comps {
		if isSchMarker(c.ComponentType) && c.AnchorAvailable && pointsClose(c.X, c.Y, p.X, p.Y, eps) {
			return true
		}
		for _, pin := range c.Pins {
			if pointsClose(pin.X, pin.Y, p.X, p.Y, eps) {
				return true
			}
		}
	}
	return false
}

func alignmentSideHasMarker(comps []layoutComp, segs []alignmentSegment, indexes map[int]bool) bool {
	for _, c := range comps {
		if !isSchMarker(c.ComponentType) || !c.AnchorAvailable || c.Net == "" {
			continue
		}
		for idx := range indexes {
			if alignmentSegmentTouchesPoint(segs[idx], c.X, c.Y) {
				return true
			}
		}
	}
	return false
}

func alignmentSinglePartMoveEligible(comps []layoutComp, designator string) bool {
	for _, comp := range comps {
		if comp.Designator != designator || alignmentActivePart(comp) {
			continue
		}
		// A single two-terminal passive can be moved on a signal net when the
		// trunk itself supplies the connectivity evidence. Larger or active
		// bodies still require a complete local functional group.
		return len(comp.Pins) == 2
	}
	return false
}

func alignmentExpandLocalGroup(comps []layoutComp, seed, stationary []string, trunkNet string, seedBBox layoutBBox) []string {
	group := map[string]bool{}
	for _, d := range seed {
		if d != "" {
			group[d] = true
		}
	}
	if len(group) == 0 {
		return nil
	}
	excluded := map[string]bool{}
	for _, d := range stationary {
		excluded[d] = true
	}
	activeNets := map[string]bool{}
	addNets := func(c layoutComp) {
		for _, pin := range c.Pins {
			net := strings.TrimSpace(pin.Net)
			if !alignmentNetEligible(net, trunkNet) {
				continue
			}
			activeNets[net] = true
		}
	}
	for _, c := range comps {
		if group[c.Designator] {
			addNets(c)
		}
	}
	area := inflateLayoutBBox(seedBBox, alignmentExpandRadius)
	for pass := 0; pass < alignmentMaxGroup; pass++ {
		changed := false
		for _, c := range comps {
			if c.Designator == "" || group[c.Designator] || excluded[c.Designator] || c.BBox == nil ||
				(c.ComponentType != "" && c.ComponentType != schLayoutPartType) || alignmentActivePart(c) {
				continue
			}
			cx, cy := (c.BBox.MinX+c.BBox.MaxX)/2, (c.BBox.MinY+c.BBox.MaxY)/2
			if !pointInBox2(cx, cy, area, 0) {
				continue
			}
			linked := false
			for _, pin := range c.Pins {
				if activeNets[strings.TrimSpace(pin.Net)] {
					linked = true
					break
				}
			}
			if !linked {
				continue
			}
			group[c.Designator] = true
			addNets(c)
			changed = true
			if len(group) >= alignmentMaxGroup {
				break
			}
		}
		if !changed || len(group) >= alignmentMaxGroup {
			break
		}
	}
	out := make([]string, 0, len(group))
	for d := range group {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func alignmentNetEligible(net, trunkNet string) bool {
	if strings.TrimSpace(net) == "" || net == trunkNet || isPowerClusterGround(net) || isPowerNetName(net) {
		return false
	}
	return true
}

func alignmentActivePart(c layoutComp) bool {
	role := strings.ToUpper(strings.TrimSpace(c.Designator))
	return len(c.Pins) >= 4 &&
		(strings.HasPrefix(role, "U") || strings.HasPrefix(role, "Q") || strings.HasPrefix(role, "IC"))
}

func alignmentOwnedResources(comps []layoutComp, wires []schGroupWire, items []visibleTextItem,
	group []string) (moveWires, sharedWires, moveMarkers, moveLabels []string) {
	groupSet := map[string]bool{}
	for _, d := range group {
		groupSet[d] = true
	}
	moveSet := map[string]bool{}
	sharedSet := map[string]bool{}
	for _, w := range wires {
		touchesGroup, touchesOutside := false, false
		for _, c := range comps {
			if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
				continue
			}
			for _, pin := range c.Pins {
				if !wireTouchesPoint(w, pin.X, pin.Y) {
					continue
				}
				if groupSet[c.Designator] {
					touchesGroup = true
				} else {
					touchesOutside = true
				}
				break
			}
		}
		if !touchesGroup {
			continue
		}
		if touchesOutside {
			sharedSet[w.ID] = true
		} else {
			moveSet[w.ID] = true
		}
	}
	markerSet := map[string]bool{}
	for _, c := range comps {
		if !isSchMarker(c.ComponentType) || !c.AnchorAvailable {
			continue
		}
		for _, w := range wires {
			if moveSet[w.ID] && wireTouchesPoint(w, c.X, c.Y) {
				markerSet[c.ID] = true
				break
			}
		}
	}
	labelSet := map[string]bool{}
	for _, it := range items {
		if it.ID == "" || it.ParentID == "" {
			continue
		}
		if moveSet[it.ParentID] || markerSet[it.ParentID] {
			labelSet[it.ID] = true
		}
	}
	for id := range moveSet {
		moveWires = append(moveWires, id)
	}
	for id := range sharedSet {
		sharedWires = append(sharedWires, id)
	}
	for id := range markerSet {
		moveMarkers = append(moveMarkers, id)
	}
	for id := range labelSet {
		moveLabels = append(moveLabels, id)
	}
	sort.Strings(moveWires)
	sort.Strings(sharedWires)
	sort.Strings(moveMarkers)
	sort.Strings(moveLabels)
	return
}

func alignmentTargetBlockers(comps []layoutComp, group, exclude []string, target layoutBBox) (blockers, independent []string) {
	excluded := map[string]bool{}
	for _, d := range exclude {
		excluded[d] = true
	}
	groupNets := alignmentLocalNetSet(comps, group)
	for _, c := range comps {
		if c.BBox == nil || excluded[c.Designator] || c.ComponentType == "sheet" ||
			(c.ComponentType != "" && c.ComponentType != schLayoutPartType) {
			continue
		}
		// A rigid group move commits its final geometry atomically. The old
		// swept-from/to rectangle made a nearby part look mandatory even when
		// the final bodies and rebuilt wires were clear, forcing blockers to
		// drift farther on every replan.
		if boxesOverlap2(*c.BBox, target) {
			label := c.Designator
			if label == "" {
				label = c.ID
			}
			if label != "" {
				blockers = append(blockers, label)
				blockerNets := alignmentLocalNetSet(comps, []string{c.Designator})
				shared := false
				for net := range blockerNets {
					if groupNets[net] {
						shared = true
						break
					}
				}
				if !shared {
					independent = append(independent, label)
				}
			}
		}
	}
	sort.Strings(blockers)
	sort.Strings(independent)
	return blockers, independent
}

func alignmentLocalNetSet(comps []layoutComp, designators []string) map[string]bool {
	seen := map[string]bool{}
	for _, d := range designators {
		seen[d] = true
	}
	nets := map[string]bool{}
	for _, c := range comps {
		if !seen[c.Designator] || (c.ComponentType != "" && c.ComponentType != schLayoutPartType) {
			continue
		}
		for _, pin := range c.Pins {
			net := strings.TrimSpace(pin.Net)
			if net != "" && !isPowerClusterGround(net) && !isPowerNetName(net) {
				nets[net] = true
			}
		}
	}
	return nets
}

func alignmentGroupBBox(comps []layoutComp, segs []alignmentSegment, moveSegments map[int]bool, move []string) (layoutBBox, bool) {
	moveSet := map[string]bool{}
	for _, d := range move {
		moveSet[d] = true
	}
	var box layoutBBox
	have := false
	add := func(b layoutBBox) {
		if !have {
			box, have = b, true
			return
		}
		box.MinX, box.MinY = math.Min(box.MinX, b.MinX), math.Min(box.MinY, b.MinY)
		box.MaxX, box.MaxY = math.Max(box.MaxX, b.MaxX), math.Max(box.MaxY, b.MaxY)
	}
	for _, c := range comps {
		if !moveSet[c.Designator] {
			continue
		}
		if c.BBox == nil {
			return layoutBBox{}, false
		}
		add(*c.BBox)
	}
	for idx := range moveSegments {
		s := segs[idx]
		add(layoutBBox{MinX: math.Min(s.X0, s.X1), MinY: math.Min(s.Y0, s.Y1), MaxX: math.Max(s.X0, s.X1), MaxY: math.Max(s.Y0, s.Y1)})
	}
	return box, have
}
