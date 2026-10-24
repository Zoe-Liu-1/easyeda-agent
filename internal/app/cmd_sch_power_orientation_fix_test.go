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

func TestPlanAdapterPowerOrientationPreservesParentWire(t *testing.T) {
	rot := flagBodyRotation["power"]["right"]
	flag := layoutComp{
		ID: "flag", ComponentType: "netflag", Net: "+3V3", X: 50, Y: 70,
		Rotation: &rot, AnchorAvailable: true,
		BBox: &layoutBBox{MinX: 50, MinY: 65, MaxX: 60, MaxY: 75},
	}
	wires := []schGroupWire{{
		ID: "shared-tree", Net: "+3V3",
		Points: []float64{20, 60, 20, 70, 50, 70},
	}}
	got, ok, reason := planAdapterPowerOrientation(flag, "power", "up", nil,
		[]layoutComp{flag}, wires,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil)
	if !ok || got.Status != "ready" || got.Mode != "adapter-vertical-flag" ||
		got.Host == nil || got.Host.X != 50 || got.Host.Y != 70 ||
		got.NewAnchor == nil || got.NewAnchor.X != 50 || got.NewAnchor.Y <= 70 ||
		got.StubWireID != "shared-tree" || len(got.NewStubPath) < 2 {
		t.Fatalf("adapter orientation fallback = %+v ok=%v reason=%s", got, ok, reason)
	}
}

func TestPowerOrientationPathRejectsForeignWireAtEndpoint(t *testing.T) {
	stub := &destaggerStub{WireID: "stub", HostX: 40, HostY: 70}
	path := [][2]float64{{40, 70}, {20, 70}, {20, 60}}
	wires := []schGroupWire{
		{ID: "stub", Points: []float64{40, 70, 50, 70}},
		{ID: "foreign", Points: []float64{10, 60, 30, 60}},
	}
	if powerOrientationPathEndpointsClear(stub, path, wires) {
		t.Fatal("path endpoint on a foreign wire was accepted")
	}
	wires[1].Points = []float64{10, 55, 30, 55}
	if !powerOrientationPathEndpointsClear(stub, path, wires) {
		t.Fatal("clear path endpoint was rejected")
	}
}

func TestPowerOrientationAStarFallbackRespectsBodyDirections(t *testing.T) {
	comps, wires := powerOrientationFixture("+3V3", flagBodyRotation["power"]["right"], 0)
	flag := comps[0]
	host := comps[1].Pins[0]
	stub, reason := stubOfMarker(flag, wires)
	if stub == nil || reason != "" {
		t.Fatalf("fixture stub = %+v reason=%s", stub, reason)
	}
	got, ok := choosePowerOrientationPathAStar(
		flag, stub, host, "right", "up", nil, comps, wires,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 300, MaxY: 300}, nil)
	if !ok || got.NewAnchor == nil || len(got.NewStubPath) < 2 {
		t.Fatalf("A* power proposal = %+v ok=%v", got, ok)
	}
	lastA, lastB := got.NewStubPath[len(got.NewStubPath)-2], got.NewStubPath[len(got.NewStubPath)-1]
	if directionBetween(lastA, lastB) != "up" {
		t.Fatalf("power marker must be entered upward: %+v", got.NewStubPath)
	}
}

func TestPowerOrientationSegmentRejectsPinLeadOverlap(t *testing.T) {
	comps := []layoutComp{{
		ID: "c4", Designator: "C4", ComponentType: schLayoutPartType,
		BBox: &layoutBBox{MinX: 1031.5, MinY: 1019.5, MaxX: 1048.5, MaxY: 1040.5},
		Pins: []layoutPin{{
			Number: "1", X: 1040, Y: 1015, Rotation: 90, RotationKnown: true,
		}},
	}}
	stub := &destaggerStub{WireID: "old", HostX: 0, HostY: 0}
	host := layoutPin{X: 0, Y: 0}
	if powerOrientationSegmentClear(
		layoutComp{ID: "moving"},
		stub, host,
		[2]float64{985, 1015}, [2]float64{1045, 1015},
		nil, comps, []schGroupWire{{ID: "old", Points: []float64{0, 0, 10, 0}}},
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 1200, MaxY: 1200},
	) {
		t.Fatal("segment along a foreign pin lead was accepted")
	}
}

func TestPowerOrientationRouteRejectsPinLeadOverlapOutsideBody(t *testing.T) {
	c4 := layoutComp{
		ID: "c4", Designator: "C4", ComponentType: schLayoutPartType,
		BBox: &layoutBBox{MinX: 1031, MinY: 1025, MaxX: 1049, MaxY: 1045},
		Pins: []layoutPin{{
			Number: "1", X: 1040, Y: 1020, Rotation: 270, RotationKnown: true,
		}},
	}
	stub := &destaggerStub{WireID: "old", HostX: 1100, HostY: 1022.5}
	movBox := layoutBBox{MinX: 980, MinY: 1018, MaxX: 990, MaxY: 1027}
	if powerOrientationRouteClear(
		layoutComp{ID: "moving"},
		movBox, stub,
		[][2]float64{{1100, 1022.5}, {985, 1022.5}},
		nil, []layoutComp{c4},
		[]schGroupWire{{ID: "old", Points: []float64{1100, 1022.5, 1110, 1022.5}}},
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 1200, MaxY: 1200}, nil,
	) {
		t.Fatal("route crossing a foreign pin lead outside its body was accepted")
	}
}

func TestPowerOrientationMarkerBoxRejectsPinLeadOverlap(t *testing.T) {
	c4 := layoutComp{
		ID: "c4", Designator: "C4", ComponentType: schLayoutPartType,
		BBox: &layoutBBox{MinX: 1031, MinY: 1025, MaxX: 1049, MaxY: 1045},
		Pins: []layoutPin{{
			Number: "1", X: 1040, Y: 1020, Rotation: 270, RotationKnown: true,
		}},
	}
	newBox := layoutBBox{MinX: 1035, MinY: 1015, MaxX: 1045, MaxY: 1020.5}
	if powerOrientationMarkerBoxClear(
		layoutComp{ID: "moving"}, newBox, nil,
		[]layoutComp{c4}, nil, "old",
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 1200, MaxY: 1200}, nil,
	) {
		t.Fatal("marker box overlapping a foreign pin lead was accepted")
	}
}

func TestVerifyPowerOrientationArtifactDeletion(t *testing.T) {
	want := []string{"marker", "wire-a", "wire-b"}
	success := &actionResult{Result: map[string]any{
		"verified": true,
		"partial":  false,
		"deletedIds": map[string]any{
			"components": []any{"marker"},
			"wires":      []any{"wire-a", "wire-b"},
		},
	}}
	if err := verifyPowerOrientationArtifactDeletion(success, want); err != nil {
		t.Fatalf("verified explicit deletion was rejected: %v", err)
	}

	partial := &actionResult{Result: map[string]any{
		"verified": true,
		"partial":  true,
		"deletedIds": map[string]any{
			"components": []any{"marker"},
			"wires":      []any{"wire-a"},
		},
	}}
	if err := verifyPowerOrientationArtifactDeletion(partial, want); err == nil {
		t.Fatal("partial explicit deletion was accepted")
	}

	missing := &actionResult{Result: map[string]any{
		"verified": true,
		"partial":  false,
		"deletedIds": map[string]any{
			"components": []any{"marker"},
			"wires":      []any{"wire-a"},
		},
	}}
	if err := verifyPowerOrientationArtifactDeletion(missing, want); err == nil {
		t.Fatal("explicit deletion without proof for every artifact was accepted")
	}
}
