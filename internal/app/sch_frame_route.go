package app

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zhoushoujianwork/easyeda-agent/internal/schguard"
)

const (
	frameRouteTolerance = 0.5
	frameRouteLead      = 5.0
	frameRouteMaxShift  = 60.0
)

type frameRoutePinAnchor struct {
	PartIndex int
	PinIndex  int
	Pin       layoutPin
	Frame     *schFrameSpec
}

type frameRouteIssue struct {
	ID                 string                   `json:"id"`
	Frame              *schFrameSpec            `json:"frame,omitempty"`
	Net                string                   `json:"net,omitempty"`
	WireIDs            []string                 `json:"wireIds,omitempty"`
	PinRefs            []string                 `json:"pinRefs,omitempty"`
	MoveDesignators    []string                 `json:"moveDesignators,omitempty"`
	MoveComponentIDs   []string                 `json:"moveComponentIds,omitempty"`
	MoveWireIDs        []string                 `json:"moveWireIds,omitempty"`
	MoveMarkerIDs      []string                 `json:"moveMarkerIds,omitempty"`
	MoveLabelIDs       []string                 `json:"moveLabelIds,omitempty"`
	DeleteWireIDs      []string                 `json:"deleteWireIds,omitempty"`
	BoundaryWires      []frameRouteWire         `json:"boundaryWires,omitempty"`
	BoundaryPaths      [][]float64              `json:"boundaryPaths,omitempty"`
	ShiftX             float64                  `json:"shiftX,omitempty"`
	ShiftY             float64                  `json:"shiftY,omitempty"`
	At                 *checkPoint              `json:"at,omitempty"`
	Status             string                   `json:"status"`
	Blockers           []string                 `json:"blockers,omitempty"`
	OriginalWireStates []frameRouteOriginalWire `json:"-"`
}

type frameRouteWire struct {
	Net    string    `json:"net"`
	Points []float64 `json:"points"`
}

type frameRouteOriginalWire struct {
	ID     string
	Net    string
	Points []float64
}

type frameRouteReport struct {
	SchemaVersion int               `json:"schemaVersion"`
	DocumentID    string            `json:"documentId"`
	Issues        []frameRouteIssue `json:"issues"`
	Ready         int               `json:"ready"`
	Blocked       int               `json:"blocked"`
}

// frameRouteFindings enforces route containment at the functional-frame level.
// A tree that touches pins in multiple frames is a real cross-frame connection
// and is not judged here. A tree owned by exactly one frame must keep every pin,
// segment and marker/stub inside that frame.
func frameRouteFindings(comps []layoutComp, wires []schGroupWire, frames []schFrameSpec, eps float64) []checkFinding {
	if len(frames) == 0 {
		return nil
	}
	if eps <= 0 || eps > frameRouteTolerance {
		eps = frameRouteTolerance
	}
	var out []checkFinding
	for i := range comps {
		c := &comps[i]
		if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
			continue
		}
		frame := componentFrame(c, frames)
		if frame == nil {
			continue
		}
		for _, p := range c.Pins {
			if pointInBox2(p.X, p.Y, frame.Rect, eps) {
				continue
			}
			ref := c.Designator + "." + p.Number
			out = append(out, checkFinding{
				Type: "pin-outside-frame", Level: "warn", Designator: c.Designator,
				PrimitiveId: c.ID, WireNet: p.Net, Pins: []string{p.Number},
				PrimitiveIds: []string{c.ID, frame.ID}, Keepout: &frame.Rect,
				At: &checkPoint{X: round2(p.X), Y: round2(p.Y)},
				Message: fmt.Sprintf("%s 引脚 %s 位于功能框 %s 外 (%.0f,%.0f) — 移动器件组使其引脚和接入线回到框内",
					c.Designator, ref, frame.ID, p.X, p.Y),
			})
		}
	}
	out = append(out, wireTreeFrameFindings(comps, wires, frames, eps)...)
	return out
}

func wireTreeFrameFindings(comps []layoutComp, wires []schGroupWire, frames []schFrameSpec, eps float64) []checkFinding {
	find, _ := wireTreeFinder(wires)
	trees := map[int][]int{}
	for i := range wires {
		trees[find(i)] = append(trees[find(i)], i)
	}
	roots := make([]int, 0, len(trees))
	for root := range trees {
		roots = append(roots, root)
	}
	sort.Ints(roots)
	var out []checkFinding
	for _, root := range roots {
		indexes := trees[root]
		anchors, pinFrames := frameRouteTreeAnchors(comps, wires, indexes, frames)
		if len(pinFrames) != 1 {
			continue
		}
		var frame *schFrameSpec
		for _, f := range pinFrames {
			frame = f
		}
		if frame == nil {
			continue
		}
		var outsidePin *frameRoutePinAnchor
		for i := range anchors {
			if !pointInBox2(anchors[i].Pin.X, anchors[i].Pin.Y, frame.Rect, eps) {
				outsidePin = &anchors[i]
				break
			}
		}
		var outsideSegment []float64
		for _, wi := range indexes {
			for _, s := range labelWireSegments(wires[wi]) {
				if segmentInsideFrame(s[0], s[1], s[2], s[3], frame.Rect, eps) {
					continue
				}
				outsideSegment = []float64{s[0], s[1], s[2], s[3]}
				break
			}
			if outsideSegment != nil {
				break
			}
		}
		var outsideMarker *layoutComp
		for i := range comps {
			marker := &comps[i]
			if !isSchMarker(marker.ComponentType) || marker.BBox == nil || !marker.AnchorAvailable {
				continue
			}
			touches := false
			for _, wi := range indexes {
				if wireTouchesPoint(wires[wi], marker.X, marker.Y) {
					touches = true
					break
				}
			}
			if touches && !boxInside2(*marker.BBox, frame.Rect) {
				outsideMarker = marker
				break
			}
		}
		if outsidePin == nil && outsideSegment == nil && outsideMarker == nil {
			continue
		}
		ids := make([]string, 0, len(indexes))
		for _, wi := range indexes {
			ids = append(ids, wires[wi].ID)
		}
		sort.Strings(ids)
		refs := make([]string, 0, len(anchors))
		for _, a := range anchors {
			refs = append(refs, comps[a.PartIndex].Designator+"."+a.Pin.Number)
		}
		sort.Strings(refs)
		net := frameRouteTreeNet(comps, wires, indexes)
		reason := ""
		at := checkPoint{}
		if outsidePin != nil {
			reason = fmt.Sprintf("引脚 %s.%s 在框外 (%.0f,%.0f)", comps[outsidePin.PartIndex].Designator,
				outsidePin.Pin.Number, outsidePin.Pin.X, outsidePin.Pin.Y)
			at = checkPoint{X: round2(outsidePin.Pin.X), Y: round2(outsidePin.Pin.Y)}
		} else if outsideSegment != nil {
			reason = fmt.Sprintf("线段 (%.0f,%.0f)→(%.0f,%.0f) 在框外",
				outsideSegment[0], outsideSegment[1], outsideSegment[2], outsideSegment[3])
			at = checkPoint{X: round2((outsideSegment[0] + outsideSegment[2]) / 2),
				Y: round2((outsideSegment[1] + outsideSegment[3]) / 2)}
		} else {
			reason = fmt.Sprintf("网络标识 %s(%s) 本体在框外，bbox %.0f..%.0f × %.0f..%.0f",
				outsideMarker.Net, outsideMarker.ID, outsideMarker.BBox.MinX, outsideMarker.BBox.MaxX,
				outsideMarker.BBox.MinY, outsideMarker.BBox.MaxY)
			at = checkPoint{X: round2(outsideMarker.X), Y: round2(outsideMarker.Y)}
		}
		out = append(out, checkFinding{
			Type: "wire-outside-frame", Level: "warn", WirePrimitiveId: ids[0],
			WireNet: net, Nets: []string{net}, Pins: refs,
			PrimitiveIds: append(append([]string(nil), ids...), frame.ID),
			Keepout:      &frame.Rect, At: &at,
			Message: fmt.Sprintf("网络 %s 的同框线树越出功能框 %s：%s — 移回框内并重新布正交线",
				frameRouteDisplayNet(net), frame.ID, reason),
		})
	}
	return out
}

func componentFrame(c *layoutComp, frames []schFrameSpec) *schFrameSpec {
	if c == nil || c.BBox == nil || len(frames) == 0 {
		return nil
	}
	cx := (c.BBox.MinX + c.BBox.MaxX) / 2
	cy := (c.BBox.MinY + c.BBox.MaxY) / 2
	var matched []int
	for i := range frames {
		if pointInBox2(cx, cy, frames[i].Rect, frameRouteTolerance) {
			matched = append(matched, i)
		}
	}
	if len(matched) != 1 {
		return nil
	}
	return &frames[matched[0]]
}

func frameRouteTreeAnchors(comps []layoutComp, wires []schGroupWire, indexes []int,
	frames []schFrameSpec) ([]frameRoutePinAnchor, map[string]*schFrameSpec) {
	type key struct {
		part int
		pin  int
	}
	seen := map[key]bool{}
	var anchors []frameRoutePinAnchor
	pinFrames := map[string]*schFrameSpec{}
	for _, wi := range indexes {
		for pi := range comps {
			c := &comps[pi]
			if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
				continue
			}
			for pinIndex, pin := range c.Pins {
				if !wireTouchesPoint(wires[wi], pin.X, pin.Y) {
					continue
				}
				k := key{part: pi, pin: pinIndex}
				if seen[k] {
					continue
				}
				seen[k] = true
				frame := componentFrame(c, frames)
				if frame == nil {
					continue
				}
				pinFrames[frame.ID] = frame
				anchors = append(anchors, frameRoutePinAnchor{
					PartIndex: pi, PinIndex: pinIndex, Pin: pin, Frame: frame,
				})
			}
		}
	}
	return anchors, pinFrames
}

func frameRouteTreeNet(comps []layoutComp, wires []schGroupWire, indexes []int) string {
	nets := map[string]bool{}
	for _, wi := range indexes {
		if net := strings.TrimSpace(wires[wi].Net); net != "" {
			nets[net] = true
		}
		for _, c := range comps {
			if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
				continue
			}
			for _, pin := range c.Pins {
				if strings.TrimSpace(pin.Net) != "" && wireTouchesPoint(wires[wi], pin.X, pin.Y) {
					nets[strings.TrimSpace(pin.Net)] = true
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

func frameRouteDisplayNet(net string) string {
	if strings.TrimSpace(net) == "" {
		return "?"
	}
	return net
}

func segmentInsideFrame(x0, y0, x1, y1 float64, frame layoutBBox, eps float64) bool {
	return pointInBox2(x0, y0, frame, eps) && pointInBox2(x1, y1, frame, eps)
}

func frameRoutePlans(comps []layoutComp, wires []schGroupWire, items []visibleTextItem,
	frames []schFrameSpec, sheet, titleBlock *layoutBBox) []frameRouteIssue {
	if len(frames) == 0 {
		return nil
	}
	plansByKey := map[string]frameRouteIssue{}
	coveredTrees := map[string]bool{}
	for pi := range comps {
		c := &comps[pi]
		if c.ComponentType != "" && c.ComponentType != schLayoutPartType || c.Designator == "" {
			continue
		}
		frame := componentFrame(c, frames)
		if frame == nil {
			continue
		}
		outside := false
		for _, pin := range c.Pins {
			if !pointInBox2(pin.X, pin.Y, frame.Rect, frameRouteTolerance) {
				outside = true
				break
			}
		}
		if !outside {
			continue
		}
		issue := planFrameRouteMove(c, frame, items, comps, wires, sheet, titleBlock)
		for _, id := range issue.DeleteWireIDs {
			coveredTrees[id] = true
		}
		plansByKey["move:"+c.Designator] = issue
	}

	find, _ := wireTreeFinder(wires)
	trees := map[int][]int{}
	for i := range wires {
		trees[find(i)] = append(trees[find(i)], i)
	}
	roots := make([]int, 0, len(trees))
	for root := range trees {
		roots = append(roots, root)
	}
	sort.Ints(roots)
	for _, root := range roots {
		indexes := trees[root]
		covered := false
		for _, wi := range indexes {
			if coveredTrees[wires[wi].ID] {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		anchors, pinFrames := frameRouteTreeAnchors(comps, wires, indexes, frames)
		if len(pinFrames) != 1 {
			continue
		}
		var frame *schFrameSpec
		for _, f := range pinFrames {
			frame = f
		}
		outside := false
		for _, a := range anchors {
			if !pointInBox2(a.Pin.X, a.Pin.Y, frame.Rect, frameRouteTolerance) {
				outside = true
			}
		}
		for _, wi := range indexes {
			for _, s := range labelWireSegments(wires[wi]) {
				if !segmentInsideFrame(s[0], s[1], s[2], s[3], frame.Rect, frameRouteTolerance) {
					outside = true
				}
			}
		}
		if !outside {
			continue
		}
		issue := planFrameRouteWireTree(frame, comps, wires, items, indexes, sheet, titleBlock)
		if len(issue.DeleteWireIDs) == 0 {
			continue
		}
		plansByKey["wire:"+issue.DeleteWireIDs[0]] = issue
	}

	out := make([]frameRouteIssue, 0, len(plansByKey))
	for _, issue := range plansByKey {
		out = append(out, issue)
	}
	sort.Slice(out, func(i, j int) bool {
		ai, aj := frameRouteIssueKey(out[i]), frameRouteIssueKey(out[j])
		return ai < aj
	})
	for i := range out {
		out[i].ID = fmt.Sprintf("frame-route-%03d", i+1)
	}
	return out
}

func planFrameRouteMove(c *layoutComp, frame *schFrameSpec, items []visibleTextItem,
	comps []layoutComp, wires []schGroupWire, sheet, titleBlock *layoutBBox) frameRouteIssue {
	issue := frameRouteIssue{Frame: frame, MoveDesignators: []string{c.Designator},
		MoveComponentIDs: []string{c.ID}, Status: "blocked"}
	attachedSet := frameRouteAttachedWireSet(c, wires)
	var sharedWires []string
	for _, wire := range wires {
		if !attachedSet[wire.ID] || !frameRouteWireTouchesOtherPart(c, wire, comps) {
			continue
		}
		sharedWires = append(sharedWires, wire.ID)
	}
	sort.Strings(sharedWires)
	issue.DeleteWireIDs = append(issue.DeleteWireIDs, sharedWires...)
	sort.Strings(issue.DeleteWireIDs)
	for _, wireID := range sharedWires {
		wire, ok := wireByID(wires, wireID)
		if !ok {
			continue
		}
		for _, a := range frameRouteWirePinAnchors(comps, wire) {
			issue.PinRefs = appendUniqueString(issue.PinRefs,
				comps[a.PartIndex].Designator+"."+a.Pin.Number)
		}
	}
	sort.Strings(issue.PinRefs)
	for _, w := range wires {
		if containsString(sharedWires, w.ID) {
			issue.OriginalWireStates = append(issue.OriginalWireStates, frameRouteOriginalWire{
				ID: w.ID, Net: w.Net, Points: append([]float64(nil), wireRoutePoints(w)...),
			})
		}
	}
	if sheet == nil {
		issue.Blockers = []string{"sheet-geometry-unavailable"}
		return issue
	}
	movedComp := map[string]bool{c.ID: true}
	movedWire := map[string]bool{}
	movedText := map[string]bool{}
	for _, it := range items {
		if it.ID != "" && it.BBox != nil &&
			it.BBox.MaxX-it.BBox.MinX > frameRouteTolerance &&
			it.BBox.MaxY-it.BBox.MinY > frameRouteTolerance &&
			movedComp[it.ParentID] {
			movedText[it.ID] = true
		}
	}
	obstacleWires := make([]schGroupWire, 0, len(wires))
	for _, wire := range wires {
		if !attachedSet[wire.ID] {
			obstacleWires = append(obstacleWires, wire)
		}
	}
	var last []string
	var bestBoundaryBlockers []string
	var boundarySamples []string
	frameRejects, collisionRejects, boundaryRejects := 0, 0, 0
	for _, delta := range frameRouteShiftDeltas() {
		last = nil
		if !frameMovedPartInside(c, frame, delta, movedComp, movedWire, movedText, items, comps, wires) {
			frameRejects++
			last = []string{"outside-functional-frame"}
			continue
		}
		if !groupMoveClear(delta, movedComp, movedWire, visibleTextIDList(movedText),
			items, comps, obstacleWires, sheet, titleBlock) {
			collisionRejects++
			last = []string{"move-collision"}
			continue
		}
		boundary, blockers := planFrameRouteBoundaries(c.Designator, delta, sharedWires, frame,
			comps, wires, items, movedWire, movedComp, movedText)
		if len(blockers) != 0 {
			boundaryRejects++
			sample := fmt.Sprintf("delta=%.0f,%.0f:%s", delta[0], delta[1], strings.Join(blockers, ","))
			if !stringInSlice(sample, boundarySamples) {
				boundarySamples = append(boundarySamples, sample)
			}
			if len(bestBoundaryBlockers) == 0 {
				bestBoundaryBlockers = append([]string(nil), blockers...)
			}
			last = blockers
			continue
		}
		issue.ShiftX, issue.ShiftY = delta[0], delta[1]
		issue.BoundaryWires = boundary
		for _, wire := range boundary {
			if strings.TrimSpace(issue.Net) == "" {
				issue.Net = wire.Net
			}
			if wire.Net != issue.Net {
				last = []string{"multiple-boundary-nets-require-separate-plans"}
				break
			}
		}
		if len(last) != 0 {
			continue
		}
		if path := frameRoutePathForNet(boundary, issue.Net); len(path) >= 4 {
			issue.BoundaryPaths = append(issue.BoundaryPaths, path)
		}
		issue.Status = "ready"
		issue.Blockers = nil
		issue.At = &checkPoint{X: round2(c.X + delta[0]), Y: round2(c.Y + delta[1])}
		return issue
	}
	if len(last) == 0 {
		last = []string{"no-clear-in-frame-move"}
	}
	if len(bestBoundaryBlockers) > 0 {
		last = append([]string(nil), bestBoundaryBlockers...)
		if len(boundarySamples) > 1 {
			if len(boundarySamples) > 4 {
				boundarySamples = boundarySamples[:4]
			}
			last = append(last, "boundary-samples="+strings.Join(boundarySamples, " | "))
		}
	}
	if len(last) > 0 {
		last = append(last, fmt.Sprintf("search-rejects-collision=%d,frame=%d,boundary=%d",
			collisionRejects, frameRejects, boundaryRejects))
	}
	issue.Blockers = last
	return issue
}

func frameRouteAttachedWireSet(c *layoutComp, wires []schGroupWire) map[string]bool {
	out := map[string]bool{}
	for _, wire := range wires {
		for _, pin := range c.Pins {
			if wireTouchesPoint(wire, pin.X, pin.Y) {
				out[wire.ID] = true
				break
			}
		}
	}
	return out
}

func frameRouteWireTouchesOtherPart(c *layoutComp, wire schGroupWire, comps []layoutComp) bool {
	for _, other := range comps {
		if other.ID == c.ID || (other.ComponentType != "" && other.ComponentType != schLayoutPartType) {
			continue
		}
		for _, pin := range other.Pins {
			if wireTouchesPoint(wire, pin.X, pin.Y) {
				return true
			}
		}
	}
	return false
}

func planFrameRouteWireTree(frame *schFrameSpec, comps []layoutComp, wires []schGroupWire,
	items []visibleTextItem, indexes []int, sheet, titleBlock *layoutBBox) frameRouteIssue {
	issue := frameRouteIssue{Frame: frame, Status: "blocked"}
	ids := make([]string, 0, len(indexes))
	for _, wi := range indexes {
		ids = append(ids, wires[wi].ID)
	}
	sort.Strings(ids)
	issue.DeleteWireIDs = ids
	issue.WireIDs = append([]string(nil), ids...)
	issue.Net = frameRouteTreeNet(comps, wires, indexes)
	for _, wi := range indexes {
		issue.OriginalWireStates = append(issue.OriginalWireStates, frameRouteOriginalWire{
			ID: wires[wi].ID, Net: wires[wi].Net, Points: append([]float64(nil), wireRoutePoints(wires[wi])...),
		})
	}
	anchors, _ := frameRouteTreeAnchors(comps, wires, indexes, []schFrameSpec{*frame})
	if len(anchors) < 2 || issue.Net == "" {
		issue.Blockers = []string{"requires-at-least-two-same-net-pins"}
		return issue
	}
	excluded := map[string]bool{}
	for _, id := range issue.DeleteWireIDs {
		excluded[id] = true
	}
	var allSegments [][4]float64
	var blockers []string
	hub := frameRouteHubIndex(anchors)
	for i := range anchors {
		if i == hub {
			continue
		}
		segments, why := frameRoutePathSegments(frame.Rect, anchors[hub].Pin, anchors[i].Pin,
			comps, wires, items, excluded, map[string]bool{}, nil)
		if len(why) != 0 {
			blockers = append(blockers, why...)
			continue
		}
		allSegments = append(allSegments, segments...)
		issue.BoundaryPaths = append(issue.BoundaryPaths, frameRouteSegmentsPath(segments))
		issue.PinRefs = append(issue.PinRefs,
			comps[anchors[i].PartIndex].Designator+"."+anchors[i].Pin.Number)
	}
	if len(blockers) != 0 {
		issue.Blockers = blockers
		return issue
	}
	allSegments = alignmentDedupeSegments(allSegments)
	issue.BoundaryWires = frameRouteSegmentsAsWires(issue.Net, allSegments)
	issue.PinRefs = append(issue.PinRefs,
		comps[anchors[hub].PartIndex].Designator+"."+anchors[hub].Pin.Number)
	sort.Strings(issue.PinRefs)
	issue.Status = "ready"
	issue.At = &checkPoint{X: round2(anchors[0].Pin.X), Y: round2(anchors[0].Pin.Y)}
	return issue
}

func frameRouteHubIndex(anchors []frameRoutePinAnchor) int {
	for i, anchor := range anchors {
		dir, ok := pinCardinalOutward(anchor.Pin)
		if ok && (dir == "left" || dir == "right") {
			return i
		}
	}
	return 0
}

func planFrameRouteBoundaries(movedDesignator string, delta [2]float64, sharedWires []string,
	frame *schFrameSpec, comps []layoutComp, wires []schGroupWire, items []visibleTextItem,
	movedWire, movedComp, movedText map[string]bool) ([]frameRouteWire, []string) {
	var out []frameRouteWire
	var blockers []string
	movedPins := map[string]bool{}
	for _, c := range comps {
		if c.Designator != movedDesignator {
			continue
		}
		for _, p := range c.Pins {
			movedPins[fmt.Sprintf("%.3f,%.3f", p.X+delta[0], p.Y+delta[1])] = true
		}
	}
	for _, wireID := range sharedWires {
		wire, ok := wireByID(wires, wireID)
		if !ok {
			blockers = append(blockers, "shared-wire-missing:"+wireID)
			continue
		}
		anchors := frameRouteWirePinAnchors(comps, wire)
		if len(anchors) != 2 {
			blockers = append(blockers, "unsupported-branch-count:"+wireID)
			continue
		}
		net := frameRouteWireNet(comps, wire)
		if net == "" {
			blockers = append(blockers, "net-ambiguous:"+wireID)
			continue
		}
		var fixed, moved frameRoutePinAnchor
		for _, a := range anchors {
			if comps[a.PartIndex].Designator == movedDesignator {
				a.Pin.X += delta[0]
				a.Pin.Y += delta[1]
				moved = a
			} else {
				fixed = a
			}
		}
		if moved.PartIndex == fixed.PartIndex && moved.Pin.Number == fixed.Pin.Number {
			blockers = append(blockers, "boundary-endpoints-not-split:"+wireID)
			continue
		}
		if !pointInBox2(fixed.Pin.X, fixed.Pin.Y, frame.Rect, frameRouteTolerance) ||
			!pointInBox2(moved.Pin.X, moved.Pin.Y, frame.Rect, frameRouteTolerance) {
			blockers = append(blockers, "boundary-pin-outside-frame:"+wireID)
			continue
		}
		exclude := map[string]bool{}
		for _, id := range sharedWires {
			exclude[id] = true
		}
		simWire := map[string]bool{}
		for id := range movedWire {
			simWire[id] = true
		}
		simComp := map[string]bool{}
		for id := range movedComp {
			simComp[id] = true
		}
		segments, why := frameRoutePathSegments(frame.Rect, fixed.Pin, moved.Pin, comps, wires, items,
			exclude, simWire, &frameRouteSimulation{Delta: delta, MovedComp: simComp, MovedWire: simWire,
				MovedText: movedText})
		if len(why) != 0 {
			blockers = append(blockers, "boundary-route:"+wireID+":"+strings.Join(why, ","))
			continue
		}
		out = append(out, frameRouteSegmentsAsWires(net, segments)...)
	}
	sort.Strings(blockers)
	return out, blockers
}

type frameRouteSimulation struct {
	Delta     [2]float64
	MovedComp map[string]bool
	MovedWire map[string]bool
	MovedText map[string]bool
}

func frameRoutePathSegments(frame layoutBBox, a, b layoutPin, comps []layoutComp,
	wires []schGroupWire, items []visibleTextItem, excludeWire, movedWire map[string]bool,
	simulation *frameRouteSimulation) ([][4]float64, []string) {
	aDir, aOK := pinCardinalOutward(a)
	bDir, bOK := pinCardinalOutward(b)
	if !aOK || !bOK {
		return nil, []string{"pin-outward-direction-unavailable"}
	}
	aLead := [2]float64{a.X, a.Y}
	bLead := [2]float64{b.X, b.Y}
	switch aDir {
	case "up":
		aLead[1] += frameRouteLead
	case "down":
		aLead[1] -= frameRouteLead
	case "left":
		aLead[0] -= frameRouteLead
	case "right":
		aLead[0] += frameRouteLead
	}
	switch bDir {
	case "up":
		bLead[1] += frameRouteLead
	case "down":
		bLead[1] -= frameRouteLead
	case "left":
		bLead[0] -= frameRouteLead
	case "right":
		bLead[0] += frameRouteLead
	}
	if !pointInBox2(aLead[0], aLead[1], frame, frameRouteTolerance) ||
		!pointInBox2(bLead[0], bLead[1], frame, frameRouteTolerance) {
		return nil, []string{"pin-outward-lead-outside-frame"}
	}
	clear := func(p, q [2]float64) bool {
		if !segmentInsideFrame(p[0], p[1], q[0], q[1], frame, frameRouteTolerance) {
			return false
		}
		seg := [4]float64{p[0], p[1], q[0], q[1]}
		for ci := range comps {
			c := &comps[ci]
			if c.BBox == nil || c.ComponentType == "sheet" {
				continue
			}
			box := *c.BBox
			if simulation != nil && simulation.MovedComp[c.ID] {
				box = translateLayoutBBox(box, simulation.Delta)
			}
			if schguard.SegmentEntersBody(
				schguard.Point{X: seg[0], Y: seg[1]}, schguard.Point{X: seg[2], Y: seg[3]},
				schguard.BBox{MinX: box.MinX, MinY: box.MinY, MaxX: box.MaxX, MaxY: box.MaxY},
			) {
				return false
			}
			for _, pin := range c.Pins {
				px, py := pin.X, pin.Y
				if simulation != nil && simulation.MovedComp[c.ID] {
					px += simulation.Delta[0]
					py += simulation.Delta[1]
				}
				if pointsClose(px, py, a.X, a.Y, frameRouteTolerance) ||
					pointsClose(px, py, b.X, b.Y, frameRouteTolerance) {
					continue
				}
				if pointNearSegment2(px, py, p, q, frameRouteTolerance) {
					return false
				}
			}
		}
		for _, it := range items {
			if it.BBox == nil || (simulation != nil && simulation.MovedText[it.ID]) {
				continue
			}
			if schSegmentCrossesBox(seg[0], seg[1], seg[2], seg[3], inflateLayoutBBox(*it.BBox, 1)) {
				return false
			}
		}
		for _, w := range wires {
			if excludeWire[w.ID] {
				continue
			}
			for _, s := range labelWireSegments(w) {
				os := [4]float64{s[0], s[1], s[2], s[3]}
				if simulation != nil && simulation.MovedWire[w.ID] {
					os[0] += simulation.Delta[0]
					os[1] += simulation.Delta[1]
					os[2] += simulation.Delta[0]
					os[3] += simulation.Delta[1]
				}
				if segmentsCross2(p, q, [2]float64{os[0], os[1]}, [2]float64{os[2], os[3]}) ||
					alignmentSegmentsOverlap(seg, os) {
					return false
				}
			}
		}
		return true
	}
	route, ok := dcFindOrthogonalPath(aLead, bLead, &frame, nil, clear, 1600)
	if !ok {
		return nil, []string{"no-clear-orthogonal-route-inside-frame"}
	}
	full := [][2]float64{{a.X, a.Y}, {aLead[0], aLead[1]}}
	for _, p := range route[1 : len(route)-1] {
		full = append(full, p)
	}
	full = append(full, bLead, [2]float64{b.X, b.Y})
	full = cleanDCPath(full)
	if !firstSegmentMatchesDir(full, aDir) {
		return nil, []string{"first-segment-not-outward"}
	}
	incoming := directionBetween(full[len(full)-2], full[len(full)-1])
	if oppositeDirection(incoming) != bDir {
		return nil, []string{"last-segment-not-outward"}
	}
	var out [][4]float64
	for i := 0; i+1 < len(full); i++ {
		if math.Abs(full[i][0]-full[i+1][0]) <= frameRouteTolerance &&
			math.Abs(full[i][1]-full[i+1][1]) <= frameRouteTolerance {
			continue
		}
		out = append(out, [4]float64{full[i][0], full[i][1], full[i+1][0], full[i+1][1]})
	}
	return alignmentDedupeSegments(out), nil
}

func frameRouteSegmentsAsWires(net string, segments [][4]float64) []frameRouteWire {
	out := make([]frameRouteWire, 0, len(segments))
	for _, s := range segments {
		out = append(out, frameRouteWire{Net: net, Points: []float64{s[0], s[1], s[2], s[3]}})
	}
	return out
}

func frameRouteSegmentsPath(segments [][4]float64) []float64 {
	if len(segments) == 0 {
		return nil
	}
	remaining := append([][4]float64(nil), segments...)
	first := remaining[0]
	remaining = remaining[1:]
	points := []float64{first[0], first[1], first[2], first[3]}
	cur := [2]float64{first[2], first[3]}
	for len(remaining) > 0 {
		found := false
		for i, s := range remaining {
			if pointsClose(cur[0], cur[1], s[0], s[1], frameRouteTolerance) {
				points = append(points, s[2], s[3])
				cur = [2]float64{s[2], s[3]}
				remaining = append(remaining[:i], remaining[i+1:]...)
				found = true
				break
			}
			if pointsClose(cur[0], cur[1], s[2], s[3], frameRouteTolerance) {
				points = append(points, s[0], s[1])
				cur = [2]float64{s[0], s[1]}
				remaining = append(remaining[:i], remaining[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return points
}

func frameRouteWirePinAnchors(comps []layoutComp, wire schGroupWire) []frameRoutePinAnchor {
	var out []frameRoutePinAnchor
	for pi := range comps {
		c := &comps[pi]
		if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
			continue
		}
		for pinIndex, pin := range c.Pins {
			if pin.Net != "" && wireTouchesPoint(wire, pin.X, pin.Y) {
				out = append(out, frameRoutePinAnchor{PartIndex: pi, PinIndex: pinIndex, Pin: pin})
			}
		}
	}
	return out
}

func frameRouteWireNet(comps []layoutComp, wire schGroupWire) string {
	nets := map[string]bool{}
	if strings.TrimSpace(wire.Net) != "" {
		nets[strings.TrimSpace(wire.Net)] = true
	}
	for _, a := range frameRouteWirePinAnchors(comps, wire) {
		if strings.TrimSpace(a.Pin.Net) != "" {
			nets[strings.TrimSpace(a.Pin.Net)] = true
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

func frameMovedPartInside(c *layoutComp, frame *schFrameSpec, delta [2]float64,
	movedComp, movedWire, movedText map[string]bool, items []visibleTextItem,
	comps []layoutComp, wires []schGroupWire) bool {
	if c.BBox == nil || !boxInside2(translateLayoutBBox(*c.BBox, delta), frame.Rect) {
		return false
	}
	for _, p := range c.Pins {
		if !pointInBox2(p.X+delta[0], p.Y+delta[1], frame.Rect, frameRouteTolerance) {
			return false
		}
	}
	for _, mc := range comps {
		if !movedComp[mc.ID] || mc.BBox == nil {
			continue
		}
		if !boxInside2(translateLayoutBBox(*mc.BBox, delta), frame.Rect) {
			return false
		}
	}
	for _, w := range wires {
		if !movedWire[w.ID] {
			continue
		}
		for _, s := range labelWireSegments(w) {
			if !segmentInsideFrame(s[0]+delta[0], s[1]+delta[1],
				s[2]+delta[0], s[3]+delta[1], frame.Rect, frameRouteTolerance) {
				return false
			}
		}
	}
	for _, it := range items {
		if !movedText[it.ID] || it.BBox == nil {
			continue
		}
		if it.BBox.MaxX-it.BBox.MinX <= frameRouteTolerance ||
			it.BBox.MaxY-it.BBox.MinY <= frameRouteTolerance {
			continue // empty/unrendered attribute placeholder, not visible text
		}
		if !boxInside2(translateLayoutBBox(*it.BBox, delta), frame.Rect) {
			return false
		}
	}
	return true
}

func frameRouteIssueKey(issue frameRouteIssue) string {
	if len(issue.MoveDesignators) > 0 {
		return "move:" + strings.Join(issue.MoveDesignators, ",")
	}
	if len(issue.DeleteWireIDs) > 0 {
		return "wire:" + strings.Join(issue.DeleteWireIDs, ",")
	}
	return issue.ID
}

func frameRouteShiftDeltas() [][2]float64 {
	const step = 5.0
	var out [][2]float64
	for r := step; r <= frameRouteMaxShift; r += step {
		for dy := -r; dy <= r; dy += step {
			for dx := -r; dx <= r; dx += step {
				if math.Max(math.Abs(dx), math.Abs(dy)) != r {
					continue
				}
				out = append(out, [2]float64{dx, dy})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai := math.Abs(out[i][0]) + math.Abs(out[i][1])
		aj := math.Abs(out[j][0]) + math.Abs(out[j][1])
		if ai != aj {
			return ai < aj
		}
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

func wireRoutePoints(w schGroupWire) []float64 {
	if len(w.Points) >= 4 {
		return append([]float64(nil), w.Points...)
	}
	var points []float64
	for _, s := range labelWireSegments(w) {
		if len(points) == 0 {
			points = append(points, s[0], s[1])
		}
		points = append(points, s[2], s[3])
	}
	return points
}
