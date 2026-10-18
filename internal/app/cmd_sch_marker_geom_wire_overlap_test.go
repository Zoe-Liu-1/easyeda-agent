package app

import "testing"

func TestMarkerWireOverlapFindings(t *testing.T) {
	marker := layoutComp{
		ID: "f1", ComponentType: "netflag", Net: "+3V3",
		X: 110, Y: 120, BBox: &layoutBBox{MinX: 100, MinY: 100, MaxX: 120, MaxY: 140},
	}
	wires := []schGroupWire{
		{ID: "stub", Points: []float64{110, 120, 110, 80}},   // 自己的短桩:应跳过
		{ID: "graze", Points: []float64{100, 100, 130, 100}}, // 贴边:内缩 eps 后不算
		{ID: "cross", Points: []float64{100, 110, 130, 110}}, // 真正穿入:应报
	}
	got := markerWireOverlapFindings([]layoutComp{marker}, wires, 1.0)
	if len(got) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(got), got)
	}
	if got[0].Type != "marker-wire-overlap" || got[0].Level != "warn" {
		t.Fatalf("bad finding: %+v", got[0])
	}
	if got[0].WirePrimitiveId != "cross" || got[0].MarkerPrimitiveId != "f1" {
		t.Fatalf("unexpected ids: %+v", got[0])
	}
}
