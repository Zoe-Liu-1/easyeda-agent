package app

import "testing"

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
