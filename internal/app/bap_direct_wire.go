package app

import (
	"fmt"
	"io"
	"math"
)

// A block-internal net that joins exactly two pins which FACE each other on one
// line is drawn as a single pin-to-pin wire instead of a pair of same-named
// netports. Live 2026-10-13: the LED block's R.2→LED.+ net rendered as two
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

// bapWireDirectPairs draws the facing internal two-pin nets and returns the
// members it connected (to be removed from the autoconnect list). Any read or
// write failure leaves those members to autoconnect.
func bapWireDirectPairs(cfg *appConfig, window string, nets []bapNet, stderr io.Writer) map[string]bool {
	done := map[string]bool{}
	var cands []bapNet
	for _, n := range nets {
		if n.Port == "" && len(n.Members) == 2 {
			cands = append(cands, n)
		}
	}
	if len(cands) == 0 {
		return done
	}
	res, err := requestAction(cfg, "schematic.components.list", window, map[string]any{"includeBBox": true, "includePins": true, "includePinNets": false})
	if err != nil {
		fmt.Fprintf(stderr, "direct wiring skipped (geometry unavailable: %v) — autoconnect wires every net\n", err)
		return done
	}
	scene := buildScene(res.Result)
	comps, err := parseLayoutComps(res.Result)
	if err != nil {
		return done
	}
	for _, n := range cands {
		a, aerr := resolvePinCoord(scene, n.Members[0])
		b, berr := resolvePinCoord(scene, n.Members[1])
		if aerr != nil || berr != nil || a.Designator == b.Designator || !bapFacingPair(a, b) ||
			!bapDirectPathClear(a, b, comps, scene.Pins, map[string]bool{a.Designator: true, b.Designator: true}) {
			continue
		}
		_, werr := requestAction(cfg, "schematic.wire.create", window, map[string]any{
			"points": []float64{a.X, a.Y, b.X, b.Y}, "net": n.Net,
		})
		if werr != nil {
			fmt.Fprintf(stderr, "direct ✗ %s — %s (%s): %v — falling back to autoconnect\n", n.Members[0], n.Members[1], n.Net, werr)
			continue
		}
		fmt.Fprintf(stderr, "direct ✓ %s — %s wired pin-to-pin as %s\n", n.Members[0], n.Members[1], n.Net)
		done[n.Members[0]], done[n.Members[1]] = true, true
	}
	return done
}
