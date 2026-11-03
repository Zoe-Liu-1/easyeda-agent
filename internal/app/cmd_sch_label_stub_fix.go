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

type labelStubFixReport struct {
	SchemaVersion int                    `json:"schemaVersion"`
	DocumentID    string                 `json:"documentId"`
	Proposals     []labelStubFixProposal `json:"proposals"`
	AppliedIDs    []string               `json:"appliedIds,omitempty"`
	Blocked       int                    `json:"blocked"`
	Applied       bool                   `json:"applied"`
	Saved         bool                   `json:"saved,omitempty"`
	Notes         []string               `json:"notes,omitempty"`
}

type labelStubFixProposal struct {
	LabelID          string         `json:"labelId"`
	WireID           string         `json:"wireId"`
	Net              string         `json:"net"`
	Leaf             checkPoint     `json:"leaf"`
	Junction         checkPoint     `json:"junction"`
	Status           string         `json:"status"`
	Reason           string         `json:"reason,omitempty"`
	Stub             [][4]float64   `json:"stubSegments,omitempty"`
	Replacement      [][4]float64   `json:"replacementSegments,omitempty"`
	OriginalSegments [][4]float64   `json:"originalSegments,omitempty"`
	OriginalPoints   []float64      `json:"originalPoints,omitempty"`
	TextBBox         *layoutBBox    `json:"textBBox,omitempty"`
	Style            map[string]any `json:"style,omitempty"`
}

type labelStubFixScene struct {
	Comps  []layoutComp
	Wires  []schGroupWire
	Items  []visibleTextItem
	Styles map[string]map[string]any
	Geos   []labelStubGeometry
}

func newSchLabelStubFixCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var idRaw, out string
	var apply, save, allowPartial bool
	cmd := &cobra.Command{
		Use:   "label-stub-fix --doc <page> [--id <attributeIds>] [--apply] [--save]",
		Short: "Remove decorative T-tails used only to host wire net labels",
		Long: `Find visible wire Name attributes attached to a leaf spur that branches off
an existing wire trunk. Repair by rebuilding the wire without the spur and
attaching the Name directly to the remaining wire. A net label is already an
attribute; it must not add a separate wire tail.

The current safe execution scope is one wire primitive with one visible net
name. Multi-primitive trees, multiple labels, pins/markers on the alleged spur,
ambiguous parents, and unreadable geometry remain blocked.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			scene, err := readLabelStubFixScene(pinned, win, doc)
			if err != nil {
				return err
			}
			report := buildLabelStubFixReport(doc, scene, parsePrimitiveIDSet(idRaw))
			if !apply {
				return writeLabelStubFixReport(report, out, stdout, stderr, true)
			}
			if len(report.Proposals) == 0 {
				return fmt.Errorf("label-stub-fix: no label-stub candidates found")
			}
			if report.Blocked > 0 && !allowPartial {
				return writeLabelStubFixReport(report, out, stdout, stderr, false)
			}
			beforeNets, _, err := readLiveNets(pinned, win)
			if err != nil {
				return fmt.Errorf("label-stub-fix: baseline netlist: %w", err)
			}
			beforeBridges, err := dcBridgeSignatures(pinned, win, doc)
			if err != nil {
				return fmt.Errorf("label-stub-fix: baseline bridge: %w", err)
			}
			for i := range report.Proposals {
				p := &report.Proposals[i]
				if p.Status != "ready" {
					continue
				}
				freshScene, err := readLabelStubFixScene(pinned, win, doc)
				if err != nil {
					return fmt.Errorf("label-stub-fix: fresh scene before %s: %w", p.LabelID, err)
				}
				if err := applyLabelStubFix(pinned, win, doc, p, freshScene); err != nil {
					return fmt.Errorf("label-stub-fix: %s: %w", p.LabelID, err)
				}
				report.AppliedIDs = append(report.AppliedIDs, p.LabelID)
			}
			afterNets, _, err := readLiveNets(pinned, win)
			if err != nil || !dcNetsEqual(beforeNets, afterNets) {
				return fmt.Errorf("label-stub-fix: full pin netlist changed after repair (%v)", err)
			}
			afterBridges, err := dcBridgeSignatures(pinned, win, doc)
			if err != nil || !stringSlicesEqual(beforeBridges, afterBridges) {
				return fmt.Errorf("label-stub-fix: bridge set changed after repair (%v)", err)
			}
			report.Applied = len(report.AppliedIDs) > 0
			if save {
				res, err := requestAutolayoutAction(pinned, "schematic.save", win, nil, doc, "label-stub-fix save")
				if err != nil {
					return fmt.Errorf("label-stub-fix: save: %w", err)
				}
				if saved, _ := res.Result["saved"].(bool); !saved {
					return fmt.Errorf("label-stub-fix: schematic.save did not return saved:true")
				}
				report.Saved = true
			}
			return writeLabelStubFixReport(report, out, stdout, stderr, false)
		},
	}
	cmd.Flags().StringVar(&idRaw, "id", "", "optional comma-separated wire Name attribute IDs")
	cmd.Flags().BoolVar(&apply, "apply", false, "execute ready repairs")
	cmd.Flags().BoolVar(&save, "save", false, "save after full pin-net and bridge verification")
	cmd.Flags().BoolVar(&allowPartial, "allow-partial", false, "apply ready repairs while reporting blocked candidates")
	cmd.Flags().StringVar(&out, "out", "", "write structured report to JSON")
	return cmd
}

func readLabelStubFixScene(pinned *appConfig, win, doc string) (labelStubFixScene, error) {
	geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePins": true, "includePagePrimitives": true,
	})
	if err != nil {
		return labelStubFixScene{}, err
	}
	comps, err := parseLayoutComps(geom.Result)
	if err != nil {
		return labelStubFixScene{}, err
	}
	wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
	if err != nil {
		return labelStubFixScene{}, err
	}
	vt, err := requestAction(pinned, "schematic.visible_text.bboxes", win, nil)
	if err != nil {
		return labelStubFixScene{}, err
	}
	var vision alignmentVision
	raw, _ := json.Marshal(vt.Result)
	if err := json.Unmarshal(raw, &vision); err != nil {
		return labelStubFixScene{}, fmt.Errorf("parse visible text: %w", err)
	}
	return labelStubFixScene{
		Comps: comps, Wires: wires, Items: vision.Items,
		Styles: markerCleanWireStyles(geom.Result),
		Geos:   labelStubGeometries(vision.Items, comps, wires),
	}, nil
}

func buildLabelStubFixReport(doc string, scene labelStubFixScene, requested map[string]bool) labelStubFixReport {
	report := labelStubFixReport{SchemaVersion: 1, DocumentID: doc}
	for _, geo := range scene.Geos {
		if len(requested) > 0 && !requested[geo.Item.ID] {
			continue
		}
		p := labelStubFixProposal{
			LabelID: geo.Item.ID, WireID: geo.Wire.ID, Net: geo.Item.Value,
			Leaf:     checkPoint{X: round2(geo.Leaf.X), Y: round2(geo.Leaf.Y)},
			Junction: checkPoint{X: round2(geo.Junction.X), Y: round2(geo.Junction.Y)},
			Stub:     geo.StubSegments, Replacement: uniqueLabelStubEdges(geo.RemainingSegments),
			OriginalSegments: labelWireSegments(geo.Wire),
			OriginalPoints:   append([]float64(nil), geo.Wire.Points...),
			TextBBox:         geo.Item.BBox,
			Style:            scene.Styles[geo.Wire.ID], Status: "ready",
		}
		idx := -1
		for i := range scene.Wires {
			if scene.Wires[i].ID == geo.Wire.ID {
				idx = i
				break
			}
		}
		if idx < 0 {
			p.Status, p.Reason = "blocked", "parent-wire-not-found"
		} else {
			treeCount := len(labelStubConnectedWireIndexes(scene.Wires, idx))
			if treeCount != 1 {
				p.Status, p.Reason = "blocked", fmt.Sprintf("multi-primitive-wire-tree:%d", treeCount)
			}
		}
		labelCount := 0
		for _, item := range scene.Items {
			if item.ParentID == geo.Wire.ID && item.Kind == "attribute" && item.Key == "Name" &&
				strings.TrimSpace(item.Value) != "" && (item.Visible == nil || *item.Visible) {
				labelCount++
			}
		}
		if p.Status == "ready" && labelCount != 1 {
			p.Status, p.Reason = "blocked", fmt.Sprintf("wire-label-count:%d", labelCount)
		}
		if p.Status == "ready" && (len(p.OriginalPoints) < 4 || len(p.OriginalPoints)%2 != 0) {
			p.Status, p.Reason = "blocked", "invalid-original-wire-geometry"
		}
		if p.Status == "ready" && len(p.Replacement) == 0 {
			p.Status, p.Reason = "blocked", "empty-replacement-wire"
		}
		if p.Status != "ready" {
			report.Blocked++
		}
		report.Proposals = append(report.Proposals, p)
	}
	sort.Slice(report.Proposals, func(i, j int) bool {
		if report.Proposals[i].Net != report.Proposals[j].Net {
			return report.Proposals[i].Net < report.Proposals[j].Net
		}
		return report.Proposals[i].LabelID < report.Proposals[j].LabelID
	})
	return report
}

func applyLabelStubFix(pinned *appConfig, win, doc string, p *labelStubFixProposal, scene labelStubFixScene) error {
	deleted, err := requestAutolayoutAction(pinned, "schematic.primitives.delete", win,
		map[string]any{"primitiveIds": []string{p.WireID}}, doc, "label-stub-fix 删除带线头父线")
	if err != nil || len(asStringSlice(resultMap(deleted.Result)["deletedIds"])) == 0 {
		// Some hosts summarize deletes by type rather than in deletedIds; verify below.
		if err != nil {
			return fmt.Errorf("delete parent wire: %w", err)
		}
	}
	if present, err := schWireIDPresent(pinned, win, doc, p.WireID); err != nil || present {
		return fmt.Errorf("parent wire deletion not proven (present=%v, err=%v)", present, err)
	}

	var createdWireIDs []string
	for _, edge := range p.Replacement {
		payload := map[string]any{
			"points": []float64{edge[0], edge[1], edge[2], edge[3]},
		}
		addLabelStubStyle(payload, p.Style)
		res, err := requestAutolayoutAction(pinned, "schematic.wire.create", win, payload, doc,
			"schematic.wire.create")
		if err != nil {
			_ = rollbackLabelStubFix(pinned, win, doc, p, createdWireIDs)
			return fmt.Errorf("recreate wire without label tail: %w", err)
		}
		if id := asString(res.Result["primitiveId"]); id != "" {
			createdWireIDs = append(createdWireIDs, id)
		}
	}
	anchor, err := chooseLabelStubDirectAnchor(p, scene)
	if err != nil {
		_ = rollbackLabelStubFix(pinned, win, doc, p, createdWireIDs)
		return err
	}
	parent, err := findWireContainingPoint(pinned, win, doc, anchor.X, anchor.Y, p.Net)
	if err != nil {
		_ = rollbackLabelStubFix(pinned, win, doc, p, createdWireIDs)
		return err
	}
	attached, err := requestAutolayoutAction(pinned, "schematic.netflag.create", win, map[string]any{
		"kind": "net_label", "net": p.Net, "x": anchor.X, "y": anchor.Y,
		"expectedParentWireId": parent.ID,
	}, doc, "label-stub-fix 直接挂到主干")
	if err != nil || attached.Result["verified"] != true {
		_ = rollbackLabelStubFix(pinned, win, doc, p, createdWireIDs)
		return fmt.Errorf("attach label without tail: %v", err)
	}
	newLabelID := asString(attached.Result["primitiveId"])
	freshScene, err := readLabelStubFixScene(pinned, win, doc)
	if err != nil {
		_ = rollbackLabelStubFix(pinned, win, doc, p, createdWireIDs)
		return err
	}
	for _, f := range netLabelStubFindings(freshScene.Items, freshScene.Comps, freshScene.Wires) {
		if f.PrimitiveId == newLabelID {
			_ = rollbackLabelStubFix(pinned, win, doc, p, createdWireIDs)
			return fmt.Errorf("new label is still a decorative T-tail")
		}
	}
	for _, f := range visibleTextOverlapFindings(freshScene.Items, freshScene.Comps, freshScene.Wires, 1) {
		if f.PrimitiveId == newLabelID {
			_ = rollbackLabelStubFix(pinned, win, doc, p, createdWireIDs)
			return fmt.Errorf("new direct label collides with visible geometry")
		}
	}
	return nil
}

func rollbackLabelStubFix(pinned *appConfig, win, doc string, p *labelStubFixProposal, createdWireIDs []string) error {
	if len(createdWireIDs) > 0 {
		_, _ = requestAutolayoutAction(pinned, "schematic.primitives.delete", win,
			map[string]any{"primitiveIds": uniqueSorted(createdWireIDs)}, doc, "label-stub-fix rollback new wires")
	}
	for _, edge := range p.OriginalSegments {
		payload := map[string]any{"points": []float64{edge[0], edge[1], edge[2], edge[3]}}
		addLabelStubStyle(payload, p.Style)
		if _, err := requestAutolayoutAction(pinned, "schematic.wire.create", win, payload, doc,
			"label-stub-fix rollback original wire"); err != nil {
			return err
		}
	}
	parent, err := findWireContainingPoint(pinned, win, doc, p.Leaf.X, p.Leaf.Y, p.Net)
	if err != nil {
		return err
	}
	res, err := requestAutolayoutAction(pinned, "schematic.netflag.create", win, map[string]any{
		"kind": "net_label", "net": p.Net, "x": p.Leaf.X, "y": p.Leaf.Y,
		"expectedParentWireId": parent.ID,
	}, doc, "label-stub-fix rollback original label")
	if err != nil || res.Result["verified"] != true {
		return fmt.Errorf("restore original label: %v", err)
	}
	return nil
}

func addLabelStubStyle(payload map[string]any, style map[string]any) {
	for key, value := range style {
		switch key {
		case "Color":
			payload["color"] = value
		case "LineWidth":
			payload["lineWidth"] = value
		case "LineType":
			payload["lineType"] = value
		}
	}
}

func chooseLabelStubDirectAnchor(p *labelStubFixProposal, scene labelStubFixScene) (checkPoint, error) {
	if p.TextBBox == nil {
		return checkPoint{}, fmt.Errorf("label text bbox is unavailable")
	}
	dx := p.TextBBox.MinX - p.Leaf.X
	dy := p.TextBBox.MinY - p.Leaf.Y
	width := p.TextBBox.MaxX - p.TextBBox.MinX
	height := p.TextBBox.MaxY - p.TextBBox.MinY
	candidates := labelStubAnchorCandidates(p.Replacement, p.Junction)
	var otherItems []visibleTextItem
	for _, item := range scene.Items {
		if item.ID != p.LabelID {
			otherItems = append(otherItems, item)
		}
	}
	var predictedWires []schGroupWire
	for _, wire := range scene.Wires {
		if wire.ID != p.WireID {
			predictedWires = append(predictedWires, wire)
		}
	}
	for _, edge := range p.Replacement {
		predictedWires = append(predictedWires, schGroupWire{
			ID: "candidate-parent", Net: p.Net,
			ObservedSegments: [][4]float64{edge},
		})
	}
	for _, point := range candidates {
		if labelStubEndpointConnected(scene.Comps, point.X, point.Y) {
			continue
		}
		candidate := visibleTextItem{
			ID: "candidate-label", Kind: "attribute", ComponentType: "wire", ParentType: "wire",
			Key: "Name", Value: p.Net, ParentID: "candidate-parent",
			BBox: &layoutBBox{MinX: point.X + dx, MinY: point.Y + dy,
				MaxX: point.X + dx + width, MaxY: point.Y + dy + height},
		}
		items := append(append([]visibleTextItem(nil), otherItems...), candidate)
		blocked := false
		for _, finding := range visibleTextOverlapFindings(items, scene.Comps, predictedWires, 1) {
			if finding.PrimitiveId == candidate.ID {
				blocked = true
				break
			}
		}
		if !blocked {
			return point, nil
		}
	}
	return checkPoint{}, fmt.Errorf("no collision-free anchor on the remaining wire")
}

func labelStubAnchorCandidates(edges [][4]float64, junction checkPoint) []checkPoint {
	seen := map[string]bool{}
	var out []checkPoint
	for _, edge := range edges {
		length := math.Abs(edge[2]-edge[0]) + math.Abs(edge[3]-edge[1])
		if length < 1 {
			continue
		}
		steps := int(math.Floor(length/5)) + 1
		for i := 0; i <= steps; i++ {
			t := math.Min(1, float64(i*5)/length)
			x := round2(edge[0] + (edge[2]-edge[0])*t)
			y := round2(edge[1] + (edge[3]-edge[1])*t)
			key := fmt.Sprintf("%.3f,%.3f", x, y)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, checkPoint{X: x, Y: y})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		di := math.Abs(out[i].X-junction.X) + math.Abs(out[i].Y-junction.Y)
		dj := math.Abs(out[j].X-junction.X) + math.Abs(out[j].Y-junction.Y)
		if di != dj {
			return di < dj
		}
		if out[i].X != out[j].X {
			return out[i].X < out[j].X
		}
		return out[i].Y < out[j].Y
	})
	return out
}

func findWireContainingPoint(pinned *appConfig, win, doc string, x, y float64, net string) (*schGroupWire, error) {
	wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
	if err != nil {
		return nil, err
	}
	var matches []schGroupWire
	for _, wire := range wires {
		if wireTouchesPoint(wire, x, y) {
			matches = append(matches, wire)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no rebuilt parent wire contains (%.3f,%.3f)", x, y)
	}
	var sameNet []schGroupWire
	for _, wire := range matches {
		if wire.Net == net {
			sameNet = append(sameNet, wire)
		}
	}
	if len(sameNet) == 1 {
		return &sameNet[0], nil
	}
	if len(matches) == 1 {
		return &matches[0], nil
	}
	return nil, fmt.Errorf("parent wire is ambiguous at (%.3f,%.3f): %d matches, %d on net %q", x, y, len(matches), len(sameNet), net)
}

func schWireIDPresent(pinned *appConfig, win, doc, id string) (bool, error) {
	wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
	if err != nil {
		return false, err
	}
	for _, wire := range wires {
		if wire.ID == id {
			return true, nil
		}
	}
	return false, nil
}

func uniqueLabelStubEdges(edges [][4]float64) [][4]float64 {
	seen := map[[4]float64]bool{}
	var out [][4]float64
	for _, edge := range edges {
		key := labelStubCanonicalEdge(edge)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, edge)
	}
	return out
}

func writeLabelStubFixReport(report labelStubFixReport, out string, stdout, stderr io.Writer, dryRun bool) error {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if out != "" {
		if err := os.WriteFile(out, append(raw, '\n'), 0644); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(stdout, string(raw))
	}
	if dryRun {
		fmt.Fprintln(stderr, "label-stub-fix: dry-run only; pass --apply to remove decorative label tails")
	}
	return nil
}

func resultMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
