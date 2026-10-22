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

type crossChipWireBackup struct {
	ID       string       `json:"id"`
	Net      string       `json:"net"`
	Points   []float64    `json:"points,omitempty"`
	Segments [][4]float64 `json:"segments,omitempty"`
}

type crossChipFixProposal struct {
	ID              string         `json:"id"`
	Net             string         `json:"net"`
	ChipDesignators []string       `json:"chipDesignators"`
	WireIDs         []string       `json:"wireIds"`
	PinRefs         []string       `json:"pinRefs"`
	Connections     []acConnResult `json:"connections,omitempty"`
	Status          string         `json:"status"`
	BlockReason     string         `json:"blockReason,omitempty"`
	wireBackups     []crossChipWireBackup
}

type crossChipFixReport struct {
	SchemaVersion int                    `json:"schemaVersion"`
	DocumentID    string                 `json:"documentId"`
	Proposals     []crossChipFixProposal `json:"proposals"`
	Blocked       int                    `json:"blocked"`
	AppliedIDs    []string               `json:"appliedIds,omitempty"`
	Applied       bool                   `json:"applied"`
	Saved         bool                   `json:"saved,omitempty"`
	RolledBack    bool                   `json:"rolledBack,omitempty"`
	RollbackError string                 `json:"rollbackError,omitempty"`
	Notes         []string               `json:"notes,omitempty"`
}

type crossChipFixScene struct {
	projectID  string
	documentID string
	comps      []layoutComp
	wires      []schGroupWire
	items      []visibleTextItem
	sheet      *layoutBBox
	title      *layoutBBox
	proposals  []crossChipFixProposal
}

type appliedCrossChipTree struct {
	Proposal   crossChipFixProposal
	CreatedIDs []string
}

func newSchCrossChipLabelFixCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var treeIDs, out string
	var apply, save, allowPartial bool
	cmd := &cobra.Command{
		Use:   "cross-chip-label-fix",
		Short: "Replace cross-chip direct wire trees with per-pin net labels",
		Long: `Find physical wire trees that directly join pins of two or more active chips
on the same non-power net. A ready plan replaces the entire tree with one short
outward stub plus a native net_label at every connected pin. Existing markers on
the tree, ambiguous/mixed nets, incomplete pin evidence and unsafe label slots
remain blocked.

Apply mode deletes the complete old wire tree, builds all label stubs in one
scenario, verifies the complete pin-to-net map and bridge set, and restores the
original wire primitives if any step or verification fails. Without --apply this
command is read-only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			scene, err := readCrossChipFixScene(pinned, win, doc, stderr)
			if err != nil {
				return err
			}
			wanted := parsePrimitiveIDSet(treeIDs)
			report := crossChipFixReport{
				SchemaVersion: 1, DocumentID: doc,
				Proposals: filterCrossChipFixProposals(scene.proposals, wanted),
			}
			if len(wanted) > 0 && len(report.Proposals) == 0 {
				return fmt.Errorf("cross-chip-label-fix: selected IDs have no proposal")
			}
			for _, p := range report.Proposals {
				if p.Status == "blocked" {
					report.Blocked++
				}
			}
			if err := writeCrossChipFixReport(stdout, out, report); err != nil {
				return err
			}
			if !apply {
				fmt.Fprintln(stderr, "cross-chip-label-fix: dry-run only; pass --apply to write")
				return nil
			}
			if report.Blocked > 0 && !allowPartial {
				return fmt.Errorf("cross-chip-label-fix: %d tree(s) blocked; pass --allow-partial to apply only ready trees", report.Blocked)
			}
			if err := executeCrossChipFix(pinned, win, doc, scene, &report, save); err != nil {
				return err
			}
			return writeCrossChipFixReport(stdout, out, report)
		},
	}
	cmd.Flags().StringVar(&treeIDs, "tree", "", "optional comma-separated wire IDs identifying trees")
	cmd.Flags().BoolVar(&apply, "apply", false, "replace ready cross-chip wire trees")
	cmd.Flags().BoolVar(&save, "save", false, "save after verification")
	cmd.Flags().BoolVar(&allowPartial, "allow-partial", false, "apply ready trees while reporting blocked trees")
	cmd.Flags().StringVar(&out, "out", "", "write structured report to JSON")
	return cmd
}

func writeCrossChipFixReport(stdout io.Writer, out string, report crossChipFixReport) error {
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

func readCrossChipFixScene(pinned *appConfig, win, doc string, stderr io.Writer) (crossChipFixScene, error) {
	geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePins": true, "includePinNets": true, "includePagePrimitives": true,
	})
	if err != nil {
		return crossChipFixScene{}, err
	}
	if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
		return crossChipFixScene{}, fmt.Errorf("cross-chip-label-fix: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
	}
	comps, err := parseLayoutComps(geom.Result)
	if err != nil {
		return crossChipFixScene{}, err
	}
	wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
	if err != nil {
		return crossChipFixScene{}, err
	}
	vt, err := requestAction(pinned, "schematic.visible_text.bboxes", win, nil)
	if err != nil {
		return crossChipFixScene{}, err
	}
	var vision alignmentVision
	raw, _ := json.Marshal(vt.Result)
	if err := json.Unmarshal(raw, &vision); err != nil {
		return crossChipFixScene{}, fmt.Errorf("cross-chip-label-fix: parse visible text: %w", err)
	}
	sheet := sheetBBoxOf(comps)
	titleBlock, _ := titleBlockKeepoutWithSource(sheet)
	findings := crossChipDirectWireFindings(comps, wires)
	proposals := buildCrossChipFixProposals(comps, wires, vision.Items, sheet, titleBlock, findings)
	projectID := ""
	if geom.Context != nil {
		projectID = geom.Context.ProjectUUID
	}
	return crossChipFixScene{
		projectID: projectID, documentID: doc, comps: comps, wires: wires,
		items: vision.Items, sheet: sheet, title: titleBlock, proposals: proposals,
	}, nil
}

func buildCrossChipFixProposals(comps []layoutComp, wires []schGroupWire, items []visibleTextItem,
	sheet, titleBlock *layoutBBox, findings []checkFinding) []crossChipFixProposal {
	find, wireIndex := wireTreeFinder(wires)
	trees := map[int]*crossChipFixProposal{}
	for _, finding := range findings {
		if finding.Type != "cross-chip-direct-wire" {
			continue
		}
		idx, ok := wireIndex[finding.WirePrimitiveId]
		if !ok {
			continue
		}
		root := find(idx)
		p := trees[root]
		if p == nil {
			p = &crossChipFixProposal{Net: strings.TrimSpace(finding.MarkerNet), Status: "blocked"}
			trees[root] = p
		}
		if finding.Designator != "" {
			for _, designator := range strings.Split(finding.Designator, ",") {
				if d := strings.TrimSpace(designator); d != "" {
					p.ChipDesignators = appendUniqueString(p.ChipDesignators, d)
				}
			}
		}
	}
	if len(trees) == 0 {
		return nil
	}
	for root, p := range trees {
		var treeWires []schGroupWire
		for i, wire := range wires {
			if find(i) == root {
				treeWires = append(treeWires, wire)
			}
		}
		sort.Slice(treeWires, func(i, j int) bool { return treeWires[i].ID < treeWires[j].ID })
		p.wireBackups = make([]crossChipWireBackup, 0, len(treeWires))
		for _, wire := range treeWires {
			p.WireIDs = append(p.WireIDs, wire.ID)
			p.wireBackups = append(p.wireBackups, crossChipWireBackup{
				ID: wire.ID, Net: wire.Net, Points: append([]float64(nil), wire.Points...),
				Segments: append([][4]float64(nil), labelWireSegments(wire)...),
			})
		}
		sort.Strings(p.WireIDs)
		sort.Strings(p.ChipDesignators)
		p.ID = p.WireIDs[0]
		if p.Net == "" || strings.Contains(p.Net, ",") {
			p.BlockReason = "ambiguous-shared-signal-net"
			continue
		}
		if isRailLabelNet(p.Net) {
			p.BlockReason = "power-or-ground-tree-not-converted-to-signal-labels"
			continue
		}
		targetPins, reason := crossChipTreePins(p.Net, treeWires, comps)
		if reason != "" {
			p.BlockReason = reason
			continue
		}
		p.PinRefs = make([]string, 0, len(targetPins))
		pins := make([]acPin, 0, len(targetPins))
		for _, target := range targetPins {
			p.PinRefs = append(p.PinRefs, target.ref)
			pins = append(pins, target.pin)
		}
		sort.Strings(p.PinRefs)
		if reason := crossChipTreeMarkerConflict(treeWires, comps); reason != "" {
			p.BlockReason = reason
			continue
		}
		excluded := map[string]bool{}
		for _, id := range p.WireIDs {
			excluded[id] = true
		}
		ac := crossChipAcScene(comps, wires, items, sheet, titleBlock, excluded)
		targetSet := map[string]bool{}
		for _, ref := range p.PinRefs {
			targetSet[ref] = true
		}
		for i := range ac.Pins {
			ref := ac.Pins[i].Designator + ":" + ac.Pins[i].PinNumber
			if targetSet[ref] {
				ac.Pins[i].Net = ""
				ac.Pins[i].NetKnown = true
			}
		}
		connections, reason := planCrossChipLabelConnections(ac, p.Net, p.PinRefs)
		if reason != "" {
			p.BlockReason = reason
			continue
		}
		p.Connections = connections
		p.Status = "ready"
		p.BlockReason = ""
	}
	out := make([]crossChipFixProposal, 0, len(trees))
	for _, p := range trees {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type crossChipTargetPin struct {
	ref string
	pin acPin
}

func crossChipTreePins(net string, treeWires []schGroupWire, comps []layoutComp) ([]crossChipTargetPin, string) {
	var out []crossChipTargetPin
	seen := map[string]bool{}
	for _, c := range comps {
		if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
			continue
		}
		for _, pin := range c.Pins {
			if !wireTreeTouchesPoint(treeWires, pin.X, pin.Y) {
				continue
			}
			ref := c.Designator + ":" + pin.Number
			if c.Designator == "" || pin.Number == "" || seen[ref] {
				continue
			}
			seen[ref] = true
			if strings.TrimSpace(pin.Net) != net {
				return nil, "tree-pin-net-mismatch:" + ref
			}
			out = append(out, crossChipTargetPin{
				ref: ref,
				pin: acPin{
					X: pin.X, Y: pin.Y, Designator: c.Designator, PinNumber: pin.Number,
					OwnerBBox: c.BBox, Net: pin.Net, NetKnown: true,
					PinRotation: layoutPinRotationValue(pin),
				},
			})
		}
	}
	if len(out) < 2 {
		return nil, "fewer-than-two-explicit-pins-on-tree"
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ref < out[j].ref })
	return out, ""
}

func crossChipTreeMarkerConflict(treeWires []schGroupWire, comps []layoutComp) string {
	for _, c := range comps {
		if !isSchMarker(c.ComponentType) || !c.AnchorAvailable {
			continue
		}
		if wireTreeTouchesPoint(treeWires, c.X, c.Y) {
			return "tree-carries-existing-marker:" + c.ID
		}
	}
	return ""
}

func wireTreeTouchesPoint(wires []schGroupWire, x, y float64) bool {
	for _, wire := range wires {
		for _, seg := range labelWireSegments(wire) {
			if pointOnSegment2(x, y, [2]float64{seg[0], seg[1]}, [2]float64{seg[2], seg[3]}) {
				return true
			}
		}
	}
	return false
}

func crossChipAcScene(comps []layoutComp, wires []schGroupWire, items []visibleTextItem,
	sheet, titleBlock *layoutBBox, excluded map[string]bool) acScene {
	scene := acScene{TitleBlock: titleBlock}
	if sheet != nil {
		usable := schUsableArea(*sheet)
		scene.Usable = &usable
	}
	for _, c := range comps {
		if isSchMarker(c.ComponentType) {
			if c.BBox != nil {
				scene.Flags = append(scene.Flags, *c.BBox)
			}
			continue
		}
		if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
			continue
		}
		if c.BBox != nil {
			scene.Parts = append(scene.Parts, *c.BBox)
		}
		scene.Components = append(scene.Components, acComponent{
			Designator: c.Designator, HasPins: len(c.Pins) > 0,
		})
		for _, pin := range c.Pins {
			scene.Pins = append(scene.Pins, acPin{
				X: pin.X, Y: pin.Y, Designator: c.Designator, PinNumber: pin.Number,
				OwnerBBox: c.BBox, PinRotation: layoutPinRotationValue(pin),
				Net: pin.Net, NetKnown: true,
			})
		}
	}
	for _, item := range items {
		if item.Kind == "text" && item.BBox != nil {
			scene.Texts = append(scene.Texts, *item.BBox)
		}
	}
	for _, wire := range wires {
		if excluded[wire.ID] {
			continue
		}
		for _, seg := range labelWireSegments(wire) {
			scene.Wires = append(scene.Wires, wireSegment{
				X0: seg[0], Y0: seg[1], X1: seg[2], Y1: seg[3], Net: wire.Net,
			})
		}
	}
	return scene
}

func layoutPinRotationValue(pin layoutPin) *float64 {
	if !pin.RotationKnown {
		return nil
	}
	rotation := pin.Rotation
	return &rotation
}

func planCrossChipLabelConnections(scene acScene, net string, pinRefs []string) ([]acConnResult, string) {
	rules := defaultAutoconnectRules()
	lanes := map[string]float64{}
	out := make([]acConnResult, 0, len(pinRefs))
	for _, ref := range pinRefs {
		pin, err := resolvePinCoord(scene, ref)
		if err != nil {
			return nil, "pin-resolution-failed:" + ref
		}
		laneFloor := 0.0
		for _, dir := range acDirections {
			if used, ok := lanes[laneKeyOf(pin.Designator, dir)]; ok {
				if need := used + laneStepFor("net_label", net); need > laneFloor {
					laneFloor = need
				}
			}
		}
		all := planConnection(pin, "net_label", net, scene, rules, laneFloor)
		if len(all) == 0 {
			return nil, "no-net-label-candidate:" + ref
		}
		selected := applyLaneStagger(all, lanes, pin.Designator, net, "net_label")
		cr := acConnResult{Pin: ref, Net: net, Kind: "net_label", PinX: pin.X, PinY: pin.Y, Selected: &selected}
		if candidateHardRejected(selected) {
			return nil, "no-safe-net-label-slot:" + ref + ":" + dominantReason(selected)
		}
		if !applySelectionGate(&cr, selected, true) {
			return nil, "net-label-candidate-tainted:" + ref
		}
		out = append(out, cr)
		lanes[laneKeyOf(pin.Designator, selected.Direction)] = selected.Offset
		from := acPoint{X: pin.X, Y: pin.Y}
		if selected.Via != nil {
			scene.Wires = append(scene.Wires, wireSegment{X0: pin.X, Y0: pin.Y, X1: selected.Via.X, Y1: selected.Via.Y, Net: net})
			from = *selected.Via
		}
		scene.Wires = append(scene.Wires, wireSegment{X0: from.X, Y0: from.Y, X1: selected.EndPoint.X, Y1: selected.EndPoint.Y, Net: net})
	}
	return out, ""
}

func filterCrossChipFixProposals(in []crossChipFixProposal, wanted map[string]bool) []crossChipFixProposal {
	if len(wanted) == 0 {
		return in
	}
	var out []crossChipFixProposal
	for _, p := range in {
		matched := wanted[p.ID]
		for _, id := range p.WireIDs {
			matched = matched || wanted[id]
		}
		if matched {
			out = append(out, p)
		}
	}
	return out
}

func executeCrossChipFix(pinned *appConfig, win, doc string, scene crossChipFixScene,
	report *crossChipFixReport, save bool) error {
	beforeNets, _, err := readLiveNets(pinned, win)
	if err != nil {
		return fmt.Errorf("cross-chip-label-fix: baseline netlist: %w", err)
	}
	beforeBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("cross-chip-label-fix: baseline bridge set: %w", err)
	}
	var applied []appliedCrossChipTree
	rollback := func(cause error) error {
		rbErr := rollbackCrossChipTrees(pinned, win, doc, applied)
		report.RolledBack = rbErr == nil
		if rbErr != nil {
			report.RollbackError = rbErr.Error()
			return fmt.Errorf("%v; cross-chip rollback FAILED (%v)", cause, rbErr)
		}
		return fmt.Errorf("%v; cross-chip trees were rolled back", cause)
	}
	for i := range report.Proposals {
		p := &report.Proposals[i]
		if p.Status != "ready" {
			continue
		}
		if err := verifyCrossChipTreeStillCurrent(pinned, win, doc, *p); err != nil {
			return rollback(err)
		}
		ids := []any{}
		for _, id := range p.WireIDs {
			ids = append(ids, id)
		}
		deleted, err := requestAutolayoutAction(pinned, "schematic.primitives.delete", win,
			map[string]any{"primitiveIds": ids}, doc, "cross-chip-label-fix 删除旧线树")
		if err != nil {
			return rollback(err)
		}
		if nf := countDeleteNotFound(deleted.Result["notFound"]); nf > 0 {
			return rollback(fmt.Errorf("old tree %s had %d missing wire(s)", p.ID, nf))
		}
		conns := make([]acConnSpec, 0, len(p.PinRefs))
		for _, ref := range p.PinRefs {
			conns = append(conns, acConnSpec{PinRef: ref, Kind: "net_label", Net: p.Net})
		}
		acReport, runErr := runAutoconnectOpts(pinned, win, conns, defaultAutoconnectRules(),
			acRunOpts{Strict: true, FreshPins: true}, io.Discard, io.Discard)
		var created []string
		for _, cr := range acReport.Connections {
			if cr.WirePrimitiveID != "" {
				created = append(created, cr.WirePrimitiveID)
			}
			if cr.FlagPrimitiveID != "" {
				created = append(created, cr.FlagPrimitiveID)
			}
		}
		applied = append(applied, appliedCrossChipTree{Proposal: *p, CreatedIDs: created})
		if runErr != nil || !acReport.OK {
			if runErr == nil {
				runErr = fmt.Errorf("autoconnect reported failure")
			}
			return rollback(fmt.Errorf("tree %s: %w", p.ID, runErr))
		}
		p.Connections = acReport.Connections
		report.AppliedIDs = append(report.AppliedIDs, p.ID)
		if err := verifyCrossChipBaseline(pinned, win, doc, beforeNets, beforeBridges); err != nil {
			return rollback(err)
		}
	}
	if save {
		saved, serr := requestAutolayoutAction(pinned, "schematic.save", win, nil, doc, "cross-chip-label-fix 保存")
		if serr != nil || saved.Result["saved"] != true {
			if serr == nil {
				serr = fmt.Errorf("schematic.save did not return saved:true")
			}
			return rollback(fmt.Errorf("cross-chip-label-fix: save: %w", serr))
		}
		report.Saved = true
	}
	report.Applied = true
	return nil
}

func verifyCrossChipTreeStillCurrent(pinned *appConfig, win, doc string, p crossChipFixProposal) error {
	scene, err := readCrossChipFixScene(pinned, win, doc, io.Discard)
	if err != nil {
		return err
	}
	byID := map[string]schGroupWire{}
	for _, wire := range scene.wires {
		byID[wire.ID] = wire
	}
	for _, backup := range p.wireBackups {
		wire, ok := byID[backup.ID]
		if !ok || strings.TrimSpace(wire.Net) != strings.TrimSpace(backup.Net) ||
			!sameFloatPoints(wire.Points, backup.Points) {
			return fmt.Errorf("cross-chip tree %s changed before apply", p.ID)
		}
	}
	return nil
}

func sameFloatPoints(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > 0.01 {
			return false
		}
	}
	return true
}

func rollbackCrossChipTrees(pinned *appConfig, win, doc string, applied []appliedCrossChipTree) error {
	for i := len(applied) - 1; i >= 0; i-- {
		tree := applied[i]
		if len(tree.CreatedIDs) > 0 {
			ids := make([]any, 0, len(tree.CreatedIDs))
			for _, id := range tree.CreatedIDs {
				ids = append(ids, id)
			}
			if _, err := requestAutolayoutAction(pinned, "schematic.primitives.delete", win,
				map[string]any{"primitiveIds": ids}, doc, "cross-chip-label-fix 回滚新标签"); err != nil {
				return err
			}
		}
		for _, backup := range tree.Proposal.wireBackups {
			points := backup.Points
			if len(points) == 0 {
				for _, seg := range backup.Segments {
					points = append(points, seg[0], seg[1], seg[2], seg[3])
				}
			}
			if _, err := requestAutolayoutAction(pinned, "schematic.wire.create", win,
				map[string]any{"points": points, "net": backup.Net}, doc, "cross-chip-label-fix 恢复旧线树"); err != nil {
				return err
			}
		}
	}
	return nil
}

func verifyCrossChipBaseline(pinned *appConfig, win, doc string,
	beforeNets map[string]map[string]bool, beforeBridges []string) error {
	afterNets, _, err := readLiveNets(pinned, win)
	if err != nil || !dcNetsEqual(beforeNets, afterNets) {
		return fmt.Errorf("cross-chip-label-fix: pin netlist differs from baseline (%v)", err)
	}
	afterBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("cross-chip-label-fix: bridge verification failed: %w", err)
	}
	if added := dcNewBridgeSignatures(beforeBridges, afterBridges); len(added) > 0 {
		return fmt.Errorf("cross-chip-label-fix: bridge set gained %s", strings.Join(added, ","))
	}
	return nil
}
