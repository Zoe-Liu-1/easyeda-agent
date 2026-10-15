package app

import (
	"strings"
	"testing"
)

func validLibraryBuildSpec(t *testing.T) libraryAssetBuildSpec {
	t.Helper()
	var spec libraryAssetBuildSpec
	spec.Name, spec.LibraryUUID, spec.Designator = "ABC123", "personal", "U"
	spec.Symbol.Name = "ABC123"
	spec.Footprint.Name = "QFN-3"
	spec.Symbol.Geometry = map[string]any{
		"outline": []any{float64(-10), float64(-10), float64(10), float64(-10), float64(10), float64(10), float64(-10), float64(10)},
		"pins": []any{
			map[string]any{"number": "1", "name": "IN", "x": float64(-20), "y": float64(0)},
			map[string]any{"number": "2", "name": "GND", "x": float64(0), "y": float64(-20)},
		},
	}
	spec.Footprint.Geometry = map[string]any{
		"pads": []any{
			map[string]any{"number": "1", "layer": float64(1), "x": float64(-10), "y": float64(0), "shape": []any{"RECT", float64(12), float64(8), float64(0)}},
			map[string]any{"number": "2", "layer": float64(1), "x": float64(10), "y": float64(0), "shape": []any{"RECT", float64(12), float64(8), float64(0)}},
		},
	}
	spec.Evidence.Manufacturer = "Acme"
	spec.Evidence.MPN = "ABC123"
	spec.Evidence.PackageVariant = "QFN-3"
	spec.Evidence.Datasheet.Title = "ABC123 datasheet"
	spec.Evidence.Datasheet.Locator = "https://example.test/abc123.pdf"
	spec.Evidence.PinoutPages = []int{3}
	spec.Evidence.PackageDrawingPages = []int{12}
	spec.Evidence.LandPattern.Basis = "datasheet"
	spec.Evidence.LandPattern.Pages = []int{13}
	return spec
}

func TestValidateLibraryAssetBuildSpec(t *testing.T) {
	spec := validLibraryBuildSpec(t)
	summary, err := validateLibraryAssetBuildSpec(&spec)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.EvidenceComplete || !summary.PinMappingVerified || summary.SymbolPins != 2 || summary.FootprintPads != 2 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestValidateLibraryAssetBuildSpecRequiresExactPinExceptions(t *testing.T) {
	spec := validLibraryBuildSpec(t)
	pads := spec.Footprint.Geometry["pads"].([]any)
	spec.Footprint.Geometry["pads"] = append(pads, map[string]any{"number": "EP", "layer": float64(1), "x": float64(0), "y": float64(0), "shape": []any{"RECT", float64(20), float64(20), float64(0)}})
	_, err := validateLibraryAssetBuildSpec(&spec)
	if err == nil || !strings.Contains(err.Error(), "footprint-only pads are [EP]") {
		t.Fatalf("expected mapping failure, got %v", err)
	}
	spec.PinMapping.FootprintOnly = []string{"EP"}
	if _, err := validateLibraryAssetBuildSpec(&spec); err != nil {
		t.Fatalf("declared mapping exception rejected: %v", err)
	}
}

func TestValidateLibraryAssetBuildSpecRejectsMissingEvidence(t *testing.T) {
	spec := validLibraryBuildSpec(t)
	spec.Evidence.PackageDrawingPages = nil
	_, err := validateLibraryAssetBuildSpec(&spec)
	if err == nil || !strings.Contains(err.Error(), "packageDrawingPages") {
		t.Fatalf("expected evidence failure, got %v", err)
	}
}

// #279: the host only accepts string shape types; the old example's numeric
// `[1, w, h]` failed mid-build, so offline validation refuses it up front.
func TestValidateFootprintShapesRequiresOfficialTuple(t *testing.T) {
	for _, bad := range [][]any{
		{float64(1), float64(23.6), float64(11.8)},
		{"RECT", float64(12), float64(8)},
		{"OVAL", float64(12), float64(-8)},
		{"POLYGON", []any{}},
		{"SQUARE", float64(1), float64(1)},
	} {
		if validateFootprintShapes(map[string]any{"pads": []any{map[string]any{"shape": bad}}}) == nil {
			t.Fatalf("shape %v must be refused", bad)
		}
	}
	for _, good := range [][]any{
		{"RECT", float64(12), float64(8), float64(0)},
		{"OVAL", float64(12), float64(8)},
		{"POLYGON", []any{float64(0), float64(0), "L", float64(1), float64(0)}},
	} {
		if err := validateFootprintShapes(map[string]any{"pads": []any{map[string]any{"shape": good}}}); err != nil {
			t.Fatalf("shape %v refused: %v", good, err)
		}
	}
}
