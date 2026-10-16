package app

import (
	"math"
	"testing"

	"github.com/zhoushoujianwork/easyeda-agent/internal/blocks"
)

// Regression for the 2026-10-13 ESP32-S3 demo: block.esp32s3_wroom1_module
// threw its attach parts 270–540 units sideways (one 100nF decoupler sat 540
// from the 3V3 pin it is declared to hug), interleaved the 3V3 and EN groups,
// and dropped two parts on the title block. Geometry is the demo's measured
// anchor (bbox [314,240]-[386,660]; 3V3/EN on the left column, IO0 top right).
func TestBlockAttachDemoStaysOnItsPinsAndOffTheTitleBlock(t *testing.T) {
	// Second case: the live readback reports half-unit edges (host top 660.5);
	// rounding the seed back toward the host rejected every attach seed.
	for _, anchor := range []layoutBBox{{MinX: 314, MinY: 240, MaxX: 386, MaxY: 660}, {MinX: 314.5, MinY: 239.5, MaxX: 385.5, MaxY: 660.5}} {
		checkBlockAttachDemo(t, anchor)
	}
}

func checkBlockAttachDemo(t *testing.T, anchor layoutBBox) {
	t.Helper()
	blk, ok, err := blocks.Get("block.esp32s3_wroom1_module")
	if err != nil || !ok {
		t.Fatal("block missing", err)
	}
	sl, err := blk.SchematicLayout()
	if err != nil {
		t.Fatal(err)
	}
	rel, ok := bslRelationsFrom(sl)
	if !ok {
		t.Fatal("no relations")
	}
	r180, r0 := 180.0, 0.0
	pins := map[string]acPin{
		"3V3": {X: 305, Y: 465, PinName: "3V3", PinNumber: "2", PinRotation: &r180},
		"EN":  {X: 305, Y: 455, PinName: "EN", PinNumber: "3", PinRotation: &r180},
		"IO0": {X: 395, Y: 650, PinName: "IO0", PinNumber: "27", PinRotation: &r0},
	}
	sheet := layoutBBox{MinX: 0, MinY: 0, MaxX: 1169, MaxY: 825}
	usable := schUsableArea(sheet)
	tb, _ := titleBlockKeepout(&sheet)
	solved, notes := bslSolveAround(blk, rel, bslBlockNets(blk), nil, "U", pins, anchor, []layoutBBox{anchor, *tb}, &usable)
	if len(solved) != 5 {
		t.Fatalf("all five attach parts must solve, got %d (notes %v)", len(solved), notes)
	}
	at := map[string]bslSolved{}
	for _, s := range solved {
		at[s.Role] = s
		if s.Source != "attach" {
			t.Errorf("%s fell back to %s", s.Role, s.Source)
		}
		box := bslPartBox(blk.Parts[s.Role].Part)
		body := layoutBBox{MinX: s.X + box.MinX, MinY: s.Y + box.MinY, MaxX: s.X + box.MaxX, MaxY: s.Y + box.MaxY}
		if !boxInside(body, usable) || boxesGapOverlap(body, *tb, 0) {
			t.Errorf("%s at (%.0f,%.0f) leaves the sheet or sits on the title block", s.Role, s.X, s.Y)
		}
		// The host-facing end of a vertical part needs its own stub + marker lane
		// (live: C_VDD over the body had its 3V3 stub rejected by the guard).
		if s.Rotation == 270 && body.MinY > anchor.MaxY && body.MinY-anchor.MaxY < bslMarkerVReach {
			t.Errorf("%s leaves only %.1f between its host-facing end and the host (need ≥ %.0f)", s.Role, body.MinY-anchor.MaxY, bslMarkerVReach)
		}
	}
	// #43 (2026-10-15): members of a left/right-column pin go OUTWARD past every
	// connected host pin's stub + marker lane (no longer above/below the host),
	// and stay near their pin: within that side's lane depth plus their own
	// marker cluster.
	// 2026-10-16: a host pin wired by a trunk carries no marker and owns no lane;
	// its trunk members sit within bslTrunkMaxReach of the pin instead.
	nets := bslBlockNets(blk)
	lanes := bslHostPinLanesExcept(nets, "U", pins, anchor, bslRoleReach(nets, "U"), bslTestTrunkPins(rel, pins, solved))
	checkTrunkWires(t, blk, rel, pins, anchor, solved)
	hull := bslHostHull(anchor, pins)
	pinOf := map[string]string{"C_VDD": "3V3", "C_BULK": "3V3", "R_EN": "EN", "C_EN": "EN", "R_IO0": "IO0"}
	for role, pn := range pinOf {
		s, pin := at[role], pins[pn]
		box := bslPartBox(blk.Parts[role].Part)
		body := layoutBBox{MinX: s.X + box.MinX, MinY: s.Y + box.MinY, MaxX: s.X + box.MaxX, MaxY: s.Y + box.MaxY}
		for _, l := range lanes {
			if boxesGapOverlap(body, l, 0) {
				t.Errorf("%s %+v sits in a host pin lane %+v", role, body, l)
			}
		}
		depth := 0.0
		for _, l := range lanes {
			depth = math.Max(depth, l.MaxX-l.MinX)
		}
		v := math.Max(bslMarkerVReach, bslRoleReach(nets, role))
		if s.Wire != nil {
			if d := math.Abs(s.X - pin.X); d > bslTrunkMaxReach {
				t.Errorf("%s is wired %.0f out from its pin %s (want ≤ %d)", role, d, pn, bslTrunkMaxReach)
			}
		} else if d := math.Hypot(s.X-pin.X, s.Y-pin.Y); d > depth+2*(box.MaxY+v)+bslPartGap {
			t.Errorf("%s is %.0f from its pin %s (want ≤ lane depth %.0f + its own cluster)", role, d, pn, depth)
		}
		switch outwardDirection(pin) {
		case "left":
			if body.MaxX >= hull.MinX {
				t.Errorf("%s (left-column pin %s) is not outward-left of the host: x=%.0f", role, pn, s.X)
			}
		case "right":
			if body.MinX <= hull.MaxX {
				t.Errorf("%s (right-column pin %s) is not outward-right of the host: x=%.0f", role, pn, s.X)
			}
		}
	}
	// Each pin's parts are contiguous: no other group's part between them.
	groups := map[string][]bslSolved{}
	for role, g := range pinOf {
		groups[g] = append(groups[g], at[role])
	}
	for g, members := range groups {
		if len(members) < 2 {
			continue
		}
		a, b := members[0], members[1]
		lo := layoutBBox{MinX: math.Min(a.X, b.X), MinY: math.Min(a.Y, b.Y), MaxX: math.Max(a.X, b.X), MaxY: math.Max(a.Y, b.Y)}
		for _, s := range solved {
			if pinOf[s.Role] == g {
				continue
			}
			if s.X >= lo.MinX && s.X <= lo.MaxX && s.Y >= lo.MinY && s.Y <= lo.MaxY {
				t.Errorf("group %s is split by %s: %+v", g, s.Role, solved)
			}
		}
	}
}
