package app

import "testing"

func TestPowerGroundOrientationFindings(t *testing.T) {
	rot := func(v float64) *float64 { return &v }
	comps := []layoutComp{
		{ID: "okp", ComponentType: "netflag", Net: "+3V3", Rotation: rot(flagBodyRotation["power"]["up"])},
		{ID: "badp", ComponentType: "netflag", Net: "+3V3_SIDE", Rotation: rot(flagBodyRotation["power"]["left"])},
		{ID: "okg", ComponentType: "netflag", Net: "GND", Rotation: rot(flagBodyRotation["ground"]["down"])},
		{ID: "badg", ComponentType: "netflag", Net: "GND_SIDE", Rotation: rot(flagBodyRotation["ground"]["up"])},
		{ID: "port", ComponentType: "netport", Net: "SIG", Rotation: rot(0)},
	}
	got := powerGroundOrientationFindings(comps)
	if len(got) != 2 {
		t.Fatalf("want 2 findings, got %d: %+v", len(got), got)
	}
	if got[0].PrimitiveId != "badg" || got[1].PrimitiveId != "badp" {
		t.Fatalf("unexpected order/ids: %+v", got)
	}
	for _, f := range got {
		if f.Type != "power-ground-orientation" || f.Level != "warn" {
			t.Errorf("bad finding: %+v", f)
		}
	}
}
