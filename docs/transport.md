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

### 当前实现与目标边界

协议 15 的本地实现接入了第 0a 期的显式完成证据、进程级执行/安全状态、独立同步、v4 接受记录与五类签名结果。产品仍只有中继；完整验收和后续阶段见 [plan](plan.md#直连实施与验收)。这不表示已实现跨路径重试或 direct。

### 正确性前提与所有权

**第 0a 期必须先满足以下前提，之后才允许主动跨连接重试或接入 direct。** 取消结果与同 ID 协调由独立正确性任务验证和修复，本设计不把它们当作现有保证。

- 一个 daemon 进程只有一个 dispatcher，拥有安全 endpoint、接受日志、成员同步、执行上下文、同 ID 协调器、逻辑终端注册表及资源池。`session()` 只拥有该路 WS 登录凭据、socket 和转发等待者；独立成员同步循环持有自己的登录凭据与 HTTP client，凭据续期不依赖主 WS（见第 5 节）；执行、`workers` 和 `terminals` 不随主 WS 排空或销毁。进程退出才停止 dispatcher，不能同时打开互不协调的 endpoint。
- 所有路径共用入口，先作大小/范围检查、验签解密，认证后查询同 ID 记录，新执行再检查当前成员/租约并准入。把当前 `handleCall` 的 Machine、Command Purpose、请求 ID 长度检查和 `readTerminal` 的 Machine/Session/Request/Purpose 绑定全部搬入该入口；请求 ID 保持现有 16–128 字节及合法字符约束。直连每帧 Sender 还必须等于信令绑定的对端设备，不能只证明它是某个账户成员。终端重组与 Accept 的顺序见第 3 节。
- 一次副作用操作只执行一次。开始执行前持久记受理，执行中重复只加入有界结果等待表：每 key 最多 32 个、全局 256 个，每个等待不超过原操作期限与 30 秒中的较短者；等待表满立即回复 `unknown/busy`，等待超时回复 `unknown`，均只允许原 ID 查询；只有确有本进程运行者才入表，重启残留及已结束未定案立即回复 unknown。等待者、成员同步和信令都不占执行槽。区分未知 epoch 与执行在途，后者不能触发成员拉取。
- 关闭连接或取消等待者不表示 CLI 撤销。执行上下文属于 dispatcher，遵守原操作截止时间；后端因取消、超时、关闭返回时，除非有可信的完成证据，必须保留不确定状态，不能将 `canceled` / `deadline_exceeded` 缓存为确定失败。撤销阻止新准入并关闭流，不倒推已经准入的副作用未执行。

### 结果类别与证据矩阵（第 0a 期最终契约）

Link 使用五类结果，`executed` 包含确定的业务失败，不等于业务成功：

| 类别 | 证据与调用方行为 |
|---|---|
| 未发送 `not_sent` | 所有尝试均有未进入执行准入的证据；可以发起新操作。路径上的未转发诊断只证明该次尝试 |
| 确定拒绝 `rejected` | daemon 对请求绑定的单调终局原因签名：信封无效、结构/范围非法、协议不符、成员已移除。相同授权载荷的迟到副本仍拒绝；改正载荷必须用新 ID |
| 已执行 `executed` | daemon 明确确认操作完成或业务已定案，含成功、确定业务失败；精简记录可只返回类别与结果正文不可恢复标志 |
| 未定 `unknown` | 可能受理且无可信终局结果；只查询/重试原 ID，不自动换 ID 做副作用 |
| 冲突 `conflict` | 同 ID 不同载荷，或后端确定的版本/对象冲突。必须带冲突种类；前者不能证明原载荷的结果，后者保留现有重载/编辑冲突流程 |

容量、租约、时钟、未知 epoch、存储故障不是单调拒绝，不能签发 rejected；恢复后只可重试原 ID。成员移除按本次授权所绑定的成员身份/密钥判定，重新加入不是恢复旧授权。无效签名只能拒绝该字节串，不能拿其声称的 ID 覆盖另一份已认证请求；未能认证逻辑身份的拒绝对业务结果仍是 unknown。先查经认证的同 ID 记录：已受理不能被后来的范围/版本拒绝改写成未执行，同 ID 不同载荷返回 conflict。

`CommandResult` 的签名密文内增加显式 `outcome`、`reason`、请求 key hash、载荷 hash 和 `result_available`；外壳也能表达无法解码为 Command 的认证入口结论。daemon 在每个后端调用点依据完成证据分类，不从 Connect 字符串反推副作用。后端没有完成证据即 unknown；取消/超时可以是 unknown，也可以在已有可信完成证据时是 executed。定时任务 revision 冲突、文件传输 generation/对象冲突标 `conflict/business`，保留业务码及重载流程；同 ID 载荷冲突标 `conflict/identity`，不允许把原操作当未执行。业务冲突已定案的本次调用可缓存，identity conflict 不覆盖原记录。查完已有身份和运行者后，过期且缺记录先返回 unknown，不能由当前结构或协议检查改称 rejected（包括手动升级丢弃的旧记录）。结构/范围/协议等单调拒绝检查在新请求持久受理前完成；已经进入后端的回调不能再用 rejected 表示业务失败。

`ForwardReply` 转发该密文证明，外层只携带路由需要的类别/原因码；server 不读取结果或错误明文。客户端核验签名、请求/载荷绑定和内外一致性，最终以密文中的类别为准。server 自发失败增加绑定 attempt ID 的 `not_forwarded` 标志，必须在任何入队/转发之前决定；入队后断线、写失败或超时不能补称未转发。错误码仅供诊断及业务呈现，不作结果类别判断。第 0a 即定下最终结果格式，第 1 期复用，不重新定义。

下表是四端共用测试向量的规范。H 表示**此前其他尝试**可能进入准入；当前尝试也可能已进入时归入 H=是。H=否包含发起过 HTTP 但收到与该次 attempt 绑定的转发前拒绝。所有状态码枚举须逐个生成向量，不能只测一条代表值。

| 来源 | 状态/证据 | H=否 | H=是 |
|---|---|---|---|
| 本地 | 校验失败、取消、过期、无网、槽/队列不足，能证明本次尚未准入/发出 | not_sent | unknown |
| 本地 | 已发出后的取消、超时、断线、解密/签名/请求绑定失败 | unknown | unknown |
| server 自发 | Unavailable（电脑离线、队列满）、ResourceExhausted（配额/限流），有 `not_forwarded` | not_sent，保留电脑离线等具体原因 | unknown；该次未发送不能覆盖先前尝试 |
| server 自发 | Unauthenticated、PermissionDenied、InvalidArgument、FailedPrecondition，有 `not_forwarded` | not_sent，保留登录/更新等具体原因 | unknown |
| server 自发 | canceled、deadline_exceeded、unknown、aborted、internal、unavailable 或其他码，无 `not_forwarded` | unknown | unknown |
| daemon 签名且绑定有效 | outcome=rejected，单调原因；任何诊断码 | rejected | rejected，仅限相同载荷且不存在已受理证据，否则协议矛盾按 unknown 报诊断 |
| daemon 签名且绑定有效 | outcome=executed；成功或任意错误码（包括 unknown/canceled/deadline_exceeded） | executed | executed |
| daemon 签名且绑定有效 | outcome=unknown；任何码，包括 unknown、aborted、internal、unavailable | unknown，只重试原 ID | unknown，只重试原 ID |
| daemon 签名且绑定有效 | outcome=conflict/business；aborted 等业务码 | conflict，原有编辑/文件冲突流程 | conflict，原有编辑/文件冲突流程 |
| daemon 签名且绑定有效 | outcome=conflict/identity；任何码 | conflict，原载荷单独查询 | conflict，原载荷单独查询 |
| daemon 签名且绑定有效 | outcome=not_sent，原子准入前队列/槽拒绝且记录未受理 | not_sent，可退避；自动重试仍用原 ID | unknown，原 ID 查询 |
| 伪称 daemon 或无有效绑定 | 缺类别、未知类别、内外不一致、伪签名或拒绝了未经认证的 ID | unknown | unknown |

Router 合并全部路径/尝试：有仍可能返回可信结果的路径就继续等，所有路径失败或到期才向等待者呈现 unknown；主动停止等待不是终局。not_sent 只在**每一尝试**均确定未准入时成立；rejected 只限相同不可变载荷的终局拒绝。server 的未转发标志仅是诚实 relay 对本次调度的说明，不是密码学上的 daemon 执行证明，不能推翻 daemon 或其他路径证据。重试保留原 ID/载荷/期限，不把任何服务器状态用作自动换 ID 的授权。独立取消结果任务的测试结论由负责人整合；本表先规定目标，不声明现代码已满足。

### 接受记录、保留与时序

受理记录增加经解包后的原始业务载荷与不可变 scope/原截止时间的域隔离 SHA-256，排除加密随机数和密钥 epoch；相同授权重试必须保留首次序列化的原始字节，不能把 protobuf 解码成对象再编码（map 的顺序可能改变）；换密钥只重新加密这些字节，不改变 hash。客户端保存待发送请求时，一并密封保存原字节、scope、ID 和原 deadline，并在可能发出前完成保存；损坏或旧格式的待发送记录不靠重新序列化恢复。同 key 先认证再比较 hash，不同则 conflict，不能返回另一载荷的缓存正文。`accepted` 表增加 `payload_hash`、`retain_until`、显式状态/类别，逐条校验和覆盖新字段，并保留接受记录时间水位；不再用 Done/Failed 两位推导结论。本期不做全表 manifest；逐条校验和与时间水位不能检测整行删除或整库回滚，额外完整性证明列在 [plan](plan.md#传输模块与直连) 的待办。

**采用独立精简条目预算**，保证截止时间后固定 **10 分钟**可查询终局类别，全文结果仅尽力保留：

- 全文缓存仍为 8,192 条 / 16 MiB，计入 key、正文和开销。平均每条仅 2,048 字节；单个近 768 KiB 结果加开销只能存约 21 条，不能靠此预算保证全量宽限期。正文被驱逐时标 `result_available=false`，不能编造业务返回值。
- 独立持久精简表为 **65,536 条 / 16 MiB**，每条按 **256 字节**计费，包含域隔离 key hash 32、载荷 hash 32、deadline/retain_until 共 16、状态/类别/原因及完成标志 16、校验和 32，另预留索引/条目逻辑开销 128。合计 256；不存长 key 或正文。SQLite 页/WAL 的物理空间另行监控，16 MiB 是逻辑准入预算，不能宣称数据库文件恰为 16 MiB。
- 准入原子预留精简条目和运行记录；全文预算不足可不留正文，精简预算不足不执行，只回原 ID 可重试的 unknown/背压，不能提前驱逐未到 retain_until 的条目。请求最远截止为现在 +10 分钟，故最长驻留 20 分钟；65,536 / 1,200 ≈ **54.6 次/秒**是最坏截止分布下的持续受理容量，突发受条数限制，满即背压。不是无界吞吐承诺。
- `retain_until = 原 deadline + 600 秒`，到点前精简条目与防重复身份不能删除。到点后无论过去是否完成，缺记录的过期 ID 对外**永久 unknown，永不重新执行**；记录仍在时可返回已有证据。不能延长旧授权 deadline 来“恢复”过期操作。超过恢复目标的断网立即显式显示结果待核对；恢复后自动查原 ID，宽限内可恢复类别，正文丢失要显示结果详情不可恢复；宽限后缺记录标结果无法确定，供用户查会话事件并自行决定新操作，不能自动重做。

| 时序/状态 | 唯一处理 |
|---|---|
| 同 ID 在途；等待表未满/满 | 有运行者则有界等待；表满立即 unknown/busy，不占执行槽，不取消原执行 |
| 已完成，截止后、retain_until 前查询 | 认证后先查记录，再判新执行期限；返回原类别及仍可得的正文 |
| 过期且缺记录 / 无效信封 | 必须拆分内部错误：前者 unknown 且不执行，后者仅按可认证的单调拒绝规则处理；不能都映射 ErrInvalid/rejected |
| 运行中越过保留期、持久条目被剪枝 | 独立进程运行表仍阻止第二次执行；结束后不重建过期条目，对外 unknown，释放运行登记和槽 |
| 回调结束但无可信定案，或连精简类别也无法记录 | 第三状态 `ended_unknown`，当前及后续等待者立即 unknown，不再入等待表；能持久化则记精简状态，失败时磁盘原 in-progress 仍阻止重执行 |
| 重启后残留 in-progress / ended_unknown | 无本进程运行者，立即 unknown，不进等待表、不重新执行 |
| 回调取消/超时/关闭 | 回调接口须返回 `final(outcome, result)` 或 `undetermined(reason)`，error 另作诊断；marshal 成功不代表定案。无证据返回 undetermined，不能落假终局 |
| Open 与原子 start 之间到达副本 | 共享执行闸门重新查 key/hash 并完成持久准入；只一个回调启动，执行期间不持全局锁 |

受理后的状态代数为 `running → final(executed/business conflict) 或 ended_unknown`，运行者只在内存登记；身份 conflict 不改变此状态。全文超限不必降为 unknown，只要精简终局类别可保存。终局证据只在原子保存成功后回复为可恢复结果；正文写失败但精简终局提交成功可回复该类别及详情不可恢复。精简提交也失败、或保留期已过，即使本次回调算出了结果仍统一回复 unknown，避免首份与后续查询结论分叉。

此存储变化把 daemon 安全状态代数从 **3 升到 4**（`storageVersion` 和 `security-v4.db`，相关初始化/配对路径一起改）。产品不自动迁移、不并行打开旧 endpoint、不猜旧记录 hash。升级先停止旧 daemon 并核对在途操作，通过仓库内一次性手动工具备份、校验并保留原配对身份、成员链、密钥和租约；旧接受记录没有载荷 hash，不迁移，旧授权结果查询保持 unknown。工具在使用截止时间前逐条验证原 v3 接受记录的形状和 checksum；任一记录不符即拒绝，dry-run 也给出明确错误，不跳过损坏行。全部记录通过后要求旧截止时间全部已过，并保留持久时间下界，避免旧请求在时钟回拨后重新准入。具体操作与回退限制见[开发说明](development.md#安全状态-v3-到-v4-的一次性手动升级)。四端协议统一为 15，必须一起更新。自动更新策略的 state-generation 为 4；旧 daemon 的自动更新器遇到状态代数变化会停下，手动迁移工具成功后才写入新标记，不能仅用协议升级替代这一屏障。升级要求须写入发布说明。

终局类别与正文可用性分别传达：正文不可恢复时保留已确定类别，客户端刷新最新状态；身份冲突不返回原记录正文、不覆盖原记录，也不能当作原提交未执行而换 ID 自动重发。

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

第 1 期启用 bulk 时，执行槽按 daemon 全路径合计 8 个划分（第 0b 期维持中继已有并发行为）：交互/Interrupt/审批 2 个，普通指令与非 bulk 只读 4 个，bulk 2 个。**三池互不借用**，普通/bulk 不能占交互预留；交互按会话轮转、每会话最多 1 槽，machine 级交互以发送 peer 作为公平性 key，不能用多个 request ID 绕过。

读循环只做有界外层解析与分类入队，不同步等待解密 worker、执行槽、成员网络拉取、fsync 或后端；命令种类在密文内，由共享安全入口解密后分类，不信任 server 标签。认证工作用独立有界队列（64 帧/4 MiB，单 peer 至多 8 帧/1 MiB）与 2 个 worker；worker 验签解密后分类，未知 epoch 交独立同步等待队列（每 peer 1 个、全局 8 个、5 秒期限），不占认证 worker。完成后分别进入交互 32 条/256 KiB、普通 64 条/2 MiB、bulk 16 条/2 MiB 队列，每 peer 公平调度并限制为各队列的 1/4；整帧仍受 1 MiB 上限。执行消费者先查同 ID 协调状态，重复进等待表；新请求**先取槽，再在执行闸门内复查并持久受理**。队列满或排队 100 ms 仍无槽，原子确认尚未准入后回复该次 not_sent/busy，Router 按证据矩阵合并并退避原 ID；不留 in-progress。认证队列堵塞只拒当前帧，不阻塞读取审批、心跳或结果。

交互最长占槽 **5 秒**，普通 **60 秒**，bulk **10 秒**，且不超过原 deadline；输入/文件长任务必须拆成有界后端调用。实现须让具体后端 I/O 遵守期限并确认回调退出，不能仅取消 ctx 就假释放仍运行的槽；违反期限的回调标 unknown、隔离该资源并停止向它准入，仍计原槽，禁止新 goroutine 绕过 8 槽。此情形验收失败，不能声称仍满足调度上界。其他会话的队列到期照常返回，不能无限等待。审批从已认证入队到获槽或明确背压的 **p99 ≤100 ms**，同时报告实际获槽/完成比例，不能以全拒绝冒充及时审批；健康有界后端下无洪泛的不同会话审批须成功完成。

两路重复只占原执行的一个槽；等待、ACK、信令、成员同步不占槽，另有 CPU/队列限额。磁盘 bulk 不在共享调度锁内等 fsync；uploads 全局锁公平性必须验证。

`GetSharedFileChunk` 明确为只读类：验签、当前成员/租约和共享对象授权仍执行，**结果不进接受日志、不落盘**，允许重新求值，进入 bulk 池；上传 Put 依靠 durable offset 幂等。下载对象、固定副本与生命周期由 [session-files.md](session-files.md) 负责；当前 checkout 尚无该接口，须整合 agent 发文件任务的只读实现，不另定一套路径/版本规则。server `GetImage` 显示副本仍走原服务。

初始资源预算：daemon 最多 8 个客户端 peer，每 peer 最多交互/bulk 两个 PC，客户端最多连接 4 台 daemon；同代次替换短暂并存也计入上限，超限保留 relay。每客户端最多 8 条逻辑终端、每 daemon 合计 16 条。每 peer 发送与重组各最多 2 MiB / 128 帧，daemon 全局各 16 MiB；单密文重组上限 1 MiB、期限 5 秒。候选、信令、重连另见第 4/5 节。daemon 自己实施数量、帧率和字节限额，不依赖 server 的每设备 16 条 WS 或账户 streams 额度替直连把关；这些本地上限不声称实现账户全局计费限额。多客户端与多 daemon 验收必须同时施压。

## 3. 选路、指令与终端

### 升级与热备

1. 认证 relay 可用即交付；后台有界探测 direct，失败退避，网络代次变化重新探测。
2. 同用途对照取至少 5 秒、至少 100 个交互样本。direct 交互 p95 不超过 relay `max(5 ms, 10%)` 的容差且队列不增长才允许升级；bulk 用同文件至少 4 MiB、两轮吞吐均达到 relay 的 95% 才升级，不能用 RTT 替代吞吐。样本不足保持 relay。
3. direct 有效时交互指令热备：**只封一次，两路发送完全相同的密文字节**，取先到可信结果。重封只用于依法允许的密钥更新，业务身份不变；不按 Path 分配序号。文件正文不双发。
4. 最近两个 5 秒窗中 direct 交互 p95 超过 relay `max(10 ms, 20%)`，或 bulk 吞吐低于 relay 90%，该用途退 relay；硬故障或软期限立即启用备用，不等 ICE `failed`。重新升级至少等 5 秒稳定且重新满足第 2 项，避免振荡。上述数值是初始策略，测量成本与能耗见计划。

### 指令与结果

同一副作用操作只准备和授权一次；双发、换路沿用原 request ID、授权载荷和截止时间。入口按 ID 合并执行及等待者，第 1 节的五类结果适用于每路及合并结果。只读可重复求值，不能充作副作用幂等证据。持久记录只有开始时继续 unknown；不因连接换代重建业务 ID，不把取消 HTTP 当取消 CLI。

接收端可在验签前合并相同密文哈希的在途校验，并丢弃已经验证过的字节级副本（每 peer 最多 4,096 个 SHA-256，保留 10 秒）；未验证帧不能进入“已验证”缓存。哈希命中不推进业务/ACK 水位；缓存被驱逐后仍须完整验签并由逻辑去重保护。不同密文即使声称同 ID 也必须验签，冲突载荷返回 `conflict/identity`，不覆盖原受理结果。这是省重复验签开销，不是身份验证的替代。

### 终端连续性

一个逻辑流只拥有一个 tmux control 连接。两方向各有路径无关的序号分配器、连续应用 ACK、失败水位和有界 replay；第一个业务序号为 1，0 表示尚未应用。范围/签名/当前成员/租约验证后，**先按流、方向、序号重组，再对下一个连续可执行帧调用 Accept**。不能把第 7 帧先交 Accept 执行再等第 6 帧。已应用水位、冲突摘要、失败水位独立于接受日志保留到流结束，不能随 60 秒信封过期而丢失。

缺口缓冲最多 128 帧 / 1 MiB、10 秒；只有下一个连续序号可应用。流内旧序号只确认、冲突拒绝，四端现有 `seq <= last` 即关流必须改造：Go `internal/relay/terminal.go:readTerminal`、网页 `remote.ts:openTerminal`、Android `Remote.kt:openTerminal`、iOS `RACore/RemoteTerminal.swift:openTerminal`。验证过期帧不能绕过截止时间；缺口等到帧过期就进入网络恢复，不能永远等或跳号。发送时的 60 秒截止时间也包含本地排队和补发，不延长已授权帧。

输入帧是**顺序处理单元，不是可回滚事务**：Input 每 256 字节一条 tmux send-keys，可能只注入前半帧。仅该帧全部 tmux 命令明确完成才给成功 ACK，写 socket 不算完成。**fail-stop 只由 daemon 侧 tmux Input 注入失败、超时或结果未知触发**；记录失败序号/已知或可能前缀并立即认证通知，其后依赖输入全部停止，另一条路也不能补打失败帧。Resize、History 等非输入失败不触发 fail-stop：记录该帧失败类别并推进连续“已处理”水位，后续帧继续；查询时必须能分清成功应用与已处理失败。

ACK 携带最高连续已处理序号及输入成功水位/失败状态：不能把越过失败帧的处理水位解释为所有输入成功。Input 失败也推进已处理水位、固定失败水位，后续输入只记为 blocked、不注入；认证失败/缺口不推进任何水位。输出 ACK 在渲染消费完成推进；输入成功水位在 tmux 全帧完成推进。每 20 ms 或累计 16 帧发 ACK，失败立即发送，不逐传输片确认冒充应用成功。

每方向输出 replay 最多 2 MiB / 512 帧 / 30 秒，越界只表示不能补全输出，**不等于 Input fail-stop**。输入每流缓存 64 KiB / **30 秒（direct 10 秒恢复目标的 3 倍）**；重发仍遵守原 60 秒信封 deadline。活流输入的成功/失败水位、每个未确认帧的摘要及结果保留至流结束；旧流重建后再保留 30 秒查询期，每 daemon 最多 16 个活流 +16 个退休查询条目，各最多 128 条/64 KiB 明细，超额暂停新输入/建流，不能静默丢仍待查询的水位。输出与输入预算独立，大输出不能驱逐输入依据。

新增路径交换认证的流 ID、双方进程实例、路径代次、双向水位与失败信息；barrier 最多等 5 秒，超时只放弃新腿。中继腿恢复用 `AttachPath` 绑定原 stream ID、sender、session、新腿 ID/代次和水位，server 按新腿 ID 配对；daemon 查注册表挂载，**不调用 OpenTerminal、不新开 tmux**。旧回调不能关闭新 Path。

路径全断、缺口过期或输出 replay 越界是**网络恢复**：daemon 实例未变时，先用独立认证 `InputStatus` 查询旧逻辑流的输入水位/失败明细。成功的输入不重发，明确尚未应用且原授权有效的输入在旧流按原序补发；daemon 明确拒绝的过期未应用帧可结束旧流，不把后续输入无声跳接到新流。若仅输出无法补齐，核清输入并以 barrier 停止旧流准入、取得最终水位后，才用全新 stream ID + OpenTerminal +快照重建；明确未应用且仍有效的待发输入可在新流重新授权续接，绝不重放已应用/部分失败输入。水位查询与关闭 barrier 必须串在旧流执行序上，不能以仍可能前进的快照作为重发依据。

daemon 实例变化/重启、旧水位已不可得时不作 Input fail-stop 归因，但暂停自动输入，必须用户确认后新流+快照；已有 daemon 注入 fail-stop 同样需确认重建。**确认**表示用户已核对 CLI 画面及按序列出的成功、部分失败、未知和尚未发送内容，并决定放弃旧队列后继续新输入；不是证明旧输入未执行，也不是授权一键重打未知内容。未知/部分失败内容保持可核对，不标送达、不默认选中重放；用户若决定重发须作为独立新输入。仅尚未发送的内容可恢复为可编辑草稿。快照只恢复画面，不能证明按键次数。

终端输入、resize、ACK/barrier/失败控制帧总是热备。输出密文帧 ≤4 KiB 且每流热备副本不超过 32 KiB/s（突发 64 KiB）时封一次两路发；超阈值输出按序只走当前优选路径，保留另一腿以便补发。大输出也不进入文件 bulk PC，仍受终端队列及 replay 预算约束，不能抢审批槽；其切路恢复受探测及 replay 时间限制，不承诺一个包间隔。业务按相同逻辑水位确认两路，不生成 ACK 回环。

物理路径丢失不直接调用产品 onClose，不清屏、不失焦、不重新抢尺寸。真正重建保留旧画面直到新快照可用，并单独处理未知输入。进程崩溃不能承诺任意 CLI 的 exactly-once。

### 分块续传

bulk 软期限从 `max(500 ms, 4×近期 RTT + 调度余量)` 起，交互/切换窗口探测可每 50 ms，空闲降频，后台停止。超预算启动备用续传，不判电脑离线；250 ms 仅为受控低延时配置，[有限窗口原型](transport-notes.md#3-网络切换与热备)不能证明真实网络零误切。

上传换路先查 Status，只认 durable `persisted_bytes`，按同一 transfer ID、offset、字节及哈希重发。**原块请求尚有效时沿用原 request ID/截止时间；块已过期时允许新 request ID 和新期限重发相同 offset**，安全来自上传的归属、重叠字节比较和持久 offset 幂等，而非重复执行 CLI。这个例外不适用于会话创建、消息、审批等副作用指令。下载在固定共享副本内按已经校验并持久保存的连续 offset 恢复，允许只读块用新 ID 重新求值；副本删除/过期按文件设计失败，不悄悄切换文件。进度不归零，未确认尾部不算完成。

## 4. 网络切换与第 0 期中继修复

### 可测的恢复承诺

以下都是**待产品验收的目标**，前提为客户端在前台可调度、daemon 进程可调度、授权有效；不将系统无网或挂起时间算入可用网络上的恢复预算：

| 情形 | 目标与测量起点 |
|---|---|
| 单路故障，备用路仍通 | 热备范围内的交互流量，故障后最大应用间隔相对健康对照增加不超过一个正常包间隔（20 ms 发包场景为增加 ≤20 ms）；记录两路 RTT，该目标仍须在非对称 RTT 下验收：备用路的传播时延可能使其不达标，需如实报告并由计划中的目标校准决定后续处理 |
| 本机 Wi-Fi↔蜂窝等，两条路同时失效 | 从新网络可用的通知/首次可验证探测起，relay 达认证 Ready 且一次真实 Link 调用可用目标 ≤3 秒，direct 恢复身份绑定与业务可用目标 ≤10 秒；不把只收到 Ready 当作业务恢复 |
| 无系统 hint 的静默黑洞 | 前台活性预算目标 ≤5 秒判可疑，再按 relay ≤3 秒 / direct ≤10 秒恢复，分别记录发现与恢复；不能用 hint 场景掩盖静默故障 |

双路断开目标以[旧中继 RST 约 1.18 秒到 Ready](transport-notes.md#1-既有中继与网络失效)为有限依据，为重拨与认证留到 3 秒，另给信令/ICE 到 10 秒；该单次本地测量不是移动网络证明。超 RTT 条件、大输出、重放越界分别报告偏离，不能改写成已经达到目标。

预算内不因暂时断路弹操作错误、不清屏，界面可显示恢复状态；输入每流有界缓存 64 KiB / 30 秒，不显示虚假已送达，超限停止接收并明确要求处理，不能静默丢输入。恢复先按第 3 节查询旧流输入水位，网络型不确定自动续接，水位不可得才要求确认；副作用结果按原 ID 核对。超恢复目标需给可证实状态，不能永远隐藏失败。

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

后台不以伪音频/VoIP 或无限 ping 保活，暂停活性累计。回前台/休眠唤醒先 Touch、刷新成员/租约，核对单调时钟与路径代次，再恢复 relay、结果/offset 及逻辑流；不能直接用睡眠前的 socket。daemon 重启丢弃 PC、内存 replay 和旧进程实例，副作用只查持久结果，终端必须新流+快照，未知输入不重打；唤醒越过租约先停收发；仅 replay 越界仍先查询旧输入水位，不把网络恢复变成注入 fail-stop。客户端同样遵循平台进程回收边界。

## 5. 协议与安全绑定

保留现有业务信封加密、签名、AAD 和防重放；DTLS 不替代设备授权。绑定还能防错误设备连接、IP 暴露和 DoS，但不能消除 relay 已知的交互元数据，热备仍有中继小流量。

### 第 1 期验证后冻结的 wire 契约

第 0a 的结果格式是最终格式并单独协调协议升级；**第 1 期第一步先用 Go Pion、浏览器（含 Safari）、Android 和 iOS 实际 SDK 验证信令、SDP 原串、候选合并及角色状态机，再冻结下列新增格式**，不重定义结果。终端重组、ACK、InputStatus、replay、barrier/AttachPath 先用两条可注入故障的假路径跑完整状态机和四端向量，第 3 期才接真实热备；第 2/3 期仅启用已定义能力。字段编号和编码由实现时 proto/安全编码及共用向量负责，本表规定不能拖到后期的语义。

| 格式 | 第 1 期必须覆盖的语义 |
|---|---|
| 外层定向帧 | ToDevice、FromDevice（server 从登录设备写入）、Signal/Control 类别、发送者本地 frame ID、有界密文字节及 PeerFrameResult；无历史序号、无落库 ACK，离线立即失败 |
| 身份与代次 | 固定域/账户、双方设备和角色、machine、协议、成员 epoch+head、双方进程随机实例、随机 connection ID、PC 用途、generation、双方 boot challenge、信令方向与单调序号、短期限 |
| 分片 | 消息 ID、片号/总片数、完整密文长度与 hash、路径代次；重组后才验业务信封，片级收到不算业务 ACK |
| 逻辑流 | stream ID、方向、路径无关逻辑 seq、原期限；流 ID 永不复用；路径 generation 不重置逻辑 seq |
| 结果 | 原样复用第 0a 的五类结果、证据矩阵与 hash 绑定，不新增另一套结果报文 |
| ACK / replay / barrier | 双向最高连续已处理 seq、输入成功水位、失败 seq/状态和旧流 InputStatus、重放起点与窗口、barrier ID/水位、barrier 确认；ACK 不按片、不越过缺口 |
| 新 Purpose 与控制体 | 设备对信令 `PeerSignal`；传输控制 `TransportControl`，显式区分 Capabilities、AttachPath、InputStatus、Ack、Replay、Barrier、StreamFailed、ClosePath、DirectPolicy；控制序号独立于业务与接受日志。现有 Command/Result/终端 Purpose 仍按用途校验 |
| 信令类型 | Probe/Challenge、Offer/Answer、Candidate/EndCandidates、Confirm、Restart、Close；每种状态及允许角色/序号有固定转换，禁止将任意 Command 混入信令 |

业务信封的 Header.Seq 是逻辑序号，不绑定某一条路径代次；分片外层的路径代次只用于该路重组和旧回调隔离，不能授权、推进逻辑水位或改变两路相同的业务密文。控制体将需要认证的路径代次显式签入；TransportControl 不走业务接受日志，所有 Control 都签入双方当前进程实例，ACK 等再依赖逻辑水位防重放，策略更新另依赖原子持久化 revision 幂等。daemon 重启时旧实例的任何 Control 均拒绝，即使内存水位已丢；引导 Probe/Challenge 是唯一可在实例未知时使用的类型，不能执行控制动作。

未启用能力收到相应控制体时返回认证的 unsupported，不执行；不能以忽略未知控制体代替解析。四处协议号（Go `internal/protocol`、网页 `web/src/protocol.ts`、Android `core/Version.kt`、iOS `RACore/Version.swift`）同值；具体号随实现基线确定，本次文档不改。控制/信令使用固定 type ID + 有界 payload 的可扩展编码，未知 type ID 可被认证后明确回复 unsupported；在此编码中，新增 TransportControl 变体或信令类型，且旧端按固定编码返回已认证 `unsupported`、发起方保留 relay 而不改变既有操作语义，属于同一协议内扩展，不升协议；这不是旧协议降级。改变已有字段/类别/授权含义、未知类型处理或使旧端误解的行为，仍按 [architecture §4](architecture.md#4-协议设计要点) 升级，不写旧协议降级。可关闭直连只是**同一协议内运行时开关**，不能回退上一期二进制；升级/重建要求必须写发布说明。

### 非持久双向信令

选**新增定向 server 帧**，不采用 Forward 长轮询应答：双方主 WS 均可发送 `DirectedPeerFrame`，server 校验登录设备、同账户目标及 Client↔Machine 角色，向指定设备的主 WS 投递 `PeerFrameDelivery`；daemon 可主动发送 answer、追加候选和 ICE restart，不需等客户端轮询。外层 sender 不取客户端自报值，接收端解密后还核对签名身份和内层目标；server 只见路由标识、长度和时序。同一种定向帧另设 Control 类承载加密 TransportControl，让能力握手、AttachPath、远程策略及确认也有双向 relay 载体；已有终端腿及 direct channel 可直接承载控制密文。Signal/Control 分类不暴露业务内容，不能据外层类别免去内层 Purpose 校验。离线不排队、不补投、不广播、不写数据库或持久日志；对端断线就失败，重连重新协商。

定向投递增加 `PeerFrameResult(frame_id, delivered|offline|limited, retry_after)`，返回发送者当前主 WS，绑定其本地随机 frame ID；delivered 只表示已写入目标 WS，不表示对端已认证/处理。队列驻留超时返回 limited，目标先断开返回 offline；成功回执丢失可重投同 ID。同一（发送者、目标、类别）队列 FIFO，Signal 与 Control 之间不保序，内层序号/状态机负责顺序；接收方重复确认而不重复调用 SDK。server 为回执另留每 peer 512 帧/256 KiB（计入 Control 全局预算），丢回执走相同重试，不从 delivered 推断业务执行。

Signal 与 Control **各自使用独立于现有 `peer.out` 的有界队列及字节记账**，共用 socket writer 但主推送优先、各队列公平轮转。超额仅丢当前定向帧并回 limited，绝不因信令洪泛调用现有 sendFrame 的慢接收方断连分支；socket 本身故障仍正常断连。Signal 单帧 ≤64 KiB，每设备 **256 帧/s、突发 256，4 MiB/s、突发 4 MiB**；账户 **1,024 帧/s、突发 1,024，16 MiB/s、突发 16 MiB**。每目标 Signal 队列 256 帧/4 MiB、驻留 ≤1 秒。Control 每设备 512 帧/s（突发 1,024）、512 KiB/s，账户 2,048 帧/s、4 MiB/s，单帧 ≤16 KiB、每目标 512 帧/1 MiB、驻留 ≤1 秒。server 全局定向待发送 **32 MiB，其中 Signal 至多 24 MiB、Control 保留 8 MiB**，不挤占主推送预算。daemon 自身同等或更严限额，不能依赖中继守规矩；各入口认证 CPU 另限额。

发送端按相同 token bucket 跨 PC 公平节流，优先握手/Confirm，再候选。候选每 50 ms 汇集，攒满 16 个才发整批，收集结束时发尾批（不能每个 tick 发不足一批导致超过 4 批）；每帧至多 16 个、每个编码 ≤1 KiB；每 PC 每代次最多 64 个，最多 4 批，超量结束本次协商保留 relay，不用每个候选单独抢帧。每个需确认的信令以认证的对端序号 ACK 为成功依据（ACK 走独立 Control 预算，不再反向请求 ACK）；PeerFrameResult 只能加速诊断。未确认在 **500 ms、再 1 秒**重发相同序号/密文/本地 frame ID，至多重试 2 次（总 3 次）；limited 尊重 retry_after 且仍计重试/总期限，offline 暂停到对端发现更新。从本次网络可用/重启恢复起总期限 **10 秒**（含 relay 重建），超时只退 relay，后续按第 4 节退避重开 connection/generation，不无限重发或无限生成新代次。

预算按 daemon 重启后 **8 客户端 ×2 PC=16 PC**并发重建核算，不假定“约 6 个候选”：每发送方向每 PC 最多 1 SDP +4 候选批 +End +Confirm=7 帧；112 帧加设备对引导/确认预算 16 帧，共 **128 帧、≤3 MiB**。三份含重试至多 **384 帧、9 MiB**；至多 384 个序号 ACK（每个 ≤1 KiB，共 ≤384 KiB）另计 Control，落在其 1,024 帧突发/512 KiB/s 内；突发后帧调度 ≤(384−256)/256=0.5 秒，字节调度 ≤(9−4)/4=1.25 秒，取较大 1.25 秒，加重试定时 1.5 秒、relay 重建/认证 3 秒、实例发现/引导 1 秒、ICE/DTLS 3 秒，共 **9.75 秒 ≤10 秒**。客户端发向一个 daemon 的汇总预算也须满足账户限制；八客户端总初发不超过同一组 128 帧，不能把每客户端预算乘八漏算。该算术以授权仍有效、无额外成员追赶、公平调度、WS 在 3 秒内恢复且 RTT/SDK 在分项预算内且最多两次丢失为前提；第三次仍丢、洪泛限流或 SDK 较慢如实记不达标，不能把容量算术当成功率实测。第 1 期用四端 SDK 测初发/重试实际帧数与耗时后冻结，超 3 MiB/128 帧必须先改预算并复验。

### 设备对加密、指纹与防重放

含 IP 和 ICE 凭据的 SDP/候选用**双方成员状态中已认证 X25519 密钥**导出设备对密钥，经 HKDF 的固定信令域、账户、排序设备 ID、双方进程实例和 connection ID 隔离，使用 AEAD 并由发送设备签名；不用账户内共享的内容密钥。Probe/Challenge 也设备对加密并签名，可用不含 SDP 的引导域交换 boot nonce；正式代次密钥只在双方 nonce 都已认证后导出。不能从 server 普通 meta 接收密钥。实现须提供四端加密/签名/编码一致向量，不自创另一套成员身份系统。

每次进程启动随机生成 boot nonce，先 Probe/Challenge，Offer 必须绑定接收方当前 nonce；老 Offer 在接收方重启、内存缓存丢失后仍不能重新建连。daemon 主 WS 重连后发送签名的实例公告，客户端见新实例立即废弃旧 PC；主 WS 尚不可用而 direct 活着时，Link 的实例探测间隔不超过 1 秒（不复用后台停止的性能探测）。收到绑定旧 nonce/实例的 Offer 不静默丢弃：返回设备对认证的当前 Challenge 与 stale_instance，限每 peer 每秒 1 次，不创建 PC；发起方换新 connection 后重走握手。该发现预算计入上述 10 秒。Probe 重放最多触发受限新 challenge，不能创建 PC 或送 SDP。缓存 key 覆盖双方设备、实例、connection、generation、方向和序号，保存到信令截止时间 **加当前 `deviceauth.MaxClockSkew`**（当前代码为 5 分钟，不是 10 分钟）；信令期限最多 30 秒。每 peer 最多 2,048 条、全局 16,384 条，满时拒绝新协商，不能提前驱逐仍有效的防重放项。关闭连接仍保留 tombstone 到窗口结束；持久成员时间 floor 防时钟回退，回退异常暂停协商。

每方向每 generation 序号从 1 单调增加，Offer/Answer/Candidate/Confirm 都计数；完全相同重发仅返回已有确认，不重调 SDK，同序号不同 hash 拒绝。乱序最多缓冲 32 条 / 1 秒，缺口超时重开 generation，不能让候选越过未验证的 SDP。ICE restart 增 generation、换 ICE 凭据，connection 与对端身份不变；进程实例变化必须新 connection。glare 采用固定规则：按 device ID 字节序较小的一方作 Offerer，另一方只发 Restart 请求；冲突 offer 拒绝并保持确定角色，不同时安装两份 SDP。

双方必须先同步到**同一已验证成员 epoch+head**才协商。收到未知的较新 epoch 可有界拉取并验证链后重启协商；旧 epoch 不借历史成员身份继续连接，也不按发送方旧 head 放行。同步不成功就保留 relay；任何 Install 改 head 后重新校验会话，并重建受影响的信令代次/指纹绑定，不允许旧 head 无限续命。

下列 SDP 安全约束须先在第 1 期用四端真实 SDK 原串验证可实现，再冻结具体语法与共享正负向量；未通过不得宣称该端可直连。SDP 以严格 UTF-8、CRLF 行结束解析，拒绝 NUL、重复/歧义属性和未知媒体；只允许一个 `m=application` SCTP section、无音视频、无 `ice-lite`，恰好一个有效的 sha-256 指纹值，session 继承与每个 m-section 的声明必须一致，其他算法、冲突或同 scope 重复指纹均拒绝。不通过字符串裁剪“修好”SDP；验证的 hash 覆盖**完整原串**，交 SDK 的必须就是已验证的原串，不能重序列化。验证 candidate 绑定当前 ICE ufrag/代次，每 PC 每代次最多 64 个候选，按第 5 节批量节流；禁止凭候选扫描额外端口。

每 PC 只允许预定义的业务 channel 和探测 channel（固定 negotiated ID 0/1），无额外 DCEP 通道、媒体或未声明通道；探测不承载业务。先验签、设备/角色、目标、head、租约、nonce、序号、期限，再交 SDK。DTLS 必须核验认证指纹；双方 Confirm 签名绑定两份 SDP hash、指纹、nonce、connection、generation 和用途后才开放业务。重放、拼接、伪指纹、ICE 凭据替换均拒绝，不关闭 SDK 指纹校验。

### 撤销、寿命与关闭直连

成员同步是独立进程级循环（客户端在前台可调度时运行，daemon 只要求进程可调度，不称“前台”），由安全组件用本机账户设备签名登录，独立持有 token 与 HTTP client。token 过期/Unauthenticated 时丢弃旧 token，重新 challenge/login 并原子替换，合并并发续期；凭据未恢复不能声称同步成功，PermissionDenied/成员失效立即暂停新准入及相应流。主 WS 断开但 direct 活着时仍通过独立 HTTP client 同步，不等待 WS session 重建或取消。

**成员同步与 ensurePublished 拆成独立循环和队列**，不共用可跨网络 I/O 持有的锁或 RPC 槽；发布步骤慢、失败或 60 秒超时不能推迟安全定时器。每次 Install、InstallLease、Touch 后（即便报错）触发所有 direct 会话重新校验双方成员/角色/密钥/head 与有效租约；本地安装与收发闸门原子生效，关闭资源异步完成。仅收输出也检查，每帧（含输出/控制）核对 Sender，失效立即停新帧、停止输出并关闭 PC。

安全同步按**固定起点每 15 秒**尝试，整轮含登录/全部分页/租约安装限 **5 秒**，不在上轮完成后再睡 15 秒。已验证分页可原子 Install 并立即撤销对应 peer；5 秒到仍未追完则保留已安装部分、记录未完成，**暂停 direct 收发和新准入**，下一轮从当前已验证 head 继续，不将部分追赶标作成功/续寿命。此时不承诺撤销全量已同步，只承诺不再用未完成同步维持 direct；relay 控制也继续执行自身成员/租约检查。

PC 寿命不超过租约 NotAfter，定时器粒度最多 1 秒，不因无流量延长；续租重新校验才延长，设备密钥变化重建。诚实可用 server 新状态可读起，整轮能在期限内完成时，**15+5+1=21 秒内关闭失效 peer；本机已 Install 起 ≤1 秒；NotAfter 起 ≤1 秒停止收发**。同步超时（含多页）也在该轮到期后 ≤1 秒暂停 direct，不能持续输出等下次成功；server 不可达/扣留撤销时不声称新状态已读到或全局 ≤21 秒，只受失同步暂停与现有租约边界保护（租约最长 72 小时），不能把旧 head 当新鲜证据。发布慢不得改变这两个数。挂起不收发，唤醒先 Touch/同步和校验；时钟异常按安全 floor 暂停，不额外加时钟偏差延长 NotAfter。信任边界见 [scope §4](scope.md#4-信任边界)。

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
