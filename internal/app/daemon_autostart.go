package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/zhoushoujianwork/easyeda-agent/internal/selfupdate"
	"github.com/zhoushoujianwork/easyeda-agent/internal/version"
)

// Background daemon start. Before this, first use meant "open a terminal and
// keep `easyeda daemon start` running in the foreground" — the hardest setup
// step for people and impossible for an agent whose shell blocks on it.
//
// Auto-start is skipped for dev builds (make dev runs the daemon under air;
// a CLI call during an air rebuild must not race it) and with
// EASYEDA_NO_AUTOSTART=1. Two CLI processes starting at once would otherwise
// let the second daemon "replace" the first, so a lock file serializes the
// spawn and the holder re-scans before spawning.

const daemonStartWait = 15 * time.Second

func daemonLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".easyeda-agent", "daemon.log")
}

var releaseVersionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// autoStartAllowed reports whether a missing daemon may be started implicitly:
// installed builds only — a tagged release or a local -dev.N bundle. Source
// builds (git describe "v1.11.0-2-gffac5fe-dirty", "dev") are what `make dev`
// runs under air; a CLI call during an air rebuild must not race it.
func autoStartAllowed() bool {
	if os.Getenv("EASYEDA_NO_AUTOSTART") != "" {
		return false
	}
	return releaseVersionPattern.MatchString(version.Version) || selfupdate.IsLocalVersion(version.Version)
}

// startDaemonBackground spawns `easyeda daemon start` detached (log appended to
// ~/.easyeda-agent/daemon.log) and waits until /health answers. It returns the
// port the daemon answered on.
func startDaemonBackground(cfg *appConfig, stderr io.Writer, extraArgs ...string) (int, error) {
	portStart, portEnd, err := cfg.portRange()
	if err != nil {
		return 0, err
	}
	scan := func() int {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if r := scanHealth(ctx, hostPortOptions{host: cfg.host, portStart: portStart, portEnd: portEnd}); r.Found != nil {
			return r.Found.Port
		}
		return 0
	}
	waitUp := func() int {
		deadline := time.Now().Add(daemonStartWait)
		for time.Now().Before(deadline) {
			if p := scan(); p != 0 {
				return p
			}
			time.Sleep(300 * time.Millisecond)
		}
		return 0
	}
	logPath := daemonLogPath()
	if logPath == "" {
		return 0, errors.New("cannot locate home directory for the daemon log")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return 0, err
	}
	lock := logPath + ".start.lock"
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		// Another CLI is starting it right now (or a crashed start left the lock).
		if st, serr := os.Stat(lock); serr == nil && time.Since(st.ModTime()) > 2*daemonStartWait {
			_ = os.Remove(lock)
			return startDaemonBackground(cfg, stderr, extraArgs...)
		}
		if p := waitUp(); p != 0 {
			return p, nil
		}
		return 0, fmt.Errorf("another easyeda process is starting the daemon but it did not come up; see %s", logPath)
	}
	f.Close()
	defer os.Remove(lock)
	if p := scan(); p != 0 { // started by someone else meanwhile
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer logf.Close()
	// Same host/ports as the caller: a spawned daemon on the default port would
	// "replace" whatever easyeda daemon already holds it.
	args := append([]string{"daemon", "start", "--host", cfg.host, "--ports", cfg.ports}, extraArgs...)
	cmd := exec.Command(exe, args...)
	fmt.Fprintf(logf, "\n=== %s background start: %s %v ===\n", time.Now().Format(time.RFC3339), exe, args)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = logf, logf, nil
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start daemon: %w", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	if p := waitUp(); p != 0 {
		fmt.Fprintf(stderr, "↻ easyeda daemon 已在后台启动（pid %d，端口 %d，日志 %s）\n", pid, p, logPath)
		return p, nil
	}
	return 0, fmt.Errorf("daemon (pid %d) did not answer /health within %s; see %s", pid, daemonStartWait, logPath)
}
