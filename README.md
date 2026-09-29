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

> 状态：设计阶段。项目边界见 [docs/scope.md](docs/scope.md)，全部文档见 [docs/README.md](docs/README.md)，开发规范见 [AGENTS.md](AGENTS.md)。
