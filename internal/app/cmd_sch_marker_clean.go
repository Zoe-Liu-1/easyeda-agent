package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type markerCleanReport struct {
	SchemaVersion int                       `json:"schemaVersion"`
	DocumentID    string                    `json:"documentId"`
	Candidates    []markerCleanGroup        `json:"candidates"`
	WireTreePlans []markerCleanWireTreePlan `json:"wireTreePlans,omitempty"`
	DeleteIds     []string                  `json:"deleteIds,omitempty"`
	Applied       bool                      `json:"applied"`
	Saved         bool                      `json:"saved,omitempty"`
	Blockers      []string                  `json:"blockers,omitempty"`
	Notes         []string                  `json:"notes,omitempty"`
}

type markerCleanGroup struct {
	Type          string   `json:"type"`
	Net           string   `json:"net"`
	ComponentType string   `json:"componentType,omitempty"`
	PrimitiveIds  []string `json:"primitiveIds"`
	VisibleIds    []string `json:"visibleIds,omitempty"`
}

type markerCleanWireReplay struct {
	PrimitiveID string         `json:"primitiveId"`
	Points      []float64      `json:"points"`
	Net         string         `json:"net,omitempty"`
	Style       map[string]any `json:"style,omitempty"`
}

type markerCleanWireTreePlan struct {
	RootID    string                  `json:"rootId"`
	Net       string                  `json:"net"`
	WireIDs   []string                `json:"wireIds"`
	LabelIDs  []string                `json:"labelIds"`
	MarkerIDs []string                `json:"markerIds"`
	Replays   []markerCleanWireReplay `json:"replays"`
}

func newSchMarkerCleanCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var deleteRaw, out string
	var apply, save, rebuildWireTrees bool
	cmd := &cobra.Command{
		Use:   "marker-clean",
		Short: "Safely remove explicitly selected duplicate or redundant net names",
		Long: `Inspect duplicate-net-marker, redundant-net-marker, label-economy and
cross-kind-label-economy candidates. This command never guesses the survivor:
pass --delete <primitiveId,...> after reviewing the candidate groups.

Before deletion it proves that every affected wire island keeps at least one
visible network name. It then compares the complete pin-to-net map before and
after deletion. Without --apply it is read-only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			report, restore, err := freshMarkerCleanReport(pinned, win, doc, deleteRaw, apply, save, rebuildWireTrees)
			if err != nil {
				return err
			}
			if !apply {
				raw, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return err
				}
				if out != "" {
					if err := os.WriteFile(out, append(raw, '\n'), 0644); err != nil {
						return fmt.Errorf("write marker-clean report: %w", err)
					}
				} else {
					fmt.Fprintln(stdout, string(raw))
				}
				fmt.Fprintln(stderr, "marker-clean: dry-run only; pass --delete <ids> --apply to remove selected names")
				return nil
			}
			if err := executeMarkerClean(pinned, win, doc, &report, restore, rebuildWireTrees, save, stdout, stderr); err != nil {
				return err
			}
			raw, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			if out != "" {
				return os.WriteFile(out, append(raw, '\n'), 0644)
			}
			fmt.Fprintln(stdout, string(raw))
			return nil
		},
	}
	cmd.Flags().StringVar(&deleteRaw, "delete", "", "explicit primitive IDs to delete after reviewing candidates")
	cmd.Flags().BoolVar(&apply, "apply", false, "execute the explicit deletion")
	cmd.Flags().BoolVar(&save, "save", false, "save after netlist-preserving verification")
	cmd.Flags().BoolVar(&rebuildWireTrees, "rebuild-wire-trees", false,
		"rebuild proof-safe parent wire trees without wire Name attributes when the host has no attribute delete")
	cmd.Flags().StringVar(&out, "out", "", "write structured report to JSON")
	return cmd
}

type markerRestore struct {
	component *layoutComp
	label     *visibleTextItem
	anchor    checkPoint
}

func freshMarkerCleanReport(pinned *appConfig, win, doc, deleteRaw string, apply, save, rebuildWireTrees bool) (markerCleanReport, map[string]markerRestore, error) {
	geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePins": true, "includePinNets": true, "includePagePrimitives": true,
	})
	if err != nil {
		return markerCleanReport{}, nil, err
	}
	if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
		return markerCleanReport{}, nil, fmt.Errorf("marker-clean: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
	}
	comps, err := parseLayoutComps(geom.Result)
	if err != nil {
		return markerCleanReport{}, nil, err
	}
	wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
	if err != nil {
		return markerCleanReport{}, nil, err
	}
	vt, err := requestAction(pinned, "schematic.visible_text.bboxes", win, nil)
	if err != nil {
		return markerCleanReport{}, nil, err
	}
	var vision alignmentVision
	raw, _ := json.Marshal(vt.Result)
	if err := json.Unmarshal(raw, &vision); err != nil {
		return markerCleanReport{}, nil, fmt.Errorf("marker-clean: parse visible text: %w", err)
	}
	anchors := parseVisibleAttributeAnchors(geom.Result)
	var findings []checkFinding
	findings = append(findings, duplicateNetMarkerFindings(comps)...)
	findings = append(findings, redundantNetMarkerFindings(comps, wires)...)
	findings = append(findings, netLabelEconomyFindings(vision.Items, wires)...)
	findings = append(findings, crossKindLabelEconomyFindings(vision.Items, comps, wires)...)
	visible := map[string]bool{}
	for _, it := range vision.Items {
		if it.ID != "" && it.Key == "Name" && strings.TrimSpace(it.Value) != "" &&
			(it.Visible == nil || *it.Visible) {
			visible[it.ID] = true
			if it.ParentID != "" {
				visible[it.ParentID] = true
			}
		}
	}
	report := markerCleanReport{SchemaVersion: 1, DocumentID: doc}
	for _, f := range findings {
		g := markerCleanGroup{
			Type: f.Type, Net: f.MarkerNet, ComponentType: f.ComponentType,
			PrimitiveIds: append([]string(nil), f.PrimitiveIds...),
		}
		for _, id := range g.PrimitiveIds {
			if visible[id] {
				g.VisibleIds = append(g.VisibleIds, id)
			}
		}
		sort.Strings(g.PrimitiveIds)
		sort.Strings(g.VisibleIds)
		report.Candidates = append(report.Candidates, g)
	}
	sort.Slice(report.Candidates, func(i, j int) bool {
		a, b := report.Candidates[i], report.Candidates[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Net != b.Net {
			return a.Net < b.Net
		}
		return strings.Join(a.PrimitiveIds, ",") < strings.Join(b.PrimitiveIds, ",")
	})
	requested := parsePrimitiveIDSet(deleteRaw)
	if len(requested) > 0 {
		report.DeleteIds = mapKeys(requested)
		report.Blockers = markerCleanBlockers(report.Candidates, requested, visible)
	}
	restore := markerRestoreMap(comps, vision.Items, anchors, requested)
	if rebuildWireTrees && len(requested) > 0 {
		plans, planBlockers := markerCleanWireTreePlans(geom.Result, comps, wires, vision.Items, requested, restore)
		report.WireTreePlans = plans
		report.Blockers = append(report.Blockers, planBlockers...)
	}
	report.Blockers = uniqueSorted(report.Blockers)
	if apply && len(report.DeleteIds) == 0 {
		return report, restore, fmt.Errorf("marker-clean: --apply requires --delete with explicit candidate IDs")
	}
	if apply && len(report.Blockers) > 0 {
		return report, restore, fmt.Errorf("marker-clean refused: %s", strings.Join(report.Blockers, "; "))
	}
	return report, restore, nil
}

func markerCleanWireTreePlans(geomResult map[string]any, comps []layoutComp, wires []schGroupWire,
	items []visibleTextItem, requested map[string]bool, restore map[string]markerRestore) ([]markerCleanWireTreePlan, []string) {
	find, wireIndex := wireTreeFinder(wires)
	type pendingPlan struct {
		root     int
		net      string
		labelIDs []string
	}
	plansByRoot := map[int]*pendingPlan{}
	var blockers []string
	for id := range requested {
		item, ok := restore[id]
		if !ok || item.component != nil {
			continue
		}
		if item.label == nil || item.label.ParentID == "" {
			blockers = append(blockers, "wire-tree-rebuild:name-parent-unresolved:"+id)
			continue
		}
		idx, ok := wireIndex[item.label.ParentID]
		if !ok {
			blockers = append(blockers, "wire-tree-rebuild:parent-wire-not-found:"+id)
			continue
		}
		root := find(idx)
		plan := plansByRoot[root]
		if plan == nil {
			plan = &pendingPlan{root: root, net: item.label.Value}
			plansByRoot[root] = plan
		}
		if plan.net == "" || plan.net != item.label.Value {
			blockers = append(blockers, "wire-tree-rebuild:mixed-or-unknown-net:"+item.label.ParentID)
			continue
		}
		plan.labelIDs = append(plan.labelIDs, id)
	}
	styles := markerCleanWireStyles(geomResult)
	visibleMarkerNames := map[string]bool{}
	for _, item := range items {
		if item.Key == "Name" && item.ParentID != "" && strings.TrimSpace(item.Value) != "" &&
			(item.Visible == nil || *item.Visible) {
			visibleMarkerNames[item.ParentID] = true
		}
	}
	var plans []markerCleanWireTreePlan
	for _, pending := range plansByRoot {
		var tree []schGroupWire
		for i, wire := range wires {
			if find(i) == pending.root {
				tree = append(tree, wire)
			}
		}
		sort.Slice(tree, func(i, j int) bool { return tree[i].ID < tree[j].ID })
		if len(tree) == 0 {
			blockers = append(blockers, "wire-tree-rebuild:empty-tree")
			continue
		}
		wireSet := map[string]bool{}
		for _, wire := range tree {
			wireSet[wire.ID] = true
		}
		var markerIDs []string
		for _, comp := range comps {
			if !isSchMarker(comp.ComponentType) || comp.Net != pending.net || requested[comp.ID] ||
				!visibleMarkerNames[comp.ID] {
				continue
			}
			for _, wire := range tree {
				if wireTouchesPoint(wire, comp.X, comp.Y) {
					markerIDs = append(markerIDs, comp.ID)
					break
				}
			}
		}
		sort.Strings(markerIDs)
		if len(markerIDs) == 0 {
			blockers = append(blockers, "wire-tree-rebuild:no-surviving-visible-marker:"+pending.net)
			continue
		}
		mixed := ""
		for _, item := range items {
			if !wireSet[item.ParentID] || strings.TrimSpace(item.Value) == "" {
				continue
			}
			if item.Visible != nil && !*item.Visible {
				continue
			}
			if item.Value != pending.net {
				mixed = item.Value
				break
			}
		}
		if mixed != "" {
			blockers = append(blockers, "wire-tree-rebuild:mixed-label-net:"+pending.net+":"+mixed)
			continue
		}
		replays := make([]markerCleanWireReplay, 0, len(tree))
		invalid := false
		for _, wire := range tree {
			points := append([]float64(nil), wire.Points...)
			if len(points) < 4 && len(wire.ObservedSegments) > 0 {
				for _, seg := range wire.ObservedSegments {
					if len(points) == 0 {
						points = append(points, seg[0], seg[1])
					}
					points = append(points, seg[2], seg[3])
				}
			}
			if len(points) < 4 || len(points)%2 != 0 {
				blockers = append(blockers, "wire-tree-rebuild:invalid-wire-geometry:"+wire.ID)
				invalid = true
				break
			}
			replays = append(replays, markerCleanWireReplay{
				PrimitiveID: wire.ID, Points: points, Net: wire.Net, Style: styles[wire.ID],
			})
		}
		if invalid {
			continue
		}
		labelIDs := append([]string(nil), pending.labelIDs...)
		sort.Strings(labelIDs)
		plans = append(plans, markerCleanWireTreePlan{
			RootID: tree[0].ID, Net: pending.net, WireIDs: append([]string(nil), func() []string {
				ids := make([]string, len(tree))
				for i, wire := range tree {
					ids[i] = wire.ID
				}
				return ids
			}()...), LabelIDs: labelIDs, MarkerIDs: markerIDs, Replays: replays,
		})
	}
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].Net != plans[j].Net {
			return plans[i].Net < plans[j].Net
		}
		return plans[i].RootID < plans[j].RootID
	})
	return plans, uniqueSorted(blockers)
}

func markerCleanWireStyles(geomResult map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	page, _ := geomResult["pagePrimitives"].(map[string]any)
	rows, _ := page["wires"].([]any)
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		id := asString(row["primitiveId"])
		if id == "" {
			continue
		}
		style := map[string]any{}
		for _, key := range []string{"Color", "LineWidth", "LineType"} {
			if value := row[key]; value != nil {
				style[key] = value
			}
		}
		if len(style) > 0 {
			out[id] = style
		}
	}
	return out
}

func markerCleanBlockers(candidates []markerCleanGroup, requested, visible map[string]bool) []string {
	componentOf, groupsByComponent := markerCleanCandidateComponents(candidates)
	var blockers []string
	checkedComponents := map[int]bool{}
	for id := range requested {
		if !visible[id] {
			blockers = append(blockers, "not-visible-name:"+id)
		}
		found := false
		for groupIndex, group := range candidates {
			if !stringInSlice(id, group.PrimitiveIds) {
				continue
			}
			found = true
			if !visible[id] {
				continue
			}
			component := componentOf[groupIndex]
			if checkedComponents[component] {
				continue
			}
			checkedComponents[component] = true
			remaining := false
			for _, candidateIndex := range groupsByComponent[component] {
				for _, candidate := range candidates[candidateIndex].VisibleIds {
					if visible[candidate] && !requested[candidate] {
						remaining = true
						break
					}
				}
				if remaining {
					break
				}
			}
			if !remaining {
				blockers = append(blockers, "would-remove-last-visible-name:"+group.Net)
			}
		}
		if !found {
			blockers = append(blockers, "not-a-marker-clean-candidate:"+id)
		}
	}
	return uniqueSorted(blockers)
}

// markerCleanCandidateComponents groups candidate rows that describe the same
// physical wire island. Different rules can cover overlapping subsets of one
// island: cross-kind-label-economy may include a netflag plus two wire labels,
// while label-economy contains only those two wire labels. Treating the second
// row in isolation would falsely claim that deleting both labels removes the
// island's last visible name, even though the retained netflag is the intended
// survivor. Rows are linked when they share any primitive ID; unrelated islands
// sharing only a net name remain separate.
func markerCleanCandidateComponents(candidates []markerCleanGroup) ([]int, map[int][]int) {
	parent := make([]int, len(candidates))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	owners := map[string][]int{}
	for i, group := range candidates {
		for _, id := range group.PrimitiveIds {
			owners[id] = append(owners[id], i)
		}
	}
	for _, indexes := range owners {
		if len(indexes) < 2 {
			continue
		}
		for _, index := range indexes[1:] {
			union(indexes[0], index)
		}
	}
	componentOf := make([]int, len(candidates))
	groupsByComponent := map[int][]int{}
	for i := range candidates {
		root := find(i)
		componentOf[i] = root
		groupsByComponent[root] = append(groupsByComponent[root], i)
	}
	return componentOf, groupsByComponent
}

func markerRestoreMap(comps []layoutComp, items []visibleTextItem, anchors map[string]checkPoint,
	requested map[string]bool) map[string]markerRestore {
	out := map[string]markerRestore{}
	for i := range comps {
		if requested[comps[i].ID] {
			c := comps[i]
			out[c.ID] = markerRestore{component: &c}
		}
	}
	for i := range items {
		if requested[items[i].ID] {
			it := items[i]
			a := anchors[it.ID]
			out[it.ID] = markerRestore{label: &it, anchor: a}
		}
	}
	return out
}

func executeMarkerClean(pinned *appConfig, win, doc string, report *markerCleanReport,
	restore map[string]markerRestore, rebuildWireTrees, save bool, stdout, stderr io.Writer) error {
	before, _, err := readLiveNets(pinned, win)
	if err != nil {
		return fmt.Errorf("marker-clean: baseline netlist: %w", err)
	}
	var componentIDs []string
	var labelItems []map[string]any
	var unclassified []string
	for _, id := range report.DeleteIds {
		item, ok := restore[id]
		switch {
		case !ok:
			unclassified = append(unclassified, id)
		case item.component != nil:
			componentIDs = append(componentIDs, id)
		case item.label != nil && item.label.ParentID != "":
			labelItems = append(labelItems, map[string]any{
				"primitiveId":  id,
				"parentWireId": item.label.ParentID,
				"net":          item.label.Value,
			})
		default:
			unclassified = append(unclassified, id)
		}
	}
	if len(unclassified) > 0 {
		sort.Strings(unclassified)
		return fmt.Errorf("marker-clean: requested IDs have no exact deletable component/name classification: %s", strings.Join(unclassified, ","))
	}
	var deleteErrors []string
	notFound := 0
	if rebuildWireTrees && len(labelItems) > 0 {
		plannedLabels := map[string]bool{}
		var items []map[string]any
		for _, plan := range report.WireTreePlans {
			for _, id := range plan.LabelIDs {
				plannedLabels[id] = true
			}
			for _, replay := range plan.Replays {
				item := map[string]any{
					"primitiveId": replay.PrimitiveID,
					"points":      replay.Points,
				}
				for key, value := range replay.Style {
					switch key {
					case "Color":
						item["color"] = value
					case "LineWidth":
						item["lineWidth"] = value
					case "LineType":
						item["lineType"] = value
					}
				}
				items = append(items, item)
			}
		}
		var uncovered []string
		for _, item := range labelItems {
			if id := asString(item["primitiveId"]); !plannedLabels[id] {
				uncovered = append(uncovered, id)
			}
		}
		if len(uncovered) > 0 {
			sort.Strings(uncovered)
			return fmt.Errorf("marker-clean: rebuild plan does not cover requested names: %s", strings.Join(uncovered, ","))
		}
		res, err := requestAutolayoutActionTimed(pinned, "schematic.wire_tree.rebuild_without_labels", win,
			map[string]any{"items": items}, 90*time.Second, doc, "marker-clean 无标签重建父线树")
		if err != nil {
			return fmt.Errorf("marker-clean: wire-tree rebuild failed; inspect fresh geometry and netlist before retrying: %w", err)
		}
		if res == nil || res.Result["verified"] != true {
			return fmt.Errorf("marker-clean: wire-tree rebuild was not verified")
		}
	} else if len(labelItems) > 0 {
		res, err := requestAutolayoutAction(pinned, "schematic.net_label.delete", win, map[string]any{
			"items": labelItems,
		}, doc, "marker-clean 删除导线网络名称")
		if err != nil {
			deleteErrors = append(deleteErrors, "net-label: "+err.Error())
		} else if res == nil || res.Result["verified"] != true {
			deleteErrors = append(deleteErrors, "net-label: write not verified")
		}
	}
	if len(componentIDs) > 0 {
		res, err := requestAutolayoutAction(pinned, "schematic.primitives.delete", win, map[string]any{
			"primitiveIds": componentIDs,
		}, doc, "marker-clean 删除符号名称")
		if err != nil {
			deleteErrors = append(deleteErrors, "component: "+err.Error())
		}
		if res != nil {
			notFound += countDeleteNotFound(res.Result["notFound"])
		}
	}
	present, presentErr := markerCleanPresentIDs(pinned, win, doc)
	if presentErr != nil {
		return fmt.Errorf("marker-clean: delete state is unverified and a fresh readback failed (%v); do not retry until the page is readable", presentErr)
	}
	var missing []string
	for _, id := range report.DeleteIds {
		if !present[id] {
			missing = append(missing, id)
		}
	}
	if len(deleteErrors) > 0 || notFound > 0 {
		if len(missing) == 0 {
			return fmt.Errorf("marker-clean: delete reported an error/no-op (errors=%s, notFound=%d), and fresh readback shows every requested name still present; no mutation was accepted",
				strings.Join(deleteErrors, "; "), notFound)
		}
		report.Notes = append(report.Notes,
			fmt.Sprintf("delete reply was not clean (errors=%s, notFound=%d), but fresh readback proves %d/%d explicit names were removed",
				strings.Join(deleteErrors, "; "), notFound, len(missing), len(report.DeleteIds)))
	}
	report.Applied = true
	after, _, nerr := readLiveNets(pinned, win)
	if nerr != nil || !dcNetsEqual(before, after) {
		restoreMissing := missingMarkerRestores(restore, missing)
		restoreErr := restoreMarkerNames(pinned, win, doc, restoreMissing)
		report.Applied = false
		if nerr != nil {
			return fmt.Errorf("marker-clean: post-delete netlist read failed (%v); restore attempted (%v)", nerr, restoreErr)
		}
		if restoreErr != nil {
			return fmt.Errorf("marker-clean: deletion changed pin connectivity and restoration is not verified (%v); stop and inspect fresh page state before any retry", restoreErr)
		}
		restored, _, rerr := readLiveNets(pinned, win)
		if rerr != nil || !dcNetsEqual(before, restored) {
			return fmt.Errorf("marker-clean: deletion changed pin connectivity; restored names were recreated but the netlist still differs (%v)", rerr)
		}
		return fmt.Errorf("marker-clean: deletion changed pin connectivity; all removed names were recreated and fresh netlist readback matches the baseline")
	}
	if save {
		saved, serr := requestAutolayoutAction(pinned, "schematic.save", win, nil, doc, "marker-clean 保存")
		if serr != nil {
			return fmt.Errorf("marker-clean: save: %w", serr)
		}
		ok, _ := saved.Result["saved"].(bool)
		if !ok {
			return fmt.Errorf("marker-clean: schematic.save did not return saved:true")
		}
		report.Saved = true
		savedNets, _, rerr := readLiveNets(pinned, win)
		if rerr != nil || !dcNetsEqual(before, savedNets) {
			return fmt.Errorf("marker-clean: post-save netlist verification failed (%v)", rerr)
		}
	}
	fmt.Fprintf(stdout, "✓ marker-clean removed %d explicit duplicate name(s)\n", len(report.DeleteIds))
	return nil
}

func missingMarkerRestores(restore map[string]markerRestore, missing []string) map[string]markerRestore {
	out := map[string]markerRestore{}
	for _, id := range missing {
		if item, ok := restore[id]; ok {
			out[id] = item
		}
	}
	return out
}

func markerCleanPresentIDs(pinned *appConfig, win, doc string) (map[string]bool, error) {
	present := map[string]bool{}
	geom, err := requestAutolayoutAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePagePrimitives": true,
	}, doc, "marker-clean 删除后对象回读")
	if err != nil {
		return nil, err
	}
	comps, err := parseLayoutComps(geom.Result)
	if err != nil {
		return nil, err
	}
	for _, c := range comps {
		if c.ID != "" {
			present[c.ID] = true
		}
	}
	wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
	if err != nil {
		return nil, err
	}
	for _, w := range wires {
		if w.ID != "" {
			present[w.ID] = true
		}
	}
	vt, err := requestAutolayoutAction(pinned, "schematic.visible_text.bboxes", win, nil, doc, "marker-clean 名称回读")
	if err != nil {
		return nil, err
	}
	var vision alignmentVision
	raw, _ := json.Marshal(vt.Result)
	if err := json.Unmarshal(raw, &vision); err != nil {
		return nil, err
	}
	for _, item := range vision.Items {
		if item.ID != "" {
			present[item.ID] = true
		}
	}
	return present, nil
}

func restoreMarkerNames(pinned *appConfig, win, doc string, restore map[string]markerRestore) error {
	var failures []string
	keys := make([]string, 0, len(restore))
	for id := range restore {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	var created []string
	for _, id := range keys {
		item := restore[id]
		payload := map[string]any{}
		if item.component != nil {
			c := item.component
			kind := "net_label"
			switch c.ComponentType {
			case "netflag":
				if isPowerClusterGround(c.Net) {
					kind = "ground"
				} else {
					kind = "power"
				}
			case "netport":
				kind = "net_port_bi"
			case "netlabel":
				kind = "net_label"
			default:
				continue
			}
			payload = map[string]any{"kind": kind, "net": c.Net, "x": c.X, "y": c.Y}
			if c.Rotation != nil {
				payload["rotation"] = *c.Rotation
			}
		} else if item.label != nil {
			payload = map[string]any{
				"kind": "net_label", "net": item.label.Value,
				"x": item.anchor.X, "y": item.anchor.Y,
			}
		}
		if len(payload) == 0 {
			continue
		}
		res, err := requestAutolayoutAction(pinned, "schematic.netflag.create", win, payload, doc, "marker-clean 恢复名称")
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		if newID, _ := res.Result["primitiveId"].(string); newID != "" {
			created = append(created, newID)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	if len(created) != len(restore) {
		return fmt.Errorf("created %d/%d names with a verifiable primitive ID", len(created), len(restore))
	}
	present, err := markerCleanPresentIDs(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("restored names could not be read back: %w", err)
	}
	for _, id := range created {
		if !present[id] {
			failures = append(failures, "restored primitive missing:"+id)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func countDeleteNotFound(v any) int {
	switch n := v.(type) {
	case []any:
		return len(n)
	case []string:
		return len(n)
	case float64:
		if n > 0 {
			return int(n)
		}
	}
	return 0
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func stringInSlice(value string, values []string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func uniqueSorted(values []string) []string {
	m := map[string]bool{}
	for _, v := range values {
		m[v] = true
	}
	return mapKeys(m)
}
