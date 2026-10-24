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
	"github.com/zhoushoujianwork/easyeda-agent/internal/schguard"
)

type powerOrientationProposal struct {
	MarkerID      string         `json:"markerId"`
	Net           string         `json:"net"`
	Family        string         `json:"family"`
	Mode          string         `json:"mode"`
	FromDirection string         `json:"fromDirection"`
	ToDirection   string         `json:"toDirection"`
	FromRotation  float64        `json:"fromRotation"`
	ToRotation    float64        `json:"toRotation"`
	Host          *checkPoint    `json:"host"`
	NewAnchor     *checkPoint    `json:"newAnchor"`
	NewStubPath   [][2]float64   `json:"newStubPath"`
	StubWireID    string         `json:"stubWireId"`
	Status        string         `json:"status"`
	Reason        string         `json:"reason,omitempty"`
	Original      *layoutComp    `json:"-"`
	OriginalStub  *destaggerStub `json:"-"`
}

type powerOrientationFixReport struct {
	SchemaVersion int                        `json:"schemaVersion"`
	DocumentID    string                     `json:"documentId"`
	Proposals     []powerOrientationProposal `json:"proposals"`
	Blocked       int                        `json:"blocked"`
	AppliedIDs    []string                   `json:"appliedIds,omitempty"`
	Applied       bool                       `json:"applied"`
	Saved         bool                       `json:"saved,omitempty"`
	RolledBack    bool                       `json:"rolledBack,omitempty"`
	RollbackError string                     `json:"rollbackError,omitempty"`
}

type powerOrientationScene struct {
	projectID  string
	documentID string
	comps      []layoutComp
	wires      []schGroupWire
	items      []visibleTextItem
	sheet      *layoutBBox
	title      *layoutBBox
	proposals  []powerOrientationProposal
}

type appliedPowerOrientation struct {
	Proposal         powerOrientationProposal
	NewMarkerID      string
	NewWireIDs       []string
	OldMarkerDeleted bool
	Changed          bool
}

func newSchPowerOrientationFixCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var ids, out string
	var apply, save, allowPartial bool
	cmd := &cobra.Command{
		Use:   "power-orientation-fix",
		Short: "Re-route power/ground flags so their bodies face up/down",
		Long: `Plan from power-ground-orientation findings. Power flags target body-up; ground
flags target body-down. A single-stub branch is rerouted from the host pin when
a clear orthogonal path exists; otherwise a marker whose anchor is on exactly
one parent wire is recreated vertically in place without changing that wire.
Reverse pins are not forced: they remain blocked until an explicit
orientationExempt is recorded.

Apply mode verifies one marker replacement at a time, compares the complete
pin-to-net map and bridge set, requires every target finding to disappear, and
rolls back newly changed markers on failure. Without --apply this command is
read-only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			scene, err := readPowerOrientationScene(pinned, win, doc)
			if err != nil {
				return err
			}
			wanted := parsePrimitiveIDSet(ids)
			report := powerOrientationFixReport{
				SchemaVersion: 1, DocumentID: doc,
				Proposals: filterPowerOrientationProposals(scene.proposals, wanted),
			}
			if len(wanted) > 0 && len(report.Proposals) == 0 {
				return fmt.Errorf("power-orientation-fix: selected IDs have no orientation proposal")
			}
			for _, p := range report.Proposals {
				if p.Status == "blocked" {
					report.Blocked++
				}
			}
			if err := writePowerOrientationReport(stdout, out, report); err != nil {
				return err
			}
			if !apply {
				fmt.Fprintln(stderr, "power-orientation-fix: dry-run only; pass --apply to write")
				return nil
			}
			if report.Blocked > 0 && !allowPartial {
				return fmt.Errorf("power-orientation-fix: %d proposal(s) are blocked; pass --allow-partial to apply only ready items", report.Blocked)
			}
			if err := executePowerOrientationFix(pinned, win, doc, scene, &report, save); err != nil {
				return err
			}
			return writePowerOrientationReport(stdout, out, report)
		},
	}
	cmd.Flags().StringVar(&ids, "id", "", "optional comma-separated netflag IDs")
	cmd.Flags().BoolVar(&apply, "apply", false, "replace ready marker branches")
	cmd.Flags().BoolVar(&save, "save", false, "save after fresh verification")
	cmd.Flags().BoolVar(&allowPartial, "allow-partial", false, "apply ready markers while reporting blocked markers")
	cmd.Flags().StringVar(&out, "out", "", "write structured report to JSON")
	return cmd
}

func writePowerOrientationReport(stdout io.Writer, out string, report powerOrientationFixReport) error {
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

func readPowerOrientationScene(pinned *appConfig, win, doc string) (powerOrientationScene, error) {
	geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePins": true, "includePinNets": true, "includePagePrimitives": true,
	})
	if err != nil {
		return powerOrientationScene{}, err
	}
	if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
		return powerOrientationScene{}, fmt.Errorf("power-orientation-fix: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
	}
	comps, err := parseLayoutComps(geom.Result)
	if err != nil {
		return powerOrientationScene{}, err
	}
	wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
	if err != nil {
		return powerOrientationScene{}, err
	}
	vt, err := requestAction(pinned, "schematic.visible_text.bboxes", win, nil)
	if err != nil {
		return powerOrientationScene{}, err
	}
	var vision alignmentVision
	raw, _ := json.Marshal(vt.Result)
	if err := json.Unmarshal(raw, &vision); err != nil {
		return powerOrientationScene{}, fmt.Errorf("power-orientation-fix: parse visible text: %w", err)
	}
	sheet := sheetBBoxOf(comps)
	titleBlock, _ := titleBlockKeepoutWithSource(sheet)
	proposals := buildPowerOrientationProposals(comps, wires, vision.Items, sheet, titleBlock, nil)
	projectID := ""
	if geom.Context != nil {
		projectID = geom.Context.ProjectUUID
	}
	return powerOrientationScene{
		projectID: projectID, documentID: doc, comps: comps, wires: wires,
		items: vision.Items, sheet: sheet, title: titleBlock, proposals: proposals,
	}, nil
}

func buildPowerOrientationProposals(comps []layoutComp, wires []schGroupWire,
	items []visibleTextItem, sheet, titleBlock *layoutBBox, exempt map[string]bool) []powerOrientationProposal {
	problems, _ := powerGroundOrientationFindings(comps, exempt)
	seen := map[string]bool{}
	for _, finding := range problems {
		seen[finding.PrimitiveId] = true
	}
	for _, finding := range reversedNetFlagFindings(comps, wires) {
		if !seen[finding.PrimitiveId] {
			problems = append(problems, finding)
			seen[finding.PrimitiveId] = true
		}
	}
	byID := map[string]layoutComp{}
	for _, c := range comps {
		byID[c.ID] = c
	}
	var out []powerOrientationProposal
	for _, finding := range problems {
		c, ok := byID[finding.PrimitiveId]
		if !ok || c.Rotation == nil {
			continue
		}
		family := "power"
		if isPowerClusterGround(c.Net) {
			family = "ground"
		}
		want := "up"
		if family == "ground" {
			want = "down"
		}
		fromDir, _ := flagDirectionOf(family, *c.Rotation)
		base := powerOrientationProposal{
			MarkerID: c.ID, Net: c.Net, Family: family,
			FromDirection: fromDir, ToDirection: want,
			FromRotation: *c.Rotation, ToRotation: flagBodyRotation[family][want],
			Status: "blocked", Original: &c,
		}
		stub, reason := stubOfMarker(c, wires)
		if stub == nil || reason != "" {
			if fallback, ok, adapterReason := planAdapterPowerOrientation(c, family, want, items, comps, wires, sheet, titleBlock); ok {
				fallback.MarkerID, fallback.Net, fallback.Family = c.ID, c.Net, family
				fallback.FromDirection, fallback.ToDirection = fromDir, want
				fallback.FromRotation, fallback.ToRotation = *c.Rotation, flagBodyRotation[family][want]
				fallback.Original = &c
				out = append(out, fallback)
				continue
			} else if adapterReason != "" {
				reason += ";adapter:" + adapterReason
			}
			base.Reason = "unsafe-marker-stub:" + reason
			out = append(out, base)
			continue
		}
		base.StubWireID = stub.WireID
		base.OriginalStub = stub
		host, hostPin, ok := powerOrientationHostPin(comps, stub.HostX, stub.HostY)
		if !ok {
			base.Reason = "host-pin-unresolved"
			out = append(out, base)
			continue
		}
		_ = host
		outward, ok := pinCardinalOutward(hostPin)
		if !ok {
			base.Reason = "pin-outward-direction-unknown"
			out = append(out, base)
			continue
		}
		if (want == "up" && outward == "down") || (want == "down" && outward == "up") {
			base.Reason = "reverse-pin-requires-orientation-exempt"
			out = append(out, base)
			continue
		}
		best, ok := choosePowerOrientationPath(c, stub, hostPin, outward, want, items, comps, wires, sheet, titleBlock)
		if !ok {
			best, ok = choosePowerOrientationPathAStar(c, stub, hostPin, outward, want, items, comps, wires, sheet, titleBlock)
		}
		if !ok {
			if fallback, ok, _ := planAdapterPowerOrientation(c, family, want, items, comps, wires, sheet, titleBlock); ok {
				fallback.MarkerID, fallback.Net, fallback.Family = c.ID, c.Net, family
				fallback.FromDirection, fallback.ToDirection = fromDir, want
				fallback.FromRotation, fallback.ToRotation = *c.Rotation, flagBodyRotation[family][want]
				fallback.Original = &c
				out = append(out, fallback)
				continue
			}
			_, _, adapterReason := planAdapterPowerOrientation(c, family, want, items, comps, wires, sheet, titleBlock)
			base.Reason = "no-clear-vertical-marker-route"
			if adapterReason != "" {
				base.Reason += ":" + adapterReason
			}
			out = append(out, base)
			continue
		}
		best.MarkerID, best.Net, best.Family = c.ID, c.Net, family
		best.FromDirection, best.ToDirection = fromDir, want
		best.FromRotation, best.ToRotation = *c.Rotation, flagBodyRotation[family][want]
		best.Mode = "reroute-stub"
		best.StubWireID = stub.WireID
		best.Original = &c
		best.OriginalStub = stub
		out = append(out, best)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MarkerID < out[j].MarkerID })
	return out
}

func powerOrientationHostPin(comps []layoutComp, x, y float64) (layoutComp, layoutPin, bool) {
	for _, c := range comps {
		if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
			continue
		}
		for _, p := range c.Pins {
			if math.Abs(p.X-x) <= 0.5 && math.Abs(p.Y-y) <= 0.5 {
				return c, p, true
			}
		}
	}
	return layoutComp{}, layoutPin{}, false
}

// planAdapterPowerOrientation preserves the existing shared/branched wire tree
// and appends one short orthogonal adapter branch at the flag's existing wire
// endpoint. Recreating a horizontal flag vertically at the same point would
// make bridge/reverse rules see a body-side mismatch, so the new branch makes
// the final approach serve the required up/down body direction.
func planAdapterPowerOrientation(c layoutComp, family, want string, items []visibleTextItem,
	comps []layoutComp, wires []schGroupWire, sheet, titleBlock *layoutBBox) (powerOrientationProposal, bool, string) {
	wireID, attach, ok := powerOrientationAttachmentPoint(c, wires)
	if !ok {
		return powerOrientationProposal{}, false, "attachment-unresolved"
	}
	kind := "power"
	if family == "ground" {
		kind = "ground"
	}
	stub := &destaggerStub{Flag: c, WireID: wireID, HostX: attach.X, HostY: attach.Y, Dir: want}
	vertical := 1.0
	if want == "down" {
		vertical = -1
	}
	lastBlocker := "no-clear-adapter-route"
	for _, length := range []float64{15, 20, 25, 30, 40, 50, 60, 70, 80, 90, 100, 120, 140} {
		for _, dx := range []float64{0, 20, -20, 40, -40, 60, -60, 80, -80, 100, -100, 120, -120} {
			end := [2]float64{attach.X + dx, attach.Y + vertical*length}
			var path [][2]float64
			if dx == 0 {
				path = [][2]float64{{attach.X, attach.Y}, end}
			} else {
				path = [][2]float64{
					{attach.X, attach.Y},
					{attach.X + dx, attach.Y},
					end,
				}
			}
			newBox := predictedMarkerBBox(end[0], end[1], kind, want, c.Net)
			if !powerOrientationMarkerBoxClear(c, newBox, items, comps, wires, wireID, sheet, titleBlock) {
				lastBlocker = "marker-box"
				continue
			}
			if !powerOrientationPathEndpointsClear(stub, path, wires) {
				lastBlocker = "path-endpoint"
				continue
			}
			if !powerOrientationRouteClear(c, newBox, stub, path, items, comps, wires, sheet, titleBlock) {
				lastBlocker = "path-geometry"
				continue
			}
			host := attach
			anchor := checkPoint{X: end[0], Y: end[1]}
			return powerOrientationProposal{
				Mode: "adapter-vertical-flag", Host: &host, NewAnchor: &anchor,
				NewStubPath: append([][2]float64(nil), path...),
				StubWireID:  wireID, Status: "ready",
			}, true, ""
		}
	}
	return powerOrientationProposal{}, false, lastBlocker
}

func powerOrientationAttachmentPoint(c layoutComp, wires []schGroupWire) (string, checkPoint, bool) {
	const attachmentEps = 5.5
	var wireIDs []string
	bestDistance := math.Inf(1)
	var best checkPoint
	for _, wire := range wires {
		if wire.Net != "" && c.Net != "" && wire.Net != c.Net {
			continue
		}
		found := false
		for _, seg := range labelWireSegments(wire) {
			for _, point := range [][2]float64{{seg[0], seg[1]}, {seg[2], seg[3]}} {
				dx, dy := point[0]-c.X, point[1]-c.Y
				if math.Abs(dx) > attachmentEps || math.Abs(dy) > attachmentEps {
					continue
				}
				distance := math.Hypot(dx, dy)
				if distance < bestDistance {
					bestDistance = distance
					best = checkPoint{X: point[0], Y: point[1]}
				}
				found = true
			}
		}
		if found {
			wireIDs = appendUniqueString(wireIDs, wire.ID)
		}
	}
	if len(wireIDs) != 1 {
		return "", checkPoint{}, false
	}
	return wireIDs[0], best, true
}

func pinCardinalOutward(p layoutPin) (string, bool) {
	if !p.RotationKnown {
		return "", false
	}
	x, y, ok := schguard.CardinalOutward(p.Rotation)
	if !ok {
		return "", false
	}
	switch {
	case x > 0:
		return "right", true
	case x < 0:
		return "left", true
	case y > 0:
		return "up", true
	default:
		return "down", true
	}
}

func choosePowerOrientationPath(c layoutComp, stub *destaggerStub, host layoutPin,
	outward, want string, items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox) (powerOrientationProposal, bool) {
	vertical := 1.0
	if want == "down" {
		vertical = -1
	}
	var best *powerOrientationProposal
	bestCost := math.Inf(1)
	lengths := []float64{20, 30, 40, 50, 60, 70, 80, 100, 120, 140}
	rises := []float64{20, 30, 40, 50, 60, 70, 80, 100}
	for _, length := range lengths {
		paths := [][][2]float64{}
		if outward == want {
			paths = append(paths, [][2]float64{
				{host.X, host.Y},
				{host.X, host.Y + vertical*length},
			})
			firstY := host.Y + vertical*20
			for _, xoff := range []float64{20, -20, 40, -40, 60, -60, 80, -80} {
				paths = append(paths, [][2]float64{
					{host.X, host.Y},
					{host.X, firstY},
					{host.X + xoff, firstY},
					{host.X + xoff, host.Y + vertical*length},
				})
			}
		} else {
			hx := 1.0
			if outward == "left" {
				hx = -1
			}
			for _, rise := range rises {
				corner := [2]float64{host.X + hx*rise, host.Y}
				end := [2]float64{corner[0], corner[1] + vertical*length}
				paths = append(paths, [][2]float64{{host.X, host.Y}, corner, end})
			}
		}
		for _, path := range paths {
			end := path[len(path)-1]
			kind := "power"
			if want == "down" {
				kind = "ground"
			}
			newBox := predictedMarkerBBox(end[0], end[1], kind, want, c.Net)
			if !powerOrientationRouteClear(c, newBox, stub, path, items, comps, wires, sheet, titleBlock) {
				continue
			}
			if !firstSegmentMatchesDir(path, outward) {
				continue
			}
			cost := math.Abs(end[0]-c.X) + math.Abs(end[1]-c.Y)
			if cost < bestCost {
				h := checkPoint{X: host.X, Y: host.Y}
				a := checkPoint{X: end[0], Y: end[1]}
				best = &powerOrientationProposal{
					Host: &h, NewAnchor: &a, NewStubPath: append([][2]float64(nil), path...),
					Status: "ready",
				}
				bestCost = cost
			}
		}
	}
	if best == nil {
		return powerOrientationProposal{}, false
	}
	return *best, true
}

// choosePowerOrientationPathAStar is the fallback used after the bounded
// straight/elbow candidates fail. It searches the same 5-raw routing lattice
// for a nearby vertical anchor and uses the shared obstacle-aware A* core.
// The final segment must enter the marker from below for power-up and from
// above for ground-down, so the visible connection point matches the body.
func choosePowerOrientationPathAStar(c layoutComp, stub *destaggerStub, host layoutPin,
	outward, want string, items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox) (powerOrientationProposal, bool) {
	if sheet == nil || stub == nil {
		return powerOrientationProposal{}, false
	}
	vertical := 1.0
	if want == "down" {
		vertical = -1
	}
	type anchorCandidate struct {
		x, y float64
		cost float64
	}
	var anchors []anchorCandidate
	// The fast straight/elbow pass already covers short local offsets. This
	// fallback deliberately searches a bounded set of wider lanes; expanding the
	// full Cartesian grid here multiplies A* cost by every blocked marker.
	for _, dx := range []float64{-120, -60, 0, 60, 120} {
		for _, length := range []float64{40, 80, 120, 160, 200} {
			x := host.X + dx
			y := host.Y + vertical*length
			anchors = append(anchors, anchorCandidate{
				x: x, y: y, cost: math.Abs(dx) + length,
			})
		}
	}
	sort.Slice(anchors, func(i, j int) bool {
		if anchors[i].cost != anchors[j].cost {
			return anchors[i].cost < anchors[j].cost
		}
		if anchors[i].x != anchors[j].x {
			return anchors[i].x < anchors[j].x
		}
		return anchors[i].y < anchors[j].y
	})
	start := [2]float64{host.X, host.Y}
	for _, anchor := range anchors {
		end := [2]float64{anchor.x, anchor.y}
		newBox := predictedMarkerBBox(end[0], end[1], map[bool]string{true: "ground", false: "power"}[want == "down"], want, c.Net)
		if !powerOrientationMarkerBoxClear(c, newBox, items, comps, wires, stub.WireID, sheet, titleBlock) {
			continue
		}
		clear := func(p, q [2]float64) bool {
			if pointEquals2(p[0], p[1], start) && !firstSegmentMatchesDir([][2]float64{p, q}, outward) {
				return false
			}
			return powerOrientationSegmentClear(c, stub, host, p, q, items, comps, wires, sheet)
		}
		path, ok := dcFindOrthogonalPath(start, end, sheet, titleBlock, clear, 500)
		if !ok || !firstSegmentMatchesDir(path, outward) {
			continue
		}
		lastA, lastB := path[len(path)-2], path[len(path)-1]
		if directionBetween(lastA, lastB) != want {
			continue
		}
		if !powerOrientationRouteClear(c, newBox, stub, path, items, comps, wires, sheet, titleBlock) {
			continue
		}
		h := checkPoint{X: host.X, Y: host.Y}
		a := checkPoint{X: end[0], Y: end[1]}
		return powerOrientationProposal{
			Host: &h, NewAnchor: &a, NewStubPath: append([][2]float64(nil), path...),
			Status: "ready",
		}, true
	}
	return powerOrientationProposal{}, false
}

func powerOrientationSegmentClear(c layoutComp, stub *destaggerStub, host layoutPin,
	p, q [2]float64, items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet *layoutBBox) bool {
	trimPtr := trimSegmentAtPoints(
		[4]float64{p[0], p[1], q[0], q[1]},
		[]checkPoint{{X: host.X, Y: host.Y}},
		0.5,
	)
	var trimmed [4]float64
	if trimPtr == nil {
		trimmed = [4]float64{p[0], p[1], q[0], q[1]}
	} else {
		trimmed = *trimPtr
	}
	const clearance = 2.0
	if sheet != nil {
		usable := schUsableArea(*sheet)
		if !pointInBox2(trimmed[0], trimmed[1], usable, clearance) ||
			!pointInBox2(trimmed[2], trimmed[3], usable, clearance) {
			return false
		}
	}
	for _, comp := range comps {
		if comp.ID == c.ID || comp.BBox == nil || comp.ComponentType == "sheet" {
			continue
		}
		if schSegmentCrossesBox(trimmed[0], trimmed[1], trimmed[2], trimmed[3],
			inflateLayoutBBox(*comp.BBox, clearance)) {
			return false
		}
		for _, pin := range comp.Pins {
			if math.Abs(pin.X-host.X) <= 0.5 && math.Abs(pin.Y-host.Y) <= 0.5 {
				continue
			}
			if powerOrientationPinLeadTouchesSegment(comp, pin, p, q) {
				return false
			}
			if pointNearSegment2(pin.X, pin.Y,
				[2]float64{trimmed[0], trimmed[1]},
				[2]float64{trimmed[2], trimmed[3]}, clearance) {
				return false
			}
		}
	}
	for _, item := range items {
		if item.ParentID == c.ID || item.BBox == nil {
			continue
		}
		if schSegmentCrossesBox(trimmed[0], trimmed[1], trimmed[2], trimmed[3],
			inflateLayoutBBox(*item.BBox, clearance)) {
			return false
		}
	}
	for _, wire := range wires {
		if wire.ID == stub.WireID {
			continue
		}
		for _, seg := range labelWireSegments(wire) {
			if segmentsCross2(
				[2]float64{trimmed[0], trimmed[1]},
				[2]float64{trimmed[2], trimmed[3]},
				[2]float64{seg[0], seg[1]},
				[2]float64{seg[2], seg[3]}) {
				return false
			}
		}
	}
	return true
}

func powerOrientationRouteClear(c layoutComp, newBox layoutBBox, stub *destaggerStub,
	path [][2]float64, items []visibleTextItem, comps []layoutComp, wires []schGroupWire,
	sheet, titleBlock *layoutBBox) bool {
	if sheet == nil || len(path) < 2 {
		return false
	}
	if !powerOrientationPathEndpointsClear(stub, path, wires) {
		return false
	}
	if !powerOrientationMarkerBoxClear(c, newBox, items, comps, wires, stub.WireID, sheet, titleBlock) {
		return false
	}
	const clearance = 2.0
	end := path[len(path)-1]
	cuts := []checkPoint{{X: stub.HostX, Y: stub.HostY}, {X: end[0], Y: end[1]}}
	for i := 0; i+1 < len(path); i++ {
		trimmed := trimSegmentAtPoints([4]float64{path[i][0], path[i][1], path[i+1][0], path[i+1][1]}, cuts, 1.0)
		if trimmed == nil {
			continue
		}
		for _, o := range comps {
			if o.ID == c.ID || o.BBox == nil || o.ComponentType == "sheet" {
				continue
			}
			if schSegmentCrossesBox(trimmed[0], trimmed[1], trimmed[2], trimmed[3],
				inflateLayoutBBox(*o.BBox, clearance)) {
				return false
			}
			for _, pin := range o.Pins {
				if math.Abs(pin.X-stub.HostX) <= 0.5 && math.Abs(pin.Y-stub.HostY) <= 0.5 {
					continue
				}
				if powerOrientationPinLeadTouchesSegment(o, pin,
					[2]float64{trimmed[0], trimmed[1]},
					[2]float64{trimmed[2], trimmed[3]}) {
					return false
				}
				if pointNearSegment2(pin.X, pin.Y, [2]float64{trimmed[0], trimmed[1]},
					[2]float64{trimmed[2], trimmed[3]}, clearance) {
					return false
				}
			}
		}
		for _, it := range items {
			if it.ParentID == c.ID || it.BBox == nil {
				continue
			}
			if schSegmentCrossesBox(trimmed[0], trimmed[1], trimmed[2], trimmed[3],
				inflateLayoutBBox(*it.BBox, clearance)) {
				return false
			}
		}
		for _, w := range wires {
			if w.ID == stub.WireID {
				continue
			}
			for _, ws := range labelWireSegments(w) {
				if segmentsCross2(
					[2]float64{trimmed[0], trimmed[1]}, [2]float64{trimmed[2], trimmed[3]},
					[2]float64{ws[0], ws[1]}, [2]float64{ws[2], ws[3]}) {
					return false
				}
			}
		}
	}
	return true
}

// The host endpoint is intentionally shared with the original pin. Every
// later vertex, including the final marker anchor, must be electrically clear
// of foreign wire geometry. Segment-crossing checks trim both ends, so they
// cannot catch a candidate that terminates exactly on another net's wire.
func powerOrientationPathEndpointsClear(stub *destaggerStub, path [][2]float64,
	wires []schGroupWire) bool {
	if stub == nil || len(path) < 2 {
		return false
	}
	for i := 1; i < len(path); i++ {
		px, py := path[i][0], path[i][1]
		for _, w := range wires {
			if w.ID == stub.WireID {
				continue
			}
			for _, s := range labelWireSegments(w) {
				if pointOnSegment2(px, py, [2]float64{s[0], s[1]}, [2]float64{s[2], s[3]}) {
					return false
				}
			}
		}
	}
	return true
}

func powerOrientationMarkerBoxClear(c layoutComp, newBox layoutBBox, items []visibleTextItem,
	comps []layoutComp, wires []schGroupWire, stubWireID string, sheet, titleBlock *layoutBBox) bool {
	if sheet == nil {
		return false
	}
	const clearance = 2.0
	usable := schUsableArea(*sheet)
	if !boxInside2(newBox, usable) {
		return false
	}
	if titleBlock != nil && positiveOverlap(newBox, *titleBlock) {
		return false
	}
	newBoxGrown := inflateLayoutBBox(newBox, clearance)
	for _, o := range comps {
		if o.ID == c.ID || o.BBox == nil || o.ComponentType == "sheet" {
			continue
		}
		if anyOverlap(newBoxGrown, *o.BBox) {
			return false
		}
		for _, pin := range o.Pins {
			if powerOrientationPinLeadCrossesBox(o, pin, newBoxGrown) {
				return false
			}
		}
	}
	for _, it := range items {
		if it.ParentID == c.ID || it.BBox == nil {
			continue
		}
		if anyOverlap(newBoxGrown, *it.BBox) {
			return false
		}
	}
	for _, w := range wires {
		if w.ID == stubWireID {
			continue
		}
		for _, s := range labelWireSegments(w) {
			if schSegmentCrossesBox(s[0], s[1], s[2], s[3], newBoxGrown) {
				return false
			}
		}
	}
	return true
}

func powerOrientationPinLeadTouchesSegment(comp layoutComp, pin layoutPin, p, q [2]float64) bool {
	leadStart, leadEnd, ok := powerOrientationPinLead(comp, pin)
	if !ok {
		return false
	}
	return schguard.SegmentTouchesPinLead(
		schguard.Point{X: p[0], Y: p[1]},
		schguard.Point{X: q[0], Y: q[1]},
		leadStart, leadEnd, schguard.Point{X: pin.X, Y: pin.Y},
	)
}

func powerOrientationPinLeadCrossesBox(comp layoutComp, pin layoutPin, box layoutBBox) bool {
	leadStart, leadEnd, ok := powerOrientationPinLead(comp, pin)
	if !ok {
		return false
	}
	return schSegmentCrossesBox(leadStart.X, leadStart.Y, leadEnd.X, leadEnd.Y, box)
}

func powerOrientationPinLead(comp layoutComp, pin layoutPin) (schguard.Point, schguard.Point, bool) {
	if comp.BBox == nil || !pin.RotationKnown {
		return schguard.Point{}, schguard.Point{}, false
	}
	return schguard.PinLeadInterval(
		schguard.Point{X: pin.X, Y: pin.Y}, pin.Rotation,
		schguard.BBox{
			MinX: comp.BBox.MinX, MinY: comp.BBox.MinY,
			MaxX: comp.BBox.MaxX, MaxY: comp.BBox.MaxY,
		},
	)
}

func filterPowerOrientationProposals(in []powerOrientationProposal, wanted map[string]bool) []powerOrientationProposal {
	if len(wanted) == 0 {
		return in
	}
	var out []powerOrientationProposal
	for _, p := range in {
		if wanted[p.MarkerID] {
			out = append(out, p)
		}
	}
	return out
}

func executePowerOrientationFix(pinned *appConfig, win, doc string, scene powerOrientationScene,
	report *powerOrientationFixReport, save bool) error {
	beforeNets, _, err := readLiveNets(pinned, win)
	if err != nil {
		return fmt.Errorf("power-orientation-fix: baseline netlist: %w", err)
	}
	beforeBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("power-orientation-fix: baseline bridge set: %w", err)
	}
	var applied []appliedPowerOrientation
	rollback := func(cause error) error {
		if len(applied) == 0 {
			return cause
		}
		rbErr := rollbackPowerOrientation(pinned, win, doc, applied)
		report.RolledBack = rbErr == nil
		if rbErr != nil {
			report.RollbackError = rbErr.Error()
			return fmt.Errorf("%v; marker rollback FAILED (%v)", cause, rbErr)
		}
		return fmt.Errorf("%v; marker changes were rolled back", cause)
	}
	for _, proposal := range report.Proposals {
		if proposal.Status != "ready" {
			continue
		}
		app, err := executePowerOrientationProposal(pinned, win, doc, proposal)
		if app.Changed || app.NewMarkerID != "" || len(app.NewWireIDs) > 0 || app.OldMarkerDeleted {
			applied = append(applied, app)
		}
		if err != nil {
			return rollback(err)
		}
		report.AppliedIDs = append(report.AppliedIDs, proposal.MarkerID)
		if err := verifyPowerOrientationBaseline(pinned, win, doc, beforeNets, beforeBridges); err != nil {
			return rollback(err)
		}
	}
	after, err := readPowerOrientationScene(pinned, win, doc)
	if err != nil {
		return rollback(err)
	}
	wantGone := map[string]bool{}
	for _, p := range report.Proposals {
		if p.Status == "ready" {
			wantGone[p.MarkerID] = true
		}
	}
	newMarkerIDs := map[string]bool{}
	for _, app := range applied {
		if app.NewMarkerID != "" {
			newMarkerIDs[app.NewMarkerID] = true
		}
	}
	remaining, _ := powerGroundOrientationFindings(after.comps, nil)
	for _, finding := range remaining {
		if wantGone[finding.PrimitiveId] || newMarkerIDs[finding.PrimitiveId] {
			return rollback(fmt.Errorf("power-orientation-fix: marker %s remains non-vertical", finding.PrimitiveId))
		}
	}
	for _, finding := range reversedNetFlagFindings(after.comps, after.wires) {
		if wantGone[finding.PrimitiveId] || newMarkerIDs[finding.PrimitiveId] {
			return rollback(fmt.Errorf("power-orientation-fix: marker %s remains body/stub-reversed", finding.PrimitiveId))
		}
	}
	if err := verifyPowerOrientationBaseline(pinned, win, doc, beforeNets, beforeBridges); err != nil {
		return rollback(err)
	}
	if save {
		saved, serr := requestAutolayoutAction(pinned, "schematic.save", win, nil, doc, "power-orientation-fix 保存")
		if serr != nil {
			return fmt.Errorf("power-orientation-fix: save: %w", serr)
		}
		ok, _ := saved.Result["saved"].(bool)
		if !ok {
			return fmt.Errorf("power-orientation-fix: schematic.save did not return saved:true")
		}
		report.Saved = true
	}
	report.Applied = true
	return nil
}

func executePowerOrientationProposal(pinned *appConfig, win, doc string,
	p powerOrientationProposal) (appliedPowerOrientation, error) {
	if p.Mode == "adapter-vertical-flag" {
		return executeAdapterPowerOrientation(pinned, win, doc, p)
	}
	if p.Mode == "in-place-vertical-flag" {
		return executeInPlacePowerOrientation(pinned, win, doc, p)
	}
	if p.Original == nil || p.OriginalStub == nil || p.Host == nil || p.NewAnchor == nil || len(p.NewStubPath) < 2 {
		return appliedPowerOrientation{Proposal: p}, fmt.Errorf("proposal %s lacks complete geometry", p.MarkerID)
	}
	kind := "power"
	if p.Family == "ground" {
		kind = "ground"
	}
	app := appliedPowerOrientation{Proposal: p}
	disc, err := requestAutolayoutAction(pinned, "schematic.pin.disconnect", win, map[string]any{
		"wirePrimitiveId": p.StubWireID, "pinX": p.Host.X, "pinY": p.Host.Y,
	}, doc, "power-orientation-fix 拆除旧支路")
	if err != nil {
		return app, err
	}
	if disc.Result["disconnected"] != true || disc.Result["partial"] == true {
		return app, fmt.Errorf("old marker branch disconnect not proven")
	}
	app.Changed = true
	var preliminaryWireIDs []string
	connectStart := [2]float64{p.Host.X, p.Host.Y}
	var via *[2]float64
	if len(p.NewStubPath) > 3 {
		pre := p.NewStubPath[:len(p.NewStubPath)-1]
		points := make([]float64, 0, len(pre)*2)
		for _, q := range pre {
			points = append(points, q[0], q[1])
		}
		wres, werr := requestAutolayoutAction(pinned, "schematic.wire.create", win, map[string]any{
			"points": points, "net": p.Net,
		}, doc, "power-orientation-fix 创建多段前置路径")
		if werr != nil {
			return app, werr
		}
		if id, _ := wres.Result["primitiveId"].(string); id != "" {
			preliminaryWireIDs = append(preliminaryWireIDs, id)
		}
		connectStart = pre[len(pre)-1]
	} else {
		_, _, v, err := dcMarkerPathTerm(p.NewStubPath, p.Host.X, p.Host.Y, p.NewAnchor.X, p.NewAnchor.Y)
		if err != nil {
			return appliedPowerOrientation{Proposal: p}, err
		}
		via = v
	}
	lastA := p.NewStubPath[len(p.NewStubPath)-2]
	lastB := p.NewStubPath[len(p.NewStubPath)-1]
	dir, off, _, err := dcMarkerPathTerm([][2]float64{lastA, lastB},
		lastA[0], lastA[1], lastB[0], lastB[1])
	if err != nil {
		return appliedPowerOrientation{Proposal: p}, err
	}
	payload := map[string]any{
		"pinX": connectStart[0], "pinY": connectStart[1], "kind": kind, "net": p.Net,
		"direction": dir, "offset": off, "rotation": p.ToRotation,
	}
	if via != nil {
		payload["via"] = []float64{via[0], via[1]}
	}
	created, err := requestAutolayoutAction(pinned, "schematic.power.connect_pin", win, payload, doc, "power-orientation-fix 重建支路")
	if err != nil {
		if len(preliminaryWireIDs) > 0 {
			_, _ = requestAutolayoutAction(pinned, "schematic.primitives.delete", win,
				map[string]any{"primitiveIds": preliminaryWireIDs}, doc, "power-orientation-fix 清理前置路径")
		}
		return app, err
	}
	newMarker, _ := created.Result["flagPrimitiveId"].(string)
	newWire, _ := created.Result["wirePrimitiveId"].(string)
	if newMarker == "" || newWire == "" {
		if len(preliminaryWireIDs) > 0 {
			_, _ = requestAutolayoutAction(pinned, "schematic.primitives.delete", win,
				map[string]any{"primitiveIds": preliminaryWireIDs}, doc, "power-orientation-fix 清理未确认前置路径")
		}
		app.NewMarkerID = newMarker
		app.NewWireIDs = append(app.NewWireIDs, preliminaryWireIDs...)
		if newWire != "" {
			app.NewWireIDs = append(app.NewWireIDs, newWire)
		}
		return app, fmt.Errorf("new marker identities incomplete")
	}
	preliminaryWireIDs = append(preliminaryWireIDs, newWire)
	app.NewMarkerID = newMarker
	app.NewWireIDs = preliminaryWireIDs
	return app, nil
}

func executeAdapterPowerOrientation(pinned *appConfig, win, doc string,
	p powerOrientationProposal) (appliedPowerOrientation, error) {
	app := appliedPowerOrientation{Proposal: p}
	if p.Original == nil || p.Host == nil || p.NewAnchor == nil ||
		len(p.NewStubPath) < 2 || p.StubWireID == "" {
		return app, fmt.Errorf("adapter orientation proposal %s lacks complete geometry", p.MarkerID)
	}
	kind := "power"
	if p.Family == "ground" {
		kind = "ground"
	}
	lastA := p.NewStubPath[len(p.NewStubPath)-2]
	lastB := p.NewStubPath[len(p.NewStubPath)-1]
	offset := math.Abs(lastB[0]-lastA[0]) + math.Abs(lastB[1]-lastA[1])
	payload := map[string]any{
		"pinX": p.Host.X, "pinY": p.Host.Y, "kind": kind, "net": p.Net,
		"direction": p.ToDirection, "offset": offset, "rotation": p.ToRotation,
	}
	if len(p.NewStubPath) > 2 {
		corner := p.NewStubPath[len(p.NewStubPath)-2]
		payload["via"] = []float64{corner[0], corner[1]}
	}
	created, err := requestAutolayoutAction(pinned, "schematic.power.connect_pin", win, payload,
		doc, "power-orientation-fix 创建竖排适配支路")
	if err != nil {
		return app, err
	}
	app.NewMarkerID, _ = created.Result["flagPrimitiveId"].(string)
	if wireID, _ := created.Result["wirePrimitiveId"].(string); wireID != "" {
		app.NewWireIDs = append(app.NewWireIDs, wireID)
	}
	app.Changed = app.NewMarkerID != "" || len(app.NewWireIDs) > 0
	if app.NewMarkerID == "" || len(app.NewWireIDs) == 0 {
		return app, fmt.Errorf("adapter marker/wire identities are incomplete")
	}
	if err := verifyVerticalPowerOrientationMarker(pinned, win, doc, p, app.NewMarkerID); err != nil {
		return app, err
	}
	deleted, err := requestAutolayoutAction(pinned, "schematic.primitives.delete", win, map[string]any{
		"primitiveIds": []string{p.MarkerID},
	}, doc, "power-orientation-fix 删除旧横排网络符号")
	if err == nil {
		app.OldMarkerDeleted = markerDeleteProven(deleted.Result)
	}
	if !app.OldMarkerDeleted {
		present, readErr := signalMarkerPresent(pinned, win, doc, p.MarkerID)
		if readErr == nil && !present {
			app.OldMarkerDeleted = true
		}
	}
	if !app.OldMarkerDeleted {
		if err != nil {
			return app, err
		}
		return app, fmt.Errorf("old adapter marker deletion was not proven")
	}
	return app, nil
}

func executeInPlacePowerOrientation(pinned *appConfig, win, doc string,
	p powerOrientationProposal) (appliedPowerOrientation, error) {
	app := appliedPowerOrientation{Proposal: p}
	if p.Original == nil || p.NewAnchor == nil || p.StubWireID == "" {
		return app, fmt.Errorf("in-place orientation proposal %s lacks original/anchor geometry", p.MarkerID)
	}
	kind := "power"
	if p.Family == "ground" {
		kind = "ground"
	}
	created, err := requestAutolayoutAction(pinned, "schematic.netflag.create", win, map[string]any{
		"kind": kind, "net": p.Net, "x": p.NewAnchor.X, "y": p.NewAnchor.Y,
		"rotation": p.ToRotation,
	}, doc, "power-orientation-fix 原位创建竖排旧网络符号")
	if err != nil {
		return app, err
	}
	app.NewMarkerID, _ = created.Result["primitiveId"].(string)
	if app.NewMarkerID == "" {
		return app, fmt.Errorf("in-place vertical marker identity is missing")
	}
	app.Changed = true
	if err := verifyVerticalPowerOrientationMarker(pinned, win, doc, p, app.NewMarkerID); err != nil {
		return app, err
	}
	deleted, err := requestAutolayoutAction(pinned, "schematic.primitives.delete", win, map[string]any{
		"primitiveIds": []string{p.MarkerID},
	}, doc, "power-orientation-fix 删除旧横排网络符号")
	if err == nil {
		app.OldMarkerDeleted = markerDeleteProven(deleted.Result)
	}
	if !app.OldMarkerDeleted {
		present, readErr := signalMarkerPresent(pinned, win, doc, p.MarkerID)
		if readErr == nil && !present {
			app.OldMarkerDeleted = true
		}
	}
	if !app.OldMarkerDeleted {
		if err != nil {
			return app, err
		}
		return app, fmt.Errorf("old horizontal marker deletion was not proven")
	}
	return app, nil
}

func verifyVerticalPowerOrientationMarker(pinned *appConfig, win, doc string,
	p powerOrientationProposal, markerID string) error {
	res, err := requestAutolayoutAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePagePrimitives": true,
	}, doc, "power-orientation-fix 回读原位竖排网络符号")
	if err != nil {
		return err
	}
	comps, err := parseLayoutComps(res.Result)
	if err != nil {
		return err
	}
	for _, c := range comps {
		if c.ID != markerID {
			continue
		}
		if c.ComponentType != "netflag" || c.Net != p.Net || c.Rotation == nil ||
			math.Abs(c.X-p.NewAnchor.X) > 0.5 || math.Abs(c.Y-p.NewAnchor.Y) > 0.5 {
			return fmt.Errorf("new marker state differs from request")
		}
		dir, ok := flagDirectionOf(p.Family, *c.Rotation)
		if !ok || dir != p.ToDirection {
			return fmt.Errorf("new marker body direction is %q, want %q", dir, p.ToDirection)
		}
		return ensurePowerOrientationMarkerNameVisible(pinned, win, doc, markerID)
	}
	return fmt.Errorf("new marker %s is absent from fresh component inventory", markerID)
}

func ensurePowerOrientationMarkerNameVisible(pinned *appConfig, win, doc, markerID string) error {
	scene, err := readLabelFixScene(pinned, win, doc, io.Discard)
	if err != nil {
		return err
	}
	var target *visibleTextItem
	for i := range scene.items {
		item := &scene.items[i]
		if item.ParentID == markerID && item.Kind == "attribute" && item.Key == "Name" {
			target = item
			break
		}
	}
	if target == nil {
		return fmt.Errorf("new marker %s has no native Name attribute", markerID)
	}
	if target.Visible != nil && *target.Visible {
		return nil
	}
	state, err := dcAttributeState(scene.geomResult, target.ID)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"projectUuid": scene.projectID, "documentUuid": doc,
		"primitiveId": target.ID, "expected": state,
		"patch": map[string]any{"valueVisible": true},
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
	fresh, err := readLabelFixScene(pinned, win, doc, io.Discard)
	if err != nil {
		return err
	}
	for _, item := range fresh.items {
		if item.ID == target.ID {
			if item.Visible == nil || !*item.Visible {
				return fmt.Errorf("new marker Name visibility was not proven after modify")
			}
			return nil
		}
	}
	return fmt.Errorf("new marker Name attribute disappeared after modify")
}

func rollbackPowerOrientation(pinned *appConfig, win, doc string, applied []appliedPowerOrientation) error {
	for i := len(applied) - 1; i >= 0; i-- {
		app := applied[i]
		old := app.Proposal.Original
		if old == nil || !app.Changed {
			continue
		}
		kind := "power"
		if app.Proposal.Family == "ground" {
			kind = "ground"
		}
		if app.Proposal.Mode == "in-place-vertical-flag" || app.Proposal.Mode == "adapter-vertical-flag" {
			if app.OldMarkerDeleted {
				restored, err := requestAutolayoutAction(pinned, "schematic.netflag.create", win, map[string]any{
					"kind": kind, "net": old.Net, "x": old.X, "y": old.Y,
					"rotation": app.Proposal.FromRotation,
				}, doc, "power-orientation-fix 回滚原横排网络符号")
				if err != nil {
					return err
				}
				if id, _ := restored.Result["primitiveId"].(string); id == "" {
					return fmt.Errorf("restored original marker has no verified primitive ID")
				}
			}
			artifactIDs := append([]string(nil), app.NewWireIDs...)
			if app.NewMarkerID != "" {
				artifactIDs = append([]string{app.NewMarkerID}, artifactIDs...)
			}
			if len(artifactIDs) > 0 {
				deleted, err := requestAutolayoutAction(pinned, "schematic.primitives.delete", win,
					map[string]any{"primitiveIds": artifactIDs}, doc, "power-orientation-fix 回滚竖排网络符号")
				if err != nil {
					return err
				}
				if err := verifyPowerOrientationArtifactDeletion(deleted, artifactIDs); err != nil {
					return err
				}
			}
			continue
		}
		if app.Proposal.OriginalStub == nil || app.Proposal.Host == nil {
			return fmt.Errorf("rollback proposal %s missing original branch geometry", app.Proposal.MarkerID)
		}
		artifactIDs := append([]string(nil), app.NewWireIDs...)
		if app.NewMarkerID != "" {
			artifactIDs = append([]string{app.NewMarkerID}, artifactIDs...)
		}
		if len(artifactIDs) > 0 {
			deleted, err := requestAutolayoutAction(pinned, "schematic.primitives.delete", win,
				map[string]any{"primitiveIds": artifactIDs}, doc, "power-orientation-fix 回滚新支路")
			if err != nil {
				return err
			}
			if err := verifyPowerOrientationArtifactDeletion(deleted, artifactIDs); err != nil {
				return err
			}
		}
		stub := app.Proposal.OriginalStub
		if _, err := requestAutolayoutAction(pinned, "schematic.power.connect_pin", win, map[string]any{
			"pinX": stub.HostX, "pinY": stub.HostY, "kind": kind, "net": old.Net,
			"direction": stub.Dir, "offset": stub.Offset, "rotation": app.Proposal.FromRotation,
		}, doc, "power-orientation-fix 恢复原支路"); err != nil {
			return err
		}
	}
	return nil
}

func verifyPowerOrientationArtifactDeletion(res *actionResult, wantIDs []string) error {
	if res == nil || res.Result == nil {
		return fmt.Errorf("rollback delete returned no verification result")
	}
	if res.Result["verified"] == false {
		return fmt.Errorf("rollback delete result is unverified")
	}
	if partial, _ := res.Result["partial"].(bool); partial {
		return fmt.Errorf("rollback delete is partial")
	}
	removed := confirmedDeletedIDSet(res.Result)
	if absent, ok := res.Result["notFound"].(map[string]any); ok {
		for id := range survivedIDSet(map[string]any{"survived": absent}) {
			removed[id] = true
		}
	}
	var missing []string
	seen := map[string]bool{}
	for _, id := range wantIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if !removed[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("rollback delete did not prove removal of %s", strings.Join(missing, ","))
	}
	return nil
}

func verifyPowerOrientationBaseline(pinned *appConfig, win, doc string,
	beforeNets map[string]map[string]bool, beforeBridges []string) error {
	afterNets, _, err := readLiveNets(pinned, win)
	if err != nil || !dcNetsEqual(beforeNets, afterNets) {
		return fmt.Errorf("power-orientation-fix: pin netlist differs from baseline (%v)", err)
	}
	afterBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("power-orientation-fix: bridge verification failed: %w", err)
	}
	if added := dcNewBridgeSignatures(beforeBridges, afterBridges); len(added) > 0 {
		return fmt.Errorf("power-orientation-fix: bridge set gained %s", strings.Join(added, ","))
	}
	return nil
}
