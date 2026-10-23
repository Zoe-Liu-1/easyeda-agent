package app

import "testing"

func TestLongWireFindingsUsesLongestPhysicalSegment(t *testing.T) {
	wires := []schGroupWire{{
		ID:  "short",
		Net: "SIG",
		ObservedSegments: [][4]float64{
			{0, 0, 100, 0},
		},
	}, {
		ID:  "bent",
		Net: "SIG",
		ObservedSegments: [][4]float64{
			{0, 20, 80, 20},
			{80, 20, 80, 140},
		},
	}}
	got := longWireFindings(wires)
	if len(got) != 1 {
		t.Fatalf("long-wire findings = %+v", got)
	}
	finding := got[0]
	if finding.Type != "long-wire" || finding.Level != "info" ||
		finding.WirePrimitiveId != "bent" || finding.WireLength != 120 ||
		finding.WireSegment[0] != 80 || finding.WireSegment[3] != 140 ||
		finding.At == nil || finding.At.X != 80 || finding.At.Y != 80 {
		t.Fatalf("wrong long-wire finding: %+v", finding)
	}
}

func TestLongWireFindingsDoesNotUsePolylineSpan(t *testing.T) {
	wires := []schGroupWire{{
		ID: "compact-bend",
		ObservedSegments: [][4]float64{
			{0, 0, 60, 0},
			{60, 0, 60, 60},
			{60, 60, 0, 60},
		},
	}}
	if got := longWireFindings(wires); len(got) != 0 {
		t.Fatalf("a bend with 120-unit overall span but 60-unit segments was flagged: %+v", got)
	}
}
