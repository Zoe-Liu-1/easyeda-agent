package app

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/zhoushoujianwork/easyeda-agent/internal/blocks"
	"github.com/zhoushoujianwork/easyeda-agent/internal/connectivity"
)

// peripheralDirectFinding is sch check's live backstop for the SKILL rule
// "同名网络、同框或高分不能代替要求的外围实际直连". Until 2026-10-13 it was text
// only: the ESP32-S3 demo shipped every decoupler/pull-up as a lone part joined
// to U1 by net labels, and every gate passed. It judges the page's registered
// modules (sch groups) with compose's own ruler — schematicObservedOwnershipLayout
// rebuilds wire roots from a fresh components.list, ValidateSchematicPeripheralDirect
// decides — so a page compose would refuse is never green here.
// schSimplePeripheralMaxPins: parts with at most this many pins count as simple
// peripherals that must be wired to their core (2-pin passives, 4-pin crystals).
const schSimplePeripheralMaxPins = 4

func isSchModuleCoreRef(ref string) bool {
	d := strings.ToUpper(strings.TrimSpace(ref))
	return strings.HasPrefix(d, "U") || strings.HasPrefix(d, "Q") || strings.HasPrefix(d, "IC")
}

func isSchModuleBoundaryRef(ref string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(ref)), "J")
}

func peripheralDirectFinding(cfg *appConfig, window string, comps []layoutComp, stderr io.Writer) []checkFinding {
	parts := 0
	for _, c := range comps {
		if c.ComponentType == schLayoutPartType {
			parts++
		}
	}
	if parts < 2 {
		return nil // no core+peripheral pair can exist; skip the registry/wire reads
	}
	_, win, _, _, _, groups, err := loadSchGroupsContext(cfg, window)
	if err != nil || len(groups) == 0 {
		return nil // no module registry → no ownership to judge (partition rule covers the page)
	}
	res, err := requestAction(cfg, "schematic.components.list", win, map[string]any{"includePins": true, "includeWires": true})
	if err != nil {
		fmt.Fprintf(stderr, "sch check: peripheral-direct skipped — %v\n", err)
		return []checkFinding{{Type: "peripheral-direct-unverified", Level: "warn", Message: "外围直连无法核验(读取失败): " + err.Error()}}
	}
	return peripheralDirectFindingsFor(res.Result, groups)
}

// peripheralDirectFindingsFor is the pure core. Groups of one block instance
// merge into one module (block-apply registers per-function subgroups); the core
// is the member with the most pins. Each module is validated on its own, every
// other part standing as a solo module, so a failure names its module.
func peripheralDirectFindingsFor(result map[string]any, groups []*schGroup) []checkFinding {
	pinCount := map[string]int{}
	for _, c := range mapsField(result, "components") {
		if strField(c, "componentType") == schLayoutPartType {
			pins, _ := c["pins"].([]any)
			pinCount[strField(c, "designator")] = len(pins)
		}
	}
	type module struct {
		name    string
		members []string
		tile    bool // a frame of a tiled block (blocks.SchematicTile)
	}
	var modules []*module
	byInstance := map[string]*module{}
	for _, g := range groups {
		m := &module{name: g.Name}
		if m.name == "" {
			m.name = g.ID
		}
		if g.BlockID != "" && g.Instance != "" {
			key := g.BlockID + "\x00" + g.Instance
			if prev := byInstance[key]; prev != nil {
				m = prev
			} else {
				m.name = g.BlockID + "#" + g.Instance
				m.tile = strings.Contains(g.BlockID, blocks.TileSeparator)
				byInstance[key] = m
				modules = append(modules, m)
			}
		} else {
			modules = append(modules, m)
		}
		for _, ref := range g.Members {
			if _, ok := pinCount[ref]; ok {
				m.members = append(m.members, ref)
			}
		}
	}
	ids := map[string]string{}
	for ref := range pinCount {
		ids[ref] = ref
	}
	var out []checkFinding
	for _, m := range modules {
		if len(m.members) < 2 {
			continue // a lone part has no peripheral to attach
		}
		members := append([]string(nil), m.members...)
		sort.Slice(members, func(i, j int) bool {
			if pinCount[members[i]] != pinCount[members[j]] {
				return pinCount[members[i]] > pinCount[members[j]]
			}
			return members[i] < members[j]
		})
		// Only simple peripherals must be wired to a core. Every multi-pin active
		// device in the module is a legitimate core: a functional block can contain
		// more than one peer IC (for example two current monitors), and a passive
		// attached to any one of them is still physically attached inside the group.
		// Pin count stands in for the part class block-apply uses
		// (bslIsSimplePeripheral): passives have 2 pins, crystals 4.
		var cores []string
		for _, ref := range members {
			if pinCount[ref] > schSimplePeripheralMaxPins && isSchModuleCoreRef(ref) {
				cores = append(cores, ref)
			}
		}
		// Multi-pin connectors are interface/power boundaries. Once an active
		// core exists, they are legitimate local attachment anchors rather than
		// peripherals that must be chased back to a chip with long wires.
		if len(cores) > 0 {
			for _, ref := range members {
				if pinCount[ref] >= 3 && isSchModuleBoundaryRef(ref) {
					cores = append(cores, ref)
				}
			}
		}
		// A frame of a tiled block made only of 2-pin parts (decoupling bank, a
		// pull-up row) has no core: its parts join the chip through power symbols
		// and labels, the reference-schematic convention the tile exists for.
		// Ordinary passive-only groups retain the historical largest-part anchor so
		// an incomplete module is still reported rather than silently skipped.
		if len(cores) == 0 {
			if m.tile {
				continue
			}
			cores = []string{members[0]}
		}
		owned := map[string]bool{}
		for _, ref := range cores {
			owned[ref] = true
		}
		var peripherals []string
		for _, ref := range members {
			if owned[ref] {
				continue
			}
			if pinCount[ref] <= schSimplePeripheralMaxPins {
				owned[ref] = true
				peripherals = append(peripherals, ref)
			}
		}
		if len(peripherals) == 0 {
			continue
		}
		mods := []connectivity.Module{{ID: m.name, CoreComponents: cores, PeripheralComponents: peripherals}}
		for ref := range pinCount {
			if !owned[ref] {
				mods = append(mods, connectivity.Module{ID: "solo:" + ref, CoreComponents: []string{ref}})
			}
		}
		layout, err := schematicObservedOwnershipLayout(result, ids)
		if err == nil {
			err = ValidateSchematicPeripheralDirect(layout, mods)
		}
		if err == nil {
			continue
		}
		out = append(out, checkFinding{
			Type: "peripheral-not-direct", Level: "warn", Designator: members[0],
			Message: fmt.Sprintf("模块 %s(核心 %s,外围 %s)没有全部用实际导线接到核心: %v — 同名标签不算直连;按 auto-layout-sop §2 走 `sch lib-layout` → `sch compose --preserve-instances` 重建",
				m.name, strings.Join(cores, ","), strings.Join(peripherals, ","), err),
		})
	}
	return out
}
