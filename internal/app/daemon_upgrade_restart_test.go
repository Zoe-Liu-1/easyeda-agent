package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/easyeda-agent/internal/selfupdate"
)

// Only installed builds are replaced, and only by a newer installed CLI: a
// make dev daemon (git-describe stamp) is never stopped, and an older CLI never
// downgrades a newer daemon.
func TestStaleInstalledDaemon(t *testing.T) {
	for _, c := range []struct {
		daemon, cli string
		want        bool
	}{
		{"v1.12.0", "v1.13.0", true},
		{"1.12.0", "v1.13.0", true},
		{"1.13.0-dev.2", "v1.14.0", true},
		{"v1.13.0", "v1.13.0", false},
		{"v1.14.0", "v1.13.0", false},
		{"v1.12.0-24-ge5ed0fb-dirty", "v1.13.0", false},
		{"v1.12.0", "v1.13.0-3-gabc1234", false},
		{"", "v1.13.0", false},
	} {
		if got := staleInstalledDaemon(c.daemon, c.cli); got != c.want {
			t.Errorf("staleInstalledDaemon(%q, %q) = %v, want %v", c.daemon, c.cli, got, c.want)
		}
	}
}

func TestShouldRestartDaemonAfterUpdate(t *testing.T) {
	base := func() updateReport {
		return updateReport{
			CLI:       &selfupdate.CLIOutcome{Status: "updated"},
			Connector: &connectorReport{DaemonRunning: true, DaemonStatus: "mismatch", DaemonVersion: "v1.12.0"},
		}
	}
	if !shouldRestartDaemonAfterUpdate(base(), false, false, true) {
		t.Fatal("updated CLI with an installed mismatched daemon must restart it")
	}
	up := base()
	up.CLI.Status = "up-to-date" // binary already current, daemon left behind earlier
	if !shouldRestartDaemonAfterUpdate(up, false, false, true) {
		t.Fatal("up-to-date CLI with a stale daemon must restart it")
	}
	for name, c := range map[string]struct {
		rep                        updateReport
		check, skillOnly, disabled bool
	}{
		"check only":   {rep: base(), check: true},
		"skill only":   {rep: base(), skillOnly: true},
		"flag off":     {rep: base(), disabled: true},
		"cli failed":   {rep: func() updateReport { r := base(); r.CLI.Status = "error"; return r }()},
		"dev skipped":  {rep: func() updateReport { r := base(); r.CLI.Status = "skipped"; return r }()},
		"current":      {rep: func() updateReport { r := base(); r.Connector.DaemonStatus = "current"; return r }()},
		"make dev":     {rep: func() updateReport { r := base(); r.Connector.DaemonVersion = "v1.12.0-24-ge5ed0fb-dirty"; return r }()},
		"not running":  {rep: func() updateReport { r := base(); r.Connector.DaemonRunning = false; return r }()},
		"no connector": {rep: func() updateReport { r := base(); r.Connector = nil; return r }()},
	} {
		if shouldRestartDaemonAfterUpdate(c.rep, c.check, c.skillOnly, !c.disabled) {
			t.Errorf("%s: must not restart", name)
		}
	}
}

// Two CLIs can see the same stale daemon; the second must not stop the first
// one's fresh replacement, so the live version is re-read right before stopping.
func TestRestartInstalledDaemonSkipsWhenAlreadyReplaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"service":"easyeda-agent","version":"v1.13.0","windows":[]}`))
	}))
	defer srv.Close()
	host, portStr, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	port, _ := strconv.Atoi(portStr)
	cfg := &appConfig{host: host, ports: portStr + "-" + portStr}
	stale := func(v string) bool { return staleInstalledDaemon(v, "v1.13.0") }
	if err := restartInstalledDaemon(cfg, port, "", stale, io.Discard); err != nil {
		t.Fatalf("already-current daemon must be left alone: %v", err)
	}
}

func TestUpdateNotesReportDaemonRestart(t *testing.T) {
	rep := updateReport{Target: "1.13.0", DaemonRestart: &daemonRestartReport{From: "v1.12.0", Status: "restarted"},
		Connector: &connectorReport{DaemonStatus: "current"}}
	notes := strings.Join(updateNotes(rep), "\n")
	if !strings.Contains(notes, "v1.12.0 restarted in the background on v1.13.0") || strings.Contains(notes, "DIFFERENT binary") {
		t.Fatalf("restart not reported: %s", notes)
	}
	rep.DaemonRestart = &daemonRestartReport{From: "v1.12.0", Status: "failed", Error: "port busy"}
	rep.Connector.DaemonStatus = "mismatch"
	notes = strings.Join(updateNotes(rep), "\n")
	if !strings.Contains(notes, "automatic daemon restart failed: port busy") || !strings.Contains(notes, "DIFFERENT binary") {
		t.Fatalf("failed restart must keep the manual instruction: %s", notes)
	}
}
