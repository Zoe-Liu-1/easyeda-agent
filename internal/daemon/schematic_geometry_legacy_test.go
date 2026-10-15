package daemon

import (
	"context"
	"testing"
)

func TestLocalWirePreservesLegacyDiagnostics(t *testing.T) {
	for _, scenario := range []string{"zero-length", "old-pin-exit", "new-defect", "worsened-old-defect", "missing-data", "new-invalid-path", "missing-wire"} {
		t.Run(scenario, func(t *testing.T) {
			before, after := geometryFixture(t), geometryFixture(t)
			old := map[string]any{"primitiveId": "old", "x0": 100., "y0": 100., "x1": 100., "y1": 100.}
			if scenario == "old-pin-exit" {
				old = map[string]any{"primitiveId": "old", "x0": 590., "y0": 1020., "x1": 590., "y1": 1050.}
			}
			before["wires"] = []any{old}
			landed := map[string]any{"primitiveId": "added", "x0": 590., "y0": 1020., "x1": 620., "y1": 1020.}
			after["wires"] = []any{old, landed}
			if scenario == "new-defect" {
				after["wires"] = append(after["wires"].([]any), map[string]any{"primitiveId": "new-zero", "x0": 200., "y0": 200., "x1": 200., "y1": 200.})
			}
			if scenario == "worsened-old-defect" {
				after["wires"] = []any{map[string]any{"primitiveId": "old", "x0": 200., "y0": 100., "x1": 200., "y1": 100.}, landed}
			}
			if scenario == "missing-data" {
				delete(before["components"].([]any)[0].(map[string]any), "bbox")
			}
			if scenario == "missing-wire" {
				after["wires"] = []any{old}
			}
			path := `[[590,1020],[620,1020]]`
			if scenario == "new-invalid-path" {
				path = `[[590,1020],[535,1020]]`
			}
			writes := 0
			res, err := New(Options{}).forwardSchematicGeometry(context.Background(), geometryRequest(path), geometryDispatcher(before, after, &writes, false))
			if err != nil {
				t.Fatal(err)
			}
			pass := scenario == "zero-length" || scenario == "old-pin-exit"
			if res.OK != pass {
				t.Fatalf("result=%+v", res)
			}
			if pass {
				guard := res.Result["geometryGuard"].(map[string]any)
				if guard["preexistingFindings"].(int) == 0 || guard["baselineFindings"] == nil || len(res.Warnings) == 0 {
					t.Fatalf("lost historical diagnostics: %+v", res)
				}
			}
			wantWrites := 1
			if scenario == "missing-data" || scenario == "new-invalid-path" {
				wantWrites = 0
			}
			if writes != wantWrites {
				t.Fatalf("writes=%d, want=%d", writes, wantWrites)
			}
			if !pass && writes == 1 && res.Result["partial"] != true {
				t.Fatal("lost partial-write evidence")
			}
		})
	}
}

// Component writes are judged by their own geometry delta, exactly like wire
// writes: untouched legacy findings stay diagnostics, anything new fails.
func TestGuardedComponentWritesPreserveExactLegacyGeometry(t *testing.T) {
	for _, action := range []string{"schematic.component.place", "schematic.component.replace", "schematic.component.modify", "schematic.group.move"} {
		for _, scenario := range []string{"legacy", "new-defect", "changed-defect", "duplicate-defect", "missing-before", "missing-after", "wrong-doc"} {
			t.Run(action+"/"+scenario, func(t *testing.T) {
				before, after := geometryFixture(t), geometryFixture(t)
				old := map[string]any{"primitiveId": "old", "x0": 100., "y0": 100., "x1": 100., "y1": 100.}
				before["wires"] = []any{old}
				after["wires"] = []any{old}
				after["components"] = append(after["components"].([]any), map[string]any{"componentType": "part", "primitiveId": "r1", "designator": "R1", "bbox": map[string]any{"minX": 0., "maxX": 20., "minY": 0., "maxY": 10.}, "pinsAvailable": true, "pins": []any{}})
				switch scenario {
				case "new-defect":
					after["wires"] = append(after["wires"].([]any), map[string]any{"primitiveId": "new-zero", "x0": 200., "y0": 200., "x1": 200., "y1": 200.})
				case "changed-defect":
					after["wires"] = []any{map[string]any{"primitiveId": "old", "x0": 300., "y0": 100., "x1": 300., "y1": 100.}}
				case "duplicate-defect":
					after["wires"] = []any{old, old}
				case "missing-before":
					delete(before["components"].([]any)[0].(map[string]any), "bbox")
				case "missing-after":
					delete(after["components"].([]any)[0].(map[string]any), "bbox")
				}
				req := geometryRequest(`[]`)
				req.Action, req.Payload = action, map[string]any{}
				writes := 0
				res, err := New(Options{}).forwardSchematicGeometry(context.Background(), req, geometryDispatcher(before, after, &writes, scenario == "wrong-doc"))
				if err != nil {
					t.Fatal(err)
				}
				pass := scenario == "legacy"
				if res.OK != pass {
					t.Fatalf("result=%+v", res)
				}
				if pass {
					guard := res.Result["geometryGuard"].(map[string]any)
					if guard["preexistingFindings"].(int) != 1 || guard["baselineFindings"] == nil || len(res.Warnings) == 0 {
						t.Fatalf("lost historical diagnostics: %+v", res)
					}
				}
				wantWrites := 1
				if scenario == "missing-before" {
					wantWrites = 0
				}
				if writes != wantWrites {
					t.Fatalf("writes=%d, want=%d", writes, wantWrites)
				}
				if !pass && writes == 1 && res.Result["partial"] != true {
					t.Fatal("lost partial-write evidence")
				}
			})
		}
	}
}
