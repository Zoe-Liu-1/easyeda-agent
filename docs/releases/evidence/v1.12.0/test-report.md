# v1.12.0 有限 CLI/工作流验收报告

用户于 2026-10-14 批准发布 v1.12，沿用有限范围。本报告使用 schema 3，通过范围为 cli-fixes；
完整设计 E2E 延期至 follow-up。有限软件通过不代表电路或制造通过。
产品源码绑定 dev 提交 `fd334d9`；现场执行时间 2026-10-14T09:18:27Z–09:20:54Z（UTC，E1/F2/L1/N1/R1 一次连续执行），
F1 与 M1 在同一候选上于其前完成。

## 已完成的软件验证

- 候选 Go 全仓测试：5,543 项通过、0 失败、1 项跳过，15 个包。
- 连接器测试：1,165 项通过、0 失败；typecheck 通过。新增未知器件拒绝、确认框滞后关闭判定、后台安全等待等正反例。
- 发布脚本测试：122 项通过；agent-check 通过；skill-check：205 个 tracked 文件及安装后链接通过。
- 现场 CLI/daemon 版本串 `v1.11.0-21-gfd334d9-dirty`（dirty 仅为未提交的连接器版本号）；dev 构建的
  health versionGate 为"未判定"（不做硬判定），候选绑定依据是版本串与本地包哈希。
  连接器开发包 dist/index.js SHA256 `97766eae62bc2ce8d67f36b0d6fda1c07c435919c69047e709f5c77c6d7e4fce`，
  经 IndexedDB 热重载装入（现场 1.11.10），`extension/src` 与候选提交一致（`git diff HEAD -- extension/src internal cmd` 为空）。
- 五平台交叉构建与资产核对由 release-build 执行，不代表其他平台运行时验证。

## 现场回读

EDA Web 4.1.71，窗口全程最小化：开始、N1 之后与结束三次读取 `document.visibilityState` 均为 hidden，
用例结束后同一页面上一个 250ms 的裸 setTimeout 实测耗时 66s（throttle-probe），说明深度节流已生效。

F1：以 v1.12.0 版本号构建的 CLI（f1-binary-version 为 `easyeda-agent v1.12.0`）单发调用拉起 1 个后台 daemon（端口 60900），停止后 0 残留；3 路并发只出现
1 条启动提示、1 个 daemon，停止后进程与启动锁均为 0；开发 daemon pid 前后不变；源码构建不自动启动并提示
`daemon start --background`。`health` 清单五项 ✓，后台标签页显示为备注"操作照常；仅 PCB 画布截图需切到前台"。

F2：`pcb check` 19 次审计调用、0 失败（v1.11.0 为 87 次），结论 ERROR=0、WARN=7、INFO=1，与 v1.11.0 记录构成一致；
`sch gate` 21 次调用、0 失败（v1.11.0 为 67 次），layout-lint/clusters/check/bridge-check/DRC 五项 PASS。

E1：已同步时 import-changes 退出 0，提示"原理图与 PCB 已同步，无需导入"；pcb save 回包 saved:true；
doc reload PCB1 回执 passed（closed/saved 为 true）；原生 DRC passed:true；dump 语义哈希 226113afd926c4ee…，10 个器件。

N1：临时页放置 R99 退出 0（3s）；用 16 位符号 id 冒充器件 uuid 的放置 1s 内 PRECONDITION_REFUSED，未尝试创建；
导入新增 confirm=applied、10→11、verified:true（9s），删页后导入删除 confirm=applied、11→10、verified:true（5s），
两份回包 pageVisibility 均为 hidden；doc reload P1 与 PCB1 回执均 passed（16s/8s）；`pcb snapshot` 以
PRECONDITION_REFUSED 拒绝且未采集；`sch export-image` 退出 0 导出 PNG（3s）。

L1：临时页 block-apply 退出 0：R4:2—LED2:+ 引脚直连，R4:1→L1CTRL 与 LED2:-→GND 两条 connect_pin 落定，
reconcile 3 个网络与实时网表一致，clusters 0 重叠/0 出图；临时页删除后回读页列表已无该页。silk-align C1 bottom
退出 1，提示 "nothing was written; unresolved: C1 (…closer to its own part…)"，回包 appliedIds 为空。
auto-place --dry-run 退出 0，applied=0，7 个外围计划位置（按每边 60mil 保守包络）与 2 个禁布区无交、全在板框内。
标题栏 Name/Description 显示时 `sch check` 报 2 条 out-of-sheet，隐藏后 0 条，随后保存。两项在用例前即为隐藏（同一轮 F2 的严格门禁报 0 out-of-sheet，而显示后为 2），用例结束时 titleblock 回读两项 showTitle/showValue 均为 false、值不变，即恢复到用例前状态。

R1：页面列表仅 P1；PCB dump 10 个器件，语义哈希 226113afd926c4ee…，与开始时基准及 v1.11.0 记录一致。

| ID | 状态 | 证据与限制 |
|---|---|---|
| M1 | pass | 候选 fd334d9：Go 5543/0、连接器 1165/0、typecheck、发布脚本 122、agent/skill-check 通过；版本串与候选一致，源码自候选起未改。 |
| F1 | pass | f1-autostart：单发 1 个 daemon、并发 3 路仍 1 个、停止后 0 进程 0 锁、开发 daemon pid 不变、源码构建不自启；f1-health-checklist 五项 ✓。 |
| F2 | pass | f2-pcb-check.calls 19 次/0 失败（原 87），ERROR=0；f2-sch-gate.calls 21 次/0 失败（原 67），严格门禁五项 PASS。 |
| E1 | pass | e1-import.exit=0 且提示已同步；e1-save saved:true；e1-reload passed；e1-drc passed:true；e1-dump 语义哈希与基准一致。 |
| L1 | pass | l1-block-apply 连接全部落定、reconcile 3 网一致、删页回读；l1-silk-align 未写入且列出原因；auto-place 预演 0 违例；可见文字越界 2→0。 |
| N1 | pass | 后台 hidden 下 n1-place 成功、未知器件即拒、n1-import-add/remove 均 verified、n1-reload-P1/PCB1 passed、截图拒绝、原理图导出成功。 |
| R1 | pass | r1-pages 仅 P1；r1-pcb-after 10 个器件，语义哈希 226113afd926c4ee… 与 r1-pcb-before-cand 一致。 |
| E2E | not-run | 本版未在冻结候选上执行需求到两层 PCB 成品的完整流程；延期 follow-up，不补签。 |

## 失败与未完成项

- 完整 E2E 与 B00–B10 未在冻结候选上执行。
- PCB 没有可在后台使用的官方图片导出：`pcb_ManufactureData.getPdfFile` 在后台 30s 未返回（连接器未被阻塞），
  PCB 画布截图仍需窗口在前台；替代为回读渲染 `pcb layout render --actual`（不含走线与铺铜）。
- silk-align 判定未写入时，daemon 仍按"写动作已派发"给后续读取加 staleRisk 提示（保守但多余）。
- 后台 doc reload 只在本工程（单页原理图、10 器件 PCB）上验证，未覆盖多页与大板。
- Skill 包 205 个文件，仍超过 SkillHub 200 文件上限，该平台同步预计失败；连接器市场需人工提交。

原始记录保留在本地忽略目录 artifacts/release-v1.12.0/；公开材料不包含用户路径、工程 UUID 或私有设计数据。

## 独立复核

独立 Agent 于 2026-10-14 只读复核，未访问现场 EDA，也未运行 easyeda 命令，结论 pass（首轮为有条件通过，所列缺口已补）。复算：HEAD 为 fd334d9，`extension/src internal cmd` 相对候选无改动，dist/index.js 哈希与记录一致，版本串含 gfd334d9；F2 调用数 19/21、0 失败，pcb check ERROR=0/WARN=7/INFO=1，严格门禁五项 PASS；E1 no-dialog、10→10、verified，saved:true，reload passed，DRC passed；N1 导入新增 10→11 与删除 11→10 均 applied/verified 且回包为 hidden，未知器件与截图均 PRECONDITION_REFUSED，P1/PCB1 reload passed；L1 silk-align appliedIds 为空、auto-place 7 个计划 0 违例、out-of-sheet 2→0；r1-pcb-before-cand、e1-dump、r1-pcb-after 语义哈希均为 226113afd926c4ee…、10 个器件，页面仅 P1，三次可见性读取均为 hidden。抽查 deadlines.sleep、导入确认的器件清单兜底、未知器件拒绝与后台拒绝的移除均在源码中。聚焦测试：连接器 19 项、Go 234 项通过；release-check 的用例表规则通过。复核指出的缺口已处理：补充节流探测与 F1 二进制版本的原始记录，披露并回读标题栏属性最终状态，候选前的旧拒绝记录移至本地 pre-candidate 目录。health 清单中 versions 项打勾而内容为"未判定"，属 dev 构建的显示方式，已在软件验证一节说明。

## 发布准备

具体版本与有限范围已获用户批准。正式发布仍由现有 release-check/release-build/publish-release
路径生成并核对版本、资产及远端 SHA256；正式非草稿 Release 完成前不宣称已发布。
