package app

import (
	"testing"

	"github.com/zhoushoujianwork/easyeda-agent/internal/version"
)

func TestAutoStartAllowedOnlyForReleaseBuildsWithoutOptOut(t *testing.T) {
	old := version.Version
	t.Cleanup(func() { version.Version = old })
	version.Version = "v1.11.0"
	t.Setenv("EASYEDA_NO_AUTOSTART", "")
	if !autoStartAllowed() {
		t.Fatal("a release build without opt-out must auto-start")
	}
	t.Setenv("EASYEDA_NO_AUTOSTART", "1")
	if autoStartAllowed() {
		t.Fatal("EASYEDA_NO_AUTOSTART=1 must disable auto-start")
	}
	t.Setenv("EASYEDA_NO_AUTOSTART", "")
	for _, dev := range []string{"v1.11.0-2-gffac5fe-dirty", "v1.11.0-dirty", "dev"} {
		version.Version = dev
		if autoStartAllowed() {
			t.Fatalf("source build %q (make dev runs the daemon under air) must never auto-start", dev)
		}
	}
	version.Version = "1.12.0-dev.3"
	if !autoStartAllowed() {
		t.Fatal("an installed local -dev.N bundle behaves like a release")
	}
}
