# iOS 客户端的设计

iOS 原生 App 要把 iPhone 和 iPad 上的体验做到原生水平。本文写 App 自己的选型和约束：工程和模块、怎么和其他端保持一致、生命周期、密钥保管、原生终端，以及现在还不能做的部分。协议、同步、指令和终端的规则都和网页、Android 一样，见 [architecture §10](architecture.md#10-报文同步和指令)、[architecture §11](architecture.md#11-终端视图)，这里不重复；范围见 [scope.md](scope.md) 第 5 节，进度和未定的问题见 [plan.md](plan.md)。

## 选型

| 组件 | 选型 | 理由 |
|---|---|---|
| 工程结构 | Swift Package `ios/RemoteAgentCore` 加一个 Xcode 工程 `ios/RemoteAgent.xcodeproj` | 见下文「模块」 |
| 工程文件 | 手写并提交的 `project.pbxproj`，用文件系统同步分组：加一个 Swift 文件不用改工程 | 不必装 XcodeGen，也没有「生成出来的工程和提交的不一样」这回事；工程文件因此很小，可以直接读 |
| UI | SwiftUI；状态用 `@Observable` 的主线程 store，同步引擎是一个 actor，向界面发布值快照 | 导航、sheet、键盘和安全区都用系统的；前台界面只读快照，不和同步抢锁 |
| RPC | connect-swift + URLSession，Connect 协议，二进制 Protobuf | 接口由 proto 生成，不手写请求（[architecture §3](architecture.md#3-通信方式)） |
| Protobuf | 用 `buf` 的远程插件从 `proto/` 生成 swift-protobuf 和 connect-swift 代码，生成物提交进仓库 | `proto/` 仍是唯一的协议定义；本机不用装 `protoc-gen-swift` |
| WebSocket | `URLSessionWebSocketTask`，`/v1/stream` 和 `/v1/term` 各一条 | 和其他端相同的两条连接 |
| 加密 | CryptoKit：AES-256-GCM、X25519、Ed25519、HKDF-SHA256；压缩用系统 zlib 的 raw DEFLATE | 四种原语系统都有，不引第三方密码库；见下文「加密和密钥」 |
| 本地存储 | GRDB（SQLite）存密文信封和同步游标，`UserDefaults` 存不含内容的本机偏好 | 本地不保存明文 |
| 终端 | SwiftTerm 的 `TerminalView`，用 `UIViewRepresentable` 嵌进 SwiftUI | 原生渲染和输入法；它本来就支持由外部喂字节、把按键交给回调，不用像 Android 那样改写终端组件 |
| 扫码 | AVFoundation 扫，Core Image 生成 | 系统自带，本地解码 |
| Markdown | 把网页和 Android 用的那个有界解析器移植成 Swift，原生绘制 | 通用的 Markdown 库是另一种方言，三端要读成一样（[design-system §7](design-system.md)） |
| 系统版本 | iOS 17 起 | Observation 和现在的 SwiftUI 导航都从 17 起可用，不用为旧系统写两套 |

## 为什么是纯 Swift

[android-client.md](android-client.md#为什么是纯-kotlin) 把这个问题留到了做 iOS 的时候。

| 方案 | 结论 | 理由 |
|---|---|---|
| 把 `android/core` 迁到 KMP | 不采用 | `:core` 不依赖 `android.*`，但依赖 JVM：javalite、connect-kotlin 和 OkHttp、Bouncy Castle、JCA、`java.util.zip`，这些都上不了 Kotlin/Native。迁过去等于同时重做 Android 已经在用的传输、生成代码和加密 |
| gomobile 复用 Go | 不采用 | Go 里能直接用的只有 `internal/e2e`；同步引擎和事件归约只有 TS 和 Kotlin 版，用 Go 也要新写，还多一个运行时和一层绑定 |
| 纯 Swift | 采用 | 共享的是协议定义和行为约定，不是代码：见下一节 |

代价是同步和归约有了第三份实现。

## 和其他端保持一致

三份实现靠共用的输入对齐，不靠逐行对照：

- **协议**：`proto/` 生成代码；`ios/scripts/check.sh` 重新生成一遍，有差异就失败
- **加密**：Swift 的测试读 `internal/e2e/testdata/` 下 Go 和 Kotlin 写的向量（`vectors.json`、`v2-vectors.json`、`v3-vectors.json`、`kotlin-vectors.json`），以及 `internal/deviceauth/testdata/domain-v3.json` 里的成员状态链、grant 和 `PrevSecret` 走链。同一个 nonce 下封出的信封和 Kotlin 的逐字节相同
- **同步**：Swift 的测试构建并启动本仓库的 Go server，登记、登录、分页、补缺口、配对、带签名的指令和拒绝重定向都对着它跑；没有 Go 时这些测试跳过
- **归约和列表规则**：Android `:core` 的测试用例逐个移植。Kotlin 依赖 JVM 语义的地方（UTF-16 长度、排序的稳定性、`LinkedHashMap` 的顺序）在 Swift 里照着结果做，并各有测试
- **Markdown**：读 `testdata/markdown/cases.json`，和网页、Android 同一份用例
- **协议版本**：`RACore/Version.swift` 里的值由测试对照 Go、网页和 Android 的那三处

CryptoKit 的 Ed25519 签名带随机盐，同一条消息两次签名不同，所以签名只核对「能验过」，不比字节；对端本来也只验签。

## 模块

- **`RACore`**（Swift Package）：不依赖 UIKit，能在 Mac 上用 `swift test` 跑。包括 RPC 和 WebSocket、加密、配对、同步、事件归约、项目分组、图片的编码和分块上传，以及从界面层抽出来的纯逻辑（聊天行、输入区、项目页的配置）
- **`RAProto`**：生成的协议代码，类型加 `PB` 前缀，和 Foundation、`RACore` 自己的名字分开
- **`RAMarkdown`、`RAMarkdownUI`**：解析器和它的 SwiftUI 绘制
- **App**（`ios/RemoteAgent`）：界面、终端视图、生命周期、钥匙串、GRDB、扫码

## 同步和生命周期

规则照搬 Android（[android-client · 同步和生命周期](android-client.md#同步和生命周期)）：前台保持主 WebSocket，进入后台关闭主连接和终端连接，回到前台重新认证、追赶历史。iOS 上还有几处要另外说明：

- **actor 的重入**：actor 在 `await` 处会让出，所以每一步归约都同步完成，每条连接带一个代号，换掉的连接回调不能改状态，`await` 之后重新确认自己还是当前连接
- **ping**：URLSession 没有定时 ping，改为每 20 秒发一次，下一次到点时上一个 pong 还没回就断开重连。pong 只在有读取挂起时才送达，主连接一直在读，不受影响
- **超时**：URLSession 没有单独的建连超时。登录和开流有 15 秒上限，其余请求连不上要等 45 秒的请求超时
- **重定向**：session 级的 delegate 对 RPC、普通请求和 WebSocket 握手一律不跟随（[architecture §9](architecture.md#9-账户设备和密钥)）
- **本机有没有网**：`NWPathMonitor` 的结果当作线索，不当作结论；没网时 URLSession 直接报未联网而不去解析，这几种错误和解析失败一样算本机网络不通（[architecture · 断线时显示什么](architecture.md#断线时显示什么)）
- **应用切换器**：进入后台前盖住窗口，系统截下的快照里没有解密后的内容

## 加密和密钥

- **保管**：根密钥（只有建账户或从旧版本保留根材料的设备才有）、设备 seed、server 地址和设备 ID 合成一个钥匙串条目，一起写入；通知密钥和登录 token 各一个条目。都是 `WhenUnlockedThisDeviceOnly`、不同步：锁屏时读不到，不进 iCloud 钥匙串，也不随备份迁到另一台设备。写不进去就报错，不退到别的存储
- **成员状态**：固定安全域、完整状态链、当前 head、独立签名租约、独立交换私钥及时间下界单独以 `WhenUnlockedThisDeviceOnly` 保存；跨进程锁下先持久更新 head、租约和时间下界；发送使用随机 nonce。历史内容密钥按内容时期缓存，从状态链的 `PrevSecret` 解出；遇到没有 `PrevSecret` 的恢复边界，更早的内容标为不可读、不重试。通知只持有 `notify` 子密钥。缺失状态链时用手机重新配对，保留旧身份；损坏则拒绝覆盖，见 [architecture §9](architecture.md#9-账户设备和密钥)。
- **不是 Secure Enclave**：协议要原始的 32 字节（设备 seed 用来签信封，根密钥只用于旧历史），所以这些密钥用的时候在内存里，钥匙串只保护存储，和 Android 的 Keystore 包装是同一个程度
- **密文缓存**：数据库、WAL 和图片文件设了「首次解锁后可用」的文件保护，排除在备份之外；每个账户一个库，退出账户时先删钥匙串条目，再删缓存和待发图片。钥匙串条目删掉之后，缓存和待发图片各删各的，一处失败不跳过另一处；清理结束前不读取账户，清理没做完时「重试」先把它做完，再看本机有没有账户

## 终端

尺寸和积压的规则同 [architecture §11](architecture.md#11-终端视图)：进入终端时自动适配一次，之后键盘弹出、旋转屏幕都不改远端尺寸；输出跟不上就断开重拿快照，不能丢字节接着显示。和 Android 一样不执行终端输出里的 OSC 52 剪贴板写入；链接、响铃、标题和通知序列也都忽略，终端输出里的图片一律不解码也不显示：iTerm2 的内联图片（OSC 1337）由 App 接走，Sixel 和 Kitty 图形所在的控制串（DCS、APC）在交给仿真器之前就滤掉。

- **尺寸**：SwiftTerm 的行列数由视图的大小推出，所以 App 把视图定成远端的行列数，再把它缩到放得进没有键盘时的区域。缩的是视图而不是字体：SwiftTerm 1.11.2 一换字体就软复位终端，程序设的方向键模式、滚动区域和当前样式都会丢，而远端不会重发。字体只在快照到来时换成所选字号（快照本来就整个重置），其间旋转屏幕、调字号都只缩放视图，并按缩放后的比例绘制，文字不糊；只有制表符线条在缩放时不再对齐像素。键盘是否弹出按视图在窗口里的位置判断，不靠键盘通知：键盘已经在场时才创建的视图收不到通知
- **字号**：10 到 22 pt 可调，默认 14，存在本机；已适配时调字号会重新适配
- **字体**：系统的 SF Mono，中文和符号走系统回退，不打包字体
- **仿真器的应答不发回远端**：状态查询、焦点和鼠标报告这些由仿真器自己生成的字节一律丢掉。程序面对的终端是电脑上的 tmux，它已经答过；再答一次会被当成敲进去的字，带回显的查询还能让输出替人打字。代价是程序开了鼠标模式时点按不上报。网页和 Android 目前会发回去
- **按键栏**：和 Android 相同的一排：Esc、方向键、回车、Tab、Ctrl+C、键盘

SwiftTerm 固定在 1.11.2：1.12 起带 Metal 着色器，构建需要 Xcode 另装的 Metal Toolchain 组件；1.19 起带构建插件，命令行构建要跳过插件校验。升级前先决定接不接受这两项。

## 还不能做的

这几项都卡在 Apple 开发者账号上，做不做、怎么做未定，见 [plan.md](plan.md) 的「待确认」：

- **装到真机**：这台开发机上没有签名身份，App 只在模拟器上运行过。免费的个人签名 7 天过期，不适合日常用
- **推送**：要用 APNs，server 要加一种推送目标和 APNs 的凭据。没做之前 iOS 只能打开 App 查看
- **在通知里审批**：iOS 的通知内容一旦交给系统，锁屏上显不显示由系统的预览设置决定，没有 Android `VISIBILITY_SECRET` 那样「锁屏上整条不出现」的办法（[scope.md](scope.md) 第 4 节的硬性规则）。要做得先决定接不接受这一点
- **App 更新**：iOS 不能自己下载安装包安装，Android 的更新流程没有对应物，跟着分发渠道走
