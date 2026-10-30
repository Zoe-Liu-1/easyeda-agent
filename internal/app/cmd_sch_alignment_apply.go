package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

func newSchAlignmentApplyCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var netFilter, planID, out string
	var execute, save bool
	cmd := &cobra.Command{
		Use:   "alignment-apply",
		Short: "Apply one safe alignment plan, including its planned shared-boundary reconnect",
		Long: `Recompute one alignment plan from fresh page geometry, then optionally execute it.

Execution is fail-closed:
  - the selected plan must be rewire-required with proposed boundary plans and no blockers;
  - only the explicitly planned shared wire ids may cross the shared-tree guard;
  - the normal move kernel deletes/recreates owned wiring, moves the component group,
    creates the planned orthogonal boundary wires, and reconciles every pin's net;
  - any kernel failure enters the existing page-wide recovery path.

Without --apply this command only validates and prints the execution envelope.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			item, comps, err := freshAlignmentApplyPlan(pinned, win, doc, netFilter, planID)
			if err != nil {
				return err
			}
			report, err := alignmentApplyEnvelope(item, comps, execute, save)
			if err != nil {
				return err
			}
			report.DocumentID = doc
			raw, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			if out != "" {
				if err := os.WriteFile(out, append(raw, '\n'), 0644); err != nil {
					return fmt.Errorf("write alignment apply report: %w", err)
				}
			} else {
				fmt.Fprintln(stdout, string(raw))
			}
			if !execute {
				fmt.Fprintln(stderr, "alignment-apply: dry-run only; pass --apply to execute")
				return nil
			}
			return executeAlignmentApplyPlan(pinned, win, doc, item, comps, save, stdout, stderr)
		},
	}
	cmd.Flags().StringVar(&netFilter, "net", "", "alignment plan net (required when multiple plans exist)")
	cmd.Flags().StringVar(&planID, "plan", "", "alignment plan id, for example alignment-001")
	cmd.Flags().BoolVar(&execute, "apply", false, "execute the selected plan after preflight")
	cmd.Flags().BoolVar(&save, "save", false, "save the schematic after move, boundary rebuild, and net reconciliation")
	cmd.Flags().StringVar(&out, "out", "", "write the preflight/apply envelope to a JSON file")
	return cmd
}

type alignmentApplyReport struct {
	SchemaVersion     int                 `json:"schemaVersion"`
	DocumentID        string              `json:"documentId"`
	PlanID            string              `json:"planId"`
	Net               string              `json:"net"`
	Status            string              `json:"status"`
	Apply             bool                `json:"apply"`
	Save              bool                `json:"save"`
	AnchorDesignators []string            `json:"anchorDesignators,omitempty"`
	Move              []alignmentApplyPin `json:"moveComponents"`
	Boundary          alignmentBoundary   `json:"boundary"`
}

type alignmentApplyPin struct {
	Designator string  `json:"designator"`
	FromX      float64 `json:"fromX"`
	FromY      float64 `json:"fromY"`
	ToX        float64 `json:"toX"`
	ToY        float64 `json:"toY"`
}

type alignmentBoundary struct {
	SharedWireIDs []string               `json:"sharedWireIds"`
	PinRefs       []string               `json:"pinRefs"`
	Wires         []alignmentPlannedWire `json:"wires"`
}

func freshAlignmentApplyPlan(pinned *appConfig, win, doc, netFilter, planID string) (alignmentPlanItem, []layoutComp, error) {
	geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePins": true, "includePinNets": true, "includePagePrimitives": true,
	})
	if err != nil {
		return alignmentPlanItem{}, nil, err
	}
	if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
		return alignmentPlanItem{}, nil, fmt.Errorf("alignment-apply: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
	}
	comps, err := parseLayoutComps(geom.Result)
	if err != nil {
		return alignmentPlanItem{}, nil, err
	}
	wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
	if err != nil {
		return alignmentPlanItem{}, nil, err
	}
	vt, err := requestAction(pinned, "schematic.visible_text.bboxes", win, nil)
	if err != nil {
		return alignmentPlanItem{}, nil, err
	}
	if vt.Context != nil && vt.Context.DocumentUUID != "" && vt.Context.DocumentUUID != doc {
		return alignmentPlanItem{}, nil, fmt.Errorf("alignment-apply: visible text came from %q, expected %q", vt.Context.DocumentUUID, doc)
	}
	var vision alignmentVision
	raw, _ := json.Marshal(vt.Result)
	if err := json.Unmarshal(raw, &vision); err != nil {
		return alignmentPlanItem{}, nil, fmt.Errorf("alignment-apply: parse visible text: %w", err)
	}
	surveyValue, err := fetchSchVisualSurvey(pinned, win, comps)
	if err != nil {
		return alignmentPlanItem{}, nil, fmt.Errorf("alignment-apply: functional-frame geometry unavailable: %w", err)
	}
	survey, err := parseSchFrameSurvey(surveyValue)
	if err != nil {
		return alignmentPlanItem{}, nil, fmt.Errorf("alignment-apply: parse functional frames: %w", err)
	}
	frames := schModuleFrames(survey)
	sheet := sheetBBoxOf(comps)
	titleBlock, _ := titleBlockKeepoutWithSource(sheet)
	candidates := alignmentOpportunityCandidatesWithFrames(vision.Items, comps, wires, sheet, titleBlock, frames)
	report := buildAlignmentPlanReportWithFrames(doc, netFilter, candidates, comps, vision.Items, wires, sheet, titleBlock, frames)
	var matches []alignmentPlanItem
	for _, item := range report.Plans {
		if planID != "" && item.ID != planID {
			continue
		}
		matches = append(matches, item)
	}
	if len(matches) != 1 {
		return alignmentPlanItem{}, nil, fmt.Errorf("alignment-apply: selected %d plans; pass --net and/or --plan to select exactly one", len(matches))
	}
	return matches[0], comps, nil
}

func alignmentApplyEnvelope(item alignmentPlanItem, comps []layoutComp, apply, save bool) (alignmentApplyReport, error) {
	report := alignmentApplyReport{
		SchemaVersion: 1, PlanID: item.ID, Net: item.Net, Status: item.Status,
		Apply: apply, Save: save, AnchorDesignators: append([]string(nil), item.AnchorDesignators...),
	}
	if item.Status != "rewire-required" {
		return report, fmt.Errorf("alignment-apply: plan %s is %s; only rewire-required plans are executable", item.ID, item.Status)
	}
	if len(item.Blockers) != 0 || len(item.SharedWireIds) == 0 || len(item.BoundaryPlans) == 0 {
		return report, fmt.Errorf("alignment-apply: plan %s lacks a complete blocker-free shared-boundary plan", item.ID)
	}
	byDesignator := map[string]layoutComp{}
	for _, c := range comps {
		if c.Designator != "" {
			byDesignator[c.Designator] = c
		}
	}
	for _, designator := range item.MoveDesignators {
		c, ok := byDesignator[designator]
		if !ok || c.BBox == nil {
			return report, fmt.Errorf("alignment-apply: missing geometry for moving component %s", designator)
		}
		report.Move = append(report.Move, alignmentApplyPin{
			Designator: designator,
			FromX:      c.X, FromY: c.Y,
			ToX: c.X + item.ShiftX, ToY: c.Y + item.ShiftY,
		})
	}
	report.Boundary.SharedWireIDs = append([]string(nil), item.SharedWireIds...)
	pins := map[string]bool{}
	for _, bp := range item.BoundaryPlans {
		if bp.Status != "proposed" || len(bp.Blockers) != 0 || len(bp.Wires) == 0 {
			return report, fmt.Errorf("alignment-apply: boundary net %s is not fully proposed (status=%s blockers=%v wires=%d)",
				bp.Net, bp.Status, bp.Blockers, len(bp.Wires))
		}
		for _, ref := range append(append([]string(nil), bp.FixedPins...), bp.MovedPins...) {
			pins[ref] = true
		}
		for _, wire := range bp.Wires {
			if len(wire.Points) != 4 ||
				(wire.Points[0] != wire.Points[2] && wire.Points[1] != wire.Points[3]) {
				return report, fmt.Errorf("alignment-apply: boundary net %s has invalid wire points %v", bp.Net, wire.Points)
			}
			report.Boundary.Wires = append(report.Boundary.Wires, wire)
		}
	}
	for ref := range pins {
		report.Boundary.PinRefs = append(report.Boundary.PinRefs, strings.Replace(ref, ".", ":", 1))
	}
	sort.Strings(report.Boundary.PinRefs)
	sort.Strings(report.Boundary.SharedWireIDs)
	return report, nil
}

func executeAlignmentApplyPlan(pinned *appConfig, win, doc string,
	item alignmentPlanItem, comps []layoutComp, save bool, stdout, stderr io.Writer) error {
	envelope, err := alignmentApplyEnvelope(item, comps, true, save)
	if err != nil {
		return err
	}
	items := make([]moveItem, 0, len(envelope.Move))
	for _, move := range envelope.Move {
		items = append(items, moveItem{
			Designator: move.Designator, HasTarget: true, X: move.ToX, Y: move.ToY,
		})
	}
	boundary := &moveKernelBoundary{
		SharedWireIDs: append([]string(nil), envelope.Boundary.SharedWireIDs...),
		PinRefs:       append([]string(nil), envelope.Boundary.PinRefs...),
	}
	for _, wire := range envelope.Boundary.Wires {
		boundary.Wires = append(boundary.Wires, moveKernelBoundaryWire{
			Net: wire.Net, Points: append([]float64(nil), wire.Points...),
		})
	}
	rep, err := schMoveKernel(pinned, win, doc, items, moveKernelOpts{
		Label: "alignment-apply", Stdout: stdout, Stderr: stderr, Boundary: boundary,
	})
	if err != nil {
		return err
	}
	if len(rep.StillBroken) > 0 || len(rep.NetDiffs) > 0 || len(rep.NewBridges) > 0 {
		return fmt.Errorf("alignment-apply: post-move reconciliation is not clean (stillBroken=%v netDiffs=%v bridges=%v)",
			rep.StillBroken, rep.NetDiffs, rep.NewBridges)
	}
	if save {
		res, err := requestAction(pinned, "schematic.save", win, nil)
		if err != nil {
			return fmt.Errorf("alignment-apply: save: %w", err)
		}
		saved, _ := res.Result["saved"].(bool)
		if !saved {
			return fmt.Errorf("alignment-apply: schematic.save did not return saved:true")
		}
		fmt.Fprintln(stdout, "✓ alignment-apply saved")
	}
	fmt.Fprintf(stdout, "✓ alignment-apply %s complete: %d component(s), %d boundary wire(s)\n",
		item.ID, len(items), len(boundary.Wires))
	return nil
}
