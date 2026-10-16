# v1.13.0 有限 CLI/工作流用例

以下 ID 只定义本次用户批准的有限范围；不沿用完整电路验收中的同名判据。
全部用例在冻结候选 `e5ed0fb` 上现场执行；现场写入只针对 ceshi 专用工程，EDA 标签页全程在后台（hidden）。

| ID | 范围 | 输入、步骤与判据 |
|---|---|---|
| M1 | 软件与候选绑定 | 候选上运行 Go 全仓、连接器测试与 typecheck、发布脚本测试、agent-check、skill-check；现场 CLI/daemon 版本串指向候选提交，连接器源码自连接器构建起无改动。全部通过、0 失败。 |
| F1 | 写入健康度 | 对不存在的原理图器件 id 发一次修改（宿主真实失败，非参数拒绝）后，`health` 的写入健康度给出 `lastFailureError`，含错误码与宿主信息。 |
| F2 | 密页派发预算 | 页面放置 ESP32-S3R8 芯片级块（约 110 个图元）后，`sch export-image` 与 `sch clear` 在后台标签页里于预算内回包成功；清页回读 remaining=0。 |
| E1 | 保存、重载与 DRC | PCB 显式保存（saved:true）、`doc reload` 成功、原生 DRC 完成并返回结构化结果、语义哈希与开始基准一致。本板只导入了器件（另有 2 个 GND 过孔）、无板框无布线，DRC 不以 passed 为判据：违例须全部可由板状态解释（未布线的焊盘/过孔无连接；原理图重新落块后器件实例与 PCB 先前导入的不对应引起的网表不一致），不得出现其他类别。 |
| L1 | 库资产（#279） | 个人库封装 create；数字形状 `[1,w,h]` build 在连接器写入前拒绝；官方字符串元组 build 成功并按 id 回读焊盘存在；工程库封装 build 以 PRECONDITION_REFUSED 拒绝并指引个人库；两个临时封装删除后回读确认。本用例不做开窗与焊盘几何核验。 |
| S1 | 原理图写入修复 | 临时页：保留器件清页后引脚与引脚属性（键、值、坐标）在保存重载后一致（#267）；LED 块 + 分区框后 `zone move` 退出 0、网表对账一致并清旧框重画；typed `schematic.text.move` 平移回读一致、目标不存在时写前拒绝（#212）；`export-image --out x.png` 产出 PNG，`--format svg --out x.png` 拒绝且不写文件。 |
| K1 | 关系块落图 | ESP32-S3R8 芯片级块 `block-apply` 退出 0，锚件需平移时平移成功且无 cmdKey 拒绝，reconcile 全部网络与实时网表一致；外围实线直连按 `sch check` 实测如实记录（不以网表一致代替直连）。 |
| P1 | PCB 写入与检查 | `pcb modify` 翻面回读 layer=2、非法层拒绝、翻回 layer=1（#275）；`pcb group` 整组平移逐件位移一致、组内单件改位置被拒、移回并解散（#173）；规则写入回读核验、重复写入 changes=0、恢复原值（#258）；`pcb check` 输出 ERROR 与 openingInward 计数（#231）。 |
| N1 | 后台标签页 | 留存为 JSON 的现场回包与 `health` 中记录的 `pageVisibility` 全部为 hidden，写入、重载、DRC 与导出照常完成（未留存 JSON 的命令不计入统计）。 |
| R1 | 清理与状态 | 本轮临时页与临时库资产已清除：页面列表仅 P1 与 PCB1，P1 只剩图框，PCB 器件数与语义哈希回到开始基准。 |
| E2E | 延期 | ESP32-S3 芯片级 N8R8 完整流程（#43）进行中：原理图外围实线直连未达标，PCB 阶段未开始；记 in-progress，留待 follow-up。 |
