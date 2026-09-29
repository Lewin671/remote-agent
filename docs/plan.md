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
- **弹窗**：注入前先抓屏，确认没有信任目录、切换模型之类的弹窗挡着
- **找到 rollout**：把会话和它的 rollout 文件对应起来
- **解析 rollout**：转成统一事件，包括文本、推理摘要、工具调用和结果、轮次开始和结束、token 用量
- **空闲判断**：以 `task_complete` 为主，屏幕底部的输入提示符为辅
- **输入排队**：多个来源的输入按到达顺序排队注入

**审批**

- `remote-agent hook codex` 作为 Codex 的 `PermissionRequest` hook，把请求交给 daemon，并等待结果
- 超时默认拒绝

**统一事件**

在 proto 里定义统一事件的格式。它就是以后加密内层的内容，P1 会原样沿用。

### 要先实测的

- ~~hook 应该装在哪里~~：已验证，启动时用 `-c` 传入，只对该会话生效（见 codex-notes 第 3 节）
- 中断一轮该发什么键（Esc 还是 Ctrl-C），以及会不会顺带清掉输入框里的内容
- 多个会话同时运行时，怎样把每个会话和它自己的 rollout 可靠地对应起来。线索：hook 的 payload 里带有 `transcript_path`，还要验证 `SessionStart` hook 是否一启动就提供这个字段
- 审批和 `auto_review` 的配合：**已决定先让 `auto_review` 处理，只把它拿不准的发到手机**。要验证 Codex 能不能做到这一点，做不到的话怎么变通

结果记到 [codex-notes.md](codex-notes.md)。

### 不做

server、加密、网页、终端视图、按项目分组、推送、开机自启。

### 验收标准

1. `remote-agent codex` 启动后，终端里的使用体验和直接运行 `codex` 一样；detach 后会话继续在后台运行
2. 用 `send` 发消息，Codex 会执行；同一时间在本地终端里打字也不会出问题
3. `events --follow` 能完整、按顺序地输出一轮对话的所有事件
4. Codex 请求权限时，`approve` 和 `deny` 都能生效；不回复的话，超时后自动拒绝
5. `interrupt` 能打断正在进行的一轮
6. 重启 daemon 后，`ls` 仍能看到原来的会话，事件接着输出
7. rollout 解析有 fixture 测试，样本已脱敏
