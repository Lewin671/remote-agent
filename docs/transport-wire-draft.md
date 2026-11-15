# 传输 wire 草案

**草案，待负责人冻结。** 本文为第 1 期实验提出字段和安全编码，不代表产品已接入；
既定语义以 [transport](transport.md) 为准，未决项在 [plan](plan.md#传输模块与直连)。
完整引导已有[串行重试反例](transport-notes.md#8-完整引导链的串行重试下界)，四端信令与终端状态机未完成；本草案不具备冻结条件。
字段草案在 [transport-draft.proto](../spike/transport-1a/wire/transport-draft.proto)，不参与现有生成流程。

## 格式边界

DirectedPeerFrame 只提交目标、类别、本地随机 frame ID 和密文；PeerFrameDelivery 的来源由登录设备写入，
接收端仍验证内层身份、角色、账户和目标。Result 绑定当前主 WS 与 frame ID，delivered 仅表示写入目标 WS。
同 ID 的透明重投必须保持全部字节；断线不落库、不补投。Signal/Control 各自 FIFO，跨类别不保序。
外层分片是未认证资源声明，只决定有界重组；不得推进业务水位，不能改变被双发的信封字节。

每条逻辑流有独立双向序号，首帧 1，0 为尚未处理。路径代次仅控制腿与分片，不能重置逻辑 seq。
Ack 同时表达 processed、input_success 和输入失败状态；非输入失败可以越过 processed，不能暗示输入成功。
InputStatus 的每条结果必须绑定原序号和原字节 hash，部分注入以已知/可能前缀区间表达，不能只给一个布尔值。
查询/关闭 Barrier 进入旧流执行序；BarrierReply 的 final 标志只在停止旧流新准入后成立。
AttachPath 按原 stream、sender、session、双方进程实例、腿 ID/代次及双向水位挂载，不调用 OpenTerminal。
InputStatus 的 unavailable 与 daemon 实例变化要求人工核对，不能自动重发未知输入。

## 编码与认证顺序

草案选择 `TypedPayload { uint32 type_id; bytes payload; }`，不用 protobuf oneof 承载可扩展类型。
未知 type ID 的原字节仍能完成认证，然后返回认证 Unsupported；不会执行或调用 SDK。
已知类型 payload 只允许规定字段及范围；不以默认值补缺必需字段。所有整数先检查范围/溢出，
单帧、候选、分片、缓存条数与时间上限在分配内存前核对，protobuf 解析有深度限制。
签名/hash 覆盖首次编码后的原字节，接收端不重编码后验签；重复、非最短 varint、重复单值字段与歧义编码拒绝。
未来扩展新增 type ID，不能偷偷改变已有 type 的严格 schema。

server 只应看到路由标识、Signal/Control、frame ID、密文长度与时序。因此不能把 head、boot nonce、
SDP hash 或正式代次密钥所需的全部 context 明文放在转发层。安全编码候选为两层封装：

1. 从当前同一已验证成员 epoch+head 中取得双方 X25519/Ed25519 公钥，拒绝普通 machine meta 中的密钥。
   X25519 shared secret 经 HKDF-SHA256 导出设备对 context 包装密钥。info 采用长度前缀编码的固定域
   `remote-agent/peer-context/v1`、账户、按原始字节排序的两个设备 ID、明确的发送方向。
2. Context 的原 protobuf 字节用 AES-256-GCM 加密，随机 12 字节 nonce；AAD 绑定固定域、协议、账户、
   登录来源/目标、外层类别和本地 frame ID。Context 内含双方角色/实例/boot nonce、connection、用途、
   generation、head、信令/控制方向与 seq、原签发时间和短 deadline。正式上下文所有字段必需。
3. 正式 payload 密钥再次从同一设备对 secret 派生，HKDF info 加入双方已认证实例/boot nonce、connection、
   PC 用途、generation、方向与 PeerSignal/TransportControl 域。TypedPayload 的原字节用独立 AES-GCM nonce
   加密；AAD 加入完整加密 Context 的 hash，防止交换 context 和 body。
4. Ed25519 对固定签名域及两段密文、两个 nonce、长度和路由绑定签名。采用明确的长度前缀，
   不直接串接可变长字符串；签名覆盖精确编码，未知 type 也受保护。接收方先做总大小/来源/角色范围检查，
   验签后才解 Context，核对当前 head/租约/nonce/实例/期限/序号，再解 body、查 type；通过后才交 SDK。
5. Probe/Challenge 使用独立 bootstrap 域，只含源实例与 boot challenge、探测关联 ID 和已知的目标信息，
   不含 SDP、不创建 PC、不执行控制。双方 nonce 验证完成才允许正式密钥。旧实例仅产生受限的当前 Challenge。
   同一 bootstrap 授权的副本不能无限生成新 PC、清空缓存或改变正式代次。

该双层封装是本次安全编码**候选**，尚须四端密码学一致向量与安全复核，不能直接宣布冻结。
它避免为了导出正式密钥把私有上下文暴露给 relay；增加的 nonce/tag/signature/长度开销须计入真实帧字节。
设备成员身份、租约、时间 floor 和原有业务信封算法保持原定义；此编码不进入业务接受日志。

信令 seq 在每方向、每 generation 从 1 增长，重复确认与 SDK 安装记录共用同一个有界缓存；
同 seq 异 hash 拒绝。每方向未确认窗口 32，乱序缓冲 32，缺口自首个乱序到达固定 2.5 秒，重发不延长。
重试保持 seq、frame ID、Context 和密文字节；只有依法新 generation/connection 才使用新身份，不能改变原恢复总期限。
防重放条目与关闭 tombstone 保留至 deadline+MaxClockSkew，不能因 PC 关闭提前驱逐。

## 类型表与大小

| 域 | 固定 ID（草案） | Payload 上限/补充约束 |
|---|---|---|
| PeerSignal | 1 Probe，2 Challenge，3 Offer，4 Answer，5 Candidate，6 EndCandidates，7 Confirm，8 Restart，9 Close | 完整 Signal ≤64 KiB；SDP 预留封装空间，候选每条编码 ≤1 KiB、批≤16、每代≤64/4批 |
| TransportControl | 1 Capabilities，2 AttachPath，3 InputStatus，4 Ack，5 Replay，6 Barrier，7 StreamFailed，8 ClosePath，9 DirectPolicy，10 Unsupported，11 InputStatusReply，12 BarrierReply，13 PolicyReply | 完整 Control ≤16 KiB；Signal ACK ≤1 KiB；结果明细需分页且绑定同一查询/屏障快照，不能每页观察不同水位 |

Ack 不请求 ACK；Unsupported/明确回复类型也不产生确认回环。回复以关联请求 seq/hash 和双方实例绑定，
重复回复只被原等待者消费。Capabilities 最长 60 秒；DirectPolicy revision 持久化并按角色区分机器/自身 peer 范围。
所有类型都不能借控制队列执行任意 Command。既有五类 CommandResult、身份/业务冲突和正文可得性原样复用。

## 协议号建议

在协议 15 基线上引入这些新路由帧、Purpose 与授权语义，建议升为 **16**，四端一起更新；
真正整合时若 16 已被占用，按当时基线定号。本实验不改任何现有协议常量。
固定编码冻结后，新增类型且旧端认证后返回 Unsupported、发起方保留 relay、不改变原操作语义，
可在同一协议内扩展。已有字段、角色、类别、授权、ACK 水位或未知类型处理含义变化须升协议，不能添加旧协议推断分支。
本草案未涉及存储迁移；实际实施若需要数据重建或协同升级，必须写清公开升级要求。
