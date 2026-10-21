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
	MarkerID     string         `json:"markerId"`
	Net          string         `json:"net"`
	FromType     string         `json:"fromType"`
	Host         *checkPoint    `json:"host"`
	Direction    string         `json:"direction"`
	StubWireID   string         `json:"stubWireId"`
	NewStubPath  [][2]float64   `json:"newStubPath"`
	Status       string         `json:"status"`
	Reason       string         `json:"reason,omitempty"`
	Original     *layoutComp    `json:"-"`
	OriginalStub *destaggerStub `json:"-"`
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
	Proposal    signalMarkerFixProposal
	NewMarkerID string
	NewWireID   string
}

func newSchSignalMarkerFixCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var ids, out string
	var apply, save, allowPartial bool
	cmd := &cobra.Command{
		Use:   "signal-marker-fix",
		Short: "Replace signal netflags/netports with text net labels",
		Long: `Convert signal-net netflag/netport warnings to native text net labels.
Only a marker on one safe single-segment stub with a concrete host pin is
eligible. Shared/branched trees and unknown pin directions stay blocked.
Module-boundary ports must be registered with signal-port-exempt instead.

Apply mode replaces one marker branch at a time, compares the complete
pin-to-net map and bridge set, and restores the original marker on regression.
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
		}
		stub, reason := stubOfMarker(c, wires)
		if stub == nil || reason != "" {
			p.Reason = "unsafe-marker-stub:" + reason
			out = append(out, p)
			continue
		}
		hostPart, hostPin, ok := powerOrientationHostPin(comps, stub.HostX, stub.HostY)
		_ = hostPart
		if !ok {
			p.Reason = "host-pin-unresolved"
			out = append(out, p)
			continue
		}
		outward, ok := pinCardinalOutward(hostPin)
		if !ok {
			p.Reason = "pin-outward-direction-unknown"
			out = append(out, p)
			continue
		}
		offset := math.Max(20, math.Ceil((acNetLabelTextWidth(c.Net)+4)/5)*5)
		path := [][2]float64{{stub.HostX, stub.HostY}, endpointPoint(stub.HostX, stub.HostY, offset, outward)}
		newBox := predictedMarkerBBox(path[1][0], path[1][1], "net_label", outward, c.Net)
		if !powerOrientationRouteClear(c, newBox, stub, path, items, comps, wires, sheet, titleBlock) {
			p.Reason = "no-clear-net-label-stub"
			out = append(out, p)
			continue
		}
		h := checkPoint{X: stub.HostX, Y: stub.HostY}
		p.Host = &h
		p.StubWireID = stub.WireID
		p.NewStubPath = path
		p.Direction = outward
		p.Status = "ready"
		p.Original = &c
		p.OriginalStub = stub
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MarkerID < out[j].MarkerID })
	return out
}

func endpointPoint(x, y, offset float64, dir string) [2]float64 {
	switch dir {
	case "up":
		return [2]float64{x, y + offset}
	case "down":
		return [2]float64{x, y - offset}
	case "left":
		return [2]float64{x - offset, y}
	default:
		return [2]float64{x + offset, y}
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
		if err != nil {
			return rollback(err)
		}
		applied = append(applied, app)
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
	if p.Direction == "" || len(p.NewStubPath) < 2 {
		return appliedSignalMarker{Proposal: p}, fmt.Errorf("host direction/path unavailable")
	}
	outward := p.Direction
	disc, err := requestAutolayoutAction(pinned, "schematic.pin.disconnect", win, map[string]any{
		"wirePrimitiveId": p.StubWireID, "pinX": p.Host.X, "pinY": p.Host.Y,
	}, doc, "signal-marker-fix 拆除旧标记")
	if err != nil {
		return appliedSignalMarker{Proposal: p}, err
	}
	if disc.Result["disconnected"] != true || disc.Result["partial"] == true {
		return appliedSignalMarker{Proposal: p}, fmt.Errorf("old signal marker disconnect not proven")
	}
	end := p.NewStubPath[len(p.NewStubPath)-1]
	offset := math.Abs(end[0]-p.Host.X) + math.Abs(end[1]-p.Host.Y)
	created, err := requestAutolayoutAction(pinned, "schematic.power.connect_pin", win, map[string]any{
		"pinX": p.Host.X, "pinY": p.Host.Y, "kind": "net_label", "net": p.Net,
		"direction": outward, "offset": offset,
	}, doc, "signal-marker-fix 创建网络标签")
	if err != nil {
		return appliedSignalMarker{Proposal: p}, err
	}
	newMarker, _ := created.Result["flagPrimitiveId"].(string)
	newWire, _ := created.Result["wirePrimitiveId"].(string)
	if newMarker == "" || newWire == "" {
		return appliedSignalMarker{Proposal: p}, fmt.Errorf("net-label replacement identities incomplete")
	}
	return appliedSignalMarker{Proposal: p, NewMarkerID: newMarker, NewWireID: newWire}, nil
}

func rollbackSignalMarkers(pinned *appConfig, win, doc string, applied []appliedSignalMarker) error {
	for i := len(applied) - 1; i >= 0; i-- {
		app := applied[i]
		disc, err := requestAutolayoutAction(pinned, "schematic.pin.disconnect", win, map[string]any{
			"wirePrimitiveId": app.NewWireID, "pinX": app.Proposal.Host.X, "pinY": app.Proposal.Host.Y,
		}, doc, "signal-marker-fix 回滚网络标签")
		if err != nil {
			return err
		}
		if disc.Result["disconnected"] != true || disc.Result["partial"] == true {
			return fmt.Errorf("new net label disconnect not proven")
		}
		kind := tidyRestoreKind(app.Proposal.FromType, app.Proposal.Net)
		if kind == "" {
			return fmt.Errorf("cannot restore marker type %s", app.Proposal.FromType)
		}
		stub := app.Proposal.OriginalStub
		rotation := 0.0
		if app.Proposal.Original.Rotation != nil {
			rotation = *app.Proposal.Original.Rotation
		}
		if _, err := requestAutolayoutAction(pinned, "schematic.power.connect_pin", win, map[string]any{
			"pinX": stub.HostX, "pinY": stub.HostY, "kind": kind, "net": app.Proposal.Net,
			"direction": stub.Dir, "offset": stub.Offset, "rotation": rotation,
		}, doc, "signal-marker-fix 恢复旧标记"); err != nil {
			return err
		}
	}
	return nil
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
