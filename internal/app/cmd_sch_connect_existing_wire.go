package app

import "fmt"

// existingWireAtPoint resolves an explicit label coordinate against the live
// page. When the coordinate is already on a wire, net-label creation must attach
// to that wire and must not create the otherwise automatic 20-unit T-tail.
func existingWireAtPoint(cfg *appConfig, window, doc string, x, y float64, net string) (*schGroupWire, error) {
	wires, err := fetchSchWirePolylinesStable(cfg, window, doc)
	if err != nil {
		return nil, err
	}
	return selectExistingWireAtPoint(wires, x, y, net)
}

func selectExistingWireAtPoint(wires []schGroupWire, x, y float64, net string) (*schGroupWire, error) {
	var matches []schGroupWire
	for _, wire := range wires {
		segments := labelWireSegments(wire)
		hit := false
		for _, seg := range segments {
			if pointOnSegment(x, y, seg[0], seg[1], seg[2], seg[3], 0.5) {
				hit = true
				break
			}
		}
		if hit {
			matches = append(matches, wire)
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}
	var sameNet []schGroupWire
	for _, wire := range matches {
		if wire.Net == net {
			sameNet = append(sameNet, wire)
		}
	}
	if len(sameNet) == 1 {
		return &sameNet[0], nil
	}
	if len(matches) == 1 {
		return &matches[0], nil
	}
	return nil, fmt.Errorf(
		"net_label attach point (%.3f,%.3f) is ambiguous: %d wires touch it and %d match net %q; pass `sch netflag --parent-wire` with an exact wire ID",
		x, y, len(matches), len(sameNet), net)
}
