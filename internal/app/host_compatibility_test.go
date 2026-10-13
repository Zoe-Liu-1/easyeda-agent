package app

import (
	"strings"
	"testing"
)

func TestEvaluateHostVersionV4Mainline(t *testing.T) {
	cases := []struct {
		version string
		want    string
	}{
		{"3.2.203", versionSevBlock},
		{"4.0.9", versionSevWarn},
		{"4.1.59", versionSevWarn},
		{"4.1.60", versionSevOK},
		{"4.2.0.123456", versionSevOK},
		{"5.0.0", versionSevWarn},
		{"unknown", versionSevSkipped},
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			got := evaluateHostVersion("w1", tc.version)
			if got.Severity != tc.want {
				t.Fatalf("severity=%q want=%q: %+v", got.Severity, tc.want, got)
			}
		})
	}
}

func TestHostCompatibilityFromHealthWorstWindowWins(t *testing.T) {
	rep := hostCompatibilityFromHealth([]byte(`{"windows":[
	  {"windowId":"v4","easyedaVersion":"4.1.60"},
	  {"windowId":"v3","easyedaVersion":"3.2.203"}]}`))
	if rep.Verdict != versionSevBlock || len(rep.Findings) != 2 {
		t.Fatalf("unexpected report: %+v", rep)
	}
	if !strings.Contains(hostCompatibilitySummary(rep), "升级到 V4") {
		t.Fatalf("summary does not give the required migration: %s", hostCompatibilitySummary(rep))
	}
}

func TestHostCompatibilityIsSeparateFromExtensionAPIEngine(t *testing.T) {
	rep := hostCompatibilityFromHealth([]byte(`{"windows":[{"easyedaVersion":"4.1.60"}]}`))
	if rep.Baseline != "4.0.0" || rep.Recommended != "4.1.60" || rep.Verdict != versionSevOK {
		t.Fatalf("unexpected V4 declaration: %+v", rep)
	}
}
