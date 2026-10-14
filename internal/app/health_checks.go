package app

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// healthCheck is one line of the onboarding checklist `easyeda health` prints:
// what is being checked, whether it holds, and — when it does not — the exact
// next step. The JSON envelope keeps every raw field; this is the one-screen
// answer to "is everything ready, and if not, what do I do".
type healthCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// healthChecks derives the checklist from a scan result. Pure: no I/O.
// Order follows the setup path — daemon, connector, versions, host, page.
func healthChecks(r healthResult) []healthCheck {
	if r.Found == nil {
		return []healthCheck{{Name: "daemon", Status: "fail",
			Detail: fmt.Sprintf("%s 端口 %s 上没有 easyeda daemon", r.Host, r.Ports),
			Fix:    "运行 `easyeda daemon start`（或任意 easyeda 命令会自动在后台启动它）"}}
	}
	var raw struct {
		Version string `json:"version"`
		Windows []struct {
			WindowID         string `json:"windowId"`
			ConnectorVersion string `json:"connectorVersion"`
			Context          struct {
				ProjectName    string `json:"projectName"`
				DocumentType   string `json:"documentType"`
				PageVisibility string `json:"pageVisibility"`
			} `json:"context"`
		} `json:"windows"`
	}
	_ = json.Unmarshal(r.Found.Raw, &raw)
	out := []healthCheck{{Name: "daemon", Status: "ok", Detail: fmt.Sprintf("%s 运行在端口 %d", display(raw.Version), r.Found.Port)}}
	if len(raw.Windows) == 0 {
		return append(out, healthCheck{Name: "connector", Status: "fail",
			Detail: "没有 EasyEDA 窗口连上 daemon",
			Fix:    "在 EasyEDA Pro 导入连接器 .eext（GitHub Release 或立创插件市场），打开工程，并开启「设置 → 允许外部交互」"})
	}
	var versions []string
	for _, w := range raw.Windows {
		versions = append(versions, display(w.ConnectorVersion))
	}
	conn := healthCheck{Name: "connector", Status: "ok", Detail: fmt.Sprintf("%d 个窗口已连接（连接器 %s）", len(raw.Windows), strings.Join(versions, ", "))}
	if len(raw.Windows) > 1 {
		conn.Status, conn.Fix = "warn", "多个窗口同时连接：命令加 `--project <工程名>` 指定目标，或关掉多余的 EasyEDA 标签页"
	}
	out = append(out, conn)
	if g := r.VersionGate; g != nil {
		c := healthCheck{Name: "versions", Status: "ok", Detail: strings.TrimSpace(strings.TrimLeft(versionGateSummary(*g), "✓⚠·✗ "))}
		if g.Verdict == versionSevWarn || g.Verdict == versionSevBlock {
			c.Status, c.Fix = "warn", "运行 `easyeda update --check` 查看差异；按提示 `easyeda update`，连接器需重新导入对应版本 .eext"
		}
		out = append(out, c)
	}
	if h := r.HostCompatibility; h != nil {
		c := healthCheck{Name: "easyeda", Status: "ok", Detail: strings.TrimSpace(strings.TrimLeft(hostCompatibilitySummary(*h), "✓⚠·✗ "))}
		switch h.Verdict {
		case versionSevBlock:
			c.Status, c.Fix = "fail", "升级 EasyEDA Pro 到 V4（推荐 "+h.Recommended+" 或更新）"
		case versionSevWarn:
			c.Status, c.Fix = "warn", "建议升级 EasyEDA Pro 到 "+h.Recommended+" 或更新"
		}
		out = append(out, c)
	}
	for _, w := range raw.Windows {
		name := w.Context.ProjectName
		if name == "" {
			out = append(out, healthCheck{Name: "project", Status: "warn", Detail: "窗口 " + w.WindowID + " 没有打开工程",
				Fix: "在 EasyEDA 里打开要操作的工程"})
			continue
		}
		c := healthCheck{Name: "project", Status: "ok", Detail: fmt.Sprintf("%s（当前 %s）", name, display(w.Context.DocumentType))}
		// Hidden is not a fault: reads, writes, import and reload ride the
		// connector's worker clock. Only the canvas stops repainting.
		if w.Context.PageVisibility == "hidden" {
			c.Detail += "，标签页在后台（操作照常；仅 PCB 画布截图需切到前台）"
		}
		out = append(out, c)
	}
	return out
}

// printHealthChecklist writes the checklist for humans (stderr; stdout stays
// the JSON contract agents parse).
func printHealthChecklist(w io.Writer, checks []healthCheck) {
	mark := map[string]string{"ok": "✓", "warn": "⚠", "fail": "✗"}
	ready := true
	for _, c := range checks {
		fmt.Fprintf(w, "%s %-9s %s\n", mark[c.Status], c.Name, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(w, "            → %s\n", c.Fix)
		}
		if c.Status == "fail" {
			ready = false
		}
	}
	if ready {
		fmt.Fprintln(w, "就绪：可以开始操作 EasyEDA。")
	} else {
		fmt.Fprintln(w, "未就绪：按 → 的步骤处理后重跑 `easyeda health`。")
	}
}
