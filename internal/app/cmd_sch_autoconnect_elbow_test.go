package app

import "testing"

// Live 2026-10-16 (tile layout trial): a vertical 10k pull-up's top pin could not
// take its EN netport — sideways is a pin-exit hard reject and straight up stands
// the label folded. The drafting convention (BOOT0 above its resistor) is a short
// wire up, then a horizontal label: the planner must offer and prefer that elbow.
func TestPlanConnectionElbowsNetportOffVerticalPin(t *testing.T) {
	rot := 90.0
	owner := layoutBBox{MinX: 995, MinY: 700, MaxX: 1005, MaxY: 740}
	pin := acPin{X: 1000, Y: 740, Designator: "R1", PinNumber: "1", PinRotation: &rot, OwnerBBox: &owner}
	scene := acScene{Parts: []layoutBBox{owner}, Pins: []acPin{pin, {X: 1000, Y: 700, Designator: "R1", PinNumber: "2"}}}
	all := planConnection(pin, "net_port_bi", "EN", scene, defaultAutoconnectRules(), 0)
	best := all[0]
	if best.Via == nil || (best.Direction != "left" && best.Direction != "right") {
		t.Fatalf("best candidate must be an elbow with a horizontal label, got %+v", best)
	}
	if best.Via.X != 1000 || best.Via.Y <= 740 {
		t.Fatalf("elbow corner must sit straight above the pin, got %+v", *best.Via)
	}
	if best.EndPoint.Y != best.Via.Y {
		t.Fatalf("label stub must leave the corner horizontally: via %+v end %+v", *best.Via, best.EndPoint)
	}
	if candidateHardRejected(best) || best.Score >= acScoreWarnThreshold {
		t.Fatalf("a clear elbow must score clean, got %+v", best)
	}
}

// The elbow's vertical leg is wiring like any stub: one that would end on a
// foreign pin is a hard reject, so the planner rises past it or turns elsewhere.
func TestElbowLegCannotEndOnForeignPin(t *testing.T) {
	rot := 90.0
	pin := acPin{X: 1000, Y: 740, Designator: "R1", PinNumber: "1", PinRotation: &rot}
	foreign := acPin{X: 1000, Y: 750, Designator: "C1", PinNumber: "2"}
	scene := acScene{Pins: []acPin{pin, foreign}}
	for _, c := range elbowCandidates(pin, "net_port_bi", "EN", scene, defaultAutoconnectRules()) {
		if c.Via.Y >= 750 && !candidateHardRejected(c) {
			t.Fatalf("a leg ending on or crossing C1.2 must be rejected: %+v", c)
		}
	}
}

// Ground/power markers are near-square and read fine vertically: no elbows.
func TestElbowOnlyForNetports(t *testing.T) {
	rot := 90.0
	pin := acPin{X: 0, Y: 0, PinRotation: &rot}
	if got := elbowCandidates(pin, "power", "3V3", acScene{}, defaultAutoconnectRules()); got != nil {
		t.Fatalf("power markers must not elbow, got %d candidates", len(got))
	}
	h := 0.0
	pin.PinRotation = &h
	if got := elbowCandidates(pin, "net_port_bi", "EN", acScene{}, defaultAutoconnectRules()); got != nil {
		t.Fatalf("a horizontal pin's netport already reads horizontally, got %d elbows", len(got))
	}
}

// Live 2026-10-16: C6 (EN) sat between R1 and R2. R2's IO0 elbow already ran up
// to y=750 and its port pointed left toward C6; C6's EN elbow then went right at
// the same height — its stub ran under the IO0 port and its label lay on R2's
// leg, while no two label boxes overlapped. Both are marker-on-connection
// overlaps and must be steered away from when a clean exit exists.
func TestPlanConnectionAvoidsStubThroughMarkerAndLabelOnWire(t *testing.T) {
	rot := 90.0
	owner := layoutBBox{MinX: 1035, MinY: 700, MaxX: 1045, MaxY: 740}
	pin := acPin{X: 1040, Y: 740, Designator: "C6", PinNumber: "1", PinRotation: &rot, OwnerBBox: &owner}
	// R2's IO0 port body beside C6's column (not over it: then no clean exit
	// exists at all and the overlap can only be reported, not avoided).
	io0 := layoutBBox{MinX: 1055, MinY: 744.5, MaxX: 1086, MaxY: 755.5}
	scene := acScene{
		Parts: []layoutBBox{owner},
		Pins:  []acPin{pin},
		Flags: []layoutBBox{io0},
		Wires: []wireSegment{{X0: 1100, Y0: 740, X1: 1100, Y1: 750, Net: "IO0"}, {X0: 1100, Y0: 750, X1: 1080, Y1: 750, Net: "IO0"}},
	}
	best := planConnection(pin, "net_port_bi", "EN", scene, defaultAutoconnectRules(), 0)[0]
	for _, r := range best.Reasons {
		if r.Cost >= costFlagCollision {
			t.Fatalf("best candidate still overlaps a marker or wire: %+v", best)
		}
	}
	from := acPoint{X: pin.X, Y: pin.Y}
	if best.Via != nil {
		from = *best.Via
	}
	if segHitsRect(from.X, from.Y, best.EndPoint.X, best.EndPoint.Y, io0) {
		t.Fatalf("stub must not run through R2's IO0 port: %+v", best)
	}
}

// R2's IO0 went left over C6's head because nothing kept another pin's exit
// free; with C6.1 pointing up beside it, R2 must turn the other way.
func TestPlanConnectionKeepsNeighbourPinExitFree(t *testing.T) {
	up := 90.0
	r2 := acPin{X: 1100, Y: 740, Designator: "R2", PinNumber: "1", PinRotation: &up}
	c6 := acPin{X: 1060, Y: 740, Designator: "C6", PinNumber: "1", PinRotation: &up}
	// Live, R2 sat at the right sheet edge: a right-hand label left the sheet.
	usable := layoutBBox{MinX: 0, MinY: 0, MaxX: 1150, MaxY: 900}
	scene := acScene{Pins: []acPin{r2, c6}, Usable: &usable}
	// C6 stands only 40 away: every left-hand label lies over its exit, so the
	// honest outcome is a folded label (a WARN), never one blocking C6.
	best := planConnection(r2, "net_port_bi", "IO0", scene, defaultAutoconnectRules(), 0)[0]
	ex, ey := endpointFor(c6.X, c6.Y, 10, "up")
	lbl := predictedMarkerBBox(best.EndPoint.X, best.EndPoint.Y, "net_port_bi", best.Direction, "IO0")
	from := acPoint{X: r2.X, Y: r2.Y}
	if best.Via != nil {
		from = *best.Via
	}
	if segHitsRect(c6.X, c6.Y, ex, ey, lbl) || segmentsTouch(from.X, from.Y, best.EndPoint.X, best.EndPoint.Y, c6.X, c6.Y, ex, ey) {
		t.Fatalf("IO0 label must not sit in front of C6.1: %+v", best)
	}
}

// Live 2026-10-16 (second trial): C6's EN stub stopped one grid short of R2's
// elbow leg, and the netport's own lead (stub end → body) lay across that leg.
// The lead belongs to the marker: a wire under it is a crossing.
func TestMarkerLeadOverForeignWireIsPenalised(t *testing.T) {
	rot := 0.0
	pin := acPin{X: 1040, Y: 760, PinRotation: &rot}
	// R2's vertical leg at x=1070, inside the lead of a right label ending at 1065.
	scene := acScene{Wires: []wireSegment{{X0: 1070, Y0: 740, X1: 1070, Y1: 780, Net: "IO0"}}}
	c := scoreCandidate(pin, "right", 24, "net_port_bi", "EN", scene, defaultAutoconnectRules())
	if c.EndPoint.X != 1065 {
		t.Fatalf("setup: end %+v", c.EndPoint)
	}
	hit := false
	for _, r := range c.Reasons {
		hit = hit || r.Desc == "label lies on an existing wire"
	}
	if !hit {
		t.Fatalf("a wire under the marker lead must count as label-on-wire: %+v", c)
	}
}

// Next live run: C6's EN elbow ran right at y=760, over R2's head, and R2's IO0
// then had no leg that avoided that foreign wire. A stub across another pin's
// exit is as blocking as a label there.
func TestStubAcrossNeighbourPinExitIsPenalised(t *testing.T) {
	up := 90.0
	r2 := acPin{X: 1060, Y: 740, Designator: "R2", PinNumber: "1", PinRotation: &up}
	corner := acPin{X: 1040, Y: 760, PinRotation: new(float64)}
	c := scoreCandidate(corner, "right", 60, "net_port_bi", "EN", acScene{Pins: []acPin{r2}}, defaultAutoconnectRules())
	hit := false
	for _, r := range c.Reasons {
		hit = hit || r.Desc == "stub or label blocks another pin's exit"
	}
	if !hit {
		t.Fatalf("a stub over R2.1's exit must be penalised: %+v", c)
	}
}
