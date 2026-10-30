# 原理图对齐检查与隐藏根层网络名清理

本例记录两类容易“检查通过但图面仍错”的现场问题：偏轴折返的移动感知对齐，以及
可见、却不被批量属性枚举返回的 root-owned `_NETLABEL_`。它说明 finding、计划、
执行和回读证据必须分层，不提供可复制到其他工程的绝对坐标或删除清单。

**状态：`live-verified`，仅本例记录的检查/计划、P1 目标效果的现场完成、
FB1 标签清理及保存重载范围。** 标准 `alignment-apply` 的宿主成功路径未在本轮获得
完成证据：它在部分写入后命中 `component.modify` 故障，随后由完整快照恢复并以小事务完成。
完整页面仍有旧导线交叉、长线 INFO、对齐/电源簇建议和电源朝向等待办，不由本例签通过；
P2、PCB、制造与硬件未验证。

## 来源与开始状态

- 工程：`moto_AI_New Project_2026-10-15_22-26-53_frame-route-fixed`
- 工程 UUID：`be297f5c4c1fc17220df6fefb3072882ce8bdfd33ee1f74ff29ab671521411b0`
- 页面：P1，文档 UUID `d4149979420e8f30`
- 坐标：原理图 raw，y 向上，器件落点按 5 raw 栅格
- 连接器：`1.14.19`

P1 的 `VOUT1` 主骨架由多段 wire primitive 组成，当时呈现 `H-V-H` 偏轴折返。
候选把 `J12/R4/R5/R6/R7` 作为外围组移动，同时保留 `J4` 为功能框边界锚点；
跨锚点的 `VOUT1/FB1/JSELA1/JSELB1/VLOAD1` 和本地 GND 支路必须按移动后的真实引脚重建。

FB1 图面上另有一个陈旧名称：源记录为 `FB1`，坐标 `(685,720)`，
`parentId:"$$root"`，精确 primitiveId `e59fd4e78cc9f8ea`。它会被导出和渲染，
但不在 `sch_PrimitiveAttribute.getAll()`、`getAllPrimitiveId()` 或
`schematic.visible_text.bboxes` 的批量结果中。因此“结构化枚举没有该对象”不能证明图中没有。

## 可迁移规则

1. **端点方向不能按数组第一项猜。** 先把端点/T 接的 wire primitives 组成物理骨架；
   比较两侧臂偏移时，取每条臂远离共享主干的外端点。EasyEDA `flat-segments`
   不保证端点存储方向；固定使用坐标数组第一端会把反向存储的短折返误算成零偏移。
2. **布局候选同时检查 X/Y，而非沿用原线段方向。** 只有完整移动组、移动后的 body、
   全部 pins、专属导线和实际有面积的可见文字均通过碰撞与功能框检查，计划才可执行。
   零尺寸空属性 bbox 不是可见障碍。
3. **功能组允许边界分解。** 完整刚体越框或自身碰撞时，保留最多 4 个
   `anchorDesignators`，把原跨边界网列入 `sharedWireIds` 并重建；不能把连接器拖出框，
   也不能只移动器件留下断线或旧标签。
4. **已经落在目标主干轴线上的引脚直接收线。** 主干应终止于该 pin，不再补外向 lead
   后折回；否则会留下视觉小尾巴，使本已直线的网络仍有转角。
5. **finding、plan、apply 是三个状态。** `alignment-opportunity` 只指出候选；
   `alignment-plan` 只读生成资源、锚点、共享线和阻塞项；只有无 blockers 的
   `ready`/已完整重连的 `rewire-required` 才可进入 apply。
6. **隐藏对象只按精确 ID 处理。** root-owned `_NETLABEL_` 必须从页面原语或源快照取得
   精确 ID 和父对象；连接器先 `get(id)`，优先调用属性实例 `delete()`，宿主未暴露时
   才回退到官方 `sch_PrimitiveObject.delete([id])`。禁止按 `_NETLABEL_` 前缀全页扫删。
7. **删除对象不等于删除网络。** 只能删除已由源记录证明是陈旧根层对象的 ID；
   线岛上真正承载连接的可见名称必须保留，并在删除前后比较完整 pin→net。

## 实际检查与执行形式

每次写入前都重新读取同一工程、同一页面；以下只表示命令职责，不复制本次 ID 到新工程。

```bash
easyeda sch check --doc <page> --strict --json --out check.json
easyeda sch alignment-plan --doc <page> --net <net> --json --out plan.json
easyeda sch alignment-apply --doc <page> --plan <plan-id> --out dry-run.json
easyeda sch alignment-apply --doc <page> --plan <plan-id> --apply --save
```

1. 从 fresh `sch check` 和 `alignment-plan` 对账 finding、移动成员、锚点、
   `sharedWireIds`、边界草案及 blockers。重复规划必须一致，dry-run 通过后才允许写入。
2. 本工程的第一次标准 `alignment-apply` 命中宿主已知故障：移动三个器件并移除旧
   `VOUT1` 树后，`component.modify` 报 `cmdKey` 解构错误。不得把已恢复电气网的物理标签
   当成目标图面完成。
3. 使用写入前完整快照回滚：恢复三个器件位姿、37 个受影响线段几何、三个本地 GND
   符号及五个相关标签 bbox；保存、重载、fresh 回读后再重算。
4. 第二次按小事务完成：删除计划中的旧局部树 → 每次只移动一个器件 → 从移动后引脚
   外向 lead 重建共享边界网 → 每个线岛只保留一个可见名称。任何一步失败都停止并重算，
   不跨阶段复用 stale ID。
5. FB1 最终主线的 `U5.FB` 与 `R4.FB1` 同为 `y=670`；主线
   `637ac5666cf56734` 从 `(470,670)` 直接到 `(900,670)`。R4 本体在 `(900,650)`，
   不再为已对齐引脚补折返尾巴。
6. 陈旧根层标签 `e59fd4e78cc9f8ea` 只按精确 ID 删除。连接器返回
   `deleted.attributes:1` 后，仍必须直接 `get(id)` 验证不存在；调用返回 `true` 不是证据。

## 结果与验证

- 保留主线可见标签 `3855bb54811fa53c`，父线 `637ac5666cf56734`。
- 保留 J4 线岛标签 `f2dee78c0c193845`，父线 `e6c328c76919efbd`，
  几何 `(700,545)→(700,585)`。
- 删除陈旧根层标签 `e59fd4e78cc9f8ea`；保存、关闭、重开后的直接 `get(id)`
  返回 `found:false`，最终渲染不再显示该名称。
- `schematic.save` 返回布尔 `saved:true`；save → close/reopen → fresh readback 一致。
- 定向结果是 floating pins、geom/net mismatch、multi-net wire、wire contact、
  wire-over-pin、visible-text overlap、text/frame crossing、pin/wire frame violation 均为 0。
- 完整页面并未变成“零 finding”：仍保留两条既有 wire crossing、长线 INFO、
  VOUT1 对齐候选、电源簇建议及三个电源/GND 朝向 finding。它们属于后续独立任务，
  不能因本次标签清理而隐藏或宣称全页通过。

关键证据（SHA-256）：

| 证据 | SHA-256 |
|---|---|
| `P1-final-readback.json` | `27395657106F95825BDE4FBBF4458D7E6C423DB938F369607B484227E67B0F5F` |
| `P1-final-check.json` | `D5005E7303B4A1E0C3BF32D49BD3FF977184FE3300CD5D2E2DDBD53530E31D0C` |
| `delete-orphan-FB1-v11419.json` | `9D7673DC3AAAA2AEF9D9E2BA7A2B7BBFA82FCAFB660FFEB18FC5C45CD781D956` |
| `P1-final.png` | `ABEB395F83E3A6D44FFEAC410B9A54D58AE3A3CD8540FBBA7BB311E7E239ED1E` |

本次证据目录为 `D:\AI\codex\20261030_fb1_label_final\outputs\`；哈希用于确认复核时
读取的是同一批次，不代替新工程的现场回读。

## 覆盖边界

- 覆盖：同一物理骨架的对齐候选、边界锚点、共享树计划、frame-aware 碰撞、
  小事务执行、失败恢复、显式 ID 删除、保存重载与图面复核。
- 不覆盖：看不到源记录或页面原语时“猜测”隐藏对象、无界全局清理、所有宿主版本上的
  `component.modify`、任意复杂共享树自动重连、P2/PCB 回归及全页既有 finding 清零。
- 若标准 apply 在首个 mutation 前就失败，无须制造“部分成功”；若已部分写入，
  必须先 fresh 回读并按快照恢复，不能继续执行旧队列。
