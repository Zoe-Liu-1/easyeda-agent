package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestHealthChecksGiveANextStepPerSetupStage(t *testing.T) {
	byName := func(cs []healthCheck) map[string]healthCheck {
		m := map[string]healthCheck{}
		for _, c := range cs {
			m[c.Name] = c
		}
		return m
	}
	// No daemon: one failing line that says how to start it.
	got := healthChecks(healthResult{Host: "127.0.0.1", Ports: "60832-60841"})
	if len(got) != 1 || got[0].Status != "fail" || !strings.Contains(got[0].Fix, "daemon start") {
		t.Fatalf("absent daemon: %+v", got)
	}
	// Daemon but no EasyEDA window: connector fails with the import + 允许外部交互 step.
	got = healthChecks(healthResult{Found: &daemonHealth{Port: 60832, Raw: []byte(`{"version":"v1.11.0","windows":[]}`)}})
	if c := byName(got)["connector"]; c.Status != "fail" || !strings.Contains(c.Fix, "允许外部交互") {
		t.Fatalf("no window: %+v", got)
	}
	// Two windows, one hidden and one without a project.
	raw := []byte(`{"version":"v1.11.0","windows":[
		{"windowId":"w-hidden","connectorVersion":"1.11.0","context":{"projectName":"ceshi","documentType":"pcb","pageVisibility":"hidden"}},
		{"windowId":"w-empty","connectorVersion":"1.11.0","context":{}}]}`)
	got = healthChecks(healthResult{Found: &daemonHealth{Port: 60832, Raw: raw}})
	var hidden, empty bool
	for _, c := range got {
		hidden = hidden || (c.Name == "project" && c.Status == "ok" && strings.Contains(c.Detail, "后台"))
		empty = empty || (c.Name == "project" && strings.Contains(c.Detail, "w-empty"))
	}
	if !hidden || !empty || byName(got)["connector"].Status != "warn" {
		t.Fatalf("hidden tab is noted (not a fault); empty window / multi-window must each be actionable: %+v", got)
	}
	var buf bytes.Buffer
	printHealthChecklist(&buf, got)
	if !strings.Contains(buf.String(), "→") || !strings.Contains(buf.String(), "就绪") {
		t.Fatalf("checklist must show next steps and a verdict:\n%s", buf.String())
	}
}
