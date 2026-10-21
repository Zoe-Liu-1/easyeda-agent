package app

import "testing"

func TestCrossChipDirectWireFinding(t *testing.T) {
	comps := []layoutComp{
		alignmentCorePart("u1", "U1", 0, 0, "SIG"),
		alignmentCorePart("u2", "U2", 100, 0, "SIG"),
	}
	wires := []schGroupWire{{
		ID: "cross", Net: "SIG",
		ObservedSegments: [][4]float64{{5, 0, 100, 0}},
	}}
	got := crossChipDirectWireFindings(comps, wires)
	if len(got) != 1 || got[0].Type != "cross-chip-direct-wire" ||
		got[0].Level != "warn" || got[0].Designator != "U1,U2" {
		t.Fatalf("wrong cross-chip finding: %+v", got)
	}
}

func TestCrossChipDirectWireIgnoresPowerRail(t *testing.T) {
	comps := []layoutComp{
		alignmentCorePart("u1", "U1", 0, 0, "DC55"),
		alignmentCorePart("u2", "U2", 100, 0, "DC55"),
	}
	wires := []schGroupWire{{
		ID: "rail", Net: "DC55",
		ObservedSegments: [][4]float64{{5, 0, 100, 0}},
	}}
	if got := crossChipDirectWireFindings(comps, wires); len(got) != 0 {
		t.Fatalf("power rail must not use the signal cross-chip rule: %+v", got)
	}
}

func TestDirectConnectOpportunitySkipsDifferentChipIslands(t *testing.T) {
	comps := []layoutComp{
		alignmentCorePart("u1", "U1", 0, 0, "SIG"),
		alignmentCorePart("u2", "U2", 100, 0, "SIG"),
	}
	wires := []schGroupWire{
		{ID: "w1", Net: "SIG", ObservedSegments: [][4]float64{{5, 0, 5, 30}}},
		{ID: "w2", Net: "SIG", ObservedSegments: [][4]float64{{105, 0, 105, 30}}},
	}
	items := []visibleTextItem{
		{ID: "l1", ComponentType: "wire", ParentID: "w1", Key: "Name", Value: "SIG",
			BBox: &layoutBBox{MinX: 5, MinY: 15, MaxX: 25, MaxY: 23}},
		{ID: "l2", ComponentType: "wire", ParentID: "w2", Key: "Name", Value: "SIG",
			BBox: &layoutBBox{MinX: 105, MinY: 15, MaxX: 125, MaxY: 23}},
	}
	if got := directConnectOpportunityFindings(items, comps, wires, nil, nil); len(got) != 0 {
		t.Fatalf("different chips must not get a direct-wire suggestion: %+v", got)
	}
}
