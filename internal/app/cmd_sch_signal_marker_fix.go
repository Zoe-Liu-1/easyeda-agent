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

type signalMarkerFixProposal struct {
	MarkerID       string         `json:"markerId"`
	Net            string         `json:"net"`
	FromType       string         `json:"fromType"`
	Mode           string         `json:"mode"`
	Anchor         *checkPoint    `json:"anchor"`
	AnchorWireID   string         `json:"anchorWireId"`
	AnchorBlockers []string       `json:"anchorBlockers,omitempty"`
	Host           *checkPoint    `json:"host"`
	Direction      string         `json:"direction"`
	StubWireID     string         `json:"stubWireId"`
	NewStubPath    [][2]float64   `json:"newStubPath"`
	Status         string         `json:"status"`
	Reason         string         `json:"reason,omitempty"`
	Original       *layoutComp    `json:"-"`
	OriginalStub   *destaggerStub `json:"-"`
}

type signalMarkerFixReport struct {
	SchemaVersion int                       `json:"schemaVersion"`
	DocumentID    string                    `json:"documentId"`
	Proposals     []signalMarkerFixProposal `json:"proposals"`
	Blocked       int                       `json:"blocked"`
	AppliedIDs    []string                  `json:"appliedIds,omitempty"`
	Applied       bool                      `json:"applied"`
	Saved         bool                      `json:"saved,omitempty"`
	RolledBack    bool                      `json:"rolledBack,omitempty"`
	RollbackError string                    `json:"rollbackError,omitempty"`
}

type signalMarkerScene struct {
	comps     []layoutComp
	wires     []schGroupWire
	items     []visibleTextItem
	sheet     *layoutBBox
	title     *layoutBBox
	proposals []signalMarkerFixProposal
}

type appliedSignalMarker struct {
	Proposal        signalMarkerFixProposal
	NewLabelID      string
	LabelParentWire string
	MarkerDeleted   bool
}

func newSchSignalMarkerFixCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var ids, out string
	var apply, save, allowPartial bool
	cmd := &cobra.Command{
		Use:   "signal-marker-fix",
		Short: "Replace signal netflags/netports with text net labels",
		Long: `Convert signal-net netflag/netport warnings to native text net labels.
The safest conversion keeps every existing wire tree unchanged: the marker
must sit on exactly one identified wire vertex, and the new native label is
bound to that same parent wire. Labels that would overlap another primitive or
leave the sheet stay blocked.
Module-boundary ports must be registered with signal-port-exempt instead.

Apply mode creates and verifies the label before deleting the old marker. It
compares the complete pin-to-net map and bridge set for every conversion and
restores the original marker on regression.
Without --apply this command is read-only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			scene, err := readSignalMarkerScene(pinned, win, doc)
			if err != nil {
				return err
			}
			wanted := parsePrimitiveIDSet(ids)
			report := signalMarkerFixReport{
				SchemaVersion: 1, DocumentID: doc,
				Proposals: filterSignalMarkerProposals(scene.proposals, wanted),
			}
			if len(wanted) > 0 && len(report.Proposals) == 0 {
				return fmt.Errorf("signal-marker-fix: selected IDs have no conversion proposal")
			}
			for _, p := range report.Proposals {
				if p.Status == "blocked" {
					report.Blocked++
				}
			}
			if err := writeSignalMarkerReport(stdout, out, report); err != nil {
				return err
			}
			if !apply {
				fmt.Fprintln(stderr, "signal-marker-fix: dry-run only; pass --apply to write")
				return nil
			}
			if report.Blocked > 0 && !allowPartial {
				return fmt.Errorf("signal-marker-fix: %d proposal(s) are blocked; pass --allow-partial to convert ready markers", report.Blocked)
			}
			if err := executeSignalMarkerFix(pinned, win, doc, &report, save); err != nil {
				return err
			}
			return writeSignalMarkerReport(stdout, out, report)
		},
	}
	cmd.Flags().StringVar(&ids, "id", "", "optional comma-separated marker IDs")
	cmd.Flags().BoolVar(&apply, "apply", false, "replace ready signal markers")
	cmd.Flags().BoolVar(&save, "save", false, "save after fresh verification")
	cmd.Flags().BoolVar(&allowPartial, "allow-partial", false, "convert ready markers while reporting blocked markers")
	cmd.Flags().StringVar(&out, "out", "", "write structured report to JSON")
	return cmd
}

func writeSignalMarkerReport(stdout io.Writer, out string, report signalMarkerFixReport) error {
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

func readSignalMarkerScene(pinned *appConfig, win, doc string) (signalMarkerScene, error) {
	geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePins": true, "includePinNets": true, "includePagePrimitives": true,
	})
	if err != nil {
		return signalMarkerScene{}, err
	}
	if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
		return signalMarkerScene{}, fmt.Errorf("signal-marker-fix: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
	}
	comps, err := parseLayoutComps(geom.Result)
	if err != nil {
		return signalMarkerScene{}, err
	}
	wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
	if err != nil {
		return signalMarkerScene{}, err
	}
	vt, err := requestAction(pinned, "schematic.visible_text.bboxes", win, nil)
	if err != nil {
		return signalMarkerScene{}, err
	}
	var vision alignmentVision
	raw, _ := json.Marshal(vt.Result)
	if err := json.Unmarshal(raw, &vision); err != nil {
		return signalMarkerScene{}, fmt.Errorf("signal-marker-fix: parse visible text: %w", err)
	}
	sheet := sheetBBoxOf(comps)
	titleBlock, _ := titleBlockKeepoutWithSource(sheet)
	proposals := buildSignalMarkerProposals(comps, wires, vision.Items, sheet, titleBlock)
	return signalMarkerScene{
		comps: comps, wires: wires, items: vision.Items,
		sheet: sheet, title: titleBlock, proposals: proposals,
	}, nil
}

func buildSignalMarkerProposals(comps []layoutComp, wires []schGroupWire,
	items []visibleTextItem, sheet, titleBlock *layoutBBox) []signalMarkerFixProposal {
	var out []signalMarkerFixProposal
	for _, c := range comps {
		if c.ComponentType != "netflag" && c.ComponentType != "netport" {
			continue
		}
		if c.Net == "" || isRailLabelNet(c.Net) || c.OrientationExempt {
			continue
		}
		p := signalMarkerFixProposal{
			MarkerID: c.ID, Net: c.Net, FromType: c.ComponentType, Status: "blocked",
			Mode: "existing-wire-label",
		}
		wireID, reason := signalMarkerAnchorWire(c, wires)
		if wireID == "" {
			p.Reason = "unsafe-marker-anchor:" + reason
			out = append(out, p)
			continue
		}
		if signalMarkerParentAlreadyNamed(items, wireID, c.Net) {
			p.Reason = "existing-net-label-on-parent-wire"
			out = append(out, p)
			continue
		}
		var anchor *checkPoint
		blockers := map[string]bool{}
		for _, candidate := range signalLabelAnchorCandidates(c, signalMarkerWireByID(wires, wireID)) {
			newBox := predictedMarkerBBox(candidate.X, candidate.Y, "net_label", "right", c.Net)
			if blocker := signalMarkerLabelBoxBlocker(c, newBox, items, comps, wires, wireID, sheet, titleBlock); blocker == "" {
				anchor = &candidate
				break
			} else {
				blockers[blocker] = true
			}
		}
		if anchor == nil {
			for blocker := range blockers {
				p.AnchorBlockers = append(p.AnchorBlockers, blocker)
			}
			sort.Strings(p.AnchorBlockers)
			p.Reason = "no-clear-net-label-anchor-on-parent-wire"
			out = append(out, p)
			continue
		}
		p.Anchor = anchor
		p.AnchorWireID = wireID
		p.Direction = "right"
		p.Status = "ready"
		p.Original = &c
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MarkerID < out[j].MarkerID })
	return out
}

func signalMarkerWireByID(wires []schGroupWire, id string) schGroupWire {
	for _, wire := range wires {
		if wire.ID == id {
			return wire
		}
	}
	return schGroupWire{}
}

// signalLabelAnchorCandidates keeps the electrical parent wire unchanged and
// searches in 5-raw steps away from the old marker along its incident segment.
// Native text net labels are attached by coordinate, so an interior point on the
// same wire is a pure annotation move; no wire or pin is disconnected.
func signalLabelAnchorCandidates(c layoutComp, wire schGroupWire) []checkPoint {
	candidates := []checkPoint{{X: c.X, Y: c.Y}}
	const maxDistance = 80.0
	for _, seg := range labelWireSegments(wire) {
		var ox, oy, dx, dy, length float64
		switch {
		case pointsClose(c.X, c.Y, seg[0], seg[1], 0.5):
			ox, oy = seg[0], seg[1]
			dx, dy = seg[2]-seg[0], seg[3]-seg[1]
			length = math.Hypot(dx, dy)
		case pointsClose(c.X, c.Y, seg[2], seg[3], 0.5):
			ox, oy = seg[2], seg[3]
			dx, dy = seg[0]-seg[2], seg[1]-seg[3]
			length = math.Hypot(dx, dy)
		default:
			continue
		}
		if length < 1 {
			continue
		}
		ux, uy := dx/length, dy/length
		limit := math.Min(length, maxDistance)
		for distance := 5.0; distance <= limit+0.01; distance += 5 {
			candidates = append(candidates, checkPoint{
				X: ox + ux*distance,
				Y: oy + uy*distance,
			})
		}
	}
	return candidates
}

func signalMarkerParentAlreadyNamed(items []visibleTextItem, parentWireID, net string) bool {
	for _, item := range items {
		if item.Kind == "attribute" && item.ComponentType == "wire" &&
			item.ParentID == parentWireID && item.Key == "Name" && item.Value == net {
			return true
		}
	}
	return false
}

// signalMarkerLabelBoxBlocker checks actual positive-area overlap. Dense pin
// rows commonly leave 2-4 raw between two 8-raw-tall labels or between a label
// and the next parallel wire; inflating every obstacle would reject a visibly
// non-overlapping row and report a false blocker. A tiny inset keeps a label's
// own baseline touching a foreign parallel wire from counting as coverage.
func signalMarkerLabelBoxBlocker(c layoutComp, newBox layoutBBox, items []visibleTextItem,
	comps []layoutComp, wires []schGroupWire, parentWireID string, sheet, titleBlock *layoutBBox) string {
	if sheet == nil {
		return "sheet-unavailable"
	}
	usable := schUsableArea(*sheet)
	if !boxInside2(newBox, usable) {
		return "outside-usable-sheet"
	}
	if titleBlock != nil && positiveOverlap(newBox, *titleBlock) {
		return "title-block"
	}
	interior := newBox
	if interior.MaxX-interior.MinX > 0.2 && interior.MaxY-interior.MinY > 0.2 {
		interior.MinX += 0.1
		interior.MinY += 0.1
		interior.MaxX -= 0.1
		interior.MaxY -= 0.1
	}
	for _, o := range comps {
		if o.ID == c.ID || o.BBox == nil || o.ComponentType == "sheet" {
			continue
		}
		if positiveOverlap(newBox, *o.BBox) {
			return "component:" + o.ID + ":" + o.ComponentType
		}
		for _, pin := range o.Pins {
			if powerOrientationPinLeadCrossesBox(o, pin, interior) {
				return "pin-lead:" + o.Designator + ":" + pin.Number
			}
		}
	}
	for _, item := range items {
		if item.ParentID == c.ID || item.BBox == nil {
			continue
		}
		if positiveOverlap(newBox, *item.BBox) {
			return "text:" + item.ID + ":" + item.Value
		}
	}
	for _, wire := range wires {
		if wire.ID == parentWireID {
			continue
		}
		for _, seg := range labelWireSegments(wire) {
			if schSegmentCrossesBox(seg[0], seg[1], seg[2], seg[3], interior) {
				return "wire:" + wire.ID
			}
		}
	}
	return ""
}

func signalMarkerAnchorWire(c layoutComp, wires []schGroupWire) (string, string) {
	if !c.AnchorAvailable {
		return "", "no-anchor"
	}
	var matches []schGroupWire
	for _, wire := range wires {
		if wire.Net != "" && c.Net != "" && wire.Net != c.Net {
			continue
		}
		found := false
		for _, seg := range labelWireSegments(wire) {
			if pointsClose(c.X, c.Y, seg[0], seg[1], 0.5) ||
				pointsClose(c.X, c.Y, seg[2], seg[3], 0.5) {
				found = true
				break
			}
		}
		if found {
			matches = append(matches, wire)
		}
	}
	switch len(matches) {
	case 0:
		return "", "not-on-wire-endpoint"
	case 1:
		return matches[0].ID, ""
	default:
		ids := make([]string, 0, len(matches))
		for _, match := range matches {
			ids = append(ids, match.ID)
		}
		sort.Strings(ids)
		return "", "ambiguous-wire-endpoint:" + strings.Join(ids, ",")
	}
}

func filterSignalMarkerProposals(in []signalMarkerFixProposal, wanted map[string]bool) []signalMarkerFixProposal {
	if len(wanted) == 0 {
		return in
	}
	var out []signalMarkerFixProposal
	for _, p := range in {
		if wanted[p.MarkerID] {
			out = append(out, p)
		}
	}
	return out
}

func executeSignalMarkerFix(pinned *appConfig, win, doc string,
	report *signalMarkerFixReport, save bool) error {
	beforeNets, _, err := readLiveNets(pinned, win)
	if err != nil {
		return fmt.Errorf("signal-marker-fix: baseline netlist: %w", err)
	}
	beforeBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("signal-marker-fix: baseline bridge set: %w", err)
	}
	var applied []appliedSignalMarker
	rollback := func(cause error) error {
		if len(applied) == 0 {
			return cause
		}
		rbErr := rollbackSignalMarkers(pinned, win, doc, applied)
		report.RolledBack = rbErr == nil
		if rbErr != nil {
			report.RollbackError = rbErr.Error()
			return fmt.Errorf("%v; signal-marker rollback FAILED (%v)", cause, rbErr)
		}
		return fmt.Errorf("%v; signal-marker replacements were rolled back", cause)
	}
	for _, proposal := range report.Proposals {
		if proposal.Status != "ready" {
			continue
		}
		app, err := executeSignalMarkerProposal(pinned, win, doc, proposal)
		if app.NewLabelID != "" || app.MarkerDeleted {
			applied = append(applied, app)
		}
		if err != nil {
			return rollback(err)
		}
		report.AppliedIDs = append(report.AppliedIDs, proposal.MarkerID)
		if err := verifySignalMarkerBaseline(pinned, win, doc, beforeNets, beforeBridges); err != nil {
			return rollback(err)
		}
	}
	after, err := readSignalMarkerScene(pinned, win, doc)
	if err != nil {
		return rollback(err)
	}
	changed := map[string]bool{}
	for _, id := range report.AppliedIDs {
		changed[id] = true
	}
	for _, f := range signalMarkerTypeFindings(after.comps, nil) {
		if changed[f.PrimitiveId] {
			return rollback(fmt.Errorf("signal marker finding remains for %s", f.PrimitiveId))
		}
	}
	if err := verifySignalMarkerBaseline(pinned, win, doc, beforeNets, beforeBridges); err != nil {
		return rollback(err)
	}
	if save {
		saved, serr := requestAutolayoutAction(pinned, "schematic.save", win, nil, doc, "signal-marker-fix 保存")
		if serr != nil {
			return fmt.Errorf("signal-marker-fix: save: %w", serr)
		}
		ok, _ := saved.Result["saved"].(bool)
		if !ok {
			return fmt.Errorf("signal-marker-fix: schematic.save did not return saved:true")
		}
		report.Saved = true
	}
	report.Applied = true
	return nil
}

func executeSignalMarkerProposal(pinned *appConfig, win, doc string,
	p signalMarkerFixProposal) (appliedSignalMarker, error) {
	app := appliedSignalMarker{Proposal: p}
	if p.Anchor == nil || p.AnchorWireID == "" {
		return app, fmt.Errorf("existing-wire label anchor unavailable")
	}
	created, err := requestAutolayoutAction(pinned, "schematic.netflag.create", win, map[string]any{
		"kind": "net_label", "net": p.Net, "x": p.Anchor.X, "y": p.Anchor.Y,
		"expectedParentWireId": p.AnchorWireID,
	}, doc, "signal-marker-fix 创建网络标签")
	if err != nil {
		return app, err
	}
	if created.Result["verified"] != true {
		return app, fmt.Errorf("new net label was not verified on the existing wire")
	}
	app.NewLabelID, _ = created.Result["primitiveId"].(string)
	app.LabelParentWire, _ = created.Result["parentPrimitiveId"].(string)
	if app.NewLabelID == "" || app.LabelParentWire != p.AnchorWireID {
		return app, fmt.Errorf("new net label identity/parent is incomplete (label=%q parent=%q expected=%q)",
			app.NewLabelID, app.LabelParentWire, p.AnchorWireID)
	}
	deleted, err := requestAutolayoutAction(pinned, "schematic.primitives.delete", win, map[string]any{
		"primitiveIds": []string{p.MarkerID},
	}, doc, "signal-marker-fix 删除旧信号标记")
	if err == nil {
		app.MarkerDeleted = markerDeleteProven(deleted.Result)
	}
	if !app.MarkerDeleted {
		present, readErr := signalMarkerPresent(pinned, win, doc, p.MarkerID)
		if readErr == nil && !present {
			app.MarkerDeleted = true
		}
	}
	if !app.MarkerDeleted {
		if err != nil {
			return app, err
		}
		return app, fmt.Errorf("old signal marker deletion was not proven")
	}
	return app, nil
}

func rollbackSignalMarkers(pinned *appConfig, win, doc string, applied []appliedSignalMarker) error {
	for i := len(applied) - 1; i >= 0; i-- {
		app := applied[i]
		if app.MarkerDeleted {
			kind := tidyRestoreKind(app.Proposal.FromType, app.Proposal.Net)
			if kind == "" || app.Proposal.Original == nil {
				return fmt.Errorf("cannot restore marker type %s", app.Proposal.FromType)
			}
			original := app.Proposal.Original
			rotation := 0.0
			if original.Rotation != nil {
				rotation = *original.Rotation
			}
			restored, err := requestAutolayoutAction(pinned, "schematic.netflag.create", win, map[string]any{
				"kind": kind, "net": app.Proposal.Net, "x": original.X, "y": original.Y,
				"rotation": rotation,
			}, doc, "signal-marker-fix 恢复旧标记")
			if err != nil {
				return err
			}
			if id, _ := restored.Result["primitiveId"].(string); id == "" {
				return fmt.Errorf("restored marker has no verified primitive ID")
			}
		}
		if app.NewLabelID != "" {
			deleted, err := requestAutolayoutAction(pinned, "schematic.net_label.delete", win, map[string]any{
				"items": []map[string]any{{
					"primitiveId":  app.NewLabelID,
					"parentWireId": app.LabelParentWire,
					"net":          app.Proposal.Net,
				}},
			}, doc, "signal-marker-fix 回滚网络标签")
			if err != nil {
				return err
			}
			if deleted.Result["verified"] != true {
				return fmt.Errorf("new net label rollback was not verified")
			}
		}
	}
	return nil
}

func markerDeleteProven(result map[string]any) bool {
	if result["partial"] == true || result["verified"] == false {
		return false
	}
	if total, ok := result["total"].(float64); ok && total == 1 {
		return true
	}
	if deletedIDs, ok := result["deletedIds"].(map[string]any); ok {
		for _, raw := range deletedIDs {
			if ids, ok := raw.([]any); ok && len(ids) == 1 {
				return true
			}
			if ids, ok := raw.([]string); ok && len(ids) == 1 {
				return true
			}
		}
	}
	return false
}

func signalMarkerPresent(pinned *appConfig, win, doc, markerID string) (bool, error) {
	res, err := requestAutolayoutAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePagePrimitives": true,
	}, doc, "signal-marker-fix 确认旧标记删除")
	if err != nil {
		return false, err
	}
	comps, err := parseLayoutComps(res.Result)
	if err != nil {
		return false, err
	}
	for _, c := range comps {
		if c.ID == markerID {
			return true, nil
		}
	}
	return false, nil
}

func verifySignalMarkerBaseline(pinned *appConfig, win, doc string,
	beforeNets map[string]map[string]bool, beforeBridges []string) error {
	afterNets, _, err := readLiveNets(pinned, win)
	if err != nil || !dcNetsEqual(beforeNets, afterNets) {
		return fmt.Errorf("signal-marker-fix: pin netlist differs from baseline (%v)", err)
	}
	afterBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("signal-marker-fix: bridge verification failed: %w", err)
	}
	if added := dcNewBridgeSignatures(beforeBridges, afterBridges); len(added) > 0 {
		return fmt.Errorf("signal-marker-fix: bridge set gained %s", strings.Join(added, ","))
	}
	return nil
}
