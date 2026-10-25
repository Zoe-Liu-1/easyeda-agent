package app

import (
	"io"
	"testing"
)

func TestVisibleTextEvidenceFailsClosedWhenConnectorLacksAction(t *testing.T) {
	cfg, cleanup := errorDaemon(t, `{"ok":false,"error":{"code":"UNKNOWN_ACTION","message":"unknown action schematic.visible_text.bboxes"}}`)
	defer cleanup()

	got := visibleTextFindings(cfg, "w1", &layoutBBox{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}, nil, nil, 0.5, io.Discard)
	if len(got) != 1 || got[0].Type != "visible-text-unverified" || got[0].Level != "ERROR" {
		t.Fatalf("legacy connector must fail closed, got %+v", got)
	}
}

func TestVisibleTextEvidenceFailsClosedWhenItemsAreUnmeasured(t *testing.T) {
	cfg, cleanup := errorDaemon(t, `{"ok":true,"result":{"items":[],"unmeasured":[{"id":"text-1","content":"value"}]}}`)
	defer cleanup()

	got := visibleTextFindings(cfg, "w1", &layoutBBox{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}, nil, nil, 0.5, io.Discard)
	if len(got) != 1 || got[0].Type != "visible-text-unverified" || got[0].Level != "ERROR" {
		t.Fatalf("unmeasured visible text must fail closed, got %+v", got)
	}
}

func TestPartitionAndTitleblockEvidenceFailClosedOnReadErrors(t *testing.T) {
	cfg, cleanup := errorDaemon(t, `{"ok":false,"error":{"code":"EDA_CALL_FAILED","message":"read failed"}}`)
	defer cleanup()

	partition := partitionFinding(cfg, "w1", twelveParts(), io.Discard)
	if len(partition) != 1 || partition[0].Type != "partition-geometry-unavailable" || partition[0].Level != "ERROR" {
		t.Fatalf("partition read failure must fail closed, got %+v", partition)
	}
	title := titleBlockFinding(cfg, "w1", io.Discard)
	if len(title) != 1 || title[0].Type != "titleblock-geometry-unavailable" || title[0].Level != "ERROR" {
		t.Fatalf("title-block read failure must fail closed, got %+v", title)
	}
}
