package app

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/easyeda-agent/internal/blocks"
	"github.com/zhoushoujianwork/easyeda-agent/internal/connectivity"
)

// trunkTestBlockJSON is an ESP32-like minimum system at half scale: a two-column
// core U with supply decouplers, an EN RC on a port, an IO0 pull-up, a
// feedback cap on a block-internal signal net and a UART port that keeps a
// marker. Every peripheral is attached to the core pin it serves.
const trunkTestBlockJSON = `{
  "id": "block.test_trunk_minsys",
  "category": "mcu",
  "parts": {
    "U":     {"part": "mcu.test_core", "qty": 1},
    "C1":    {"part": "cap.100nf_0402", "qty": 1},
    "C3":    {"part": "cap.100nf_0402", "qty": 1},
    "C_EN":  {"part": "cap.1uf_0402", "qty": 1},
    "R_EN":  {"part": "res.10k_0402", "qty": 1},
    "R_PU":  {"part": "res.10k_0402", "qty": 1},
    "C_F":   {"part": "cap.100nf_0402", "qty": 1}
  },
  "internal_nets": [
    ["U.VDD", "U.VDDA", "C1.1", "C3.1", "R_EN.2", "R_PU.2", "PORT:3V3"],
    ["U.GND", "C1.2", "C3.2", "C_EN.2", "C_F.2", "PORT:GND"],
    ["U.EN", "R_EN.1", "C_EN.1", "PORT:EN"],
    ["U.IO0", "R_PU.1", "PORT:IO0"],
    ["U.FB", "C_F.1"],
    ["U.TX", "PORT:TX"]
  ],
  "ports": {
    "3V3": {"dir": "in", "at": "U.VDD", "default_net": "3V3"},
    "GND": {"dir": "bidir", "at": "U.GND", "default_net": "GND"},
    "EN":  {"dir": "bidir", "at": "U.EN", "default_net": "EN"},
    "IO0": {"dir": "bidir", "at": "U.IO0", "default_net": "IO0"},
    "TX":  {"dir": "out", "at": "U.TX", "default_net": "TX"}
  },
  "schematic_layout": {
    "anchor": "U",
    "attach": {"C1": "U.VDD", "C3": "U.VDDA", "C_EN": "U.EN", "R_EN": "U.EN", "R_PU": "U.IO0", "C_F": "U.FB"},
    "orient": {"C1": "vertical", "C3": "vertical", "C_EN": "vertical", "R_EN": "vertical", "R_PU": "vertical", "C_F": "vertical"}
  }
}`

func trunkTestBlock(t *testing.T) (blocks.Block, bslRelations) {
	t.Helper()
	var b blocks.Block
	if err := json.Unmarshal([]byte(trunkTestBlockJSON), &b); err != nil {
		t.Fatal(err)
	}
	b.Raw = json.RawMessage(trunkTestBlockJSON)
	l, err := b.SchematicLayout()
	if err != nil {
		t.Fatal(err)
	}
	rel, ok := bslRelationsFrom(l)
	if !ok {
		t.Fatal("not relational")
	}
	return b, rel
}

// trunkTestPin is one core pin: name, column side and row (0 = top).
type trunkTestPin struct {
	name string
	side string
	row  int
}

// The core: 10 rows per column, 10 apart, tips 5 outside the body (half-scale
// QFN). VDD/EN sit two rows apart on the left, IO0/VDDA on the right, GND at the
// bottom centre.
var trunkTestPins = []trunkTestPin{
	{"VDD", "left", 1}, {"EN", "left", 3}, {"FB", "left", 7},
	{"TX", "right", 2}, {"IO0", "right", 5}, {"VDDA", "right", 8},
}

func trunkTestAnchor(ox, oy float64) (layoutBBox, map[string]acPin) {
	body := layoutBBox{MinX: ox - 40, MinY: oy - 55, MaxX: ox + 40, MaxY: oy + 55}
	pins := map[string]acPin{}
	n := 0
	add := func(name string, x, y, rot float64) {
		n++
		r, owner := rot, body
		p := acPin{X: x, Y: y, Designator: "U1", PinNumber: fmt.Sprint(n), PinName: name, OwnerBBox: &owner, PinRotation: &r}
		pins[name], pins[p.PinNumber] = p, p
	}
	for _, p := range trunkTestPins {
		x, rot := ox-45, 180.0
		if p.side == "right" {
			x, rot = ox+45, 0
		}
		add(p.name, x, oy+45-10*float64(p.row), rot)
	}
	add("GND", ox, oy-60, 270)
	return body, pins
}

func trunkTestSheet() (layoutBBox, *layoutBBox) {
	sheet := layoutBBox{MinX: 0, MinY: 0, MaxX: 1170, MaxY: 825}
	usable := schUsableArea(sheet)
	return sheet, &usable
}

// The solver puts every attach member of the half-scale core on a trunk from
// its host pin: within bslTrunkMaxReach of the pin end, with an orthogonal 5-grid
// route that leaves the host outward and lands on the member's net pin, and no
// route crosses another body or another host's route.
func TestBslTrunk_SyntheticCoreWiresEveryAttachNear(t *testing.T) {
	blk, rel := trunkTestBlock(t)
	_, usable := trunkTestSheet()
	anchor, pins := trunkTestAnchor(500, 400)
	nets := bslBlockNetsWithPorts(blk)
	solved, notes := bslSolveAround(blk, rel, nets, nil, "U", pins, anchor, []layoutBBox{anchor}, usable)
	if len(solved) != len(rel.Attach) {
		t.Fatalf("all %d attach members must solve, got %d (%v)", len(rel.Attach), len(solved), notes)
	}
	if bslTestHasFallback(notes) {
		t.Fatalf("no member may fall back: %v", notes)
	}
	for _, s := range solved {
		if s.Wire == nil {
			t.Errorf("%s has no planned wire", s.Role)
			continue
		}
		_, tPin, _ := splitBlockPinRef(rel.Attach[s.Role])
		if d := math.Abs(s.X - pins[tPin].X); d > bslTrunkMaxReach {
			t.Errorf("%s sits %.0f from its host pin end (want ≤ %d)", s.Role, d, bslTrunkMaxReach)
		}
		if testing.Verbose() {
			t.Logf("%-5s → %-7s (%.0f,%.0f) rot=%.0f wire %v", s.Role, rel.Attach[s.Role], s.X, s.Y, s.Rotation, s.Wire)
		}
	}
	checkTrunkWires(t, blk, rel, pins, anchor, solved)
	// The TX port pin keeps its marker lane; nothing of the trunks intrudes it.
	lanes := bslHostPinLanesExcept(nets, "U", pins, anchor, bslRoleReach(nets, "U"), bslTestTrunkPins(rel, pins, solved))
	if len(lanes) != 2 { // TX and GND
		t.Fatalf("want the TX and GND lanes only, got %d", len(lanes))
	}
	for _, s := range solved {
		for i := 1; i < len(s.Wire); i++ {
			for _, l := range lanes {
				if boxesGapOverlap(bslSegBox(s.Wire[i-1], s.Wire[i]), l, 0) {
					t.Errorf("%s wire crosses a marker lane %+v", s.Role, l)
				}
			}
		}
	}
}

// When the trunk zone of a host is walled off, its members fall back to the old
// outward placement and the solver says so — never silently.
func TestBslTrunk_BlockedZoneFallsBackWithReason(t *testing.T) {
	blk, rel := trunkTestBlock(t)
	_, usable := trunkTestSheet()
	anchor, pins := trunkTestAnchor(500, 400)
	en := pins["EN"]
	// A foreign part right in front of EN, the rows around it and everything
	// within the trunk window to the left of the host.
	wall := layoutBBox{MinX: usable.MinX, MinY: en.Y - 45, MaxX: en.X - 6, MaxY: en.Y + 15}
	solved, notes := bslSolveAround(blk, rel, bslBlockNetsWithPorts(blk), nil, "U", pins, anchor, []layoutBBox{anchor, wall}, usable)
	for _, s := range solved {
		if s.Role != "R_EN" && s.Role != "C_EN" {
			continue
		}
		if s.Wire != nil {
			t.Errorf("%s got a trunk through the wall: %v", s.Role, s.Wire)
		}
	}
	joined := strings.Join(notes, "\n")
	for _, role := range []string{"R_EN", "C_EN"} {
		if !strings.Contains(joined, role+": 贴 U.EN 的干线放不下") {
			t.Errorf("no fallback reason reported for %s:\n%s", role, joined)
		}
	}
}

// trunkTestLive turns a solved plan into the components.list result the
// connector would return after placement (pins at the measured 0402 offsets,
// with nets from the plan), so the executor and the ownership validator run on
// the same geometry block-apply sees.
func trunkTestLive(t *testing.T, blk blocks.Block, rel bslRelations, ox, oy float64, obstacles []layoutBBox) (bapPlan, []bslSolved, map[string]any) {
	t.Helper()
	l, _ := blk.SchematicLayout()
	devices := map[string]bapDevice{}
	for _, p := range blk.Parts {
		devices[p.Part] = bapDevice{DeviceUUID: "dev", LibraryUUID: "lib"}
	}
	plan, err := planBlockApply(bapInput{Block: blk, Topology: bslBlockNets(blk), Devices: devices, Layout: l, OriginX: ox, OriginY: oy, AtExplicit: true})
	if err != nil {
		t.Fatal(err)
	}
	_, usable := trunkTestSheet()
	anchor, pins := trunkTestAnchor(ox, oy)
	solved, notes := bslSolveAround(blk, rel, bslBlockNetsWithPorts(blk), nil, "U", pins, anchor, append([]layoutBBox{anchor}, obstacles...), usable)
	if len(solved) != len(rel.Attach) {
		t.Fatalf("solve incomplete: %v", notes)
	}
	return plan, solved, trunkTestReadback(plan, solved, anchor, pins, ox, oy)
}

// trunkTestReadback renders a solve as the components.list readback the live
// executor sees: the anchor with its pins, and every solved vertical passive as
// a 2-pin part with the measured 20-raw pin span.
func trunkTestReadback(plan bapPlan, solved []bslSolved, anchor layoutBBox, pins map[string]acPin, ox, oy float64) map[string]any {
	desig := map[string]string{}
	for _, p := range plan.Placements {
		desig[p.Role] = p.Designator
	}
	netOf := map[string]string{} // DESIG:PIN → net
	for _, n := range plan.Nets {
		for _, m := range n.Members {
			netOf[m] = n.Net
		}
	}
	pin := func(d, number, name string, x, y, rot float64) map[string]any {
		net := netOf[d+":"+number]
		if net == "" {
			net = netOf[d+":"+name]
		}
		return map[string]any{"pinNumber": number, "pinName": name, "x": x, "y": y, "rotation": rot, "net": net, "noConnected": false}
	}
	bbox := func(b layoutBBox) map[string]any {
		return map[string]any{"minX": b.MinX, "minY": b.MinY, "maxX": b.MaxX, "maxY": b.MaxY}
	}
	u := desig["U"]
	var upins []any
	seen := map[string]bool{}
	for _, p := range pins {
		if seen[p.PinNumber] {
			continue
		}
		seen[p.PinNumber] = true
		upins = append(upins, pin(u, p.PinNumber, p.PinName, p.X, p.Y, *p.PinRotation))
	}
	comps := []any{map[string]any{"componentType": "part", "designator": u, "primitiveId": "id-" + u,
		"x": ox, "y": oy, "bbox": bbox(anchor), "pins": upins}}
	for _, s := range solved {
		d := desig[s.Role]
		up, down := "1", "2" // placed with rotation 90: pin 1 up (host stores it as 270)
		if s.Rotation == 270 {
			up, down = "2", "1"
		}
		comps = append(comps, map[string]any{"componentType": "part", "designator": d, "primitiveId": "id-" + d,
			"x": s.X, "y": s.Y, "rotation": s.Rotation,
			"bbox": bbox(layoutBBox{MinX: s.X - 8.5, MinY: s.Y - 10.5, MaxX: s.X + 8.5, MaxY: s.Y + 10.5}),
			"pins": []any{pin(d, up, up, s.X, s.Y+measuredPassivePinHalf, 90), pin(d, down, down, s.X, s.Y-measuredPassivePinHalf, 270)}})
	}
	return map[string]any{"components": comps, "wires": []any{}, "wiresAvailable": true}
}

func trunkTestWireInput(t *testing.T, plan bapPlan, rel bslRelations, raw map[string]any) bapWireInput {
	t.Helper()
	comps, err := parseLayoutComps(raw)
	if err != nil {
		t.Fatal(err)
	}
	return bapWireInput{Nets: plan.Nets, Attach: bapAttachHostsFrom(rel.Attach, plan.Placements),
		Scene: buildScene(raw), Comps: comps, Raw: raw}
}

// Judgement coordinates == landed coordinates: on the placed geometry the
// executor draws exactly the trunks the solver planned, labels the fully wired
// EN/IO0 ports at their trunk ends, and the resulting page passes the same
// peripheral-direct ruler `sch check` uses. Removing one route (a member that
// fell back to a label) makes the ruler fail again — the negative control.
func TestBapPlanBlockWires_TrunksPassPeripheralDirect(t *testing.T) {
	blk, rel := trunkTestBlock(t)
	plan, solved, raw := trunkTestLive(t, blk, rel, 500, 400, nil)
	wp := bapPlanBlockWires(trunkTestWireInput(t, plan, rel, raw))
	if len(wp.Fallback) != 0 {
		t.Fatalf("no member may fall back on clean geometry: %+v", wp.Fallback)
	}
	desig := map[string]string{}
	for _, p := range plan.Placements {
		desig[p.Role] = p.Designator
	}
	byMember := map[string]bapWireRoute{}
	for _, r := range wp.Routes {
		byMember[strings.SplitN(r.Member, ":", 2)[0]] = r
	}
	for _, s := range solved {
		r, ok := byMember[desig[s.Role]]
		if !ok {
			t.Errorf("%s (%s) has no route", s.Role, desig[s.Role])
			continue
		}
		if fmt.Sprint(r.Points) != fmt.Sprint(s.Wire) {
			t.Errorf("%s: executor route %v differs from the solver's plan %v", s.Role, r.Points, s.Wire)
		}
	}
	labels := map[string]bool{}
	for _, l := range wp.Labels {
		labels[l.Net] = true
		if l.Rotation != 180 && l.Rotation != 0 {
			t.Errorf("label %s must continue its trunk outward, rotation %.0f", l.Net, l.Rotation)
		}
	}
	if !labels["EN"] || !labels["IO0"] || labels["3V3"] || len(wp.Labels) != 2 {
		t.Errorf("want labels for the fully wired EN and IO0 ports only, got %+v", wp.Labels)
	}

	observed := func(routes []bapWireRoute) error {
		var wires []any
		for _, r := range routes {
			for i := 1; i < len(r.Points); i++ {
				a, b := r.Points[i-1], r.Points[i]
				wires = append(wires, map[string]any{"x0": a[0], "y0": a[1], "x1": b[0], "y1": b[1], "net": r.Net})
			}
		}
		res := map[string]any{"components": raw["components"], "wires": wires, "wiresAvailable": true}
		ids := map[string]string{}
		var core string
		var peripherals []string
		for _, p := range plan.Placements {
			ids[p.Designator] = p.Designator
			if p.Role == "U" {
				core = p.Designator
			} else {
				peripherals = append(peripherals, p.Designator)
			}
		}
		layout, err := schematicObservedOwnershipLayout(res, ids)
		if err != nil {
			return err
		}
		return ValidateSchematicPeripheralDirect(layout, []connectivity.Module{{ID: plan.Instance, CoreComponents: []string{core}, PeripheralComponents: peripherals}})
	}
	if err := observed(wp.Routes); err != nil {
		t.Fatalf("wired block must pass peripheral-direct: %v", err)
	}
	var without []bapWireRoute
	for _, r := range wp.Routes {
		if !strings.HasPrefix(r.Member, desig["C_F"]+":") {
			without = append(without, r)
		}
	}
	if err := observed(without); err == nil {
		t.Fatal("negative control: with C_F on a label only, peripheral-direct must fail")
	}
}

// A route that would cross another part's body, touch a foreign pin or merge
// into an existing wire is rejected; that member falls back with the reason and
// every other member is still wired.
func TestBapPlanBlockWires_RejectsUnclearRoutesWithReason(t *testing.T) {
	blk, rel := trunkTestBlock(t)
	plan, solved, raw := trunkTestLive(t, blk, rel, 500, 400, nil)
	desig := map[string]string{}
	for _, p := range plan.Placements {
		desig[p.Role] = p.Designator
	}
	var c1 bslSolved
	for _, s := range solved {
		if s.Role == "C1" {
			c1 = s
		}
	}
	// The corner of C1's trunk (on the VDD row) and a point on its drop.
	corner, drop := c1.Wire[len(c1.Wire)-2], c1.Wire[len(c1.Wire)-1]
	mid := [2]float64{(corner[0] + c1.Wire[0][0]) / 2, corner[1]}
	cases := []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"foreign body on the trunk", func(r map[string]any) {
			r["components"] = append(r["components"].([]any), map[string]any{"componentType": "part", "designator": "J9", "primitiveId": "id-J9",
				"x": mid[0], "y": mid[1], "bbox": map[string]any{"minX": mid[0] - 3, "minY": mid[1] - 3, "maxX": mid[0] + 3, "maxY": mid[1] + 3}, "pins": []any{}})
		}, "穿过 J9"},
		{"foreign pin on the drop", func(r map[string]any) {
			p := [2]float64{corner[0], (corner[1] + drop[1]) / 2}
			r["components"] = append(r["components"].([]any), map[string]any{"componentType": "part", "designator": "TP9", "primitiveId": "id-TP9",
				"x": p[0] - 20, "y": p[1], "bbox": map[string]any{"minX": p[0] - 30, "minY": p[1] - 3, "maxX": p[0] - 24, "maxY": p[1] + 3},
				"pins": []any{map[string]any{"pinNumber": "1", "x": p[0], "y": p[1], "rotation": 0.0, "net": "", "noConnected": false}}})
		}, "碰到别的引脚 TP9:1"},
		{"existing wire across the trunk", func(r map[string]any) {
			r["wires"] = []any{map[string]any{"primitiveId": "w9", "x0": mid[0], "y0": mid[1] - 5, "x1": mid[0], "y1": mid[1] + 5, "net": "OTHER"}}
		}, "碰到页面已有导线"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := map[string]any{"components": append([]any(nil), raw["components"].([]any)...), "wires": raw["wires"], "wiresAvailable": true}
			tc.edit(r)
			wp := bapPlanBlockWires(trunkTestWireInput(t, plan, rel, r))
			var got *bapWireFallback
			for i, f := range wp.Fallback {
				if strings.HasPrefix(f.Member, desig["C1"]+":") {
					got = &wp.Fallback[i]
				} else {
					t.Errorf("only C1 may fall back, also got %+v", f)
				}
			}
			if got == nil || !strings.Contains(got.Reason, tc.want) {
				t.Fatalf("C1 must fall back with %q, got %+v", tc.want, got)
			}
			for _, rt := range wp.Routes {
				if strings.HasPrefix(rt.Member, desig["C1"]+":") {
					t.Fatalf("C1 still routed: %+v", rt)
				}
			}
			if len(wp.Routes) != len(solved)-1 {
				t.Errorf("the other %d members stay wired, got %d routes", len(solved)-1, len(wp.Routes))
			}
		})
	}
}

// A member pin that faces away from the trunk would need two bends; the
// executor refuses rather than leave the pin sideways (the geometry guard would
// reject the write anyway).
func TestBapPlanBlockWires_MemberFacingAwayFallsBack(t *testing.T) {
	blk, rel := trunkTestBlock(t)
	plan, _, raw := trunkTestLive(t, blk, rel, 500, 400, nil)
	desig := map[string]string{}
	for _, p := range plan.Placements {
		desig[p.Role] = p.Designator
	}
	for _, c := range raw["components"].([]any) {
		m := c.(map[string]any)
		if m["designator"] != desig["C_F"] {
			continue
		}
		for _, p := range m["pins"].([]any) {
			pm := p.(map[string]any)
			pm["rotation"] = math.Mod(pm["rotation"].(float64)+180, 360) // pins point into the body
		}
	}
	wp := bapPlanBlockWires(trunkTestWireInput(t, plan, rel, raw))
	if len(wp.Fallback) != 1 || !strings.HasPrefix(wp.Fallback[0].Member, desig["C_F"]+":") ||
		!strings.Contains(wp.Fallback[0].Reason, "引出方向") {
		t.Fatalf("C_F must fall back for facing away, got %+v", wp.Fallback)
	}
}

func TestBapAttachHostsFrom(t *testing.T) {
	got := bapAttachHostsFrom(map[string]string{"C1": "U.VDD", "R": "U.3", "X": "Q.1"},
		[]bapPlacement{{Role: "U", Designator: "U2"}, {Role: "C1", Designator: "C7"}, {Role: "R", Designator: "R4"}})
	want := map[string]string{"C7": "U2:VDD", "R4": "U2:3"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// The host reads pins back with float noise (674.9999999999999 for 675). The
// planner must treat the noisy reading as the same pin: before the snap a
// member's own pin was reported as a foreign pin its drop touches.
func TestBapPlanBlockWires_FloatNoiseInMeasuredPins(t *testing.T) {
	blk, rel := trunkTestBlock(t)
	plan, _, raw := trunkTestLive(t, blk, rel, 500, 400, nil)
	clean := bapPlanBlockWires(trunkTestWireInput(t, plan, rel, raw))
	in := trunkTestWireInput(t, plan, rel, raw)
	noisy := make([]acPin, len(in.Scene.Pins))
	for i, q := range in.Scene.Pins {
		q.X, q.Y = q.X-1e-13, q.Y+1e-13
		noisy[i] = q
	}
	in.Scene.Pins = noisy
	got := bapPlanBlockWires(in)
	if len(got.Fallback) != 0 {
		t.Fatalf("float noise made members fall back: %+v", got.Fallback)
	}
	if fmt.Sprint(got.Routes) != fmt.Sprint(clean.Routes) {
		t.Fatalf("noisy pins changed the plan:\n got %v\nwant %v", got.Routes, clean.Routes)
	}
}

// measuredPassivePinHalf is the live readback (Web 4.1.78, ceshi 2026-10-16):
// a vertical 0402/0805 cap or 0402 resistor placed at y reports its pins at
// y±20. Kept independent of bslPassivePinHalf so a drifting solver constant
// fails these tests instead of cancelling out against its own fixture.
const measuredPassivePinHalf = 20.0

// The host rejects zero-length wire segments; a drop whose corner is the
// trunk's current end produced [start, start, pin] and every such wire failed.
func TestBapCompactRouteDropsRepeatedPoints(t *testing.T) {
	got := bapCompactRoute([][2]float64{{1, 2}, {1, 2}, {3, 2}, {3, 2}, {3, 5}})
	if fmt.Sprint(got) != "[[1 2] [3 2] [3 5]]" {
		t.Fatalf("got %v", got)
	}
	if len(bapCompactRoute([][2]float64{{1, 2}, {1, 2}})) != 1 {
		t.Fatal("a fully degenerate route compacts to a single point")
	}
}
