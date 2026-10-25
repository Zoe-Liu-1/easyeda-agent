package app

import (
	"strings"
	"testing"
)

func powerPlanFixture() ([]layoutComp, []schGroupWire) {
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
	return comps, wires
}

func TestPlanPowerClusterReady(t *testing.T) {
	comps, wires := powerPlanFixture()
	plans := planPowerClusters(comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 50}, nil, nil)
	if len(plans) != 1 || plans[0].Status != "ready" ||
		plans[0].KeepFlag != "f1" || len(plans[0].DeleteFlags) != 1 ||
		plans[0].DeleteFlags[0] != "f2" || len(plans[0].Connections) != 1 ||
		plans[0].Connections[0].Status != "clear" ||
		len(plans[0].Connections[0].Path) < 2 || len(plans[0].Order) == 0 {
		t.Fatalf("want one ready power-cluster plan, got %+v", plans)
	}
}

func TestPlanPowerClusterBlocked(t *testing.T) {
	comps, wires := powerPlanFixture()
	comps = append(comps, layoutComp{
		ID: "wall", Designator: "WALL", ComponentType: "part",
		BBox: &layoutBBox{MinX: 40, MinY: -1000, MaxX: 60, MaxY: 1000},
	})
	plans := planPowerClusters(comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 50}, nil, nil)
	if len(plans) != 1 || plans[0].Status != "blocked" ||
		len(plans[0].Connections) != 1 || plans[0].Connections[0].Status != "blocked" ||
		len(plans[0].Connections[0].Blockers) == 0 {
		t.Fatalf("want one blocked power-cluster plan, got %+v", plans)
	}
	for _, step := range plans[0].Order {
		if len(step) >= 6 && step[:6] == "delete" {
			t.Fatalf("blocked plan must not suggest deletion: %+v", plans[0].Order)
		}
	}
}

func TestPlanPowerClusterExempt(t *testing.T) {
	comps, wires := powerPlanFixture()
	plans := planPowerClusters(comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 50}, nil,
		map[string]bool{"+3V3|U1": true})
	if len(plans) != 1 || plans[0].Status != "exempt" || len(plans[0].Connections) != 0 {
		t.Fatalf("want one exempt power-cluster plan, got %+v", plans)
	}
}

func TestPlanPowerClusterPartial(t *testing.T) {
	comps, wires := powerPlanFixture()
	comps = append(comps,
		layoutComp{ID: "c2", Designator: "C2", ComponentType: "part",
			Pins: []layoutPin{{Number: "1", X: 100, Y: 50, Net: "+3V3"}}},
		layoutComp{ID: "f3", ComponentType: "netflag", Net: "+3V3", X: 100, Y: 60, AnchorAvailable: true},
		layoutComp{ID: "wall", Designator: "WALL", ComponentType: "part",
			BBox: &layoutBBox{MinX: 90, MinY: 40, MaxX: 110, MaxY: 60}},
	)
	wires = append(wires, schGroupWire{ID: "w3", Points: []float64{100, 40, 100, 60}})
	plans := planPowerClusters(comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 100}, nil, nil)
	if len(plans) != 1 || plans[0].Status != "partial" || len(plans[0].DeleteFlags) != 1 {
		t.Fatalf("want a partial power-cluster plan with one deletable flag, got %+v", plans)
	}
	var clear, blocked bool
	for _, c := range plans[0].Connections {
		if c.Status == "clear" {
			clear = true
		}
		if c.Status == "blocked" {
			blocked = true
		}
	}
	if !clear || !blocked {
		t.Fatalf("want one clear and one blocked passive, got %+v", plans[0].Connections)
	}
	for _, step := range plans[0].Order {
		if strings.Contains(step, "connect C2.1") && strings.HasSuffix(step, "via ") {
			t.Fatalf("blocked passive must not get an empty connect step: %+v", plans[0].Order)
		}
	}
}

func TestPowerClusterPlaybookOrder(t *testing.T) {
	comps, wires := powerPlanFixture()
	plans := planPowerClusters(comps, wires, nil,
		&layoutBBox{MinX: -50, MinY: -50, MaxX: 200, MaxY: 50}, nil, nil)
	pb, err := powerClusterPlaybook(plans, comps, "project", "doc")
	if err != nil {
		t.Fatalf("compile playbook: %v", err)
	}
	if !pb.RequireFullExecution || len(pb.Steps) < 5 {
		t.Fatalf("want a full guarded playbook, got %+v", pb)
	}
	var wireIdx, connectivityIdx, deleteIdx, saveIdx = -1, -1, -1, -1
	for i, s := range pb.Steps {
		switch s.Action {
		case "schematic.wire.create":
			if wireIdx < 0 {
				wireIdx = i
			}
		case "schematic.components.list":
			if s.ID == "verify-power-connectivity" {
				connectivityIdx = i
			}
		case "schematic.primitives.delete":
			deleteIdx = i
		case "schematic.save":
			saveIdx = i
		}
	}
	if wireIdx < 0 || connectivityIdx < 0 || deleteIdx < 0 || saveIdx < 0 ||
		!((wireIdx < connectivityIdx) && (connectivityIdx < deleteIdx) && (deleteIdx < saveIdx)) {
		t.Fatalf("wrong playbook order: %+v", pb.Steps)
	}
}

func TestPowerRouteBlocksCrossingOnOwnPowerRoot(t *testing.T) {
	find, wireIndex := wireTreeFinder([]schGroupWire{{
		ID: "existing", Net: "+3V3", Points: []float64{5, -10, 5, 10},
	}})
	blockers := powerRouteBlockers(
		[][2]float64{{0, 0}, {10, 0}},
		layoutComp{}, layoutComp{},
		nil,
		map[int]bool{find(wireIndex["existing"]): true},
		map[int]bool{find(wireIndex["existing"]): true},
		nil, nil,
		[]schGroupWire{{ID: "existing", Net: "+3V3", Points: []float64{5, -10, 5, 10}}},
	)
	found := false
	for _, blocker := range blockers {
		if blocker.Kind == "crossing" && blocker.ID == "existing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("crossing on own power root was accepted: %+v", blockers)
	}
}

func TestPowerRouteBlocksFullPinLeadNotOnlyPinPoint(t *testing.T) {
	comps := []layoutComp{{
		ID: "r1", Designator: "R1", ComponentType: "part",
		BBox: &layoutBBox{MinX: 95, MinY: 95, MaxX: 105, MaxY: 105},
		Pins: []layoutPin{{
			Number: "2", X: 100, Y: 90, Net: "SCL", Rotation: 270, RotationKnown: true,
		}},
	}}
	blockers := powerRouteBlockers(
		[][2]float64{{90, 93}, {110, 93}}, layoutComp{}, layoutComp{},
		nil, nil, nil, nil, comps, nil,
	)
	found := false
	for _, blocker := range blockers {
		if blocker.Kind == "pin-lead" && blocker.ID == "r1:2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("route crossing the body-to-pin lead must be blocked, got %+v", blockers)
	}
}
