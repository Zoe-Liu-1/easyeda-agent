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

// direct-connect-fix is the execution layer above direct-connect-plan. It
// executes exactly one selected proposal, then reads the page again and requires
// the same pair to be already-clear before creating the joining wire or deleting
// either label.

type dcFixScene struct {
	projectID  string
	documentID string
	geomResult map[string]any
	comps      []layoutComp
	wires      []schGroupWire
	items      []visibleTextItem
	anchors    map[string]checkPoint
	sheet      *layoutBBox
	titleBlock *layoutBBox
	pairs      []dcPairPlan
}

type dcAppliedMove struct {
	Kind          string `json:"kind"`
	NewMarkerID   string `json:"newMarkerId,omitempty"`
	NewWireID     string `json:"newWireId,omitempty"`
	Applied       bool   `json:"applied"`
	RolledBack    bool   `json:"rolledBack,omitempty"`
	RollbackError string `json:"rollbackError,omitempty"`
}

type directConnectFixReport struct {
	SchemaVersion int            `json:"schemaVersion"`
	DocumentID    string         `json:"documentId"`
	Net           string         `json:"net"`
	LabelA        string         `json:"labelA"`
	LabelB        string         `json:"labelB"`
	InitialStatus string         `json:"initialStatus"`
	FinalStatus   string         `json:"finalStatus,omitempty"`
	Initial       dcPairPlan     `json:"initial"`
	Final         *dcPairPlan    `json:"final,omitempty"`
	Move          *dcAppliedMove `json:"move,omitempty"`
	Path          [][2]float64   `json:"path,omitempty"`
	Applied       bool           `json:"applied"`
	Saved         bool           `json:"saved,omitempty"`
	Notes         []string       `json:"notes,omitempty"`
}

func newSchDirectConnectFixCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var pair, out string
	var apply, save, allowMove bool
	var maxDistance, maxMove float64
	cmd := &cobra.Command{
		Use:   "direct-connect-fix",
		Short: "Plan and safely execute one direct-label connection, including a selected pre-move",
		Long: `Read one fresh direct-connect plan and select one action:
  - already-clear: create the orthogonal wire and remove the two labels;
  - text move: move one Designator/Value attribute, then re-plan;
  - marker move: replace one single-stub netflag/netport at its proposed L;
  - part/group move: execute the proposed rigid move through the shared move kernel.

After a move the command reads the document again. The selected pair must then
re-plan as already-clear before any wire is created or label is deleted. The
complete pin-to-net map and bridge set must match the pre-command baseline.
Without --apply this command is read-only. --allow-move is required to execute a
non-already-clear proposal.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			scene, err := readDCFixScene(pinned, win, doc, maxDistance, maxMove)
			if err != nil {
				return err
			}
			pairPlan, err := selectDCFixPair(scene.pairs, pair)
			if err != nil {
				return err
			}
			report := directConnectFixReport{
				SchemaVersion: 1, DocumentID: doc,
				Net: pairPlan.Net, LabelA: pairPlan.LabelA, LabelB: pairPlan.LabelB,
				InitialStatus: pairPlan.Status, Initial: pairPlan, Path: pairPlan.PathPoints,
			}
			if err := writeDCFixReport(stdout, out, report); err != nil {
				return err
			}
			if !apply {
				fmt.Fprintln(stderr, "direct-connect-fix: dry-run only; pass --apply to execute")
				return nil
			}
			if pairPlan.Status != "already-clear" && !allowMove {
				return fmt.Errorf("direct-connect-fix: selected plan is %s; pass --allow-move to execute the proposed move", pairPlan.Status)
			}
			if err := executeDCFix(pinned, win, doc, scene, &report, maxDistance, maxMove, allowMove, save, stdout, stderr); err != nil {
				return err
			}
			return writeDCFixReport(stdout, out, report)
		},
	}
	cmd.Flags().StringVar(&pair, "pair", "", "label primitive ids as LABEL_A,LABEL_B")
	cmd.Flags().Float64Var(&maxDistance, "max-distance", 250, "maximum Manhattan distance between label anchors")
	cmd.Flags().Float64Var(&maxMove, "max-move", 40, "maximum move per axis while replanning")
	cmd.Flags().BoolVar(&apply, "apply", false, "execute the selected move/connection and verify connectivity")
	cmd.Flags().BoolVar(&allowMove, "allow-move", false, "allow executing a text/marker/part/group move proposal")
	cmd.Flags().BoolVar(&save, "save", false, "save after final connectivity and bridge verification")
	cmd.Flags().StringVar(&out, "out", "", "write structured report to JSON")
	return cmd
}

func writeDCFixReport(stdout io.Writer, out string, report directConnectFixReport) error {
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

func readDCFixScene(pinned *appConfig, win, doc string, maxDistance, maxMove float64) (dcFixScene, error) {
	geom, err := requestAction(pinned, "schematic.components.list", win, map[string]any{
		"includeBBox": true, "includePins": true, "includePinNets": false, "includePagePrimitives": true,
	})
	if err != nil {
		return dcFixScene{}, err
	}
	if geom.Context != nil && geom.Context.DocumentUUID != "" && geom.Context.DocumentUUID != doc {
		return dcFixScene{}, fmt.Errorf("direct-connect-fix: geometry came from %q, expected %q", geom.Context.DocumentUUID, doc)
	}
	comps, err := parseLayoutComps(geom.Result)
	if err != nil {
		return dcFixScene{}, err
	}
	wires, err := fetchSchWirePolylinesStable(pinned, win, doc)
	if err != nil {
		return dcFixScene{}, err
	}
	vt, err := requestAction(pinned, "schematic.visible_text.bboxes", win, nil)
	if err != nil {
		return dcFixScene{}, err
	}
	if vt.Context != nil && vt.Context.DocumentUUID != "" && vt.Context.DocumentUUID != doc {
		return dcFixScene{}, fmt.Errorf("direct-connect-fix: visible text came from %q, expected %q", vt.Context.DocumentUUID, doc)
	}
	var vision alignmentVision
	raw, _ := json.Marshal(vt.Result)
	if err := json.Unmarshal(raw, &vision); err != nil {
		return dcFixScene{}, fmt.Errorf("direct-connect-fix: parse visible text: %w", err)
	}
	sheet := sheetBBoxOf(comps)
	titleBlock, _ := titleBlockKeepoutWithSource(sheet)
	pairs := planDirectConnectTextMoves(vision.Items, comps, wires,
		parseVisibleAttributeAnchors(geom.Result), sheet, titleBlock, maxDistance, maxMove)
	return dcFixScene{
		projectID: func() string {
			if geom.Context != nil {
				return geom.Context.ProjectUUID
			}
			return ""
		}(),
		documentID: doc,
		geomResult: geom.Result, comps: comps, wires: wires, items: vision.Items,
		anchors: parseVisibleAttributeAnchors(geom.Result), sheet: sheet, titleBlock: titleBlock,
		pairs: pairs,
	}, nil
}

func dcActionableStatus(status string) bool {
	switch status {
	case "already-clear", "move-proposed", "marker-move-proposed", "part-move-proposed", "group-move-proposed":
		return true
	case "unresolved":
		return false
	default:
		return false
	}
}

func selectDCFixPair(pairs []dcPairPlan, pairArg string) (dcPairPlan, error) {
	wanted := map[string]bool{}
	for _, id := range strings.Split(pairArg, ",") {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = true
		}
	}
	var matches []dcPairPlan
	for _, pair := range pairs {
		if len(wanted) > 0 && !(wanted[pair.LabelA] && wanted[pair.LabelB]) {
			continue
		}
		if dcActionableStatus(pair.Status) {
			matches = append(matches, pair)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(wanted) > 0 && len(matches) == 0 {
		return dcPairPlan{}, fmt.Errorf("direct-connect-fix: selected pair has no actionable plan")
	}
	return dcPairPlan{}, fmt.Errorf("direct-connect-fix: selected %d actionable pairs; pass --pair LABEL_A,LABEL_B", len(matches))
}

// resolveDCFixFinalPair accepts an unchanged label-ID pair first. Rigid moves may
// recreate wire-label attributes with new IDs, so it then matches the same net
// against the simulated post-move path endpoints. Ambiguity is refused.
func resolveDCFixFinalPair(scene dcFixScene, intended dcPairPlan) (dcPairPlan, error) {
	var exact []dcPairPlan
	var byNet []dcPairPlan
	var byEndpoint []dcPairPlan
	for _, pair := range scene.pairs {
		if pair.Status != "already-clear" || len(pair.PathPoints) < 2 || pair.Net != intended.Net {
			continue
		}
		if pair.LabelA == intended.LabelA && pair.LabelB == intended.LabelB {
			exact = append(exact, pair)
		}
		byNet = append(byNet, pair)
		if len(intended.PathPoints) >= 2 && dcPathEndpointsMatch(pair.PathPoints, intended.PathPoints) {
			byEndpoint = append(byEndpoint, pair)
		}
	}
	for _, group := range [][]dcPairPlan{exact, byEndpoint, byNet} {
		if len(group) == 1 {
			return group[0], nil
		}
		if len(group) > 1 {
			return dcPairPlan{}, fmt.Errorf("direct-connect-fix: %d already-clear candidates match intended %s %s↔%s; refusing to guess",
				len(group), intended.Net, intended.LabelA, intended.LabelB)
		}
	}
	return dcPairPlan{}, fmt.Errorf("direct-connect-fix: post-move re-plan did not yield a unique already-clear path for %s", intended.Net)
}

func dcPathEndpointsMatch(a, b [][2]float64) bool {
	if len(a) < 2 || len(b) < 2 {
		return false
	}
	const eps = 1.0
	forward := pointEquals2(a[0][0], a[0][1], b[0]) && pointEquals2(a[len(a)-1][0], a[len(a)-1][1], b[len(b)-1])
	reverse := pointEquals2(a[0][0], a[0][1], b[len(b)-1]) && pointEquals2(a[len(a)-1][0], a[len(a)-1][1], b[0])
	return forward || reverse
}

func sortedSameStrings(a, b []string) bool {
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	if len(aa) != len(bb) {
		return false
	}
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
