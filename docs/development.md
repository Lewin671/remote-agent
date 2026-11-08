# 开发与本地使用

从源码构建、在本机运行和调试 remote-agent 的说明，面向开发者和编程 agent。项目介绍、安装和自部署见根目录的 [README](../README.zh-CN.md) 和[自部署指南](self-hosting.zh-CN.md)。

## 组成

| 组件 | 语言 | 说明 |
|---|---|---|
| `remote-agent` | Go | 每台电脑一个，daemon 和本地命令在同一个二进制里：tmux 托管会话、读取 transcript、hook 审批、输入注入 |
| `remote-agent-server` | Go | 部署在 VPS：鉴权、密文转发与存储、推送 |
| `web` | TypeScript（React） | PWA 客户端：聊天视图、终端视图、权限审批 |
| `android` | Kotlin（Jetpack Compose） | Android 原生客户端：原生终端，在通知里审批 |
| `ios` | Swift（SwiftUI） | iOS 原生客户端（开发中）：原生终端 |

> 当前进度和下一步见 [plan.md](plan.md)，项目边界见 [scope.md](scope.md)，全部文档见 [docs/README.md](README.md)，开发规范见 [AGENTS.md](../AGENTS.md)。

## 本地使用

需要 Go 1.26.10+、tmux 3.2+，以及已登录的 codex、claude 或 agy。

```sh
scripts/install.sh             # 编译到 ~/bin/remote-agent；装过后台服务的话顺便重启 daemon
scripts/daemon.sh install      # 把 daemon 装成后台服务（macOS 用 launchd，Linux 用 systemd --user），开机自启
remote-agent daemon            # 或者：在前台运行 daemon
remote-agent codex             # 另开一个终端：在当前目录启动 Codex 并 attach
remote-agent claude            # 同上，启动 Claude Code
remote-agent agy               # 同上，启动 Antigravity
remote-agent ls                # 列出会话
remote-agent send <id> 你好     # 从别处发消息，id 可以只写前几位
remote-agent events <id> -f    # 持续查看事件
```

- 从手机或网页新开的会话由你自己的登录 shell 启动（交互式登录，读 `.zshrc`、`.profile` 等），环境每次现算：升级 nvm、装新命令、CLI 换了位置都会在下一个会话生效，不用重新 `install` 或重启 daemon。从终端用 `remote-agent codex` 开的会话仍用那个终端的环境，里面临时 `export` 的变量有效，启动的也是在那个终端里敲 `codex` 会找到的那个（PATH 里靠前的包装脚本同样生效）；找不到就报错，不会改用 daemon 自己找到的那个
- CLI 需要的代理等环境变量写进 `~/.remote-agent/agent.env`（旧的 `codex.env` 仍然有效），一行一个 `KEY=VALUE`，daemon 启动每个会话时都会带上；这里设置的值会覆盖 shell 配置算出来的结果
- 后台服务的日常操作：`scripts/daemon.sh restart|stop|start|status|log`，日志在 `~/.remote-agent/daemon.log`。重启 daemon 不影响正在 tmux 里运行的会话。服务用的是安装时 shell 的 `PATH`：它决定 daemon 自己找到哪个 codex/claude/agy（`doctor` 用它；版本检查在有会话启动后跟着会话实际跑的那个），tmux 或 CLI 换了位置就重新 `install` 一次
- 第一次在某个目录启动时，Codex 会问是否信任这个目录、是否信任 hook，需要在终端里回答
- 正式 Release 安装支持 `remote-agent update check`、`remote-agent update` 和 `remote-agent update status`。自动更新默认关闭；`remote-agent update auto enable --time 04:00` 开启本机每日检查，`remote-agent update auto disable` 关闭。需要先用 Release 安装器升级一次以获得这些命令；源码构建不自更新。限制和恢复方式见 [daemon 更新](architecture.md#daemon-更新)。
- **升级 Codex 后运行 `remote-agent doctor`**：它用一个临时会话（GPT-6-Luna，低档位）检查 remote-agent 依赖的各项行为，并记下这个版本已验证

## 连接 server 和网页

```sh
deploy/deploy.sh root@your-vps           # 部署 server，第一次会生成 setup token
scripts/ship.sh [vX.Y.Z]                # 我们自己的部署：按顺序上线 server、网页和本机 daemon
remote-agent account join --server https://ra.example.com --code 12345678   # 在新电脑上加入（管理端显示配对码）
cd web && pnpm install && pnpm run deploy                   # 部署网页到 Cloudflare Pages（先 npx wrangler login）
```

- 账户和配对可在网页、Android 或 iOS 客户端里操作：用户先建账户成为唯一管理端，电脑只执行 `account join`。本地端到端产品验收从 `ios/scripts/dev-env.sh start --no-account` 开始，用真实客户端建账户和批准配对；测试夹具或 `internal/remoteclient` 只用于自动回归，不能代替产品链路验收；`account init`、`account pair` 不再是用户入口
- server 前面需要有负责 HTTPS 的反向代理，部署方式见 [docs/architecture.md](architecture.md) 第 1 节
- 本地调试网页用 `pnpm build && pnpm preview`，在 http://localhost:4173 打开
- 会话内容由内容密钥解开；账户根密钥用于账户签名，不再用于解密旧历史，同样只留在自己的设备上
- daemon 发现 `~/.remote-agent/account.json` 后会自动连上 server，不用重启
- 网页能登录但一直「未连接」时，多半是浏览器的拦截扩展挡住了 WebSocket，见 [docs/browser-notes.md](browser-notes.md)

## 开发

- `go test ./...`：单元测试，不依赖真实的 Codex
- `scripts/acceptance.sh`：用真实的 Codex 跑 P0 验收
- `cd web && pnpm test`：网页的单元测试，包括和 Go 共用的加密测试向量
- 改了 `proto/` 之后运行 `buf generate`（需要 `buf`、`protoc-gen-go`、`protoc-gen-connect-go`），再在 `web/` 里运行 `pnpm gen`；生成的代码在 `gen/` 和 `web/src/gen/`，一并提交
- Android 构建需要 JDK 17 和 Android SDK；Gradle 会按项目的 daemon JVM criteria 选择 JDK 17。找不到时先安装 JDK 17 再构建。

## iOS 卡顿诊断

实现和隐私边界见 [iOS 客户端设计](ios-client.md#本机卡顿诊断)。前台主线程超过两秒没有回复时才生成报告，正常运行时目录可能不存在。开发签名的 Release 构建与 Debug 使用同一个容器内相对路径；无需启动或重装 App 即可取回已有文件：

```sh
xcrun devicectl device copy from --device "$RA_TEST_DEVICE" \
  --domain-type appDataContainer \
  --domain-identifier io.github.lewin671.remoteagent \
  --source 'Library/Application Support/Diagnostics' \
  --destination ./chat-diagnostics
```

`RA_TEST_DEVICE` 填目标设备的标识；独立 Debug App 的 bundle identifier 改为 `io.github.lewin671.remoteagent.dev`。此命令需要可访问 App 数据容器的开发签名及配对环境，不承诺 App Store 安装包可这样读取。模拟器可用 `xcrun simctl get_app_container <simulator> <bundle-identifier> data` 找到数据容器后读取相同相对目录。

报告 `format = 1`。`path` 对应代码中的 `ChatDiagnosticPath.rawValue`；`a…d` 是对应 `record` 调用的数值，`nonFinite` 的四个位标记原输入非有限值。`entity` 是进程内小整数，`sequence` 是诊断序号，均不是服务器事件或会话标识。`layoutBegin.a` 指明测量种类，`layout` 配对 begin/end；`mainThread` 区分主线程与后台测量，`mainLayout` 是快照时尚未结束的主线程 scope。时间单位是纳秒，相对于该进程内缓冲的创建；`createdAtUnixMilliseconds` 仅用于文件时间。`startedAt` 是未回复 ping 的投递时间，`recoveredAt` 和 `duration` 在恢复后补入，不代表已经定位卡顿起点或根因。

聊天 collection 的 `.sight` 是合并后的 UIKit viewport 样本（offset、contentSize.height、bounds.height）；`.content` 是内容高度和 `height - offset`，`.floor` 是 viewport 的底边高度，两者都用 collection 内坐标，不是窗口坐标。`.row` 是可见 cell 相对 viewport 的 y、高、宽和列表实体，离开可见区记 `.rowHidden`。`.listLayout` scope 每次 `UICollectionView.layoutSubviews` 都记录，因此即使观测合并，也能从逐路径计数看见布局风暴。`.keep` / `.keepTarget` / `.positionY` / `.kept` 表示 snapshot 前插或 cell 重排时的单次 offset 修正，`.positionID` 表示 UIKit 按行定位；`proxy…` 这些已有路径名保留其操作目的，定位已由 UIKit 执行。主线程 watchdog 和报告格式不变。

模拟器聊天回归：`ChatScrollStressTests.tallRowsAtViewportBoundary` 用 609 pt viewport 和长短高度相差超过 1,000 pt 的正文／代码／表格混排行，测试附件保存数值环缓冲与开始／结束计数。`ChatCollectionTests` 断言跟随、前插、fresh、cell 自适应和宽屏布局；`RemoteAgentUITests/ChatGestureTests` 用 `-demo -demoRoute session/sample-boundary` 的合成数据执行原生拖动／快速滑动、回到底部和输入区收键盘。它们不连接中继或真实 CLI，不替代端到端验收。
