package app

import "testing"

func TestBuildDesignatorFixProposalMovesOffBodyAndWire(t *testing.T) {
	item := visibleTextItem{
		ID: "d1", Kind: "attribute", ParentID: "u1", Key: "Designator", Value: "U1",
		BBox: &layoutBBox{MinX: 92, MinY: 92, MaxX: 100, MaxY: 100},
	}
	comps := []layoutComp{{
		ID: "u1", Designator: "U1", ComponentType: schLayoutPartType,
		BBox: &layoutBBox{MinX: 70, MinY: 70, MaxX: 100, MaxY: 100},
	}}
	wires := []schGroupWire{{ID: "w", Points: []float64{96, 70, 96, 130}}}
	findings := []checkFinding{
		{Type: "designator-overlap", PrimitiveId: "d1"},
		{Type: "designator-wire-overlap", PrimitiveId: "d1"},
	}
	got := buildDesignatorFixProposals(findings, []visibleTextItem{item}, comps, wires,
		map[string]checkPoint{"d1": {X: 96, Y: 96}},
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 300, MaxY: 200}, nil)
	if len(got) != 1 || got[0].Status != "ready" || got[0].ToAnchor == nil {
		t.Fatalf("designator proposal = %+v", got)
	}
	if !designatorSlotClear(visibleTextItem{
		ID: "d1", BBox: got[0].ToBBox, Key: "Designator",
	}, "d1", []visibleTextItem{item}, comps, wires,
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 300, MaxY: 200}, nil) {
		t.Fatal("selected designator slot is not clear")
	}
}

func TestBuildDesignatorFixBlocksNonDesignatorTarget(t *testing.T) {
	item := visibleTextItem{
		ID: "v1", Kind: "attribute", ParentID: "u1", Key: "Value",
		BBox: &layoutBBox{MinX: 20, MinY: 20, MaxX: 30, MaxY: 30},
	}
	got := buildDesignatorFixProposals(
		[]checkFinding{{Type: "designator-overlap", PrimitiveId: "v1"}},
		[]visibleTextItem{item}, nil, nil, map[string]checkPoint{"v1": {X: 25, Y: 25}},
		&layoutBBox{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}, nil)
	if len(got) != 1 || got[0].Status != "blocked" ||
		got[0].Reason != "designator-geometry-unavailable" {
		t.Fatalf("non-designator proposal = %+v", got)
	}
}
