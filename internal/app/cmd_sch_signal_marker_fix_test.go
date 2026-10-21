package app

import "testing"

func TestBuildSignalMarkerProposalConvertsNetflagToLabel(t *testing.T) {
	rot := flagBodyRotation["power"]["right"]
	comps := []layoutComp{
		{ID: "f1", ComponentType: "netflag", Net: "SIG", X: 50, Y: 70, AnchorAvailable: true,
			Rotation: &rot, BBox: &layoutBBox{MinX: 50, MinY: 65, MaxX: 60, MaxY: 75}},
		{ID: "u1", ComponentType: schLayoutPartType, Designator: "U1",
			BBox: &layoutBBox{MinX: 10, MinY: 60, MaxX: 30, MaxY: 80},
			Pins: []layoutPin{{Number: "1", X: 40, Y: 70, Rotation: 0, RotationKnown: true}}},
	}
	wires := []schGroupWire{{ID: "w1", Net: "SIG", Points: []float64{40, 70, 50, 70}}}
	got := buildSignalMarkerProposals(comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil)
	if len(got) != 1 || got[0].Status != "ready" || got[0].Direction != "right" {
		t.Fatalf("signal marker proposal = %+v", got)
	}
}

func TestBuildSignalMarkerProposalSkipsRailsAndSharedTrees(t *testing.T) {
	rot := 0.0
	comps := []layoutComp{
		{ID: "gnd", ComponentType: "netflag", Net: "GND", X: 50, Y: 70, AnchorAvailable: true,
			Rotation: &rot, BBox: &layoutBBox{MinX: 50, MinY: 65, MaxX: 60, MaxY: 75}},
		{ID: "sig", ComponentType: "netflag", Net: "SIG", X: 50, Y: 70, AnchorAvailable: true,
			Rotation: &rot, BBox: &layoutBBox{MinX: 50, MinY: 65, MaxX: 60, MaxY: 75}},
		{ID: "u1", ComponentType: schLayoutPartType, Designator: "U1",
			BBox: &layoutBBox{MinX: 10, MinY: 60, MaxX: 30, MaxY: 80},
			Pins: []layoutPin{{Number: "1", X: 40, Y: 70, Rotation: 0, RotationKnown: true}}},
	}
	wires := []schGroupWire{
		{ID: "w1", Net: "SIG", Points: []float64{40, 70, 50, 70}},
		{ID: "w2", Net: "SIG", Points: []float64{50, 70, 80, 70}},
	}
	got := buildSignalMarkerProposals(comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil)
	if len(got) != 1 || got[0].MarkerID != "sig" || got[0].Status != "blocked" ||
		got[0].Reason == "" {
		t.Fatalf("signal marker blockers = %+v", got)
	}
}
