package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadIssue273Fixture(t *testing.T, name string) libLayoutSource {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata/schematic-layout/2026-09-29-power-layout", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	input, err := decodeLibLayout(raw)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

// Issue #273 root cause: the attachment route plus provisional nearby-rail
// joins of D3/R3 walled in U1.COMP (BUCK_COMP, direct) before R6 was placed.
// R6 then had no legal XY in the whole 400/200 raw window (6,480 rejected
// candidates at a 100k budget) and chronological rollback of unrelated R8/R7
// burned the frozen 3,000-candidate allowance. Provisional wires are now held
// to the same local direct-frontier ruler as bodies, so every peripheral of the
// frozen buck input reaches a placement checkpoint without a placement failure.
func TestIssue273BuckPlacementKeepsPendingDirectFrontier(t *testing.T) {
	input := loadIssue273Fixture(t, "input")
	start := time.Now()
	_, err := planLibLayout(input)
	if elapsed := time.Since(start); elapsed > 60*time.Second {
		t.Fatalf("bounded search took %s", elapsed)
	}
	var failure *SchematicLayoutSearchFailure
	if err == nil {
		return // A complete layout is the stronger outcome; validators already ran.
	}
	if !errors.As(err, &failure) {
		t.Fatalf("unexpected failure kind: %v", err)
	}
	if failure.Search.PlacementFailures != 0 || strings.Contains(err.Error(), "cmp-R6 (R6) placement") {
		t.Fatalf("R6 placement is still sealed by provisional wires: %v", err)
	}
	for _, module := range input.LayoutModules {
		for _, peripheral := range module.Peripherals {
			if failure.Search.CheckpointAlternatives[peripheral.ComponentID] == 0 {
				t.Fatalf("%s never reached a placement checkpoint: %v", peripheral.ComponentID, err)
			}
		}
	}
	if failure.RemainingCandidates != 0 && !isSchematicSearchResourceStop(err) {
		t.Fatalf("failure must keep an explicit resource or conflict reason: %v", err)
	}
}

// Route alternatives and reroute rounds re-prove the same island on identical
// geometry. A complete verdict is replayed without a second debit; the probe
// input (bodies, pins, wires, markers, island, policy) is the cache key.
func TestIssue273RouteNamingProbeReplaysCompleteVerdict(t *testing.T) {
	input := loadIssue273Fixture(t, "boost-input")
	var core powerLayoutPlacement
	for _, m := range input.Measurements {
		if m.Designator == "U3" {
			core = plTranslate(m, -m.X, -m.Y)
		}
	}
	p := powerLayoutPlan{Placements: []powerLayoutPlacement{core}}
	pin, ok := libPin(core, "4")
	if !ok {
		t.Fatal("missing FB pin")
	}
	island := libIsland{net: pin.Net, pins: []powerLayoutPin{pin}}
	routing, err := newSchematicRoutingContext(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	budget := 100000
	routing.candidateBudget = &budget
	first, complete := libRouteNamingProbe(&p, island, "direct", routing)
	spent := 100000 - budget
	if !complete || first == nil || spent == 0 {
		t.Fatalf("baseline probe complete=%v witness=%v spent=%d", complete, first != nil, spent)
	}
	again, complete := libRouteNamingProbe(&p, island, "direct", routing)
	if !complete || again != first || 100000-budget != spent {
		t.Fatalf("identical probe was re-paid: spent %d then %d", spent, 100000-budget-spent)
	}
	moved := p
	moved.Wires = []powerLayoutWire{{Net: pin.Net, Points: [][2]float64{{pin.X, pin.Y}, {pin.X + 5, pin.Y}}}}
	if _, complete = libRouteNamingProbe(&moved, island, "direct", routing); !complete || 100000-budget == spent {
		t.Fatal("changed geometry must not reuse the cached verdict")
	}
}
