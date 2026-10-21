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

type labelFixProposal struct {
	Kind        []string       `json:"kind"`
	AttributeID string         `json:"attributeId"`
	ParentID    string         `json:"parentId"`
	Net         string         `json:"net,omitempty"`
	Patch       map[string]any `json:"patch"`
	Status      string         `json:"status"` // ready | blocked
	Reason      string         `json:"reason,omitempty"`
	Original    map[string]any `json:"-"`
}

type labelFixReport struct {
	SchemaVersion int                `json:"schemaVersion"`
	DocumentID    string             `json:"documentId"`
	Proposals     []labelFixProposal `json:"proposals"`
	Blocked       int                `json:"blocked"`
	AppliedIDs    []string           `json:"appliedIds,omitempty"`
	Applied       bool               `json:"applied"`
	Saved         bool               `json:"saved,omitempty"`
	RolledBack    bool               `json:"rolledBack,omitempty"`
	RollbackError string             `json:"rollbackError,omitempty"`
	Notes         []string           `json:"notes,omitempty"`
}

type labelFixScene struct {
	projectID  string
	documentID string
	geomResult map[string]any
	comps      []layoutComp
	wires      []schGroupWire
	items      []visibleTextItem
	extra      []checkFinding
	proposals  []labelFixProposal
}

func newSchLabelFixCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var ids, out string
	var apply, save, allowPartial bool
	cmd := &cobra.Command{
		Use:   "label-fix",
		Short: "Repair hidden or vertical schematic network-name attributes",
		Long: `Inspect label-visibility and label-orientation findings, then propose strict
attribute property patches. Hidden Name attributes become visible; vertical text
rotations 90/270 become 0; an empty Name may be filled only from an explicit net
name already carried by its parent wire or marker.

Missing Name primitives and unknown net names stay blocked because this command
never invents a network identity. Apply mode re-reads every attribute before
writing, verifies only the requested fields changed, compares the complete
pin-to-net map, and blocks newly introduced visible-text collisions.

Without --apply this command is read-only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			scene, err := readLabelFixScene(pinned, win, doc, stderr)
			if err != nil {
				return err
			}
			selected := parsePrimitiveIDSet(ids)
			report := labelFixReport{
				SchemaVersion: 1, DocumentID: doc,
				Proposals: filterLabelFixProposals(scene.proposals, selected),
			}
			if len(selected) > 0 && len(report.Proposals) == 0 {
				return fmt.Errorf("label-fix: selected IDs have no visibility/orientation proposal")
			}
			for _, p := range report.Proposals {
				if p.Status == "blocked" {
					report.Blocked++
				}
			}
			if err := writeLabelFixReport(stdout, out, report); err != nil {
				return err
			}
			if !apply {
				fmt.Fprintln(stderr, "label-fix: dry-run only; pass --apply to write")
				return nil
			}
			if report.Blocked > 0 && !allowPartial {
				return fmt.Errorf("label-fix: %d proposal(s) are blocked; pass --allow-partial to apply only ready items", report.Blocked)
			}
			if err := executeLabelFix(pinned, win, doc, scene, &report, save, stderr); err != nil {
				return err
			}
			return writeLabelFixReport(stdout, out, report)
		},
	}
	cmd.Flags().StringVar(&ids, "id", "", "optional comma-separated attribute IDs")
	cmd.Flags().BoolVar(&apply, "apply", false, "write ready visibility/orientation patches")
	cmd.Flags().BoolVar(&save, "save", false, "save after fresh verification")
	cmd.Flags().BoolVar(&allowPartial, "allow-partial", false, "apply ready items while reporting blocked items")
	cmd.Flags().StringVar(&out, "out", "", "write structured report to JSON")
	return cmd
}

func writeLabelFixReport(stdout io.Writer, out string, report labelFixReport) error {
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

func readLabelFixScene(pinned *appConfig, win, doc string, stderr io.Writer) (labelFixScene, error) {
	geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePins": true, "includePinNets": true, "includePagePrimitives": true,
	})
	if err != nil {
		return labelFixScene{}, err
	}
	if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
		return labelFixScene{}, fmt.Errorf("label-fix: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
	}
	comps, err := parseLayoutComps(geom.Result)
	if err != nil {
		return labelFixScene{}, err
	}
	wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
	if err != nil {
		return labelFixScene{}, err
	}
	vt, err := requestAction(pinned, "schematic.visible_text.bboxes", win, nil)
	if err != nil {
		return labelFixScene{}, err
	}
	if vt.Context != nil && vt.Context.DocumentUUID != "" && vt.Context.DocumentUUID != doc {
		return labelFixScene{}, fmt.Errorf("label-fix: visible text came from %q, expected %q", vt.Context.DocumentUUID, doc)
	}
	var vision alignmentVision
	raw, _ := json.Marshal(vt.Result)
	if err := json.Unmarshal(raw, &vision); err != nil {
		return labelFixScene{}, fmt.Errorf("label-fix: parse visible text: %w", err)
	}
	extra := visibleTextFindings(pinned, win, sheetBBoxOf(comps), comps, wires, 1.0, stderr)
	proposals := buildLabelFixProposals(vision.Items, comps, wires)
	projectID := ""
	if geom.Context != nil {
		projectID = geom.Context.ProjectUUID
	}
	return labelFixScene{
		projectID: projectID, documentID: doc, geomResult: geom.Result,
		comps: comps, wires: wires, items: vision.Items, extra: extra, proposals: proposals,
	}, nil
}

func buildLabelFixProposals(items []visibleTextItem, comps []layoutComp, wires []schGroupWire) []labelFixProposal {
	byID := map[string]visibleTextItem{}
	for _, it := range items {
		byID[it.ID] = it
	}
	proposals := map[string]*labelFixProposal{}
	add := func(find checkFinding) {
		it, ok := byID[find.PrimitiveId]
		if !ok || it.Kind != "attribute" || it.Key != "Name" {
			proposals["blocked:"+find.PrimitiveId] = &labelFixProposal{
				Kind: []string{find.Type}, AttributeID: find.PrimitiveId,
				Status: "blocked", Reason: "missing-native-name-attribute",
			}
			return
		}
		p := proposals[find.PrimitiveId]
		if p == nil {
			p = &labelFixProposal{
				AttributeID: find.PrimitiveId, ParentID: it.ParentID,
				Patch: map[string]any{}, Status: "ready",
			}
			proposals[find.PrimitiveId] = p
		}
		p.Kind = appendUniqueString(p.Kind, find.Type)
		if find.Type == "label-visibility" {
			p.Patch["valueVisible"] = true
			if strings.TrimSpace(it.Value) == "" {
				net := labelFixNetForParent(it.ParentID, comps, wires)
				if net == "" {
					p.Status = "blocked"
					p.Reason = "empty-name-without-explicit-net"
				} else {
					p.Patch["value"] = net
					p.Net = net
				}
			}
		}
		if find.Type == "label-orientation" {
			p.Patch["rotation"] = 0.0
		}
	}
	for _, find := range netLabelVisibilityFindings(items) {
		add(find)
	}
	for _, find := range netLabelOrientationFindings(items) {
		if find.Type == "label-orientation" {
			add(find)
		}
	}
	// A hidden label is skipped by the orientation check. Once label-fix makes
	// it visible, a 90/270 rotation would immediately become a new finding, so
	// fold that deterministic repair into the same patch now.
	for id, p := range proposals {
		if strings.HasPrefix(id, "blocked:") || p.Status != "ready" || p.Patch["valueVisible"] != true {
			continue
		}
		it, ok := byID[id]
		if !ok || it.Rotation == nil {
			continue
		}
		rot := math.Mod(*it.Rotation, 360)
		if rot < 0 {
			rot += 360
		}
		if rot == 90 || rot == 270 {
			p.Kind = appendUniqueString(p.Kind, "label-orientation")
			p.Patch["rotation"] = 0.0
		}
	}
	var out []labelFixProposal
	for _, p := range proposals {
		if len(p.Kind) == 0 {
			continue
		}
		sort.Strings(p.Kind)
		if len(p.Patch) == 0 && p.Status == "ready" {
			p.Status = "blocked"
			p.Reason = "no-supported-property-patch"
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AttributeID < out[j].AttributeID })
	return out
}

func labelFixNetForParent(parentID string, comps []layoutComp, wires []schGroupWire) string {
	for _, c := range comps {
		if c.ID == parentID && isSchMarker(c.ComponentType) {
			return strings.TrimSpace(c.Net)
		}
	}
	for _, w := range wires {
		if w.ID == parentID {
			return strings.TrimSpace(w.Net)
		}
	}
	return ""
}

func filterLabelFixProposals(in []labelFixProposal, wanted map[string]bool) []labelFixProposal {
	if len(wanted) == 0 {
		return in
	}
	var out []labelFixProposal
	for _, p := range in {
		if wanted[p.AttributeID] {
			out = append(out, p)
		}
	}
	return out
}

func executeLabelFix(pinned *appConfig, win, doc string, scene labelFixScene,
	report *labelFixReport, save bool, stderr io.Writer) error {
	beforeNets, _, err := readLiveNets(pinned, win)
	if err != nil {
		return fmt.Errorf("label-fix: baseline netlist: %w", err)
	}
	beforeBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("label-fix: baseline bridge set: %w", err)
	}
	beforeExtras := findingSignatureSet(scene.extra)
	var applied []labelFixProposal
	rollback := func(cause error) error {
		if len(applied) == 0 {
			return cause
		}
		rbErr := rollbackLabelFixes(pinned, win, doc, applied)
		report.RolledBack = rbErr == nil
		if rbErr != nil {
			report.RollbackError = rbErr.Error()
			return fmt.Errorf("%v; label rollback FAILED (%v) — inspect fresh attributes before retry", cause, rbErr)
		}
		return fmt.Errorf("%v; label patches were rolled back", cause)
	}
	freshScene, err := readLabelFixScene(pinned, win, doc, io.Discard)
	if err != nil {
		return err
	}
	for i := range report.Proposals {
		proposal := &report.Proposals[i]
		if proposal.Status != "ready" {
			continue
		}
		state, err := dcAttributeState(freshScene.geomResult, proposal.AttributeID)
		if err != nil {
			return rollback(err)
		}
		proposal.Original = state
		payload := map[string]any{
			"projectUuid": scene.projectID, "documentUuid": doc,
			"primitiveId": proposal.AttributeID, "expected": state, "patch": proposal.Patch,
		}
		if err := validateSchAttributeModifyPayload(payload); err != nil {
			return rollback(err)
		}
		res, err := requestAction(pinned, "schematic.attribute.modify", win, payload)
		if err != nil {
			return rollback(err)
		}
		if err := schAttributeModifyResultError("schematic.attribute.modify", res.Result); err != nil {
			return rollback(err)
		}
		applied = append(applied, *proposal)
		report.AppliedIDs = append(report.AppliedIDs, proposal.AttributeID)
	}
	afterScene, err := readLabelFixScene(pinned, win, doc, stderr)
	if err != nil {
		return rollback(err)
	}
	changed := map[string]bool{}
	for _, proposal := range applied {
		changed[proposal.AttributeID] = true
	}
	for _, find := range append(netLabelVisibilityFindings(afterScene.items), netLabelOrientationFindings(afterScene.items)...) {
		if changed[find.PrimitiveId] {
			return rollback(fmt.Errorf("label-fix: target finding remains after write: %s %s", find.Type, find.PrimitiveId))
		}
	}
	for sig := range findingSignatureSet(afterScene.extra) {
		if !beforeExtras[sig] {
			return rollback(fmt.Errorf("label-fix: repair introduced visible-text finding: %s", sig))
		}
	}
	afterNets, _, err := readLiveNets(pinned, win)
	if err != nil || !dcNetsEqual(beforeNets, afterNets) {
		return rollback(fmt.Errorf("label-fix: pin netlist changed (%v)", err))
	}
	afterBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return rollback(fmt.Errorf("label-fix: bridge check failed: %w", err))
	}
	if added := dcNewBridgeSignatures(beforeBridges, afterBridges); len(added) > 0 {
		return rollback(fmt.Errorf("label-fix: bridge set gained %s", strings.Join(added, ",")))
	}
	if save {
		saved, serr := requestAutolayoutAction(pinned, "schematic.save", win, nil, doc, "label-fix 保存")
		if serr != nil {
			return fmt.Errorf("label-fix: save: %w", serr)
		}
		ok, _ := saved.Result["saved"].(bool)
		if !ok {
			return fmt.Errorf("label-fix: schematic.save did not return saved:true")
		}
		report.Saved = true
	}
	report.Applied = true
	fmt.Fprintf(stderr, "✓ label-fix applied %d attribute patch(es)\n", len(report.AppliedIDs))
	return nil
}

func rollbackLabelFixes(pinned *appConfig, win, doc string, applied []labelFixProposal) error {
	for i := len(applied) - 1; i >= 0; i-- {
		scene, err := readLabelFixScene(pinned, win, doc, io.Discard)
		if err != nil {
			return err
		}
		state, err := dcAttributeState(scene.geomResult, applied[i].AttributeID)
		if err != nil {
			return err
		}
		if applied[i].Original == nil {
			return fmt.Errorf("label rollback original state missing for %s", applied[i].AttributeID)
		}
		inverse := map[string]any{}
		for key := range applied[i].Patch {
			stateKey := ""
			switch key {
			case "rotation":
				stateKey = "Rotation"
			case "valueVisible":
				stateKey = "ValueVisible"
			case "keyVisible":
				stateKey = "KeyVisible"
			case "value":
				stateKey = "Value"
			}
			if stateKey == "" {
				return fmt.Errorf("label rollback cannot map patch.%s", key)
			}
			inverse[key] = applied[i].Original[stateKey]
		}
		payload := map[string]any{
			"projectUuid": scene.projectID, "documentUuid": doc,
			"primitiveId": applied[i].AttributeID, "expected": state, "patch": inverse,
		}
		if err := validateSchAttributeModifyPayload(payload); err != nil {
			return err
		}
		res, err := requestAction(pinned, "schematic.attribute.modify", win, payload)
		if err != nil {
			return err
		}
		if err := schAttributeModifyResultError("schematic.attribute.modify", res.Result); err != nil {
			return err
		}
	}
	return nil
}

func findingSignatureSet(findings []checkFinding) map[string]bool {
	out := map[string]bool{}
	for _, f := range findings {
		out[fmt.Sprintf("%s|%s|%v|%s|%s", f.Type, f.PrimitiveId, f.PrimitiveIds,
			f.WirePrimitiveId, f.MarkerPrimitiveId)] = true
	}
	return out
}
