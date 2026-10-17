package app

import "testing"

// Net labels on a 10-pitch pin column stand in one even row: one stub length
// for every short name, no stagger to a deeper lane, and a left-hand label's
// name — which runs back over its stub — stops short of the pin.
func TestNetLabelsStandInOneEvenRow(t *testing.T) {
	rules := defaultAutoconnectRules()
	if got := candidateOffsets(rules, "net_label", "EN"); len(got) != 1 || got[0] != candidateOffsets(rules, "net_label", "SPICLK")[0] {
		t.Fatalf("short names share one stub length, got %v vs %v", got, candidateOffsets(rules, "net_label", "SPICLK"))
	}
	left := 180.0
	var scene acScene
	rows := []string{"SPID", "SPIQ", "SPICLK", "SPICS0"}
	for i, net := range rows {
		pin := acPin{X: 100, Y: 500 - 10*float64(i), Designator: "U1", PinNumber: net, PinRotation: &left}
		scene.Pins = append(scene.Pins, pin)
	}
	var first acCandidate
	for i, net := range rows {
		pin := scene.Pins[i]
		best := planConnection(pin, "net_label", net, scene, rules, 0)[0]
		if best.Direction != "left" {
			t.Fatalf("%s: label must leave outward, got %+v", net, best)
		}
		if i == 0 {
			first = best
		} else if best.Offset != first.Offset {
			t.Fatalf("%s: offset %v differs from the column's %v — labels must not stagger", net, best.Offset, first.Offset)
		}
		if text := predictedMarkerBBox(best.EndPoint.X, best.EndPoint.Y, "net_label", best.Direction, net); text.MaxX >= pin.X {
			t.Fatalf("%s: the name (to x=%v) must stop short of the pin at x=%v", net, text.MaxX, pin.X)
		}
	}
}
