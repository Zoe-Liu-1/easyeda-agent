package app

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// EasyEDA Pro V4 is the supported mainline. The editor product version is
// deliberately kept separate from extension/extension.json's engines.eda:
// that field describes the extension API engine and is still 3.2 in the
// official V4 SDK template.
const (
	hostBaselineVersion    = "4.0.0"
	hostRecommendedVersion = "4.1.60"
)

type hostCompatibilityFinding struct {
	WindowID string `json:"windowId,omitempty"`
	Version  string `json:"version,omitempty"`
	Severity string `json:"severity"` // ok | warn | block | skipped
	Reason   string `json:"reason"`
	Fix      string `json:"fix,omitempty"`
}

type hostCompatibilityReport struct {
	Baseline    string                     `json:"baseline"`
	Recommended string                     `json:"recommended"`
	Verdict     string                     `json:"verdict"`
	Findings    []hostCompatibilityFinding `json:"findings,omitempty"`
}

func hostCompatibilityFromHealth(raw []byte) hostCompatibilityReport {
	rep := hostCompatibilityReport{Baseline: hostBaselineVersion, Recommended: hostRecommendedVersion, Verdict: versionSevSkipped}
	var parsed struct {
		Windows []struct {
			WindowID       string `json:"windowId"`
			EasyEDAVersion string `json:"easyedaVersion"`
		} `json:"windows"`
	}
	if json.Unmarshal(raw, &parsed) != nil || len(parsed.Windows) == 0 {
		return rep
	}
	findings := make([]versionFinding, 0, len(parsed.Windows))
	for _, w := range parsed.Windows {
		f := evaluateHostVersion(w.WindowID, w.EasyEDAVersion)
		rep.Findings = append(rep.Findings, f)
		findings = append(findings, versionFinding{Severity: f.Severity})
	}
	rep.Verdict = worstSeverity(findings)
	return rep
}

func evaluateHostVersion(windowID, raw string) hostCompatibilityFinding {
	f := hostCompatibilityFinding{WindowID: strings.TrimSpace(windowID), Version: strings.TrimSpace(raw)}
	parts, ok := productVersionNumbers(raw)
	if !ok {
		f.Severity = versionSevSkipped
		f.Reason = "宿主未上报可比较的 EasyEDA 产品版本，无法确认 V4 兼容基线"
		return f
	}
	core := fmt.Sprintf("%d.%d.%d", parts[0], parts[1], parts[2])
	switch {
	case parts[0] < 4:
		f.Severity = versionSevBlock
		f.Reason = fmt.Sprintf("EasyEDA Pro %s 低于项目 V4 主线基线 %s", core, hostBaselineVersion)
		f.Fix = "升级到 EasyEDA Pro V4；推荐使用当前已验证的 " + hostRecommendedVersion + " 或更新 V4 版本。"
	case parts[0] > 4:
		f.Severity = versionSevWarn
		f.Reason = fmt.Sprintf("EasyEDA Pro %s 高于已声明支持的 V4 主线，需按新大版本重新验收", core)
		f.Fix = "在完成该大版本的 save → reload → readback 回归前，不要把它当作已验证宿主。"
	case compareSemverNumbers(parts, [3]int{4, 1, 60}) < 0:
		f.Severity = versionSevWarn
		f.Reason = fmt.Sprintf("EasyEDA Pro %s 属于 V4，但低于推荐且已验证的 %s", core, hostRecommendedVersion)
		f.Fix = "建议升级到 EasyEDA Pro " + hostRecommendedVersion + " 或更新 V4 版本。"
	default:
		f.Severity = versionSevOK
		f.Reason = fmt.Sprintf("EasyEDA Pro %s 满足 V4 主线基线", core)
	}
	return f
}

func productVersionNumbers(raw string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(raw), "v"), ".")
	// EasyEDA product builds may append a fourth numeric build id, for example
	// 3.2.149.88089769. Product compatibility is decided by the first 3 fields.
	if len(parts) < 3 {
		return out, false
	}
	for i, p := range parts[:3] {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func compareSemverNumbers(a, b [3]int) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

func hostCompatibilitySummary(rep hostCompatibilityReport) string {
	switch rep.Verdict {
	case versionSevBlock:
		return "✗ EasyEDA 宿主:低于 V4 主线要求；请升级到 V4，推荐 " + rep.Recommended
	case versionSevWarn:
		return "⚠ EasyEDA 宿主:未处于已验证的 V4 推荐区间；见 hostCompatibility.findings"
	case versionSevOK:
		return "✓ EasyEDA 宿主:V4 主线（推荐基线 " + rep.Recommended + "）"
	default:
		return "· EasyEDA 宿主:未判定（无窗口或版本不可读）"
	}
}
