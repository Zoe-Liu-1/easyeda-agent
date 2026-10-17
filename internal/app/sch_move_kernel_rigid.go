package app

// sch_move_kernel_rigid.go — the move kernel's rigid rebuild of wire trees that
// move as a whole.
//
// The kernel deletes every wire tree on a moved part and reconnects its pins
// through markers (ADR-0004): re-laying a wire next to unmoved copper can merge
// it into a neighbour and short two nets. That is right for a tree that also
// touches something staying put, and wrong for a tree that only joins parts
// moving by the same (dx,dy): the pin-to-pin wire of a crystal and its load
// capacitor came back as two labels (live 2026-10-17), and a net-label stub came
// back as whatever autoconnect scored. Such a tree keeps its shape relative to
// everything it touches, so it is re-laid segment by segment at the same offset,
// with its net labels, before the marker reconnect; the reconcile step still
// judges the result electrically and the recovery path still backs it.

import (
	"math"
	"sort"
	"strings"
	"time"
)

// moveWireSeg is one observed wire segment (a branched wire primitive is
// several), with the wire's own net name.
type moveWireSeg struct {
	WireID string
	Seg    [4]float64
	Net    string
}

// moveWireLabel is a native net label: a visible Name attribute on a wire.
type moveWireLabel struct {
	WireID string
	X, Y   float64
	Net    string
}

// moveRigidTree is a wire tree re-laid by translation.
type moveRigidTree struct {
	Segs   []moveWireSeg
	Labels []moveWireLabel
	Pins   []string // DESIG:PIN refs the tree joins
	DX, DY float64
}

const moveRigidEps = 0.5

// moveKernelRigidTrees picks the wire trees that move as a whole: every pin the
// tree touches belongs to a part translated (no rotation) by one common offset,
// and no marker component (power/ground flag, netport) sits on it — those keep
// their own preserved-stub path. delta maps a translated designator to its
// offset; parts absent from it stay put or turn.
func moveKernelRigidTrees(segs []moveWireSeg, labels []moveWireLabel, comps []layoutComp, delta map[string][2]float64) []moveRigidTree {
	if len(segs) == 0 {
		return nil
	}
	parent := make([]int, len(segs))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	touches := func(a, b [4]float64) bool {
		return pointOnSeg(a[0], a[1], b[0], b[1], b[2], b[3]) || pointOnSeg(a[2], a[3], b[0], b[1], b[2], b[3]) ||
			pointOnSeg(b[0], b[1], a[0], a[1], a[2], a[3]) || pointOnSeg(b[2], b[3], a[0], a[1], a[2], a[3])
	}
	for i := range segs {
		for j := i + 1; j < len(segs); j++ {
			if segs[i].WireID == segs[j].WireID || touches(segs[i].Seg, segs[j].Seg) {
				parent[find(i)] = find(j)
			}
		}
	}
	groups := map[int][]int{}
	for i := range segs {
		groups[find(i)] = append(groups[find(i)], i)
	}
	onTree := func(idx []int, x, y float64) bool {
		for _, i := range idx {
			s := segs[i].Seg
			if math.Hypot(x-s[0], y-s[1]) <= moveRigidEps || math.Hypot(x-s[2], y-s[3]) <= moveRigidEps ||
				pointOnSeg(x, y, s[0], s[1], s[2], s[3]) {
				return true
			}
		}
		return false
	}
	roots := make([]int, 0, len(groups))
	for r := range groups {
		roots = append(roots, r)
	}
	sort.Ints(roots) // deterministic order
	var out []moveRigidTree
	for _, r := range roots {
		idx := groups[r]
		var pins []string
		var d [2]float64
		ok, haveDelta := true, false
		for _, c := range comps {
			if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
				if c.AnchorAvailable && onTree(idx, c.X, c.Y) {
					ok = false // a marker component: its own preserved path
				}
				continue
			}
			for _, p := range c.Pins {
				if !onTree(idx, p.X, p.Y) {
					continue
				}
				off, moving := delta[strings.ToUpper(c.Designator)]
				if !moving || (haveDelta && (math.Abs(off[0]-d[0]) > moveRigidEps || math.Abs(off[1]-d[1]) > moveRigidEps)) {
					ok = false
				}
				d, haveDelta = off, true
				pins = append(pins, c.Designator+":"+p.Number)
			}
		}
		if !ok || len(pins) == 0 {
			continue
		}
		t := moveRigidTree{Pins: pins, DX: d[0], DY: d[1]}
		wires := map[string]bool{}
		for _, i := range idx {
			t.Segs = append(t.Segs, segs[i])
			wires[segs[i].WireID] = true
		}
		for _, l := range labels {
			if wires[l.WireID] {
				t.Labels = append(t.Labels, l)
			}
		}
		out = append(out, t)
	}
	return out
}

// treeNet is the net a re-laid tree's wires carry: the wires' own net, else
// its labels' name.
func (t moveRigidTree) treeNet() string {
	for _, s := range t.Segs {
		if s.Net != "" {
			return s.Net
		}
	}
	for _, l := range t.Labels {
		if l.Net != "" {
			return l.Net
		}
	}
	return ""
}

// relayRigidTrees re-lays each tree shifted by its offset — every segment as
// its own wire (EasyEDA merges touching wires of one net back into the observed
// primitive), then any of its net labels not already drawn by the wires' net
// name. It returns the pin refs whose tree was re-laid in full; a tree that
// failed part-way leaves its pins to the marker reconnect and the reconcile.
func relayRigidTrees(ops moveKernelOps, trees []moveRigidTree, retryDelay time.Duration) (covered map[string]bool, notes []string) {
	covered = map[string]bool{}
	// The kernel has just read the netlist (merge spot-check), and the host
	// refuses writes for a few seconds after a netlist export (#256): live, the
	// first segment landed and the next two came back "create failed!". A
	// refused create made nothing, and a duplicate of a same-net segment merges
	// into it, so a short paced retry is safe.
	retry := func(f func() error) error {
		err := f()
		for i := 0; err != nil && i < 2; i++ {
			time.Sleep(retryDelay)
			err = f()
		}
		return err
	}
	for _, t := range trees {
		// A wire created with its net prints the name on every segment (live:
		// three XTAL_P labels came back for two). When the tree carries labels,
		// they name the net: lay the wires bare and restore exactly those labels.
		net := t.treeNet()
		if len(t.Labels) > 0 {
			net = ""
		}
		ok := true
		for _, s := range t.Segs {
			seg := [4]float64{s.Seg[0] + t.DX, s.Seg[1] + t.DY, s.Seg[2] + t.DX, s.Seg[3] + t.DY}
			if err := retry(func() error { return ops.createWire(seg, net) }); err != nil {
				notes = append(notes, "rigid re-lay of "+strings.Join(t.Pins, ",")+" stopped: "+err.Error())
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		for _, l := range t.Labels {
			if err := retry(func() error { return ops.ensureNetLabel(l.X+t.DX, l.Y+t.DY, l.Net) }); err != nil {
				notes = append(notes, "net label "+l.Net+" on re-laid tree not restored: "+err.Error())
			}
		}
		for _, p := range t.Pins {
			covered[strings.ToUpper(p)] = true
		}
	}
	return covered, notes
}

// parseMoveWireSnapshot reads components.list(includeWires, includePagePrimitives):
// wire segments with their net, and the visible, non-empty Name attributes
// whose parent is a wire — the native net labels. A component's own Name
// (value "={Value}") has a component parent and is not one.
func parseMoveWireSnapshot(result map[string]any) ([]moveWireSeg, []moveWireLabel, error) {
	if avail, _ := result["wiresAvailable"].(bool); !avail {
		return nil, nil, errMoveWireSnapshot("wires unavailable")
	}
	var segs []moveWireSeg
	wireIDs := map[string]bool{}
	for _, w := range mapsField(result, "wires") {
		id := strField(w, "primitiveId")
		x0, ok0 := w["x0"].(float64)
		y0, ok1 := w["y0"].(float64)
		x1, ok2 := w["x1"].(float64)
		y1, ok3 := w["y1"].(float64)
		if id == "" || !ok0 || !ok1 || !ok2 || !ok3 {
			return nil, nil, errMoveWireSnapshot("wire segment without id or coordinates")
		}
		wireIDs[id] = true
		segs = append(segs, moveWireSeg{WireID: id, Seg: [4]float64{x0, y0, x1, y1}, Net: strField(w, "net")})
	}
	pp, _ := result["pagePrimitives"].(map[string]any)
	if pp == nil {
		return nil, nil, errMoveWireSnapshot("page primitives unavailable")
	}
	var labels []moveWireLabel
	for _, a := range mapsField(pp, "attributes") {
		parent := strField(a, "ParentPrimitiveId")
		value := strField(a, "Value")
		visible, _ := a["ValueVisible"].(bool)
		if strField(a, "Key") != "Name" || !wireIDs[parent] || value == "" || !visible {
			continue
		}
		x, okx := a["X"].(float64)
		y, oky := a["Y"].(float64)
		if !okx || !oky {
			return nil, nil, errMoveWireSnapshot("net label without position")
		}
		labels = append(labels, moveWireLabel{WireID: parent, X: x, Y: y, Net: value})
	}
	return segs, labels, nil
}

type errMoveWireSnapshot string

func (e errMoveWireSnapshot) Error() string { return "wire snapshot: " + string(e) }
