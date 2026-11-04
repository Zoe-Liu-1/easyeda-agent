package app

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

type labelStubPoint struct {
	X float64
	Y float64
}

type labelStubGeometry struct {
	Item              visibleTextItem
	Wire              schGroupWire
	Leaf              labelStubPoint
	Junction          labelStubPoint
	StubSegments      [][4]float64
	RemainingSegments [][4]float64
}

// netLabelStubFindings reports the "label on a decorative T-tail" shape:
//
//	wire trunk ─────┬─────
//	                │
//	               Name
//
// A wire Name already belongs to its parent wire. Adding a leaf branch solely
// to host the text is unnecessary geometry and is forbidden. A label at a wire
// endpoint is not reported when the endpoint is a pin/marker, or when the path
// does not branch off a larger connected wire.
func netLabelStubFindings(items []visibleTextItem, comps []layoutComp, wires []schGroupWire) []checkFinding {
	return netLabelStubFindingsWithAnchors(items, comps, wires, nil)
}

func netLabelStubFindingsWithAnchors(items []visibleTextItem, comps []layoutComp,
	wires []schGroupWire, anchors map[string]checkPoint) []checkFinding {
	geometries := labelStubGeometriesWithAnchors(items, comps, wires, anchors)
	out := make([]checkFinding, 0, len(geometries))
	for _, g := range geometries {
		out = append(out, checkFinding{
			Type:            "label-stub",
			Level:           "ERROR",
			PrimitiveId:     g.Item.ID,
			WirePrimitiveId: g.Wire.ID,
			MarkerNet:       g.Item.Value,
			At:              &checkPoint{X: round2(g.Leaf.X), Y: round2(g.Leaf.Y)},
			PrimitiveIds:    []string{g.Item.ID},
			Message: fmt.Sprintf("网络标签「%s」挂在仅为标注而伸出的 T 形线头末端 @(%.0f,%.0f)；标签必须直接落在既有导线上，不能增加额外短线",
				g.Item.Value, g.Leaf.X, g.Leaf.Y),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MarkerNet != out[j].MarkerNet {
			return out[i].MarkerNet < out[j].MarkerNet
		}
		return out[i].PrimitiveId < out[j].PrimitiveId
	})
	return out
}

func labelStubGeometries(items []visibleTextItem, comps []layoutComp, wires []schGroupWire) []labelStubGeometry {
	return labelStubGeometriesWithAnchors(items, comps, wires, nil)
}

func labelStubGeometriesWithAnchors(items []visibleTextItem, comps []layoutComp,
	wires []schGroupWire, anchors map[string]checkPoint) []labelStubGeometry {
	byID := make(map[string]schGroupWire, len(wires))
	for _, wire := range wires {
		byID[wire.ID] = wire
	}
	var out []labelStubGeometry
	for _, item := range items {
		if item.Kind != "attribute" || item.ComponentType != "wire" || item.Key != "Name" ||
			item.ParentID == "" || item.BBox == nil || strings.TrimSpace(item.Value) == "" ||
			(item.Visible != nil && !*item.Visible) {
			continue
		}
		wire, ok := byID[item.ParentID]
		if !ok {
			continue
		}
		index := -1
		for i := range wires {
			if wires[i].ID == item.ParentID {
				index = i
				break
			}
		}
		if index < 0 {
			continue
		}
		tree := labelStubConnectedWireIndexes(wires, index)
		var treeSegments [][4]float64
		for i := range wires {
			if tree[i] {
				treeSegments = append(treeSegments, labelWireSegments(wires[i])...)
			}
		}
		anchor, hasAnchor := anchors[item.ID]
		if g, ok := labelStubGeometryFor(item, wire, comps, treeSegments, anchor, hasAnchor); ok {
			out = append(out, g)
		}
	}
	return out
}

func labelStubConnectedWireIndexes(wires []schGroupWire, start int) map[int]bool {
	connected := map[int]bool{}
	if start < 0 || start >= len(wires) {
		return connected
	}
	connected[start] = true
	for changed := true; changed; {
		changed = false
		for i := range wires {
			if connected[i] {
				continue
			}
			for j := range wires {
				if !connected[j] || i == j {
					continue
				}
				if labelStubWiresTouch(wires[i], wires[j]) {
					connected[i] = true
					changed = true
					break
				}
			}
		}
	}
	return connected
}

func labelStubWiresTouch(a, b schGroupWire) bool {
	for _, left := range labelWireSegments(a) {
		for _, right := range labelWireSegments(b) {
			for _, lp := range [][2]float64{{left[0], left[1]}, {left[2], left[3]}} {
				if pointOnSegment(lp[0], lp[1], right[0], right[1], right[2], right[3], 0.5) {
					return true
				}
			}
			for _, rp := range [][2]float64{{right[0], right[1]}, {right[2], right[3]}} {
				if pointOnSegment(rp[0], rp[1], left[0], left[1], left[2], left[3], 0.5) {
					return true
				}
			}
		}
	}
	return false
}

func labelStubGeometryFor(item visibleTextItem, wire schGroupWire, comps []layoutComp,
	segments [][4]float64, exactAnchor checkPoint, hasExactAnchor bool) (labelStubGeometry, bool) {
	if len(segments) < 2 {
		return labelStubGeometry{}, false
	}
	var anchor labelStubPoint
	if hasExactAnchor {
		anchor = labelStubPoint{X: round2(exactAnchor.X), Y: round2(exactAnchor.Y)}
	} else {
		cx := (item.BBox.MinX + item.BBox.MaxX) / 2
		cy := (item.BBox.MinY + item.BBox.MaxY) / 2
		best := math.Inf(1)
		for _, seg := range segments {
			x, y, d := closestPointOnSegment(cx, cy, seg[0], seg[1], seg[2], seg[3])
			if d < best {
				best = d
				anchor = labelStubPoint{X: round2(x), Y: round2(y)}
			}
		}
		if math.IsInf(best, 1) {
			return labelStubGeometry{}, false
		}
	}

	nodes := labelStubVertices(segments, anchor)
	adj := labelStubAdjacency(segments, nodes)
	anchor, ok := labelStubFindVertex(adj, anchor)
	if !ok || len(adj[anchor]) != 1 {
		return labelStubGeometry{}, false
	}
	if labelStubEndpointConnected(comps, anchor.X, anchor.Y) {
		return labelStubGeometry{}, false
	}

	path, junction, ok := labelStubPathToBranch(adj, anchor)
	if !ok || len(path) == 0 || len(adj[junction]) < 3 {
		return labelStubGeometry{}, false
	}
	if labelStubEndpointConnected(comps, junction.X, junction.Y) {
		return labelStubGeometry{}, false
	}
	remove := make(map[[4]float64]bool, len(path))
	for _, edge := range path {
		remove[labelStubCanonicalEdge(edge)] = true
		for _, p := range []labelStubPoint{{X: edge[0], Y: edge[1]}, {X: edge[2], Y: edge[3]}} {
			if !labelStubSamePoint(p, anchor) && !labelStubSamePoint(p, junction) &&
				labelStubEndpointConnected(comps, p.X, p.Y) {
				return labelStubGeometry{}, false
			}
		}
	}
	var remaining [][4]float64
	for _, seg := range segments {
		if !remove[labelStubCanonicalEdge(seg)] {
			remaining = append(remaining, seg)
		}
	}
	if len(remaining) == 0 {
		return labelStubGeometry{}, false
	}
	return labelStubGeometry{
		Item: item, Wire: wire, Leaf: anchor, Junction: junction,
		StubSegments: path, RemainingSegments: remaining,
	}, true
}

func labelStubVertices(segments [][4]float64, anchor labelStubPoint) []labelStubPoint {
	var points []labelStubPoint
	add := func(p labelStubPoint) {
		for _, existing := range points {
			if labelStubSamePoint(existing, p) {
				return
			}
		}
		points = append(points, p)
	}
	for _, seg := range segments {
		add(labelStubPoint{X: round2(seg[0]), Y: round2(seg[1])})
		add(labelStubPoint{X: round2(seg[2]), Y: round2(seg[3])})
	}
	for _, seg := range segments {
		if pointOnSegment(anchor.X, anchor.Y, seg[0], seg[1], seg[2], seg[3], 0.5) {
			add(anchor)
		}
	}
	return points
}

func labelStubAdjacency(segments [][4]float64, vertices []labelStubPoint) map[labelStubPoint]map[labelStubPoint]bool {
	adj := make(map[labelStubPoint]map[labelStubPoint]bool)
	for _, seg := range segments {
		horizontal := math.Abs(seg[1]-seg[3]) <= 0.5
		vertical := math.Abs(seg[0]-seg[2]) <= 0.5
		if !horizontal && !vertical {
			continue
		}
		var on []labelStubPoint
		for _, p := range vertices {
			if pointOnSegment(p.X, p.Y, seg[0], seg[1], seg[2], seg[3], 0.5) {
				on = append(on, p)
			}
		}
		sort.Slice(on, func(i, j int) bool {
			if horizontal {
				return on[i].X < on[j].X
			}
			return on[i].Y < on[j].Y
		})
		for i := 0; i+1 < len(on); i++ {
			labelStubConnect(adj, on[i], on[i+1])
		}
	}
	return adj
}

func labelStubConnect(adj map[labelStubPoint]map[labelStubPoint]bool, a, b labelStubPoint) {
	if labelStubSamePoint(a, b) {
		return
	}
	if adj[a] == nil {
		adj[a] = map[labelStubPoint]bool{}
	}
	if adj[b] == nil {
		adj[b] = map[labelStubPoint]bool{}
	}
	adj[a][b] = true
	adj[b][a] = true
}

func labelStubPathToBranch(adj map[labelStubPoint]map[labelStubPoint]bool,
	leaf labelStubPoint) ([][4]float64, labelStubPoint, bool) {
	previous := leaf
	current := leaf
	var path [][4]float64
	for len(path) <= len(adj) {
		neighbors := make([]labelStubPoint, 0, len(adj[current]))
		for next := range adj[current] {
			neighbors = append(neighbors, next)
		}
		sort.Slice(neighbors, func(i, j int) bool {
			if neighbors[i].X != neighbors[j].X {
				return neighbors[i].X < neighbors[j].X
			}
			return neighbors[i].Y < neighbors[j].Y
		})
		var next labelStubPoint
		found := false
		for _, candidate := range neighbors {
			if !labelStubSamePoint(candidate, previous) || labelStubSamePoint(current, leaf) {
				next, found = candidate, true
				break
			}
		}
		if !found {
			return nil, labelStubPoint{}, false
		}
		path = append(path, [4]float64{current.X, current.Y, next.X, next.Y})
		if len(adj[next]) >= 3 {
			return path, next, true
		}
		if len(adj[next]) != 2 {
			return nil, labelStubPoint{}, false
		}
		previous, current = current, next
	}
	return nil, labelStubPoint{}, false
}

func labelStubFindVertex(adj map[labelStubPoint]map[labelStubPoint]bool, target labelStubPoint) (labelStubPoint, bool) {
	for p := range adj {
		if labelStubSamePoint(p, target) {
			return p, true
		}
	}
	return labelStubPoint{}, false
}

func labelStubEndpointConnected(comps []layoutComp, x, y float64) bool {
	for _, comp := range comps {
		if comp.ComponentType == "sheet" {
			continue
		}
		for _, pin := range comp.Pins {
			if labelStubSamePoint(labelStubPoint{X: pin.X, Y: pin.Y}, labelStubPoint{X: x, Y: y}) {
				return true
			}
		}
		if isSchMarker(comp.ComponentType) &&
			labelStubSamePoint(labelStubPoint{X: comp.X, Y: comp.Y}, labelStubPoint{X: x, Y: y}) {
			return true
		}
	}
	return false
}

func labelStubCanonicalEdge(seg [4]float64) [4]float64 {
	a, b := labelStubPoint{X: round2(seg[0]), Y: round2(seg[1])}, labelStubPoint{X: round2(seg[2]), Y: round2(seg[3])}
	if a.X > b.X || (a.X == b.X && a.Y > b.Y) {
		a, b = b, a
	}
	return [4]float64{a.X, a.Y, b.X, b.Y}
}

func labelStubSamePoint(a, b labelStubPoint) bool {
	return math.Abs(a.X-b.X) <= 0.5 && math.Abs(a.Y-b.Y) <= 0.5
}
