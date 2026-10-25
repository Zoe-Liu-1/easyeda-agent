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

若线树重建在删除成功后中断，只能从删除前的完整 `Line/style + marker/label ID` 快照恢复。
恢复入口必须先用新鲜回读证明旧 ID 仍缺失，再按小批次创建无 `Net` 属性几何。EasyEDA 会把
共线相接的新段合并并替换 primitive ID，因此验收口径是“每个目标线段都被回读几何完整覆盖”，
不能要求创建调用返回的临时 ID 继续存在。恢复后仍须比较逐脚网表与 bridge，并清除因名称
删除失去落点而留下的零散短枝；不能用一个新标签掩盖 `dangling-wire`。

创建调用返回后，宿主还可能延迟一次或数轮才把新线发布到 `getAll()`。恢复与无标签重建都必须在
同一写锁内执行有界完整回读（250ms 间隔、最多 2 秒），不能把第一次空读当成创建失败并立即回滚。
目标线段也可能被宿主在 T 接点拆成多个同轴段；覆盖判定须合并同轴区间，不得要求单条回读记录
完整包含目标段。回读预算耗尽、库存不可读或身份漂移仍失败关闭。

## Finding 覆盖表

| Finding | 等级 | 执行边界 |
|---|---|---|
| `duplicate-net-marker` / `redundant-net-marker` | 显式计划 | `sch marker-clean`；保留对象需要基线或明确选择 |
| `label-economy` / `cross-kind-label-economy` | 显式计划 | `sch marker-clean`；同一线岛至少留一个可见名称。共享 ID 的候选行按同一线岛合并判断；Power/GND 符号可保留而删除同岛导线 Name。优先用 typed `schematic.net_label.delete`；宿主缺该方法时可显式 `--rebuild-wire-trees`，但只接受单网名且有可见保留符号的完整线树，必须保存原 Line/style、事务重建、恢复失败回滚并复验 pin→net/bridge |
| `marker-overlap` | 直接执行 | `sch destagger --apply` 只处理可安全换向/改短桩的 marker；跳过项必须读出 |
| `direct-connect-opportunity` | 直接执行或显式移动执行 | `already-clear` 用 `sch direct-connect-apply`；文字、单桩 marker、part/group 候选用 `sch direct-connect-fix --allow-move`，移动后必须重新规划成唯一 clear 路径再建线删标签 |
| `cross-chip-direct-wire` | 直接执行或显式阻断 | `sch cross-chip-label-fix` 按物理线树把全部相关芯片引脚改成向外短线 + 同名 `net_label`；有现成 marker、混合网名、未解析引脚或没有安全标签槽时 blocked |
| `alignment-opportunity` | 显式计划 | `alignment-plan` 完整后执行：无阻塞的 ready/边界方案可走 `alignment-fix` 或 `alignment-apply`；存在独立阻塞组时仅当全部为 `ready` 才由 `alignment-fix` 先移阻塞、逐步重算再移主组 |
| `power-cluster-candidate` | 显式计划 | 每个被动件按最近单个消费脚判断；已同岛即局部完成，不要求同芯片其他电源/GND 脚并入同树。先由 `power-cluster-plan` 生成接线与“先接后删”顺序；路径必须无器件/文字/异网线和 90° 交叉，不能直接删符号。playbook 以目标网表和 bridge 增量验收，不被无关的既有整页 finding 阻断 |
| 远置电源被动件 | 显式计划 | 先用 `power-cluster-relocate-plan` 选定同组、同网消费脚并搜索无碰撞刚体平移；目标不唯一时必须用 `--pair`。执行“平移 → fresh readback → 重新运行 `power-cluster-plan` → 接线/删除新 flag”。移动前 flag ID 在移动后失效，不能直接跨阶段删除 |
| `floating-pin` | 语义选择 | 设计上使用则接线；明确不用才 `sch no-connect`；未知不等于 NC |
| `geom-net-mismatch` / `net-marker-mismatch` | 语义选择或重排 | 先确定触脚但网表未认领的原因，再重连或修名称；不能靠重导网表掩盖 |
| `dangling-wire` / `zero-length-wire` | 语义选择 | 先确认是否为有意命名端点；删除局部残支前核对共享树 |
| `long-wire` | 诊断或重排 | 单物理段超过 `100 raw` 只作 INFO；用 `alignment-plan` / `direct-connect-plan` 验证完整功能组和障碍后再决策。不能因为长度直接移动器件或拆开真实直连 |
| `multi-net-wire` / `wire-bridge` / `wire-crossing` / `wire-contact` / `non-orthogonal-wire` | 重排重布 | 先找短路/并网来源，拆树或按正交通道重走；`wire-contact` 表示异网导线的端点、T 接或共线重叠已经形成真实接触，必须按物理线岛拆开重布，不得只删接触点。当前没有通用自动重连入口，complex/shared tree 保持人工或功能组重排 |
| `wire-over-pin` / `wire-over-pin-lead` / `wire-through-body` | 直接执行或重排重布 | `wire-reroute-fix` 只替换单段隔离线并搜索障碍感知正交通道；多段/共享树、端点不明或无路径保持 blocked |
| `pin-exit-direction` | 重排重布 | 首段先沿引脚外向再正交折线；`wire-reroute-fix` 只在完整单段替换可行时执行，多段共享树由对应功能级执行器重建 |
| `signal-netflag-type` / `signal-netport-type` | 直接执行或显式阻断 | `signal-marker-fix` 保留既有线树，在端口落点唯一命中的父线上创建文字 net_label；原位碰撞时只允许沿同一父线移动标签锚点并重验完整 bbox。父线已有同名标签、父线归属不明或搜索范围内无安全槽时阻断。模块边界端口用 `signal-port-exempt` 保留并审计 |
| `power-ground-orientation` / `reversed-net-flag` | 直接执行或显式阻断 | `power-orientation-fix` 优先重接单桩 netflag；首段沿引脚外向，最终 body 朝上/下。共享/分支树无法重走时，可从原锚点或有界 A* 正交通道接到安全竖排锚点；移挂只允许发生在原内部结点，原端口是叶端点时必须从原锚点绕障，避免留下 dangling stub。布线段与预测标记占位都必须避开器件引脚 lead、异网导线和图签，并复验最终进线方向。Apply 逐项核对 pin→net、bridge、dangling-wire 与目标 finding，失败恢复原符号/支路。反向脚保持 blocked 并登记 `orientationExempt` |
| `label-visibility` / `label-orientation` | 直接执行或显式阻断 | `sch label-fix` 设置可见和水平；空名称只有父 wire/marker 已携带明确网名时补齐。缺原生 Name 或网名未知不猜 |
| `label-distance` | 语义选择或重排 | 用 `attribute-move` 把可见名称移回挂载点附近，并复查文字/导线碰撞 |
| `polarity-convention-outlier` | 语义选择 | 对照器件极性和同页多数派确认是否反装；编号约定本身不证明电气极性 |
| `visible-text-overlap` | 直接执行或显式阻断 | `visual-text-fix` 在 60 单位内搜索无碰撞槽位，属性/自由文字分别走 typed move；无锚点或无可清槽位保持 blocked |
| `text-frame-crossing` | 重排重布或显式阻断 | 当前没有独立的跨框文字移动闭环；根据实测 bbox 把文字移入所属框，或重新计算功能框/功能组。测量缺失、归属不唯一或框样式未认证时保持 blocked |
| `designator-overlap` / `designator-wire-overlap` | 直接执行或显式阻断 | `designator-fix` 只移动 Designator 属性，5 单位网格避开本体、引脚、marker、文字、导线、图签与纸边；无安全槽保持 blocked |
| `marker-wire-overlap` | 重排重布 | 与 bridge-check 使用同一约半格连接容差，排除标识自身的父线端点；只有异线穿入 bbox 才报告。修复时移动 marker 或改走线，并复查器件、引脚、导线和标题区 |
| `titleblock-overlap` / `out-of-sheet` | 重排重布 | 移入图签外/图纸内；不缩小页面或隐藏文字来规避 |
| `partition-overlap` / `part-out-of-frame` / `part-frame-unresolved` / `designator-out-of-frame` | 重排重布 | 重新布局功能组、区框或文字槽位；不能删除位号或把未认领器件硬塞进框 |
| `missing-partition` / `missing-titleblock` / `missing-note` / `zones-not-claimed` / `titleblock-key-absent` | 显式计划或语义选择 | `zones` / `zone-draw` / `titleblock` 按已声明归属生成；字段内容无依据时保留待确认 |
| `peripheral-not-direct` | 重排重布 | 把外围真实短线接到核心脚；标签只作证不清白时的兜底并继续报警 |
| `*-unavailable` / `*-unverified` / `peripheral-direct-unverified` | 仅诊断 | 修复采集或写入数据源后重跑；不得把不可见范围当通过。连接器缺少 `schematic.visible_text.bboxes`、标签 rotation 或文字 bbox、`schematic.text.list`、`schematic.titleblock.get` 均必须产生 finding；strict 不得跳过这些证据 |

## 收尾判据

一次修复只有在原 finding 消失、完整 pin→net 表与预期一致、无新增 bridge、bridge-check
通过、保存返回 `saved:true`，并在 save/reload 后 fresh readback 一致时才算完成。
DRC 的 WARN 仍要单独解释；不能用 bridge-check 或 DRC 之一代替其他覆盖范围。
