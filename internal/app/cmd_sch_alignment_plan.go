package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/spf13/cobra"
)

type alignmentPlanReport struct {
	SchemaVersion  int                 `json:"schemaVersion"`
	DocumentID     string              `json:"documentId"`
	Net            string              `json:"net,omitempty"`
	Plans          []alignmentPlanItem `json:"plans"`
	Ready          int                 `json:"ready"`
	RewireRequired int                 `json:"rewireRequired"`
	Blocked        int                 `json:"blocked"`
}

type alignmentPlanItem struct {
	ID                  string              `json:"id"`
	Net                 string              `json:"net"`
	Status              string              `json:"status"` // ready | rewire-required | blocked
	Axis                string              `json:"axis"`
	ShiftX              float64             `json:"shiftX,omitempty"`
	ShiftY              float64             `json:"shiftY,omitempty"`
	BendReduction       int                 `json:"bendReduction"`
	MoveDesignators     []string            `json:"moveDesignators"`
	MoveComponentIds    []string            `json:"moveComponentIds"`
	MoveWireIds         []string            `json:"moveWireIds,omitempty"`
	MoveMarkerIds       []string            `json:"moveMarkerIds,omitempty"`
	MoveLabelIds        []string            `json:"moveLabelIds,omitempty"`
	SharedWireIds       []string            `json:"sharedWireIds,omitempty"`
	Blockers            []string            `json:"blockers,omitempty"`
	IndependentBlockers []string            `json:"independentBlockers,omitempty"`
	Steps               []alignmentPlanStep `json:"steps"`
	At                  *checkPoint         `json:"at,omitempty"`
	BBox                *layoutBBox         `json:"bbox,omitempty"`
	Note                string              `json:"note,omitempty"`
}

type alignmentPlanStep struct {
	Order         int      `json:"order"`
	Kind          string   `json:"kind"`
	Action        string   `json:"action,omitempty"`
	Designators   []string `json:"designators,omitempty"`
	PrimitiveIds  []string `json:"primitiveIds,omitempty"`
	DX            float64  `json:"dx,omitempty"`
	DY            float64  `json:"dy,omitempty"`
	Required      bool     `json:"required"`
	ExecutableNow bool     `json:"executableNow"`
	Note          string   `json:"note,omitempty"`
}

type alignmentVision struct {
	Items []visibleTextItem `json:"items"`
}

func newSchAlignmentPlanCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var jsonOut bool
	var out, netFilter string
	cmd := &cobra.Command{
		Use:   "alignment-plan",
		Short: "Plan rigid group moves that remove unnecessary H-V-H/V-H-V jogs (read-only)",
		Long: `Read the active page and emit read-only plans derived from sch-check alignment-opportunity findings.
Each plan contains the complete movable functional group, a translation vector, owned wires,
markers, labels, shared boundary wires, blockers, and independent prerequisite blockers.
The command never calls modify, group.move, wire, connect, delete or save.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
				"includeBBox": true, "includePins": true, "includePinNets": true, "includePagePrimitives": true,
			})
			if err != nil {
				return err
			}
			if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
				return fmt.Errorf("alignment-plan: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
			}
			comps, err := parseLayoutComps(geom.Result)
			if err != nil {
				return err
			}
			wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
			if err != nil {
				return err
			}
			vt, err := requestAction(pinned, "schematic.visible_text.bboxes", win, nil)
			if err != nil {
				return err
			}
			if vt.Context != nil && vt.Context.DocumentUUID != "" && vt.Context.DocumentUUID != doc {
				return fmt.Errorf("alignment-plan: visible text came from %q, expected %q", vt.Context.DocumentUUID, doc)
			}
			var items alignmentVision
			raw, _ := json.Marshal(vt.Result)
			if err := json.Unmarshal(raw, &items); err != nil {
				return fmt.Errorf("alignment-plan: parse visible text: %w", err)
			}
			sheet := sheetBBoxOf(comps)
			titleBlock, _ := titleBlockKeepoutWithSource(sheet)
			candidates := alignmentOpportunityCandidates(items.Items, comps, wires, sheet, titleBlock)
			report := buildAlignmentPlanReport(doc, netFilter, candidates, comps)
			if out != "" {
				b, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return err
				}
				b = append(b, '\n')
				if err := os.WriteFile(out, b, 0644); err != nil {
					return fmt.Errorf("write alignment plan: %w", err)
				}
				fmt.Fprintf(stderr, "alignment plan written to %s (%d plan(s))\n", out, len(report.Plans))
				return nil
			}
			if jsonOut {
				b, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return err
				}
				_, err = stdout.Write(append(b, '\n'))
				return err
			}
			renderAlignmentPlan(stdout, report)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit structured JSON")
	cmd.Flags().StringVar(&out, "out", "", "write structured JSON to a file")
	cmd.Flags().StringVar(&netFilter, "net", "", "only plan opportunities for this net")
	return cmd
}

func buildAlignmentPlanReport(doc, netFilter string, candidates []alignmentCandidate, comps []layoutComp) alignmentPlanReport {
	report := alignmentPlanReport{SchemaVersion: 1, DocumentID: doc, Net: netFilter, Plans: []alignmentPlanItem{}}
	partIDs := map[string]string{}
	for _, c := range comps {
		if c.Designator != "" {
			partIDs[c.Designator] = c.ID
		}
	}
	for _, c := range candidates {
		if netFilter != "" && c.Net != netFilter {
			continue
		}
		item := alignmentPlanItem{
			ID:  fmt.Sprintf("alignment-%03d", len(report.Plans)+1),
			Net: c.Net, Axis: c.Axis, ShiftX: 0, ShiftY: 0, BendReduction: 2,
			MoveDesignators:     append([]string(nil), c.MoveDesignators...),
			MoveWireIds:         append([]string(nil), c.MoveWireIDs...),
			MoveMarkerIds:       append([]string(nil), c.MoveMarkerIDs...),
			MoveLabelIds:        append([]string(nil), c.MoveLabelIDs...),
			SharedWireIds:       append([]string(nil), c.SharedWireIDs...),
			Blockers:            append([]string(nil), c.Blockers...),
			IndependentBlockers: append([]string(nil), c.IndependentBlockers...),
			At:                  &c.At, BBox: &c.MovedBBox,
		}
		if c.Axis == "x" {
			item.ShiftX = c.Shift
		} else {
			item.ShiftY = c.Shift
		}
		var componentIDs []string
		for _, d := range c.MoveDesignators {
			if id := partIDs[d]; id != "" {
				componentIDs = append(componentIDs, id)
			}
		}
		sort.Strings(componentIDs)
		item.MoveComponentIds = componentIDs
		switch {
		case len(item.Blockers) > 0:
			item.Status = "blocked"
			item.Note = "先处理 blockers；independentBlockers 可尝试作为单独组前置移动"
			report.Blocked++
		case len(item.SharedWireIds) > 0:
			item.Status = "rewire-required"
			item.Note = "移动后必须重建 sharedWireIds 的边界连接"
			report.RewireRequired++
		default:
			item.Status = "ready"
			item.Note = "组内没有外部阻塞或共享边界线"
			report.Ready++
		}
		item.Steps = alignmentPlanSteps(item, c)
		report.Plans = append(report.Plans, item)
	}
	return report
}

func alignmentPlanSteps(item alignmentPlanItem, candidate alignmentCandidate) []alignmentPlanStep {
	var steps []alignmentPlanStep
	order := 0
	add := func(kind, action string, designators, ids []string, dx, dy float64, required, executable bool, note string) {
		order++
		steps = append(steps, alignmentPlanStep{
			Order: order, Kind: kind, Action: action, Designators: designators,
			PrimitiveIds: ids, DX: dx, DY: dy, Required: required,
			ExecutableNow: executable, Note: note,
		})
	}
	if len(item.IndependentBlockers) > 0 {
		add("move-independent-blockers", "", item.IndependentBlockers, nil, 0, 0, true, false,
			"先为这些独立块选择落点并整体移动；当前 planner 不猜测目标坐标")
	}
	moveIDs := append([]string(nil), item.MoveComponentIds...)
	moveIDs = append(moveIDs, item.MoveWireIds...)
	moveIDs = append(moveIDs, item.MoveMarkerIds...)
	sort.Strings(moveIDs)
	add("move-owned-group", "schematic.group.move", nil, moveIDs,
		candidateShiftX(candidate), candidateShiftY(candidate), true,
		item.Status == "ready", "专属导线和标志随组平移；导线标签由 group.move 重建")
	if len(item.SharedWireIds) > 0 {
		add("rewire-shared-boundary", "", nil, item.SharedWireIds, 0, 0, true, false,
			"共享线触及固定器件，不能随组移动；必须按移动后的真实端点重新生成边界线")
	}
	add("verify-check", "sch check", nil, nil, 0, 0, true, item.Status == "ready",
		"要求没有新增 overlap/crossing/net/click failures")
	add("verify-bridge", "sch bridge-check", nil, nil, 0, 0, true, item.Status == "ready",
		"确认移动和边界重连没有断开或桥接")
	add("save-readback", "sch save", nil, nil, 0, 0, true, item.Status == "ready",
		"保存后重载并重新读取几何、连接和渲染")
	return steps
}

func candidateShiftX(c alignmentCandidate) float64 {
	if c.Axis == "x" {
		return c.Shift
	}
	return 0
}

func candidateShiftY(c alignmentCandidate) float64 {
	if c.Axis == "y" {
		return c.Shift
	}
	return 0
}

func renderAlignmentPlan(w io.Writer, report alignmentPlanReport) {
	fmt.Fprintf(w, "alignment-plan: %d plan(s) — %d ready, %d rewire-required, %d blocked\n",
		len(report.Plans), report.Ready, report.RewireRequired, report.Blocked)
	for _, p := range report.Plans {
		fmt.Fprintf(w, "  %-12s %-5s %-20s shift=(%g,%g)  %s\n",
			p.ID, p.Status, p.Net, p.ShiftX, p.ShiftY, stringsJoinNonEmpty(p.MoveDesignators))
		if len(p.Blockers) > 0 {
			fmt.Fprintf(w, "    blockers: %s\n", stringsJoinNonEmpty(p.Blockers))
		}
		if len(p.SharedWireIds) > 0 {
			fmt.Fprintf(w, "    shared boundary wires: %s\n", stringsJoinNonEmpty(p.SharedWireIds))
		}
		for _, step := range p.Steps {
			state := "planned"
			if !step.ExecutableNow {
				state = "not-executable-yet"
			}
			fmt.Fprintf(w, "    %d. %-26s %s\n", step.Order, step.Kind, state)
		}
	}
}

func stringsJoinNonEmpty(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	out := values[0]
	for _, value := range values[1:] {
		out += "," + value
	}
	return out
}
