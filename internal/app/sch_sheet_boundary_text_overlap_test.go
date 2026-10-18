package app

import "testing"

func TestVisibleTextOverlapFindings(t *testing.T) {
	b := func(x0, y0, x1, y1 float64) *layoutBBox {
		return &layoutBBox{MinX: x0, MinY: y0, MaxX: x1, MaxY: y1}
	}
	comps := []layoutComp{
		{ID: "u1", Designator: "U1", ComponentType: "part", BBox: b(0, 0, 100, 100)},
		{ID: "f1", Net: "+3V3", ComponentType: "netflag", BBox: b(0, 120, 30, 145)},
	}
	items := []visibleTextItem{
		{ID: "t_own", Kind: "attribute", ParentID: "u1", Designator: "U1", Key: "Value", BBox: b(10, 10, 40, 25)},
		{ID: "t_cross", Kind: "attribute", ParentID: "r1", Designator: "R1", Key: "Value", BBox: b(50, 50, 90, 70)},
		{ID: "t_text", Kind: "text", Content: "说明", BBox: b(0, 120, 20, 140)},
	}
	got := visibleTextOverlapFindings(items, comps, 1.0)
	if len(got) != 2 {
		t.Fatalf("want 2 findings, got %d: %+v", len(got), got)
	}
	for _, f := range got {
		if f.Type != "visible-text-overlap" {
			t.Errorf("bad finding type: %+v", f)
		}
	}
}
