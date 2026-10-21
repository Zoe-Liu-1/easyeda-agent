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

type markerCleanReport struct {
	SchemaVersion int                `json:"schemaVersion"`
	DocumentID    string             `json:"documentId"`
	Candidates    []markerCleanGroup `json:"candidates"`
	DeleteIds     []string           `json:"deleteIds,omitempty"`
	Applied       bool               `json:"applied"`
	Saved         bool               `json:"saved,omitempty"`
	Blockers      []string           `json:"blockers,omitempty"`
	Notes         []string           `json:"notes,omitempty"`
}

type markerCleanGroup struct {
	Type          string   `json:"type"`
	Net           string   `json:"net"`
	ComponentType string   `json:"componentType,omitempty"`
	PrimitiveIds  []string `json:"primitiveIds"`
	VisibleIds    []string `json:"visibleIds,omitempty"`
}

func newSchMarkerCleanCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var deleteRaw, out string
	var apply, save bool
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
			report, restore, err := freshMarkerCleanReport(pinned, win, doc, deleteRaw, apply, save)
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
			if err := executeMarkerClean(pinned, win, doc, &report, restore, save, stdout, stderr); err != nil {
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
	cmd.Flags().StringVar(&out, "out", "", "write structured report to JSON")
	return cmd
}

type markerRestore struct {
	component *layoutComp
	label     *visibleTextItem
	anchor    checkPoint
}

func freshMarkerCleanReport(pinned *appConfig, win, doc, deleteRaw string, apply, save bool) (markerCleanReport, map[string]markerRestore, error) {
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
	report.Blockers = uniqueSorted(report.Blockers)
	restore := markerRestoreMap(comps, vision.Items, anchors, requested)
	if apply && len(report.DeleteIds) == 0 {
		return report, restore, fmt.Errorf("marker-clean: --apply requires --delete with explicit candidate IDs")
	}
	if apply && len(report.Blockers) > 0 {
		return report, restore, fmt.Errorf("marker-clean refused: %s", strings.Join(report.Blockers, "; "))
	}
	return report, restore, nil
}

func markerCleanBlockers(candidates []markerCleanGroup, requested, visible map[string]bool) []string {
	var blockers []string
	for id := range requested {
		if !visible[id] {
			blockers = append(blockers, "not-visible-name:"+id)
		}
		found := false
		for _, group := range candidates {
			if !stringInSlice(id, group.PrimitiveIds) {
				continue
			}
			found = true
			if !visible[id] {
				continue
			}
			remaining := false
			for _, candidate := range group.VisibleIds {
				if !requested[candidate] {
					remaining = true
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
	restore map[string]markerRestore, save bool, stdout, stderr io.Writer) error {
	before, _, err := readLiveNets(pinned, win)
	if err != nil {
		return fmt.Errorf("marker-clean: baseline netlist: %w", err)
	}
	res, deleteErr := requestAutolayoutAction(pinned, "schematic.primitives.delete", win, map[string]any{
		"primitiveIds": report.DeleteIds,
	}, doc, "marker-clean 删除显式网络名称")
	notFound := 0
	if res != nil {
		notFound = countDeleteNotFound(res.Result["notFound"])
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
	if deleteErr != nil || notFound > 0 {
		if len(missing) == 0 {
			return fmt.Errorf("marker-clean: delete reported an error/no-op (error=%v, notFound=%d), and fresh readback shows every requested name still present; no mutation was accepted",
				deleteErr, notFound)
		}
		report.Notes = append(report.Notes,
			fmt.Sprintf("delete reply was not clean (error=%v, notFound=%d), but fresh readback proves %d/%d explicit names were removed",
				deleteErr, notFound, len(missing), len(report.DeleteIds)))
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
