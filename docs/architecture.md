# 架构与技术决策

项目边界见 [scope.md](scope.md)。本文记录各端怎么划分、彼此怎么通信、怎么接入 Codex，以及所有技术决策和理由。Codex 的实测行为见 [codex-notes.md](codex-notes.md)，还没定的问题见 [plan.md](plan.md) 的「待确认」。

## 1. 各端与部署

```
 手机 / 平板 / 浏览器              VPS（海外）                         每台电脑
 ────────────────────             ───────────                         ────────
 PWA / Android App                 Caddy（HTTPS）                      remote-agent（Go）
   │  网页来自 Cloudflare Pages      └─ remote-agent-server（Go）        ├─ daemon
   │  App 侧载安装                      SQLite                          ├─ 本地 CLI 命令
   │                                                                    └─ hook 入口
   └──────── HTTPS / WSS ────────▶ api.<域名> ◀──────── HTTPS / WSS ─────┘
                                                                     tmux ─ codex
```

| 端 | 形态 | 部署 |
|---|---|---|
| `remote-agent` | Go 单二进制：daemon、本地 CLI（`remote-agent codex` 等）和 hook 入口都在里面 | 每台电脑一个 |
| `remote-agent-server` | Go 单二进制，SQLite 存储，不直接对外暴露端口 | VPS 上，前面由 Caddy 负责 HTTPS 和反向代理，域名为 `api.<域名>`（当前部署在 `ra.remote-agent.top`，见下文） |
| `web` | PWA，React + Vite + TypeScript | Cloudflare Pages，挂在子域名 `app.<域名>` 上，用 CNAME 指过去，域名不用迁到 Cloudflare |
| `android` | 原生 App，Kotlin + Jetpack Compose，见第 15 节 | 自己签名的 APK，侧载安装 |

当前的 VPS 上已经有 Happy 用 docker compose 跑的 Caddy（占用 80/443），server 因此也用容器部署：接入 Caddy 所在的 docker 网络，不映射主机端口，Caddyfile 里只加一个站点块反代过去。镜像基于 `scratch`，以 nobody 运行，内存上限 128 MiB。部署文件和脚本在 `deploy/`。

网页部署在 Cloudflare Pages 的项目 `remote-agent` 上（`remote-agent.pages.dev`），自定义域名 `remote-agent.top` 由阿里云 DNS 用 CNAME 指过去。用的是经典的 Pages 而不是 Workers 静态资源：Workers 绑定自定义域名要求域名托管在 Cloudflare，Pages 则支持外部 DNS 用 CNAME 接入。新版 wrangler 默认把 `pages project create` 转成 Workers，所以创建项目时要加 `--force`，之后的部署不用。构建时会生成 `_headers`，让 Pages 在响应头里带上和页面里一致的 CSP，外加 `frame-ancestors 'none'` 等只能靠响应头生效的限制。server 的 `ALLOWED_ORIGINS` 要包含网页的地址。

网页代码**不由 VPS 提供**：如果 VPS 被攻破后能替换网页的 JS，端到端加密就会被绕过。放在另一个平台上，攻击者必须同时攻破两处才行。Cloudflare 账号因此也成了关键资产，必须开启两步验证。

代码放在同一个仓库：一个 Go module，外加 `web/` 和 `android/` 两个目录。

## 2. 报文分层

所有跨网络的消息都分两层，类似信封和信纸：

- **外层（信封）**：发给谁、哪个会话、序号、鉴权。server 靠它来转发和存储，**server 能读**。
- **内层（内容）**：消息、工具调用、审批、终端数据、指令。先用 Protobuf 编码，再加密，**只有客户端和 daemon 能读**。

以手机发一条消息、Codex 回复为例：

```
 手机（PWA）                    server（VPS）                 电脑（daemon）                  Codex（tmux 里）
 ───────────                    ─────────────                 ──────────────                  ────────────────

 ① 用户输入
   "帮我修一下这个 bug"
        │
   [生成内层] 写成内层消息
   [加密]     内层 → 密文
   [生成外层] 装进信封：
     发给 机器A / 会话123
        │
        └──── 外层 ─────────▶ [读外层] 看收件人
                               存密文，转发给机器A
                               （内层打不开）
                                     │
                                     └──── 外层 ─────────▶ [读外层] 找到会话123
                                                           [解密]  密文 → 内层
                                                           [读内层] 拿到文字
                                                                 │
                                                                 └─ tmux 粘贴 + 回车 ─▶ ② Codex 开始干活
                                                                                             │
                                                                                        写 rollout 文件
                                                                                             │
                                                           [读 rollout] ◀────────────────────┘
                                                           [生成内层] "已修改 main.go …"
                                                           [加密]     内层 → 密文
                                                           [生成外层] 装进信封：
                                                             来自 机器A / 会话123，序号 42
                                                                 │
                               [读外层] 存下序号42 ◀──── 外层 ────┘
                               推给所有在线客户端
                                     │
 [读外层] 会话123 有新消息 ◀─ 外层 ──┘
 [解密]  密文 → 内层
 [读内层] ③ 显示 Codex 的回复
```

| 角色 | 外层（信封） | 内层（内容） |
|---|---|---|
| 手机 / 其他客户端 | 生成、读取 | 生成、读取 |
| server | 读取，按它转发、存储 | **碰不到**，只存和转发密文 |
| daemon | 生成、读取 | 生成、读取 |
| Codex | 不涉及 | 不涉及：只知道 tmux 输入进来的文字，以及自己写出的 rollout |

本地 CLI、hook 和 daemon 之间的通信不经过 server，只有一层，不需要加密。

发给 server 自己处理的请求（登录、拉列表和历史）只用到外层字段，返回的会话内容仍是密文。密钥怎么来、报文的具体格式见第 9、10 节；扫码配对的报文格式还没设计，见 plan.md。

## 3. 通信方式

**所有通信都通过 IDL 定义，不手写请求。** IDL 用 Protobuf，工具链用 `buf`；Go 端用 protoc-gen-go 和 connect-go，TS 端用 protobuf-es 和 connect-es。

```
 客户端                              server                             daemon
 ──────                              ──────                             ──────
 ① 普通请求（登录、查列表、拉历史）
    ──── HTTPS（ConnectRPC）────▶  处理并返回

 ② 发给 daemon 的指令（发消息、审批、新建会话…）
    ──── HTTPS，Forward ────────▶  不拆开，原样转发 ── 主 WebSocket ──▶  解密并执行
    ◀────────────── 结果原路返回 ─────────────────────────────────────

 ③ 实时推送（新事件、审批提醒、机器上下线）
    ◀──── 主 WebSocket（一直连着）──  主动推送  ◀──── 主 WebSocket ──── 上报

 ④ 终端画面（只在打开终端视图时）
    ◀──── 终端 WebSocket ────────▶  只转发，不存储 ◀── 终端 WebSocket ─▶  tmux
```

| 链路 | 承载方式 | 理由 |
|---|---|---|
| 一问一答（调 server） | ConnectRPC over HTTPS | 超时、错误码、重试都是现成的；接口由 proto 生成；可以用 curl 调试 |
| 客户端发给 daemon 的指令 | 统一走一个 `Forward` 接口：外层只写目标机器、请求 ID 和密文；具体指令在加密的内层里用 `oneof` 区分 | server 只按机器转发，连"是发消息还是审批"都分辨不出来 |
| 实时推送 | 主 WebSocket，打开 App 就连上，一直保持 | server 需要主动往下推；浏览器里的 ConnectRPC 不支持双向流 |
| 终端 | 单独一条 WebSocket，打开终端视图时建立，离开就断开 | 终端输出量大，和审批提醒挤在一条连接里会把提醒堵住；网络慢时可以单独断开重连，然后重新抓屏幕快照 |
| 本地 CLI、hook ↔ daemon | ConnectRPC over unix socket | 和上面用同一套工具；只有本机进程能连上 |

daemon 和 server 之间同样分主 WebSocket 和终端 WebSocket 两条连接。

## 4. 协议设计要点

以下原则已经定下，具体做法见第 10 节：

- **幂等**：`Forward` 里的指令都带稳定的请求 ID，daemon 记住已经处理过的请求，重试时直接返回原来的结果，不重复注入
- **防重放**：加密时把版本、发送方、目标机器、会话 ID、请求 ID 或序号、截止时间一起纳入认证数据；过期或重复的审批一律拒绝
- **历史和实时衔接**：server 先写库再推送；客户端按序号去重、补缺口
- **终端积压**：终端帧设大小上限，缓冲队列有长度上限；客户端太慢时断开终端流，重新抓快照
- **离线**：daemon 离线时，server 立即返回"不可用"，不替它排队
- **版本**：外层和内层各带协议版本号，遇到无法识别的指令时明确返回版本错误

## 5. 技术决策

| 决策 | 结论 | 理由 |
|---|---|---|
| 语言 | daemon 和 server 都用 Go，全部重写，不复用 Happy 和 botmux 的代码 | 单二进制、内存占用低、方便分发；需要的功能远少于 Happy |
| 参考实现 | Happy（协议、加密、客户端交互）和 botmux（tmux、Codex 的各种坑）**只作参考** | 两者都是 MIT 协议，但它们是 TS 写的，而且范围远大于本项目 |
| 客户端 | 自己写 PWA（React + Vite + TypeScript），部署在 Cloudflare Pages；Android 另做原生 App（第 15 节） | PWA 一套代码覆盖所有设备，不上架应用商店，网页不和 server 放在一起；Android 是主力手机，终端和通知要做到原生体验 |
| 通信 | Protobuf 定义所有接口；一问一答走 ConnectRPC，实时推送和终端各用一条 WebSocket | 见第 3 节 |
| HTTPS | server 只监听本机端口，由 Caddy 负责证书和反向代理 | |
| server 存储 | SQLite | 单用户，部署简单 |
| tmux 接入 | 使用独立的 socket（`tmux -L remote-agent`）；P0 直接执行 tmux 命令（建会话、抓屏、存活检查），终端视图（P2）再接 control mode（`tmux -C`） | 不影响平时用的 tmux；P0 不需要 pane 的字节流，会话存活由 tmux 和 PTY 代理判断 |
| 输入 | tmux pane 里跑一层 PTY 代理（`remote-agent pty-host`），Codex 以原生 TUI 跑在它下面；本地按键和远程注入都经过它，见第 6 节 | 注入时要先确认输入框为空再写入；只靠 tmux 命令，检查和写入之间本地敲的键会混进远程消息 |
| tmux 依赖 | 必需；安装脚本和 `remote-agent` 启动时检查是否存在、版本是否满足 | |
| 输出来源 | Codex rollout jsonl，不从屏幕解析消息 | 屏幕内容不可靠 |
| 审批超时 | 拒绝；默认 10 分钟，`remote-agent daemon --approval-timeout` 可调 | 安全优先；手机上从收到提醒到处理需要时间 |
| 本地状态 | 每个会话一个目录：`session.json`、append-only 的 `events.jsonl` 和待发消息 `queue.json`；事件记录里带着读到的 rollout 位置，同时充当读取进度 | 重启后从记录的位置接着读，事件序号连续、不重复；已受理的消息不丢；单用户、单机，不需要数据库 |
| `/` 开头的输入 | 客户端不直接注入：常用命令做成按钮和面板，其余先提醒再发送 | 带参数的斜杠命令会被当成普通消息发给模型；打开菜单的命令在手机上看不到（[codex-notes §6](codex-notes.md#6-踩过的坑)） |
| 上下文提示 | 占用超过 80% 时提示，并提供「压缩」按钮（注入 `/compact`） | 数据来自 rollout 的 `token_count` |

## 6. 接入 Codex

下面每条做法都基于 [codex-notes.md](codex-notes.md) 里的实测，括号里的「notes §n」指它的第 n 节。升级 Codex 后要跟着复核。

- **启动**：tmux pane 里运行 `remote-agent pty-host -- codex …`。环境变量取自发起的 `remote-agent codex` 进程，再叠加 `~/.remote-agent/agent.env`（放代理等变量，所有 CLI 共用一份；daemon 由 launchd 启动时读不到 shell 里的配置。没有这个文件时读以前的 `codex.env`），并设置 `RA_SESSION=<会话 ID>`、`RA_EXE=<remote-agent 的路径>`。这些变量不经过 tmux 的命令行（命令行对本机所有用户可见，而环境变量里常有 API key），而是存进会话目录下只有自己可读的 `env.json`，供恢复会话时沿用；每次启动另写一份副本交给 PTY 代理，读取后删除。用 `-c` 注入 `PermissionRequest`、`SessionStart` 两个 hook，只对这个会话生效。hook 的定义（命令和 `timeout`）必须固定：Codex 每个 hook 位置只记一份信任，定义一变就要重新信任，而且会顶掉之前的信任。所以命令写成 `"$RA_EXE" hook codex`，不含二进制的路径，换了路径或同时跑测试用的另一份二进制也不用重新信任（notes §3、§5）
- **locale**：daemon 没有 UTF-8 的 locale 时（launchd、systemd 启动就是这样）自己补上 `LC_CTYPE`，tmux 和 Codex 都继承它。否则 tmux 会把输出里的 tab 和非 ASCII 字符换成 `_`：会话列表读不出来，所有会话都会被当成已经消失，屏幕里的 `›` 也认不出来。标成已结束、但 tmux 会话其实还在的会话，daemon 启动时会重新接上
- **恢复会话**：`codex resume <Codex 会话 ID>`，沿用原来的环境变量和参数，去掉位置参数（如第一句话），再用最近一轮的模型、effort 和审批模式覆盖；审批模式只在最近一轮的沙箱、审批策略和审批方与某个标准模式完全一致时才覆盖，免得把只读沙箱之类的更严设置放宽。Codex 不接受重复的 `-m`，命令行的审批选项又比 `-c` 优先，所以被覆盖的选项先从原参数里去掉
- **启动弹窗**：用 `-c` 关掉检查更新、限额换模型提示等能关的弹窗；关不掉的 `Trust this folder?`、`Hooks need review` 每个目录或每套 hook 只问一次，客户端提示去终端处理。能不弹就不弹；信任目录是安全决定，留给用户在终端里做（notes §5）
- **启动时不注入**：Codex 在新目录启动时会先闪一下输入框再弹出信任对话框（notes §10），所以会话处于「启动中」时不注入，等状态检测连续两次看到同样的画面、进入空闲再说
- **防弹窗挡输入**：每次注入前先抓屏，确认没有弹窗挡着。判断画面时只看屏幕底部；以列表里选中的行判断有没有弹窗，标题只用来给弹窗命名，对话里出现同样的字不算（notes §5、§6、§10）
- **等画面**：轮询屏幕，等到预期画面出现再操作，并设超时；不用固定的 sleep（notes §6）
- **输入**：远程消息按到达顺序排队，一次注入一条；队列在受理前写入 `queue.json`，重启后接着发。请求 ID 会记住最近 1000 个，重试不会重复发送。写入前记下 `history.jsonl` 的位置和当前事件序号。注入时 daemon 先让 PTY 代理暂存本地按键，等 80ms 让已经敲下的键显示出来，再抓屏：输入框就绪且没有未发送的草稿，才一次写入 bracketed paste 包起来的正文和回车，然后放开暂存。暂存期间本地敲的键不丢，放开后按顺序进入新的输入框；PTY 是按顺序的字节流，它们一定排在这条消息的回车之后。本地有草稿或有弹窗时，消息继续排队，绝不覆盖或拼接草稿。写入后以 `~/.codex/history.jsonl` 新增一行（已知 Codex 会话 ID 时只认这个会话的行）或 rollout 里出现这条用户消息，确认提交成功（notes §1、§10）。每条消息的状态以事件记录：排队、已提交、无法确认。10 秒内确认不了的记为「无法确认」，**不重发**，因为按键已经送到，重发可能提交两次；写入时恰好 daemon 停了的，重启后同样只等确认、不重发。注入、回答提问和中断共用一把锁，同一时刻只有一个操作使用 PTY 代理；代理也只允许持有暂存的连接写入和放开。正文里的控制字符会被去掉，避免提前结束粘贴
- **判断输入框**：抓屏时保留颜色属性（`capture-pane -e`），按**样式属性**判断，不按转义序列的字面写法：逐个解析行首的 SGR 序列得到加粗、变淡、反色，再看第一个可见字符。Codex 升级常换写法（0.159.0 把 `\e[1m` 换成了 `\e[0;1m`），样式本身不变。输入框的提示符是加粗的 `›`，占位提示是淡色字（前面可能先有背景色设置，判断时跳过开头的样式和空格，看第一个可见字符是不是淡色），列表里选中的行是加粗反色的 `›`。只有看到占位提示才认定输入框是空的：多行草稿的第一行可能是空的，其余行和底部状态栏分不开（notes §10）
- **找到 rollout**：`SessionStart` hook（发出第一条消息时触发）把 `RA_SESSION` 和 `transcript_path` 一起报给 daemon（notes §3）
- **轮次和空闲**：一轮以 `task_started` 开始，以 `task_complete` 或 `turn_aborted`（被中断）结束；屏幕底部的输入提示符作为辅助（notes §1、§8）
- **模型提问**：问题从 rollout 的 `function_call` 读出，它在等待回答时就已经写入，不需要 hook。回答时暂存本地按键、抓屏确认选择框还在，再注入选项编号；以 rollout 的 `function_call_output` 为准，先在终端里答了的就收回卡片。异步提问（`request_user_input_async`）不单独处理，一轮结束后当普通消息回复（notes §9）
- **中断**：只在一轮运行时发 Esc，绝不发 Ctrl-C。空闲时发 Ctrl-C 会清空输入框，再按一次会退出 Codex（notes §8）
- **切换模型和 effort**：空闲时操作 `/model` 菜单：按显示名在屏幕上找模型的编号并按下，在档位列表里用方向键移到目标档位后按 `s`，只改当前会话（回车会改全局默认值）。Max、Ultra 在 `More reasoning…` 子菜单里。以底部状态栏出现新的模型和档位为成功，认不出菜单就按 Esc 退出并报错。rollout 要到下一轮才记录新模型（notes §2），所以 daemon 先记一条 `SettingsChanged`，会话信息立即按它更新，下一轮的 `turn_context` 再校对。可选的模型和档位读自 `~/.codex/models_cache.json`
- **切换审批模式**：询问、自动、完全放行分别是 `/permissions` 菜单的 1、2、3，按数字直接选中；完全放行还要在 `Enable full access?` 里按 1 确认，以屏幕出现 `Permission selection requested` 为成功。计划模式用 Shift+Tab 切换，以状态栏右侧的 `Plan mode` 判断当前状态。运行中也能切，下一轮起生效（notes §4）。同样先记 `SettingsChanged`
- **压缩上下文**：空闲时输入 `/compact`。压缩在 rollout 里是一条 `ContextCompaction`，无论是客户端发起的还是终端里敲的，都解析成「上下文已压缩」事件
- **计划**：Plan 模式下模型给出计划后，rollout 里的 `Plan` 条目解析成 `PlanProposed` 事件，屏幕上的 `Implement this plan?` 让会话处于「计划待批准」，客户端显示计划卡片，回答时注入 1、2 或 3（notes §4）
- **工具开始**：rollout 在调用工具时先写一条 `custom_tool_call`，完成后才有 `item_completed`；前者解析成 `ToolStarted`，客户端据此显示运行中的工具和耗时。两条记录没有共同的 ID，按先后顺序配对
- **菜单操作的共同做法**：和注入一样拿控制锁、暂存本地按键，并在操作期间续期暂存；每一步都先确认画面再按键，等不到预期画面就按 Esc 退出。操作期间暂停状态检测，免得把自己打开的菜单当成弹窗
- **命令参数**：Go 代码调用 tmux 和 codex 时一律传参数数组，不拼 shell 字符串

## 7. 审批

`remote-agent hook codex` 作为 Codex 的 `PermissionRequest` hook，通过 unix socket 把请求交给 daemon。daemon 先读完 rollout，按最近一次 `turn_context` 的 `approvals_reviewer` 判断会话的审批方式：

| 会话的审批方式 | hook 的行为 | 用户看到什么 |
|---|---|---|
| 开着 `auto_review`（Codex 的 `Approve for me`） | 不输出、正常退出，交给 `auto_review` | 手机上不弹审批卡；模型在操作前拿不准时会在对话里问你（已实测），直接回复即可；`auto_review` 直接拒绝后模型会不会来问，未验证 |
| 没开 `auto_review` | 把请求转到客户端，等待回复；超时拒绝 | 审批卡片：允许 / 拒绝，拒绝时可以附一句说明 |

依据：Codex 先调用 hook，hook 不表态时才交给 `auto_review`；hook 只能返回 allow 或 deny 加 message，做不到原生审批框里的「以后不再询问」（notes §3、§4）。

- **hook 的超时**：hook 定义里的 `timeout` 设为 3600 秒，审批超时必须比它短。hook 自己超时的话，Codex 会改为在终端里弹原生审批框（notes §3），就做不到「超时拒绝」了
- **daemon 连不上**：hook 什么都不输出，交给 Codex 在终端里弹原生审批框，仍然由人决定。审批等待中 daemon 重启时，hook 会重试 30 秒，连上新的 daemon 后重新发起审批；旧的那条记为「已取消」

### 客户端的审批模式

客户端的模式切换只做 Codex 和 Claude Code 都有的 4 种，这样接入 Claude Code 时不用改界面：

| 客户端 | Codex | Claude Code |
|---|---|---|
| 询问 | `Ask for approval`（`/permissions`） | manual |
| 自动 | `Approve for me`（`auto_review`） | auto |
| 计划 | Plan mode（Shift+Tab，和权限相互独立） | plan |
| 完全放行 | `Full Access` | bypass（启动时没允许的会话置灰） |
| 不提供 | — | accept edits |

各项的实测情况见 [codex-notes §4](codex-notes.md#4-审批模式) 和 [claude-code-notes.md](claude-code-notes.md)。

- **只做共有的**：审批卡片只有「允许 / 拒绝」加说明；「总是允许」「允许并切到 auto」这类只有一边有的选项不做，需要时去终端
- **「询问」的语义不抹平**：Codex 在工作区内改文件、跑命令本来就不问，只有联网和越界才问；Claude Code manual 每一步都问。客户端按 CLI 实际发来的请求显示
- **Codex 的计划模式**：计划模式和权限是两个开关。选「计划」只打开计划模式、权限不变；选其他三种会先关掉计划模式再切权限
- **以 CLI 为准**：客户端只发「切换」指令，显示的是从 rollout、transcript 或屏幕读回来的结果；在电脑终端里切的，聊天里记一条。终端切到了这 4 种以外的模式（如 accept edits），照实显示
- **完全放行要二次确认**；新会话的默认模式在 daemon 的设置里指定，没指定就沿用 CLI 自己的配置（第 13 节）

## 8. 项目标识

项目按会话工作目录的**绝对路径**判定：路径相同就是同一个项目，不同电脑上路径相同的目录也算同一个项目；项目名是路径的最后一段。分组在客户端完成（`web/src/model.ts`），路径在加密的会话元数据里，server 看不到。同名但路径不同的是两个项目，列表里用路径区分。

## 9. 账户、设备和密钥

一个账户对应一个随机的 32 字节**根密钥**。第一台电脑用 `remote-agent account init` 生成它；server 上只有一个账户，第一次登记设备时必须带上 server 配置里的一次性 setup token，账户建好后 token 就不再起作用。

所有密钥都从根密钥派生，一个用途一把（HKDF-SHA256，info 为 `remote-agent/v1/<用途>`）：

| 用途 | 派生出的密钥 | 用来 |
|---|---|---|
| `account-sign` | Ed25519 账户密钥 | 签设备登记；server 只存它的公钥 |
| `wrap` | AES-256 | 加密会话密钥 |
| `machine-meta` | AES-256 | 加密机器元数据（机器名、系统） |
| `command` | AES-256 | 加密客户端发给 daemon 的指令和返回结果 |
| `notify` | AES-256 | 加密推送里的通知内容，只给 Android App 在后台解密用（第 15 节） |

每个会话另有一把随机的**会话密钥**，加密这个会话的事件和元数据。它存在 daemon 的会话目录里（`relay.key`），用 `wrap` 加密后上传到 server，客户端从 server 取回再解开。

**设备**：每台电脑、每个浏览器和每个 App 都有自己的 Ed25519 设备密钥，设备 ID 是公钥 SHA-256 的前 16 字节。登记时用账户密钥签名，server 用账户公钥验证；登录时设备签 server 给的一次性 challenge，换取 30 天有效的 token。设备登记由账户密钥签名，而不是由一台已登记的设备签名：单用户场景下根密钥本身就是信任根，这样新设备加入时不需要旧设备在线。

**把根密钥交给新设备：配对**。已有设备（网页的「设置 › 添加设备」或 `remote-agent account pair`）显示二维码和 8 位配对码，新设备扫码、输码（网页），或用 `remote-agent account join --code` 加入。实现在 `internal/e2e/pair.go`、`internal/relay/pair.go` 和 `web/src/pairing.ts`，要点：

- **8 位码只用来找到配对**，不参与加密，也不算授权：server 能看到它，熵也只有 26 位多
- **先承诺、后揭示**：已有设备建配对时只交出自己一次性 X25519 公钥的 SHA-256；新设备发出公钥之后，已有设备才揭示公钥，新设备核对它和承诺一致。已有设备的公钥在看到新设备的公钥之前就定了，替换密钥的 server 没法挑选让两边图案碰巧一致的密钥
- **四个 emoji**：双方用 X25519 共享值和整个会话记录（配对 ID、两把公钥、新设备类型）经 HKDF 派生 32 位，映射到 256 个 emoji 里的 4 个。用户核对一致后在已有设备上允许，已有设备才用同一共享值派生的密钥封好根密钥和 server 地址交出去。被替换过密钥的两端算出的图案不同
- **二维码**带上承诺，扫码的新设备直接比对，不只依赖人工核对
- **限制**：配对 5 分钟过期，同一时间只有一个候选设备，每个码最多 3 次加入；错误的码在全局按每分钟限流。配对状态只放在 server 内存里。密文只给持候选令牌的新设备，响应丢了可以重取；新设备加入完成后确认收到，server 随即清掉密文，把配对标为「已交付」再保留一分钟。发起方看到「已交付」才报成功，超时或配对中途消失则提示结果不确定
- 新设备拿到根密钥后，照常用账户密钥签名登记自己

账户密钥串 `ra1.<根密钥>.<server 地址>`（`remote-agent account secret`）仍可作为备用方式粘贴，它不放进 URL，以免留在浏览历史里。

**浏览器怎么存**：设备私钥存成不可导出的 `CryptoKey`（IndexedDB）；根密钥除了不可导出的 HKDF 密钥，还存一份原始字节，因为配对时要把它交给新设备（WebCrypto 的不可导出密钥取不回原始字节）。代价是页面一旦被注入脚本，根密钥能被带走；不过那种情况下脚本本来就能通过页面解密全部内容，差别在于带走后长期有效。配对上线前加入的浏览器没有原始字节，不能发起配对，要重新加入一次。不同的网页来源（如 localhost 和 `app.<域名>`）各有各的存储，换了地址要重新加入。App 怎么存见第 15 节。

## 10. 报文、同步和指令

字段以 `proto/remoteagent/v1/{envelope,relay,remote}.proto` 为准，这里只写约束和理由。

**信封**：内层先编码成 Protobuf，再用 AES-256-GCM 加密。`Header` 写明用途、机器、会话、序号或请求 ID、截止时间和发送方，序列化以后**原样**作为附加数据参与认证，接收方解开后按原始字节解码，从不重新编码。接收方逐项核对 header 和信封所在的位置（哪台机器、哪个会话、第几号、哪个请求），所以 server 没法把一个信封挪到别处。nonce 是 12 字节随机数。server 也会读 header（它是明文），只用来尽早拒绝放错位置的请求，安全性不依赖这一步。

**事件上传**：daemon 连上后先查 server 已有到哪个序号（`ListSessionHeads`），再按序号从后面接着传；server 只接受紧接着的序号，已有的跳过，有空缺的拒绝。重试时同一序号的事件会用新 nonce 重新加密：本地日志里每个序号对应的内容不会变，所以解出来的明文相同，server 保留第一份。每个会话一把密钥，随机 nonce 的数量远低于 GCM 的使用上限。

**客户端同步**：先连上 WebSocket，再拉会话列表和缺的事件，这样两者之间不会漏掉。server 先写库再推送；客户端按序号去重，推送来的事件如果前面有空缺，就先放着，再用 `GetEvents` 补齐。会话和机器的元数据带递增的修订号（header 的 `seq`），客户端只接受更新的版本，server 也只保留最新的。

**指令**：客户端的指令统一走 `Forward`，header 里带 128 位随机的请求 ID 和 60 秒后的截止时间。daemon 的处理规则：

- 截止时间已过的拒绝；比现在晚 10 分钟以上的也拒绝，并提示检查时钟
- 执行前先把请求 ID 记为「开始」并落盘，执行后记下结果，也落盘（`commands.json`）
- 同一个请求 ID 再来，直接返回记下的结果；如果只记了「开始」没有结果，说明上次执行到一半 daemon 停了，返回「结果未知」，**绝不重新执行**
- 记录保留到截止时间过后，因为过了截止时间的指令本来就会被拒绝

server 把指令原样转给那台机器的 WebSocket，最多等 30 秒。机器不在线时立即返回「不可用」，**不排队**；已经转发出去但没等到结果时，返回「可能执行了、也可能没有」，客户端用同一个请求 ID 重试即可。

**WebSocket**：路径 `/v1/stream`，承载二进制 Protobuf 帧。浏览器的 WebSocket 不能自定义请求头，所以第一帧发 token，认证通过前 server 不发任何数据；浏览器连接还要检查 `Origin`；原生 App 不发 `Origin`，server 用的 coder/websocket 对没有 `Origin` 的请求直接放行，App 同样靠第一帧的 token 认证。server 每 20 秒 ping 一次；某个客户端积压超过 256 帧就断开它，它重连后从历史补。每台机器同时只保留一条连接，新连接顶掉旧的。

**大小限制**：单个信封不超过 1 MiB，一次上传不超过 500 个事件，推送帧超过 1 MiB 就拆开发。

**网页**：事件内容一律当文本渲染，不插入 HTML；构建产物带 CSP，只允许运行自己的脚本。网页和 server 要放在同一个注册域名下（`app.<域名>` 和 `ra.<域名>`）：拦截扩展常按「第三方请求」过滤，实测 AdGuard 会拦掉跨站发往 `*.*.top` 的 WebSocket（[browser-notes §1](browser-notes.md#1-adguard-拦截发往-top-的跨站-websocket)）。本地开发遇到这种拦截时，可以用 `RA_PROXY` 让 vite 把请求转发到 server，同时用 `VITE_SAME_ORIGIN_API=1` 构建。

## 11. 终端视图

客户端打开终端时，发一条加密指令 `OpenTerminal`，带上随机的流 ID。客户端和 daemon 各自用这个流 ID 连 server 的 `/v1/term`，server 把两条连接配对后原样转发，**不存储、不解析**：配对要求一端是客户端设备，另一端正是客户端指定的机器；先到的一方最多等 20 秒。

- **画面来源**：daemon 为每条流起一个 tmux control mode 客户端（`tmux -C attach`），它不参与窗口尺寸的计算。先开始接收 `%output` 里这个 pane 的输出，再用 `capture-pane -e` 和光标位置拼出快照，快照之后把收到的输出全部转发。抓屏前后到达的输出可能已经在快照里，会再画一遍；Codex 用绝对坐标绘制，重画不改变结果，而丢掉它们会让画面缺块。窗口尺寸变化时（`%layout-change`，比如电脑上调了终端大小），重新发快照
- **加密**：每一帧用会话密钥封成信封，类型分输出和输入，`request_id` 是流 ID，`seq` 是各自的帧序号，接收方要求序号严格递增，乱序或重放就断开
- **输入**：客户端敲的键经 `tmux send-keys -H` 进入 pane，走的是和本地键盘相同的路，所以注入消息时对本地按键的暂存同样挡住它们
- **尺寸**：默认不改 tmux 窗口，客户端按列数缩小字号；「适配屏幕」才 `resize-window` 到客户端能放下的大小，取消适配或关闭终端时把 `window-size` 设回 `latest`
- **积压**：输出通道满了就结束这条流，客户端重连后拿到新的快照；server 写一侧超过 10 秒写不出去，两边一起断开

## 12. 推送通知

server 首次启动时生成 VAPID 密钥对，存在数据库里。客户端订阅时说明要哪几类通知：「等我处理时」（审批、提问、计划、终端弹窗）和「一轮结束时」。

- **谁决定推送**：server 看不懂事件，由 daemon 在事件上传成功之后调用 `Notify`，只说明会话 ID 和类别。只对两分钟内发生的事件通知，重连后补传旧事件不会推送
- **内容**：只有通用文案（「有会话在等你处理」），外加用于跳转的会话 ID，符合 scope 第 4 节「推送不含具体内容」。同一会话同一类别 15 秒内只推一次
- **客户端**：service worker 收到推送时，如果应用正开着并在前台，就不再弹通知；点通知打开对应会话。iPhone 上要先把网页添加到主屏幕才能订阅推送。推送服务返回 404 或 410 时，server 删掉这个订阅
- **Android**：推送走 FCM，内容是 daemon 加密的密文，通知里可以直接审批，见第 15 节

## 13. daemon 设置与远程新建会话

daemon 的设置存在 `~/.remote-agent/settings.json`，客户端可以读取和修改：审批超时（30 秒到 hook 超时之前）、新会话默认的模型、effort 和审批模式。未设置的项沿用启动参数或 Codex 自己的配置。

- **远程新建会话**：客户端指定机器、目录、第一句话，以及可选的模型、effort、审批模式；daemon 把它们换成 codex 的 `-m` 和 `-c` 参数，第一句话像普通消息一样排队，等 Codex 的输入框出现再发。会话的环境变量用 daemon 自己的，不用客户端的。目录由客户端通过 `ListDirectories` 逐级浏览选择，隐藏目录不列出
- **新会话的配置跟着项目走**：在已有项目里新建时，客户端沿用这个项目最近一个会话的机器、目录、模型、effort 和审批模式，都从会话信息里取，不另外存储，所以换设备也一样。两种模式不沿用：完全放行需要每次二次确认，计划只是一段临时状态；遇到这两种就用机器的默认模式。那台机器不在线时，换这个项目用过的另一台在线机器，并改用那台机器的默认设置。新项目没有会话可沿用，用机器的默认设置。这样新建只需要写第一句话，要改才打开设置
- **机器级指令**：新建会话、列模型、浏览目录、读写设置不属于某个会话，信封头里不带会话 ID；daemon 拒绝带了会话 ID 的机器级指令
- **移除设备**：客户端可以在 server 上删除一台设备：删掉它的登记、登录凭证和推送订阅；如果是机器，连同它的会话和事件密文一起删除，并断开它的连接。单用户模型下这不是吊销：手里还有根密钥的设备可以重新登记

## 14. 适配 Codex 升级

remote-agent 依赖 Codex 的屏幕和 rollout，Codex 升级可能改变它们。做法是让变化尽量不破坏功能，破坏了要能看出来、能查出哪一项：

- **容忍写法变化**：画面按样式属性和文字判断（第 6 节）；菜单按标题和选项文字定位；rollout 里认不出的条目保留为通用工具调用，不报错
- **看得出来**：排队的消息发不出去时，daemon 在会话信息里给出原因（终端有草稿、有弹窗、认不出输入框）；认不出输入框超过 20 秒会记一条带 Codex 版本的警告，网页上提示可能是 Codex 升级改了界面
- **查得出来**：`remote-agent doctor` 用独立的 daemon、状态目录和 tmux，在 `~/.remote-agent/doctor-work` 里以 GPT-6-Luna 低档位真的跑一个会话，逐项检查：认出输入框、区分本地草稿、注入并读回回复、切模型、切审批模式和计划模式、中断、终端快照。失败时附上画面分析的结果（不含内容）
- **记录版本**：daemon 启动时读 `codex --version`，放进机器元数据；代码测试过的版本（`codex.TestedVersions`）和本机 doctor 通过的版本算「已验证」，否则启动日志和网页的机器详情里提示运行 doctor

## 15. Android 客户端

Android 原生 App 要把手机上的体验做到原生水平，范围见 [scope.md](scope.md) 第 5 节，进度和未定的问题见 [plan.md](plan.md)。协议、同步、指令和终端的规则都和网页一样（第 10、11 节），这里只写 App 自己的选型和约束。

### 选型

| 组件 | 选型 | 理由 |
|---|---|---|
| 工程结构 | 纯 Android 工程，分成 `:core` 和 `:app` 两个模块，代码放在 `android/` | 见下文「模块」 |
| UI | Jetpack Compose + Material 3；状态用 Coroutines + StateFlow，事件在一个串行队列里归约 | 返回手势、键盘 insets、动态取色这些原生交互都能直接控制；前台界面和后台通知走同一套逻辑 |
| RPC | connect-kotlin + OkHttp，Connect 协议，二进制 Protobuf | ConnectRPC 官方的 Android 用法 |
| Protobuf | 用 `buf` 从 `proto/` 生成 javalite 和 connect-kotlin 代码 | `proto/` 仍是唯一的协议定义 |
| WebSocket | OkHttp，`/v1/stream` 和 `/v1/term` 各一条 | 和网页相同的两条连接 |
| 加密 | AES-256-GCM 用平台 JCA；X25519、Ed25519、HKDF 用 Bouncy Castle 的 lightweight API | 先保证和 Go、TS 逐字节一致，见下文「加密和密钥」 |
| 本地存储 | Room 存密文信封和同步游标，DataStore 存偏好 | 本地不保存明文 |
| 推送 | 手机有 GMS 用 FCM data message，没有再用 UnifiedPush | 见下文「推送和通知里审批」 |
| 终端 | Termux 的 `terminal-emulator` 和 `terminal-view`，用 `AndroidView` 嵌进 Compose | 原生渲染和输入法都比 WebView 里的 xterm.js 好 |
| 扫码 | CameraX + ZXing core | 本地解码，只扫配对二维码这一种 |
| 构建和分发 | Gradle version catalog 锁定依赖版本；release 构建开 R8，测试也在 release 包上跑；自己签名，侧载安装 | 不上架应用商店 |

### 为什么是纯 Kotlin

| 方案 | 结论 | 理由 |
|---|---|---|
| KMP 共享核心 | 暂不采用 | 价值在于和 iOS 共享，iOS 还没定；connect-kotlin 只发布 JVM 版（2026-09-29 核实），放不进 `commonMain`，只能自己实现 Connect 协议。`:core` 不依赖 `android.*`，以后做 iOS 再迁，业务规则和测试都能保留 |
| gomobile 复用 Go | 暂不采用 | 能直接复用的只有 `internal/e2e`：`relay.JoinByPairing` 按机器登记并写 `account.json`，`remoteclient` 是测试用的客户端，不保存设备私钥；同步引擎和事件归约只有 TS 版，用 Go 也要新写。还要承担：Go 的 TLS 不走 Android 的网络安全策略，系统代理不自动生效，网络切换要由 Kotlin 通知，协程取消要桥接到 `context`，Go 和 JVM 两个堆，以及 NDK、R8、16 KB 页面的构建问题。做 iOS 时和 KMP 一起重新评估 |
| 只把加密交给 Go | 不采用 | 绑定层的固定成本一样不少，复用最少 |
| Flutter、React Native | 不采用 | 终端和系统交互仍要写原生模块；Flutter 多出一套 Dart；RN 也搬不动现有网页，它依赖 WebCrypto、IndexedDB、xterm.js 和 DOM |
| 包装 PWA（TWA、Capacitor） | 不采用 | 界面仍是网页，终端输入、键盘和系统交互达不到原生 |

### 模块

- **`:core`**：纯 Kotlin/JVM，不依赖 `android.*`。包括 proto、RPC 和 WebSocket、加密、配对、同步、事件归约和项目分组，都能在 JVM 上单独测试
- **`:app`**：Compose UI、终端视图、生命周期、Keystore、Room 和 DataStore、FCM 和通知、扫码。通过少量接口使用 `:core`，不把 Android 类型传进去

### 同步和生命周期

同步、指令的规则照搬网页（第 10 节）：先连主 WebSocket，收到 `Ready` 后再拉列表和缺的事件；按序号去重、补缺口；元数据只接受更新的修订号；指令重试沿用同一个请求 ID 和截止时间，结果未知就如实显示。

App 在前台时保持主 WebSocket；进入后台就断开，改靠推送；回到前台重新同步。不靠常驻后台的连接收通知。

### 加密和密钥

- **实现**：Android 13（API 33）起平台 JCA 也有 XDH 和 Ed25519，但导入导出原始 32 字节密钥、从 seed 推出公钥都没验证过，验证通过前继续用 Bouncy Castle。直接用 Bouncy Castle 的 lightweight API，不依赖系统里名为 `BC` 的旧 provider
- **互通**：`:core` 的测试读取 `internal/e2e/testdata/vectors.json`；另加 Kotlin 生成、Go 和 TS 校验的反向向量。要特别对齐的是：header 按原始字节参与认证、密文后接 16 字节 tag、签名上下文末尾的 `\0`、配对 transcript 的长度前缀和 emoji 表、拒绝全零的 X25519 共享值、`uint64` 序号不经过有符号数
- **保管**：在 Keystore 里生成不可导出的 AES 密钥，用它加密根密钥和设备 seed，存在应用私有目录，不进云备份和设备迁移。签名和配对都要原始字节，所以用的时候它们仍会进内存，Keystore 只保护存储。AndroidX `security-crypto` 已弃用，不用 `EncryptedSharedPreferences`
- **通知密钥**：`notify`（第 9 节）单独包装保存，后台收到推送时就能解密；根密钥和 `command` 密钥不因为收通知而在后台解开

### 终端

`terminal-emulator` 和 `terminal-view` 是 Termux 仓库里以 Apache 2.0 发布的两个模块（仓库其余部分是 GPLv3，2026-09-29 核实），只引这两个。`TerminalView` 原本绑定的是本地 PTY 的 `TerminalSession`，要改成接收远程字节流。

尺寸和积压的规则同第 11 节：默认不改 tmux 窗口，键盘弹出、旋转屏幕都不改远端尺寸，只有「适配屏幕」才改；输出跟不上就断开重拿快照，不能丢字节接着显示。

### 推送和通知里审批

- **推送后端**：server 新增按类型登记推送目标的接口，Web Push 和 FCM 并存，旧的 `SetPushSubscription` 留给网页；server 用 FCM HTTP v1 发送，token 失效时删除。没有 GMS 时改用 UnifiedPush，未定，见 plan.md
- **内容**：daemon 用 `notify` 密钥加密审批摘要（机器、会话、审批 ID、命令、截止时间），随 `Notify` 交给 server 原样转发。server 和 FCM 只看到密文；FCM 的 payload 上限是 4 KB，放不下时只发通用提醒
- **合并**：审批按审批 ID 去重，不再按「同一会话同一类别 15 秒内只推一次」合并，免得漏掉接连出现的审批
- **显示**：通知用 `VISIBILITY_PRIVATE`，锁屏上只显示通用文案。是否在通知里显示审批内容未定（scope 第 4 节），不显示就只发通用提醒
- **按钮**：「允许 / 拒绝」设 `setAuthenticationRequired(true)`（API 31 起），先解锁手机才能点；每条通知用独立、不可变的 `PendingIntent`，只绑定自己那条审批；命令显示不全时，「允许」改为打开完整的审批页
- **执行**：在后台发 `Forward`，必要时交给 WorkManager；先显示「处理中」，daemon 返回才算完成；结果未知就如实显示，重试沿用同一个请求 ID
- **daemon 的校验**：超时和回复都要先抢到审批的 `claimed` 标记，只有一方生效，过期的回复不会被放行。另在审批里存一个绝对截止时间，处理回复时一并检查，作为额外防护
- **投递失败**：推送没送到不影响 daemon 的超时拒绝，审批仍可以打开 App 处理

## 16. 接入 Claude Code

Claude Code 和 Codex 走同一条路：原生 TUI 跑在 tmux 的 PTY 代理下，输出读 transcript，审批和轮次边界靠 hook，屏幕只用来判断状态。客户端能做的操作两者一致（第 7 节的 4 种审批模式就是按两者共有的定的）。具体做法依据 [claude-code-notes.md](claude-code-notes.md) 的实测，随实测补全。

- **会话属于哪个 CLI**：会话信息、新建会话、列模型都带 `Agent`；以前的会话没有这个字段，按 Codex 处理。机器元数据列出本机装了哪些 CLI 和版本，客户端只提供装了的
- **daemon 的分层**：会话生命周期、事件序号、持久化队列、控制锁、审批的竞争处理、同步和终端流是共用的；每个 CLI 各自实现三组接口：启动和恢复（参数、版本、模型列表）、事件来源（绑定 transcript、增量解析、确认消息已提交）、终端控制（认输入框、注入、中断、回答提问、计划、切换设置）。屏幕、菜单、参数和 JSON 格式的知识只留在各自的包里
- **环境变量**：和 Codex 共用 `~/.remote-agent/agent.env`（第 6 节），代理这类配置属于电脑，不属于某个 CLI
- **新建会话的默认值**：daemon 设置里 Codex 和 Claude Code 各有一套默认模型、推理强度和审批模式；「配置跟着项目走」（第 13 节）也沿用最近一个会话的 CLI
- **工具开始**：Claude Code 会并行调用工具，`ToolStarted` 带上调用 ID 配对，不按先后顺序
