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
	"sort"
	"strings"

	"github.com/zhoushoujianwork/easyeda-agent/internal/blocks"
)

// runTiledBlockApply applies the tiles in declaration order. The first tile
// (the core) lands at the requested origin; every later tile asks for that
// same point without pinning it, so the origin search moves it to the nearest
// free spot that also clears its own label halo — the frames gather around
// the core instead of being stacked by hand-picked coordinates.
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
			tin.AtExplicit = false
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
