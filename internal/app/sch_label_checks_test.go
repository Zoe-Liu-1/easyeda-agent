package app

import "testing"

func TestNetLabelOrientationFindings(t *testing.T) {
	rot := func(v float64) *float64 { return &v }
	items := []visibleTextItem{
		{ID: "ok", Kind: "attribute", ComponentType: "wire", Key: "Name", Value: "SIG", Rotation: rot(0)},
		{ID: "bad", Kind: "attribute", ComponentType: "wire", Key: "Name", Value: "SIG", Rotation: rot(90)},
		{ID: "missing", Kind: "attribute", ComponentType: "wire", Key: "Name", Value: "SIG"},
		{ID: "gnd", Kind: "attribute", ComponentType: "wire", Key: "Name", Value: "GND", Rotation: rot(90)},
		{ID: "empty", Kind: "attribute", ComponentType: "wire", Key: "Name", Value: "", Rotation: rot(90)},
	}
	got := netLabelOrientationFindings(items)
	if len(got) != 3 {
		t.Fatalf("want two label-orientation + unverified, got %d: %+v", len(got), got)
	}
	if got[0].Type != "label-orientation" || got[0].PrimitiveId != "bad" {
		t.Fatalf("wrong orientation finding: %+v", got[0])
	}
	if got[1].Type != "label-orientation" || got[1].PrimitiveId != "gnd" {
		t.Fatalf("power/ground name must be checked too: %+v", got[1])
	}
	if got[2].Type != "label-orientation-unverified" || got[2].Level != "info" {
		t.Fatalf("wrong unverified finding: %+v", got[2])
	}
}

func TestNetLabelVisibilityFindings(t *testing.T) {
	visible := true
	hidden := false
	items := []visibleTextItem{
		{ID: "good", Kind: "attribute", ComponentType: "netflag", Key: "Name", Value: "GND", Visible: &visible},
		{ID: "hidden", Kind: "attribute", ComponentType: "netflag", Key: "Name", Value: "GND", Visible: &hidden},
		{ID: "missing", Kind: "marker", ComponentType: "netport", ParentID: "port-1", MissingName: true},
		{ID: "empty", Kind: "attribute", ComponentType: "wire", Key: "Name", Value: "", Visible: &visible},
	}
	got := netLabelVisibilityFindings(items)
	if len(got) != 3 {
		t.Fatalf("want hidden/missing/empty findings, got %d: %+v", len(got), got)
	}
	ids := map[string]bool{}
	for _, f := range got {
		if f.Type != "label-visibility" || f.Level != "warn" {
			t.Fatalf("bad visibility finding: %+v", f)
		}
		ids[f.PrimitiveId] = true
	}
	for _, want := range []string{"hidden", "missing", "empty"} {
		if !ids[want] {
			t.Fatalf("missing finding for %s: %+v", want, got)
		}
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
