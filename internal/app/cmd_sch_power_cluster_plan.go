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

type powerClusterPlanReport struct {
	SchemaVersion int                `json:"schemaVersion"`
	DocumentID    string             `json:"documentId"`
	Plans         []powerClusterPlan `json:"plans"`
	Ready         int                `json:"ready"`
	Partial       int                `json:"partial"`
	Blocked       int                `json:"blocked"`
	Exempt        int                `json:"exempt"`
}

type powerClusterPlan struct {
	Net         string                   `json:"net"`
	Consumer    string                   `json:"consumer"`
	Status      string                   `json:"status"` // ready | blocked | exempt
	KeepFlag    string                   `json:"keepFlag,omitempty"`
	DeleteFlags []string                 `json:"deleteFlags,omitempty"`
	Connections []powerClusterConnection `json:"connections,omitempty"`
	Order       []string                 `json:"order,omitempty"`
	Note        string                   `json:"note,omitempty"`
}

type powerClusterConnection struct {
	Passive     string       `json:"passive"`
	From        checkPoint   `json:"from"`
	To          checkPoint   `json:"to"`
	Status      string       `json:"status"` // clear | already-connected | blocked
	Path        [][2]float64 `json:"path,omitempty"`
	Blockers    []string     `json:"blockers,omitempty"`
	DeleteFlags []string     `json:"deleteFlags,omitempty"`
}

func newSchPowerClusterPlanCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var jsonOut bool
	var out, exemptInline, playbookOut string
	cmd := &cobra.Command{
		Use:   "power-cluster-plan",
		Short: "Plan local power-cluster wiring and flag deletion (read-only)",
		Long: `Read the active page's power pins, local passives, wire roots and visible text.
For each sch-check power-cluster-candidate, plan a short orthogonal connection from the
passive pin to the nearest consumer power pin, keep the consumer-side flag, and list the
passive-side flags to delete only after the connection is verified.
The command is read-only: it never calls modify, wire, connect, prim-delete or save.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
				"includeBBox": true, "includePins": true, "includePinNets": true, "includePagePrimitives": true,
			})
			if err != nil {
				return err
			}
			if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
				return fmt.Errorf("power-cluster-plan: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
			}
			comps, err := parseLayoutComps(geom.Result)
			if err != nil {
				return err
			}
			wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
			if err != nil {
				return err
			}
			vt, err := requestAction(pinned, "schematic.visible_text.bboxes", win, nil)
			if err != nil {
				return err
			}
			if vt.Context != nil && vt.Context.DocumentUUID != "" && vt.Context.DocumentUUID != doc {
				return fmt.Errorf("power-cluster-plan: visible text came from %q, expected %q", vt.Context.DocumentUUID, doc)
			}
			var items struct {
				Items []visibleTextItem `json:"items"`
			}
			raw, _ := json.Marshal(vt.Result)
			if err := json.Unmarshal(raw, &items); err != nil {
				return fmt.Errorf("power-cluster-plan: parse visible text: %w", err)
			}
			sheet := sheetBBoxOf(comps)
			titleBlock, _ := titleBlockKeepoutWithSource(sheet)
			exempt := parsePowerClusterExemptSet(exemptInline)
			plans := planPowerClusters(comps, wires, items.Items, sheet, titleBlock, exempt)
			if playbookOut != "" {
				project := ""
				if geom.Context != nil {
					project = geom.Context.ProjectUUID
				}
				pb, perr := powerClusterPlaybook(plans, comps, project, doc)
				if perr != nil {
					return perr
				}
				b, mErr := json.MarshalIndent(pb, "", "  ")
				if mErr != nil {
					return mErr
				}
				b = append(b, '\n')
				if werr := os.WriteFile(playbookOut, b, 0644); werr != nil {
					return fmt.Errorf("write power-cluster playbook: %w", werr)
				}
				fmt.Fprintf(stderr, "power-cluster playbook written to %s (apply with easyeda sch apply %s --yes)\n", playbookOut, playbookOut)
			}
			report := powerClusterPlanReport{SchemaVersion: 1, DocumentID: doc, Plans: plans}
			for _, p := range plans {
				switch p.Status {
				case "ready":
					report.Ready++
				case "partial":
					report.Partial++
				case "blocked":
					report.Blocked++
				case "exempt":
					report.Exempt++
				}
			}
			if jsonOut || out != "" {
				b, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return err
				}
				b = append(b, '\n')
				if out != "" {
					return os.WriteFile(out, b, 0644)
				}
				_, err = stdout.Write(b)
				return err
			}
			renderPowerClusterPlan(report, stdout)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit structured JSON")
	cmd.Flags().StringVar(&out, "out", "", "write structured JSON to file")
	cmd.Flags().StringVar(&exemptInline, "exempt", "", "comma-separated DESIGNATOR or NET|DESIGNATOR clusters treated as intentional and not planned")
	cmd.Flags().StringVar(&playbookOut, "playbook", "", "write a guarded sch apply playbook for ready/partial plans (read-only generation; does not execute)")
	return cmd
}

func planPowerClusters(comps []layoutComp, wires []schGroupWire, items []visibleTextItem,
	sheet, titleBlock *layoutBBox, exempt map[string]bool) []powerClusterPlan {
	findings := powerClusterFindings(comps, wires, exempt)
	pinRoot, flagRoot := powerConnectivity(comps, wires)
	wireFind, wireIndex := wireTreeFinder(wires)
	var plans []powerClusterPlan
	for _, f := range findings {
		if f.Type == "power-cluster-exempt" {
			plans = append(plans, powerClusterPlan{
				Net: f.MarkerNet, Consumer: f.Designator, Status: "exempt",
				Note: "显式豁免；未生成接线计划，也不删除任何符号",
			})
			continue
		}
		if f.Type != "power-cluster-candidate" {
			continue
		}
		consumerPins, passivePins := splitPowerPins(comps, f.Pins, f.Designator)
		if len(consumerPins) == 0 || len(passivePins) == 0 {
			continue
		}
		clusterFlags := map[string]bool{f.SuggestKeepId: true}
		deleteSet := map[string]bool{}
		for _, id := range f.SuggestDeleteIds {
			clusterFlags[id] = true
			deleteSet[id] = true
		}
		plan := powerClusterPlan{
			Net: f.MarkerNet, Consumer: f.Designator, KeepFlag: f.SuggestKeepId,
			Status: "ready",
			Note:   "按就近消费脚归簇；应用前确认 zones/消费归属",
		}
		readyDeletes := map[string]bool{}
		blockedFlags := map[string]bool{}
		clearCount, blockedCount := 0, 0
		for _, pp := range passivePins {
			pr := pinRoot[powerPinKey(pp.c, pp.p)]
			candidates := append([]powerPinRef(nil), consumerPins...)
			sort.SliceStable(candidates, func(i, j int) bool {
				return manhattan(pp.p.X, pp.p.Y, candidates[i].p.X, candidates[i].p.Y) <
					manhattan(pp.p.X, pp.p.Y, candidates[j].p.X, candidates[j].p.Y)
			})
			var conn powerClusterConnection
			var firstBlocked *powerClusterConnection
			for _, cp := range candidates {
				c := powerClusterConnection{
					Passive: fmt.Sprintf("%s.%s", pp.c.Designator, pp.p.Number),
					From:    checkPoint{X: round2(pp.p.X), Y: round2(pp.p.Y)},
					To:      checkPoint{X: round2(cp.p.X), Y: round2(cp.p.Y)},
				}
				cr := pinRoot[powerPinKey(cp.c, cp.p)]
				if pr == cr {
					c.Status = "already-connected"
					c.DeleteFlags = flagsOnRoot(deleteSet, flagRoot, pr)
					conn = c
					break
				}
				path, blockers := planPowerRoute(pp.c, pp.p, cp.c, cp.p, clusterFlags,
					wireRootsAtPin(wires, wireFind, wireIndex, pp.p),
					wireRootsAtPin(wires, wireFind, wireIndex, cp.p),
					items, comps, wires, sheet, titleBlock)
				if len(blockers) == 0 {
					c.Status = "clear"
					c.Path = path
					c.DeleteFlags = flagsOnRoot(deleteSet, flagRoot, pr)
					conn = c
					break
				}
				c.Status = "blocked"
				c.Blockers = blockerLabels(blockers)
				if firstBlocked == nil {
					firstBlocked = &c
				}
			}
			if conn.Status == "" && firstBlocked != nil {
				conn = *firstBlocked
			}
			if conn.Status == "clear" || conn.Status == "already-connected" {
				for _, id := range conn.DeleteFlags {
					readyDeletes[id] = true
				}
				clearCount++
			} else {
				for _, id := range flagsOnRoot(deleteSet, flagRoot, pr) {
					blockedFlags[id] = true
				}
				blockedCount++
			}
			plan.Connections = append(plan.Connections, conn)
		}
		switch {
		case blockedCount == 0:
			plan.Status = "ready"
		case clearCount == 0:
			plan.Status = "blocked"
		default:
			plan.Status = "partial"
		}
		for id := range readyDeletes {
			plan.DeleteFlags = append(plan.DeleteFlags, id)
		}
		sort.Strings(plan.DeleteFlags)
		if plan.Status != "blocked" {
			for _, c := range plan.Connections {
				if c.Status == "already-connected" {
					continue
				}
				if c.Status == "blocked" {
					plan.Order = append(plan.Order, "resolve blocked passive "+c.Passive+" before deleting its flag")
					continue
				}
				plan.Order = append(plan.Order, fmt.Sprintf("connect %s -> %s via %s",
					c.Passive, pointLabel(c.To), pointsLabel(c.Path)))
			}
			plan.Order = append(plan.Order, "verify net "+plan.Net+" and merged pin roots")
			if len(plan.DeleteFlags) > 0 {
				plan.Order = append(plan.Order, "delete passive-side flags: "+strings.Join(plan.DeleteFlags, ","))
			}
			if blockedCount > 0 {
				var keep []string
				for id := range blockedFlags {
					keep = append(keep, id)
				}
				sort.Strings(keep)
				plan.Order = append(plan.Order, "keep blocked-passive flags until their routes clear: "+strings.Join(keep, ","))
			}
		} else {
			plan.Order = append(plan.Order, "do not delete flags until every passive route is clear")
		}
		plans = append(plans, plan)
	}
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].Net != plans[j].Net {
			return plans[i].Net < plans[j].Net
		}
		return plans[i].Consumer < plans[j].Consumer
	})
	return plans
}

// powerClusterPlaybook compiles the clear/partial portion of a power-cluster
// plan into the existing guarded Apply runner. It never deletes a flag before
// the new wires have passed check + bridge-check.
func powerClusterPlaybook(plans []powerClusterPlan, comps []layoutComp, project, doc string) (*playbook, error) {
	var wireSteps []playbookStep
	deleteIDs := map[string]bool{}
	clearCount := 0
	for _, p := range plans {
		for _, c := range p.Connections {
			if c.Status == "clear" {
				clearCount++
				wireSteps = append(wireSteps, playbookStep{
					ID:      fmt.Sprintf("wire-%03d-%s", clearCount, strings.ReplaceAll(c.Passive, ".", "_")),
					Name:    fmt.Sprintf("%s local power connection to %s", p.Net, p.Consumer),
					Action:  "schematic.wire.create",
					Payload: map[string]any{"points": c.Path},
					Capture: map[string]string{fmt.Sprintf("WIRE_%03d", clearCount): "$.primitiveId"},
				})
			}
		}
		for _, id := range p.DeleteFlags {
			deleteIDs[id] = true
		}
	}
	if clearCount == 0 && len(deleteIDs) == 0 {
		return nil, fmt.Errorf("no clear power-cluster wire or already-connected passive flag to apply")
	}
	expect := powerClusterExpectation(plans, comps)
	timeout, retry := 90, 0
	cont := false
	pb := &playbook{
		Version: 1, RequireFullExecution: true,
		Meta:     playbookMeta{Name: "power-cluster consolidation", Project: project, Doc: doc},
		Defaults: stepPolicy{TimeoutSec: &timeout, Retry: &retry, ContinueOnError: &cont},
	}
	pb.Steps = append(pb.Steps, playbookStep{
		ID: "verify-source-power-pins", Name: "verify consumer/passive pins before wiring",
		Action:          "schematic.components.list",
		Payload:         map[string]any{"includeBBox": true, "includePins": true, "includePinNets": true},
		ExpectSchematic: expect,
	})
	pb.Steps = append(pb.Steps, wireSteps...)
	checkAssert := map[string]string{
		"$.summary.danglingWires":       "==0",
		"$.summary.multiNetWires":       "==0",
		"$.summary.netMarkerMismatches": "==0",
		"$.summary.wireOverPins":        "==0",
		"$.summary.nonOrthogonalWires":  "==0",
		"$.summary.zeroLengthWires":     "==0",
	}
	pb.Steps = append(pb.Steps,
		playbookStep{ID: "verify-wiring-check", Run: "sch check", Flags: map[string]any{"json": true}, Assert: checkAssert},
		playbookStep{ID: "verify-wiring-bridge", Run: "sch bridge-check", Flags: map[string]any{"json": true},
			Assert: map[string]string{"$.summary.bridges": "==0", "$.summary.orphans": "==0"}},
	)
	if len(deleteIDs) > 0 {
		ids := make([]string, 0, len(deleteIDs))
		for id := range deleteIDs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		confirm := true
		pb.Steps = append(pb.Steps, playbookStep{
			ID: "delete-connected-passive-flags", Name: "delete only flags on verified connected passive roots",
			Action:  "schematic.primitives.delete",
			Payload: map[string]any{"primitiveIds": ids},
			Confirm: &confirm,
			Assert:  map[string]string{"$.total": fmt.Sprintf("==%d", len(ids))},
		})
	}
	pb.Steps = append(pb.Steps,
		playbookStep{ID: "verify-cleanup-check", Run: "sch check", Flags: map[string]any{"json": true}, Assert: checkAssert},
		playbookStep{ID: "verify-cleanup-bridge", Run: "sch bridge-check", Flags: map[string]any{"json": true},
			Assert: map[string]string{"$.summary.bridges": "==0", "$.summary.orphans": "==0"}},
		playbookStep{ID: "save-verified-power-clusters", Action: "schematic.save",
			Assert: map[string]string{"$.saved": "true"}, Checkpoint: true},
	)
	if errs := preflight(pb, nil); len(errs) > 0 {
		return nil, fmt.Errorf("power-cluster playbook failed preflight: %v", errs)
	}
	return pb, nil
}

func powerClusterExpectation(plans []powerClusterPlan, comps []layoutComp) *schematicStateExpectation {
	names := map[string]bool{}
	for _, p := range plans {
		if p.Consumer != "" {
			names[p.Consumer] = true
		}
		for _, c := range p.Connections {
			if dot := strings.LastIndex(c.Passive, "."); dot > 0 {
				names[c.Passive[:dot]] = true
			}
		}
	}
	byName := map[string]layoutComp{}
	for _, c := range comps {
		if c.Designator != "" {
			byName[c.Designator] = c
		}
	}
	expect := &schematicStateExpectation{Parts: map[string]schematicPartExpectation{}}
	for name := range names {
		c, ok := byName[name]
		if !ok {
			continue
		}
		x, y := c.X, c.Y
		part := schematicPartExpectation{PrimitiveID: c.ID, X: &x, Y: &y, Pins: map[string]schematicPinExpectation{}}
		if c.Rotation != nil {
			part.Rotation = c.Rotation
		}
		for _, pin := range c.Pins {
			px, py := pin.X, pin.Y
			ep := schematicPinExpectation{X: &px, Y: &py}
			if pin.Net != "" {
				net := pin.Net
				ep.Net = &net
			}
			part.Pins[pin.Number] = ep
		}
		expect.Parts[name] = part
	}
	return expect
}

type powerPinRef struct {
	c layoutComp
	p layoutPin
}

func splitPowerPins(comps []layoutComp, pins []string, consumer string) ([]powerPinRef, []powerPinRef) {
	byDesignator := map[string]layoutComp{}
	for _, c := range comps {
		if c.Designator != "" {
			byDesignator[c.Designator] = c
		}
	}
	var consumers, passives []powerPinRef
	for _, ref := range pins {
		dot := strings.LastIndex(ref, ".")
		if dot <= 0 {
			continue
		}
		d, num := ref[:dot], ref[dot+1:]
		c, ok := byDesignator[d]
		if !ok {
			continue
		}
		for _, p := range c.Pins {
			if p.Number == num {
				x := powerPinRef{c: c, p: p}
				if d == consumer {
					consumers = append(consumers, x)
				} else {
					passives = append(passives, x)
				}
				break
			}
		}
	}
	return consumers, passives
}

func planPowerRoute(passive layoutComp, pp layoutPin, consumer layoutComp, cp layoutPin,
	clusterFlags map[string]bool, passiveWireRoots, consumerWireRoots map[int]bool,
	items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox) ([][2]float64, []dcBlocker) {
	base := [][][2]float64{
		{{pp.X, pp.Y}, {cp.X, pp.Y}, {cp.X, cp.Y}},
		{{pp.X, pp.Y}, {pp.X, cp.Y}, {cp.X, cp.Y}},
	}
	candidates := append(powerLeadCandidates(passive, pp, consumer, cp), base...)
	candidates = append(candidates, powerRouteCandidates(base, powerObstaclesNearby(pp, cp, comps))...)
	candidates = filterDCPaths(dedupeDCPaths(candidates), sheet, titleBlock)
	var firstBlockers []dcBlocker
	bestLen := math.Inf(1)
	var best [][2]float64
	for _, path := range candidates {
		if !powerEndpointDirections(passive, pp, consumer, cp, path) {
			continue
		}
		bs := powerRouteBlockers(path, passive, consumer, clusterFlags, passiveWireRoots, consumerWireRoots,
			items, comps, wires)
		if len(bs) == 0 {
			l := powerPathLength(path)
			if l < bestLen {
				best, bestLen = path, l
			}
		} else if len(firstBlockers) == 0 {
			firstBlockers = bs
		}
	}
	if len(best) > 0 {
		return best, nil
	}
	if len(firstBlockers) == 0 {
		firstBlockers = []dcBlocker{{
			Kind: "route", ID: "no-candidate", Label: "route:no-orthogonal-path",
		}}
	}
	return nil, firstBlockers
}

func powerLeadCandidates(passive layoutComp, pp layoutPin, consumer layoutComp, cp layoutPin) [][][2]float64 {
	pd := pinOutwardDir(passive, pp)
	cd := pinOutwardDir(consumer, cp)
	if pd == "" || cd == "" {
		return nil
	}
	vec := func(dir string) [2]float64 {
		switch dir {
		case "up":
			return [2]float64{0, 1}
		case "down":
			return [2]float64{0, -1}
		case "left":
			return [2]float64{-1, 0}
		default:
			return [2]float64{1, 0}
		}
	}
	pv, cv := vec(pd), vec(cd)
	var out [][][2]float64
	for _, lp := range []float64{5, 10, 15, 20, 30, 40, 60, 80} {
		p1 := [2]float64{pp.X + pv[0]*lp, pp.Y + pv[1]*lp}
		for _, lc := range []float64{5, 10, 15, 20, 30, 40, 60, 80} {
			c1 := [2]float64{cp.X + cv[0]*lc, cp.Y + cv[1]*lc}
			p0 := [2]float64{pp.X, pp.Y}
			c0 := [2]float64{cp.X, cp.Y}
			out = append(out,
				[][2]float64{p0, p1, {c1[0], p1[1]}, c1, c0},
				[][2]float64{p0, p1, {p1[0], c1[1]}, c1, c0},
			)
		}
	}
	return out
}

func powerObstaclesNearby(pp, cp layoutPin, comps []layoutComp) []layoutBBox {
	var out []layoutBBox
	minX, maxX := math.Min(pp.X, cp.X)-20, math.Max(pp.X, cp.X)+20
	minY, maxY := math.Min(pp.Y, cp.Y)-20, math.Max(pp.Y, cp.Y)+20
	for _, c := range comps {
		if c.BBox == nil || c.ComponentType == "sheet" {
			continue
		}
		b := *c.BBox
		if b.MaxX < minX || b.MinX > maxX || b.MaxY < minY || b.MinY > maxY {
			continue
		}
		out = append(out, b)
	}
	return out
}

func powerRouteCandidates(base [][][2]float64, obstacles []layoutBBox) [][][2]float64 {
	out := append([][][2]float64(nil), base...)
	for _, b := range obstacles {
		for _, y := range []float64{b.MinY - 10, b.MaxY + 10} {
			p := base[0]
			out = append(out, [][2]float64{
				{p[0][0], p[0][1]}, {p[0][0], y}, {p[len(p)-1][0], y}, {p[len(p)-1][0], p[len(p)-1][1]},
			})
		}
		for _, x := range []float64{b.MinX - 10, b.MaxX + 10} {
			p := base[0]
			out = append(out, [][2]float64{
				{p[0][0], p[0][1]}, {x, p[0][1]}, {x, p[len(p)-1][1]}, {p[len(p)-1][0], p[len(p)-1][1]},
			})
		}
	}
	return out
}

func powerEndpointDirections(passive layoutComp, pp layoutPin, consumer layoutComp, cp layoutPin, path [][2]float64) bool {
	if d := pinOutwardDir(passive, pp); d != "" && !firstSegmentMatchesDir(path, d) {
		return false
	}
	if d := pinOutwardDir(consumer, cp); d != "" {
		if len(path) < 2 {
			return false
		}
		last, prev := path[len(path)-1], path[len(path)-2]
		dx, dy := prev[0]-last[0], prev[1]-last[1]
		switch d {
		case "up":
			return math.Abs(dx) < 0.5 && dy > 0.5
		case "down":
			return math.Abs(dx) < 0.5 && dy < -0.5
		case "left":
			return math.Abs(dy) < 0.5 && dx < -0.5
		case "right":
			return math.Abs(dy) < 0.5 && dx > 0.5
		}
	}
	return true
}

func pinOutwardDir(c layoutComp, p layoutPin) string {
	if c.BBox == nil {
		return ""
	}
	b := *c.BBox
	const eps = 0.5
	switch {
	case p.X > b.MaxX+eps:
		return "right"
	case p.X < b.MinX-eps:
		return "left"
	case p.Y > b.MaxY+eps:
		return "up"
	case p.Y < b.MinY-eps:
		return "down"
	}
	return ""
}

func powerRouteBlockers(path [][2]float64, passive, consumer layoutComp, clusterFlags map[string]bool,
	passiveRoots, consumerRoots map[int]bool, items []visibleTextItem, comps []layoutComp, wires []schGroupWire) []dcBlocker {
	find, wireIndex := wireTreeFinder(wires)
	var out []dcBlocker
	add := func(b dcBlocker) {
		for _, x := range out {
			if x.Kind == b.Kind && x.ID == b.ID {
				return
			}
		}
		out = append(out, b)
	}
	first, last := path[0], path[len(path)-1]
	for i := 0; i+1 < len(path); i++ {
		s := [4]float64{path[i][0], path[i][1], path[i+1][0], path[i+1][1]}
		trimmed := trimSegmentAtPoints(s, []checkPoint{{X: first[0], Y: first[1]}, {X: last[0], Y: last[1]}}, 1.5)
		if trimmed != nil {
			for _, c := range comps {
				if c.BBox == nil || c.ComponentType == "sheet" || clusterFlags[c.ID] {
					continue
				}
				if schSegmentCrossesBox(trimmed[0], trimmed[1], trimmed[2], trimmed[3], *c.BBox) {
					name := c.Designator
					if name == "" {
						name = c.ID
					}
					kindName := "component"
					if isSchMarker(c.ComponentType) {
						kindName = "marker"
						name = markerLabel(c)
					}
					add(dcBlocker{Kind: kindName, ID: c.ID, Label: kindName + ":" + name})
				}
			}
			for _, it := range items {
				if it.BBox == nil || clusterFlags[it.ParentID] {
					continue
				}
				if schSegmentCrossesBox(trimmed[0], trimmed[1], trimmed[2], trimmed[3], *it.BBox) {
					add(dcBlocker{Kind: "text", ID: it.ID, ParentID: it.ParentID, Label: "text:" + textLabel(it)})
				}
			}
		}
		for _, c := range comps {
			if clusterFlags[c.ID] {
				continue
			}
			for _, pin := range c.Pins {
				if pointEquals2(pin.X, pin.Y, first) || pointEquals2(pin.X, pin.Y, last) {
					continue
				}
				if pointOnSegment2(pin.X, pin.Y, path[i], path[i+1]) {
					add(dcBlocker{Kind: "pin", ID: c.ID + ":" + pin.Number,
						Label: "pin:" + c.Designator + "." + pin.Number})
				}
			}
		}
		for _, w := range wires {
			idx, ok := wireIndex[w.ID]
			if ok && (passiveRoots[find(idx)] || consumerRoots[find(idx)]) {
				continue
			}
			for _, ws := range labelWireSegments(w) {
				if segmentsCross2(path[i], path[i+1],
					[2]float64{ws[0], ws[1]}, [2]float64{ws[2], ws[3]}) {
					add(dcBlocker{Kind: "wire", ID: w.ID, Label: "wire:" + w.ID})
				}
			}
		}
	}
	return out
}

func powerPathLength(path [][2]float64) float64 {
	l := 0.0
	for i := 0; i+1 < len(path); i++ {
		l += math.Abs(path[i+1][0]-path[i][0]) + math.Abs(path[i+1][1]-path[i][1])
	}
	return l
}

func wireRootsAtPin(wires []schGroupWire, find func(int) int, wireIndex map[string]int, p layoutPin) map[int]bool {
	roots := map[int]bool{}
	for _, w := range wires {
		if !wireTouchesPoint(w, p.X, p.Y) {
			continue
		}
		if idx, ok := wireIndex[w.ID]; ok {
			roots[find(idx)] = true
		}
	}
	return roots
}

func flagsOnRoot(deleteSet map[string]bool, flagRoot map[string]int, root int) []string {
	var out []string
	for id := range deleteSet {
		if flagRoot[id] == root {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func pointLabel(p checkPoint) string {
	return fmt.Sprintf("(%.0f,%.0f)", p.X, p.Y)
}

func pointsLabel(path [][2]float64) string {
	var parts []string
	for _, p := range path {
		parts = append(parts, fmt.Sprintf("(%.0f,%.0f)", p[0], p[1]))
	}
	return strings.Join(parts, "->")
}

func renderPowerClusterPlan(rep powerClusterPlanReport, w io.Writer) {
	fmt.Fprintf(w, "power-cluster-plan: %d cluster(s), %d ready, %d partial, %d blocked, %d exempt (document %s)\n",
		len(rep.Plans), rep.Ready, rep.Partial, rep.Blocked, rep.Exempt, rep.DocumentID)
	for _, p := range rep.Plans {
		fmt.Fprintf(w, "  [%s] %s %s", p.Status, p.Net, p.Consumer)
		if p.KeepFlag != "" {
			fmt.Fprintf(w, " keep=%s", p.KeepFlag)
		}
		if len(p.DeleteFlags) > 0 {
			fmt.Fprintf(w, " delete=%s", strings.Join(p.DeleteFlags, ","))
		}
		fmt.Fprintln(w)
		for _, c := range p.Connections {
			fmt.Fprintf(w, "    %s: %s -> %s", c.Passive, pointLabel(c.From), pointLabel(c.To))
			if c.Status == "clear" {
				fmt.Fprintf(w, " path=%s", pointsLabel(c.Path))
			} else {
				fmt.Fprintf(w, " [%s]", c.Status)
			}
			if len(c.Blockers) > 0 {
				fmt.Fprintf(w, " blockers=%s", strings.Join(c.Blockers, ","))
			}
			if len(c.DeleteFlags) > 0 {
				fmt.Fprintf(w, " delete=%s", strings.Join(c.DeleteFlags, ","))
			}
			fmt.Fprintln(w)
		}
		for _, o := range p.Order {
			fmt.Fprintf(w, "    order: %s\n", o)
		}
		if p.Note != "" {
			fmt.Fprintf(w, "    note: %s\n", p.Note)
		}
	}
}
