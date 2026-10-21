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
			if it.BBox == nil || it.BBox.MaxX-it.BBox.MinX <= 0.5 || it.BBox.MaxY-it.BBox.MinY <= 0.5 {
				continue
			}
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

// labelDistanceFindings catches a label whose text has drifted far from its
// wire (wire Name) or its symbol body (netflag/netport Name). The platform
// legitimately offsets text by a few units; 50 canvas units is already a
// detached-looking annotation, not normal label padding.
func labelDistanceFindings(items []visibleTextItem, comps []layoutComp, wires []schGroupWire, maxDist float64) []checkFinding {
	if maxDist <= 0 {
		maxDist = 50
	}
	find, wireIndex := wireTreeFinder(wires)
	compsByID := map[string]layoutComp{}
	for _, c := range comps {
		compsByID[c.ID] = c
	}
	var out []checkFinding
	for _, it := range items {
		if it.Kind != "attribute" || it.Key != "Name" || it.BBox == nil ||
			strings.TrimSpace(it.Value) == "" || (it.Visible != nil && !*it.Visible) {
			continue
		}
		cx := (it.BBox.MinX + it.BBox.MaxX) / 2
		cy := (it.BBox.MinY + it.BBox.MaxY) / 2
		distance := -1.0
		var nearest *checkPoint
		if it.ComponentType == "wire" && it.ParentID != "" {
			idx, ok := wireIndex[it.ParentID]
			if !ok {
				continue
			}
			root := find(idx)
			best := math.Inf(1)
			for _, w := range wires {
				wi, ok := wireIndex[w.ID]
				if !ok || find(wi) != root {
					continue
				}
				for _, s := range labelWireSegments(w) {
					x, y, d := closestPointOnSegment(cx, cy, s[0], s[1], s[2], s[3])
					if d < best {
						best = d
						nearest = &checkPoint{X: round2(x), Y: round2(y)}
					}
				}
			}
			if math.IsInf(best, 1) {
				continue
			}
			distance = best
		} else if isSchMarker(it.ComponentType) && it.ParentID != "" {
			c, ok := compsByID[it.ParentID]
			if !ok || c.BBox == nil {
				continue
			}
			dx := math.Max(0, math.Max(c.BBox.MinX-it.BBox.MaxX, it.BBox.MinX-c.BBox.MaxX))
			dy := math.Max(0, math.Max(c.BBox.MinY-it.BBox.MaxY, it.BBox.MinY-c.BBox.MaxY))
			distance = math.Hypot(dx, dy)
			nearest = &checkPoint{X: round2(c.X), Y: round2(c.Y)}
		} else {
			continue
		}
		if distance <= maxDist {
			continue
		}
		out = append(out, checkFinding{
			Type: "label-distance", Level: "warn", PrimitiveId: it.ID,
			Designator: it.Designator, BBox: it.BBox, At: nearest,
			Message: fmt.Sprintf("网络标签「%s」离所属%s约 %.0f 个画布单位（阈值 %.0f）— 移回线/符号旁，避免看起来像悬空标签",
				it.Value, map[bool]string{true: "导线", false: "电源/地符号"}[it.ComponentType == "wire"], distance, maxDist),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PrimitiveId < out[j].PrimitiveId })
	return out
}

// crossKindLabelEconomyFindings covers the one combination the existing rules
// deliberately split: a wire Name label and a netflag/netport/netlabel for the
// same net on the SAME physical wire island. label-economy only sees wire
// labels; redundant-net-marker only sees same-kind markers.
func crossKindLabelEconomyFindings(items []visibleTextItem, comps []layoutComp, wires []schGroupWire) []checkFinding {
	if len(wires) == 0 {
		return nil
	}
	find, wireIndex := wireTreeFinder(wires)
	visibleName := map[string]bool{}
	for _, it := range items {
		if it.Key == "Name" && strings.TrimSpace(it.Value) != "" &&
			(it.Visible == nil || *it.Visible) && it.ParentID != "" {
			visibleName[it.ParentID] = true
		}
	}
	rootAtPoint := func(x, y float64) (int, bool) {
		for _, w := range wires {
			if !wireTouchesPoint(w, x, y) {
				continue
			}
			if idx, ok := wireIndex[w.ID]; ok {
				return find(idx), true
			}
		}
		return 0, false
	}
	type group struct {
		root    int
		net     string
		labels  []visibleTextItem
		markers []layoutComp
	}
	groups := map[string]*group{}
	for _, it := range items {
		if it.ComponentType != "wire" || it.Key != "Name" || it.ParentID == "" || strings.TrimSpace(it.Value) == "" {
			continue
		}
		idx, ok := wireIndex[it.ParentID]
		if !ok {
			continue
		}
		root := find(idx)
		key := fmt.Sprintf("%d|%s", root, it.Value)
		g := groups[key]
		if g == nil {
			g = &group{root: root, net: it.Value}
			groups[key] = g
		}
		g.labels = append(g.labels, it)
	}
	for _, c := range comps {
		if !isSchMarker(c.ComponentType) || !c.AnchorAvailable || c.Net == "" {
			continue
		}
		root, ok := rootAtPoint(c.X, c.Y)
		if !ok {
			continue
		}
		key := fmt.Sprintf("%d|%s", root, c.Net)
		g := groups[key]
		if g == nil {
			g = &group{root: root, net: c.Net}
			groups[key] = g
		}
		g.markers = append(g.markers, c)
	}
	var out []checkFinding
	for _, g := range groups {
		if len(g.labels) == 0 || len(g.markers) == 0 {
			continue
		}
		sort.Slice(g.markers, func(i, j int) bool { return g.markers[i].ID < g.markers[j].ID })
		sort.Slice(g.labels, func(i, j int) bool { return g.labels[i].ID < g.labels[j].ID })
		ids := []string{}
		var keep string
		var deleteIDs []string
		if isRailLabelNet(g.net) {
			visibleMarkers := []layoutComp{}
			for _, m := range g.markers {
				if visibleName[m.ID] {
					visibleMarkers = append(visibleMarkers, m)
				}
			}
			for _, m := range g.markers {
				ids = append(ids, m.ID)
			}
			for _, l := range g.labels {
				ids = append(ids, l.ID)
			}
			if len(visibleMarkers) == 0 {
				out = append(out, checkFinding{
					Type: "cross-kind-label-economy", Level: "warn",
					MarkerNet: g.net, PrimitiveIds: ids, Count: len(ids),
					Message: fmt.Sprintf("同一power/ground线岛上网络 '%s' 同时有导线标签和隐藏名称的标识符号 — 先显示标识名称，再决定删除哪一个；当前不给删除建议", g.net),
				})
				continue
			}
			keep = visibleMarkers[0].ID
			for _, l := range g.labels {
				deleteIDs = append(deleteIDs, l.ID)
			}
			for _, m := range g.markers {
				if m.ID != keep {
					deleteIDs = append(deleteIDs, m.ID)
				}
			}
		} else {
			visibleLabels := []visibleTextItem{}
			for _, l := range g.labels {
				if l.Visible == nil || *l.Visible {
					visibleLabels = append(visibleLabels, l)
				}
			}
			for _, l := range g.labels {
				ids = append(ids, l.ID)
			}
			for _, m := range g.markers {
				ids = append(ids, m.ID)
			}
			if len(visibleLabels) == 0 {
				out = append(out, checkFinding{
					Type: "cross-kind-label-economy", Level: "warn",
					MarkerNet: g.net, PrimitiveIds: ids, Count: len(ids),
					Message: fmt.Sprintf("同一signal线岛上网络 '%s' 同时有隐藏导线标签和标识符号 — 先显示导线标签，再删除标识；当前不给删除建议", g.net),
				})
				continue
			}
			keep = visibleLabels[0].ID
			for _, m := range g.markers {
				deleteIDs = append(deleteIDs, m.ID)
			}
		}
		if len(deleteIDs) == 0 {
			continue
		}
		at := &checkPoint{X: round2(g.markers[0].X), Y: round2(g.markers[0].Y)}
		if len(g.labels) > 0 && g.labels[0].BBox != nil {
			at = &checkPoint{X: round2(g.labels[0].BBox.MinX), Y: round2(g.labels[0].BBox.MinY)}
		}
		kind := "signal"
		if isRailLabelNet(g.net) {
			kind = "power/ground"
		}
		out = append(out, checkFinding{
			Type: "cross-kind-label-economy", Level: "warn",
			MarkerNet: g.net, PrimitiveIds: ids,
			SuggestKeepId: keep, SuggestDeleteIds: deleteIDs, Count: len(ids), At: at,
			Message: fmt.Sprintf("同一%s线岛上网络 '%s' 同时有导线标签和标识符号 — 只保留一个；建议保留 %s，删除 %s",
				kind, g.net, keep, strings.Join(deleteIDs, ",")),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MarkerNet != out[j].MarkerNet {
			return out[i].MarkerNet < out[j].MarkerNet
		}
		return out[i].SuggestKeepId < out[j].SuggestKeepId
	})
	return out
}

// signalMarkerTypeFindings enforces the marker-type convention: signal nets are
// named by text-only net labels. A signal netflag or netport is a WARN unless a
// netport primitiveId is explicitly registered as a module-boundary exception;
// the exception remains visible as INFO.
func signalMarkerTypeFindings(comps []layoutComp, portExempt map[string]bool) []checkFinding {
	var out []checkFinding
	for _, c := range comps {
		if c.ComponentType != "netflag" && c.ComponentType != "netport" {
			continue
		}
		if c.Net == "" || isRailLabelNet(c.Net) {
			continue
		}
		level := "warn"
		typ := "signal-netflag-type"
		what := "电源旗标"
		if c.ComponentType == "netport" {
			what = "端口符号"
			typ = "signal-netport-type"
			if portExempt[c.ID] {
				out = append(out, checkFinding{
					Type: "signal-port-exempt", Level: "info",
					PrimitiveId: c.ID, Designator: c.Designator,
					ComponentType: c.ComponentType, MarkerNet: c.Net,
					At: &checkPoint{X: round2(c.X), Y: round2(c.Y)}, BBox: c.BBox,
					Message: fmt.Sprintf("信号网 '%s' 的模块边界端口显式豁免：%s", c.Net, c.ID),
				})
				continue
			}
		}
		out = append(out, checkFinding{
			Type: typ, Level: level, PrimitiveId: c.ID, Designator: c.Designator,
			ComponentType: c.ComponentType, MarkerNet: c.Net,
			At: &checkPoint{X: round2(c.X), Y: round2(c.Y)}, BBox: c.BBox,
			Message: fmt.Sprintf("信号网 '%s' 使用了%s — 信号命名优先使用网络标签；模块边界端口需显式说明例外", c.Net, what),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PrimitiveId < out[j].PrimitiveId })
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

// labelAttachmentPoint returns the point on the label's own wire tree closest
// to its measured text. A visible label's bbox center is where the glyphs are
// drawn, not where the label attaches to the wire; routing from the center can
// invent an L path that starts in empty space. Geometry checks and planners
// must both use this attachment point.
func labelAttachmentPoint(it visibleTextItem, wires []schGroupWire,
	wireIndex map[string]int, find func(int) int) (float64, float64, bool) {
	if it.BBox == nil || it.ParentID == "" {
		return 0, 0, false
	}
	idx, ok := wireIndex[it.ParentID]
	if !ok {
		return 0, 0, false
	}
	root := find(idx)
	px := (it.BBox.MinX + it.BBox.MaxX) / 2
	py := (it.BBox.MinY + it.BBox.MaxY) / 2
	best := math.Inf(1)
	var bx, by float64
	found := false
	for _, w := range wires {
		wi, ok := wireIndex[w.ID]
		if !ok || find(wi) != root {
			continue
		}
		for _, s := range labelWireSegments(w) {
			x, y, d := closestPointOnSegment(px, py, s[0], s[1], s[2], s[3])
			if d < best {
				best, bx, by, found = d, x, y, true
			}
		}
	}
	return bx, by, found
}

func closestPointOnSegment(px, py, x0, y0, x1, y1 float64) (float64, float64, float64) {
	dx, dy := x1-x0, y1-y0
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = ((px-x0)*dx + (py-y0)*dy) / l2
		t = math.Max(0, math.Min(1, t))
	}
	x, y := x0+t*dx, y0+t*dy
	return x, y, math.Hypot(px-x, py-y)
}

// directConnectOpportunityFindings is deliberately conservative: it only
// reports a clear two-segment L route between two same-net labels on different
// wire islands. A multi-bend route, a blocked L, or missing geometry stays
// silent (false negatives are safer than suggesting a wire through obstacles).
func directConnectOpportunityFindings(items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox) []checkFinding {
	if len(wires) == 0 {
		return nil
	}
	find, wireIndex := wireTreeFinder(wires)
	type label struct {
		it     visibleTextItem
		root   int
		ax, ay float64 // attachment point on the wire tree (route endpoint)
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
		ax, ay, ok := labelAttachmentPoint(it, wires, wireIndex, find)
		if !ok {
			continue
		}
		labels = append(labels, label{it: it, root: find(idx), ax: ax, ay: ay})
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
	samePoint := func(px, py, qx, qy float64) bool {
		return math.Abs(px-qx) <= 0.5 && math.Abs(py-qy) <= 0.5
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
					if samePoint(pin.X, pin.Y, points[0][0], points[0][1]) ||
						samePoint(pin.X, pin.Y, points[len(points)-1][0], points[len(points)-1][1]) {
						continue
					}
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
			dist := math.Abs(a.ax-b.ax) + math.Abs(a.ay-b.ay)
			if dist > 250 {
				continue
			}
			candidates := [][][2]float64{
				{{a.ax, a.ay}, {b.ax, a.ay}, {b.ax, b.ay}},
				{{a.ax, a.ay}, {a.ax, b.ay}, {b.ax, b.ay}},
			}
			candidates = filterDCPaths(candidates, sheet, titleBlock)
			found := false
			for _, path := range candidates {
				if clear(path, a, b) {
					out = append(out, checkFinding{
						Type: "direct-connect-opportunity", Level: "info",
						MarkerNet: a.it.Value, PrimitiveIds: []string{a.it.ID, b.it.ID},
						At: &checkPoint{X: round2(a.ax), Y: round2(a.ay)},
						Message: fmt.Sprintf("网络 '%s' 的两个标签 @(%.0f,%.0f) 与 @(%.0f,%.0f) 存在无遮挡短 L 路径（Manhattan=%.0f）— 优先直连并删除标签",
							a.it.Value, a.ax, a.ay, b.ax, b.ay, dist),
					})
					found = true
					break
				}
			}
			if !found {
				if path, ok := dcMazePath([2]float64{a.ax, a.ay}, [2]float64{b.ax, b.ay},
					a.root, b.root, a.it.ID, b.it.ID, items, comps, wires, find, wireIndex,
					sheet, titleBlock, math.Max(100, dist*2)); ok && clear(path, a, b) {
					out = append(out, checkFinding{
						Type: "direct-connect-opportunity", Level: "info",
						MarkerNet: a.it.Value, PrimitiveIds: []string{a.it.ID, b.it.ID},
						At: &checkPoint{X: round2(a.ax), Y: round2(a.ay)},
						Message: fmt.Sprintf("网络 '%s' 的两个标签 @(%.0f,%.0f) 与 @(%.0f,%.0f) 存在无遮挡正交绕行路径（%d 段）— 优先直连并删除标签",
							a.it.Value, a.ax, a.ay, b.ax, b.ay, len(path)-1),
					})
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
		strings.HasPrefix(n, "vbias") || strings.HasPrefix(n, "dc")
}
