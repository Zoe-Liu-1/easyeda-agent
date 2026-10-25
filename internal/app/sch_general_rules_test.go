package app

import "testing"

func TestLabelDistanceFindings(t *testing.T) {
	wires := []schGroupWire{{ID: "w1", Points: []float64{0, 0, 100, 0}}}
	closeLabel := visibleTextItem{
		ID: "near", Kind: "attribute", ComponentType: "wire", ParentID: "w1",
		Key: "Name", Value: "SIG", BBox: &layoutBBox{MinX: 10, MinY: 2, MaxX: 20, MaxY: 10},
	}
	farLabel := closeLabel
	farLabel.ID = "far"
	farLabel.BBox = &layoutBBox{MinX: 10, MinY: 100, MaxX: 20, MaxY: 108}
	if got := labelDistanceFindings([]visibleTextItem{closeLabel}, nil, wires, 50); len(got) != 0 {
		t.Fatalf("near label must not be flagged: %+v", got)
	}
	got := labelDistanceFindings([]visibleTextItem{farLabel}, nil, wires, 50)
	if len(got) != 1 || got[0].Type != "label-distance" {
		t.Fatalf("far label must be flagged: %+v", got)
	}

	marker := layoutComp{
		ID: "m1", ComponentType: "netflag", Net: "GND", X: 5, Y: 5,
		BBox: &layoutBBox{MinX: 0, MinY: 0, MaxX: 10, MaxY: 10},
	}
	markerText := visibleTextItem{
		ID: "marker-text", Kind: "attribute", ComponentType: "netflag", ParentID: "m1",
		Key: "Name", Value: "GND", BBox: &layoutBBox{MinX: 100, MinY: 100, MaxX: 112, MaxY: 108},
	}
	got = labelDistanceFindings([]visibleTextItem{markerText}, []layoutComp{marker}, nil, 50)
	if len(got) != 1 || !stringsContains(got[0].Message, "电源/地符号") {
		t.Fatalf("detached marker name must be flagged: %+v", got)
	}
}

func TestCrossKindLabelEconomyFindings(t *testing.T) {
	wires := []schGroupWire{{ID: "w1", Points: []float64{0, 0, 100, 0}}}
	label := visibleTextItem{
		ID: "lbl", Kind: "attribute", ComponentType: "wire", ParentID: "w1",
		Key: "Name", Value: "GND", BBox: &layoutBBox{MinX: 10, MinY: 5, MaxX: 20, MaxY: 15},
	}
	flagName := visibleTextItem{
		ID: "f1-name", Kind: "attribute", ComponentType: "netflag", ParentID: "f1",
		Key: "Name", Value: "GND", BBox: &layoutBBox{MinX: 45, MinY: 5, MaxX: 55, MaxY: 15},
	}
	flag := layoutComp{
		ID: "f1", ComponentType: "netflag", Net: "GND", X: 50, Y: 0, AnchorAvailable: true,
	}
	got := crossKindLabelEconomyFindings([]visibleTextItem{label, flagName}, []layoutComp{flag}, wires)
	if len(got) != 1 || got[0].Type != "cross-kind-label-economy" ||
		got[0].SuggestKeepId != "" || len(got[0].SuggestDeleteIds) != 0 {
		t.Fatalf("without provenance cross-kind must report only, got %+v", got)
	}
	got = crossKindLabelEconomyFindingsWithPreferred(
		[]visibleTextItem{label, flagName}, []layoutComp{flag}, wires, map[string]bool{"f1": true})
	if len(got) != 1 || got[0].SuggestKeepId != "f1" || len(got[0].SuggestDeleteIds) != 1 ||
		got[0].SuggestDeleteIds[0] != "lbl" {
		t.Fatalf("want baseline-preferred GND marker, got %+v", got)
	}

	// A hidden marker name cannot justify deleting the visible wire label.
	got = crossKindLabelEconomyFindings([]visibleTextItem{label}, []layoutComp{flag}, wires)
	if len(got) != 1 || got[0].SuggestKeepId != "" || len(got[0].SuggestDeleteIds) != 0 ||
		!stringsContains(got[0].Message, "当前不给删除建议") {
		t.Fatalf("hidden retained representation must warn without delete suggestions, got %+v", got)
	}

	flag.Net = "SIG"
	label.Value = "SIG"
	flagName.Value = "SIG"
	got = crossKindLabelEconomyFindings([]visibleTextItem{label}, []layoutComp{flag}, wires)
	if len(got) != 1 || got[0].SuggestKeepId != "" || len(got[0].SuggestDeleteIds) != 0 {
		t.Fatalf("without provenance signal cross-kind must report only, got %+v", got)
	}
	got = crossKindLabelEconomyFindingsWithPreferred(
		[]visibleTextItem{label}, []layoutComp{flag}, wires, map[string]bool{"lbl": true})
	if len(got) != 1 || got[0].SuggestKeepId != "lbl" ||
		len(got[0].SuggestDeleteIds) != 1 || got[0].SuggestDeleteIds[0] != "f1" {
		t.Fatalf("want baseline-preferred signal label, got %+v", got)
	}

	hiddenLabel := label
	hidden := false
	hiddenLabel.Visible = &hidden
	got = crossKindLabelEconomyFindings([]visibleTextItem{hiddenLabel}, []layoutComp{flag}, wires)
	if len(got) != 1 || got[0].SuggestKeepId != "" || len(got[0].SuggestDeleteIds) != 0 ||
		!stringsContains(got[0].Message, "当前不给删除建议") {
		t.Fatalf("hidden signal label must warn without deleting the marker, got %+v", got)
	}
}

func TestSignalMarkerTypeFindings(t *testing.T) {
	comps := []layoutComp{
		{ID: "f1", ComponentType: "netflag", Net: "SIG", X: 0, Y: 0},
		{ID: "p1", ComponentType: "netport", Net: "SIG", X: 0, Y: 0},
		{ID: "f2", ComponentType: "netflag", Net: "+3V3", X: 0, Y: 0},
		{ID: "f3", ComponentType: "netflag", Net: "VOUT1", X: 0, Y: 0},
		{ID: "f4", ComponentType: "netflag", Net: "VLOAD1", X: 0, Y: 0},
		{ID: "f5", ComponentType: "netflag", Net: "DC55", X: 0, Y: 0},
		{ID: "l1", ComponentType: "netlabel", Net: "SIG", X: 0, Y: 0},
	}
	got := signalMarkerTypeFindings(comps, nil)
	if len(got) != 4 || got[0].Type != "signal-netflag-type" || got[0].Level != "warn" ||
		got[1].Type != "signal-netflag-type" || got[1].Level != "warn" ||
		got[2].Type != "signal-netflag-type" || got[2].Level != "warn" ||
		got[3].Type != "signal-netport-type" || got[3].Level != "warn" {
		t.Fatalf("wrong signal marker classification: %+v", got)
	}
	exempt := signalMarkerTypeFindings(comps, map[string]bool{"p1": true})
	if len(exempt) != 4 || exempt[3].Type != "signal-port-exempt" || exempt[3].Level != "info" {
		t.Fatalf("module-boundary exception must remain visible as INFO: %+v", exempt)
	}
}

func TestVisibleTextTitleblockAndPin(t *testing.T) {
	sheet := layoutComp{ID: "sheet", ComponentType: "sheet"}
	titleBlock := &layoutBBox{MinX: 90, MinY: 90, MaxX: 110, MaxY: 110}
	items := []visibleTextItem{
		{ID: "inside", Kind: "text", Content: "note", BBox: &layoutBBox{MinX: 95, MinY: 95, MaxX: 105, MaxY: 105}},
		{ID: "titleOwned", Kind: "attribute", ComponentType: "sheet", ParentID: "sheet", Key: "Name",
			Value: "title", BBox: &layoutBBox{MinX: 95, MinY: 95, MaxX: 105, MaxY: 105}},
	}
	if got := visibleTextTitleblockFindings(items, []layoutComp{sheet}, titleBlock, 1); len(got) != 1 ||
		got[0].PrimitiveId != "inside" || got[0].Type != "titleblock-overlap" {
		t.Fatalf("title block text rule wrong: %+v", got)
	}
	comps := []layoutComp{{
		ID: "p1", ComponentType: "part", Designator: "U1",
		BBox: &layoutBBox{MinX: 0, MinY: 0, MaxX: 10, MaxY: 10},
		Pins: []layoutPin{{Number: "1", X: 20, Y: 5, Rotation: 0, RotationKnown: true}},
	}}
	pinText := []visibleTextItem{{
		ID: "pinText", Kind: "text", ParentID: "other",
		BBox: &layoutBBox{MinX: 12, MinY: 3, MaxX: 16, MaxY: 7},
	}}
	got := visibleTextOverlapFindings(pinText, comps, nil, 1)
	found := false
	for _, f := range got {
		if f.Type == "visible-text-overlap" && stringsContains(f.Message, "引脚 U1.1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("text on a pin lead must be flagged: %+v", got)
	}

	markerOnly := []layoutComp{{
		ID: "m1", ComponentType: "netflag", Net: "GND",
		Pins: []layoutPin{{Number: "1", X: 20, Y: 5}},
	}}
	if got := visibleTextOverlapFindings(pinText, markerOnly, nil, 1); len(got) != 0 {
		t.Fatalf("symbol bookkeeping pins are not physical part leads: %+v", got)
	}
}

func TestZonesNotClaimedOnlyAfterSuccessfulClaimRead(t *testing.T) {
	if got := zonesNotClaimedFinding(schPartitionMinParts-1, true, 0); got != nil {
		t.Fatalf("small page must stay silent: %+v", got)
	}
	if got := zonesNotClaimedFinding(schPartitionMinParts, false, 0); got != nil {
		t.Fatalf("failed claim read must not become zones-not-claimed: %+v", got)
	}
	if got := zonesNotClaimedFinding(schPartitionMinParts, true, 2); got != nil {
		t.Fatalf("claimed page must stay silent: %+v", got)
	}
	got := zonesNotClaimedFinding(schPartitionMinParts, true, 0)
	if got == nil || got.Type != "zones-not-claimed" || got.Level != "info" {
		t.Fatalf("successful empty claim read should be advisory: %+v", got)
	}
}

func stringsContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
