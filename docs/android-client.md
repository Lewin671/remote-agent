# Android 客户端的设计

Android 原生 App 要把手机上的体验做到原生水平。本文写 App 自己的选型和约束：工程结构、生命周期、密钥保管、原生终端、推送和在通知里审批。协议、同步、指令和终端的规则都和网页一样，见 [architecture §10](architecture.md#10-报文同步和指令)、[architecture §11](architecture.md#11-终端视图)，这里不重复；范围见 [scope.md](scope.md) 第 5 节，库和系统的实测见 [android-notes.md](android-notes.md)，进度和未定的问题见 [plan.md](plan.md)。

## 选型

| 组件 | 选型 | 理由 |
|---|---|---|
| 工程结构 | 纯 Android 工程，分成 `:core` 和 `:app` 两个模块，代码放在 `android/` | 见下文「模块」 |
| UI | Jetpack Compose + Material 3；状态用 Coroutines + StateFlow，事件在一个串行队列里归约 | 返回手势、键盘 insets、动态取色这些原生交互都能直接控制；前台界面和后台通知走同一套逻辑 |
| RPC | connect-kotlin + OkHttp，Connect 协议，二进制 Protobuf | ConnectRPC 官方的 Android 用法 |
| Protobuf | 用 `buf` 从 `proto/` 生成 javalite 和 connect-kotlin 代码 | `proto/` 仍是唯一的协议定义 |
| WebSocket | OkHttp，`/v1/stream` 和 `/v1/term` 各一条 | 和网页相同的两条连接 |
| 加密 | AES-256-GCM 用平台 JCA；X25519、Ed25519、HKDF 用 Bouncy Castle 的 lightweight API | 先保证和 Go、TS 逐字节一致，见下文「加密和密钥」 |
| 本地存储 | Room 存密文信封和同步游标，DataStore 存偏好 | 本地不保存明文 |
| 推送 | FCM data message（自己的手机有 GMS） | 见下文「推送和通知里审批」 |
| 终端 | Termux 的 `terminal-emulator` 和 `terminal-view`，源码放在 `android/terminal`，用 `AndroidView` 嵌进 Compose | 原生渲染和输入法都比 WebView 里的 xterm.js 好；为什么放源码见下文「终端」 |
| 扫码 | CameraX + ZXing core | 本地解码，只扫配对二维码这一种 |
| 构建和分发 | Gradle version catalog 锁定依赖版本；minSdk 26，targetSdk 35，compileSdk 36（connect-kotlin 0.9 依赖的 OkHttp 5.4 要求）；release 构建开 R8，另有一个 `minified` 构建类型，和 release 相同但允许明文 HTTP，用来在本机 server 上测 R8 的包；自己签名，侧载安装；Firebase 的 `google-services.json` 和签名配置放在仓库外的 `~/.config/remote-agent/`，没有时照常构建、推送关闭 | 不上架应用商店；`setAuthenticationRequired` 等 API 31 起的能力按版本判断 |

## 为什么是纯 Kotlin

| 方案 | 结论 | 理由 |
|---|---|---|
| KMP 共享核心 | 暂不采用 | 价值在于和 iOS 共享，iOS 还没定；connect-kotlin 只发布 JVM 版（2026-09-29 核实），放不进 `commonMain`，只能自己实现 Connect 协议。iOS 最后也没有走这条路，写成了 Swift（[ios-client.md](ios-client.md#为什么是纯-swift)） |
| gomobile 复用 Go | 暂不采用 | 能直接复用的只有 `internal/e2e`：`relay.JoinByPairing` 按机器登记并写 `account.json`，`remoteclient` 是测试用的客户端，不保存设备私钥；同步引擎和事件归约只有 TS 版，用 Go 也要新写。还要承担：Go 的 TLS 不走 Android 的网络安全策略，系统代理不自动生效，网络切换要由 Kotlin 通知，协程取消要桥接到 `context`，Go 和 JVM 两个堆，以及 NDK、R8、16 KB 页面的构建问题 |
| 只把加密交给 Go | 不采用 | 绑定层的固定成本一样不少，复用最少 |
| Flutter、React Native | 不采用 | 终端和系统交互仍要写原生模块；Flutter 多出一套 Dart；RN 也搬不动现有网页，它依赖 WebCrypto、IndexedDB、xterm.js 和 DOM |
| 包装 PWA（TWA、Capacitor） | 不采用 | 界面仍是网页，终端输入、键盘和系统交互达不到原生 |

## 模块

- **`:core`**：纯 Kotlin/JVM，不依赖 `android.*`。包括 proto、RPC 和 WebSocket、加密、配对、同步、事件归约和项目分组，都能在 JVM 上单独测试
- **`:app`**：Compose UI、终端视图、生命周期、Keystore、Room 和 DataStore、FCM 和通知、扫码。通过少量接口使用 `:core`，不把 Android 类型传进去

## 同步和生命周期

同步、指令的规则照搬网页（[architecture §10](architecture.md#10-报文同步和指令)）：先连主 WebSocket，收到 `Ready` 后再拉列表和缺的事件；按序号去重、补缺口；元数据只接受更新的修订号；指令重试沿用同一个请求 ID 和截止时间，结果未知就如实显示。

App 在前台时保持主 WebSocket；进入后台关闭主连接和终端连接，暂停自动历史分页与界面计时，改靠推送；回到前台重新认证、追赶历史，终端等待主连接就绪后取得新快照。不靠常驻后台的连接收通知。退出账户的清理不随页面销毁取消，清理完成后才显示加入入口；清理失败时显示重试，不能直接加入另一账户。

Android 的事件列表用不可变分块快照，追加时复用完整旧页，前缀校验按页比较；聊天状态按追加事件增量推导，每次发布独立快照；普通消息复用旧聊天行，审批、工具结果和压缩等影响旧行的事件，以及 CLI 类型或显示上下文变化，仍完整重建。终端保留字节顺序，将相邻输出合并通知视图，并限制批次与队列大小；积压超限重新获取快照，不能跳过中间字节。模拟器前后 Profile 见 [android-notes §8](android-notes.md#8-客户端性能-profile)。

## 加密和密钥

- **实现**：Android 13（API 33）起平台 JCA 也有 XDH 和 Ed25519，但导入导出原始 32 字节密钥、从 seed 推出公钥都没验证过，验证通过前继续用 Bouncy Castle。直接用 Bouncy Castle 的 lightweight API，不依赖系统里名为 `BC` 的旧 provider
- **互通**：`:core` 的测试读取 `internal/e2e/testdata/vectors.json`；另加 Kotlin 生成、Go 和 TS 校验的反向向量。要特别对齐的是：header 按原始字节参与认证、密文后接 16 字节 tag、签名上下文末尾的 `\0`、配对 transcript 的长度前缀和 emoji 表、拒绝全零的 X25519 共享值、`uint64` 序号不经过有符号数
- **保管**：在 Keystore 里生成不可导出的 AES 密钥，用它加密根密钥和设备 seed，存在应用私有目录，不进云备份和设备迁移。API 28 起账户包装密钥要求设备解锁；生成失败时报错，不降级成锁屏可用的密钥。API 26–27 没有这个平台限制。签名和配对都要原始字节，所以用的时候它们仍会进内存，Keystore 只保护存储。AndroidX `security-crypto` 已弃用，不用 `EncryptedSharedPreferences`
- **通知密钥**：`notify`（[architecture §9](architecture.md#9-账户设备和密钥)）单独包装保存，后台收到推送时就能解密；根密钥和 `command` 密钥不因为收通知而在后台解开

## 终端

Android 不执行终端输出里的 OSC 52 剪贴板写入，避免恶意文件或模型输出修改手机剪贴板；用户主动选择复制仍可用。本地 CLI 的结构化事件、模型和会话列表把控制码显示为转义文本，避免消息或文件内容操纵终端及审批显示；原生 tmux 字节流保留交互行为。

`terminal-emulator` 和 `terminal-view` 是 Termux 仓库里以 Apache 2.0 发布的两个模块（仓库其余部分是 GPLv3，2026-09-29 核实），只用这两个。`TerminalView` 绑定的 `TerminalSession` 是 `final`，还在 `updateSize` 里通过 JNI 起本地进程，引用发布的包就换不掉它。所以两个模块的源码固定在 v0.118.3 放进 `android/terminal`，只重写 `TerminalSession`：输入交给回调，远程的输出和快照由外部喂入，JNI 和 C 代码去掉（实测见 [android-notes §2](android-notes.md#2-termux-终端组件)）。升级上游时对照这一个文件合并。

Android 终端的字体依次是 JetBrains Mono、Sarasa Fixed SC 的符号子集（JetBrains Mono 没有的 `⏵`、`⎿`、`✻` 和盲文点阵等，约 290 KB）、系统字体（中文由它提供）。自带的回退链用 Android 10 起的 `Typeface.CustomFallbackBuilder`；Android 8–9 只有 JetBrains Mono 加系统字体，系统缺的符号仍显示不出。JetBrains Mono 的格宽是 0.6em，中文字形比两格窄：渲染器把比格子窄的字形按原宽居中画，不再横向拉伸；框线、色块和 Powerline 分隔符仍拉满格子以保持相连。实测见 [android-notes §2](android-notes.md#2-termux-终端组件)。字号默认 14dp，可在 10–22dp 间调整，保存在本机。适配和绘制使用同一字体在实际像素字号下测出的格宽、取整行高及顶部基线偏移；Termux 的字符宽度规则不变，比格子宽的字形仍然压缩进格子，避免破坏框线和光标对齐。

尺寸和积压的规则同 [architecture §11](architecture.md#11-终端视图)：进入终端时自动适配一次，之后键盘弹出、旋转屏幕都不改远端尺寸，只有手动「适配」「还原」及已适配时主动调整字号才改；输出跟不上就断开重拿快照，不能丢字节接着显示。

## 更新

App 是侧载的，没有应用商店替它更新，所以自己做：发现新版本、下载、交给系统安装。清单和 APK 从哪里来见 [architecture · 产品版本与发布](architecture.md#产品版本与发布)，界面见 [client-v38 · App 更新](design/client-v42.html#update)。

- **什么时候看**：回到前台时，距上次超过 6 小时才看；设置里可以手动看。自动看失败不提示，手动看失败才写原因。只比较 versionCode，和系统判断能不能覆盖安装用的是同一个数
- **再次检查**：已有更新或已下载时，弹层里仍留一个轻量的「检查更新」，手动检查不受 6 小时间隔限制。检查期间已发现的版本与说明留在屏幕上，下载、安装和再次检查暂时禁用；回来的发布版比它新就替换版本号、说明和大小；原来的安装包留到本应用已是最新时清理，避免系统安装器仍在读取；还是同一个就保留已校验的包，只写「已是最新发布版本」；失败保留原来的更新并写原因（只有手动检查写），可以重试。下载过程中不检查
- **不自动下载**：看到新版本只提示，点了才下载。发版很频繁，每版都在后台下 6 MiB 不值得，而且装不装本来就要人确认
- **下载和校验**：存在应用私有文件目录的 `updates/` 里（不放缓存目录：系统清缓存时安装器可能还在读它），大小和 SHA-256 都和清单一致才算下载好，否则不留文件；多于清单大小就中止。已有一份校验通过的就不再下载。发现自己已是最新时清掉这个目录
- **安装**：通过 FileProvider 把 APK 的内容 URI 交给系统安装器（`ACTION_INSTALL_PACKAGE`，带回结果），只授予读这一个文件的权限，确认框是系统的。没有用 `PackageInstaller` 会话：小米等厂商的安装器会不出界面就中止会话，内容 URI 这条路各家都认，一条路也少一半要验证的状态。静默安装需要设备所有者权限，做不到，也不该做
- **先看权限**：打开安装器之前先问系统是否允许本应用安装应用（`canRequestPackageInstalls`），不允许就带去「安装未知应用」里本应用那一页（个别系统没有这一页，退到安全设置），回来后若正在检查，先等检查结束；只有原来请求的版本仍已下载才接着装。发现更高发布版时回到新版本的下载按钮，由用户重新操作。不能指望安装器自己引导：不是每家的安装器都会
- **只从前台打开**：安装器由正开着的更新弹层打开，不从下载的协程里打开。Android 10 起后台启动界面会被直接丢掉，而下载完的时候人可能已经切走了；那时安装包留着，回来点「安装」
- **来历**：权限、前台和厂商安装器这几条，是照着 `local-voice-ime` 的应用更新踩过的问题做的
- **信任在哪**：系统只在覆盖已装应用时才核对签名，换一个包名的 APK 会被当成新应用装上，谁签的都行。所以交给安装器之前，App 先读出下载到的 APK 的包名和 versionCode：包名必须是自己的，版本必须是清单说的那个，否则删掉不装。包名对了，签名就由系统把关：公开仓库或下载途中被人改了，也装不上别人的包。SHA-256 只是为了不把下坏的文件交给系统
- **地址**：清单读最新 Release 的（`releases/latest/download/latest.properties`），APK 按清单里的版本号去它自己的 Release 下（`releases/download/vX.Y.Z/…`），所以看到新版本之后又发了一版，原来那个 APK 仍然下得到。版本号只接受 `X.Y.Z`，文件名只接受 `remote-agent-vX.Y.Z.apk`，两者进地址之前先核对，不能指到别处。Release 的文件由另一个域名经重定向提供；连中继用的 HTTP 客户端不跟随重定向，更新用的那个跟随，但不降到明文 HTTP
- **协议版本**：清单带协议版本，和 App 现在的不一样时在弹层里提醒电脑上的 remote-agent 也要升级；不拦着装，因为先升哪一端由用户定
- **开发版**：包名不同、签名可能不同，也没有对应的发布，不检查更新

## 推送和通知里审批

server 怎么发推送见 [architecture §12](architecture.md#12-推送通知)。

- **Android 订阅恢复**：获取 token 前检查 Google Play 服务是否可用，区分 Google 注册失败和中继服务器登记失败；暂时失败在前台指数退避重试，网络恢复时立即重试。新 token 或通知偏好替换旧重试，进入后台或退出账户时取消，回到前台重新登记。退出账户一开始就摘下账户并停止登记，清理期间的网络恢复、回到前台和新 token 都不再登记它，下一个账户启用后才恢复。后台的新 token 回调只在账户已加载时登记一次，不读取账户密钥，也不持续重试；关闭两类通知时直接清空订阅，不依赖 Google token。退出账户时尽力清空服务器上的订阅；接收端按本地账户和类别开关过滤，账户正在清理或密文打不开时不显示通知。推送故障不阻塞聊天、同步和审批，不显示顶部警告或自动弹窗；只在通知设置分组下方以灰色说明给出原因和可选操作，不显示 Java 异常链。不自动请求通知权限，用户在通知设置里点「开启」时才进入系统设置。重试不能解决手机网络持续无法访问 Google 的问题。
- **内容**：daemon 用 `notify` 密钥加密审批摘要（机器、会话、审批 ID、命令、截止时间），随 `Notify` 交给 server 原样转发。server 和 FCM 只看到密文；FCM 的 payload 上限是 4 KB，放不下时只发通用提醒
- **合并**：审批按审批 ID 去重，不再按「同一会话同一类别 15 秒内只推一次」合并，免得漏掉接连出现的审批
- **显示**：默认只显示通用文案，点开直接进审批页。App 设置里打开「在通知里显示内容并直接审批」后，手机解锁时才显示会话标题、命令和按钮，这样的通知用 `VISIBILITY_SECRET`，锁屏上整条不出现；锁着时收到的仍只发通用文案。`VISIBILITY_PRIVATE` 不够：用户允许锁屏显示敏感内容时它照样全文显示（scope 第 4 节，实测见 [android-notes §4](android-notes.md#4-fcm)）。账户只在前台读取，推送拉起的后台进程只用通知密钥。密文总是随推送下发，开关只在手机上决定怎么显示。App 在前台时不弹通知
- **按钮**：「允许 / 拒绝」设 `setAuthenticationRequired(true)`，先解锁手机才能点；API 31 以下做不到这一点，就不给按钮，点通知进审批页；每条通知用独立、不可变的 `PendingIntent`，只绑定自己那条审批；命令显示不全时，「允许」改为打开完整的审批页
- **执行**：点按钮时在广播里封好指令（请求 ID 和截止时间就此固定），交给 WorkManager 发 `Forward`；发送前同步保存可能已执行的标记，进程被回收后仍按未知结果重试；daemon 确认后记为已完成，这条记录留到 WorkManager 记下任务结束，其间任务被重跑时不再发送，也不会因为过了截止时间报成没提交；先显示「允许中…」，daemon 返回才算完成；结果未知时重试同一个信封，过了截止时间还不知道就如实显示
- **daemon 的校验**：超时和回复都要先抢到审批的 `claimed` 标记，只有一方生效，过期的回复不会被放行。另在审批里存一个绝对截止时间，处理回复时一并检查，作为额外防护
- **投递失败**：推送没送到不影响 daemon 的超时拒绝，审批仍可以打开 App 处理
