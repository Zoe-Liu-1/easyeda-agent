package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/zhoushoujianwork/easyeda-agent/internal/workflow"
)

// ── pure core ───────────────────────────────────────────────────────────────

func TestResolvePcbGroupMembers(t *testing.T) {
	comps := []pcbGroupComp{{ID: "p3", Designator: "H3"}, {ID: "p4", Designator: "H4"}, {ID: "px"}}
	got, err := resolvePcbGroupMembers([]string{"R1"}, []string{"p3", "p4"}, comps)
	if err != nil || strings.Join(got, ",") != "R1,H3,H4" {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := resolvePcbGroupMembers(nil, []string{"nope"}, comps); err == nil {
		t.Error("unknown primitiveId must be refused")
	}
	if _, err := resolvePcbGroupMembers(nil, []string{"px"}, comps); err == nil {
		t.Error("a component without designator cannot be a member")
	}
	if _, err := resolvePcbGroupMembers(nil, nil, comps); err == nil {
		t.Error("empty member list must be refused")
	}
}

func TestPcbGroupViolations(t *testing.T) {
	groups, _, _ := groupsCreate(nil, "mount", []string{"H3", "H4"})
	groups, _, _ = groupsCreate(groups, "solo", []string{"U1"})
	cases := []struct {
		name  string
		sel   []string
		rigid bool
		want  int
	}{
		{"rigid partial", []string{"H3"}, true, 1},
		{"rigid whole group", []string{"H3", "H4", "R9"}, true, 0},
		{"rigid unrelated", []string{"R9"}, true, 0},
		{"non-rigid whole group still breaks it", []string{"h3", "H4"}, false, 1},
		{"non-rigid single-member group is free", []string{"U1", "R9"}, false, 0},
	}
	for _, c := range cases {
		if got := pcbGroupViolations(groups, c.sel, c.rigid); len(got) != c.want {
			t.Errorf("%s: got %d violated, want %d", c.name, len(got), c.want)
		}
	}
	err := pcbGroupViolationError("pcb align", pcbGroupViolations(groups, []string{"H3"}, false), false)
	if err == nil || !strings.Contains(err.Error(), "--break-group") || !strings.Contains(err.Error(), "mode=virtual") {
		t.Errorf("refusal must name the override and the virtual mode: %v", err)
	}
}

func TestPlanAndVerifyPcbGroupMove(t *testing.T) {
	g := &schGroup{ID: "g1", Members: []string{"H3", "H4"}}
	comps := []pcbGroupComp{
		{ID: "p3", Designator: "H3", X: 590, Y: 3570, Rotation: 90, Layer: 1},
		{ID: "p4", Designator: "H4", X: 890, Y: 3570, Rotation: -90, Layer: 1},
	}
	targets, err := planPcbGroupMove(g, comps, 100, -25.5)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].ToX != 690 || targets[0].ToY != 3544.5 || targets[1].ToX != 990 {
		t.Fatalf("exact per-member targets wrong: %+v", targets)
	}
	// 相对几何不变:中心距仍是 300。
	if d := targets[1].ToX - targets[0].ToX; d != 300 {
		t.Errorf("relative geometry changed: %v", d)
	}
	after := []pcbGroupComp{
		{ID: "p3", Designator: "H3", X: 690, Y: 3544.5, Rotation: 90, Layer: 1},
		{ID: "p4", Designator: "H4", X: 990, Y: 3544.5, Rotation: 270, Layer: 1}, // -90 ≡ 270
	}
	if bad := verifyPcbGroupMove(targets, after, pcbGroupMoveTol); len(bad) != 0 {
		t.Fatalf("exact readback must verify, got %v", bad)
	}
	after[1].X = 890 // H4 没动
	if bad := verifyPcbGroupMove(targets, after, pcbGroupMoveTol); len(bad) != 1 || !strings.Contains(bad[0], "H4") {
		t.Fatalf("a member left behind must be reported, got %v", bad)
	}
	if bad := verifyPcbGroupMove(targets, after[:1], pcbGroupMoveTol); len(bad) != 1 || !strings.Contains(bad[0], "not in the readback") {
		t.Fatalf("a member missing from the readback must be reported, got %v", bad)
	}

	if _, err := planPcbGroupMove(g, comps[:1], 10, 0); err == nil || !strings.Contains(err.Error(), "H4") {
		t.Errorf("stale member must refuse the whole move: %v", err)
	}
	locked := append([]pcbGroupComp(nil), comps...)
	locked[1].Locked = true
	if _, err := planPcbGroupMove(g, locked, 10, 0); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Errorf("locked member must refuse the whole move: %v", err)
	}
}

// PCB 组与原理图组同表存放,跨页的原理图消费者必须把它们挡在外面。
func TestPcbGroupsHiddenFromSchCrossPageReaders(t *testing.T) {
	st := &workflow.State{}
	sch, _, _ := groupsCreate(nil, "mcu", []string{"U1", "C1"})
	pcb, _, _ := groupsCreate(nil, "mount", []string{"H3", "H4"})
	st.SetGroupsForPage("schDoc", sch)
	st.SetGroupsForPage("pcbDoc", markPcbGroups(pcb))

	if got := schOnlyGroups(st.GroupsForPage("pcbDoc")); len(got) != 0 {
		t.Errorf("schOnlyGroups must drop PCB groups, got %+v", got)
	}
	sources, skipped := specCollectGroupsLive(st, "", map[string]bool{"schDoc": true})
	if len(sources) != 1 || sources[0].Name != "mcu" {
		t.Errorf("spec backfill must only see the schematic group, got %+v", sources)
	}
	if len(skipped) != 0 {
		t.Errorf("a PCB-only doc is not a skipped schematic page, got %v", skipped)
	}
}

// ── planners keep group members fixed ───────────────────────────────────────

func TestPlanAutoPlace_GroupMembersFixed(t *testing.T) {
	comps := ceshiBoard()
	opt := defaultApOptions()
	opt.fixed = map[string]bool{"C1": true, "C2": true}
	moves, diags := planAutoPlace(comps, opt)
	byDes := map[string]apComp{}
	for _, c := range comps {
		byDes[c.designator] = c
	}
	for _, m := range moves {
		if m.Designator == "C1" || m.Designator == "C2" {
			t.Fatalf("group member %s moved: %+v", m.Designator, m)
		}
		// 其余卫星不得落到固定件身上。
		c := byDes[m.Designator]
		hw, hh := c.width()/2, c.height()/2
		for _, f := range []string{"C1", "C2"} {
			fc := byDes[f]
			if m.NewX-hw < fc.maxX && fc.minX < m.NewX+hw && m.NewY-hh < fc.maxY && fc.minY < m.NewY+hh {
				t.Errorf("%s planned onto fixed group member %s", m.Designator, f)
			}
		}
	}
	notes := 0
	for _, d := range diags {
		if strings.HasPrefix(d.Reason, "group:fixed") {
			notes++
		}
	}
	if notes != 2 {
		t.Errorf("want 2 group:fixed diags, got %+v", diags)
	}
	// --break-group(fixed 为空)恢复旧行为:C1/C2 照常被搬。
	moves, _ = planAutoPlace(ceshiBoard(), defaultApOptions())
	found := 0
	for _, m := range moves {
		if m.Designator == "C1" || m.Designator == "C2" {
			found++
		}
	}
	if found != 2 {
		t.Errorf("without fixed set C1/C2 must move as before, got %d", found)
	}
}

func TestPlanAutoPlace_FixedMainVetoesChipSpacing(t *testing.T) {
	opt := defaultApOptions()
	opt.fixed = map[string]bool{"U2": true}
	moves, diags := planAutoPlace(twoChipBoard(), opt)
	for _, m := range moves {
		if m.Via == "chip-spacing" {
			t.Errorf("a fixed main chip must not be spaced: %+v", m)
		}
	}
	vetoed := false
	for _, d := range diags {
		vetoed = vetoed || strings.Contains(d.Reason, "chip-spacing skipped")
	}
	if !vetoed {
		t.Errorf("the skipped spacing must be reported, diags=%+v", diags)
	}
}

func TestPlanConstrainedPlace_GroupMembersFixed(t *testing.T) {
	board := cpRect{x0: 0, y0: 0, x1: 2400, y1: 1700}
	mk := func() []cpComp {
		return []cpComp{
			mkCP("U2", "esp32-s3-wroom-1", 1, 4799, -1088, 748, 1030, 41),
			mkCP("J1", "conn.usb_c_16p", 1, 1200, 850, 474, 351, 16),
			mkCP("C1", "cap.100nf", 1, 1710, 331, 75, 45, 2),
			mkCP("R1", "res.10k", 1, 3955, -395, 80, 45, 2),
		}
	}
	opt := defaultCpOptions()
	opt.board = &board
	before, _ := planConstrainedPlace(mk(), nil, opt)
	movedBefore := map[string]bool{}
	for _, m := range before {
		movedBefore[m.Designator] = true
	}
	if !movedBefore["J1"] || !movedBefore["C1"] {
		t.Fatalf("precondition: J1/C1 are moved without a group, got %+v", before)
	}

	opt.fixed = map[string]bool{"J1": true, "C1": true}
	moves, diags := planConstrainedPlace(mk(), nil, opt)
	for _, m := range moves {
		if m.Designator == "J1" || m.Designator == "C1" {
			t.Errorf("group member %s moved: %+v", m.Designator, m)
		}
	}
	n := 0
	for _, d := range diags {
		if strings.HasPrefix(d.Reason, "group:fixed") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("want 2 group:fixed diags, got %+v", diags)
	}
}

func TestRefineLoop_GroupMembersImmovable(t *testing.T) {
	cfg, d, done := newRefineLoopDaemon(t, refineLoopBoard())
	defer done()
	opts := defaultRefineOpts()
	opts.GroupFixed = map[string]string{"C1": "member of virtual group g1", "C2": "member of virtual group g1"}
	rep, err := runRefineLoop(cfg, "", nil, opts, 0, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range d.modifies {
		if m.id == "p1" || m.id == "p2" {
			t.Errorf("group member %s was snapped individually", m.id)
		}
	}
	if rep.Immovable != 2 {
		t.Errorf("immovable = %d, want the 2 group members", rep.Immovable)
	}
}

// ── command level (fake daemon) ─────────────────────────────────────────────

type fakePcbGroupBoard struct {
	mu         sync.Mutex
	doc        string
	docType    string
	comps      []map[string]any
	ignoreMove map[string]bool
}

func (b *fakePcbGroupBoard) respond(_ int, call autolayoutTestCall) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	ctx := fmt.Sprintf(`"context":{"documentUuid":%q,"documentType":%q,"projectUuid":"P1"}`, b.doc, b.docType)
	switch call.Action {
	case "document.current":
		return fmt.Sprintf(`{"ok":true,"result":{"uuid":%q,"documentType":%q,"parentProjectUuid":"P1"},%s}`, b.doc, b.docType, ctx)
	case "project.current":
		return `{"ok":true,"result":{"uuid":"P1","friendlyName":"grp-test"}}`
	case "pcb.components.list":
		raw, _ := json.Marshal(map[string]any{"components": b.comps})
		return `{"ok":true,"result":` + string(raw) + `,` + ctx + `}`
	case "pcb.components.move":
		dx, _ := call.Payload["dx"].(float64)
		dy, _ := call.Payload["dy"].(float64)
		ids, _ := call.Payload["primitiveIds"].([]any)
		for _, id := range ids {
			if b.ignoreMove[id.(string)] {
				continue
			}
			for _, c := range b.comps {
				if c["primitiveId"] == id {
					c["x"] = c["x"].(float64) + dx
					c["y"] = c["y"].(float64) + dy
				}
			}
		}
		return `{"ok":true,"result":{}}`
	}
	return `{"ok":true,"result":{}}`
}

func newFakePcbGroupBoard() *fakePcbGroupBoard {
	return &fakePcbGroupBoard{doc: "pcbA", docType: "pcb", comps: []map[string]any{
		{"primitiveId": "p3", "designator": "H3", "x": 590.0, "y": 3570.0, "rotation": 90.0, "layer": 1.0},
		{"primitiveId": "p4", "designator": "H4", "x": 890.0, "y": 3570.0, "rotation": -90.0, "layer": 1.0},
		{"primitiveId": "pr", "designator": "R1", "x": 100.0, "y": 100.0, "rotation": 0.0, "layer": 1.0},
	}}
}

func runPcbCmd(t *testing.T, cfg *appConfig, args ...string) (string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	cmd := newPcbCmd(cfg, &out, &errb)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errb.String(), err
}

func countCalls(d *autolayoutTestDaemon, action string) int {
	n := 0
	for _, c := range d.snapshot() {
		if c.Action == action {
			n++
		}
	}
	return n
}

func TestPcbGroupCommands_CRUDPerDocument(t *testing.T) {
	t.Setenv(workflow.EnvDir, t.TempDir())
	board := newFakePcbGroupBoard()
	cfg, _, done := newAutolayoutTestDaemon(t, board.respond)
	defer done()

	out, _, err := runPcbCmd(t, cfg, "group", "create", "--ids", "p3,p4", "--name", "mount-pair")
	if err != nil || !strings.Contains(out, "mode=virtual") {
		t.Fatalf("create: %v %q", err, out)
	}
	st, _ := loadPcbStageState("grp-test")
	gs := st.GroupsForPage("pcbA")
	if len(gs) != 1 || strings.Join(gs[0].Members, ",") != "H3,H4" || !gs[0].IsPCB() {
		t.Fatalf("stored group wrong: %+v", gs)
	}

	if _, _, err := runPcbCmd(t, cfg, "group", "add", "--group", "mount-pair", "--designators", "R1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runPcbCmd(t, cfg, "group", "remove", "--group", "g1", "--designators", "R1"); err != nil {
		t.Fatal(err)
	}

	// 另一块板:组表隔离。
	board.mu.Lock()
	board.doc = "pcbB"
	board.mu.Unlock()
	out, _, err = runPcbCmd(t, cfg, "group", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var lj struct {
		Mode   string `json:"mode"`
		Groups []any  `json:"groups"`
	}
	_ = json.Unmarshal([]byte(out), &lj)
	if lj.Mode != "virtual" || len(lj.Groups) != 0 {
		t.Fatalf("pcbB must not see pcbA's groups: %s", out)
	}

	// 回到 A,H4 已从板上消失 → stale。
	board.mu.Lock()
	board.doc = "pcbA"
	board.comps = board.comps[:1]
	board.mu.Unlock()
	out, _, err = runPcbCmd(t, cfg, "group", "list")
	if err != nil || !strings.Contains(out, "H4(stale)") || !strings.Contains(out, "mode=virtual") {
		t.Fatalf("list must mark H4 stale: %v %q", err, out)
	}

	if _, _, err := runPcbCmd(t, cfg, "group", "ungroup", "--group", "g1"); err != nil {
		t.Fatal(err)
	}
	st, _ = loadPcbStageState("grp-test")
	if len(st.GroupsForPage("pcbA")) != 0 {
		t.Error("ungroup must clear the table")
	}
}

func TestPcbGroupCommands_RefuseWhenSchematicActive(t *testing.T) {
	t.Setenv(workflow.EnvDir, t.TempDir())
	board := newFakePcbGroupBoard()
	board.docType = "schematic"
	cfg, _, done := newAutolayoutTestDaemon(t, board.respond)
	defer done()
	_, _, err := runPcbCmd(t, cfg, "group", "create", "--designators", "H3,H4")
	if err == nil || !strings.Contains(err.Error(), "not a pcb") {
		t.Fatalf("a schematic in front must be refused clearly, got %v", err)
	}
}

func seedPcbGroup(t *testing.T, doc string, members ...string) {
	t.Helper()
	st, err := loadPcbStageState("grp-test")
	if err != nil {
		t.Fatal(err)
	}
	gs, _, err := groupsCreate(nil, "mount", members)
	if err != nil {
		t.Fatal(err)
	}
	if err := savePcbGroups(st, doc, gs); err != nil {
		t.Fatal(err)
	}
}

func TestPcbLayoutWrites_GuardGroups(t *testing.T) {
	t.Setenv(workflow.EnvDir, t.TempDir())
	seedPcbGroup(t, "pcbA", "H3", "H4")
	board := newFakePcbGroupBoard()
	cfg, d, done := newAutolayoutTestDaemon(t, board.respond)
	defer done()

	cases := []struct {
		args    []string
		action  string
		refused bool
	}{
		{[]string{"modify", "--id", "p3", "--patch", `{"x":1}`}, "pcb.component.modify", true},
		{[]string{"modify", "--id", "p3", "--patch", `{"x":1}`, "--break-group"}, "pcb.component.modify", false},
		{[]string{"modify", "--id", "p3", "--patch", `{"designator":"H9"}`}, "pcb.component.modify", false},
		{[]string{"modify", "--id", "pr", "--patch", `{"rotation":90}`}, "pcb.component.modify", false},
		{[]string{"move", "--ids", "p3", "--dx", "10"}, "pcb.components.move", true},
		{[]string{"move", "--ids", "p3,p4,pr", "--dx", "10"}, "pcb.components.move", false},
		{[]string{"align", "--ids", "p3,p4", "--mode", "left"}, "pcb.align", true},
		{[]string{"align", "--ids", "p3,p4", "--mode", "left", "--break-group"}, "pcb.align", false},
		{[]string{"distribute", "--ids", "p3,pr", "--axis", "x"}, "pcb.distribute", true},
		{[]string{"grid-snap", "--ids", "p4", "--grid", "50"}, "pcb.grid_snap", true},
		{[]string{"arrange", "--ids", "p3,p4,pr"}, "pcb.components.arrange", true},
	}
	for _, c := range cases {
		before := countCalls(d, c.action)
		_, _, err := runPcbCmd(t, cfg, c.args...)
		sent := countCalls(d, c.action) - before
		if c.refused {
			if err == nil || !strings.Contains(err.Error(), "--break-group") || sent != 0 {
				t.Errorf("%v: want refusal with no %s sent, got err=%v sent=%d", c.args, c.action, err, sent)
			}
		} else if err != nil || sent != 1 {
			t.Errorf("%v: want %s dispatched once, got err=%v sent=%d", c.args, c.action, err, sent)
		}
	}
}

// 组表读不到(当前不是 PCB)只降级成提示,主命令照常执行。
func TestPcbLayoutWrites_GuardDegradesToNote(t *testing.T) {
	t.Setenv(workflow.EnvDir, t.TempDir())
	board := newFakePcbGroupBoard()
	board.docType = "schematic"
	cfg, d, done := newAutolayoutTestDaemon(t, board.respond)
	defer done()
	_, errOut, err := runPcbCmd(t, cfg, "move", "--ids", "p3", "--dx", "10")
	if err != nil || countCalls(d, "pcb.components.move") != 1 {
		t.Fatalf("guard load failure must not block: err=%v", err)
	}
	if !strings.Contains(errOut, "group integrity check unavailable") {
		t.Errorf("degradation must be noted on stderr, got %q", errOut)
	}
}

func TestPcbGroupMove_VerifiedAndMismatch(t *testing.T) {
	t.Setenv(workflow.EnvDir, t.TempDir())
	seedPcbGroup(t, "pcbA", "H3", "H4")
	board := newFakePcbGroupBoard()
	cfg, d, done := newAutolayoutTestDaemon(t, board.respond)
	defer done()

	out, _, err := runPcbCmd(t, cfg, "group", "move", "--group", "g1", "--dx", "100", "--dy", "-20", "--json")
	if err != nil {
		t.Fatalf("clean move must verify: %v\n%s", err, out)
	}
	var rep struct {
		OK       bool   `json:"ok"`
		Mode     string `json:"mode"`
		Verified bool   `json:"verified"`
		Members  []pcbGroupMoveTarget
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.OK || !rep.Verified || rep.Mode != "virtual" || len(rep.Members) != 2 {
		t.Fatalf("report wrong: %s", out)
	}
	if rep.Members[0].ToX != 690 || rep.Members[0].ToY != 3550 || rep.Members[1].ToX != 990 {
		t.Errorf("targets wrong: %+v", rep.Members)
	}
	var moveCall autolayoutTestCall
	for _, c := range d.snapshot() {
		if c.Action == "pcb.components.move" {
			moveCall = c
		}
	}
	if ids, _ := moveCall.Payload["primitiveIds"].([]any); len(ids) != 2 {
		t.Errorf("one rigid move over both members expected, got %+v", moveCall.Payload)
	}

	// 平台只搬了一件:回读不一致 → 非零退出,并点名没到位的成员。
	board.mu.Lock()
	board.ignoreMove = map[string]bool{"p4": true}
	board.mu.Unlock()
	_, _, err = runPcbCmd(t, cfg, "group", "move", "--group", "g1", "--dx", "10")
	if err == nil || !strings.Contains(err.Error(), "H4") || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("readback mismatch must fail naming H4, got %v", err)
	}
}

func TestPcbDelete_CascadesGroupMembership(t *testing.T) {
	t.Setenv(workflow.EnvDir, t.TempDir())
	seedPcbGroup(t, "pcbA", "H3", "H4", "R1")
	board := newFakePcbGroupBoard()
	cfg, _, done := newAutolayoutTestDaemon(t, func(i int, call autolayoutTestCall) string {
		if call.Action == "pcb.component.delete" {
			board.mu.Lock()
			var kept []map[string]any
			for _, c := range board.comps {
				if c["primitiveId"] != "p4" {
					kept = append(kept, c)
				}
			}
			board.comps = kept
			board.mu.Unlock()
			return `{"ok":true,"result":{}}`
		}
		return board.respond(i, call)
	})
	defer done()
	_, errOut, err := runPcbCmd(t, cfg, "delete", "--ids", "p4")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := loadPcbStageState("grp-test")
	gs := st.GroupsForPage("pcbA")
	if len(gs) != 1 || strings.Join(gs[0].Members, ",") != "H3,R1" || !gs[0].IsPCB() {
		t.Fatalf("H4 must be cascaded out of the group, got %+v (stderr %q)", gs, errOut)
	}
}
