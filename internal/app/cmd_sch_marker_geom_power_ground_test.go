package app

import "testing"

func TestPowerGroundOrientationFindings(t *testing.T) {
	rot := func(v float64) *float64 { return &v }
	comps := []layoutComp{
		{ID: "okp", ComponentType: "netflag", Net: "+3V3", Rotation: rot(flagBodyRotation["power"]["up"])},
		{ID: "badp", ComponentType: "netflag", Net: "+3V3_SIDE", Rotation: rot(flagBodyRotation["power"]["left"])},
		{ID: "okg", ComponentType: "netflag", Net: "GND", Rotation: rot(flagBodyRotation["ground"]["down"])},
		{ID: "badg", ComponentType: "netflag", Net: "GND_SIDE", Rotation: rot(flagBodyRotation["ground"]["up"])},
		{ID: "revp", ComponentType: "netflag", Net: "+3V3_REV", Rotation: rot(flagBodyRotation["power"]["down"])},
		{ID: "revp-prop", ComponentType: "netflag", Net: "+3V3_PROP", Rotation: rot(flagBodyRotation["power"]["down"]), OrientationExempt: true},
		{ID: "port", ComponentType: "netport", Net: "SIG", Rotation: rot(0)},
	}
	exempt := map[string]bool{"revp": true}
	problems, exempted := powerGroundOrientationFindings(comps, exempt)
	if len(problems) != 2 {
		t.Fatalf("want 2 problems, got %d: %+v", len(problems), problems)
	}
	if problems[0].PrimitiveId != "badg" || problems[1].PrimitiveId != "badp" {
		t.Fatalf("unexpected problem order/ids: %+v", problems)
	}
	for _, f := range problems {
		if f.Type != "power-ground-orientation" || f.Level != "warn" {
			t.Errorf("bad finding: %+v", f)
		}
	}
	if len(exempted) != 2 {
		t.Fatalf("want 2 exempted (CLI id + readback property), got %d: %+v", len(exempted), exempted)
	}
	for _, f := range exempted {
		if f.Type != "orientation-exempt" || f.Level != "info" {
			t.Errorf("bad exemption finding: %+v", f)
		}
	}
}
