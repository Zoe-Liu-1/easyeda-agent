package app

import (
	"reflect"
	"testing"
)

func TestSelectDCFixPairRequiresOneActionablePlan(t *testing.T) {
	pairs := []dcPairPlan{
		{Net: "SIG", LabelA: "a", LabelB: "b", Status: "already-clear"},
		{Net: "SIG", LabelA: "c", LabelB: "d", Status: "unresolved"},
		{Net: "OTHER", LabelA: "e", LabelB: "f", Status: "part-move-proposed"},
	}
	got, err := selectDCFixPair(pairs, "a,b")
	if err != nil || got.LabelA != "a" || got.Status != "already-clear" {
		t.Fatalf("exact select = %+v, %v", got, err)
	}
	if _, err := selectDCFixPair(pairs, ""); err == nil {
		t.Fatal("multiple actionable pairs must require --pair")
	}
	if _, err := selectDCFixPair(pairs[1:2], "c,d"); err == nil {
		t.Fatal("unresolved pair must not be executable")
	}
}

func TestResolveDCFixFinalPairUsesEndpointsAfterRigidMove(t *testing.T) {
	intended := dcPairPlan{
		Net: "SIG", LabelA: "old-a", LabelB: "old-b",
		PathPoints: [][2]float64{{10, 20}, {10, 30}, {110, 30}, {110, 20}},
	}
	scene := dcFixScene{pairs: []dcPairPlan{{
		Net: "SIG", LabelA: "new-a", LabelB: "new-b", Status: "already-clear",
		PathPoints: [][2]float64{{110, 20}, {110, 30}, {10, 30}, {10, 20}},
	}}}
	got, err := resolveDCFixFinalPair(scene, intended)
	if err != nil || got.LabelA != "new-a" {
		t.Fatalf("endpoint match failed: %+v %v", got, err)
	}
}

func TestDCAttributeStateRequiresCompleteNativeRecord(t *testing.T) {
	state := map[string]any{}
	for _, key := range schAttributeStateFields {
		state[key] = nil
	}
	state["primitiveId"] = "a1"
	state["ParentPrimitiveId"] = "r1"
	state["Key"] = "Value"
	state["Value"] = "10k"
	state["X"] = 100.0
	state["Y"] = 50.0
	state["KeyVisible"] = false
	state["ValueVisible"] = true
	result := map[string]any{"pagePrimitives": map[string]any{
		"attributes": []any{state},
	}}
	got, err := dcAttributeState(result, "a1")
	if err != nil || got["X"] != 100.0 || got["Value"] != "10k" {
		t.Fatalf("state = %+v, %v", got, err)
	}
	delete(state, "Rotation")
	if _, err := dcAttributeState(result, "a1"); err == nil {
		t.Fatal("incomplete native attribute state must be refused")
	}
}

func TestDCMarkerPathTermPreservesElbow(t *testing.T) {
	dir, off, via, err := dcMarkerPathTerm([][2]float64{{50, 70}, {50, 90}, {70, 90}}, 50, 70, 70, 90)
	if err != nil || dir != "right" || off != 20 || via == nil || *via != [2]float64{50, 90} {
		t.Fatalf("term dir=%s off=%g via=%v err=%v", dir, off, via, err)
	}
	if _, _, _, err := dcMarkerPathTerm([][2]float64{{50, 70}, {70, 90}}, 50, 70, 70, 90); err == nil {
		t.Fatal("diagonal marker stub must be refused")
	}
}

func TestDCNewBridgeSignaturesIsIncremental(t *testing.T) {
	got := dcNewBridgeSignatures([]string{"[A,B]"}, []string{"[A,B]", "[C,D]"})
	if !reflect.DeepEqual(got, []string{"[C,D]"}) {
		t.Fatalf("new bridges = %v", got)
	}
}
