# 分阶段计划

按风险从高到低推进：先把最依赖实测、最容易出意外的本地这一侧跑通，再接 server、加密和客户端。每个阶段完成后打 tag。

**当前重点是做出 MVP**，也就是完成 P0 到 P2，对应 [scope.md](scope.md) 里「做」的全部内容。P3 以及「不做」里的功能，在 MVP 完成之前都不碰。

| 阶段 | 内容 | 做完后能证明什么 |
|---|---|---|
| **P0 本地打通**（已完成） | 只做 `remote-agent` 这一个二进制：在 tmux 里托管 Codex，注入输入、解析 rollout、处理审批 hook；用本地命令模拟手机 | 最难的一段跑通了 |
| **P1 端到端打通**（已完成） | 定义协议，写 server 并部署到 VPS，接入加密，做一个最简单的网页：看消息、发消息、审批 | 手机上真的能用 |
| **P2 可以日常使用** | 终端视图、切换模型和压缩上下文、按项目分组、推送、配对流程、网页部署到 Cloudflare Pages | 能替代现有的工作方式 |
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
| `remote-agent new [参数]` | 同上，但不 attach，只打印会话 ID |
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

- 使用独立的 tmux socket，每个会话对应一个 tmux session，命名为 `ra-<id前8位>`，完整 ID 记在 session 选项 `@ra_session_id` 里
- pane 里运行 PTY 代理，Codex 跑在它下面（见 architecture 第 6 节）
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

`scripts/acceptance.sh` 用真实的 Codex 检查第 2–7 条（需要已登录的 codex，会消耗少量额度）；第 8 条由 `go test ./...` 覆盖；第 1 条的 attach 体验和第 6 条「本地先答」需要在终端里手动确认。

1. `remote-agent codex` 启动后，终端里的使用体验和直接运行 `codex` 一样；detach 后会话继续在后台运行
2. 用 `send` 发消息，Codex 会执行；同一时间在本地终端里打字也不会出问题
3. `events --follow` 能完整、按顺序地输出一轮对话的所有事件
4. 没开 `auto_review` 的会话里，Codex 请求权限时，`approve` 和 `deny` 都能生效，不回复的话超时后自动拒绝；开着 `auto_review` 的会话里，hook 不表态，由 `auto_review` 处理
5. `interrupt` 能打断正在进行的一轮
6. Codex 提问时，`events` 能看到问题和选项，`answer` 能回答；在本地终端里先答了，`events` 里能看到答案
7. 重启 daemon 后，`ls` 仍能看到原来的会话，事件接着输出
8. rollout 解析有 fixture 测试，样本已脱敏

## P1 端到端打通

### 目标

浏览器经过部署在 VPS 上的 server，能查看已有的 Codex 会话、发消息、审批、回答提问和中断；server 上只有密文，daemon 和客户端断线重连后，历史连续、指令不重复执行。

### 要做的

- **协议**：外层信封、server 的接口和 WebSocket 帧、加密内层的指令，都定义在 proto 里；设计见 [architecture.md](architecture.md) 第 9、10 节
- **server**：`remote-agent-server`，包括设备登记和登录、密文事件的存储和推送、指令转发；用容器部署到 VPS，由现有的 Caddy 反代到 `ra.remote-agent.top`
- **daemon**：`remote-agent account init / join / secret / status`；daemon 连上 server 后上传会话的元数据和事件，执行转发来的指令
- **网页**：`web/`，粘贴账户密钥后显示机器和会话；聊天视图；发消息；审批卡片（允许、拒绝并附理由、剩余时间）；模型提问的选项卡片；中断；结束会话

### 不做

远程新建会话、扫码配对、终端视图、模型切换、按项目分组、推送、网页部署到 Cloudflare Pages，都留到 P2。

### 验收标准

第 1–4 条由 `go test ./...`（`internal/relay`、`internal/server`、`internal/e2e`）和 `cd web && pnpm test` 覆盖；第 5–8 条在本机 Chrome 上对线上 server 和真实 Codex 手动验证（2026-09-29 已通过）。

1. 加密的格式在 Go 和浏览器两边一致：测试向量 `internal/e2e/testdata/vectors.json` 由 Go 生成，网页的测试用它来校验
2. 事件按序号上传、推送；daemon 断线重连后从 server 已有的序号接着传，不丢、不重复
3. 同一个请求 ID 的指令只执行一次，daemon 重启后也一样；过期、被篡改、用别的账户密钥加密的指令都被拒绝；机器离线时立即失败，重连后也不会执行
4. 没有 setup token 不能建账户；不是账户密钥签名的设备不能登记；一台机器不能写另一台机器的会话
5. 网页粘贴账户密钥后能看到机器和会话；发消息后 Codex 执行，回复实时出现
6. 审批：允许后命令执行；拒绝时附的理由 Codex 能看到；不回复的话超时自动拒绝
7. 中断能打断正在进行的一轮；模型提问能在网页上点选回答
8. daemon 停掉时网页显示机器离线、发消息立即失败；期间在终端里的对话，daemon 重启后补到网页上；server 的数据库和日志里搜不到消息、命令、路径和机器名的明文

## 待确认

所有还没定的问题和待实测的行为都列在这里，有了结论就写进对应的文档，再从这里删掉。

**范围和产品**

- 项目判定规则：[architecture.md](architecture.md) 第 8 节的提案是否采用
- 终端视图默认可以输入还是只读（倾向可以输入）
- 客户端显示 Codex 账户额度是否放进 MVP（界面稿里画了）
- 客户端切换审批模式（[architecture.md](architecture.md) 第 7 节）和 fast 模式是否放进 MVP（优先级低）
- 是否允许在手机上切到完全放行（目前的设计是允许，但要二次确认）

**设计**

- 配对流程的报文格式

**P1 遗留**

- 手机实测：P1 只在本机 Chrome 上验证过；手机需要通过 HTTPS 打开网页（Web Crypto 只在安全上下文里可用），等 P2 部署到 Cloudflare Pages 后再测
- 本机 Chrome 装的 AdGuard 会立即拦掉 localhost 页面发往 `ra.remote-agent.top` 的 WebSocket（从 `ra.remote-agent.top` 自己的页面发起则正常，Happy 的 WebSocket 同样被拦）；网页部署到 `app.<域名>` 后还会不会被拦，未验证

**Codex 待实测**

- `Full Access` 对应的配置项
- 切换模型后，rollout 的 `turn_context` 是否要到下一轮才记录新模型
- 运行中能否执行 `/permissions`
- `auto_review` 直接拒绝时，模型是否会在对话里请求批准（目前没复现出来）
- 计划模式怎么结束，能否在手机上批准计划
- 模型提问：多个问题时按编号是否逐题前进；`None of the above` 加备注怎么按；非 Plan 模式下模型会不会用这两个工具
- `[notice]` 下除 `hide_rate_limit_model_nudge` 以外的几个开关是否有效

**Claude Code 待实测**（P3 之前不急）

- auto 模式下，`PermissionRequest` hook 是只收到分类器拦下的请求，还是全部请求
- `ExitPlanMode` 能不能走 hook
- 运行中能否用 Shift+Tab 切换模式
