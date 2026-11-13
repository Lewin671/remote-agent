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

**源码结论（同基线，原生端未经真机测量）**：网页无 `navigator.connection.change` 监听，静默 watchdog 60 秒，独立 Forward HTTP 约 45 秒不被主连接换代加速；Android 有默认网络 available/lost 回调、20 秒 ping 和 45 秒 API readTimeout，但终端不是可迁移逻辑流；iOS 只比较 available 与接口名，同接口换 Wi-Fi/DHCP 不触发重连，20 秒一次 ping 且等下一次检查前次 pong，静默故障可能等 20–40 秒；daemon 每 25 秒 ping、等 20 秒，没有本机接口变化订阅。这些都是恢复设计要处理的缺陷，不是既有“无感”能力。

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

**源码结论**：旧终端每次开流从零编号，拒绝 `seq <= last`，只检查严格递增而非连续；两路直接广播会关流，跳号可能静默漏输出。旧 endpoint 在已开始未完成时返回 unknown，不自带跨路结果等待；每连接各建 endpoint 会破坏共享至多一次接受。只读命令绕过持久去重，不能用 ping 证明副作用至多执行一次。现有接受日志 8,192 条/16 MiB、relay 8 个执行槽；上传须按 durable 偏移推进，原型八块在途窗口不能原样移植。

## 4. NAT、移动库与验证边界

**2026-10 采样事实**：一类 IPv4 UDP 路径为端点无关映射、地址和端口依赖过滤；另一类同 socket 访问同 server 不同端口时公网映射端口变化，符合端点相关映射，过滤类型未定。仅代表采样路径，不能推导全国/运营商成功率、全部 CGNAT 或 TCP 行为。映射与过滤机制见 [RFC 4787](https://www.rfc-editor.org/rfc/rfc4787.html)、[ICE RFC 8445](https://www.rfc-editor.org/rfc/rfc8445.html)。

**推断**：同一可达 LAN 或双方全球 IPv6 可达时机会较大；对称映射不等于必定失败，也不能从 CGNAT 名称判断结果。双方严格端点相关映射、多重 NAT 或禁止 UDP 时更依赖中继；有 IPv6 地址不等于端到端可达。loopback ICE 不增加真实 NAT 成功率证据。

**依赖归档检查（2026-11-12，不是 App 运行）**：Android `io.github.webrtc-sdk:android:150.7871.04` 标准 AAR 49.07 MB，四 ABI 的 `.so` 合计 48.00 MB，arm64 12.27 MB；当前通用 APK 暂估增量 49–51 MB，arm64 包 13–14 MB，stripped 版本接入及压缩方式未验。iOS `stasel/WebRTC 157.0.0` 的真机 arm64 二进制 12.59 MB，ZIP 成员 5.82 MB，安装/下载暂估 +13–16/+6–9 MB。均为依赖成员推算，不能当实际 APK/IPA/App Store 结果；渠道是社区分发，不声称 Google 官方维护保证（`library-evidence.json`）。

**平台资料推断，未经手机实测**：Android 后台调度/Doze 与 iOS 挂起不能由 DataChannel 保活保证；NWPathMonitor/Android NetworkMonitor 不会替应用恢复业务。资料见 [Android 网络状态](https://developer.android.com/develop/connectivity/network-ops/reading-network-state)、[Doze](https://developer.android.com/training/monitoring-device-state/doze-standby)、[Apple data-only WebRTC 后台答复](https://developer.apple.com/forums/thread/799259)、[NWPathMonitor](https://developer.apple.com/documentation/network/nwpathmonitor)。本次没有操作真机、真实 Wi-Fi/蜂窝、Safari/PWA，也没有原生 SDK 运行、商店归档、30 分钟耗电/内存或生产身份绑定验证；这些项目统一列入 [待确认](plan.md#传输模块与直连)。
