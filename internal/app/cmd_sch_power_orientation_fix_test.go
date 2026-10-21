package app

import "testing"

func powerOrientationFixture(net string, markerRotation float64, pinRotation float64) ([]layoutComp, []schGroupWire) {
	rot := markerRotation
	comp := layoutComp{
		ID: "flag", ComponentType: "netflag", Net: net, X: 50, Y: 70,
		Rotation: &rot, AnchorAvailable: true,
		BBox: &layoutBBox{MinX: 50, MinY: 65, MaxX: 60, MaxY: 75},
	}
	part := layoutComp{
		ID: "part", ComponentType: schLayoutPartType, Designator: "R1", X: 30, Y: 70,
		BBox: &layoutBBox{MinX: 20, MinY: 60, MaxX: 30, MaxY: 80},
		Pins: []layoutPin{{Number: "1", X: 40, Y: 70, Rotation: pinRotation, RotationKnown: true}},
	}
	wires := []schGroupWire{{ID: "stub", Points: []float64{40, 70, 50, 70}}}
	return []layoutComp{comp, part}, wires
}

func TestBuildPowerOrientationPlanUsesOrthogonalElbow(t *testing.T) {
	comps, wires := powerOrientationFixture("+3V3", flagBodyRotation["power"]["right"], 0)
	got := buildPowerOrientationProposals(comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil, nil)
	if len(got) != 1 || got[0].Status != "ready" || got[0].ToDirection != "up" ||
		len(got[0].NewStubPath) != 3 || !firstSegmentMatchesDir(got[0].NewStubPath, "right") {
		t.Fatalf("power orientation proposal = %+v", got)
	}
}

func TestBuildPowerOrientationPlanBlocksReversePin(t *testing.T) {
	comps, wires := powerOrientationFixture("+3V3", flagBodyRotation["power"]["down"], 270)
	got := buildPowerOrientationProposals(comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil, nil)
	if len(got) != 1 || got[0].Status != "blocked" ||
		got[0].Reason != "reverse-pin-requires-orientation-exempt" {
		t.Fatalf("reverse-pin proposal = %+v", got)
	}
}

func TestBuildPowerOrientationPlanTargetsGroundDown(t *testing.T) {
	comps, wires := powerOrientationFixture("GND", flagBodyRotation["ground"]["up"], 0)
	got := buildPowerOrientationProposals(comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil, nil)
	if len(got) != 1 || got[0].Status != "ready" || got[0].ToDirection != "down" ||
		got[0].NewAnchor == nil || got[0].NewAnchor.Y >= 70 {
		t.Fatalf("ground orientation proposal = %+v", got)
	}
}
