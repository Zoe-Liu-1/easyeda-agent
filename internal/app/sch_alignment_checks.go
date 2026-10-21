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
	WireID                string
	MoveDesignators       []string
	StationaryDesignators []string
	AllSegments           []alignmentSegment
	MoveSegments          map[int]bool
	MoveWireIDs           []string
	MoveMarkerIDs         []string
	MoveLabelIDs          []string
	SharedWireIDs         []string
	OriginalBBox          layoutBBox
	MovedBBox             layoutBBox
	At                    checkPoint
	Blockers              []string
	IndependentBlockers   []string
}

func alignmentFinalizeCandidate(c alignmentCandidate, items []visibleTextItem, comps []layoutComp,
	wires []schGroupWire, sheet, titleBlock *layoutBBox) (alignmentCandidate, bool) {
	expanded := alignmentExpandLocalGroup(comps, c.MoveDesignators, c.StationaryDesignators, c.Net, c.OriginalBBox)
	if len(expanded) == 0 {
		return alignmentCandidate{}, false
	}
	c.MoveDesignators = expanded
	bbox, ok := alignmentGroupBBox(comps, c.AllSegments, c.MoveSegments, expanded)
	if !ok {
		return alignmentCandidate{}, false
	}
	c.OriginalBBox = bbox
	if c.Axis == "y" {
		c.MovedBBox = translateLayoutBBox(bbox, [2]float64{0, c.Shift})
	} else {
		c.MovedBBox = translateLayoutBBox(bbox, [2]float64{c.Shift, 0})
	}
	c.MoveWireIDs, c.SharedWireIDs, c.MoveMarkerIDs, c.MoveLabelIDs =
		alignmentOwnedResources(comps, wires, items, expanded)
	exclude := append([]string(nil), expanded...)
	exclude = append(exclude, c.StationaryDesignators...)
	c.Blockers, c.IndependentBlockers = alignmentSweepBlockers(comps, expanded, exclude, c.OriginalBBox, c.MovedBBox)
	if len(expanded) > alignmentMaxGroup {
		c.Blockers = append(c.Blockers, fmt.Sprintf("group-too-large:%d", len(expanded)))
	}
	if titleBlock != nil && boxesOverlap2(c.MovedBBox, *titleBlock) {
		c.Blockers = append(c.Blockers, "title-block")
	}
	if sheet != nil && !boxInside2(c.MovedBBox, *sheet) {
		c.Blockers = append(c.Blockers, "sheet-boundary")
	}
	return c, true
}

func alignmentOpportunityFindings(items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox) []checkFinding {
	best := map[string]alignmentCandidate{}
	segs := alignmentSegments(wires)
	if len(segs) < 3 {
		return nil
	}
	adj := alignmentSegmentAdjacency(segs)
	for connectorIndex, connector := range segs {
		if connector.Horizontal {
			for _, candidate := range alignmentCandidatesForConnector(
				comps, segs, adj, connectorIndex, "x",
			) {
				candidate, ok := alignmentFinalizeCandidate(candidate, items, comps, wires, sheet, titleBlock)
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
		if connector.Vertical {
			for _, candidate := range alignmentCandidatesForConnector(
				comps, segs, adj, connectorIndex, "y",
			) {
				candidate, ok := alignmentFinalizeCandidate(candidate, items, comps, wires, sheet, titleBlock)
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
	findings := make([]checkFinding, 0, len(best))
	for _, candidate := range best {
		findings = append(findings, alignmentFinding(candidate))
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].MarkerNet != findings[j].MarkerNet {
			return findings[i].MarkerNet < findings[j].MarkerNet
		}
		return findings[i].Message < findings[j].Message
	})
	return findings
}

func alignmentFinding(c alignmentCandidate) checkFinding {
	blocked := ""
	if len(c.Blockers) > 0 {
		blocked = "；当前有阻塞项 " + strings.Join(c.Blockers, ",") + "，需先作为独立组移开或改写方案"
		if len(c.IndependentBlockers) > 0 {
			blocked += "（可独立前置移动：" + strings.Join(c.IndependentBlockers, ",") + "）"
		}
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
		Blockers: append([]string(nil), c.Blockers...), IndependentBlockers: append([]string(nil), c.IndependentBlockers...),
		Message: fmt.Sprintf("主走线 %s 存在可直线化的偏轴折返；候选将 %s 沿 %s 平移 %.0f，把两段同向主干合为一条直线并减少 2 个拐角%s",
			c.Net, strings.Join(c.MoveDesignators, "/"), axisLabel(c.Axis), c.Shift, blocked),
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
	if len(a.MoveDesignators) != len(b.MoveDesignators) {
		return len(a.MoveDesignators) > len(b.MoveDesignators)
	}
	return math.Abs(a.Shift) < math.Abs(b.Shift)
}

func alignmentOpportunityKey(c alignmentCandidate) string {
	return c.Net + "|" + c.Axis + "|" + strings.Join(c.MoveDesignators, ",")
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
	if axis == "x" {
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
			p0 := alignmentSegmentPoint(segs[first], 0)
			p1 := alignmentSegmentPoint(segs[second], 0)
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
			var shift float64
			switch {
			case aCore && !bCore && len(partsB) >= 1 && len(partsA) <= 3:
				move, stationary = partsB, partsA
				moveSegments = sideB
				shift = targetArmCoordinate(segs, first) - targetArmCoordinate(segs, second)
			case bCore && !aCore && len(partsA) >= 1 && len(partsB) <= 3:
				move, stationary = partsA, partsB
				moveSegments = sideA
				shift = targetArmCoordinate(segs, second) - targetArmCoordinate(segs, first)
			case aCore && bCore && aPins > bPins && len(partsA) < len(partsB):
				move, stationary = partsB, partsA
				moveSegments = sideB
				shift = targetArmCoordinate(segs, first) - targetArmCoordinate(segs, second)
			case aCore && bCore && bPins > aPins && len(partsB) < len(partsA):
				move, stationary = partsA, partsB
				moveSegments = sideA
				shift = targetArmCoordinate(segs, second) - targetArmCoordinate(segs, first)
			default:
				continue
			}
			net := alignmentCommonNet(comps, segs, allSegments)
			if net == "" {
				continue
			}
			if len(move) == 1 && (!isRailLabelNet(net) || isPowerClusterGround(net) ||
				!alignmentSideHasMarker(comps, segs, moveSegments)) {
				continue
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
				Axis: axis, Net: net, Shift: shift, MoveDesignators: move, StationaryDesignators: stationary,
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

func targetArmCoordinate(segs []alignmentSegment, index int) float64 {
	a, b := segs[index], segs[index]
	if a.Horizontal {
		return (b.Y0 + b.Y1) / 2
	}
	return (a.X0 + a.X1) / 2
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

func alignmentSweepBlockers(comps []layoutComp, group, exclude []string, from, to layoutBBox) (blockers, independent []string) {
	excluded := map[string]bool{}
	for _, d := range exclude {
		excluded[d] = true
	}
	swept := from
	swept.MinX, swept.MinY = math.Min(from.MinX, to.MinX), math.Min(from.MinY, to.MinY)
	swept.MaxX, swept.MaxY = math.Max(from.MaxX, to.MaxX), math.Max(from.MaxY, to.MaxY)
	groupNets := alignmentLocalNetSet(comps, group)
	for _, c := range comps {
		if c.BBox == nil || excluded[c.Designator] || c.ComponentType == "sheet" ||
			(c.ComponentType != "" && c.ComponentType != schLayoutPartType) {
			continue
		}
		if boxesOverlap2(*c.BBox, swept) {
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
