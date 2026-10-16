package blocks

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// SchematicTile is one functional frame of a tiled schematic layout: the core
// chip alone with its pin labels, a decoupling bank, a crystal with its load
// capacitors. Inside a tile parts are wired (by Layout); between tiles the nets
// are joined by labels, the convention of a drafted reference schematic where
// a many-pin MCU never carries its peripherals on its own pins.
type SchematicTile struct {
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
	// Layout is the tile's own template (relational or legacy offsets); nil
	// lets the fallback grid place the tile.
	Layout *SchematicLayout `json:"layout,omitempty"`
}

// TileSeparator joins a block id and a tile name into the derived tile block's
// id: "block.esp32s3r8_chip_minsys#core".
const TileSeparator = "#"

// Tiles returns the block's declared tiles (nil when it is not tiled).
func (b Block) Tiles() ([]SchematicTile, error) {
	layout, err := b.SchematicLayout()
	if err != nil || layout == nil {
		return nil, err
	}
	return layout.Tiles, nil
}

var reTileNetName = regexp.MustCompile(`[^A-Za-z0-9_+\-]`)

// TileNetNames names every parent net that crosses a tile boundary and has no
// port: the function name of its first named pin (U.XTAL_P → XTAL_P), so the
// labels joining two tiles read like the chip's own pin names. A numeric pin
// falls back to ROLE_PIN; clashes with ports or each other get _2, _3. The
// result is keyed by the net's index in internal_nets and is the same for every
// tile, which is what makes the labels meet.
func (b Block) TileNetNames(tiles []SchematicTile) map[int]string {
	owner := map[string]string{}
	for _, t := range tiles {
		for _, r := range t.Roles {
			owner[r] = t.Name
		}
	}
	taken := map[string]bool{}
	for name := range b.Ports {
		taken[strings.ToUpper(name)] = true
	}
	out := map[int]string{}
	for i, net := range blockInternalNets(b) {
		if netHasPort(net) || !netCrossesTiles(net, owner) {
			continue
		}
		base := ""
		for _, m := range net {
			role, pin, ok := splitPinRef(strings.TrimSuffix(m, pinFanoutSuffix))
			if !ok {
				continue
			}
			if !isNumeric(pin) {
				base = pin
				break
			}
			if base == "" {
				base = role + "_" + pin
			}
		}
		base = strings.Trim(reTileNetName.ReplaceAllString(base, "_"), "_")
		name := base
		for n := 2; taken[strings.ToUpper(name)]; n++ {
			name = fmt.Sprintf("%s_%d", base, n)
		}
		taken[strings.ToUpper(name)] = true
		out[i] = name
	}
	return out
}

// Tile derives one tile as a standalone block: its parts, its share of every
// net, and a port for each net that leaves the tile (a parent port keeps its
// name and default net; any other crossing net becomes a port named by
// TileNetNames). The derivation is a pure function of the parent's data, so
// `Get("<id>#<tile>")` rebuilds the same block in any later command.
func (b Block) Tile(name string) (Block, error) {
	tiles, err := b.Tiles()
	if err != nil {
		return Block{}, err
	}
	var tile *SchematicTile
	owner := map[string]string{}
	for i := range tiles {
		for _, r := range tiles[i].Roles {
			owner[r] = tiles[i].Name
		}
		if tiles[i].Name == name {
			tile = &tiles[i]
		}
	}
	if tile == nil {
		return Block{}, fmt.Errorf("block %s has no schematic tile %q", b.ID, name)
	}
	in := map[string]bool{}
	parts := map[string]Part{}
	for _, r := range tile.Roles {
		p, ok := b.Parts[r]
		if !ok {
			return Block{}, fmt.Errorf("tile %q: unknown role %q", name, r)
		}
		in[r], parts[r] = true, p
	}
	names := b.TileNetNames(tiles)
	ports := map[string]Port{}
	var nets [][]string
	for i, net := range blockInternalNets(b) {
		var local []string
		var parentPort string
		for _, m := range net {
			if p, ok := strings.CutPrefix(m, "PORT:"); ok {
				parentPort = p
				continue
			}
			if role, _, ok := splitPinRef(strings.TrimSuffix(m, pinFanoutSuffix)); ok && in[role] {
				local = append(local, m)
			}
		}
		if len(local) == 0 {
			continue
		}
		switch {
		case parentPort != "":
			p := b.Ports[parentPort]
			if role, _, ok := splitPinRef(strings.TrimSuffix(p.At, pinFanoutSuffix)); !ok || !in[role] {
				p.At = strings.TrimSuffix(local[0], pinFanoutSuffix)
			}
			ports[parentPort] = p
			local = append(local, "PORT:"+parentPort)
		case names[i] != "":
			ports[names[i]] = Port{Dir: "bidir", At: strings.TrimSuffix(local[0], pinFanoutSuffix),
				Desc: "tile link", DefaultNet: names[i]}
			local = append(local, "PORT:"+names[i])
		}
		if len(local) >= 2 {
			nets = append(nets, local)
		}
	}
	// A parent port anchored on a tile pin with no internal net (a one-pin
	// boundary) still belongs to that tile.
	for pname, p := range b.Ports {
		if role, _, ok := splitPinRef(strings.TrimSuffix(p.At, pinFanoutSuffix)); ok && in[role] {
			if _, done := ports[pname]; !done {
				ports[pname] = p
			}
		}
	}
	doc := map[string]any{
		"id": b.ID + TileSeparator + name, "desc": fmt.Sprintf("%s — tile %s", b.Desc, name),
		"category": b.Category, "author": b.Author, "contributors": b.Contributors,
		"added": b.Added, "updated": b.Updated, "source": b.Source, "validated": b.Validated,
		"parts": parts, "internal_nets": nets, "ports": ports,
	}
	if tile.Layout != nil {
		doc["schematic_layout"] = tile.Layout
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return Block{}, err
	}
	var out Block
	if err := json.Unmarshal(raw, &out); err != nil {
		return Block{}, err
	}
	out.Raw = raw
	return out, nil
}

// TileNames lists the tile names in declaration order.
func TileNames(tiles []SchematicTile) []string {
	out := make([]string, len(tiles))
	for i, t := range tiles {
		out[i] = t.Name
	}
	return out
}

func netHasPort(net []string) bool {
	for _, m := range net {
		if strings.HasPrefix(m, "PORT:") {
			return true
		}
	}
	return false
}

func netCrossesTiles(net []string, owner map[string]string) bool {
	seen := ""
	for _, m := range net {
		role, _, ok := splitPinRef(strings.TrimSuffix(m, pinFanoutSuffix))
		if !ok {
			continue
		}
		t := owner[role]
		if seen != "" && t != seen {
			return true
		}
		seen = t
	}
	return false
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// validateSchTiles checks a tiled layout: every part in exactly one tile, the
// tiles alone (no top-level relations beside them), and each derived tile a
// valid block on its own — its layout judged by the same rules as any block.
func validateSchTiles(b Block, layout *SchematicLayout, add func(string, string)) {
	if layout.IsLegacy() || layout.IsRelational() || layout.Anchor != "" {
		add("schematic_layout.tiles", "tiles 与顶层 roles/anchor/flow/attach/pair 不可混用 —— 关系写进各 tile 的 layout")
	}
	seenTile := map[string]bool{}
	seenRole := map[string]string{}
	for i, t := range layout.Tiles {
		field := fmt.Sprintf("schematic_layout.tiles[%d]", i)
		if t.Name == "" || strings.Contains(t.Name, TileSeparator) || seenTile[t.Name] {
			add(field+".name", fmt.Sprintf("tile name %q must be non-empty, unique and free of %q", t.Name, TileSeparator))
		}
		seenTile[t.Name] = true
		if len(t.Roles) == 0 {
			add(field+".roles", "a tile needs at least one role")
		}
		for _, r := range t.Roles {
			if _, ok := b.Parts[r]; !ok {
				add(field+".roles", fmt.Sprintf("unknown role %q", r))
			}
			if prev, dup := seenRole[r]; dup {
				add(field+".roles", fmt.Sprintf("role %q already in tile %q", r, prev))
			}
			seenRole[r] = t.Name
		}
	}
	roles := make([]string, 0, len(b.Parts))
	for r := range b.Parts {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	for _, r := range roles {
		if _, ok := seenRole[r]; !ok {
			add("schematic_layout.tiles", fmt.Sprintf("role %q is in no tile", r))
		}
	}
	for _, t := range layout.Tiles {
		tb, err := b.Tile(t.Name)
		if err != nil {
			add("schematic_layout.tiles."+t.Name, err.Error())
			continue
		}
		// Only what the derivation adds: the tile's nets, ports and layout. The
		// metadata is the parent's, already judged once.
		for _, e := range Validate(tb) {
			if ve, ok := e.(ValidationError); ok && !strings.HasPrefix(ve.Field, "schematic_layout") &&
				!strings.HasPrefix(ve.Field, "internal_nets") && !strings.HasPrefix(ve.Field, "ports") {
				continue
			}
			add("schematic_layout.tiles."+t.Name, e.Error())
		}
	}
}
