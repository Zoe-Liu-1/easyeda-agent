package app

// cmd_pcb_group.go — persistent VIRTUAL groups for the PCB (issue #173, PCB side).
//
// 平台墙(与原理图侧同一结论,3.2.121 / 3.2.149 两次探测):官方扩展 API 没有任何
// 器件编组接口 —— api-index 里全部 `*Group*` 命中都是 DRC 约束域(等长网络组、
// 焊盘对组),UI 里建的「组合」对扩展完全不可见,扩展建的组 UI 里也看不见。所以
// easyeda-agent 自己持久化这层关系,**所有输出都标 `mode: "virtual"`**,绝不宣称
// 支持原生 UI 编组。
//
// 存储与原理图组同一张表:workflow.State.GroupsByPage,键是 **PCB documentUuid**
// (多板工程每块板各自一张,不跨板串组),组上 Kind="pcb",让跨页读取的原理图
// 消费者把它们挡在外面。CRUD 纯函数(groupsCreate/AddMembers/RemoveMembers/
// Ungroup/findPartialGroups)与原理图侧共用,不另起数据模型。
//
// 消费方:
//   - `pcb group move --group <id> --dx --dy` —— 解析成员位号 → 活体 primitiveId,
//     一次 `pcb.components.move` 刚性平移,再回读逐件核对位移;
//   - modify / move / align / distribute / grid-snap / arrange —— 选择集破坏组
//     即拒绝(guardPcbGroupIntegrity),--break-group 放行;
//   - auto-place / place-constrained / refine —— 规划器把组成员当**固定件**,
//     --break-group 恢复旧行为;
//   - `pcb delete` —— 删除成员后级联摘除组登记。
//
// v1 不做:整组旋转(成员 rotation 与绕组中心的位置要一起改,anchor↔bbox 偏移随
// 旋转变化)、走线/过孔跟随(pcb.components.move 只搬器件)。

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/easyeda-agent/internal/workflow"
)

// pcbGroupMode is stamped on every pcb group output: the relation lives in
// easyeda-agent's workflow state, NOT in the EasyEDA document.
const pcbGroupMode = "virtual"

// breakGroupFlagHelp is the shared --break-group text of the per-part layout ops.
const breakGroupFlagHelp = "allow repositioning members of a virtual `pcb group` individually (breaks the group's relative geometry)"

// pcbGroupMoveTol is the readback tolerance (mil) for a group move: the
// connector writes x+dx exactly, the host keeps 4 decimals in mil.
const pcbGroupMoveTol = 0.01

// pcbGroupComp is the slice of a live PCB component the group tools need.
type pcbGroupComp struct {
	ID         string
	Designator string
	X, Y       float64
	Rotation   float64
	Layer      int
	Locked     bool
}

func parsePcbGroupComps(result map[string]any) []pcbGroupComp {
	var out []pcbGroupComp
	for _, c := range parseBoardComponents(result) {
		out = append(out, pcbGroupComp{
			ID: c.ID, Designator: strings.TrimSpace(c.Designator),
			X: c.X, Y: c.Y, Rotation: c.Rotation, Layer: c.Layer, Locked: c.Locked,
		})
	}
	return out
}

// ── pure core (table-tested) ────────────────────────────────────────────────

// resolvePcbGroupMembers turns --designators / --ids into member designators.
// ids are resolved against the live component list; an unknown id or a part
// without a designator is a hard error (a group of unnamed parts can't be
// re-resolved after the next primitiveId churn).
func resolvePcbGroupMembers(designators, ids []string, comps []pcbGroupComp) ([]string, error) {
	out := append([]string(nil), designators...)
	if len(ids) > 0 {
		byID := map[string]pcbGroupComp{}
		for _, c := range comps {
			byID[c.ID] = c
		}
		for _, id := range ids {
			c, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("primitiveId %s is not a component on the active PCB (`pcb list` to see ids)", id)
			}
			if c.Designator == "" {
				return nil, fmt.Errorf("component %s has no designator — PCB groups are keyed by designator", id)
			}
			out = append(out, c.Designator)
		}
	}
	out = schGroupDesignators(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("pass --designators R1,C5,U2 or --ids <primitiveId,...>")
	}
	return out, nil
}

// markPcbGroups stamps Kind=pcb on every group of a PCB document table.
func markPcbGroups(groups []*schGroup) []*schGroup {
	for _, g := range groups {
		if g != nil {
			g.Kind = workflow.GroupKindPCB
		}
	}
	return groups
}

// pcbGroupViolations decides which groups a layout op on `selected` would break.
//
// rigid=true (a translation applied identically to every selected part, e.g.
// `pcb move`, a single-part modify): only a PARTIAL cover breaks a group — the
// whole group moving by the same vector keeps its geometry.
//
// rigid=false (align / distribute / grid-snap / arrange — each part gets its own
// target): touching ANY member of a multi-part group breaks it, even when the
// whole group is selected, because the members are repositioned individually.
func pcbGroupViolations(groups []*schGroup, selected []string, rigid bool) []*schGroup {
	if rigid {
		return findPartialGroups(groups, selected)
	}
	sel := map[string]bool{}
	for _, d := range selected {
		sel[strings.ToUpper(strings.TrimSpace(d))] = true
	}
	var out []*schGroup
	for _, g := range groups {
		if g == nil || len(g.Members) < 2 {
			continue
		}
		for _, m := range g.Members {
			if sel[strings.ToUpper(m)] {
				out = append(out, g)
				break
			}
		}
	}
	return out
}

// pcbGroupViolationError renders the refusal (nil when nothing is broken).
func pcbGroupViolationError(verb string, violated []*schGroup, rigid bool) error {
	if len(violated) == 0 {
		return nil
	}
	var lines []string
	for _, g := range violated {
		lines = append(lines, fmt.Sprintf("  %s: %s", describeSchGroup(g), strings.Join(g.Members, ",")))
	}
	what := "selection partially covers"
	if !rigid {
		what = "would reposition members individually in"
	}
	return fmt.Errorf("%s %s %d virtual PCB group(s) — a group moves as a rigid body (mode=virtual):\n%s\n"+
		"use `pcb group move --group <id> --dx <mil> --dy <mil>` to translate a group rigidly, "+
		"pass --break-group to override, or `pcb group ungroup --group <id>` to dissolve it",
		verb, what, len(violated), strings.Join(lines, "\n"))
}

// pcbGroupFixedSet maps UPPER designator → reason for every member of a
// multi-part group: planners keep these parts where they are.
func pcbGroupFixedSet(groups []*schGroup) map[string]string {
	out := map[string]string{}
	for _, g := range groups {
		if g == nil || len(g.Members) < 2 {
			continue
		}
		for _, m := range g.Members {
			out[strings.ToUpper(strings.TrimSpace(m))] = "member of virtual group " + describeSchGroup(g)
		}
	}
	return out
}

// pcbGroupMoveTarget is one member's planned rigid translation.
type pcbGroupMoveTarget struct {
	Designator string  `json:"designator"`
	ID         string  `json:"primitiveId"`
	FromX      float64 `json:"fromX"`
	FromY      float64 `json:"fromY"`
	ToX        float64 `json:"toX"`
	ToY        float64 `json:"toY"`
	Rotation   float64 `json:"rotation"`
	Layer      int     `json:"layer"`
	// Readback (filled by verifyPcbGroupMove).
	GotX   *float64 `json:"readbackX,omitempty"`
	GotY   *float64 `json:"readbackY,omitempty"`
	Status string   `json:"status,omitempty"` // ok | displaced-wrong | rotated | layer-changed | missing
}

// planPcbGroupMove resolves every member and computes its exact target. A
// stale member (designator not on the board) or a locked member refuses the
// whole move: moving the rest would itself break the group.
func planPcbGroupMove(g *schGroup, comps []pcbGroupComp, dx, dy float64) ([]pcbGroupMoveTarget, error) {
	byDes := map[string][]pcbGroupComp{}
	for _, c := range comps {
		if c.Designator != "" {
			byDes[strings.ToUpper(c.Designator)] = append(byDes[strings.ToUpper(c.Designator)], c)
		}
	}
	var targets []pcbGroupMoveTarget
	var stale, locked, dup []string
	for _, m := range g.Members {
		hits := byDes[strings.ToUpper(m)]
		switch {
		case len(hits) == 0:
			stale = append(stale, m)
			continue
		case len(hits) > 1:
			dup = append(dup, m)
			continue
		}
		c := hits[0]
		if c.Locked {
			locked = append(locked, m)
		}
		targets = append(targets, pcbGroupMoveTarget{
			Designator: m, ID: c.ID,
			FromX: c.X, FromY: c.Y, ToX: c.X + dx, ToY: c.Y + dy,
			Rotation: c.Rotation, Layer: c.Layer,
		})
	}
	switch {
	case len(stale) > 0:
		return nil, fmt.Errorf("group %s has member(s) not on the board: %s — `pcb group remove --group %s --designators %s` first",
			describeSchGroup(g), strings.Join(stale, ","), g.ID, strings.Join(stale, ","))
	case len(dup) > 0:
		return nil, fmt.Errorf("group %s member designator(s) resolve to more than one component: %s — fix the duplicate designators first",
			describeSchGroup(g), strings.Join(dup, ","))
	case len(locked) > 0:
		return nil, fmt.Errorf("group %s has locked member(s): %s — unlock them first (`pcb lock --ids <id> --unlock`); a partial move would break the group",
			describeSchGroup(g), strings.Join(locked, ","))
	}
	return targets, nil
}

// verifyPcbGroupMove checks a FRESH readback: every member displaced by exactly
// (dx,dy) (so relative geometry is preserved by construction), rotation and
// layer unchanged. Returns the mismatch lines (empty = verified).
func verifyPcbGroupMove(targets []pcbGroupMoveTarget, after []pcbGroupComp, tol float64) []string {
	byID := map[string]pcbGroupComp{}
	for _, c := range after {
		byID[c.ID] = c
	}
	var bad []string
	for i := range targets {
		t := &targets[i]
		c, ok := byID[t.ID]
		if !ok {
			t.Status = "missing"
			bad = append(bad, fmt.Sprintf("%s (%s): not in the readback", t.Designator, t.ID))
			continue
		}
		gx, gy := c.X, c.Y
		t.GotX, t.GotY = &gx, &gy
		switch {
		case math.Abs(c.X-t.ToX) > tol || math.Abs(c.Y-t.ToY) > tol:
			t.Status = "displaced-wrong"
			bad = append(bad, fmt.Sprintf("%s: expected (%.4f, %.4f), read back (%.4f, %.4f)", t.Designator, t.ToX, t.ToY, c.X, c.Y))
		case math.Abs(math.Remainder(c.Rotation-t.Rotation, 360)) > 1e-6:
			t.Status = "rotated"
			bad = append(bad, fmt.Sprintf("%s: rotation changed %.2f → %.2f", t.Designator, t.Rotation, c.Rotation))
		case c.Layer != t.Layer:
			t.Status = "layer-changed"
			bad = append(bad, fmt.Sprintf("%s: layer changed %d → %d", t.Designator, t.Layer, c.Layer))
		default:
			t.Status = "ok"
		}
	}
	return bad
}

// ── live context ────────────────────────────────────────────────────────────

// pinPcbDocument is pinZonePage for the PCB: the ACTIVE document must be a PCB;
// its UUID keys the group table. A schematic in front is refused, never guessed.
func pinPcbDocument(cfg *appConfig, window string) (*appConfig, string, string, error) {
	win, err := resolveTargetWindow(cfg, window)
	if err != nil {
		return nil, "", "", fmt.Errorf("pcb document: resolve target window: %w", err)
	}
	local := *cfg
	if err := ensureActiveDoc(&local, win); err != nil {
		return nil, "", "", fmt.Errorf("pcb document: activate target document: %w", err)
	}
	docUUID, err := currentDocumentUUIDOfType(&local, win, "pcb")
	if err != nil {
		return nil, "", "", fmt.Errorf("pcb document: %w — switch to the PCB first (`easyeda doc switch <pcb>`)", err)
	}
	local.doc = docUUID
	return &local, win, docUUID, nil
}

// loadPcbGroupsContext pins the active PCB and loads its group table (same
// identity/binding path as loadSchGroupsContext).
func loadPcbGroupsContext(cfg *appConfig, window string) (pinned *appConfig, win, docUUID, project string, st *pcbStageState, groups []*schGroup, err error) {
	pinned, win, docUUID, err = pinPcbDocument(cfg, window)
	if err != nil {
		return nil, "", "", "", nil, nil, err
	}
	var uuid string
	project, uuid, err = resolveStageIdentity(pinned, win)
	if err != nil {
		return nil, "", "", "", nil, nil, err
	}
	st, err = loadPcbStageState(project)
	if err != nil {
		return nil, "", "", "", nil, nil, err
	}
	st.Bind(uuid)
	return pinned, win, docUUID, project, st, st.GroupsForPage(docUUID), nil
}

func savePcbGroups(st *pcbStageState, docUUID string, groups []*schGroup) error {
	return saveSchGroups(st, docUUID, markPcbGroups(groups))
}

func readPcbGroupComps(cfg *appConfig, window, why string) ([]pcbGroupComp, error) {
	res, err := requestAction(cfg, "pcb.components.list", window, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: read pcb components: %w", why, err)
	}
	return parsePcbGroupComps(res.Result), nil
}

// guardPcbGroupIntegrity is the layout-write gate. ids are the primitiveIds the
// command is about to reposition. Best-effort by design (mirrors
// guardSchGroupIntegrity): failing to LOAD the table or the component list is a
// stderr note, never a block — the gate must not kill the primary command.
func guardPcbGroupIntegrity(cfg *appConfig, window, verb string, ids []string, rigid, breakGroup bool, stderr io.Writer) error {
	if breakGroup || len(ids) == 0 {
		return nil
	}
	pinned, win, _, _, _, groups, err := loadPcbGroupsContext(cfg, window)
	if err != nil {
		fmt.Fprintf(stderr, "note: PCB group integrity check unavailable (%v)\n", err)
		return nil
	}
	if len(groups) == 0 {
		return nil
	}
	comps, err := readPcbGroupComps(pinned, win, "group integrity check")
	if err != nil {
		fmt.Fprintf(stderr, "note: PCB group integrity check unavailable (%v)\n", err)
		return nil
	}
	return pcbGroupViolationError(verb, pcbGroupSelection(groups, ids, comps, rigid), rigid)
}

// pcbGroupSelection maps selected primitiveIds to designators and returns the
// groups the selection would break.
func pcbGroupSelection(groups []*schGroup, ids []string, comps []pcbGroupComp, rigid bool) []*schGroup {
	desByID := map[string]string{}
	for _, c := range comps {
		desByID[c.ID] = c.Designator
	}
	var selected []string
	for _, id := range ids {
		if d := desByID[id]; d != "" {
			selected = append(selected, d)
		}
	}
	return pcbGroupViolations(groups, selected, rigid)
}

// pcbModifyMovesPart reports whether a modify patch repositions the part.
func pcbModifyMovesPart(patch map[string]any) bool {
	for _, k := range []string{"x", "y", "rotation", "layer"} {
		if _, ok := patch[k]; ok {
			return true
		}
	}
	return false
}

// loadPcbGroupFixed is the planner hook: the members of every multi-part group
// on the active PCB, to be kept FIXED. Prints one note naming the groups. Load
// failures degrade to a note (planner runs as before).
func loadPcbGroupFixed(cfg *appConfig, window, verb string, breakGroup bool, stderr io.Writer) map[string]string {
	if breakGroup {
		return nil
	}
	_, _, _, _, _, groups, err := loadPcbGroupsContext(cfg, window)
	if err != nil {
		fmt.Fprintf(stderr, "note: PCB virtual groups unavailable (%v) — %s runs without group protection\n", err, verb)
		return nil
	}
	fixed := pcbGroupFixedSet(groups)
	if len(fixed) == 0 {
		return nil
	}
	var names []string
	for _, g := range groups {
		if g != nil && len(g.Members) >= 2 {
			names = append(names, describeSchGroup(g))
		}
	}
	fmt.Fprintf(stderr, "note: %d virtual PCB group(s) (%s) — %s keeps their %d member(s) FIXED; `pcb group move` translates a group, --break-group lets %s move members individually\n",
		len(names), strings.Join(names, ", "), verb, len(fixed), verb)
	return fixed
}

func pcbGroupFixedBools(fixed map[string]string) map[string]bool {
	if len(fixed) == 0 {
		return nil
	}
	out := make(map[string]bool, len(fixed))
	for k := range fixed {
		out[k] = true
	}
	return out
}

// planPcbGroupDeleteCascade resolves, BEFORE a delete, which doomed ids are
// group members (id → designator). Best-effort and read-only.
func planPcbGroupDeleteCascade(cfg *appConfig, window string, ids []string, stderr io.Writer) map[string]string {
	if len(ids) == 0 {
		return nil
	}
	pinned, win, _, _, _, groups, err := loadPcbGroupsContext(cfg, window)
	if err != nil || len(groups) == 0 {
		return nil
	}
	comps, err := readPcbGroupComps(pinned, win, "check group membership")
	if err != nil {
		return nil
	}
	desByID := map[string]string{}
	for _, c := range comps {
		desByID[c.ID] = c.Designator
	}
	plan := map[string]string{}
	for _, id := range ids {
		if d := desByID[id]; d != "" {
			if g := groupOfMember(groups, d); g != nil {
				plan[id] = d
				fmt.Fprintf(stderr, "note: %s belongs to virtual PCB group %s — its registration is removed once the delete is read back\n", d, describeSchGroup(g))
			}
		}
	}
	return plan
}

// cascadePcbGroupDelete strips deleted members from the PCB group table after
// a FRESH readback proves they are gone (platform delete can fake-succeed).
// Fail-soft: the delete already happened; `pcb group list` marks stale.
func cascadePcbGroupDelete(cfg *appConfig, window string, plan map[string]string, stderr io.Writer) {
	if len(plan) == 0 {
		return
	}
	pinned, win, docUUID, _, st, groups, err := loadPcbGroupsContext(cfg, window)
	if err != nil {
		fmt.Fprintf(stderr, "warn: PCB group cascade unavailable (%v) — `pcb group list` marks stale members, `pcb group remove` clears them\n", err)
		return
	}
	res, err := requestReadAfterWrite(pinned, "pcb.components.list", win, nil, "pcb delete · group cascade readback")
	if err != nil {
		fmt.Fprintf(stderr, "warn: PCB group cascade skipped — readback failed (%v); `pcb group list` marks stale members\n", err)
		return
	}
	present := map[string]bool{}
	for _, c := range parsePcbGroupComps(res.Result) {
		present[c.ID] = true
	}
	drop := map[string]bool{}
	for id, d := range plan {
		if !present[id] {
			drop[strings.ToUpper(d)] = true
		}
	}
	if len(drop) == 0 {
		return
	}
	next, notes := cascadeGroupsRemoveDesignators(groups, drop)
	if notes == nil {
		return
	}
	if err := savePcbGroups(st, docUUID, next); err != nil {
		fmt.Fprintf(stderr, "warn: PCB group cascade not persisted (%v) — use `pcb group remove`\n", err)
		return
	}
	for _, n := range notes {
		fmt.Fprintf(stderr, "cascade ✓ %s\n", n)
	}
}

// ── cobra surface ───────────────────────────────────────────────────────────

func writePcbGroupJSON(stdout io.Writer, v any) error {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func newPcbGroupCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	group := &cobra.Command{
		Use:   "group",
		Short: "Persistent VIRTUAL component groups on the active PCB (mode=virtual — not EasyEDA UI groups)",
		Long: `Persistent virtual groups over PCB components (issue #173).

The platform wall: the EasyEDA extension API exposes NO component-grouping call
(every *Group* API is a DRC net/pad-pair group), and groups made in the UI are
invisible to extensions. So easyeda-agent persists the relation itself, keyed by
the ACTIVE PCB's documentUuid in the project workflow state (the same table as
schematic groups, ~/.easyeda-agent/workflow/<project>.json). Every output says
mode=virtual: these groups do NOT appear in the EasyEDA UI, and UI groups are
NOT read here.

Members are DESIGNATORS (stable within a board; primitiveIds churn), resolved to
live primitiveIds at use. A designator belongs to at most one group per board.

Layout actions consume the table:
  - ` + "`pcb group move`" + ` translates a whole group rigidly (readback-verified);
  - modify / move / align / distribute / grid-snap / arrange REFUSE a selection
    that would break a group (--break-group overrides);
  - auto-place / place-constrained / refine keep group members FIXED
    (--break-group restores the old individual placement);
  - ` + "`pcb delete`" + ` of a member removes it from its group.

v1 limits: no group rotation; tracks/vias are NOT carried by a group move.`,
	}

	addMemberFlags := func(c *cobra.Command, desig, ids *string, verb string) {
		c.Flags().StringVar(desig, "designators", "", "member designators to "+verb+" — CSV: R1,C5,U2")
		c.Flags().StringVar(ids, "ids", "", "member primitiveIds to "+verb+" — CSV (resolved to designators via the live board)")
	}
	resolveMembers := func(pinned *appConfig, win, desig, ids string) ([]string, error) {
		idList := []string(nil)
		if strings.TrimSpace(ids) != "" {
			var err error
			if idList, err = parseIDList(ids); err != nil {
				return nil, err
			}
		}
		var comps []pcbGroupComp
		if len(idList) > 0 {
			var err error
			if comps, err = readPcbGroupComps(pinned, win, "resolve --ids"); err != nil {
				return nil, err
			}
		}
		return resolvePcbGroupMembers(splitDesignators(desig), idList, comps)
	}

	// ── create ──
	{
		var desig, ids, name string
		var asJSON bool
		c := &cobra.Command{
			Use:   "create",
			Short: "Create a virtual group (id auto-assigned g1, g2, …) on the active PCB",
			Args:  cobra.NoArgs,
			Example: `  easyeda pcb group create --designators H3,H4 --name mount-pair
  easyeda pcb group create --ids <pid1>,<pid2>`,
			RunE: func(cmd *cobra.Command, args []string) error {
				pinned, win, docUUID, project, st, groups, err := loadPcbGroupsContext(cfg, *window)
				if err != nil {
					return err
				}
				members, err := resolveMembers(pinned, win, desig, ids)
				if err != nil {
					return err
				}
				next, g, err := groupsCreate(groups, name, members)
				if err != nil {
					return err
				}
				g.Kind = workflow.GroupKindPCB
				if err := savePcbGroups(st, docUUID, next); err != nil {
					return err
				}
				if asJSON {
					return writePcbGroupJSON(stdout, map[string]any{"ok": true, "mode": pcbGroupMode, "project": project, "documentUuid": docUUID, "group": g})
				}
				fmt.Fprintf(stdout, "✓ created virtual PCB group %s (mode=virtual) — %d member(s): %s (project %q, pcb %s)\n",
					describeSchGroup(g), len(g.Members), strings.Join(g.Members, ","), project, docUUID)
				return nil
			},
		}
		addMemberFlags(c, &desig, &ids, "group")
		c.Flags().StringVar(&name, "name", "", "optional human-readable group name")
		c.Flags().BoolVar(&asJSON, "json", false, "emit the created group as JSON")
		group.AddCommand(c)
	}

	// ── list ──
	{
		var asJSON bool
		c := &cobra.Command{
			Use:   "list",
			Short: "List the active PCB's virtual groups; members absent from the board are marked stale",
			Args:  cobra.NoArgs,
			Example: `  easyeda pcb group list
  easyeda pcb group list --json`,
			RunE: func(cmd *cobra.Command, args []string) error {
				pinned, win, docUUID, project, _, groups, err := loadPcbGroupsContext(cfg, *window)
				if err != nil {
					return err
				}
				present := map[string]bool{}
				presenceKnown := false
				if comps, rerr := readPcbGroupComps(pinned, win, "check group member presence"); rerr == nil {
					presenceKnown = true
					for _, c := range comps {
						if c.Designator != "" {
							present[strings.ToUpper(c.Designator)] = true
						}
					}
				} else {
					fmt.Fprintf(stderr, "note: live presence check unavailable (%v) — stale members not marked\n", rerr)
				}
				type memberOut struct {
					Designator string `json:"designator"`
					Stale      bool   `json:"stale,omitempty"`
				}
				type groupOut struct {
					ID      string      `json:"id"`
					Name    string      `json:"name,omitempty"`
					Members []memberOut `json:"members"`
					Stale   int         `json:"staleCount,omitempty"`
					At      string      `json:"at,omitempty"`
				}
				var outs []groupOut
				for _, g := range groups {
					if g == nil {
						continue
					}
					o := groupOut{ID: g.ID, Name: g.Name, At: g.At}
					for _, m := range g.Members {
						stale := presenceKnown && !present[strings.ToUpper(m)]
						if stale {
							o.Stale++
						}
						o.Members = append(o.Members, memberOut{Designator: m, Stale: stale})
					}
					outs = append(outs, o)
				}
				if asJSON {
					return writePcbGroupJSON(stdout, map[string]any{
						"mode": pcbGroupMode, "project": project, "documentUuid": docUUID,
						"presenceChecked": presenceKnown, "groups": outs,
					})
				}
				if len(outs) == 0 {
					fmt.Fprintf(stdout, "no virtual PCB groups for %q on pcb %s (mode=virtual) — `pcb group create --designators H3,H4`\n", project, docUUID)
					return nil
				}
				fmt.Fprintf(stdout, "virtual PCB groups (mode=virtual — not EasyEDA UI groups) — project %q, pcb %s\n", project, docUUID)
				for _, o := range outs {
					rendered := make([]string, 0, len(o.Members))
					for _, m := range o.Members {
						if m.Stale {
							rendered = append(rendered, m.Designator+"(stale)")
						} else {
							rendered = append(rendered, m.Designator)
						}
					}
					name := ""
					if o.Name != "" {
						name = fmt.Sprintf(" %q", o.Name)
					}
					line := fmt.Sprintf("  %-4s%s  %d member(s): %s", o.ID, name, len(o.Members), strings.Join(rendered, ","))
					if o.Stale > 0 {
						line += fmt.Sprintf("  ⚠ %d stale (designator not on the board — `pcb group remove`)", o.Stale)
					}
					fmt.Fprintln(stdout, line)
				}
				if !presenceKnown {
					fmt.Fprintln(stdout, "  ⚠ 本次未能读到板上器件表 —— 上面**没有**做在场校验,stale 成员不会被标出")
				}
				return nil
			},
		}
		c.Flags().BoolVar(&asJSON, "json", false, "emit groups as JSON")
		group.AddCommand(c)
	}

	// ── add ──
	{
		var groupRef, desig, ids string
		c := &cobra.Command{
			Use:     "add",
			Short:   "Add members to an existing virtual group",
			Args:    cobra.NoArgs,
			Example: `  easyeda pcb group add --group g1 --designators C6,C7`,
			RunE: func(cmd *cobra.Command, args []string) error {
				pinned, win, docUUID, _, st, groups, err := loadPcbGroupsContext(cfg, *window)
				if err != nil {
					return err
				}
				members, err := resolveMembers(pinned, win, desig, ids)
				if err != nil {
					return err
				}
				next, g, err := groupsAddMembers(groups, groupRef, members)
				if err != nil {
					return err
				}
				if err := savePcbGroups(st, docUUID, next); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "✓ virtual PCB group %s (mode=virtual) now has %d member(s): %s\n", describeSchGroup(g), len(g.Members), strings.Join(g.Members, ","))
				return nil
			},
		}
		c.Flags().StringVar(&groupRef, "group", "", "group id (g1) or name (required)")
		addMemberFlags(c, &desig, &ids, "add")
		_ = c.MarkFlagRequired("group")
		group.AddCommand(c)
	}

	// ── remove ──
	{
		var groupRef, desig, ids string
		c := &cobra.Command{
			Use:     "remove",
			Short:   "Remove members from a virtual group (an emptied group is deleted)",
			Args:    cobra.NoArgs,
			Example: `  easyeda pcb group remove --group g1 --designators C6`,
			RunE: func(cmd *cobra.Command, args []string) error {
				_, _, docUUID, _, st, groups, err := loadPcbGroupsContext(cfg, *window)
				if err != nil {
					return err
				}
				// remove 只按位号:被删掉/改名的件(正是要清理的 stale 成员)已经
				// 没有活体 primitiveId 可解析了。
				if strings.TrimSpace(ids) != "" {
					return fmt.Errorf("remove takes --designators only (stale members have no live primitiveId)")
				}
				next, g, removedGroup, err := groupsRemoveMembers(groups, groupRef, splitDesignators(desig))
				if err != nil {
					return err
				}
				if err := savePcbGroups(st, docUUID, next); err != nil {
					return err
				}
				if removedGroup {
					fmt.Fprintf(stdout, "✓ virtual PCB group %s is now empty — group deleted\n", describeSchGroup(g))
				} else {
					fmt.Fprintf(stdout, "✓ virtual PCB group %s now has %d member(s): %s\n", describeSchGroup(g), len(g.Members), strings.Join(g.Members, ","))
				}
				return nil
			},
		}
		c.Flags().StringVar(&groupRef, "group", "", "group id (g1) or name (required)")
		addMemberFlags(c, &desig, &ids, "remove")
		_ = c.MarkFlagRequired("group")
		group.AddCommand(c)
	}

	// ── ungroup ──
	{
		var groupRef string
		c := &cobra.Command{
			Use:     "ungroup",
			Short:   "Dissolve a virtual group (relation only — no component is touched)",
			Args:    cobra.NoArgs,
			Example: `  easyeda pcb group ungroup --group g1`,
			RunE: func(cmd *cobra.Command, args []string) error {
				_, _, docUUID, _, st, groups, err := loadPcbGroupsContext(cfg, *window)
				if err != nil {
					return err
				}
				next, g, err := groupsUngroup(groups, groupRef)
				if err != nil {
					return err
				}
				if err := savePcbGroups(st, docUUID, next); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "✓ dissolved virtual PCB group %s (%d member(s) released: %s) — components untouched, members now move independently\n",
					describeSchGroup(g), len(g.Members), strings.Join(g.Members, ","))
				return nil
			},
		}
		c.Flags().StringVar(&groupRef, "group", "", "group id (g1) or name (required)")
		_ = c.MarkFlagRequired("group")
		group.AddCommand(c)
	}

	// ── move ──
	{
		var groupRef string
		var dx, dy float64
		var asJSON bool
		c := &cobra.Command{
			Use:   "move",
			Short: "Rigidly translate a whole virtual group by (dx, dy) mil, verified by fresh readback",
			Long: `Translate every member of a virtual group by the same (dx, dy) in one
` + "`pcb.components.move`" + ` call, then re-read the board and check each member moved
by EXACTLY (dx, dy) with rotation and layer unchanged — relative geometry is
preserved by construction, and the readback proves it.

Refused up front (nothing is moved) when a member is not on the board (stale),
is locked, or its designator is duplicated. A readback mismatch exits non-zero
with the per-member report.

v1 scope: translation only — group ROTATION is not supported yet. Tracks and
vias are NOT carried: re-route (or rip-up first) connections of moved members.`,
			Args: cobra.NoArgs,
			Example: `  easyeda pcb group move --group g1 --dx 100 --dy 0
  easyeda pcb group move --group mount-pair --dx -50 --dy 25 --json`,
			RunE: func(cmd *cobra.Command, args []string) error {
				if !cmd.Flags().Changed("dx") && !cmd.Flags().Changed("dy") {
					return fmt.Errorf("at least one of --dx / --dy is required")
				}
				pinned, win, docUUID, _, _, groups, err := loadPcbGroupsContext(cfg, *window)
				if err != nil {
					return err
				}
				g, err := findSchGroup(groups, groupRef)
				if err != nil {
					return err
				}
				return runPcbGroupMove(pinned, win, docUUID, g, dx, dy, asJSON, stdout, stderr)
			},
		}
		c.Flags().StringVar(&groupRef, "group", "", "group id (g1) or name (required)")
		c.Flags().Float64Var(&dx, "dx", 0, "X offset (mil)")
		c.Flags().Float64Var(&dy, "dy", 0, "Y offset (mil)")
		c.Flags().BoolVar(&asJSON, "json", false, "emit the move report as JSON")
		_ = c.MarkFlagRequired("group")
		group.AddCommand(c)
	}

	return group
}

// runPcbGroupMove plans, writes and verifies one rigid group translation.
func runPcbGroupMove(cfg *appConfig, win, docUUID string, g *schGroup, dx, dy float64, asJSON bool, stdout, stderr io.Writer) error {
	comps, err := readPcbGroupComps(cfg, win, "group move")
	if err != nil {
		return err
	}
	targets, err := planPcbGroupMove(g, comps, dx, dy)
	if err != nil {
		return err
	}
	ids := make([]string, len(targets))
	for i, t := range targets {
		ids[i] = t.ID
	}
	_, moveErr := requestAction(cfg, "pcb.components.move", win, map[string]any{"primitiveIds": ids, "dx": dx, "dy": dy})
	// 写入报错也照样回读:动作可能「失败」却已改了一部分画布。
	type readOut struct {
		comps []pcbGroupComp
		bad   []string
	}
	ro, verified, readErr := settleRead(func() (readOut, bool, error) {
		res, rerr := requestReadAfterWrite(cfg, "pcb.components.list", win, nil, "pcb group move · readback")
		if rerr != nil {
			return readOut{}, false, rerr
		}
		after := parsePcbGroupComps(res.Result)
		bad := verifyPcbGroupMove(targets, after, pcbGroupMoveTol)
		return readOut{after, bad}, len(bad) == 0, nil
	})
	report := map[string]any{
		"ok": moveErr == nil && verified, "mode": pcbGroupMode, "documentUuid": docUUID,
		"group": g.ID, "name": g.Name, "dx": dx, "dy": dy,
		"verified": verified, "members": targets,
		"note": "virtual group: tracks/vias are not carried — re-route moved members' connections; group rotation is not supported in v1",
	}
	if moveErr != nil {
		report["moveError"] = moveErr.Error()
	}
	if readErr != nil {
		report["readbackError"] = readErr.Error()
	}
	if len(ro.bad) > 0 {
		report["mismatches"] = ro.bad
	}
	if asJSON {
		if err := writePcbGroupJSON(stdout, report); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(stdout, "virtual PCB group %s (mode=virtual): %d member(s) by (%.4g, %.4g) mil\n", describeSchGroup(g), len(targets), dx, dy)
		for _, t := range targets {
			fmt.Fprintf(stdout, "  %-6s (%.4f, %.4f) → (%.4f, %.4f)  %s\n", t.Designator, t.FromX, t.FromY, t.ToX, t.ToY, t.Status)
		}
		fmt.Fprintln(stdout, "  note: tracks/vias are not carried — re-route the moved members' connections")
	}
	switch {
	case moveErr != nil:
		return fmt.Errorf("group move write failed: %w (readback above shows what landed)", moveErr)
	case readErr != nil:
		return fmt.Errorf("group move readback unavailable: %w — `pcb list` after `doc reload` to confirm", readErr)
	case !verified:
		sort.Strings(ro.bad)
		return fmt.Errorf("group move readback mismatch (%d member(s)):\n  %s", len(ro.bad), strings.Join(ro.bad, "\n  "))
	}
	if !asJSON {
		fmt.Fprintf(stdout, "✓ verified by fresh readback: every member displaced by exactly (%.4g, %.4g)\n", dx, dy)
	}
	return nil
}
