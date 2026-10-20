package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// ── sch check: reconstructed per-item design check ──────────────────────────
//
// The EDA schematic DRC API (eda.sch_Drc.check) returns only an aggregate
// {count,type} — the per-item detail the UI panel shows is not exposed by any
// public API. schematic.check reconstructs the actionable findings from the
// primitives directly (connector side). Rule 1: floating pins via geometric
// connectivity CROSS-CHECKED against the JSON-authoritative netlist — a net in
// the netlist drops #15-class geometric false positives, while geometry that
// touches a pin the netlist puts on no net surfaces as a geom-net-mismatch
// (suspected missed report). This file renders that report and (with --strict)
// gates on it. Output is by designator + pin number — feed it straight into
// `sch no-connect`.
//
// Every finding carries a kebab-case rule Type (same convention as pcb check),
// so findings can be counted/gated per rule. Connector-produced types:
//
//	floating-pin        WARN  pin with no wire and no NC marker
//	geom-net-mismatch   WARN  wire touches pin but netlist puts it on no net
//	net-marker-mismatch WARN  netflag/port/label name ≠ the wire's net name
//	multi-net-wire      WARN  one wire primitive carrying multiple net names
//	wire-crossing       WARN  two wires cross mid-segment
//	wire-over-pin       WARN  a wire body runs through a pin it doesn't end on
//	non-orthogonal-wire ERROR a diagonal/non-axis wire segment
//	zero-length-wire    WARN  degenerate zero-length segment
//	dangling-wire       WARN  wire end anchored to nothing (incl. orphan stubs)
//	polarity-convention-outlier WARN cap whose rail-side pin contradicts the
//	                          same-page majority (#183 phase 1; MLCC numbering is
//	                          arbitrary, so a consistency hint, not an assertion)
//
// checkSummary mirrors these with one per-type count field each.

type checkPinDetail struct {
	Number string  `json:"number"`
	Name   string  `json:"name,omitempty"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
}

// checkPoint is a finding's anchor coordinate. Named (not an anonymous struct) so
// the Go-side geometric rules can construct it, while the JSON shape stays exactly
// what the connector emits for `at`.
type checkPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// checkOverlapSide describes the OTHER primitive in a pairwise geometric finding
// (marker-overlap), so a caller has both primitive IDs + types to act on. See #148.
type checkOverlapSide struct {
	PrimitiveId   string      `json:"primitiveId"`
	ComponentType string      `json:"componentType,omitempty"`
	Designator    string      `json:"designator,omitempty"`
	Net           string      `json:"net,omitempty"`
	BBox          *layoutBBox `json:"bbox,omitempty"`
}

type checkFinding struct {
	Type              string           `json:"type"`
	Level             string           `json:"level"`
	Designator        string           `json:"designator,omitempty"`
	PrimitiveId       string           `json:"primitiveId,omitempty"`
	WirePrimitiveId   string           `json:"wirePrimitiveId,omitempty"`
	MarkerPrimitiveId string           `json:"markerPrimitiveId,omitempty"`
	WireNet           string           `json:"wireNet,omitempty"`
	MarkerNet         string           `json:"markerNet,omitempty"`
	Nets              []string         `json:"nets,omitempty"`
	Pins              []string         `json:"pins,omitempty"`
	PinDetails        []checkPinDetail `json:"pinDetails,omitempty"`
	// Count 是规则专属槽:floating-pin=悬空脚数 / multi-net-wire=异名数 /
	// polarity-convention-outlier=同页多数派票数(majorityCount)。
	Count   int         `json:"count,omitempty"`
	Message string      `json:"message,omitempty"`
	At      *checkPoint `json:"at,omitempty"`
	// Geometric marker rules (issues #146/#147/#148), computed Go-side from the
	// components.list bboxes/anchors — the electrical check never sees them.
	ComponentType    string            `json:"componentType,omitempty"`    // primary primitive's type
	PrimitiveIds     []string          `json:"primitiveIds,omitempty"`     // duplicate-net-marker: ALL coincident ids; marker-overlap: [a,b]
	SuggestKeepId    string            `json:"suggestKeepId,omitempty"`    // duplicate-net-marker: id to keep
	SuggestDeleteIds []string          `json:"suggestDeleteIds,omitempty"` // duplicate-net-marker: ids to prim-delete
	BBox             *layoutBBox       `json:"bbox,omitempty"`             // primary primitive bbox (titleblock/marker overlap)
	Keepout          *layoutBBox       `json:"keepout,omitempty"`          // titleblock-overlap: the keep-out rect
	Other            *checkOverlapSide `json:"other,omitempty"`            // marker-overlap: the B side
	OverlapX         float64           `json:"overlapX,omitempty"`         // overlap extent (marker/titleblock)
	OverlapY         float64           `json:"overlapY,omitempty"`
}

type checkSummary struct {
	FloatingPins           int `json:"floatingPins"`
	ComponentsWithFloating int `json:"componentsWithFloating"`
	GeomNetMismatches      int `json:"geomNetMismatches"`
	NetMarkerMismatches    int `json:"netMarkerMismatches"`
	MultiNetWires          int `json:"multiNetWires"`
	WireCrossings          int `json:"wireCrossings"`
	WireOverPins           int `json:"wireOverPins"`
	NonOrthogonalWires     int `json:"nonOrthogonalWires"`
	ZeroLengthWires        int `json:"zeroLengthWires"`
	DanglingWires          int `json:"danglingWires"`
	// Go-side geometric marker rules (issues #146/#147/#148).
	DuplicateNetMarkers int `json:"duplicateNetMarkers"`
	TitleblockOverlaps  int `json:"titleblockOverlaps"`
	// Any part/marker/wire leaving the sheet's usable area (sheet-boundary ruler).
	OutOfSheet     int `json:"outOfSheet"`
	MarkerOverlaps int `json:"markerOverlaps"`
	// A net marker (netflag/netport) body sitting on a foreign wire.
	MarkerWireOverlaps int `json:"markerWireOverlaps"`
	// Visible text (free text + shown attributes, incl. Value/name text) overlapping
	// a different owner's part body / marker / text.
	VisibleTextOverlaps int `json:"visibleTextOverlaps"`
	// Visible wire-name label economy/orientation (Go-side, net-label data).
	LabelVisibility            int `json:"labelVisibility"`
	LabelEconomy               int `json:"labelEconomy"`
	LabelOrientations          int `json:"labelOrientations"`
	DirectConnectOpportunities int `json:"directConnectOpportunities"`
	// Local power-cluster rule: consumer pin + nearby passive(s) on separate
	// wire roots, each carrying its own same-name power symbol.
	PowerClusterCandidates int `json:"powerClusterCandidates"`
	// Layout-organization rule (铁律 #15): a multi-module page with zero functional
	// zone frames. Mechanical backstop so "分区分区" is enforced by
	// the check, not by remembering the rule (which is unreliable — it was skipped
	// twice in one session before this existed).
	MissingPartitions int `json:"missingPartitions"`
	// Same-tree redundant markers (anchors differ so duplicate-net-marker misses).
	RedundantNetMarkers int `json:"redundantNetMarkers"`
	// Readability rule: netports standing vertical (rotation 90/270) render their
	// net name sideways — the "标签折起来" fail on dense pin columns.
	FoldedNetLabels int `json:"foldedNetLabels"`
	// Orientation rule: a netflag whose stored rotation contradicts its stub
	// direction per the orientation truth table (reversed/upside-down flag).
	ReversedNetFlags int `json:"reversedNetFlags"`
	// Absolute orientation rule (电源上/地上下): power flag body must point up,
	// GND flag body must point down — independent of which side the wire enters.
	PowerGroundOrientation int `json:"powerGroundOrientation"`
	// Power/ground flags explicitly exempted from the absolute orientation rule
	// (reverse pins): read from otherProperty.orientationExempt or declared via
	// --orientation-exempt/--orientation-exempt-file. Listed separately as INFO
	// findings — never silently dropped.
	OrientationExempted int `json:"orientationExempted"`
	// Polarity-convention outliers (#183 phase 1): caps whose rail-side pin
	// number contradicts the same-page supermajority — the mechanically
	// detectable core of a reversed tantalum that once sailed through this gate.
	PolarityConventionOutliers int `json:"polarityConventionOutliers"`
	Total                      int `json:"total"`
}

type checkReport struct {
	Passed   bool           `json:"passed"`
	Summary  checkSummary   `json:"summary"`
	Findings []checkFinding `json:"findings"`
}

// runSchCheck runs the reconstructed design check, renders it, and (only with
// strict) returns a non-zero exit when there are findings. By default it is
// informational — floating IO pins are normal on an MCU board until NC-marked.
func runSchCheck(cfg *appConfig, window string, allPages, strict, asJSON bool, overlapEps float64, orientationExempt map[string]bool, stdout, stderr io.Writer) error {
	payload := map[string]any{}
	if allPages {
		payload["allPages"] = true
	}
	res, err := requestAction(cfg, "schematic.check", window, payload)
	if err != nil {
		return err
	}

	rep, perr := parseCheckReport(res.Result)
	if perr != nil {
		if b, mErr := json.MarshalIndent(res.Result, "", "  "); mErr == nil {
			_, _ = stdout.Write(b)
			fmt.Fprintln(stdout)
		}
		return perr
	}

	// Go-side geometric marker rules (issues #146/#147/#148): the connector's
	// electrical schematic.check can't see coincident net markers, a netport that
	// landed on the A4 title block, or a marker body overlapping a part/marker —
	// those are pure bbox/anchor geometry. Compute them here from the SAME
	// components.list `sch layout-lint` uses, and fold the findings into the report
	// so `sch check` is the single delivery gate. Best-effort: a components.list
	// failure leaves the electrical findings intact.
	mergeMarkerGeomFindings(cfg, window, allPages, overlapEps, &rep, stderr, orientationExempt)

	if asJSON {
		// Wrap the reconstructed report in the same {id,type,version,ok,result}
		// envelope the transparent commands (sch list/read/place) stream, so a
		// uniform-envelope parser reading result.findings works here too (#66).
		if err := encodeResultEnvelope(res, rep, stdout); err != nil {
			return err
		}
	} else {
		renderCheckReport(rep, stdout)
	}

	// --strict blocks only on blocking-level findings (fatal/error/warn); info
	// findings — e.g. titleblock-overlap against an ESTIMATED (non-A4) keep-out,
	// issue #172 — are advisory and never gate.
	if strict {
		if n := blockingCheckFindings(rep, true); n > 0 {
			return fmt.Errorf("sch check: %d blocking finding(s) (--strict; info 级不计入)", n)
		}
	}
	return nil
}

func parseCheckReport(result map[string]any) (checkReport, error) {
	var rep checkReport
	if result == nil {
		return rep, fmt.Errorf("empty check result")
	}
	b, err := json.Marshal(result)
	if err != nil {
		return rep, err
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		return rep, fmt.Errorf("unexpected check result shape: %w", err)
	}
	return rep, nil
}

// parseOrientationExemptSet 把命令行声明的电源/地朝向豁免拼成一个 primitiveId
// 集合。inline 是逗号分隔的 primitiveId 列表;file 是 JSON 文件,内容可为字符串
// 数组 ["id1","id2"] 或对象数组 [{"primitiveId":"id1","reason":"..."}](reason 仅
// 供审计,参与判定的只有 primitiveId)。两个入口可同时使用,取并集。
func parseOrientationExemptSet(inline, file string) (map[string]bool, error) {
	set := make(map[string]bool)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id != "" {
			set[id] = true
		}
	}
	if strings.TrimSpace(inline) != "" {
		for _, id := range strings.Split(inline, ",") {
			add(id)
		}
	}
	if strings.TrimSpace(file) != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read orientation-exempt file: %w", err)
		}
		var items []any
		if err := json.Unmarshal(b, &items); err != nil {
			return nil, fmt.Errorf("parse orientation-exempt file %q (expect a JSON array of primitiveId strings or {primitiveId,reason} objects): %w", file, err)
		}
		for _, it := range items {
			switch v := it.(type) {
			case string:
				add(v)
			case map[string]any:
				if id, ok := v["primitiveId"].(string); ok {
					add(id)
				}
			}
		}
	}
	return set, nil
}

// checkLevelBlocks is the ONE severity ruler for whether a check finding blocks
// (issue #172): fatal/error always block; warn (and any unknown level — fail
// closed) blocks only under strict; info NEVER blocks — it exists precisely for
// hits against estimated geometry (e.g. a fallback-ratio titleblock keep-out)
// that need human confirmation, and promoting it would re-create the false-
// positive gate the level was introduced to prevent. Both `sch check --strict`
// and the gate's check stage must call this, not re-derive it.
func checkLevelBlocks(level string, strict bool) bool {
	switch strings.ToLower(level) {
	case "fatal", "error":
		return true
	case "info":
		return false
	default:
		return strict
	}
}

// blockingCheckFindings counts the findings that block at the given strictness.
func blockingCheckFindings(rep checkReport, strict bool) int {
	n := 0
	for _, f := range rep.Findings {
		if checkLevelBlocks(f.Level, strict) {
			n++
		}
	}
	return n
}

func checkLevelTag(level string) string {
	switch strings.ToLower(level) {
	case "fatal":
		return "FATAL"
	case "error":
		return "ERROR"
	case "warn":
		return "WARN"
	case "info":
		return "INFO"
	default:
		return "?????"
	}
}

func renderCheckReport(rep checkReport, w io.Writer) {
	s := rep.Summary
	fmt.Fprintf(w, "sch check: %d finding(s) — %d floating pin(s)/%d comp, %d geom-net mismatch(es), %d net-marker mismatch(es), %d multi-net wire(s), %d wire-crossing(s), %d wire-over-pin(s), %d non-orthogonal-wire(s), %d zero-length wire(s), %d dangling wire(s), %d duplicate-net-marker(s), %d titleblock-overlap(s), %d out-of-sheet, %d marker-overlap(s), %d marker-wire-overlap(s), %d visible-text-overlap(s), %d label-visibility(s), %d label-economy(s), %d label-orientation(s), %d direct-connect-opportunity(s), %d power-cluster(s), %d missing-deliverable(partition/titleblock), %d folded-net-label(s), %d redundant-net-marker(s), %d reversed-net-flag(s), %d power-ground-orientation(s), %d orientation-exempted, %d polarity-convention outlier(s)\n",
		s.Total, s.FloatingPins, s.ComponentsWithFloating, s.GeomNetMismatches, s.NetMarkerMismatches, s.MultiNetWires, s.WireCrossings, s.WireOverPins, s.NonOrthogonalWires, s.ZeroLengthWires, s.DanglingWires, s.DuplicateNetMarkers, s.TitleblockOverlaps, s.OutOfSheet, s.MarkerOverlaps, s.MarkerWireOverlaps, s.VisibleTextOverlaps, s.LabelVisibility, s.LabelEconomy, s.LabelOrientations, s.DirectConnectOpportunities, s.PowerClusterCandidates, s.MissingPartitions, s.FoldedNetLabels, s.RedundantNetMarkers, s.ReversedNetFlags, s.PowerGroundOrientation, s.OrientationExempted, s.PolarityConventionOutliers)

	for _, f := range rep.Findings {
		tag := checkLevelTag(f.Level)
		msg := f.Message
		if msg == "" {
			msg = f.Type
		}
		// Rule-type column aligned with renderPcbCheckReport's %-17s style so sch
		// and pcb findings can be grepped/gated the same way by type name.
		line := fmt.Sprintf("  %-5s  %-17s  ", tag, f.Type)
		// Prefer the human designator; fall back to the primitiveId so a finding on
		// a component with an empty designator is still identifiable.
		switch {
		case f.Designator != "":
			line += f.Designator + "  "
		case f.PrimitiveId != "":
			line += f.PrimitiveId + "  "
		case f.WirePrimitiveId != "":
			line += f.WirePrimitiveId + "  "
		case f.MarkerPrimitiveId != "":
			line += f.MarkerPrimitiveId + "  "
		}
		line += msg
		if len(f.Pins) > 0 {
			line += "  [" + strings.Join(f.Pins, ",") + "]"
		}
		if f.WireNet != "" || f.MarkerNet != "" {
			line += fmt.Sprintf("  marker=%s wire=%s", f.MarkerNet, f.WireNet)
		}
		if len(f.Nets) > 0 {
			line += "  nets=[" + strings.Join(f.Nets, ",") + "]"
		}
		if f.At != nil {
			line += fmt.Sprintf("  @(%.2f,%.2f)", f.At.X, f.At.Y)
		}
		fmt.Fprintln(w, line)
		// Per-pin breakdown (floating-pin): pin number/name + coords so the report is
		// actionable without a second lookup.
		for _, pd := range f.PinDetails {
			label := pd.Number
			if pd.Name != "" {
				label += " (" + pd.Name + ")"
			}
			fmt.Fprintf(w, "          pin %s  @(%.2f,%.2f)\n", label, pd.X, pd.Y)
		}
	}

	if rep.Passed {
		if len(rep.Findings) == 0 {
			fmt.Fprintln(w, "✓ no findings")
		} else {
			fmt.Fprintf(w, "✓ no blocking findings (%d advisory finding(s))\n", len(rep.Findings))
		}
		return
	}
	if s.FloatingPins > 0 {
		// The per-pin breakdown printed above is the exact input `sch no-connect`
		// takes, so name it: a hint that stops at "mark intentional ones NC"
		// leaves the reader to discover the command that consumes its own output.
		fmt.Fprintln(w, "→ floating pins: wire them, or mark the intentional ones NC — the listed pins are exactly what `sch no-connect --designator <REF> --pin <n>[,<n>]` takes")
	}
	if s.GeomNetMismatches > 0 {
		// Geometry touches the pin but the authoritative netlist has no net for it —
		// recompile the netlist, or fix the wire so it electrically connects.
		fmt.Fprintln(w, "→ geom-net mismatch: wire touches pin but netlist has no net — recompile netlist, or fix the stub so it truly connects")
	}
	if s.WireCrossings > 0 || s.WireOverPins > 0 {
		fmt.Fprintln(w, "→ routing: reroute crossings in clear channels (L-bends); never run a wire through a pin")
	}
	if s.NonOrthogonalWires > 0 {
		fmt.Fprintln(w, "→ non-orthogonal-wire: split every diagonal/non-axis segment into horizontal/vertical runs, then rerun bridge-check")
	}
	if s.LabelEconomy > 0 {
		fmt.Fprintln(w, "→ label-economy: one label per line island; delete duplicate same-net labels (suggestDeleteIds)")
	}
	if s.LabelVisibility > 0 {
		fmt.Fprintln(w, "→ label-visibility: every netflag/netport/netlabel and wire label must have a visible, non-empty Name")
	}
	if s.LabelOrientations > 0 {
		fmt.Fprintln(w, "→ label-orientation: net-label text must be horizontal; rotate the label or fix its parent wire")
	}
	if s.DirectConnectOpportunities > 0 {
		fmt.Fprintln(w, "→ direct-connect-opportunity: labels on different islands have a clear short L route; prefer a real wire and remove the labels")
	}
	if s.PowerClusterCandidates > 0 {
		fmt.Fprintln(w, "→ power-cluster-candidate: 消费电源脚与本地被动件分属多个线岛、各有同名电源符号 — 先物理接入消费脚，再合并为一个符号；不要直接删符号")
	}
	if s.NetMarkerMismatches > 0 || s.MultiNetWires > 0 {
		fmt.Fprintln(w, "→ net names: fix mismatched flags/ports/labels before treating schematic DRC as clean")
	}
	if s.ZeroLengthWires > 0 || s.DanglingWires > 0 {
		fmt.Fprintln(w, "→ stray wires: delete the zero-length/dangling segments (sch prim-delete --ids <wirePrimitiveId>)")
	}
	if s.DuplicateNetMarkers > 0 {
		fmt.Fprintln(w, "→ duplicate markers: a partial autoconnect stacked coincident flags/ports — sch prim-delete the suggested IDs (keep one)")
	}
	if s.TitleblockOverlaps > 0 {
		fmt.Fprintln(w, "→ title-block: a part/marker intrudes the A4 图签 keep-out — move it out or pick another connect direction")
	}
	if s.MarkerOverlaps > 0 {
		// `sch destagger` exists for exactly this finding and is safe by default
		// (plans only; --apply re-runs this very check per move and rolls the
		// batch back on any electrical regression). Describing the manual fix
		// while withholding the command is the same dead end #227 reported.
		fmt.Fprintln(w, "→ marker overlap: net markers cover a part/each other — `sch destagger` plans the fix (add --apply to land it; it re-verifies and rolls back on any electrical regression), or re-run autoconnect with more offset")
	}
	// 交付三件套共用一个聚合计数槽,所以**提示行不能读那个槽** —— 读了就会在只有
	// missing-titleblock(图签写入当前禁用,必然长亮)的页上印出「→ missing-partition:
	// 没画功能分区框」,引着人反复去查明明画好的框。2026-08-17 已经把汇总行改成
	// missing-deliverable,这一行是同一处错漏下来的另一半(issue #181「恒报 2」)。
	// 逐条明细带真实类型,提示行按真实类型分别给。
	for _, line := range missingDeliverableHints(rep.Findings) {
		fmt.Fprintln(w, line)
	}

	if s.RedundantNetMarkers > 0 {
		fmt.Fprintln(w, "→ redundant-net-marker: 同一线树上同网标志重复(修补残留)— 按 suggestDeleteIds `sch prim-delete` 清冗余(保留一个)")
	}
	if s.FoldedNetLabels > 0 {
		fmt.Fprintln(w, "→ folded-net-label: netport 竖排、网名侧向难读 — `sch disconnect` 后重连(autoconnect 现已惩罚竖排,水平错列优先),或显式 --direction left|right")
	}
	if s.PowerGroundOrientation > 0 {
		fmt.Fprintln(w, "→ power-ground-orientation: 电源/地旗 body 未朝上/下 — 按三层策略重连(直桩/折线),反向脚显式 orientationExempt")
	}
	if s.OrientationExempted > 0 {
		fmt.Fprintln(w, "→ orientation-exempt: 已按反向脚豁免电源/地朝向检查,明细见上方 INFO 行(不静默放过)")
	}
	if s.VisibleTextOverlaps > 0 {
		fmt.Fprintln(w, "→ visible-text-overlap: 可见文字(阻值/网名/说明)压在别的器件、标识或文字上 — 挪开或调属性位置")
	}
	if s.MarkerWireOverlaps > 0 {
		fmt.Fprintln(w, "→ marker-wire-overlap: 电源/地旗或端口本体压在导线上 — 挪开标识或改走线")
	}
	if s.PolarityConventionOutliers > 0 {
		fmt.Fprintln(w, "→ polarity: verify the flagged caps' rail pin against the page majority (钽/电解 reverse = thermal-runaway/fire risk); MLCC numbering is arbitrary — ignore if non-polarized (#183)")
	}
}
