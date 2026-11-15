# SDP 实验向量

这是销毁过的独立实验 PeerConnection 的原串，不含用户账户、私钥或真实网卡候选地址。
`manifest.json` 保存原字节 SHA-256、来源与预期结果；`vectors.tsv` 是四端验证器共同输入。
`.gitattributes` 禁止 Git 转换行尾；不要把 `.sdp` 文件通过文本读写“修好”。

真实原串共 15 份，每个实现各 Offer/Answer/Restart：

| 前缀 | 实现 |
|---|---|
| pion | Pion v4.2.26，CGO_ENABLED=0 |
| chrome | Chrome 157.0.8103.52 |
| webkit | Playwright 1.65.1 WebKit 27.2，build 2388 |
| android | webrtc-sdk android 150.7871.04，API 36 arm64 模拟器 |
| ios | stasel/WebRTC 157.0.0，iOS 27 模拟器 |

`good-inherited-fingerprint.sdp` 是 Chrome 原串的派生正例，不冒称 SDK 直接输出。
16 个 `bad-*.bin` 是明确的负向变异，包括真正的非法 UTF-8 字节；不是 SDK 原串。
只验证有限的解析/约束一致性，不代表真实恶意 DCEP、候选代次、完整信令、安全编码或跨端连接已验收。
复跑与证据边界见 [实验说明](../../../spike/transport-1a/README.md)。
