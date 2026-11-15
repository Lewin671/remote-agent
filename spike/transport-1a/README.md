# 传输 1a：SDK 实验与信令期限模型

按负责人 2026-11-14 裁定运行的有限虚拟时钟模型。设计以 [transport §5](../../docs/transport.md#非持久双向信令) 为准；
结果见 [transport-notes](../../docs/transport-notes.md#6-信令重试与缺口期限的模型验证)。
这不是 SDK、密码学或产品测试，不测真实 wire 字节或网络时间。

```sh
REMOTE_AGENT_HOME=/tmp/ra-transport-model CGO_ENABLED=0 \
  python3 spike/transport-1a/retry-gap.py
```

只有 Python 标准库，不创建状态目录、不读账户、不打开网络，不调用 SDK。
Signal/Control 是可分别丢帧的假路径；有效身份、head、租约、challenge、认证作为前提。
`sdk_observer_calls` 只是本应调用 SDK 的观察点，合成 payload 不是 SDP。

缺口从其后第一帧乱序到达起算 2500 ms，重发和后续帧不延长期限。
发送窗口最多 32 个未确认序号；满则等待 ACK。重发同序号不占新槽，初发后 500/1500 ms 重试，
limited 可推迟到 retry_after，但最多三份并保留 10 秒总期限。模型不用等待/轮询循环。

十场景覆盖正常、一次 Offer 丢失、ACK 丢失、原两次 Offer 丢失反例、候选批缺口、
limited/retry_after、retry_after 超过总期限、每代丢两份 Offer、丢全部三份 Offer、发送窗口背压。
原反例在 1500 ms 补齐，不再提前退休；所有副本的 hash 一致，接收处理不重复。
全丢场景给代次重开零成本的有利条件，仍最多 4 代；它不代表真实 Restart/ICE 的耗时。
窗口压力用 64 条合成消息，不代表信令允许 64 个候选批。

旧 1 秒反例保存在 git 历史中；现在只维护裁定后的规则。

## 完整引导链的第二个反例

```sh
REMOTE_AGENT_HOME=/tmp/ra-transport-model CGO_ENABLED=0 \
  python3 spike/transport-1a/handshake-budget.py testdata/transport/sdp
```

载入五组 SDK/引擎原串，以两条假管道模拟 Signal 和健康 Control，8 客户端×2 PC，
每 PC 64 个合成候选/4 批。没有运行 16 个真实 PeerConnection。
正常和每条只丢初发均 16/16 Ready；每条关键消息丢前两份时，10 秒内 0/16 Ready。
Probe→Challenge→Offer→Answer→Offerer Confirm 的五次串行重试，即使其余成本为零，
加 3 秒 relay 也需要至少 10.5 秒。它不是“整轮只有两帧丢失”的场景。
主场景采用请求/应答 bootstrap。另一个有利变体省去 Probe 等待、主动 Challenge，并把 Confirm 与设计允许的 3 秒 ICE/DTLS 并行，仍在 10 秒内 0/16 Ready；它不声明新的 bootstrap 产品规则。
2.5 秒缺口没有超时、发送窗口峰值 7；此模型仅计 payload 字节，不把合成候选当 SDK 实测候选。

**在此按任务停止条件停止**，未完成四端完整信令或终端假路径状态机，wire 草案不能冻结。
证据和待裁定入口见 [transport-notes §8](../../docs/transport-notes.md#8-完整引导链的串行重试下界)。

## 独立 SDK 与共享 SDP 向量

- `go/` 独立 Go module，Pion 版本与依赖锁在 go.mod/go.sum，不导入产品代码。
- `browser.mjs` 使用独立安装的 Playwright 模块，运行已安装 Chrome 与下载的 WebKit。
- `AndroidLab.java` / `IOSLab.swift` 是独立空壳 App；生成 Offer/Answer/Restart 原串并传测试消息。
- `SDPCheck.java` / `SDPCheck.swift`、`sdp.mjs`、`go/sdp.go` 是实验验证器，读取同一份 vectors.tsv。
- `build-android.py` / `build-ios.py` 建实验 App 或无 SDK 包体对照；输出只允许放 /tmp，不会安装 App。
- [downloads.json](downloads.json) 固定下载 URL、SHA-256；iOS zip 与 SPM checksum、Android AAR 与 Maven sha256 核对通过。
  这不是发布者签名或可复现构建证明。工具链下载也记录，已有 Chrome/Android SDK/Xcode 未重装。
- [字段草案](wire/transport-draft.proto)只供语法检查，不进入生产生成目录。

每个运行/编译命令显式使用 `REMOTE_AGENT_HOME=/tmp/transport-lab/state CGO_ENABLED=0`。
下载、依赖解包、模块缓存、App 和输出必须放本任务自己的 /tmp 目录；每个重步骤前检查磁盘≥15 GiB。
不要用手机；只在自己创建且记录 ID/PID 的模拟器上安装实验 App，结束 shutdown/delete 或停止并删除私有 AVD。

示例（先创建自己的隔离目录，固定二进制按 downloads.json 下载并核对，不从这里更新产品）：

```sh
# 从 go/ 中运行；输出 SDP 只用于临时测试，不打到日志。
REMOTE_AGENT_HOME=/tmp/transport-lab/state CGO_ENABLED=0 \
  GOMODCACHE=/tmp/transport-lab/gomod go run . /tmp/transport-lab/sdp

# 从仓库根运行。Playwright 应独立安装，不修改 web/package.json。
REMOTE_AGENT_HOME=/tmp/transport-lab/state CGO_ENABLED=0 \
  PLAYWRIGHT_BROWSERS_PATH=/tmp/transport-lab/browsers \
  node spike/transport-1a/browser.mjs /tmp/transport-lab/node/node_modules/playwright/index.mjs /tmp/transport-lab/sdp

REMOTE_AGENT_HOME=/tmp/transport-lab/state CGO_ENABLED=0 \
  python3 spike/transport-1a/build-android.py --sdk "$ANDROID_SDK_ROOT" \
  --work /tmp/transport-lab/android --aar /tmp/transport-lab/android.aar

REMOTE_AGENT_HOME=/tmp/transport-lab/state CGO_ENABLED=0 \
  python3 spike/transport-1a/build-ios.py --framework /tmp/transport-lab/WebRTC.xcframework \
  --work /tmp/transport-lab/ios
```

`--baseline` 生成对应空壳包体对照；iOS `--simulator` 用模拟器 slice，默认仅编译未签名真机 arm64 包，**不安装真机**。
SDK 实际启动时，记录 PID，从自己的 App 容器取回 `.sdp/.json`；不要取其他 App 的文件。
iOS 使用 scene 生命周期；Android 先注销 DataChannel 回调再释放，避免清理回调访问已释放的 JNI 对象。

共享向量验证：

```sh
REMOTE_AGENT_HOME=/tmp/transport-lab/state CGO_ENABLED=0 \
  node spike/transport-1a/check-vectors.mjs testdata/transport/sdp
# go/ 目录：
REMOTE_AGENT_HOME=/tmp/transport-lab/state CGO_ENABLED=0 \
  GOMODCACHE=/tmp/transport-lab/gomod go run . vectors ../../../testdata/transport/sdp
# 仓库根：
REMOTE_AGENT_HOME=/tmp/transport-lab/state CGO_ENABLED=0 \
  javac -d /tmp/transport-lab/classes spike/transport-1a/SDPCheck.java
REMOTE_AGENT_HOME=/tmp/transport-lab/state CGO_ENABLED=0 \
  java -cp /tmp/transport-lab/classes example.transportlab.SDPCheck testdata/transport/sdp
REMOTE_AGENT_HOME=/tmp/transport-lab/state CGO_ENABLED=0 \
  swiftc -parse-as-library -D VECTOR_CLI -module-cache-path /tmp/transport-lab/swift-cache \
  spike/transport-1a/SDPCheck.swift -o /tmp/transport-lab/swift-vectors
REMOTE_AGENT_HOME=/tmp/transport-lab/state CGO_ENABLED=0 \
  /tmp/transport-lab/swift-vectors testdata/transport/sdp
```

有限向量不代替完整 SDP fuzz、恶意额外 DCEP、跨端互通、真实候选重启、防重放或完整安全编码一致向量。
每个 SDK 的自配对消息成功也不等于产品可直连、真实 NAT 成功率或手机性能验收。
