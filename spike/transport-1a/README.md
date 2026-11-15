# 传输 1a：独立 SDK、信令与终端实验

只验证设计前提与字段，不导入产品 Link/Router，不启用直连、不修改生产协议常量。
[设计](../../docs/transport.md)、[实测与证据边界](../../docs/transport-notes.md#6-信令重试与缺口期限的模型验证)、
[已冻结（协议 16）](../../docs/transport-wire-draft.md)分别负责规则、结果和冻结编码。

## 代码与证据分层

| 入口 | 验什么 | 不声称什么 |
|---|---|---|
| `retry-gap.py`、`handshake-budget.py`、`round-loss-matrix.py` | 两条假管道，2.5 秒固定缺口、32 在途、整轮合计最多丢两帧、10 秒截止与退避 | SDK 时延、任意网络条件下成功 |
| `Signal` / `signal.mjs` / `go/labwire` | 四种语言的 bootstrap、全部信令类型、角色/代次/原串/候选门、重复与乱序、确认和关闭 | action 计数不是实际 SDK 调用次数 |
| `Sender` / `sender.mjs` | 32 窗口、冻结重发、limited、截止、退避、50 ms/16/64/4 候选汇集 | 实时调度精度 |
| `Wire` / `wire.mjs` / `go/labwire/codec.go` | 实际 X25519/HKDF/AES-GCM/Ed25519、有界严格 protobuf、共享正负向量 | 产品成员同步/租约实现或独立密码学审计 |
| `go/cmd/measure-wire`、`SenderVectors`、`sender-check.mjs` | 实际整包字节、各原生 crypto 重封装和三份冻结副本 | 128 PC 或 16 PC 同时跑 SDK；合成大候选不交 SDK |
| `go/cmd/sdk-peer`、`cross-browser.mjs`、`CrossAndroid`、`CrossIOS` | 本机 Pion 与四类真实客户端 SDK 原串、候选、restart、DCEP/未知 SID/探测通道负例 | HTTP 对端夹具不是中继；尚非产品密码学信令与 SDK 闭环 |
| `terminal/` | 两条假路径、现有业务信封/成员/租约、重组/水位/失败/重放/最终 barrier/Attach | fake tmux 与 Accept 观察点不是持久协调器或真实 CLI 验收 |

原 1 秒缺口反例与逐消息丢两份的串行反例已有负责人裁定。成功前提按**整轮合计最多丢两帧**，
超出前提保留 relay、10 秒截止；不因后续网络通知刷新退避。SDK 分项过慢也是超前提失败，不能扩大成功口径。
SDK 实验与状态机轨迹是分别运行的两层证据；产品适配、完整成员/撤销门与 16 PC 并发留给第 1 期整合。

## 隔离与依赖

每个实验/编译命令显式设置 `REMOTE_AGENT_HOME=/tmp/transport-lab/state CGO_ENABLED=0`。
输出、下载、模块缓存、构建和模拟器均放本任务自己的临时目录；重步骤前可用空间须至少 15 GiB。
不接日常 daemon/账户/手机/线上；只关闭记录了 PID/启动时间的自有进程。
SDK 从 [downloads.json](downloads.json) 固定 URL 下载并验 SHA-256；额外 JVM provider 在
[downloads-r3.json](downloads-r3.json)。摘要校验不等于发布者签名或可复现构建证明。
Java 使用 Bouncy Castle 1.85 的实验 provider，未加入 Android 产品依赖。Swift 使用系统 CryptoKit。
Go 依赖固定在两个独立 module 的 go.mod/go.sum，不修改根模块。

## 有限离线复验

先把已核验的 BC JAR 放入自己的临时目录，并准备 Go 1.26.11、Node、JDK、Swift、protoc。

```sh
REMOTE_AGENT_HOME=/tmp/transport-lab/state CGO_ENABLED=0 \
  GOMODCACHE=/tmp/transport-lab/gomod \
  python3 spike/transport-1a/verify.py \
  --out /tmp/transport-lab/check --bc /tmp/transport-lab/downloads/bcprov.jar
```

可用 `--go /path/to/go` 指定已安装工具链。脚本运行有限事件集合，不写构建等待循环；每项有 timeout，
低于磁盘下限就报错。结果和逐项耗时写 `verification.json`，模型/容量结果另存 JSON。
本入口不下载浏览器、不创建模拟器、不操作产品或真实 tmux。

共享材料：

- `testdata/transport/sdp`：15 份 SDK 原串、1 份继承指纹正例、29 份负例，共 45 项；原串禁文本换行转换。
- `testdata/transport/wire`：27 项真实密码学向量。所有身份种子是公开测试材料；绝不能用于账户。
- `testdata/transport/signal/scenarios.tsv`：150 个共享场景、2,103 个断言；RX 的类别在签名包内，顺序/时间/拒绝结果显式记录。
- `testdata/transport/terminal/expected.json`：49 组终端模型预期；不是四套终端引擎均已执行的声明。

`signal-vectors.mjs testdata/transport/sdp /tmp/transport-lab/signal` 可重新生成并先用 JS 执行轨迹；
seal 使用新随机数，所以重生成的包字节不同，语义预期不变。同一发送的副本则必须逐字节相同。
CryptoKit 的签名可随机化；Swift CLI 先核对固定随机输入下密文字节，再由 Go 交叉验证完整签名。

共享轨迹还包含冻结前 r1 回归：PolicyReply 原 seq/hash/scope 绑定、stale Challenge 的布尔字段、
逐个到达的 5/63/64 个候选、两个缺口先后出现及同一缺口拆分。BATCH_ADD/FLUSH 同时核对批长与原字节，
不只检查总帧数；POLICY 核对回复与冻结原请求。终端回归覆盖实际保留字节/伪小总长与输出两阶段身份冲突。

只复验真实浏览器中的共享轨迹时，可使用 `check-browser-signal.mjs PLAYWRIGHT_MODULE TRACE_TSV OUTPUT_JSON SDP_DIRECTORY`；
沿用上述 /tmp 环境与固定浏览器目录。它只监听本机临时 HTTP 端口提供公开测试模块，同时检查 SDP 向量，执行后关闭浏览器和监听，
无需启动 Pion peer 或移动模拟器。它不会再次测量 SDK 时延。

r2 增加 Offer 的隐式引导确认、原 Challenge Context 的 ACK 绑定和 trickle-only SDP 门；
CASE 显式声明 PC use，CHALLENGE 保存原 sealed 包；正确 bootstrap ACK 的 acknowledged generation/use 均为 0。
普通 Probe ACK 不替代 Challenge。共享终端预期另验完整编码字节计费及输出独立 10 秒缺口，连续已收但未渲染不算丢帧。
整轮丢失模型执行相同的 nonce/ACK 准入顺序：冷启动修正后的两种 Control ACK 顺序各 14,402 例，加审查指定时序共 28,805 例，
最慢 8,560 ms；关闭隐式证明的负对照按预期超时。它不把合法 Offer 的隐式引导确认当作原 hash 的传输序号 ACK。

## 真实 SDK 独立实验

`go/` 根入口、`browser.mjs`、`AndroidLab.java`、`IOSLab.swift` 生成自配对 Offer/Answer/Restart 原串。
移动包用 `build-android.py --sdk ... --work /tmp/... --aar /tmp/...` 和
`build-ios.py --framework /tmp/... --work /tmp/...`，`--baseline` 生成空壳对照；
iOS `--simulator` 编译模拟器，默认只编译未签名 arm64 目标，不安装真机。
Android 也支持 `--sdk-tree` 使用自己已核验的 AAR 解包目录。SDK 不加入主工程。

跨 SDK 时先在隔离环境编译 `go/cmd/sdk-peer`，用 `-info /tmp/transport-lab/peer.json` 启动。
私有 JSON 含 loopback URL/token/PID，只交给自有测试 App，不打印或提交。它是测试对端，持有自身 SDP 的明文权限。
浏览器可运行：

```sh
REMOTE_AGENT_HOME=/tmp/transport-lab/state CGO_ENABLED=0 \
  PLAYWRIGHT_BROWSERS_PATH=/tmp/transport-lab/browsers \
  node spike/transport-1a/cross-browser.mjs \
  /tmp/transport-lab/node/node_modules/playwright/index.mjs \
  /tmp/transport-lab/peer.json /tmp/transport-lab/cross-browser.json \
  /tmp/transport-lab/check/capacity.tsv testdata/transport/signal/scenarios.tsv
```

最后两参数启用 Chrome 与 WebKit **各自 WebCrypto** 的容量与信令重放；不是 Node 替浏览器计算。
将 peer.json 写入独立 Android App 的 files/ 或 iOS App 的 Documents/ 后再启动，即跑跨 SDK 四组实验。
Android 只用自己的 AVD/ADB server，iOS 只用自己创建的模拟器；构建产物与容器结果在 /tmp 留档后删除模拟器。
实验只为本地 peer 允许明文 HTTP；不把这一设置带入产品。

四组分别是客户端 Offerer、Pion Offerer+ICE restart、恶意额外 DCEP、未声明 negotiated SID 2。
客户端在 SetRemoteDescription 前验证并原样传入参数，不把 SDK getter 的规范化结果拿来重算 hash。
额外 DCEP 关闭 PC；未声明 SID 没有应用接收入口。Pion 接收侧由 `TestUnexpectedChannels` 实际验证。
候选错误 ufrag/generation 在 AddICECandidate 前拒绝；业务不走探测通道。
新的探测是否在 2.5 秒完成单独记录，超时是失败观察，随后继续诊断 SDK 是否最终恢复；后面的诊断耗时不是允许产品延长总期限。

结束时关闭自己记录的 peer PID、私有 ADB/emulator、iOS shutdown/delete，并确认不存在；不使用 pkill/killall。


## r3 系统自查

`testdata/transport/audit/parity.json` 列出缓冲路径与对应共享场景；报告的对照表分别说明四语言与模型覆盖，不以总数替代规则核验。
所有 Signal 从仅含成员身份/公钥的 peer 开始，禁止预加载对端 instance/boot。Context 24 和 Challenge 6 是 r3 当时未冻结 wire 的直接变更（r5 已冻结），旧实验包不兼容；生产协议仍未改。
新增的 SDK dispatch、发送 write-admit、候选 flush、查询 reply 等入口是在交付时检查的边界；外围产品适配器仍未接入。
Sender 的原 expiry 与 binding 来自冻结发送记录；owner 必须在真实身份/代次变化时更新 binding，不把网络 hint 当作新授权。
Python 的 `audit-models.py` 单独复核有限事件模型的过期/身份/代次路径；终端使用真实业务信封，后端与路径仍是假实现。


## r4 编码、准入与发送授权审计

`schema-generate.py` 从未修改的 draft proto 生成 36 种消息的 schema 表；`wire/body-bounds.json` 与 `wire/body-relations.json` 记录应用约束。`--check` 检查表与来源一致。四语言各自解析；Python 模型通过独立 codec 边界运行同一 SCHEMA/BODY 向量。517 条结构向量与 72 条已知正文向量覆盖默认值省略、显式默认值、重复/非最短/越界编码和消息间字段约束。真实加密轨迹另覆盖省略 Context/Challenge/空 Close 正文，不能用纯 parser 测试替代验签后的行为。

已知正文在 Seen、Pending、NextGeneration、bootstrap/Close/SDK 动作前校验；乱序出队再校验。Restart 绑定当前 PC 用途和代次。实验 Signal 仍是单 PC 接收器；peer-scope 的多 PC 协调由后续产品 owner 整合。未实现的已知 Control 先验证完整 schema 再返回 Unsupported，不宣称已经实现策略持久化或全部终端控制器。

Signal `replyAllowed` 和 Sender `admit` 是最终发送门。实验构造器明确给一轮有效的 fixture 授权；外围必须用 `setAuthorization` 更新当前租约上限、成员授权和单调 owner revision，并在真实写入时检查。原记录不随授权变更复活；普通网络通知不更新授权。JS 在 crypto await 后再次检查，其他实验调用在串行入口执行。终端 Go 直接用现有 `LeaseCertificate.Valid`、当前成员/密钥、状态 head 和逻辑 owner 验证每页回复与 ACK；其他三端没有终端执行引擎。两个模型把 schema_valid 与实时授权视为显式可撤销前提，既有事件矩阵继续检验时序，不承担密码学证明。


## r5 冻结收尾

Wire 已冻结（协议 16）；当前生产协议常量不变。冻结后同协议仅新增可返回 Unsupported 的类型，不改已有语义。`browser-modules.mjs` 供两个浏览器入口共享 allowlist，`check-browser-modules.mjs` 检查完整静态依赖图（10 个模块），`verify.py` 会执行；上面的 cross-browser 命令必须携带容量 corpus 和共享 trace 才能同时检查 SDK、WebCrypto 和状态机。

主动 Challenge 的虚拟对照明确预置一条已发送/记录/收到且仍有效的 Probe，绝不绕过 hash 关联；无丢包必须进入 Offer/ICE 并 Ready，逐消息丢两份的失败必须确实发生在 ICE 时序预算，不能以关联拒绝冒充时间证据。它不是无 Probe 的产品流程。

`r5-*` 共享轨迹检验 Close、换代、缺口退休、整轮结束后的原 hash 回执仍可发送；SDK dispatch 仍关闭，冲突、未见旧包、过期/撤销/换实例拒绝，无 ACK 回环。`frozen_bidirectional_status_snapshot` 共享终端预期在两页之间推进输出：两页仍显示原水位，后续查询 token 随输出变化，BarrierReply 使用同一双向快照。
