package app

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// netLabelOrientationFindings reports visible wire Name labels that are folded
// vertically. A missing rotation is not assumed horizontal: it is surfaced once
// as unverified so an older connector cannot silently pass the rule.
func netLabelOrientationFindings(items []visibleTextItem) []checkFinding {
	var out []checkFinding
	missing := 0
	for _, it := range items {
		if it.ComponentType != "wire" || it.Key != "Name" {
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
