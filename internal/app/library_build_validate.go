package app

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
)

type libraryBuildEvidence struct {
	Manufacturer   string `json:"manufacturer"`
	MPN            string `json:"mpn"`
	PackageVariant string `json:"packageVariant"`
	Datasheet      struct {
		Title    string `json:"title"`
		Locator  string `json:"locator"`
		Revision string `json:"revision"`
		SHA256   string `json:"sha256"`
	} `json:"datasheet"`
	PinoutPages         []int `json:"pinoutPages"`
	PackageDrawingPages []int `json:"packageDrawingPages"`
	LandPattern         struct {
		Basis string `json:"basis"`
		Pages []int  `json:"pages"`
		Note  string `json:"note"`
	} `json:"landPattern"`
}

type libraryPinMapping struct {
	FootprintOnly []string `json:"footprintOnly"`
	SymbolOnly    []string `json:"symbolOnly"`
}

type libraryBuildValidationSummary struct {
	Name               string   `json:"name"`
	MPN                string   `json:"mpn"`
	PackageVariant     string   `json:"packageVariant"`
	SymbolPins         int      `json:"symbolPins"`
	FootprintPads      int      `json:"footprintPads"`
	FootprintOnly      []string `json:"footprintOnly,omitempty"`
	SymbolOnly         []string `json:"symbolOnly,omitempty"`
	EvidenceComplete   bool     `json:"evidenceComplete"`
	PinMappingVerified bool     `json:"pinMappingVerified"`
}

func readLibraryAssetBuildSpec(path string) (libraryAssetBuildSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return libraryAssetBuildSpec{}, fmt.Errorf("read --spec: %w", err)
	}
	var spec libraryAssetBuildSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return libraryAssetBuildSpec{}, fmt.Errorf("parse --spec: %w", err)
	}
	return spec, nil
}

func validateLibraryAssetBuildSpec(spec *libraryAssetBuildSpec) (libraryBuildValidationSummary, error) {
	if spec.Name == "" || spec.LibraryUUID == "" || spec.Symbol.Name == "" || spec.Footprint.Name == "" || len(spec.Symbol.Geometry) == 0 || len(spec.Footprint.Geometry) == 0 {
		return libraryBuildValidationSummary{}, fmt.Errorf("spec requires name, libraryUUID, symbol{name,geometry}, and footprint{name,geometry}")
	}
	if strings.TrimSpace(spec.Designator) == "" {
		return libraryBuildValidationSummary{}, fmt.Errorf("designator is required")
	}
	if spec.Model3D != nil {
		if strings.TrimSpace(spec.Model3D.Name) == "" || strings.TrimSpace(spec.Model3D.File) == "" {
			return libraryBuildValidationSummary{}, fmt.Errorf("model3D requires name and file")
		}
		info, err := os.Stat(spec.Model3D.File)
		if err != nil {
			return libraryBuildValidationSummary{}, fmt.Errorf("model3D.file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return libraryBuildValidationSummary{}, fmt.Errorf("model3D.file must be a regular file")
		}
	}
	if err := validateLibraryEvidence(spec.Evidence); err != nil {
		return libraryBuildValidationSummary{}, err
	}

	pins, err := numberedGeometry(spec.Symbol.Geometry, "pins")
	if err != nil {
		return libraryBuildValidationSummary{}, fmt.Errorf("symbol.geometry: %w", err)
	}
	if err := validateOutline(spec.Symbol.Geometry["outline"]); err != nil {
		return libraryBuildValidationSummary{}, fmt.Errorf("symbol.geometry.outline: %w", err)
	}
	pads, err := numberedGeometry(spec.Footprint.Geometry, "pads")
	if err != nil {
		return libraryBuildValidationSummary{}, fmt.Errorf("footprint.geometry: %w", err)
	}
	if err := validateFootprintShapes(spec.Footprint.Geometry); err != nil {
		return libraryBuildValidationSummary{}, fmt.Errorf("footprint.geometry: %w", err)
	}

	footprintOnly := setDifference(pads, pins)
	symbolOnly := setDifference(pins, pads)
	if !sameStrings(footprintOnly, normalizedUnique(spec.PinMapping.FootprintOnly)) {
		return libraryBuildValidationSummary{}, fmt.Errorf("pin mapping mismatch: footprint-only pads are %v; declare the exact list in pinMapping.footprintOnly", footprintOnly)
	}
	if !sameStrings(symbolOnly, normalizedUnique(spec.PinMapping.SymbolOnly)) {
		return libraryBuildValidationSummary{}, fmt.Errorf("pin mapping mismatch: symbol-only pins are %v; declare the exact list in pinMapping.symbolOnly", symbolOnly)
	}

	return libraryBuildValidationSummary{
		Name: spec.Name, MPN: spec.Evidence.MPN, PackageVariant: spec.Evidence.PackageVariant,
		SymbolPins: len(pins), FootprintPads: len(pads), FootprintOnly: footprintOnly,
		SymbolOnly: symbolOnly, EvidenceComplete: true, PinMappingVerified: true,
	}, nil
}

func validateLibraryEvidence(e libraryBuildEvidence) error {
	if strings.TrimSpace(e.Manufacturer) == "" || strings.TrimSpace(e.MPN) == "" || strings.TrimSpace(e.PackageVariant) == "" {
		return fmt.Errorf("evidence requires manufacturer, mpn, and packageVariant")
	}
	if strings.TrimSpace(e.Datasheet.Title) == "" || strings.TrimSpace(e.Datasheet.Locator) == "" {
		return fmt.Errorf("evidence.datasheet requires title and locator (URL or local PDF path)")
	}
	if err := positivePages("evidence.pinoutPages", e.PinoutPages); err != nil {
		return err
	}
	if err := positivePages("evidence.packageDrawingPages", e.PackageDrawingPages); err != nil {
		return err
	}
	switch e.LandPattern.Basis {
	case "datasheet":
		if err := positivePages("evidence.landPattern.pages", e.LandPattern.Pages); err != nil {
			return err
		}
	case "calculated", "copied-verified":
		if strings.TrimSpace(e.LandPattern.Note) == "" {
			return fmt.Errorf("evidence.landPattern.note is required when basis is %q", e.LandPattern.Basis)
		}
	default:
		return fmt.Errorf("evidence.landPattern.basis must be datasheet, calculated, or copied-verified")
	}
	return nil
}

func positivePages(label string, pages []int) error {
	if len(pages) == 0 {
		return fmt.Errorf("%s requires at least one page", label)
	}
	for _, page := range pages {
		if page < 1 {
			return fmt.Errorf("%s contains invalid page %d", label, page)
		}
	}
	return nil
}

func numberedGeometry(geometry map[string]any, key string) (map[string]struct{}, error) {
	raw, ok := geometry[key].([]any)
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("%s must be a non-empty array", key)
	}
	result := make(map[string]struct{}, len(raw))
	for i, item := range raw {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be an object", key, i)
		}
		number, ok := obj["number"].(string)
		number = strings.TrimSpace(number)
		if !ok || number == "" {
			return nil, fmt.Errorf("%s[%d].number is required", key, i)
		}
		if _, duplicate := result[number]; duplicate {
			return nil, fmt.Errorf("duplicate %s number %q", strings.TrimSuffix(key, "s"), number)
		}
		for _, field := range []string{"x", "y"} {
			if !finiteNumber(obj[field]) {
				return nil, fmt.Errorf("%s[%d].%s must be finite", key, i, field)
			}
		}
		if rotation, exists := obj["rotation"]; exists && !finiteNumber(rotation) {
			return nil, fmt.Errorf("%s[%d].rotation must be finite", key, i)
		}
		if key == "pins" {
			if _, ok := obj["name"].(string); !ok {
				return nil, fmt.Errorf("pins[%d].name must be a string", i)
			}
			if length, exists := obj["length"]; exists && !finitePositive(length) {
				return nil, fmt.Errorf("pins[%d].length must be positive", i)
			}
		} else if !finiteNumber(obj["layer"]) {
			return nil, fmt.Errorf("pads[%d].layer must be finite", i)
		}
		result[number] = struct{}{}
	}
	return result, nil
}

func validateOutline(raw any) error {
	values, ok := raw.([]any)
	if !ok || len(values) < 8 || len(values)%2 != 0 {
		return fmt.Errorf("must contain at least four x/y points")
	}
	for i, value := range values {
		if !finiteNumber(value) {
			return fmt.Errorf("value %d must be finite", i)
		}
	}
	return nil
}

// officialPadShape mirrors the connector's padShapeTupleError: the host only
// accepts string shape types; a numeric type (old example `[1, w, h]`) fails
// mid-build with "无法创建焊盘图元" (#279).
func officialPadShape(shape []any) bool {
	if len(shape) == 0 {
		return false
	}
	switch shape[0] {
	case "RECT":
		if len(shape) != 4 {
			return false
		}
		r, ok := shape[3].(float64)
		return finitePositive(shape[1]) && finitePositive(shape[2]) && ok && finiteNumber(r) && r >= 0
	case "ELLIPSE", "OVAL", "NGON":
		return len(shape) == 3 && finitePositive(shape[1]) && finitePositive(shape[2])
	case "POLYGON":
		if len(shape) != 2 {
			return false
		}
		poly, ok := shape[1].([]any)
		return ok && len(poly) > 0
	}
	return false
}

func validateFootprintShapes(geometry map[string]any) error {
	pads := geometry["pads"].([]any)
	for i, item := range pads {
		obj := item.(map[string]any)
		shape, ok := obj["shape"].([]any)
		if !ok || !officialPadShape(shape) {
			return fmt.Errorf(`pads[%d].shape must be an official pad-shape tuple: ["RECT", width, height, cornerRadius%%], ["ELLIPSE"|"OVAL"|"NGON", width, height] or ["POLYGON", polygonSource]`, i)
		}
	}
	if lines, exists := geometry["lines"]; exists {
		items, ok := lines.([]any)
		if !ok {
			return fmt.Errorf("lines must be an array")
		}
		for i, item := range items {
			obj, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("lines[%d] must be an object", i)
			}
			for _, field := range []string{"layer", "startX", "startY", "endX", "endY"} {
				if !finiteNumber(obj[field]) {
					return fmt.Errorf("lines[%d].%s must be finite", i, field)
				}
			}
			if !finitePositive(obj["width"]) {
				return fmt.Errorf("lines[%d].width must be positive", i)
			}
		}
	}
	return nil
}

func finiteNumber(value any) bool {
	n, ok := value.(float64)
	return ok && !math.IsNaN(n) && !math.IsInf(n, 0)
}

func finitePositive(value any) bool {
	n, ok := value.(float64)
	return ok && n > 0 && !math.IsNaN(n) && !math.IsInf(n, 0)
}

func normalizedUnique(values []string) []string {
	set := map[string]struct{}{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func setDifference(left, right map[string]struct{}) []string {
	result := []string{}
	for value := range left {
		if _, exists := right[value]; !exists {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
