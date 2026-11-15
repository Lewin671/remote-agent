# 传输与网络切换实测

本文保存 2026-11-12 可行性验证中支持传输设计的观察、证据边界与复现入口，不定义产品选路策略或验收承诺；设计见 [transport.md](transport.md)，实施与待实测事项见 [plan.md](plan.md#传输模块与直连)。本次为原型和既有中继基线，不是 DirectPath 产品验收。

## 证据与复现口径

测量日期为 2026-11-12（America/Los_Angeles，原始数据为次日 UTC）。产品基线提交 `f8518f4f9944de01b596f8fb695065f9ce0b2c3f`、协议 12；设计任务 checkout 的协议为 13，不能把旧基线当作当前性能复验。环境为 macOS arm64、M1 Pro 10 核/16 GiB、Go 1.26.11、HeadlessChrome 157.0.0.0；Pion WebRTC v4.2.26 / SCTP v1.12.0、quic-go v0.65.0。MB/s 为十进制，MiB 为二进制。

证据来源是负责人提供的 `spike-report.md`（A–H 及「网络切换」）及配套 `data/`：`validation.json`、`network-validation.json`、各 JSONL 和库检查元数据。原型仅在本地 `wt/p2p-spike`，提交 `f871936515abd0db62d633f0671b28c721f90a8d` 和补测 `c3b85279f3db3bbfe75c41bd80ba939404d1e3ee`；原始材料未随本文入库，也没有可公开下载的归档。本文保留文件名和汇总便于核对，不链接本机绝对路径；没有这些本地对象/材料的 checkout 无法直接复跑。

有原型提交时，在独立 checkout 读取 `spike/p2p/README.md`；`run-suite.py` 跑回环/整形，`run-browser.py` 跑 Chrome，`run-network.py` 跑黑洞/迁移/隔离，`run-chrome-migration.py` 跑显式 ICE restart，`run-relay-network.py` 跑旧中继故障。入口自建临时账户、独立数据库/状态、tmux socket 与浏览器 profile；构建当前 checkout 的 server/daemon，不用日常会话，结束清理。suite 的结果目录由操作者提供，端口自动分配。

“实测”指上述数据，“源码结论”指指定基线的代码审计，“推断”不算测量。回环一般三次、每向 64 MiB，表中值为各次统计量中位数；整形每组合一次、每向 3 MiB。整形用包级固定延时和独立随机丢包，没有物理限带宽、突发丢包或真实运营商路径；中继 TCP 是用户态栈，Pion 用 vnet，QUIC 用真实 UDP 整形，拓扑不同，不能据此宣布生产协议性能胜负。

## 1. 既有中继与网络失效

**实测（协议 12 基线）**：本地 HTTP/WS、保留产品端到端加密，真实 server/daemon/tmux；终端执行 raw PTY 的 `/bin/cat`，不是 AI 模型回合或屏幕渲染。只读 GetSettings 的 p50/p95 为 0.856/1.139 ms，终端标记回显为 0.756/1.194 ms；768 KiB 块上传（含签名、加密和 fsync，不含 Begin/Complete）为 26.66 MB/s。现有 `GetImage` 的 237.09 MB/s 是预置 server 密文对象验证解密，不是 daemon 文件下载（`relay-{1,2,3}.jsonl`）。

网络故障使用真实旧网页 Remote 状态机和真实加密 Forward；浏览器 provisioning、token、安全/历史同步有 stub，不是完整 React UI 手工验收。代理仅让旧 TCP 流 RST/黑洞，新连接始终可用；黑洞代理继续本地 ACK，用于隔离应用 watchdog，不是真实手机 TCP 行为。每场景一次：

| 故障 | 察觉 / 主流 Ready | 在途 Forward | 同 ID 重试 |
|---|---|---|---|
| RST，无网络 hint | 约 0 / 1.18 秒 | 立即 unknown | 取得认证结果 |
| 黑洞，显式网络 hint | 约 0 / 11 ms | 约 45 秒后 unknown | 取得认证结果 |
| 黑洞，无 hint | 约 60 / 61.2 秒 | 约 45 秒后 unknown | 取得认证结果 |

Forward 用已执行的 RenameSession 截断回复，结果验证的是未知与缓存重试，不证明操作被取消。新 HTTP 调用约 2 ms 可用不表示旧主 WS/终端已恢复（`net-relay-*.jsonl`、`net-relay-browser-*.json`）。

**源码结论（同基线，原生端未经真机测量）**：网页无 `navigator.connection.change` 监听，静默 watchdog 60 秒，独立 Forward HTTP 约 45 秒不被主连接换代加速；Android 有默认网络 available/lost 回调、20 秒 ping 和 45 秒 API readTimeout，但终端不是可迁移逻辑流；iOS 只比较 available 与接口名，同接口换 Wi-Fi/DHCP 不触发重连，20 秒一次 ping 且等下一次检查前次 pong，静默故障可能等 20–40 秒；daemon 每 25 秒 ping、等 20 秒，没有本机接口变化订阅。这些都是恢复设计要处理的缺陷，不是既有恢复保证。

## 2. 吞吐与排队

**实测（上述库版本）**：纯传输不含产品鉴权、压缩、磁盘或 UI，不能按与产品上传的比值承诺加速倍数。

| 同机回环，默认 16 KiB 片 | 上传 / 取回 MB/s | 说明 |
|---|---|---|
| Pion Go↔Go | 68.38 / 61.90 | 双端合计平均约 4.14 核，RSS 高水位 57.57 MB |
| Chrome↔Go | 54.99 / 45.20 | 六次 setup 170–346 ms；实测 `maxMessageSize` 为 262,144 B |
| quic-go Go↔Go | 163.36 / 126.06 | 双端合计约 3.74 核，RSS 35.90 MB；假定 UDP 已可达，不含打洞 |

| 整形 RTT / 每向独立丢包 | Pion 上传 / 取回 MB/s | QUIC 上传 / 取回 MB/s | Pion / QUIC 1 KiB 回显 p95 ms |
|---|---|---|---|
| 40 ms / 1% | 0.302 / 0.340 | 1.881 / 0.529 | 44.329 / 46.413 |
| 40 ms / 3% | 0.162 / 0.191 | 0.331 / 0.283 | 212.584 / 44.118 |
| 150 ms / 1% | 0.079 / 0.094 | 0.324 / 0.136 | 153.544 / 153.628 |
| 150 ms / 3% | 0.046 / 0.051 | 0.099 / 0.086 | 1197.274 / 159.215 |

数据为 `dc-*`、`quic-*`、`browser-*`。1 KiB 为 stop-and-wait 回显，不是连续终端或键盘到屏幕延迟。两机 LAN/WAN 未测；150 ms 中继、10–40 ms 直连可少等 110–140 ms 只是条件估算。

**实测**：同 SCTP association 内另开 DataChannel，bulk 时控制 p95 约 11–15 ms，空载约 0.16–0.27 ms（`mixed-*.jsonl`）；更大的缓冲、zero checksum 没有明确回环收益。单分 DataChannel 不等于队列/拥塞隔离。16 KiB 分片的机制依据见 [RFC 8831 §6.6](https://www.rfc-editor.org/rfc/rfc8831#section-6.6)；较大消息与上限须按协商能力处理。

## 3. 网络切换与热备

**实测（Pion/vnet）**：6 MiB 文件与 20 ms 间隔小包，direct 黑洞后经预连接 OS TCP 备用续传；备用只有固定延时、无丢包，未接产品 server/终端/设备身份门。共享逻辑序号与 ACK 的原型校验了完整文件哈希和小包数量/连续性，不等于产品已接入。

- 默认 ICE 实际约 6.9 秒才切换。共享 association 在 3% 丢包的健康 30 秒窗口中，多个 100–1000 ms 应用期限发生误切；不能把更短的 probe deadline 当链路死亡证明。
- bulk 独立 PeerConnection 后，40/150 ms RTT × 250/500 ms 期限各 30 秒，合计 120 秒未观察误切。250 ms 配置的六次切断约 200–235 ms 检测，文件/流校验通过；500 ms 隔离配置只测健康窗口，没有断路恢复时延。250 ms 是所测窗口通过的最短值，未穷举更短值，不是长期零误判率（`net-isolated-*`）。
- 同逻辑序号的小包从一开始热备双发，在 direct 3% 丢包、40/150 ms RTT 条件下，最大应用间隔的三次中位数为 34.3/30.0 ms，所有运行范围为 27.8–43.4 ms，哈希及流序号/数量均通过（`net-mirror-*`）。这证明受控前台热备能绕开故障判断等待，不证明两路同时断开仍可即时工作。
- Pion 虚拟 IPv4 变化后显式 ICE restart，同一 PC/DC 的新业务回显中位数约 205/389/509 ms（模型 RTT 0/40/150 ms）；不是物理换网。Chrome 黑洞远端 ICE socket 后，被动 8 秒的三次运行均无自主 SDP 协商；显式 restart 的 SDP 完成不等于业务已恢复。同一个 DC 连续序号校验通过，不证明手机本地 NIC 变化行为（`net-pion-migrate-*`、`net-chrome-migrate-*`）。

**源码结论**：旧终端计数器每次开流从 0 初始化、发送前自增，首帧为 1；拒绝 `seq <= last`，只检查严格递增而非连续；两路直接广播会关流，跳号可能静默漏输出。旧 endpoint 在已开始未完成时返回 unknown，不自带跨路结果等待；每连接各建 endpoint 会破坏共享至多一次接受。只读命令绕过持久去重，不能用 ping 证明副作用至多执行一次。现有接受日志 8,192 条/16 MiB、relay 8 个执行槽；上传须按 durable 偏移推进，原型八块在途窗口不能原样移植。

## 4. NAT、移动库与验证边界

**2026-10 采样事实**：一类 IPv4 UDP 路径为端点无关映射、地址和端口依赖过滤；另一类同 socket 访问同 server 不同端口时公网映射端口变化，符合端点相关映射，过滤类型未定。仅代表采样路径，不能推导全国/运营商成功率、全部 CGNAT 或 TCP 行为。映射与过滤机制见 [RFC 4787](https://www.rfc-editor.org/rfc/rfc4787.html)、[ICE RFC 8445](https://www.rfc-editor.org/rfc/rfc8445.html)。

**推断**：同一可达 LAN 或双方全球 IPv6 可达时机会较大；对称映射不等于必定失败，也不能从 CGNAT 名称判断结果。双方严格端点相关映射、多重 NAT 或禁止 UDP 时更依赖中继；有 IPv6 地址不等于端到端可达。loopback ICE 不增加真实 NAT 成功率证据。

**依赖归档检查（2026-11-12，不是 App 运行）**：Android `io.github.webrtc-sdk:android:150.7871.04` 标准 AAR 49.07 MB，四 ABI 的 `.so` 合计 48.00 MB，arm64 12.27 MB；当前通用 APK 暂估增量 49–51 MB，arm64 包 13–14 MB，stripped 版本接入及压缩方式未验。iOS `stasel/WebRTC 157.0.0` 的真机 arm64 二进制 12.59 MB，ZIP 成员 5.82 MB，安装/下载暂估 +13–16/+6–9 MB。均为依赖成员推算，不能当实际 APK/IPA/App Store 结果；渠道是社区分发，不声称 Google 官方维护保证（`library-evidence.json`）。

**平台资料推断，未经手机实测**：Android 后台调度/Doze 与 iOS 挂起不能由 DataChannel 保活保证；NWPathMonitor/Android NetworkMonitor 不会替应用恢复业务。资料见 [Android 网络状态](https://developer.android.com/develop/connectivity/network-ops/reading-network-state)、[Doze](https://developer.android.com/training/monitoring-device-state/doze-standby)、[Apple data-only WebRTC 后台答复](https://developer.apple.com/forums/thread/799259)、[NWPathMonitor](https://developer.apple.com/documentation/network/nwpathmonitor)。本次没有操作真机、真实 Wi-Fi/蜂窝、Safari/PWA，也没有原生 SDK 运行、商店归档、30 分钟耗电/内存或生产身份绑定验证；这些项目统一列入 [待确认](plan.md#传输模块与直连)。

## 5. 设计前源码基线的核对

本节是协议 13 的历史审计快照，不描述当前分支的所有权、结果定案或安全同步实现；当前规则见 [architecture §10](architecture.md#10-报文同步和指令)，本地验收范围见[验收记录](acceptance-notes.md#传输-0a-前三步本地实现2026-11-13)。

**源码证据，未运行行为测试（2026-11-12，复核提交 `d38905f4`、协议 13；相对 `3dd329d5` 仅文档变化）**：本次只核对代码，不将旧原型数据当新基线复验。

- `internal/daemon/rpc.go:rpcErr` 将 context 错误映射成 canceled/deadline_exceeded；`internal/relay/handle.go:handleCall/execute` 把它装进 CommandResult，回调 marshal 返回 nil error；`internal/deviceauth/endpoint.go:Accept/finishAccepted` 因此可能记录 Done。代码足以支持取消被当最终答复的风险；是否已发生重复副作用及各后端取消行为未经本任务测试，由独立正确性任务验证。
- 同 ID in-progress 的 `lookupAccepted` 返回 ErrUnknown，`securitySession.accept` 把它和未知 epoch 一起触发同步；`hub.forward` 把 ForwardReply.Error 统一映射 Aborted。Android/iOS 的网络重试集合包含 Aborted，网页网络集合没有；网页 `uncertain()` 又承认 aborted，不能把“没有自动网络重试”写成“网页所有调用都会当确定失败”。
- `finishAccepted` 结果超限保留 in-progress；其他新准入触发的到期剪枝可删除尚在运行的条目，此后完成返回 ErrInvalid。不是无限保存 unknown：过期后缺少记录的原请求不能再获准执行，但也没有过去未执行的证据。
- `session()` 打开安全 endpoint，其 defer 等 workers/terminals 再关闭；执行和终端挂在 conn 上。四端终端重复序号会关流，但只检查递增，未保证连续（跳过缺口仍能被接受）。计数器初始化为 0，发送前自增，首帧为 1。终端 Input 按 256 字节送 tmux，`TermStream.command` 单次期限 5 秒，超时/管道写失败会 end 流，不能推导整帧原子性。普通 tmux 命令错误返回给 Input；`readTerminal` 对 Input/Resize/History 回调错误只记日志并继续读，只有成功才推进 last。不能把普通命令失败、超时、History 容量拒绝都写成同一种关流行为。
- `ParseHeader → Header.validate` 已检查 Purpose/kind、ID 格式、请求 ID 最短 16 字节；`codec.validID` 已限最长 128 字节。`Header.Scope` 也已核对角色与目标 Machine 成员。缺的是本机 Machine 等值及特定流 Session/Request/绑定 Sender 的上下文检查，不是所有范围检查都在 Accept 外。共享入口要保留这些已有检查并补齐上下文。
- `internal/deviceauth/codec.go` 的 MaxClockSkew 是 **5 分钟**；Accept 允许的最大未来截止时间是 **10 分钟**，二者不是同一参数。接受日志开销为 key 长度 + 结果长度 + 128 字节；16 MiB 不全是结果空间。
- agent 发文件任务分支 `wt/send-file`（核对提交 `7766a145`）已有 `Command_GetSharedFileChunk`：`readOnlyCommand` 白名单、relay handler 和共享副本设计。它不缓存块结果；“约 21 块填满日志”只适用于错误地将下载接成普通持久指令的假设，不能当成该分支的实际瓶颈。当前设计 checkout 没有该接口，不能说产品已经具备。
- 上传 `uploads.put` 在 RECEIVING 状态核对归属、重叠字节并 fsync 后保存进度；完成后的晚到块在范围内直接回状态，不重新比较每个字节。因此“所有重叠重复都比较”过宽；已完成对象的最终哈希及不再写入仍保护内容。Put 仍持全局锁，fsync 对交互公平性的影响未测。
- `CommandResult`（`proto/remoteagent/v1/remote.proto`）只有 error_code/error_message 与结果 oneof，`ForwardReply` 只有 result/error；两者都没有显式结果类别。`rpcErr` 默认 unknown；网页 `uncertain()` 接受字符串 unknown/aborted，Android/iOS 只将 unknown_outcome 或来自 daemon 的 aborted 识别为 unknown。`schedule.go` 的 expected revision、`files.go` 的 generation/文件身份冲突确实使用 aborted；Android `core/Schedules.kt` 单独按该码恢复编辑冲突，不能统一抹成 unknown。
- server `hub.forward` 在找不到电脑或 send 入队失败时返回 Unavailable；`services.Forward` 与 RPC 额度入口也可在转发前拒绝。Android Forward 对 Unavailable 报 unknown，网页按 mayHaveRun 区分，iOS `notSent` 闭包同样保留先前可能执行的状态。这些分支当前没有统一的 attempt 未转发证明。网页仅 `sendMessage` 对 deadline_exceeded 尝试新槽，仍沿用 Send 的 sendId；不能推广为所有指令都会换 ID。
- `accepted` 表及 `acceptedChecksum` 不含载荷 hash，存储 generation 为 3、路径 `security-v3.db`。`Accept` 在截止检查处返回 ErrInvalid，尽管较底层 `startAccepted` 已有 ErrExpired；只改底层映射不能区分“过期缺记录”与“无效信封”。已知结果在 finishAccepted 失败时被丢弃；回调接口只有 bytes/error，不含“不定案”。
- `readForwardCalls` 在读取下一个帧前同步等 `Relay.calls` 的槽，确会形成读循环阻塞；8 槽本身已由 Relay 持有，并非每 conn 另建。只把槽改为 2/4/2 不能解决读取/排队次序。
- server 主 WS 的 `peer.out` 共享 ForwardCall、广播和心跳；`sendFrame` 超预算会 cancel 接收 peer。当前 proto 无定向信令/投递回执，尚不能从“未来信令超额拒绝”推导已有队列能安全复用。
- `securitySyncLoop` 是 60 秒 tick、同步限 15 秒、随后串行 ensurePublished 最长 60 秒；安全 HTTP client 使用 session 登录 token。`pull` 每页最多 8 条并逐页 Install，只有追平 head 才 InstallLease；不能用现有循环声称 21 秒撤销或主 WS 外独立续 token。本次未测各后端 ctx 响应、SDK 原串或最大候选数；信令恢复预算是设计核算而非实测。

上述是 [transport.md](transport.md) 前提与改造位置的依据；新目标和决策仅在那里维护，阶段验收结果仍需另行取得。

## 6. 信令重试与缺口期限的模型验证

**虚拟时钟模型，非 SDK/产品实测（2026-11-14，v0.76.2、协议 15 基线）**：
[可复跑模型](../spike/transport-1a/README.md)落实负责人裁定：接收缺口从首次乱序帧到达起
固定 2.5 秒、重发不刷新；每方向最多 32 个未确认序号。重试点仍为初发后 500/1500 ms，
总期限仍 10 秒。Signal/Control 为独立假路径，身份与认证有效作为前提。

| 模型场景 | 观察 |
|---|---|
| Offer 前两份丢失、候选 50 ms 先到 | 1500 ms 补齐并按序处理，未退休代次；原 1050 ms 提前换代反例已消除 |
| 候选批 seq=2 前两份丢失、seq=3 先到 | 1550 ms 补齐，候选不越过缺口；每帧只处理一次 |
| ACK 丢失 | 重发同一逻辑帧只重复确认，不重复处理 |
| limited 两次、retry_after 为 800/900 ms | Offer 实际发送点 0/800/1700 ms，仍三份，同序号、同字节 |
| retry_after 超过总期限 | 不越过 10 秒补发；不能宣称建立成功 |
| 每代 Offer 前两份丢失 | 第一代已能补齐，只建一代 |
| Offer 三份全丢 | 零成本立即重开的有利模型中，10 秒内最多开始 4 代、退休 3 代，未处理 SDP；总期限不变 |
| 64 条合成消息、ACK 延迟 600 ms | 未确认窗口峰值 32，等待 ACK 才继续；这不是允许每代 64 个候选批 |

2.5 秒是接收方故障期限，与发送方重试重叠，成功补齐时不额外等待。因此不额外给原算术
1.25 秒节流 +1.5 秒重试 +3 秒 relay +1 秒引导 +3 秒 ICE/DTLS 的 9.75 秒加一次 2.5 秒；
完整引导须同时满足第 8 节的整轮丢失前提，不能把逐消息三份尝试当作无条件恢复上界。
最后重试未在缺口期限内到达、缺口到期后的新代次及超长 retry_after 须另列失败/耗时，
不沿用此成功预算或延长 10 秒。当前只有模型结果，不是四端 SDK 或 16 PC 恢复实测。

## 7. 四端 SDK 原串与独立实验

**SDK 运行与离线向量实测（2026-11-14，v0.76.2/协议 15 工作树；无产品接入）**：
[实验源码](../spike/transport-1a/README.md)单独运行 Pion v4.2.26、Chrome 157.0.8103.52、
Playwright 1.65.1 的 WebKit 27.2（build 2388）、Android `io.github.webrtc-sdk:android:150.7871.04`
和 iOS `stasel/WebRTC 157.0.0`。移动端是独立空壳 App，分别在自建 API 36 arm64 / iOS 27 模拟器运行；
不是主工程、手机或 Safari/iOS PWA 的验收。

每个实现均产生原始 Offer/Answer/Restart，固定 negotiated ID 0/1，业务通道可靠有序、探测通道 maxRetransmits=0，
同 SDK 两端收到测试消息；未创建音视频 track 或配置 STUN/TURN。原串经过实验验证器后直接传入 SDK，
输入未裁剪或重编码。跨 SDK 时浏览器 getter 即使在追加候选前也可能规范化 Pion SDP；应用核对传入参数与验证原串相同，hash 不取 getter。
正常协商未产生额外 DCEP 回调；后续恶意通道、候选负例和跨 SDK 证据见第 9 节。

[共享原串与正负向量](../testdata/transport/sdp/manifest.json)有 15 份 SDK 原串、1 份相同指纹继承正例和 25 份负例，
共 41 项；Go、JavaScript、Java/JVM 与 Swift 验证器消费同一 vectors.tsv。
覆盖 BOM/LF/裸 CR/末尾缺失、NUL、无效 UTF-8、额外音频/application/未知媒体、ice-lite、指纹缺失/算法/长度/冲突/同 scope 重复、
重复 ufrag、尺寸超限，以及 session/媒体级单个、65 个、错误 ufrag、过长/无值候选和 Answer/Restart 的内嵌候选。SDP 在候选收集前取得，只含临时实验凭据与公用占位地址；没有用户身份、私钥或实际网卡候选地址。
这些有限向量不是完整 RFC 语法/fuzz 或密码学认证证明。

候选数（每侧）Pion/Chrome/WebKit 为 1，Android 为 2，iOS 为 4。
Android 第一次独立运行触及 10 秒 SDK 期限，没有足够诊断定位；后续两次消息传递成功；最后一次已修复夹具释放顺序，先注销 DataChannel 回调再释放对象。
iOS 最初空壳未采用 iOS 27 scene 生命周期，启动崩溃；修正夹具后运行成功。这些中间失败不计入成功数，不能用最后一条耗时冒充恢复分位或稳定性保证。

**同构空壳包体对照**：Android arm64 未压缩 JNI、16 KiB ZIP 对齐、相同临时签名，空包 8,585 B，SDK 包 12,505,571 B，
增量 12,496,986 B。它是独立 debug 空壳、未跑 R8 的打包增量，不是产品 release APK 测量或设备安装空间。
iOS 真机 arm64 目标编译成功，但没有运行/安装真机：未签名 App 从 75,643 B 到 12,959,436 B（增量 12,883,793 B），
相同 ZIP 参数的未签名 IPA 从 9,032 B 到 5,957,625 B（增量 5,948,593 B）；不是 App Store thinning/加密后的下载量。

负责人已采纳实验阶段选型为 Pion v4.2.26、浏览器原生、Android 标准 webrtc-sdk 150.7871.04 和 iOS stasel 157.0.0，
均不加入主工程。Android prefixed/stripped、自建 data-only libwebrtc、iOS webrtc-sdk/Specs 是尚未运行的备选，不能按归档大小推荐发布。
Pion LICENSE 为 MIT；Android Maven POM 声明 BSD-3-Clause，分发仓库脚本 LICENSE 为 MIT；iOS XCFramework 附 WebRTC 三条款许可与 PrivacyInfo。
Android arm64 ELF 的三个 PT_LOAD 对齐均为 16 KiB，APK ZIP 对齐检查通过；未在 16 KiB 页设备运行。完整第三方 notices、Android R8、iOS 商店/本地网络权限和供应链签名复核仍属于产品依赖冻结门槛。
来源：[Pion 固定版本](https://github.com/pion/webrtc/releases/tag/v4.2.26)、[Android 分发](https://github.com/webrtc-sdk/android)、
[stasel 固定 Package.swift](https://github.com/stasel/WebRTC/blob/157.0.0/Package.swift)。固定下载 URL、SHA-256 及包体摘要另存实验 manifest；
SPM zip 校验值已与 Package.swift 一致，不把下载 hash 当作独立的发布者签名证明。

## 8. 完整引导链的串行重试下界

**虚拟时钟反例，非 SDK 时延实测**：[handshake-budget.py](../spike/transport-1a/handshake-budget.py)
把原来“双方 challenge 已知”的前提展开，主场景采用 Probe 请求、Challenge 应答的冷引导。Signal/Control 为独立假路径，Control 健康，
确定较小 device ID 的客户端作 Offerer；成员/head/租约有效。relay 恢复用允许的 3 秒，
密码学、传播、调度、SDK 和 ICE/DTLS 全部设为零成本。每条 Probe、Challenge、Offer、Answer、Confirm 各丢前两份，
第三份送达，其他帧不丢；**不是整轮只丢两帧**。

| 最早时刻 | 必须完成的依赖 |
|---|---|
| 3000 ms | relay Ready；Probe 初发 |
| 4500 ms | Probe 第三份到达，接收方才生成相应 Challenge |
| 6000 ms | Challenge 第三份到达，Offerer 取得当前接收方 boot nonce，才能发绑定的 Offer |
| 7500 ms | Offer 第三份到达，Answerer 安装后才能产生 Answer |
| 9000 ms | Answer 第三份到达，Offerer 才知道两个 SDP hash 并能发 Confirm |
| 10500 ms | Offerer Confirm 第三份最早可送达；已越过固定 10000 ms 总期限 |

Answerer 的 Confirm 已允许与 Answer 重试并行；不要求 Confirm 等 ICE，不能再通过重叠它消除 Offerer 的因果依赖。
2.5 秒缺口从首次乱序到达起算，重传不刷新；该模型没有任何 gap 超时或 generation 重开。
8 客户端×2 PC 的初发每方向 120 帧（≤128），未确认窗口峰值 7、乱序缓冲峰值 5，候选每 PC 64 个/4批；
截至 10 秒，客户端方向发送 184 帧、daemon 方向 200 帧，均低于各自 384 帧含重试预算，却是 **0/16 Ready**。
每条关键消息只丢初发的对照与无丢包对照均 16/16 Ready。

模型分别载入五个 SDK/引擎的原串，均复现相同时序；仍是同一个 Python 模型，不冒称五套 SDK 信令状态机。
它只统计原始 payload 字节，不包含尚未冻结的加密/wire 编码，所以不判 3 MiB 生产帧预算通过。
即使把这些开销免费处理，下界 **3+5×1.5=10.5 秒**仍大于 10 秒。

这证明“每条逻辑信令最多丢两份”不能由只加一次 1.5 秒的算术保证本次 10 秒成功。
另做更有利的变体：假定 daemon 已知客户端 nonce，主动发 Challenge，完全省去等待 Probe；同时允许 Confirm 与 ICE/DTLS 并行。即便如此，ICE/DTLS 用设计允许的 3 秒、从双方 SDP 可安装后完成，仍有 `3+3×1.5+max(1.5,3)=10.5 秒`，模型也是 0/16 Ready。这不是当前 SDK 实际耗时，而是设计分项允许值内的反例；没有把主动 Challenge 宣布为已冻结规则。

负责人已裁定成功前提为整轮合计最多丢两帧；上面的逐消息丢两份保留为超出前提的退路场景。每条最多三份、2.5 秒缺口、32 条窗口、10 秒总期限及 128 帧/3 MiB 均保留。超时释放本轮资源，保留防重放依据并按全抖动退避，不能因新网络通知延长本轮或清零退避。


**裁定后的有限事件枚举（2026-11-15，含 r2 准入门）**：[round-loss-matrix.py](../spike/transport-1a/round-loss-matrix.py)
分别在即时 ACK、Control ACK 晚 10 ms 两种顺序下重跑原 14,884 例（各含两个固定 Offerer 角色与 8 peer×2 PC 对照），
再加入审查指定的两次丢 Challenge ACK、第三份于 5510 ms 到达场景，合计 **29,769 例**。最多两帧丢失均 Ready，最慢 **8560 ms**。
审查序列中，Offer 4000 ms 的 nonce 证明已通过，7050 ms Ready；5510 ms 的显式 ACK 晚于最后计划的 Offer 重试点 5500 ms。
仅禁用隐式证明时，同序列会在 8050 ms 缺口退休并超时；该负对照保留，避免模型再次跳过真实准入门。
模型显式跟踪从 Probe/Challenge 学到的 nonce、各 PC 的引导就绪和原 Challenge ACK 绑定；加密/身份/成员/租约仍是模型前提，四语言真实 codec 另验。
每方向初发 120 帧，大规模对照 16/16 Ready。relay 3 秒、发现 1 秒、ICE 3 秒是注入条件；
有限事件枚举**不是穷举任意连续网络时延或同时运行 16 个 SDK PC**，不能据此放宽分项时延前提。
逐消息丢两份仍是超前提的退路测试：10 秒失败，PC/待发帧/计时器清空、relay 可用，退避不被换网重置。

## 9. 四端信令、真实封装与跨 SDK 互连

**独立实验，2026-11-15；仍非产品直连验收。** Go、浏览器 WebCrypto（Chrome/WebKit）、Java/JVM（Bouncy Castle 1.85）
和 Swift/CryptoKit 各实现同一草案 codec、信令接收器、发送窗口及候选汇集器。
[共享 sealed 轨迹](../testdata/transport/signal/scenarios.tsv)分 Signal/Control 两条可乱序、重放或丢失的假管道，
29 个共享场景（26 个信令/策略、3 个候选汇集）、636 个断言包含 Probe/Challenge、Offer/Answer、候选/End、Confirm、Restart/Close、glare 角色门、
旧 SDK callback token、未来代候选先到、同序号冲突、固定缺口期限和未知类型认证 Unsupported。
Probe 只可触发受限 Challenge；等待显式 ACK 的接收方也可用合法首份 Offer 的当前 nonce 证明解除引导门，详见第 12 节。
单独收到重放 Probe 不能进入 SDK；接收器返回的 action 才是 SDK 调用许可，计数不是实际 API 调用次数。
Close 与缺口退休分开：前者终结连接，后者允许固定 Offerer 开下一代，沿用整轮期限与防重放缓存。

四种实现的七组发送/汇集测试覆盖 32 在途背压、0/500/1500 ms、ACK 释放、limited、10 秒清空、退避不被换网重置，
以及 50 ms/16 条/64 个/4 批和旧代候选回调。时钟是虚拟时钟；代码没有用真实睡眠冒充重试精度。
[16 项密码学向量](../testdata/transport/wire/vectors.json)验证真实 X25519/HKDF-SHA256/AES-256-GCM/Ed25519，
错误身份、nonce/head/scope、拼接、畸形编码和签名不能推进状态。Java/JS/Go 的固定随机输入可逐字节比对；
CryptoKit 签名可能采用随机化，密文和 AAD 一致、签名交叉验签一致，不要求签名字节相同。
Apple 对此有[明确说明](https://developer.apple.com/documentation/cryptokit/curve25519/signing/privatekey/signature(for:))；重试必须冻结整包，不能重新调用签名器。
固定测试身份由公开种子产生，ID 仍按既有成员公钥摘要规则生成；这些材料不能用于账户。

容量实验以 8 客户端×2 PC、每候选**编码后恰 1,024 B**、每代 64 个/4 批构造实际加密帧。
候选使用文档地址，只测编码，未交 SDK。每 PC 每方向 1 引导+1 SDP+4批+End+Confirm，正好 128 个初发帧/方向。
采用 Android 原串的客户端方向 1,145,100 B、Pion 方向 1,145,956 B；最大整帧 17,075 B。
每包保留原字节送三份，为 384 帧、分别 3,435,300/3,437,868 B；ACK 每方向 384 帧、259,968 B，最大 677 B。
均小于设计 3 MiB 初发、9 MiB 三份及 1 KiB 单 ACK。Go 对五类 SDP 原串全部核算通过。
同一双向容量语料经 Chrome/WebKit/Java/Swift 自己的 crypto **重新封装、解密并经 sender 送三份**，
各得到 256/768 帧、2,291,056/6,873,168 B。墙钟包含编码/验签开销，Chrome 331 ms、WebKit 660 ms，
Java 566 ms、Swift 547 ms；它们是主机测试耗时，不能加成手机性能或端到端 Ready 保证。

真实 SDK 互连的另一层实验由本机 Pion 充当对端，Chrome、WebKit、Android API 36、iOS 27 分别实际交换原始 SDP 和候选。
HTTP 夹具是有权看到自身 SDP 的 **peer**，只监听 loopback、随机 token 认证；不是 server 密文转发实现。
Android 使用模拟器 host gateway，并由其主动 ICE 检查让 Pion 学习 peer-reflexive 候选，不让主机探测模拟器私网地址。
每个客户端各四组：自身 Offerer、Pion Offerer+真实 ICE restart、额外 DCEP、未声明 negotiated SID 2。
候选错误 ufrag/代次在 AddICECandidate 前拒绝；探测通道收到业务不分派；额外 DCEP 立即关闭 PC；
未声明 SID 不产生应用回调或业务交付。Pion 接收侧另有实际 SDK 通道负例测试。

两次信令交换后 ufrag 确实改变，可靠/探测通道可恢复，但本机 restart 时延不稳定：
Android 一次 1,189 ms；最终浏览器轮次 Chrome 6,391 ms、WebKit 5,971 ms，2.5 秒新代次探测均超时。
iOS 早一轮完整四组通过、restart 5,929 ms；新增探测曾超时，最终四组重跑通过、restart 2,593 ms，保留所有失败样本，不能据最终成功声明稳定性。
这些运行没有远端公网 STUN、真实移动换网或统计采样；不定位为单一 SDK/网络原因，不声称加探测就解决 SCTP 恢复。
设计的 ICE/DTLS 分项是成功前提，超过时须保持 relay 并在整轮 10 秒截止，不能改总期限。
故障轨迹状态机与真实 SDK 互连分别运行，尚非四端产品信令适配器的密码学闭环，也不是 16 PC 并发 SDK 性能验收。

## 10. 终端假路径与最小字段验证

**Go 有限状态实验，2026-11-15**：[terminal/](../spike/transport-1a/terminal/)
复用现有 deviceauth 的真实成员状态、租约、业务信封和 RequestIdentity；两条路径与 tmux 注入后端是测试替身。
28 组场景输出在[共用预期](../testdata/transport/terminal/expected.json)。未调用产品 Endpoint.Accept 或真实 tmux，
Accept 观察点只在重组、认证、绑定和连续序号通过后触发；不能作为持久接受日志/CLI 端到端验收。

覆盖整密文重组后认证、双路乱序/重复/冲突、5 秒碎片清理、10 秒缺口恢复、渲染后 ACK、20 ms/16 帧合并、
输出 replay 与输入依据分离、64 KiB/30 秒输入背压、部分 600 B 输入的已知 256/可能 512 B 前缀及后续 blocked，
Resize 失败不 fail-stop、换密钥仅重新加密原业务字节且只注入一次、物理断路不取消正在注入、
AttachPath 不调用 OpenTerminal、旧腿回调不能关新腿、实例变更要求核对、认证 InputStatus/分页回复/最终 Barrier/ACK、
先核清再补发旧流、旧流最终停止准入后才重新授权明确未应用输入、16 活流+16 退休条目/30 秒清理。

最小字段已落在 [wire 草案](transport-wire-draft.md)：应用 seq/原 deadline/原信封、path generation、整密文长度/hash、
processed/input_success/failed_seq、每条原授权 hash 和 outcome/注入前缀区间、查询关联 ID/不可变 snapshot token/分页完成标志、
最终 stop-admission barrier、Attach 的旧 stream/实例/新腿绑定。输出 replay 失效不传播成 Input fail-stop。
四端 codec 可承载这些固定 type ID 与有界 protobuf；当前终端语义轨迹只在 Go 跑，Java/Swift/JS 终端移植尚未执行。
其移植不要求端上模拟 tmux：将后端结果作为显式输入事件，以同一事件时钟和预期 JSON 核对即可；产品阶段仍须接真实后端。
本模型在明细满时保守背压，未模拟健康长流已确认摘要的回收优化；生产实现须接现有接受记录与水位保留策略，不能直接复制实验 map。


## 11. 冻结前复核 r1 的六项修复

**回归实测，2026-11-15，重定基到 v0.77.0 / 协议 15；未接产品路径。**
负责人采纳的六项缺陷均有命名回归；字段与规范由 [wire 草案](transport-wire-draft.md)和 [transport §5](transport.md#5-协议与安全绑定)维护。

| 复核项 | 回归与结果 |
|---|---|
| PolicyReply 缺原请求/作用范围 | 实际 sealed 请求/回复中，同 revision 的 machine/peer 正例通过；跨请求、seq/hash、scope、peer、connection 错配与旧三字段回复均拒绝。四语言共用 10 个判断；匹配对象是冻结原发送记录，不是回复自身的 Context.seq |
| stale-instance Challenge 被拒 | 字段 5=true 到达处理器，重复不重复处理；false 与省略通过，数值 2/bytes wire type 拒绝；原身份/nonce 绑定不放宽 |
| 零散候选超过四批 | 5/63/64 个候选每 100 ms 到一条、每条后 50 ms flush；中途只发整批，收集结束发尾批，分别为 1/4/4 批，顺序与内容逐字节一致；旧代与结束后的回调拒绝 |
| 后一缺口继承前一期限 | seq2@0、seq4@2000、seq1@2100 后剩 seq3：期限为 4500 ms，4400 ms 到达可继续；重发不刷新，未补齐在 4500 ms 退休。另验 seq4@0、seq2@2000、seq1@2100，剩 seq3 仍用原 2500 ms，不能无条件重置计时 |
| 分片只按声明计费 | total=1/count=128/body=16 KiB 在存入前拒绝；累计超过 total 拒绝且旧缓存不变；peer 实际保留达到 2 MiB 后再加一字节拒绝，5 秒清理归零。缓存复制片段，调用方大 backing array 或后续修改不能改变保留内容；重复不重复收费 |
| 输出重复未比身份 | 待渲染、已渲染、replay 正文清理后三处异 hash 同 seq 均拒绝；同授权重新加密仍只渲染一次；输出摘要独立保留至原 deadline。模型保守限制待渲染/摘要合计 512 条，满则背压、不提前驱逐；摘要过期后未知旧 seq 拒绝，且不改变输入水位/fail-stop。这是实验资源门，不是新增产品性能承诺 |

共享轨迹由 Go、JS/Node、Java/JVM、Swift，以及 Chrome 157.0.8103.52/WebKit 27.2 的实际 WebCrypto 全部通过，均为 20 场景/523 断言。
终端仅有 Go 原型，新增四组回归进入共用 expected.json；原有 20 组结果逐项保持，合计 24 组。
`verify.py` 已纳入 JS 共享轨迹检查并完整通过；14,884 个整轮丢失模型用例最慢仍为 8,550 ms，真实封装容量未超预算。
本轮不重跑移动 SDK 安装或 ICE 时延采样，保留第 9 节原有失败边界；未运行产品端到端验收或宣布 wire 已冻结。


## 12. 冻结前复核 r2 的五项修复

**回归实测，2026-11-15，沿用 v0.77.0 / 协议 15 基线。** 规则见 [transport §5](transport.md#5-协议与安全绑定)与 [wire 草案](transport-wire-draft.md)。

| 复核项 | 观察与共享回归 |
|---|---|
| Offer 先于 Challenge ACK | PC 用途 1/2 均可凭有效首份 Offer 的当前 nonce 证明准入，重复两次不增加 SDK 动作；迟到的合法 ACK 接受，错 nonce/错误角色/无效 SDP 不开引导门。模型显式执行同一门，结果见第 8 节 |
| ACK 误用接收 PC 上下文 | 保存发送时完整 Challenge Context/hash；共享 CHALLENGE 事件携带原 sealed 包供四端核对，不再直接赋一个 hash。use=0 正例通过，use=1、generation、seq、hash、purpose 错配拒绝；首份 Offer 已准入后也不能接受错标 ACK。普通 Probe ACK 仍可交发送窗口处理，但不标记引导就绪 |
| SDP 绕过候选门 | 四种校验器拒绝所有内嵌 `a=candidate`；新增 8 份负例，15 份真实 pre-gather SDP 原串仍全部通过。另在共享状态机中验 Offer/Answer/Restart 均不产生 SDK 动作；未重跑移动 SDK 安装或 ICE 时延采样 |
| 输出缓存越过字节预算 | 512 KiB 输出编码后为 524,665 B；seq1 缺失时第一份可留，随后两份均在插入前拒绝，实际计费保持一份。相同副本不重复计费，渲染逐帧扣费并恢复容量 |
| 输出缺口无期限 | 独立输出期限在 10,000 ms 进入网络恢复，输入推进/重发不刷新，不触发 Input fail-stop；缺口先补齐则即使尚未渲染也不误报。两个先后缺口各保留首次发现时间，11,999 ms 补齐或 12,000 ms 到期分别验证 |

Go、JS/Node、Java/JVM、Swift 共用 **29 场景/636 断言**，四端 SDP 共 **41 项**；Chrome 157.0.8103.52 和 WebKit 27.2 也在各自引擎中通过同样的场景及 SDP 向量。
终端新增四组命名预期，原有 24 组结果不变，共 28 组。完整 `verify.py`、更新后的丢失矩阵、Pion 实验包测试与容量封装检查通过。
本轮修复与验证只在实验模块，不表示产品 DirectPath、完整四端适配闭环、手机性能或真实 CLI E2E 已验收，也未冻结 wire。
