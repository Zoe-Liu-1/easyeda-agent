package app

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
)

// sheetBoundaryFindings is sch check's sheet-boundary ruler over EVERY drawn
// primitive — parts, markers AND wires. Live 2026-10-13: wires crossed the title
// block and labels left the frame, yet titleblock-overlap (parts/markers only),
// layout-lint (part bodies only) and the electrical rules all stayed at 0; only
// `sch clusters --strict` saw the off-sheet part, through a coarse group bbox.
//
// It reuses the page geometry the other rulers use — sheetBBoxOf, schUsableArea
// and the title-block keep-out — so "inside the sheet" means one thing.
//
//	out-of-sheet        error  part/marker bbox or wire segment leaves the usable area
//	titleblock-overlap  warn   a wire segment enters the title-block keep-out
//	                           (info when the keep-out is an estimate, as for parts)
//
// No sheet (paperless) → no findings: there is no boundary to judge.
func sheetBoundaryFindings(comps []layoutComp, wires []schGroupWire, sheet, titleBlock *layoutBBox, tbSource string, eps float64) []checkFinding {
	if sheet == nil {
		return nil
	}
	usable := schUsableArea(*sheet)
	inside := func(b layoutBBox) bool {
		return b.MinX >= usable.MinX-eps && b.MaxX <= usable.MaxX+eps && b.MinY >= usable.MinY-eps && b.MaxY <= usable.MaxY+eps
	}
	var out []checkFinding
	for _, c := range comps {
		if c.BBox == nil || (c.ComponentType != schLayoutPartType && c.ComponentType != "" && !isSchMarker(c.ComponentType)) {
			continue // the sheet itself and unmeasured primitives are not judged
		}
		if inside(*c.BBox) {
			continue
		}
		b := *c.BBox
		out = append(out, checkFinding{
			Type: "out-of-sheet", Level: "error", PrimitiveId: c.ID, ComponentType: c.ComponentType,
			Designator: c.Designator, MarkerNet: c.Net, BBox: c.BBox,
			Message: fmt.Sprintf("%s(%s) 超出图纸可用区（%.0f..%.0f × %.0f..%.0f，可用区 %.0f..%.0f × %.0f..%.0f）— 打印不出来，移回图内",
				markerLabel(c), c.ComponentType, b.MinX, b.MaxX, b.MinY, b.MaxY, usable.MinX, usable.MaxX, usable.MinY, usable.MaxY),
		})
	}
	var inner *layoutBBox
	if titleBlock != nil {
		// Only positive intrusion counts; a wire running along the keep-out edge does not.
		in := layoutBBox{MinX: titleBlock.MinX + eps, MinY: titleBlock.MinY + eps, MaxX: titleBlock.MaxX - eps, MaxY: titleBlock.MaxY - eps}
		if in.MaxX > in.MinX && in.MaxY > in.MinY {
			inner = &in
		}
	}
	confirmed := tbSource == sheetSourceKnownTemplate
	for _, w := range wires {
		offSheet, inTitle := false, false
		for _, s := range schDesignatorWireSegments(w) {
			seg := layoutBBox{MinX: math.Min(s[0], s[2]), MaxX: math.Max(s[0], s[2]), MinY: math.Min(s[1], s[3]), MaxY: math.Max(s[1], s[3])}
			if !offSheet && !inside(seg) {
				offSheet = true
				out = append(out, checkFinding{
					Type: "out-of-sheet", Level: "error", WirePrimitiveId: w.ID,
					At:      &checkPoint{X: round2(s[0]), Y: round2(s[1])},
					Message: fmt.Sprintf("导线 %s 的线段 (%.0f,%.0f)→(%.0f,%.0f) 超出图纸可用区 — 连同它挂的标签移回图内", w.ID, s[0], s[1], s[2], s[3]),
				})
			}
			if !inTitle && inner != nil && schSegmentCrossesBox(s[0], s[1], s[2], s[3], *inner) {
				inTitle = true
				level, msg := "warn", fmt.Sprintf("导线 %s 穿过标题栏 keep-out（线段 (%.0f,%.0f)→(%.0f,%.0f)）— 改走线或挪开所连器件", w.ID, s[0], s[1], s[2], s[3])
				if !confirmed {
					level = "info"
					msg = fmt.Sprintf("导线 %s 命中基于估计的图签区（source=%s）— keep-out 非 A4 标定，仅供参考", w.ID, tbSource)
				}
				out = append(out, checkFinding{
					Type: "titleblock-overlap", Level: level, WirePrimitiveId: w.ID, Keepout: titleBlock,
					At: &checkPoint{X: round2(s[0]), Y: round2(s[1])}, Message: msg,
				})
			}
		}
	}
	return out
}

// visibleTextItem is one measured entry of schematic.visible_text.bboxes.
type visibleTextItem struct {
	ID            string      `json:"id"`
	Kind          string      `json:"kind"` // text | attribute
	Content       string      `json:"content"`
	ParentID      string      `json:"parentId"`
	ComponentType string      `json:"componentType"`
	ParentType    string      `json:"parentType"`
	Designator    string      `json:"designator"`
	Key           string      `json:"key"`
	Value         string      `json:"value"`
	Rotation      *float64    `json:"rotation"`
	Visible       *bool       `json:"visible"`
	MissingName   bool        `json:"missingName"`
	BBox          *layoutBBox `json:"bbox"`
}

// visibleTextBoundaryFindings extends the sheet-boundary ruler to every visible
// text: free text and shown attributes (parts' Name/Value…, the sheet's own
// title-block fields). Live 2026-10-13: showing the title-block Name and
// Description drew them below the frame and no rule saw it. Text is judged
// against the PAPER, not the 12-raw placement margin of schUsableArea: frame
// titles legitimately sit in that margin, text past the paper edge is lost.
func visibleTextBoundaryFindings(items []visibleTextItem, sheet *layoutBBox, eps float64) []checkFinding {
	if sheet == nil {
		return nil
	}
	paper := *sheet
	var out []checkFinding
	for _, it := range items {
		b := it.BBox
		if b == nil || (b.MinX >= paper.MinX-eps && b.MaxX <= paper.MaxX+eps && b.MinY >= paper.MinY-eps && b.MaxY <= paper.MaxY+eps) {
			continue
		}
		what := "文字「" + it.Content + "」"
		if it.Kind == "attribute" {
			owner := it.Designator
			if owner == "" {
				owner = it.ComponentType
			}
			what = fmt.Sprintf("%s 的可见属性 %s", owner, it.Key)
		}
		out = append(out, checkFinding{
			Type: "out-of-sheet", Level: "warn", PrimitiveId: it.ID, Designator: it.Designator, BBox: b,
			Message: fmt.Sprintf("%s 超出纸张（%.0f..%.0f × %.0f..%.0f）— 打印不出来；移回图内，或不需要显示时关闭该属性的显示",
				what, b.MinX, b.MaxX, b.MinY, b.MaxY),
		})
	}
	return out
}

// visibleTextFindings reads the measured visible text and applies the ruler.
// An older connector without the action is reported once on stderr (no
// finding: the rule simply does not exist there); a failed read or items the
// host could not measure are surfaced, never treated as "inside".
func visibleTextFindings(cfg *appConfig, window string, sheet *layoutBBox, comps []layoutComp, wires []schGroupWire, eps float64, stderr io.Writer) []checkFinding {
	if sheet == nil {
		return nil
	}
	res, err := requestAction(cfg, "schematic.visible_text.bboxes", window, map[string]any{})
	if err != nil {
		if res != nil && res.errorCode == "UNKNOWN_ACTION" {
			fmt.Fprintln(stderr, "sch check: visible-text out-of-sheet skipped — connector predates schematic.visible_text.bboxes")
			return nil
		}
		return []checkFinding{{Type: "visible-text-unverified", Level: "warn", Message: "可见文字/属性是否越出图纸无法核验（读取失败）: " + err.Error()}}
	}
	var parsed struct {
		Items      []visibleTextItem `json:"items"`
		Unmeasured []visibleTextItem `json:"unmeasured"`
	}
	raw, _ := json.Marshal(res.Result)
	if json.Unmarshal(raw, &parsed) != nil {
		return []checkFinding{{Type: "visible-text-unverified", Level: "warn", Message: "可见文字/属性测量结果无法解析"}}
	}
	out := visibleTextBoundaryFindings(parsed.Items, sheet, eps)
	out = append(out, visibleTextOverlapFindings(parsed.Items, comps, wires, eps)...)
	out = append(out, netLabelVisibilityFindings(parsed.Items)...)
	out = append(out, netLabelOrientationFindings(parsed.Items)...)
	out = append(out, netLabelEconomyFindings(parsed.Items, wires)...)
	out = append(out, directConnectOpportunityFindings(parsed.Items, comps, wires)...)
	if n := len(parsed.Unmeasured); n > 0 {
		out = append(out, checkFinding{Type: "visible-text-unverified", Level: "info",
			Message: fmt.Sprintf("%d 个可见文字/属性宿主未给出 bbox，越界情况未核验", n)})
	}
	return out
}

// visibleTextOverlapFindings 把 schematic.visible_text.bboxes 测到的可见文字
// （自由文字 + 器件可见属性，含 Value/阻值、Name/网名）与器件本体、网络标识的
// bbox 做重叠判断。属性压自己的父器件不算（属性文字本来就画在自己器件附近）；
// 跨 owner 的文字 vs 文字重叠同样报。net label 是导线 Name 属性，只排除它自己
// 挂载的那根线；Designator 的导线碰撞由 designator-wire-overlap 单独报告，避免重复。
func visibleTextOverlapFindings(items []visibleTextItem, comps []layoutComp, wires []schGroupWire, eps float64) []checkFinding {
	type obstacle struct {
		id, what string
		bbox     *layoutBBox
	}
	var obstacles []obstacle
	for _, c := range comps {
		if c.BBox == nil {
			continue
		}
		switch c.ComponentType {
		case "part":
			d := c.Designator
			if d == "" {
				d = c.ID
			}
			obstacles = append(obstacles, obstacle{c.ID, "器件 " + d, c.BBox})
		case "netflag", "netport":
			obstacles = append(obstacles, obstacle{c.ID, "标识 " + c.Net, c.BBox})
		}
	}
	describe := func(it visibleTextItem) string {
		if it.Kind == "attribute" {
			if it.ComponentType == "wire" && it.Key == "Name" {
				return "网络标签「" + it.Value + "」"
			}
			owner := it.Designator
			if owner == "" {
				owner = it.ComponentType
			}
			return fmt.Sprintf("%s 的可见属性 %s", owner, it.Key)
		}
		return "文字「" + it.Content + "」"
	}
	overlap := func(a, b *layoutBBox) (float64, float64) {
		ix := math.Min(a.MaxX, b.MaxX) - math.Max(a.MinX, b.MinX)
		iy := math.Min(a.MaxY, b.MaxY) - math.Max(a.MinY, b.MinY)
		return ix, iy
	}
	var out []checkFinding
	for _, it := range items {
		if it.BBox == nil {
			continue
		}
		for _, o := range obstacles {
			if it.ParentID == o.id {
				continue
			}
			ix, iy := overlap(it.BBox, o.bbox)
			if ix > eps && iy > eps {
				out = append(out, checkFinding{
					Type: "visible-text-overlap", Level: "warn", PrimitiveId: it.ID,
					Designator: it.Designator, BBox: it.BBox,
					Message: fmt.Sprintf("%s 压在 %s 上（重叠 %.0f×%.0f）— 挪开或调整属性位置",
						describe(it), o.what, ix, iy),
				})
				break
			}
		}
	}
	for _, it := range items {
		if it.BBox == nil || it.Key == "Designator" {
			continue
		}
		for _, w := range wires {
			if it.ComponentType == "wire" && it.ParentID == w.ID {
				continue // net label on its own parent wire
			}
			hit := false
			for _, seg := range schDesignatorWireSegments(w) {
				if schSegmentCrossesBox(seg[0], seg[1], seg[2], seg[3], *it.BBox) {
					out = append(out, checkFinding{
						Type: "visible-text-overlap", Level: "warn",
						PrimitiveId: it.ID, Designator: it.Designator,
						WirePrimitiveId: w.ID, BBox: it.BBox,
						Message: fmt.Sprintf("%s 压在导线 %s 上（线段 (%.0f,%.0f)→(%.0f,%.0f)）— 挪开文字或改走线",
							describe(it), w.ID, seg[0], seg[1], seg[2], seg[3]),
					})
					hit = true
					break
				}
			}
			if hit {
				break
			}
		}
	}
	for i := 0; i < len(items); i++ {
		a := items[i]
		if a.BBox == nil {
			continue
		}
		for j := i + 1; j < len(items); j++ {
			b := items[j]
			if b.BBox == nil || (a.ParentID != "" && a.ParentID == b.ParentID) {
				continue
			}
			ix, iy := overlap(a.BBox, b.BBox)
			if ix > eps && iy > eps {
				out = append(out, checkFinding{
					Type: "visible-text-overlap", Level: "warn", PrimitiveId: a.ID,
					Designator: a.Designator, BBox: a.BBox,
					Message: fmt.Sprintf("%s 与 %s 文字重叠（%.0f×%.0f）",
						describe(a), describe(b), ix, iy),
				})
				break
			}
		}
	}
	return out
}
