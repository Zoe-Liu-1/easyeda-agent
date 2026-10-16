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
	foot := layoutBBox{MinX: -100, MinY: -80, MaxX: 100, MaxY: 80}
	x, y, ok := bapFirstFitOrigin(foot, nil, usable, nil)
	if !ok || x+foot.MinX < usable.MinX || y+foot.MaxY > usable.MaxY || x+foot.MinX > usable.MinX+bapTileScanStep || y+foot.MaxY < usable.MaxY-bapTileScanStep {
		t.Fatalf("empty sheet: want the top-left corner, got (%v,%v) ok=%v", x, y, ok)
	}
	blocker := layoutBBox{MinX: 12, MinY: 600, MaxX: 400, MaxY: 813}
	x2, y2, ok := bapFirstFitOrigin(foot, []layoutBBox{blocker}, usable, nil)
	if !ok || y2 != y || x2+foot.MinX < blocker.MaxX+bapObstacleGap {
		t.Fatalf("blocked corner: want the same row right of the blocker, got (%v,%v) first (%v,%v)", x2, y2, x, y)
	}
	title := layoutBBox{MinX: 460, MinY: 12, MaxX: 1158, MaxY: 190}
	full := []layoutBBox{{MinX: 12, MinY: 200, MaxX: 1158, MaxY: 813}}
	x3, y3, ok := bapFirstFitOrigin(foot, full, usable, &title)
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
	// decap: five caps 40 apart, ±50 body, only designators sideways (20); its
	// power/ground symbols rise above and below the ±20 pins.
	if w, h := bboxSize(fd); w != 160+2*bapPartMargin+2*4*schAnchorGrid || h != 2*(bslPassivePinHalf+bapTileSymbolReach) {
		t.Fatalf("decap footprint width %v", w)
	}
	if fw, _ := bboxSize(ff); fw <= 2*bapRoleHalfExtent("ic.w25q64_flash")+2*bapEdgeMarkerReach {
		t.Fatalf("flash footprint must carry the label halo sideways, width %v", fw)
	}
}
