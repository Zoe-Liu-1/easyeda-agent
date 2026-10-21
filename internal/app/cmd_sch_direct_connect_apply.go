package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

type directConnectApplyReport struct {
	SchemaVersion int          `json:"schemaVersion"`
	DocumentID    string       `json:"documentId"`
	Net           string       `json:"net"`
	LabelA        string       `json:"labelA"`
	LabelB        string       `json:"labelB"`
	Path          [][2]float64 `json:"path"`
	Apply         bool         `json:"apply"`
	Save          bool         `json:"save"`
}

func newSchDirectConnectApplyCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var pair, out string
	var execute, save bool
	var maxDistance, maxMove float64
	cmd := &cobra.Command{
		Use:   "direct-connect-apply",
		Short: "Apply one already-clear direct-label connection, preserving the full pin netlist",
		Long: `Recompute direct-connect opportunities from fresh geometry, require the selected
pair to have a fully clear orthogonal path, then optionally create one wire and
remove the two now-redundant labels.

The command snapshots the complete pin-to-net map before and after every write.
If the created wire changes connectivity, it is removed. If deleting labels
changes connectivity, both labels are recreated and the new wire is removed.
Without --apply this is a read-only preflight.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			report, anchors, err := freshDirectConnectPair(pinned, win, doc, pair, maxDistance, maxMove)
			if err != nil {
				return err
			}
			report.Apply = execute
			report.Save = save
			raw, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			if out != "" {
				if err := os.WriteFile(out, append(raw, '\n'), 0644); err != nil {
					return fmt.Errorf("write direct-connect apply report: %w", err)
				}
			} else {
				fmt.Fprintln(stdout, string(raw))
			}
			if !execute {
				fmt.Fprintln(stderr, "direct-connect-apply: dry-run only; pass --apply to execute")
				return nil
			}
			return executeDirectConnectPair(pinned, win, doc, report, anchors, save, stdout, stderr)
		},
	}
	cmd.Flags().StringVar(&pair, "pair", "", "label primitive ids as LABEL_A,LABEL_B (required when multiple clear pairs exist)")
	cmd.Flags().Float64Var(&maxDistance, "max-distance", 250, "maximum Manhattan distance between label anchors")
	cmd.Flags().Float64Var(&maxMove, "max-move", 40, "maximum text move per axis used while replanning")
	cmd.Flags().BoolVar(&execute, "apply", false, "create the planned wire and delete the two labels")
	cmd.Flags().BoolVar(&save, "save", false, "save after netlist-preserving verification")
	cmd.Flags().StringVar(&out, "out", "", "write preflight/apply envelope to JSON")
	return cmd
}

func freshDirectConnectPair(pinned *appConfig, win, doc, pairArg string, maxDistance, maxMove float64) (directConnectApplyReport, map[string]checkPoint, error) {
	geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePins": true, "includePinNets": false, "includePagePrimitives": true,
	})
	if err != nil {
		return directConnectApplyReport{}, nil, err
	}
	if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
		return directConnectApplyReport{}, nil, fmt.Errorf("direct-connect-apply: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
	}
	comps, err := parseLayoutComps(geom.Result)
	if err != nil {
		return directConnectApplyReport{}, nil, err
	}
	wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
	if err != nil {
		return directConnectApplyReport{}, nil, err
	}
	vt, err := requestAction(pinned, "schematic.visible_text.bboxes", win, nil)
	if err != nil {
		return directConnectApplyReport{}, nil, err
	}
	if vt.Context != nil && vt.Context.DocumentUUID != "" && vt.Context.DocumentUUID != doc {
		return directConnectApplyReport{}, nil, fmt.Errorf("direct-connect-apply: visible text came from %q, expected %q", vt.Context.DocumentUUID, doc)
	}
	var vision alignmentVision
	raw, _ := json.Marshal(vt.Result)
	if err := json.Unmarshal(raw, &vision); err != nil {
		return directConnectApplyReport{}, nil, fmt.Errorf("direct-connect-apply: parse visible text: %w", err)
	}
	anchors := parseVisibleAttributeAnchors(geom.Result)
	sheet := sheetBBoxOf(comps)
	titleBlock, _ := titleBlockKeepoutWithSource(sheet)
	pairs := planDirectConnectTextMoves(vision.Items, comps, wires, anchors, sheet, titleBlock, maxDistance, maxMove)
	wanted := map[string]bool{}
	for _, id := range strings.Split(pairArg, ",") {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = true
		}
	}
	var matches []dcPairPlan
	for _, p := range pairs {
		if len(wanted) > 0 && !(wanted[p.LabelA] && wanted[p.LabelB]) {
			continue
		}
		if p.Status == "already-clear" && len(p.PathPoints) >= 2 {
			matches = append(matches, p)
		}
	}
	if len(matches) != 1 {
		return directConnectApplyReport{}, nil, fmt.Errorf("direct-connect-apply: selected %d clear pairs; pass --pair LABEL_A,LABEL_B", len(matches))
	}
	p := matches[0]
	return directConnectApplyReport{
		SchemaVersion: 1, DocumentID: doc, Net: p.Net,
		LabelA: p.LabelA, LabelB: p.LabelB, Path: p.PathPoints,
	}, anchors, nil
}

func executeDirectConnectPair(pinned *appConfig, win, doc string, report directConnectApplyReport,
	anchors map[string]checkPoint, save bool, stdout, stderr io.Writer) error {
	before, _, err := readLiveNets(pinned, win)
	if err != nil {
		return fmt.Errorf("direct-connect-apply: baseline netlist: %w", err)
	}
	points := make([][]float64, 0, len(report.Path))
	for _, p := range report.Path {
		points = append(points, []float64{p[0], p[1]})
	}
	created, err := requestAction(pinned, "schematic.wire.create", win, map[string]any{
		"points": points, "net": report.Net,
	})
	if err != nil {
		return fmt.Errorf("direct-connect-apply: create wire: %w", err)
	}
	wireID, _ := created.Result["primitiveId"].(string)
	if wireID == "" {
		return fmt.Errorf("direct-connect-apply: wire.create returned no primitiveId")
	}
	rollbackWire := func() {
		_, _ = requestAction(pinned, "schematic.primitives.delete", win, map[string]any{"primitiveIds": []string{wireID}})
	}
	afterWire, _, err := readLiveNets(pinned, win)
	if err != nil || !dcNetsEqual(before, afterWire) {
		rollbackWire()
		if err != nil {
			return fmt.Errorf("direct-connect-apply: read netlist after wire: %w", err)
		}
		return fmt.Errorf("direct-connect-apply: created wire changed connectivity; wire removed")
	}
	deleted, err := requestAction(pinned, "schematic.primitives.delete", win, map[string]any{
		"primitiveIds": []string{report.LabelA, report.LabelB},
	})
	if err != nil {
		rollbackWire()
		return fmt.Errorf("direct-connect-apply: delete labels: %w", err)
	}
	if nf, ok := deleted.Result["notFound"].(float64); ok && nf > 0 {
		rollbackWire()
		return fmt.Errorf("direct-connect-apply: %.0f label(s) were not found", nf)
	}
	afterLabels, _, err := readLiveNets(pinned, win)
	if err == nil && dcNetsEqual(before, afterLabels) {
		if save {
			saved, serr := requestAction(pinned, "schematic.save", win, nil)
			if serr != nil {
				return fmt.Errorf("direct-connect-apply: save: %w", serr)
			}
			ok, _ := saved.Result["saved"].(bool)
			if !ok {
				return fmt.Errorf("direct-connect-apply: schematic.save did not return saved:true")
			}
		}
		fmt.Fprintf(stdout, "✓ direct-connect-apply %s %s↔%s complete (%d-segment wire)\n",
			report.Net, report.LabelA, report.LabelB, len(report.Path)-1)
		return nil
	}
	// Restore labels from their original measured anchors before removing the
	// joining wire. This path is expected only when the host mutates net names
	// during label deletion.
	for _, id := range []string{report.LabelA, report.LabelB} {
		anchor, ok := anchors[id]
		if !ok {
			continue
		}
		_, _ = requestAction(pinned, "schematic.netflag.create", win, map[string]any{
			"kind": "net_label", "net": report.Net, "x": anchor.X, "y": anchor.Y,
		})
	}
	rollbackWire()
	if err != nil {
		return fmt.Errorf("direct-connect-apply: read netlist after label deletion: %w", err)
	}
	return fmt.Errorf("direct-connect-apply: deleting labels changed connectivity; labels restored and wire removed")
}

func dcNetsEqual(a, b map[string]map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for net, pins := range a {
		other, ok := b[net]
		if !ok || len(pins) != len(other) {
			return false
		}
		for pin := range pins {
			if !other[pin] {
				return false
			}
		}
	}
	return true
}
