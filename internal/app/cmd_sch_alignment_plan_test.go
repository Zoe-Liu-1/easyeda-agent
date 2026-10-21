package app

import (
	"math"
	"strings"
	"testing"
)

func TestBuildAlignmentPlanReport(t *testing.T) {
	comps := []layoutComp{
		{ID: "c1", Designator: "C1", ComponentType: schLayoutPartType},
		{ID: "r1", Designator: "R1", ComponentType: schLayoutPartType},
	}
	candidates := []alignmentCandidate{{
		Axis: "y", Net: "MAIN", Shift: -40,
		MoveDesignators:     []string{"C1", "R1"},
		MoveWireIDs:         []string{"w-owned"},
		MoveMarkerIDs:       []string{"m-owned"},
		MoveLabelIDs:        []string{"l-owned"},
		SharedWireIDs:       []string{"w-shared"},
		Blockers:            []string{"B1"},
		IndependentBlockers: []string{"B1"},
	}}
	report := buildAlignmentPlanReport("doc", "", candidates, comps, nil, nil, nil, nil)
	if len(report.Plans) != 1 || report.Blocked != 1 {
		t.Fatalf("wrong plan report: %+v", report)
	}
	p := report.Plans[0]
	if p.Status != "blocked" || p.ShiftY != -40 || p.MoveComponentIds[0] != "c1" ||
		p.MoveComponentIds[1] != "r1" || p.MoveLabelIds[0] != "l-owned" ||
		p.SharedWireIds[0] != "w-shared" || p.IndependentBlockers[0] != "B1" {
		t.Fatalf("wrong plan identity/resources: %+v", p)
	}
	if len(p.Steps) < 5 || p.Steps[0].Kind != "move-independent-blockers" {
		t.Fatalf("missing prerequisite/verification steps: %+v", p.Steps)
	}
	foundOwnedLabel := false
	for _, id := range p.Steps[1].PrimitiveIds {
		if id == "l-owned" {
			foundOwnedLabel = true
		}
	}
	if !foundOwnedLabel {
		t.Fatalf("owned label must be part of the movable resource list: %+v", p.Steps[1])
	}
	for _, step := range p.Steps {
		if step.Kind == "move-owned-group" && step.ExecutableNow {
			t.Fatalf("blocked plan must not be executable: %+v", step)
		}
	}
}

func TestAlignmentPlanIDsStayStableUnderNetFilter(t *testing.T) {
	sheet := &layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 200}
	comps := []layoutComp{
		{ID: "c1", Designator: "C1", ComponentType: schLayoutPartType,
			BBox: &layoutBBox{MinX: 50, MinY: 30, MaxX: 70, MaxY: 40}},
		{ID: "c2", Designator: "C2", ComponentType: schLayoutPartType,
			BBox: &layoutBBox{MinX: 100, MinY: 30, MaxX: 120, MaxY: 40}},
	}
	candidates := []alignmentCandidate{
		{Axis: "x", Net: "FIRST", Shift: 25, MoveDesignators: []string{"C1"},
			MoveWireIDs: []string{"w1"}, At: checkPoint{X: 60, Y: 35}, MovedBBox: layoutBBox{MinX: 75, MinY: 30, MaxX: 95, MaxY: 40}},
		{Axis: "x", Net: "SECOND", Shift: 25, MoveDesignators: []string{"C2"},
			MoveWireIDs: []string{"w2"}, At: checkPoint{X: 110, Y: 35}, MovedBBox: layoutBBox{MinX: 125, MinY: 30, MaxX: 145, MaxY: 40}},
	}
	all := buildAlignmentPlanReport("doc", "", candidates, comps, nil, nil, sheet, nil)
	filtered := buildAlignmentPlanReport("doc", "SECOND", candidates, comps, nil, nil, sheet, nil)
	if len(all.Plans) != 2 || len(filtered.Plans) != 1 ||
		all.Plans[1].ID != filtered.Plans[0].ID || filtered.Plans[0].ID != "alignment-002" {
		t.Fatalf("filter changed plan identity: all=%+v filtered=%+v", all.Plans, filtered.Plans)
	}
}

func TestBuildAlignmentPlanReadyWithoutSharedBoundary(t *testing.T) {
	sheet := &layoutBBox{MinX: 0, MinY: 0, MaxX: 200, MaxY: 100}
	comps := []layoutComp{
		{ID: "c1", Designator: "C1", ComponentType: schLayoutPartType,
			BBox: &layoutBBox{MinX: 50, MinY: 30, MaxX: 70, MaxY: 40}},
	}
	report := buildAlignmentPlanReport("doc", "", []alignmentCandidate{{
		Axis: "x", Net: "SIG", Shift: 25,
		MoveDesignators: []string{"C1"}, MoveWireIDs: []string{"w1"},
	}}, comps, nil, nil, sheet, nil)
	if report.Ready != 1 || report.Plans[0].Status != "ready" ||
		report.Plans[0].ShiftX != 25 || !report.Plans[0].Steps[0].ExecutableNow {
		t.Fatalf("ready plan classification wrong: %+v", report)
	}
}

func TestBuildAlignmentPlanAddsBoundaryRewireDraft(t *testing.T) {
	comps := []layoutComp{
		{
			ID: "u1", Designator: "U1", ComponentType: schLayoutPartType,
			BBox: &layoutBBox{MinX: -10, MinY: -5, MaxX: 0, MaxY: 5},
			Pins: []layoutPin{
				{Number: "1", Net: "MAIN", X: 0, Y: 0, Rotation: 0, RotationKnown: true},
				{Number: "2", Net: "MAIN", X: 0, Y: 3, Rotation: 0, RotationKnown: true},
				{Number: "3", Net: "NC1", X: -5, Y: 0, Rotation: 180, RotationKnown: true},
				{Number: "4", Net: "NC2", X: -5, Y: 5, Rotation: 180, RotationKnown: true},
			},
		},
		{
			ID: "c1", Designator: "C1", ComponentType: schLayoutPartType,
			BBox: &layoutBBox{MinX: 55, MinY: 40, MaxX: 75, MaxY: 55},
			Pins: []layoutPin{{Number: "1", Net: "MAIN", X: 60, Y: 40, Rotation: 270, RotationKnown: true}},
		},
		{
			ID: "r1", Designator: "R1", ComponentType: schLayoutPartType,
			BBox: &layoutBBox{MinX: 85, MinY: 40, MaxX: 105, MaxY: 55},
			Pins: []layoutPin{{Number: "1", Net: "MAIN", X: 90, Y: 40, Rotation: 270, RotationKnown: true}},
		},
	}
	wires := []schGroupWire{{
		ID: "shared", Points: []float64{0, 0, 30, 0, 30, 0, 30, 40, 30, 40, 120, 40},
		ObservedSegments: [][4]float64{{0, 0, 30, 0}, {30, 0, 30, 40}, {30, 40, 120, 40}},
	}}
	candidates := alignmentOpportunityCandidates(nil, comps, wires, nil, nil)
	if len(candidates) != 1 {
		t.Fatalf("fixture must produce one candidate: %+v", candidates)
	}
	sheet := &layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 100}
	report := buildAlignmentPlanReport("doc", "", candidates, comps, nil, wires, sheet, nil)
	p := report.Plans[0]
	if p.Status != "rewire-required" || len(p.BoundaryPlans) != 1 {
		t.Fatalf("missing boundary rewire draft: %+v", p)
	}
	b := p.BoundaryPlans[0]
	if b.Status != "proposed" || b.Net != "MAIN" || len(b.Wires) == 0 ||
		len(b.FixedPins) == 0 || len(b.MovedPins) == 0 {
		t.Fatalf("wrong boundary draft: %+v", b)
	}
	for _, step := range p.Steps {
		if step.Kind == "create-boundary-wire" && (step.Action != "schematic.wire.create" || len(step.Points) != 4) {
			t.Fatalf("wrong boundary wire step: %+v", step)
		}
		if step.ExecutableNow {
			t.Fatalf("boundary-replan steps must remain non-executable before apply support: %+v", step)
		}
	}
}

func TestBuildAlignmentPlanBlocksUnsafePrimaryGroupMove(t *testing.T) {
	sheet := &layoutBBox{MinX: 0, MinY: 0, MaxX: 200, MaxY: 100}
	comps := []layoutComp{
		{ID: "c1", Designator: "C1", ComponentType: schLayoutPartType,
			BBox: &layoutBBox{MinX: 0, MinY: 0, MaxX: 20, MaxY: 10}},
		{ID: "u9", Designator: "U9", ComponentType: schLayoutPartType,
			BBox: &layoutBBox{MinX: 45, MinY: 0, MaxX: 65, MaxY: 10}},
	}
	report := buildAlignmentPlanReport("doc", "", []alignmentCandidate{{
		Axis: "x", Net: "SIG", Shift: 30,
		MoveDesignators: []string{"C1"}, MoveWireIDs: []string{"w1"},
	}}, comps, nil, nil, sheet, nil)
	p := report.Plans[0]
	if p.Status != "blocked" || p.Steps[1].ExecutableNow {
		t.Fatalf("unsafe primary move must remain blocked: %+v", p)
	}
	found := false
	for _, blocker := range p.Blockers {
		if blocker == "group-move-geometry-collision" {
			found = true
		}
	}
	if !found {
		t.Fatalf("primary collision must be explicit: %+v", p.Blockers)
	}
}

func TestBuildAlignmentPlanMovesIndependentBlockerFirst(t *testing.T) {
	sheet := &layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 100}
	comps := []layoutComp{
		{ID: "c1", Designator: "C1", ComponentType: schLayoutPartType, BBox: &layoutBBox{MinX: 50, MinY: 0, MaxX: 70, MaxY: 10}},
		{ID: "r1", Designator: "R1", ComponentType: schLayoutPartType, BBox: &layoutBBox{MinX: 80, MinY: 0, MaxX: 100, MaxY: 10}},
		{ID: "b1", Designator: "B1", ComponentType: schLayoutPartType, BBox: &layoutBBox{MinX: 0, MinY: 0, MaxX: 10, MaxY: 10}},
	}
	candidate := alignmentCandidate{
		Axis: "y", Net: "MAIN", Shift: -40,
		MoveDesignators: []string{"C1", "R1"},
		MovedBBox:       layoutBBox{MinX: 50, MinY: 0, MaxX: 100, MaxY: 10},
		At:              checkPoint{X: 40, Y: 20},
		Blockers:        []string{"B1"}, IndependentBlockers: []string{"B1"},
	}
	report := buildAlignmentPlanReport("doc", "", []alignmentCandidate{candidate}, comps, nil, nil, sheet, nil)
	if len(report.Plans) != 1 || report.Blocked != 1 {
		t.Fatalf("main plan should wait for its blocker: %+v", report)
	}
	p := report.Plans[0]
	if len(p.IndependentBlockerPlans) != 1 || p.IndependentBlockerPlans[0].Status != "ready" {
		t.Fatalf("missing ready blocker plan: %+v", p.IndependentBlockerPlans)
	}
	b := p.IndependentBlockerPlans[0]
	if b.MoveDesignators[0] != "B1" || (b.DX == 0 && b.DY == 0) ||
		len(p.Steps) == 0 || p.Steps[0].Kind != "move-independent-blockers" || !p.Steps[0].ExecutableNow {
		t.Fatalf("blocker must be an executable prerequisite: plan=%+v step=%+v", b, p.Steps[0])
	}
}

func TestAlignmentBlockerMoveRequiresReviewOutsideLocalBudget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		dx, dy float64
		want   bool
	}{
		{name: "local cardinal", dx: 45, dy: 0, want: true},
		{name: "local diagonal", dx: 15, dy: 35, want: true},
		{name: "axis too large", dx: 55, dy: 0, want: false},
		{name: "l1 too large", dx: 35, dy: 35, want: false},
		{name: "remote", dx: 165, dy: 65, want: false},
	} {
		if got := alignmentBlockerMoveIsLocal(tc.dx, tc.dy); got != tc.want {
			t.Fatalf("%s: got local=%v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAlignmentPlanReviewRequiredBlockerStaysNonExecutable(t *testing.T) {
	item := alignmentPlanItem{
		Status: "blocked",
		IndependentBlockerPlans: []alignmentBlockerPlan{{
			InitialDesignators: []string{"C9"},
			Status:             "review-required",
			MoveDesignators:    []string{"C9"},
			MoveComponentIds:   []string{"c9"},
			DX:                 100,
			DY:                 50,
		}},
	}
	steps := alignmentPlanSteps(item, alignmentCandidate{Axis: "y", Shift: -30})
	if len(steps) == 0 || steps[0].Kind != "move-independent-blockers" || steps[0].ExecutableNow {
		t.Fatalf("review-required blocker must stay non-executable: %+v", steps)
	}
}

func TestAlignmentApplyEnvelopeRequiresProposedBoundary(t *testing.T) {
	comps := []layoutComp{{
		ID: "c10", Designator: "C10", ComponentType: schLayoutPartType,
		X: 230, Y: 695,
		BBox: &layoutBBox{MinX: 220, MinY: 685, MaxX: 240, MaxY: 705},
	}}
	valid := alignmentPlanItem{
		ID: "alignment-001", Net: "DC55", Status: "rewire-required", ShiftY: -30,
		MoveDesignators: []string{"C10"}, SharedWireIds: []string{"shared"},
		BoundaryPlans: []alignmentBoundaryPlan{{
			Net: "DC55", Status: "proposed",
			FixedPins: []string{"U5.1", "U5.2"}, MovedPins: []string{"C10.1"},
			Wires: []alignmentPlannedWire{{Net: "DC55", Points: []float64{230, 665, 230, 680}}},
		}},
	}
	report, err := alignmentApplyEnvelope(valid, comps, false, false)
	if err != nil {
		t.Fatalf("valid apply envelope rejected: %v", err)
	}
	if len(report.Move) != 1 || report.Move[0].ToY != 665 ||
		strings.Join(report.Boundary.PinRefs, ",") != "C10:1,U5:1,U5:2" ||
		len(report.Boundary.Wires) != 1 {
		t.Fatalf("wrong apply envelope: %+v", report)
	}

	blocked := valid
	blocked.BoundaryPlans = append([]alignmentBoundaryPlan(nil), valid.BoundaryPlans...)
	blocked.BoundaryPlans[0].Status = "blocked"
	if _, err := alignmentApplyEnvelope(blocked, comps, false, false); err == nil {
		t.Fatal("blocked boundary must be refused")
	}
}

func TestAlignmentBoundaryRoutesRespectPinOutwardDirections(t *testing.T) {
	anchors := []alignmentBoundaryPin{
		{Key: "U5.1", X: 370, Y: 680, Rotation: 180, RotationKnown: true},
		{Key: "U5.2", X: 370, Y: 670, Rotation: 180, RotationKnown: true},
		{Key: "C10.1", X: 230, Y: 665, Rotation: 90, RotationKnown: true},
	}
	segs := alignmentBusWires(anchors, true, 680)
	if len(segs) == 0 {
		t.Fatal("no boundary bus generated")
	}
	for _, pin := range anchors {
		wantX, wantY, ok := alignmentOutwardVector(pin)
		if !ok {
			t.Fatalf("bad fixture rotation for %s", pin.Key)
		}
		found := false
		for _, seg := range segs {
			var ox, oy float64
			switch {
			case math.Abs(seg[0]-pin.X) <= alignmentTolerance && math.Abs(seg[1]-pin.Y) <= alignmentTolerance:
				ox, oy = seg[2], seg[3]
			case math.Abs(seg[2]-pin.X) <= alignmentTolerance && math.Abs(seg[3]-pin.Y) <= alignmentTolerance:
				ox, oy = seg[0], seg[1]
			default:
				continue
			}
			dx, dy := ox-pin.X, oy-pin.Y
			if math.Hypot(dx, dy) > alignmentTolerance && dx*wantX+dy*wantY > 0 {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s has no outward first segment: %v", pin.Key, segs)
		}
	}
}

func TestBuildAlignmentPlanBlocksBoundaryWireCrossing(t *testing.T) {
	sheet := &layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 100}
	comps := []layoutComp{
		alignmentCorePart("u1", "U1", 0, 0, "MAIN"),
		{
			ID: "c1", Designator: "C1", ComponentType: schLayoutPartType,
			BBox: &layoutBBox{MinX: 50, MinY: 40, MaxX: 70, MaxY: 55},
			Pins: []layoutPin{{Number: "1", Net: "MAIN", X: 60, Y: 40, Rotation: 90, RotationKnown: true}},
		},
		{
			ID: "r1", Designator: "R1", ComponentType: schLayoutPartType,
			BBox: &layoutBBox{MinX: 80, MinY: 40, MaxX: 100, MaxY: 55},
			Pins: []layoutPin{{Number: "1", Net: "MAIN", X: 90, Y: 40, Rotation: 90, RotationKnown: true}},
		},
		{
			ID: "c3", Designator: "C3", ComponentType: schLayoutPartType,
			BBox: &layoutBBox{MinX: 38, MinY: 0, MaxX: 42, MaxY: 5},
			Pins: []layoutPin{{Number: "1", Net: "OTHER", X: 40, Y: 0, Rotation: 90, RotationKnown: true}},
		},
	}
	wires := []schGroupWire{
		{
			ID: "shared", Points: []float64{0, 0, 30, 0, 30, 0, 30, 40, 30, 40, 120, 40},
			ObservedSegments: [][4]float64{{0, 0, 30, 0}, {30, 0, 30, 40}, {30, 40, 120, 40}},
		},
		{ID: "foreign", Net: "OTHER", ObservedSegments: [][4]float64{{40, -10, 40, 10}}},
	}
	candidates := alignmentOpportunityCandidates(nil, comps, wires, nil, nil)
	report := buildAlignmentPlanReport("doc", "", candidates, comps, nil, wires, sheet, nil)
	if len(report.Plans) != 1 || report.Plans[0].Status != "blocked" ||
		len(report.Plans[0].BoundaryPlans) != 1 || report.Plans[0].BoundaryPlans[0].Status != "blocked" {
		t.Fatalf("foreign boundary wire crossing must block the plan: %+v", report)
	}
	foundForeign := false
	for _, blocker := range report.Plans[0].Blockers {
		if stringsContains(blocker, "foreign") {
			foundForeign = true
		}
	}
	if !foundForeign {
		t.Fatalf("foreign-wire blocker must be explicit: %+v", report.Plans[0].Blockers)
	}
	if len(report.Plans[0].IndependentBlockerPlans) != 1 ||
		report.Plans[0].IndependentBlockerPlans[0].InitialDesignators[0] != "C3" ||
		report.Plans[0].IndependentBlockerPlans[0].Status != "ready" {
		t.Fatalf("foreign wire owner should become an independent blocker plan: %+v", report.Plans[0])
	}
}

func TestBuildAlignmentPlanBlocksIndependentBlockerWithSharedWire(t *testing.T) {
	sheet := &layoutBBox{MinX: -50, MinY: -50, MaxX: 300, MaxY: 150}
	comps := []layoutComp{
		{ID: "c1", Designator: "C1", ComponentType: schLayoutPartType, BBox: &layoutBBox{MinX: 50, MinY: 0, MaxX: 70, MaxY: 10}},
		{ID: "r1", Designator: "R1", ComponentType: schLayoutPartType, BBox: &layoutBBox{MinX: 80, MinY: 0, MaxX: 100, MaxY: 10}},
		{ID: "b1", Designator: "B1", ComponentType: schLayoutPartType, BBox: &layoutBBox{MinX: 0, MinY: 30, MaxX: 10, MaxY: 40},
			Pins: []layoutPin{{Number: "1", Net: "LOCAL", X: 5, Y: 30}}},
		{ID: "u2", Designator: "U2", ComponentType: schLayoutPartType, BBox: &layoutBBox{MinX: 200, MinY: 20, MaxX: 220, MaxY: 40},
			Pins: []layoutPin{
				{Number: "1", Net: "LOCAL", X: 200, Y: 30},
				{Number: "2", Net: "NC2", X: 200, Y: 25},
				{Number: "3", Net: "NC3", X: 200, Y: 35},
				{Number: "4", Net: "NC4", X: 220, Y: 30},
			}},
	}
	wires := []schGroupWire{{ID: "shared-local", Net: "LOCAL", ObservedSegments: [][4]float64{{5, 30, 200, 30}}}}
	candidate := alignmentCandidate{
		Axis: "y", Net: "MAIN", Shift: -40,
		MoveDesignators: []string{"C1", "R1"},
		MovedBBox:       layoutBBox{MinX: 50, MinY: 0, MaxX: 100, MaxY: 10},
		At:              checkPoint{X: 40, Y: 20},
		Blockers:        []string{"B1"}, IndependentBlockers: []string{"B1"},
	}
	report := buildAlignmentPlanReport("doc", "", []alignmentCandidate{candidate}, comps, nil, wires, sheet, nil)
	p := report.Plans[0]
	if len(p.IndependentBlockerPlans) != 1 ||
		p.IndependentBlockerPlans[0].Status != "blocked" ||
		len(p.IndependentBlockerPlans[0].SharedWireIds) != 1 ||
		p.Steps[0].ExecutableNow {
		t.Fatalf("shared-wire blocker must remain non-executable: plan=%+v step=%+v", p.IndependentBlockerPlans, p.Steps[0])
	}
}

func TestBuildAlignmentPlanBoundaryDraftAvoidsVisibleText(t *testing.T) {
	sheet := &layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 100}
	comps := []layoutComp{
		alignmentCorePart("u1", "U1", 0, 0, "MAIN"),
		{
			ID: "c1", Designator: "C1", ComponentType: schLayoutPartType,
			BBox: &layoutBBox{MinX: 50, MinY: 40, MaxX: 70, MaxY: 55},
			Pins: []layoutPin{{Number: "1", Net: "MAIN", X: 60, Y: 40, Rotation: 90, RotationKnown: true}},
		},
		{
			ID: "r1", Designator: "R1", ComponentType: schLayoutPartType,
			BBox: &layoutBBox{MinX: 80, MinY: 40, MaxX: 100, MaxY: 55},
			Pins: []layoutPin{{Number: "1", Net: "MAIN", X: 90, Y: 40, Rotation: 90, RotationKnown: true}},
		},
	}
	wires := []schGroupWire{{
		ID: "shared", Points: []float64{0, 0, 30, 0, 30, 0, 30, 40, 30, 40, 120, 40},
		ObservedSegments: [][4]float64{{0, 0, 30, 0}, {30, 0, 30, 40}, {30, 40, 120, 40}},
	}}
	text := visibleTextItem{ID: "note", Value: "BLOCK", BBox: &layoutBBox{MinX: 45, MinY: -2, MaxX: 55, MaxY: 2}}
	candidate := alignmentCandidate{
		Axis: "y", Net: "MAIN", Shift: -40, TargetCoord: 0,
		MoveDesignators: []string{"C1", "R1"}, SharedWireIDs: []string{"shared"},
		MoveWireIDs: []string{}, OriginalBBox: layoutBBox{MinX: 50, MinY: 40, MaxX: 100, MaxY: 55},
		MovedBBox: layoutBBox{MinX: 50, MinY: 0, MaxX: 100, MaxY: 15},
	}
	plans, _ := alignmentBoundaryPlans(candidate, comps, []visibleTextItem{text}, wires, sheet, nil)
	if len(plans) != 1 || plans[0].Status != "blocked" {
		t.Fatalf("text crossing must block boundary draft: %+v", plans)
	}
	found := false
	for _, blocker := range plans[0].Blockers {
		if blocker == "text:BLOCK" {
			found = true
		}
	}
	if !found {
		t.Fatalf("visible-text blocker must be explicit: %+v", plans[0])
	}
}
