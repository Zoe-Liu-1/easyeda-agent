package app

import "testing"

func TestNetLabelOrientationFindings(t *testing.T) {
	rot := func(v float64) *float64 { return &v }
	items := []visibleTextItem{
		{ID: "ok", ComponentType: "wire", Key: "Name", Value: "SIG", Rotation: rot(0)},
		{ID: "bad", ComponentType: "wire", Key: "Name", Value: "SIG", Rotation: rot(90)},
		{ID: "missing", ComponentType: "wire", Key: "Name", Value: "SIG"},
		{ID: "gnd", ComponentType: "wire", Key: "Name", Value: "GND", Rotation: rot(90)},
		{ID: "empty", ComponentType: "wire", Key: "Name", Value: "", Rotation: rot(90)},
	}
	got := netLabelOrientationFindings(items)
	if len(got) != 2 {
		t.Fatalf("want label-orientation + unverified, got %d: %+v", len(got), got)
	}
	if got[0].Type != "label-orientation" || got[0].PrimitiveId != "bad" {
		t.Fatalf("wrong orientation finding: %+v", got[0])
	}
	if got[1].Type != "label-orientation-unverified" || got[1].Level != "info" {
		t.Fatalf("wrong unverified finding: %+v", got[1])
	}
}

func TestNetLabelEconomyFindings(t *testing.T) {
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, 0, 0, 100}},
		{ID: "w2", Points: []float64{0, 100, 100, 100}},
		{ID: "w3", Points: []float64{200, 0, 200, 100}},
	}
	items := []visibleTextItem{
		{ID: "a", ComponentType: "wire", ParentID: "w1", Key: "Name", Value: "SIG"},
		{ID: "b", ComponentType: "wire", ParentID: "w2", Key: "Name", Value: "SIG"},
		{ID: "c", ComponentType: "wire", ParentID: "w3", Key: "Name", Value: "SIG"},
	}
	got := netLabelEconomyFindings(items, wires)
	if len(got) != 1 {
		t.Fatalf("want one duplicate-label finding, got %d: %+v", len(got), got)
	}
	if got[0].Type != "label-economy" || got[0].Count != 2 ||
		got[0].SuggestKeepId != "a" || len(got[0].SuggestDeleteIds) != 1 ||
		got[0].SuggestDeleteIds[0] != "b" {
		t.Fatalf("wrong label-economy finding: %+v", got[0])
	}
}

func TestNetLabelEconomyFindingsDifferentNets(t *testing.T) {
	wires := []schGroupWire{{ID: "w1", Points: []float64{0, 0, 0, 100}}}
	items := []visibleTextItem{
		{ID: "a", ComponentType: "wire", ParentID: "w1", Key: "Name", Value: "SIG_A"},
		{ID: "b", ComponentType: "wire", ParentID: "w1", Key: "Name", Value: "SIG_B"},
	}
	if got := netLabelEconomyFindings(items, wires); len(got) != 0 {
		t.Fatalf("different net names must not be duplicate labels: %+v", got)
	}
}

func TestDirectConnectOpportunityFindings(t *testing.T) {
	box := func(x0, y0, x1, y1 float64) *layoutBBox {
		return &layoutBBox{MinX: x0, MinY: y0, MaxX: x1, MaxY: y1}
	}
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, 0, 0, 100}},
		{ID: "w2", Points: []float64{200, 0, 200, 100}},
	}
	items := []visibleTextItem{
		{ID: "t1", ComponentType: "wire", ParentID: "w1", Key: "Name", Value: "SIG", BBox: box(0, 40, 20, 48)},
		{ID: "t2", ComponentType: "wire", ParentID: "w2", Key: "Name", Value: "SIG", BBox: box(200, 40, 220, 48)},
	}
	got := directConnectOpportunityFindings(items, nil, wires)
	if len(got) != 1 || got[0].Type != "direct-connect-opportunity" || got[0].Level != "info" {
		t.Fatalf("want one direct-connect opportunity, got %+v", got)
	}
}

func TestDirectConnectOpportunityBlockedByPin(t *testing.T) {
	box := func(x0, y0, x1, y1 float64) *layoutBBox {
		return &layoutBBox{MinX: x0, MinY: y0, MaxX: x1, MaxY: y1}
	}
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, 0, 0, 100}},
		{ID: "w2", Points: []float64{200, 0, 200, 100}},
	}
	items := []visibleTextItem{
		{ID: "t1", ComponentType: "wire", ParentID: "w1", Key: "Name", Value: "SIG", BBox: box(0, 40, 20, 48)},
		{ID: "t2", ComponentType: "wire", ParentID: "w2", Key: "Name", Value: "SIG", BBox: box(200, 40, 220, 48)},
	}
	comps := []layoutComp{
		{ID: "jumper", ComponentType: "part", Pins: []layoutPin{{Number: "2", X: 100, Y: 44}}},
	}
	if got := directConnectOpportunityFindings(items, comps, wires); len(got) != 0 {
		t.Fatalf("a pin between the labels must block the L route: %+v", got)
	}
}
