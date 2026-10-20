package app

import (
	"math"
	"strings"
	"testing"
)

func dcLabel(id, parent string, x0, x1 float64) visibleTextItem {
	return visibleTextItem{
		ID: id, Kind: "attribute", ComponentType: "wire", ParentID: parent,
		Key: "Name", Value: "SIG", BBox: &layoutBBox{MinX: x0, MinY: 40, MaxX: x1, MaxY: 48},
	}
}

func dcBlockingText(id string, x0, x1 float64) visibleTextItem {
	return visibleTextItem{
		ID: id, Kind: "attribute", ComponentType: "part", ParentID: "r1",
		Key: "Value", Value: "10k", BBox: &layoutBBox{MinX: x0, MinY: 40, MaxX: x1, MaxY: 48},
	}
}

func TestPlanDirectConnectTextMoves(t *testing.T) {
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, 0, 0, 100}},
		{ID: "w2", Points: []float64{200, 0, 200, 100}},
	}
	items := []visibleTextItem{dcLabel("t1", "w1", 0, 20), dcLabel("t2", "w2", 200, 220), dcBlockingText("v1", 90, 110)}
	plans := planDirectConnectTextMoves(items, nil, wires,
		map[string]checkPoint{"v1": {X: 100, Y: 44}}, &layoutBBox{MinX: -50, MinY: -50, MaxX: 300, MaxY: 200}, nil, 250, 40)
	if len(plans) != 1 || plans[0].Status != "move-proposed" || plans[0].Move == nil {
		t.Fatalf("want one text-move proposal, got %+v", plans)
	}
	if plans[0].Move.AttributeID != "v1" || plans[0].Move.DeltaY == 0 {
		t.Fatalf("wrong move: %+v", plans[0].Move)
	}
	if plans[0].Move.ToAnchor == nil || plans[0].Move.ToAnchor.Y <= plans[0].Move.FromAnchor.Y {
		t.Fatalf("target anchor missing/not shifted: %+v", plans[0].Move)
	}
}

func TestPlanDirectConnectTextMovesBlockedByWire(t *testing.T) {
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, 0, 0, 100}},
		{ID: "w2", Points: []float64{200, 0, 200, 100}},
		{ID: "w3", Points: []float64{0, 44, 200, 44}},
	}
	items := []visibleTextItem{dcLabel("t1", "w1", 0, 20), dcLabel("t2", "w2", 200, 220), dcBlockingText("v1", 90, 110)}
	plans := planDirectConnectTextMoves(items, nil, wires,
		map[string]checkPoint{"v1": {X: 100, Y: 44}}, &layoutBBox{MinX: -50, MinY: -50, MaxX: 300, MaxY: 200}, nil, 250, 40)
	if len(plans) != 1 || plans[0].Status != "unresolved" || plans[0].Move != nil {
		t.Fatalf("wire blocker must remain unresolved, got %+v", plans)
	}
}

func TestPlanDirectConnectAlreadyClear(t *testing.T) {
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, 0, 0, 100}},
		{ID: "w2", Points: []float64{200, 0, 200, 100}},
	}
	items := []visibleTextItem{dcLabel("t1", "w1", 0, 20), dcLabel("t2", "w2", 200, 220)}
	plans := planDirectConnectTextMoves(items, nil, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 300, MaxY: 200}, nil, 250, 40)
	if len(plans) != 1 || plans[0].Status != "already-clear" || plans[0].Move != nil {
		t.Fatalf("want already-clear, got %+v", plans)
	}
}

func TestPlanDirectConnectTextMovesBlockedByPin(t *testing.T) {
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, 0, 0, 100}},
		{ID: "w2", Points: []float64{200, 0, 200, 100}},
	}
	items := []visibleTextItem{dcLabel("t1", "w1", 0, 20), dcLabel("t2", "w2", 200, 220), dcBlockingText("v1", 90, 110)}
	comps := []layoutComp{{ID: "j1", ComponentType: "part", Pins: []layoutPin{{Number: "2", X: 100, Y: 44}}}}
	plans := planDirectConnectTextMoves(items, comps, wires,
		map[string]checkPoint{"v1": {X: 100, Y: 44}}, &layoutBBox{MinX: -50, MinY: -50, MaxX: 300, MaxY: 200}, nil, 250, 40)
	if len(plans) != 1 || plans[0].Status != "unresolved" || plans[0].Move != nil {
		t.Fatalf("pin blocker must remain unresolved, got %+v", plans)
	}
}

func dcMarkerFixture() ([]visibleTextItem, []layoutComp, []schGroupWire) {
	label := func(id, parent string, x0, x1 float64) visibleTextItem {
		return visibleTextItem{
			ID: id, Kind: "attribute", ComponentType: "wire", ParentID: parent,
			Key: "Name", Value: "SDA", BBox: &layoutBBox{MinX: x0, MinY: 95, MaxX: x1, MaxY: 105},
		}
	}
	items := []visibleTextItem{
		label("t1", "w1", 0, 10),
		label("t2", "w2", 100, 110),
		{
			ID: "mt1", Kind: "attribute", ComponentType: "netflag", ParentID: "m1",
			Key: "Name", Value: "+3V3", BBox: &layoutBBox{MinX: 45, MinY: 98, MaxX: 55, MaxY: 106},
		},
	}
	// The two SDA label centers are (5,100) and (105,100), so the h-v path is a
	// straight line at y=100. The +3V3 name band covers that line.
	comps := []layoutComp{
		{
			ID: "m1", ComponentType: "netflag", Net: "+3V3", X: 50, Y: 90, AnchorAvailable: true,
			BBox: &layoutBBox{MinX: 45, MinY: 92, MaxX: 55, MaxY: 98},
			Pins: []layoutPin{{Number: "1", X: 50, Y: 90}},
		},
		{
			ID: "p1", ComponentType: "part", Designator: "R1", X: 50, Y: 64,
			BBox: &layoutBBox{MinX: 45, MinY: 60, MaxX: 55, MaxY: 68},
			Pins: []layoutPin{{Number: "1", X: 50, Y: 70}},
		},
	}
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, 95, 0, 105}},
		{ID: "w2", Points: []float64{110, 95, 110, 105}},
		{ID: "w3", Points: []float64{50, 90, 50, 70}},
	}
	return items, comps, wires
}

func TestPlanDirectConnectMarkerMove(t *testing.T) {
	items, comps, wires := dcMarkerFixture()
	plans := planDirectConnectTextMoves(items, comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 300, MaxY: 200}, nil, 250, 40)
	if len(plans) != 1 || plans[0].Status != "marker-move-proposed" || plans[0].MarkerMove == nil {
		t.Fatalf("want one marker-move proposal, got %+v", plans)
	}
	mv := plans[0].MarkerMove
	if mv.MarkerID != "m1" || mv.StubWireID != "w3" || mv.DeltaY >= 0 {
		t.Fatalf("wrong marker move: %+v", mv)
	}
	if mv.Host == nil || mv.Host.X != 50 || mv.Host.Y != 70 {
		t.Fatalf("host pin must stay put: %+v", mv.Host)
	}
	if len(mv.NewStubPath) == 0 || mv.NewMarkerBBox == nil {
		t.Fatalf("proposal must carry the re-routed stub and the new bbox: %+v", mv)
	}
	if len(mv.AttachedTextIDs) != 1 || mv.AttachedTextIDs[0] != "mt1" {
		t.Fatalf("marker Name text must move with it: %+v", mv.AttachedTextIDs)
	}
}

func TestPlanDirectConnectMarkerMoveUsesAsymmetricOffset(t *testing.T) {
	items, comps, wires := dcMarkerFixture()
	// A free text band blocks the straight-down move; only a shifted-down
	// (non-axis, non-equal-diagonal) position clears it.
	items = append(items, visibleTextItem{
		ID: "obs", Kind: "text", Content: "note",
		BBox: &layoutBBox{MinX: 40, MinY: 85, MaxX: 50, MaxY: 90},
	})
	items = append(items, visibleTextItem{
		ID: "obs2", Kind: "text", Content: "note2",
		BBox: &layoutBBox{MinX: 52, MinY: 85, MaxX: 58, MaxY: 93},
	})
	plans := planDirectConnectTextMoves(items, comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 300, MaxY: 200}, nil, 250, 40)
	if len(plans) != 1 || plans[0].Status != "marker-move-proposed" || plans[0].MarkerMove == nil {
		t.Fatalf("want a marker move, got %+v", plans)
	}
	mv := plans[0].MarkerMove
	if mv.DeltaX == 0 || mv.DeltaY == 0 || math.Abs(mv.DeltaX) == math.Abs(mv.DeltaY) {
		t.Fatalf("the clearing position requires an asymmetric offset, got %+v", mv)
	}
}

func TestPlanDirectConnectMarkerMoveRejectsSharedTree(t *testing.T) {
	items, comps, wires := dcMarkerFixture()
	wires = append(wires, schGroupWire{ID: "w4", Points: []float64{50, 70, 20, 70}})
	plans := planDirectConnectTextMoves(items, comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 300, MaxY: 200}, nil, 250, 40)
	if len(plans) != 1 || plans[0].Status != "unresolved" || plans[0].MarkerMove != nil {
		t.Fatalf("a branched stub tree must stay unresolved, got %+v", plans)
	}
	if len(plans[0].MarkerSkips) == 0 ||
		!strings.Contains(plans[0].MarkerSkips[0].Reason, "shared-or-branched") {
		t.Fatalf("skip reason must name the shared tree: %+v", plans[0].MarkerSkips)
	}
}

func TestMarkerMoveClearRejectsPartOnNewStub(t *testing.T) {
	items, comps, wires := dcMarkerFixture()
	c := comps[0]
	stub, reason := stubOfMarker(c, wires)
	if stub == nil {
		t.Fatalf("fixture stub not found: %s", reason)
	}
	comps = append(comps, layoutComp{
		ID: "p2", ComponentType: "part", Designator: "C1",
		BBox: &layoutBBox{MinX: 20, MinY: 65, MaxX: 40, MaxY: 75},
	})
	delta := [2]float64{-20, -10}
	segs := markerStubPath(stub.HostX, stub.HostY, c.X+delta[0], c.Y+delta[1], 0)
	if markerMoveClear(c, delta, stub, segs, items, comps, wires,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 300, MaxY: 200}, nil) {
		t.Fatal("a part sitting on the new elbow must reject the move")
	}
}

func TestMarkerMoveClearRejectsMovedTextOverlap(t *testing.T) {
	items, comps, wires := dcMarkerFixture()
	c := comps[0]
	stub, reason := stubOfMarker(c, wires)
	if stub == nil {
		t.Fatalf("fixture stub not found: %s", reason)
	}
	items = append(items, visibleTextItem{
		ID: "other", Kind: "text", Content: "note",
		BBox: &layoutBBox{MinX: 45, MinY: 86, MaxX: 55, MaxY: 90},
	})
	delta := [2]float64{0, -10}
	segs := markerStubPath(stub.HostX, stub.HostY, c.X+delta[0], c.Y+delta[1], 0)
	if markerMoveClear(c, delta, stub, segs, items, comps, wires,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 300, MaxY: 200}, nil) {
		t.Fatal("moving the Name band onto another text must be rejected")
	}
}

func TestMarkerMoveClearRejectsTitleBlock(t *testing.T) {
	items, comps, wires := dcMarkerFixture()
	c := comps[0]
	stub, reason := stubOfMarker(c, wires)
	if stub == nil {
		t.Fatalf("fixture stub not found: %s", reason)
	}
	delta := [2]float64{0, -10}
	segs := markerStubPath(stub.HostX, stub.HostY, c.X+delta[0], c.Y+delta[1], 0)
	if markerMoveClear(c, delta, stub, segs, items, comps, wires,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 300, MaxY: 200},
		&layoutBBox{MinX: 40, MinY: 80, MaxX: 60, MaxY: 90}) {
		t.Fatal("a marker body inside the title-block keep-out must be rejected")
	}
}

func TestMarkerMoveClearRejectsBodyOverLabel(t *testing.T) {
	items, comps, wires := dcMarkerFixture()
	c := comps[0]
	stub, reason := stubOfMarker(c, wires)
	if stub == nil {
		t.Fatalf("fixture stub not found: %s", reason)
	}
	items = append(items, visibleTextItem{
		ID: "label", Kind: "attribute", ComponentType: "wire", ParentID: "w2",
		Key: "Name", Value: "SDA", BBox: &layoutBBox{MinX: 41, MinY: 84, MaxX: 49, MaxY: 88},
	})
	delta := [2]float64{0, -10}
	segs := markerStubPath(stub.HostX, stub.HostY, c.X+delta[0], c.Y+delta[1], 0)
	if markerMoveClear(c, delta, stub, segs, items, comps, wires,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 300, MaxY: 200}, nil) {
		t.Fatal("a marker body moved onto a wire label must be rejected")
	}
}

func TestPlanDirectConnectMarkerMoveIgnoresNetLabel(t *testing.T) {
	items, comps, wires := dcMarkerFixture()
	comps[0].ComponentType = "netlabel"
	plans := planDirectConnectTextMoves(items, comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 300, MaxY: 200}, nil, 250, 40)
	if len(plans) != 1 || plans[0].Status != "unresolved" || plans[0].MarkerMove != nil {
		t.Fatalf("netlabel must not enter the marker-move layer, got %+v", plans)
	}
}

func TestDirectConnectPlanUsesWireAttachmentPoint(t *testing.T) {
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, 0, 0, 100}},
		{ID: "w2", Points: []float64{200, 0, 200, 100}},
	}
	items := []visibleTextItem{
		{
			ID: "t1", Kind: "attribute", ComponentType: "wire", ParentID: "w1",
			Key: "Name", Value: "SIG", BBox: &layoutBBox{MinX: 10, MinY: 45, MaxX: 30, MaxY: 55},
		},
		{
			ID: "t2", Kind: "attribute", ComponentType: "wire", ParentID: "w2",
			Key: "Name", Value: "SIG", BBox: &layoutBBox{MinX: 170, MinY: 45, MaxX: 190, MaxY: 55},
		},
	}
	// The part sits between w1 and t1's text. A route from the text center (20,50)
	// would miss it; a route from the real wire attachment (0,50) crosses it.
	comps := []layoutComp{{
		ID: "p1", ComponentType: "part", Designator: "D1",
		BBox: &layoutBBox{MinX: 2, MinY: 45, MaxX: 8, MaxY: 55},
	}}
	plans := planDirectConnectTextMoves(items, comps, wires, nil,
		&layoutBBox{MinX: -20, MinY: -20, MaxX: 240, MaxY: 140}, nil, 250, 40)
	if len(plans) != 1 || plans[0].Status != "unresolved" {
		t.Fatalf("route must start on the wire, not the text center: %+v", plans)
	}
	if len(plans[0].BlockersA) == 0 || !strings.Contains(strings.Join(plans[0].BlockersA, ","), "part:D1") {
		t.Fatalf("wire-side obstacle must be reported: %+v", plans[0].BlockersA)
	}
}

func TestPlanDirectConnectMarkerMoveFromWireBlocker(t *testing.T) {
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, 60, 0, 70}},
		{ID: "w2", Points: []float64{20, 70, 20, 80}},
		{ID: "w3", Points: []float64{10, 80, 10, 60}},
	}
	items := []visibleTextItem{
		{
			ID: "t1", Kind: "attribute", ComponentType: "wire", ParentID: "w1",
			Key: "Name", Value: "SIG", BBox: &layoutBBox{MinX: 0, MinY: 65, MaxX: 10, MaxY: 75},
		},
		{
			ID: "t2", Kind: "attribute", ComponentType: "wire", ParentID: "w2",
			Key: "Name", Value: "SIG", BBox: &layoutBBox{MinX: 20, MinY: 65, MaxX: 30, MaxY: 75},
		},
		{
			ID: "mt1", Kind: "attribute", ComponentType: "netflag", ParentID: "m1",
			Key: "Name", Value: "+5V", BBox: &layoutBBox{MinX: 4, MinY: 88, MaxX: 16, MaxY: 96},
		},
	}
	comps := []layoutComp{
		{
			ID: "m1", ComponentType: "netflag", Net: "+5V", X: 10, Y: 80, AnchorAvailable: true,
			BBox: &layoutBBox{MinX: 5, MinY: 82, MaxX: 15, MaxY: 88},
			Pins: []layoutPin{{Number: "1", X: 10, Y: 80}},
		},
		{
			ID: "p1", ComponentType: "part", Designator: "R1",
			BBox: &layoutBBox{MinX: 5, MinY: 50, MaxX: 15, MaxY: 55},
			Pins: []layoutPin{{Number: "1", X: 10, Y: 60}},
		},
	}
	plans := planDirectConnectTextMoves(items, comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil, 250, 40)
	// The only clear route would start the stub horizontally at the host pin,
	// perpendicular to its outward (up) direction. That is illegal, so the
	// planner must nominate the marker from the wire blocker and then refuse it.
	if len(plans) != 1 || plans[0].MarkerMove != nil {
		t.Fatalf("a perpendicular first stub segment must be refused: %+v", plans)
	}
	if plans[0].Status != "unresolved" && plans[0].Status != "part-move-proposed" {
		t.Fatalf("the marker refusal must fall through to unresolved or a part move: %+v", plans)
	}
	found := false
	for _, s := range plans[0].MarkerSkips {
		if s.MarkerID == "m1" && strings.Contains(s.Reason, "no-clear-stub-move") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the wire blocker must nominate m1 and report the refusal: %+v", plans[0].MarkerSkips)
	}
}

func TestMarkerStubFirstSegmentFollowsHostDirection(t *testing.T) {
	horizontalFirst := markerStubPath(10, 60, 50, 80, 0)
	if firstSegmentMatchesDir(horizontalFirst, "up") {
		t.Fatal("a horizontal first segment must not satisfy an up-facing host pin")
	}
	verticalFirst := markerStubPath(10, 60, 50, 80, 1)
	if !firstSegmentMatchesDir(verticalFirst, "up") {
		t.Fatal("an up-first folded route must satisfy an up-facing host pin")
	}
	if firstSegmentMatchesDir(verticalFirst, "down") {
		t.Fatal("an up-first route must not satisfy a down-facing host pin")
	}
}

func TestDCDeltaCandidatesCoverAsymmetricGrid(t *testing.T) {
	deltas := dcDeltaCandidates(40)
	if len(deltas) != 17*17-1 {
		t.Fatalf("want the full 5-unit grid, got %d candidates", len(deltas))
	}
	want := map[[2]float64]bool{{40, -15}: false, {15, 5}: false, {-20, 40}: false}
	prev := -1
	for _, d := range deltas {
		if d[0] == 0 && d[1] == 0 {
			t.Fatal("zero delta must not be a candidate")
		}
		if math.Abs(d[0]) > 40 || math.Abs(d[1]) > 40 {
			t.Fatalf("delta outside maxMove: %+v", d)
		}
		if math.Mod(d[0], 5) != 0 || math.Mod(d[1], 5) != 0 {
			t.Fatalf("delta is off the 5-unit grid: %+v", d)
		}
		cost := int(math.Abs(d[0]) + math.Abs(d[1]))
		if cost < prev {
			t.Fatalf("candidates are not cost-sorted at %+v", d)
		}
		prev = cost
		if _, ok := want[d]; ok {
			want[d] = true
		}
	}
	for d, seen := range want {
		if !seen {
			t.Fatalf("asymmetric delta %+v missing", d)
		}
	}
}

func dcPartMoveFixture() ([]visibleTextItem, []layoutComp, []schGroupWire) {
	label := func(id, parent string, x0, x1 float64) visibleTextItem {
		return visibleTextItem{
			ID: id, Kind: "attribute", ComponentType: "wire", ParentID: parent,
			Key: "Name", Value: "SIG", BBox: &layoutBBox{MinX: x0, MinY: 65, MaxX: x1, MaxY: 75},
		}
	}
	items := []visibleTextItem{
		label("t1", "w1", 0, 10),
		label("t2", "w2", 40, 50),
		{
			ID: "pv", Kind: "attribute", ComponentType: "part", ParentID: "p1",
			Key: "Value", Value: "10k", BBox: &layoutBBox{MinX: 15, MinY: 80, MaxX: 25, MaxY: 88},
		},
		{
			ID: "fv", Kind: "attribute", ComponentType: "netflag", ParentID: "f1",
			Key: "Name", Value: "+5V", BBox: &layoutBBox{MinX: 14, MinY: 48, MaxX: 26, MaxY: 56},
		},
	}
	comps := []layoutComp{
		{
			ID: "p1", ComponentType: "part", Designator: "R9", X: 20, Y: 50,
			BBox: &layoutBBox{MinX: 15, MinY: 66, MaxX: 25, MaxY: 74},
			Pins: []layoutPin{{Number: "1", X: 20, Y: 60}, {Number: "2", X: 20, Y: 80}},
		},
		{
			ID: "f1", ComponentType: "netflag", Net: "+5V", X: 20, Y: 40, AnchorAvailable: true,
			BBox: &layoutBBox{MinX: 15, MinY: 42, MaxX: 25, MaxY: 48},
			Pins: []layoutPin{{Number: "1", X: 20, Y: 40}},
		},
	}
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, 60, 0, 70}},
		{ID: "w2", Points: []float64{40, 60, 40, 70}},
		{ID: "w3", Points: []float64{20, 60, 20, 40}},
		{ID: "w4", Points: []float64{20, 80, 20, 100}},
	}
	return items, comps, wires
}

func TestPlanDirectConnectPartMove(t *testing.T) {
	items, comps, wires := dcPartMoveFixture()
	plans := planDirectConnectTextMoves(items, comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil, 250, 40)
	if len(plans) != 1 || plans[0].Status != "part-move-proposed" || plans[0].PartMove == nil {
		t.Fatalf("want one rigid part move, got %+v", plans)
	}
	mv := plans[0].PartMove
	if mv.PartID != "p1" || (mv.DeltaX == 0 && mv.DeltaY == 0) {
		t.Fatalf("wrong part move: %+v", mv)
	}
	if len(mv.WireIDs) != 2 || len(mv.FlagIDs) != 1 {
		t.Fatalf("part must carry both stubs and the flag: %+v", mv)
	}
	for _, id := range []string{"p1", "f1", "pv", "fv"} {
		found := id == mv.PartID
		for _, f := range mv.FlagIDs {
			if f == id {
				found = true
			}
		}
		for _, t := range mv.TextIDs {
			if t == id {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s must move with the rigid set: %+v", id, mv)
		}
	}
}

func TestPlanDirectConnectPartMoveRejectsSharedTree(t *testing.T) {
	items, comps, wires := dcPartMoveFixture()
	wires = append(wires, schGroupWire{ID: "w5", Points: []float64{20, 60, 100, 60}})
	comps = append(comps, layoutComp{
		ID: "p2", ComponentType: "part", Designator: "U9", X: 100, Y: 50,
		BBox: &layoutBBox{MinX: 98, MinY: 40, MaxX: 102, MaxY: 70},
		Pins: []layoutPin{{Number: "1", X: 100, Y: 60}},
	})
	plans := planDirectConnectTextMoves(items, comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil, 250, 40)
	if len(plans) != 1 || plans[0].PartMove != nil {
		t.Fatalf("a shared wire tree must refuse the single-part move, got %+v", plans)
	}
	if plans[0].Status != "unresolved" && plans[0].Status != "group-move-proposed" {
		t.Fatalf("a shared tree must fall through to a group move or unresolved, got %+v", plans)
	}
	if len(plans[0].PartSkips) == 0 || !strings.Contains(plans[0].PartSkips[0].Reason, "shared-wire-tree") {
		t.Fatalf("skip reason must name the shared tree: %+v", plans[0].PartSkips)
	}
}

func TestPlanDirectConnectPartMoveRejectsPinContact(t *testing.T) {
	items, comps, wires := dcPartMoveFixture()
	comps = append(comps, layoutComp{
		ID: "p2", ComponentType: "part", Designator: "U9", X: 30, Y: 60,
		BBox: &layoutBBox{MinX: 25, MinY: 55, MaxX: 35, MaxY: 65},
		Pins: []layoutPin{{Number: "1", X: 20, Y: 60}},
	})
	plans := planDirectConnectTextMoves(items, comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil, 250, 40)
	if len(plans) != 1 || plans[0].PartMove != nil {
		t.Fatalf("a pin-to-pin contact must refuse the single-part move, got %+v", plans)
	}
	if plans[0].Status != "unresolved" && plans[0].Status != "group-move-proposed" {
		t.Fatalf("a pin contact must fall through to a group move or unresolved, got %+v", plans)
	}
	if len(plans[0].PartSkips) == 0 || !strings.Contains(plans[0].PartSkips[0].Reason, "pin-to-pin") {
		t.Fatalf("skip reason must name the pin contact: %+v", plans[0].PartSkips)
	}
}

func TestPartMoveClearRejectsUnmovedPartOnMovedTree(t *testing.T) {
	items, comps, wires := dcPartMoveFixture()
	c := comps[0]
	comps = append(comps, layoutComp{
		ID: "p2", ComponentType: "part", Designator: "U9",
		BBox: &layoutBBox{MinX: 15, MinY: 105, MaxX: 25, MaxY: 115},
	})
	movedComp := map[string]bool{"p1": true, "f1": true}
	movedWire := map[string]bool{"w3": true, "w4": true}
	if partMoveClear(c, [2]float64{0, 35}, movedComp, movedWire, []string{"pv", "fv"},
		items, comps, wires, &layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil) {
		t.Fatal("a moved wire tree landing on an unmoved part must be rejected")
	}
}

func TestPlanDirectConnectPartMoveFromWireBlocker(t *testing.T) {
	items := []visibleTextItem{
		{
			ID: "t1", Kind: "attribute", ComponentType: "wire", ParentID: "w1",
			Key: "Name", Value: "SIG", BBox: &layoutBBox{MinX: 0, MinY: 65, MaxX: 10, MaxY: 75},
		},
		{
			ID: "t2", Kind: "attribute", ComponentType: "wire", ParentID: "w2",
			Key: "Name", Value: "SIG", BBox: &layoutBBox{MinX: 40, MinY: 65, MaxX: 50, MaxY: 75},
		},
	}
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, 60, 0, 80}},
		{ID: "w2", Points: []float64{40, 60, 40, 80}},
		{ID: "w3", Points: []float64{20, 60, 20, 90}},
	}
	comps := []layoutComp{{
		ID: "p1", ComponentType: "part", Designator: "R1", X: 20, Y: 50,
		BBox: &layoutBBox{MinX: 15, MinY: 45, MaxX: 25, MaxY: 55},
		Pins: []layoutPin{{Number: "1", X: 20, Y: 60}, {Number: "2", X: 20, Y: 80}},
	}}
	plans := planDirectConnectTextMoves(items, comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil, 250, 40)
	if len(plans) != 1 || plans[0].Status != "part-move-proposed" || plans[0].PartMove == nil {
		t.Fatalf("a wire-only blocker must nominate its owner part: %+v", plans)
	}
	mv := plans[0].PartMove
	if mv.PartID != "p1" || mv.DeltaX == 0 || len(mv.WireIDs) != 1 || mv.WireIDs[0] != "w3" {
		t.Fatalf("wrong owner-part move: %+v", mv)
	}
}

func TestPlanDirectConnectGroupMoveForSharedTree(t *testing.T) {
	items := []visibleTextItem{
		{
			ID: "t1", Kind: "attribute", ComponentType: "wire", ParentID: "w1",
			Key: "Name", Value: "SIG", BBox: &layoutBBox{MinX: 0, MinY: 45, MaxX: 10, MaxY: 55},
		},
		{
			ID: "t2", Kind: "attribute", ComponentType: "wire", ParentID: "w2",
			Key: "Name", Value: "SIG", BBox: &layoutBBox{MinX: 40, MinY: 45, MaxX: 50, MaxY: 55},
		},
	}
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, 40, 0, 60}},
		{ID: "w2", Points: []float64{40, 40, 40, 60}},
		{ID: "w3", Points: []float64{20, 30, 20, 60}},
	}
	comps := []layoutComp{
		{
			ID: "p1", ComponentType: "part", Designator: "R1", X: 20, Y: 70,
			BBox: &layoutBBox{MinX: 15, MinY: 65, MaxX: 25, MaxY: 75},
			Pins: []layoutPin{{Number: "1", X: 20, Y: 60}, {Number: "2", X: 20, Y: 80}},
		},
		{
			ID: "p2", ComponentType: "part", Designator: "R2", X: 20, Y: 10,
			BBox: &layoutBBox{MinX: 15, MinY: 0, MaxX: 25, MaxY: 10},
			Pins: []layoutPin{{Number: "1", X: 20, Y: 30}, {Number: "2", X: 20, Y: 20}},
		},
	}
	plans := planDirectConnectTextMoves(items, comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil, 250, 40)
	if len(plans) != 1 || plans[0].Status != "group-move-proposed" || plans[0].GroupMove == nil {
		t.Fatalf("want a 2-part group move, got %+v", plans)
	}
	mv := plans[0].GroupMove
	if len(mv.PartIDs) != 2 || mv.PartIDs[0] != "p1" || mv.PartIDs[1] != "p2" ||
		mv.DeltaX == 0 || len(mv.WireIDs) != 1 || mv.WireIDs[0] != "w3" {
		t.Fatalf("wrong group move: %+v", mv)
	}
}

func TestPlanDirectConnectRejectsPathThroughTitleBlock(t *testing.T) {
	items := []visibleTextItem{
		{
			ID: "t1", Kind: "attribute", ComponentType: "wire", ParentID: "w1",
			Key: "Name", Value: "SIG", BBox: &layoutBBox{MinX: 0, MinY: 95, MaxX: 10, MaxY: 105},
		},
		{
			ID: "t2", Kind: "attribute", ComponentType: "wire", ParentID: "w2",
			Key: "Name", Value: "SIG", BBox: &layoutBBox{MinX: 110, MinY: 95, MaxX: 120, MaxY: 105},
		},
	}
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, 95, 0, 105}},
		{ID: "w2", Points: []float64{110, 95, 110, 105}},
	}
	plans := planDirectConnectTextMoves(items, nil, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200},
		&layoutBBox{MinX: 40, MinY: 95, MaxX: 60, MaxY: 105}, 250, 40)
	if len(plans) != 1 || plans[0].Status != "unresolved" ||
		!strings.Contains(plans[0].Note, "title-block") {
		t.Fatalf("a route through the title block must be refused: %+v", plans)
	}
}
