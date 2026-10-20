package app

import (
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
		map[string]checkPoint{"v1": {X: 100, Y: 44}}, &layoutBBox{MinX: 0, MinY: 0, MaxX: 300, MaxY: 200}, nil, 250, 40)
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
		map[string]checkPoint{"v1": {X: 100, Y: 44}}, &layoutBBox{MinX: 0, MinY: 0, MaxX: 300, MaxY: 200}, nil, 250, 40)
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
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 300, MaxY: 200}, nil, 250, 40)
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
		map[string]checkPoint{"v1": {X: 100, Y: 44}}, &layoutBBox{MinX: 0, MinY: 0, MaxX: 300, MaxY: 200}, nil, 250, 40)
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
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 300, MaxY: 200}, nil, 250, 40)
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

func TestPlanDirectConnectMarkerMoveRejectsSharedTree(t *testing.T) {
	items, comps, wires := dcMarkerFixture()
	wires = append(wires, schGroupWire{ID: "w4", Points: []float64{50, 70, 20, 70}})
	plans := planDirectConnectTextMoves(items, comps, wires, nil,
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 300, MaxY: 200}, nil, 250, 40)
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
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 300, MaxY: 200}, nil) {
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
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 300, MaxY: 200}, nil) {
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
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 300, MaxY: 200},
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
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 300, MaxY: 200}, nil) {
		t.Fatal("a marker body moved onto a wire label must be rejected")
	}
}

func TestPlanDirectConnectMarkerMoveIgnoresNetLabel(t *testing.T) {
	items, comps, wires := dcMarkerFixture()
	comps[0].ComponentType = "netlabel"
	plans := planDirectConnectTextMoves(items, comps, wires, nil,
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 300, MaxY: 200}, nil, 250, 40)
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
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 200, MaxY: 200}, nil, 250, 40)
	if len(plans) != 1 || plans[0].Status != "marker-move-proposed" || plans[0].MarkerMove == nil {
		t.Fatalf("a stub-only blocker must nominate its marker: %+v", plans)
	}
	if plans[0].MarkerMove.MarkerID != "m1" || plans[0].MarkerMove.StubWireID != "w3" ||
		plans[0].MarkerMove.DeltaX <= 0 {
		t.Fatalf("wrong stub-owner proposal: %+v", plans[0].MarkerMove)
	}
}
