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

type visualTextFixProposal struct {
	PrimitiveID string      `json:"primitiveId"`
	ParentID    string      `json:"parentId,omitempty"`
	Kind        string      `json:"kind"`
	Key         string      `json:"key,omitempty"`
	FromAnchor  *checkPoint `json:"fromAnchor,omitempty"`
	ToAnchor    *checkPoint `json:"toAnchor,omitempty"`
	FromBBox    *layoutBBox `json:"fromBBox,omitempty"`
	ToBBox      *layoutBBox `json:"toBBox,omitempty"`
	DeltaX      float64     `json:"deltaX"`
	DeltaY      float64     `json:"deltaY"`
	Status      string      `json:"status"`
	Reason      string      `json:"reason,omitempty"`
}

type visualTextFixReport struct {
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

type visualTextFixScene struct {
	projectID  string
	documentID string
	geomResult map[string]any
	comps      []layoutComp
	wires      []schGroupWire
	items      []visibleTextItem
	attrAnchor map[string]checkPoint
	textAnchor map[string]checkPoint
	extra      []checkFinding
	proposals  []visualTextFixProposal
}

func newSchVisualTextFixCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var ids, out string
	var apply, save, allowPartial bool
	cmd := &cobra.Command{
		Use:   "visual-text-fix",
		Short: "Move visible text to nearby collision-free slots",
		Long: `Inspect visible-text-overlap findings and propose small nearby moves on the
5-unit grid. Attribute text uses the protected attribute.move action; free text
uses schematic.text.move. A candidate must leave the sheet/title block clear,
avoid every part, pin, marker, other text and foreign wire, and remain within a
local 60-unit radius.

Apply mode re-reads every target, verifies the requested absolute position,
then compares all visible-text findings, the complete pin-to-net map and bridge
set against the command baseline. New findings or failed writes trigger reverse
moves. Without --apply this command is read-only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			scene, err := readVisualTextFixScene(pinned, win, doc, stderr)
			if err != nil {
				return err
			}
			wanted := parsePrimitiveIDSet(ids)
			report := visualTextFixReport{
				SchemaVersion: 1, DocumentID: doc,
				Proposals: filterVisualTextProposals(scene.proposals, wanted),
			}
			if len(wanted) > 0 && len(report.Proposals) == 0 {
				return fmt.Errorf("visual-text-fix: selected IDs have no overlap proposal")
			}
			for _, p := range report.Proposals {
				if p.Status == "blocked" {
					report.Blocked++
				}
			}
			if err := writeVisualTextFixReport(stdout, out, report); err != nil {
				return err
			}
			if !apply {
				fmt.Fprintln(stderr, "visual-text-fix: dry-run only; pass --apply to write")
				return nil
			}
			if report.Blocked > 0 && !allowPartial {
				return fmt.Errorf("visual-text-fix: %d proposal(s) are blocked; pass --allow-partial to apply only ready items", report.Blocked)
			}
			if err := executeVisualTextFix(pinned, win, doc, scene, &report, save, stderr); err != nil {
				return err
			}
			return writeVisualTextFixReport(stdout, out, report)
		},
	}
	cmd.Flags().StringVar(&ids, "id", "", "optional comma-separated text/attribute IDs")
	cmd.Flags().BoolVar(&apply, "apply", false, "move ready text items")
	cmd.Flags().BoolVar(&save, "save", false, "save after fresh verification")
	cmd.Flags().BoolVar(&allowPartial, "allow-partial", false, "apply ready items while reporting blocked items")
	cmd.Flags().StringVar(&out, "out", "", "write structured report to JSON")
	return cmd
}

func writeVisualTextFixReport(stdout io.Writer, out string, report visualTextFixReport) error {
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

func readVisualTextFixScene(pinned *appConfig, win, doc string, stderr io.Writer) (visualTextFixScene, error) {
	geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePins": true, "includePinNets": true, "includePagePrimitives": true,
	})
	if err != nil {
		return visualTextFixScene{}, err
	}
	if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
		return visualTextFixScene{}, fmt.Errorf("visual-text-fix: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
	}
	comps, err := parseLayoutComps(geom.Result)
	if err != nil {
		return visualTextFixScene{}, err
	}
	wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
	if err != nil {
		return visualTextFixScene{}, err
	}
	vt, err := requestAction(pinned, "schematic.visible_text.bboxes", win, nil)
	if err != nil {
		return visualTextFixScene{}, err
	}
	var vision alignmentVision
	raw, _ := json.Marshal(vt.Result)
	if err := json.Unmarshal(raw, &vision); err != nil {
		return visualTextFixScene{}, fmt.Errorf("visual-text-fix: parse visible text: %w", err)
	}
	texts, err := requestAction(pinned, "schematic.text.list", win, nil)
	if err != nil {
		return visualTextFixScene{}, err
	}
	textAnchors := map[string]checkPoint{}
	for _, row := range mapsField(texts.Result, "texts") {
		id := strField(row, "primitiveId")
		x, okx := row["x"].(float64)
		y, oky := row["y"].(float64)
		if id != "" && okx && oky {
			textAnchors[id] = checkPoint{X: x, Y: y}
		}
	}
	sheet := sheetBBoxOf(comps)
	titleBlock, _ := titleBlockKeepoutWithSource(sheet)
	extra := visibleTextFindings(pinned, win, sheet, comps, wires, 1.0, stderr)
	proposals := buildVisualTextFixProposals(extra, vision.Items, comps, wires,
		parseVisibleAttributeAnchors(geom.Result), textAnchors, sheet, titleBlock)
	projectID := ""
	if geom.Context != nil {
		projectID = geom.Context.ProjectUUID
	}
	return visualTextFixScene{
		projectID: projectID, documentID: doc, geomResult: geom.Result,
		comps: comps, wires: wires, items: vision.Items,
		attrAnchor: parseVisibleAttributeAnchors(geom.Result), textAnchor: textAnchors,
		extra: extra, proposals: proposals,
	}, nil
}

func buildVisualTextFixProposals(findings []checkFinding, items []visibleTextItem,
	comps []layoutComp, wires []schGroupWire, attrAnchor, textAnchor map[string]checkPoint,
	sheet, titleBlock *layoutBBox) []visualTextFixProposal {
	byID := map[string]visibleTextItem{}
	for _, it := range items {
		byID[it.ID] = it
	}
	targets := map[string]bool{}
	for _, f := range findings {
		if f.Type == "visible-text-overlap" && f.PrimitiveId != "" {
			targets[f.PrimitiveId] = true
		}
	}
	var out []visualTextFixProposal
	for id := range targets {
		it, ok := byID[id]
		if !ok || it.BBox == nil {
			out = append(out, visualTextFixProposal{PrimitiveID: id, Status: "blocked", Reason: "text-geometry-unavailable"})
			continue
		}
		p := visualTextFixProposal{
			PrimitiveID: id, ParentID: it.ParentID, Kind: it.Kind, Key: it.Key,
			FromBBox: it.BBox,
		}
		var anchor checkPoint
		switch it.Kind {
		case "attribute":
			if it.Key != "Name" && it.Key != "Designator" && it.Key != "Value" {
				p.Status = "blocked"
				p.Reason = "unsupported-attribute-key:" + it.Key
				out = append(out, p)
				continue
			}
			a, ok := attrAnchor[id]
			if !ok {
				p.Status = "blocked"
				p.Reason = "attribute-anchor-unavailable"
				out = append(out, p)
				continue
			}
			anchor = a
		case "text":
			a, ok := textAnchor[id]
			if !ok {
				p.Status = "blocked"
				p.Reason = "free-text-anchor-unavailable"
				out = append(out, p)
				continue
			}
			anchor = a
		default:
			p.Status = "blocked"
			p.Reason = "unsupported-text-kind:" + it.Kind
			out = append(out, p)
			continue
		}
		p.FromAnchor = &checkPoint{X: anchor.X, Y: anchor.Y}
		bestFound := false
		bestCost := math.Inf(1)
		for _, delta := range visualTextOffsets() {
			moved := it
			b := translateLayoutBBox(*it.BBox, delta)
			moved.BBox = &b
			if !visualTextBoxClear(moved, it.ID, it.ParentID, items, comps, wires, sheet, titleBlock) {
				continue
			}
			cost := math.Abs(delta[0]) + math.Abs(delta[1])
			if cost < bestCost {
				p.DeltaX, p.DeltaY = delta[0], delta[1]
				p.ToAnchor = &checkPoint{X: anchor.X + delta[0], Y: anchor.Y + delta[1]}
				p.ToBBox = &b
				bestCost = cost
				bestFound = true
			}
		}
		if !bestFound {
			p.Status = "blocked"
			p.Reason = "no-clear-local-text-slot"
		} else {
			p.Status = "ready"
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PrimitiveID < out[j].PrimitiveID })
	return out
}

func visualTextOffsets() [][2]float64 {
	var out [][2]float64
	for d := 5.0; d <= 60; d += 5 {
		out = append(out,
			[2]float64{0, d}, [2]float64{0, -d}, [2]float64{d, 0}, [2]float64{-d, 0},
			[2]float64{d, d}, [2]float64{d, -d}, [2]float64{-d, d}, [2]float64{-d, -d})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return math.Abs(out[i][0])+math.Abs(out[i][1]) < math.Abs(out[j][0])+math.Abs(out[j][1])
	})
	return out
}

func visualTextBoxClear(moved visibleTextItem, originalID, originalParent string,
	items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox) bool {
	if moved.BBox == nil || sheet == nil {
		return false
	}
	usable := schUsableArea(*sheet)
	if !boxInside2(*moved.BBox, usable) || (titleBlock != nil && positiveOverlap(*moved.BBox, *titleBlock)) {
		return false
	}
	for _, c := range comps {
		if c.ID == originalParent || c.BBox == nil || c.ComponentType == "sheet" {
			continue
		}
		if positiveOverlap(*moved.BBox, *c.BBox) {
			return false
		}
		for _, pin := range c.Pins {
			if pointInBox2(pin.X, pin.Y, *moved.BBox, 0.5) {
				return false
			}
		}
	}
	for _, it := range items {
		if it.ID == originalID || it.BBox == nil || (originalParent != "" && it.ParentID == originalParent) {
			continue
		}
		if positiveOverlap(*moved.BBox, *it.BBox) {
			return false
		}
	}
	for _, w := range wires {
		if moved.ComponentType == "wire" && w.ID == originalParent {
			continue
		}
		for _, s := range labelWireSegments(w) {
			if schSegmentCrossesBox(s[0], s[1], s[2], s[3], *moved.BBox) {
				return false
			}
		}
	}
	return true
}

func filterVisualTextProposals(in []visualTextFixProposal, wanted map[string]bool) []visualTextFixProposal {
	if len(wanted) == 0 {
		return in
	}
	var out []visualTextFixProposal
	for _, p := range in {
		if wanted[p.PrimitiveID] {
			out = append(out, p)
		}
	}
	return out
}

func executeVisualTextFix(pinned *appConfig, win, doc string, scene visualTextFixScene,
	report *visualTextFixReport, save bool, stderr io.Writer) error {
	beforeNets, _, err := readLiveNets(pinned, win)
	if err != nil {
		return fmt.Errorf("visual-text-fix: baseline netlist: %w", err)
	}
	beforeBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("visual-text-fix: baseline bridge set: %w", err)
	}
	beforeExtra := findingSignatureSet(scene.extra)
	var applied []visualTextFixProposal
	rollback := func(cause error) error {
		if len(applied) == 0 {
			return cause
		}
		rbErr := rollbackVisualTextFixes(pinned, win, doc, applied)
		report.RolledBack = rbErr == nil
		if rbErr != nil {
			report.RollbackError = rbErr.Error()
			return fmt.Errorf("%v; text rollback FAILED (%v)", cause, rbErr)
		}
		return fmt.Errorf("%v; text moves were rolled back", cause)
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
	after, err := readVisualTextFixScene(pinned, win, doc, io.Discard)
	if err != nil {
		return rollback(err)
	}
	moved := map[string]bool{}
	for _, id := range report.AppliedIDs {
		moved[id] = true
	}
	for _, finding := range after.extra {
		if finding.Type == "visible-text-overlap" && moved[finding.PrimitiveId] {
			return rollback(fmt.Errorf("visual-text-fix: moved item %s still overlaps", finding.PrimitiveId))
		}
	}
	for sig := range findingSignatureSet(after.extra) {
		if !beforeExtra[sig] {
			return rollback(fmt.Errorf("visual-text-fix: introduced visible-text finding: %s", sig))
		}
	}
	afterNets, _, err := readLiveNets(pinned, win)
	if err != nil || !dcNetsEqual(beforeNets, afterNets) {
		return rollback(fmt.Errorf("visual-text-fix: pin netlist changed (%v)", err))
	}
	afterBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return rollback(fmt.Errorf("visual-text-fix: bridge check failed: %w", err))
	}
	if added := dcNewBridgeSignatures(beforeBridges, afterBridges); len(added) > 0 {
		return rollback(fmt.Errorf("visual-text-fix: bridge set gained %s", strings.Join(added, ",")))
	}
	if save {
		saved, serr := requestAutolayoutAction(pinned, "schematic.save", win, nil, doc, "visual-text-fix 保存")
		if serr != nil {
			return fmt.Errorf("visual-text-fix: save: %w", serr)
		}
		ok, _ := saved.Result["saved"].(bool)
		if !ok {
			return fmt.Errorf("visual-text-fix: schematic.save did not return saved:true")
		}
		report.Saved = true
	}
	report.Applied = true
	fmt.Fprintf(stderr, "✓ visual-text-fix moved %d text item(s)\n", len(report.AppliedIDs))
	return nil
}

func executeVisualTextProposal(pinned *appConfig, win, doc string, p visualTextFixProposal) error {
	if p.ToAnchor == nil {
		return fmt.Errorf("text proposal %s has no target anchor", p.PrimitiveID)
	}
	scene, err := readVisualTextFixScene(pinned, win, doc, io.Discard)
	if err != nil {
		return err
	}
	switch p.Kind {
	case "attribute":
		state, err := dcAttributeState(scene.geomResult, p.PrimitiveID)
		if err != nil {
			return err
		}
		payload := map[string]any{
			"projectUuid": scene.projectID, "documentUuid": doc,
			"primitiveId": p.PrimitiveID, "expected": state,
			"x": p.ToAnchor.X, "y": p.ToAnchor.Y,
		}
		if err := validateSchAttributeMovePayload(payload); err != nil {
			return err
		}
		res, err := requestAction(pinned, "schematic.attribute.move", win, payload)
		if err != nil {
			return err
		}
		return schAttributeMoveResultError("schematic.attribute.move", res.Result)
	case "text":
		res, err := requestAction(pinned, "schematic.text.move", win, map[string]any{
			"moves": []any{map[string]any{
				"primitiveId": p.PrimitiveID, "x": p.ToAnchor.X, "y": p.ToAnchor.Y,
			}},
		})
		if err != nil {
			return err
		}
		if res.Result["verified"] != true || res.Result["partial"] == true {
			return fmt.Errorf("schematic.text.move did not verify %s", p.PrimitiveID)
		}
		return nil
	default:
		return fmt.Errorf("unsupported text kind %s", p.Kind)
	}
}

func rollbackVisualTextFixes(pinned *appConfig, win, doc string, applied []visualTextFixProposal) error {
	for i := len(applied) - 1; i >= 0; i-- {
		p := applied[i]
		if p.FromAnchor == nil {
			return fmt.Errorf("rollback %s missing original anchor", p.PrimitiveID)
		}
		restore := p
		restore.ToAnchor = p.FromAnchor
		if err := executeVisualTextProposal(pinned, win, doc, restore); err != nil {
			return err
		}
	}
	return nil
}
