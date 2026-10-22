# 原理图检查与修复覆盖

检查发现说明“画布与规则不符”，不自动授予修复动作。先按本页给 finding 归类，再从
最新快照生成计划；修复后重新运行原 finding、完整网表和 bridge-check。

## 修复等级

| 等级 | 含义 | 必须满足 |
|---|---|---|
| 直接执行 | 已实现带前后验证的 typed 命令 | 默认 dry-run，显式 `--apply`；回读证明保存与电气结果 |
| 显式计划 | 工具能算完整计划，但必须选定对象或目标 | 计划 `ready`；候选/来源不代替参数 |
| 语义选择 | 修复会改变设计含义或表示法 | 人工/代理必须先决定“接到哪里、保留什么” |
| 重排重布 | 需要移动器件、标签或重走整棵线树 | 用布局/布线 planner 重算，局部补线不能替代 |
| 仅诊断 | 数据源、规则或写入接口尚不足以安全执行 | 保留 finding 与证据，标 `unsupported` / `incomplete` |

## 名称去重与来源

`duplicate-net-marker`、`redundant-net-marker`、`label-economy` 与
`cross-kind-label-economy` 是候选组，不是删除清单。primitiveId 的字典序、创建时间或
“看起来像新对象”都不构成来源证据；没有移动前基线时只报告候选。

有唯一基线时，可以保留基线中的可见名称并删除新增对象。执行统一走：

```text
sch marker-clean --doc <page>
sch marker-clean --doc <page> --delete <explicit-ids> --apply --save
```

命令拒绝非候选、隐藏名称和会删除线岛最后一个可见名称的请求。删除后必须证明对象已消失、
完整 pin→net 表不变；变化时恢复已删名称并回读。`zone-arrange --apply` 只提示这类候选，
不得在收尾时自动扫删。move kernel 在每次移动后比较移动前后 marker ID：本次新生 marker
若在同树同名上形成重复，报告 `NewNameDuplicates` 并拒绝把操作判成完成，避免完整网表
通过却把视觉重复留在页面上。

## Finding 覆盖表

| Finding | 等级 | 执行边界 |
|---|---|---|
| `duplicate-net-marker` / `redundant-net-marker` | 显式计划 | `sch marker-clean`；保留对象需要基线或明确选择 |
| `label-economy` / `cross-kind-label-economy` | 显式计划 | `sch marker-clean`；同一线岛至少留一个可见名称 |
| `marker-overlap` | 直接执行 | `sch destagger --apply` 只处理可安全换向/改短桩的 marker；跳过项必须读出 |
| `direct-connect-opportunity` | 直接执行或显式移动执行 | `already-clear` 用 `sch direct-connect-apply`；文字、单桩 marker、part/group 候选用 `sch direct-connect-fix --allow-move`，移动后必须重新规划成唯一 clear 路径再建线删标签 |
| `cross-chip-direct-wire` | 直接执行或显式阻断 | `sch cross-chip-label-fix` 按物理线树把全部相关芯片引脚改成向外短线 + 同名 `net_label`；有现成 marker、混合网名、未解析引脚或没有安全标签槽时 blocked |
| `alignment-opportunity` | 显式计划 | `alignment-plan` 完整后执行：无阻塞的 ready/边界方案可走 `alignment-fix` 或 `alignment-apply`；存在独立阻塞组时仅当全部为 `ready` 才由 `alignment-fix` 先移阻塞、逐步重算再移主组 |
| `power-cluster-candidate` | 显式计划 | 先由 `power-cluster-plan` 生成接线与“先接后删”顺序；路径必须无器件/文字/异网线和 90° 交叉，不能直接删符号。playbook 以目标网表和 bridge 增量验收，不被无关的既有整页 finding 阻断 |
| `floating-pin` | 语义选择 | 设计上使用则接线；明确不用才 `sch no-connect`；未知不等于 NC |
| `geom-net-mismatch` / `net-marker-mismatch` | 语义选择或重排 | 先确定触脚但网表未认领的原因，再重连或修名称；不能靠重导网表掩盖 |
| `dangling-wire` / `zero-length-wire` | 语义选择 | 先确认是否为有意命名端点；删除局部残支前核对共享树 |
| `multi-net-wire` / `wire-bridge` / `wire-crossing` / `non-orthogonal-wire` | 重排重布 | 先找短路/并网来源，拆树或按正交通道重走；不得只删交叉点 |
| `wire-over-pin` / `wire-over-pin-lead` / `wire-through-body` | 直接执行或重排重布 | `wire-reroute-fix` 只替换单段隔离线并搜索障碍感知正交通道；多段/共享树、端点不明或无路径保持 blocked |
| `pin-exit-direction` | 重排重布 | 首段先沿引脚外向再正交折线；`wire-reroute-fix` 只在完整单段替换可行时执行，多段共享树由对应功能级执行器重建 |
| `signal-netflag-type` / `signal-netport-type` | 直接执行或显式阻断 | `signal-marker-fix` 把安全单桩替换为文字 net_label；共享/分支树阻断。模块边界端口用 `signal-port-exempt` 保留并审计 |
| `power-ground-orientation` / `reversed-net-flag` | 直接执行或显式阻断 | `power-orientation-fix` 重接单桩 netflag；首段沿引脚外向，最终 body 朝上/下。布线段与预测的标记占位都必须避开所有器件引脚 lead，候选终点落在异网导线上时硬拒绝。Apply 失败按本次新建 marker/wire 的显式 primitiveId 删除并逐 ID 回读，不能改用会被多段共享线拒绝的 `disconnect` 做回滚。共享树、无单桩和反向脚保持 blocked，反向脚登记 `orientationExempt` |
| `label-visibility` / `label-orientation` | 直接执行或显式阻断 | `sch label-fix` 设置可见和水平；空名称只有父 wire/marker 已携带明确网名时补齐。缺原生 Name 或网名未知不猜 |
| `label-distance` | 语义选择或重排 | 用 `attribute-move` 把可见名称移回挂载点附近，并复查文字/导线碰撞 |
| `polarity-convention-outlier` | 语义选择 | 对照器件极性和同页多数派确认是否反装；编号约定本身不证明电气极性 |
| `visible-text-overlap` | 直接执行或显式阻断 | `visual-text-fix` 在 60 单位内搜索无碰撞槽位，属性/自由文字分别走 typed move；无锚点或无可清槽位保持 blocked |
| `designator-overlap` / `designator-wire-overlap` | 直接执行或显式阻断 | `designator-fix` 只移动 Designator 属性，5 单位网格避开本体、引脚、marker、文字、导线、图签与纸边；无安全槽保持 blocked |
| `marker-wire-overlap` | 重排重布 | 移动 marker 或改走线，必须复查器件、引脚、导线和标题区 |
| `titleblock-overlap` / `out-of-sheet` | 重排重布 | 移入图签外/图纸内；不缩小页面或隐藏文字来规避 |
| `partition-overlap` / `part-out-of-frame` / `part-frame-unresolved` / `designator-out-of-frame` | 重排重布 | 重新布局功能组、区框或文字槽位；不能删除位号或把未认领器件硬塞进框 |
| `missing-partition` / `missing-titleblock` / `missing-note` / `zones-not-claimed` / `titleblock-key-absent` | 显式计划或语义选择 | `zones` / `zone-draw` / `titleblock` 按已声明归属生成；字段内容无依据时保留待确认 |
| `peripheral-not-direct` | 重排重布 | 把外围真实短线接到核心脚；标签只作证不清白时的兜底并继续报警 |
| `*-unavailable` / `*-unverified` / `peripheral-direct-unverified` | 仅诊断 | 修复采集或写入数据源后重跑；不得把不可见范围当通过 |

## 收尾判据

一次修复只有在原 finding 消失、完整 pin→net 表与预期一致、无新增 bridge、bridge-check
通过、保存返回 `saved:true`，并在 save/reload 后 fresh readback 一致时才算完成。
DRC 的 WARN 仍要单独解释；不能用 bridge-check 或 DRC 之一代替其他覆盖范围。
