package app

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/zhoushoujianwork/easyeda-agent/internal/blocks"
	"github.com/zhoushoujianwork/easyeda-agent/internal/schguard"
)

// ── block-apply 的真实导线(2026-10-16)───────────────────────────────────────────
//
// 「同名标签不算直连」:外围件必须用实际导线接到核心件的引脚(sch check 的
// peripheral-not-direct,判据 ValidateSchematicPeripheralDirect)。block-apply 过去只给
// 「两脚正对、同一条线上」的块内两脚网拉直线(bapFacingPair),其余全交 autoconnect
// 挂同名标签 —— 现场 ESP32-S3R8 的 15 个外围件全部只靠标签连到 U。
//
// 这里按**实测引脚**规划导线,形状与布局求解器的干线契约(bsl_attach_trunk.go)相同:
//
//   - 干线(trunk):attach 外围件的接网脚 → 拐点 → 宿主脚,拐点在宿主脚的引出射线上,
//     同一只宿主脚的后续件把干线继续往外延或 T 到已有干线上;两段都从各自引脚朝外离开。
//   - 分支(branch):信号网里别的脚沿自己的引出方向直线 T 到同网已规划的导线上。
//   - 正对(facing):块内两脚网、两脚在一条线上相向 —— 原 bapWireDirectPairs 的规则。
//
// 每一段都要可证明地清白:不穿别的件、不碰别的脚、不碰已有导线与 marker、与别的网的
// 已规划导线不相接也不交叉(EasyEDA 会把相接的共线导线合并成一条,当场串网);最后把
// 整组导线交给守护进程同一把几何尺(schguard.AnalyzeWireGeometry)重放,只认本次新增
// 的发现。证不清的成员退回 autoconnect 标签,并结构化地写进清单 —— 绝不静默。

// bapWireRoute 是一根要画的导线。
type bapWireRoute struct {
	Kind   string       `json:"kind"` // trunk | branch | facing
	Net    string       `json:"net"`
	Host   string       `json:"host,omitempty"` // 干线挂在哪只核心脚上(DESIG:PIN)
	Member string       `json:"member"`         // 这根线落到的脚(DESIG:PIN)
	Points [][2]float64 `json:"points"`
}

// bapWireFallback 是一个没能用导线接上、退回标签的成员。
type bapWireFallback struct {
	Member string `json:"member"`
	Net    string `json:"net"`
	Host   string `json:"host,omitempty"`
	Reason string `json:"reason"`
}

// bapWireLabel 是整网都被导线接住的端口网要在干线末端补的那支标签。
type bapWireLabel struct {
	Net      string  `json:"net"`
	Kind     string  `json:"kind"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Rotation float64 `json:"rotation"` // 引出方向(0=右 90=上 180=左 270=下)
	Host     string  `json:"host"`
}

// bapWirePlan 是纯规划的结果。
type bapWirePlan struct {
	Routes   []bapWireRoute
	Labels   []bapWireLabel
	Fallback []bapWireFallback
}

// bapWireReport 进 manifest:画上了哪些线、哪些成员退回了标签、为什么。
type bapWireReport struct {
	Wired    []bapWireRoute    `json:"wired,omitempty"`
	Fallback []bapWireFallback `json:"fallback,omitempty"`
	Labels   []bapWireLabel    `json:"labels,omitempty"`
	Skipped  string            `json:"skipped,omitempty"` // 整步没跑(读不到几何等)
}

// bapWireInput 是纯规划的输入。
type bapWireInput struct {
	Nets []bapNet
	// Attach 是外围件位号 → 它贴的核心脚(DESIG:PIN),来自块模板的 schematic_layout.attach。
	Attach map[string]string
	Scene  acScene
	Comps  []layoutComp
	// Raw 是同一次 components.list 的原始结果(含 components/wires),用来按守护进程的
	// 几何守卫重放;nil = 不重放(纯函数测试可不给)。
	Raw map[string]any
}

type bapPlannedSeg struct {
	A, B [2]float64
	Net  string
}

func bapDirVec(dir string) (float64, float64, bool) {
	switch dir {
	case "left":
		return -1, 0, true
	case "right":
		return 1, 0, true
	case "up":
		return 0, 1, true
	case "down":
		return 0, -1, true
	}
	return 0, 0, false
}

func bapDirRotation(dir string) float64 {
	return map[string]float64{"right": 0, "up": 90, "left": 180, "down": 270}[dir]
}

// bapSegsIntersect:两段正交线段的包围盒相交(含端点相接与交叉)。
func bapSegsIntersect(a, b, c, d [2]float64) bool {
	const eps = 0.01
	return math.Min(a[0], b[0]) <= math.Max(c[0], d[0])+eps && math.Max(a[0], b[0]) >= math.Min(c[0], d[0])-eps &&
		math.Min(a[1], b[1]) <= math.Max(c[1], d[1])+eps && math.Max(a[1], b[1]) >= math.Min(c[1], d[1])-eps
}

type bapWirePlanner struct {
	in       bapWireInput
	planned  []bapPlannedSeg
	routes   []bapWireRoute
	baseline []schguard.Finding
	rawWires []any
}

// segClear 判一段新线 a→b(网 net)是否清白。ends 是允许挨着的引脚(本段两端的脚),
// owners 是这根线连接的件(它们的本体交给守卫重放按引脚出线规则判)。
func (p *bapWirePlanner) segClear(a, b [2]float64, net string, ends []acPin, owners map[string]bool) (bool, string) {
	if a == b {
		return false, "零长线段"
	}
	if a[0] != b[0] && a[1] != b[1] {
		return false, "非正交线段"
	}
	if !plGrid(a[0]) || !plGrid(a[1]) || !plGrid(b[0]) || !plGrid(b[1]) {
		return false, "线段端点不在 5 网格上(判定坐标必须等于落地坐标)"
	}
	pa, pb := acPin{X: a[0], Y: a[1]}, acPin{X: b[0], Y: b[1]}
	for _, e := range ends {
		if e.X == a[0] && e.Y == a[1] {
			pa = e
		}
		if e.X == b[0] && e.Y == b[1] {
			pb = e
		}
	}
	for _, c := range p.in.Comps {
		if c.BBox == nil || owners[c.Designator] || c.ComponentType != schLayoutPartType {
			continue
		}
		if schSegmentCrossesBox(a[0], a[1], b[0], b[1], *c.BBox) {
			return false, "穿过 " + c.Designator + " 的本体"
		}
	}
	for _, q := range p.in.Scene.Pins {
		if (q.Designator == pa.Designator && q.PinNumber == pa.PinNumber && pa.Designator != "") ||
			(q.Designator == pb.Designator && q.PinNumber == pb.PinNumber && pb.Designator != "") {
			continue
		}
		if onSegmentPoint(q.X, q.Y, a[0], a[1], b[0], b[1]) {
			return false, fmt.Sprintf("碰到别的引脚 %s:%s", q.Designator, q.PinNumber)
		}
	}
	for _, w := range p.in.Scene.Wires {
		if bapSegsIntersect(a, b, [2]float64{w.X0, w.Y0}, [2]float64{w.X1, w.Y1}) {
			return false, "碰到页面已有导线(会被自动合并)"
		}
	}
	for _, f := range p.in.Scene.Flags {
		if schSegmentCrossesBox(a[0], a[1], b[0], b[1], f) {
			return false, "穿过已有 marker"
		}
	}
	for _, s := range p.planned {
		if s.Net != net && bapSegsIntersect(a, b, s.A, s.B) {
			return false, "与别的网(" + s.Net + ")已规划的导线相接/交叉"
		}
	}
	return true, ""
}

// guardClear 把已规划的线 + 这根新线交给守护进程的几何尺重放,只认新增发现。
func (p *bapWirePlanner) guardClear(points [][2]float64) (bool, string) {
	if p.in.Raw == nil {
		return true, ""
	}
	wires := append([]any(nil), p.rawWires...)
	for i, r := range append(append([]bapWireRoute(nil), p.routes...), bapWireRoute{Points: points}) {
		pts := make([]any, 0, len(r.Points))
		for _, q := range r.Points {
			pts = append(pts, []any{q[0], q[1]})
		}
		wires = append(wires, map[string]any{"primitiveId": fmt.Sprintf("planned-%d", i), "points": pts})
	}
	cand := map[string]any{"components": p.in.Raw["components"], "wires": wires, "wiresAvailable": true}
	if f := schguard.NewGeometryFindings(p.baseline, schguard.AnalyzeWireGeometry(cand)); len(f) > 0 {
		return false, "几何守卫会拒:" + f[0].Type + " — " + f[0].Message
	}
	return true, ""
}

func (p *bapWirePlanner) routeClear(points [][2]float64, net string, ends []acPin, owners map[string]bool) (bool, string) {
	for i := 1; i < len(points); i++ {
		if ok, why := p.segClear(points[i-1], points[i], net, ends, owners); !ok {
			return false, why
		}
	}
	return p.guardClear(points)
}

func (p *bapWirePlanner) add(r bapWireRoute) {
	p.routes = append(p.routes, r)
	for i := 1; i < len(r.Points); i++ {
		p.planned = append(p.planned, bapPlannedSeg{A: r.Points[i-1], B: r.Points[i], Net: r.Net})
	}
}

// bapPlanBlockWires 是纯规划:按实测几何给块内连接规划真实导线。
func bapPlanBlockWires(in bapWireInput) bapWirePlan {
	in.Scene = bapSnapScene(in.Scene)
	p := &bapWirePlanner{in: in}
	if in.Raw != nil {
		p.baseline = schguard.AnalyzeWireGeometry(in.Raw)
		p.rawWires, _ = in.Raw["wires"].([]any)
	}
	var out bapWirePlan
	wired := map[string]bool{}
	failed := map[string]bool{}
	fail := func(member, net, host, why string) {
		failed[member] = true
		out.Fallback = append(out.Fallback, bapWireFallback{Member: member, Net: net, Host: host, Reason: why})
	}
	core := map[string]bool{} // 核心件位号(attach 的目标)
	for _, host := range in.Attach {
		d, _, _ := strings.Cut(host, ":")
		core[d] = true
	}

	// ① 正对的块内两脚网(原 bapWireDirectPairs 规则,现在与其余导线共用清白判据)。
	for _, n := range in.Nets {
		if n.Port != "" || len(n.Members) != 2 {
			continue
		}
		a, aerr := resolvePinCoord(in.Scene, n.Members[0])
		b, berr := resolvePinCoord(in.Scene, n.Members[1])
		if aerr != nil || berr != nil || a.Designator == b.Designator || !bapFacingPair(a, b) {
			continue
		}
		pts := [][2]float64{{a.X, a.Y}, {b.X, b.Y}}
		if ok, _ := p.routeClear(pts, n.Net, []acPin{a, b}, map[string]bool{a.Designator: true, b.Designator: true}); !ok {
			continue // 正对直线清不了就照旧交给 autoconnect(这类网本来就允许标签)
		}
		p.add(bapWireRoute{Kind: "facing", Net: n.Net, Member: n.Members[1], Host: n.Members[0], Points: pts})
		wired[n.Members[0]], wired[n.Members[1]] = true, true
	}

	// ② 干线:attach 外围件 → 它贴的核心脚。地网不画(地不参与直连判据,标签即可)。
	type tree struct {
		host     string
		pin      acPin
		dir      string
		end      float64
		net      string
		hasRoute bool
	}
	var trees []*tree
	for _, n := range in.Nets {
		if bapNetIsGround(n.Net) {
			continue
		}
		inNet := map[string]bool{}
		for _, m := range n.Members {
			inNet[m] = true
		}
		byHost := map[string][]string{}
		for _, m := range n.Members {
			desig, _, _ := strings.Cut(m, ":")
			host, ok := in.Attach[desig]
			if !ok || !inNet[host] || wired[m] {
				continue
			}
			byHost[host] = append(byHost[host], m)
		}
		hosts := make([]string, 0, len(byHost))
		for h := range byHost {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		for _, host := range hosts {
			members := byHost[host]
			P, err := resolvePinCoord(in.Scene, host)
			if err != nil {
				for _, m := range members {
					fail(m, n.Net, host, "读不到核心脚坐标: "+err.Error())
				}
				continue
			}
			dir := outwardDirection(P)
			dx, dy, ok := bapDirVec(dir)
			if !ok || P.PinRotation == nil {
				for _, m := range members {
					fail(m, n.Net, host, "核心脚没有实测引出方向")
				}
				continue
			}
			t := &tree{host: host, pin: P, dir: dir, net: n.Net}
			trees = append(trees, t)
			type cand struct {
				ref   string
				pin   acPin
				along float64
			}
			var cs []cand
			for _, m := range members {
				M, err := resolvePinCoord(in.Scene, m)
				if err != nil {
					fail(m, n.Net, host, "读不到外围脚坐标: "+err.Error())
					continue
				}
				cs = append(cs, cand{m, M, (M.X-P.X)*dx + (M.Y-P.Y)*dy})
			}
			sort.SliceStable(cs, func(i, j int) bool { return cs[i].along < cs[j].along })
			for _, c := range cs {
				M := c.pin
				um := outwardDirection(M)
				ux, uy, uok := bapDirVec(um)
				if M.PinRotation == nil || !uok {
					fail(c.ref, n.Net, host, "外围脚没有实测引出方向")
					continue
				}
				if c.along <= 0 {
					fail(c.ref, n.Net, host, "外围脚不在核心脚的引出方向一侧")
					continue
				}
				corner := [2]float64{P.X + dx*c.along, P.Y + dy*c.along}
				start := [2]float64{P.X, P.Y}
				if t.end > 0 {
					start = [2]float64{P.X + dx*t.end, P.Y + dy*t.end}
				}
				mp := [2]float64{M.X, M.Y}
				var pts [][2]float64
				switch {
				case corner == mp: // 外围脚就在干线上:必须迎着核心脚
					if ux != -dx || uy != -dy || c.along <= t.end {
						fail(c.ref, n.Net, host, fmt.Sprintf("外围脚落在干线上但不迎着核心脚(外围脚 (%g,%g) 朝 %s,核心脚 (%g,%g))", M.X, M.Y, um, P.X, P.Y))
						continue
					}
					pts = [][2]float64{start, mp}
				default:
					// 落线要沿外围脚自己的引出方向走到拐点
					vx, vy := corner[0]-mp[0], corner[1]-mp[1]
					l := math.Hypot(vx, vy)
					if math.Abs(vx/l-ux) > 1e-6 || math.Abs(vy/l-uy) > 1e-6 {
						fail(c.ref, n.Net, host, fmt.Sprintf("外围脚 (%g,%g) 朝 %s,拐点 (%g,%g) 不在它的引出方向上(需要两个以上的弯)", M.X, M.Y, um, corner[0], corner[1]))
						continue
					}
					if c.along > t.end {
						pts = [][2]float64{start, corner, mp}
					} else {
						pts = [][2]float64{corner, mp}
					}
				}
				ends := []acPin{M}
				if start == [2]float64{P.X, P.Y} {
					ends = append(ends, P)
				}
				owners := map[string]bool{P.Designator: true, M.Designator: true}
				if ok, why := p.routeClear(pts, n.Net, ends, owners); !ok {
					fail(c.ref, n.Net, host, why)
					continue
				}
				p.add(bapWireRoute{Kind: "trunk", Net: n.Net, Host: host, Member: c.ref, Points: pts})
				wired[c.ref], wired[host], t.hasRoute = true, true, true
				if c.along > t.end {
					t.end = c.along
				}
			}
		}

		// ③ 分支:信号网里其余的脚沿自己的引出方向直线 T 到同网已规划的导线上。电源网
		//    的其余脚(上拉的 3V3 端之类)留给标签 —— 直连判据只要求信号分支成一棵树。
		if tidyNetClass(n.Net) != "signal" {
			continue
		}
		hasCore := false
		for _, m := range n.Members {
			d, _, _ := strings.Cut(m, ":")
			hasCore = hasCore || core[d]
		}
		for _, m := range n.Members {
			if wired[m] || failed[m] {
				continue
			}
			if d, _, _ := strings.Cut(m, ":"); core[d] {
				continue // 核心脚本身:要么是干线宿主,要么由别的脚接过来
			}
			hasTree := false
			for _, s := range p.planned {
				if s.Net == n.Net {
					hasTree = true
					break
				}
			}
			if !hasTree {
				if hasCore {
					// 直连判据要求核心与外围的信号脚同在一棵线树上;本步没有能接的树,
					// 照实报出来,不能让它看起来像「本来就该是标签」。
					fail(m, n.Net, "", "这条信号网没有可接的线树(块模板没把它 attach 到核心脚,或那棵树没画上)")
				}
				continue
			}
			M, err := resolvePinCoord(in.Scene, m)
			if err != nil {
				fail(m, n.Net, "", "读不到引脚坐标: "+err.Error())
				continue
			}
			ux, uy, ok := bapDirVec(outwardDirection(M))
			if M.PinRotation == nil || !ok {
				fail(m, n.Net, "", "引脚没有实测引出方向")
				continue
			}
			best, bestD := [2]float64{}, math.Inf(1)
			for _, s := range p.planned {
				if s.Net != n.Net {
					continue
				}
				var hit [2]float64
				var d float64
				if s.A[1] == s.B[1] && ux == 0 { // 横线,竖射线
					if M.X < math.Min(s.A[0], s.B[0]) || M.X > math.Max(s.A[0], s.B[0]) {
						continue
					}
					hit, d = [2]float64{M.X, s.A[1]}, (s.A[1]-M.Y)*uy
				} else if s.A[0] == s.B[0] && uy == 0 { // 竖线,横射线
					if M.Y < math.Min(s.A[1], s.B[1]) || M.Y > math.Max(s.A[1], s.B[1]) {
						continue
					}
					hit, d = [2]float64{s.A[0], M.Y}, (s.A[0]-M.X)*ux
				} else {
					continue
				}
				if d > 0 && d < bestD {
					best, bestD = hit, d
				}
			}
			if math.IsInf(bestD, 1) {
				fail(m, n.Net, "", "沿引出方向直线够不到同网的导线(需要折线)")
				continue
			}
			pts := [][2]float64{{M.X, M.Y}, best}
			if ok, why := p.routeClear(pts, n.Net, []acPin{M}, map[string]bool{M.Designator: true}); !ok {
				fail(m, n.Net, "", why)
				continue
			}
			p.add(bapWireRoute{Kind: "branch", Net: n.Net, Member: m, Points: pts})
			wired[m] = true
		}
	}
	out.Routes = p.routes

	// ④ 端口网整网都被导线接住时,别处没有 marker 印出它的名字 —— 在第一棵干线的末端
	//    沿引出方向补一支(求解器已为它预留通道,见 bslSolveTrunks)。
	for _, n := range in.Nets {
		if n.Port == "" {
			continue
		}
		all := true
		for _, m := range n.Members {
			all = all && wired[m]
		}
		if !all {
			continue
		}
		for _, t := range trees {
			if t.net != n.Net || !t.hasRoute || t.end == 0 {
				continue
			}
			dx, dy, _ := bapDirVec(t.dir)
			out.Labels = append(out.Labels, bapWireLabel{Net: n.Net, Kind: n.Kind, Host: t.host,
				X: t.pin.X + dx*t.end, Y: t.pin.Y + dy*t.end, Rotation: bapDirRotation(t.dir)})
			break
		}
	}
	return out
}

// bapNetIsGround 报告网是不是地(不画线,只挂标签)。
func bapNetIsGround(net string) bool {
	switch bapFlagKind(net) {
	case "gnd", "agnd", "pgnd":
		return true
	}
	return tidyNetClass(net) == "ground"
}

// bapWireBlock 是 I/O 外壳:读一次实测几何 → 纯规划 → 逐根下发 → 汇报。返回已被导线
// 接住的成员(从 autoconnect 列表里拿掉)和要补的端口标签(以 autoconnect 显式坐标的
// 方式挂,方向锁死在干线的引出方向)。
func bapWireBlock(cfg *appConfig, window string, plan bapPlan, stderr io.Writer) (map[string]bool, []acConnSpec, *bapWireReport) {
	done := map[string]bool{}
	rep := &bapWireReport{}
	attach := bapAttachHosts(plan)
	need := len(attach) > 0
	for _, n := range plan.Nets {
		if n.Port == "" && len(n.Members) == 2 {
			need = true
		}
	}
	if !need {
		return done, nil, nil
	}
	res, err := requestAction(cfg, "schematic.components.list", window,
		map[string]any{"includeBBox": true, "includePins": true, "includePinNets": false, "includeWires": true})
	if err != nil {
		rep.Skipped = fmt.Sprintf("读不到实测几何(%v)—— 全部连接交给 autoconnect 标签", err)
		fmt.Fprintf(stderr, "wire ✗ %s\n", rep.Skipped)
		return done, nil, rep
	}
	comps, err := parseLayoutComps(res.Result)
	if err != nil {
		rep.Skipped = fmt.Sprintf("几何解析失败(%v)—— 全部连接交给 autoconnect 标签", err)
		fmt.Fprintf(stderr, "wire ✗ %s\n", rep.Skipped)
		return done, nil, rep
	}
	wp := bapPlanBlockWires(bapWireInput{Nets: plan.Nets, Attach: attach, Scene: buildScene(res.Result), Comps: comps, Raw: res.Result})
	rep.Fallback = append(rep.Fallback, wp.Fallback...)
	brokenTree := map[string]string{} // 干线一段没画上,同一棵树后面的线可能 T 在缺的那段上
	for _, r := range wp.Routes {
		if r.Kind == "trunk" && brokenTree[r.Host] != "" {
			rep.Fallback = append(rep.Fallback, bapWireFallback{Member: r.Member, Net: r.Net, Host: r.Host,
				Reason: "同一棵干线的前一段没画上(" + brokenTree[r.Host] + ")"})
			continue
		}
		pts := bapCompactRoute(r.Points)
		if len(pts) < 2 {
			continue // degenerate: the member pin already sits on the tree
		}
		flat := make([]float64, 0, 2*len(pts))
		for _, q := range pts {
			flat = append(flat, q[0], q[1])
		}
		if _, werr := requestAction(cfg, "schematic.wire.create", window, map[string]any{"points": flat, "net": r.Net}); werr != nil {
			fmt.Fprintf(stderr, "wire ✗ %s → %s (%s) points=%v: %v — 退回 autoconnect 标签\n", r.Member, r.Host, r.Net, pts, werr)
			rep.Fallback = append(rep.Fallback, bapWireFallback{Member: r.Member, Net: r.Net, Host: r.Host, Reason: "下发失败: " + werr.Error()})
			if r.Kind == "trunk" {
				brokenTree[r.Host] = r.Member
			}
			continue
		}
		fmt.Fprintf(stderr, "wire ✓ %s %s → %s as %s\n", r.Kind, r.Member, bapOr(r.Host, "tree"), r.Net)
		rep.Wired = append(rep.Wired, r)
		done[r.Member] = true
		if r.Host != "" {
			done[r.Host] = true
		}
	}
	for _, f := range rep.Fallback {
		fmt.Fprintf(stderr, "wire ✗ %s (%s) 没用导线接上 —— %s;退回 autoconnect 标签\n", f.Member, f.Net, f.Reason)
	}
	var labels []acConnSpec
	for _, l := range wp.Labels {
		complete := true
		for _, n := range plan.Nets {
			if n.Net != l.Net {
				continue
			}
			for _, m := range n.Members {
				complete = complete && done[m]
			}
		}
		if !complete {
			continue // 有成员退回了标签,网名已经会印出来
		}
		x, y, rot := l.X, l.Y, l.Rotation
		labels = append(labels, acConnSpec{X: &x, Y: &y, Kind: l.Kind, Net: l.Net, Outward: &rot})
		rep.Labels = append(rep.Labels, l)
	}
	return done, labels, rep
}

func bapOr(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

// bapAttachHosts 把块模板的 attach(ROLE → ROLE.PIN)翻成位号:外围件位号 → 核心脚
// DESIG:PIN。非关系模板或读不到模板时返回空表(只剩正对直线)。
func bapAttachHosts(plan bapPlan) map[string]string {
	out := map[string]string{}
	blk, ok, err := blocks.Get(plan.BlockID)
	if err != nil || !ok {
		return out
	}
	layout, err := blk.SchematicLayout()
	if err != nil {
		return out
	}
	rel, isRel := bslRelationsFrom(layout)
	if !isRel {
		return out
	}
	return bapAttachHostsFrom(rel.Attach, plan.Placements)
}

func bapAttachHostsFrom(attach map[string]string, placements []bapPlacement) map[string]string {
	desig := map[string]string{}
	for _, p := range placements {
		desig[p.Role] = p.Designator
	}
	out := map[string]string{}
	for role, target := range attach {
		tRole, tPin, ok := splitBlockPinRef(target)
		if !ok || desig[role] == "" || desig[tRole] == "" {
			continue
		}
		out[desig[role]] = desig[tRole] + ":" + tPin
	}
	return out
}

// bapSnapScene rounds measured pin and wire coordinates to 1e-3 raw. The host
// reads pins back with float noise (674.9999999999999 for 675), and the planner
// decides "this segment ends on that pin" by exact equality — so without the
// snap a member's own pin was reported as a foreign pin the wire touches
// (live 2026-10-16: "碰到别的引脚 C2:1" on C2's own trunk drop).
func bapSnapScene(sc acScene) acScene {
	snap := func(v float64) float64 { return math.Round(v*1000) / 1000 }
	pins := make([]acPin, len(sc.Pins))
	for i, q := range sc.Pins {
		q.X, q.Y = snap(q.X), snap(q.Y)
		pins[i] = q
	}
	sc.Pins = pins
	wires := make([]wireSegment, len(sc.Wires))
	for i, w := range sc.Wires {
		w.X0, w.Y0, w.X1, w.Y1 = snap(w.X0), snap(w.Y0), snap(w.X1), snap(w.Y1)
		wires[i] = w
	}
	sc.Wires = wires
	return sc
}

// bapCompactRoute drops consecutive repeated points. The host's
// sch_PrimitiveWire.create rejects any zero-length segment ("create failed!",
// live 2026-10-16: every trunk drop whose corner coincided with the trunk's
// current end failed, 0 of 23 wires drawn).
func bapCompactRoute(pts [][2]float64) [][2]float64 {
	out := make([][2]float64, 0, len(pts))
	for _, q := range pts {
		if n := len(out); n > 0 && out[n-1] == q {
			continue
		}
		out = append(out, q)
	}
	return out
}
