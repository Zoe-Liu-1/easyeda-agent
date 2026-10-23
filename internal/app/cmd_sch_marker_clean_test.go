package app

import (
	"reflect"
	"testing"
)

func TestMarkerCleanBlockersRequireExplicitCandidateAndVisibleSurvivor(t *testing.T) {
	candidates := []markerCleanGroup{{
		Type: "redundant-net-marker", Net: "RAIL_A",
		PrimitiveIds: []string{"orig", "ghost"}, VisibleIds: []string{"orig", "ghost"},
	}}
	visible := map[string]bool{"orig": true, "ghost": true}

	got := markerCleanBlockers(candidates, map[string]bool{"outside": true}, visible)
	if !reflect.DeepEqual(got, []string{
		"not-a-marker-clean-candidate:outside",
		"not-visible-name:outside",
	}) {
		t.Fatalf("non-candidate blocker = %v", got)
	}

	got = markerCleanBlockers(candidates, map[string]bool{"orig": true, "ghost": true}, visible)
	if !reflect.DeepEqual(got, []string{"would-remove-last-visible-name:RAIL_A"}) {
		t.Fatalf("last-visible-name blocker = %v", got)
	}

	if got := markerCleanBlockers(candidates, map[string]bool{"ghost": true}, visible); len(got) != 0 {
		t.Fatalf("one explicit duplicate may be removed when another visible name remains: %v", got)
	}
}

func TestMarkerCleanBlockersRejectHiddenSource(t *testing.T) {
	candidates := []markerCleanGroup{{
		Type: "cross-kind-label-economy", Net: "SIG",
		PrimitiveIds: []string{"visible", "hidden"}, VisibleIds: []string{"visible"},
	}}
	got := markerCleanBlockers(candidates, map[string]bool{"hidden": true},
		map[string]bool{"visible": true})
	if !reflect.DeepEqual(got, []string{"not-visible-name:hidden"}) {
		t.Fatalf("hidden delete blockers = %v", got)
	}
}

func TestMarkerCleanBlockersUnionsOverlappingCandidateRowsOnOneIsland(t *testing.T) {
	candidates := []markerCleanGroup{
		{
			Type: "cross-kind-label-economy", Net: "GND",
			PrimitiveIds: []string{"flag", "label-a", "label-b"},
			VisibleIds:   []string{"flag", "label-a", "label-b"},
		},
		{
			Type: "label-economy", Net: "GND",
			PrimitiveIds: []string{"label-a", "label-b"},
			VisibleIds:   []string{"label-a", "label-b"},
		},
	}
	visible := map[string]bool{"flag": true, "label-a": true, "label-b": true}
	got := markerCleanBlockers(candidates,
		map[string]bool{"label-a": true, "label-b": true}, visible)
	if len(got) != 0 {
		t.Fatalf("retained netflag on the same island must satisfy every overlapping label group: %v", got)
	}
}

func TestMarkerCleanWireTreePlanRequiresVisibleMarkerAndPreservesWireGeometry(t *testing.T) {
	comps := []layoutComp{{
		ID: "flag", ComponentType: "netflag", Net: "GND", X: 0, Y: 10, AnchorAvailable: true,
	}}
	wires := []schGroupWire{{
		ID: "wire", Net: "GND", Points: []float64{0, 0, 0, 10},
		ObservedSegments: [][4]float64{{0, 0, 0, 10}},
	}}
	items := []visibleTextItem{
		{ID: "label", Key: "Name", Value: "GND", ParentID: "wire"},
		{ID: "flag-name", Key: "Name", Value: "GND", ParentID: "flag"},
	}
	restore := map[string]markerRestore{
		"label": {label: &items[0]},
	}
	geom := map[string]any{"pagePrimitives": map[string]any{"wires": []any{
		map[string]any{"primitiveId": "wire", "Color": "#00AA00", "LineWidth": 1.0, "LineType": 0.0},
	}}}
	plans, blockers := markerCleanWireTreePlans(
		geom, comps, wires, items, map[string]bool{"label": true}, restore)
	if len(blockers) != 0 || len(plans) != 1 {
		t.Fatalf("plans=%+v blockers=%v", plans, blockers)
	}
	plan := plans[0]
	if plan.RootID != "wire" || plan.Net != "GND" || !reflect.DeepEqual(plan.MarkerIDs, []string{"flag"}) ||
		!reflect.DeepEqual(plan.LabelIDs, []string{"label"}) || len(plan.Replays) != 1 ||
		!reflect.DeepEqual(plan.Replays[0].Points, []float64{0, 0, 0, 10}) ||
		plan.Replays[0].Style["Color"] != "#00AA00" {
		t.Fatalf("wrong rebuild plan: %+v", plan)
	}

	items[1].Visible = boolPtr(false)
	if _, blockers := markerCleanWireTreePlans(
		geom, comps, wires, items, map[string]bool{"label": true}, restore); len(blockers) == 0 {
		t.Fatal("a hidden marker Name must not qualify as the surviving visible name")
	}
}
