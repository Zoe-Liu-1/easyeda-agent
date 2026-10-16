package app

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Fixtures are trimmed live components.list reads of the 2026-10-13 ESP32-S3
// demo page P1 (ceshi): labels-only.json is the block-apply/autoconnect result
// (every decoupler/pull-up a lone part tied to U1 by net labels, all gates green),
// composed.json the same parts after `sch compose --preserve-instances`.
func TestPeripheralDirectFindingCatchesLabelOnlyModules(t *testing.T) {
	load := func(name string) map[string]any {
		raw, err := os.ReadFile("testdata/peripheral-direct/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	groups := []*schGroup{
		{ID: "g1", Name: "MCU", Members: []string{"U1", "C3", "C1", "R1", "C2", "R2"}},
		{ID: "g2", Name: "LED", Members: []string{"LED1", "R3"}},
		{ID: "g3", Name: "PWR", Members: []string{"J1"}},
	}
	got := peripheralDirectFindingsFor(load("labels-only.json"), groups)
	if len(got) == 0 || got[0].Type != "peripheral-not-direct" || !strings.Contains(got[0].Message, "MCU") || got[0].Designator != "U1" {
		t.Fatalf("label-only MCU module must be flagged with U1 as core, got %+v", got)
	}
	if got := peripheralDirectFindingsFor(load("composed.json"), groups); len(got) != 0 {
		t.Fatalf("composed page passes compose's own ruler, got %+v", got)
	}
	// block-apply subgroups of one instance merge: U1 is in a different subgroup
	// than its decouplers, yet they form one module with U1 as core.
	sub := []*schGroup{
		{ID: "g1", BlockID: "esp32", Instance: "i1", Members: []string{"C1", "C3"}},
		{ID: "g2", BlockID: "esp32", Instance: "i1", Members: []string{"U1"}},
	}
	if got := peripheralDirectFindingsFor(load("composed.json"), sub); len(got) != 0 {
		t.Fatalf("merged subgroups judge as one module, got %+v", got)
	}
}

// User correction 2026-10-16: a module's simple peripherals are wired to the
// core, but another IC in it (an SPI flash) is joined by net labels by design.
// A label-only ≥5-pin member must not fail the check; a label-only 2-pin member
// still must.
func TestPeripheralDirectFindingExemptsLabelJoinedICs(t *testing.T) {
	load := func() map[string]any {
		raw, err := os.ReadFile("testdata/peripheral-direct/composed.json")
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	// clone copies a component of the fixture under a new designator, shifted
	// far from everything so it touches no wire (label-only by construction).
	clone := func(page map[string]any, from, to string) {
		comps := page["components"].([]any)
		for _, c := range comps {
			m := c.(map[string]any)
			if m["designator"] != from {
				continue
			}
			raw, _ := json.Marshal(m)
			var cp map[string]any
			_ = json.Unmarshal(raw, &cp)
			cp["designator"], cp["primitiveId"] = to, "id-"+to
			for _, p := range cp["pins"].([]any) {
				pm := p.(map[string]any)
				pm["x"] = pm["x"].(float64) + 5000
			}
			page["components"] = append(comps, cp)
			return
		}
		t.Fatalf("fixture has no %s", from)
	}
	groups := func(extra string) []*schGroup {
		return []*schGroup{{ID: "g1", Name: "MCU", Members: []string{"U1", "C3", "C1", "R1", "C2", "R2", extra}}}
	}
	ic := load()
	clone(ic, "U1", "U9")
	if got := peripheralDirectFindingsFor(ic, groups("U9")); len(got) != 0 {
		t.Fatalf("a label-joined IC in the module must not fail peripheral-direct, got %+v", got)
	}
	cap := load()
	clone(cap, "C1", "C9")
	if got := peripheralDirectFindingsFor(cap, groups("C9")); len(got) == 0 {
		t.Fatal("a label-only 2-pin peripheral must still fail peripheral-direct")
	}
}

// A frame of a tiled block holding only 2-pin parts (a decoupling bank) has no
// core: its caps reach the chip through power symbols by design. The same
// label-joined caps registered as an ordinary block still must be wired.
func TestPeripheralDirectSkipsPassiveOnlyTile(t *testing.T) {
	raw, err := os.ReadFile("testdata/peripheral-direct/composed.json")
	if err != nil {
		t.Fatal(err)
	}
	var page map[string]any
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	comps := page["components"].([]any)
	for _, c := range comps {
		m := c.(map[string]any)
		if m["designator"] != "C1" {
			continue
		}
		b, _ := json.Marshal(m)
		var cp map[string]any
		_ = json.Unmarshal(b, &cp)
		cp["designator"], cp["primitiveId"] = "C9", "id-C9"
		for _, p := range cp["pins"].([]any) {
			pm := p.(map[string]any)
			pm["x"] = pm["x"].(float64) + 5000
		}
		page["components"] = append(comps, cp)
		break
	}
	bank := func(blockID string) []*schGroup {
		return []*schGroup{{ID: "g1", Name: "bank", BlockID: blockID, Instance: "C9", Members: []string{"C9", "C2"}}}
	}
	if got := peripheralDirectFindingsFor(page, bank("block.esp32s3r8_chip_minsys#decap")); len(got) != 0 {
		t.Fatalf("a passive-only tile must not fail peripheral-direct, got %+v", got)
	}
	if got := peripheralDirectFindingsFor(page, bank("block.led_indicator_gpio")); len(got) == 0 {
		t.Fatal("the same unwired passives in an ordinary block must still fail peripheral-direct")
	}
}
