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
