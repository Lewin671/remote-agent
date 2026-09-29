# remote-agent

在任何设备上远程操作跑在自己电脑上的 AI 编程 CLI（先支持 Codex）。

- **原生 CLI**：agent 以原生交互形态跑在 tmux 里，不走 SDK；电脑终端和手机同时可用
- **端到端加密**：自部署中继服务器只转发和存储密文
- **多设备**：多台电脑各跑一个 daemon，客户端按项目分组查看所有会话

## 组成

| 组件 | 语言 | 说明 |
|---|---|---|
| `remote-agent` | Go | 每台电脑一个，daemon 和本地命令在同一个二进制里：tmux 托管会话、读取 transcript、hook 审批、输入注入 |
| `remote-agent-server` | Go | 部署在 VPS：鉴权、密文转发与存储、推送 |
| `web` | TypeScript（React） | PWA 客户端：聊天视图、终端视图、权限审批 |

> 状态：P1 完成，可以在浏览器里经自部署的 server 查看会话、发消息、审批；扫码配对、终端视图、推送等在 P2。项目边界见 [docs/scope.md](docs/scope.md)，全部文档见 [docs/README.md](docs/README.md)，开发规范见 [AGENTS.md](AGENTS.md)。

## 本地使用

需要 Go 1.25+、tmux 3.2+ 和已登录的 codex。

```sh
go build -o ~/bin/remote-agent ./cmd/remote-agent
remote-agent daemon            # 在前台运行 daemon
remote-agent codex             # 另开一个终端：在当前目录启动 Codex 并 attach
remote-agent ls                # 列出会话
remote-agent send <id> 你好     # 从别处发消息，id 可以只写前几位
remote-agent events <id> -f    # 持续查看事件
```

- Codex 需要的代理等环境变量写进 `~/.remote-agent/codex.env`，一行一个 `KEY=VALUE`，daemon 启动每个会话时都会带上
- 二进制的路径要固定：它是 Codex hook 的命令，路径一变 Codex 就会要求重新信任 hook
- 第一次在某个目录启动时，Codex 会问是否信任这个目录、是否信任 hook，需要在终端里回答

## 连接 server 和网页

```sh
deploy/deploy.sh root@your-vps           # 部署 server，第一次会生成 setup token
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
