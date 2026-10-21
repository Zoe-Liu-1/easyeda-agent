package app

import "testing"

func TestSchWireGeometryAdapterPreservesEvidenceAndBlocks(t *testing.T) {
	result := map[string]any{"components": []any{map[string]any{
		"componentType": "part", "designator": "U1", "primitiveId": "u1",
		"bbox":          map[string]any{"minX": 0.0, "minY": 0.0, "maxX": 20.0, "maxY": 20.0},
		"pinsAvailable": true, "pins": []any{map[string]any{"pinNumber": "1", "x": 30.0, "y": 10.0, "rotation": 0.0}},
	}}, "wires": "original observation must remain intact"}
	fs := schWireGeometryFindings(result, []schGroupWire{{ID: "stable-wire-id", Points: []float64{10, 10, 30, 10}}})
	types := map[string]bool{}
	for _, f := range fs {
		types[f.Type] = true
	}
	for _, want := range []string{"wire-through-body", "wire-over-pin-lead", "pin-exit-direction"} {
		if !types[want] {
			t.Fatalf("missing %s in geometry findings: %+v", want, fs)
		}
	}
	for _, f := range fs {
		if f.WirePrimitiveId != "stable-wire-id" || f.Designator != "U1" || f.PrimitiveId != "u1" || f.Message == "" {
			t.Fatalf("lost evidence: %+v", f)
		}
		if !checkLevelBlocks(f.Level, false) {
			t.Fatalf("hard errors cannot depend on strict: %+v", f)
		}
	}
	if result["wires"] != "original observation must remain intact" {
		t.Fatal("adapter modified original raw snapshot")
	}
}

func TestSchWireGeometryAdapterDoesNotInventMergedTreeSegments(t *testing.T) {
	result := map[string]any{"components": []any{map[string]any{
		"componentType": "part", "designator": "U1", "primitiveId": "u1",
		"bbox":          map[string]any{"minX": 0.0, "minY": 0.0, "maxX": 20.0, "maxY": 20.0},
		"pinsAvailable": true, "pins": []any{},
	}}}
	// Official merged-tree array has two independent horizontal segments,
	// not the fictitious (30,-10)→(-10,30) diagonal through U1.
	wires := []schGroupWire{{ID: "merged", Points: []float64{-10, -10, 30, -10, -10, 30, 30, 30}}}
	if fs := schWireGeometryFindings(result, wires); len(fs) != 0 {
		t.Fatalf("fabricated inter-segment edge: %+v", fs)
	}
}
