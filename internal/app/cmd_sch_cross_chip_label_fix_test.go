package app

import "testing"

func crossChipFixture() ([]layoutComp, []schGroupWire) {
	mk := func(id, designator string, bbox layoutBBox, x float64, outward float64) layoutComp {
		return layoutComp{
			ID: id, Designator: designator, ComponentType: schLayoutPartType,
			X: x, Y: 100, AnchorAvailable: true,
			BBox: &bbox,
			Pins: []layoutPin{
				{Number: "1", X: x, Y: 85, Rotation: outward, RotationKnown: true},
				{Number: "2", X: x, Y: 90, Rotation: outward, RotationKnown: true},
				{Number: "3", X: x, Y: 95, Rotation: outward, RotationKnown: true},
				{Number: "4", X: x, Y: 100, Rotation: outward, RotationKnown: true, Net: "SIG"},
			},
		}
	}
	comps := []layoutComp{
		mk("u1", "U1", layoutBBox{MinX: 80, MinY: 80, MaxX: 100, MaxY: 120}, 100, 0),
		mk("u2", "U2", layoutBBox{MinX: 220, MinY: 80, MaxX: 240, MaxY: 120}, 220, 180),
	}
	wires := []schGroupWire{{
		ID: "tree", Net: "SIG", Points: []float64{100, 100, 220, 100},
	}}
	return comps, wires
}

func TestBuildCrossChipFixProposalConvertsEveryPin(t *testing.T) {
	comps, wires := crossChipFixture()
	findings := crossChipDirectWireFindings(comps, wires)
	got := buildCrossChipFixProposals(comps, wires, nil,
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 400, MaxY: 200}, nil, findings)
	if len(got) != 1 || got[0].Status != "ready" {
		t.Fatalf("proposal = %+v", got)
	}
	if len(got[0].WireIDs) != 1 || got[0].WireIDs[0] != "tree" ||
		len(got[0].PinRefs) != 2 || len(got[0].Connections) != 2 {
		t.Fatalf("conversion proposal incomplete: %+v", got[0])
	}
	for _, connection := range got[0].Connections {
		if connection.Kind != "net_label" || connection.Net != "SIG" || connection.Selected == nil {
			t.Fatalf("bad label connection: %+v", connection)
		}
	}
}

func TestBuildCrossChipFixBlocksExistingMarkerOnTree(t *testing.T) {
	comps, wires := crossChipFixture()
	rotation := 0.0
	comps = append(comps, layoutComp{
		ID: "marker", ComponentType: "netport", Net: "SIG", X: 100, Y: 100,
		AnchorAvailable: true, Rotation: &rotation,
		BBox: &layoutBBox{MinX: 90, MinY: 95, MaxX: 100, MaxY: 105},
	})
	findings := crossChipDirectWireFindings(comps[:2], wires)
	got := buildCrossChipFixProposals(comps, wires, nil,
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 400, MaxY: 200}, nil, findings)
	if len(got) != 1 || got[0].Status != "blocked" ||
		got[0].BlockReason != "tree-carries-existing-marker:marker" {
		t.Fatalf("marker conflict proposal = %+v", got)
	}
}

func TestBuildCrossChipFixBlocksMixedPinNet(t *testing.T) {
	comps, wires := crossChipFixture()
	comps[1].Pins[3].Net = "OTHER"
	findings := crossChipDirectWireFindings(comps, wires)
	got := buildCrossChipFixProposals(comps, wires, nil,
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 400, MaxY: 200}, nil, findings)
	if len(got) != 1 || got[0].Status != "blocked" ||
		got[0].BlockReason != "ambiguous-shared-signal-net" {
		t.Fatalf("mixed-net proposal = %+v", got)
	}
}
