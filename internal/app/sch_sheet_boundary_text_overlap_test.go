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
	got := visibleTextOverlapFindings(items, comps, nil, 1.0)
	if len(got) != 2 {
		t.Fatalf("want 2 findings, got %d: %+v", len(got), got)
	}
	for _, f := range got {
		if f.Type != "visible-text-overlap" {
			t.Errorf("bad finding type: %+v", f)
		}
	}
}

func TestVisibleTextOverlapFindings_WireCollision(t *testing.T) {
	b := func(x0, y0, x1, y1 float64) *layoutBBox {
		return &layoutBBox{MinX: x0, MinY: y0, MaxX: x1, MaxY: y1}
	}
	items := []visibleTextItem{
		{ID: "t_net", Kind: "attribute", ComponentType: "wire", ParentID: "w1", Key: "Name", Value: "SIG", BBox: b(10, 10, 30, 20)},
		{ID: "t_val", Kind: "attribute", ComponentType: "part", ParentID: "u1", Designator: "U1", Key: "Value", BBox: b(10, 40, 30, 50)},
		{ID: "t_des", Kind: "attribute", ComponentType: "part", ParentID: "u1", Designator: "U1", Key: "Designator", BBox: b(10, 60, 30, 70)},
	}
	wires := []schGroupWire{
		{ID: "w1", ObservedSegments: [][4]float64{{15, 0, 15, 30}}}, // t_net's own wire: must not fire
		{ID: "w2", ObservedSegments: [][4]float64{{25, 0, 25, 30}}}, // foreign wire through t_net
		{ID: "w3", ObservedSegments: [][4]float64{{0, 45, 40, 45}}}, // foreign wire through t_val
		{ID: "w4", ObservedSegments: [][4]float64{{0, 65, 40, 65}}}, // Designator is reported by its own rule
	}
	got := visibleTextOverlapFindings(items, nil, wires, 1.0)
	if len(got) != 2 {
		t.Fatalf("want 2 wire-text findings (net + Value), got %d: %+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, f := range got {
		if f.Type != "visible-text-overlap" {
			t.Errorf("bad finding type: %+v", f)
		}
		seen[f.WirePrimitiveId] = true
	}
	if !seen["w2"] || !seen["w3"] || seen["w1"] || seen["w4"] {
		t.Fatalf("wire collision set wrong: %+v", got)
	}
}

func TestVisibleTextFrameCrossingIncludesShownAttributes(t *testing.T) {
	box := func(x0, y0, x1, y1 float64) *layoutBBox {
		return &layoutBBox{MinX: x0, MinY: y0, MaxX: x1, MaxY: y1}
	}
	items := []visibleTextItem{
		{ID: "value", Kind: "attribute", ComponentType: "part", ParentID: "r1", Designator: "R1", Key: "Value",
			BBox: box(90, 20, 130, 30)},
		{ID: "inside", Kind: "attribute", ComponentType: "part", ParentID: "r2", Designator: "R2", Key: "Value",
			BBox: box(20, 20, 40, 30)},
		{ID: "title", Kind: "attribute", ComponentType: "sheet", ParentID: "sheet", Key: "Name",
			BBox: box(90, 20, 130, 30)},
	}
	frames := []schFrameSpec{{ID: "f1", Rect: *box(0, 0, 100, 100)}}
	got := visibleTextFrameCrossingFindings(items, frames, 0.5)
	if len(got) != 1 || got[0].PrimitiveId != "value" || got[0].Type != "text-frame-crossing" {
		t.Fatalf("want only the shown Value crossing the module boundary, got %+v", got)
	}
}

func TestVisibleTextOverlapLeavesDesignatorOwnershipToDesignatorRule(t *testing.T) {
	box := func(x0, y0, x1, y1 float64) *layoutBBox {
		return &layoutBBox{MinX: x0, MinY: y0, MaxX: x1, MaxY: y1}
	}
	comps := []layoutComp{{ID: "u1", Designator: "U1", ComponentType: "part", BBox: box(0, 0, 100, 100)}}
	items := []visibleTextItem{{ID: "d1", Kind: "attribute", ComponentType: "part", ParentID: "u1", Designator: "U1",
		Key: "Designator", BBox: box(40, 40, 60, 50)}}
	if got := visibleTextOverlapFindings(items, comps, nil, 0.5); len(got) != 0 {
		t.Fatalf("Designator collisions must be owned by designator-overlap, got %+v", got)
	}
}
