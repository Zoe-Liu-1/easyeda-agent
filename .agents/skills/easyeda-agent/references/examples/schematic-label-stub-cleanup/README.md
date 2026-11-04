# 网络标签线头清理

本例记录可见网络名挂在“只为标注而从主干分出的 T 形叶枝”末端的问题，以及对应
检测、直挂和执行器。

**状态：`live-verified`，仅 P1 的 7 个线头修复与保存重载范围。**

## 错误形态

正确形态是 Name 属性直接属于既有导线：

```text
wire trunk ─────────────  Name
```

错误形态是额外分叉一根没有引脚、marker 或电气用途的短枝，只把 Name 放在枝端：

```text
wire trunk ──────┬──────
                 │
                Name
```

后者增加阅读噪声，也会让后续去重、移动和文字避让把标签当成独立端点。

## 检查口径

`label-stub` 为 ERROR。检测要求：

- 是可见的 wire `Name`，有父导线和实测 bbox；
- 必须用 `pagePrimitives.attributes[].X/Y` 的原生属性锚点；bbox 只表示文字包围盒，
  它的中心可能落在短枝中段，不能拿来做锚点；
- 标签锚点在物理线树的 degree-1 叶端点；
- 叶枝沿唯一路径接到 degree>=3 的主干线；
- 叶枝及其连接点没有真实引脚或 marker。

普通 pin→短桩→Name、标签直接位于主干上、以及标签本来就是唯一端点的线，不报告。

VOUT3 现场就暴露了这个 bbox 中心误判：属性锚点为 `(510,400)`，位于
`(510,400)→(510,420)` 叶枝末端；文字 bbox 中心却落在短枝中段。第一版检查因此漏报。
改为读取原生 X/Y 后，检查正确报出并修复。

## 正确创建

已有导线：

```bash
easyeda sch netflag --kind net_label --parent-wire <wire-id> \
  --x <on-wire-x> --y <on-wire-y> --net <name>
```

`sch connect --kind net_label --x/--y` 在坐标已落到导线时也会自动改走该无桩路径。
只有目标引脚尚无导线、确实需要建立“pin → 非零短桩 → Name”时才使用 `sch connect`。

## 修复

```bash
easyeda sch label-stub-fix --doc <page> --apply --save
```

执行顺序：

1. 冻结父线树、独立线段、样式、Name 和旧锚点；
2. 删除带装饰叶枝的父线；
3. 按原始独立线段重建、移除只有标签用途的叶枝；
4. 沿剩余主干预测并选择无文字碰撞的安全锚点；
5. 用 `expectedParentWireId` 创建 Name，不创建新线；
6. 复验 `label-stub`、完整 pin→net、bridge 与可见文字；
7. 失败时逐段恢复原始几何和标签。

当前自动执行限单 wire primitive、单可见 Name。多 primitive、多标签、引脚/marker
落在叶枝上或父线歧义时，检查仍报告 ERROR，但执行器 blocked。

## 本工程结果

- 修改前 P1：7 个 `label-stub`（FB1、FB3、VLOAD1/2/4、VOUT2/4）。
- 修改后：`labelStubs:0`。
- 233 个物理脚：修改前后 pin→net/NC 差异 0。
- floating pin、geom/net mismatch、multi-net、wire contact、wire-over-pin、visible
  text overlap、label economy/orientation、frame violation 均为 0。
- bridge：0；仅保留原有 `+3V3` orphan flag。
- `schematic.save` 返回 `saved:true`，随后 doc reload 与 fresh check 均通过。

关键证据 SHA-256：

| 证据 | SHA-256 |
|---|---|
| `P1-final.png` | `3D7837B601DB72160F2C2C3FB5797E676F0022493E7D1B5CD05330185B0539DF` |
| `final-check.json` | `9DC116D378B0381F8E0A729ABCEB20422D4BE805B6107FB2732B759B6DF74C6D` |
| `final-list.json` | `4F9544CDEAE02926A7A49F74CF3208F3B16F749AA2785080196FB35093F68723` |

本轮证据目录：`D:\AI\codex\20261103_label_stub_fix\outputs\`。
