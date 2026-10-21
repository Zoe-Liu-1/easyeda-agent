package app

import "testing"

func schAttributeModifyTestState() map[string]any {
	return map[string]any{
		"primitiveId": "a1", "X": 10.0, "Y": 20.0, "Rotation": 90.0,
		"Color": nil, "FontName": nil, "FontSize": nil, "Bold": nil,
		"Italic": nil, "UnderLine": nil, "AlignMode": nil, "FillColor": nil,
		"Key": "Name", "Value": "NET", "KeyVisible": false, "ValueVisible": false,
		"ParentPrimitiveId": "wire1",
	}
}

func TestValidateSchAttributeModifyPayload(t *testing.T) {
	expected := schAttributeModifyTestState()
	payload := map[string]any{
		"projectUuid": "p", "documentUuid": "d", "primitiveId": "a1",
		"expected": expected,
		"patch":    map[string]any{"rotation": 0.0, "valueVisible": true},
	}
	if err := validateSchAttributeModifyPayload(payload); err != nil {
		t.Fatalf("valid payload refused: %v", err)
	}
	payload["patch"] = map[string]any{"fontSize": 99.0}
	if err := validateSchAttributeModifyPayload(payload); err == nil {
		t.Fatal("unsupported property accepted")
	}
}

func TestSchAttributeModifyResultErrorRequiresExactDelta(t *testing.T) {
	before := schAttributeModifyTestState()
	after := schAttributeModifyTestState()
	after["Rotation"] = 0.0
	after["ValueVisible"] = true
	ok := map[string]any{
		"scope": "attribute-properties", "primitiveId": "a1", "verified": true,
		"partial": false, "before": before, "after": after,
		"requested": map[string]any{"rotation": 0.0, "valueVisible": true},
	}
	if err := schAttributeModifyResultError("schematic.attribute.modify", ok); err != nil {
		t.Fatalf("valid result refused: %v", err)
	}
	after["Value"] = "CHANGED"
	if err := schAttributeModifyResultError("schematic.attribute.modify", ok); err == nil {
		t.Fatal("unrequested field drift accepted")
	}
}
