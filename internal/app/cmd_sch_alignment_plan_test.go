package app

import "testing"

func TestBuildAlignmentPlanReport(t *testing.T) {
	comps := []layoutComp{
		{ID: "c1", Designator: "C1", ComponentType: schLayoutPartType},
		{ID: "r1", Designator: "R1", ComponentType: schLayoutPartType},
	}
	candidates := []alignmentCandidate{{
		Axis: "y", Net: "MAIN", Shift: -40,
		MoveDesignators:     []string{"C1", "R1"},
		MoveWireIDs:         []string{"w-owned"},
		MoveMarkerIDs:       []string{"m-owned"},
		MoveLabelIDs:        []string{"l-owned"},
		SharedWireIDs:       []string{"w-shared"},
		Blockers:            []string{"B1"},
		IndependentBlockers: []string{"B1"},
	}}
	report := buildAlignmentPlanReport("doc", "", candidates, comps)
	if len(report.Plans) != 1 || report.Blocked != 1 {
		t.Fatalf("wrong plan report: %+v", report)
	}
	p := report.Plans[0]
	if p.Status != "blocked" || p.ShiftY != -40 || p.MoveComponentIds[0] != "c1" ||
		p.MoveComponentIds[1] != "r1" || p.MoveLabelIds[0] != "l-owned" ||
		p.SharedWireIds[0] != "w-shared" || p.IndependentBlockers[0] != "B1" {
		t.Fatalf("wrong plan identity/resources: %+v", p)
	}
	if len(p.Steps) < 5 || p.Steps[0].Kind != "move-independent-blockers" {
		t.Fatalf("missing prerequisite/verification steps: %+v", p.Steps)
	}
	for _, step := range p.Steps {
		if step.Kind == "move-owned-group" && step.ExecutableNow {
			t.Fatalf("blocked plan must not be executable: %+v", step)
		}
	}
}

func TestBuildAlignmentPlanReadyWithoutSharedBoundary(t *testing.T) {
	comps := []layoutComp{
		{ID: "c1", Designator: "C1", ComponentType: schLayoutPartType},
	}
	report := buildAlignmentPlanReport("doc", "", []alignmentCandidate{{
		Axis: "x", Net: "SIG", Shift: 25,
		MoveDesignators: []string{"C1"}, MoveWireIDs: []string{"w1"},
	}}, comps)
	if report.Ready != 1 || report.Plans[0].Status != "ready" ||
		report.Plans[0].ShiftX != 25 || !report.Plans[0].Steps[0].ExecutableNow {
		t.Fatalf("ready plan classification wrong: %+v", report)
	}
}
