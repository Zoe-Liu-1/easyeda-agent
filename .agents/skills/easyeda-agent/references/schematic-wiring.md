# EasyEDA Schematic — 连线(pin-aware autoconnect)

> 从 [`schematic.md`](schematic.md) 拆出(RFC #178)。入口、器件放置、Actions 目录、电气铁律仍在
`schematic.md` —— **先读它**,再按需读本文件。

---

## Pin-aware autoconnect — let the planner pick direction/offset

写线/接标记的 daemon 执行路径会校验引脚方向、零长/斜线和穿本体等非法输入，并做写后回读。
以下候选评分负责寻找合法形态，不能替代这些正确性检查。已有连通不证明布局合法；详见
schematic.md 的 typed 写入校验。

宿主可能在 create 返回后才把导线发布给 getAll。只有首轮完整回读的几何合法、但拟线段尚未
覆盖时，daemon 才在同一窗口串行锁内最多再读 4 次（间隔 250ms，总追加预算 2s，且服从
调用方期限）。每轮重新核对文档、递增 FIFO、无 abandoned 变化、库存和几何；线段完整覆盖
后还必须通过拓扑比较。缺测、新非法几何或到期响应立即失败。它不重发 mutation，
不凭 create 返回的 PID/line 放行。到期仍不可见就返回 partial/非零，保留
readbackAttempts/observations 及可用的原始审计；
调用方保存当前已知状态、fresh 回读后从参数源重新生成受保护计划，不能盲重放失败队列。

`sch wire` 的成功也不能保证删除后用相同参数再次创建成功。Web 4.1.60 / 本地
`1.9.1-dev.4` 的单动作测试中，首次引脚导线创建和精确删除都通过，随后相同参数的
创建返回宿主 `create failed!`、CLI 非零退出；完整回读、保存重载后连接库存为空。
根因仍未知，不据此增加写入重试或归因缓存。冻结输入和错误，停止依赖该导线的步骤；
新空页的首次创建成功只证明那个独立用例，不修复或补签原页的失败。

`connect_pin` (`sch connect`) keeps the connection **safe** (pin → short wire →
flag/netport, never a netflag on a bare pin), but it still makes YOU pick
`--direction` and `--offset`, so layout quality depends on judgment. **`sch
autoconnect` removes that judgment**: it pulls the real geometry (part bboxes,
pin coords, existing flag/port/label bboxes, title-block keep-out), scores every
`up/down/left/right × offset` candidate with a deterministic cost function
(flag-collision / through-part penalties, shortest-offset + outward-side +
kind-default bonuses), picks the lowest-cost one, and delegates the mutation to
`connect_pin`. Same schematic state + spec → same selection (deterministic).

`sch connect --kind net_label` 使用原生 `createNetLabel(x, y, net)`；方向和偏移只决定
桩线端点，该接口不接收旋转参数，因此不会创建 `__ROTPROBE__` 旋转校准旗。
电源、地和 netport 仍按原流程校准旋转。跳过无用探针不代表宿主已支持原生标签；
接口标注 EDA v4 起提供。Web 4.1.60 可能已创建标签却返回空值；连接器比较调用前后
新增的 Name 属性，并核对坐标、可见性、父导线及真实网名。仅唯一匹配才返回 `verified:true`。
读回不完整或匹配不唯一时返回 `partial:true, verified:false`，保留对象 ID，CLI 非零退出；
先保存并回读，不能盲重试或仅因返回空而删除桩线。
兼容性与验证说明见 [schematic.md](schematic.md#原生-net_label-超时191)。

**网络标签必须经 `sch connect --kind net_label` 带短桩创建。** 裸 `sch netflag --kind net_label`
（无线）是部分写入：只留下一条带网名的导线、没有可见的 Name 文字，且返回
`verified:false`；不要用它。网络标签只在本页内按名字连通，跨页信号用 `net_port`。

**直连优先（现场执行口径）。** 同一功能块、同一网络，存在正交、不穿器件/文字、不与其他
网络交叉的短路径时，先用真实导线；只有跨块/跨页/远距离、引脚首段方向不允许，或直连必然
交叉/穿器件/压字时，才用同名标签。不得为省标签绕斜线或制造交叉。标签按**线岛**计数：
同一线岛同一网络只标一次；标签水平、可见，不压导线/引脚/器件/文字；电源、地、器件名称
不计入标签数量。**不同主动芯片之间的信号网不用长导线直连**；两块芯片各自在引脚端放置
同名网络标签，只有同一芯片功能块内部的短外围才保持 wire 直连。非电源/地信号网使用
netflag 或带箭头的 netport 均为 WARN；确属模块边界
的 netport 需用 `sch check --signal-port-exempt <primitiveId,...>` 显式登记，登记项只保留
INFO 审计，不再静默放过。跨芯片线树由 `cross-chip-direct-wire` 报 WARN，
`direct-connect-plan` 和 `alignment-plan` 都跳过它，必须先把长线拆成标签连接，不能通过
移动芯片继续优化这根线。

**导线只能停在引脚外向端点，不能覆盖 pin lead。** 从 pin 坐标出发的合法桩线只允许在
该端点与 lead 接触后向外延伸；沿 lead 进入符号、横穿 lead、或从符号内部穿过该引脚均为
`wire-over-pin-lead` ERROR。正常的外向短桩不会触发，规则按实测 pin rotation 与 body bbox
计算 lead 区间。

**局部电源/地不要用 `sch wire --net` 重复显示网名。** 导线本身不命名，末端 netflag 定义
网名；否则 `--net` 会把网名画在导线上，与 netflag 名称重复并增加压字。信号网的
`net_label` 仍按上面的 `sch connect --kind net_label` 创建。

**电源/地/端口符号的 Name 文字按命名标签检查。** 名称必须可见、水平、不压导线/引脚/
器件/其他文字，同一线岛同名只保留一个。netflag/netport 的 Name 不享受“自身桩线豁免”，
即使压在自己的短桩上也报错；符号 body 的上/下朝向仍由 `power-ground-orientation`
独立检查。

**三脚选择器例外。** 三脚跳线/选择器的中间脚夹在两个相邻脚之间，直连通常必须跨过输出
母线或相邻脚；允许中间脚保留 1 个同名标签，外侧两脚仍直连。不要把这个例外扩大为
“所有密集脚都用标签”。

**先消除非必要偏轴折返，再比较线长。** 先把端点重合或端点落到另一线段上的多个
wire primitive 组成同一物理骨架，再对同网主干检查
`H-V-H` 和 `V-H-V`：若两个折点没有引脚/网络标识，且一侧是固定核心、另一侧是独立外围簇，
优先把外围簇整体平移到同一直线。水平主路径通常通过 Y 向平移对齐，垂直主路径通过 X 向
平移对齐。候选先从种子外围沿非 GND、非电源轨、非主干的局部网络扩展到完整功能组，
再列出 `moveDesignators`、`moveWireIds`、`moveMarkerIds`、`moveLabelIds`、
`sharedWireIds` 和最终落点包络中的 `blockers`。成员的上限为 16；扩展遇到另一颗多脚主动
器件（标准角色 `U`/`Q`/`IC` 且至少 4 脚）时停止，不靠具体实例位号、器件型号或网络名
决定核心。不能只为了缩短长度移动，也不能拆开反馈、
电源/GND 或连接器功能组。与移动组不共享局部网的阻塞件列入
`independentBlockers`。主组和独立阻塞组都复用整组移动的器件、引脚、文字和导线碰撞检查；
任何一层碰撞都会阻止 ready。planner 为阻塞件扩展局部功能组，在 5 单位栅格上由近到远
搜索二维安全落点，并给出 `independentBlockerPlans[]` 位移；单轴不超过 50、L1 不超过
60 的局部位置才可为 `ready`，超出预算为 `review-required`。该前置组自身若仍触及固定
引脚，列入其 `sharedWireIds` 并保持 `blocked`，不能把“只移器件”标成 ready。
共享边界线必须删除后按移动后的真实端点重新布线。`boundaryPlans[]` 对每个边界网给出
`fixedPins`、`movedPins`、正交 `wires`、`deleteWireIds`、`labelIds` 和阻塞原因；主网从目标
轴线生成总线/分支，次网枚举正交 L 路由。草案要避开移动后的器件本体、异网引脚和导线、
全部可见文字、图签区、图纸边界及其他网络交叉。每段路径的第一段必须沿对应引脚的
外向 rotation 出线；pin rotation 缺失时边界计划为 blocked。`sch alignment-plan --doc
<page> [--net <net>] [--json|--out]` 输出只读步骤和资源。真正执行使用 `sch
alignment-apply ... --apply --save`：安全内核只对计划列出的共享树解除 fail-closed，
移动后重建边界线并逐脚比对全页网表；失败进入恢复段，不能把部分写入报成成功。

**多段线树按自由端逐个复核。** 宿主会把同一网络的多段导线合并成一个 segment-array
`Line`，不能因为树上任意一端已接引脚或标识，就把整根线判为已用尽。先按线段端点求度数，
对每个 degree-1 自由端分别检查是否命中引脚、netflag/netport/netlabel/short_symbol、
其他导线端点或另一线段内部。树端数大于 2 且存在无落点自由端、网名为空或自动生成时，
`sch check` 报 `dangling-wire`；finding 的 `at` 必须指向真正悬空的端点，`count` 为该线树
上的无落点自由端数量。只保留明确短支路，删除残留分支或把它接到明确目标。

**重画/复制已有布局时，不要回放宿主合并后的 `Line` 数组。** EasyEDA 会把多段线合并并
重排顶点，直接回放可能产生斜线/回折；应从干净的正交模板生成，逐段 `sch wire`，完成后跑
`sch bridge-check` + `sch read` 逐脚对账。

**直连机会规划先只读。** `sch direct-connect-plan` 会列出同网不同线岛的近距离标签对。
先检查两条短 L；都受阻时用 5 单位障碍感知 A* 搜索多折正交路径，A* 仍无干净路径时，
才按风险从小到大给出小位移候选：

1. `move-proposed`：只移动阻挡的 part Designator/Value 文字；
2. `marker-move-proposed`：只处理 `netflag`/`netport` + **单段直线短桩**。标识本体与
   它的 Name 文字刚性平移，rotation 不变（电源/地 body 朝向因此不变），短桩从**不动的
   宿主 pin** 重新走成一条正交 L；第一步必须沿宿主 pin 的原朝向（原桩方向）出线，
   可以先沿引脚走一段再折，不能一出门就横拐。候选必须同时满足：不压器件/标识/引脚/其他文字、
   不穿图签/越出图纸、新桩线不碰任何其他导线（相触即并网），并且确实清空至少一条
   直连 L 路径；
3. `part-move-proposed`：把阻挡的器件连同**真正终止于它引脚上的线树、标识和网络标签**
   作刚体平移。复用 `sch group-move` 的附件展开判据：共享线树（线树还终止在非成员
   引脚上）、疑似半移动残留、引脚与引脚直接相触一律拒绝；整棵被移动的线树同样要过
   器件/文字/引脚/导线/图签/图纸边界检查。附件组超过 16 根导线或整体跨度超过 400
   画布单位时视为“非局部调整”，跳过而不是拖着一整条母线做 5 单位的微调。

**导线阻挡会映射回它的所有器件。** 即使器件本体不在路径上，只要它的引脚接触阻挡
导线，就会进入器件迁移候选；`partSkips` 会逐器件给出 `shared-wire-tree` /
`no-clear-part-move` 等原因。共享线树意味着单器件一动就会撕裂与其它器件的连接，
正确解法是移动整个功能块或重走该网络，不能靠单器件微调。

`already-clear` 的对可由 `--playbook` 生成受保护队列，先创建真实导线，再删除两个
冗余标签。单对执行也可用 `sch direct-connect-apply --pair A,B --apply --save`；它在
建线前后和删标签前后比较完整 pin→net 表，失败时删除新线并恢复标签。移动类候选不在
自动 playbook 范围内，必须先用 dry-run/预览确认。

**共享线树按功能块处理。** 2–4 个器件通过共享线树连成的小块会整体刚体平移；如果标签
本身所在的线岛也在被移动的块里，规划器会重新计算标签在线岛上的连接点再验路径。超过
4 个器件的块（例如 10 器件的 LDO 通道）会明确报 `functional-block-too-large`，留给
整通道重排或重走网络，不做单标签级的整页拖动。

**电源/地局部簇（`sch check` 已实现非 GND 电源簇候选；GND 仍为建议）。** 一个局部簇由**消费电源脚**
（IC 的 VDD/VCC/VIN/VBUS/BIAS/IN/OUT 等）和就近的本地去耦/上拉被动件组成。同一局部
簇内应尽量：

1. 只保留一个同名电源/地符号，优先放在消费脚或其局部公共节点；
2. 把本地被动件用短的正交线物理接到消费脚；不能继续靠同名符号“隔空连”；
3. 允许不同功能块各自保留同名符号，但不得拉跨功能区的长电源母线；
4. 电源/地符号仍须 body 上/下、名称可见、不压字/不压线/不压图签。

判定顺序必须是：先按功能区和引脚网归簇，再看线树 root，最后才用距离。同一 root 上多个
同名符号是重复候选；消费脚和本地被动件分属不同 root 且相距很近，是“应物理互联”的
候选；跨功能区或 10 器件以上通道不做自动合并。`sch check` 已实现
`power-cluster-candidate`（非 GND 按 250 单位 WARN；GND 按 150 单位 INFO，
仍不自动落图；`power-cluster-plan` 默认不规划 GND，需显式 `--include-gnd`），
`sch power-cluster-plan` 提供只读接线计划：`ready` 输出每个被动件到消费脚的短线、
保留/删除符号和“先接后验证再删”的顺序；`partial` 只接已经 clear 的被动件并删除其
对应符号，被挡的 passive 保留符号；`blocked` 不生成删除步骤，只列阻挡对象。路径仍须
由 Apply 阶段回读验证，planner 本身不落图。
加 `--playbook <file>` 会生成受保护 `sch apply` 队列：先验证目标引脚，再
`schematic.wire.create`，随后跑 `sch check` + `sch bridge-check`，通过后才
`schematic.primitives.delete` 被动侧符号，再复验并保存。`requireFullExecution:true`
禁止跳过前置门禁或断点续跑。
有意保留的独立去耦排/电源域用 `sch check --power-cluster-exempt '<NET>|<DESIGNATOR>'`
登记为 INFO 例外；同一消费脚的本地被动件仍应先物理接入，不能借豁免逃避局部直连。

两层移动候选都枚举 `±--max-move` 内的**完整 5 网格**（含 `(+40,-15)` 这类非对称
位移），不是只试 8 个等幅方向。实际排布里“先横后竖”与“先竖后横”的避让位置往往
只差一个非对称偏移；候选栅格过稀会把本来一条 L 就能解决的情况误判成无解。

共享/分支线树、多段折线桩、宿主端不在 pin 上、以及 `netlabel`/`short_symbol` 一律跳过，
并在 JSON 的 `markerSkips` / `partSkips` 里给出原因。命令只输出候选，不调用 modify、
wire、connect 或 save；真正落图仍需人工确认后另行执行。

**路径端点取线岛上的真实连接点，不取标签文字中心。** 标签文字 bbox 的中心通常落在导线
旁边甚至空白处；规划器按“该标签所属线岛上离文字最近的点”起算 L 路径，否则会算出起点
悬空的假机会。候选评判还要求标识本体/文字与其它文字、器件、导线留出至少 2 个画布单位
的可见间隙（导线与导线相触仍按并网硬拒）。候选 L 本身也必须留在图纸可用区内并避开
图签 keep-out；不能只检查被移动的对象而放过新导线穿图签。

**批次内互斥 (issue #138):** 同一批(--spec / 多 --pin)里**已规划的短桩会当作
既存导线注册回 scene**,后续连接对它做同样的异网硬拒——同器件相邻异网引脚
(隔离 DC-DC 的 B0512S 类四域脚)不再出现短桩共线相触被 EasyEDA 合并成隐性
短路;规划器会自动换方向/offset 错开,四向全堵时按 #64 语义响亮报
"no safe candidate" 拒绝落笔。多域脚器件仍建议 power 上/gnd 下方向分治,给
规划器留出错开空间。**标签 stagger 用真实 marker bbox 预测(#148 Phase-2):**
预测框按 family + direction 使用活体 `getPrimitivesBBox` 标定值,并相对连接端点朝
body 所在一侧偏移,不再用旧的端点居中 24×11 框:ground 为 10×21/21×10、
power 为 6×11/11×6;**netport 的长度跟网名走**(`6*len(net)+8`,下限 31)——
写死 31 会让任何长于 3 字符的网名少算,评分器于是算出「刚好不撞」而渲染出来擦在
一起。**预测框 = 符号本体 ∪ 文字带**:`sch check` 的 marker-overlap 判的就是合并
后的框(power/ground 的网名画在符号旁,长 `6*len(net)`、高 12),判定与生成必须
同一把尺,否则评分器挑的「干净」位置在 check 眼里照样重叠。故
**10-unit pitch 平行脚上相邻 marker 会触发 stagger,自动挑不同 offset 错开**;
候选打分与同批后续连接注册回 scene 使用同一预测函数。

**密集区会拉长桩线超出 `--offset-max`。** 常规档位里一个「既可选(未被 #64 硬
拒绝)又不撞 marker」的候选都没有时,规划器把候选范围扩到 **3×offsetMax** 继续
找干净位置 —— 人工画法本就如此(同侧密集旗阶梯 offset 错列)。所以看到某根桩线
明显比同页其他的长,那是**让开标签**的结果,不是失控;扩展候选照样过全部判据,
#64 短路保护不会被绕过。真机 ceshi 单块回归:markerOverlaps 12 → 3。
残留不可避免的密集重叠由 `sch check` 的 marker-overlap 门捕获(见 Actions)。

**net_label 一列排齐、不错开（block-apply 在必须用标签的跨框/跨页信号上默认）。** 原生网络标签的网名画在线上、落点右侧；规划器给每个
≤7 字符的网名同一桩长 40（更长按字宽 +10），左侧引脚的网名正好落在桩线上、不压引脚；
不做 stagger、不拉长档位；出现压字必须回到布局/桩长调整，不能以“相邻轻微压字”放行。`sch zone move` /
`group-move` 平移导线时会把线上的网络标签按同一 (dx,dy) 重挂到新线上并逐个校验。

**竖脚上的 netport 拐弯挂横标签。** 竖放上拉/RC 的朝上脚,直出只能竖着折起标签
(costFoldedPort),横出又违反「首段沿引脚朝外」。规划器为此加拐弯候选:沿引脚朝外
先走 10/20/30,再横向出桩挂标签(参考图 BOOT0 画法);整段由 `connect_pin` 的
`via` 画成**一根**折线,拐点不会被合并掉。竖腿与普通桩线过同样的硬拒/穿件判据。
另有三项与标签互压同级(1000)的检查:桩线穿过已有标记、标签(含符号自带短引脚)
压在已有导线上、桩线或标签挡在另一只**未接线**引脚前方 30 以内的出线口。

**Hard rejects (issue #64):** two hazards are never soft penalties — they make a
candidate *unusable* no matter the offset, because EasyEDA would silently merge
nets and the post-hoc DRC can't see it: (1) a stub whose endpoint or path touches
an existing **foreign-net wire** (endpoint-on-wire = junction = net merge), and
(2) a stub **crossing a non-target pin** (EasyEDA trims+connects there, and the
wire-over-pin rule exempts pin endpoints). autoconnect now pulls existing wire
geometry into the scene automatically; a wire already on the target net is fine
(that's the connection point). **Title-block intrusion is now a THIRD hard reject
(#147)**: a label landing in the A4 图签 keep-out steers to a safe direction, or —
when every candidate enters it — fails rather than dropping a netport on the
明细表 (which layout-lint, part-only, and the geometry-blind electrical check both
miss). If EVERY direction/offset is a
hard reject, autoconnect refuses to place the stub and reports the connection as
failed — resolve the layout (move the part / clear the wire / free the title-block
corner) and retry. **Create-after backstop (#147 DoD2):** the plan hard-rejects a
title-block hit using a NOMINAL box, but a marker whose real rendered width (net-name
text) still spills into the 图签 is caught by a post-batch real-bbox re-read — the
intruding wire+marker is DELETED and that pin failed, so the command never returns
success with a marker on the title block. **Partial-run bookkeeping (#146):** a batch interrupted mid-way
returns `partial:true` + `succeeded[]`/`failed[]` pin lists — **retry ONLY the
failed pins, never replay the whole spec** (a blind re-run stacks duplicate markers
on the already-connected pins, which `NetKnown=false` after a connector drop can't
detect). **Always run `sch check` right after a batch autoconnect** — its new
`duplicate-net-marker` rule is the guard that catches those stacked markers.
Review them with `sch marker-clean`; a primitiveId is not provenance, so deletion
requires explicit IDs and the full pin-net comparison performed by that command.

**带痕候选不再静默入选。** 硬拒之外的碰撞惩罚是软性累加,此前选中候选哪怕
score 上千(真机:score=1737 的长桩扎进邻组标签区)也照连且报告只显示落选项。
现在**选中候选** score 超过软阈值或 reasons 里含碰撞类惩罚时,结果行会带
`⚠ WARN`(默认档照连);**`--strict`** 则把这类连接直接判失败、不落地。
看到 WARN 的处方是**挪件腾位后重连**,不是忽略它。

**平台会随机吞掉一个连接(stuck-at-99%),autoconnect 现在自己救一次。** 实测 2821 次
connect_pin 里 57 次失败,其中 23 次是 netflag 卡在「请求被丢掉但平台不报错」——
它是随机的,同一脚重发通常就成。所以失败后**重试一次,但只在连接器明确声明回滚
之后**(netflag 失败时它会先删掉已建的桩线);`connector did not respond` 这类
**状态未知**的失败绝不重试 —— 那可能只是我们没等到回应而对方已经建好了,重试会得到
第二条桩线和第二面旗。被救回来的连接在报告里标 `retried`,**别忽略这个字段**:
它是平台在变差还是变好的唯一现场证据。

另外 connect_pin 用的是 **35s 专用预算**而非默认 20s(**裸 `sch connect` 也
已对齐**,此前它还吃 20s 默认值,慢速成功被报成失败):连接器内部最坏路径
(wire 7s + 重试 0.25s + wire 重试 7s + netflag 7s = 21.25s)本来就超过 20s,
默认预算会让 daemon 先于连接器放弃 —— 报「connector did not respond」而对方其实
已经把线和旗建完了(实测 57 次失败里 17 次是这么来的)。**`sch connect` 失败必须
非零退出**；不能仅因回读 pin 已在目标网就报告 `slowLanded` 成功，因为修复前
就可能已经同网但几何错误。明确的几何门拒绝保留原始错误；超时等未知结果须回读
导线、标记及方向后核实，不盲重试。这项错误处理修正尚须随下一开发包部署。

```bash
# single pin by designator:pin (number OR name)
easyeda sch autoconnect --pin U1:41 --kind gnd --net GND
easyeda sch autoconnect --pin U1:3V3 --kind power --net +3V3

# 同名多脚:尾缀 * 一次连全部(连接器的冗余 VBUS/GND/屏蔽脚,#145)。
# 光写功能名会被判歧义并拒绝——autoconnect 不该替你挑一个;* 是你说"全接"。
# 对单脚是恒等,所以电源/地/屏蔽脚可放心加星。
easyeda sch autoconnect --pin J1:VBUS* --kind power --net 5V   # → J1:A4B9 + J1:B4A9

# explicit coordinates (compat with existing flows)
easyeda sch autoconnect --x 720 --y 670 --kind gnd --net GND

# preview the plan + rejected options WITHOUT mutating
easyeda sch autoconnect --pin U1:41 --kind gnd --net GND --dry-run --json

# batch spec — clustered pins auto-stagger so labels don't stack
easyeda sch autoconnect --spec p1-connect.json

# re-run the SAME spec safely — pins already on the target net are skipped
easyeda sch autoconnect --spec p1-connect.json          # idempotent, no growth

# re-route pins currently on the WRONG net (delete old flag+wire, reconnect)
easyeda sch autoconnect --spec p1-connect.json --replace
```

**Idempotent (issue #50):** before connecting, autoconnect reads each pin's
current net (via `sch list --include-pins`, which now carries `net`) and classifies
every connection into three states — `new` (floating → connect), `already-connected`
(already on the target net → **skip**, no duplicate flag+wire), and `conflict` (on a
different net). A conflict is an error by default; pass `--replace` to delete the old
flag+wire (deleted **together**, so no orphan stub — see issue #51) and reconnect.
Re-running the same spec is therefore safe and never stacks duplicates. `--dry-run`
reports the three states without mutating.

Spec JSON (`--spec`): `{"connections":[{"pin":"U1:41","kind":"gnd","net":"GND"},
{"pin":"U1:3V3","kind":"power","net":"+3V3"}], "rules":{"avoidTitleBlock":true,
"avoidPinFanout":true,"staggerLabels":true,"offsetRange":[18,80],"offsetStep":6,
"minLabelGap":12}}`. Each result reports the `selected` candidate (direction /
offset / endPoint / score), the `rejected` alternatives with reasons, and the
`wirePrimitiveId` / `flagPrimitiveId`. The title-block keep-out comes from the
shared `sch sheet-geometry` derivation (issue #26) — when the sheet bbox isn't
exposed it is reported as **provisional** and not geometrically enforced (so a
guessed box can't corrupt scoring). **Prefer `sch autoconnect`
over hand-picking `sch connect --direction/--offset`** for power/ground/netport
stubs; `sch connect` stays for when you deliberately override the geometry.
