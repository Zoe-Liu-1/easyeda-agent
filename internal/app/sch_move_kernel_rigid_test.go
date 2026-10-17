package app

import "testing"

// Live 2026-10-17, crystal tile: X1.1 (570,490) and C7.1 (550,475) are joined
// by one branched wire carrying XTAL_P — trunk, drop and the label stub ending
// at (510,490) where the net label sits. Moving the tile through the kernel
// turned the pin-to-pin wire into two labels and left C7 floating.
func crystalScene() ([]moveWireSeg, []moveWireLabel, []layoutComp) {
	segs := []moveWireSeg{
		{WireID: "w1", Seg: [4]float64{550, 490, 570, 490}, Net: "XTAL_P"},
		{WireID: "w1", Seg: [4]float64{550, 475, 550, 490}, Net: "XTAL_P"},
		{WireID: "w1", Seg: [4]float64{510, 490, 550, 490}, Net: "XTAL_P"},
		{WireID: "g1", Seg: [4]float64{550, 425, 550, 445}, Net: ""}, // C7.2 → GND flag stub
	}
	labels := []moveWireLabel{{WireID: "w1", X: 510, Y: 490, Net: "XTAL_P"}}
	comps := []layoutComp{
		{Designator: "X1", ComponentType: "part", Pins: []layoutPin{{Number: "1", X: 570, Y: 490}}},
		{Designator: "C7", ComponentType: "part", Pins: []layoutPin{{Number: "1", X: 550, Y: 475}, {Number: "2", X: 550, Y: 445}}},
		{ID: "gnd", Designator: "", ComponentType: "netflag", Net: "GND", X: 550, Y: 425, AnchorAvailable: true},
	}
	return segs, labels, comps
}

func TestRigidTreesPickTheTreeMovingAsAWhole(t *testing.T) {
	segs, labels, comps := crystalScene()
	trees := moveKernelRigidTrees(segs, labels, comps, map[string][2]float64{"X1": {20, 30}, "C7": {20, 30}})
	if len(trees) != 1 {
		t.Fatalf("only the XTAL_P tree moves as a whole (the GND stub carries a flag), got %d: %+v", len(trees), trees)
	}
	tr := trees[0]
	if len(tr.Segs) != 3 || len(tr.Labels) != 1 || len(tr.Pins) != 2 || tr.DX != 20 || tr.DY != 30 {
		t.Fatalf("tree: %+v", tr)
	}
	// One end staying put, or two different offsets, is not a rigid move.
	if got := moveKernelRigidTrees(segs, labels, comps, map[string][2]float64{"X1": {20, 30}}); len(got) != 0 {
		t.Fatalf("a tree touching a part that stays put must reconnect through markers: %+v", got)
	}
	if got := moveKernelRigidTrees(segs, labels, comps, map[string][2]float64{"X1": {20, 30}, "C7": {0, 30}}); len(got) != 0 {
		t.Fatalf("a tree whose ends move differently must reconnect through markers: %+v", got)
	}
}

func TestRelayRigidTreesTranslatesWiresAndLabels(t *testing.T) {
	segs, labels, comps := crystalScene()
	trees := moveKernelRigidTrees(segs, labels, comps, map[string][2]float64{"X1": {20, 30}, "C7": {20, 30}})
	f := &fakeMoveOps{}
	covered, notes := relayRigidTrees(f, trees, 0)
	if len(notes) != 0 {
		t.Fatalf("notes: %v", notes)
	}
	// Labelled tree: bare wires, the recorded label names the net (a wire created
	// with its net prints the name on every segment).
	if len(f.createdSeg) != 3 || f.createdSeg[0].Seg != [4]float64{570, 520, 590, 520} || f.createdSeg[0].Net != "" {
		t.Fatalf("segments must be re-laid at the same offset, bare: %+v", f.createdSeg)
	}
	if len(f.labelsMade) != 1 || f.labelsMade[0] != (moveWireLabel{X: 530, Y: 520, Net: "XTAL_P"}) {
		t.Fatalf("the net label must follow: %+v", f.labelsMade)
	}
	if !covered["X1:1"] || !covered["C7:1"] || covered["C7:2"] {
		t.Fatalf("re-laid pins skip the marker reconnect; the GND pin keeps its path: %v", covered)
	}
}

func TestParseMoveWireSnapshotKeepsOnlyWireNetLabels(t *testing.T) {
	res := map[string]any{
		"wiresAvailable": true,
		"wires":          []any{map[string]any{"primitiveId": "w1", "x0": 550.0, "y0": 490.0, "x1": 570.0, "y1": 490.0, "net": "XTAL_P"}},
		"pagePrimitives": map[string]any{"attributes": []any{
			map[string]any{"Key": "Name", "ParentPrimitiveId": "w1", "Value": "XTAL_P", "ValueVisible": true, "X": 510.0, "Y": 490.0},
			map[string]any{"Key": "Name", "ParentPrimitiveId": "w1", "Value": "", "ValueVisible": true, "X": 1.0, "Y": 1.0},
			map[string]any{"Key": "Name", "ParentPrimitiveId": "part1", "Value": "={Value}", "ValueVisible": true, "X": 2.0, "Y": 2.0},
		}},
	}
	segs, labels, err := parseMoveWireSnapshot(res)
	if err != nil || len(segs) != 1 || segs[0].Net != "XTAL_P" {
		t.Fatalf("segs %+v err %v", segs, err)
	}
	if len(labels) != 1 || labels[0] != (moveWireLabel{WireID: "w1", X: 510, Y: 490, Net: "XTAL_P"}) {
		t.Fatalf("only the visible named label on a wire is a net label: %+v", labels)
	}
}

// Live: the first re-laid segment landed, the next came back "create failed!"
// inside the host's post-netlist write window. A paced retry finishes the tree.
func TestRelayRigidTreesRetriesARefusedWrite(t *testing.T) {
	segs, labels, comps := crystalScene()
	trees := moveKernelRigidTrees(segs, labels, comps, map[string][2]float64{"X1": {20, 30}, "C7": {20, 30}})
	f := &fakeMoveOps{refuseWire: 2}
	covered, notes := relayRigidTrees(f, trees, 0)
	if len(notes) != 0 || len(f.createdSeg) != 3 || !covered["C7:1"] {
		t.Fatalf("a refused write is retried: notes %v segs %d covered %v", notes, len(f.createdSeg), covered)
	}
}

// A tree with no label keeps its name on the wire itself.
func TestRelayRigidTreesKeepsNetOnUnlabelledTree(t *testing.T) {
	segs, _, comps := crystalScene()
	trees := moveKernelRigidTrees(segs, nil, comps, map[string][2]float64{"X1": {20, 30}, "C7": {20, 30}})
	f := &fakeMoveOps{}
	relayRigidTrees(f, trees, 0)
	if len(f.createdSeg) == 0 || f.createdSeg[0].Net != "XTAL_P" || len(f.labelsMade) != 0 {
		t.Fatalf("unlabelled tree re-laid with its net: %+v labels %+v", f.createdSeg, f.labelsMade)
	}
}
