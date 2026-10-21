package app

import (
	"reflect"
	"testing"
)

func TestMoveKernelNewMarkerDuplicatesUsesProvenance(t *testing.T) {
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, 0, 0, 100}},
		{ID: "w2", Points: []float64{300, 0, 300, 100}},
	}
	comps := []layoutComp{
		{ID: "old", ComponentType: "netflag", Net: "RAIL_A", X: 0, Y: 70, AnchorAvailable: true},
		{ID: "new", ComponentType: "netflag", Net: "RAIL_A", X: 0, Y: 90, AnchorAvailable: true},
		{ID: "elsewhere", ComponentType: "netflag", Net: "RAIL_A", X: 300, Y: 70, AnchorAvailable: true},
	}
	got := moveKernelNewMarkerDuplicates(map[string]bool{"old": true, "elsewhere": true}, comps, wires, nil)
	if !reflect.DeepEqual(got, []string{"new"}) {
		t.Fatalf("new duplicate = %v, want [new]", got)
	}

	// Two markers that were both present before the move are existing debt; this
	// postcondition must not attribute them to the current operation.
	if got := moveKernelNewMarkerDuplicates(map[string]bool{"old": true, "new": true}, comps[:2], wires, nil); len(got) != 0 {
		t.Fatalf("baseline debt must not be blamed on the move: %v", got)
	}
}

func TestMoveKernelNewMarkerDuplicatesCatchesNewCrossKindName(t *testing.T) {
	wires := []schGroupWire{{ID: "w1", Points: []float64{0, 0, 0, 100}}}
	comps := []layoutComp{{
		ID: "new-marker", ComponentType: "netflag", Net: "SIG", X: 0, Y: 80, AnchorAvailable: true,
	}}
	labels := []moveWireLabel{{WireID: "w1", X: 0, Y: 30, Net: "SIG"}}
	got := moveKernelNewMarkerDuplicates(nil, comps, wires, labels)
	if !reflect.DeepEqual(got, []string{"new-marker"}) {
		t.Fatalf("new marker duplicating an existing visible label = %v", got)
	}
}
