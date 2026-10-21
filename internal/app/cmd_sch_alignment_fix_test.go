package app

import "testing"

func TestAlignmentFixPreflightRequiresAllBlockersReady(t *testing.T) {
	item := alignmentPlanItem{
		ID: "alignment-001", Status: "blocked",
		Blockers: []string{"group-move-geometry-collision"},
		IndependentBlockerPlans: []alignmentBlockerPlan{
			{InitialDesignators: []string{"C3"}, Status: "ready", MoveDesignators: []string{"C3"}, DX: 20},
		},
	}
	if err := validateAlignmentBlockerPreflight(item); err != nil {
		t.Fatalf("ready blocker refused: %v", err)
	}
	item.IndependentBlockerPlans[0].Status = "review-required"
	if err := validateAlignmentBlockerPreflight(item); err == nil {
		t.Fatal("review-required blocker must stop preflight")
	}
	item.IndependentBlockerPlans = nil
	if err := validateAlignmentBlockerPreflight(item); err == nil {
		t.Fatal("blocked plan without a blocker plan must stop preflight")
	}
}

func TestSelectAlignmentFixPlanTracksMoveGroupAfterReplan(t *testing.T) {
	identity := alignmentPlanItem{Net: "RAIL", MoveDesignators: []string{"C2", "C1"}}
	report := alignmentPlanReport{Plans: []alignmentPlanItem{
		{ID: "alignment-004", Net: "RAIL", MoveDesignators: []string{"C1", "C2"}, Status: "rewire-required"},
		{ID: "alignment-005", Net: "RAIL", MoveDesignators: []string{"C2", "C1", "R9"}, Status: "blocked"},
	}}
	got, err := selectAlignmentFixPlan(report, "", &identity)
	if err != nil || got.ID != "alignment-004" {
		t.Fatalf("replan selection = %+v, %v", got, err)
	}
}

func TestSameAlignmentMoveGroupIgnoresOrder(t *testing.T) {
	if !sameAlignmentMoveGroup([]string{"C2", "C1"}, []string{"C1", "C2"}) {
		t.Fatal("same designators in a different order must match")
	}
	if sameAlignmentMoveGroup([]string{"C1"}, []string{"C1", "C2"}) {
		t.Fatal("different groups must not match")
	}
}
