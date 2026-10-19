#!/usr/bin/env python3
"""Data-only schematic linter: read full layout JSON, report problem points."""
import json, sys
from collections import defaultdict, Counter
from math import hypot
import orient

_args = [a for a in sys.argv[1:] if not a.startswith('--')]
JSON_OUT = '--json' in sys.argv[1:]   # structured findings for diff.py; text otherwise
# 输出含中文与 ✅ 等字符:管道/重定向时 Windows 默认走 cp936/cp950 会 UnicodeEncodeError,
# 所以固定 utf-8(控制台本来就是 utf-8,等价空操作)。输入同理固定 utf-8 解码。
for _stream in (sys.stdout, sys.stderr):
    if hasattr(_stream, 'reconfigure'):
        _stream.reconfigure(encoding='utf-8')
data = json.load(open(_args[0], encoding='utf-8'))
parts  = [p for p in data['parts'] if p.get('type') == 'part']
sheets = [p for p in data['parts'] if p.get('type') == 'sheet']
flags  = data['flags']
wires  = data['wires']

def segs(line):
    if not line: return []
    pts = [tuple(p) for p in line] if isinstance(line[0], list) else list(zip(line[0::2], line[1::2]))
    return list(zip(pts, pts[1:]))

# ---- connectivity index ----
wire_endpoints = defaultdict(list)   # point -> list of neighbor points (along a wire)
all_zero_wires = []
for w in wires:
    for (a, b) in segs(w.get('line') or []):
        if a == b:
            all_zero_wires.append((w['pid'], a)); continue
        wire_endpoints[a].append(b)
        wire_endpoints[b].append(a)

pin_pts = {}     # (x,y) -> "DES.pin"
for p in parts:
    for pin in p.get('pins', []):
        pin_pts[(pin['x'], pin['y'])] = f"{p.get('designator')}.{pin['num']}({pin['name']})"

flag_pts = {(f['x'], f['y']): f for f in flags}

def direction(frm, to):
    dx, dy = to[0]-frm[0], to[1]-frm[1]
    if dx == 0 and dy == 0: return None
    # EasyEDA is y-UP: +y renders upward on screen (verified via bbox calibration).
    return ('right' if dx>0 else 'left') if abs(dx) >= abs(dy) else ('up' if dy>0 else 'down')

# Orientation table — DERIVED from the canonical spec (orientation.json), never
# hand-edited here. Body points outward along the stub. The connector's
# connect_pin (extension/src/actions.ts deriveBodyRotation) derives the same
# table from the same four facts; scripts/skill-lint-tests/run.py (repo) asserts they agree. §3.5.
BODY_ROT = orient.load_body_rotation()
def family(f):
    if f['type'] == 'netport': return 'port'
    n = (f.get('net') or '').lower()
    return 'ground' if any(g in n for g in ('gnd','vss','agnd','dgnd','pgnd')) else 'power'

problems = defaultdict(list)

# ---- Check 1: netflag/netport orientation (顺着导线) ----
rel_fail = set()
for f in flags:
    pt = (f['x'], f['y'])
    nbrs = wire_endpoints.get(pt)
    if not nbrs:
        problems['flag_no_wire'].append(f"{f['type']} {f['net']} @{pt} 没有 wire 连到它（悬空标识）")
        continue
    toward = direction(pt, nbrs[0])           # wire leaves toward circuit
    body = {'up':'down','down':'up','left':'right','right':'left'}[toward]  # body points opposite
    fam = family(f)
    want = BODY_ROT[fam].get(body)
    if want is not None and f['rotation'] != want:
        rel_fail.add(f.get('pid') or (pt, f['net']))
        problems['orientation'].append(
            f"{f['type']:>7} {f['net']:>9} @{pt}  rot={f['rotation']:>3} 应为 {want:>3}  "
            f"(导线朝{toward}, body应朝{body}, {fam})")

# ---- Check 1b: absolute body orientation (电源上 / 地上下) + reverse-pin exemption ----
# 规则 #2: 电源 flag body 必须朝上、地 flag body 必须朝下 (硬 MUST)。
# 反向脚(引脚外向与要求方向差 180°)由源数据显式标记 `orientationExempt: true` 豁免,
# 豁免脚单独列出、不静默放过。已在 Check 1 报过相对朝向的脚不重复报。
REQUIRED_BODY = {'power': 'up', 'ground': 'down'}
ROT_TO_BODY = {fam: {rot: d for d, rot in table.items()} for fam, table in BODY_ROT.items()}
exempted = []
for f in flags:
    fam = family(f)
    if fam not in REQUIRED_BODY:
        continue
    key = f.get('pid') or ((f['x'], f['y']), f['net'])
    if key in rel_fail:
        continue
    if f.get('orientationExempt'):
        exempted.append(f"{f['type']} {f['net']} @({f['x']},{f['y']}) 反向豁免 (body 随引脚方向)")
        continue
    body = ROT_TO_BODY[fam].get(f['rotation'])
    if body is None:
        continue  # 非 0/90/180/270, 由其它检查/写前守卫处理
    want = REQUIRED_BODY[fam]
    if body != want:
        problems['power_ground_orientation'].append(
            f"{f['type']:>7} {f['net']:>9} @({f['x']},{f['y']})  body={body} 应为 {want} "
            f"(电源上/地上下硬规则; 反向脚须标 orientationExempt)")

# ---- Check 2: flag overlaps a pin (DRC fatal) ----
for f in flags:
    if (f['x'], f['y']) in pin_pts:
        problems['flag_on_pin'].append(f"{f['type']} {f['net']} @({f['x']},{f['y']}) 与引脚 {pin_pts[(f['x'],f['y'])]} 重叠 → DRC fatal")

# ---- Check 3: floating pins (no wire, no flag at pin) ----
for p in parts:
    for pin in p.get('pins', []):
        pt = (pin['x'], pin['y'])
        if pt not in wire_endpoints and pt not in flag_pts:
            problems['floating_pin'].append(f"{p['designator']}.{pin['num']}({pin['name']}) @{pt} 悬空（无导线/标识）")

# ---- Check 4: zero-length wires ----
for pid, pt in all_zero_wires:
    problems['zero_wire'].append(f"wire {pid} @{pt} 零长度（DRC 会报错）")

# ---- Check 4b: diagonal / non-orthogonal wires ----
for w in wires:
    for (a, b) in segs(w.get('line') or []):
        if a == b:
            continue
        if a[0] != b[0] and a[1] != b[1]:
            problems['diagonal_wire'].append(
                f"wire {w['pid']} 段 {a}->{b} 不是水平/竖直（dx={b[0]-a[0]:g}, dy={b[1]-a[1]:g}）")

# ---- Check 5: off-grid ----
for p in parts + flags:
    if p['x'] % 5 or p['y'] % 5:
        problems['off_grid'].append(f"{p.get('designator') or p.get('type')} @({p['x']},{p['y']}) 不在 5 网格上")

# ---- Check 6: bbox overlap between parts ----
def overlap(a, b):
    return not (a[2] < b[0] or b[2] < a[0] or a[3] < b[1] or b[3] < a[1])
bb = [(p['designator'], p['bbox']) for p in parts if p.get('bbox')]
for i in range(len(bb)):
    for j in range(i+1, len(bb)):
        if overlap(bb[i][1], bb[j][1]):
            problems['bbox_overlap'].append(f"{bb[i][0]} 与 {bb[j][0]} bbox 重叠")

# ---- Check 7: duplicate designators ----
desigs = Counter(p['designator'] for p in parts if p.get('designator'))
for dd, n in desigs.items():
    if n > 1:
        problems['dup_designator'].append(f"位号 {dd} 出现 {n} 次")

# ---- Check 8: net-ports of same net close on one page (use a wire/label) ----
ports = [f for f in flags if f['type'] == 'netport']
byport = defaultdict(list)
for p in ports: byport[p['net']].append(p)
for net, ps in byport.items():
    for i in range(len(ps)):
        for j in range(i+1, len(ps)):
            dist = abs(ps[i]['x']-ps[j]['x']) + abs(ps[i]['y']-ps[j]['y'])
            if dist <= 300:
                problems['netport_hop'].append(
                    f"net-port '{net}' @({ps[i]['x']},{ps[i]['y']}) 与 @({ps[j]['x']},{ps[j]['y']}) 仅隔 {dist} → 同页应改导线/net label")

# ---- Check 9: different-net flags collinear through a component (visual false-short) ----
for p in parts:
    cx, cy, bb = p['x'], p['y'], p.get('bbox')
    if not bb: continue
    near = [f for f in flags if abs(f['x']-cx) <= 20 and bb[1]-80 <= f['y'] <= bb[3]+80]
    above = [f for f in near if f['y'] < cy]
    below = [f for f in near if f['y'] > cy]
    if above and below and {f['net'] for f in above} != {f['net'] for f in below}:
        problems['collinear_flags'].append(
            f"{p['designator']} @({cx},{cy}): 上 {[f['net'] for f in above]} / 下 {[f['net'] for f in below]} 在 x≈{cx} 共线 → 视觉像导线穿过元件")

# ---- Check 10: dangling wire ends (空连: degree-1 wire vertex with no pin/flag) ----
deg = defaultdict(int)
for w in wires:
    for (a, b) in segs(w.get('line') or []):
        if a == b: continue
        deg[a] += 1; deg[b] += 1
for pt, dgr in deg.items():
    if dgr == 1 and pt not in pin_pts and pt not in flag_pts:
        problems['dangling_wire'].append(f"导线端点 @{pt} 空连（degree=1，无引脚/标识）")

# ---- Check 10b: marker_wire_overlap (异线经过标识锚点附近, 近似) ----
# 离线模型没有 flag bbox,这里用「异线到标识锚点的距离 < MARKER_WIRE_EPS」做粗筛;
# 现场 sch check 用真实 bbox 判同一类缺陷。标识自己的短桩(endpoint 在锚点)不计。
MARKER_WIRE_EPS = 5
def seg_dist_point(px, py, a, b):
    x0, y0 = a; x1, y1 = b
    if x0 == x1:
        lo, hi = min(y0, y1), max(y0, y1)
        if lo <= py <= hi:
            return abs(px - x0)
        return min(hypot(px - x0, py - lo), hypot(px - x0, py - hi))
    lo, hi = min(x0, x1), max(x0, x1)
    if lo <= px <= hi:
        return abs(py - y0)
    return min(hypot(px - lo, py - y0), hypot(px - hi, py - y0))

def seg_intersects_box(a, b, box):
    """Closed segment/AABB intersection: edge or corner contact counts."""
    x0, y0 = a
    x1, y1 = b
    minx, miny, maxx, maxy = box
    dx, dy = x1 - x0, y1 - y0
    p = (-dx, dx, -dy, dy)
    q = (x0 - minx, maxx - x0, y0 - miny, maxy - y0)
    u1, u2 = 0.0, 1.0
    for pi, qi in zip(p, q):
        if pi == 0:
            if qi < 0:
                return False
            continue
        r = qi / pi
        if pi < 0:
            if r > u2:
                return False
            if r > u1:
                u1 = r
        else:
            if r < u1:
                return False
            if r < u2:
                u2 = r
    return True

for f in flags:
    ax, ay = f['x'], f['y']
    for w in wires:
        for (a, b) in segs(w.get('line') or []):
            if a == b:
                continue
            if (a[0] == ax and a[1] == ay) or (b[0] == ax and b[1] == ay):
                continue  # 标识自己的短桩
            d = seg_dist_point(ax, ay, a, b)
            if d < MARKER_WIRE_EPS:
                problems['marker_wire_overlap'].append(
                    f"{f['type']} {f['net']} @({ax},{ay}) 与导线 {w['pid']} 段 {a}->{b} "
                    f"距离 {d:.1f} < {MARKER_WIRE_EPS}（异线经过标识附近，锚点近似）")
                break

# ---- Check 10c: visible text vs wire (closed collision) ----
def visible_text_boxes():
    out = []
    raw = data.get('visible_text') or data.get('text_bboxes') or data.get('textBboxes') or []
    for i, item in enumerate(raw):
        box = None
        if isinstance(item, dict):
            box = item.get('bbox')
            if box is None:
                box = [item.get('minX'), item.get('minY'), item.get('maxX'), item.get('maxY')]
        if not box or len(box) != 4 or any(v is None for v in box):
            continue
        try:
            box = [float(v) for v in box]
        except (TypeError, ValueError):
            continue
        if box[0] > box[2]:
            box[0], box[2] = box[2], box[0]
        if box[1] > box[3]:
            box[1], box[3] = box[3], box[1]
        label = (item.get('label') or item.get('text') or item.get('content')
                 or item.get('value') or item.get('key') or f"text{i+1}")
        out.append({'label': str(label), 'bbox': box,
                    'parentWireId': (item.get('parentWireId') or item.get('parent_wire')
                                     or item.get('parentId')),
                    'componentType': item.get('componentType'),
                    'key': item.get('key'),
                    'rotation': item.get('rotation'),
                    'net': item.get('value') if item.get('key') == 'Name' else None})
    for p in parts:
        for i, item in enumerate(p.get('textBboxes') or p.get('text_bboxes') or []):
            box = item.get('bbox') if isinstance(item, dict) else item
            if not box or len(box) != 4:
                continue
            try:
                box = [float(v) for v in box]
            except (TypeError, ValueError):
                continue
            out.append({'label': f"{p.get('designator', 'part')}.text{i+1}",
                        'bbox': box, 'parentWireId': None,
                        'componentType': 'part', 'key': None,
                        'rotation': None, 'net': None})
    return out

for t in visible_text_boxes():
    for w in wires:
        if t['parentWireId'] == w.get('pid'):
            continue
        hit = False
        for (a, b) in segs(w.get('line') or []):
            if a == b:
                continue
            if seg_intersects_box(a, b, t['bbox']):
                problems['text_wire_overlap'].append(
                    f"{t['label']} bbox=({t['bbox'][0]:g},{t['bbox'][1]:g})-"
                    f"({t['bbox'][2]:g},{t['bbox'][3]:g}) 被导线 {w['pid']} 段 {a}->{b} 穿过")
                hit = True
                break
        if hit:
            break

# ---- Check 10d: vertical/folded net labels ----
for t in visible_text_boxes():
    if t['componentType'] != 'wire' or t['key'] != 'Name':
        continue
    rot = t.get('rotation')
    if isinstance(rot, (int, float)) and int(round(rot)) % 180 == 90:
        problems['label_vertical'].append(
            f"网络标签「{t['net'] or t['label']}」 @bbox({t['bbox'][0]:g},{t['bbox'][1]:g}) "
            f"rotation={rot:g} 竖排/折叠（应水平显示）")

# ---- net trace (union-find) for power/ground audit ----
uf = {}
def ufind(x):
    uf.setdefault(x, x)
    while uf[x] != x: uf[x] = uf[uf[x]]; x = uf[x]
    return x
def uunion(a, b): ufind(a); ufind(b); uf[ufind(a)] = ufind(b)
for w in wires:
    pl = segs(w.get('line') or [])
    for (a, b) in pl:
        if a != b: uunion(a, b)
for pt in list(pin_pts) + list(flag_pts):
    ufind(pt)

# ---- Check 10e: same line island carries duplicate same-net labels ----
def wire_root(w):
    for (a, b) in segs(w.get('line') or []):
        if a != b:
            return ufind(a)
    return None

wire_roots = {w['pid']: wire_root(w) for w in wires}
labels_by_island = defaultdict(list)
for t in visible_text_boxes():
    if t['componentType'] != 'wire' or t['key'] != 'Name' or not t['parentWireId']:
        continue
    root = wire_roots.get(t['parentWireId'])
    if root is not None:
        labels_by_island[(t['net'] or t['label'], root)].append(t)
for (net, root), group in labels_by_island.items():
    if len(group) > 1:
        problems['redundant_label'].append(
            f"线岛 {root} 上网络 '{net}' 有 {len(group)} 个同名标签 — 只保留一个："
            + ", ".join(f"@{t['bbox'][0]:g},{t['bbox'][1]:g}" for t in group))

net_members = defaultdict(lambda: {'pins': set(), 'flags': set()})
net_pts = defaultdict(set)   # root -> set of (x,y) pin/flag points on that net (for span/proximity)
for pt in list(uf):
    root = ufind(pt)
    if pt in pin_pts: net_members[root]['pins'].add(pin_pts[pt]); net_pts[root].add(pt)
    if pt in flag_pts: net_members[root]['flags'].add(f"{flag_pts[pt]['net']}"); net_pts[root].add(pt)

def is_power(n): return any(p in n.lower() for p in ('vcc','vdd','3v3','5v','+','vbat','vbus'))
def is_gnd(n):   return any(g in n.lower() for g in ('gnd','vss','agnd'))

# ---- Check 11: single-pin nets (一个引脚连到孤立网络 = 实质空连) ----
# ---- Check 12: unnamed multi-pin signal nets (没命名, 可读性/跨页隐患) ----
for root, m in net_members.items():
    pins, fset = m['pins'], m['flags']
    if not pins and not fset: continue
    has_pwr = any(is_power(n) for n in fset)
    has_gnd = any(is_gnd(n) for n in fset)
    sig_flags = [n for n in fset if not is_power(n) and not is_gnd(n)]
    if len(pins) == 1 and not fset:
        problems['single_pin_net'].append(f"引脚 {next(iter(pins))} 所在网络只有它自己、且无电源/地/标识 → 空连")
    if len(pins) >= 2 and not fset:
        problems['unnamed_net'].append(f"网络 {{{', '.join(sorted(pins))}}} 多引脚但无命名标识（建议加 net label / 电源地）")

# ==== auto-layout SOP enforcement (auto-layout-sop.md) ====
# These catch the bulk-realization defect (scattered decaps + flag-every-pin), which
# only manifests at board scale — gate on total pin count so tiny focused fixtures
# (which legitimately use a couple of flags) don't trip them.
total_pins = sum(len(p.get('pins', [])) for p in parts)
SOP_SCALE = total_pins >= 30
if SOP_SCALE:
    # IC pins on a power net = the "VCC pads" decoupling should hug.
    ic_pins = {(pin['x'], pin['y']): p['designator']
               for p in parts if len(p.get('pins', [])) > 4 for pin in p['pins']}
    power_pin_pts = set()
    for root, m in net_members.items():
        if any(is_power(n) for n in m['flags']):
            power_pin_pts |= {pt for pt in net_pts[root] if pt in ic_pins}

    # ---- Check 13: decoupling not near its IC VCC pad (§6 / SOP Step 2) ----
    for p in parts:
        if not str(p.get('designator', '')).startswith('C') or len(p.get('pins', [])) != 2:
            continue
        if not power_pin_pts:
            continue
        on_power = any(any(is_power(n) for n in net_members[ufind((pin['x'], pin['y']))]['flags'])
                       for pin in p['pins'])
        if not on_power:
            continue
        cx, cy = p['x'], p['y']
        dmin = min(abs(cx - px) + abs(cy - py) for px, py in power_pin_pts)
        if dmin > 120:
            problems['decap_far'].append(
                f"{p['designator']} 距最近 IC 电源引脚 {dmin}u > 120u MUST → 去耦未就近 (§6 / SOP Step2，应贴 VCC 焊盘)")

    # ---- Check 14: over-flagging — flag count ≈ pin count (SOP Step 3) ----
    if len(flags) / total_pins > 0.6:
        problems['flag_density'].append(
            f"标识/端口 {len(flags)} / 引脚 {total_pins} = {len(flags)/total_pins:.0%} > 60% "
            f"→ 过度按名接线(flag-every-pin),簇内/短网络应改本地导线 (SOP Step3 决策表)")

    # ---- Check 15: short ≥2-pin signal net realized as scattered flags vs a local wire ----
    # Group by net NAME, not union-find root: a "by-name" net is split into one root per
    # pin (pin→stub→flag, no wire between them), so per-root it looks like single-pin nets.
    sig_by_name = defaultdict(lambda: {'pins': set(), 'pts': set()})
    for root, m in net_members.items():
        for n in m['flags']:
            if is_power(n) or is_gnd(n):
                continue
            sig_by_name[n]['pins'] |= m['pins']
            sig_by_name[n]['pts'] |= net_pts[root]
    for n, d in sig_by_name.items():
        if not (2 <= len(d['pins']) <= 3) or len(d['pts']) < 2:
            continue
        pts = d['pts']
        span = (max(x for x, y in pts) - min(x for x, y in pts)) + (max(y for x, y in pts) - min(y for x, y in pts))
        if span < 250:
            problems['local_net_as_flag'].append(
                f"信号网 '{n}' ({len(d['pins'])}脚, 跨度 {span}u < 250) 由分散标识连 → 同簇应本地 pin→wire→pin (SOP §3.6)")

order = ['flag_on_pin','zero_wire','diagonal_wire','dangling_wire','floating_pin','single_pin_net',
         'flag_no_wire','marker_wire_overlap','text_wire_overlap','label_vertical','redundant_label','orientation','power_ground_orientation','bbox_overlap','dup_designator','decap_far',
         'netport_hop','local_net_as_flag','flag_density','collinear_flags','unnamed_net','off_grid']
labels = {
  'flag_on_pin':'🔴 标识与引脚重叠 (DRC fatal)','zero_wire':'🔴 零长度导线',
  'diagonal_wire':'🔴 斜线/非正交导线',
  'dangling_wire':'🔴 导线空连端点','floating_pin':'🟠 悬空引脚',
  'single_pin_net':'🟠 单引脚孤网 (空连)','flag_no_wire':'🟠 标识无导线',
  'marker_wire_overlap':'🟠 标识压在导线上(锚点近似)',
  'text_wire_overlap':'🟠 可见文字被导线穿过',
  'label_vertical':'🟡 网络标签竖排/折叠',
  'redundant_label':'🟡 同线岛重复网络标签',
  'orientation':'🟡 朝向不顺导线','power_ground_orientation':'🔴 电源/地标识朝向错误(应上/下)',
  'bbox_overlap':'🟠 元件 bbox 重叠',
  'dup_designator':'🟠 重复位号','netport_hop':'🟡 net-port 同页近距离',
  'collinear_flags':'🟡 异网标识共线穿元件','unnamed_net':'🔵 多引脚网络未命名',
  'off_grid':'🔵 不在 5 网格',
  'decap_far':'🟠 去耦未就近 IC 电源脚 (§6/SOP)','flag_density':'🟡 过度按名接线 (flag≈pin)',
  'local_net_as_flag':'🟡 短信号网用标识应改本地线',
}
# Structured findings — (rule, msg) is a stable identity for diff.py: the same
# problem yields the same deterministic msg across runs.
findings = [{'rule': k, 'label': labels[k], 'msg': m} for k in order for m in problems[k]]
summary = {'parts': len(parts), 'flags': len(flags), 'wires': len(wires), 'sheets': len(sheets),
           'nets': len([m for m in net_members.values() if m['pins'] or m['flags']]),
           'problems': len(findings), 'orientationExempted': len(exempted)}

if JSON_OUT:
    print(json.dumps({'summary': summary, 'findings': findings, 'exemptions': exempted}, ensure_ascii=False))
else:
    print(f"== 概览 ==  parts={summary['parts']} flags={summary['flags']} wires={summary['wires']} sheets={summary['sheets']} nets≈{summary['nets']}")
    if not findings:
        print("\n✅ 无问题")
    for k in order:
        if problems[k]:
            print(f"\n{labels[k]} ({len(problems[k])}):")
            for line in problems[k]:
                print(f"  - {line}")
    if exempted:
        print(f"\n⚪ 反向豁免 ({len(exempted)}):")
        for line in exempted:
            print(f"  - {line}")
