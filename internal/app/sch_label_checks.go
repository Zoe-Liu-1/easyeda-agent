package app

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// netLabelVisibilityFindings audits the Name text of every named marker and
// wire label. Hidden names and markers with no Name attribute are failures:
// the schematic cannot communicate the net by name in the printed drawing.
func netLabelVisibilityFindings(items []visibleTextItem) []checkFinding {
	var out []checkFinding
	for _, it := range items {
		if it.Kind == "marker" && it.MissingName {
			net := strings.TrimSpace(it.Value)
			if net == "" {
				net = it.ParentID
			}
			out = append(out, checkFinding{
				Type: "label-visibility", Level: "warn",
				PrimitiveId: it.ID, MarkerNet: net,
				Message: fmt.Sprintf("电源/地/端口符号 %s 没有 Name 属性 — 名称不可见，必须补名称", net),
			})
			continue
		}
		if it.Kind != "attribute" || it.Key != "Name" {
			continue
		}
		if it.Visible != nil && !*it.Visible {
			out = append(out, checkFinding{
				Type: "label-visibility", Level: "warn",
				PrimitiveId: it.ID, MarkerNet: it.Value, BBox: it.BBox,
				Message: fmt.Sprintf("名称「%s」的可见性为 false — 电源/地/信号名称必须显示", it.Value),
			})
			continue
		}
		if strings.TrimSpace(it.Value) == "" {
			out = append(out, checkFinding{
				Type: "label-visibility", Level: "warn",
				PrimitiveId: it.ID, BBox: it.BBox,
				Message: "Name 属性已显示但内容为空 — 必须填写网络名",
			})
		}
	}
	return out
}

// netLabelOrientationFindings reports visible wire Name labels that are folded
// vertically. A missing rotation is not assumed horizontal: it is surfaced once
// as unverified so an older connector cannot silently pass the rule.
func netLabelOrientationFindings(items []visibleTextItem) []checkFinding {
	var out []checkFinding
	missing := 0
	for _, it := range items {
		if it.Kind != "attribute" || it.Key != "Name" ||
			strings.TrimSpace(it.Value) == "" ||
			(it.Visible != nil && !*it.Visible) {
			continue
		}
		switch it.ComponentType {
		case "wire", "netflag", "netport", "netlabel":
		default:
			continue
		}
		if it.Rotation == nil {
			missing++
			continue
		}
		rot := math.Mod(*it.Rotation, 360)
		if rot < 0 {
			rot += 360
		}
		if rot == 90 || rot == 270 {
			out = append(out, checkFinding{
				Type: "label-orientation", Level: "warn",
				PrimitiveId: it.ID, BBox: it.BBox,
				Message: fmt.Sprintf("网络标签「%s」rotation=%g 竖排/折叠 — 改为水平显示",
					it.Value, *it.Rotation),
			})
		}
	}
	if missing > 0 {
		out = append(out, checkFinding{
			Type: "label-orientation-unverified", Level: "info",
			Message: fmt.Sprintf("%d 个网络标签没有 rotation 证据；竖排/折叠未核验（连接器需提供 rotation）", missing),
		})
	}
	return out
}

// netLabelEconomyFindings reports duplicate same-net labels on one physical
// wire island. Wires are joined only by shared endpoints here; this conservative
// union cannot invent connectivity across independent flat segments. Labels on
// the same primitive are always grouped together.
func netLabelEconomyFindings(items []visibleTextItem, wires []schGroupWire) []checkFinding {
	if len(wires) == 0 {
		return nil
	}
	find, wireIndex := wireTreeFinder(wires)

	type groupKey struct {
		root int
		net  string
	}
	groups := map[groupKey][]visibleTextItem{}
	for _, it := range items {
		if it.ComponentType != "wire" || it.Key != "Name" || it.ParentID == "" {
			continue
		}
		idx, ok := wireIndex[it.ParentID]
		if !ok {
			continue
		}
		k := groupKey{find(idx), it.Value}
		groups[k] = append(groups[k], it)
	}

	var out []checkFinding
	for k, group := range groups {
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].ID < group[j].ID })
		ids := make([]string, 0, len(group))
		var del []string
		var at *checkPoint
		for i, it := range group {
			ids = append(ids, it.ID)
			if i == 0 && it.BBox != nil {
				at = &checkPoint{X: round2(it.BBox.MinX), Y: round2(it.BBox.MinY)}
			}
			if i > 0 {
				del = append(del, it.ID)
			}
		}
		out = append(out, checkFinding{
			Type: "label-economy", Level: "warn",
			MarkerNet: k.net, PrimitiveIds: ids, SuggestKeepId: group[0].ID,
			SuggestDeleteIds: del, At: at, Count: len(group),
			Message: fmt.Sprintf("同一线岛上网络 '%s' 有 %d 个同名标签 — 只保留一个；建议保留 %s，删除 %s",
				k.net, len(group), group[0].ID, strings.Join(del, ",")),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SuggestKeepId < out[j].SuggestKeepId })
	return out
}

func wireTreeFinder(wires []schGroupWire) (func(int) int, map[string]int) {
	parent := make([]int, len(wires))
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
	union := func(a, b int) { parent[find(a)] = find(b) }

	pointOwner := map[string]int{}
	wireIndex := make(map[string]int, len(wires))
	endpoint := func(x, y float64) string {
		return fmt.Sprintf("%.3f,%.3f", x, y)
	}
	for i, w := range wires {
		wireIndex[w.ID] = i
		var pts []float64
		if len(w.Points) > 0 {
			pts = w.Points
		} else {
			for _, s := range w.ObservedSegments {
				pts = append(pts, s[0], s[1], s[2], s[3])
			}
		}
		for k := 0; k+1 < len(pts); k += 2 {
			key := endpoint(pts[k], pts[k+1])
			if other, ok := pointOwner[key]; ok {
				union(i, other)
			} else {
				pointOwner[key] = i
			}
		}
	}
	return find, wireIndex
}

// directConnectOpportunityFindings is deliberately conservative: it only
// reports a clear two-segment L route between two same-net labels on different
// wire islands. A multi-bend route, a blocked L, or missing geometry stays
// silent (false negatives are safer than suggesting a wire through obstacles).
func directConnectOpportunityFindings(items []visibleTextItem, comps []layoutComp, wires []schGroupWire) []checkFinding {
	if len(wires) == 0 {
		return nil
	}
	find, wireIndex := wireTreeFinder(wires)
	type label struct {
		it   visibleTextItem
		root int
		x, y float64
	}
	var labels []label
	for _, it := range items {
		if it.ComponentType != "wire" || it.Key != "Name" || it.ParentID == "" ||
			it.BBox == nil || strings.TrimSpace(it.Value) == "" ||
			isRailLabelNet(it.Value) {
			continue
		}
		idx, ok := wireIndex[it.ParentID]
		if !ok {
			continue
		}
		labels = append(labels, label{it, find(idx),
			(it.BBox.MinX + it.BBox.MaxX) / 2, (it.BBox.MinY + it.BBox.MaxY) / 2})
	}

	var obstacles []*layoutBBox
	for _, c := range comps {
		if c.BBox == nil || c.ComponentType == "sheet" {
			continue
		}
		if c.ComponentType == schLayoutPartType || isSchMarker(c.ComponentType) {
			obstacles = append(obstacles, c.BBox)
		}
	}
	pointOnSegment := func(px, py, ax, ay, bx, by float64) bool {
		cross := (bx-ax)*(py-ay) - (by-ay)*(px-ax)
		if math.Abs(cross) > 0.5 {
			return false
		}
		return px >= math.Min(ax, bx)-0.5 && px <= math.Max(ax, bx)+0.5 &&
			py >= math.Min(ay, by)-0.5 && py <= math.Max(ay, by)+0.5
	}
	segmentsIntersect := func(a, b, c, d [2]float64) bool {
		orient := func(p, q, r [2]float64) float64 {
			return (q[0]-p[0])*(r[1]-p[1]) - (q[1]-p[1])*(r[0]-p[0])
		}
		o1, o2, o3, o4 := orient(a, b, c), orient(a, b, d), orient(c, d, a), orient(c, d, b)
		if ((o1 > 0.5 && o2 < -0.5) || (o1 < -0.5 && o2 > 0.5)) &&
			((o3 > 0.5 && o4 < -0.5) || (o3 < -0.5 && o4 > 0.5)) {
			return true
		}
		return (math.Abs(o1) <= 0.5 && pointOnSegment(c[0], c[1], a[0], a[1], b[0], b[1])) ||
			(math.Abs(o2) <= 0.5 && pointOnSegment(d[0], d[1], a[0], a[1], b[0], b[1])) ||
			(math.Abs(o3) <= 0.5 && pointOnSegment(a[0], a[1], c[0], c[1], d[0], d[1])) ||
			(math.Abs(o4) <= 0.5 && pointOnSegment(b[0], b[1], c[0], c[1], d[0], d[1]))
	}
	wireSegments := func(w schGroupWire) [][4]float64 {
		if len(w.ObservedSegments) > 0 {
			return w.ObservedSegments
		}
		var out [][4]float64
		for i := 0; i+3 < len(w.Points); i += 2 {
			out = append(out, [4]float64{w.Points[i], w.Points[i+1], w.Points[i+2], w.Points[i+3]})
		}
		return out
	}
	clear := func(points [][2]float64, a, b label) bool {
		for i := 0; i+1 < len(points); i++ {
			p, q := points[i], points[i+1]
			if p == q {
				continue
			}
			for _, box := range obstacles {
				if schSegmentCrossesBox(p[0], p[1], q[0], q[1], *box) {
					return false
				}
			}
			for _, it := range items {
				if it.BBox == nil || it.ID == a.it.ID || it.ID == b.it.ID {
					continue
				}
				if schSegmentCrossesBox(p[0], p[1], q[0], q[1], *it.BBox) {
					return false
				}
			}
			for _, c := range comps {
				for _, pin := range c.Pins {
					if pointOnSegment(pin.X, pin.Y, p[0], p[1], q[0], q[1]) {
						return false
					}
				}
			}
			for _, w := range wires {
				idx := wireIndex[w.ID]
				if find(idx) == a.root || find(idx) == b.root {
					continue
				}
				for _, s := range wireSegments(w) {
					if segmentsIntersect(p, q, [2]float64{s[0], s[1]}, [2]float64{s[2], s[3]}) {
						return false
					}
				}
			}
		}
		return true
	}

	var out []checkFinding
	for i := 0; i < len(labels); i++ {
		for j := i + 1; j < len(labels); j++ {
			a, b := labels[i], labels[j]
			if a.root == b.root || a.it.Value != b.it.Value {
				continue
			}
			dist := math.Abs(a.x-b.x) + math.Abs(a.y-b.y)
			if dist > 250 {
				continue
			}
			candidates := [][][2]float64{
				{{a.x, a.y}, {b.x, a.y}, {b.x, b.y}},
				{{a.x, a.y}, {a.x, b.y}, {b.x, b.y}},
			}
			for _, path := range candidates {
				if clear(path, a, b) {
					out = append(out, checkFinding{
						Type: "direct-connect-opportunity", Level: "info",
						MarkerNet: a.it.Value, PrimitiveIds: []string{a.it.ID, b.it.ID},
						At: &checkPoint{X: round2(a.x), Y: round2(a.y)},
						Message: fmt.Sprintf("网络 '%s' 的两个标签 @(%.0f,%.0f) 与 @(%.0f,%.0f) 存在无遮挡短 L 路径（Manhattan=%.0f）— 优先直连并删除标签",
							a.it.Value, a.x, a.y, b.x, b.y, dist),
					})
					break
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MarkerNet != out[j].MarkerNet {
			return out[i].MarkerNet < out[j].MarkerNet
		}
		return out[i].Message < out[j].Message
	})
	return out
}

func isRailLabelNet(net string) bool {
	n := strings.ToLower(strings.TrimSpace(net))
	if isPowerNetName(net) || strings.Contains(n, "gnd") ||
		strings.Contains(n, "vss") || strings.Contains(n, "agnd") {
		return true
	}
	return strings.HasPrefix(n, "vout") || strings.HasPrefix(n, "vload") ||
		strings.HasPrefix(n, "dc")
}
