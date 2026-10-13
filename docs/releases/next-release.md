# v1.11.0 发布记录

用户于 2026-10-14 选择按有限范围发布 v1.11.0。连接器运行时自 v1.10.0 起有改动（页面可见性、
删除级联与显式 id、bridge-check 孤桩判据、导入空操作核验），按版本规则递增 minor；
CLI/daemon、连接器和 Skill 统一为 1.11.0。

## 版本范围

- 原理图：外围实线直连门禁、页面边界统一检查、块 attach 贴脚成排、`compose` 状态与工程名统一。
- PCB：导入默认不回填库属性（消除 DRC Netlist Error）、陈旧分区认领整表判废、天线禁铜整板宽、
  `silk-off-board`、dump 语义哈希浮点归一、已同步导入判为核验通过的空操作。
- 安全：删除必须显式 id、`page-delete` 回读判定、文档类型不符给出 `--doc` 提示。

## 发布范围与状态

候选产品源码为 dev 提交 `699d140`。[有限验收报告](evidence/v1.11.0/test-report.md) 通过 schema 3
记录绑定本版本的用户范围决定；M1/F1/F2/E1/L1/N1/R1 在冻结候选上重新现场执行并经独立复核。
ESP32-S3 最小点灯板已在开发过程中从原始需求跑到两层 PCB 布线完成、原生 DRC passed，但期间多次
修改源码与热重载连接器，完整设计 E2E 记 in-progress，留待 follow-up。

升级 CLI/Skill 使用 `easyeda update`；本版新增连接器动作字段，需重新导入 1.11.0 连接器 .eext。

v1.11.0 于 2026-10-13 18:07:43 UTC 正式发布，非草稿、非预发布，附注标签绑定提交 `1f6c920`，main 已快进到该提交。
十项资产加 checksums.txt 共十一份文件，远端核对后才转正式。ClawHub 同步成功（easyeda-agent@1.11.0）。
SkillHub [自动发布](https://github.com/zhoushoujianwork/easyeda-agent/actions/runs/38444234203) 仍被平台
"最多 200 个文件"限制拒绝（本包 205 个），未为通过上传删减设计知识。立创连接器市场需人工提交，未在本轮更新。

## v1.10.0 历史记录


当前公开正式版为 [v1.10.0](https://github.com/zhoushoujianwork/easyeda-agent/releases/tag/v1.10.0)。
用户已批准 v1.10.0，指示复用刚完成的验证并直接发布；本轮按明确的有限 CLI/工作流范围交付。
新动作与字段要求重新导入连接器，按版本规则递增 minor，CLI/daemon、连接器和 Skill 统一为 1.10.0。

## 版本范围

- 实际 PCB 正反面 SVG 与离线图册，保留官方图片及明确的缺测、镜像、未布线和问题状态。
- 保存重载阶段回执、文档就绪读取、PCB 采集身份/哈希/丝印读取状态与截图判定。
- dev 已提交的原理图保留、布局与公共库解析、库身份/开窗核对及实验性同源回写。
- 排除工作区其他未提交修改、私有设计数据与未完成的封装修补。

## 发布范围与状态

候选产品源码为 `46dc97bf`。其 v1.10.0-dev.1 的 Go、连接器、Skill、发布脚本、五平台交叉构建
和原生离线资产 smoke 已通过；现场三次保存重载、实测丝印与采集哈希通过。
本次不重复这些测试，不跑完整基础批次或 ESP32-S3 需求到成品回归。
[有限验收报告](evidence/v1.10.0/test-report.md) 通过 schema 3 记录绑定本版本的用户范围决定，
完整设计 E2E 留待 follow-up；已有工程创建无 UUID、官方截图空白、测试夹具 Netlist Error
及导航超时等失败继续保留，不把工具发布当作电路或制造验收。

v1.10.0 于 2026-10-11 14:02:54 UTC 正式发布，非草稿、非预发布。
附注标签绑定提交 `66b44aab4d833cd0bb6afb6d3b1daa3ad7408817`；正式版本构建与 release-check 通过，
五平台 CLI、连接器、Skill、安装脚本及验收说明共十项资产，加 checksums.txt 共十一份文件，
远端名称、大小及 SHA256 全部匹配。GitHub 包保留 205 个 Skill 文件。
本次只执行正式打包与资产核对，没有重跑最近的工程测试、现场回归或完整 E2E；
完整验收与失败状态保持冻结报告中的原结论。

SkillHub [自动发布](https://github.com/zhoushoujianwork/easyeda-agent/actions/runs/38145602735)
被平台“最多 200 个文件”限制拒绝，未同步成功；没有为通过上传而删除设计知识或改写正式包。
ClawHub 因本机没有 clawhub 命令而未同步。立创连接器市场仍需要人工提交，未在本轮更新。
上述平台状态与已核验的 GitHub Release 分别记录。

升级 CLI/Skill 使用 `easyeda update`；新增 EDA 动作需重新导入本版连接器 .eext。
历史有限范围不成为下一版本的默认豁免，完整设计后续仍按仓库验收流程执行。

## v1.9.1 历史记录

# v1.9.1 发布评估

当前正式版为 [v1.9.1](https://github.com/zhoushoujianwork/easyeda-agent/releases/tag/v1.9.1)，已获用户批准并正式发布。
本版只纳入 main 已集成的 DSH 分发修复与对应说明，附注标签绑定提交 `b2522c8701367da31e3af4519be68a04ff58af7b`。
发布规则见 [发布流程](../release-workflow.md)，既有设计验收范围见
[v1.9.0 发布记录](release-1.9.md)，日常开发状态见 [CLI Status](../cli-STATUS.md)。

## 修复与用户收益

- #274：根包提供声明的 `index.js` 入口和 MCP SDK，Git 安装不再缺入口或嵌套依赖。
- 分发白名单限制为公开入口、bundle patch、MCP 源码与 Skill，排除本地运行资料及无关代码。
- npm tarball 在仓库外安装、SDK/MCP 握手、12 个工具与目录发现回归覆盖三平台 CI；
  `--prefer-offline` 允许缺失元数据时联网，不声明严格断网安装。
- DSH 安装文档补齐独立 CLI、Windows `EASYEDA_BIN`、配置与运行检查的区别。

入口修复为 [4391cfd](https://github.com/zhoushoujianwork/easyeda-agent/commit/4391cfd2a5188689d26da5eba80fe54177c1e998)，
干净缓存回归修复为 [b535c26](https://github.com/zhoushoujianwork/easyeda-agent/commit/b535c26c4047fcdce982c4565f6c9ae6a59c5084)。
本版不改变连接器运行逻辑或动作契约，按 patch 递增，全部分发组件仍统一为 1.9.1。

## 验证状态与边界

- 发布提交 `b2522c8` 的 [CI](https://github.com/zhoushoujianwork/easyeda-agent/actions/runs/38118649015)
  全部通过；Windows/macOS/Linux 的 npm 分发测试均为 8/8，CI 没有运行 DSH 安装器。
- macOS 独立 DSH_HOME、全新 web profile 的真实 Git 安装、Hub v1.4.8 装后入口检查、
  官方 DSH Web 启动、12 个 MCP 工具、真实 CLI 目录调用和 Skill provider 正文读取通过。
  CLI 0.1.5-rc.1 的 MCP/Skill 依赖解析为 rc.3，loader 为 1.0.3；
  [完整反馈](https://github.com/zhoushoujianwork/easyeda-agent/issues/274#issuecomment-6104813413)保留精确范围。
- v1.9.1 候选的版本一致性、打包/MCP 回归、发布脚本与自更新定向测试、发布构建通过；
  9 项资产 SHA256、15 项本机 CLI 冒烟及 201 个 Skill 文件与安装后链接核对通过。
- 发布前，macOS 隔离目录中的真实安装脚本读取候选资产字节，首次安装和从 v1.9.0 升级通过，
  下载端点使用本地替身；候选本地 Git ref 的 DSH 安装、Web/MCP/Skill 加载也通过。
- 发布后，GitHub 非草稿、非预发布的 v1.9.1 已成为 latest；9 项资产及 `checksums.txt`
  全部实际下载，并核对名称、大小、SHA256 与本地发布资产一致。
- macOS 独立目录中，从真实 GitHub latest 首次安装和从 v1.9.0 执行 `easyeda update`
  联网升级通过；CLI、Skill 版本与 201 个 Skill 文件内容全部对账，没有替换下载端点。
  测试限定安装目录与 Skill 目标，没有启动 daemon 或操作 EDA。
- 独立 DSH_HOME 中的正式 `#v1.9.1` Git 安装解析到上述发布提交；真实 Web 返回 HTTP 200，
  12 个 MCP 工具、CLI 离线目录调用及 1.9.1 Skill 正文读取通过。
  Hub v1.4.8 原版函数按已安装包名检查入口通过；按带标签 Git URL 解析依赖时跳过检查，
  此跳过不计为通过，完整 Hub 网页流程仍未验证。
- 原 Windows 报障环境、Hub 网页完整安装流程仍待复测，#274 保持 open。
  发布修复包不自动关闭用户故障。
- 完整设计 E2E 保持原状态。本次不运行全集，不把离线或 DSH 加载成功补签为 EDA 设计通过。

SkillHub 的 [自动发布](https://github.com/zhoushoujianwork/easyeda-agent/actions/runs/38118948996)
成功，平台回执 `skillId=173258` 未返回审核状态，不声明市场前台已可安装。
ClawHub 本机未登录，本次未同步；已有发布授权下可在登录后补发，不影响已核验的 GitHub Release。

## 其他近期 Issue

#273 的显式标签/展开布局已在 v1.8.1 提供离线替代路径，默认搜索耗尽仍未修复；
#267 在 dev 已提交的 CLI 保留器件重建有有限防护，但广义连接器清理和现场验收仍未完成；
#268 的后台保存改动仍在未提交开发工作中。它们不计为 v1.9.1 已解决，也不夹带入此分发补丁。

v1.8.0 的历史发布评估与有限验收范围保留在 [对应发布记录](release-1.8.md)，
不作为新版本的默认豁免。
