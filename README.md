# remote-agent

在任何设备上远程操作跑在自己电脑上的 AI 编程 CLI（Codex 和 Claude Code）。

- **原生 CLI**：agent 以原生交互形态跑在 tmux 里，不走 SDK；电脑终端和手机同时可用
- **端到端加密**：自部署中继服务器只转发和存储密文
- **多设备**：多台电脑各跑一个 daemon，客户端按项目分组查看所有会话

## 组成

| 组件 | 语言 | 说明 |
|---|---|---|
| `remote-agent` | Go | 每台电脑一个，daemon 和本地命令在同一个二进制里：tmux 托管会话、读取 transcript、hook 审批、输入注入 |
| `remote-agent-server` | Go | 部署在 VPS：鉴权、密文转发与存储、推送 |
| `web` | TypeScript（React） | PWA 客户端：聊天视图、终端视图、权限审批 |
| `android` | Kotlin（Jetpack Compose） | Android 原生客户端（开发中）：原生终端，在通知里审批 |
| `ios` | Swift（SwiftUI） | iOS 原生客户端（开发中）：原生终端 |

> 当前进度和下一步见 [docs/plan.md](docs/plan.md)，项目边界见 [docs/scope.md](docs/scope.md)，全部文档见 [docs/README.md](docs/README.md)，开发规范见 [AGENTS.md](AGENTS.md)。

## 本地使用

需要 Go 1.26.9+、tmux 3.2+，以及已登录的 codex 或 claude。

```sh
scripts/install.sh             # 编译到 ~/bin/remote-agent；装过后台服务的话顺便重启 daemon
scripts/daemon.sh install      # 把 daemon 装成后台服务（macOS 用 launchd，Linux 用 systemd --user），开机自启
remote-agent daemon            # 或者：在前台运行 daemon
remote-agent codex             # 另开一个终端：在当前目录启动 Codex 并 attach
remote-agent claude            # 同上，启动 Claude Code
remote-agent ls                # 列出会话
remote-agent send <id> 你好     # 从别处发消息，id 可以只写前几位
remote-agent events <id> -f    # 持续查看事件
```

- 从手机或网页新开的会话由你自己的登录 shell 启动（交互式登录，读 `.zshrc`、`.profile` 等），环境每次现算：升级 nvm、装新命令、CLI 换了位置都会在下一个会话生效，不用重新 `install` 或重启 daemon。从终端用 `remote-agent codex` 开的会话仍用那个终端的环境，里面临时 `export` 的变量有效
- CLI 需要的代理等环境变量写进 `~/.remote-agent/agent.env`（旧的 `codex.env` 仍然有效），一行一个 `KEY=VALUE`，daemon 启动每个会话时都会带上；这里设置的值会覆盖 shell 配置算出来的结果
- 后台服务的日常操作：`scripts/daemon.sh restart|stop|start|status|log`，日志在 `~/.remote-agent/daemon.log`。重启 daemon 不影响正在 tmux 里运行的会话。服务用的是安装时 shell 的 `PATH`：它决定 daemon 自己找到哪个 codex/claude（终端里开的会话、版本检查和 `doctor` 用它），tmux 或 CLI 换了位置就重新 `install` 一次
- 第一次在某个目录启动时，Codex 会问是否信任这个目录、是否信任 hook，需要在终端里回答
- 正式 Release 安装支持 `remote-agent update check`、`remote-agent update` 和 `remote-agent update status`。自动更新默认关闭；`remote-agent update auto enable --time 04:00` 开启本机每日检查，`remote-agent update auto disable` 关闭。需要先用 Release 安装器升级一次以获得这些命令；源码构建不自更新。限制和恢复方式见 [daemon 更新](docs/architecture.md#daemon-更新)。
- **升级 Codex 后运行 `remote-agent doctor`**：它用一个临时会话（GPT-6-Luna，低档位）检查 remote-agent 依赖的各项行为，并记下这个版本已验证

## 连接 server 和网页

```sh
deploy/deploy.sh root@your-vps           # 部署 server，第一次会生成 setup token
scripts/deploy.sh [server|web|all]      # 部署 server 和/或网页，host 取 RA_DEPLOY_HOST
remote-agent account init --server https://ra.example.com   # 建账户，输入 setup token
remote-agent account pair --web https://app.example.com   # 添加设备：显示二维码和配对码
remote-agent account join --server https://ra.example.com --code 12345678   # 在新电脑上加入
cd web && pnpm install && pnpm run deploy                   # 部署网页到 Cloudflare Pages（先 npx wrangler login）
```

- server 前面需要有负责 HTTPS 的反向代理，部署方式见 [docs/architecture.md](docs/architecture.md) 第 1 节
- 本地调试网页用 `pnpm build && pnpm preview`，在 http://localhost:4173 打开
- 账户密钥能解开所有会话，只粘贴到自己的设备上
- daemon 发现 `~/.remote-agent/account.json` 后会自动连上 server，不用重启
- 网页能登录但一直「未连接」时，多半是浏览器的拦截扩展挡住了 WebSocket，见 [docs/browser-notes.md](docs/browser-notes.md)

## 开发

- `go test ./...`：单元测试，不依赖真实的 Codex
- `scripts/acceptance.sh`：用真实的 Codex 跑 P0 验收
- `cd web && pnpm test`：网页的单元测试，包括和 Go 共用的加密测试向量
- 改了 `proto/` 之后运行 `buf generate`（需要 `buf`、`protoc-gen-go`、`protoc-gen-connect-go`），再在 `web/` 里运行 `pnpm gen`；生成的代码在 `gen/` 和 `web/src/gen/`，一并提交
- Android 构建需要 JDK 17 和 Android SDK；Gradle 会按项目的 daemon JVM criteria 选择 JDK 17。找不到时先安装 JDK 17 再构建。
