package app

import (
	"strings"
	"testing"
)

func TestSheetBoundaryFindingsCatchWiresAndMarkersTheOldRulersMissed(t *testing.T) {
	// A4 page shaped like the 2026-10-13 demo: C3's stub ran from the part down
	// through the title block and its 3V3 flag landed below the frame.
	sheet := &layoutBBox{MinX: 0, MinY: 0, MaxX: 1169, MaxY: 825}
	titleBlock, source := titleBlockKeepoutWithSource(sheet)
	if titleBlock == nil {
		t.Fatal("A4 sheet must yield a title-block keep-out")
	}
	midX := (titleBlock.MinX + titleBlock.MaxX) / 2
	comps := []layoutComp{
		{ID: "ok", ComponentType: "part", Designator: "C1", BBox: &layoutBBox{MinX: 100, MinY: 400, MaxX: 120, MaxY: 420}},
		{ID: "flag", ComponentType: "netflag", Net: "3V3", BBox: &layoutBBox{MinX: midX - 9, MinY: -40, MaxX: midX + 9, MaxY: -22}},
	}
	wires := []schGroupWire{
		{ID: "stub", ObservedSegments: [][4]float64{{midX, 300, midX, -22}}},
		{ID: "clean", ObservedSegments: [][4]float64{{100, 420, 100, 500}}},
	}
	got := sheetBoundaryFindings(comps, wires, sheet, titleBlock, source, 0.5)
	seen := map[string]bool{}
	for _, f := range got {
		seen[f.Type+"/"+f.PrimitiveId+f.WirePrimitiveId] = true
	}
	for _, want := range []string{"out-of-sheet/flag", "out-of-sheet/stub", "titleblock-overlap/stub"} {
		if !seen[want] {
			t.Errorf("missing %s in %+v", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("want exactly 3 findings (clean part and wire stay silent), got %d: %+v", len(got), got)
	}
	if sheetBoundaryFindings(comps, wires, nil, nil, sheetSourceNone, 0.5) != nil {
		t.Error("paperless page has no boundary to judge")
	}
}

func TestVisibleTextBoundaryFindingsFlagsShownAttributesBelowTheFrame(t *testing.T) {
	sheet := &layoutBBox{MinX: 0, MinY: 0, MaxX: 1170, MaxY: 825}
	items := []visibleTextItem{
		// live 2026-10-13: title-block Name shown → drawn under the frame
		{ID: "a1", Kind: "attribute", ComponentType: "sheet", Key: "Name", BBox: &layoutBBox{MinX: 0, MinY: -20, MaxX: 120, MaxY: -10}},
		{ID: "t1", Kind: "text", Content: "MCU", BBox: &layoutBBox{MinX: 20, MinY: 790, MaxX: 200, MaxY: 815}},
		{ID: "a2", Kind: "attribute", Designator: "R1", Key: "Value", BBox: &layoutBBox{MinX: 300, MinY: 300, MaxX: 330, MaxY: 310}},
		{ID: "u1", Kind: "text", Content: "unmeasured"},
	}
	got := visibleTextBoundaryFindings(items, sheet, 0.5)
	if len(got) != 1 || got[0].PrimitiveId != "a1" || got[0].Type != "out-of-sheet" || !strings.Contains(got[0].Message, "Name") {
		t.Fatalf("only the shown title attribute below the frame is out of sheet: %+v", got)
	}
	if visibleTextBoundaryFindings(items, nil, 0.5) != nil {
		t.Fatal("paperless page has no boundary to judge")
	}
}
