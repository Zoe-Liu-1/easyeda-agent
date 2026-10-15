package app

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/easyeda-agent/internal/blocks"
)

// esp32s3r8Pins builds the ESP32-S3R8 (QFN-56) symbol as read back live on
// 2026-10-15 (#43 E2E, anchor U2 @ 400,520 → bbox [324,380]-[476,670]): pins
// 1–28 on the left column at x−85 from top to bottom, 29–56 on the right
// column at x+85 from bottom to top, 10 apart; the GND pad (57) at the bottom
// centre. The readback bbox excludes the pin lines (tips sit 9 outside it).
func esp32s3r8Pins(ox, oy float64) (layoutBBox, map[string]acPin) {
	names := []string{"", "LNA_IN", "VDD3P3", "VDD3P3", "CHIP_PU", "GPIO0", "GPIO1", "GPIO2", "GPIO3", "GPIO4",
		"GPIO5", "GPIO6", "GPIO7", "GPIO8", "GPIO9", "GPIO10", "GPIO11", "GPIO12", "GPIO13", "GPIO14",
		"VDD3P3_RTC", "XTAL_32K_P", "XTAL_32K_N", "GPIO17", "GPIO18", "GPIO19", "GPIO20", "GPIO21", "SPICS1",
		"VDD_SPI", "SPIHD", "SPIWP", "SPICS0", "SPICLK", "SPIQ", "SPID", "SPICLK_N", "SPICLK_P", "GPIO33",
		"GPIO34", "GPIO35", "GPIO36", "GPIO37", "GPIO38", "MTCK", "MTDO", "VDD3P3_CPU", "MTDI", "MTMS",
		"U0TXD", "U0RXD", "GPIO45", "GPIO46", "XTAL_N", "XTAL_P", "VDDA", "VDDA", "GND"}
	r0, r180, r270 := 0.0, 180.0, 270.0
	bbox := layoutBBox{MinX: ox - 76, MinY: oy - 140, MaxX: ox + 76, MaxY: oy + 150}
	pins := map[string]acPin{}
	add := func(n int, x, y float64, rot *float64) {
		owner := bbox
		p := acPin{X: x, Y: y, Designator: "U2", PinNumber: fmt.Sprint(n), PinName: names[n], OwnerBBox: &owner, PinRotation: rot}
		pins[p.PinName] = p
		pins[p.PinNumber] = p
	}
	for n := 1; n <= 28; n++ {
		add(n, ox-85, oy+150-10*float64(n), &r180)
	}
	for n := 29; n <= 56; n++ {
		add(n, ox+85, oy+140-10*float64(56-n), &r0)
	}
	add(57, ox, oy-150, &r270)
	return bbox, pins
}

func esp32s3r8Block(t *testing.T) (blocks.Block, bslRelations) {
	t.Helper()
	blk, ok, err := blocks.Get("block.esp32s3r8_chip_minsys")
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
	return blk, rel
}

// esp32s3r8LiveReach mirrors bslRoleReachFrom on the live instance names
// (internal nets become MCU_N<k>, ports keep their default_net).
func esp32s3r8LiveReach(blk blocks.Block) map[string]float64 {
	out := map[string]float64{}
	for k, net := range bslBlockNets(blk) {
		name := fmt.Sprintf("MCU_N%d", k+1)
		for _, m := range net {
			if strings.HasPrefix(m, "PORT:") {
				port := strings.TrimPrefix(m, "PORT:")
				name = port
				if p, ok := blk.Ports[port]; ok && p.DefaultNet != "" {
					name = p.DefaultNet
				}
				break
			}
		}
		r := bslReach(name)
		for _, m := range net {
			if role, _, ok := splitBlockPinRef(m); ok && r > out[role] {
				out[role] = r
			}
		}
	}
	return out
}

// #43 E2E (2026-10-15): the tall two-column ESP32-S3R8 anchor. Members of
// left/right pins go outward past every connected host pin's stub + marker
// lane, all 15 roles solve, nothing overlaps, right-column members sit to the
// right, and the whole block (members + their markers + the host lanes) fits
// the usable A4 area away from the title block. (320,580) is where the live
// relocation lands the anchor from the planner's (400,520); (300,400) is a
// second feasible spot. Both net-name rulers are covered: the live instance
// names and the template proxy names (nil roleReach, as in the seed).
func TestBslSolveAround_TallTwoColumnAnchorLaysOutOutward(t *testing.T) {
	blk, rel := esp32s3r8Block(t)
	sheet := layoutBBox{MinX: 0, MinY: 0, MaxX: 1170, MaxY: 825}
	usable := schUsableArea(sheet)
	tb, _ := titleBlockKeepout(&sheet)
	for _, reach := range []map[string]float64{esp32s3r8LiveReach(blk), nil} {
		for _, o := range [][2]float64{{320, 580}, {300, 400}} {
			name := fmt.Sprintf("origin=%v/liveNames=%v", o, reach != nil)
			t.Run(name, func(t *testing.T) {
				checkTallAnchorOutward(t, blk, rel, reach, o[0], o[1], usable, *tb)
			})
		}
	}
}

// The planner picks its origin from an estimated envelope (no pins exist
// before the anchor lands): on an empty A4 that is (400,520), where the right
// column's outward band cannot hold FLASH + crystal + six passives (#43 live:
// "unresolved roles FLASH, X" → rollback). With measured pins the solver finds
// the nearest anchor translation that solves the whole block outward.
func TestBslRelocateAnchor_ESP32S3R8FromPlannerOrigin(t *testing.T) {
	blk, rel := esp32s3r8Block(t)
	l, err := blk.SchematicLayout()
	if err != nil {
		t.Fatal(err)
	}
	d := map[string]bapDevice{}
	for _, p := range blk.Parts {
		d[p.Part] = bapDevice{DeviceUUID: "test-device", LibraryUUID: "test-library"}
	}
	sheet := layoutBBox{MinX: 0, MinY: 0, MaxX: 1170, MaxY: 825}
	tb, _ := titleBlockKeepout(&sheet)
	usable := schUsableArea(sheet)
	plan, err := planBlockApply(bapInput{Block: blk, Topology: bslBlockNets(blk), Devices: d, Layout: l,
		OriginX: 400, OriginY: 300, Sheet: &sheet, TitleBlock: tb, Instance: "MCU"})
	if err != nil {
		t.Fatal(err)
	}
	ox, oy := plan.Origin.X, plan.Origin.Y
	nets := bslBlockNetsWithPorts(blk)
	for _, reach := range []map[string]float64{esp32s3r8LiveReach(blk), nil} {
		anchor, pins := esp32s3r8Pins(ox, oy)
		others := []layoutBBox{*tb}
		solved, _ := bslSolveAround(blk, rel, nets, reach, "U", pins, anchor, append(others, anchor), &usable)
		if bslSolveComplete(rel, "U", solved) {
			t.Logf("planner origin (%.0f,%.0f) already solves; relocation not needed", ox, oy)
			continue
		}
		dx, dy, s2, notes, ok := bslRelocateAnchor(blk, rel, nets, reach, "U", pins, anchor, anchor, others, usable)
		if !ok {
			t.Fatalf("no anchor translation solves the block from (%.0f,%.0f)", ox, oy)
		}
		if dx == 0 && dy == 0 {
			t.Fatal("relocation must move the anchor")
		}
		if !bslSolveComplete(rel, "U", s2) || len(notes) != 0 {
			t.Fatalf("relocated solve incomplete: %v", notes)
		}
		dx2, dy2, _, _, _ := bslRelocateAnchor(blk, rel, nets, reach, "U", pins, anchor, anchor, others, usable)
		if dx2 != dx || dy2 != dy {
			t.Fatalf("relocation not deterministic: (%v,%v) vs (%v,%v)", dx, dy, dx2, dy2)
		}
		t.Logf("planner origin (%.0f,%.0f) → anchor moved (%.0f,%.0f)", ox, oy, dx, dy)
		checkTallAnchorOutward(t, blk, rel, reach, ox+dx, oy+dy, usable, *tb)
	}
}

// Relocation never moves the anchor onto existing geometry: with the rest of
// the page taken it returns ok=false and the caller rolls back as before.
func TestBslRelocateAnchor_RespectsPageObstacles(t *testing.T) {
	blk, rel := esp32s3r8Block(t)
	sheet := layoutBBox{MinX: 0, MinY: 0, MaxX: 1170, MaxY: 825}
	usable := schUsableArea(sheet)
	anchor, pins := esp32s3r8Pins(400, 520)
	hull := bslHostHull(anchor, pins)
	others := []layoutBBox{ // everything around the host's own spot is taken
		{MinX: 0, MinY: 0, MaxX: 1170, MaxY: hull.MinY - 5},
		{MinX: 0, MinY: hull.MaxY + 5, MaxX: 1170, MaxY: 825},
		{MinX: 0, MinY: 0, MaxX: hull.MinX - 5, MaxY: 825},
		{MinX: hull.MaxX + 5, MinY: 0, MaxX: 1170, MaxY: 825},
	}
	if _, _, _, _, ok := bslRelocateAnchor(blk, rel, bslBlockNetsWithPorts(blk), nil, "U", pins, anchor, anchor, others, usable); ok {
		t.Fatal("relocated onto existing geometry")
	}
}

// One lane per host pin the block connects (internal nets and port anchors),
// none for unconnected pins, at least one marker reach deep.
func TestBslHostPinLanes_OnePerConnectedPin(t *testing.T) {
	blk, _ := esp32s3r8Block(t)
	anchor, pins := esp32s3r8Pins(400, 520)
	nets := bslBlockNetsWithPorts(blk)
	reach := bslRoleReach(nets, "U")
	lanes := bslHostPinLanes(nets, "U", pins, anchor, reach)
	if len(lanes) != 21 {
		t.Fatalf("the block connects 21 U pins, got %d lanes", len(lanes))
	}
	for _, l := range lanes {
		if math.Max(l.MaxX-l.MinX, l.MaxY-l.MinY) < reach {
			t.Errorf("lane %+v shallower than one marker reach %.0f", l, reach)
		}
	}
	// GPIO1 (pin 6) and MTCK (pin 44) carry nothing: their stubs stay free.
	for _, n := range []string{"6", "44"} {
		p := pins[n]
		probe := layoutBBox{MinX: p.X - 1, MinY: p.Y - 0.5, MaxX: p.X + 1, MaxY: p.Y + 0.5}
		for _, l := range lanes {
			if boxesGapOverlap(probe, l, 0) {
				t.Errorf("unconnected pin %s got a lane %+v", n, l)
			}
		}
	}
}

func checkTallAnchorOutward(t *testing.T, blk blocks.Block, rel bslRelations, roleReach map[string]float64,
	ox, oy float64, usable, tb layoutBBox) {
	t.Helper()
	anchor, pins := esp32s3r8Pins(ox, oy)
	nets := bslBlockNetsWithPorts(blk)
	solved, notes := bslSolveAround(blk, rel, nets, roleReach, "U", pins, anchor, []layoutBBox{anchor, tb}, &usable)
	if len(solved) != len(rel.Attach) {
		t.Fatalf("all %d attach members must solve, got %d (notes %v)", len(rel.Attach), len(solved), notes)
	}
	reachOf := func(role string) float64 {
		if r, ok := roleReach[role]; ok && r > 0 {
			return r
		}
		return bslRoleReach(nets, role)
	}
	lanes := bslHostPinLanes(nets, "U", pins, anchor, reachOf("U"))
	// every U pin the block connects must own a lane
	connected := map[string]bool{}
	for _, net := range nets {
		for _, m := range net {
			if r, p, ok := splitBlockPinRef(m); ok && r == "U" {
				connected[p] = true
			}
		}
	}
	wantLanes := 0
	counted := map[string]bool{}
	for _, p := range pins {
		if !counted[p.PinNumber] && (connected[p.PinNumber] || connected[p.PinName]) {
			wantLanes++
		}
		counted[p.PinNumber] = true
	}
	if len(lanes) != wantLanes {
		t.Fatalf("lanes %d, want one per connected host pin %d", len(lanes), wantLanes)
	}
	hull := bslHostHull(anchor, pins)
	env := hull
	var bodies []layoutBBox
	var roles []string
	for _, s := range solved {
		if s.Source != "attach" {
			t.Errorf("%s fell back to %s", s.Role, s.Source)
		}
		// passives declared vertical turn 270; FLASH and the crystal (no orient)
		// keep 0 — a turned multi-pin symbol no longer matches its estimated box.
		if want := map[bool]float64{true: 270, false: 0}[bslIsDiscrete(blk.Parts[s.Role].Part)]; s.Rotation != want {
			t.Errorf("%s rotation %.0f, want %.0f", s.Role, s.Rotation, want)
		}
		b := bslPartBox(blk.Parts[s.Role].Part)
		body := layoutBBox{MinX: s.X + b.MinX, MinY: s.Y + b.MinY, MaxX: s.X + b.MaxX, MaxY: s.Y + b.MaxY}
		if boxesGapOverlap(body, hull, 0) {
			t.Errorf("%s body %+v overlaps the host (pins included)", s.Role, body)
		}
		for _, l := range lanes {
			if boxesGapOverlap(body, l, 0) {
				t.Errorf("%s body %+v intrudes a host pin lane %+v", s.Role, body, l)
			}
		}
		for i, o := range bodies {
			if boxesGapOverlap(body, o, 0) {
				t.Errorf("%s overlaps %s", s.Role, roles[i])
			}
		}
		bodies, roles = append(bodies, body), append(roles, s.Role)
		// members of a right-column pin sit to the right of the host, left to the left
		_, tPin, _ := splitBlockPinRef(rel.Attach[s.Role])
		pin := pins[tPin]
		switch outwardDirection(pin) {
		case "right":
			if body.MinX <= hull.MaxX {
				t.Errorf("%s (pin %s, right column) at x=%.0f is not right of the host", s.Role, tPin, s.X)
			}
		case "left":
			if body.MaxX >= hull.MinX {
				t.Errorf("%s (pin %s, left column) at x=%.0f is not left of the host", s.Role, tPin, s.X)
			}
		}
		// the envelope counts each member's own markers (same ruler as free())
		h, v := reachOf(s.Role), bslMarkerVReach
		if s.Rotation == 270 {
			h, v = bslLateralHalf(reachOf(s.Role)), math.Max(bslMarkerVReach, reachOf(s.Role))
		}
		c := layoutBBox{MinX: body.MinX - h, MinY: body.MinY - v, MaxX: body.MaxX + h, MaxY: body.MaxY + v}
		if !boxInside(c, usable) {
			t.Errorf("%s with its markers %+v leaves the usable sheet", s.Role, c)
		}
		if boxesGapOverlap(c, tb, 0) {
			t.Errorf("%s with its markers %+v sits on the title block", s.Role, c)
		}
		env = layoutBBox{MinX: math.Min(env.MinX, c.MinX), MinY: math.Min(env.MinY, c.MinY),
			MaxX: math.Max(env.MaxX, c.MaxX), MaxY: math.Max(env.MaxY, c.MaxY)}
	}
	// host lanes count toward the envelope too
	for _, l := range lanes {
		env = layoutBBox{MinX: math.Min(env.MinX, l.MinX), MinY: math.Min(env.MinY, l.MinY),
			MaxX: math.Max(env.MaxX, l.MaxX), MaxY: math.Max(env.MaxY, l.MaxY)}
	}
	if !boxInside(env, usable) {
		t.Errorf("block envelope %+v does not fit usable A4 %+v", env, usable)
	}
	// members of one pin stay contiguous: no other pin's member sits between two
	// of them (checked along the axis they spread on).
	byTarget := map[string][]bslSolved{}
	for _, s := range solved {
		byTarget[rel.Attach[s.Role]] = append(byTarget[rel.Attach[s.Role]], s)
	}
	for target, group := range byTarget {
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].Role < group[j].Role })
		a, b := group[0], group[1]
		lo := layoutBBox{MinX: math.Min(a.X, b.X), MinY: math.Min(a.Y, b.Y), MaxX: math.Max(a.X, b.X), MaxY: math.Max(a.Y, b.Y)}
		for _, s := range solved {
			if rel.Attach[s.Role] == target {
				continue
			}
			if s.X > lo.MinX && s.X < lo.MaxX && s.Y >= lo.MinY && s.Y <= lo.MaxY ||
				s.Y > lo.MinY && s.Y < lo.MaxY && s.X >= lo.MinX && s.X <= lo.MaxX {
				t.Errorf("%s splits the %s group (%s, %s)", s.Role, target, a.Role, b.Role)
			}
		}
	}
	if testing.Verbose() {
		sort.Slice(solved, func(i, j int) bool { return solved[i].Role < solved[j].Role })
		for _, s := range solved {
			t.Logf("%-9s → %-16s (%.0f,%.0f) rot=%.0f", s.Role, rel.Attach[s.Role], s.X, s.Y, s.Rotation)
		}
		t.Logf("envelope %+v", env)
	}
}
