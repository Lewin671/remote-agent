# 分阶段计划

按风险从高到低推进：先把最依赖实测、最容易出意外的本地这一侧跑通，再接 server、加密和客户端。每个阶段完成后打 tag。

**当前重点是做出 MVP**，也就是完成 P0 到 P2，对应 [scope.md](scope.md) 里「做」的全部内容。P3 以及「不做」里的功能，在 MVP 完成之前都不碰。

| 阶段 | 内容 | 做完后能证明什么 |
|---|---|---|
| **P0 本地打通** | 只做 `remote-agent` 这一个二进制：在 tmux 里托管 Codex，注入输入、解析 rollout、处理审批 hook；用本地命令模拟手机 | 最难的一段跑通了 |
| **P1 端到端打通** | 定义协议，写 server，接入加密，做一个最简单的网页：看消息、发消息、审批 | 手机上真的能用 |
| **P2 可以日常使用** | 终端视图、按项目分组、推送、配对流程、部署到 VPS 和 Cloudflare Pages | 能替代现有的工作方式 |
| **P3 以后** | Claude Code 等其他 CLI、附件等 | |

## P0 本地打通

### 目标

不依赖 server 和客户端，只在一台电脑上就能验证：Codex 跑在 tmux 里，外部可以给它发消息、中断它、审批它的权限请求，同时以统一事件的形式拿到它的完整输出。在这期间，本地终端里照常能用 Codex。

### 要做的

**daemon 和本地命令**

| 命令 | 作用 |
|---|---|
| `remote-agent daemon` | 在前台运行 daemon（开机自启放到 P2） |
| `remote-agent codex [参数]` | 在当前目录新建 Codex 会话，并在当前终端 attach 上去；参数原样传给 codex |
| `remote-agent ls` | 列出会话及其状态 |
| `remote-agent attach <id>` | attach 到已有会话 |

下面几个是**模拟手机的调试命令**，P1 接入客户端后，它们对应的就是客户端调用的同一批接口：

| 命令 | 作用 |
|---|---|
| `remote-agent send <id> <文字>` | 发一条消息 |
| `remote-agent events <id> [--follow]` | 查看统一事件，`--follow` 持续输出新事件 |
| `remote-agent approve / deny <id> <审批ID>` | 回复审批请求 |
| `remote-agent answer <id> <提问ID> <选项编号>` | 回答模型提问 |
| `remote-agent interrupt <id>` | 中断当前这一轮 |
| `remote-agent stop <id>` | 结束会话 |

CLI、hook 和 daemon 之间走 ConnectRPC over unix socket，接口在 proto 里定义。

**tmux**

- 使用独立的 socket（`tmux -L remote-agent`），每个会话对应一个 tmux session，命名为 `ra-<id前8位>`
- daemon 通过 control mode 连接 tmux，接收输出和退出通知
- 启动时检查 tmux 是否存在、版本是否满足要求
- daemon 重启后，重新接上仍在运行的会话

**Codex adapter**

- **输入**：用 `paste-buffer -p` 粘贴，再单独发 Enter；以 `~/.codex/history.jsonl` 新增的那一行确认提交成功
- **启动**：启动 Codex 时带上代理等环境变量（daemon 读不到 shell 里的配置），并设置 `RA_SESSION=<会话 ID>`；用 `-c` 注入 `PermissionRequest` 和 `SessionStart` 两个 hook；hook 命令的路径要固定，否则每次都要重新信任
- **弹窗**：启动时用 `-c` 关掉能关的弹窗（见 [scope.md](scope.md) 第 6 节），剩下的 `Trust this folder?`、`Hooks need review` 依次处理；注入前先抓屏，确认没有弹窗挡着。判断时只看屏幕底部几行
- **找到 rollout**：由 `SessionStart` hook 上报 `RA_SESSION` 和 `transcript_path`（发出第一条消息时触发）
- **中断**：只在一轮运行时发 Esc；绝不发 Ctrl-C
- **模型提问**：用 `-c` 再注入一个 `PreToolUse` hook，把 `request_user_input` 转成统一事件；回答时注入选项编号
- **解析 rollout**：转成统一事件，包括文本、推理摘要、工具调用和结果、轮次开始和结束、token 用量
- **空闲判断**：一轮以 `task_started` 开始，以 `task_complete` 或 `turn_aborted`（被中断）结束；屏幕底部的输入提示符作为辅助
- **输入排队**：多个来源的输入按到达顺序排队注入

**审批**

- `remote-agent hook codex` 作为 Codex 的 hook，通过 unix socket 把请求交给 daemon
- 会话开着 `auto_review` 时，hook 不表态，交给 `auto_review`；拿不准的操作，模型会在对话里问你
- 会话没开 `auto_review` 时，daemon 等待外部回复（P0 用 `approve` / `deny` 命令），超时默认拒绝

**统一事件**

在 proto 里定义统一事件的格式。它就是以后加密内层的内容，P1 会原样沿用。

### 要先实测的

- ~~hook 应该装在哪里~~：已验证，启动时用 `-c` 传入，只对该会话生效（见 codex-notes 第 3 节）
- ~~中断一轮该发什么键~~：已验证，运行中发 Esc，输入框草稿保留；绝不发 Ctrl-C（见 codex-notes 第 8 节）
- ~~多个会话怎么对应各自的 rollout~~：已验证，启动 Codex 时设置环境变量 `RA_SESSION`，`SessionStart` hook（发出第一条消息时触发）把它和 `transcript_path` 一起报给 daemon（见 codex-notes 第 3 节）
- ~~审批和 `auto_review` 的配合~~：**已决定先让 `auto_review` 处理，它拒绝的再由你决定**。实测 Codex 的顺序是先 hook、后 `auto_review`，所以做法是：
  - 会话开着 `auto_review` 时，hook 一律不表态，交给 `auto_review`；被拒绝后，模型会在对话里向你说明风险并请求批准，你在手机上直接回复就行
  - 会话没开 `auto_review` 时，hook 把请求转到手机，超时拒绝
  - 已实测：拿不准的操作，模型会在对话里问你，回复确认后 `auto_review` 放行

- 模型提问的细节：多个问题时按编号是否逐题前进；`None of the above` 加备注怎么按；非 Plan 模式下模型会不会用这两个工具

结果记到 [codex-notes.md](codex-notes.md)。

### 不做

server、加密、网页、终端视图、按项目分组、推送、开机自启。

### 验收标准

1. `remote-agent codex` 启动后，终端里的使用体验和直接运行 `codex` 一样；detach 后会话继续在后台运行
2. 用 `send` 发消息，Codex 会执行；同一时间在本地终端里打字也不会出问题
3. `events --follow` 能完整、按顺序地输出一轮对话的所有事件
4. Codex 请求权限时，`approve` 和 `deny` 都能生效；不回复的话，超时后自动拒绝
5. `interrupt` 能打断正在进行的一轮
6. Codex 提问时，`events` 能看到问题和选项，`answer` 能回答；在本地终端里先答了，`events` 里能看到答案
7. 重启 daemon 后，`ls` 仍能看到原来的会话，事件接着输出
8. rollout 解析有 fixture 测试，样本已脱敏
