package app

import "testing"

func alignmentTestPart(id, designator string, x, y float64, net string) layoutComp {
	return layoutComp{
		ID: id, Designator: designator, ComponentType: schLayoutPartType,
		BBox: &layoutBBox{MinX: x - 10, MinY: y - 5, MaxX: x + 10, MaxY: y + 5},
		Pins: []layoutPin{{Number: "1", Net: net, X: x, Y: y, Rotation: 90, RotationKnown: true}},
	}
}

func alignmentCorePart(id, designator string, x, y float64, net string) layoutComp {
	c := alignmentTestPart(id, designator, x, y, net)
	c.Pins = []layoutPin{
		{Number: "1", Net: net, X: x, Y: y, Rotation: 90, RotationKnown: true},
		{Number: "2", Net: net, X: x + 5, Y: y, Rotation: 90, RotationKnown: true},
		{Number: "3", Net: net, X: x, Y: y + 5, Rotation: 90, RotationKnown: true},
		{Number: "4", Net: net, X: x + 5, Y: y + 5, Rotation: 90, RotationKnown: true},
	}
	return c
}

func TestAlignmentOpportunityFindsHVH(t *testing.T) {
	comps := []layoutComp{
		alignmentCorePart("u1", "U1", 0, 0, "MAIN"),
		alignmentTestPart("c1", "C1", 60, 40, "MAIN"),
		alignmentTestPart("r1", "R1", 90, 40, "MAIN"),
	}
	wires := []schGroupWire{{
		ID: "w1", Points: []float64{0, 0, 30, 0, 30, 0, 30, 40, 30, 40, 120, 40},
		ObservedSegments: [][4]float64{{0, 0, 30, 0}, {30, 0, 30, 40}, {30, 40, 120, 40}},
	}}
	got := alignmentOpportunityFindings(nil, comps, wires, nil, nil)
	if len(got) != 1 {
		t.Fatalf("want one alignment opportunity, got %+v", got)
	}
	f := got[0]
	if f.Type != "alignment-opportunity" || f.Level != "info" || f.MarkerNet != "MAIN" ||
		f.AlignmentAxis != "y" || f.ShiftY != -40 || f.BendReduction != 2 ||
		len(f.MoveDesignators) != 2 || f.MoveDesignators[0] != "C1" || f.MoveDesignators[1] != "R1" ||
		f.At == nil || f.At.X != 30 || f.At.Y != 20 {
		t.Fatalf("wrong H-V-H alignment finding: %+v", f)
	}
}

func TestAlignmentOpportunityAcrossWirePrimitives(t *testing.T) {
	comps := []layoutComp{
		alignmentCorePart("u1", "U1", 0, 0, "MAIN"),
		alignmentTestPart("c1", "C1", 60, 40, "MAIN"),
		alignmentTestPart("r1", "R1", 90, 40, "MAIN"),
	}
	wires := []schGroupWire{
		{ID: "h1", ObservedSegments: [][4]float64{{0, 0, 30, 0}}},
		{ID: "v1", ObservedSegments: [][4]float64{{30, 0, 30, 40}}},
		{ID: "h2", ObservedSegments: [][4]float64{{30, 40, 120, 40}}},
	}
	got := alignmentOpportunityFindings(nil, comps, wires, nil, nil)
	if len(got) != 1 || got[0].MarkerNet != "MAIN" || got[0].ShiftY != -40 {
		t.Fatalf("cross-primitive H-V-H must be reconstructed as one tree: %+v", got)
	}
}

func TestAlignmentOpportunityRejectsNecessaryOrAmbiguousJogs(t *testing.T) {
	baseComps := []layoutComp{
		alignmentCorePart("u1", "U1", 0, 0, "MAIN"),
		alignmentTestPart("c1", "C1", 60, 40, "MAIN"),
		alignmentTestPart("r1", "R1", 90, 40, "MAIN"),
	}
	wire := []schGroupWire{{
		ID: "w1", Points: []float64{0, 0, 30, 0, 30, 0, 30, 40, 30, 40, 120, 40},
		ObservedSegments: [][4]float64{{0, 0, 30, 0}, {30, 0, 30, 40}, {30, 40, 120, 40}},
	}}

	// The jog itself lands on a pin: the offset is semantically necessary.
	pinAtBend := append([]layoutComp(nil), baseComps...)
	pinAtBend = append(pinAtBend, alignmentTestPart("u2", "U2", 30, 0, "MAIN"))
	if got := alignmentOpportunityFindings(nil, pinAtBend, wire, nil, nil); len(got) != 0 {
		t.Fatalf("pin at bend must suppress alignment suggestion: %+v", got)
	}

	// Two core-bearing sides are ambiguous: do not guess which functional block moves.
	equal := []layoutComp{
		alignmentCorePart("u1", "U1", 0, 0, "MAIN"),
		alignmentCorePart("u2", "U2", 60, 40, "MAIN"),
		alignmentTestPart("c1", "C1", 60, 40, "MAIN"),
		alignmentTestPart("r1", "R1", 90, 40, "MAIN"),
	}
	equalWire := []schGroupWire{{
		ID: "w1", Points: []float64{0, 0, 30, 0, 30, 0, 30, 40, 30, 40, 120, 40},
		ObservedSegments: [][4]float64{{0, 0, 60, 0}, {30, 0, 30, 40}, {30, 40, 120, 40}},
	}}
	if got := alignmentOpportunityFindings(nil, equal, equalWire, nil, nil); len(got) != 0 {
		t.Fatalf("equal group sizes must stay silent: %+v", got)
	}

	// Mixed pin nets are not one straightenable physical bus.
	mixed := []layoutComp{
		alignmentCorePart("u1", "U1", 0, 0, "MAIN"),
		alignmentTestPart("c1", "C1", 60, 40, "MAIN"),
		alignmentTestPart("r1", "R1", 90, 40, "GND"),
	}
	if got := alignmentOpportunityFindings(nil, mixed, wire, nil, nil); len(got) != 0 {
		t.Fatalf("mixed-net arms must stay silent: %+v", got)
	}

	// Sub-grid/near-alignment noise is not worth moving a functional block.
	smallShift := []layoutComp{
		alignmentCorePart("u1", "U1", 0, 0, "MAIN"),
		alignmentTestPart("c1", "C1", 60, 5, "MAIN"),
		alignmentTestPart("r1", "R1", 90, 5, "MAIN"),
	}
	smallWire := []schGroupWire{{
		ID: "w1", Points: []float64{0, 0, 30, 0, 30, 0, 30, 5, 30, 5, 120, 5},
		ObservedSegments: [][4]float64{{0, 0, 30, 0}, {30, 0, 30, 5}, {30, 5, 120, 5}},
	}}
	if got := alignmentOpportunityFindings(nil, smallShift, smallWire, nil, nil); len(got) != 0 {
		t.Fatalf("sub-10-unit offset must stay silent: %+v", got)
	}
}

func TestAlignmentOpportunityVerticalAndBlocker(t *testing.T) {
	comps := []layoutComp{
		alignmentCorePart("u1", "U1", 0, 0, "SIG"),
		alignmentTestPart("c1", "C1", 40, 60, "SIG"),
		alignmentTestPart("r1", "R1", 40, 90, "SIG"),
		alignmentTestPart("b1", "B1", 0, 70, "OTHER"),
	}
	wires := []schGroupWire{{
		ID: "w1", Points: []float64{0, 0, 0, 30, 0, 30, 40, 30, 40, 30, 40, 120},
		ObservedSegments: [][4]float64{{0, 0, 0, 30}, {0, 30, 40, 30}, {40, 30, 40, 120}},
	}}
	got := alignmentOpportunityFindings(nil, comps, wires, nil, nil)
	if len(got) != 1 {
		t.Fatalf("want one vertical alignment opportunity, got %+v", got)
	}
	f := got[0]
	if f.AlignmentAxis != "x" || f.ShiftX != -40 || len(f.Blockers) == 0 || f.Blockers[0] != "B1" {
		t.Fatalf("wrong vertical/blocker result: %+v", f)
	}
	if len(f.IndependentBlockers) != 1 || f.IndependentBlockers[0] != "B1" {
		t.Fatalf("foreign blocker should be independently movable: %+v", f)
	}
}

func TestAlignmentTargetBlockersIgnoreFormerSweptCorridor(t *testing.T) {
	comps := []layoutComp{{
		ID: "b1", Designator: "B1", ComponentType: schLayoutPartType,
		BBox: &layoutBBox{MinX: 45, MinY: 10, MaxX: 55, MaxY: 20},
	}}
	target := layoutBBox{MinX: 20, MinY: 10, MaxX: 40, MaxY: 20}
	blockers, independent := alignmentTargetBlockers(comps, []string{"C1"}, nil, target)
	if len(blockers) != 0 || len(independent) != 0 {
		t.Fatalf("a part outside the final group envelope must not block an atomic move: %v/%v", blockers, independent)
	}
}

func TestAlignmentOpportunitySinglePartPowerCluster(t *testing.T) {
	wires := []schGroupWire{{
		ID: "w1", Points: []float64{0, 0, 30, 0, 30, 0, 30, 40, 30, 40, 70, 40},
		ObservedSegments: [][4]float64{{0, 0, 30, 0}, {30, 0, 30, 40}, {30, 40, 70, 40}},
	}}
	comps := []layoutComp{
		alignmentCorePart("u1", "U1", 0, 0, "+3V3"),
		alignmentTestPart("c1", "C1", 60, 40, "+3V3"),
		{ID: "f1", ComponentType: "netflag", Net: "+3V3", X: 60, Y: 40, AnchorAvailable: true},
	}
	got := alignmentOpportunityFindings(nil, comps, wires, nil, nil)
	if len(got) != 1 || len(got[0].MoveDesignators) != 1 || got[0].MoveDesignators[0] != "C1" ||
		len(got[0].Blockers) != 0 {
		t.Fatalf("single-part non-ground power cluster should be a candidate, got %+v", got)
	}

	comps[0].Pins = withPinNets(comps[0].Pins, "GND")
	comps[1].Pins = withPinNets(comps[1].Pins, "GND")
	comps[2].Net = "GND"
	if got := alignmentOpportunityFindings(nil, comps, wires, nil, nil); len(got) != 0 {
		t.Fatalf("single-part GND cluster must stay conservative: %+v", got)
	}
}

func TestAlignmentOpportunityExpandsFunctionalGroupResources(t *testing.T) {
	comps := []layoutComp{
		alignmentCorePart("u1", "U1", 0, 0, "MAIN"),
		{
			ID: "c1", Designator: "C1", ComponentType: schLayoutPartType,
			BBox: &layoutBBox{MinX: 50, MinY: 35, MaxX: 70, MaxY: 55},
			Pins: []layoutPin{{Number: "1", Net: "MAIN", X: 60, Y: 40}, {Number: "2", Net: "LOCAL", X: 60, Y: 50}},
		},
		{
			ID: "r1", Designator: "R1", ComponentType: schLayoutPartType,
			BBox: &layoutBBox{MinX: 80, MinY: 35, MaxX: 100, MaxY: 55},
			Pins: []layoutPin{{Number: "1", Net: "MAIN", X: 90, Y: 40}, {Number: "2", Net: "LOCAL", X: 90, Y: 50}},
		},
		{
			ID: "j1", Designator: "J1", ComponentType: schLayoutPartType,
			BBox: &layoutBBox{MinX: 110, MinY: 45, MaxX: 130, MaxY: 60},
			Pins: []layoutPin{
				{Number: "1", Net: "LOCAL", X: 120, Y: 50},
				{Number: "2", Net: "LOCAL", X: 122, Y: 50},
				{Number: "3", Net: "LOCAL", X: 124, Y: 50},
				{Number: "4", Net: "LOCAL", X: 126, Y: 50},
				{Number: "5", Net: "LOCAL", X: 128, Y: 50},
				{Number: "6", Net: "LOCAL", X: 130, Y: 50},
			},
		},
		alignmentCorePart("u2", "U2", 110, 10, "LOCAL"),
		{ID: "m1", ComponentType: "netflag", Net: "LOCAL", X: 80, Y: 50, AnchorAvailable: true},
	}
	wires := []schGroupWire{
		{
			ID: "bus", Points: []float64{0, 0, 30, 0, 30, 0, 30, 40, 30, 40, 120, 40},
			ObservedSegments: [][4]float64{{0, 0, 30, 0}, {30, 0, 30, 40}, {30, 40, 120, 40}},
		},
		{ID: "local-wire", Points: []float64{60, 50, 120, 50}, ObservedSegments: [][4]float64{{60, 50, 120, 50}}, Net: "LOCAL"},
	}
	items := []visibleTextItem{{ID: "label1", Kind: "attribute", ComponentType: "wire", ParentID: "local-wire", Key: "Name", Value: "LOCAL"}}
	got := alignmentOpportunityFindings(items, comps, wires, nil, nil)
	if len(got) != 1 {
		t.Fatalf("want one expanded alignment candidate, got %+v", got)
	}
	f := got[0]
	if got := stringsJoin(f.MoveDesignators); got != "C1,J1,R1" {
		t.Fatalf("functional group expansion wrong: %v", f.MoveDesignators)
	}
	if got := stringsJoin(f.MoveWireIds); got != "local-wire" {
		t.Fatalf("owned wire wrong: %v", f.MoveWireIds)
	}
	if got := stringsJoin(f.SharedWireIds); got != "bus" {
		t.Fatalf("shared boundary wire wrong: %v", f.SharedWireIds)
	}
	if got := stringsJoin(f.MoveMarkerIds); got != "m1" {
		t.Fatalf("marker ownership wrong: %v", f.MoveMarkerIds)
	}
	if got := stringsJoin(f.MoveLabelIds); got != "label1" {
		t.Fatalf("label ownership wrong: %v", f.MoveLabelIds)
	}
}

func withPinNets(pins []layoutPin, net string) []layoutPin {
	out := append([]layoutPin(nil), pins...)
	for i := range out {
		out[i].Net = net
	}
	return out
}

func stringsJoin(values []string) string {
	out := ""
	for i, value := range values {
		if i > 0 {
			out += ","
		}
		out += value
	}
	return out
}
