package app

// cmd_sch_block_apply_tiles.go — block-apply for a tiled block
// (schematic_layout.tiles): the core chip alone with its pin labels, each
// peripheral function in its own frame, frames joined by net labels. Each tile
// is an ordinary block (blocks.Get("<id>#<tile>") derives it from the parent's
// data), so it goes through the whole single-block pipeline — placement gate,
// wiring, netlist reconcile, grouping — unchanged.

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/zhoushoujianwork/easyeda-agent/internal/blocks"
)

// runTiledBlockApply applies the tiles in declaration order. The first tile
// (the core) lands at the requested origin; every later tile takes the first
// free spot in reading order (bapPlaceTile), so the frames fill the sheet in
// rows like a drafted reference sheet instead of being stacked by hand-picked
// coordinates.
//
// A tile that fails rolls back its own parts (the single-block contract); the
// tiles before it stay, and the error names them, because they are complete,
// reconciled blocks of their own.
func runTiledBlockApply(cfg *appConfig, window string, b blocks.Block, tiles []blocks.SchematicTile, in bapInput,
	partsPath string, dryRun, asJSON bool, maxAttempts int, stdout, stderr io.Writer) error {

	derived := make([]blocks.Block, len(tiles))
	known := map[string]bool{}
	for i, t := range tiles {
		tb, err := b.Tile(t.Name)
		if err != nil {
			return err
		}
		derived[i] = tb
		for p := range tb.Ports {
			known[p] = true
		}
	}
	for port := range in.Bind {
		if !known[port] {
			return fmt.Errorf("--bind %s=…: block %s has no port %q (tile links: %s)", port, b.ID, port, strings.Join(sortedKeys(known), ", "))
		}
	}
	if dryRun {
		fmt.Fprintf(stderr, "tiles: %s — dry-run plans each tile on the current page; later tiles are not displaced by earlier ones until they are really placed\n",
			strings.Join(blocks.TileNames(tiles), ", "))
	}
	var applied []string
	for i, t := range tiles {
		tin := in
		tin.Bind = map[string]string{}
		for port, net := range in.Bind {
			if _, ok := derived[i].Ports[port]; ok {
				tin.Bind[port] = net
			}
		}
		if i > 0 {
			bapPlaceTile(cfg, window, derived[i], &tin, dryRun, stderr)
		}
		fmt.Fprintf(stderr, "tile %d/%d %s (%d part(s))\n", i+1, len(tiles), t.Name, len(t.Roles))
		if err := runBlockApply(cfg, window, derived[i].ID, tin, partsPath, dryRun, asJSON, maxAttempts, stdout, stderr); err != nil {
			if len(applied) > 0 && !dryRun {
				return fmt.Errorf("tile %s failed; tiles %s are already applied and stay on the page (PARTIAL STATE — each is a complete, reconciled block; delete them or apply the remaining tiles): %w",
					t.Name, strings.Join(applied, ", "), err)
			}
			return fmt.Errorf("tile %s: %w", t.Name, err)
		}
		applied = append(applied, t.Name)
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// bapPlaceTile picks a tile's origin: the first spot in reading order (rows top
// to bottom, left to right on a 20 grid) where the tile's estimated footprint
// clears every part and marker already on the page — measured, so the tiles
// placed before it count with their real extent — the title block and the
// sheet edge. First fit in reading order is what lines the frames up in rows;
// the old "nearest free spot to the core" pushed a 3-part crystal tile into the
// sheet corner, where its trunks had no room and grew long wires (live
// 2026-10-17). No fit leaves the old nearest-free search in charge.
func bapPlaceTile(cfg *appConfig, window string, tile blocks.Block, in *bapInput, dryRun bool, stderr io.Writer) {
	in.AtExplicit = false
	obstacles, title, sheet, _ := fetchSchObstaclesAndKeepout(cfg, window)
	if sheet == nil {
		return
	}
	foot, err := bapTileFootprint(tile, in.PerRow, in.Instance, in.Bind)
	if err != nil {
		return
	}
	// The slot must hold the tile's partition frame, not just its content:
	// zone-draw frames every tile (title band on top, note band below) and frames
	// may not overlap. Judged with the tightest frame zone-plan accepts
	// (partitionFrameFloor: the side padding yields, the bands do not), applied
	// to the new tile and to every primitive already placed.
	opts := defaultPartitionOpts()
	frame := func(b layoutBBox) layoutBBox { return partitionFrameFloor(b, opts.TitleBand, opts.NoteBand) }
	bounds := layoutBBox{MinX: sheet.MinX + opts.Margin, MinY: sheet.MinY + opts.Margin, MaxX: sheet.MaxX - opts.Margin, MaxY: sheet.MaxY - opts.Margin}
	// A relational tile is also judged before placement by the planner's own
	// body rectangle (relational preflight); a slot must pass that ruler too, or
	// the tile is refused after the slot was chosen (live: the crystal tile).
	accept := bapTilePreflight(tile, in.PerRow, obstacles, title)
	x, y, ok := bapFirstFitOrigin(foot, frame, opts.Gutter, obstacles, bounds, title, accept)
	if !ok {
		// The staggered second lane is the estimate's slack (live W25Q64: 494
		// measured against 600 estimated); one label lane still keeps every
		// label off the neighbours, and the scorer turns away what is left.
		if lane := bapTileLaneSlack(tile, in.Instance, in.Bind); lane > 0 {
			slim := layoutBBox{MinX: foot.MinX + lane, MinY: foot.MinY, MaxX: foot.MaxX - lane, MaxY: foot.MaxY}
			x, y, ok = bapFirstFitOrigin(slim, frame, opts.Gutter, obstacles, bounds, title, accept)
		}
	}
	if !ok {
		fmt.Fprintf(stderr, "tile %s: no reading-order slot clears its estimated footprint — falling back to the nearest free spot\n", tile.ID)
		return
	}
	in.OriginX, in.OriginY, in.AtExplicit = x, y, true
	if dryRun {
		fmt.Fprintf(stderr, "tile %s: slot (%.0f,%.0f) judged against the page as it is now\n", tile.ID, x, y)
	}
}

// bapTileFootprint is a tile's estimated envelope relative to its origin,
// calibrated on the live ESP32-S3R8 tiles (2026-10-17): the parts' bodies by
// class — a 2-pin part's pins end ±20 from its centre, an IC is as tall as its
// referenced pins stack per side — plus the markers they grow. A passive row
// carries power/ground symbols and elbowed labels above and below
// (bapTileSymbolReach) and little sideways; a chip or a trunk carries its pin
// labels sideways in two staggered lanes (label halo + one lane step) and
// little above and below; a trunk tile short trunks ending in a port label.
func bapTileFootprint(b blocks.Block, perRow int, instance string, bind map[string]string) (layoutBBox, error) {
	layout, err := b.SchematicLayout()
	if err != nil {
		return layoutBBox{}, err
	}
	pins := map[string]map[string]bool{}
	for _, net := range bslBlockNets(b) {
		for _, m := range net {
			if role, pin, ok := splitBlockPinRef(strings.TrimSuffix(m, "*")); ok {
				if pins[role] == nil {
					pins[role] = map[string]bool{}
				}
				pins[role][pin] = true
			}
		}
	}
	roles := make([]string, 0, len(b.Parts))
	halfOf := map[string]float64{}
	halfH := map[string]float64{}
	wide := false
	for r, p := range b.Parts {
		roles = append(roles, r)
		// Measured, not the placement grid's floor (bapPartMargin): a vertical
		// passive with its designator spans ±20, an SOP-class chip ±80.
		halfOf[r] = math.Min(bapRoleHalfExtent(p.Part), bapTileChipHalf)
		if bslIsSimplePeripheral(p.Part) {
			halfOf[r] = bapTilePassiveHalf
			halfH[r] = bslPassivePinHalf
		} else {
			wide = true
			halfH[r] = math.Max(30, math.Ceil(float64(len(pins[r]))/2)*float64(schAnchorGrid)+15)
		}
	}
	sort.Strings(roles)
	if perRow < 1 {
		perRow = 4
	}
	offsets := bapRoleOffsets(roles, layout, bapGridSpacing(roles, b), perRow, halfOf)
	rise := bapTileSymbolReach
	trunk := false
	if rel, ok := bslRelationsFrom(layout); ok {
		anchor, err := bslAnchorRole(b, rel, bslBlockNets(b))
		if err != nil {
			return layoutBBox{}, err
		}
		offsets = bslSeedOffsets(b, rel, anchor, roles)
		trunk = true
	} else if wide {
		rise = bapEdgeMarkerReach / 2
	}
	first := true
	var box layoutBBox
	for role, o := range offsets {
		hw, hh := halfOf[role], halfH[role]
		r := layoutBBox{MinX: o.dx - hw, MinY: o.dy - hh, MaxX: o.dx + hw, MaxY: o.dy + hh}
		if first {
			box, first = r, false
			continue
		}
		box = layoutBBox{MinX: math.Min(box.MinX, r.MinX), MinY: math.Min(box.MinY, r.MinY),
			MaxX: math.Max(box.MaxX, r.MaxX), MaxY: math.Max(box.MaxY, r.MaxY)}
	}
	halo := bapMarkerHalo(b, instance, bind)
	side := float64(2 * schAnchorGrid) // a power/ground-only row: nothing sideways
	longest := ""
	for port, p := range b.Ports {
		n := bapOr(bind[port], bapOr(p.DefaultNet, port))
		if bapFlagKind(n) != "netport" {
			continue
		}
		if len(n) > len(longest) {
			longest = n
		}
		if b.SignalMarker == "net_label" {
			side = math.Max(side, acNetLabelTextWidth(longest)) // the name runs right of its stub
		} else {
			side = bapEdgeMarkerReach // elbowed ports turn sideways
		}
	}
	switch {
	case wide && b.SignalMarker == "net_label": // one even row: stub + name
		side = acNetLabelStub(longest) + acNetLabelTextWidth(longest)
	case wide: // a chip's pin labels stand in two staggered lanes
		side = halo + (halo - defaultAutoconnectRules().OffsetMin) + acLaneGap
	case trunk: // short trunks with a port label at the end (live crystal tile: 155 wide)
		side = float64(4 * schAnchorGrid)
	}
	return layoutBBox{MinX: box.MinX - side, MinY: box.MinY - rise, MaxX: box.MaxX + side, MaxY: box.MaxY + rise}, nil
}

// bapTilePreflight is planBlockApply's relational preflight as a slot test:
// the planner's seed body rectangle must clear the existing geometry and the
// title block by bapObstacleGap (nil for a non-relational tile).
func bapTilePreflight(b blocks.Block, perRow int, obstacles []layoutBBox, title *layoutBBox) func(x, y float64) bool {
	layout, err := b.SchematicLayout()
	if err != nil {
		return nil
	}
	rel, ok := bslRelationsFrom(layout)
	if !ok {
		return nil
	}
	anchor, err := bslAnchorRole(b, rel, bslBlockNets(b))
	if err != nil {
		return nil
	}
	roles := make([]string, 0, len(b.Parts))
	for r := range b.Parts {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	offsets := bslSeedOffsets(b, rel, anchor, roles)
	half := func(role string) float64 { return math.Max(bapRoleHalfExtent(b.Parts[role].Part), bapPartMargin) }
	return func(x, y float64) bool {
		rect := bapBlockRect(x, y, offsets, half)
		if title != nil && boxesGapOverlap(rect, *title, bapObstacleGap) {
			return false
		}
		for _, o := range obstacles {
			if boxesGapOverlap(rect, o, bapObstacleGap) {
				return false
			}
		}
		return true
	}
}

// bapTileLaneSlack is the second label lane bapTileFootprint adds on each side
// of a chip tile (zero for other tiles).
func bapTileLaneSlack(b blocks.Block, instance string, bind map[string]string) float64 {
	if b.SignalMarker == "net_label" {
		return 0 // net labels do not stagger
	}
	for _, p := range b.Parts {
		if !bslIsSimplePeripheral(p.Part) {
			return bapMarkerHalo(b, instance, bind) - defaultAutoconnectRules().OffsetMin + acLaneGap
		}
	}
	return 0
}

// bapTilePassiveHalf / bapTileChipHalf are measured half widths for the tile
// footprint (live 2026-10-17): a vertical passive with its designator text, and
// an SOP-class chip with its pins (W25Q64 box ±71 + pin 10).
const (
	bapTilePassiveHalf = 20.0
	bapTileChipHalf    = 80.0
)

// bapTileSymbolReach is how far a passive row's markers rise past its pins: a
// power/ground symbol with its name band, or a netport elbowed up and turned
// (live: 127 tall for the decoupling row, 167 for the EN/IO0 pull-ups).
const bapTileSymbolReach = 65.0

// bapTileScanStep is the reading-order scan pitch: four connection grids keeps
// the frames aligned without thousands of candidates.
const bapTileScanStep = 4 * schAnchorGrid

// bapFirstFitOrigin scans origins in reading order (y-up: top row first) and
// returns the first where grow(footprint) stays inside bounds, clears the title
// block, and keeps gap from grow(o) for every obstacle o — grow is the frame
// around a tile's content, so two grown boxes apart means two frames apart.
func bapFirstFitOrigin(foot layoutBBox, grow func(layoutBBox) layoutBBox, gap float64, obstacles []layoutBBox, bounds layoutBBox, title *layoutBBox, accept func(x, y float64) bool) (float64, float64, bool) {
	g := grow(foot)
	w, h := bboxSize(g)
	grown := make([]layoutBBox, len(obstacles))
	for i, o := range obstacles {
		grown[i] = grow(o)
	}
	for top := bounds.MaxY; top-h >= bounds.MinY; top -= bapTileScanStep {
		for left := bounds.MinX; left+w <= bounds.MaxX; left += bapTileScanStep {
			ox, oy := snapAnchor(left-g.MinX), snapAnchor(top-g.MaxY)
			env := layoutBBox{MinX: g.MinX + ox, MinY: g.MinY + oy, MaxX: g.MaxX + ox, MaxY: g.MaxY + oy}
			if !boxInside(env, bounds) || (title != nil && boxesGapOverlap(env, *title, gap)) {
				continue
			}
			clear := true
			for _, o := range grown {
				if boxesGapOverlap(env, o, gap) {
					clear = false
					break
				}
			}
			if clear && (accept == nil || accept(ox, oy)) {
				return ox, oy, true
			}
		}
	}
	return 0, 0, false
}
