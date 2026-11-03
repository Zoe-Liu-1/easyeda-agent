package app

import "testing"

func TestSelectExistingWireAtPointPrefersRequestedNet(t *testing.T) {
	wires := []schGroupWire{
		{ID: "foreign", Net: "GND", ObservedSegments: [][4]float64{{0, 0, 100, 0}}},
		{ID: "signal", Net: "FB1", ObservedSegments: [][4]float64{{0, 0, 100, 0}}},
	}
	got, err := selectExistingWireAtPoint(wires, 50, 0, "FB1")
	if err != nil || got == nil || got.ID != "signal" {
		t.Fatalf("expected signal parent, got wire=%+v err=%v", got, err)
	}
}

func TestSelectExistingWireAtPointRejectsAmbiguousDifferentNets(t *testing.T) {
	wires := []schGroupWire{
		{ID: "a", Net: "A", ObservedSegments: [][4]float64{{0, 0, 100, 0}}},
		{ID: "b", Net: "B", ObservedSegments: [][4]float64{{0, 0, 100, 0}}},
	}
	if got, err := selectExistingWireAtPoint(wires, 50, 0, "C"); err == nil || got != nil {
		t.Fatalf("expected ambiguity rejection, got wire=%+v err=%v", got, err)
	}
}

func TestSelectExistingWireAtPointReturnsNilWhenOffWire(t *testing.T) {
	wires := []schGroupWire{{ID: "a", Net: "A", ObservedSegments: [][4]float64{{0, 0, 100, 0}}}}
	if got, err := selectExistingWireAtPoint(wires, 50, 20, "A"); err != nil || got != nil {
		t.Fatalf("off-wire point must keep normal pin-stub path, got wire=%+v err=%v", got, err)
	}
}
