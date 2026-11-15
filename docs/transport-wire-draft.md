# 传输 wire 契约（协议 16）

**已冻结（协议 16），2026-11-15，负责人裁定 7。** 字段和既有语义已冻结；文件名保留供现有链接使用，不代表产品已接入。整合时若 16 已被占用，顺延到可用协议号。冻结后同协议只能新增可认证后返回 Unsupported、保留 relay 且不改变已有语义的类型，不能修改现有语义；
既定语义以 [transport](transport.md) 为准，未决项在 [plan](plan.md#传输模块与直连)。
完整引导的成功前提已裁定为整轮合计最多丢两帧；[串行重试模型](transport-notes.md#8-完整引导链的串行重试下界)中更强的丢失组合用于检查按时退路。信令轨迹、封装一致性和终端假路径证据见[实测](transport-notes.md#9-四端信令真实封装与跨-sdk-互连)；产品适配闭环与真机性能不在本实验中宣称完成。
冻结字段在 [transport-draft.proto](../spike/transport-1a/wire/transport-draft.proto)，不参与现有生成流程。

## 格式边界

DirectedPeerFrame 只提交目标、类别、本地随机 frame ID 和密文；PeerFrameDelivery 的来源由登录设备写入，
接收端仍验证内层身份、角色、账户和目标。Result 绑定当前主 WS 与 frame ID，delivered 仅表示写入目标 WS。
同 ID 的透明重投必须保持全部字节；断线不落库、不补投。Signal/Control 各自 FIFO，跨类别不保序。
外层分片是未认证资源声明，只决定有界重组；每片入缓存前，核对该消息已保留字节加本片长度不超过声明总长，且 peer 实际保留字节不超预算。相同分片重复不重复计费，完成/到期按实际保留量释放。不得推进业务水位，不能改变被双发的信封字节。

每条逻辑流有独立双向序号，首帧 1，0 为尚未处理。路径代次仅控制腿与分片，不能重置逻辑 seq。
Ack 同时表达 processed、input_success 和输入失败状态；非输入失败可以越过 processed，不能暗示输入成功。
InputStatus 的每条结果必须绑定原序号和原字节 hash，部分注入以已知/可能前缀区间表达，不能只给一个布尔值。
查询/关闭 Barrier 进入旧流执行序；BarrierReply 的 final 标志只在停止旧流新准入后成立。
AttachPath 按原 stream、sender、session、双方进程实例、腿 ID/代次及双向水位挂载，不调用 OpenTerminal。
InputStatus 的 unavailable 与 daemon 实例变化要求人工核对，不能自动重发未知输入。
输出缓存同输入侧一样，在插入前核对实际保留的完整编码字节不超过 1 MiB；重复不增量，渲染消费扣除原计费量。输出序号缺口使用独立 10 秒期限，不能复用输入计时；已收到连续前缀但尚未渲染不算网络缺口。缺口未补齐进入网络恢复，不改变 Input fail-stop。
输出待渲染帧及已渲染帧均核对原授权 hash；已渲染摘要不随输出 replay 正文驱逐。摘要已到期或不可得时，不能仅按 `seq <= rendered` 当作相同重发确认。
InputStatusReply 必须先在同一个串行查询点冻结 to_daemon 和 to_client 两方向的全部水位与明细，再把完整 FlowState 绑定到 snapshot token。每页和相应 BarrierReply 必须使用这份冻结的双向状态，不能在发下一页时读取 live rendered 水位；仅输出方向变化也必须得到不同 token。分页不能跨快照。NotApplied 是该快照中未应用的结果，只能在旧流按原授权去重续接；换新流重新授权前还必须取得停止旧流准入的最终 BarrierReply。
终端明细的 hash 沿用第 0a RequestIdentity 的原授权 hash，不能用换密钥后会变化的密文 hash；Fragment 的 hash 则是当前整密文 hash。

## 编码与认证顺序

本契约使用 `TypedPayload { uint32 type_id; bytes payload; }`，不用 protobuf oneof 承载可扩展类型。
未知 type ID 的原字节仍能完成认证，然后返回认证 Unsupported；不会执行或调用 SDK。
已知类型 payload 只允许规定字段及范围；不以默认值补缺必需字段。所有整数先检查范围/溢出，
单帧、候选、分片、缓存条数与时间上限在分配内存前核对，protobuf 解析有深度限制。
签名/hash 覆盖首次编码后的原字节，接收端不重编码后验签；重复、非最短 varint、重复单值字段与歧义编码拒绝。
未来扩展新增 type ID，不能偷偷改变已有 type 的严格 schema。

server 只应看到路由标识、Signal/Control、frame ID、密文长度与时序。因此不能把 head、boot nonce、
SDP hash 或正式代次密钥所需的全部 context 明文放在转发层。实验已实现的安全编码候选为两层封装：

1. 从当前同一已验证成员 epoch+head 中取得双方 X25519/Ed25519 公钥，拒绝普通 machine meta 中的密钥。
   X25519 shared secret 经 HKDF-SHA256 导出设备对 context 包装密钥。info 采用长度前缀编码的固定域
   `remote-agent/peer-context/v1`、账户、账户 domain、按原始字节排序的两个设备 ID、发送 ID、接收 ID。
2. Context 的原 protobuf 字节用 AES-256-GCM 加密，随机 12 字节 nonce；AAD 绑定固定域、协议、账户、
   登录来源/目标、外层类别和本地 frame ID。Context 用字段 24 `recipient_unknown` 明确表示接收方进程未知。仅 Probe 允许该值为 true，此时 receiver_instance/receiver_boot_nonce 必须为空；false 时两字段须为完整已知值。其他任何消息带 true 一律拒绝，不从空字段猜状态。Context 内含双方角色/实例/boot nonce、connection、用途、
   generation、head、信令/控制方向与 seq、原签发时间和短 deadline。除显式 recipient_unknown 的 Probe 两个接收方字段为空外，正式上下文所有字段必需。
3. 正式 payload 密钥再次从同一设备对 secret 派生，HKDF info 加入双方已认证实例/boot nonce、connection、
   PC 用途、generation、方向与 PeerSignal/TransportControl 域。TypedPayload 的原字节用独立 AES-GCM nonce
   加密；AAD 加入完整加密 Context 的 hash，防止交换 context 和 body。
4. Ed25519 对固定签名域及两段密文、两个 nonce、长度和路由绑定签名。采用明确的长度前缀，
   不直接串接可变长字符串；签名覆盖精确编码，未知 type 也受保护。接收方先做总大小/来源/角色范围检查，
   验签后才解 Context，核对当前 head/租约/nonce/实例/期限/序号，再解 body、查 type；通过后才交 SDK。
5. Probe/Challenge 使用独立 bootstrap 域，只含源实例与 boot challenge、探测关联 ID 和已知的目标信息，
   不含 SDP、不创建 PC、不执行控制。双方 nonce 验证完成才允许正式密钥。Challenge 只能响应已验证的 Probe，字段 6 `probe_sha256` 绑定其首次冻结整包的 hash，并回传响应者自己的实例/nonce；发起方与仍有效的本地 Probe 原包匹配后才安装对端进程信息。成员记录只提供身份、公钥和角色，不能预置进程 nonce。Challenge 字段 5 `stale_instance` 接受布尔 true；省略与显式 false 均表示当前协议的 false，非布尔 wire type 或数值大于 1 拒绝。该标志保留给恢复处理器，不跳过身份/nonce 校验，也不直接创建 PC。
   同一 bootstrap 授权的副本不能无限生成新 PC、清空缓存或改变正式代次。收到 Probe 仅记待证明状态；它不预先知道接收方实例/nonce。Probe 的确认由关联 Challenge 完成，不再要求一个早于 Challenge、尚无法按已知进程身份验证的独立 Probe ACK。已认证、固定角色/connection/用途/代次/原串有效的首份 Offer 若绑定接收方当前 boot nonce，即作为隐式引导确认直接准入；不能因为独立 Control ACK 晚到而耗尽三份 Offer。旧 nonce 或无效 SDP 仍拒绝，Probe ACK 本身不能跳过 Challenge。
   显式 Challenge ACK 绑定发送时冻结的完整原 Context 与 hash：回显原 seq、generation、use，bootstrap generation/use 为 0；还核对相反身份方向、实例、nonce 和同一 connection，不能用接收 PC 的 use=1/2 代替原 use。Offer 证明只解除引导等待，发送窗口的序号确认仍按原冻结包匹配；副本继续走有界重试，不会反向 ACK ACK。

该双层封装是本次安全编码**候选**，四种语言的加密/验签向量已通过，尚须负责人审定与产品安全复核，不能直接宣布冻结。
它避免为了导出正式密钥把私有上下文暴露给 relay；增加的 nonce/tag/signature/长度开销须计入真实帧字节。
设备成员身份、租约、时间 floor 和原有业务信封算法保持原定义；此编码不进入业务接受日志。

具体字节算法由实验 `Wire`/`labwire` 与[固定向量](../testdata/transport/wire/vectors.json)共同约束：
`LP` 对每段加 uint32 大端长度；KDF 中整数为 uint64 大端。HKDF-SHA256 的 salt 是
`SHA256("remote-agent/peer-hkdf/v1")`，输出 32 B；context/body/bootstrap/AAD/signature 使用互异固定域。
所有 AEAD nonce 12 B、tag 16 B，frame ID 16 B，双方 instance 16 B、boot nonce 32 B；账户 domain 和 head 各 32 B。
Context.direction 保存 sender ID，正式 KDF 中同时有 from/to 与类别，不把角色混同于方向。
明文 Context 的 seq/use/generation 只在解密后可见；bootstrap generation=0，正式 generation 从 1 开始。
PCUse=0 是 peer scope，正式信令只允许 Restart/Close 使用它；控制消息可不针对 PC。

proto3 scalar/bytes 的默认值省略与显式零值/空值具有相同语义；包括 Challenge.stale_instance=false、Context 的零值作用范围/代次和 recipient_unknown=false。消息字段的存在性及非零身份、hash、目标等应用约束仍须验证。解析器可为本地校验补默认值，签名、hash 和重传必须始终使用收到或首次冻结的原始字节，禁止将补值后的内容重新编码成签名材料。
所有已知 Signal/Control 都先完整解析消息体，检查字段类型、范围、嵌套内容和相互约束，再写去重缓存、乱序缓存、推进代次或产生动作；Restart 还须核对目标用途及当前代次，Close 的 reason 省略为零但畸形正文仍拒绝。未知 type ID 认证后才返回 Unsupported。
重复字段只在 schema 声明 repeated 的位置允许（Candidate/RestartPC/FrameStatus 等）；普通字段拒绝重复和非最短整数。
CryptoKit Ed25519 可能随机化签名，跨语言必须校验相同签名输入与验签成功，不能要求所有签名器产出同一字节。
三次发送冻结首次整个 PeerFrameDelivery 字节；初次重封装测量与重试测量分开。
实验成员/进程 fixture 用公开固定种子便于复现，真实启动的 instance/boot nonce 必须随机生成并服从现有成员授权。

SDP 只允许 trickle：Offer/Answer/Restart 的任何内嵌 `a=candidate` 均拒绝；必须在收集候选前获取原串，不能裁剪 getter 输出后冒充原串。候选只经 Candidate 消息的编码大小、计数、ufrag/代次门进入 SDK。

信令 seq 在每方向、每 generation 从 1 增长，重复确认与 SDK 安装记录共用同一个有界缓存；
同 seq 异 hash 拒绝。每方向未确认窗口 32，乱序缓冲 32，每个缺口各自的首次发现时间固定 2.5 秒；补齐前一缺口不继承或刷新后一缺口的期限，口径见 [transport §5](transport.md#设备对加密指纹与防重放)。
重试保持 seq、frame ID、Context 和密文字节；只有依法新 generation/connection 才使用新身份，不能改变原恢复总期限。
防重放条目与关闭 tombstone 保留至 deadline+MaxClockSkew，不能因 PC 关闭提前驱逐。

## 类型表与大小

| 域 | 固定 ID（已冻结） | Payload 上限/补充约束 |
|---|---|---|
| PeerSignal | 1 Probe，2 Challenge，3 Offer，4 Answer，5 Candidate，6 EndCandidates，7 Confirm，8 Restart，9 Close | 完整 Signal ≤64 KiB；SDP 预留封装空间，候选每条编码 ≤1 KiB、批≤16、每代≤64/4批 |
| TransportControl | 1 Capabilities，2 AttachPath，3 InputStatus，4 Ack，5 Replay，6 Barrier，7 StreamFailed，8 ClosePath，9 DirectPolicy，10 Unsupported，11 InputStatusReply，12 BarrierReply，13 PolicyReply，14 CapabilitySnapshot | 完整 Control ≤16 KiB；Signal ACK ≤1 KiB；结果明细需分页且绑定同一查询/屏障快照，不能每页观察不同水位 |

Signal ACK 另携带被确认 generation/use；包括 bootstrap 的 generation=0，不能只靠数值 seq 关联另一条 PC 或另一代。
Ack 不请求 ACK；Unsupported/明确回复类型也不产生确认回环。回复以关联请求 seq/hash 和双方实例绑定，
重复回复只被原等待者消费。Capabilities 最长 60 秒；DirectPolicy revision 持久化并按角色区分机器/自身 peer 范围。
DirectPolicy 使用显式 `scope`（machine 或单个 peer），`policy_scope_peer` 在 machine scope 必须为空，在 peer scope 必须是该 peer ID；不能只从 revision 猜作用范围。
PolicyReply 的 `request_seq`/`request_sha256` 回显**原请求**序号和首次冻结完整定向投递包的 SHA-256，并回显 `scope`/`policy_scope_peer`。
原请求 hash 覆盖 Context、路由和原始密文/签名；重试必须保持这些字节。回复的 Context.seq 仍属于回复自身，不能替代关联字段。
接收方先认证回复，再与自己缓存的原发送记录核对上述字段、revision、身份方向和 connection；同 revision 的另一作用范围、peer 或请求不能被它确认。
PolicyReply 只证明哪个请求的结果；不替代 DirectPolicy 原有角色权限和策略持久化规则。
所有类型都不能借控制队列执行任意 Command。既有五类 CommandResult、身份/业务冲突和正文可得性原样复用。

## 已冻结协议与扩展边界

在协议 15 基线上引入这些新路由帧、Purpose 与授权语义，负责人已冻结为 **16**，四端一起更新；
真正整合时若 16 已被占用，顺延到当时可用的下一个号。本实验不改任何现有协议常量。
冻结后，新增类型且旧端认证后返回 Unsupported、发起方保留 relay、不改变原操作语义，
可在同一协议内扩展。已有字段、角色、类别、授权、ACK 水位或未知类型处理含义变化须升协议，不能添加旧协议推断分支。
本契约未涉及存储迁移；实际实施若需要数据重建或协同升级，必须写清公开升级要求。


## 缓存与延迟交付契约

issued_at 必须早于 deadline；交付截止统一为 `now < deadline`。既有 MaxClockSkew 只用于签发时钟检查，不延长 deadline；到原 deadline 即失效，重试、限流、重组、换腿或出队不延长它。
信令接收器保存冻结原包，出乱序缓存与 SDK 动作实际交付前，按当前成员/进程绑定、connection、用途/代次重新验证；
缺口期限取仍跨越缺口的首次到达时间 +2.5 秒与等待帧原 deadline、整轮截止时间的最小值。
已接受帧（包括 Close）关闭连接或退休 generation 后，只要当前租约/成员/双方实例、原 connection 和原 deadline 仍有效，必须仍能按首次冻结原包 hash 幂等确认。回执路径以保留的原 hash 为准，不能用 Closed 或当前 generation 阻断；它不重新开放 SDK 准入，不推进新代次，不确认冲突 hash、未见旧包或已失效授权。ACK/Unsupported/明确回复类型不产生 ACK 回环。
发送窗口、候选汇集和传输写队列必须携带原期限及 owner 的身份/代次绑定标记；换绑定清空旧队列，网络通知不能重置总期限。
SDK 创建描述的回调属于该 round/generation；已发出的 SDK 动作另保留原认证消息，不能靠一次到达时验签授权迟到执行。

终端输入/输出均按各缺口自身的首次发现时间计算 `min(first_seen+10s, waiting_frame.original_deadline)`。
在 Admit、输入队列出队、渲染、replay、缓存补发和状态查询回调交付前重新检查逻辑流、daemon 实例、当前身份/租约和原期限；
物理腿代次只约束分片/腿，不能取消已准入的后端执行。已经完成的后端证据须保留在原 owner 下，
过期或新实例的等待者不再接收回复，不能把已执行改成未执行或再次注入。每一页 InputStatusReply、BarrierReply、结果和 ACK 在最终发出时均重查当前有效租约、双方当前成员及密钥、逻辑 owner/授权代次和原请求期限；排队时检查不能替代发送时检查。物理腿切换不改变逻辑 owner。授权失败只抑制发送，已完成的执行证据照常保留。
候选字符串采用 ASCII token，`ufrag` 扩展必须小写且最多出现一次（相同值重复也拒绝）；非 CRLF 的行分隔符不进入 SDP。

## 冻结后的第一个扩展：CapabilitySnapshot（Control 14）

2026-11-15 负责人裁定：新增 type 14，完整携带现有 MachineCapabilities、capability_id 和最长 60 秒的 expires_at_ms。沿用同一设备对签名与进程实例、成员 head 绑定；过期、旧实例或绑定不符拒绝。Capabilities（type 1）的字段和语义不变。未知接收端先认证再返回 Unsupported，发起方保留 relay。直连上传须持有有效快照且其中允许上传；无快照或已过期时上传走 relay，下载不受影响。协议 16 未发布，本次不升协议号。字段定义见产品 transport.proto；MachineCapabilities 仅移到独立 proto 文件以避免循环 import，既有编号和含义未变。
