# v1.14.0 有限验收报告

用户于 2026-10-17 批准发布 v1.14.0，沿用有限范围。本报告使用 schema 3，通过范围为 cli-fixes；
完整设计 E2E（#43 PCB 阶段）延期至 follow-up。有限软件通过不代表电路或制造通过。
产品源码绑定 dev 提交 `f2369a5`。验收轮（F1→R1，含 N1）于本地 15:15:25–15:17:13（UTC+8）一次连续执行，
EasyEDA 标签页全程处于后台；M1 在同一候选上于其前完成。

## 运行经过

- 首轮（14:52–14:53）：F1/L1/G1/F2/E1/R1 除 zone-plan 六项计数无据外按判据通过，但标签页未处于后台（用户窗口只有一个标签页、窗口可见，
  health 快照全程 visible），不能作为 N1 证据；其 zone-plan 文本输出只有五项计数。
- N1 首次运行（15:08–15:09）：用户在 EasyEDA 窗口新开标签页后执行，实时可见性全为 hidden；落块、group-move、
  保存重载均成功，但 zone-draw 时「复位与启动」的标题文字创建返回 undefined（zone-draw 自带一次重试后仍失败），
  门禁 check 报该框 3 个 part-frame-unresolved（gate FAIL）；后台状态下重跑 zone-draw 补画 1 框后 gate PASS。
  这是事后补救，不计入 N1 通过。
- 独立复核首轮结论 fail（六项计数无据、L1 统计口径未说明、N1 非一轮完成且判据不全、R1 不是最终状态、
  两处无日志支撑的说法）。据此在后台状态下干净重跑一整轮（下表结果），补齐 JSON 六项计数、标题回读、
  每个检查点的 health 快照与实时可见性，R1 放在最后。

## 已完成的软件验证

- Go 全仓：15 个包全部 ok；顶层测试 2,862 项通过、0 失败、1 项跳过。
- 连接器测试 1,181 项通过、0 失败；typecheck 退出 0。
- 发布脚本测试 123 项通过；agent-check 通过；skill-check：189 个 tracked 文件通过。
- 现场 CLI/daemon 版本串 `v1.13.1-16-gf2369a5`；连接器由候选源码构建（1.14.0，`dist/index.js` SHA256
  `822bc081f4181d880c19fcfdac36c762df5d6f80938121bdb3fef44719a8a784`），经 IndexedDB 热重载装入，验收轮开头
  health 记录 connectorVersion 1.14.0；`extension/src` 自候选无改动。
- 五平台交叉构建与资产核对由 release-build 执行，不代表其他平台运行时验证。

## 用例结果

| ID | 结果 | 回读与证据 |
|---|---|---|
| M1 | pass | Go 2,862 通过 0 失败（15 包）、连接器 1,181 通过 0 失败、typecheck 退出 0、发布脚本 123 项、agent-check、skill-check 189 文件全部通过；验收轮开头 health 记录版本串 v1.13.1-16-gf2369a5 与 connectorVersion 1.14.0。 |
| F1 | pass | 清页 clear-exit=0 后 block-apply 退出 0：tile 1/5 core 16 网、decap 2 网、reset_boot 4 网、xtal 3 网、flash 8 网，逐框 reconcile ✓ 与实时网表一致；flash 框按阅读顺序未找到位置，退回就近空位（落在 240,220）后对账仍一致。 |
| L1 | pass | 落块后页面连接汇总 netports=0、netflags=32、wires=57、netlabels=0（汇总不计导线 Name 属性）；导线上可见的 Name 属性（原生网络标签）25 个；XTAL_P 只有 (405,720) 主控引脚与 (880,550) 晶振干线末端各 1，XTAL_N 只有 (405,710) 与 (1060,570) 各 1。 |
| G1 | pass | 4 次 group-move 全部 exit=0、内核对账绿（网表逐引脚一致，无新增 bridge）：复位框刚性重建 3 棵导线树、晶振框 2 棵（4 只引脚）、Flash 框 7 棵；全页网络标签移动前 25 个、移动后 25 个，坐标按各自位移平移。 |
| F2 | pass | zone-plan --json 六项 sheetOverflow/partitionOverlap/titleBlockHits/moduleOutsideZone/labelCollisions/sheetMarginHits 全 0；框 主控 (80,332)..(456,806)、滤波 (468,552)..(665,806)、复位 (677,552)..(846,806)、晶振 (858,472)..(1038,806)、Flash (476,228)..(784,534)：相邻间距 12/12/12，Flash 与上方滤波/复位框隔 18、与主控隔 20；zone-draw 退出 0、画 5 框；text-list 回读 5 个功能名标题各 1 个。 |
| E1 | pass | sch save 回包 "saved": true；doc reload 报 saved → closed → reopened；重载后 sch gate PASS：layout-lint 0 overlap、clusters 0 重叠、check 0 error、bridge-check 0 bridge/orphan（57 wire tree）、drc 0 fatal 0 error native PASS；重载后 zone-plan --json 六项仍全 0、5 个框坐标与重载前一致。 |
| N1 | pass | 用户在 EasyEDA 窗口新开标签页后，F1→R1 整轮一次完成、无补救步骤；start、F1、L1、G1、F2、E1、after-reload、R1、end 九个检查点的实时可见性（pages.list 回包）与 health 快照全部为 hidden；该轮各用例结果即上表 F1/L1/G1/F2/E1/R1。 |
| R1 | pass | 该轮最后一步 doc ls 只列出 schematic P1 与 pcb PCB1，无临时页；P1 为该轮功能框布局（已保存、重载）；导出整页 PNG 作为本地复核材料（未入库）。 |
| E2E | in-progress | ESP32-S3 芯片级 N8R8 完整流程（#43）：原理图阶段功能框排版现场完成、门禁通过；PCB 阶段未开始，留待 follow-up，不补签完整设计通过。 |

## 现场回读

- 验收轮逐步输出、每个检查点的实时/快照可见性、group-move 报告、zone-plan JSON（前后两份）、标题回读、门禁、
  check 明细、doc ls 与整页导出保存在本地忽略目录 `artifacts/release-v1.14.0/`（round2.sh 执行脚本、round2.log 与 r2/ 子目录）。
- 首轮与 N1 首次运行的输出在同目录 live.log、n1.log、n1-redraw.log 及对应分步日志，按「运行经过」记录。
- 门禁 check 的 4 条 WARN：U1 36 个未用引脚未打 NC；2 处分区标题文字擦到框边（text-frame-crossing）；
  SPI Flash 框外围以电源符号与标签连到 Flash（peripheral-not-direct）。均不阻塞，未改判。

## 已知限制与说明

- 分区框标题文字创建在 N1 首次运行中返回 undefined 一次（zone-draw 自带一次重试仍失败，重跑 zone-draw 补画成功）；
  本报告只观察到这一次，未定性其频率。
- ESP32-S3R8 芯片级块在块库中仍为 draft；本次只验排版与工具链行为。
- 网络标签是导线上的属性而不是元件，`sch check` / `clusters` 的重叠判据看不到它；网名压字需看导出图。

## 独立复核

独立复核者只读核对了 test-report 各用例与本地原始输出（round2.log、r2/ 下的分步日志与 JSON、m1-* 日志），并查看了
`git log v1.13.1..f2369a5` 和 `extension/src` 的差异。验收轮的落块对账、网络标签计数与坐标、四次整体移动、zone-plan
六项计数、标题回读、保存重载后门禁、九个检查点的后台可见性及收尾文档列表，均与日志一致。首轮复核提出的问题已经修正，
首轮运行和补救运行未计入通过。本复核只覆盖有限范围 cli-fixes，不代表完整设计 E2E、PCB 或制造通过。

复核经过：首轮复核结论 fail（6 项必须修改）；修订并干净重跑后第二次复核结论 pass，无必须修改项；采纳其措辞建议
（首轮表述收窄、G1 删去未由日志支撑的「直连」推断）。
