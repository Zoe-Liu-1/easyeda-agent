package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReloadReceiptRecordsObservedStages(t *testing.T) {
	fx, cfg := newReloadFixture(t, false)
	r, err := reloadDocumentWithReceipt(cfg, "w1", "pcb-target", "pcb-target")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "passed" || !r.Saved || !r.Closed || r.Reloaded != "pcb-target" || r.ActiveRestored != "pcb-target" || r.Readiness == nil || !r.Readiness.Ready {
		t.Fatalf("receipt: %+v", r)
	}
	if r.FinalContext.DocumentUUID != "pcb-target" || r.TargetContext.ProjectUUID != "project-1" || fx.openCount != 1 {
		t.Fatalf("identity or open count: %+v", r)
	}
	stages := []string{}
	for _, s := range r.Steps {
		if s.Stage != "ready" && (s.RequestID == "" || s.ResponseCreatedAt == nil) {
			t.Fatalf("missing original response provenance: %+v", s)
		}
		start, e := time.Parse(time.RFC3339Nano, s.StartedAt)
		if e != nil {
			t.Fatal(e)
		}
		end, e := time.Parse(time.RFC3339Nano, s.CompletedAt)
		if e != nil || end.Before(start) {
			t.Fatalf("stage timing: %+v", s)
		}
		stages = append(stages, s.Stage)
	}
	if strings.Join(stages, ",") != "inspect,save,close,close-settle,reopen,ready" {
		t.Fatal(stages)
	}
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), `"timeSource":"cli-host-observation"`) {
		t.Fatal(string(b))
	}
}

func TestReloadCLIEmitsJSONOnFailure(t *testing.T) {
	_, cfg := newReloadFixture(t, true)
	var stdout, stderr bytes.Buffer
	cmd := newDocCmd(cfg, &stdout, &stderr)
	cmd.SetArgs([]string{"reload", "pcb-target", "--window", "w1", "--json"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected failed reopen")
	}
	var got documentReloadReceipt
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("receipt lost on error: %v: %s", err, stdout.String())
	}
	if got.Status != "partial" || got.FailedStage != "reopen" || !got.Saved || !got.Closed || got.Reloaded != "" {
		t.Fatalf("%+v", got)
	}
}

func TestReloadReceiptRetainsPartialFailure(t *testing.T) {
	for _, mode := range []string{"save", "close", "reopen"} {
		t.Run(mode, func(t *testing.T) {
			fx, cfg := newReloadFixture(t, mode == "reopen")
			if mode == "save" {
				fx.saveResult = map[string]any{"saved": "true"}
			}
			if mode == "close" {
				fx.failClose = true
			}
			r, err := reloadDocumentWithReceipt(cfg, "w1", "pcb-target", "pcb-target")
			if err == nil || r.FailedStage != mode || r.Error == "" || r.Reloaded != "" || r.Readiness != nil {
				t.Fatalf("receipt=%+v err=%v", r, err)
			}
			if mode == "save" && (r.Status != "failed" || r.Saved || fx.closed || fx.openCount != 0) {
				t.Fatal(r)
			}
			if mode != "save" && (r.Status != "partial" || !r.Saved) {
				t.Fatal(r)
			}
			if mode == "reopen" && (!r.Closed || fx.openCount != 1) {
				t.Fatal(r)
			}
		})
	}
}

func TestReloadCLITargetResolutionFailureEmitsReceipt(t *testing.T) {
	_, cfg := newReloadFixture(t, false)
	var stdout, stderr bytes.Buffer
	cmd := newDocCmd(cfg, &stdout, &stderr)
	cmd.SetArgs([]string{"reload", "missing-target", "--window", "w1", "--json"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected target resolution failure")
	}
	var got documentReloadReceipt
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.FailedStage != "resolve-target" || got.Status != "failed" || got.Saved || got.Reloaded != "" || got.Target != "missing-target" {
		t.Fatalf("%+v", got)
	}
}

func TestReloadReceiptRejectsUnverifiedIdentityAtBoundaries(t *testing.T) {
	for _, mode := range []string{"inspect-result", "save-project", "close-context", "ready-window", "ready-unordered", "ready-project", "reopen-timeout"} {
		t.Run(mode, func(t *testing.T) {
			fx, cfg := newReloadFixture(t, mode == "reopen-timeout")
			if mode == "reopen-timeout" {
				fx.openErrorCode = "ACTION_TIMEOUT"
			}
			fx.responseChange = func(action string, response map[string]any) {
				ctx := response["context"].(map[string]any)
				if mode == "inspect-result" && action == "document.current" && !fx.closed {
					response["result"].(map[string]any)["uuid"] = "wrong"
				}
				if mode == "save-project" && action == "pcb.save" {
					ctx["projectUuid"] = "other"
				}
				if mode == "close-context" && action == "document.current" && fx.closed && !fx.opened {
					response["context"] = nil
				}
				if action == "document.current" && fx.opened {
					switch mode {
					case "ready-window":
						response["windowId"] = "other-window"
					case "ready-project":
						ctx["projectUuid"] = "other"
					}
				}
				if mode == "ready-unordered" && action == "pcb.components.list" && fx.opened {
					response["unordered"] = true
				}
			}
			r, err := reloadDocumentWithReceipt(cfg, "w1", "pcb-target", "pcb-target")
			if err == nil || r.Status == "passed" || r.Reloaded != "" {
				t.Fatalf("receipt=%+v err=%v", r, err)
			}
			expected := map[string]string{"inspect-result": "inspect", "save-project": "save", "close-context": "close-settle", "ready-window": "ready", "ready-unordered": "ready", "ready-project": "ready", "reopen-timeout": "reopen"}[mode]
			if r.FailedStage != expected {
				t.Fatalf("stage=%s expected=%s", r.FailedStage, expected)
			}
			if (mode == "inspect-result" || mode == "save-project" || mode == "close-context") && fx.openCount != 0 {
				t.Fatal("unverified identity reopened document")
			}
			if mode == "reopen-timeout" && fx.openCount != 1 {
				t.Fatal("timeout automatically retried")
			}
		})
	}
}

func TestReloadReceiptAcceptsBypassCurrentWithOrderedInventory(t *testing.T) {
	fx, cfg := newReloadFixture(t, false)
	fx.responseChange = func(action string, response map[string]any) {
		response["unordered"] = action == "document.current"
	}
	r, err := reloadDocumentWithReceipt(cfg, "w1", "pcb-target", "pcb-target")
	if err != nil || r.Status != "passed" || r.Readiness == nil || !r.Readiness.Ready || fx.openCount != 1 {
		t.Fatalf("bypass identity observation rejected: receipt=%+v err=%v", r, err)
	}
}
