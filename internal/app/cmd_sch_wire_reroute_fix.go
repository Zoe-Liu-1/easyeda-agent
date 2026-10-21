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

type wireRerouteProposal struct {
	WireID      string       `json:"wireId"`
	Net         string       `json:"net"`
	Reasons     []string     `json:"reasons"`
	Original    [4]float64   `json:"original"`
	NewPath     [][2]float64 `json:"newPath,omitempty"`
	NewID       string       `json:"newId,omitempty"`
	PinRefs     []string     `json:"pinRefs,omitempty"`
	Status      string       `json:"status"`
	BlockReason string       `json:"blockReason,omitempty"`
}

type wireRerouteReport struct {
	SchemaVersion int                   `json:"schemaVersion"`
	DocumentID    string                `json:"documentId"`
	Proposals     []wireRerouteProposal `json:"proposals"`
	Blocked       int                   `json:"blocked"`
	AppliedIDs    []string              `json:"appliedIds,omitempty"`
	Applied       bool                  `json:"applied"`
	Saved         bool                  `json:"saved,omitempty"`
	RolledBack    bool                  `json:"rolledBack,omitempty"`
	RollbackError string                `json:"rollbackError,omitempty"`
}

type wireRerouteScene struct {
	comps     []layoutComp
	wires     []schGroupWire
	items     []visibleTextItem
	sheet     *layoutBBox
	title     *layoutBBox
	proposals []wireRerouteProposal
}

func newSchWireRerouteFixCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var wireIDs, out string
	var apply, save, allowPartial bool
	cmd := &cobra.Command{
		Use:   "wire-reroute-fix",
		Short: "Reroute isolated wires that cross pins or component bodies",
		Long: `Plan orthogonal replacement paths for wire-over-pin, wire-over-pin-lead and
wire-through-body findings. Automatic execution is deliberately limited to one
wire primitive with one physical segment whose two endpoints are explicit pins
or markers and whose replacement is a bounded obstacle-aware orthogonal route.
Branched/shared trees and cross-chip direct wires remain blocked for manual
functional replanning.

Apply mode deletes the old wire, creates the planned polyline, compares the full
pin-to-net map and bridge set, and restores the original wire on regression.
Without --apply this command is read-only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			scene, err := readWireRerouteScene(pinned, win, doc)
			if err != nil {
				return err
			}
			wanted := parsePrimitiveIDSet(wireIDs)
			report := wireRerouteReport{
				SchemaVersion: 1, DocumentID: doc,
				Proposals: filterWireRerouteProposals(scene.proposals, wanted),
			}
			if len(wanted) > 0 && len(report.Proposals) == 0 {
				return fmt.Errorf("wire-reroute-fix: selected IDs have no reroute proposal")
			}
			for _, p := range report.Proposals {
				if p.Status == "blocked" {
					report.Blocked++
				}
			}
			if err := writeWireRerouteReport(stdout, out, report); err != nil {
				return err
			}
			if !apply {
				fmt.Fprintln(stderr, "wire-reroute-fix: dry-run only; pass --apply to write")
				return nil
			}
			if report.Blocked > 0 && !allowPartial {
				return fmt.Errorf("wire-reroute-fix: %d proposal(s) are blocked; pass --allow-partial to apply only ready wires", report.Blocked)
			}
			if err := executeWireRerouteFix(pinned, win, doc, scene, &report, save); err != nil {
				return err
			}
			return writeWireRerouteReport(stdout, out, report)
		},
	}
	cmd.Flags().StringVar(&wireIDs, "wire", "", "optional comma-separated wire primitive IDs")
	cmd.Flags().BoolVar(&apply, "apply", false, "replace ready isolated wires")
	cmd.Flags().BoolVar(&save, "save", false, "save after fresh verification")
	cmd.Flags().BoolVar(&allowPartial, "allow-partial", false, "apply ready wires while reporting blocked wires")
	cmd.Flags().StringVar(&out, "out", "", "write structured report to JSON")
	return cmd
}

func writeWireRerouteReport(stdout io.Writer, out string, report wireRerouteReport) error {
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

func readWireRerouteScene(pinned *appConfig, win, doc string) (wireRerouteScene, error) {
	geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePins": true, "includePinNets": true, "includePagePrimitives": true,
	})
	if err != nil {
		return wireRerouteScene{}, err
	}
	if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
		return wireRerouteScene{}, fmt.Errorf("wire-reroute-fix: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
	}
	comps, err := parseLayoutComps(geom.Result)
	if err != nil {
		return wireRerouteScene{}, err
	}
	wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
	if err != nil {
		return wireRerouteScene{}, err
	}
	vt, err := requestAction(pinned, "schematic.visible_text.bboxes", win, nil)
	if err != nil {
		return wireRerouteScene{}, err
	}
	var vision alignmentVision
	raw, _ := json.Marshal(vt.Result)
	if err := json.Unmarshal(raw, &vision); err != nil {
		return wireRerouteScene{}, fmt.Errorf("wire-reroute-fix: parse visible text: %w", err)
	}
	checkRes, err := requestAction(pinned, "schematic.check", win, nil)
	if err != nil {
		return wireRerouteScene{}, err
	}
	rep, err := parseCheckReport(checkRes.Result)
	if err != nil {
		return wireRerouteScene{}, err
	}
	findings := append([]checkFinding(nil), rep.Findings...)
	findings = append(findings, schWireGeometryFindings(geom.Result, wires)...)
	findings = append(findings, crossChipDirectWireFindings(comps, wires)...)
	sheet := sheetBBoxOf(comps)
	titleBlock, _ := titleBlockKeepoutWithSource(sheet)
	proposals := buildWireRerouteProposals(findings, comps, wires, vision.Items, sheet, titleBlock)
	return wireRerouteScene{
		comps: comps, wires: wires, items: vision.Items,
		sheet: sheet, title: titleBlock, proposals: proposals,
	}, nil
}

func buildWireRerouteProposals(findings []checkFinding, comps []layoutComp,
	wires []schGroupWire, items []visibleTextItem, sheet, titleBlock *layoutBBox) []wireRerouteProposal {
	byWire := map[string]*wireRerouteProposal{}
	for _, finding := range findings {
		switch finding.Type {
		case "wire-over-pin", "wire-over-pin-lead", "wire-through-body", "cross-chip-direct-wire":
		default:
			continue
		}
		id := finding.WirePrimitiveId
		if id == "" {
			continue
		}
		p := byWire[id]
		if p == nil {
			p = &wireRerouteProposal{WireID: id, Status: "blocked"}
			byWire[id] = p
		}
		p.Reasons = appendUniqueString(p.Reasons, finding.Type)
	}
	var out []wireRerouteProposal
	for _, p := range byWire {
		wire, ok := wireByID(wires, p.WireID)
		if !ok {
			p.BlockReason = "wire-missing"
			out = append(out, *p)
			continue
		}
		p.Net = wire.Net
		segs := labelWireSegments(wire)
		if len(segs) != 1 {
			p.BlockReason = "multi-segment-tree-requires-functional-replan"
			out = append(out, *p)
			continue
		}
		seg := segs[0]
		p.Original = seg
		if stringInSlice("cross-chip-direct-wire", p.Reasons) {
			p.BlockReason = "cross-chip-requires-net-label-conversion"
			out = append(out, *p)
			continue
		}
		aEnd, okA := resolveWireRerouteEndpoint(comps, seg[0], seg[1])
		bEnd, okB := resolveWireRerouteEndpoint(comps, seg[2], seg[3])
		if !okA || !okB {
			p.BlockReason = "endpoint-not-an-explicit-pin-or-marker"
			out = append(out, *p)
			continue
		}
		p.PinRefs = []string{aEnd.ref, bEnd.ref}
		path, ok := wireReroutePath(wire, seg, aEnd, bEnd, comps, wires, items, sheet, titleBlock)
		if !ok {
			p.BlockReason = "no-clear-orthogonal-route"
			out = append(out, *p)
			continue
		}
		p.NewPath = path
		p.Status = "ready"
		p.BlockReason = ""
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WireID < out[j].WireID })
	return out
}

func wireByID(wires []schGroupWire, id string) (schGroupWire, bool) {
	for _, w := range wires {
		if w.ID == id {
			return w, true
		}
	}
	return schGroupWire{}, false
}

type wireRerouteEndpoint struct {
	ref       string
	kind      string
	outward   string
	connected bool
}

func resolveWireRerouteEndpoint(comps []layoutComp, x, y float64) (wireRerouteEndpoint, bool) {
	for _, c := range comps {
		if isSchMarker(c.ComponentType) && c.AnchorAvailable &&
			math.Abs(c.X-x) <= 0.5 && math.Abs(c.Y-y) <= 0.5 {
			return wireRerouteEndpoint{ref: c.ID, kind: "marker", connected: true}, true
		}
		if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
			continue
		}
		for _, p := range c.Pins {
			if math.Abs(p.X-x) <= 0.5 && math.Abs(p.Y-y) <= 0.5 {
				dir, _ := pinCardinalOutward(p)
				return wireRerouteEndpoint{
					ref: c.Designator + ":" + p.Number, kind: "pin", outward: dir, connected: true,
				}, true
			}
		}
	}
	return wireRerouteEndpoint{}, false
}

func wireReroutePath(wire schGroupWire, seg [4]float64, aEnd, bEnd wireRerouteEndpoint,
	comps []layoutComp, wires []schGroupWire, items []visibleTextItem,
	sheet, titleBlock *layoutBBox) ([][2]float64, bool) {
	find, wireIndex := wireTreeFinder(wires)
	idx, ok := wireIndex[wire.ID]
	if !ok {
		return nil, false
	}
	root := find(idx)
	clear := func(p, q [2]float64) bool {
		for _, blocker := range directPathBlockers([][2]float64{p, q}, root, root,
			"", "", items, comps, wires, find, wireIndex) {
			if blocker.ID == wire.ID || blocker.ID == aEnd.ref || blocker.ID == bEnd.ref {
				continue
			}
			return false
		}
		return true
	}
	start := [2]float64{seg[0], seg[1]}
	end := [2]float64{seg[2], seg[3]}
	path, ok := dcFindOrthogonalPath(start, end, sheet, titleBlock, clear, 500)
	if !ok || len(path) < 2 {
		return nil, false
	}
	path = cleanDCPath(path)
	if aEnd.kind == "pin" && aEnd.outward != "" && !firstSegmentMatchesDir(path, aEnd.outward) {
		return nil, false
	}
	if bEnd.kind == "pin" && bEnd.outward != "" {
		lastA, lastB := path[len(path)-2], path[len(path)-1]
		incoming := directionBetween(lastA, lastB)
		if incoming == "" || oppositeDirection(incoming) != bEnd.outward {
			return nil, false
		}
	}
	return path, true
}

func directionBetween(a, b [2]float64) string {
	dx, dy := b[0]-a[0], b[1]-a[1]
	switch {
	case math.Abs(dx) < 0.5 && dy > 0:
		return "up"
	case math.Abs(dx) < 0.5 && dy < 0:
		return "down"
	case math.Abs(dy) < 0.5 && dx > 0:
		return "right"
	case math.Abs(dy) < 0.5 && dx < 0:
		return "left"
	}
	return ""
}

func filterWireRerouteProposals(in []wireRerouteProposal, wanted map[string]bool) []wireRerouteProposal {
	if len(wanted) == 0 {
		return in
	}
	var out []wireRerouteProposal
	for _, p := range in {
		if wanted[p.WireID] {
			out = append(out, p)
		}
	}
	return out
}

func executeWireRerouteFix(pinned *appConfig, win, doc string, scene wireRerouteScene,
	report *wireRerouteReport, save bool) error {
	beforeNets, _, err := readLiveNets(pinned, win)
	if err != nil {
		return fmt.Errorf("wire-reroute-fix: baseline netlist: %w", err)
	}
	beforeBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("wire-reroute-fix: baseline bridge set: %w", err)
	}
	var applied []wireRerouteProposal
	rollback := func(cause error) error {
		if len(applied) == 0 {
			return cause
		}
		rbErr := rollbackWireReroutes(pinned, win, doc, applied)
		report.RolledBack = rbErr == nil
		if rbErr != nil {
			report.RollbackError = rbErr.Error()
			return fmt.Errorf("%v; wire rollback FAILED (%v)", cause, rbErr)
		}
		return fmt.Errorf("%v; wire reroutes were rolled back", cause)
	}
	for _, p := range report.Proposals {
		if p.Status != "ready" {
			continue
		}
		app, err := executeWireRerouteProposal(pinned, win, doc, p)
		if err != nil {
			return rollback(err)
		}
		applied = append(applied, app)
		report.AppliedIDs = append(report.AppliedIDs, p.WireID)
		if err := verifyWireRerouteBaseline(pinned, win, doc, beforeNets, beforeBridges); err != nil {
			return rollback(err)
		}
	}
	if save {
		saved, serr := requestAutolayoutAction(pinned, "schematic.save", win, nil, doc, "wire-reroute-fix 保存")
		if serr != nil {
			return fmt.Errorf("wire-reroute-fix: save: %w", serr)
		}
		ok, _ := saved.Result["saved"].(bool)
		if !ok {
			return fmt.Errorf("wire-reroute-fix: schematic.save did not return saved:true")
		}
		report.Saved = true
	}
	report.Applied = true
	return nil
}

func executeWireRerouteProposal(pinned *appConfig, win, doc string,
	p wireRerouteProposal) (wireRerouteProposal, error) {
	if p.Status != "ready" || len(p.NewPath) < 2 {
		return p, fmt.Errorf("wire %s is not ready", p.WireID)
	}
	del, err := requestAutolayoutAction(pinned, "schematic.primitives.delete", win, map[string]any{
		"primitiveIds": []string{p.WireID},
	}, doc, "wire-reroute-fix 删除旧导线")
	if err != nil {
		return p, err
	}
	if nf := countDeleteNotFound(del.Result["notFound"]); nf > 0 {
		return p, fmt.Errorf("wire %s was not found", p.WireID)
	}
	points := make([]float64, 0, len(p.NewPath)*2)
	for _, q := range p.NewPath {
		points = append(points, q[0], q[1])
	}
	created, err := requestAutolayoutAction(pinned, "schematic.wire.create", win, map[string]any{
		"points": points, "net": p.Net,
	}, doc, "wire-reroute-fix 创建新导线")
	if err != nil {
		return p, err
	}
	newID, _ := created.Result["primitiveId"].(string)
	if newID == "" {
		return p, fmt.Errorf("wire create returned no primitiveId")
	}
	p.NewID = newID
	return p, nil
}

func rollbackWireReroutes(pinned *appConfig, win, doc string, applied []wireRerouteProposal) error {
	for i := len(applied) - 1; i >= 0; i-- {
		p := applied[i]
		if _, err := requestAutolayoutAction(pinned, "schematic.primitives.delete", win,
			map[string]any{"primitiveIds": []string{p.NewID}}, doc, "wire-reroute-fix 回滚新导线"); err != nil {
			return err
		}
		if _, err := requestAutolayoutAction(pinned, "schematic.wire.create", win, map[string]any{
			"points": []float64{p.Original[0], p.Original[1], p.Original[2], p.Original[3]},
			"net":    p.Net,
		}, doc, "wire-reroute-fix 恢复旧导线"); err != nil {
			return err
		}
	}
	return nil
}

func verifyWireRerouteBaseline(pinned *appConfig, win, doc string,
	beforeNets map[string]map[string]bool, beforeBridges []string) error {
	afterNets, _, err := readLiveNets(pinned, win)
	if err != nil || !dcNetsEqual(beforeNets, afterNets) {
		return fmt.Errorf("wire-reroute-fix: pin netlist differs from baseline (%v)", err)
	}
	afterBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("wire-reroute-fix: bridge verification failed: %w", err)
	}
	if added := dcNewBridgeSignatures(beforeBridges, afterBridges); len(added) > 0 {
		return fmt.Errorf("wire-reroute-fix: bridge set gained %s", strings.Join(added, ","))
	}
	return nil
}
