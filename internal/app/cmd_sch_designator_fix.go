package app

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

type designatorFixReport struct {
	SchemaVersion int                     `json:"schemaVersion"`
	DocumentID    string                  `json:"documentId"`
	Proposals     []visualTextFixProposal `json:"proposals"`
	Blocked       int                     `json:"blocked"`
	AppliedIDs    []string                `json:"appliedIds,omitempty"`
	Applied       bool                    `json:"applied"`
	Saved         bool                    `json:"saved,omitempty"`
	RolledBack    bool                    `json:"rolledBack,omitempty"`
	RollbackError string                  `json:"rollbackError,omitempty"`
}

type designatorFixScene struct {
	projectID  string
	documentID string
	comps      []layoutComp
	wires      []schGroupWire
	items      []visibleTextItem
	sheet      *layoutBBox
	title      *layoutBBox
	findings   []checkFinding
	proposals  []visualTextFixProposal
}

func newSchDesignatorFixCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var ids, out string
	var apply, save, allowPartial bool
	cmd := &cobra.Command{
		Use:   "designator-fix",
		Short: "Move colliding Designator text to nearby clear slots",
		Long: `Repair designator-overlap and designator-wire-overlap findings by moving only
the Designator attribute on a 5-unit grid. Candidates avoid every part body,
pin, marker, other visible text, foreign wire, title block and sheet boundary.

Apply mode moves each attribute through the protected attribute.move action,
then rechecks designator geometry, visible text, complete pin-to-net connectivity
and bridge sets. Any regression triggers reverse moves. Without --apply this
command is read-only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			scene, err := readDesignatorFixScene(pinned, win, doc, stderr)
			if err != nil {
				return err
			}
			wanted := parsePrimitiveIDSet(ids)
			report := designatorFixReport{
				SchemaVersion: 1, DocumentID: doc,
				Proposals: filterVisualTextProposals(scene.proposals, wanted),
			}
			if len(wanted) > 0 && len(report.Proposals) == 0 {
				return fmt.Errorf("designator-fix: selected IDs have no collision proposal")
			}
			for _, p := range report.Proposals {
				if p.Status == "blocked" {
					report.Blocked++
				}
			}
			if err := writeDesignatorFixReport(stdout, out, report); err != nil {
				return err
			}
			if !apply {
				fmt.Fprintln(stderr, "designator-fix: dry-run only; pass --apply to write")
				return nil
			}
			if report.Blocked > 0 && !allowPartial {
				return fmt.Errorf("designator-fix: %d proposal(s) blocked; pass --allow-partial", report.Blocked)
			}
			if err := executeDesignatorFix(pinned, win, doc, scene, &report, save); err != nil {
				return err
			}
			return writeDesignatorFixReport(stdout, out, report)
		},
	}
	cmd.Flags().StringVar(&ids, "id", "", "optional comma-separated Designator attribute IDs")
	cmd.Flags().BoolVar(&apply, "apply", false, "move ready Designator attributes")
	cmd.Flags().BoolVar(&save, "save", false, "save after verification")
	cmd.Flags().BoolVar(&allowPartial, "allow-partial", false, "apply ready items while reporting blocked items")
	cmd.Flags().StringVar(&out, "out", "", "write structured report to JSON")
	return cmd
}

func writeDesignatorFixReport(stdout io.Writer, out string, report designatorFixReport) error {
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

func readDesignatorFixScene(pinned *appConfig, win, doc string, stderr io.Writer) (designatorFixScene, error) {
	geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePins": true, "includePinNets": true, "includePagePrimitives": true,
	})
	if err != nil {
		return designatorFixScene{}, err
	}
	if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
		return designatorFixScene{}, fmt.Errorf("designator-fix: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
	}
	comps, err := parseLayoutComps(geom.Result)
	if err != nil {
		return designatorFixScene{}, err
	}
	wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
	if err != nil {
		return designatorFixScene{}, err
	}
	vt, err := requestAction(pinned, "schematic.visible_text.bboxes", win, nil)
	if err != nil {
		return designatorFixScene{}, err
	}
	var vision alignmentVision
	raw, _ := json.Marshal(vt.Result)
	if err := json.Unmarshal(raw, &vision); err != nil {
		return designatorFixScene{}, fmt.Errorf("designator-fix: parse visible text: %w", err)
	}
	designatorRes, err := requestAction(pinned, "schematic.designators.list", win, nil)
	if err != nil {
		return designatorFixScene{}, err
	}
	designatorExport, err := parseSchDesignatorGeometryExport(designatorRes.Result, doc)
	if err != nil {
		return designatorFixScene{}, err
	}
	sheet := sheetBBoxOf(comps)
	title, _ := titleBlockKeepoutWithSource(sheet)
	survey := schFrameSurvey{Texts: map[string]map[string]any{}}
	for _, item := range vision.Items {
		if item.Kind != "text" || item.BBox == nil {
			continue
		}
		survey.Texts[item.ID] = map[string]any{"bbox": map[string]any{
			"minX": item.BBox.MinX, "minY": item.BBox.MinY,
			"maxX": item.BBox.MaxX, "maxY": item.BBox.MaxY,
		}}
	}
	findings := schDesignatorFindings(survey, designatorExport.Designators, comps, wires)
	proposals := buildDesignatorFixProposals(findings, vision.Items, comps, wires,
		parseVisibleAttributeAnchors(geom.Result), sheet, title)
	projectID := ""
	if geom.Context != nil {
		projectID = geom.Context.ProjectUUID
	}
	return designatorFixScene{
		projectID: projectID, documentID: doc, comps: comps, wires: wires,
		items: vision.Items, sheet: sheet, title: title, findings: findings, proposals: proposals,
	}, nil
}

func buildDesignatorFixProposals(findings []checkFinding, items []visibleTextItem,
	comps []layoutComp, wires []schGroupWire, anchors map[string]checkPoint,
	sheet, titleBlock *layoutBBox) []visualTextFixProposal {
	byID := map[string]visibleTextItem{}
	for _, item := range items {
		byID[item.ID] = item
	}
	targets := map[string]bool{}
	for _, finding := range findings {
		switch finding.Type {
		case "designator-overlap", "designator-wire-overlap":
			if finding.PrimitiveId != "" {
				targets[finding.PrimitiveId] = true
			}
		}
	}
	var out []visualTextFixProposal
	for id := range targets {
		item, ok := byID[id]
		if !ok || item.BBox == nil || item.Key != "Designator" {
			out = append(out, visualTextFixProposal{PrimitiveID: id, Status: "blocked", Reason: "designator-geometry-unavailable"})
			continue
		}
		anchor, ok := anchors[id]
		proposal := visualTextFixProposal{
			PrimitiveID: id, ParentID: item.ParentID, Kind: item.Kind, Key: item.Key,
			FromBBox: item.BBox, FromAnchor: &anchor,
		}
		if !ok {
			proposal.Status = "blocked"
			proposal.Reason = "designator-anchor-unavailable"
			out = append(out, proposal)
			continue
		}
		bestCost := math.Inf(1)
		for _, delta := range visualTextOffsets() {
			moved := item
			box := translateLayoutBBox(*item.BBox, delta)
			moved.BBox = &box
			if !designatorSlotClear(moved, item.ID, items, comps, wires, sheet, titleBlock) {
				continue
			}
			cost := math.Abs(delta[0]) + math.Abs(delta[1])
			if cost < bestCost {
				bestCost = cost
				proposal.DeltaX, proposal.DeltaY = delta[0], delta[1]
				proposal.ToAnchor = &checkPoint{X: anchor.X + delta[0], Y: anchor.Y + delta[1]}
				proposal.ToBBox = &box
			}
		}
		if math.IsInf(bestCost, 1) {
			proposal.Status = "blocked"
			proposal.Reason = "no-clear-local-designator-slot"
		} else {
			proposal.Status = "ready"
		}
		out = append(out, proposal)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PrimitiveID < out[j].PrimitiveID })
	return out
}

func designatorSlotClear(moved visibleTextItem, originalID string, items []visibleTextItem,
	comps []layoutComp, wires []schGroupWire, sheet, titleBlock *layoutBBox) bool {
	if moved.BBox == nil || sheet == nil {
		return false
	}
	usable := schUsableArea(*sheet)
	if !boxInside2(*moved.BBox, usable) || (titleBlock != nil && positiveOverlap(*moved.BBox, *titleBlock)) {
		return false
	}
	for _, comp := range comps {
		if comp.BBox == nil || comp.ComponentType == "sheet" {
			continue
		}
		if positiveOverlap(*moved.BBox, *comp.BBox) {
			return false
		}
		for _, pin := range comp.Pins {
			if pointInBox2(pin.X, pin.Y, *moved.BBox, 0.5) {
				return false
			}
		}
	}
	for _, item := range items {
		if item.ID == originalID || item.BBox == nil {
			continue
		}
		if positiveOverlap(*moved.BBox, *item.BBox) {
			return false
		}
	}
	for _, wire := range wires {
		for _, seg := range labelWireSegments(wire) {
			if schSegmentCrossesBox(seg[0], seg[1], seg[2], seg[3], *moved.BBox) {
				return false
			}
		}
	}
	return true
}

func executeDesignatorFix(pinned *appConfig, win, doc string, scene designatorFixScene,
	report *designatorFixReport, save bool) error {
	beforeNets, _, err := readLiveNets(pinned, win)
	if err != nil {
		return fmt.Errorf("designator-fix: baseline netlist: %w", err)
	}
	beforeBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("designator-fix: baseline bridge set: %w", err)
	}
	beforeFindings := designatorFindingSignatureSet(scene.findings)
	var applied []visualTextFixProposal
	rollback := func(cause error) error {
		if len(applied) == 0 {
			return cause
		}
		rbErr := rollbackVisualTextFixes(pinned, win, doc, applied)
		report.RolledBack = rbErr == nil
		if rbErr != nil {
			report.RollbackError = rbErr.Error()
			return fmt.Errorf("%v; designator rollback FAILED (%v)", cause, rbErr)
		}
		return fmt.Errorf("%v; designator moves were rolled back", cause)
	}
	for _, proposal := range report.Proposals {
		if proposal.Status != "ready" {
			continue
		}
		if err := executeVisualTextProposal(pinned, win, doc, proposal); err != nil {
			return rollback(err)
		}
		applied = append(applied, proposal)
		report.AppliedIDs = append(report.AppliedIDs, proposal.PrimitiveID)
	}
	fresh, err := readDesignatorFixScene(pinned, win, doc, io.Discard)
	if err != nil {
		return rollback(err)
	}
	afterFindings := designatorFindingSignatureSet(fresh.findings)
	changed := map[string]bool{}
	for _, proposal := range applied {
		changed[proposal.PrimitiveID] = true
	}
	for signature := range afterFindings {
		if changed[signatureID(signature)] {
			return rollback(fmt.Errorf("designator-fix: target finding remains: %s", signature))
		}
		if !beforeFindings[signature] {
			return rollback(fmt.Errorf("designator-fix: repair introduced a finding: %s", signature))
		}
	}
	afterNets, _, err := readLiveNets(pinned, win)
	if err != nil || !dcNetsEqual(beforeNets, afterNets) {
		return rollback(fmt.Errorf("designator-fix: pin netlist changed (%v)", err))
	}
	afterBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return rollback(err)
	}
	if added := dcNewBridgeSignatures(beforeBridges, afterBridges); len(added) > 0 {
		return rollback(fmt.Errorf("designator-fix: bridge set gained %s", strings.Join(added, ",")))
	}
	if save {
		saved, serr := requestAutolayoutAction(pinned, "schematic.save", win, nil, doc, "designator-fix 保存")
		if serr != nil || saved.Result["saved"] != true {
			if serr == nil {
				serr = fmt.Errorf("schematic.save did not return saved:true")
			}
			return rollback(fmt.Errorf("designator-fix: save: %w", serr))
		}
		report.Saved = true
	}
	report.Applied = true
	return nil
}

func designatorFindingSignatureSet(findings []checkFinding) map[string]bool {
	out := map[string]bool{}
	for _, finding := range findings {
		if finding.Type == "designator-overlap" || finding.Type == "designator-wire-overlap" {
			out[findingSignature(finding)] = true
		}
	}
	return out
}

func signatureID(signature string) string {
	parts := strings.Split(signature, "|")
	if len(parts) > 1 {
		return parts[1]
	}
	return ""
}
