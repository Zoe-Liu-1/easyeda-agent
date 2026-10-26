package app

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

type frameRouteScene struct {
	Comps      []layoutComp
	Wires      []schGroupWire
	Items      []visibleTextItem
	Frames     []schFrameSpec
	Sheet      *layoutBBox
	TitleBlock *layoutBBox
	Issues     []frameRouteIssue
}

type frameRouteOutput struct {
	frameRouteReport
	Applied    bool     `json:"applied"`
	Saved      bool     `json:"saved,omitempty"`
	AppliedIDs []string `json:"appliedIds,omitempty"`
	RolledBack bool     `json:"rolledBack,omitempty"`
}

func newSchFrameRouteFixCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var wireIDs, parts, planID, out string
	var apply, save bool
	cmd := &cobra.Command{
		Use:   "frame-route-fix",
		Short: "Plan or apply in-frame rerouting for pins and wire trees crossing their functional frame",
		Long: `Find pin-outside-frame and wire-outside-frame defects from fresh live geometry.

A wire tree owned by exactly one functional frame must remain inside it. Trees that
touch pins owned by multiple frames are real inter-frame connections and are not
flagged. Automatic apply is restricted to:
  - one moved two-pin passive whose outside pin is brought back into the frame,
    followed by an obstacle-aware orthogonal boundary reroute; or
  - one two-pin same-net wire tree whose endpoints are already inside the frame.

Every path is checked on the 5-raw grid inside the frame, respects outward pin
direction, and avoids parts, pins, text, title block and foreign wires. Apply uses
the shared move kernel for moved groups and verifies the complete pin-to-net map
and bridge set. Complex branches, multi-part crossings and unresolved ownership
remain blocked.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			scene, err := readFrameRouteScene(pinned, win, doc)
			if err != nil {
				return err
			}
			issues, err := filterFrameRouteIssues(scene.Issues, wireIDs, parts, planID)
			if err != nil {
				return err
			}
			if len(issues) == 0 {
				return fmt.Errorf("frame-route-fix: no matching frame-routing issue")
			}
			report := frameRouteOutput{frameRouteReport: frameRouteReport{
				SchemaVersion: 1, DocumentID: doc, Issues: issues,
			}}
			for _, issue := range issues {
				if issue.Status == "ready" {
					report.Ready++
				} else {
					report.Blocked++
				}
			}
			if !apply {
				if err := writeFrameRouteOutput(stdout, out, report); err != nil {
					return err
				}
				fmt.Fprintln(stderr, "frame-route-fix: dry-run only; pass --apply to write")
				return nil
			}
			if report.Ready != 1 || report.Blocked != 0 {
				if err := writeFrameRouteOutput(stdout, out, report); err != nil {
					return err
				}
				return fmt.Errorf("frame-route-fix: select exactly one blocker-free ready issue (ready=%d blocked=%d)",
					report.Ready, report.Blocked)
			}
			issue := issues[0]
			if err := executeFrameRouteIssue(pinned, win, doc, issue, save, stdout, stderr); err != nil {
				return err
			}
			report.Applied = true
			report.Saved = save
			report.AppliedIDs = append(report.AppliedIDs, issue.ID)
			return writeFrameRouteOutput(stdout, out, report)
		},
	}
	cmd.Flags().StringVar(&wireIDs, "wire", "", "optional comma-separated offending wire primitive IDs")
	cmd.Flags().StringVar(&parts, "part", "", "optional comma-separated designators with pins outside their frame")
	cmd.Flags().StringVar(&planID, "issue", "", "optional frame-route issue id, for example frame-route-001")
	cmd.Flags().BoolVar(&apply, "apply", false, "execute exactly one ready issue")
	cmd.Flags().BoolVar(&save, "save", false, "save after fresh net and bridge verification")
	cmd.Flags().StringVar(&out, "out", "", "write the structured plan/apply report to JSON")
	return cmd
}

func readFrameRouteScene(pinned *appConfig, win, doc string) (frameRouteScene, error) {
	geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePins": true, "includePinNets": true, "includePagePrimitives": true,
	})
	if err != nil {
		return frameRouteScene{}, err
	}
	if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
		return frameRouteScene{}, fmt.Errorf("frame-route-fix: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
	}
	comps, err := parseLayoutComps(geom.Result)
	if err != nil {
		return frameRouteScene{}, err
	}
	wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
	if err != nil {
		return frameRouteScene{}, err
	}
	vt, err := requestAction(pinned, "schematic.visible_text.bboxes", win, nil)
	if err != nil {
		return frameRouteScene{}, err
	}
	var vision alignmentVision
	raw, _ := json.Marshal(vt.Result)
	if err := json.Unmarshal(raw, &vision); err != nil {
		return frameRouteScene{}, fmt.Errorf("frame-route-fix: parse visible text: %w", err)
	}
	surveyValue, err := fetchSchVisualSurvey(pinned, win, comps)
	if err != nil {
		return frameRouteScene{}, err
	}
	survey, err := parseSchFrameSurvey(surveyValue)
	if err != nil {
		return frameRouteScene{}, err
	}
	frames := schModuleFrames(survey)
	if len(frames) == 0 {
		return frameRouteScene{}, fmt.Errorf("frame-route-fix: no live functional frames")
	}
	sheet := sheetBBoxOf(comps)
	titleBlock, _ := titleBlockKeepoutWithSource(sheet)
	return frameRouteScene{
		Comps: comps, Wires: wires, Items: vision.Items, Frames: frames,
		Sheet: sheet, TitleBlock: titleBlock,
		Issues: frameRoutePlans(comps, wires, vision.Items, frames, sheet, titleBlock),
	}, nil
}

func filterFrameRouteIssues(in []frameRouteIssue, wireCSV, partCSV, planID string) ([]frameRouteIssue, error) {
	wires := parsePrimitiveIDSet(wireCSV)
	parts := map[string]bool{}
	for _, p := range strings.Split(partCSV, ",") {
		if p = strings.TrimSpace(p); p != "" {
			parts[strings.ToUpper(p)] = true
		}
	}
	var out []frameRouteIssue
	for _, issue := range in {
		if planID != "" && issue.ID != planID {
			continue
		}
		if len(wires) > 0 {
			hit := false
			for _, id := range issue.DeleteWireIDs {
				if wires[id] {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		if len(parts) > 0 {
			hit := false
			for _, d := range issue.MoveDesignators {
				if parts[strings.ToUpper(d)] {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		out = append(out, issue)
	}
	if planID != "" && len(out) == 0 {
		return nil, fmt.Errorf("frame-route-fix: issue %s not found", planID)
	}
	return out, nil
}

func writeFrameRouteOutput(stdout io.Writer, out string, report frameRouteOutput) error {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if out != "" {
		return os.WriteFile(out, raw, 0644)
	}
	_, err = stdout.Write(raw)
	return err
}

func executeFrameRouteIssue(pinned *appConfig, win, doc string, issue frameRouteIssue,
	save bool, stdout, stderr io.Writer) error {
	if issue.Status != "ready" {
		return fmt.Errorf("frame-route-fix: issue %s is %s: %s", issue.ID, issue.Status, strings.Join(issue.Blockers, ","))
	}
	if len(issue.MoveDesignators) > 0 {
		return executeFrameRouteMove(pinned, win, doc, issue, save, stdout, stderr)
	}
	return executeFrameRouteReroute(pinned, win, doc, issue, save, stdout, stderr)
}

func executeFrameRouteMove(pinned *appConfig, win, doc string, issue frameRouteIssue,
	save bool, stdout, stderr io.Writer) error {
	if len(issue.MoveDesignators) != 1 || len(issue.DeleteWireIDs) == 0 || len(issue.BoundaryWires) == 0 {
		return fmt.Errorf("frame-route-fix: move issue lacks a complete single-part boundary plan")
	}
	var target *layoutComp
	comps, _, _, _, _, err := frameRouteApplyScene(pinned, win, doc)
	if err != nil {
		return err
	}
	for i := range comps {
		if comps[i].Designator == issue.MoveDesignators[0] {
			target = &comps[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("frame-route-fix: moving part %s is missing", issue.MoveDesignators[0])
	}
	boundary := &moveKernelBoundary{
		SharedWireIDs: append([]string(nil), issue.DeleteWireIDs...),
	}
	for _, ref := range issue.PinRefs {
		boundary.PinRefs = append(boundary.PinRefs, strings.Replace(ref, ".", ":", 1))
	}
	for _, wire := range issue.BoundaryWires {
		boundary.Wires = append(boundary.Wires, moveKernelBoundaryWire{
			Net: wire.Net, Points: append([]float64(nil), wire.Points...),
		})
	}
	items := []moveItem{{
		Designator: target.Designator, HasTarget: true,
		X: target.X + issue.ShiftX, Y: target.Y + issue.ShiftY,
	}}
	rep, err := schMoveKernel(pinned, win, doc, items, moveKernelOpts{
		Label: "frame-route-fix", Stdout: stdout, Stderr: stderr, Boundary: boundary,
		StubPolicy: moveStubFree,
	})
	if err != nil {
		return err
	}
	if len(rep.StillBroken) > 0 || len(rep.NetDiffs) > 0 || len(rep.NewBridges) > 0 {
		return fmt.Errorf("frame-route-fix: post-move reconciliation is not clean (stillBroken=%v netDiffs=%v bridges=%v)",
			rep.StillBroken, rep.NetDiffs, rep.NewBridges)
	}
	if err := verifyFrameRouteApplied(pinned, win, doc, issue); err != nil {
		return err
	}
	return saveFrameRoute(pinned, win, doc, save, stdout)
}

func executeFrameRouteReroute(pinned *appConfig, win, doc string, issue frameRouteIssue,
	save bool, stdout, stderr io.Writer) error {
	if len(issue.DeleteWireIDs) == 0 || len(issue.BoundaryWires) == 0 || issue.Net == "" {
		return fmt.Errorf("frame-route-fix: reroute issue is incomplete")
	}
	if stringInSlice("wire-outside-frame", issue.Blockers) {
		return fmt.Errorf("frame-route-fix: blocked issue cannot be applied")
	}
	beforeNets, _, err := readLiveNets(pinned, win)
	if err != nil {
		return fmt.Errorf("frame-route-fix: baseline netlist: %w", err)
	}
	beforeBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("frame-route-fix: baseline bridge set: %w", err)
	}
	del, err := requestAutolayoutAction(pinned, "schematic.primitives.delete", win,
		map[string]any{"primitiveIds": issue.DeleteWireIDs}, doc, "frame-route-fix 删除越框线树")
	if err != nil {
		return err
	}
	if nf := countDeleteNotFound(del.Result["notFound"]); nf > 0 {
		return fmt.Errorf("frame-route-fix: %d old wire(s) were not found", nf)
	}
	paths := append([][]float64(nil), issue.BoundaryPaths...)
	if len(paths) == 0 {
		if path := frameRoutePathForNet(issue.BoundaryWires, issue.Net); len(path) >= 4 {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return fmt.Errorf("frame-route-fix: planned boundary polyline is incomplete")
	}
	var newIDs []string
	for i, points := range paths {
		wireNet := issue.Net
		if i > 0 {
			// The first path carries the visible/testable net name. Additional
			// branches meet it at the same hub; repeating the Name on every branch
			// would create duplicate labels for one physical island.
			wireNet = ""
		}
		created, err := requestAutolayoutAction(pinned, "schematic.wire.create", win,
			map[string]any{"points": points, "net": wireNet}, doc, "frame-route-fix 创建框内导线")
		if err != nil {
			return rollbackFrameRouteReroute(pinned, win, doc, issue, newIDs, err)
		}
		newID, _ := created.Result["primitiveId"].(string)
		if newID == "" {
			return rollbackFrameRouteReroute(pinned, win, doc, issue, newIDs,
				fmt.Errorf("wire create returned no primitiveId"))
		}
		newIDs = append(newIDs, newID)
	}
	if err := verifyFrameRouteNetBaseline(pinned, win, doc, beforeNets, beforeBridges); err != nil {
		return rollbackFrameRouteReroute(pinned, win, doc, issue, newIDs, err)
	}
	if err := verifyFrameRouteApplied(pinned, win, doc, issue); err != nil {
		return rollbackFrameRouteReroute(pinned, win, doc, issue, newIDs, err)
	}
	fmt.Fprintf(stdout, "✓ frame-route-fix rerouted %s in %s (%d segments)\n",
		frameRouteDisplayNet(issue.Net), issue.Frame.ID, len(issue.BoundaryWires))
	return saveFrameRoute(pinned, win, doc, save, stdout)
}

func rollbackFrameRouteReroute(pinned *appConfig, win, doc string, issue frameRouteIssue,
	newIDs []string, cause error) error {
	if len(newIDs) > 0 {
		if _, err := requestAutolayoutAction(pinned, "schematic.primitives.delete", win,
			map[string]any{"primitiveIds": newIDs}, doc, "frame-route-fix 回滚新导线"); err != nil {
			return fmt.Errorf("%v; rollback new wire failed: %w", cause, err)
		}
	}
	for _, old := range issue.OriginalWireStates {
		net := old.Net
		if strings.TrimSpace(net) == "" {
			net = issue.Net
		}
		if _, err := requestAutolayoutAction(pinned, "schematic.wire.create", win,
			map[string]any{"points": old.Points, "net": net}, doc, "frame-route-fix 恢复旧导线"); err != nil {
			return fmt.Errorf("%v; restore old wire %s failed: %w", cause, old.ID, err)
		}
	}
	return fmt.Errorf("%v; frame-routing change was rolled back", cause)
}

func verifyFrameRouteNetBaseline(pinned *appConfig, win, doc string,
	beforeNets map[string]map[string]bool, beforeBridges []string) error {
	afterNets, _, err := readLiveNets(pinned, win)
	if err != nil || !dcNetsEqual(beforeNets, afterNets) {
		return fmt.Errorf("frame-route-fix: pin netlist differs from baseline (%v)", err)
	}
	afterBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("frame-route-fix: bridge verification failed: %w", err)
	}
	if added := dcNewBridgeSignatures(beforeBridges, afterBridges); len(added) > 0 {
		return fmt.Errorf("frame-route-fix: bridge set gained %s", strings.Join(added, ","))
	}
	return nil
}

func verifyFrameRouteApplied(pinned *appConfig, win, doc string, applied frameRouteIssue) error {
	scene, err := readFrameRouteScene(pinned, win, doc)
	if err != nil {
		return err
	}
	findings := frameRouteFindings(scene.Comps, scene.Wires, scene.Frames, frameRouteTolerance)
	for _, f := range findings {
		if f.Type == "pin-outside-frame" && applied.Frame != nil && f.Designator != "" {
			for _, d := range applied.MoveDesignators {
				if f.Designator == d {
					return fmt.Errorf("frame-route-fix: %s still has an external pin: %s", d, f.Message)
				}
			}
		}
		if f.Type == "wire-outside-frame" && f.WireNet == applied.Net {
			return fmt.Errorf("frame-route-fix: net %s still has a wire outside its frame: %s", applied.Net, f.Message)
		}
	}
	return nil
}

func saveFrameRoute(pinned *appConfig, win, doc string, save bool, stdout io.Writer) error {
	if !save {
		return nil
	}
	res, err := requestAutolayoutAction(pinned, "schematic.save", win, nil, doc, "frame-route-fix 保存")
	if err != nil {
		return err
	}
	ok, _ := res.Result["saved"].(bool)
	if !ok {
		return fmt.Errorf("frame-route-fix: schematic.save did not return saved:true")
	}
	fmt.Fprintln(stdout, "✓ frame-route-fix saved")
	return nil
}

func frameRoutePathForNet(wires []frameRouteWire, net string) []float64 {
	var segs [][4]float64
	for _, w := range wires {
		if w.Net == net && len(w.Points) == 4 {
			segs = append(segs, [4]float64{w.Points[0], w.Points[1], w.Points[2], w.Points[3]})
		}
	}
	if len(segs) == 0 {
		return nil
	}
	remaining := append([][4]float64(nil), segs...)
	first := remaining[0]
	remaining = remaining[1:]
	points := []float64{first[0], first[1], first[2], first[3]}
	cur := [2]float64{first[2], first[3]}
	for len(remaining) > 0 {
		found := false
		for i, s := range remaining {
			if pointsClose(cur[0], cur[1], s[0], s[1], frameRouteTolerance) {
				points = append(points, s[2], s[3])
				cur = [2]float64{s[2], s[3]}
				remaining = append(remaining[:i], remaining[i+1:]...)
				found = true
				break
			}
			if pointsClose(cur[0], cur[1], s[2], s[3], frameRouteTolerance) {
				points = append(points, s[0], s[1])
				cur = [2]float64{s[0], s[1]}
				remaining = append(remaining[:i], remaining[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return cleanRoutePoints(points)
}

func cleanRoutePoints(points []float64) []float64 {
	if len(points) < 4 {
		return points
	}
	out := []float64{points[0], points[1]}
	for i := 2; i+1 < len(points); i += 2 {
		x, y := points[i], points[i+1]
		if math.Abs(x-out[len(out)-2]) <= frameRouteTolerance &&
			math.Abs(y-out[len(out)-1]) <= frameRouteTolerance {
			continue
		}
		if len(out) >= 4 {
			ax, ay := out[len(out)-4], out[len(out)-3]
			bx, by := out[len(out)-2], out[len(out)-1]
			if (math.Abs(ax-bx) <= frameRouteTolerance && math.Abs(bx-x) <= frameRouteTolerance) ||
				(math.Abs(ay-by) <= frameRouteTolerance && math.Abs(by-y) <= frameRouteTolerance) {
				out[len(out)-2], out[len(out)-1] = x, y
				continue
			}
		}
		out = append(out, x, y)
	}
	return out
}

func frameRouteApplyScene(pinned *appConfig, win, doc string) ([]layoutComp, []schGroupWire, []visibleTextItem, []schFrameSpec, *layoutBBox, error) {
	scene, err := readFrameRouteScene(pinned, win, doc)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	return scene.Comps, scene.Wires, scene.Items, scene.Frames, scene.Sheet, nil
}
