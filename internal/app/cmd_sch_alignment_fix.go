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

type alignmentFixScene struct {
	projectID string
	comps     []layoutComp
	wires     []schGroupWire
	items     []visibleTextItem
	sheet     *layoutBBox
	title     *layoutBBox
	report    alignmentPlanReport
}

type alignmentFixReport struct {
	SchemaVersion   int                    `json:"schemaVersion"`
	DocumentID      string                 `json:"documentId"`
	PlanID          string                 `json:"planId"`
	Net             string                 `json:"net"`
	InitialStatus   string                 `json:"initialStatus"`
	FinalStatus     string                 `json:"finalStatus,omitempty"`
	BlockerPlans    []alignmentBlockerPlan `json:"blockerPlans,omitempty"`
	AppliedBlockers []string               `json:"appliedBlockers,omitempty"`
	MainMoved       bool                   `json:"mainMoved"`
	Applied         bool                   `json:"applied"`
	Saved           bool                   `json:"saved,omitempty"`
	RolledBack      bool                   `json:"rolledBack,omitempty"`
	RollbackError   string                 `json:"rollbackError,omitempty"`
	Notes           []string               `json:"notes,omitempty"`
}

type appliedAlignmentBlocker struct {
	Designators []string
	DX          float64
	DY          float64
}

func newSchAlignmentFixCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var netFilter, planID, out string
	var apply, save bool
	cmd := &cobra.Command{
		Use:   "alignment-fix",
		Short: "Execute ready independent blockers, re-plan, then execute the alignment main group",
		Long: `Read one fresh alignment plan and execute the prerequisite chain:
  1. every known independent blocker group must be ready; review-required and blocked plans stop preflight;
  2. move ready blocker groups one at a time through the shared move kernel;
  3. after every move, re-read the page and re-plan the same main group;
  4. execute a final ready group, or a rewire-required group through its planned shared-boundary reconnect.

The complete pin-to-net map and bridge set must remain equal to the command baseline
after every blocker move and after the final group. Without --apply this command is
read-only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			scene, err := readAlignmentFixScene(pinned, win, doc, netFilter)
			if err != nil {
				return err
			}
			item, err := selectAlignmentFixPlan(scene.report, planID, nil)
			if err != nil {
				return err
			}
			if planID == "" {
				planID = item.ID
			}
			report := alignmentFixReport{
				SchemaVersion: 1, DocumentID: doc, PlanID: planID, Net: item.Net,
				InitialStatus: item.Status, BlockerPlans: append([]alignmentBlockerPlan(nil), item.IndependentBlockerPlans...),
			}
			if err := validateAlignmentBlockerPreflight(item); err != nil {
				report.Notes = append(report.Notes, err.Error())
				_ = writeAlignmentFixReport(stdout, out, report)
				return err
			}
			if err := writeAlignmentFixReport(stdout, out, report); err != nil {
				return err
			}
			if !apply {
				fmt.Fprintln(stderr, "alignment-fix: dry-run only; pass --apply to execute the blocker chain")
				return nil
			}
			if err := executeAlignmentFix(pinned, win, doc, scene, item, &report, save, stdout, stderr); err != nil {
				return err
			}
			return writeAlignmentFixReport(stdout, out, report)
		},
	}
	cmd.Flags().StringVar(&netFilter, "net", "", "alignment plan net")
	cmd.Flags().StringVar(&planID, "plan", "", "alignment plan id; omitted with --net when unique")
	cmd.Flags().BoolVar(&apply, "apply", false, "execute ready blockers, re-plan, and execute the main group")
	cmd.Flags().BoolVar(&save, "save", false, "save after final connectivity and bridge verification")
	cmd.Flags().StringVar(&out, "out", "", "write structured report to JSON")
	return cmd
}

func writeAlignmentFixReport(stdout io.Writer, out string, report alignmentFixReport) error {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if out != "" {
		return os.WriteFile(out, raw, 0644)
	}
	fmt.Fprintln(stdout, string(raw))
	return nil
}

func readAlignmentFixScene(pinned *appConfig, win, doc, netFilter string) (alignmentFixScene, error) {
	geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePins": true, "includePinNets": true, "includePagePrimitives": true,
	})
	if err != nil {
		return alignmentFixScene{}, err
	}
	if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
		return alignmentFixScene{}, fmt.Errorf("alignment-fix: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
	}
	comps, err := parseLayoutComps(geom.Result)
	if err != nil {
		return alignmentFixScene{}, err
	}
	wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
	if err != nil {
		return alignmentFixScene{}, err
	}
	vt, err := requestAction(pinned, "schematic.visible_text.bboxes", win, nil)
	if err != nil {
		return alignmentFixScene{}, err
	}
	if vt.Context != nil && vt.Context.DocumentUUID != "" && vt.Context.DocumentUUID != doc {
		return alignmentFixScene{}, fmt.Errorf("alignment-fix: visible text came from %q, expected %q", vt.Context.DocumentUUID, doc)
	}
	var vision alignmentVision
	raw, _ := json.Marshal(vt.Result)
	if err := json.Unmarshal(raw, &vision); err != nil {
		return alignmentFixScene{}, fmt.Errorf("alignment-fix: parse visible text: %w", err)
	}
	sheet := sheetBBoxOf(comps)
	titleBlock, _ := titleBlockKeepoutWithSource(sheet)
	candidates := alignmentOpportunityCandidates(vision.Items, comps, wires, sheet, titleBlock)
	report := buildAlignmentPlanReport(doc, netFilter, candidates, comps, vision.Items, wires, sheet, titleBlock)
	projectID := ""
	if geom.Context != nil {
		projectID = geom.Context.ProjectUUID
	}
	return alignmentFixScene{
		projectID: projectID, comps: comps, wires: wires, items: vision.Items,
		sheet: sheet, title: titleBlock, report: report,
	}, nil
}

func selectAlignmentFixPlan(report alignmentPlanReport, planID string, identity *alignmentPlanItem) (alignmentPlanItem, error) {
	var matches []alignmentPlanItem
	for _, item := range report.Plans {
		if planID != "" && item.ID != planID {
			continue
		}
		if identity != nil && !sameAlignmentMoveGroup(item.MoveDesignators, identity.MoveDesignators) {
			continue
		}
		matches = append(matches, item)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if identity != nil {
		return alignmentPlanItem{}, fmt.Errorf("alignment-fix: cannot uniquely re-identify main group %s after re-plan (%d matches)",
			strings.Join(identity.MoveDesignators, ","), len(matches))
	}
	return alignmentPlanItem{}, fmt.Errorf("alignment-fix: selected %d plans; pass --net and/or --plan", len(matches))
}

func sameAlignmentMoveGroup(a, b []string) bool {
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	return strings.Join(aa, ",") == strings.Join(bb, ",")
}

func validateAlignmentBlockerPreflight(item alignmentPlanItem) error {
	if item.Status != "blocked" {
		return nil
	}
	if len(item.IndependentBlockerPlans) == 0 {
		return fmt.Errorf("alignment-fix: plan %s is blocked without an executable independent blocker plan: %s",
			item.ID, strings.Join(item.Blockers, ","))
	}
	var notReady []string
	for _, blocker := range item.IndependentBlockerPlans {
		if blocker.Status != "ready" {
			notReady = append(notReady, fmt.Sprintf("%s=%s", strings.Join(blocker.InitialDesignators, "+"), blocker.Status))
		}
	}
	if len(notReady) > 0 {
		return fmt.Errorf("alignment-fix: blocker preflight refused before write: %s", strings.Join(notReady, ","))
	}
	return nil
}

func executeAlignmentFix(pinned *appConfig, win, doc string, scene alignmentFixScene,
	initial alignmentPlanItem, report *alignmentFixReport, save bool, stdout, stderr io.Writer) error {
	beforeNets, _, err := readLiveNets(pinned, win)
	if err != nil {
		return fmt.Errorf("alignment-fix: baseline netlist: %w", err)
	}
	beforeBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("alignment-fix: baseline bridge set: %w", err)
	}

	identity := initial
	current := initial
	movedKeys := map[string]bool{}
	var movedGroups []appliedAlignmentBlocker
	rollbackBlockers := func(cause error) error {
		if len(movedGroups) == 0 {
			return cause
		}
		rbErr := rollbackAlignmentBlockers(pinned, win, doc, movedGroups)
		report.RolledBack = rbErr == nil
		if rbErr != nil {
			report.RollbackError = rbErr.Error()
			return fmt.Errorf("%v; blocker rollback FAILED (%v) — inspect fresh page before retry", cause, rbErr)
		}
		return fmt.Errorf("%v; blocker moves were rolled back", cause)
	}

	for iteration := 0; iteration < len(initial.IndependentBlockerPlans)+2; iteration++ {
		if current.Status != "blocked" {
			break
		}
		if err := validateAlignmentBlockerPreflight(current); err != nil {
			return rollbackBlockers(err)
		}
		var next *alignmentBlockerPlan
		for i := range current.IndependentBlockerPlans {
			key := strings.Join(current.IndependentBlockerPlans[i].InitialDesignators, ",")
			if !movedKeys[key] {
				next = &current.IndependentBlockerPlans[i]
				break
			}
		}
		if next == nil {
			return rollbackBlockers(fmt.Errorf("alignment-fix: plan remains blocked after all prepared blocker moves: %s",
				strings.Join(current.Blockers, ",")))
		}
		key := strings.Join(next.InitialDesignators, ",")
		if err := executeAlignmentBlockerMove(pinned, win, doc, current, *next, stderr); err != nil {
			return rollbackBlockers(err)
		}
		movedKeys[key] = true
		movedGroups = append(movedGroups, appliedAlignmentBlocker{
			Designators: append([]string(nil), next.MoveDesignators...),
			DX:          next.DX, DY: next.DY,
		})
		report.AppliedBlockers = append(report.AppliedBlockers, key)
		if err := verifyAlignmentFixBaseline(pinned, win, doc, beforeNets, beforeBridges); err != nil {
			return rollbackBlockers(err)
		}
		scene, err = readAlignmentFixScene(pinned, win, doc, identity.Net)
		if err != nil {
			return rollbackBlockers(fmt.Errorf("re-plan after blocker %s: %w", key, err))
		}
		current, err = selectAlignmentFixPlan(scene.report, "", &identity)
		if err != nil {
			return rollbackBlockers(err)
		}
	}

	if current.Status == "blocked" {
		return rollbackBlockers(fmt.Errorf("alignment-fix: main plan remains blocked after blocker chain: %s",
			strings.Join(current.Blockers, ",")))
	}
	if current.Status != "ready" && current.Status != "rewire-required" {
		return rollbackBlockers(fmt.Errorf("alignment-fix: main plan status %s is not executable", current.Status))
	}
	report.FinalStatus = current.Status
	if err := executeAlignmentFixMain(pinned, win, doc, scene, current, stderr); err != nil {
		return err
	}
	report.MainMoved = true
	if err := verifyAlignmentFixBaseline(pinned, win, doc, beforeNets, beforeBridges); err != nil {
		return err
	}
	if save {
		saved, serr := requestAutolayoutAction(pinned, "schematic.save", win, nil, doc, "alignment-fix 保存")
		if serr != nil {
			return fmt.Errorf("alignment-fix: save: %w", serr)
		}
		ok, _ := saved.Result["saved"].(bool)
		if !ok {
			return fmt.Errorf("alignment-fix: schematic.save did not return saved:true")
		}
		report.Saved = true
	}
	report.Applied = true
	fmt.Fprintf(stdout, "✓ alignment-fix %s complete: %d blocker group(s), main=%s\n",
		identity.Net, len(report.AppliedBlockers), current.Status)
	return nil
}

func executeAlignmentBlockerMove(pinned *appConfig, win, doc string, item alignmentPlanItem,
	blocker alignmentBlockerPlan, stderr io.Writer) error {
	if blocker.Status != "ready" || len(blocker.SharedWireIds) > 0 {
		return fmt.Errorf("alignment-fix: blocker %s is not ready (%s shared=%v)",
			strings.Join(blocker.InitialDesignators, "+"), blocker.Status, blocker.SharedWireIds)
	}
	geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePins": true, "includePinNets": false,
	})
	if err != nil {
		return err
	}
	comps, err := parseLayoutComps(geom.Result)
	if err != nil {
		return err
	}
	byDesig := map[string]layoutComp{}
	for _, c := range comps {
		if c.Designator != "" {
			byDesig[c.Designator] = c
		}
	}
	var items []moveItem
	for _, d := range blocker.MoveDesignators {
		c, ok := byDesig[d]
		if !ok {
			return fmt.Errorf("alignment-fix: blocker part %s missing", d)
		}
		items = append(items, moveItem{Designator: d, HasTarget: true, X: c.X + blocker.DX, Y: c.Y + blocker.DY})
	}
	_, err = schMoveKernel(pinned, win, doc, items, moveKernelOpts{
		Label: "alignment-fix-blocker", Stdout: io.Discard, Stderr: stderr, StubPolicy: moveStubPreserve,
	})
	return err
}

func executeAlignmentFixMain(pinned *appConfig, win, doc string, scene alignmentFixScene,
	item alignmentPlanItem, stderr io.Writer) error {
	if item.Status == "rewire-required" {
		return executeAlignmentApplyPlan(pinned, win, doc, item, scene.comps, false, io.Discard, stderr)
	}
	byDesig := map[string]layoutComp{}
	for _, c := range scene.comps {
		if c.Designator != "" {
			byDesig[c.Designator] = c
		}
	}
	var items []moveItem
	for _, d := range item.MoveDesignators {
		c, ok := byDesig[d]
		if !ok {
			return fmt.Errorf("alignment-fix: main part %s missing", d)
		}
		items = append(items, moveItem{Designator: d, HasTarget: true, X: c.X + item.ShiftX, Y: c.Y + item.ShiftY})
	}
	_, err := schMoveKernel(pinned, win, doc, items, moveKernelOpts{
		Label: "alignment-fix-main", Stdout: io.Discard, Stderr: stderr, StubPolicy: moveStubPreserve,
	})
	return err
}

func verifyAlignmentFixBaseline(pinned *appConfig, win, doc string,
	beforeNets map[string]map[string]bool, beforeBridges []string) error {
	afterNets, _, err := readLiveNets(pinned, win)
	if err != nil || !dcNetsEqual(beforeNets, afterNets) {
		return fmt.Errorf("alignment-fix: pin netlist differs from baseline (%v)", err)
	}
	afterBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("alignment-fix: bridge verification failed: %w", err)
	}
	if added := dcNewBridgeSignatures(beforeBridges, afterBridges); len(added) > 0 {
		return fmt.Errorf("alignment-fix: bridge set gained %s", strings.Join(added, ","))
	}
	return nil
}

func rollbackAlignmentBlockers(pinned *appConfig, win, doc string, groups []appliedAlignmentBlocker) error {
	for i := len(groups) - 1; i >= 0; i-- {
		geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
			"includeBBox": true, "includePins": true, "includePinNets": false,
		})
		if err != nil {
			return err
		}
		comps, err := parseLayoutComps(geom.Result)
		if err != nil {
			return err
		}
		byDesig := map[string]layoutComp{}
		for _, c := range comps {
			if c.Designator != "" {
				byDesig[c.Designator] = c
			}
		}
		var items []moveItem
		for _, d := range groups[i].Designators {
			c, ok := byDesig[d]
			if !ok {
				return fmt.Errorf("rollback blocker part %s missing", d)
			}
			items = append(items, moveItem{
				Designator: d, HasTarget: true,
				X: c.X - groups[i].DX, Y: c.Y - groups[i].DY,
			})
		}
		if _, err := schMoveKernel(pinned, win, doc, items, moveKernelOpts{
			Label: "alignment-fix-rollback", Stdout: io.Discard, Stderr: io.Discard,
			StubPolicy: moveStubPreserve,
		}); err != nil {
			return err
		}
	}
	return nil
}
