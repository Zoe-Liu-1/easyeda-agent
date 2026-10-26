package app

import "testing"

func frameRouteTestPart(id, designator string, x, y float64, pins ...layoutPin) layoutComp {
	return layoutComp{
		ID: id, Designator: designator, ComponentType: schLayoutPartType,
		X: x, Y: y, AnchorAvailable: true,
		BBox: &layoutBBox{MinX: x - 5, MinY: y - 5, MaxX: x + 5, MaxY: y + 5},
		Pins: pins,
	}
}

func frameRouteTestWire(id string, points ...float64) schGroupWire {
	w := schGroupWire{ID: id, Points: points}
	for i := 0; i+3 < len(points); i += 2 {
		w.ObservedSegments = append(w.ObservedSegments,
			[4]float64{points[i], points[i+1], points[i+2], points[i+3]})
	}
	if len(w.ObservedSegments) > 0 && w.Net == "" {
		// Keep Net independent; callers attach it explicitly when needed.
	}
	return w
}

func TestFrameRouteFindingsDetectPinAndSameFrameTreeOutside(t *testing.T) {
	frames := []schFrameSpec{{ID: "F", Rect: layoutBBox{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}}}
	comps := []layoutComp{
		frameRouteTestPart("u", "U1", 20, 20,
			layoutPin{Number: "1", X: 30, Y: 20, Net: "FB", Rotation: 0, RotationKnown: true}),
		frameRouteTestPart("r", "R1", 80, 95,
			layoutPin{Number: "1", X: 80, Y: 85, Net: "GND", Rotation: 270, RotationKnown: true},
			layoutPin{Number: "2", X: 80, Y: 105, Net: "FB", Rotation: 90, RotationKnown: true}),
	}
	wire := frameRouteTestWire("w", 30, 20, 40, 20, 40, 105, 80, 105)
	findings := frameRouteFindings(comps, []schGroupWire{wire}, frames, 0.5)
	if !findingsContainType(findings, "pin-outside-frame") {
		t.Fatalf("outside pin not reported: %+v", findings)
	}
	if !findingsContainType(findings, "wire-outside-frame") {
		t.Fatalf("same-frame wire tree outside its frame not reported: %+v", findings)
	}
}

func TestFrameRouteFindingsDoNotFlagTrueCrossFrameTree(t *testing.T) {
	frames := []schFrameSpec{
		{ID: "F1", Rect: layoutBBox{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}},
		{ID: "F2", Rect: layoutBBox{MinX: 110, MinY: 0, MaxX: 210, MaxY: 100}},
	}
	comps := []layoutComp{
		frameRouteTestPart("a", "U1", 20, 20,
			layoutPin{Number: "1", X: 30, Y: 20, Net: "SIG", Rotation: 0, RotationKnown: true}),
		frameRouteTestPart("b", "U2", 180, 20,
			layoutPin{Number: "1", X: 170, Y: 20, Net: "SIG", Rotation: 180, RotationKnown: true}),
	}
	wire := frameRouteTestWire("w", 30, 20, 130, 20, 130, 120, 170, 120, 170, 20)
	findings := frameRouteFindings(comps, []schGroupWire{wire}, frames, 0.5)
	if findingsContainType(findings, "wire-outside-frame") {
		t.Fatalf("a true cross-frame tree must not be treated as a local out-of-frame wire: %+v", findings)
	}
}

func TestFrameRouteFindingsDetectMarkerBodyOutsideFrame(t *testing.T) {
	frames := []schFrameSpec{{ID: "F", Rect: layoutBBox{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}}}
	comps := []layoutComp{
		frameRouteTestPart("u", "U1", 20, 20,
			layoutPin{Number: "1", X: 30, Y: 20, Net: "PWR", Rotation: 0, RotationKnown: true}),
		{ID: "m", ComponentType: "netflag", Net: "PWR", X: 60, Y: 20, AnchorAvailable: true,
			BBox: &layoutBBox{MinX: 58, MinY: 103, MaxX: 62, MaxY: 110}},
	}
	wire := frameRouteTestWire("w", 30, 20, 60, 20)
	wire.Net = "PWR"
	findings := frameRouteFindings(comps, []schGroupWire{wire}, frames, 0.5)
	if !findingsContainType(findings, "wire-outside-frame") {
		t.Fatalf("marker body outside the owned frame was not reported: %+v", findings)
	}
}

func TestFrameRoutePlansMovesOutsidePinAndReroutesInsideFrame(t *testing.T) {
	frame := schFrameSpec{ID: "F", Rect: layoutBBox{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}}
	comps := []layoutComp{
		frameRouteTestPart("u", "U1", 15, 15,
			layoutPin{Number: "1", X: 20, Y: 15, Net: "FB", Rotation: 0, RotationKnown: true}),
		frameRouteTestPart("r", "R1", 70, 85,
			layoutPin{Number: "1", X: 70, Y: 75, Net: "GND", Rotation: 270, RotationKnown: true},
			layoutPin{Number: "2", X: 70, Y: 105, Net: "FB", Rotation: 90, RotationKnown: true}),
	}
	wire := schGroupWire{
		ID: "w", Net: "FB",
		Points:           []float64{20, 15, 30, 15, 30, 105, 70, 105},
		ObservedSegments: [][4]float64{{20, 15, 30, 15}, {30, 15, 30, 105}, {30, 105, 70, 105}},
	}
	plans := frameRoutePlans(comps, []schGroupWire{wire}, nil, []schFrameSpec{frame},
		&frame.Rect, nil)
	if len(plans) != 1 {
		t.Fatalf("want one move plan, got %+v", plans)
	}
	p := plans[0]
	if p.Status != "ready" || p.MoveDesignators[0] != "R1" || p.ShiftY >= 0 || len(p.BoundaryWires) == 0 {
		t.Fatalf("outside pin was not moved into frame with a boundary reroute: %+v", p)
	}
	if p.PinRefs[0] != "R1.2" || p.PinRefs[1] != "U1.1" {
		t.Fatalf("boundary pin refs are incomplete: %v", p.PinRefs)
	}
}

func TestFrameRoutePlansReroutesTwoPinTreeWithoutMovingParts(t *testing.T) {
	frame := schFrameSpec{ID: "F", Rect: layoutBBox{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}}
	comps := []layoutComp{
		frameRouteTestPart("a", "U1", 10, 20,
			layoutPin{Number: "1", X: 15, Y: 20, Net: "SIG", Rotation: 0, RotationKnown: true}),
		frameRouteTestPart("b", "U2", 90, 20,
			layoutPin{Number: "1", X: 85, Y: 20, Net: "SIG", Rotation: 180, RotationKnown: true}),
	}
	wire := schGroupWire{
		ID: "w", Net: "SIG",
		Points: []float64{15, 20, 15, 120, 85, 120, 85, 20},
		ObservedSegments: [][4]float64{
			{15, 20, 15, 120}, {15, 120, 85, 120}, {85, 120, 85, 20},
		},
	}
	plans := frameRoutePlans(comps, []schGroupWire{wire}, nil, []schFrameSpec{frame},
		&frame.Rect, nil)
	if len(plans) != 1 || plans[0].Status != "ready" || len(plans[0].MoveDesignators) != 0 {
		t.Fatalf("want one reroute-only plan, got %+v", plans)
	}
	path := frameRoutePathForNet(plans[0].BoundaryWires, "SIG")
	if len(path) < 4 {
		t.Fatalf("reroute path is missing: %v", path)
	}
	for i := 0; i+3 < len(path); i += 2 {
		if path[i] < 0 || path[i] > 100 || path[i+1] < 0 || path[i+1] > 100 {
			t.Fatalf("reroute path leaves the frame: %v", path)
		}
	}
}

func TestFrameRoutePlansReroutesThreePinStarTree(t *testing.T) {
	frame := schFrameSpec{ID: "F", Rect: layoutBBox{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}}
	comps := []layoutComp{
		frameRouteTestPart("a", "U1", 15, 15,
			layoutPin{Number: "1", X: 20, Y: 15, Net: "PWR", Rotation: 0, RotationKnown: true},
			layoutPin{Number: "2", X: 20, Y: 25, Net: "PWR", Rotation: 0, RotationKnown: true}),
		frameRouteTestPart("b", "C1", 10, 90,
			layoutPin{Number: "1", X: 15, Y: 90, Net: "PWR", Rotation: 0, RotationKnown: true}),
	}
	wire := schGroupWire{
		ID: "w", Net: "PWR",
		Points: []float64{20, 15, 20, 25, 20, 110, 15, 110, 15, 90},
		ObservedSegments: [][4]float64{
			{20, 15, 20, 25}, {20, 25, 20, 110}, {20, 110, 15, 110}, {15, 110, 15, 90},
		},
	}
	plans := frameRoutePlans(comps, []schGroupWire{wire}, nil, []schFrameSpec{frame},
		&frame.Rect, nil)
	if len(plans) != 1 || plans[0].Status != "ready" || len(plans[0].BoundaryPaths) != 2 {
		t.Fatalf("want one ready three-pin star replan, got %+v", plans)
	}
	for _, path := range plans[0].BoundaryPaths {
		for i := 0; i+1 < len(path); i += 2 {
			if path[i] < 0 || path[i] > 100 || path[i+1] < 0 || path[i+1] > 100 {
				t.Fatalf("star branch leaves the frame: %v", path)
			}
		}
	}
}

func TestFrameRoutePinCheckDoesNotInheritOneUnitOverlapEpsilon(t *testing.T) {
	frames := []schFrameSpec{{ID: "F", Rect: layoutBBox{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}}}
	comps := []layoutComp{frameRouteTestPart("r", "R1", 50, 91, layoutPin{
		Number: "2", X: 50, Y: 101, Net: "FB", Rotation: 90, RotationKnown: true,
	})}
	findings := frameRouteFindings(comps, nil, frames, 1.0)
	if !findingsContainType(findings, "pin-outside-frame") {
		t.Fatalf("a pin one unit outside must still be reported: %+v", findings)
	}
}
