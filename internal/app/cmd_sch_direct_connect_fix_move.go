package app

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

func executeDCFix(pinned *appConfig, win, doc string, scene dcFixScene, report *directConnectFixReport,
	maxDistance, maxMove float64, allowMove, save bool, stdout, stderr io.Writer) error {
	beforeNets, _, err := readLiveNets(pinned, win)
	if err != nil {
		return fmt.Errorf("direct-connect-fix: baseline netlist: %w", err)
	}
	beforeBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("direct-connect-fix: baseline bridge set: %w", err)
	}

	var applied *dcAppliedMove
	rollback := func(cause error) error {
		if applied == nil || !applied.Applied {
			return cause
		}
		rbErr := rollbackDCFixMove(pinned, win, doc, scene, report.Initial, *applied)
		applied.RolledBack = rbErr == nil
		if rbErr != nil {
			applied.RollbackError = rbErr.Error()
			return fmt.Errorf("%v; move rollback FAILED (%v) — stop and inspect fresh page state before retry", cause, rbErr)
		}
		return fmt.Errorf("%v; the move was rolled back", cause)
	}

	finalScene := scene
	finalPair := report.Initial
	if report.InitialStatus != "already-clear" {
		if !allowMove {
			return fmt.Errorf("direct-connect-fix: selected plan is %s; pass --allow-move to execute it", report.InitialStatus)
		}
		move, moveErr := executeDCFixMove(pinned, win, doc, scene, report.Initial)
		applied = &move
		report.Move = applied
		if moveErr != nil {
			if move.Applied {
				return rollback(fmt.Errorf("execute %s: %w", report.InitialStatus, moveErr))
			}
			return fmt.Errorf("direct-connect-fix: execute %s: %w", report.InitialStatus, moveErr)
		}
		finalScene, err = readDCFixScene(pinned, win, doc, maxDistance, maxMove)
		if err != nil {
			return rollback(fmt.Errorf("post-move scene read: %w", err))
		}
		afterMoveNets, _, nerr := readLiveNets(pinned, win)
		if nerr != nil || !dcNetsEqual(beforeNets, afterMoveNets) {
			return rollback(fmt.Errorf("move changed the complete pin netlist (%v)", nerr))
		}
		afterMoveBridges, berr := dcBridgeSignatures(pinned, win, doc)
		if berr != nil || len(dcNewBridgeSignatures(beforeBridges, afterMoveBridges)) > 0 {
			return rollback(fmt.Errorf("move introduced a bridge or bridge verification failed (%v)", berr))
		}
		finalPair, err = resolveDCFixFinalPair(finalScene, report.Initial)
		if err != nil {
			return rollback(err)
		}
		report.Notes = append(report.Notes,
			fmt.Sprintf("post-move replan: %s %s↔%s is already-clear", finalPair.Net, finalPair.LabelA, finalPair.LabelB))
	}
	report.Final = &finalPair
	report.FinalStatus = finalPair.Status
	report.Path = finalPair.PathPoints

	applyReport := directConnectApplyReport{
		SchemaVersion: 1, DocumentID: doc, Net: finalPair.Net,
		LabelA: finalPair.LabelA, LabelB: finalPair.LabelB, Path: finalPair.PathPoints,
	}
	if err := executeDirectConnectPair(pinned, win, doc, applyReport, finalScene.anchors, false, io.Discard, io.Discard); err != nil {
		if applied != nil {
			return rollback(fmt.Errorf("connect already-clear pair: %w", err))
		}
		return err
	}

	afterNets, _, err := readLiveNets(pinned, win)
	if err != nil || !dcNetsEqual(beforeNets, afterNets) {
		return fmt.Errorf("direct-connect-fix: final pin netlist differs from the pre-command baseline (%v)", err)
	}
	afterBridges, err := dcBridgeSignatures(pinned, win, doc)
	if err != nil {
		return fmt.Errorf("direct-connect-fix: final bridge check: %w", err)
	}
	if added := dcNewBridgeSignatures(beforeBridges, afterBridges); len(added) > 0 {
		return fmt.Errorf("direct-connect-fix: final bridge check added %s", strings.Join(added, ", "))
	}
	if save {
		saved, serr := requestAutolayoutAction(pinned, "schematic.save", win, nil, doc, "direct-connect-fix 保存")
		if serr != nil {
			return fmt.Errorf("direct-connect-fix: save: %w", serr)
		}
		ok, _ := saved.Result["saved"].(bool)
		if !ok {
			return fmt.Errorf("direct-connect-fix: schematic.save did not return saved:true")
		}
		report.Saved = true
	}
	report.Applied = true
	fmt.Fprintf(stdout, "✓ direct-connect-fix %s %s↔%s complete (%d-segment wire)\n",
		report.Net, report.LabelA, report.LabelB, max(0, len(report.Path)-1))
	return nil
}

func executeDCFixMove(pinned *appConfig, win, doc string, scene dcFixScene, pair dcPairPlan) (dcAppliedMove, error) {
	switch pair.Status {
	case "move-proposed":
		if pair.Move == nil {
			return dcAppliedMove{}, fmt.Errorf("text-move proposal missing payload")
		}
		return dcAppliedMove{Kind: "text"}, executeDCFixTextMove(pinned, win, scene, *pair.Move)
	case "marker-move-proposed":
		if pair.MarkerMove == nil {
			return dcAppliedMove{}, fmt.Errorf("marker-move proposal missing payload")
		}
		return executeDCFixMarkerMove(pinned, win, doc, scene, *pair.MarkerMove)
	case "part-move-proposed":
		if pair.PartMove == nil {
			return dcAppliedMove{}, fmt.Errorf("part-move proposal missing payload")
		}
		return executeDCFixPartMove(pinned, win, doc, scene, *pair.PartMove)
	case "group-move-proposed":
		if pair.GroupMove == nil {
			return dcAppliedMove{}, fmt.Errorf("group-move proposal missing payload")
		}
		return executeDCFixGroupMove(pinned, win, doc, scene, *pair.GroupMove)
	default:
		return dcAppliedMove{}, fmt.Errorf("status %s has no move executor", pair.Status)
	}
}

func executeDCFixTextMove(pinned *appConfig, win string, scene dcFixScene, mv dcTextMove) error {
	state, err := dcAttributeState(scene.geomResult, mv.AttributeID)
	if err != nil {
		return err
	}
	x, y := state["X"], state["Y"]
	x0, okx := x.(float64)
	y0, oky := y.(float64)
	if !okx || !oky {
		return fmt.Errorf("attribute %s has no finite current anchor", mv.AttributeID)
	}
	tx, ty := x0+mv.DeltaX, y0+mv.DeltaY
	if mv.ToAnchor != nil {
		tx, ty = mv.ToAnchor.X, mv.ToAnchor.Y
	}
	payload := map[string]any{
		"projectUuid":  scene.projectID,
		"documentUuid": scene.documentID,
		"primitiveId":  mv.AttributeID,
		"expected":     state,
		"x":            tx,
		"y":            ty,
	}
	if err := validateSchAttributeMovePayload(payload); err != nil {
		return err
	}
	res, err := requestAction(pinned, "schematic.attribute.move", win, payload)
	if err != nil {
		return err
	}
	return schAttributeMoveResultError("schematic.attribute.move", res.Result)
}

func dcAttributeState(result map[string]any, id string) (map[string]any, error) {
	pp, _ := result["pagePrimitives"].(map[string]any)
	raws, _ := pp["attributes"].([]any)
	for _, raw := range raws {
		m, _ := raw.(map[string]any)
		if strField(m, "primitiveId") != id {
			continue
		}
		state := map[string]any{}
		for _, key := range schAttributeStateFields {
			value, ok := m[key]
			if !ok {
				return nil, fmt.Errorf("attribute %s native state is missing %s", id, key)
			}
			state[key] = value
		}
		if err := validateSchAttributeState(state); err != nil {
			return nil, err
		}
		return state, nil
	}
	return nil, fmt.Errorf("attribute %s not found in fresh page primitives", id)
}

func executeDCFixMarkerMove(pinned *appConfig, win, doc string, scene dcFixScene, mv dcMarkerMove) (dcAppliedMove, error) {
	var marker layoutComp
	found := false
	for _, c := range scene.comps {
		if c.ID == mv.MarkerID {
			marker, found = c, true
			break
		}
	}
	if !found {
		return dcAppliedMove{}, fmt.Errorf("marker %s missing from fresh scene", mv.MarkerID)
	}
	if mv.Host == nil || mv.ToAnchor == nil || len(mv.NewStubPath) < 2 {
		return dcAppliedMove{}, fmt.Errorf("marker move lacks host/path geometry")
	}
	kind, err := dcMarkerConnectKind(marker)
	if err != nil {
		return dcAppliedMove{}, err
	}
	if marker.Rotation == nil {
		return dcAppliedMove{}, fmt.Errorf("marker %s rotation unavailable; cannot preserve orientation", marker.ID)
	}
	stub, reason := stubOfMarker(marker, scene.wires)
	if stub == nil || reason != "" || stub.WireID != mv.StubWireID {
		return dcAppliedMove{}, fmt.Errorf("marker %s stub changed: got wire=%v reason=%s", marker.ID, stubWireID(stub), reason)
	}
	hostX, hostY := mv.Host.X, mv.Host.Y
	if math.Abs(stub.HostX-hostX) > 0.5 || math.Abs(stub.HostY-hostY) > 0.5 {
		return dcAppliedMove{}, fmt.Errorf("marker %s host moved since planning", marker.ID)
	}

	dir, off, via, err := dcMarkerPathTerm(mv.NewStubPath, hostX, hostY, mv.ToAnchor.X, mv.ToAnchor.Y)
	if err != nil {
		return dcAppliedMove{}, err
	}
	disconnected, err := requestAutolayoutAction(pinned, "schematic.pin.disconnect", win, map[string]any{
		"wirePrimitiveId": mv.StubWireID, "pinX": hostX, "pinY": hostY,
	}, doc, "direct-connect-fix 拆除 marker 旧桩")
	if err != nil {
		return dcAppliedMove{}, err
	}
	if disconnected.Result["disconnected"] != true || disconnected.Result["partial"] == true {
		return dcAppliedMove{}, fmt.Errorf("marker stub disconnect not proven: disconnected=%v partial=%v", disconnected.Result["disconnected"], disconnected.Result["partial"])
	}
	if also, _ := disconnected.Result["alsoDisconnectedPins"].([]any); len(also) > 0 {
		return dcAppliedMove{}, fmt.Errorf("marker stub shared another pin: %v", also)
	}
	payload := map[string]any{
		"pinX": hostX, "pinY": hostY, "kind": kind, "net": marker.Net,
		"direction": dir, "offset": off, "rotation": *marker.Rotation,
	}
	if via != nil {
		payload["via"] = []float64{via[0], via[1]}
	}
	created, err := requestAutolayoutAction(pinned, "schematic.power.connect_pin", win, payload, doc, "direct-connect-fix 重建 marker")
	if err != nil {
		return dcAppliedMove{Kind: "marker", Applied: true}, err
	}
	newFlag, _ := created.Result["flagPrimitiveId"].(string)
	newWire, _ := created.Result["wirePrimitiveId"].(string)
	if newFlag == "" || newWire == "" {
		return dcAppliedMove{Kind: "marker", Applied: true}, fmt.Errorf("marker reconnect returned incomplete identities")
	}
	return dcAppliedMove{Kind: "marker", NewMarkerID: newFlag, NewWireID: newWire, Applied: true}, nil
}

func stubWireID(stub *destaggerStub) string {
	if stub == nil {
		return ""
	}
	return stub.WireID
}

func dcMarkerConnectKind(c layoutComp) (string, error) {
	switch c.ComponentType {
	case "netport":
		return "net_port_bi", nil
	case "netflag":
		switch tidyNetClass(c.Net) {
		case "ground":
			return "ground", nil
		case "power":
			return "power", nil
		default:
			return "", fmt.Errorf("signal netflag %s is not safe for marker-move recreation; use a net_label conversion plan", c.ID)
		}
	default:
		return "", fmt.Errorf("marker type %s is not supported by direct-connect marker move", c.ComponentType)
	}
}

func dcMarkerPathTerm(path [][2]float64, hostX, hostY, markerX, markerY float64) (dir string, offset float64, via *[2]float64, err error) {
	if len(path) < 2 || len(path) > 3 {
		return "", 0, nil, fmt.Errorf("marker stub path must have 2 or 3 points, got %d", len(path))
	}
	if !pointEquals2(path[0][0], path[0][1], [2]float64{hostX, hostY}) ||
		!pointEquals2(path[len(path)-1][0], path[len(path)-1][1], [2]float64{markerX, markerY}) {
		return "", 0, nil, fmt.Errorf("marker stub path endpoints do not match planned host/marker")
	}
	for i := 0; i+1 < len(path); i++ {
		dx, dy := path[i+1][0]-path[i][0], path[i+1][1]-path[i][1]
		if math.Abs(dx) > 0.5 && math.Abs(dy) > 0.5 {
			return "", 0, nil, fmt.Errorf("marker stub path contains a diagonal segment")
		}
	}
	a, b := path[len(path)-2], path[len(path)-1]
	dx, dy := b[0]-a[0], b[1]-a[1]
	offset = math.Abs(dx) + math.Abs(dy)
	if offset <= 0 {
		return "", 0, nil, fmt.Errorf("zero-length marker stub")
	}
	switch {
	case math.Abs(dx) < 0.5 && dy > 0:
		dir = "up"
	case math.Abs(dx) < 0.5 && dy < 0:
		dir = "down"
	case math.Abs(dy) < 0.5 && dx > 0:
		dir = "right"
	case math.Abs(dy) < 0.5 && dx < 0:
		dir = "left"
	default:
		return "", 0, nil, fmt.Errorf("marker stub final segment has no direction")
	}
	if len(path) == 3 {
		v := path[1]
		via = &v
	}
	return dir, offset, via, nil
}

func executeDCFixPartMove(pinned *appConfig, win, doc string, scene dcFixScene, mv dcPartMove) (dcAppliedMove, error) {
	partIDs := []string{mv.PartID}
	designators := []string{mv.Designator}
	if err := verifyDCDirectExpansion(scene, partIDs, mv.WireIDs, mv.FlagIDs); err != nil {
		return dcAppliedMove{}, err
	}
	rep, err := runDCDirectKernelMove(pinned, win, doc, scene, partIDs, designators, mv.DeltaX, mv.DeltaY)
	if err != nil {
		return dcAppliedMove{Kind: "part", Applied: rep != nil && len(rep.Moved) > 0}, err
	}
	return dcAppliedMove{Kind: "part", Applied: true}, nil
}

func executeDCFixGroupMove(pinned *appConfig, win, doc string, scene dcFixScene, mv dcGroupMove) (dcAppliedMove, error) {
	if err := verifyDCDirectExpansion(scene, mv.PartIDs, mv.WireIDs, mv.FlagIDs); err != nil {
		return dcAppliedMove{}, err
	}
	rep, err := runDCDirectKernelMove(pinned, win, doc, scene, mv.PartIDs, mv.Designators, mv.DeltaX, mv.DeltaY)
	if err != nil {
		return dcAppliedMove{Kind: "group", Applied: rep != nil && len(rep.Moved) > 0}, err
	}
	return dcAppliedMove{Kind: "group", Applied: true}, nil
}

func verifyDCDirectExpansion(scene dcFixScene, partIDs, wantWires, wantFlags []string) error {
	moving := map[string]bool{}
	for _, id := range partIDs {
		moving[id] = true
	}
	var memberPins, otherPins [][2]float64
	for _, c := range scene.comps {
		if c.ComponentType != "" && c.ComponentType != schLayoutPartType {
			continue
		}
		for _, p := range c.Pins {
			if moving[c.ID] {
				memberPins = append(memberPins, [2]float64{p.X, p.Y})
			} else {
				otherPins = append(otherPins, [2]float64{p.X, p.Y})
			}
		}
	}
	var flags []schGroupFlag
	for _, c := range scene.comps {
		if isSchMarker(c.ComponentType) {
			flags = append(flags, schGroupFlag{ID: c.ID, X: c.X, Y: c.Y})
		}
	}
	exp := expandGroupAttachments(groupExpandInput{
		MemberPins: memberPins, OtherPins: otherPins, Wires: scene.wires, Flags: flags,
	})
	if len(exp.Suspects) > 0 || exp.SharedTrees > 0 {
		return fmt.Errorf("direct-connect-fix: planned rigid set changed into shared/suspect tree")
	}
	if !sortedSameStrings(exp.WireIDs, wantWires) || !sortedSameStrings(exp.FlagIDs, wantFlags) {
		return fmt.Errorf("direct-connect-fix: planned rigid set drifted (wires %v vs %v, flags %v vs %v)",
			exp.WireIDs, wantWires, exp.FlagIDs, wantFlags)
	}
	return nil
}

func runDCDirectKernelMove(pinned *appConfig, win, doc string, scene dcFixScene,
	partIDs, designators []string, dx, dy float64) (*moveReport, error) {
	byID := map[string]layoutComp{}
	for _, c := range scene.comps {
		byID[c.ID] = c
	}
	var items []moveItem
	for i, id := range partIDs {
		c, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("direct-connect-fix: moving part %s missing", id)
		}
		desig := c.Designator
		if desig == "" && i < len(designators) {
			desig = designators[i]
		}
		items = append(items, moveItem{Designator: desig, HasTarget: true, X: c.X + dx, Y: c.Y + dy})
	}
	return schMoveKernel(pinned, win, doc, items, moveKernelOpts{
		Label: "direct-connect-fix", StubPolicy: moveStubPreserve,
		Stdout: io.Discard, Stderr: io.Discard,
	})
}

func rollbackDCFixMove(pinned *appConfig, win, doc string, original dcFixScene,
	pair dcPairPlan, applied dcAppliedMove) error {
	switch applied.Kind {
	case "text":
		if pair.Move == nil {
			return fmt.Errorf("text rollback lost original payload")
		}
		current, err := readDCFixScene(pinned, win, doc, 250, 40)
		if err != nil {
			return err
		}
		anchor, ok := original.anchors[pair.Move.AttributeID]
		if !ok {
			return fmt.Errorf("text rollback anchor missing")
		}
		return executeDCFixTextMove(pinned, win, current, dcTextMove{
			AttributeID: pair.Move.AttributeID,
			ToAnchor:    &anchor,
		})
	case "marker":
		return rollbackDCFixMarker(pinned, win, doc, original, pair.MarkerMove, applied)
	case "part":
		if pair.PartMove == nil {
			return fmt.Errorf("part rollback lost original payload")
		}
		current, err := readDCFixScene(pinned, win, doc, 250, 40)
		if err != nil {
			return err
		}
		_, err = runDCDirectKernelMove(pinned, win, doc, current,
			[]string{pair.PartMove.PartID}, []string{pair.PartMove.Designator},
			-pair.PartMove.DeltaX, -pair.PartMove.DeltaY)
		return err
	case "group":
		if pair.GroupMove == nil {
			return fmt.Errorf("group rollback lost original payload")
		}
		current, err := readDCFixScene(pinned, win, doc, 250, 40)
		if err != nil {
			return err
		}
		_, err = runDCDirectKernelMove(pinned, win, doc, current,
			pair.GroupMove.PartIDs, pair.GroupMove.Designators,
			-pair.GroupMove.DeltaX, -pair.GroupMove.DeltaY)
		return err
	default:
		return nil
	}
}

func rollbackDCFixMarker(pinned *appConfig, win, doc string, original dcFixScene,
	mv *dcMarkerMove, applied dcAppliedMove) error {
	if mv == nil || mv.Host == nil || mv.ToAnchor == nil {
		return fmt.Errorf("marker rollback lost original payload")
	}
	var marker layoutComp
	for _, c := range original.comps {
		if c.ID == mv.MarkerID {
			marker = c
			break
		}
	}
	if marker.ID == "" || marker.Rotation == nil {
		return fmt.Errorf("original marker missing")
	}
	kind, err := dcMarkerConnectKind(marker)
	if err != nil {
		return err
	}
	wireID := applied.NewWireID
	if wireID == "" {
		current, rerr := readDCFixScene(pinned, win, doc, 250, 40)
		if rerr != nil {
			return rerr
		}
		for _, c := range current.comps {
			if c.ID == applied.NewMarkerID {
				if stub, reason := stubOfMarker(c, current.wires); stub != nil && reason == "" {
					wireID = stub.WireID
				}
				break
			}
		}
	}
	if wireID != "" {
		disc, err := requestAutolayoutAction(pinned, "schematic.pin.disconnect", win, map[string]any{
			"wirePrimitiveId": wireID, "pinX": mv.Host.X, "pinY": mv.Host.Y,
		}, doc, "direct-connect-fix 回滚 marker")
		if err != nil {
			return err
		}
		if disc.Result["disconnected"] != true || disc.Result["partial"] == true {
			return fmt.Errorf("moved marker disconnect not proven during rollback")
		}
	}
	dx, dy := marker.X-mv.Host.X, marker.Y-mv.Host.Y
	dir, off := tidyStubDirection(mv.Host.X, mv.Host.Y, marker.X, marker.Y)
	if dir == "" || off <= 0 {
		return fmt.Errorf("original marker direction invalid: dx=%g dy=%g", dx, dy)
	}
	_, err = requestAutolayoutAction(pinned, "schematic.power.connect_pin", win, map[string]any{
		"pinX": mv.Host.X, "pinY": mv.Host.Y, "kind": kind, "net": marker.Net,
		"direction": dir, "offset": off, "rotation": *marker.Rotation,
	}, doc, "direct-connect-fix 恢复 marker")
	return err
}

func dcBridgeSignatures(pinned *appConfig, win, doc string) ([]string, error) {
	res, err := requestAutolayoutAction(pinned, "schematic.bridgeCheck", win, nil, doc, "direct-connect-fix bridge 基线")
	if err != nil {
		return nil, err
	}
	rep, err := parseBridgeReport(res.Result)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, tree := range rep.Trees {
		if !strings.EqualFold(tree.Kind, "BRIDGE") {
			continue
		}
		nets := append([]string(nil), tree.Nets...)
		sort.Strings(nets)
		out = append(out, "["+strings.Join(nets, ",")+"]")
	}
	sort.Strings(out)
	return out, nil
}

func dcNewBridgeSignatures(before, after []string) []string {
	have := map[string]bool{}
	for _, sig := range before {
		have[sig] = true
	}
	var out []string
	for _, sig := range after {
		if !have[sig] {
			out = append(out, sig)
		}
	}
	return out
}
