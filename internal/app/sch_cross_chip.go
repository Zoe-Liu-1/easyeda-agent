package app

import (
	"fmt"
	"sort"
	"strings"
)

// crossChipDirectWireFindings rejects a direct wire tree that joins pins from
// two different active chips (multi-pin U/Q/IC parts) on the same signal net.
// Chip-to-chip connectivity remains legal by same-name net labels or explicit
// module ports; the visual long wire between separate chip blocks does not.
func crossChipDirectWireFindings(comps []layoutComp, wires []schGroupWire) []checkFinding {
	if len(wires) == 0 {
		return nil
	}
	find, _ := wireTreeFinder(wires)
	type rootInfo struct {
		chips  map[string]bool
		nets   map[string]bool
		wireID string
		at     checkPoint
	}
	roots := map[int]*rootInfo{}
	for wi, wire := range wires {
		root := find(wi)
		info := roots[root]
		if info == nil {
			info = &rootInfo{chips: map[string]bool{}, nets: map[string]bool{}}
			roots[root] = info
		}
		if strings.TrimSpace(wire.Net) != "" {
			info.nets[strings.TrimSpace(wire.Net)] = true
		}
		for _, c := range comps {
			if !alignmentActivePart(c) {
				continue
			}
			for _, pin := range c.Pins {
				if !wireTouchesPoint(wire, pin.X, pin.Y) {
					continue
				}
				info.chips[c.Designator] = true
				if strings.TrimSpace(pin.Net) != "" {
					info.nets[strings.TrimSpace(pin.Net)] = true
				}
				if info.wireID == "" {
					info.wireID = wire.ID
					info.at = checkPoint{X: round2(pin.X), Y: round2(pin.Y)}
				}
			}
		}
	}
	var out []checkFinding
	for _, info := range roots {
		if len(info.chips) < 2 {
			continue
		}
		allRail := len(info.nets) > 0
		for net := range info.nets {
			if !isRailLabelNet(net) {
				allRail = false
				break
			}
		}
		if allRail {
			continue
		}
		chips := make([]string, 0, len(info.chips))
		for chip := range info.chips {
			chips = append(chips, chip)
		}
		sort.Strings(chips)
		nets := make([]string, 0, len(info.nets))
		for net := range info.nets {
			nets = append(nets, net)
		}
		sort.Strings(nets)
		out = append(out, checkFinding{
			Type: "cross-chip-direct-wire", Level: "warn",
			WirePrimitiveId: info.wireID, MarkerNet: strings.Join(nets, ","),
			Designator: strings.Join(chips, ","), At: &info.at,
			Message: fmt.Sprintf("不同主动芯片 %s 被同一物理导线树直接连通 — 改用同名网络标签；只有同一芯片功能块内的器件才保持真实短线直连",
				strings.Join(chips, "/")),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Designator != out[j].Designator {
			return out[i].Designator < out[j].Designator
		}
		return out[i].MarkerNet < out[j].MarkerNet
	})
	return out
}

func wireIslandActiveChips(comps []layoutComp, wires []schGroupWire) map[int]map[string]bool {
	find, _ := wireTreeFinder(wires)
	out := map[int]map[string]bool{}
	for wi, wire := range wires {
		root := find(wi)
		for _, c := range comps {
			if !alignmentActivePart(c) {
				continue
			}
			for _, pin := range c.Pins {
				if !wireTouchesPoint(wire, pin.X, pin.Y) {
					continue
				}
				if out[root] == nil {
					out[root] = map[string]bool{}
				}
				out[root][c.Designator] = true
				break
			}
		}
	}
	return out
}

// labelsJoinDifferentChips blocks a direct wire between separate active chips.
// An island with no active chip (for example a local passive) is still eligible.
func labelsJoinDifferentChips(chips map[int]map[string]bool, ra, rb int) bool {
	a, b := chips[ra], chips[rb]
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	for chip := range a {
		if b[chip] {
			return false
		}
	}
	return true
}
