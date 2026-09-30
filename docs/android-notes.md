# Android 实测记录

本文记录 Android 客户端依赖的库和系统在实际使用中的表现：connect-kotlin、Termux 终端组件、加密实现、FCM、模拟器和 v2 客户端用到的系统能力（选图、图片方向、保存分享）。只写观察到的现象和踩过的坑；App 据此怎么设计见 [architecture.md](architecture.md) 第 15 节，还没测的见 [plan.md](plan.md) 的「待确认」。

没有特别标注的条目都是实测结果，推断或没测过的会在句中标「推测」「未验证」。

验证环境：connect-kotlin 0.9.0、OkHttp 5.4.0、protobuf-javalite 4.35.1（生成代码 31.1）、Bouncy Castle 1.81、Termux terminal-emulator / terminal-view v0.118.3，JDK 17，Android 模拟器 API 36（google_apis_playstore，arm64），2026-09-29。还没有在真手机上验证。

## 1. connect-kotlin 和现有 server

- **能直接互通**：Connect 协议加二进制 Protobuf，用 `GoogleJavaLiteProtobufStrategy`，对 connect-go 写的 server 不用任何适配。`:core` 的测试在 JVM 上起真实的 Go server 和一个假 daemon，登记、登录、拉列表、`GetEvents`、`Forward`、配对全部跑通；模拟器上的 App 同样
- **错误码**：server 返回的 Connect 错误（如机器离线的 `unavailable`）变成 `ConnectException`，`exception` 为空。网络层的失败也是 `ConnectException`：`SocketTimeoutException` 是 `DEADLINE_EXCEEDED`，其他 `IOException` 是 `UNKNOWN`，原始异常在 `exception` 里。所以「电脑不在线」要看 `exception` 为空，连不上服务器（`UnknownHostException`、`ConnectException`）说明请求没有发出
- **版本约束**：connect-kotlin 0.9.0 依赖 OkHttp 5.4.0，它的 `okhttp-android` 要求 compileSdk 36 以上（`checkAarMetadata` 报错）。0.7.4 还用 OkHttp 4.12，没有这个要求
- **生成代码**：用 buf 的远程插件 `buf.build/protocolbuffers/java`（`lite`）和 `buf.build/connectrpc/kotlin`，java_package 用 managed mode 覆盖。生成代码的版本不能高于运行时的 protobuf-javalite（本机 protoc 36.2 生成的代码要求运行时 4.36 以上），所以固定用远程插件的版本
- **R8**：release 包开 R8 后同步、指令、配对都正常，只需要保留 `GeneratedMessageLite` 的子类（`app/proguard-rules.pro`）。release 包 5.3 MB

## 2. Termux 终端组件

- **许可**：termux-app 仓库整体是 GPLv3，只有 `terminal-emulator` 和 `terminal-view` 两个模块是 Apache 2.0（仓库的 LICENSE.md 写明）
- **改接远程字节流的工作量**：`TerminalView` 只通过 `TerminalSession` 的少数方法和它打交道（`write`、`writeCodePoint`、`getEmulator`、`updateSize`、`onCopyTextToClipboard` 等）。`TerminalSession` 是 `final`，并在 `updateSize` 时通过 JNI 起本地进程，所以没法继承或只换一部分。做法是把两个模块的源码放进 `android/terminal`，只重写 `TerminalSession`（约 190 行）：键盘输入交给回调，远程输出和快照由外部喂进来，模拟器的尺寸跟随远程快照，`updateSize` 只报告视图能放下的行列数。JNI 和 C 代码一起去掉，不再需要 NDK。`TerminalView`、`TerminalEmulator` 原样使用
- **字号**：渲染器用 `measureText("X")` 算列宽。用 `M` 的宽度估算字号时，160 列的窗口在 1080 像素宽的屏幕上最右边一两个字被截掉；改成和渲染器一样量 `X` 并逐个像素试，就正好放下
- **输入法**：`TerminalView` 默认把输入类型设为 `TYPE_NULL`。Gboard 英文键盘在这种模式下逐个发按键，输入、退格、回车都正常，`^C` 等按键栏发的控制字符也正常。中文输入法未验证（模拟器上的 Gboard 只有英文）
- **键盘弹出**：`adjustResize` 下视图变矮，远程的 tmux 尺寸不变，只是把光标所在的行滚进可见区
- **adb 注入文字**：`adb shell input text` 一次发一长串时，Gboard 会丢字（模拟器负载高时更明显）；逐个词发、中间停一下就不丢。这是测试手段的问题，不是 App 的

- **字体（2026-09-30，API 36 模拟器实测）**：Sarasa Fixed SC Regular v1.0.42，31px 字号下英文 `X` 的 advance 为 16px，样本「后台命令已完成。中文」为每字 31px；按两格绘制仍有约 3.2% 的横向拉伸，同机系统等宽字体为约 22.6%。箭头 `→` 和框线 `─` 的字宽均为一格，不再缩放；实际 Claude Code 终端中的圆点、箭头和中文正常显示。取整仍可能产生少量比例差异，其他字符和真机未全部验证。
- **适配与键盘（2026-09-30，任务测试包基于 0.2.2，API 36 模拟器实测）**：字号 14 适配为 55×31，增到 16 后为 50×28；重开终端保留字号 16。键盘弹出后远端行列数与本地字号保持不变。`adjustResize` 更新视图高度会比 Compose 的 IME 可见性早一帧，需同时读取原生窗口的 IME 状态，避免把键盘遮挡后的高度用于缩小字号。

## 3. 加密

- **Bouncy Castle lightweight API**：X25519、Ed25519、HKDF 和 Go、TypeScript 逐字节一致：`vectors.json` 在 Kotlin 通过，Kotlin 生成的 `kotlin-vectors.json` 在 Go 和 TypeScript 也通过（包括大于 2^63 的 `uint64` 序号和配对封好的根密钥）
- **X25519 全零共享值**：对方公钥是低阶点（如 32 个零字节）时，BC 的 `X25519Agreement` 自己抛 `IllegalStateException`，不会返回全零；代码里仍然另外检查一次
- **AES-GCM 用平台 JCA**：`AES/GCM/NoPadding` 在 JVM 和 Android 上都能用附加数据加解密，和 Go 的结果一致（同一组向量）
- **平台 JCA 导入原始 32 字节的 X25519、Ed25519 密钥**：未验证，一直用 Bouncy Castle
- **Keystore**：`setUnlockedDeviceRequired(true)`（API 28 起）的 AES 密钥在模拟器上能生成和使用；设了它的密钥在锁屏时不能用，所以通知密钥用另一把没设这一项的密钥包装

## 4. FCM

- **可用**：模拟器（Google Play 镜像）经 `-http-proxy` 走本机代理后能拿到 FCM token，server 用 FCM HTTP v1 发的 data message 能收到。token 长 142 字符
- **耗时**：App 在后台时，从 daemon 记下审批到通知出现约 3 秒；进程被杀掉后（`am kill`），系统为 FCM 广播新起进程，从审批记下到通知出现约 2 秒，其中 `onMessageReceived` 里读偏好、解密、发通知共 191 ms（进程在时 14 到 80 ms）。模拟器负载很高时，进程启动超过 10 秒会被判定为「failed to complete startup」的 ANR，这条推送随之丢失（系统日志 `broadcast intent callback: result=CANCELLED`）。推测真手机上不会这么慢，但 Application 的 `onCreate` 仍然只做最少的事
- **`am force-stop` 之后收不到推送**：被强制停止的 App 处于 stopped 状态，系统不投递广播；测试「进程被回收」要用 `am kill`
- **通知按钮**：`setAuthenticationRequired(true)` 的按钮在没有锁屏的模拟器上直接可点，点「允许」「拒绝」后由 WorkManager 转发，daemon 记下的是 allow / deny。锁屏时要先解锁未验证（模拟器没设锁屏）
- **`VISIBILITY_PRIVATE` 挡不住锁屏**：用户在系统设置里允许锁屏显示敏感内容时，PRIVATE 的通知照样全文显示。`VISIBILITY_SECRET` 的通知在设了锁屏密码时整条不出现在锁屏上（模拟器 API 36，设了 PIN 后实测）
- **锁屏时 FCM 拉起进程**：设了 PIN 并锁屏、`am kill` 之后，推送照样把进程拉起并显示通知；这时 `setUnlockedDeviceRequired` 的密钥不能用，只有单独包装、没设这一项的通知密钥能解密。解锁后点通知进 App，账户在前台读取成功
- **系统猜的操作**：通知正文里有网址时（如 `curl https://…`），系统会自动加一个「Open link」按钮，位置就在「拒绝」「允许」上面，容易误点；要用 `setAllowSystemGeneratedContextualActions(false)` 关掉

## 5. 模拟器

- **默认的 AVD 太慢**：`Medium_Phone` 的配置是 1 个 CPU、2 GB 内存。跑 Android 36 加 Google 服务时 system_server 反复被 watchdog 重启，adb 报 `Can't find service: activity`，debug 包启动要几十秒。用 `-cores 4 -memory 3072` 启动后正常得多，但宿主机内存吃紧（在交换）时仍会卡到 SystemUI、Launcher 报 ANR
- **debug 包**：Compose 的 debug 包没有预编译，冷启动在慢机器上要十几秒，性能判断以 R8 的包为准

## 6. v2 客户端

模拟器 API 36（google_apis_playstore，arm64），Claude Code 2.1.285、Codex 0.159.0，2026-09-30。

- **工具状态**：v2 daemon 在 `ToolCall.background` 里说明调用转到了后台，App 据此标为「后台命令 · 状态待确认」并写明原因；旧 daemon 下 Claude Code 自己转到后台的命令只能从结果文字看出来（[claude-code-notes §10](claude-code-notes.md#10-中断和运行中的输入)）。一轮正常结束时仍没有结果的开始只显示「未收到工具结果」
- **Photo Picker**：API 36 上是系统的嵌入式选择器，`PickMultipleVisualMedia(n)` 的上限要求 n ≥ 2，只剩一张的名额时改用单选；选中后 `takePersistableUriPermission` 成功，用于切换「原图」时重新读取
- **图片方向**：`BitmapFactory` 不按 EXIF 转向，按自己读出的方向用 `Matrix` 转正后，带方向 6 的照片在两个 CLI 里都被认作正向；HEIC 等交给 `ImageDecoder`（未用真实 HEIC 验证）
- **Keystore 包装的文件密钥**：待发图片用进程内 AES-GCM 加密，Keystore 只包装这把密钥；第一次生成密钥的同时写入图片时，旧目录清理不能晚于写入（踩过：刚建的目录被清掉，保存失败）
- **保存和分享**：API 29 起 `MediaStore` 写入 `Pictures/remote-agent` 不需要权限；分享走 `FileProvider`，文件放在缓存的 `shared/` 下，下次启动删除
- **adb 测试手段**：模拟器访问本机 server 用 `adb reverse tcp:18080 tcp:18080` 后填 `http://127.0.0.1:18080`，这样 daemon 和手机用的是同一个 server 地址，配对不会报「账户属于另一个服务器」；`input text` 里的括号要避开（shell 语法）

## 7. v5 界面复查

2026-09-30，基于产品版本 0.4.1 的 v5 修复工作树；Compose BOM 2025.06.01，API 36 arm64 模拟器，独立包名 `.uicheck`，未连接真实账户。

- **实际渲染**：`LayoutTest` 的 4 项仪器测试通过。320dp、字体倍率 1.5、暗色下，完全放行、上下文警告和发送按钮均在输入区边界内；320dp 亮色下，长配置的新会话卡片操作未越界；聊天／终端标签的选中、切换与已结束禁用状态正确；字体倍率 2 下可滚动到审批模式面板最后一个选项。测试生成的卡片截图已人工查看。
- **状态回归**：JVM 测试验证新建首条消息在历史到达前显示，历史到达后不重复；取消、失败和无法确认收到时不继续显示正常送达文案。
- **构建**：`:core:test`、`:app:testDebugUnitTest`、debug APK、仪器测试 APK 与启用 R8 的 minified APK 构建通过。以上是模拟器与夹具验收，不替代用户手机上的整体使用验收。

## 8. 客户端性能 Profile

2026-09-30，基于 v0.5.0（`4555678`）的原版与性能优化工作树，Android 16 / API 36 arm64 模拟器，1080×2400、420dpi；独立 `.profile` 包启用 R8、`profileable`，不启用 debug，两版均以 `speed` 预编译。使用本地隔离 server 和合成 daemon，经过真实加解密、同步、Room、Compose 与 Termux 路径，不连接用户账户。

**方法与范围**：长会话约 1.1 万条交替用户／助手消息，含固定短 Markdown；每秒目标新增 5 条事件。终端为 100×40，目标 120 包/秒、2 KiB/包，内容为定位光标后覆盖文字。每段 Perfetto 录制 20 秒，按进程实际运行的调度时间汇总 CPU；100% 表示占满一个核心。终端计时调度有波动，原版生成 2,168 包、优化版 2,050 包，因此同时核对按输入包数归一化的结果。比较采样与界面仪器测试分开执行；并行占用模拟器的两段采样已排除。

| 场景 | 原版进程 CPU 时间 | 优化版进程 CPU 时间 | 观察 |
|---|---:|---:|---|
| 长聊天持续更新 | 4,311 ms | 3,032 ms | 减少约 30%；新增事件数均为 98 |
| 前台终端高输出 | 5,891 ms | 3,535 ms | 同目标负载减少约 40%；按生成包数归一化减少约 37% |
| 高输出终端切后台，稳定后采样 | 2,076 ms | 5.8 ms | 后台终端流为 0，没有终端解析或界面帧 |

- **长历史热路径**：列表构建／更新标记的平均墙钟耗时由 10.60 ms 降至 0.47 ms，状态推导由 2.43 ms 降至 1.02 ms。该负载主要走普通消息追加路径，不能据此保证工具／审批／压缩事件完整重建也有同样降幅。
- **顺序与恢复**：增量状态回归覆盖 Claude 输入归属、跨批迟到工具结果、历史替换／缩短及 CLI 切换；终端测试覆盖有界队列、关闭与积压、跨包 UTF-8／emoji／控制序列。模拟器回前台后取得 100×40 新快照，只有一个终端流并继续输出。4 项既有布局仪器测试通过。
- **限制**：本次没有真机温度或电池功耗数据。模拟器使用 emulation 图形驱动，调用栈中的 OpenGL 提交、读写及帧截止时间不能直接外推到真机。以上是该合成负载的观察，不代表任意会话或真实 CLI 都有同样性能收益。

本地采样的耗时标记和账户入口不进入正式 APK；当前设计见 [architecture §15](architecture.md#15-android-客户端)。

## 9. v7 界面还原

2026-09-30，基于 `e1a25e0` 的 v7 工作树；Compose BOM 2025.06.01，API 36 arm64 模拟器，独立包名 `.v7check`，只用固定示例数据，未连接真实账户和 CLI。

- **和设计稿同尺寸对照**：设计稿的手机内容区是 304×644 CSS px。`adb shell wm size 798x1691`（420 dpi 下即 304×644 dp）后跑仪器测试 `V7Gallery`，用 `UiAutomation.takeScreenshot()` 截整屏，底部弹层和对话框也能截进来；设计稿一侧用浏览器对每个 `.phone` 截图，去掉 8 px 边框后缩到同宽并排比较。用完要 `wm size reset`
- **固定数据**：`Remote.showForTest` 直接放入列表和历史，不连 server；需要读模型列表或设置的画面因此显示读取失败的状态，属于测试数据，不是界面问题
- **平台差异**：中文是系统的 Noto Sans CJK（可变字重，`/system/etc/fonts.xml` 里 100–900 各档都指向 `NotoSansCJK-Regular.ttc` 的 wght 轴），同一标称字重比苹方重，按 [design-system §3](design-system.md#3-字号与字重) 降一档；西文和等宽改为打包的 Inter、JetBrains Mono，`✓ ✕ ◌` 和列表圆点画成矢量。CJK 字体里的 `•` 是很小的居中点，不能直接当列表圆点用。`‹`、`›` 的字形仍略有不同；状态栏和手势导航条由系统绘制。颜色、圆角、间距和字号按设计稿取值，并排比较肉眼无差别
- **字号 130%**：`settings put system font_scale 1.3` 下 304 dp 宽的全部画面没有越界或遮挡；等待事项展开后内容在 240 dp 内滚动，操作按钮仍可见
- **构建坑**：同一工作树里交替用和不用 `-PappIdSuffix` 构建时，配置缓存会沿用上次是否启用推送的判断，而 `google-services.json` 已被另一次构建删掉，报 `File google-services.json is missing`。本地调试固定带同一个后缀，或加 `--no-configuration-cache`

