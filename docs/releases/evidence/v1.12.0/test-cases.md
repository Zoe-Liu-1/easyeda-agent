# v1.12.0 有限 CLI/工作流用例

以下 ID 只定义本次用户批准的有限范围；不沿用完整电路验收中的同名判据。
全部用例在冻结候选 `fd334d9` 上重新现场执行；现场写入只针对 ceshi 专用工程，且 EDA 窗口全程在后台。

| ID | 范围 | 输入、步骤与判据 |
|---|---|---|
| M1 | 软件与候选绑定 | 候选上运行 Go 全仓、连接器测试与 typecheck、发布脚本测试、agent-check、skill-check；现场 CLI/daemon 版本串指向候选提交，连接器源码自候选起无改动。全部通过、0 失败。 |
| F1 | 接入：daemon 自动启动与体检 | 以发布版本号构建的 CLI 在无 daemon 时单发与 3 路并发调用：各只拉起 1 个后台 daemon，停止后无残留进程与启动锁；开发 daemon 不受影响；源码构建不自动启动。`health` 输出逐项体检清单，后台标签页只作备注不告警。 |
| F2 | 读取开销 | 同一板子上 `pcb check` 与 `sch gate` 的审计调用数显著低于 v1.11.0（87 / 67 次），0 次失败调用；检查结论不变（pcb check ERROR=0，严格门禁五项 PASS）。 |
| E1 | 已同步导入与 DRC | 原理图与 PCB 已同步时 `pcb import-changes` 退出 0 并提示"已同步，无需导入"；随后显式保存（saved:true）、doc reload（passed）、原生 DRC passed；PCB 语义哈希不变。 |
| L1 | 写入与检查修复 | 临时页上 block-apply LED 块：connect_pin 全部落定、网表对账一致，用后删页并回读（#208）；`silk-align C1 --side bottom` 无合法位时报"nothing was written"且 appliedIds 为空；`auto-place --dry-run` 计划不压禁布区、不出板；标题栏 Name/Description 显示时 `sch check` 报 2 条 out-of-sheet，隐藏后为 0。 |
| N1 | 后台标签页 | 窗口最小化（hidden，深度节流生效）下：放置器件成功；未知器件 uuid 立即 PRECONDITION_REFUSED 而非挂起；带确认框的导入新增与删除均 verified；原理图与 PCB 的 doc reload 均 passed（#276）；PCB 画布截图明确拒绝；`sch export-image` 成功导出。 |
| R1 | 清理与状态 | 本轮临时页与导入的临时器件已清除：页面列表仅 P1，PCB 恰为 10 个设计器件，语义哈希回到基准 226113afd926c4ee…。 |
| E2E | 延期 | ESP32-S3 需求到原理图/两层 PCB 成品的完整流程本版未在冻结候选上重跑，记 not-run，留待 follow-up。 |
