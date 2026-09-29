# 分阶段计划

按风险从高到低推进：先把最依赖实测、最容易出意外的本地这一侧跑通，再接 server、加密和客户端。每个阶段完成后打 tag。

MVP（P0 到 P2，对应 [scope.md](scope.md) 里「做」的全部内容）已经完成。**当前并行推进三件事**：Android 原生 App、接入 Claude Code、网页按界面稿 v1 改版；P3 以及「不做」里的功能先不碰。

| 阶段 | 内容 | 做完后能证明什么 |
|---|---|---|
| **P0 本地打通**（已完成） | 只做 `remote-agent` 这一个二进制：在 tmux 里托管 Codex，注入输入、解析 rollout、处理审批 hook；用本地命令模拟手机 | 最难的一段跑通了 |
| **P1 端到端打通**（已完成） | 定义协议，写 server 并部署到 VPS，接入加密，做一个最简单的网页：看消息、发消息、审批 | 手机上真的能用 |
| **P2 可以日常使用**（已完成） | 终端视图、切换模型和压缩上下文、按项目分组、推送、配对流程、网页部署到 Cloudflare Pages | 能替代现有的工作方式 |
| **A Android 原生 App**（进行中） | 纯 Kotlin 的原生客户端：原生终端、富通知、在通知里审批 | 手机上的体验达到原生水平，手机上不再需要 PWA |
| **C 接入 Claude Code**（进行中） | daemon 抽出每个 CLI 的接口，Claude Code 和 Codex 同等支持 | 在手机上操作 Claude Code 和操作 Codex 没有区别 |
| **W 网页按 v1 改版**（进行中） | 按 [design/client-v1.html](design/client-v1.html) 改网页，手机和宽屏一起改 | 网页和界面稿一致 |
| **P3 以后** | 附件等 | |

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

## P2 可以日常使用

### 目标

不靠电脑终端也能完成日常的全部操作：新建会话、看终端、切换模型和审批模式、压缩上下文、批准计划、收推送、用扫码加入新设备；网页按界面稿做，支持亮色和暗色。

### 做了什么

设计见 [architecture.md](architecture.md) 第 6、7、8、9、11、12、13 节。

- **会话**：从网页新建（机器、目录浏览、模型、effort、审批模式、第一句话）；取消排队中的消息；运行中的工具显示耗时；终端里直接发的消息和切换的模式都在聊天里记一笔
- **会话设置**：切换模型和 effort、审批模式（询问、自动、计划、完全放行，后者二次确认）；上下文超过 80% 提醒，可以压缩；计划卡片可以批准、继续规划或清空上下文后执行
- **终端视图**：实时画面，可以输入，手机上有特殊按键栏，可以「适配屏幕」；宽屏可以和聊天并排
- **项目**：按工作目录的绝对路径分组，项目名是最后一段
- **推送**：等我处理时、一轮结束时两类，通知只有通用文案；网页可以装成 PWA
- **设备**：扫码或输码配对，核对四个 emoji；设置页有机器详情（Codex 额度、新会话默认值、审批超时、项目）、移除设备
- **网页**：界面按 [design/client-v0.html](design/client-v0.html)；亮色、暗色、跟随系统；部署在 Cloudflare Pages（`remote-agent.top`）
- **修复**：会话的环境变量不再出现在 tmux 命令行上；在真彩色终端里 attach 过的会话也能识别空输入框

### 验收标准

第 1–5 条由 `go test ./...` 和 `cd web && pnpm test` 覆盖；第 6–9 条在本机 Chrome 上用真实 Codex（GPT-6-Luna，低档位）手动验证（2026-09-29 已通过）。

1. 模型、审批模式、计划、压缩的菜单操作每一步都先确认画面，认不出就按 Esc 退出；有草稿时不动输入框（`internal/daemon/control_test.go`，按实测画面模拟 Codex）
2. 终端流：快照、输出、输入、调整尺寸；重放的帧被拒绝；终端内容不进 server 的数据库（`TestTerminalStream`）
3. 配对：两端 emoji 和密钥一致；被替换的承诺在揭示前就被发现；拒绝后拿不到密钥；码的次数和全局限流；Go 和网页两边的计算按测试向量一致
4. 推送只发给订阅了对应类别的设备，相同通知合并，失效的订阅被删除；移除的机器连同会话一起删除、登录失效
5. 超大的事件被截断而不是卡住同步；取消排队的消息后它不会再写入终端
6. 从网页新建会话；在终端视图里用按键栏确认信任目录后，第一句话自动发出
7. 在网页上切换模型和 effort、审批模式、进出计划模式，Codex 的菜单和状态栏随之变化，`~/.codex/config.toml` 不变；计划卡片批准后 Codex 开始执行；压缩后上下文从 3% 降到 0%
8. 电脑发起、浏览器扫码加入，以及浏览器发起、电脑输码加入，两个方向都成功，两端显示的 emoji 相同
9. 暗色和亮色下各页面和界面稿一致；从 `remote-agent.top` 连 server 不受 AdGuard 影响

## A Android 原生 App

### 目标

在自己的 Android 手机上完成 PWA 能做的全部操作，终端是原生视图；审批请求以通知显示，解锁后可以直接在通知里允许或拒绝。选型和设计见 [architecture.md](architecture.md) 第 15 节。

### 步骤

先做体验风险最大的终端，再做正确性风险最大的加密，最后做依赖真机的推送。

| 步骤 | 内容 | 验收 |
|---|---|---|
| A1 终端原型 | `android/` 工程骨架；Termux 终端视图接远程字节流；登录先用账户密钥串 | 真机连上真实的 Codex 会话：中文输入法、特殊按键、键盘弹出、旋转屏幕都正常，默认不改 tmux 尺寸 |
| A2 `:core` 加密和登录 | 加密、签名、配对的 Kotlin 实现；ConnectRPC 和主 WebSocket | `vectors.json` 和反向向量在 Go、TS、Kotlin 三边都通过；能注册、登录线上 server，拉到会话和事件 |
| A3 日常功能 | 扫码配对；会话列表、聊天、发消息、审批卡片、提问、中断；新建会话；切换模型和审批模式；Room 缓存 | PWA 能做的操作都能做；断网重连后事件不丢不重，指令不重复执行 |
| A4 推送和通知里审批 | server 的 FCM 发送和推送目标登记；daemon 生成 `notify` 密文、审批存绝对截止时间；App 的富通知和审批按钮 | 进程被系统回收后仍能收到通知；锁屏只显示通用文案；解锁后在通知里审批生效；过期、重复的审批被拒绝 |
| A5 完整界面 | 按 [client-v1](design/client-v1.html) 的手机画面补齐所有页面，交互照 Material 3 和 Android 的返回、键盘习惯，亮色和暗色 | 和界面稿一致；真机连续使用一天没有 ANR 和崩溃 |

### 不做

iOS 和桌面原生 App、上架应用商店。

## C 接入 Claude Code

### 目标

Codex 能做的，Claude Code 都能做：本地 `remote-agent claude`、远程新建和恢复、聊天、发消息、审批、模型提问、计划、切换模型和审批模式、压缩、中断、终端视图。设计见 [architecture.md](architecture.md) 第 16 节。

### 步骤

| 步骤 | 内容 | 验收 |
|---|---|---|
| C1 实测 | 按当前版本实测启动、transcript、hook、输入框、弹窗、模式、模型、中断、提问、计划，写进 [claude-code-notes.md](claude-code-notes.md) | 每一项有版本和证据等级 |
| C2 抽接口 | 把 Codex 的做法收进 architecture 第 16 节的三组接口，daemon 只依赖接口 | Codex 的测试和 `scripts/acceptance.sh` 全部照常通过 |
| C3 Claude Code adapter | 实现三组接口；transcript 解析有脱敏的 fixture 测试；`remote-agent doctor` 同样检查 Claude Code | 用真实的 Claude Code 走一遍 P0 的验收标准 2–7 |
| C4 客户端 | 网页和 App 新建会话时选 CLI，文案按会话的 CLI 显示 | 在网页和 App 上各完整操作一次 Claude Code 会话 |

C1–C3 已完成。C3 的验收在本机用真实的 Claude Code 2.1.284（haiku）手动走了一遍，全部通过（2026-09-29）：send 和本地草稿时排队、`events --follow` 按顺序输出一轮、approve / deny 附理由 / 超时拒绝、终端里先答时收回卡片、interrupt、单问 / 两问 / 多选提问的远程回答和本地先答、计划的远程批准、切换模型和审批模式（不可用的模式报错并回到原模式）、压缩、stop 后 resume 接着同一段对话、重启 daemon 后序号接着走；`remote-agent doctor claude` 全部通过。Codex 这边 `scripts/acceptance.sh` 照常通过。还没有 Claude Code 版的 `scripts/acceptance.sh`。

C4 要用到的协议变化：`Session.agent`、`mode`、`permission_mode`、`full_access_unavailable`；`Question.multi_select` 和 `AnswerQuestionRequest.options`；`APPROVAL_DECISION_TERMINAL`；`TurnContext.permission_mode`；`MachineMeta.agents`；`ListModelsRequest.agent`；`DaemonSettings.claude`。Claude Code 不支持计划的「清空上下文再执行」，没有账户额度，haiku 没有推理强度，也没有 auto 模式。

## W 网页按 v1 改版

按 [design/client-v1.html](design/client-v1.html) 改网页，手机和宽屏一起改，亮色和暗色都要一致。验收：界面稿里的每个画面和状态集在本机 Chrome 上逐一对照；`cd web && pnpm test` 覆盖状态优先级、停靠区顺序、消息发送状态和 Markdown 渲染。

**进度**（2026-09-29）：界面稿的全部画面和状态集已实现，也包括 C4 的网页部分（文案按会话的 CLI、新建时选 CLI、模型按 CLI 列、设置里两套默认值、`ToolStarted` 按调用 ID 配对）。规则在 `web/src/model.ts`，单元测试在 `web/src/*.test.ts(x)`。

- 开发时 `pnpm dev` 后打开 `/?demo`，用假数据看各个状态，不连 server，不进构建；`&conn=`、`&term=closed`、`&empty=` 切换连接、终端和空账户的情况（见 `web/src/dev/demo.ts`）
- 本机 Chrome 上逐屏对照过 390 宽和 1280 宽、亮色和暗色；用真实的 daemon 和 Codex（GPT-6-Luna，低档位）走过：配对加入、在项目里新建、信任目录的弹窗停靠后去终端处理、适配和还原终端、发消息、结束和恢复、切换审批模式
- Claude Code 会话在网页上用真实的 Claude Code 2.1.284（haiku）完整走过一遍（2026-09-29，隔离的 daemon 加入同一账户）：新建时选 CLI 和模型、信任目录的弹窗去终端处理、收发消息、审批允许和附理由拒绝、单选和多选提问、切到计划并批准、切换模型、压缩、中断、结束和恢复（恢复后接着同一段对话）、在终端里切到 accept edits 后照实显示、切回询问；「完全放行」按会话信息置灰，haiku 的自动模式和推理强度不提供。审批停靠区因此也有了真实数据的验证
- 还没验证：手机真机上的键盘弹出；Android App 上的 C4

## 待确认

所有还没定的问题和待实测的行为都列在这里，有了结论就写进对应的文档，再从这里删掉。

**范围和产品**

- 客户端切换 fast 模式是否要做（优先级低）

**Android App**

- 手机的型号和系统版本：决定 minSdk；`setAuthenticationRequired` 要 API 31 起
- 手机上有没有 GMS：有就用 FCM，没有要改用 UnifiedPush，并多部署一个 distributor
- 是否接受审批内容进入系统通知（可能留在通知历史里、被有通知读取权限的 App 读到）：不接受的话，A4 只发通用提醒
- 以后做不做原生 iOS：做的话，再比较把 `:core` 迁到 KMP 和改用 gomobile 复用 Go
- 待验证：connect-kotlin 和现有 server 能否直接互通；Termux 的 `TerminalView` 改接远程字节流的工作量；平台 JCA 能否导入导出原始 32 字节的 X25519、Ed25519 密钥；FCM 冷启动时解密并发出通知的耗时

**P1、P2 遗留**

- 手机实测：终端视图的按键栏、推送（iPhone 要先添加到主屏幕）、扫码配对都还只在本机 Chrome 上验证过，要在真手机上走一遍

**Codex 待实测**

- `auto_review` 直接拒绝时，模型是否会在对话里请求批准（目前没复现出来）
- 模型提问：多个问题时按编号是否逐题前进；`None of the above` 加备注怎么按；非 Plan 模式下模型会不会用这两个工具
- `[notice]` 下除 `hide_rate_limit_model_nudge` 以外的几个开关是否有效

**Claude Code 待实测**

- auto 模式下被分类器拦下的请求会不会弹权限框、触发 `PermissionRequest`（分类器放行的不触发，已实测；拦下的情况没能复现）
- 以 bypass 启动时的风险确认弹窗（本机设置跳过了它）
- API 出错时一轮怎么结束：transcript 里有没有 `turn_duration` 或其他结束记录；没有的话会一直显示运行中
- AskUserQuestion 的选择框里按 Esc、选 `Type something.` 和 `Chat about this` 的结果
