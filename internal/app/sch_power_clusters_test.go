package app

import "testing"

func TestPowerClusterCandidate(t *testing.T) {
	comps := []layoutComp{
		{
			ID: "u1", Designator: "U1", ComponentType: "part",
			Pins: []layoutPin{{Number: "1", X: 0, Y: 0, Net: "+3V3"}},
		},
		{
			ID: "c1", Designator: "C1", ComponentType: "part",
			Pins: []layoutPin{{Number: "1", X: 100, Y: 0, Net: "+3V3"}},
		},
		{ID: "f1", ComponentType: "netflag", Net: "+3V3", X: 0, Y: 10, AnchorAvailable: true},
		{ID: "f2", ComponentType: "netflag", Net: "+3V3", X: 100, Y: 10, AnchorAvailable: true},
	}
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, -10, 0, 10}},
		{ID: "w2", Points: []float64{100, -10, 100, 10}},
	}
	got := powerClusterFindings(comps, wires, nil)
	if len(got) != 1 || got[0].Type != "power-cluster-candidate" ||
		got[0].Level != "warn" || got[0].Designator != "U1" ||
		got[0].MarkerNet != "+3V3" || len(got[0].SuggestDeleteIds) != 1 {
		t.Fatalf("want one +3V3 cluster candidate, got %+v", got)
	}
}

func TestPowerClusterAlreadyWired(t *testing.T) {
	comps := []layoutComp{
		{
			ID: "u1", Designator: "U1", ComponentType: "part",
			Pins: []layoutPin{{Number: "1", X: 0, Y: 0, Net: "+3V3"}},
		},
		{
			ID: "c1", Designator: "C1", ComponentType: "part",
			Pins: []layoutPin{{Number: "1", X: 100, Y: 0, Net: "+3V3"}},
		},
		{ID: "f1", ComponentType: "netflag", Net: "+3V3", X: 0, Y: 10, AnchorAvailable: true},
	}
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, -10, 0, 10}},
		{ID: "w2", Points: []float64{100, -10, 100, 10}},
		{ID: "w3", Points: []float64{0, 0, 100, 0}},
	}
	if got := powerClusterFindings(comps, wires, nil); len(got) != 0 {
		t.Fatalf("a physically wired cluster must not be flagged: %+v", got)
	}
}

func TestPowerClusterRemoteAndCoincidentGroundStaySilent(t *testing.T) {
	comps := []layoutComp{
		{
			ID: "u1", Designator: "U1", ComponentType: "part",
			Pins: []layoutPin{
				{Number: "1", X: 0, Y: 0, Net: "+3V3"},
				{Number: "2", X: 0, Y: -100, Net: "GND"},
			},
		},
		{
			ID: "c1", Designator: "C1", ComponentType: "part",
			Pins: []layoutPin{
				{Number: "1", X: 300, Y: 0, Net: "+3V3"},
				{Number: "2", X: 0, Y: -100, Net: "GND"},
			},
		},
		{ID: "f1", ComponentType: "netflag", Net: "+3V3", X: 0, Y: 10, AnchorAvailable: true},
		{ID: "f2", ComponentType: "netflag", Net: "+3V3", X: 300, Y: 10, AnchorAvailable: true},
		{ID: "g1", ComponentType: "netflag", Net: "GND", X: 0, Y: -90, AnchorAvailable: true},
		{ID: "g2", ComponentType: "netflag", Net: "GND", X: 0, Y: -110, AnchorAvailable: true},
	}
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, -10, 0, 10}},
		{ID: "w2", Points: []float64{300, -10, 300, 10}},
		{ID: "w3", Points: []float64{0, -110, 0, -90}},
	}
	if got := powerClusterFindings(comps, wires, nil); len(got) != 0 {
		t.Fatalf("remote passive and local ground must not produce a warn candidate: %+v", got)
	}
}

func TestPowerClusterGroundIsAdvisory(t *testing.T) {
	comps := []layoutComp{
		{
			ID: "u1", Designator: "U1", ComponentType: "part",
			Pins: []layoutPin{{Number: "1", X: 0, Y: 0, Net: "GND"}},
		},
		{
			ID: "c1", Designator: "C1", ComponentType: "part",
			Pins: []layoutPin{{Number: "1", X: 100, Y: 0, Net: "GND"}},
		},
		{ID: "g1", ComponentType: "netflag", Net: "GND", X: 0, Y: 10, AnchorAvailable: true},
		{ID: "g2", ComponentType: "netflag", Net: "GND", X: 100, Y: 10, AnchorAvailable: true},
	}
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, -10, 0, 10}},
		{ID: "w2", Points: []float64{100, -10, 100, 10}},
	}
	got := powerClusterFindings(comps, wires, nil)
	if len(got) != 1 || got[0].Type != "power-cluster-candidate" ||
		got[0].Level != "info" || got[0].MarkerNet != "GND" {
		t.Fatalf("want one advisory GND cluster, got %+v", got)
	}
}

func TestPowerClusterOnlyJudgesPassiveAgainstItsNearestConsumerPin(t *testing.T) {
	comps := []layoutComp{
		{
			ID: "u1", Designator: "U1", ComponentType: "part",
			Pins: []layoutPin{
				{Number: "1", X: 0, Y: 0, Net: "GND"},
				{Number: "2", X: 0, Y: 200, Net: "GND"},
			},
		},
		{
			ID: "c1", Designator: "C1", ComponentType: "part",
			Pins: []layoutPin{{Number: "1", X: 100, Y: 0, Net: "GND"}},
		},
		{ID: "g1", ComponentType: "netflag", Net: "GND", X: 0, Y: 10, AnchorAvailable: true},
		{ID: "g2", ComponentType: "netflag", Net: "GND", X: 0, Y: 210, AnchorAvailable: true},
		{ID: "g3", ComponentType: "netflag", Net: "GND", X: 100, Y: 10, AnchorAvailable: true},
	}
	wires := []schGroupWire{
		{ID: "u-pin1", Points: []float64{0, -10, 0, 10}},
		{ID: "u-pin2", Points: []float64{0, 190, 0, 210}},
		{ID: "c1", Points: []float64{100, -10, 100, 10}},
		{ID: "consumer-local", Points: []float64{0, 0, 100, 0}},
	}
	if got := powerClusterFindings(comps, wires, nil); len(got) != 0 {
		t.Fatalf("a passive connected to its nearest consumer pin is locally complete; other GND pins are separate local islands: %+v", got)
	}
}

func TestPowerClusterExempt(t *testing.T) {
	comps := []layoutComp{
		{ID: "u1", Designator: "U1", ComponentType: "part",
			Pins: []layoutPin{{Number: "1", X: 0, Y: 0, Net: "+3V3"}}},
		{ID: "c1", Designator: "C1", ComponentType: "part",
			Pins: []layoutPin{{Number: "1", X: 100, Y: 0, Net: "+3V3"}}},
		{ID: "f1", ComponentType: "netflag", Net: "+3V3", X: 0, Y: 10, AnchorAvailable: true},
		{ID: "f2", ComponentType: "netflag", Net: "+3V3", X: 100, Y: 10, AnchorAvailable: true},
	}
	wires := []schGroupWire{
		{ID: "w1", Points: []float64{0, -10, 0, 10}},
		{ID: "w2", Points: []float64{100, -10, 100, 10}},
	}
	got := powerClusterFindings(comps, wires, map[string]bool{"+3V3|U1": true})
	if len(got) != 1 || got[0].Type != "power-cluster-exempt" || got[0].Level != "info" {
		t.Fatalf("want an explicit INFO exemption, got %+v", got)
	}
}
