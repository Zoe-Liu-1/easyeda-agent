package app

import (
	"container/heap"
	"math"
)

const (
	dcRouteStep     = 5.0
	dcRouteNodeMax  = 12000
	dcRouteMargin   = 80.0
	dcRouteBendCost = 2
)

var dcRouteDirections = [4][2]float64{{1, 0}, {0, 1}, {-1, 0}, {0, -1}}

type dcRouteKey struct {
	X, Y float64
	Dir  int
}

type dcRouteNode struct {
	key   dcRouteKey
	g, h  int
	index int
}

type dcRouteQueue []*dcRouteNode

func (q dcRouteQueue) Len() int           { return len(q) }
func (q dcRouteQueue) Less(i, j int) bool { return q[i].g+q[i].h < q[j].g+q[j].h }
func (q dcRouteQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i]; q[i].index, q[j].index = i, j }
func (q *dcRouteQueue) Push(x any)        { n := x.(*dcRouteNode); n.index = len(*q); *q = append(*q, n) }
func (q *dcRouteQueue) Pop() any          { old := *q; n := old[len(old)-1]; *q = old[:len(old)-1]; return n }

// dcFindOrthogonalPath finds a short obstacle-aware orthogonal route. The
// callback is authoritative: it must return true only when the entire segment
// is legal for the target net. A* runs on a 5-raw lattice and charges a small
// bend penalty so a clear L wins over an equivalent folded route.
func dcFindOrthogonalPath(start, end [2]float64, sheet, titleBlock *layoutBBox,
	clear func([2]float64, [2]float64) bool, maxLength float64) ([][2]float64, bool) {
	if pointEquals2(start[0], start[1], end) {
		return [][2]float64{start, end}, true
	}
	if maxLength <= 0 {
		maxLength = 250
	}
	bounds := layoutBBox{
		MinX: math.Min(start[0], end[0]) - dcRouteMargin,
		MinY: math.Min(start[1], end[1]) - dcRouteMargin,
		MaxX: math.Max(start[0], end[0]) + dcRouteMargin,
		MaxY: math.Max(start[1], end[1]) + dcRouteMargin,
	}
	if sheet != nil {
		bounds.MinX = math.Max(bounds.MinX, sheet.MinX)
		bounds.MinY = math.Max(bounds.MinY, sheet.MinY)
		bounds.MaxX = math.Min(bounds.MaxX, sheet.MaxX)
		bounds.MaxY = math.Min(bounds.MaxY, sheet.MaxY)
	}
	if bounds.MinX > bounds.MaxX || bounds.MinY > bounds.MaxY {
		return nil, false
	}
	heuristic := func(x, y float64) int {
		return int(math.Round((math.Abs(x-end[0]) + math.Abs(y-end[1])) * 10))
	}
	startKey := dcRouteKey{X: start[0], Y: start[1], Dir: 0}
	best := map[dcRouteKey]int{startKey: 0}
	prev := map[dcRouteKey]dcRouteKey{}
	q := &dcRouteQueue{{key: startKey, h: heuristic(start[0], start[1])}}
	heap.Init(q)
	expanded := 0
	for q.Len() > 0 {
		cur := heap.Pop(q).(*dcRouteNode)
		state := cur.key
		if state.Dir != 0 {
			pathPoint := [2]float64{state.X, state.Y}
			if (math.Abs(pathPoint[0]-end[0]) <= alignmentTolerance ||
				math.Abs(pathPoint[1]-end[1]) <= alignmentTolerance) &&
				math.Abs(pathPoint[0]-start[0])+math.Abs(pathPoint[1]-start[1])+
					math.Abs(end[0]-pathPoint[0])+math.Abs(end[1]-pathPoint[1]) <= maxLength &&
				clear(pathPoint, end) {
				goal := dcRouteKey{X: end[0], Y: end[1]}
				prev[goal] = state
				route := dcReconstructRoute(prev, goal, start, end)
				if exact, ok := dcOrthogonalizeRoute(route, clear); ok {
					return exact, true
				}
			}
		}
		for dir, delta := range dcRouteDirections {
			dirState := dir + 1
			if state.Dir != 0 && (state.Dir-1+2)%4 == dir {
				continue // no immediate reversal
			}
			next := [2]float64{state.X + delta[0]*dcRouteStep, state.Y + delta[1]*dcRouteStep}
			if next[0] < bounds.MinX-alignmentTolerance || next[0] > bounds.MaxX+alignmentTolerance ||
				next[1] < bounds.MinY-alignmentTolerance || next[1] > bounds.MaxY+alignmentTolerance {
				continue
			}
			if titleBlock != nil && pointInBox2(next[0], next[1], *titleBlock, 0) {
				continue
			}
			curPoint := [2]float64{state.X, state.Y}
			if !clear(curPoint, next) {
				continue
			}
			cost := cur.g + int(math.Round(math.Hypot(next[0]-state.X, next[1]-state.Y)*10))
			if state.Dir != 0 && state.Dir != dirState {
				cost += dcRouteBendCost
			}
			if float64(cost)/10 > maxLength {
				continue
			}
			nextKey := dcRouteKey{X: next[0], Y: next[1], Dir: dirState}
			if prior, ok := best[nextKey]; ok && prior <= cost {
				continue
			}
			best[nextKey] = cost
			prev[nextKey] = state
			heap.Push(q, &dcRouteNode{key: nextKey, g: cost, h: heuristic(next[0], next[1])})
		}
		expanded++
		if expanded > dcRouteNodeMax {
			break
		}
	}
	return nil, false
}

func dcOrthogonalizeRoute(path [][2]float64, clear func([2]float64, [2]float64) bool) ([][2]float64, bool) {
	if len(path) < 2 {
		return path, false
	}
	out := [][2]float64{path[0]}
	for i := 1; i < len(path); i++ {
		p, q := out[len(out)-1], path[i]
		if math.Abs(p[0]-q[0]) <= 1e-6 || math.Abs(p[1]-q[1]) <= 1e-6 {
			out = append(out, q)
			continue
		}
		candidates := [][2]float64{{q[0], p[1]}, {p[0], q[1]}}
		found := false
		for _, mid := range candidates {
			if clear(p, mid) && clear(mid, q) {
				out = append(out, mid, q)
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return cleanDCPath(out), true
}

func dcReconstructRoute(prev map[dcRouteKey]dcRouteKey, goal dcRouteKey,
	start, end [2]float64) [][2]float64 {
	keys := []dcRouteKey{goal}
	for {
		key, ok := prev[keys[len(keys)-1]]
		if !ok {
			break
		}
		keys = append(keys, key)
	}
	path := make([][2]float64, 0, len(keys)+1)
	path = append(path, start)
	for i := len(keys) - 1; i >= 0; i-- {
		path = append(path, [2]float64{keys[i].X, keys[i].Y})
	}
	path = append(path, end)
	return cleanDCPath(path)
}
