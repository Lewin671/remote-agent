# 分阶段计划

按风险从高到低推进：先把最依赖实测、最容易出意外的本地这一侧跑通，再接 server、加密和客户端。每个阶段完成后打 tag。

**当前重点是做出 MVP**，也就是完成 P0 到 P2，对应 [scope.md](scope.md) 里「做」的全部内容。P3 以及「不做」里的功能，在 MVP 完成之前都不碰。

| 阶段 | 内容 | 做完后能证明什么 |
|---|---|---|
| **P0 本地打通** | 只做 `remote-agent` 这一个二进制：在 tmux 里托管 Codex，注入输入、解析 rollout、处理审批 hook；用本地命令模拟手机 | 最难的一段跑通了 |
| **P1 端到端打通** | 定义协议，写 server，接入加密，做一个最简单的网页：看消息、发消息、审批 | 手机上真的能用 |
| **P2 可以日常使用** | 终端视图、切换模型和压缩上下文、按项目分组、推送、配对流程、部署到 VPS 和 Cloudflare Pages | 能替代现有的工作方式 |
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

- 通过 control mode 连接独立的 tmux socket，每个会话对应一个 tmux session，命名为 `ra-<id前8位>`；接收输出和退出通知
- 启动时检查 tmux 是否存在、版本是否满足要求
- daemon 重启后，重新接上仍在运行的会话

**Codex adapter**

按 [architecture.md](architecture.md) 第 6 节实现：

- 启动 Codex：环境变量、`RA_SESSION`、用 `-c` 注入 hook 和关掉能关的弹窗
- 处理剩下的启动弹窗，注入前确认没有弹窗挡着
- 注入输入，并确认提交成功
- 通过 `SessionStart` hook 找到 rollout
- 解析 rollout，转成统一事件：文本、推理摘要、工具调用和结果、轮次开始和结束、token 用量
- 判断空闲，多个来源的输入按到达顺序排队注入
- 中断
- 模型提问：把 `request_user_input` 转成统一事件，回答时注入选项编号

**审批**

按 [architecture.md](architecture.md) 第 7 节实现 `remote-agent hook codex`；没开 `auto_review` 的会话，P0 用 `approve` / `deny` 命令回复，超时默认拒绝。

**统一事件**

在 proto 里定义统一事件的格式。它就是以后加密内层的内容，P1 会原样沿用。

### 不做

server、加密、网页、终端视图、按项目分组、推送、开机自启。

### 验收标准

1. `remote-agent codex` 启动后，终端里的使用体验和直接运行 `codex` 一样；detach 后会话继续在后台运行
2. 用 `send` 发消息，Codex 会执行；同一时间在本地终端里打字也不会出问题
3. `events --follow` 能完整、按顺序地输出一轮对话的所有事件
4. 没开 `auto_review` 的会话里，Codex 请求权限时，`approve` 和 `deny` 都能生效，不回复的话超时后自动拒绝；开着 `auto_review` 的会话里，hook 不表态，由 `auto_review` 处理
5. `interrupt` 能打断正在进行的一轮
6. Codex 提问时，`events` 能看到问题和选项，`answer` 能回答；在本地终端里先答了，`events` 里能看到答案
7. 重启 daemon 后，`ls` 仍能看到原来的会话，事件接着输出
8. rollout 解析有 fixture 测试，样本已脱敏

## 待确认

所有还没定的问题和待实测的行为都列在这里，有了结论就写进对应的文档，再从这里删掉。

**范围和产品**

- 项目判定规则：[architecture.md](architecture.md) 第 8 节的提案是否采用
- 终端视图默认可以输入还是只读（倾向可以输入）
- 审批超时的时长
- 客户端显示 Codex 账户额度是否放进 MVP（界面稿里画了）
- 客户端切换审批模式（[architecture.md](architecture.md) 第 7 节）和 fast 模式是否放进 MVP（优先级低）
- 是否允许在手机上切到完全放行（目前的设计是允许，但要二次确认）

**设计**

- 配对流程的报文格式

**Codex 待实测**

- `Full Access` 对应的配置项
- 运行中能否执行 `/permissions`
- `auto_review` 直接拒绝时，模型是否会在对话里请求批准（目前没复现出来）
- 计划模式怎么结束，能否在手机上批准计划
- 模型提问：多个问题时按编号是否逐题前进；`None of the above` 加备注怎么按；非 Plan 模式下模型会不会用这两个工具
- `[notice]` 下除 `hide_rate_limit_model_nudge` 以外的几个开关是否有效

**Claude Code 待实测**（P3 之前不急）

- auto 模式下，`PermissionRequest` hook 是只收到分类器拦下的请求，还是全部请求
- `ExitPlanMode` 能不能走 hook
- 运行中能否用 Shift+Tab 切换模式
