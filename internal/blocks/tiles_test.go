package blocks

import (
	"encoding/json"
	"strings"
	"testing"
)

// The ESP32-S3R8 chip block is tiled: the core alone, the crystal with its load
// capacitors, and so on. Each tile must come back from Get as a valid block whose
// crossing nets are ports with the SAME name in every tile — that shared name is
// the label that joins the tiles on the sheet.
func TestTiledChipBlockDerivesLinkedTiles(t *testing.T) {
	core, ok, err := Get("esp32s3r8_chip_minsys#core")
	if err != nil || !ok {
		t.Fatalf("core tile: ok=%v err=%v", ok, err)
	}
	xtal, ok, err := Get("block.esp32s3r8_chip_minsys#xtal")
	if err != nil || !ok {
		t.Fatalf("xtal tile: ok=%v err=%v", ok, err)
	}
	if len(core.Parts) != 1 || core.Parts["U"].Part == "" {
		t.Fatalf("core tile must hold only U, got %v", core.Parts)
	}
	for _, name := range []string{"XTAL_P", "XTAL_N"} {
		cp, cok := core.Ports[name]
		xp, xok := xtal.Ports[name]
		if !cok || !xok || cp.DefaultNet != name || xp.DefaultNet != name {
			t.Fatalf("%s must be a port named alike on both tiles: core=%+v xtal=%+v", name, cp, xp)
		}
	}
	if xtal.Ports["XTAL_P"].At != "X.1" || core.Ports["XTAL_P"].At != "U.XTAL_P" {
		t.Fatalf("tile ports must anchor on the tile's own pin: %+v / %+v", xtal.Ports["XTAL_P"], core.Ports["XTAL_P"])
	}
	if p, ok := xtal.Ports["GND"]; !ok || p.DefaultNet != "GND" {
		t.Fatalf("a parent port keeps its name and default net in every tile that touches it: %+v", p)
	}
	for _, tile := range []Block{core, xtal} {
		if errs := Validate(tile); len(errs) != 0 {
			t.Fatalf("%s: %v", tile.ID, errs)
		}
	}
	layout, err := xtal.SchematicLayout()
	if err != nil || layout == nil || layout.Anchor != "X" || layout.Attach["C_X1"] != "X.1" {
		t.Fatalf("xtal keeps its own relational layout: %+v %v", layout, err)
	}
}

func tiledTestBlock(t *testing.T, layout string) Block {
	t.Helper()
	raw := `{"id":"block.t","desc":"d","category":"mcu","author":"a","source":"s",
	"parts":{"U":{"part":"ic.x","qty":1},"C1":{"part":"cap.x","qty":1},"R1":{"part":"res.x","qty":1}},
	"internal_nets":[["U.VDD","C1.1","PORT:VCC"],["U.GND","C1.2","PORT:GND"],["U.4","R1.1"],["U.EN","R1.2"]],
	"ports":{"VCC":{"dir":"in","at":"U.VDD"},"GND":{"dir":"bidir","at":"U.GND"}},
	"schematic_layout":` + layout + `}`
	var b Block
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	b.Raw = []byte(raw)
	return b
}

// A numeric chip pin has no function name: the link falls back to ROLE_PIN.
func TestTileNetNamesFallBackForNumericPins(t *testing.T) {
	b := tiledTestBlock(t, `{"tiles":[{"name":"core","roles":["U"]},{"name":"aux","roles":["C1","R1"]}]}`)
	aux, err := b.Tile("aux")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := aux.Ports["U_4"]; !ok {
		t.Fatalf("numeric pin U.4 must link as U_4, ports=%v", aux.Ports)
	}
	if _, ok := aux.Ports["EN"]; !ok {
		t.Fatalf("named pin U.EN must link as EN, ports=%v", aux.Ports)
	}
	if errs := Validate(b); len(errs) != 0 {
		t.Fatalf("valid tiled block rejected: %v", errs)
	}
}

func TestTiledLayoutValidation(t *testing.T) {
	for name, layout := range map[string]string{
		"role in no tile":        `{"tiles":[{"name":"core","roles":["U"]},{"name":"aux","roles":["C1"]}]}`,
		"role in two tiles":      `{"tiles":[{"name":"core","roles":["U","C1"]},{"name":"aux","roles":["C1","R1"]}]}`,
		"mixed with top anchor":  `{"anchor":"U","tiles":[{"name":"core","roles":["U"]},{"name":"aux","roles":["C1","R1"]}]}`,
		"separator in tile name": `{"tiles":[{"name":"co#re","roles":["U"]},{"name":"aux","roles":["C1","R1"]}]}`,
		"bad tile layout":        `{"tiles":[{"name":"core","roles":["U"]},{"name":"aux","roles":["C1","R1"],"layout":{"roles":{"C1":{"dx":3,"dy":0}}}}]}`,
	} {
		if errs := Validate(tiledTestBlock(t, layout)); len(errs) == 0 {
			t.Errorf("%s: must be rejected", name)
		} else if !strings.Contains(errs[0].Error(), "schematic_layout") {
			t.Errorf("%s: unexpected error %v", name, errs)
		}
	}
}
