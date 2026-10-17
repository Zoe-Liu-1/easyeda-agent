# v1.14.0 有限用例

以下 ID 只定义本次用户批准的有限范围；不沿用完整电路验收中的同名判据。
全部用例在候选 `f2369a5` 上执行；现场写入只针对 ceshi 专用工程的原理图 P1。F1、L1、G1、F2、E1、N1、R1
在同一轮连续执行（标签页处于后台），R1 为该轮最后一步。

| ID | 范围 | 输入、步骤与判据 |
|---|---|---|
| M1 | 软件与候选绑定 | 候选上运行 Go 全仓、连接器测试与 typecheck、发布脚本测试、agent-check、skill-check，全部 0 失败；现场 CLI/daemon 版本串指向候选提交，连接器由候选源码构建并热重载为 1.14.0。 |
| F1 | 功能框落块 | 清页后 `sch block-apply esp32s3r8_chip_minsys --at 280,600`：退出 0，5 个框（主控、滤波电容、复位与启动、晶振电路、SPI Flash）逐框落地，每框 reconcile 与实时网表一致。 |
| L1 | 网络标签 | F1 之后读页面图元：连接汇总 netports=0（信号网没有六边形端口）；原生网络标签按「导线上可见的 Name 属性」统计（连接汇总的 netlabels 计数不含这类导线属性）；晶振框内 XTAL_P、XTAL_N 各只有干线末端 1 个（另各有主控引脚上 1 个用于框间互连）。 |
| G1 | 整体移动 | 4 次 `sch group-move`（滤波、复位各上移 10；晶振 (-25,85)；Flash (395,250)）：每次退出 0、内核对账绿（网表逐引脚一致、无新增 bridge）；含框内直连导线的框报告"刚性重建"；全页网络标签数量移动前后一致。 |
| F2 | 分区框 | `sch zone-plan --json` 六项计数全 0；相邻分区框之间至少隔 12（gutter）；`sch zone-draw` 退出 0、画出 5 框；`sch text-list` 回读到 5 个功能名标题各 1 个。 |
| E1 | 保存、重载与门禁 | `sch save` 返回 `saved:true`；`doc reload` 成功；重载后 `sch gate` 五项（layout-lint、clusters、check、bridge-check、drc）全部 PASS，`zone-plan --json` 六项仍全 0、框坐标不变。 |
| N1 | 后台标签页 | 用户把 EasyEDA 所在窗口切到新标签页后，F1→R1 整轮一次完成、无补救步骤；每个检查点（start、F1、L1、G1、F2、E1、重载后、R1、end）的实时可见性全为 hidden。 |
| R1 | 收尾状态 | 该轮最后一步：工程文档列表只有 P1 与 PCB1，没有临时页；P1 保留该轮的功能框布局（已保存、重载）。 |
| E2E | 延期 | ESP32-S3 芯片级 N8R8 完整流程（#43）：原理图阶段完成，PCB 阶段未开始；记 in-progress，留待 follow-up。 |
