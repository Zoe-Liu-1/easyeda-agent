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

// power-cluster-relocate-plan bridges layout and power-cluster wiring. It finds
// a labelled local power passive, selects a same-net consumer in the same
// registered module, searches a collision-free rigid translation near that
// consumer, and computes the direct lead at the moved position. It is read-only.
// A move recreates wire/flag IDs, so flag deletion is deliberately deferred to a
// fresh post-move power-cluster plan.

type powerClusterRelocateReport struct {
	SchemaVersion int                        `json:"schemaVersion"`
	DocumentID    string                     `json:"documentId"`
	MaxDistance   float64                    `json:"maxDistance"`
	MaxDirect     float64                    `json:"maxDirect"`
	SearchRadius  float64                    `json:"searchRadius"`
	Plans         []powerClusterRelocatePlan `json:"plans"`
	Ready         int                        `json:"ready"`
	Blocked       int                        `json:"blocked"`
	Ambiguous     int                        `json:"ambiguous"`
	AlreadyWired  int                        `json:"alreadyWired"`
}

type powerClusterRelocatePlan struct {
	Passive                string                  `json:"passive"`
	PartID                 string                  `json:"partId"`
	Net                    string                  `json:"net"`
	PassivePin             string                  `json:"passivePin"`
	Status                 string                  `json:"status"` // ready | blocked | ambiguous | already-wired
	Reason                 string                  `json:"reason,omitempty"`
	Consumer               string                  `json:"consumer,omitempty"`
	ConsumerPin            string                  `json:"consumerPin,omitempty"`
	CurrentDistance        float64                 `json:"currentDistance,omitempty"`
	TargetCandidates       []powerRelocateTarget   `json:"targetCandidates,omitempty"`
	Move                   *powerRelocateMove      `json:"move,omitempty"`
	Connection             *powerClusterConnection `json:"connection,omitempty"`
	DeleteFlagsBeforeMove  []string                `json:"deleteFlagsBeforeMove,omitempty"`
	RequiresPostMoveReplan bool                    `json:"requiresPostMoveReplan"`
	Note                   string                  `json:"note,omitempty"`
}

type powerRelocateTarget struct {
	Designator string  `json:"designator"`
	Pin        string  `json:"pin"`
	Net        string  `json:"net"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Distance   float64 `json:"distance"`
	Group      string  `json:"group,omitempty"`
}

type powerRelocateMove struct {
	PartID       string      `json:"partId"`
	Designator   string      `json:"designator"`
	FromAnchor   *checkPoint `json:"fromAnchor"`
	ToAnchor     *checkPoint `json:"toAnchor"`
	DeltaX       float64     `json:"deltaX"`
	DeltaY       float64     `json:"deltaY"`
	WireIDs      []string    `json:"wireIds,omitempty"`
	FlagIDs      []string    `json:"flagIds,omitempty"`
	TextIDs      []string    `json:"textIds,omitempty"`
	NewBBox      *layoutBBox `json:"newBBox,omitempty"`
	CommandHints []string    `json:"commandHints,omitempty"`
}

type powerRelocatePairRequest struct {
	Passive  string
	Consumer string
	Pin      string
}

func newSchPowerClusterRelocatePlanCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var pairSpecs, partSpecs []string
	var jsonOut bool
	var out string
	var includeGround bool
	var maxDistance, maxDirect, searchRadius float64
	cmd := &cobra.Command{
		Use:   "power-cluster-relocate-plan",
		Short: "Plan a collision-free passive move followed by a power-cluster direct connection (read-only)",
		Long: `Find 2-pin power passives represented by local labels instead of a physical branch
to an owned active/connector pin. For each passive, select a same-net consumer pin in
the same registered module and search the 5-unit grid around that pin for a collision-free
rigid translation of the passive, attached stubs and flags.

The plan includes the exact rigid member IDs and dx/dy plus the direct lead computed at
the moved position. Moving recreates wire/flag primitive IDs, so passive-side flag
deletion is deferred to a fresh post-move power-cluster-plan.

No schematic mutation occurs. Use --pair PASSIVE:CONSUMER[.PIN] to resolve an otherwise
ambiguous target; the flag may be repeated.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, _, _, groups, err := loadSchGroupsContext(cfg, *window)
			if err != nil {
				return err
			}
			requests, err := parsePowerRelocatePairs(pairSpecs)
			if err != nil {
				return err
			}
			partFilter := map[string]bool{}
			for _, raw := range partSpecs {
				for _, part := range strings.Split(raw, ",") {
					if part = strings.ToUpper(strings.TrimSpace(part)); part != "" {
						partFilter[part] = true
					}
				}
			}
			geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
				"includeBBox": true, "includePins": true, "includePinNets": true, "includePagePrimitives": true,
			})
			if err != nil {
				return err
			}
			if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
				return fmt.Errorf("power-cluster-relocate-plan: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
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
				return fmt.Errorf("power-cluster-relocate-plan: visible text came from %q, expected %q", vt.Context.DocumentUUID, doc)
			}
			var visible struct {
				Items []visibleTextItem `json:"items"`
			}
			raw, _ := json.Marshal(vt.Result)
			if err := json.Unmarshal(raw, &visible); err != nil {
				return fmt.Errorf("power-cluster-relocate-plan: parse visible text: %w", err)
			}
			sheet := sheetBBoxOf(comps)
			titleBlock, _ := titleBlockKeepoutWithSource(sheet)
			surveyValue, err := fetchSchVisualSurvey(pinned, win, comps)
			if err != nil {
				return fmt.Errorf("power-cluster-relocate-plan: module-frame survey failed: %w", err)
			}
			survey, err := parseSchFrameSurvey(surveyValue)
			if err != nil {
				return fmt.Errorf("power-cluster-relocate-plan: parse module frames: %w", err)
			}
			plans := planPowerClusterRelocations(comps, wires, visible.Items, groups,
				requests, partFilter, includeGround, maxDistance, maxDirect, searchRadius,
				sheet, titleBlock, schModuleFrames(survey))
			report := powerClusterRelocateReport{
				SchemaVersion: 1, DocumentID: doc, MaxDistance: maxDistance,
				MaxDirect: maxDirect, SearchRadius: searchRadius, Plans: plans,
			}
			for _, p := range plans {
				switch p.Status {
				case "ready":
					report.Ready++
				case "blocked":
					report.Blocked++
				case "ambiguous":
					report.Ambiguous++
				case "already-wired":
					report.AlreadyWired++
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
			renderPowerClusterRelocatePlan(report, stdout)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit structured JSON")
	cmd.Flags().StringVar(&out, "out", "", "write structured JSON to file")
	cmd.Flags().StringSliceVar(&pairSpecs, "pair", nil, "explicit PASSIVE:CONSUMER[.PIN] target; repeat or comma-separate")
	cmd.Flags().StringSliceVar(&partSpecs, "part", nil, "only plan these passive designators (CSV); default scans eligible 2-pin passives")
	cmd.Flags().BoolVar(&includeGround, "include-gnd", false, "also consider GND passives and passive-side GND flags")
	cmd.Flags().Float64Var(&maxDistance, "max-distance", 1000, "maximum pin-to-pin Manhattan distance to consider a target")
	cmd.Flags().Float64Var(&maxDirect, "max-direct", 100, "maximum current pin distance that may use a direct wire without moving")
	cmd.Flags().Float64Var(&searchRadius, "search-radius", 200, "5-unit-grid search radius around the consumer pin")
	return cmd
}

func parsePowerRelocatePairs(raw []string) (map[string]powerRelocatePairRequest, error) {
	out := map[string]powerRelocatePairRequest{}
	for _, item := range raw {
		for _, spec := range strings.Split(item, ",") {
			spec = strings.TrimSpace(spec)
			if spec == "" {
				continue
			}
			parts := strings.SplitN(spec, ":", 2)
			if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
				return nil, fmt.Errorf("--pair entry %q must be PASSIVE:CONSUMER[.PIN]", spec)
			}
			passive := strings.ToUpper(strings.TrimSpace(parts[0]))
			if _, dup := out[passive]; dup {
				return nil, fmt.Errorf("--pair repeats passive %s", passive)
			}
			target := strings.TrimSpace(parts[1])
			consumer, pin := splitDesignatorPin(target)
			out[passive] = powerRelocatePairRequest{
				Passive: passive, Consumer: strings.ToUpper(consumer), Pin: pin,
			}
		}
	}
	return out, nil
}

func splitDesignatorPin(ref string) (string, string) {
	ref = strings.TrimSpace(ref)
	if i := strings.LastIndex(ref, "."); i > 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

func planPowerClusterRelocations(comps []layoutComp, wires []schGroupWire, items []visibleTextItem,
	groups []*schGroup, requests map[string]powerRelocatePairRequest, partFilter map[string]bool,
	includeGround bool, maxDistance, maxDirect, searchRadius float64,
	sheet, titleBlock *layoutBBox, frames []schFrameSpec) []powerClusterRelocatePlan {
	pinRoot, flagRoot := powerConnectivity(comps, wires)
	groupOf := map[string]string{}
	for _, g := range groups {
		if g == nil {
			continue
		}
		for _, member := range g.Members {
			groupOf[strings.ToUpper(member)] = g.ID
		}
	}
	var flags []layoutComp
	for _, c := range comps {
		if isSchMarker(c.ComponentType) && c.AnchorAvailable && c.Net != "" {
			flags = append(flags, c)
		}
	}
	flagIDs := map[string]bool{}
	for _, f := range flags {
		flagIDs[f.ID] = true
	}

	var plans []powerClusterRelocatePlan
	for _, c := range comps {
		if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
			continue
		}
		key := strings.ToUpper(c.Designator)
		if !isPowerClusterPassive(c.Designator) || len(partFilter) > 0 && !partFilter[key] {
			continue
		}
		if len(c.Pins) != 2 || c.BBox == nil {
			continue
		}
		groupID := groupOf[key]
		var powerPin *layoutPin
		var groundPin *layoutPin
		for i := range c.Pins {
			p := &c.Pins[i]
			net := strings.TrimSpace(p.Net)
			if net == "" || !isRailLabelNet(net) {
				continue
			}
			if isPowerClusterGround(net) {
				groundPin = p
			} else {
				powerPin = p
			}
		}
		if powerPin == nil || groundPin == nil {
			continue
		}
		if isPowerClusterGround(powerPin.Net) && !includeGround {
			continue
		}
		pr, ok := pinRoot[powerPinKey(c, *powerPin)]
		if !ok {
			continue
		}
		passiveFlags := flagsOnRoot(flagIDs, flagRoot, pr)
		if len(passiveFlags) == 0 {
			continue // not the label-only representation this planner targets
		}
		plan := powerClusterRelocatePlan{
			Passive: c.Designator, PartID: c.ID, Net: powerPin.Net,
			PassivePin: powerPin.Number, Status: "blocked",
			DeleteFlagsBeforeMove:  passiveFlags,
			RequiresPostMoveReplan: true,
		}
		if groupID == "" {
			plan.Reason = "no registered functional-group ownership"
			plans = append(plans, plan)
			continue
		}
		targets := powerRelocateTargets(c, *powerPin, comps, pinRoot, groupID, groupOf, maxDistance)
		plan.TargetCandidates = append([]powerRelocateTarget(nil), targets...)
		selected, ambiguous := selectPowerRelocateTarget(targets, requests[key])
		if selected == nil {
			if ambiguous {
				plan.Status = "ambiguous"
				plan.Reason = "multiple equally-ranked same-net targets; pass --pair PASSIVE:CONSUMER[.PIN]"
			} else if req, explicit := requests[key]; explicit {
				plan.Reason = fmt.Sprintf("explicit target %s is absent/not same-net/in another group", formatRelocateRequest(req))
			} else {
				plan.Reason = "no same-net active/connector consumer inside the registered group"
			}
			plans = append(plans, plan)
			continue
		}
		plan.Consumer = selected.Designator
		plan.ConsumerPin = selected.Pin
		plan.CurrentDistance = selected.Distance
		consumer, consumerPin, ok := findRelocateTargetPin(comps, *selected)
		if !ok {
			plan.Reason = "selected consumer pin is no longer readable"
			plans = append(plans, plan)
			continue
		}
		targetFrame, frameReason := frameForRelocateTarget(consumer, frames)
		if targetFrame == nil {
			plan.Reason = frameReason
			plans = append(plans, plan)
			continue
		}
		cr := pinRoot[powerPinKey(consumer, consumerPin)]
		if pr == cr {
			plan.Status = "already-wired"
			plan.Note = "passive power pin already shares a physical wire island with the selected consumer"
			plans = append(plans, plan)
			continue
		}
		clusterFlags := map[string]bool{}
		for _, id := range passiveFlags {
			clusterFlags[id] = true
		}
		for _, id := range flagsOnRoot(flagIDs, flagRoot, cr) {
			clusterFlags[id] = true
		}
		find, wireIndex := wireTreeFinder(wires)
		currentRoots := wireRootsAtPin(wires, find, wireIndex, *powerPin)
		consumerRoots := wireRootsAtPin(wires, find, wireIndex, consumerPin)
		currentPath, currentBlockers := planPowerRoute(c, *powerPin, consumer, consumerPin,
			clusterFlags, currentRoots, consumerRoots, items, comps, wires, sheet, titleBlock)
		if selected.Distance <= maxDirect && len(currentBlockers) == 0 {
			plan.Status = "ready"
			plan.Connection = &powerClusterConnection{
				Passive: fmt.Sprintf("%s.%s", c.Designator, powerPin.Number),
				From:    checkPoint{X: round2(powerPin.X), Y: round2(powerPin.Y)},
				To:      checkPoint{X: round2(consumerPin.X), Y: round2(consumerPin.Y)},
				Status:  "clear", Path: currentPath,
				DeleteFlags: append([]string(nil), passiveFlags...),
			}
			plan.Note = "current position already supports a short direct wire; no relocation required"
			plans = append(plans, plan)
			continue
		}
		mv, connection, reason := planPowerRelocateMove(c, *powerPin, consumer, consumerPin,
			clusterFlags, items, comps, wires, sheet, titleBlock, targetFrame, searchRadius)
		if mv == nil {
			plan.Reason = reason
			if len(currentBlockers) > 0 {
				plan.Reason += "; current-position blockers=" + strings.Join(blockerLabels(currentBlockers), ",")
			}
			plans = append(plans, plan)
			continue
		}
		plan.Status = "ready"
		plan.Move = mv
		plan.Connection = connection
		plan.Note = "apply move first, then rerun power-cluster-plan against fresh IDs; do not delete the old flag IDs after the move"
		plans = append(plans, plan)
	}
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].Passive != plans[j].Passive {
			return plans[i].Passive < plans[j].Passive
		}
		return plans[i].Net < plans[j].Net
	})
	return plans
}

func powerRelocateTargets(c layoutComp, p layoutPin, comps []layoutComp, pinRoot map[string]int,
	groupID string, groupOf map[string]string, maxDistance float64) []powerRelocateTarget {
	var out []powerRelocateTarget
	for _, candidate := range comps {
		if candidate.ID == c.ID || !isPowerClusterActive(candidate.Designator) ||
			groupOf[strings.ToUpper(candidate.Designator)] != groupID {
			continue
		}
		for _, cp := range candidate.Pins {
			if strings.TrimSpace(cp.Net) != p.Net {
				continue
			}
			d := manhattan(p.X, p.Y, cp.X, cp.Y)
			if d > maxDistance {
				continue
			}
			out = append(out, powerRelocateTarget{
				Designator: candidate.Designator, Pin: cp.Number, Net: cp.Net,
				X: cp.X, Y: cp.Y, Distance: round2(d), Group: groupID,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		if out[i].Designator != out[j].Designator {
			return out[i].Designator < out[j].Designator
		}
		return out[i].Pin < out[j].Pin
	})
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

func selectPowerRelocateTarget(targets []powerRelocateTarget, req powerRelocatePairRequest) (*powerRelocateTarget, bool) {
	if req.Consumer != "" {
		for i := range targets {
			if strings.ToUpper(targets[i].Designator) != req.Consumer || req.Pin != "" && targets[i].Pin != req.Pin {
				continue
			}
			return &targets[i], false
		}
		return nil, false
	}
	if len(targets) == 0 {
		return nil, false
	}
	if len(targets) > 1 && math.Abs(targets[0].Distance-targets[1].Distance) < 0.5 {
		return nil, true
	}
	return &targets[0], false
}

func findRelocateTargetPin(comps []layoutComp, target powerRelocateTarget) (layoutComp, layoutPin, bool) {
	for _, c := range comps {
		if strings.ToUpper(c.Designator) != strings.ToUpper(target.Designator) {
			continue
		}
		for _, p := range c.Pins {
			if p.Number == target.Pin && p.Net == target.Net &&
				math.Abs(p.X-target.X) < 0.5 && math.Abs(p.Y-target.Y) < 0.5 {
				return c, p, true
			}
		}
	}
	return layoutComp{}, layoutPin{}, false
}

func formatRelocateRequest(r powerRelocatePairRequest) string {
	if r.Pin != "" {
		return r.Consumer + "." + r.Pin
	}
	return r.Consumer
}

func planPowerRelocateMove(c layoutComp, pp layoutPin, consumer layoutComp, cp layoutPin,
	clusterFlags map[string]bool, items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox, targetFrame *layoutBBox, searchRadius float64) (*powerRelocateMove, *powerClusterConnection, string) {
	var flags []schGroupFlag
	for _, o := range comps {
		if isSchMarker(o.ComponentType) && o.AnchorAvailable {
			flags = append(flags, schGroupFlag{ID: o.ID, X: o.X, Y: o.Y})
		}
	}
	var memberPins, otherPins [][2]float64
	for _, p := range c.Pins {
		memberPins = append(memberPins, [2]float64{p.X, p.Y})
	}
	for _, o := range comps {
		if o.ID == c.ID || (o.ComponentType != "" && o.ComponentType != schLayoutPartType) {
			continue
		}
		for _, p := range o.Pins {
			otherPins = append(otherPins, [2]float64{p.X, p.Y})
		}
	}
	exp := expandGroupAttachments(groupExpandInput{
		MemberPins: memberPins, OtherPins: otherPins, Wires: wires, Flags: flags,
	})
	if len(exp.Suspects) > 0 {
		return nil, nil, "incomplete-attachment-expansion"
	}
	if exp.SharedTrees > 0 {
		return nil, nil, "shared-wire-tree"
	}
	if len(exp.WireIDs) > 16 || partGroupExtent(c, exp.WireIDs, wires) > 400 {
		return nil, nil, "attachment-group-too-large"
	}
	movedComp := map[string]bool{c.ID: true}
	for _, id := range exp.FlagIDs {
		movedComp[id] = true
	}
	movedWire := map[string]bool{}
	for _, id := range exp.WireIDs {
		movedWire[id] = true
	}
	var movedTextIDs []string
	for _, it := range items {
		if it.ParentID != "" && (movedComp[it.ParentID] || movedWire[it.ParentID]) {
			movedTextIDs = append(movedTextIDs, it.ID)
		}
	}
	bestCost := math.Inf(1)
	var best *powerRelocateMove
	var bestConn *powerClusterConnection
	for _, delta := range powerRelocateDeltas(c, cp, searchRadius) {
		if !partMoveClear(c, delta, movedComp, movedWire, movedTextIDs, items, comps, wires, sheet, titleBlock) {
			continue
		}
		if !relocateMoveInsideFrame(delta, movedComp, movedWire, movedTextIDs, items, comps, wires, targetFrame) {
			continue
		}
		simItems, simComps, simWires := simulateGroupMove([]string{c.ID}, exp.FlagIDs, exp.WireIDs,
			delta, items, comps, wires)
		var movedPart layoutComp
		var movedPin layoutPin
		found := false
		for _, o := range simComps {
			if o.ID != c.ID {
				continue
			}
			movedPart = o
			for _, p := range o.Pins {
				if p.Number == pp.Number {
					movedPin, found = p, true
					break
				}
			}
			break
		}
		if !found {
			continue
		}
		find, wireIndex := wireTreeFinder(simWires)
		path, blockers := planPowerRoute(movedPart, movedPin, consumer, cp, clusterFlags,
			wireRootsAtPin(simWires, find, wireIndex, movedPin),
			wireRootsAtPin(simWires, find, wireIndex, cp),
			simItems, simComps, simWires, sheet, titleBlock)
		if len(blockers) > 0 {
			continue
		}
		if !powerPathInsideFrame(path, targetFrame) {
			continue
		}
		cost := math.Abs(delta[0]) + math.Abs(delta[1]) + powerPathLength(path)
		if cost >= bestCost {
			continue
		}
		mv := &powerRelocateMove{
			PartID: c.ID, Designator: c.Designator,
			FromAnchor: &checkPoint{X: round2(c.X), Y: round2(c.Y)},
			ToAnchor:   &checkPoint{X: round2(c.X + delta[0]), Y: round2(c.Y + delta[1])},
			DeltaX:     delta[0], DeltaY: delta[1],
			WireIDs: append([]string(nil), exp.WireIDs...),
			FlagIDs: append([]string(nil), exp.FlagIDs...),
			TextIDs: append([]string(nil), movedTextIDs...),
		}
		if movedPart.BBox != nil {
			b := translateLayoutBBox(*movedPart.BBox, [2]float64{0, 0})
			mv.NewBBox = &b
		}
		ids := []string{c.ID}
		ids = append(ids, exp.WireIDs...)
		ids = append(ids, exp.FlagIDs...)
		mv.CommandHints = []string{
			fmt.Sprintf("easyeda sch group-move --ids %s --dx %.0f --dy %.0f",
				strings.Join(ids, ","), delta[0], delta[1]),
			"then rerun easyeda sch power-cluster-plan --json",
		}
		conn := &powerClusterConnection{
			Passive: fmt.Sprintf("%s.%s", c.Designator, pp.Number),
			From:    checkPoint{X: round2(movedPin.X), Y: round2(movedPin.Y)},
			To:      checkPoint{X: round2(cp.X), Y: round2(cp.Y)},
			Status:  "clear", Path: path,
		}
		best, bestConn, bestCost = mv, conn, cost
	}
	if best == nil {
		return nil, nil, "no-clear-relocation-and-connection"
	}
	return best, bestConn, ""
}

func frameForRelocateTarget(consumer layoutComp, frames []schFrameSpec) (*layoutBBox, string) {
	if consumer.BBox == nil {
		return nil, "consumer geometry unavailable for functional-frame ownership"
	}
	x, y := bboxCenter(*consumer.BBox)
	var owners []layoutBBox
	for _, f := range frames {
		if x >= f.Rect.MinX && x <= f.Rect.MaxX && y >= f.Rect.MinY && y <= f.Rect.MaxY {
			b := f.Rect
			owners = append(owners, b)
		}
	}
	if len(owners) != 1 {
		return nil, fmt.Sprintf("consumer must belong to exactly one functional frame (found %d)", len(owners))
	}
	return &owners[0], ""
}

func relocateMoveInsideFrame(delta [2]float64, movedComp, movedWire map[string]bool,
	movedTextIDs []string, items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	frame *layoutBBox) bool {
	if frame == nil {
		return false
	}
	for _, c := range comps {
		if !movedComp[c.ID] || c.BBox == nil {
			continue
		}
		if !boxInside(translateLayoutBBox(*c.BBox, delta), *frame) {
			return false
		}
	}
	wantText := map[string]bool{}
	for _, id := range movedTextIDs {
		wantText[id] = true
	}
	for _, it := range items {
		if !wantText[it.ID] || it.BBox == nil {
			continue
		}
		if !boxInside(translateLayoutBBox(*it.BBox, delta), *frame) {
			return false
		}
	}
	for _, w := range wires {
		if !movedWire[w.ID] {
			continue
		}
		for _, seg := range labelWireSegments(w) {
			if !boxInside(layoutBBox{
				MinX: seg[0] + delta[0], MinY: seg[1] + delta[1],
				MaxX: seg[2] + delta[0], MaxY: seg[3] + delta[1],
			}, *frame) {
				return false
			}
		}
	}
	return true
}

func powerPathInsideFrame(path [][2]float64, frame *layoutBBox) bool {
	if frame == nil {
		return false
	}
	for _, p := range path {
		if p[0] < frame.MinX-0.5 || p[0] > frame.MaxX+0.5 ||
			p[1] < frame.MinY-0.5 || p[1] > frame.MaxY+0.5 {
			return false
		}
	}
	return true
}

func powerRelocateDeltas(c layoutComp, target layoutPin, radius float64) [][2]float64 {
	const step = 5.0
	if radius < step {
		radius = step
	}
	n := int(math.Floor(radius / step))
	var out [][2]float64
	for dx := -n; dx <= n; dx++ {
		for dy := -n; dy <= n; dy++ {
			anchorX := target.X + float64(dx)*step
			anchorY := target.Y + float64(dy)*step
			out = append(out, [2]float64{anchorX - c.X, anchorY - c.Y})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ax, ay := c.X+out[i][0], c.Y+out[i][1]
		bx, by := c.X+out[j][0], c.Y+out[j][1]
		da := manhattan(ax, ay, target.X, target.Y)
		db := manhattan(bx, by, target.X, target.Y)
		if da != db {
			return da < db
		}
		ca := math.Abs(out[i][0]) + math.Abs(out[i][1])
		cb := math.Abs(out[j][0]) + math.Abs(out[j][1])
		if ca != cb {
			return ca < cb
		}
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

func renderPowerClusterRelocatePlan(rep powerClusterRelocateReport, w io.Writer) {
	fmt.Fprintf(w, "power-cluster-relocate-plan: %d plan(s), %d ready, %d blocked, %d ambiguous, %d already-wired (document %s)\n",
		len(rep.Plans), rep.Ready, rep.Blocked, rep.Ambiguous, rep.AlreadyWired, rep.DocumentID)
	for _, p := range rep.Plans {
		fmt.Fprintf(w, "  [%s] %s.%s %s", strings.ToUpper(p.Status), p.Passive, p.PassivePin, p.Net)
		if p.Consumer != "" {
			fmt.Fprintf(w, " -> %s.%s", p.Consumer, p.ConsumerPin)
		}
		fmt.Fprintln(w)
		if p.Reason != "" {
			fmt.Fprintf(w, "    reason: %s\n", p.Reason)
		}
		if p.Move != nil {
			fmt.Fprintf(w, "    move: (%.0f,%.0f) -> (%.0f,%.0f) d=(%.0f,%.0f), wires=%d, flags=%d\n",
				p.Move.FromAnchor.X, p.Move.FromAnchor.Y, p.Move.ToAnchor.X, p.Move.ToAnchor.Y,
				p.Move.DeltaX, p.Move.DeltaY, len(p.Move.WireIDs), len(p.Move.FlagIDs))
			for _, hint := range p.Move.CommandHints {
				fmt.Fprintf(w, "    next: %s\n", hint)
			}
		}
		if p.Connection != nil && len(p.Connection.Path) > 0 {
			fmt.Fprintf(w, "    post-move wire: %s\n", pointsLabel(p.Connection.Path))
		}
		if len(p.TargetCandidates) > 1 {
			var targets []string
			for _, t := range p.TargetCandidates {
				targets = append(targets, fmt.Sprintf("%s.%s@%.0f", t.Designator, t.Pin, t.Distance))
			}
			fmt.Fprintf(w, "    targets: %s\n", strings.Join(targets, ", "))
		}
		if p.Note != "" {
			fmt.Fprintf(w, "    note: %s\n", p.Note)
		}
	}
}
