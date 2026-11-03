package app

import "testing"

func TestNetLabelStubFindingOnBranchTail(t *testing.T) {
	wires := []schGroupWire{{
		ID: "trunk", Net: "FB1",
		ObservedSegments: [][4]float64{
			{0, 0, 100, 0},
			{50, 0, 50, 20},
		},
	}}
	items := []visibleTextItem{{
		ID: "label", Kind: "attribute", ComponentType: "wire", Key: "Name",
		Value: "FB1", ParentID: "trunk",
		BBox: &layoutBBox{MinX: 45, MinY: 20, MaxX: 60, MaxY: 28},
	}}
	got := netLabelStubFindings(items, nil, wires)
	if len(got) != 1 || got[0].Type != "label-stub" || got[0].Level != "ERROR" {
		t.Fatalf("expected one label-stub ERROR, got %+v", got)
	}
}

func TestNetLabelStubIgnoresLabelOnTrunk(t *testing.T) {
	wires := []schGroupWire{{
		ID: "trunk", Net: "FB1",
		ObservedSegments: [][4]float64{
			{0, 0, 100, 0},
			{50, 0, 50, 20},
		},
	}}
	items := []visibleTextItem{{
		ID: "label", Kind: "attribute", ComponentType: "wire", Key: "Name",
		Value: "FB1", ParentID: "trunk",
		BBox: &layoutBBox{MinX: 40, MinY: -8, MaxX: 60, MaxY: 0},
	}}
	if got := netLabelStubFindings(items, nil, wires); len(got) != 0 {
		t.Fatalf("label attached on trunk must not be a stub, got %+v", got)
	}
}

func TestNetLabelStubIgnoresLeafAtPinnedEndpoint(t *testing.T) {
	wires := []schGroupWire{{
		ID: "trunk", Net: "FB1",
		ObservedSegments: [][4]float64{
			{0, 0, 100, 0},
			{50, 0, 50, 20},
		},
	}}
	items := []visibleTextItem{{
		ID: "label", Kind: "attribute", ComponentType: "wire", Key: "Name",
		Value: "FB1", ParentID: "trunk",
		BBox: &layoutBBox{MinX: 45, MinY: 20, MaxX: 60, MaxY: 28},
	}}
	comps := []layoutComp{{
		ID: "R1", ComponentType: "part",
		Pins: []layoutPin{{Number: "1", X: 50, Y: 20}},
	}}
	if got := netLabelStubFindings(items, comps, wires); len(got) != 0 {
		t.Fatalf("a real pin at the leaf is a connection, not a decorative tail: %+v", got)
	}
}

func TestBuildLabelStubFixReportPlansSingleWireRepair(t *testing.T) {
	wire := schGroupWire{
		ID: "trunk", Net: "FB1", Points: []float64{0, 0, 100, 0, 50, 0, 50, 20},
		ObservedSegments: [][4]float64{{0, 0, 100, 0}, {50, 0, 50, 20}},
	}
	label := visibleTextItem{
		ID: "label", Kind: "attribute", ComponentType: "wire", Key: "Name",
		Value: "FB1", ParentID: "trunk",
		BBox: &layoutBBox{MinX: 45, MinY: 20, MaxX: 60, MaxY: 28},
	}
	scene := labelStubFixScene{
		Wires: []schGroupWire{wire}, Items: []visibleTextItem{label},
		Geos: labelStubGeometries([]visibleTextItem{label}, nil, []schGroupWire{wire}),
	}
	report := buildLabelStubFixReport("doc", scene, nil)
	if len(report.Proposals) != 1 || report.Proposals[0].Status != "ready" ||
		len(report.Proposals[0].Replacement) != 1 {
		t.Fatalf("expected one ready single-wire repair, got %+v", report)
	}
}

func TestNetLabelStubFindingAcrossConnectedWirePrimitives(t *testing.T) {
	wires := []schGroupWire{
		{ID: "trunk", Net: "FB1", ObservedSegments: [][4]float64{{0, 0, 100, 0}}},
		{ID: "tail", Net: "FB1", ObservedSegments: [][4]float64{{50, 0, 50, 20}}},
	}
	items := []visibleTextItem{{
		ID: "label", Kind: "attribute", ComponentType: "wire", Key: "Name",
		Value: "FB1", ParentID: "tail",
		BBox: &layoutBBox{MinX: 45, MinY: 20, MaxX: 60, MaxY: 28},
	}}
	got := netLabelStubFindings(items, nil, wires)
	if len(got) != 1 || got[0].Type != "label-stub" {
		t.Fatalf("expected cross-primitive physical tree to be detected, got %+v", got)
	}
}

func TestChooseLabelStubDirectAnchorSkipsPredictedTextCollision(t *testing.T) {
	p := labelStubFixProposal{
		Net: "FB1", Leaf: checkPoint{X: 0, Y: 0}, Junction: checkPoint{X: 0, Y: 0},
		Replacement: [][4]float64{{0, 0, 100, 0}},
		TextBBox:    &layoutBBox{MinX: 0, MinY: 0, MaxX: 20, MaxY: 8},
	}
	scene := labelStubFixScene{Comps: []layoutComp{{
		ID: "GND", ComponentType: "netflag", Net: "GND",
		BBox: &layoutBBox{MinX: 0, MinY: -2, MaxX: 20, MaxY: 10},
	}}}
	got, err := chooseLabelStubDirectAnchor(&p, scene)
	if err != nil {
		t.Fatal(err)
	}
	if got.X < 20 {
		t.Fatalf("expected a downstream clear slot, got %+v", got)
	}
}
