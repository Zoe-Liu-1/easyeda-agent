package app

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// ── block-apply 的迷宫兜底(2026-10-16)──────────────────────────────────────────
//
// 干线/分支/正对只会画直线或一个弯。前三步之后因形状(够不到、没有线树)仍退回标签的
// **非地网**成员(通道被挡的 blocked 成员除外,那要回源头修;现场
// ESP32-S3R8 的 W25Q64:FLASH 两侧 8 脚,SPI 信号与 VDD_SPI 全部退回,sch check 报
// peripheral-not-direct)在这里交给布局求解器同一个 A* 绕线器 libMazeRoute 再试一次。
//
// 绕线器吃的是 powerLayoutPlan,这里用**本次规划的输入**现场拼一个适配计划,绝不重读
// 页面、也不要求读回引脚网名(读回刻意 includePinNets:false,#256):
//
//   - 件:块包络外扩 bapMazeWindow 内的每个实体件(实测 bbox + 实测引脚与引出方向);
//     块内脚的网 = 块规划的网,其余每只脚各给一个独有的外来网名,于是对块网而言
//     块外的脚全是外来脚(不能碰、出脚的最小引线也要让开)。
//   - 线:本次已规划的导线(带规划网)+ 页面已有导线(按相接分组,各组一个外来网);
//     别的网的线切成一格一段,A* 不能跨过(bapMazeWall)。
//     接在引脚上/占着引脚出口/穿件的页面线不进模型 —— 模型不知道它们属于谁,
//     放进去只会让整个基线判不清;它们照样由下面的验收拦住。
//   - marker:实测只有 bbox,拼不出 powerLayoutFlag 的方向与偏移,不进模型。
//
// 每个候选在搜索内部就过一遍规划器自己的 segClear(与现场执行同一把尺:穿件、碰脚、
// 碰页面导线、压 marker、碰别的网已规划导线),拒掉就继续搜别的落点;找到后再整根过
// routeClear(含守护进程几何守卫重放)。证不清的成员维持原退回,原因后面追加绕线器的
// 失败原因。预算:节点数用 SchematicRoutingOptions 默认值、全块共享一个上下文,另外
// 每块最多 bapMazeMaxAttempts 次绕线,block-apply 不可能卡死在这里。

const (
	bapMazeMaxAttempts = 32
	// bapMazeWindow 大于绕线器最大的外扩信封(320 raw),信封内的件都在模型里。
	bapMazeWindow = 400.0
)

type bapMazeState struct {
	ctx      *schematicRoutingContext
	attempts int
}

// bapMazePinKey 把适配计划里的脚映射回 DESIG:PIN。
type bapMazePinKey struct {
	x, y   float64
	number string
}

type bapMazeModel struct {
	plan *powerLayoutPlan
	refs map[bapMazePinKey]string
}

// mazeFallbacks 对前三步退回标签的非地网成员逐个试迷宫绕线。成功的从 Fallback 里
// 拿掉并记为已接;失败的在原原因后追加绕线器的原因。
// blocked 的成员(形状够得着但通道被挡)不绕:那是布局/采集要回源头修的问题。
func (p *bapWirePlanner) mazeFallbacks(out *bapWirePlan, wired, blocked map[string]bool) {
	p.reserveExits = true
	defer func() { p.reserveExits = false }()
	var keep []bapWireFallback
	for _, f := range out.Fallback {
		if bapNetIsGround(f.Net) || wired[f.Member] || blocked[f.Member] {
			keep = append(keep, f)
			continue
		}
		r, why := p.mazeRoute(f.Member, f.Net)
		if r == nil {
			if why != "" {
				f.Reason += ";迷宫绕线也不行: " + why
			}
			keep = append(keep, f)
			continue
		}
		p.add(*r)
		wired[f.Member] = true
		if r.Host != "" {
			wired[r.Host] = true
		}
	}
	out.Fallback = keep
}

func (p *bapWirePlanner) mazeRoute(member, net string) (*bapWireRoute, string) {
	if p.maze == nil {
		ctx, err := newSchematicRoutingContext(nil, nil)
		if err != nil {
			return nil, err.Error()
		}
		ctx.policies = map[string]string{}
		p.maze = &bapMazeState{ctx: ctx}
	}
	if p.maze.attempts >= bapMazeMaxAttempts {
		return nil, fmt.Sprintf("本块迷宫绕线次数已用完(上限 %d)", bapMazeMaxAttempts)
	}
	M, err := resolvePinCoord(p.in.Scene, member)
	if err != nil {
		return nil, "读不到引脚坐标: " + err.Error()
	}
	if why := bapMazePinContradicts(M); why != "" {
		return nil, why
	}
	model, err := p.mazeModel(net)
	if err != nil {
		return nil, err.Error()
	}
	if err := validateLibGeometry(model.plan); err != nil {
		return nil, "障碍模型本身不清白(" + err.Error() + ")"
	}
	islands := libIslands(model.plan)
	target, source := -1, -1
	for i, is := range islands {
		if is.net != net {
			continue
		}
		for _, q := range is.pins {
			if q.X == M.X && q.Y == M.Y && q.Number == M.PinNumber {
				target = i
			}
		}
	}
	if target < 0 {
		return nil, "模型里找不到这只脚"
	}
	// 源:同网已有的线树优先(含核心脚的树最优),否则核心脚(attach 宿主优先)。
	core := map[string]bool{}
	for _, host := range p.in.Attach {
		d, _, _ := strings.Cut(host, ":")
		core[d] = true
	}
	desig, _, _ := strings.Cut(member, ":")
	host := p.in.Attach[desig]
	best := 0
	for i, is := range islands {
		if i == target || is.net != net {
			continue
		}
		score := 0
		for _, q := range is.pins {
			ref := model.refs[bapMazePinKey{q.X, q.Y, q.Number}]
			d, _, _ := strings.Cut(ref, ":")
			if core[d] {
				score = max(score, 1)
			}
			if ref == host {
				score = max(score, 2)
			}
		}
		if len(is.wireIndices) > 0 {
			score += 4
		}
		if score > best {
			best, source = score, i
		}
	}
	if source < 0 {
		return nil, "同网没有线树也没有核心脚可接"
	}
	for _, q := range islands[source].pins {
		if S, err := resolvePinCoord(p.in.Scene, model.refs[bapMazePinKey{q.X, q.Y, q.Number}]); err == nil {
			if why := bapMazePinContradicts(S); why != "" {
				return nil, why
			}
		}
	}
	p.maze.attempts++
	p.maze.ctx.policies[net] = "direct"

	ends := p.mazeNetPins(net)
	var lastWhy string
	accept := func(trial *powerLayoutPlan) bool {
		for _, s := range bapMazeNewSegs(model.plan.Wires, trial.Wires, net) {
			if ok, why := p.segClear(s[0], s[1], net, ends, bapMazeOwners(s, ends)); !ok {
				lastWhy = why
				return false
			}
		}
		return true
	}
	route, err := libMazeRouteAccepted(model.plan, islands[source], islands[target], p.maze.ctx, accept)
	if err != nil {
		why := err.Error()
		var rf *schematicRoutingFailure
		if errors.As(err, &rf) {
			why = rf.Kind + ": " + rf.cause.Error()
		}
		if lastWhy != "" {
			why += "(规划器拒掉的候选:" + lastWhy + ")"
		}
		return nil, why
	}
	pts, ok := bapMazeChain(route)
	if !ok {
		return nil, "绕线结果不是一条连续折线"
	}
	pts = bapCompactRoute(pts)
	for i := 1; i < len(pts); i++ {
		s := [2][2]float64{pts[i-1], pts[i]}
		if ok, why := p.segClear(s[0], s[1], net, ends, bapMazeOwners(s, ends)); !ok {
			return nil, "绕线结果被规划器拒: " + why
		}
	}
	if ok, why := p.guardClear(pts); !ok {
		return nil, why
	}
	r := &bapWireRoute{Kind: "maze", Net: net, Member: member, Points: pts}
	if src := islands[source]; len(src.wireIndices) == 0 && len(src.pins) == 1 {
		r.Host = model.refs[bapMazePinKey{src.pins[0].X, src.pins[0].Y, src.pins[0].Number}]
	}
	return r, ""
}

// mazeNetPins 是网 net 的所有成员脚(实测坐标),作为 segClear 允许落在端点上的脚。
func (p *bapWirePlanner) mazeNetPins(net string) []acPin {
	var out []acPin
	for _, n := range p.in.Nets {
		if n.Net != net {
			continue
		}
		for _, m := range n.Members {
			if q, err := resolvePinCoord(p.in.Scene, m); err == nil {
				out = append(out, q)
			}
		}
	}
	return out
}

// bapMazeOwners:端点落在同网脚上的件,其本体交给引脚出线规则判(与干线一致)。
func bapMazeOwners(s [2][2]float64, ends []acPin) map[string]bool {
	owners := map[string]bool{}
	for _, e := range ends {
		for _, q := range s {
			if e.X == q[0] && e.Y == q[1] {
				owners[e.Designator] = true
			}
		}
	}
	return owners
}

// mazeModel 用规划器自己的输入拼适配计划(见文件头)。
// net 是这次要绕的网:别的网的导线都切成一格一段(见 bapMazeWall)。
func (p *bapWirePlanner) mazeModel(net string) (*bapMazeModel, error) {
	pinNet := map[string]string{}
	block := map[string]bool{}
	for _, n := range p.in.Nets {
		for _, m := range n.Members {
			pinNet[m] = n.Net
			d, _, _ := strings.Cut(m, ":")
			block[d] = true
		}
	}
	env := layoutBBox{MinX: math.Inf(1), MinY: math.Inf(1), MaxX: math.Inf(-1), MaxY: math.Inf(-1)}
	for _, c := range p.in.Comps {
		if c.BBox == nil || !block[c.Designator] {
			continue
		}
		env.MinX, env.MinY = math.Min(env.MinX, c.BBox.MinX), math.Min(env.MinY, c.BBox.MinY)
		env.MaxX, env.MaxY = math.Max(env.MaxX, c.BBox.MaxX), math.Max(env.MaxY, c.BBox.MaxY)
	}
	if !plBoxValid(env) {
		return nil, fmt.Errorf("读不到块内件的实测 bbox")
	}
	win := layoutBBox{MinX: env.MinX - bapMazeWindow, MinY: env.MinY - bapMazeWindow, MaxX: env.MaxX + bapMazeWindow, MaxY: env.MaxY + bapMazeWindow}
	inWin := func(b layoutBBox) bool {
		return b.MinX <= win.MaxX && b.MaxX >= win.MinX && b.MinY <= win.MaxY && b.MaxY >= win.MinY
	}
	pinsOf := map[string][]acPin{}
	for _, q := range p.in.Scene.Pins {
		pinsOf[q.Designator] = append(pinsOf[q.Designator], q)
	}
	model := &bapMazeModel{plan: &powerLayoutPlan{SchemaVersion: 1}, refs: map[bapMazePinKey]string{}}
	plan := model.plan
	seen := map[string]bool{}
	for _, c := range p.in.Comps {
		if c.BBox == nil || c.ComponentType != schLayoutPartType || seen[c.Designator] || !inWin(*c.BBox) {
			continue
		}
		seen[c.Designator] = true
		b := *c.BBox
		pl := powerLayoutPlacement{PrimitiveID: c.ID, Designator: c.Designator, BBox: b,
			X: plFloor((b.MinX + b.MaxX) / 2), Y: plFloor((b.MinY + b.MaxY) / 2)}
		if c.Rotation != nil {
			pl.Rotation = *c.Rotation
		}
		for _, q := range pinsOf[c.Designator] {
			// 成员可能按脚号也可能按脚名写(resolvePinCoord 两者都认);按名写时同名的
			// 几只脚(多只 VDD)本来就是同一网。ref 取成员表里的写法,Host 才对得上。
			ref := q.Designator + ":" + q.PinNumber
			if byName := q.Designator + ":" + q.PinName; pinNet[ref] == "" && q.PinName != "" && pinNet[byName] != "" {
				ref = byName
			}
			pin := powerLayoutPin{Number: q.PinNumber, Name: q.PinName, X: q.X, Y: q.Y, Net: pinNet[ref]}
			if pin.Net == "" {
				pin.Net = "\x00foreign:" + ref
			}
			// powerLayoutPin.Rotation 与 acPin.PinRotation 同一约定(0=右 90=上,y-UP);
			// 先按规划器同一把尺(outwardDirection)归到正交方向,两边判的引出侧必然一致。
			if q.PinRotation != nil {
				r := bapDirRotation(outwardDirection(q))
				pin.Rotation = &r
			}
			pl.Pins = append(pl.Pins, pin)
			model.refs[bapMazePinKey{q.X, q.Y, q.PinNumber}] = ref
		}
		pl.TextBBoxes = bapMazeEdgeStrips(pl)
		plan.Placements = append(plan.Placements, pl)
	}
	for _, r := range p.routes {
		pts := bapCompactRoute(r.Points)
		for i := 1; i < len(pts); i++ {
			plan.Wires = append(plan.Wires, powerLayoutWire{Net: r.Net, Points: [][2]float64{pts[i-1], pts[i]}})
		}
	}
	p.mazePageWires(plan, inWin)
	return model, nil
}

// mazePageWires 把页面已有导线作为外来网放进模型:按相接并成组,每组一个外来网;
// 任一段接在引脚上、占着引脚出口、穿件或不规整的整组都不放(见文件头)。
func (p *bapWirePlanner) mazePageWires(plan *powerLayoutPlan, inWin func(layoutBBox) bool) {
	ws := p.in.Scene.Wires
	parent := make([]int, len(ws))
	for i := range parent {
		parent[i] = i
	}
	root := func(i int) int {
		for parent[i] != i {
			i = parent[i]
		}
		return i
	}
	seg := func(w wireSegment) ([2]float64, [2]float64) { return [2]float64{w.X0, w.Y0}, [2]float64{w.X1, w.Y1} }
	for i := range ws {
		a, b := seg(ws[i])
		for j := range ws[:i] {
			c, d := seg(ws[j])
			if plSegmentsContact(a, b, c, d) {
				parent[root(i)] = root(j)
			}
		}
	}
	bad := map[int]bool{}
	for i, w := range ws {
		a, b := seg(w)
		box := layoutBBox{MinX: math.Min(a[0], b[0]), MinY: math.Min(a[1], b[1]), MaxX: math.Max(a[0], b[0]), MaxY: math.Max(a[1], b[1])}
		if !inWin(box) {
			continue
		}
		if a == b || (a[0] != b[0] && a[1] != b[1]) || !plGrid(a[0]) || !plGrid(a[1]) || !plGrid(b[0]) || !plGrid(b[1]) {
			bad[root(i)] = true
			continue
		}
		for _, c := range plan.Placements {
			if plWireEntersBody(a, b, c) {
				bad[root(i)] = true
			}
			for _, t := range c.TextBBoxes {
				if plSegmentTouchesBox(a, b, t) {
					bad[root(i)] = true
				}
			}
			for _, q := range c.Pins {
				at := [2]float64{q.X, q.Y}
				exit := at
				if side, err := libPinSide(q, c.BBox); err == nil {
					exit[0], exit[1] = endpointFor(q.X, q.Y, schAnchorGrid, side)
				}
				if plOnSegment(at, a, b) || plSegmentsContact(a, b, at, exit) {
					bad[root(i)] = true
				}
			}
		}
		for _, r := range plan.Wires {
			if plSegmentsContact(a, b, r.Points[0], r.Points[1]) {
				bad[root(i)] = true
			}
		}
	}
	var add []powerLayoutWire
	for i, w := range ws {
		a, b := seg(w)
		box := layoutBBox{MinX: math.Min(a[0], b[0]), MinY: math.Min(a[1], b[1]), MaxX: math.Max(a[0], b[0]), MaxY: math.Max(a[1], b[1])}
		if bad[root(i)] || !inWin(box) {
			continue
		}
		add = append(add, powerLayoutWire{Net: fmt.Sprintf("\x00foreign-wire:%d", root(i)), Points: [][2]float64{a, b}})
	}
	plan.Wires = append(plan.Wires, add...)
}

// bapMazeNewSegs 是候选计划里网 net 新增(或与新线合并过)的线段。
func bapMazeNewSegs(base, trial []powerLayoutWire, net string) [][2][2]float64 {
	var out [][2][2]float64
	for _, w := range trial {
		if w.Net != net || len(w.Points) != 2 {
			continue
		}
		covered := false
		for _, b := range base {
			if b.Net == net && len(b.Points) == 2 && plOnSegment(w.Points[0], b.Points[0], b.Points[1]) && plOnSegment(w.Points[1], b.Points[0], b.Points[1]) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, [2][2]float64{w.Points[0], w.Points[1]})
		}
	}
	return out
}

// bapMazeChain 把绕线器返回的逐段线接回一条折线。
func bapMazeChain(route []powerLayoutWire) ([][2]float64, bool) {
	if len(route) == 0 {
		return nil, false
	}
	pts := [][2]float64{}
	for i, w := range route {
		if len(w.Points) != 2 {
			return nil, false
		}
		if i == 0 {
			pts = append(pts, w.Points[0])
		} else if pts[len(pts)-1] != w.Points[0] {
			return nil, false
		}
		pts = append(pts, w.Points[1])
	}
	return pts, true
}

// bapMazeEdgeStrips 是件本体四条边的内侧细条,在每只脚的出口处留缺口,作为模型里
// 的「文字框」。两个原因:
//
//   - 这次读回没有实测位号文字框;不给就会落到 libPartLabelReservation 的估计带
//     (那是给「摆件」留位置用的,对已摆好的实测件只会制造假碰撞,整份模型判不清)。
//   - 绕线器只把本体**内部**当障碍(plWireEntersBody),规划器的 segClear 却把
//     本体边界也算「穿过」。细条用 plSegmentTouchesBox(闭区间)把边界补成障碍,
//     两把尺在边界上对齐,A* 不会把贴边的候选一个个送去被拒、耗光节点。
//
// 细条全在本体内侧,不改变件与件、件与 marker 的间距判定。
func bapMazeEdgeStrips(c powerLayoutPlacement) []layoutBBox {
	const t, gap = 0.2, 1.0
	b := c.BBox
	if b.MaxX-b.MinX <= 2*t || b.MaxY-b.MinY <= 2*t {
		return nil
	}
	cuts := map[string][]float64{}
	for _, q := range c.Pins {
		side, err := libPinSide(q, b)
		sides := []string{side}
		if err != nil {
			sides = []string{"left", "right", "up", "down"}
		}
		for _, s := range sides {
			if s == "left" || s == "right" {
				cuts[s] = append(cuts[s], q.Y)
			} else {
				cuts[s] = append(cuts[s], q.X)
			}
		}
	}
	var out []layoutBBox
	pieces := func(lo, hi float64, at []float64, mk func(a, z float64) layoutBBox) {
		sort.Float64s(at)
		cur := lo
		for _, v := range at {
			if v-gap > cur {
				out = append(out, mk(cur, v-gap))
			}
			cur = math.Max(cur, v+gap)
		}
		if hi > cur {
			out = append(out, mk(cur, hi))
		}
	}
	pieces(b.MinX, b.MaxX, cuts["up"], func(a, z float64) layoutBBox { return layoutBBox{MinX: a, MinY: b.MaxY - t, MaxX: z, MaxY: b.MaxY} })
	pieces(b.MinX, b.MaxX, cuts["down"], func(a, z float64) layoutBBox { return layoutBBox{MinX: a, MinY: b.MinY, MaxX: z, MaxY: b.MinY + t} })
	pieces(b.MinY, b.MaxY, cuts["left"], func(a, z float64) layoutBBox { return layoutBBox{MinX: b.MinX, MinY: a, MaxX: b.MinX + t, MaxY: z} })
	pieces(b.MinY, b.MaxY, cuts["right"], func(a, z float64) layoutBBox { return layoutBBox{MinX: b.MaxX - t, MinY: a, MaxX: b.MaxX, MaxY: z} })
	return out
}

// bapMazePinContradicts:实测引出射线(离开脚 1 raw 之后)扎回自己的本体,说明引出
// 方向与几何互相矛盾。绕线器会忠实地沿这个方向出线(落在脚与本体之间),那不是能
// 交付的线 —— 不绕,原样退回。
func bapMazePinContradicts(q acPin) string {
	dx, dy, ok := bapDirVec(outwardDirection(q))
	if !ok || q.PinRotation == nil {
		return "引脚没有实测引出方向"
	}
	if q.OwnerBBox != nil && schSegmentCrossesBox(q.X+dx, q.Y+dy, q.X+dx*1000, q.Y+dy*1000, *q.OwnerBBox) {
		return fmt.Sprintf("%s:%s 的实测引出方向(%s)指回自己的本体,方向证据自相矛盾", q.Designator, q.PinNumber, outwardDirection(q))
	}
	return ""
}

// bapMazeWall 把别的网的导线切成一格(5 raw)一段。绕线器允许正交跨过外来导线
// (中点不算接触),规划器的 segClear 却连交叉也拒(EasyEDA 的相接合并风险)。切碎后
// 外来线上每个格点都是端点,跨过去就是「接触」,A* 自己就绕开,两把尺一致。
func bapMazeWall(wires []powerLayoutWire, net string) []powerLayoutWire {
	out := make([]powerLayoutWire, 0, len(wires))
	for _, w := range wires {
		if w.Net == net || len(w.Points) != 2 {
			out = append(out, w)
			continue
		}
		a, b := w.Points[0], w.Points[1]
		n := int(math.Round((math.Abs(b[0]-a[0]) + math.Abs(b[1]-a[1])) / schAnchorGrid))
		if n <= 1 || (a[0] != b[0] && a[1] != b[1]) {
			out = append(out, w)
			continue
		}
		dx, dy := (b[0]-a[0])/float64(n), (b[1]-a[1])/float64(n)
		for i := 0; i < n; i++ {
			p := [2]float64{a[0] + dx*float64(i), a[1] + dy*float64(i)}
			q := [2]float64{a[0] + dx*float64(i+1), a[1] + dy*float64(i+1)}
			if i == n-1 {
				q = b
			}
			out = append(out, powerLayoutWire{Net: w.Net, Points: [][2]float64{p, q}})
		}
	}
	return out
}
