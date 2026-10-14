package app

import (
	"strings"
	"testing"
)

func TestDocGuardProbesOnceForReadsAndRepinsOnDrift(t *testing.T) {
	ctx := func(doc string) string {
		return `"context":{"projectUuid":"p","documentUuid":"` + doc + `","documentType":"pcb"}`
	}
	drift := 0 // next N reads report the wrong page
	cfg, state, cleanup := newAutolayoutTestDaemon(t, func(_ int, call autolayoutTestCall) string {
		switch call.Action {
		case "document.current":
			return `{"ok":true,"result":{"uuid":"docA","documentType":"pcb","parentProjectUuid":"p"},` + ctx("docA") + `}`
		case "pcb.components.list":
			if drift > 0 {
				drift--
				return `{"ok":true,"result":{"components":[]},` + ctx("docB") + `}`
			}
			return `{"ok":true,"result":{"components":[]},` + ctx("docA") + `}`
		}
		return `{"ok":true,"result":{},` + ctx("docA") + `}`
	})
	defer cleanup()
	cfg.doc = "docA"
	count := func(action string) int {
		state.mu.Lock()
		defer state.mu.Unlock()
		n := 0
		for _, c := range state.calls {
			if c.Action == action {
				n++
			}
		}
		return n
	}
	for i := 0; i < 3; i++ {
		if _, err := requestAction(cfg, "pcb.components.list", "w1", nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := count("document.current"); got != 1 {
		t.Fatalf("three reads after one confirmation must probe once, got %d probes", got)
	}
	// Foreground drifted under a remembered pin: the read is re-pinned and resent.
	drift = 1
	if _, err := requestAction(cfg, "pcb.components.list", "w1", nil); err != nil {
		t.Fatal(err)
	}
	if got, reads := count("document.current"), count("pcb.components.list"); got != 2 || reads != 5 {
		t.Fatalf("drift must re-probe once and resend the read once: probes=%d reads=%d", got, reads)
	}
	// Writes and exec_js always probe live.
	before := count("document.current")
	for _, a := range []string{"pcb.component.modify", "debug.exec_js"} {
		if !strings.Contains(a, "exec") && !actionMutates(a) {
			t.Fatalf("%s expected to be a mutation in the catalog", a)
		}
		_, _ = requestAction(cfg, a, "w1", map[string]any{})
	}
	if got := count("document.current") - before; got != 2 {
		t.Fatalf("each write / exec_js keeps its live probe, got %d", got)
	}
}

func TestDocGuardResolvesAPageNameOnce(t *testing.T) {
	ctx := `"context":{"projectUuid":"p","documentUuid":"uuid-p1","documentType":"schematic"}`
	cfg, state, cleanup := newAutolayoutTestDaemon(t, func(_ int, call autolayoutTestCall) string {
		switch call.Action {
		case "document.current":
			return `{"ok":true,"result":{"uuid":"uuid-p1","documentType":"schematic","parentProjectUuid":"p"},` + ctx + `}`
		case "schematic.pages.list":
			return `{"ok":true,"result":{"pages":[{"uuid":"uuid-p1","name":"P1","parentSchematicUuid":"s"}]},` + ctx + `}`
		case "pcb.documents.list":
			return `{"ok":true,"result":{"pcbs":[]},` + ctx + `}`
		}
		return `{"ok":true,"result":{},` + ctx + `}`
	})
	defer cleanup()
	cfg.doc = "P1"
	for i := 0; i < 4; i++ {
		if _, err := ensureActiveDocUUID(cfg, "w1"); err != nil {
			t.Fatal(err)
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	lists := 0
	for _, c := range state.calls {
		if c.Action == "schematic.pages.list" {
			lists++
		}
	}
	if lists != 1 {
		t.Fatalf("a --doc name must be resolved once per process, listed pages %d times", lists)
	}
}
