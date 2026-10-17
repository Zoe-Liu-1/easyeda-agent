# v1.14.0 有限验收基准

## 用户范围决定

2026-10-17，用户在原理图功能框排版收尾后要求"完成之后可以发一个版本，然后再继续 PCB 阶段"。先选 v1.13.2，
随后被告知连接器运行时有改动、版本规则要求 minor，改选 v1.14.0，并采用选项中说明的有限范围：
只验本次改动，按 `cli-fixes` 发布，`full-design-e2e`（#43 PCB 阶段）延期至 `follow-up`。完整设计 E2E 不补签。
该决定只绑定本版本。

## 候选与输入

产品源码候选为 dev 提交 `f2369a5`（v1.13.1 之后 16 个提交，最后一个为版本准备）。连接器运行时自 v1.13.1 起有改动：
`schematic.power.connect_pin` 新增 `via`（竖脚标签拐弯）、`schematic.group.move` 平移导线后重挂网络标签、
原理图同步等待按请求剩余预算（后台标签页），按版本规则递增 minor；CLI/daemon、连接器、npm/lock 与 Skill 统一为 1.14.0。

现场 CLI/daemon 由该候选构建（`v1.13.1-16-gf2369a5`，提交后已触发重编，版本串绑定候选）；连接器由同一候选源码
构建（1.14.0，`dist/index.js` SHA256 `822bc081f4181d880c19fcfdac36c762df5d6f80938121bdb3fef44719a8a784`），
经 IndexedDB 热重载装入，验收轮开头 health 记录 connectorVersion 1.14.0（windowId `3a3ba102…`）；`extension/src` 自候选无改动。

现场使用获准的 ceshi 专用测试工程。开始状态：原理图 P1 为前序开发留下的功能框布局（验收轮先清页），
PCB1 为 #43 先前导入的器件，本次不写 PCB。工程身份、原始日志与本地路径不公开。

## 环境与阈值

macOS arm64，Chrome 中的 Web EDA 4.1.78。有限用例 M1/F1/F2/E1/L1/N1/R1 及 G1 的含义以本目录 test-cases.md 为准；
它们不是完整设计用例的补签。ESP32-S3R8 芯片级块在块库中仍为 draft 状态（引脚名按块内说明于 2026-07-15
现场核实，但块未走完整验证门禁），本次只验排版与工具链行为，不代表块已验证。

阈值：每个框落块后与实时网表逐网一致；移动后网表逐引脚一致且无新增短路；`sch zone-plan --json` 的六项
计数（sheetOverflow、partitionOverlap、titleBlockHits、moduleOutsideZone、labelCollisions、sheetMarginHits）全 0；
save → doc reload 后 `sch gate` 五项全部 PASS；N1 期间每个检查点的实时可见性全为 hidden。

页面可见性的口径：`health` 的上下文是连接器最近一次上报的快照；实时值取自单独调用 `schematic.pages.list`
的回包上下文（连接器在每次回包时读取 `document.visibilityState`）。验收轮每个检查点两者都记录，N1 以实时值为判据。

完整 B00–B10、完整设计（#43 PCB）与制造验收不在本次通过范围。
