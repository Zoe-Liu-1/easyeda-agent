package app

import (
	"math"
	"testing"
)

func relocatePlanFixture() ([]layoutComp, []schGroupWire, []*schGroup) {
	comps := []layoutComp{
		{
			ID: "u1", Designator: "U1", ComponentType: "part",
			X: 300, Y: 100, BBox: &layoutBBox{MinX: 280, MinY: 80, MaxX: 320, MaxY: 120},
			Pins: []layoutPin{{Number: "1", X: 270, Y: 100, Net: "+3V3", Rotation: 180, RotationKnown: true}},
		},
		{
			ID: "c1", Designator: "C1", ComponentType: "part",
			X: 50, Y: 100, BBox: &layoutBBox{MinX: 40, MinY: 90, MaxX: 60, MaxY: 110},
			Pins: []layoutPin{
				{Number: "1", X: 50, Y: 120, Net: "+3V3", Rotation: 90, RotationKnown: true},
				{Number: "2", X: 50, Y: 80, Net: "GND", Rotation: 270, RotationKnown: true},
			},
		},
		{ID: "fu", ComponentType: "netflag", Net: "+3V3", X: 240, Y: 100, AnchorAvailable: true},
		{ID: "fc", ComponentType: "netflag", Net: "+3V3", X: 50, Y: 140, AnchorAvailable: true},
		{ID: "fg", ComponentType: "netflag", Net: "GND", X: 50, Y: 60, AnchorAvailable: true},
	}
	wires := []schGroupWire{
		{ID: "wu", Points: []float64{240, 100, 270, 100}},
		{ID: "wc", Points: []float64{50, 120, 50, 140}},
		{ID: "wg", Points: []float64{50, 60, 50, 80}},
	}
	groups := []*schGroup{{
		ID: "g1", Members: []string{"U1", "C1"},
	}}
	return comps, wires, groups
}

func TestPlanPowerClusterRelocationMovesThenConnects(t *testing.T) {
	comps, wires, groups := relocatePlanFixture()
	sheet := &layoutBBox{MinX: 0, MinY: 0, MaxX: 500, MaxY: 300}
	frames := []schFrameSpec{{ID: "f1", Rect: *sheet}}
	plans := planPowerClusterRelocations(comps, wires, nil, groups, nil, nil, false, false,
		500, 100, 40, sheet, nil, frames)
	if len(plans) != 1 {
		t.Fatalf("want one relocation plan, got %+v", plans)
	}
	p := plans[0]
	if p.Passive != "C1" || p.Consumer != "U1" || p.Status != "ready" ||
		p.Move == nil || p.Connection == nil || !p.RequiresPostMoveReplan {
		t.Fatalf("wrong relocation plan: %+v", p)
	}
	if math.Abs(p.Move.DeltaX) < 0.5 && math.Abs(p.Move.DeltaY) < 0.5 {
		t.Fatalf("far passive must actually move: %+v", p.Move)
	}
	if len(p.Move.WireIDs) != 2 || len(p.Move.FlagIDs) != 2 {
		t.Fatalf("both labelled pin stubs must move rigidly: %+v", p.Move)
	}
	if len(p.Connection.Path) < 2 {
		t.Fatalf("missing post-move direct path: %+v", p.Connection)
	}
}

func TestPlanPowerClusterRelocationExplicitTargetBreaksTie(t *testing.T) {
	comps, wires, groups := relocatePlanFixture()
	comps = append(comps, layoutComp{
		ID: "u2", Designator: "U2", ComponentType: "part",
		X: 300, Y: 140, BBox: &layoutBBox{MinX: 280, MinY: 120, MaxX: 320, MaxY: 160},
		Pins: []layoutPin{{Number: "1", X: 270, Y: 140, Net: "+3V3", Rotation: 180, RotationKnown: true}},
	})
	comps = append(comps, layoutComp{ID: "fu2", ComponentType: "netflag", Net: "+3V3", X: 240, Y: 140, AnchorAvailable: true})
	wires = append(wires, schGroupWire{ID: "wu2", Points: []float64{240, 140, 270, 140}})
	groups[0].Members = append(groups[0].Members, "U2")
	sheet := &layoutBBox{MinX: 0, MinY: -200, MaxX: 500, MaxY: 300}
	frames := []schFrameSpec{{ID: "f1", Rect: *sheet}}

	auto := planPowerClusterRelocations(comps, wires, nil, groups, nil, nil, false, false,
		500, 100, 40, sheet, nil, frames)
	if len(auto) != 1 || auto[0].Status != "ambiguous" {
		t.Fatalf("equal-distance targets must require an explicit pair, got %+v", auto)
	}
	explicit := map[string]powerRelocatePairRequest{
		"C1": {Passive: "C1", Consumer: "U2", Pin: "1"},
	}
	got := planPowerClusterRelocations(comps, wires, nil, groups, explicit, nil, false, false,
		500, 100, 40, sheet, nil, frames)
	if len(got) != 1 || got[0].Consumer != "U2" || got[0].Status != "ready" {
		t.Fatalf("explicit target must break the tie, got %+v", got)
	}
}

func TestParsePowerRelocatePairs(t *testing.T) {
	got, err := parsePowerRelocatePairs([]string{"C1:U1.1,C8:J1"})
	if err != nil {
		t.Fatal(err)
	}
	if got["C1"].Consumer != "U1" || got["C1"].Pin != "1" ||
		got["C8"].Consumer != "J1" || got["C8"].Pin != "" {
		t.Fatalf("wrong parsed requests: %+v", got)
	}
	if _, err := parsePowerRelocatePairs([]string{"broken"}); err == nil {
		t.Fatal("malformed pair must fail")
	}
}

func TestRelocateMoveInsideFrameRejectsMovedTextOutside(t *testing.T) {
	comps := []layoutComp{{
		ID: "c1", Designator: "C1", ComponentType: "part",
		BBox: &layoutBBox{MinX: 10, MinY: 10, MaxX: 20, MaxY: 20},
		Pins: []layoutPin{
			{Number: "1", X: 15, Y: 25, Net: "+3V3", Rotation: 90, RotationKnown: true},
			{Number: "2", X: 15, Y: 5, Net: "GND", Rotation: 270, RotationKnown: true},
		},
	}}
	items := []visibleTextItem{{
		ID: "name", Kind: "attribute", ParentID: "flag", Key: "Name", Value: "GND",
		BBox: &layoutBBox{MinX: 10, MinY: 0, MaxX: 30, MaxY: 8},
	}}
	wires := []schGroupWire{{ID: "w1", Points: []float64{15, 15, 15, 20}}}
	frame := layoutBBox{MinX: 0, MinY: 10, MaxX: 100, MaxY: 100}
	movedComp := map[string]bool{"c1": true, "flag": true}
	if relocateMoveInsideFrame([2]float64{0, 0}, movedComp, map[string]bool{"w1": true},
		[]string{"name"}, items, comps, wires, &frame) {
		t.Fatal("moved text below the functional frame must be rejected")
	}
	items[0].BBox = &layoutBBox{MinX: 10, MinY: 20, MaxX: 30, MaxY: 28}
	wires[0].Points = []float64{15, 25, 15, 35}
	if !relocateMoveInsideFrame([2]float64{0, 0}, movedComp, map[string]bool{"w1": true},
		[]string{"name"}, items, comps, wires, &frame) {
		t.Fatal("clear in-frame move should be accepted")
	}
}

func TestPlanPowerClusterRelocationCanReturnMoveBeforeConnection(t *testing.T) {
	comps, wires, groups := relocatePlanFixture()
	comps = append(comps, layoutComp{
		ID: "wall", Designator: "WALL", ComponentType: "part",
		BBox: &layoutBBox{MinX: 180, MinY: 70, MaxX: 185, MaxY: 130},
	})
	sheet := &layoutBBox{MinX: 0, MinY: 0, MaxX: 500, MaxY: 300}
	frames := []schFrameSpec{{ID: "f1", Rect: *sheet}}
	plans := planPowerClusterRelocations(comps, wires, nil, groups, nil, nil, false, false,
		500, 100, 80, sheet, nil, frames)
	if len(plans) != 1 || plans[0].Status != "ready" || plans[0].Move == nil {
		t.Fatalf("a safe in-frame move must be returned even when the current lead is blocked, got %+v", plans)
	}
	if plans[0].Connection == nil || plans[0].Connection.Status == "" {
		t.Fatalf("connection status must remain explicit, got %+v", plans[0])
	}
}

func TestPlanPowerFrameRelocateMoveIgnoresConnectionAvailability(t *testing.T) {
	comps, wires, groups := relocatePlanFixture()
	frame := layoutBBox{MinX: 0, MinY: 80, MaxX: 500, MaxY: 300}
	plans := planPowerClusterRelocations(comps, wires, nil, groups, nil, nil, false, true,
		500, 100, 80, &frame, nil, []schFrameSpec{{ID: "f1", Rect: frame}})
	if len(plans) != 1 || plans[0].Status != "ready" || plans[0].Move == nil {
		t.Fatalf("want a frame-only move plan, got %+v", plans)
	}
}
