# 架构与技术决策

项目边界见 [scope.md](scope.md)。本文记录各端怎么划分、彼此怎么通信、怎么接入 Codex，以及所有技术决策和理由。Codex 的实测行为见 [codex-notes.md](codex-notes.md)，还没定的问题见 [plan.md](plan.md) 的「待确认」。

## 1. 各端与部署

```
 手机 / 平板 / 浏览器              VPS（海外）                         每台电脑
 ────────────────────             ───────────                         ────────
 PWA（React）                      Caddy（HTTPS）                      remote-agent（Go）
   │  网页代码来自                   └─ remote-agent-server（Go）        ├─ daemon
   │  Cloudflare Pages                  SQLite                          ├─ 本地 CLI 命令
   │                                                                    └─ hook 入口
   └──────── HTTPS / WSS ────────▶ api.<域名> ◀──────── HTTPS / WSS ─────┘
                                                                     tmux ─ codex
```

| 端 | 形态 | 部署 |
|---|---|---|
| `remote-agent` | Go 单二进制：daemon、本地 CLI（`remote-agent codex` 等）和 hook 入口都在里面 | 每台电脑一个 |
| `remote-agent-server` | Go 单二进制，SQLite 存储，不直接对外暴露端口 | VPS 上，前面由 Caddy 负责 HTTPS 和反向代理，域名为 `api.<域名>`（当前部署在 `ra.remote-agent.top`，见下文） |
| `web` | PWA，React + Vite + TypeScript | Cloudflare Pages，挂在子域名 `app.<域名>` 上，用 CNAME 指过去，域名不用迁到 Cloudflare |

当前的 VPS 上已经有 Happy 用 docker compose 跑的 Caddy（占用 80/443），server 因此也用容器部署：接入 Caddy 所在的 docker 网络，不映射主机端口，Caddyfile 里只加一个站点块反代过去。镜像基于 `scratch`，以 nobody 运行，内存上限 128 MiB。部署文件和脚本在 `deploy/`。

网页部署在 Cloudflare Pages 的项目 `remote-agent` 上（`remote-agent.pages.dev`），自定义域名 `remote-agent.top` 由阿里云 DNS 用 CNAME 指过去。用的是经典的 Pages 而不是 Workers 静态资源：Workers 绑定自定义域名要求域名托管在 Cloudflare，Pages 则支持外部 DNS 用 CNAME 接入。新版 wrangler 默认把 `pages project create` 转成 Workers，所以创建项目时要加 `--force`，之后的部署不用。构建时会生成 `_headers`，让 Pages 在响应头里带上和页面里一致的 CSP，外加 `frame-ancestors 'none'` 等只能靠响应头生效的限制。server 的 `ALLOWED_ORIGINS` 要包含网页的地址。

网页代码**不由 VPS 提供**：如果 VPS 被攻破后能替换网页的 JS，端到端加密就会被绕过。放在另一个平台上，攻击者必须同时攻破两处才行。Cloudflare 账号因此也成了关键资产，必须开启两步验证。

代码放在同一个仓库：一个 Go module，外加 `web/` 目录。

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
| 客户端 | 自己写 PWA（React + Vite + TypeScript），部署在 Cloudflare Pages | 一套代码覆盖所有设备；不上架应用商店；网页不和 server 放在一起 |
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

- **启动**：tmux pane 里运行 `remote-agent pty-host -- codex …`。环境变量取自发起的 `remote-agent codex` 进程，再叠加 `~/.remote-agent/codex.env`（放代理等变量；daemon 由 launchd 启动时读不到 shell 里的配置），并设置 `RA_SESSION=<会话 ID>`。这些变量写进会话目录下只有自己可读的 `env.json`，由 PTY 代理读取后删除，不经过 tmux 的命令行：命令行对本机所有用户可见，而环境变量里常有 API key。用 `-c` 注入 `PermissionRequest`、`SessionStart` 两个 hook，只对这个会话生效。hook 的定义（命令路径和 `timeout`）必须固定，否则每次都要重新信任（notes §3、§5）
- **启动弹窗**：用 `-c` 关掉检查更新、限额换模型提示等能关的弹窗；关不掉的 `Trust this folder?`、`Hooks need review` 每个目录或每套 hook 只问一次，客户端提示去终端处理。能不弹就不弹；信任目录是安全决定，留给用户在终端里做（notes §5）
- **防弹窗挡输入**：每次注入前先抓屏，确认没有弹窗挡着。判断画面时只看屏幕底部；以列表里选中的行判断有没有弹窗，标题只用来给弹窗命名，对话里出现同样的字不算（notes §5、§6、§10）
- **等画面**：轮询屏幕，等到预期画面出现再操作，并设超时；不用固定的 sleep（notes §6）
- **输入**：远程消息按到达顺序排队，一次注入一条；队列在受理前写入 `queue.json`，重启后接着发。请求 ID 会记住最近 1000 个，重试不会重复发送。写入前记下 `history.jsonl` 的位置和当前事件序号。注入时 daemon 先让 PTY 代理暂存本地按键，等 80ms 让已经敲下的键显示出来，再抓屏：输入框就绪且没有未发送的草稿，才一次写入 bracketed paste 包起来的正文和回车，然后放开暂存。暂存期间本地敲的键不丢，放开后按顺序进入新的输入框；PTY 是按顺序的字节流，它们一定排在这条消息的回车之后。本地有草稿或有弹窗时，消息继续排队，绝不覆盖或拼接草稿。写入后以 `~/.codex/history.jsonl` 新增一行（已知 Codex 会话 ID 时只认这个会话的行）或 rollout 里出现这条用户消息，确认提交成功（notes §1、§10）。每条消息的状态以事件记录：排队、已提交、无法确认。10 秒内确认不了的记为「无法确认」，**不重发**，因为按键已经送到，重发可能提交两次；写入时恰好 daemon 停了的，重启后同样只等确认、不重发。注入、回答提问和中断共用一把锁，同一时刻只有一个操作使用 PTY 代理；代理也只允许持有暂存的连接写入和放开。正文里的控制字符会被去掉，避免提前结束粘贴
- **判断输入框**：抓屏时保留颜色属性（`capture-pane -e`）。输入框的提示符是加粗的 `›`，占位提示是淡色字（前面可能先有背景色设置，判断时跳过开头的样式和空格，看第一个可见字符是不是淡色），列表里选中的行是加粗反色的 `›`。只有看到占位提示才认定输入框是空的：多行草稿的第一行可能是空的，其余行和底部状态栏分不开（notes §10）
- **找到 rollout**：`SessionStart` hook（发出第一条消息时触发）把 `RA_SESSION` 和 `transcript_path` 一起报给 daemon（notes §3）
- **轮次和空闲**：一轮以 `task_started` 开始，以 `task_complete` 或 `turn_aborted`（被中断）结束；屏幕底部的输入提示符作为辅助（notes §1、§8）
- **模型提问**：问题从 rollout 的 `function_call` 读出，它在等待回答时就已经写入，不需要 hook。回答时暂存本地按键、抓屏确认选择框还在，再注入选项编号；以 rollout 的 `function_call_output` 为准，先在终端里答了的就收回卡片。异步提问（`request_user_input_async`）不单独处理，一轮结束后当普通消息回复（notes §9）
- **中断**：只在一轮运行时发 Esc，绝不发 Ctrl-C。空闲时发 Ctrl-C 会清空输入框，再按一次会退出 Codex（notes §8）
- **切换模型和 effort**：空闲时操作 `/model` 菜单，按模型名在屏幕上找编号，用方向键选档位后按 `s`，只改当前会话；以屏幕出现 `for this session only` 为成功，认不出菜单就按 Esc 退出并报错，退回到用 `codex resume <id> -m <模型> -c model_reasoning_effort=<档位>` 重启。客户端先按屏幕上的确认结果更新显示，下一轮再用 rollout 校对；rollout 何时记录新模型是推测，未验证（notes §2、§6，见 plan.md）
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

客户端的模式切换只做 Codex 和 Claude Code 都有的 4 种，这样接入 Claude Code 时不用改界面（是否放进 MVP 未定，见 plan.md）：

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
- **完全放行要二次确认**；新会话的默认模式沿用 CLI 自己的配置

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

每个会话另有一把随机的**会话密钥**，加密这个会话的事件和元数据。它存在 daemon 的会话目录里（`relay.key`），用 `wrap` 加密后上传到 server，客户端从 server 取回再解开。

**设备**：每台电脑和每个浏览器都有自己的 Ed25519 设备密钥，设备 ID 是公钥 SHA-256 的前 16 字节。登记时用账户密钥签名，server 用账户公钥验证；登录时设备签 server 给的一次性 challenge，换取 30 天有效的 token。设备登记由账户密钥签名，而不是由一台已登记的设备签名：单用户场景下根密钥本身就是信任根，这样新设备加入时不需要旧设备在线。

**把根密钥交给新设备**：P1 用 `remote-agent account secret` 输出一串 `ra1.<根密钥>.<server 地址>`，粘贴到网页或 `remote-agent account join`。它不放进 URL，以免留在浏览历史里。P2 的扫码配对只替换这一步的交付方式，账户和设备的模型不变。浏览器把根密钥和设备私钥存成不可导出的 `CryptoKey`（IndexedDB），页面脚本能用它们，但读不出密钥本身。不同的网页来源（如 localhost 和以后的 `app.<域名>`）各有各的存储，换了地址要重新粘贴。

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

**WebSocket**：路径 `/v1/stream`，承载二进制 Protobuf 帧。浏览器的 WebSocket 不能自定义请求头，所以第一帧发 token，认证通过前 server 不发任何数据；浏览器连接还要检查 `Origin`。server 每 20 秒 ping 一次；某个客户端积压超过 256 帧就断开它，它重连后从历史补。每台机器同时只保留一条连接，新连接顶掉旧的。

**大小限制**：单个信封不超过 1 MiB，一次上传不超过 500 个事件，推送帧超过 1 MiB 就拆开发。

**网页**：事件内容一律当文本渲染，不插入 HTML；构建产物带 CSP，只允许运行自己的脚本。网页和 server 要放在同一个注册域名下（`app.<域名>` 和 `ra.<域名>`）：拦截扩展常按「第三方请求」过滤，实测 AdGuard 会拦掉跨站发往 `*.*.top` 的 WebSocket（[browser-notes §1](browser-notes.md#1-adguard-拦截发往-top-的跨站-websocket)）。本地开发遇到这种拦截时，可以用 `RA_PROXY` 让 vite 把请求转发到 server，同时用 `VITE_SAME_ORIGIN_API=1` 构建。
