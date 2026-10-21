package app

import "testing"

func TestBuildVisualTextFixProposalMovesOverlappingAttribute(t *testing.T) {
	target := visibleTextItem{
		ID: "a1", Kind: "attribute", ComponentType: "part", ParentID: "p1",
		Key: "Value", Value: "10k", BBox: &layoutBBox{MinX: 0, MinY: 0, MaxX: 10, MaxY: 6},
	}
	other := visibleTextItem{
		ID: "a2", Kind: "attribute", ComponentType: "part", ParentID: "p2",
		Key: "Value", Value: "22k", BBox: &layoutBBox{MinX: 8, MinY: 0, MaxX: 18, MaxY: 6},
	}
	findings := []checkFinding{{
		Type: "visible-text-overlap", Level: "warn", PrimitiveId: "a1",
	}}
	got := buildVisualTextFixProposals(findings, []visibleTextItem{target, other}, nil, nil,
		map[string]checkPoint{"a1": {X: 0, Y: 0}},
		nil, &layoutBBox{MinX: -50, MinY: -50, MaxX: 100, MaxY: 100}, nil)
	if len(got) != 1 || got[0].Status != "ready" || got[0].ToAnchor == nil ||
		(got[0].DeltaX == 0 && got[0].DeltaY == 0) {
		t.Fatalf("visual text proposal = %+v", got)
	}
}

func TestBuildVisualTextFixProposalMovesFreeText(t *testing.T) {
	target := visibleTextItem{
		ID: "t1", Kind: "text", Content: "note",
		BBox: &layoutBBox{MinX: 0, MinY: 0, MaxX: 20, MaxY: 8},
	}
	other := visibleTextItem{
		ID: "t2", Kind: "text", Content: "other",
		BBox: &layoutBBox{MinX: 15, MinY: 0, MaxX: 35, MaxY: 8},
	}
	got := buildVisualTextFixProposals(
		[]checkFinding{{Type: "visible-text-overlap", PrimitiveId: "t1"}},
		[]visibleTextItem{target, other}, nil, nil, nil,
		map[string]checkPoint{"t1": {X: 0, Y: 0}},
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 100, MaxY: 100}, nil)
	if len(got) != 1 || got[0].Status != "ready" || got[0].Kind != "text" {
		t.Fatalf("free text proposal = %+v", got)
	}
}

func TestBuildVisualTextFixProposalBlocksMissingAnchor(t *testing.T) {
	target := visibleTextItem{
		ID: "t1", Kind: "text", Content: "note",
		BBox: &layoutBBox{MinX: 0, MinY: 0, MaxX: 20, MaxY: 8},
	}
	got := buildVisualTextFixProposals(
		[]checkFinding{{Type: "visible-text-overlap", PrimitiveId: "t1"}},
		[]visibleTextItem{target}, nil, nil, nil, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 100, MaxY: 100}, nil)
	if len(got) != 1 || got[0].Status != "blocked" || got[0].Reason != "free-text-anchor-unavailable" {
		t.Fatalf("missing anchor proposal = %+v", got)
	}
}
