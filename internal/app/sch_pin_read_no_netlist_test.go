package app

import "testing"

// A pin read that only needs geometry must not ask the connector for pin nets:
// that triggers a netlist export, and the host rejects the write that follows it
// (`Cannot destructure property 'cmdKey'`, #256/#257). `sch connect --pin`
// resolves its target this way and writes immediately after.
func TestResolveSchPinSkipsNetlistExport(t *testing.T) {
	cfg, cap, cleanup := newCapturingDaemon(t)
	defer cleanup()
	_, _ = resolveSchPin(cfg, "w1", "R1:1") // the fake page has no R1; only the request matters
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.action != "schematic.components.list" || cap.payload["includePins"] != true || cap.payload["includePinNets"] != false {
		t.Fatalf("geometry-only pin read must disable pin nets: action=%s payload=%v", cap.action, cap.payload)
	}
}
