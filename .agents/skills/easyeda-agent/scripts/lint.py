#!/usr/bin/env python3
"""Data-only schematic linter: read full layout JSON, report problem points."""
import json, sys
from collections import defaultdict, Counter
from math import hypot
import heapq
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

def is_power_name(n):
    return any(p in (n or '').lower() for p in ('vcc','vdd','3v3','5v','+','vbat','vbus'))

def is_gnd_name(n):
    return any(g in (n or '').lower() for g in ('gnd','vss','agnd'))

def is_rail_name(n):
    s = (n or '').lower()
    return (is_power_name(s) or is_gnd_name(s) or
            any(s.startswith(prefix) for prefix in ('vout', 'vload', 'vbias', 'dc')))

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

# ---- Check 6b: drawing / title-block bounds when measured geometry exists ----
def as_box(value):
    if isinstance(value, dict):
        keys = ('minX','minY','maxX','maxY')
        if all(isinstance(value.get(k), (int,float)) for k in keys):
            return [float(value[k]) for k in keys]
    if isinstance(value, (list, tuple)) and len(value) == 4 and all(isinstance(v, (int,float)) for v in value):
        return [float(v) for v in value]
    return None

sheet_box = next((as_box(p.get('bbox')) for p in sheets if as_box(p.get('bbox'))), None)
keepouts = []
for item in data.get('keepouts') or []:
    box = as_box(item.get('bbox') if isinstance(item, dict) else item)
    if box:
        keepouts.append(box)
if sheet_box:
    for p in parts:
        box = as_box(p.get('bbox'))
        if box and not (sheet_box[0] <= box[0] and sheet_box[1] <= box[1] and box[2] <= sheet_box[2] and box[3] <= sheet_box[3]):
            problems['out_of_sheet'].append(f"器件 {p.get('designator') or p.get('pid')} 越出图纸 bbox")
        if box:
            for ko in keepouts:
                if overlap(box, ko):
                    problems['titleblock_overlap'].append(f"器件 {p.get('designator') or p.get('pid')} 落入图签/禁放区")
    for f in flags:
        if not (sheet_box[0] <= f['x'] <= sheet_box[2] and sheet_box[1] <= f['y'] <= sheet_box[3]):
            problems['out_of_sheet'].append(f"标识 {f.get('pid')} 越出图纸 @({f['x']},{f['y']})")
        for ko in keepouts:
            if ko[0] <= f['x'] <= ko[2] and ko[1] <= f['y'] <= ko[3]:
                problems['titleblock_overlap'].append(f"标识 {f.get('pid')} 落入图签/禁放区")
    for w in wires:
        for a, b in segs(w.get('line') or []):
            if not (sheet_box[0] <= a[0] <= sheet_box[2] and sheet_box[1] <= a[1] <= sheet_box[3]
                    and sheet_box[0] <= b[0] <= sheet_box[2] and sheet_box[1] <= b[1] <= sheet_box[3]):
                problems['out_of_sheet'].append(f"导线 {w['pid']} 越出图纸 {a}->{b}")

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

def seg_intersects_seg(a, b, c, d):
    def orient(p, q, r):
        return (q[0]-p[0])*(r[1]-p[1]) - (q[1]-p[1])*(r[0]-p[0])
    def on_seg(p, q, r):
        return (min(p[0], r[0])-1e-9 <= q[0] <= max(p[0], r[0])+1e-9 and
                min(p[1], r[1])-1e-9 <= q[1] <= max(p[1], r[1])+1e-9)
    o1, o2, o3, o4 = orient(a,b,c), orient(a,b,d), orient(c,d,a), orient(c,d,b)
    if ((o1 > 1e-9 and o2 < -1e-9) or (o1 < -1e-9 and o2 > 1e-9)) and \
       ((o3 > 1e-9 and o4 < -1e-9) or (o3 < -1e-9 and o4 > 1e-9)):
        return True
    return ((abs(o1) <= 1e-9 and on_seg(a,c,b)) or
            (abs(o2) <= 1e-9 and on_seg(a,d,b)) or
            (abs(o3) <= 1e-9 and on_seg(c,a,d)) or
            (abs(o4) <= 1e-9 and on_seg(c,b,d)))

for w in wires:
    for a, b in segs(w.get('line') or []):
        for ko in keepouts:
            if seg_intersects_box(a, b, ko):
                problems['titleblock_overlap'].append(
                    f"导线 {w['pid']} 进入图签/禁放区 {a}->{b}")

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
        visible = item.get('visible', True) if isinstance(item, dict) else True
        missing = item.get('missingName', False) if isinstance(item, dict) else False
        box = None
        if isinstance(item, dict):
            box = item.get('bbox')
            if box is None:
                box = [item.get('minX'), item.get('minY'), item.get('maxX'), item.get('maxY')]
        if not box or len(box) != 4 or any(v is None for v in box):
            if visible is False or missing:
                out.append({'label': str(item.get('value') or item.get('net') or item.get('pid') or f"text{i+1}"),
                            'bbox': None,
                            'parentWireId': item.get('parentWireId') or item.get('parent_wire') or item.get('parentId'),
                            'componentType': item.get('componentType'), 'key': item.get('key'),
                            'rotation': item.get('rotation'), 'visible': visible,
                            'missingName': missing, 'net': item.get('value')})
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
                    'visible': visible,
                    'missingName': missing,
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
                        'rotation': None, 'visible': True,
                        'missingName': False, 'net': None})
    return out

for t in visible_text_boxes():
    if not t['bbox'] or t.get('visible') is False:
        continue
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

# ---- Check 10d: name visibility (signal labels and power/ground symbols) ----
for t in visible_text_boxes():
    if t.get('missingName'):
        problems['label_visibility'].append(f"符号 {t['label']} 没有 Name 属性 — 名称不可见")
        continue
    if t.get('key') != 'Name':
        continue
    if t.get('visible') is False:
        problems['label_visibility'].append(f"名称「{t['net'] or t['label']}」可见性为 false — 必须显示")
    elif not (t['net'] or '').strip():
        if t['bbox'] and t['bbox'][2] - t['bbox'][0] > 0.5 and t['bbox'][3] - t['bbox'][1] > 0.5:
            problems['label_visibility'].append("Name 属性已显示但内容为空 — 必须填写网络名")

# ---- Check 10d2: vertical/folded signal and power/ground names ----
for t in visible_text_boxes():
    if t['componentType'] not in ('wire', 'netflag', 'netport', 'netlabel') or t['key'] != 'Name':
        continue
    if not t['bbox'] or t.get('visible') is False or not (t['net'] or '').strip():
        continue
    rot = t.get('rotation')
    if isinstance(rot, (int, float)) and int(round(rot)) % 180 == 90:
        problems['label_vertical'].append(
            f"名称「{t['net'] or t['label']}」 @bbox({t['bbox'][0]:g},{t['bbox'][1]:g}) "
            f"rotation={rot:g} 竖排/折叠（应水平显示）")

# ---- Check 10d3: signal nets must use text-only labels; explicit module ports stay audited ----
for f in flags:
    if f['type'] not in ('netflag', 'netport') or not (f.get('net') or '').strip():
        continue
    if is_rail_name(f['net']):
        continue
    if f['type'] == 'netport' and f.get('moduleBoundaryExempt'):
        problems['signal_port_exempt'].append(
            f"信号网 '{f['net']}' 的模块边界端口 {f.get('pid','?')} 显式豁免（仍应人工复核边界语义）")
        continue
    problems['signal_marker_type'].append(
        f"信号网 '{f['net']}' 使用了 {f['type']} {f.get('pid','?')} — 应改文字网络标签；"
        f"模块边界 netport 须在源数据标 moduleBoundaryExempt:true")

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
    if not t['bbox'] or t.get('visible') is False:
        continue
    root = wire_roots.get(t['parentWireId'])
    if root is not None:
        labels_by_island[(t['net'] or t['label'], root)].append(t)
for (net, root), group in labels_by_island.items():
    if len(group) > 1:
        problems['redundant_label'].append(
            f"线岛 {root} 上网络 '{net}' 有 {len(group)} 个同名标签 — 只保留一个："
            + ", ".join(f"@{t['bbox'][0]:g},{t['bbox'][1]:g}" for t in group))

# ---- Check 10e2: cross-kind duplicates on one line island ----
flags_by_pid = {f.get('pid'): f for f in flags if f.get('pid')}
marker_labels = defaultdict(list)
for t in visible_text_boxes():
    if t['componentType'] not in ('netflag', 'netport', 'netlabel') or t['key'] != 'Name':
        continue
    f = flags_by_pid.get(t.get('parentWireId'))
    if not f:
        continue
    root = ufind((f['x'], f['y']))
    marker_labels[(f.get('net') or '', root)].append((f, t))
for key, markers in marker_labels.items():
    net, root = key
    wire_labels = labels_by_island.get((net, root), [])
    if wire_labels and markers:
        problems['cross_kind_label_economy'].append(
            f"线岛 {root} 网络 '{net}' 同时有导线标签和 {len(markers)} 个标识符号 — 只保留一个")

# ---- Check 10e3: visible label text must stay within 50 units of its owner ----
def seg_point_distance(px, py, a, b):
    x0, y0 = a; x1, y1 = b
    dx, dy = x1-x0, y1-y0
    l2 = dx*dx + dy*dy
    tt = 0 if l2 == 0 else max(0, min(1, ((px-x0)*dx + (py-y0)*dy) / l2))
    return hypot(px-(x0+tt*dx), py-(y0+tt*dy))

for t in visible_text_boxes():
    if t['key'] != 'Name' or not t['bbox'] or t.get('visible') is False:
        continue
    px = (t['bbox'][0]+t['bbox'][2])/2
    py = (t['bbox'][1]+t['bbox'][3])/2
    dist = None
    if t['componentType'] == 'wire' and t['parentWireId']:
        for w in wires:
            if w.get('pid') != t['parentWireId']:
                continue
            for a, b in segs(w.get('line') or []):
                d = seg_point_distance(px, py, a, b)
                dist = d if dist is None else min(dist, d)
    elif t['componentType'] in ('netflag', 'netport', 'netlabel'):
        f = flags_by_pid.get(t.get('parentWireId'))
        if f:
            dist = hypot(px-f['x'], py-f['y'])
    if dist is not None and dist > 50:
        problems['label_distance'].append(
            f"名称「{t['net'] or t['label']}」距所属导线/符号 {dist:.1f}u > 50u")

# ---- Check 10f: same-net labels on different islands with a clear short L path ----
def path_clear(points, excluded_roots, excluded_labels, text_items):
    for (a, b) in zip(points, points[1:]):
        if a == b:
            continue
        for p in parts:
            if p.get('bbox') and seg_intersects_box(a, b, p['bbox']):
                return False
        for t in text_items:
            if t is excluded_labels[0] or t is excluded_labels[1]:
                continue
            if seg_intersects_box(a, b, t['bbox']):
                return False
        for f in flags:
            if (f['x'], f['y']) in (points[0], points[-1]):
                continue
            if seg_intersects_box(a, b, [f['x']-0.5, f['y']-0.5, f['x']+0.5, f['y']+0.5]):
                return False
        for pin in pin_pts:
            if pin in (points[0], points[-1]):
                continue
            if seg_intersects_box(a, b, [pin[0]-0.5, pin[1]-0.5, pin[0]+0.5, pin[1]+0.5]):
                return False
        for w in wires:
            if wire_roots.get(w['pid']) in excluded_roots:
                continue
            for (c, d) in segs(w.get('line') or []):
                if c != d and seg_intersects_seg(a, b, c, d):
                    return False
    return True

def label_attachment(t, root):
    """Closest point on the label's own wire tree to the measured text center."""
    px = (t['bbox'][0] + t['bbox'][2]) / 2
    py = (t['bbox'][1] + t['bbox'][3]) / 2
    best = None
    for w in wires:
        if wire_roots.get(w['pid']) != root:
            continue
        for a, b in segs(w.get('line') or []):
            x0, y0 = a; x1, y1 = b
            dx, dy = x1-x0, y1-y0
            l2 = dx*dx + dy*dy
            tt = 0 if l2 == 0 else max(0, min(1, ((px-x0)*dx + (py-y0)*dy) / l2))
            qx, qy = x0 + tt*dx, y0 + tt*dy
            d = hypot(px-qx, py-qy)
            if best is None or d < best[0]:
                best = (d, qx, qy)
    return (best[1], best[2]) if best else (px, py)

def orthogonal_detour(start, end, excluded_roots, excluded_labels, text_items, max_len=500):
    """Obstacle-aware 5-unit A* used when both direct L paths are blocked."""
    margin = 80
    minx, miny = min(start[0], end[0])-margin, min(start[1], end[1])-margin
    maxx, maxy = max(start[0], end[0])+margin, max(start[1], end[1])+margin
    sheet = next((p.get('bbox') for p in sheets if p.get('bbox')), None)
    if sheet:
        minx, miny = max(minx, sheet[0]), max(miny, sheet[1])
        maxx, maxy = min(maxx, sheet[2]), min(maxy, sheet[3])
    dirs = [(1,0),(0,1),(-1,0),(0,-1)]
    start_key = (start[0], start[1], -1)
    best = {start_key: 0}
    prev = {}
    q = [(abs(start[0]-end[0])+abs(start[1]-end[1]), 0, start[0], start[1], -1)]
    expanded = 0
    while q and expanded < 12000:
        _, g, x, y, last = heapq.heappop(q)
        key = (x, y, last)
        if g != best.get(key):
            continue
        if last >= 0 and (abs(x-end[0]) <= .5 or abs(y-end[1]) <= .5):
            if g + abs(x-end[0])+abs(y-end[1]) <= max_len and path_clear(
                    [(x,y), end], excluded_roots, excluded_labels, text_items):
                prev[(end[0],end[1],4)] = key
                chain = [(end[0],end[1],4)]
                while chain[-1] in prev:
                    chain.append(prev[chain[-1]])
                pts = [start] + [(v[0],v[1]) for v in reversed(chain)]
                out = []
                for p in pts:
                    if not out or p != out[-1]:
                        out.append(p)
                compact = []
                for p in out:
                    if len(compact) >= 2:
                        a, b = compact[-2], compact[-1]
                        if ((a[0] == b[0] == p[0]) or (a[1] == b[1] == p[1])):
                            compact[-1] = p
                            continue
                    compact.append(p)
                out = compact
                return out
        for di, (dx,dy) in enumerate(dirs):
            if last >= 0 and (last+2) % 4 == di:
                continue
            nx, ny = x+dx*5, y+dy*5
            if nx < minx-.5 or nx > maxx+.5 or ny < miny-.5 or ny > maxy+.5:
                continue
            if not path_clear([(x,y),(nx,ny)], excluded_roots, excluded_labels, text_items):
                continue
            cost = g + 5 + (0 if last < 0 or last == di else 2)
            if cost > max_len:
                continue
            nk = (nx, ny, di)
            if cost >= best.get(nk, 10**9):
                continue
            best[nk] = cost
            prev[nk] = key
            heapq.heappush(q, (cost+abs(nx-end[0])+abs(ny-end[1]), cost, nx, ny, di))
        expanded += 1
    return None

all_text = visible_text_boxes()

# ---- Check 10f0: visible text vs bodies, foreign pins and other text ----
def boxes_positive_overlap(a, b):
    return min(a[2], b[2]) > max(a[0], b[0]) and min(a[3], b[3]) > max(a[1], b[1])

for i, a in enumerate(all_text):
    if not a['bbox'] or a.get('visible') is False:
        continue
    hit = None
    for p in parts:
        if p.get('pid') == a.get('parentWireId'):
            continue
        if p.get('bbox') and boxes_positive_overlap(a['bbox'], p['bbox']):
            hit = f"器件 {p.get('designator') or p.get('pid')} bbox"
            break
    if hit is None:
        for f in flags:
            if f.get('pid') == a.get('parentWireId'):
                continue
            box = [f['x']-.5, f['y']-.5, f['x']+.5, f['y']+.5]
            if boxes_positive_overlap(a['bbox'], box):
                hit = f"标识 {f.get('pid')}"
                break
    if hit is None:
        for pin in pin_pts:
            if pin[0] >= a['bbox'][0] and pin[0] <= a['bbox'][2] and pin[1] >= a['bbox'][1] and pin[1] <= a['bbox'][3]:
                hit = f"引脚 {pin_pts[pin]}"
                break
    if hit is None:
        for b in all_text[i+1:]:
            if not b['bbox'] or b.get('visible') is False:
                continue
            if a.get('parentWireId') and a.get('parentWireId') == b.get('parentWireId'):
                continue
            if boxes_positive_overlap(a['bbox'], b['bbox']):
                hit = f"文字「{b['label']}」"
                break
    if hit:
        problems['visible_text_overlap'].append(f"{a['label']} 压缩 {hit}")
    if sheet_box and a['bbox'] and not (
            sheet_box[0] <= a['bbox'][0] and sheet_box[1] <= a['bbox'][1]
            and a['bbox'][2] <= sheet_box[2] and a['bbox'][3] <= sheet_box[3]):
        problems['out_of_sheet'].append(f"可见文字「{a['label']}」越出图纸")
    for ko in keepouts:
        if a['bbox'] and boxes_positive_overlap(a['bbox'], ko):
            problems['titleblock_overlap'].append(f"可见文字「{a['label']}」进入图签/禁放区")

label_items = []
for t in all_text:
    if t['componentType'] != 'wire' or t['key'] != 'Name' or not t['parentWireId']:
        continue
    root = wire_roots.get(t['parentWireId'])
    if root is None or is_rail_name(t['net']):
        continue
    label_items.append((t, root))

seen_pairs = set()
for i in range(len(label_items)):
    ti, ri = label_items[i]
    for j in range(i + 1, len(label_items)):
        tj, rj = label_items[j]
        if ri == rj or (ti['net'] or '') != (tj['net'] or ''):
            continue
        if (ti['net'] or '') == '':
            continue
        pair = (min(ti['label'], tj['label']), max(ti['label'], tj['label']))
        if pair in seen_pairs:
            continue
        seen_pairs.add(pair)
        a = label_attachment(ti, ri)
        b = label_attachment(tj, rj)
        if abs(a[0]-b[0]) + abs(a[1]-b[1]) > 250:
            continue
        paths = [
            [a, (b[0], a[1]), b],
            [a, (a[0], b[1]), b],
        ]
        for path in paths:
            if path_clear(path, {ri, rj}, (ti, tj), all_text):
                problems['direct_connect_opportunity'].append(
                    f"网络 '{ti['net']}' 的两个标签 @({a[0]:g},{a[1]:g}) 与 @({b[0]:g},{b[1]:g}) "
                    f"存在无遮挡短 L 路径（Manhattan={abs(a[0]-b[0])+abs(a[1]-b[1]):g}u）→ 优先直连，删除标签")
                break
        else:
            path = orthogonal_detour(a, b, {ri, rj}, (ti, tj), all_text,
                                     max_len=max(100, 2*(abs(a[0]-b[0])+abs(a[1]-b[1]))))
            if path:
                problems['direct_connect_opportunity'].append(
                    f"网络 '{ti['net']}' 的两个标签 @({a[0]:g},{a[1]:g}) 与 @({b[0]:g},{b[1]:g}) "
                    f"存在无遮挡正交绕行路径（{len(path)-1} 段）→ 优先直连，删除标签")

net_members = defaultdict(lambda: {'pins': set(), 'flags': set()})
net_pts = defaultdict(set)   # root -> set of (x,y) pin/flag points on that net (for span/proximity)
for pt in list(uf):
    root = ufind(pt)
    if pt in pin_pts: net_members[root]['pins'].add(pin_pts[pt]); net_pts[root].add(pt)
    if pt in flag_pts: net_members[root]['flags'].add(f"{flag_pts[pt]['net']}"); net_pts[root].add(pt)

# ---- Check 10g: conservative H-V-H / V-H-V offset opportunity ----
wire_segments = []
for w in wires:
    for a, b in segs(w.get('line') or []):
        if a == b:
            continue
        if a[0] == b[0]:
            ori = 'v'
        elif a[1] == b[1]:
            ori = 'h'
        else:
            continue
        wire_segments.append({'a': a, 'b': b, 'ori': ori, 'wire': w['pid'], 'net': w.get('net') or ''})

def other_end(seg, point):
    if seg['a'] == point:
        return seg['b']
    if seg['b'] == point:
        return seg['a']
    return None

seen_alignment = set()
for connector in wire_segments:
    if connector['ori'] not in ('h', 'v'):
        continue
    p0, p1 = connector['a'], connector['b']
    if p0 in pin_pts or p1 in pin_pts or p0 in flag_pts or p1 in flag_pts:
        continue
    arm_ori = 'h' if connector['ori'] == 'v' else 'v'
    arms0 = [s for s in wire_segments if s is not connector and s['ori'] == arm_ori and p0 in (s['a'], s['b'])]
    arms1 = [s for s in wire_segments if s is not connector and s['ori'] == arm_ori and p1 in (s['a'], s['b'])]
    if not arms0 or not arms1:
        continue
    a0, a1 = arms0[0], arms1[0]
    o0, o1 = other_end(a0, p0), other_end(a1, p1)
    if o0 is None or o1 is None:
        continue
    # Both arms extend to the same side; this is the conservative offset-jog
    # shape that a group translation can align without changing topology.
    d0 = (o0[0]-p0[0], o0[1]-p0[1])
    d1 = (o1[0]-p1[0], o1[1]-p1[1])
    if d0[0]*d1[0] + d0[1]*d1[1] <= 0:
        continue
    key = tuple(sorted((p0, p1, o0, o1)))
    if key in seen_alignment:
        continue
    seen_alignment.add(key)
    shape = 'H-V-H' if connector['ori'] == 'v' else 'V-H-V'
    problems['alignment_opportunity'].append(
        f"网络 '{connector['net'] or '?'}' 存在 {shape} 非必要偏轴折返 "
        f"({p0}->{p1}->{o1}, arm={o0}) — 优先平移一侧功能组使两臂共线")

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
         'flag_no_wire','marker_wire_overlap','text_wire_overlap','label_visibility','label_vertical','redundant_label','direct_connect_opportunity','orientation','power_ground_orientation','bbox_overlap','dup_designator','decap_far',
         'netport_hop','local_net_as_flag','flag_density','collinear_flags','unnamed_net','off_grid',
         'cross_kind_label_economy','label_distance','signal_marker_type','signal_port_exempt',
         'visible_text_overlap','titleblock_overlap','out_of_sheet','alignment_opportunity']
labels = {
  'flag_on_pin':'🔴 标识与引脚重叠 (DRC fatal)','zero_wire':'🔴 零长度导线',
  'diagonal_wire':'🔴 斜线/非正交导线',
  'dangling_wire':'🔴 导线空连端点','floating_pin':'🟠 悬空引脚',
  'single_pin_net':'🟠 单引脚孤网 (空连)','flag_no_wire':'🟠 标识无导线',
  'marker_wire_overlap':'🟠 标识压在导线上(锚点近似)',
  'text_wire_overlap':'🟠 可见文字被导线穿过',
  'label_visibility':'🟠 名称隐藏/为空',
  'label_vertical':'🟡 网络标签竖排/折叠',
  'redundant_label':'🟡 同线岛重复网络标签',
  'cross_kind_label_economy':'🟡 同岛导线标签与标识混用重复',
  'label_distance':'🟡 网络名称离所属对象过远',
  'signal_marker_type':'🟠 信号网使用 netflag/netport',
  'signal_port_exempt':'ℹ 信号模块端口显式豁免',
  'visible_text_overlap':'🟠 可见文字压器件/标识/引脚/其他文字',
  'titleblock_overlap':'🟠 图元/文字进入图签或禁放区',
  'out_of_sheet':'🔴 图元或可见文字越出图纸',
  'alignment_opportunity':'🔵 可平移消除的偏轴折返',
  'direct_connect_opportunity':'🔵 可直连的标签对',
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
