package app

import "math"

// A block-internal net that joins exactly two pins which FACE each other on one
// line is drawn as a single pin-to-pin wire instead of a pair of same-named
// netports (planned by bapPlanBlockWires, bap_block_wire.go). Live 2026-10-13: the LED block's R.2→LED.+ net rendered as two
// LED1_N2 ports, one landing next to an unrelated header. The wire carries the
// planned net name, so reconcile (which matches by name) is unchanged; any pair
// that is not provably facing and clear stays with autoconnect.

// bapFacingPair reports whether a and b sit on one horizontal or vertical line
// with outward directions pointing at each other (a's side toward b and b's
// side toward a), so a straight wire leaves both pins outward.
func bapFacingPair(a, b acPin) bool {
	if a.PinRotation == nil || b.PinRotation == nil {
		return false // only measured pin directions, never the bbox fallback
	}
	da, db := outwardDirection(a), outwardDirection(b)
	const eps = 0.5
	switch {
	case math.Abs(a.Y-b.Y) <= eps && a.X < b.X:
		return da == "right" && db == "left"
	case math.Abs(a.Y-b.Y) <= eps && a.X > b.X:
		return da == "left" && db == "right"
	case math.Abs(a.X-b.X) <= eps && a.Y < b.Y:
		return da == "up" && db == "down"
	case math.Abs(a.X-b.X) <= eps && a.Y > b.Y:
		return da == "down" && db == "up"
	}
	return false
}

// bapDirectPathClear reports whether the segment a→b touches no other part
// body and no other pin. owners are the two parts the wire joins.
func bapDirectPathClear(a, b acPin, comps []layoutComp, pins []acPin, owners map[string]bool) bool {
	for _, c := range comps {
		if c.BBox == nil || owners[c.Designator] || c.ComponentType == "sheet" {
			continue
		}
		if schSegmentCrossesBox(a.X, a.Y, b.X, b.Y, *c.BBox) {
			return false
		}
	}
	for _, p := range pins {
		if (p.Designator == a.Designator && p.PinNumber == a.PinNumber) || (p.Designator == b.Designator && p.PinNumber == b.PinNumber) {
			continue
		}
		if onSegmentPoint(p.X, p.Y, a.X, a.Y, b.X, b.Y) {
			return false
		}
	}
	return true
}

func onSegmentPoint(px, py, x0, y0, x1, y1 float64) bool {
	const eps = 0.5
	if px < math.Min(x0, x1)-eps || px > math.Max(x0, x1)+eps || py < math.Min(y0, y1)-eps || py > math.Max(y0, y1)+eps {
		return false
	}
	return math.Abs((x1-x0)*(py-y0)-(y1-y0)*(px-x0)) <= eps*math.Hypot(x1-x0, y1-y0)
}
