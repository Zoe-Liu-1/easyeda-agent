package app

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zhoushoujianwork/easyeda-agent/internal/blocks"
)

// ── attach 干线(trunk):外围件用真实导线贴到宿主引脚(2026-10-16)────────────────
//
// 规则「同名标签不算直连」(sch check 的 peripheral-not-direct,判据是
// ValidateSchematicPeripheralDirect)要求外围件用**实际导线**接到核心件的引脚。旧求解器
// 把每只有连接的宿主脚都当成「要挂 marker」,于是贴脚件只能越过全部 marker 通道,落在
// 离引脚几百远的地方,连线只能靠同名标签 —— 现场 ESP32-S3R8 的 15 个外围件全军覆没。
//
// 干线模型(布局与连线共用的一份几何契约):
//
//	宿主脚 P 沿引出方向 d 拉一根水平干线;每个外围件竖放,挂在干线的上方或下方,
//	它连宿主网的那只脚朝向干线,一根竖直落线(drop)T 到干线上。同一只脚的后续件
//	把干线继续往外延(共线同网),于是一棵线树接住这只脚的全部外围件。
//
// 判定与落地同源:这里给出的 Wire 就是 block-apply 落线时按实测引脚重算的同一个形状
// (bapPlanBlockWires),坐标全部在 5 网格上。干线宿主脚不再挂 marker,它的 marker 通道
// 换成「干线 + 落线 + 外围件远端脚的 marker 簇」;网有 PORT 且整网都被导线接住时,干线
// 末端另留一支标签的通道(否则这个端口在图上看不到名字)。
//
// 放不下就退回旧路径(越过通道外排、标签连接),并在 notes 里写明 —— 绝不静默。

// bslTrunkMinReach / bslTrunkMaxReach:外围件所在列离宿主脚尖的距离。下限是两个落线
// 网格以外再让出一个件的半宽(0402 实测半宽 8.5 → 20 时件边离脚尖 11.5);「贴近」的上限
// 80 是验收口径,超过它仍然允许(只要线是清的),只是不再算「贴」。
const (
	bslTrunkMinReach = 4 * schAnchorGrid
	bslTrunkMaxReach = 16 * schAnchorGrid
)

// bslTrunkCorridor 是大件外排时为还没挂上件的干线宿主让出的行长:密集列(两侧都是挂
// marker 的脚)上的干线实测要拉到 200+ 才落得下件。
const bslTrunkCorridor = 3 * bslTrunkMaxReach

// bslPassivePinHalf 是竖放 2 脚分立件的引脚连接点(引脚线外端,回读的 pin x/y)离件中心
// 的距离。本体 bbox 约 17×21,引脚线再伸出 10:现场 Web 4.1.78 实测 0402/0805 电容与
// 0402 电阻都是 ±20(放在 y=300 的件两脚回读 320/280)。曾按本体沿取 10,规划的落线
// 正好被吃掉,接网脚落在干线上全部退回标签(2026-10-16 apply9)。
const bslPassivePinHalf = 20.0

// bslTrunkClear 是干线几何(导线、件、marker 簇)彼此之间的最小间隙 —— 一个连接网格:
// 再近 EasyEDA 就可能把两根线视为相接,或视觉上贴脸。
const bslTrunkClear = float64(schAnchorGrid)

// bslTrunkDrops 是落线长度的候选:先最短(一个件就挨着干线),再给标签/邻件让一格。
var bslTrunkDrops = []float64{2 * schAnchorGrid, 4 * schAnchorGrid}

// bslTrunkHost 是一只走干线的宿主脚。
type bslTrunkHost struct {
	Key     string // 宿主脚编号
	Ref     string // attach 目标原文(U.2 / U.VDD3P3_RTC)
	pinRef  string // 与 bslTrunkState.pts 同口径的宿主脚标识(ROLE#编号)
	Pin     acPin
	Side    string // left | right(v1 只做左右列)
	Net     int    // nets 下标
	Label   string // 干线末端要留的标签网名("" = 不需要)
	Members []string
	conn    map[string]string // 成员角色 → 它接宿主网的那只脚("1"/"2")
	far     map[string]string // 成员角色 → 远端脚
	hang    string            // 首选挂向 up|down
}

type bslTrunkSeg struct {
	A, B    [2]float64
	Net     int
	Owner   string // 所属干线宿主 key
	Reserve bool   // 未服务宿主的预留桩(正式干线落下时被取代)
}

type bslTrunkPt struct {
	P   [2]float64
	Net int
	Ref string
}

// bslTrunkHosts 选出能走干线的宿主脚:左右列、目标是锚件、至少有一个外围件是声明竖放
// 的 2 脚分立件且恰有一只脚在宿主网里(只有这些件进 Members;同脚的多脚件照旧外排)。
// 地网不走干线(地不参与直连判据,标签即可)。
// skip 里的宿主(上一轮一个件都没挂上)退回旧路径。返回顺序就是求解顺序:向下挂的
// 自下而上(最底下那只脚干线最短,上面的越过它往外落),向上挂的自上而下。
func bslTrunkHosts(blk blocks.Block, rel bslRelations, nets [][]string, anchorRole string,
	pins map[string]acPin, skip map[string]bool) []*bslTrunkHost {

	netOfPin := func(key string) int {
		for k, net := range nets {
			for _, m := range net {
				r, p, ok := splitBlockPinRef(m)
				if !ok || r != anchorRole {
					continue
				}
				if q, found := pins[strings.TrimSuffix(p, acPinFanoutSuffix)]; found && q.PinNumber == key {
					return k
				}
			}
		}
		return -1
	}
	byKey := map[string]*bslTrunkHost{}
	bad := map[string]bool{}
	roles := make([]string, 0, len(rel.Attach))
	for r := range rel.Attach {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	for _, role := range roles {
		tRole, tPin, ok := splitBlockPinRef(rel.Attach[role])
		if !ok || tRole != anchorRole {
			continue
		}
		pin, found := pins[tPin]
		if !found {
			continue
		}
		key := pin.PinNumber
		side := outwardDirection(pin)
		k := netOfPin(key)
		h := byKey[key]
		if h == nil {
			h = &bslTrunkHost{Key: key, Ref: rel.Attach[role], Pin: pin, Side: side, Net: k,
				conn: map[string]string{}, far: map[string]string{}}
			byKey[key] = h
		}
		if skip[key] || (side != "left" && side != "right") || k < 0 || bslNetIsGround(nets[k]) {
			bad[key] = true
			continue
		}
		part := ""
		if p, ok := blk.Parts[role]; ok {
			part = p.Part
		}
		// 多脚件(晶振、flash)和横放件不走干线:它们照旧外排,宿主脚的其余竖放分立件仍可
		// 走干线(那只脚由干线接住,不再挂 marker)。
		if !bslIsDiscrete(part) || rel.Orient[role] == "horizontal" {
			continue
		}
		var conn []string
		for _, m := range nets[k] {
			if r, p, ok := splitBlockPinRef(m); ok && r == role {
				conn = append(conn, p)
			}
		}
		if len(conn) != 1 || (conn[0] != "1" && conn[0] != "2") {
			continue
		}
		h.Members = append(h.Members, role)
		h.conn[role] = conn[0]
		h.far[role] = map[string]string{"1": "2", "2": "1"}[conn[0]]
	}
	var hosts []*bslTrunkHost
	for key, h := range byKey {
		if !bad[key] && len(h.Members) > 0 {
			hosts = append(hosts, h)
		}
	}
	// 标签:网有 PORT,且网里每一只脚都会被干线接住(宿主脚是干线宿主、成员脚是干线
	// 成员的接网脚)—— 否则别处的 marker 已经把网名印出来了,不必再加。
	inTrunk := map[string]bool{}
	for _, h := range hosts {
		inTrunk[anchorRole+"#"+h.Key] = true
		for role, p := range h.conn {
			inTrunk[role+"."+p] = true
		}
	}
	labelled := map[int]bool{}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Key < hosts[j].Key })
	for _, h := range hosts {
		port := ""
		all := true
		for _, m := range nets[h.Net] {
			if strings.HasPrefix(m, "PORT:") {
				if port == "" {
					port = strings.TrimPrefix(m, "PORT:")
				}
				continue
			}
			r, p, ok := splitBlockPinRef(m)
			if !ok {
				all = false
				continue
			}
			if r == anchorRole {
				q, found := pins[strings.TrimSuffix(p, acPinFanoutSuffix)]
				if !found || !inTrunk[anchorRole+"#"+q.PinNumber] {
					all = false
				}
				continue
			}
			if !inTrunk[m] {
				all = false
			}
		}
		if port != "" && all && !labelled[h.Net] {
			h.Label, labelled[h.Net] = port, true
		}
	}
	// 挂向:同一列上,向下与向上各有多少只「有连接的脚」挨着(一个贴近距离内)——
	// 往空的那边挂。平手向下(去耦的地在下)。
	connected := map[string]bool{}
	for _, net := range nets {
		for _, m := range net {
			if r, p, ok := splitBlockPinRef(m); ok && r == anchorRole {
				if q, found := pins[strings.TrimSuffix(p, acPinFanoutSuffix)]; found {
					connected[q.PinNumber] = true
				}
			}
		}
	}
	seen := map[string]bool{}
	var column []acPin
	for _, p := range pins {
		if seen[p.PinNumber] || !connected[p.PinNumber] {
			continue
		}
		seen[p.PinNumber] = true
		column = append(column, p)
	}
	for _, h := range hosts {
		up, down := 0, 0
		for _, p := range column {
			if p.PinNumber == h.Pin.PinNumber || outwardDirection(p) != h.Side || math.Abs(p.X-h.Pin.X) > 0.5 {
				continue
			}
			switch d := p.Y - h.Pin.Y; {
			case d > 0 && d <= bslTrunkMaxReach:
				up++
			case d < 0 && -d <= bslTrunkMaxReach:
				down++
			}
		}
		h.hang = "down"
		if up < down {
			h.hang = "up"
		}
		sort.Strings(h.Members)
	}
	sort.SliceStable(hosts, func(i, j int) bool {
		a, b := hosts[i], hosts[j]
		if a.hang != b.hang {
			return a.hang == "down"
		}
		if a.Pin.Y != b.Pin.Y {
			if a.hang == "down" {
				return a.Pin.Y < b.Pin.Y
			}
			return a.Pin.Y > b.Pin.Y
		}
		return a.Key < b.Key
	})
	return hosts
}

// bslTrunkZones 是干线宿主的近区预留:在大件(多脚件)先外排时把这片地方让出来,否则
// 大件的 marker 簇压在宿主脚跟前,贴件只能把干线拉到几百远之外(实测 FLASH 的簇正好
// 盖住 VDD_SPI 脚下方,C_FLASH 的干线被推到 465)。宽度按成员数逐个排开,高度是首选
// 挂向上的「落线 + 件身 + 远端 marker」。
func bslTrunkZones(hosts []*bslTrunkHost, hull layoutBBox, usable *layoutBBox) []layoutBBox {
	var out []layoutBBox
	for _, h := range hosts {
		pitch := 2*bslPassivePinHalf + 2*bslTrunkClear
		w := float64(bslTrunkMinReach) + float64(len(h.Members)-1)*pitch + bslPassivePinHalf + bslTrunkClear
		v := bslTrunkDrops[0] + 2*bslPassivePinHalf + bslMarkerVReach
		x0, x1 := h.Pin.X, h.Pin.X+h.dx()*w
		y0, y1 := h.Pin.Y, h.Pin.Y-v
		if h.hang == "up" {
			y1 = h.Pin.Y + v
		}
		out = append(out, bslUnion(bslSegBox([2]float64{x0, y0}, [2]float64{x1, y0}), bslSegBox([2]float64{x1, y0}, [2]float64{x1, y1})))
		// 近区之外的一段行:干线第二遍要沿它往外拉,大件不许横在上面。只留到
		// bslTrunkCorridor —— 整行都让出来的话,同一只脚的大件(晶振贴 XTAL_P)无处可放。
		far := math.Min(bslTrunkWindow(h, hull, usable), bslTrunkCorridor)
		out = append(out, bslSegBox([2]float64{x1, y0}, [2]float64{h.Pin.X + h.dx()*far, y0}))
	}
	return out
}

func bslNetIsGround(net []string) bool {
	for _, m := range net {
		if strings.HasPrefix(m, "PORT:") {
			return tidyNetClass(strings.TrimPrefix(m, "PORT:")) == "ground"
		}
	}
	return false
}

// bslTrunkProxyNet 是一条块内网在求解阶段的代理网名(与 bslHostPinLanes 同口径:有 PORT
// 用 PORT 名);没有 PORT 时用 NET_<k>,长度与实例名(U2_N3 一类)同量级。
func bslTrunkProxyNet(nets [][]string, k int) string {
	for _, m := range nets[k] {
		if strings.HasPrefix(m, "PORT:") {
			return strings.TrimPrefix(m, "PORT:")
		}
	}
	return fmt.Sprintf("NET_%d", k+1)
}

// bslTrunkNetOfRef 找 ROLE.PIN 所在的网下标(-1 = 不在任何网里)。
func bslTrunkNetOfRef(nets [][]string, ref string) int {
	for k, net := range nets {
		for _, m := range net {
			if m == ref {
				return k
			}
		}
	}
	return -1
}

func bslSegBox(a, b [2]float64) layoutBBox {
	return layoutBBox{MinX: math.Min(a[0], b[0]), MinY: math.Min(a[1], b[1]), MaxX: math.Max(a[0], b[0]), MaxY: math.Max(a[1], b[1])}
}

func bslUnion(a, b layoutBBox) layoutBBox {
	return layoutBBox{MinX: math.Min(a.MinX, b.MinX), MinY: math.Min(a.MinY, b.MinY), MaxX: math.Max(a.MaxX, b.MaxX), MaxY: math.Max(a.MaxY, b.MaxY)}
}

// bslTrunkState 是一次干线求解的全部已占几何。
type bslTrunkState struct {
	anchor    layoutBBox
	obstacles []layoutBBox // 页面图元 + 锚件(导线必须避开;从宿主脚尖沿引出方向起步的那段除外)
	usable    *layoutBBox
	lanes     []layoutBBox // 非干线宿主脚的 marker 通道
	bodies    []layoutBBox
	clusters  []layoutBBox // 成员远端脚的 marker 簇
	labels    []layoutBBox // 已预留的干线标签
	segs      []bslTrunkSeg
	pts       []bslTrunkPt
	end       map[string]float64 // 宿主 key → 干线已延伸到的距离
}

// bslTrunkMarker 是一支 marker 的占地(桩线 ∪ 标识 ∪ 文字带),与 autoconnect 评分器、
// sch check 同一把尺(predictedMarkerBBox)。
func bslTrunkMarker(x, y float64, side, net string) layoutBBox {
	dx, dy := bslDirVec(side)
	ex, ey := x+dx*schStubLen, y+dy*schStubLen
	return bslUnion(bslSegBox([2]float64{x, y}, [2]float64{ex, ey}),
		predictedMarkerBBox(ex, ey, bapFlagKind(net), side, net))
}

// bslTrunkLabelBox 是干线末端标签的占地:桩线从末端拐点外一个网格算起(拐点处本来就接着
// 本干线的落线,同网相接),再加 marker 本体与文字带。
func bslTrunkLabelBox(at [2]float64, side, net string) layoutBBox {
	dx, dy := bslDirVec(side)
	stub := bslSegBox([2]float64{at[0] + dx*bslTrunkClear, at[1] + dy*bslTrunkClear},
		[2]float64{at[0] + dx*schStubLen, at[1] + dy*schStubLen})
	body := predictedMarkerBBox(at[0]+dx*schStubLen, at[1]+dy*schStubLen, bapFlagKind(net), side, net)
	return bslUnion(stub, body)
}

// bslTrunkCandidate 是一个外围件的一种干线落位。
type bslTrunkCandidate struct {
	X, Y, Rot float64
	Body      layoutBBox
	Cluster   layoutBBox
	Wire      [][2]float64
	Conn, Far [2]float64
	D         float64
}

func (h *bslTrunkHost) dx() float64 {
	if h.Side == "left" {
		return -1
	}
	return 1
}

// candidate 按 (列距 D, 挂向, 落线长) 生成一个落位;不检查碰撞。
func (h *bslTrunkHost) candidate(nets [][]string, role string, D float64, hang string, drop float64, end float64) bslTrunkCandidate {
	P := [2]float64{h.Pin.X, h.Pin.Y}
	cx := P[0] + h.dx()*D
	s := -1.0 // 向下挂:接网脚在上
	if hang == "up" {
		s = 1
	}
	conn := [2]float64{cx, P[1] + s*drop}
	cy := conn[1] + s*bslPassivePinHalf
	far := [2]float64{cx, cy + s*bslPassivePinHalf}
	// 下发给 place 的 rotation:90 = 1 脚朝上,270 = 1 脚朝下。宿主把器件旋转取负存储
	// (下发 90 回读 270),实测 Web 4.1.71 两种阻容封装一致;执行端按实测引脚复核,
	// 朝向不符会退回标签而不是接错。
	connUp := hang == "down"
	rot := 90.0
	if connUp != (h.conn[role] == "1") {
		rot = 270
	}
	corner := [2]float64{cx, P[1]}
	var wire [][2]float64
	if D > end {
		start := P
		if end > 0 {
			start = [2]float64{P[0] + h.dx()*end, P[1]}
		}
		wire = [][2]float64{start, corner, conn}
	} else {
		wire = [][2]float64{corner, conn}
	}
	box := bslPartBox("cap.") // 竖放分立件:与 commit / clusters 同一把尺
	body := layoutBBox{MinX: cx + box.MinX, MinY: cy + box.MinY, MaxX: cx + box.MaxX, MaxY: cy + box.MaxY}
	cluster := body
	if k := bslTrunkNetOfRef(nets, role+"."+h.far[role]); k >= 0 {
		cluster = bslUnion(body, bslTrunkMarker(far[0], far[1], hang, bslTrunkProxyNet(nets, k)))
	}
	return bslTrunkCandidate{X: cx, Y: cy, Rot: rot, Body: body, Cluster: cluster, Wire: wire, Conn: conn, Far: far, D: D}
}

func bslPointBox(p [2]float64) layoutBBox {
	return layoutBBox{MinX: p[0], MinY: p[1], MaxX: p[0], MaxY: p[1]}
}

// areaClear 判一块占地(件本体 / marker 簇 / 标签)与已占几何是否分开。ownSegs 为真时
// 跳过 owner 自己的干线(标签就接在自己干线的末端)。segGap 是与导线的间隙:件本体要
// 让出一个网格;marker 与导线只要求不相交(相邻脚间距 10,marker 离邻脚桩线本来就只有
// 几个单位)。
func (st *bslTrunkState) areaClear(a layoutBBox, owner string, ownSegs bool, segGap float64) (bool, string) {
	g := bslTrunkClear
	if st.usable != nil && !boxInside(a, *st.usable) {
		return false, "出图纸可用区"
	}
	for _, o := range st.obstacles {
		if boxesGapOverlap(a, o, g) {
			return false, "压到页面图元/锚件"
		}
	}
	for _, set := range [][]layoutBBox{st.bodies, st.clusters, st.lanes, st.labels} {
		for _, o := range set {
			if boxesGapOverlap(a, o, g) {
				return false, "压到别的外围件 / marker 通道 / 干线标签"
			}
		}
	}
	for _, s := range st.segs {
		if ownSegs && s.Owner == owner && !s.Reserve {
			continue
		}
		if boxesGapOverlap(a, bslSegBox(s.A, s.B), segGap) {
			return false, "压到已规划的导线或未服务宿主的干线行"
		}
	}
	for _, p := range st.pts {
		if boxesGapOverlap(a, bslPointBox(p.P), segGap) {
			return false, "压到别的引脚"
		}
	}
	return true, ""
}

// wireClear 判一条导线折线与已占几何是否分开。同网的正式导线允许相接(那正是要的连接);
// 未服务宿主的整行预留一律不许碰 —— 先求解的件不许把它的出路堵死。
func (st *bslTrunkState) wireClear(h *bslTrunkHost, wire [][2]float64) (bool, string) {
	g := bslTrunkClear
	P := [2]float64{h.Pin.X, h.Pin.Y}
	for i := 1; i < len(wire); i++ {
		a, b := wire[i-1], wire[i]
		sb := bslSegBox(a, b)
		for _, o := range st.obstacles {
			if a == P && o == st.anchor {
				continue // 从宿主脚尖沿引出方向起步的那一段本来就挨着宿主
			}
			if schSegmentCrossesBox(a[0], a[1], b[0], b[1], o) {
				return false, "导线穿过页面图元/锚件"
			}
		}
		for _, o := range st.bodies {
			if boxesGapOverlap(sb, o, g) {
				return false, "导线压到别的外围件"
			}
		}
		// marker 与导线只要求不相交:相邻脚(间距 10)的 marker 离别的脚的桩线本来就只有
		// 几个单位,autoconnect 天天这么摆。
		for _, set := range [][]layoutBBox{st.clusters, st.lanes, st.labels} {
			for _, o := range set {
				if boxesGapOverlap(sb, o, 0) {
					return false, "导线穿过别的 marker / marker 通道 / 干线标签"
				}
			}
		}
		for _, s := range st.segs {
			if s.Owner == h.Key {
				continue // 自己的干线(含预留行)
			}
			if s.Net == h.Net && !s.Reserve {
				continue
			}
			if boxesGapOverlap(sb, bslSegBox(s.A, s.B), g) {
				return false, "导线贴到/穿过别的网的导线或未服务宿主的干线行"
			}
		}
		for _, p := range st.pts {
			if p.Ref == h.pinRef {
				continue
			}
			if boxesGapOverlap(sb, bslPointBox(p.P), g) {
				return false, "导线贴到别的引脚"
			}
		}
	}
	return true, ""
}

func (st *bslTrunkState) clear(h *bslTrunkHost, c bslTrunkCandidate) (bool, string) {
	if ok, why := st.areaClear(c.Body, h.Key, false, bslTrunkClear); !ok {
		return false, why
	}
	if ok, why := st.areaClear(c.Cluster, h.Key, false, 0); !ok {
		return false, why
	}
	return st.wireClear(h, c.Wire)
}

// commit 把一个已通过的落位记入占用。
func (st *bslTrunkState) commit(h *bslTrunkHost, role string, c bslTrunkCandidate) {
	st.bodies = append(st.bodies, c.Body)
	if c.Cluster != c.Body {
		st.clusters = append(st.clusters, c.Cluster)
	}
	kept := st.segs[:0]
	for _, s := range st.segs {
		if !(s.Reserve && s.Owner == h.Key) { // 宿主自己的整行预留被正式干线取代
			kept = append(kept, s)
		}
	}
	st.segs = kept
	for i := 1; i < len(c.Wire); i++ {
		st.segs = append(st.segs, bslTrunkSeg{A: c.Wire[i-1], B: c.Wire[i], Net: h.Net, Owner: h.Key})
	}
	st.pts = append(st.pts,
		bslTrunkPt{P: c.Conn, Net: h.Net, Ref: role + "." + h.conn[role]},
		bslTrunkPt{P: c.Far, Net: -1, Ref: role + "." + h.far[role]})
	if c.D > st.end[h.Key] {
		st.end[h.Key] = c.D
	}
}

// bslTrunkWindow 是干线沿引出方向最远能伸多远(到图纸可用区边;没有图纸时按宿主高度)。
func bslTrunkWindow(h *bslTrunkHost, hull layoutBBox, usable *layoutBBox) float64 {
	win := bapGrow(hull, hull.MaxY-hull.MinY)
	if usable != nil {
		win = *usable
	}
	if h.Side == "left" {
		return h.Pin.X - win.MinX
	}
	return win.MaxX - h.Pin.X
}

// bslTrunkLabel 是一棵需要标签的干线在末端预留的标签(网名 + 落点 + 占地)。
type bslTrunkLabel struct {
	Host     string
	Net      string
	At       [2]float64
	Side     string
	Box      layoutBBox
	Reserved bool
}

// bslTrunkResult 是干线阶段的产出。
type bslTrunkResult struct {
	Solved   []bslSolved
	Bodies   []layoutBBox
	Clusters []layoutBBox
	// Wired 是导线与已预留标签的占地 —— 之后放的件(旧路径的贴件、flow、pair)都要避开。
	Wired  []layoutBBox
	Labels []bslTrunkLabel
	// Empty 是一个外围件都没挂上的干线宿主:下一轮把它退回旧路径(恢复它的 marker 通道)。
	// EmptyNotes 记它的每个成员为什么挂不上,退回之后仍要出现在报告里。
	Empty      []string
	EmptyNotes []string
	Notes      []string
}

// bslTrunkSolver 是干线阶段的求解器(纯函数,有状态只为分两遍):
//
//	第一遍 pass(true) 只在贴近上限内找落位 —— 近区先给贴件;
//	调用方随后外排大件(多脚件),把它们的本体与 marker 簇交回 addObstacles;
//	第二遍 pass(false) 让剩下的件把干线拉远,绕开大件;
//	finish 预留干线末端标签并汇总。
//
// 搜索:列距 D 由近到远,每个 D 先试首选挂向、再试反向,落线先短后长;第一个全部清白的
// 落位入选 —— 同输入同输出。宿主顺序见 bslTrunkHosts。
//
// 标签在**全部件落定之后**才预留:干线排成金字塔(底下那只脚的干线最短,上面的越过它
// 往外落),里层宿主的标签横在外层宿主的落线必经之处,先占标签会把整列推到几百远。
// 标签让路于导线;预留不到净空的标签照样落(autoconnect 在线端按引出方向挂),并在 notes
// 里说明。
type bslTrunkSolver struct {
	st     *bslTrunkState
	nets   [][]string
	hosts  []*bslTrunkHost
	hull   layoutBBox
	usable *layoutBBox
	done   map[string]bool   // 已落位的成员
	count  map[string]int    // 宿主 key → 已落位成员数
	why    map[string]string // 成员 → 最近一档的冲突
	res    bslTrunkResult
}

func newBslTrunkSolver(nets [][]string, hosts []*bslTrunkHost, anchorBBox, hull layoutBBox,
	anchorPins map[string]acPin, anchorRole string, obstacles, lanes []layoutBBox, usable *layoutBBox) *bslTrunkSolver {

	st := &bslTrunkState{anchor: anchorBBox, obstacles: append([]layoutBBox(nil), obstacles...), usable: usable,
		lanes: lanes, end: map[string]float64{}}
	seen := map[string]bool{}
	keys := make([]string, 0, len(anchorPins))
	for k := range anchorPins {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := anchorPins[k]
		if seen[p.PinNumber] {
			continue
		}
		seen[p.PinNumber] = true
		st.pts = append(st.pts, bslTrunkPt{P: [2]float64{p.X, p.Y}, Net: -1, Ref: anchorRole + "#" + p.PinNumber})
	}
	// 还没轮到的宿主先占住它整行的出路(到窗口边):先求解的件与落线都不许压上去。
	for _, h := range hosts {
		h.pinRef = anchorRole + "#" + h.Key
		w := bslTrunkWindow(h, hull, usable)
		st.segs = append(st.segs, bslTrunkSeg{A: [2]float64{h.Pin.X, h.Pin.Y},
			B: [2]float64{h.Pin.X + h.dx()*w, h.Pin.Y}, Net: h.Net, Owner: h.Key, Reserve: true})
	}
	return &bslTrunkSolver{st: st, nets: nets, hosts: hosts, hull: hull, usable: usable,
		done: map[string]bool{}, count: map[string]int{}, why: map[string]string{}}
}

// pass 走一遍全部未落位的成员;near 时只接受贴近上限以内的落位。
func (ts *bslTrunkSolver) pass(near bool) {
	st := ts.st
	for _, h := range ts.hosts {
		hangs := []string{h.hang, bslOppositeSide(h.hang)}
		limit := bslTrunkWindow(h, ts.hull, ts.usable)
		if near {
			limit = math.Min(limit, bslTrunkMaxReach)
		}
		for _, role := range h.Members {
			if ts.done[role] {
				continue
			}
			var best *bslTrunkCandidate
			first := ""
		search:
			for D := float64(bslTrunkMinReach); D <= limit; D += schAnchorGrid {
				for _, hang := range hangs {
					for _, drop := range bslTrunkDrops {
						c := h.candidate(ts.nets, role, D, hang, drop, st.end[h.Key])
						if ok, reason := st.clear(h, c); ok {
							best = &c
							break search
						} else if first == "" {
							first = reason
						}
					}
				}
			}
			if best == nil {
				if ts.why[role] == "" {
					ts.why[role] = first
				}
				continue
			}
			st.commit(h, role, *best)
			ts.done[role] = true
			ts.count[h.Key]++
			ts.res.Solved = append(ts.res.Solved, bslSolved{Role: role, X: best.X, Y: best.Y, Rotation: best.Rot,
				Source: "attach", Wire: best.Wire})
			if best.D > bslTrunkMaxReach {
				ts.res.Notes = append(ts.res.Notes, fmt.Sprintf("%s: 贴 %s 的干线要伸到 %.0f 才清(超过贴近上限 %.0f)—— 仍然用导线直连",
					role, h.Ref, best.D, float64(bslTrunkMaxReach)))
			}
		}
	}
}

// pending 报告还有成员没落位的宿主 key。
func (ts *bslTrunkSolver) pending() map[string]bool {
	out := map[string]bool{}
	for _, h := range ts.hosts {
		for _, role := range h.Members {
			if !ts.done[role] {
				out[h.Key] = true
			}
		}
	}
	return out
}

// addObstacles 把两遍之间外排的大件(本体与 marker 簇)记为障碍。
func (ts *bslTrunkSolver) addObstacles(b []layoutBBox) {
	ts.st.obstacles = append(ts.st.obstacles, b...)
}

// snapshot 是当前已占的干线几何(件本体、marker 簇、导线;不含预留行)。
func (ts *bslTrunkSolver) snapshot() (bodies, clusters, wired []layoutBBox) {
	for _, s := range ts.st.segs {
		if !s.Reserve {
			wired = append(wired, bslSegBox(s.A, s.B))
		}
	}
	return append([]layoutBBox(nil), ts.st.bodies...), append([]layoutBBox(nil), ts.st.clusters...), wired
}

// finish 预留标签、汇总放不下的成员,产出结果。
func (ts *bslTrunkSolver) finish() bslTrunkResult {
	st, res := ts.st, ts.res
	for _, h := range ts.hosts {
		var why []string
		for _, role := range h.Members {
			if !ts.done[role] {
				why = append(why, fmt.Sprintf("%s: 贴 %s 的干线放不下(最近一档的冲突:%s)—— 该件退回外排 + 标签连接", role, h.Ref, ts.why[role]))
			}
		}
		if ts.count[h.Key] == 0 {
			res.Empty = append(res.Empty, h.Key)
			res.EmptyNotes = append(res.EmptyNotes, why...)
			continue
		}
		res.Notes = append(res.Notes, why...)
	}
	// 一个件都没挂上的宿主不该继续占整行(下一轮它退回 marker 通道,由那条通道占位)。
	kept := st.segs[:0]
	for _, s := range st.segs {
		if !s.Reserve {
			kept = append(kept, s)
		}
	}
	st.segs = kept
	for _, h := range ts.hosts {
		end := st.end[h.Key]
		if h.Label == "" || end == 0 {
			continue
		}
		at := [2]float64{h.Pin.X + h.dx()*end, h.Pin.Y}
		lbl := bslTrunkLabel{Host: h.Key, Net: h.Label, At: at, Side: h.Side, Box: bslTrunkLabelBox(at, h.Side, h.Label)}
		if ok, why := st.areaClear(lbl.Box, h.Key, true, 0); ok {
			lbl.Reserved = true
			st.labels = append(st.labels, lbl.Box)
		} else {
			res.Notes = append(res.Notes, fmt.Sprintf("干线 %s 末端的 %s 标签预留不到净空(%s)—— 落线后仍在线端按引出方向挂标签,注意核对", h.Ref, h.Label, why))
		}
		res.Labels = append(res.Labels, lbl)
	}
	res.Bodies, res.Clusters, res.Wired = ts.snapshot()
	res.Wired = append(res.Wired, st.labels...)
	return res
}

// bslSolveTrunks 是一次性的干线求解(没有大件夹在中间时):一遍走到底再收尾。
func bslSolveTrunks(nets [][]string, hosts []*bslTrunkHost, anchorBBox, hull layoutBBox,
	anchorPins map[string]acPin, anchorRole string, obstacles, lanes []layoutBBox, usable *layoutBBox) bslTrunkResult {
	ts := newBslTrunkSolver(nets, hosts, anchorBBox, hull, anchorPins, anchorRole, obstacles, lanes, usable)
	ts.pass(false)
	return ts.finish()
}
