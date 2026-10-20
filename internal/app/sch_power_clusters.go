package app

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// power-cluster-candidate is the mechanical form of the local power rule:
// a consumer power pin and nearby local passives should share one physical
// cluster and one power symbol. It deliberately does NOT fire for GND (local
// ground symbols are commonly repeated) or for already-wired clusters.

const powerClusterRadius = 250.0

type powerClusterDSU struct {
	p []int
}

func newPowerClusterDSU(n int) *powerClusterDSU {
	u := &powerClusterDSU{p: make([]int, n)}
	for i := range u.p {
		u.p[i] = i
	}
	return u
}

func (u *powerClusterDSU) find(a int) int {
	for u.p[a] != a {
		u.p[a] = u.p[u.p[a]]
		a = u.p[a]
	}
	return a
}

func (u *powerClusterDSU) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.p[rb] = ra
	}
}

func powerPinKey(c layoutComp, p layoutPin) string {
	return c.ID + ":" + p.Number
}

// powerConnectivity returns the electrical root of every pin and marker on the
// page. Wire trees are unioned first; pins and marker anchors then union the
// roots they physically touch, so a pin-to-pin/T-junction counts as one cluster.
func powerConnectivity(comps []layoutComp, wires []schGroupWire) (map[string]int, map[string]int) {
	find, wireIndex := wireTreeFinder(wires)
	rootIndex := map[int]int{}
	for _, w := range wires {
		r := find(wireIndex[w.ID])
		if _, ok := rootIndex[r]; !ok {
			rootIndex[r] = len(rootIndex)
		}
	}
	type pinRef struct {
		c layoutComp
		p layoutPin
	}
	var pins []pinRef
	for _, c := range comps {
		for _, p := range c.Pins {
			pins = append(pins, pinRef{c: c, p: p})
		}
	}
	var flags []layoutComp
	for _, c := range comps {
		if isSchMarker(c.ComponentType) && c.AnchorAvailable && c.Net != "" {
			flags = append(flags, c)
		}
	}
	u := newPowerClusterDSU(len(rootIndex) + len(pins) + len(flags))
	pinBase := len(rootIndex)
	flagBase := pinBase + len(pins)

	wireRootsAt := func(x, y float64) []int {
		seen := map[int]bool{}
		var out []int
		for _, w := range wires {
			if !wireTouchesPoint(w, x, y) {
				continue
			}
			r := rootIndex[find(wireIndex[w.ID])]
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
		return out
	}

	pinRoot := map[string]int{}
	for i, ref := range pins {
		node := pinBase + i
		for _, r := range wireRootsAt(ref.p.X, ref.p.Y) {
			u.union(node, r)
		}
	}
	flagRoot := map[string]int{}
	for i, f := range flags {
		node := flagBase + i
		for _, r := range wireRootsAt(f.X, f.Y) {
			u.union(node, r)
		}
	}
	// Resolve roots only after ALL pin/flag unions. Unioning a later flag can
	// change the representative of an earlier pin node.
	for i, ref := range pins {
		pinRoot[powerPinKey(ref.c, ref.p)] = u.find(pinBase + i)
	}
	for i, f := range flags {
		flagRoot[f.ID] = u.find(flagBase + i)
	}
	return pinRoot, flagRoot
}

func isPowerClusterGround(net string) bool {
	n := strings.ToLower(net)
	return strings.Contains(n, "gnd") || strings.Contains(n, "vss") ||
		strings.Contains(n, "agnd") || strings.Contains(n, "pgnd") ||
		strings.Contains(n, "dgnd")
}

func isPowerClusterActive(designator string) bool {
	d := strings.ToUpper(strings.TrimSpace(designator))
	return strings.HasPrefix(d, "U") || strings.HasPrefix(d, "J") || strings.HasPrefix(d, "Q")
}

func isPowerClusterPassive(designator string) bool {
	d := strings.ToUpper(strings.TrimSpace(designator))
	for _, p := range []string{"R", "C", "L", "D"} {
		if strings.HasPrefix(d, p) {
			return true
		}
	}
	return false
}

type powerClusterPassive struct {
	c    layoutComp
	p    layoutPin
	dist float64
}

type powerClusterGroup struct {
	consumer layoutComp
	rail     string
	passives []powerClusterPassive
}

// powerClusterFindings flags local power clusters where a consumer pin and
// nearby local passives sit on different wire roots, each naming the rail with
// its own flag. Same-root duplicates remain the job of redundant-net-marker.
func powerClusterFindings(comps []layoutComp, wires []schGroupWire) []checkFinding {
	if len(wires) == 0 {
		return nil
	}
	pinRoot, flagRoot := powerConnectivity(comps, wires)
	var flags []layoutComp
	for _, c := range comps {
		if isSchMarker(c.ComponentType) && c.AnchorAvailable && c.Net != "" {
			flags = append(flags, c)
		}
	}
	type consumerPin struct {
		c layoutComp
		p layoutPin
	}
	consumersByRail := map[string][]consumerPin{}
	passivesByRail := map[string][]consumerPin{}
	for _, c := range comps {
		if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
			continue
		}
		for _, p := range c.Pins {
			net := strings.TrimSpace(p.Net)
			if net == "" || !isRailLabelNet(net) || isPowerClusterGround(net) {
				continue
			}
			if isPowerClusterActive(c.Designator) {
				consumersByRail[net] = append(consumersByRail[net], consumerPin{c: c, p: p})
			} else if isPowerClusterPassive(c.Designator) {
				passivesByRail[net] = append(passivesByRail[net], consumerPin{c: c, p: p})
			}
		}
	}

	groups := map[string]*powerClusterGroup{}
	for rail, passives := range passivesByRail {
		consumers := consumersByRail[rail]
		if len(consumers) == 0 {
			continue
		}
		for _, passive := range passives {
			best := consumers[0]
			bestDist := manhattan(passive.p.X, passive.p.Y, best.p.X, best.p.Y)
			for _, c := range consumers[1:] {
				d := manhattan(passive.p.X, passive.p.Y, c.p.X, c.p.Y)
				if d < bestDist {
					best, bestDist = c, d
				}
			}
			if bestDist > powerClusterRadius {
				continue
			}
			key := rail + "|" + best.c.ID
			g := groups[key]
			if g == nil {
				g = &powerClusterGroup{consumer: best.c, rail: rail}
				groups[key] = g
			}
			g.passives = append(g.passives, powerClusterPassive{c: passive.c, p: passive.p, dist: bestDist})
		}
	}

	var out []checkFinding
	for _, g := range groups {
		roots := map[int]bool{}
		consumerRoots := map[int]bool{}
		pins := []string{}
		var at *checkPoint
		for _, p := range consumersByRail[g.rail] {
			if p.c.ID != g.consumer.ID {
				continue
			}
			if r, ok := pinRoot[powerPinKey(p.c, p.p)]; ok {
				roots[r] = true
				consumerRoots[r] = true
			}
			pins = append(pins, fmt.Sprintf("%s.%s", p.c.Designator, p.p.Number))
			if at == nil {
				at = &checkPoint{X: round2(p.p.X), Y: round2(p.p.Y)}
			}
		}
		for _, p := range g.passives {
			if r, ok := pinRoot[powerPinKey(p.c, p.p)]; ok {
				roots[r] = true
			}
			pins = append(pins, fmt.Sprintf("%s.%s", p.c.Designator, p.p.Number))
		}
		if len(roots) <= 1 {
			continue // already one physical island
		}
		var clusterFlags []layoutComp
		for _, f := range flags {
			if f.Net != g.rail {
				continue
			}
			if r, ok := flagRoot[f.ID]; ok && roots[r] {
				clusterFlags = append(clusterFlags, f)
			}
		}
		if len(clusterFlags) < 2 {
			continue
		}
		sort.Slice(clusterFlags, func(i, j int) bool { return clusterFlags[i].ID < clusterFlags[j].ID })
		keep := clusterFlags[0]
		for _, f := range clusterFlags {
			if r, ok := flagRoot[f.ID]; ok && consumerRoots[r] {
				keep = f
				break
			}
		}
		deleteIDs := []string{}
		for _, f := range clusterFlags {
			if f.ID == keep.ID {
				continue
			}
			deleteIDs = append(deleteIDs, f.ID)
		}
		if len(deleteIDs) == 0 {
			continue
		}
		parts := make([]string, 0, len(g.passives))
		for _, p := range g.passives {
			parts = append(parts, p.c.Designator)
		}
		msg := fmt.Sprintf("电源局部簇：%s(%s) 与本地被动件 %s 分属 %d 个线岛，且各有同名电源符号 — 先物理接入消费脚，再合并为一个符号（建议保留 %s，删除 %s）",
			pins[0], g.rail, strings.Join(parts, "/"), len(roots), keep.ID, strings.Join(deleteIDs, ","))
		if at == nil {
			at = &checkPoint{X: round2(g.consumer.X), Y: round2(g.consumer.Y)}
		}
		out = append(out, checkFinding{
			Type: "power-cluster-candidate", Level: "warn",
			Designator: g.consumer.Designator, MarkerNet: g.rail,
			Pins: pins, PrimitiveIds: func() []string {
				ids := make([]string, len(clusterFlags))
				for i, f := range clusterFlags {
					ids[i] = f.ID
				}
				return ids
			}(),
			SuggestKeepId: keep.ID, SuggestDeleteIds: deleteIDs, Count: len(roots),
			At:      at,
			Message: msg,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MarkerNet != out[j].MarkerNet {
			return out[i].MarkerNet < out[j].MarkerNet
		}
		return out[i].Designator < out[j].Designator
	})
	return out
}

func manhattan(ax, ay, bx, by float64) float64 {
	return math.Abs(ax-bx) + math.Abs(ay-by)
}
