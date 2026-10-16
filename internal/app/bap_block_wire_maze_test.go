package app

import (
	"strings"
	"testing"
)

// mazeTestRaw is a components.list readback without pin nets (the live read is
// includePinNets:false, #256): core U on the left, a two-sided 8-pin FLASH on
// the right. SPI_CLK joins U:5 (U's right edge, facing right) to FLASH:6 (FLASH's
// RIGHT edge, facing away from U) — no straight/one-bend shape reaches it.
func mazeTestRaw(extra ...map[string]any) map[string]any {
	pin := func(number string, x, y, rot float64) map[string]any {
		return map[string]any{"pinNumber": number, "pinName": "P" + number, "x": x, "y": y, "rotation": rot, "noConnected": false}
	}
	part := func(d string, b layoutBBox, pins ...any) map[string]any {
		return map[string]any{"componentType": "part", "designator": d, "primitiveId": "id-" + d,
			"x": (b.MinX + b.MaxX) / 2, "y": (b.MinY + b.MaxY) / 2, "rotation": 0.0,
			"bbox": map[string]any{"minX": b.MinX, "minY": b.MinY, "maxX": b.MaxX, "maxY": b.MaxY}, "pins": pins}
	}
	comps := []any{
		part("U1", layoutBBox{MinX: 0, MinY: 0, MaxX: 100, MaxY: 200},
			pin("5", 100, 150, 0), pin("6", 100, 100, 0), pin("9", 0, 100, 180)),
		part("U2", layoutBBox{MinX: 200, MinY: 80, MaxX: 260, MaxY: 160},
			pin("1", 200, 150, 180), pin("2", 200, 130, 180), pin("3", 200, 110, 180), pin("4", 200, 90, 180),
			pin("5", 260, 90, 0), pin("6", 260, 110, 0), pin("7", 260, 130, 0), pin("8", 260, 150, 0)),
	}
	for _, e := range extra {
		comps = append(comps, e)
	}
	return map[string]any{"components": comps, "wires": []any{}, "wiresAvailable": true}
}

func mazeTestInput(t *testing.T, raw map[string]any) bapWireInput {
	t.Helper()
	comps, err := parseLayoutComps(raw)
	if err != nil {
		t.Fatal(err)
	}
	return bapWireInput{
		Nets:   []bapNet{{Net: "SPI_CLK", Kind: "signal", Members: []string{"U1:5", "U2:7"}}},
		Attach: map[string]string{"U2": "U1:9"}, // FLASH hangs off a U pin of another net
		Scene:  buildScene(raw), Comps: comps, Raw: raw,
	}
}

func mazeTestRoute(t *testing.T, wp bapWirePlan) bapWireRoute {
	t.Helper()
	for _, r := range wp.Routes {
		if r.Kind == "maze" && r.Member == "U2:7" {
			return r
		}
	}
	t.Fatalf("no maze route for U2:7; routes=%+v fallback=%+v", wp.Routes, wp.Fallback)
	return bapWireRoute{}
}

// The flash's far-side pin reaches the core with a maze route: orthogonal,
// on the 5 grid, no zero-length segment, never entering a body (own bodies
// only through the pin), never touching a foreign pin, and leaving both pins
// along their MEASURED outward rotation (U1:5 and U2:7 both face right, so the
// route must leave U2:7 rightwards and arrive at U1:5 from the right).
func TestBapMazeRoutesFarSidePinToCore(t *testing.T) {
	raw := mazeTestRaw()
	in := mazeTestInput(t, raw)
	wp := bapPlanBlockWires(in)
	r := mazeTestRoute(t, wp)
	if r.Net != "SPI_CLK" || r.Host != "U1:5" {
		t.Fatalf("route identity: %+v", r)
	}
	for _, f := range wp.Fallback {
		if f.Member == "U2:7" {
			t.Fatalf("routed member must leave the fallback list: %+v", f)
		}
	}
	pts := r.Points
	if len(pts) < 3 {
		t.Fatalf("expected a bent route, got %v", pts)
	}
	scene := bapSnapScene(in.Scene)
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		if a == b || (a[0] != b[0] && a[1] != b[1]) || !plGrid(a[0]) || !plGrid(a[1]) || !plGrid(b[0]) || !plGrid(b[1]) {
			t.Fatalf("segment %v→%v is zero/diagonal/off-grid", a, b)
		}
		for _, c := range in.Comps {
			box := *c.BBox
			if box.MinX < a[0] && a[0] < box.MaxX && box.MinY < a[1] && a[1] < box.MaxY {
				t.Fatalf("route vertex %v inside %s", a, c.Designator)
			}
			if c.Designator != "U1" && c.Designator != "U2" && schSegmentCrossesBox(a[0], a[1], b[0], b[1], box) {
				t.Fatalf("segment %v→%v crosses %s", a, b, c.Designator)
			}
		}
		for _, q := range scene.Pins {
			ref := q.Designator + ":" + q.PinNumber
			if ref == "U1:5" || ref == "U2:7" {
				continue
			}
			if onSegmentPoint(q.X, q.Y, a[0], a[1], b[0], b[1]) {
				t.Fatalf("segment %v→%v touches foreign pin %s", a, b, ref)
			}
		}
	}
	ends := map[[2]float64]bool{{100, 150}: true, {260, 130}: true}
	if !ends[pts[0]] || !ends[pts[len(pts)-1]] || pts[0] == pts[len(pts)-1] {
		t.Fatalf("route must run pin to pin, got %v", pts)
	}
	// Both pins face right (rotation 0): the adjacent vertex lies to their right.
	for _, pair := range [][2][2]float64{{pts[0], pts[1]}, {pts[len(pts)-1], pts[len(pts)-2]}} {
		if !(pair[1][0] > pair[0][0] && pair[1][1] == pair[0][1]) {
			t.Fatalf("route leaves pin %v towards %v, not along its measured outward side (right)", pair[0], pair[1])
		}
	}
}

// The adapter maps acPin.PinRotation onto powerLayoutPin.Rotation with the
// same convention the planner uses, so libPinSide agrees with outwardDirection.
func TestBapMazeModelPinSideMatchesMeasuredRotation(t *testing.T) {
	in := mazeTestInput(t, mazeTestRaw())
	in.Scene = bapSnapScene(in.Scene)
	p := &bapWirePlanner{in: in}
	m, err := p.mazeModel("SPI_CLK")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, q := range in.Scene.Pins {
		want[q.Designator+":"+q.PinNumber] = outwardDirection(q)
	}
	n := 0
	for _, c := range m.plan.Placements {
		for _, q := range c.Pins {
			ref := c.Designator + ":" + q.Number
			side, err := libPinSide(q, c.BBox)
			if err != nil || side != want[ref] {
				t.Fatalf("%s: model side %q (%v), planner side %q", ref, side, err, want[ref])
			}
			if (ref == "U1:5" || ref == "U2:7") != (q.Net == "SPI_CLK") {
				t.Fatalf("%s: net %q — only block members carry the block net", ref, q.Net)
			}
			n++
		}
	}
	if n != 11 {
		t.Fatalf("expected 11 modelled pins, got %d", n)
	}
	if err := validateLibGeometry(m.plan); err != nil {
		t.Fatalf("the obstacle model of a clean page must itself be valid: %v", err)
	}
}

// When every path is cut (a closed ring of existing page wire around the
// flash; the planner never touches or crosses page wire, and the model walls
// foreign wire off grid point by grid point), the member keeps its fallback and
// the reason says why the maze could not help.
func TestBapMazeBlockedKeepsFallbackWithReason(t *testing.T) {
	raw := mazeTestRaw()
	// A ring of part BODIES: wires may cross foreign wires (a bare X does not
	// conduct) but never pass through a body, so the member is truly enclosed.
	// 1-2 raw gaps at the corners keep the bodies from overlapping while no 5-raw
	// grid line fits through them.
	ring := [][4]float64{{184, 58, 286, 62}, {288, 58, 292, 182}, {184, 178, 286, 182}, {178, 58, 182, 182}}
	comps := raw["components"].([]any)
	for i, b := range ring {
		d := "W" + string(rune('1'+i))
		comps = append(comps, map[string]any{"componentType": "part", "designator": d, "primitiveId": "id-" + d, "x": (b[0] + b[2]) / 2, "y": (b[1] + b[3]) / 2, "rotation": 0.0,
			"bbox": map[string]any{"minX": b[0], "minY": b[1], "maxX": b[2], "maxY": b[3]}, "pins": []any{}})
	}
	raw["components"] = comps
	wp := bapPlanBlockWires(mazeTestInput(t, raw))
	for _, r := range wp.Routes {
		if r.Member == "U2:7" {
			t.Fatalf("enclosed member must not be routed: %+v", r)
		}
	}
	var got *bapWireFallback
	for i := range wp.Fallback {
		if wp.Fallback[i].Member == "U2:7" {
			got = &wp.Fallback[i]
		}
	}
	// The contract: the original reason is kept and the router's own failure is
	// appended (here either no-path or the model refusing the enclosure).
	if got == nil || !strings.Contains(got.Reason, "没有可接的线树") || !strings.Contains(got.Reason, "迷宫绕线也不行") {
		t.Fatalf("fallback must keep its reason and append the router's: %+v", got)
	}
}

// A foreign pin sitting on the straight corridor in front of U1:5 is never
// touched: the router detours around it, and the planner's own ruler refuses a
// segment that would run through it.
func TestBapMazeRefusesForeignPinContact(t *testing.T) {
	tp := map[string]any{"componentType": "part", "designator": "TP9", "primitiveId": "id-TP9", "x": 155.0, "y": 150.0, "rotation": 0.0,
		"bbox": map[string]any{"minX": 150.0, "minY": 145.0, "maxX": 160.0, "maxY": 155.0},
		"pins": []any{map[string]any{"pinNumber": "1", "pinName": "1", "x": 150.0, "y": 150.0, "rotation": 180.0, "noConnected": false}}}
	raw := mazeTestRaw(tp)
	in := mazeTestInput(t, raw)
	wp := bapPlanBlockWires(in)
	r := mazeTestRoute(t, wp)
	for i := 1; i < len(r.Points); i++ {
		a, b := r.Points[i-1], r.Points[i]
		if onSegmentPoint(150, 150, a[0], a[1], b[0], b[1]) || bapSegsIntersect(a, b, [2]float64{145, 150}, [2]float64{150, 150}) {
			t.Fatalf("segment %v→%v touches TP9:1 or its exit", a, b)
		}
		if schSegmentCrossesBox(a[0], a[1], b[0], b[1], layoutBBox{MinX: 150, MinY: 145, MaxX: 160, MaxY: 155}) {
			t.Fatalf("segment %v→%v crosses TP9", a, b)
		}
	}
	in.Scene = bapSnapScene(in.Scene)
	p := &bapWirePlanner{in: in}
	U5, _ := resolvePinCoord(in.Scene, "U1:5")
	if ok, why := p.segClear([2]float64{100, 150}, [2]float64{170, 150}, "SPI_CLK", []acPin{U5}, map[string]bool{"U1": true}); ok || !strings.Contains(why, "TP9") {
		t.Fatalf("a segment through TP9:1 must be refused, got ok=%v why=%q", ok, why)
	}
}

// The per-block attempt cap bounds work: once spent, members fall back with an
// explicit reason instead of searching again.
func TestBapMazeAttemptCap(t *testing.T) {
	in := mazeTestInput(t, mazeTestRaw())
	in.Scene = bapSnapScene(in.Scene)
	p := &bapWirePlanner{in: in}
	r, _ := p.mazeRoute("U2:7", "SPI_CLK") // creates the shared context
	if r == nil {
		t.Fatal("first attempt should route")
	}
	p.maze.attempts = bapMazeMaxAttempts
	if r, why := p.mazeRoute("U2:7", "SPI_CLK"); r != nil || !strings.Contains(why, "上限") {
		t.Fatalf("capped attempt must refuse with a reason, got %+v %q", r, why)
	}
}

// A measured outward ray that runs back into the pin's own body contradicts the
// geometry; the router would faithfully leave along it, so it is not tried.
func TestBapMazeRefusesContradictoryPinDirection(t *testing.T) {
	raw := mazeTestRaw()
	for _, c := range raw["components"].([]any) {
		m := c.(map[string]any)
		if m["designator"] != "U2" {
			continue
		}
		for _, p := range m["pins"].([]any) {
			if pm := p.(map[string]any); pm["pinNumber"] == "7" {
				pm["rotation"] = 180.0 // points into U2
			}
		}
	}
	wp := bapPlanBlockWires(mazeTestInput(t, raw))
	if len(wp.Fallback) != 1 || wp.Fallback[0].Member != "U2:7" || !strings.Contains(wp.Fallback[0].Reason, "指回自己的本体") {
		t.Fatalf("contradictory pin must keep its fallback with the reason, got %+v routes=%+v", wp.Fallback, wp.Routes)
	}
}

// Review of 3636d20: a branch that T's onto a trunk segment whose wire.create
// failed was still issued and its member dropped from autoconnect, leaving a
// dangling stub and an open net. Once a net has a failed wire, later branch and
// maze routes of that net fall back instead.
func TestBapRouteBrokenDependency(t *testing.T) {
	tree := map[string]string{"U1:3": "R1:1"}
	net := map[string]string{"SDA": "R1:1"}
	for _, c := range []struct {
		r    bapWireRoute
		skip bool
	}{
		{bapWireRoute{Kind: "branch", Net: "SDA", Member: "C5:2"}, true},
		{bapWireRoute{Kind: "maze", Net: "SDA", Member: "U2:5"}, true},
		{bapWireRoute{Kind: "trunk", Net: "SDA", Host: "U1:3", Member: "C6:1"}, true},
		{bapWireRoute{Kind: "branch", Net: "SCL", Member: "C7:2"}, false},
		{bapWireRoute{Kind: "facing", Net: "SDA", Member: "R9:1"}, false}, // standalone pin-to-pin wire joins nothing planned
	} {
		if got := bapRouteBrokenDependency(c.r, tree, net) != ""; got != c.skip {
			t.Errorf("%+v: skip=%v, want %v", c.r, got, c.skip)
		}
	}
}

// Wires conduct only on contact (endpoint on the other wire, T, or collinear
// overlap); a bare interior X crossing does not and is allowed — the same rule
// as schguard.SegmentsContact. The flash's SPI order needs crossings.
func TestSegClearAllowsBareCrossingButNotContact(t *testing.T) {
	p := &bapWirePlanner{in: bapWireInput{Scene: acScene{Wires: []wireSegment{{X0: 100, Y0: 50, X1: 100, Y1: 150, Net: "OTHER"}}}}}
	if ok, why := p.segClear([2]float64{50, 100}, [2]float64{150, 100}, "SIG", nil, nil); !ok {
		t.Fatalf("bare X crossing must be allowed: %s", why)
	}
	if ok, _ := p.segClear([2]float64{50, 100}, [2]float64{100, 100}, "SIG", nil, nil); ok {
		t.Fatal("ending on a foreign wire (T contact) must be refused")
	}
	if ok, _ := p.segClear([2]float64{100, 60}, [2]float64{100, 200}, "SIG", nil, nil); ok {
		t.Fatal("collinear overlap with a foreign wire must be refused")
	}
	p.planned = []bapPlannedSeg{{A: [2]float64{200, 50}, B: [2]float64{200, 150}, Net: "OTHER"}}
	if ok, why := p.segClear([2]float64{150, 100}, [2]float64{250, 100}, "SIG", nil, nil); !ok {
		t.Fatalf("crossing a planned foreign wire must be allowed: %s", why)
	}
}

// Direct wiring covers simple peripherals only: pins of another IC in the block
// (the SPI flash) are left to net labels and its attach entry is dropped.
func TestBapDirectWireScopeLeavesICsToLabels(t *testing.T) {
	plan := bapPlan{AnchorRole: "U",
		Placements: []bapPlacement{{Role: "U", PartKey: "mcu.esp32s3r8_chip", Designator: "U2"},
			{Role: "FLASH", PartKey: "ic.w25q64_flash", Designator: "U1"},
			{Role: "C_SPI", PartKey: "cap.100nf_0402", Designator: "C4"}},
		Nets: []bapNet{{Net: "MCU_N3", Members: []string{"U2:29", "C4:1", "U1:8"}}, {Net: "MCU_N9", Members: []string{"U2:33", "U1:6"}}}}
	nets, att := bapDirectWireScope(plan, map[string]string{"C4": "U2:29", "U1": "U2:SPICLK"})
	if got := strings.Join(nets[0].Members, ",") + "|" + strings.Join(nets[1].Members, ","); got != "U2:29,C4:1|U2:33" {
		t.Fatalf("flash pins must leave the wired scope, got %s", got)
	}
	if att["U1"] != "" || att["C4"] != "U2:29" {
		t.Fatalf("attach scope wrong: %v", att)
	}
}

// A fully wired port net's label hangs where autoconnect's own scorer finds it
// clear: the trunk end when free, else a grid point along the trunk leaving
// perpendicular (live: the IO0 label hung at a blocked end and covered C7).
func TestPickTrunkLabelAvoidsBlockedEnd(t *testing.T) {
	rot := 180.0
	host := acPin{X: 300, Y: 300, Designator: "U2", PinNumber: "5", PinRotation: &rot}
	free := &bapWirePlanner{}
	if x, y, dir := free.pickTrunkLabel(host, "left", 60, "IO0", "netport"); x != 240 || y != 300 || dir != "left" {
		t.Fatalf("a clear trunk end keeps the label: got (%v,%v) %s", x, y, dir)
	}
	blocked := &bapWirePlanner{in: bapWireInput{Scene: acScene{Parts: []layoutBBox{{MinX: 120, MinY: 285, MaxX: 235, MaxY: 315}}}}}
	x, y, dir := blocked.pickTrunkLabel(host, "left", 60, "IO0", "netport")
	if (dir != "up" && dir != "down") || y != 300 || x < 240 || x >= 300 {
		t.Fatalf("a blocked end must move the label onto the trunk, leaving perpendicular: got (%v,%v) %s", x, y, dir)
	}
}
