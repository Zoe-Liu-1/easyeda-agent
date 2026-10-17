package app

import (
	"testing"

	"github.com/zhoushoujianwork/easyeda-agent/internal/blocks"
)

// Reading order: an empty sheet puts the tile in the top-left corner; a part
// there moves it along the SAME row (aligned tops), not to the nearest spot in
// any direction; the title block is never entered.
func TestBapFirstFitOriginFillsRowsInReadingOrder(t *testing.T) {
	usable := layoutBBox{MinX: 12, MinY: 12, MaxX: 1158, MaxY: 813}
	ident := func(b layoutBBox) layoutBBox { return b }
	foot := layoutBBox{MinX: -100, MinY: -80, MaxX: 100, MaxY: 80}
	x, y, ok := bapFirstFitOrigin(foot, ident, bapObstacleGap, nil, usable, nil, nil)
	if !ok || x+foot.MinX < usable.MinX || y+foot.MaxY > usable.MaxY || x+foot.MinX > usable.MinX+bapTileScanStep || y+foot.MaxY < usable.MaxY-bapTileScanStep {
		t.Fatalf("empty sheet: want the top-left corner, got (%v,%v) ok=%v", x, y, ok)
	}
	blocker := layoutBBox{MinX: 12, MinY: 600, MaxX: 400, MaxY: 813}
	x2, y2, ok := bapFirstFitOrigin(foot, ident, bapObstacleGap, []layoutBBox{blocker}, usable, nil, nil)
	if !ok || y2 != y || x2+foot.MinX < blocker.MaxX+bapObstacleGap {
		t.Fatalf("blocked corner: want the same row right of the blocker, got (%v,%v) first (%v,%v)", x2, y2, x, y)
	}
	title := layoutBBox{MinX: 460, MinY: 12, MaxX: 1158, MaxY: 190}
	full := []layoutBBox{{MinX: 12, MinY: 200, MaxX: 1158, MaxY: 813}}
	x3, y3, ok := bapFirstFitOrigin(foot, ident, bapObstacleGap, full, usable, &title, nil)
	if ok {
		env := layoutBBox{MinX: foot.MinX + x3, MinY: foot.MinY + y3, MaxX: foot.MaxX + x3, MaxY: foot.MaxY + y3}
		if boxesGapOverlap(env, title, bapObstacleGap) {
			t.Fatalf("the slot must not enter the title block: %+v", env)
		}
	}
}

// A passive row only grows short symbols; a tile with a chip or a trunk grows
// full pin labels sideways.
func TestBapTileFootprintWidensForLabelledTiles(t *testing.T) {
	blk, ok, err := blocks.Get("esp32s3r8_chip_minsys")
	if err != nil || !ok {
		t.Fatal(err)
	}
	decap, _ := blk.Tile("decap")
	flash, _ := blk.Tile("flash")
	fd, err := bapTileFootprint(decap, 4, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ff, err := bapTileFootprint(flash, 4, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// decap: five caps 40 apart, ±20 each with designators, nothing sideways but
	// a grid of slack; its power/ground symbols rise above and below the ±20 pins.
	if w, h := bboxSize(fd); w != 160+2*bapTilePassiveHalf+2*2*schAnchorGrid || h != 2*(bslPassivePinHalf+bapTileSymbolReach) {
		t.Fatalf("decap footprint width %v", w)
	}
	// flash: the chip's net labels sideways — a stub plus the name on each side.
	if fw, _ := bboxSize(ff); fw < 2*bapTileChipHalf+2*acNetLabelStub("SPICLK") {
		t.Fatalf("flash footprint must carry its labels sideways, width %v", fw)
	}
}

// The slot holds the partition frame zone-draw will draw (title band above,
// note band below, padding): a tile whose content would fit under another
// tile's frame is pushed clear of it, so the frames never overlap.
func TestBapFirstFitOriginReservesPartitionFrames(t *testing.T) {
	opts := defaultPartitionOpts()
	frame := func(b layoutBBox) layoutBBox { return partitionFrameFloor(b, opts.TitleBand, opts.NoteBand) }
	bounds := layoutBBox{MinX: 28, MinY: 28, MaxX: 1142, MaxY: 797}
	placed := layoutBBox{MinX: 100, MinY: 500, MaxX: 400, MaxY: 700}
	foot := layoutBBox{MinX: -100, MinY: -60, MaxX: 100, MaxY: 60}
	x, y, ok := bapFirstFitOrigin(foot, frame, opts.Gutter, []layoutBBox{placed}, bounds, nil, nil)
	if !ok {
		t.Fatal("no slot on a nearly empty sheet")
	}
	got := frame(layoutBBox{MinX: foot.MinX + x, MinY: foot.MinY + y, MaxX: foot.MaxX + x, MaxY: foot.MaxY + y})
	if boxesGapOverlap(got, frame(placed), opts.Gutter) || !boxInside(got, bounds) {
		t.Fatalf("frames must keep the gutter apart and stay inside: new %+v placed %+v", got, frame(placed))
	}
}

func TestParseTileAts(t *testing.T) {
	got, err := parseTileAts([]string{"xtal=860,700", "flash=80,150"})
	if err != nil || got["xtal"] != [2]float64{860, 700} || got["flash"] != [2]float64{80, 150} {
		t.Fatalf("got %v %v", got, err)
	}
	for _, bad := range []string{"xtal", "=1,2", "xtal=1"} {
		if _, err := parseTileAts([]string{bad}); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}
