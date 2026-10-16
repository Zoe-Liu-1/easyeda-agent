# v1.13.0 有限 CLI/工作流验收报告

用户于 2026-10-16 批准发布 v1.13.0（首轮发现问题后选择"先修再发"），沿用有限范围。本报告使用 schema 3，
通过范围为 cli-fixes；完整设计 E2E 延期至 follow-up。有限软件通过不代表电路或制造通过。
产品源码绑定 dev 提交 `e5ed0fb`；现场执行时间 2026-10-16T02:33:20Z–02:36:14Z（UTC，L1/S1/K1/F2/P1/F1/E1/N1/R1
一次连续执行），M1 在同一候选上于其前完成。

## 候选演变

- 首轮候选 `4ba12a2`：S1 中 `zone move` 退出 1——器件已移动保存、网表一致，但重画分区框时旧框删除"未核验"。
  当天另以同流程复现 4 次：2 次移动成功；2 次因目的地压图签禁区被硬拒（拒绝正确，未走到重画框）。定位：清框
  内联在 exec_js 中，删完立刻回读，未落定快照会把刚删的框报成 survived；且 exec_js 不属于工程写入的 typed 路径。
  修复 `99236af`：改走 typed `schematic.primitives.delete` 逐个删除 + `deleteVerifiedOneByOne` 落定回读。现场 3 轮
  `zone move` 与 `zone-draw --clear` 均退出 0 并报告清除 2 个旧框；另一次"清框 → 重载 → 回读矩形/文本计数为 0"
  的持久化检查结果只在会话中观察，未留存原始记录。
- 第二轮候选 `99236af`：S1 通过；K1 后的密页 `sch export-image` 在 18s 处 DISPATCH_FAILED（与 9.6s 的
  防抖自动保存并发），R1 `sch clear` 超过派发时限后才落地。另发现现场 daemon 版本串仍为 `4ba12a2-dirty`
  （提交后未重编），该轮不作为候选证据。修复 `e5ed0fb`：clear/export 派发预算 120s/90s（经 `timeoutMs`
  传到 daemon），并在提交后触发重编使版本串绑定候选。
- 第三轮候选 `e5ed0fb`：本报告结果。

## 已完成的软件验证

- 候选 Go 全仓测试：5,606 项通过、0 失败、1 项跳过，15 个包。
- 连接器测试：1,177 项通过、0 失败；typecheck 通过。
- 发布脚本测试：123 项通过；agent-check 通过；skill-check：189 个 tracked 文件通过。
- 现场 CLI/daemon 版本串 `v1.12.0-24-ge5ed0fb-dirty`（dirty 仅为未提交的连接器版本号；执行时的工作区状态未单独留档）；
  连接器开发包 `dist/index.js` SHA256 `c73809aa511308c1bda73b64eff08c226ba23568f69933f33b13378c09b9ed43`，经 IndexedDB
  热重载装入（现场 1.12.19，热重载后 health 换新 windowId），`extension/src` 自 `4ba12a2` 至候选无改动。
- 五平台交叉构建与资产核对由 release-build 执行，不代表其他平台运行时验证。

## 现场回读

EDA Web 4.1.78，标签页全程后台：留存为 JSON 的 24 个回包与 1 次 health 中共 53 处 `pageVisibility` 均为 hidden
（每个回包含 context 与 mutationContext 两处）；落块、zone move、连线、建删页等未留存 JSON 的命令不在该统计内。

L1：个人库 create 退出 0；数字形状 `[1,23.622,11.811]` build 退出 1，连接器在写入前以 PRECONDITION_REFUSED 拒绝
（"must be an official pad-shape tuple"），随后同一空封装的合法 build 成功，说明未写入；字符串元组 build 退出 0，
`created.pads=2`、`verified:true`（保存后按 id 回读 2 个焊盘存在；`apertureVerifiedPads` 为 0，本输入未给开窗，
未做开窗与焊盘几何核验；`lib footprint get` 不返回几何）；工程库封装 build 退出 1，`PRECONDITION_REFUSED`：
"EasyEDA does not open project-library footprints for API editing…create the footprint in the personal library"；
两个临时封装 delete 均 `deleted:true, verified:true`。

S1：临时页放置 CX1 并接两脚；`clear --preserve-parts` 退出 0、`preservedPinIds` 列出 2 个引脚、remaining=0；
`saved:true` 后 doc reload，引脚 id 列表与 6 条引脚属性（键、值、坐标）与清页前完全一致（引脚属性经 `debug exec`
只读读取：现无列出引脚自有属性的 typed 读接口）。第二临时页 LED 块 reconcile 3 网一致；zone-draw 退出 0；
`zone move --dx 100` 退出 0，"网表逐引脚一致、无新增 bridge"，清 2 个旧框图元并重画 1 个分区框（重连有 2 处
"label overlaps a part bbox" 警告）；`schematic.text.move` 将框标题从 (349.5,344) 平移到 (399,364) `verified:true`，
回读 (399,364)；不存在 id 写前 `PRECONDITION_REFUSED`；`export-image --out .png` 为 PNG 2362×1672，
`--format svg --out .png` 退出 1 "conflicts with --out extension"，文件未生成。两临时页删除并回读确认。

K1：`block-apply esp32s3r8_chip_minsys` 退出 0（49s），锚件 U2"已平移 (-80,60)"，0 次 cmdKey 拒绝，
"reconcile ✓ 16 net(s) match the live netlist"。同一输出报 `clusters ✗`：块实测 1080×708，放不进被图签切成 L 形的
可用区；并警告 marker 晕圈放不下、seed 坐标不是已验证布局。`sch check` 共 3 条：floating-pin（U2 36 个未用 GPIO
未打 NC）、missing-partition（13 个功能模块仅 4 个分区框）、peripheral-not-direct（模块外围未全部以实线接到核心，
点名 EN 支路在 C3.1 处分成两段实线孤岛）。本用例 pass 仅限"落块退出 0、锚件平移无 cmdKey、16 网对账一致"，
不代表布局或外围直连合格，后者属 #43 未完成项。

F2：同一密页 `export-image --format png` 退出 0（3s，PNG 2362×1672，744,938 字节）；R1 中 `sch clear` 退出 0
（整条命令 19s），删除 134 个对象（器件 16、netflag 27、netport 32、导线 59），remaining=0。本轮两项均未超过原
20s 默认预算，第二轮的失败条件（与自动保存并发）未复现；120s/90s 预算的下发由 `TestSchClearAndExportSendHeavyBudgets`
证明，现场只证明该路径在后台密页上完成。

P1：R2 `modify {"layer":"bottom"}` 回读 layer=2，`"inner1"` 退出 1，`"top"` 回读 layer=1（坐标不变）；`group create C1,C3`
得 g1，`group move (100,-50)` "every member displaced by exactly (100, -50)"，组内 C1 单独改坐标退出 1 "partially covers 1
virtual PCB group"，移回 (-100,50) 后解散；`config track copperThickness2oz` 0.254→0.2032 mm changes=1/verified，
重复写入 changes=0/verified，恢复 0.254 mm changes=1/verified；`pcb check` 退出 0，ERROR=0、WARN=18、openingInward=0。

F1：不存在器件 id 的 `schematic.component.modify` 退出 1（`INVALID_STATE`），随后 `health` 的
`lastFailureError` 为 "INVALID_STATE: No schematic component found with primitiveId …"，lastFailureAction 为该动作。

E1：`pcb save` `saved:true`；`doc reload PCB1` 成功（saved → closed → reopened）；原生 DRC 完成，`passed:false`，
结构化违例共两类：Connection Error 57 条（55 个 SMD 焊盘 + 2 个 GND 过孔，均为 No Connection；按网 GND 15、3V3 13、
MCU_N3 5、EN/MCU_N6/N7/N8 各 3、IO0/MCU_N9–N13 各 2），与 Netlist Error 1 条（推断为：E1 在 K1 之后执行，P1 上重新落块的
器件实例与 PCB 先前导入的不对应；DRC 原文未给差异明细）；无其他类别。dump 语义哈希与开始基准一致，16 个器件。

R1：页面列表仅 P1 与 PCB1；P1 回读（含页面图元）仅剩图框 1 个器件及其 32 个属性，矩形、文本、导线均为 0；
PCB dump 16 个器件，语义哈希 `1a625faf0ee47601…` 与开始基准一致。

| ID | 状态 | 证据与限制 |
|---|---|---|
| M1 | pass | 候选 e5ed0fb：Go 5606/0、连接器 1177/0、typecheck、发布脚本 123、agent/skill-check 通过；版本串含 ge5ed0fb，连接器源码自构建起未改。 |
| F1 | pass | h1-fail 退出 1 INVALID_STATE；h1-health 的 lastFailureError 含错误码与宿主信息"No schematic component found…"。 |
| F2 | pass | f2-export 密页 PNG 3s 退出 0；r1-clear 密页删除 134 个对象、remaining=0；现场未超过原默认预算，预算下发由单测证明。 |
| E1 | pass | e1-save saved:true；e1-reload 成功；e1-drc 完成，违例仅 57 条未布线无连接与 1 条网表不一致（均由板状态解释）；e1-dump 语义哈希等于基准。 |
| L1 | pass | b1-bad 连接器写前拒绝未写入；b1-good 2 焊盘按 id 回读存在（未做开窗与几何核验）；b1-pbuild PRECONDITION_REFUSED 指引个人库；b1-del1/del2 verified。 |
| S1 | pass | s1 引脚与 6 条属性重载后一致（debug exec 只读）；zone move 退出 0 清旧框重画；text.move 回读 (399,364)；缺失 id 写前拒绝；export 格式推断与冲突拒绝。 |
| K1 | pass | k1-apply 退出 0、锚件平移 (-80,60)、cmdKey 0 次、reconcile 16 网一致；clusters ✗ 与 sch check 3 条发现如实记录，布局与直连不在通过范围。 |
| P1 | pass | 翻面 2→1、非法层拒绝；编组位移精确、组内单件被拒、移回解散；规则 changes 1/0/1 均 verified；pcb check ERROR=0。 |
| N1 | pass | 留存的 24 个回包与 health 共 53 处 pageVisibility 均为 hidden，写入、重载、DRC、导出与清页均完成。 |
| R1 | pass | r1-docs-after 仅 P1 与 PCB1；r1-p1-primitives 仅图框（矩形/文本/导线 0）；r1-pcb-after 16 个器件，语义哈希与基准一致。 |
| E2E | in-progress | #43 ESP32-S3 芯片级 N8R8：原理图外围实线直连未达标（peripheral-not-direct），PCB 阶段未开始；延期 follow-up，不补签。 |

## 失败与未完成项

- 完整 E2E 与 B00–B10 未在冻结候选上执行；#43 原理图外围直连、分区与布局未达标，干线直连实现两轮现场不收敛，未合入（本地 WIP 补丁保留）。
- E1 的 DRC 在无板框无布线的板上为 `passed:false`，只证明后台保存/重载/DRC 路径可用与违例归类正确，不代表任何板级通过。
- F2 现场未触发超过原 20s 默认的情形，放宽预算的效果只由单测与第二轮失败的反面证据支撑。
- 宿主在某些页面状态下静默忽略删除（首轮之前一次 108 个图元清页 0 删除，doc reload 后恢复），根因未明；本版清框与清页均以回读判定，不会把未删报成已删。
- 分区框绘制仍经 exec_js（清除已改 typed）；引脚自有属性缺 typed 读接口，S1 的该项回读经 debug exec 只读完成。
- S1 的 zone move 重连有 2 处标签压器件 bbox 的警告（布局质量，不影响连接）。
- 宿主由 4.1.71 升级到 4.1.78，本轮只覆盖上述用例涉及的接口。
- E1、L1、N1 的判据在执行后经独立复核按实际证据收窄（不改结果，只收窄声称范围）；R1 的页面图元回读晚于 R1 约 11 分钟，期间未执行写入命令，但复核者无法独立核实。

原始记录保留在本地忽略目录 artifacts/v1.13.0-evidence/（含首轮与第二轮的归档）；公开材料不包含用户路径、工程 UUID 或私有设计数据。

## 独立复核

独立 Agent 于 2026-10-16 只读复核两轮，结论 pass（首轮 conditional，所列 13 项缺口——E1 网表归因与过孔、N1 计数口径、
L1 开窗/几何与"离线拒绝"表述、S1 debug exec 读取披露、K1 漏报 clusters 与 sch check 发现、F2 预算未在现场触发、
候选演变缺原始记录、R1 页面图元、skill-check 表述——均已修正并经第二轮确认关闭）。复核重新核对了候选绑定
（e5ed0fb，自 v1.12.0 起 24 个提交，extension/src 自 4ba12a2 无改动，dist/index.js SHA256 一致）、第三轮全部原始输出的
退出码、计数与坐标，三份 PCB dump 的 semanticSha256（均为 1a625faf…）、DRC 两类违例的逐网合计（57 + 1），以及
99236af 与 e5ed0fb 的源码改动，并重跑 7 项聚焦单测全部通过。复核者没有访问现场 EDA，也没有执行 easyeda 命令；
执行时的工作区 dirty 状态、R1 图元回读前有无写入、只在会话中观察的重载计数无法独立核实。剩余限制：F2 的放宽预算
只由单测证明；L1 未做开窗与焊盘几何核验；S1 的引脚属性回读经 debug exec 只读完成；K1 的布局与外围直连及 E2E
不在通过范围。

## 发布准备

具体版本与有限范围已获用户批准。正式发布仍由现有 release-check/release-build/publish-release
路径生成并核对版本、资产及远端 SHA256；正式非草稿 Release 完成前不宣称已发布。
