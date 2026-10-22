package app

import (
	"strings"
	"testing"
)

func TestBuildLabelFixProposalsVisibilityAndOrientation(t *testing.T) {
	hidden := false
	rot := 90.0
	items := []visibleTextItem{
		{
			ID: "a1", Kind: "attribute", ComponentType: "wire", ParentID: "w1",
			Key: "Name", Value: "SIG", Visible: &hidden, Rotation: &rot,
			BBox: &layoutBBox{MinX: 0, MinY: 0, MaxX: 10, MaxY: 10},
		},
	}
	got := buildLabelFixProposals(items, nil, []schGroupWire{{ID: "w1", Net: "SIG"}})
	if len(got) != 1 || got[0].AttributeID != "a1" || got[0].Status != "ready" {
		t.Fatalf("proposal = %+v", got)
	}
	if got[0].Patch["valueVisible"] != true || got[0].Patch["rotation"] != 0.0 {
		t.Fatalf("patch = %+v", got[0].Patch)
	}
}

func TestBuildLabelFixProposalsFillsEmptyNameFromExplicitNet(t *testing.T) {
	items := []visibleTextItem{{
		ID: "a2", Kind: "attribute", ComponentType: "netflag", ParentID: "f1",
		Key: "Name", Value: "",
		BBox: &layoutBBox{MinX: 0, MinY: 0, MaxX: 10, MaxY: 10},
	}}
	comps := []layoutComp{{ID: "f1", ComponentType: "netflag", Net: "RAIL", AnchorAvailable: true}}
	got := buildLabelFixProposals(items, comps, nil)
	if len(got) != 1 || got[0].Status != "ready" || got[0].Patch["value"] != "RAIL" {
		t.Fatalf("empty-name proposal = %+v", got)
	}
}

func TestBuildLabelFixProposalsBlocksMissingNativeName(t *testing.T) {
	items := []visibleTextItem{{
		ID: "marker1", Kind: "marker", ComponentType: "netflag", MissingName: true, Value: "RAIL",
	}}
	got := buildLabelFixProposals(items, nil, nil)
	if len(got) != 1 || got[0].Status != "blocked" ||
		got[0].Reason != "missing-native-name-attribute" {
		t.Fatalf("missing-name blocker = %+v", got)
	}
}

func TestNewAttributedVisibleTextConflictsOnlyClaimsChangedText(t *testing.T) {
	before := map[string]bool{"visible-text-overlap|old|[]|wire-old|": true}
	after := []checkFinding{
		{Type: "visible-text-overlap", PrimitiveId: "old", WirePrimitiveId: "wire-old"},
		{Type: "visible-text-overlap", PrimitiveId: "label-a", WirePrimitiveId: "wire-a"},
		{Type: "visible-text-overlap", PrimitiveId: "foreign", WirePrimitiveId: "wire-b"},
	}
	conflicts, unattributed := newAttributedVisibleTextConflicts(
		before, after, map[string]bool{"label-a": true})
	if len(conflicts) != 1 || !strings.Contains(conflicts["label-a"], "wire-a") {
		t.Fatalf("conflicts = %+v", conflicts)
	}
	if len(unattributed) != 1 || unattributed[0].PrimitiveId != "foreign" {
		t.Fatalf("unattributed = %+v", unattributed)
	}
}

func TestPartitionLabelFixProposalsSeparatesPostWriteConflicts(t *testing.T) {
	applied := []labelFixProposal{
		{AttributeID: "a1"},
		{AttributeID: "a2"},
	}
	conflicts := map[string]string{"a2": "visible-text-overlap|a2|[]|w2|"}
	rejected, kept := partitionLabelFixProposals(applied, conflicts)
	if len(rejected) != 1 || rejected[0].AttributeID != "a2" || len(kept) != 1 {
		t.Fatalf("partition rejected=%+v kept=%+v", rejected, kept)
	}
}
