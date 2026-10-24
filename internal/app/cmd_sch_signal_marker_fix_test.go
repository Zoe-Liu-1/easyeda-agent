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

func TestBuildSignalMarkerProposalKeepsMultiSegmentWire(t *testing.T) {
	rot := 0.0
	comps := []layoutComp{
		{ID: "sig", ComponentType: "netflag", Net: "SIG", X: 50, Y: 80, AnchorAvailable: true,
			Rotation: &rot, BBox: &layoutBBox{MinX: 50, MinY: 74, MaxX: 55, MaxY: 86}},
	}
	wires := []schGroupWire{{
		ID: "w-multi", Net: "SIG",
		Points: []float64{20, 60, 20, 80, 50, 80},
	}}
	got := buildSignalMarkerProposals(comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil)
	if len(got) != 1 || got[0].Status != "ready" || got[0].AnchorWireID != "w-multi" ||
		got[0].Mode != "existing-wire-label" || got[0].Anchor == nil ||
		got[0].Anchor.X != 50 || got[0].Anchor.Y != 80 {
		t.Fatalf("multi-segment existing-wire label proposal = %+v", got)
	}
}

func TestBuildSignalMarkerProposalSkipsAmbiguousWireAnchor(t *testing.T) {
	rot := 0.0
	comps := []layoutComp{
		{ID: "sig", ComponentType: "netflag", Net: "SIG", X: 50, Y: 70, AnchorAvailable: true,
			Rotation: &rot, BBox: &layoutBBox{MinX: 50, MinY: 64, MaxX: 55, MaxY: 76}},
	}
	wires := []schGroupWire{
		{ID: "w-a", Net: "SIG", Points: []float64{20, 70, 50, 70}},
		{ID: "w-b", Net: "SIG", Points: []float64{50, 70, 80, 70}},
	}
	got := buildSignalMarkerProposals(comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil)
	if len(got) != 1 || got[0].Status != "blocked" ||
		got[0].Reason != "unsafe-marker-anchor:ambiguous-wire-endpoint:w-a,w-b" {
		t.Fatalf("ambiguous wire anchor proposal = %+v", got)
	}
}

func TestBuildSignalMarkerProposalMovesLabelAlongParentWire(t *testing.T) {
	rot := 0.0
	comps := []layoutComp{
		{ID: "sig", ComponentType: "netflag", Net: "SIG", X: 50, Y: 70, AnchorAvailable: true,
			Rotation: &rot, BBox: &layoutBBox{MinX: 50, MinY: 64, MaxX: 55, MaxY: 76}},
	}
	wires := []schGroupWire{{ID: "w", Net: "SIG", Points: []float64{10, 70, 50, 70}}}
	items := []visibleTextItem{{
		ID: "busy", Kind: "attribute", ComponentType: "wire", ParentID: "other-wire",
		Key: "Name", Value: "BUSY",
		BBox: &layoutBBox{MinX: 50, MinY: 69, MaxX: 80, MaxY: 78},
	}}
	got := buildSignalMarkerProposals(comps, wires, items,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil)
	if len(got) != 1 || got[0].Status != "ready" || got[0].Anchor == nil ||
		got[0].AnchorWireID != "w" || got[0].Anchor.X >= 50 {
		t.Fatalf("parent-wire label fallback = %+v", got)
	}
}

func TestBuildSignalMarkerProposalAllowsAdjacentTextGap(t *testing.T) {
	rot := 0.0
	comps := []layoutComp{
		{ID: "sig", ComponentType: "netflag", Net: "SIG", X: 20, Y: 70, AnchorAvailable: true,
			Rotation: &rot, BBox: &layoutBBox{MinX: 20, MinY: 64, MaxX: 25, MaxY: 76}},
	}
	wires := []schGroupWire{
		{ID: "w", Net: "SIG", Points: []float64{0, 70, 20, 70}},
		{ID: "parallel", Net: "OTHER", Points: []float64{0, 80, 100, 80}},
	}
	items := []visibleTextItem{{
		ID: "neighbor", Kind: "attribute", ComponentType: "wire", ParentID: "other-wire",
		Key: "Name", Value: "NEXT",
		BBox: &layoutBBox{MinX: 20, MinY: 82, MaxX: 38, MaxY: 90},
	}}
	got := buildSignalMarkerProposals(comps, wires, items,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil)
	if len(got) != 1 || got[0].Status != "ready" || got[0].Anchor == nil ||
		got[0].Anchor.X != 20 || got[0].Anchor.Y != 70 {
		t.Fatalf("non-overlapping adjacent text should not block = %+v", got)
	}
}

func TestBuildSignalMarkerProposalSkipsAlreadyNamedParentWire(t *testing.T) {
	rot := 0.0
	comps := []layoutComp{
		{ID: "sig", ComponentType: "netflag", Net: "SIG", X: 20, Y: 70, AnchorAvailable: true,
			Rotation: &rot, BBox: &layoutBBox{MinX: 20, MinY: 64, MaxX: 25, MaxY: 76}},
	}
	wires := []schGroupWire{{ID: "w", Net: "SIG", Points: []float64{0, 70, 20, 70}}}
	items := []visibleTextItem{{
		ID: "existing", Kind: "attribute", ComponentType: "wire", ParentID: "w",
		Key: "Name", Value: "SIG",
		BBox: &layoutBBox{MinX: 0, MinY: 70, MaxX: 18, MaxY: 78},
	}}
	got := buildSignalMarkerProposals(comps, wires, items,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}, nil)
	if len(got) != 1 || got[0].Status != "blocked" ||
		got[0].Reason != "existing-net-label-on-parent-wire" {
		t.Fatalf("already-named parent wire proposal = %+v", got)
	}
}
