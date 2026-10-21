package app

import "testing"

func wireRerouteFixture() ([]layoutComp, []schGroupWire, []checkFinding) {
	comps := []layoutComp{
		{ID: "m1", ComponentType: "netflag", Net: "SIG", X: 0, Y: 0, AnchorAvailable: true},
		{ID: "m2", ComponentType: "netflag", Net: "SIG", X: 100, Y: 0, AnchorAvailable: true},
		{ID: "u1", Designator: "U1", ComponentType: schLayoutPartType,
			BBox: &layoutBBox{MinX: 40, MinY: -10, MaxX: 60, MaxY: 10}},
	}
	wires := []schGroupWire{{ID: "w1", Net: "SIG", Points: []float64{0, 0, 100, 0}}}
	findings := []checkFinding{{
		Type: "wire-through-body", WirePrimitiveId: "w1", PrimitiveId: "u1",
	}}
	return comps, wires, findings
}

func TestBuildWireRerouteProposalFindsOrthogonalDetour(t *testing.T) {
	comps, wires, findings := wireRerouteFixture()
	got := buildWireRerouteProposals(findings, comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 100}, nil)
	if len(got) != 1 || got[0].Status != "ready" || len(got[0].NewPath) < 3 {
		t.Fatalf("reroute proposal = %+v", got)
	}
}

func TestBuildWireRerouteProposalBlocksMultiSegmentTree(t *testing.T) {
	comps, wires, findings := wireRerouteFixture()
	wires[0].Points = []float64{0, 0, 50, 0, 50, 20, 100, 20}
	got := buildWireRerouteProposals(findings, comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 100}, nil)
	if len(got) != 1 || got[0].Status != "blocked" ||
		got[0].BlockReason != "multi-segment-tree-requires-functional-replan" {
		t.Fatalf("multi-segment blocker = %+v", got)
	}
}

func TestBuildWireRerouteProposalBlocksCrossChipDirect(t *testing.T) {
	comps, wires, _ := wireRerouteFixture()
	got := buildWireRerouteProposals([]checkFinding{{
		Type: "cross-chip-direct-wire", WirePrimitiveId: "w1",
	}}, comps, wires, nil, &layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 100}, nil)
	if len(got) != 1 || got[0].Status != "blocked" ||
		got[0].BlockReason != "cross-chip-requires-net-label-conversion" {
		t.Fatalf("cross-chip blocker = %+v", got)
	}
}
