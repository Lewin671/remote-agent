# 传输模块与直连

本文负责客户端与 daemon 间的分层、路径选择、恢复、安全绑定与路径状态展示；不定义账户、历史同步或原生 CLI 行为。范围见 [scope.md](scope.md)，既有信封与指令见 [architecture §10](architecture.md#10-报文同步和指令)，证据见 [transport-notes.md](transport-notes.md)，分期、验收和全部未决事项见 [plan.md](plan.md#传输模块与直连)。以下为待实现设计，当前产品仍只走中继。文中的初始预算是实现及验收的起点，不是已测性能。

## 1. 分层与职责

首个请求不等待 ICE；打洞成功且满足第 3 节的性能条件才升级，失败使用中继。增加或移除 Path 不改变业务身份和授权。单路故障与本机换网的停顿目标分别见第 4 节，不承诺两路同时失效时即时回显。

| 通信 | 负责者 | 内容 |
|---|---|---|
| 与 server 通信 | 账户、同步和推送组件 | 账户鉴权、成员状态与租约、事件先落库再推送、多设备同步、图片显示副本和推送；继续独立调度 |
| 与对端设备通信 | Link / Router / Path | 指令与结果、终端、上传分块、只读查询、`GetSharedFileChunk` 下载 |
| 协商 | 独立信令组件 | 经 server **新增一种不落库的定向转发**，双向送达设备对设备加密的信令；不复用业务 Command 或历史广播 |

```text
会话 / 指令 / 终端 / 文件 / 电脑状态
共享封装、安全入口、逻辑流重组、幂等、执行准入与结果协调
                         │ 已封装帧
Link：call / open stream / transfer / status / capabilities
                         │
Router：可达性、优先级、路径代次、探测、升级与退路
                         │
Path：RelayPath / DirectPath / 后续路径（仅收发不透明帧）
信令组件：独立的非持久定向转发与设备身份绑定
```

四端同构：Go `internal/transport`、网页 `web/src/transport`、Android `core/transport`、iOS `RACore/Transport`。共享取消、迟到、重复、乱序、背压和网络变化的一致性场景。iOS App 注入 WebRTC SDK 适配器，Swift core 测试不依赖二进制 SDK；Android 网络与 JNI 回调也经适配器进入 core。

Link 提供调用、有序可靠流、分块传输、可订阅的对端可达性和已认证能力。server 的 machine `online` 只代表中继观察，不能阻止仍可直连的操作；协议版本、文件能力改由 Link 上 daemon 签名的能力握手提供，绑定设备、进程实例、成员 head 和有效期限，最长 60 秒，变化立即更新，到期先刷新。server 转发的 machine meta 仅作发现与缓存，不作授权；在只接 RelayPath 时仍读取既有已认证 meta，保持原行为。可达不等于有权执行，能力也不替代逐次安全校验。界面不能自行按 socket 状态重发。

### 正确性前提与所有权

**第 0a 期必须先满足以下前提，之后才允许主动跨连接重试或接入 direct。** 取消结果与同 ID 协调由独立正确性任务验证和修复，本设计不把它们当作现有保证。

- 一个 daemon 进程只有一个 dispatcher，拥有安全 endpoint、接受日志、成员同步、执行上下文、同 ID 协调器、逻辑终端注册表及资源池。`session()` 只拥有该路登录凭据、socket 和转发等待者；执行、`workers` 和 `terminals` 不随主 WS 排空或销毁。进程退出才停止 dispatcher，不能同时打开互不协调的 endpoint。
- 所有路径共用入口，先作大小和范围检查、验签解密及成员/租约检查，再去重和准入。把当前 `handleCall` 的 Machine、Command Purpose、请求 ID 长度检查和 `readTerminal` 的 Machine/Session/Request/Purpose 绑定全部搬入该入口；请求 ID 保持现有 16–128 字节及合法字符约束。直连每帧 Sender 还必须等于信令绑定的对端设备，不能只证明它是某个账户成员。终端重组与 Accept 的顺序见第 3 节。
- 一次副作用操作只执行一次。开始执行前持久记受理，执行中重复只加入有界结果等待表：每 key 最多 32 个、全局 256 个，每个等待不超过原操作期限与 30 秒中的较短者；等待超时可按原 ID 再查询。等待者、成员同步和信令都不占执行槽。区分未知 epoch 与执行在途，后者不能触发成员拉取。
- 关闭连接或取消等待者不表示 CLI 撤销。执行上下文属于 dispatcher，遵守原操作截止时间；后端因取消、超时、关闭返回时，除非有可信的完成证据，必须保留不确定状态，不能将 `canceled` / `deadline_exceeded` 缓存为确定失败。撤销阻止新准入并关闭流，不倒推已经准入的副作用未执行。

Link 的四类结果是跨端契约：

| 类别 | 证据与调用方行为 |
|---|---|
| 未发送 `not_sent` | 本地证明没有任何尝试可能到达执行入口；可以发起新操作 |
| 确定拒绝且未执行 `rejected` | 可信入口证明整个逻辑 ID 从未获准执行；可修正后发起新操作。单路容量拒绝不能抹去另一条路的已受理事实 |
| 已执行并带可信结果 `completed` | daemon 签名、请求绑定且可核验的最终结果，包含成功或确定的业务失败；普通错误码本身不是执行证据 |
| `unknown` | 可能受理而没有可信最终结果；保留原身份查询，不自动换 ID 再做副作用 |

Router 合并所有尝试的证据；只要另一条可用路径仍可能返回可信结果，就继续等待或按预算查询，**所有可用路径失败或到期前不得最终宣告 unknown**。调用者主动停止等待不等于操作有了最终结果。先前可能已执行后，后来的未发送、拒绝或未认证 server 错误都不能将其变成确定未执行。

第 0a 期 server→client 必须能区分拒绝与未知：`ForwardReply` 携带结构化类别和 daemon 签名的结论，server 将拒绝映射为 `PermissionDenied` / `InvalidArgument` / `ResourceExhausted` 等原因码，将可能已执行映射为 `Unknown`，不再用单个 `Aborted` 混装。签名结论以端到端密文随错误 detail 转发，server 仅按外层类别映射状态码、不读取原生 CLI 结果或错误明文；客户端核对内外类别，不能只留 server 的文字；无签名的 HTTP/Connect 状态只作路径诊断。四端对 canceled、deadline_exceeded、unknown、aborted、internal、unavailable 及认证失败使用同一证据矩阵：可能送达之后都保持不确定，只有可验证的 `rejected` / `completed` 可以定案；从未发送的本地失败另计。这项 wire 变化按第 5 节升级，不因它早于直连而省略协议升级。

接受日志仍限 8,192 条 / 16 MiB（计入 key、结果及条目开销）：准入前容量不足就不执行。要签发 `rejected(capacity)`，还必须能持久保存该 ID 的拒绝墓碑到原期限，让迟到副本不能在容量恢复后执行；墓碑计入同一预算。连拒绝记录也写不下时仅作路径背压/未知诊断，不能假造整个逻辑操作的确定拒绝。执行后结果超限或写盘失败，保留 in-progress 并向该路等待者返回 unknown，不能伪装成功或未执行；Router 仍按其他路径的状态等待。条目到期被剪枝而回调仍运行时，进程级执行注册表仍保留该 ID，不能因持久条目消失再次执行；不能再持久保存最终结果时对外仍 unknown。回调结束释放执行槽及运行登记，不无限保留结果；原截止时间后的请求只查仍在的记录，记录不存在则不重新执行，也不能据此证明过去未执行。重启后只凭持久可信结果定案，残留 in-progress 或结果不可恢复仍 unknown。已缓存结果可在过期后查询，不把“请求过期”误写成“肯定未执行”。

### 重构必须移除的连接依赖

第 0b 期只有 RelayPath，保持现有业务行为，但所有依赖必须经 Link/dispatcher 适配，不能留下 direct 接入后才发现的闸门：

| 位置 | 改造边界 |
|---|---|
| 网页 `remote.ts` 的 schedules / snapshot / 操作入口 | `this.conn === online` 与 machine `online` 改问 Link 可达性、能力和授权；历史同步仍单独看 server |
| Android `Remote.forwardOnWorker` 及上层按钮 | machine `online` 拒发改问 Link；`retryOfUnknown` 例外不能代替统一证据模型 |
| iOS `RemoteLive`、`ProjectRows` 等 UI/core 闸门 | 主连接与 machine `online` 条件改问 Link；逐个核对，而非只换 `forward` |
| 网页 `reconnectNow` / `drop`、Android socket cleanup、iOS `RemoteTerminal.finish` | socket 只结束 Path；逻辑终端由 Link 持有。仅 RelayPath 时仍可按既有策略结束流，但不由物理连接直接调用产品 onClose |
| 三端 machine meta 消费者 | online、protocolVersion、fileUpload 等能力经 Link 提供，不直接借 server 状态授权 |
| daemon `session()` / securitySyncLoop / execute / workers / terminals | 第 0a 期建立的进程级所有权在第 0b 期接入 Link，网络同步、执行和终端不能随连接被统一取消 |

## 2. 路径与资源隔离

RelayPath 适配既有 HTTPS `Forward`、主 WS 和独立终端 WS。第 0b 期只重构，第 0c 期才改活性与恢复。账户、历史、通知和图片显示副本不改语义；新增信令定向转发见第 5 节。

DirectPath 四端统一用 WebRTC DataChannel，daemon 用 Pion，自建 STUN，不部署 TURN 或依赖第三方 STUN/TURN。UDP 被挡、NAT 不可穿透、STUN 不可用就走 HTTPS/WSS；浏览器保留默认 mDNS/候选隐私，不为取地址申请音视频权限。

选择 WebRTC 因为浏览器可用且四端有共同实现路径。[实测](transport-notes.md#2-吞吐与排队)中 QUIC 的吞吐与资源更好，但原型假定 UDP 可达，未验证手机集成或打洞。本期不引入 QUIC；上层不能看到 PeerConnection/SDP。

bulk 用**独立 PeerConnection**隔离拥塞；可靠有序 DataChannel 承载业务，独立的不重传小通道只作探测。指令/审批优先于终端，终端优先于 bulk，各自有界队列并给探测留发送预算。初始传输片为 16 KiB，遵守 `maxMessageSize` 并预留 framing 空间，不能直接发 768 KiB 应用块。先封装再分片，有界重组后进入共享安全入口，未认证数据不推进水位；上传按已持久 offset 推进，不照搬原型八块并发窗口。

第 1 期启用 bulk 时，执行槽按 daemon 全路径合计 8 个划分（第 0b 期维持中继已有并发行为）：交互/Interrupt/审批保留 2 个，普通指令与非 bulk 只读 4 个，bulk 独立并发池 2 个，bulk 不得借用保留槽。两路重复只占原执行的一个槽；等待、ACK、信令、成员同步不占槽，另有 CPU/队列限额。磁盘 bulk 不得在共享调度锁内等待 fsync；现有 uploads 全局锁的公平性也须验证，不能仅靠发送优先级承诺审批及时响应。

`GetSharedFileChunk` 明确为只读类：验签、当前成员/租约和共享对象授权仍执行，**结果不进接受日志、不落盘**，允许重新求值，进入 bulk 池；上传 Put 依靠 durable offset 幂等。下载对象、固定副本与生命周期由 [session-files.md](session-files.md) 负责；当前 checkout 尚无该接口，须整合 agent 发文件任务的只读实现，不另定一套路径/版本规则。server `GetImage` 显示副本仍走原服务。

初始资源预算：daemon 最多 8 个客户端 peer，每 peer 最多交互/bulk 两个 PC，客户端最多连接 4 台 daemon；同代次替换短暂并存也计入上限，超限保留 relay。每客户端最多 8 条逻辑终端、每 daemon 合计 16 条。每 peer 发送与重组各最多 2 MiB / 128 帧，daemon 全局各 16 MiB；单密文重组上限 1 MiB、期限 5 秒。候选、信令、重连另见第 4/5 节。daemon 自己实施数量、帧率和字节限额，不依赖 server 的每设备 16 条 WS 或账户 streams 额度替直连把关；这些本地上限不声称实现账户全局计费限额。多客户端与多 daemon 验收必须同时施压。

## 3. 选路、指令与终端

### 升级与热备

1. 认证 relay 可用即交付；后台有界探测 direct，失败退避，网络代次变化重新探测。
2. 同用途对照取至少 5 秒、至少 100 个交互样本。direct 交互 p95 不超过 relay `max(5 ms, 10%)` 的容差且队列不增长才允许升级；bulk 用同文件至少 4 MiB、两轮吞吐均达到 relay 的 95% 才升级，不能用 RTT 替代吞吐。样本不足保持 relay。
3. direct 有效时交互指令热备：**只封一次，两路发送完全相同的密文字节**，取先到可信结果。重封只用于依法允许的密钥更新，业务身份不变；不按 Path 分配序号。文件正文不双发。
4. 最近两个 5 秒窗中 direct 交互 p95 超过 relay `max(10 ms, 20%)`，或 bulk 吞吐低于 relay 90%，该用途退 relay；硬故障或软期限立即启用备用，不等 ICE `failed`。重新升级至少等 5 秒稳定且重新满足第 2 项，避免振荡。上述数值是初始策略，测量成本与能耗见计划。

### 指令与结果

同一副作用操作只准备和授权一次；双发、换路沿用原 request ID、授权载荷和截止时间。入口按 ID 合并执行及等待者，第 1 节的四类结果适用于每路及合并结果。只读可重复求值，不能充作副作用幂等证据。持久记录只有开始时继续 unknown；不因连接换代重建业务 ID，不把取消 HTTP 当取消 CLI。

接收端可在验签前合并相同密文哈希的在途校验，并丢弃已经验证过的字节级副本（每 peer 最多 4,096 个 SHA-256，保留 10 秒）；未验证帧不能进入“已验证”缓存。哈希命中不推进业务/ACK 水位；缓存被驱逐后仍须完整验签并由逻辑去重保护。不同密文即使声称同 ID 也必须验签，冲突载荷拒绝。这是省重复验签开销，不是身份验证的替代。

### 终端连续性

一个逻辑流只拥有一个 tmux control 连接。两方向各有路径无关的序号分配器、连续应用 ACK、失败水位和有界 replay；第一个业务序号为 1，0 表示尚未应用。范围/签名/当前成员/租约验证后，**先按流、方向、序号重组，再对下一个连续可执行帧调用 Accept**。不能把第 7 帧先交 Accept 执行再等第 6 帧。已应用水位、冲突摘要、失败水位独立于接受日志保留到流结束，不能随 60 秒信封过期而丢失。

缺口缓冲最多 128 帧 / 1 MiB、10 秒；只有下一个连续序号可应用。流内旧序号只确认、冲突拒绝，四端现有 `seq <= last` 即关流必须改造：Go `internal/relay/terminal.go:readTerminal`、网页 `remote.ts:openTerminal`、Android `Remote.kt:openTerminal`、iOS `RACore/RemoteTerminal.swift:openTerminal`。验证过期帧不能绕过截止时间；缺口等到帧过期就进入流失败恢复，不能永远等或跳号。发送时的 60 秒截止时间也包含本地排队和补发，不延长已授权帧。

输入帧是**顺序处理单元，不是可回滚事务**：当前 Input 每 256 字节一条 tmux send-keys，可能只注入前半帧。成功 ACK 仅在该帧所有 tmux 命令明确完成后推进；写 socket 不算成功。任一帧失败、超时或注入结果未知，记录失败序号/可能应用的前缀，进入 fail-stop：其后输入全部停止应用，也不能经另一条路补打失败帧。失败水位阻止重试，成功连续 ACK 不越过失败帧；发送方收到认证的失败状态后停止队列，保留未确认内容供用户核对。用户确认并重建才可继续，界面不能默认重放输入；快照只恢复画面，不能证明按键次数。即便后端确定某帧未执行，也不自动越过它处理后续依赖帧。

输出 ACK 在客户端渲染消费队列完成后推进；输入 ACK 在 tmux 全帧确认后推进。ACK 按方向确认最高连续**已应用**逻辑序号，最多每 20 ms 或累计 16 帧发送一次，失败状态立即发送；不得逐传输片确认冒充应用成功。每方向 replay 最多 2 MiB / 512 帧 / 30 秒，超限结束旧流。新增路径交换认证的流 ID、两端进程实例、路径代次及双向 ACK，以 barrier 证明此前连续应用，补发未 ACK 帧后才加入热备；barrier 最多等待 5 秒，超时只放弃新腿，旧腿仍健康时继续服务，不能越过水位。旧代次回调不能关闭新 Path。

中继腿丢失后发送新的 `AttachPath` 控制指令，绑定原 stream ID、sender、session、新腿 ID/代次和双方 ACK；server 按新腿 ID 配对，daemon 查逻辑流注册表后重新挂载，**不调用 OpenTerminal、不新开 tmux**。`OpenTerminal` 只创建流。重放缺口、两路全断超过窗口、daemon 重启等导致真正重建时，必须换全新 stream ID（同时是信封 Request 和去重身份），重新 OpenTerminal 并取快照，不在旧 ID 下从零计数。

终端输入、resize、ACK/barrier/失败控制帧总是热备。输出密文帧 ≤4 KiB 且每流热备副本不超过 32 KiB/s（突发 64 KiB）时封一次两路发；超阈值输出按序只走当前优选路径，保留另一腿以便补发。大输出也不进入文件 bulk PC，仍受终端队列及 replay 预算约束，不能抢审批槽；其切路恢复受探测及 replay 时间限制，不承诺一个包间隔。业务按相同逻辑水位确认两路，不生成 ACK 回环。

物理路径丢失不直接调用产品 onClose，不清屏、不失焦、不重新抢尺寸。真正重建保留旧画面直到新快照可用，并单独处理未知输入。进程崩溃不能承诺任意 CLI 的 exactly-once。

### 分块续传

bulk 软期限从 `max(500 ms, 4×近期 RTT + 调度余量)` 起，交互/切换窗口探测可每 50 ms，空闲降频，后台停止。超预算启动备用续传，不判电脑离线；250 ms 仅为受控低延时配置，[有限窗口原型](transport-notes.md#3-网络切换与热备)不能证明真实网络零误切。

上传换路先查 Status，只认 durable `persisted_bytes`，按同一 transfer ID、offset、字节及哈希重发。**原块请求尚有效时沿用原 request ID/截止时间；块已过期时允许新 request ID 和新期限重发相同 offset**，安全来自上传的归属、重叠字节比较和持久 offset 幂等，而非重复执行 CLI。这个例外不适用于会话创建、消息、审批等副作用指令。下载在固定共享副本内按已经校验并持久保存的连续 offset 恢复，允许只读块用新 ID 重新求值；副本删除/过期按文件设计失败，不悄悄切换文件。进度不归零，未确认尾部不算完成。

## 4. 网络切换与第 0 期中继修复

### 可测的恢复承诺

以下都是**待产品验收的目标**，前提为进程前台可调度、授权有效；不将系统无网或挂起时间算入可用网络上的恢复预算：

| 情形 | 目标与测量起点 |
|---|---|
| 单路故障，备用路仍通 | 热备范围内的交互流量，故障后最大应用间隔相对健康对照增加不超过一个正常包间隔（20 ms 发包场景为增加 ≤20 ms）；记录两路 RTT，该目标仍须在非对称 RTT 下验收：备用路的传播时延可能使其不达标，需如实报告并由计划中的目标校准决定后续处理 |
| 本机 Wi-Fi↔蜂窝等，两条路同时失效 | 从新网络可用的通知/首次可验证探测起，relay 达认证 Ready 且一次真实 Link 调用可用目标 ≤3 秒，direct 恢复身份绑定与业务可用目标 ≤10 秒；不把只收到 Ready 当作业务恢复 |
| 无系统 hint 的静默黑洞 | 前台活性预算目标 ≤5 秒判可疑，再按 relay ≤3 秒 / direct ≤10 秒恢复，分别记录发现与恢复；不能用 hint 场景掩盖静默故障 |

双路断开目标以[旧中继 RST 约 1.18 秒到 Ready](transport-notes.md#1-既有中继与网络失效)为有限依据，为重拨与认证留到 3 秒，另给信令/ICE 到 10 秒；该单次本地测量不是移动网络证明。超 RTT 条件、大输出、重放越界分别报告偏离，不能改写成已经达到目标。

预算内不因暂时断路弹操作错误、不清屏，界面可显示恢复状态；输入每流有界缓存 64 KiB / 10 秒，不显示虚假已送达，超限停止接收并明确要求处理，不能静默丢输入。恢复后原流未 fail-stop 且在窗口内才自动续接；副作用结果按原 ID 核对。超恢复目标需给可证实状态，不能永远隐藏失败。

### 第 0c 期活性与重连

必须在第 0a 正确性与第 0b 重构之后做。账户同步组件接收统一网络代次和活性信号，但其同步结果不等同对端可达性。

| 端 | 改造 |
|---|---|
| 网页 | online/offline、可见性、freeze/resume 及支持时的 `navigator.connection.change`；原始收帧更新心跳，不等历史解密；移除 60 秒静默等待，换网唤醒原 ID 的 Forward，不让旧 HTTP 独自等约 45 秒 |
| Android | 比较默认 Network handle、地址、LinkProperties 与能力；合并通知，真实路径变化才推进代次；缩短前台 ping 预算，原 ID 调用可协调恢复 |
| iOS | NWPathMonitor 核对路径信息，同接口 Wi-Fi/DHCP 靠活性兜底；不能只比较接口名，缩短现有 20–40 秒检测 |
| daemon | macOS/Linux 接口/路由/地址通知加有界探测，替代仅 25 秒 ping + 20 秒等待；新 relay 建连不等待旧 workers/terminals 排空，旧 Path 只停止接收并退役等待者，进程级执行仍共享同一配额 |

初始前台心跳为 1 秒，连续 3 次缺失后试新连接，含调度余量的判可疑目标 ≤5 秒；单次探测失败不发布电脑离线。每目标重连令牌桶容量 2、每秒补 1 个，进程全局容量 4、每秒补 2 个；重复通知合并，首次有效换网可立即用令牌，耗尽只保留最新代次。失败按 1/2/4…最高 30 秒全抖动退避，系统通知不能清零桶或反复重置退避。新网络的快速恢复目标以桶有可用额度为条件；风暴测试同时报告限流后的恢复延迟，不能声称任意通知洪泛都 ≤3 秒。

允许一个旧路径与一个试探新路径短暂并存，认证响应后才切换，旧回调不覆盖新状态；daemon 服务端顶替主连接后旧路可立即退役，独立执行不受影响。共享 8 槽仍可能被长操作占用，要靠隔离和准入保证交互，不能通过无限新建 worker 绕过上限。更短预算在 3% 丢包、调度暂停和 bulk 下可能误判，代价限定为有限探测/重拨，**不能取消已执行操作、重复执行、结束逻辑流或连累另一条健康路**。验收统计每小时误试探次数、流量及恢复分位，初始目标健康 3% 独立丢包下误发布离线为 0、额外试探 ≤6 次/小时；未满足先调预算，不放宽安全语义。

后台不以伪音频/VoIP 或无限 ping 保活，暂停活性累计。回前台/休眠唤醒先 Touch、刷新成员/租约，核对单调时钟与路径代次，再恢复 relay、结果/offset 及逻辑流；不能直接用睡眠前的 socket。daemon 重启丢弃 PC、内存 replay 和进程实例，副作用只查持久结果，终端必须新流+快照，未知输入不重打；唤醒若越过租约或 replay 期限也先关旧流。客户端同样遵循平台进程回收边界。

## 5. 协议与安全绑定

保留现有业务信封加密、签名、AAD 和防重放；DTLS 不替代设备授权。绑定还能防错误设备连接、IP 暴露和 DoS，但不能消除 relay 已知的交互元数据，热备仍有中继小流量。

### 第 1 期一次性冻结的 wire 契约

第 0a 的拒绝/unknown 拆分若已改变旧端理解，单独协调升级；**第 1 期一次性定义以下完整 wire 格式并四端共同解析及拒绝非法组合**，第 2/3 期只开启相应能力，尽量不再升协议。字段编号和编码由实现时 proto/安全编码及共用向量负责，本表规定不能拖到后期的语义。

| 格式 | 第 1 期必须覆盖的语义 |
|---|---|
| 外层定向帧 | ToDevice、FromDevice（server 从登录设备写入）、Signal/Control 类别、有界密文字节；无历史序号、无落库 ACK，离线立即失败 |
| 身份与代次 | 固定域/账户、双方设备和角色、machine、协议、成员 epoch+head、双方进程随机实例、随机 connection ID、PC 用途、generation、双方 boot challenge、信令方向与单调序号、短期限 |
| 分片 | 消息 ID、片号/总片数、完整密文长度与 hash、路径代次；重组后才验业务信封，片级收到不算业务 ACK |
| 逻辑流 | stream ID、方向、路径无关逻辑 seq、原期限；流 ID 永不复用；路径 generation 不重置逻辑 seq |
| 结果 | `not_sent`（本地）、`rejected`、`completed`、`unknown` 与原因、请求/载荷 hash 绑定、认证结果或拒绝证明；等待中只是本地状态，不是最终 unknown |
| ACK / replay / barrier | 双向最高连续应用 seq、失败 seq/状态、重放起点与窗口、barrier ID/水位、barrier 确认；ACK 不按片、不越过缺口 |
| 新 Purpose 与控制体 | 设备对信令 `PeerSignal`；传输控制 `TransportControl`，显式区分 Capabilities、AttachPath、Ack、Replay、Barrier、StreamFailed、ClosePath、DirectPolicy；控制序号独立于业务与接受日志。现有 Command/Result/终端 Purpose 仍按用途校验 |
| 信令类型 | Probe/Challenge、Offer/Answer、Candidate/EndCandidates、Confirm、Restart、Close；每种状态及允许角色/序号有固定转换，禁止将任意 Command 混入信令 |

业务信封的 Header.Seq 是逻辑序号，不绑定某一条路径代次；分片外层的路径代次只用于该路重组和旧回调隔离，不能授权、推进逻辑水位或改变两路相同的业务密文。控制体将需要认证的路径代次显式签入；TransportControl 不走业务接受日志，ACK 等依赖逻辑水位防重放，策略更新依赖原子持久化 revision 幂等。

未启用能力收到相应控制体时返回认证的 unsupported，不执行；不能以忽略未知控制体代替解析。四处协议号（Go `internal/protocol`、网页 `web/src/protocol.ts`、Android `core/Version.kt`、iOS `RACore/Version.swift`）同值；具体号随实现基线确定，本次文档不改。后续若仍改变约定就按 [architecture §4](architecture.md#4-协议设计要点) 升级，不写旧协议降级。可关闭直连只是**同一协议内运行时开关**，不能回退上一期二进制；升级/重建要求必须写发布说明。

### 非持久双向信令

选**新增定向 server 帧**，不采用 Forward 长轮询应答：双方主 WS 均可发送 `DirectedPeerFrame`，server 校验登录设备、同账户目标及 Client↔Machine 角色，向指定设备的主 WS 投递 `PeerFrameDelivery`；daemon 可主动发送 answer、追加候选和 ICE restart，不需等客户端轮询。外层 sender 不取客户端自报值，接收端解密后还核对签名身份和内层目标；server 只见路由标识、长度和时序。同一种定向帧另设 Control 类承载加密 TransportControl，让能力握手、AttachPath、远程策略及确认也有双向 relay 载体；已有终端腿及 direct channel 可直接承载控制密文。Signal/Control 分类不暴露业务内容，不能据外层类别免去内层 Purpose 校验。离线不排队、不补投、不广播、不写数据库或持久日志；对端断线就失败，重连重新协商。

信令**不经过业务 Accept/持久接受日志**，使用独立认证入口、单调序号和有界内存防重放表；不会让 trickle 候选占用 8,192 条业务日志。server 按发送设备 8 帧/s、突发 16 帧及 256 KiB/s、突发 512 KiB 限流，账户合计 32 帧/s、1 MiB/s；一帧密文最多 64 KiB、单目标队列最多 16 帧 / 256 KiB、驻留最长 5 秒，server 全局待转发最多 8 MiB。超限拒绝当前信令，不能挤出历史/审批；所有限制均只看外层，不能解析 SDP。Control 使用独立保留队列和预算：每设备 512 帧/s（突发 1,024）、512 KiB/s，账户 2,048 帧/s、4 MiB/s，单帧最多 16 KiB，单目标队列最多 32 帧 / 256 KiB、驻留 5 秒；与 Signal 合计仍受 server 全局 8 MiB 待转发上限。候选洪泛不得占用控制保留队列。daemon 同时以 peer/全局同等或更严的独立预算防恶意中继绕过 server 限流。

### 设备对加密、指纹与防重放

含 IP 和 ICE 凭据的 SDP/候选用**双方成员状态中已认证 X25519 密钥**导出设备对密钥，经 HKDF 的固定信令域、账户、排序设备 ID、双方进程实例和 connection ID 隔离，使用 AEAD 并由发送设备签名；不用账户内共享的内容密钥。Probe/Challenge 也设备对加密并签名，可用不含 SDP 的引导域交换 boot nonce；正式代次密钥只在双方 nonce 都已认证后导出。不能从 server 普通 meta 接收密钥。实现须提供四端加密/签名/编码一致向量，不自创另一套成员身份系统。

每次进程启动随机生成 boot nonce，先 Probe/Challenge，Offer 必须绑定接收方当前 nonce；老 Offer 在接收方重启、内存缓存丢失后仍不能重新建连。Probe 重放最多触发受限新 challenge，不能创建 PC 或送 SDP。缓存 key 覆盖双方设备、实例、connection、generation、方向和序号，保存到信令截止时间 **加当前 `deviceauth.MaxClockSkew`**（当前代码为 5 分钟，不是 10 分钟）；信令期限最多 30 秒。每 peer 最多 2,048 条、全局 16,384 条，满时拒绝新协商，不能提前驱逐仍有效的防重放项。关闭连接仍保留 tombstone 到窗口结束；持久成员时间 floor 防时钟回退，回退异常暂停协商。

每方向每 generation 序号从 1 单调增加，Offer/Answer/Candidate/Confirm 都计数；完全相同重发仅返回已有确认，不重调 SDK，同序号不同 hash 拒绝。乱序最多缓冲 32 条 / 1 秒，缺口超时重开 generation，不能让候选越过未验证的 SDP。ICE restart 增 generation、换 ICE 凭据，connection 与对端身份不变；进程实例变化必须新 connection。glare 采用固定规则：按 device ID 字节序较小的一方作 Offerer，另一方只发 Restart 请求；冲突 offer 拒绝并保持确定角色，不同时安装两份 SDP。

双方必须先同步到**同一已验证成员 epoch+head**才协商。收到未知的较新 epoch 可有界拉取并验证链后重启协商；旧 epoch 不借历史成员身份继续连接，也不按发送方旧 head 放行。同步不成功就保留 relay；任何 Install 改 head 后重新校验会话，并重建受影响的信令代次/指纹绑定，不允许旧 head 无限续命。

SDP 以严格 UTF-8、CRLF 行结束解析，拒绝 NUL、重复/歧义属性和未知媒体；只允许一个 `m=application` SCTP section、无音视频、无 `ice-lite`，恰好一个有效的 sha-256 指纹值，session 继承与每个 m-section 的声明必须一致，其他算法、冲突或同 scope 重复指纹均拒绝。不通过字符串裁剪“修好”SDP；验证的 hash 覆盖**完整原串**，交 SDK 的必须就是已验证的原串，不能重序列化。验证 candidate 绑定当前 ICE ufrag/代次，每 PC 每代次最多 64 个候选、每秒 8 个；禁止凭候选扫描额外端口。

每 PC 只允许预定义的业务 channel 和探测 channel（固定 negotiated ID 0/1），无额外 DCEP 通道、媒体或未声明通道；探测不承载业务。先验签、设备/角色、目标、head、租约、nonce、序号、期限，再交 SDK。DTLS 必须核验认证指纹；双方 Confirm 签名绑定两份 SDP hash、指纹、nonce、connection、generation 和用途后才开放业务。重放、拼接、伪指纹、ICE 凭据替换均拒绝，不关闭 SDK 指纹校验。

### 撤销、寿命与关闭直连

成员同步及定时 Touch 由进程级组件负责。每次 Install、InstallLease、Touch 后（即便操作报错）遍历所有 direct 会话，检查双方当前成员/角色/密钥/head 和本机有效租约；失效立即阻止新帧、停止输出、关闭 PC、清除未验证缓冲。仅收输出的会话也必须检查，不能等下一次 Accept。每帧含输出/控制均按会话绑定核对 Sender；旧授权不能让被移除成员续收终端。

PC 的授权寿命不超过当前已验证租约 NotAfter；定时器最多 1 秒粒度，不因无流量延长。租约续期后须重新校验会话绑定才可延长，设备密钥变化直接重建。前台安全轮询初始 15 秒、整轮同步（包含所有分页及租约安装）期限 5 秒：**诚实且可用 server 的新成员状态可读起 ≤21 秒关闭失效 peer，已 Install 起 ≤1 秒，租约 NotAfter 起 ≤1 秒停止收发**。这些上界以进程可调度、同步在 5 秒内返回为条件；挂起时不收发，唤醒先检查再收发。server 扣留撤销或网络隔离不能保证 21 秒，此时最大暴露受现有租约剩余期限约束（最长 72 小时），不是无限会话；时钟异常按现有安全 floor 暂停，不能再加时钟偏差宽限延长 NotAfter。信任边界仍见 [scope §4](scope.md#4-信任边界)。

daemon 提供持久的 `direct_enabled` 本机设置和 Client 经已认证 `DirectPolicy` 的远程关闭能力；管理端可改机器策略，普通客户端只能关闭自己的 peer。关闭动作可经 relay 送达，不要求 direct 健康；策略带单调 revision，回执签名，daemon ≤1 秒关该范围所有 PC、停 STUN/ICE，重启仍关闭。重新开启机器策略仅本机或管理端可做；任一端关闭即不建立 direct。两路都不可达时远程设置不能声称已生效，待认证回执；不新增 server 解密控制面。各端本机 off/auto 默认开，保存粒度见计划。

## 6. STUN 与诊断

自建 STUN 仅发现映射，独立 IPv4/IPv6 UDP 入口，HTTPS 代理不能自动承载；不做 TURN、端口预测或扫描。小响应、源地址内存短期限流与全局预算防反射放大，关闭持久 IP access log。生产容量等未决见计划。

日志不记 IP、端口候选串、SDP、ICE 凭据、指纹全文、原始 ICE 错误对象、内容或密钥，关闭或接管 Pion/libwebrtc 默认调试输出。仅本机有界滚动聚合候选类型/地址族、字节数、RTT p50/p95、吞吐、队列峰值、建连/恢复耗时、退路原因、unknown、replay/快照和 ICE CPU；不上传。信任域暴露见 scope。

## 7. 界面规则

界面只订阅 Link 状态，显示交互主路径及 RTT，bulk 路径在检查页单列；热备 relay 副本不把主路径写成中继。可达性与 socket 分开，soft timeout 不发布电脑离线；server 历史同步不可用按 [architecture · 断线](architecture.md#断线时显示什么) 单独提示。

- 会话副标题电脑名前，direct 加 12 的 `bolt.horizontal` 类图标、`ink2` 色；relay 无图标。点开气泡显示路径和 RTT，例如“直连 · 12 ms”/“经中继 · 180 ms”，第二行 13 号 `ink2` 说明“手机和电脑直接通信”或“经中继服务器转发”。仅显示可证实的尝试进展，不仅凭超时断言对称 NAT。
- 设置 › 电脑详情标题下显示“在线 · 直连”/“在线 · 经中继”；电脑状态行附路径及 RTT。连接检查页显示各用途路径、直连可用性、原因、最近切换、恢复耗时及热备范围。
- 切换仅用 200 ms 淡入淡出更新图标文字，不 toast、震动或横幅；可见页面每 5 秒更新 RTT，隐藏停止显示用探测，Router 活性探测另计。超过恢复预算、输入超限或 fail-stop 必须明确呈现，不能以隐藏错误维持表面连续。
- 本机“尽量直连”默认开，关闭后不发 STUN/ICE；说明例句为“连接更快时优先直连，内容同样端到端加密。直连时对方设备能看到你的 IP 地址，中继看不到通信内容。”机器策略关闭时说明限制来源，不让客户端本机开关覆盖 daemon 策略。

以上文字与时延为设计示例；产品实施时进 `i18n/messages/<domain>.json` 并生成包装器。本次不改 HTML，界面稿另按计划出新版。
