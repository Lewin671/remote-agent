# 验收记录

本文记录 remote-agent 自己的功能做过哪些验收：哪天、用什么版本和环境、走了哪些操作、哪些没覆盖。它回答「这件事验证到什么程度」，代码和提交说明里查不到这些。

不写的内容：外部工具的行为（见 [codex-notes.md](codex-notes.md)、[claude-code-notes.md](claude-code-notes.md)、[android-notes.md](android-notes.md)、[browser-notes.md](browser-notes.md)），设计和理由（见 [architecture.md](architecture.md)），还没做和还没验证的事项（见 [plan.md](plan.md)）。

每条记录只代表当时的版本。之后改过的功能不因为这里有记录就算仍然通过；已经撤掉的功能不留记录。

## 1. 验证等级

下文和 [plan.md](plan.md) 用同一组说法，它们不能互相替代：

| 说法 | 含义 |
|---|---|
| 自动测试 | `go test ./...`、`cd web && pnpm test`、`cd android && ./gradlew test` 或仪器测试通过 |
| 演示页 / 示例数据 | 网页的 `/?demo`、Android 的 `V7Gallery` / `V8Gallery` 用固定数据渲染，没有连 daemon |
| 真实 CLI | 连着真实的 Codex 或 Claude Code 走过操作，客户端是本机 Chrome 或 Android 模拟器 |
| 真机 | 在真手机上走过。**目前没有任何一项做过真机验收** |

## 2. 本地和端到端（P0 到 P2）

**P0 本地打通**（tag `p0`）：`scripts/acceptance.sh` 用真实的 Codex 检查发消息、事件顺序、审批的允许 / 拒绝 / 超时、中断、模型提问、重启 daemon 后会话和事件接续（需要已登录的 codex，会消耗少量额度）；rollout 解析由 `go test ./...` 的脱敏 fixture 覆盖。attach 后的使用体验和「本地先答提问」是在终端里手动确认的。

**P1 端到端打通**（tag `p1`，2026-09-29）：

- 自动测试（`internal/relay`、`internal/server`、`internal/e2e`，`cd web && pnpm test`）：加密格式在 Go 和浏览器两边一致（测试向量 `internal/e2e/testdata/vectors.json` 由 Go 生成，网页校验）；事件按序号上传和推送，daemon 断线重连后不丢不重；同一请求 ID 的指令只执行一次，daemon 重启后也一样；过期、被篡改、用别的账户密钥加密的指令被拒绝；机器离线时立即失败，重连后也不执行；没有 setup token 不能建账户，不是账户密钥签名的设备不能登记，一台机器不能写另一台机器的会话
- 本机 Chrome 对线上 server 和真实 Codex 手动走过：粘贴账户密钥后看到机器和会话，发消息后回复实时出现；审批允许后命令执行，拒绝时附的理由 Codex 能看到，不回复超时自动拒绝；中断；模型提问点选回答；daemon 停掉时显示机器离线、发消息立即失败，期间终端里的对话在 daemon 重启后补到网页上；server 的数据库和日志里搜不到消息、命令、路径和机器名的明文

**P2 可以日常使用**（2026-09-29，没有打 tag）：

- 自动测试：模型、审批模式、计划、压缩的菜单操作每一步先确认画面，认不出就按 Esc 退出，有草稿时不动输入框（`internal/daemon/control_test.go`，按实测画面模拟 Codex）；终端流的快照、输出、输入、调整尺寸，重放的帧被拒绝，终端内容不进 server 数据库（`TestTerminalStream`）；配对两端 emoji 和密钥一致，被替换的承诺在揭示前被发现，拒绝后拿不到密钥，码的次数和全局限流，Go 和网页按测试向量一致；推送只发给订阅了对应类别的设备，相同通知合并，失效的订阅被删除；移除的机器连同会话一起删除、登录失效；超大的事件被截断而不是卡住同步；取消排队的消息后它不会再写入终端
- 本机 Chrome 用真实 Codex（GPT-6-Luna，低档位）手动走过：从网页新建会话，在终端视图里用按键栏确认信任目录后第一句话自动发出；切换模型和 effort、审批模式、进出计划模式，Codex 的菜单和状态栏随之变化，`~/.codex/config.toml` 不变；计划卡片批准后开始执行；压缩后上下文从 3% 降到 0%；电脑发起、浏览器扫码加入，以及浏览器发起、电脑输码加入，两个方向都成功，两端 emoji 相同；暗色和亮色下各页面和当时的界面稿一致；从 `remote-agent.top` 连 server 不受 AdGuard 影响
- 没覆盖：手机上的终端按键栏、推送和扫码

## 3. Claude Code

**daemon 侧**（2026-09-29，Claude Code 2.1.284 haiku，本机手动）：send 和本地有草稿时排队；`events --follow` 按顺序输出一轮；approve、deny 附理由、超时拒绝；终端里先答时收回卡片；interrupt；单问、两问、多选提问的远程回答和本地先答；计划的远程批准；切换模型和审批模式（不可用的模式报错并回到原模式）；压缩；stop 后 resume 接着同一段对话；重启 daemon 后序号接着走；`remote-agent doctor claude` 全部通过。Codex 这边 `scripts/acceptance.sh` 照常通过。

**网页上**（2026-09-29，Claude Code 2.1.284 haiku，隔离的 daemon 加入同一账户）：新建时选 CLI 和模型；信任目录的弹窗去终端处理；收发消息；审批允许和附理由拒绝；单选和多选提问；切到计划并批准；切换模型；压缩；中断；结束和恢复（恢复后接着同一段对话）；在终端里切到 accept edits 后照实显示，再切回询问；「完全放行」按会话信息置灰；haiku 的自动模式和推理强度不提供。

**daemon 侧，2.1.300**（2026-10-17，macOS，Claude Code 2.1.300 haiku；用当前 worktree 构建的二进制，独立的 `REMOTE_AGENT_HOME`、tmux socket 和临时工作目录，直连本地 daemon 的 CLI，没有连线上 server、日常 daemon 或账户）：

- `remote-agent doctor claude`：全部通过。首轮曾在「识别空输入框」超时一次，之后连续 14 次（含 8 次全新目录）全部通过，未能复现；当次据以判断的现象是 tmux 会话已随 CLI 退出而消失。手工验证到信任提示只要在它画出来之后再按 ↓、回车就不会退出（先按会落到默认的 `No, exit`）；验收脚本据此等提示出现后再按键。2.1.300 的两处适配差异见下。
- `scripts/acceptance-claude.sh`：新增的隔离入口，连续 3 遍全部通过。覆盖新建会话、发一轮并从 `events` 读回（事件顺序和回复正文）、审批允许和拒绝、单选和多选提问的远程回答、切换审批模式（ask↔plan）、切到 Plan 并批准计划、中断运行中的轮次、本地有草稿时排队再清空草稿后发出、stop 后 resume 接同一段对话（用一个暗号回问确认）、重启 daemon 后会话保留且事件序号连续无重复。
- `/model` 菜单：2.1.300 全窗口和 90×20 小窗口的真实抓屏各存进 `internal/claude/testdata/model/`，由回归测试核对整份行的解析（小窗口首尾行的 `↑`/`↓` 标记行不能被丢掉、光标行带标记也要认出）。另外用 npm 装到临时前缀的 2.1.287 抽查了 `/model` 的推理强度 ←（会两端循环）和末行的 `↓` 标记，不动用户全局 claude。
- 未覆盖：网页、Android 和 iOS 的直接界面操作；图片；auto 模式和推理强度切换；审批超时；压缩；分叉；线上 server 和日常账户。

**模型改为按具体 ID，2.1.300**（2026-10-17，macOS，Claude Code 2.1.300，Max 账户；当前 worktree 构建的二进制，独立的 `REMOTE_AGENT_HOME`、tmux socket 和临时工作目录，用**用户正常的 Claude Code 配置和登录**，没有 `CLAUDE_CONFIG_DIR` 隔离；直连本地 daemon 的 CLI，没有连线上 server、日常 daemon 或账户）：

- 逐条实测模型目录和它的行为（[notes §13](claude-code-notes.md#13-模型目录)、§1、§2、§5、§9）：`~/.claude/cache/model-catalog/…-cc.json` 的命名、字段和刷新时间；`--model <完整 ID> --effort <档位>` 照常启动（无档位的 Haiku 4.5 也接受，档位留着给之后切到的模型；超出模型的档位按上限收窄，认不出的值打印警告后忽略）；`/model <完整 ID>` 和 `/effort <档位>` 都**改全局默认**（写 `~/.claude/settings.json`），只有菜单加 `s` 只改当前会话；transcript 的 `message.model` 就是目录里的 ID（`--model claude-sonnet-5-5[1m]` 记录的也是 `claude-sonnet-5-5`），`perTurnEffort` 跟着会话的档位。测试写坏了本机 `~/.claude/settings.json` 两次（`/model <ID>` 的 `model` 键、`/effort high` 的 `modelSettings`），都用 CLI 自己的路径改回来了（菜单里的 `Default (recommended)`、`/effort auto`）；文件语义与测前一致，只留下一个空的 `modelSettings` 对象
- `scripts/acceptance-claude.sh`（用具体 ID `claude-haiku-5-5`，新增第 8 步）：全绿——新建会话、一轮收发、审批允许和拒绝、单选和多选提问、中断、草稿排队、**切换模型和推理强度（`claude-sonnet-5-5` + `low`）并核对 `~/.claude/settings.json` 未变**、切换审批模式、计划批准、stop/resume、daemon 重启后序号连续
- `remote-agent doctor claude`（私有的 daemon、状态目录和 tmux socket，工作目录在临时状态目录里；实际登录和目录）：全部通过，新增的「读模型列表」一步读到 12 个模型，「切换模型和推理强度」用目录里两个较旧的模型（`claude-sonnet-4-6` → `claude-opus-5-5`）来回切换通过
- 隔离的 `dev-env.sh` 环境（独立 RA_DEV_DIR、临时账户、server 数据库、daemon 状态目录和 tmux socket，Go 调试客户端经加密 server/daemon 操作真实 CLI）：新建会话选 `claude-haiku-5-5` + `low`（pty-host 的实际参数是 `--model claude-haiku-5-5 --effort low --permission-mode default`，终端标题写 `Haiku 5.5 with low effort`）→ 发一轮，transcript 记 `model=claude-haiku-5-5`、`perTurnEffort=low`；**中途切到没有档位的 `claude-haiku-4-5-20251001`**（菜单里 `s`，屏幕写 `Set model to Haiku 4.5 for this session only`，事件 `settingsChanged.model` 是完整 ID）→ 再发一轮，transcript 记 `claude-haiku-4-5-20251001`、`perTurnEffort=null`。全程 `~/.claude/settings.json` 校验值不变（两次切换前后各比对一次）；`model claude` 列出的 12 行与目录一致（Haiku 4.5 无档位，Opus 4.6、Sonnet 4.6 无 xhigh）
- 未覆盖：网页、Android 和 iOS 的直接界面操作（换模型、自动模式置灰仍待三端界面验收）；`standard_context` 的两种窗口在真实会话里的差别；auto 模式在具体 ID 下的 Shift+Tab 循环；目录缺失时的退化路径（只由单元测试覆盖：`TestModels` 断言无目录时报错、`TestLoadCatalogMissing`）；目录取不到时 CLI 自己的内置清单（只对着二进制和一次 `CLAUDE_CODE_MODEL_CATALOG=1` 的启动核对过）；线上 server 和日常 daemon。测试环境、tmux 和临时进程已清理；只本地提交，不推送、不发版。

## 4. 网页

**按 v1 界面稿**（2026-09-29）：本机 Chrome 上逐屏对照过 390 宽和 1280 宽、亮色和暗色；用真实的 daemon 和 Codex（GPT-6-Luna，低档位）走过配对加入、在项目里新建、信任目录的弹窗停靠后去终端处理、适配和还原终端、发消息、结束和恢复、切换审批模式。`pnpm test` 覆盖状态优先级、停靠区顺序、消息发送状态和 Markdown 渲染。

**v2 的画面，到 v7 为止**（2026-09-30，Claude Code 2.1.285 haiku，Codex 0.159.0 GPT-6-Luna low，隔离的 server、daemon 和 tmux）：

- 自动测试：`web/src/model.ts`（工具运行与待确认、队列、切换 CLI、来源）、`prefs.ts`（项目偏好的操作与冲突重做）、`devices.ts`、`quota.ts`、`attach.ts`（待发图片）、`imageview.ts` 和 `images.ts`（选图处理：转正、去元数据、压缩、JPEG 无损旋转，对 jpegtran `-perfect` 逐个系数比对）的规则，以及对 Go `v2-vectors.json` 的签名、新用途密钥与信封
- 逐屏对照：本机 Chrome 上 320 / 390 / 900 / 1280 宽、亮色和暗色；v7 另对照了设计稿的手机画面（304 宽）亮暗两套和宽屏 1176 / 900 宽截图，并查了 320 宽和放大 30%
- 真实 CLI：新项目选目录后新建两个 CLI 的会话，信任弹窗用按键栏处理；耗时 Bash 在结果返回前显示命令和计时；Claude Code `Read` 与 Codex `view_image` 的预览和大图（显示版解密后按文件头核对）；带方向标记和 GPS 的 JPEG 以原图发出后，电脑上是转正且没有 EXIF 的文件，模型读出图案；新建会话第一条消息带图；终端里留着草稿时消息排队，从队列取消；两个网页客户端（不同来源即不同设备）用配对码加入、改名同步、另一端发的排队消息显示发送者和全文、另一端取消后本端提示、另一端允许的审批显示「已允许 · 第二浏览器」、项目偏好改动实时推到另一端；本地 `remote-agent send` 的消息标为「本地终端」
- v5 的复查见 [browser-notes](browser-notes.md#v5-界面复查)

**网页换新版本**（2026-10-15）：本机 Chrome，用开发模式构建两版演示页（入口脚本不同），放在本地静态服务器上替换。页面只改地址的 `#` 部分（相当于切回前台，不重新加载）时不会自动发现；点设置页「检查更新」后首页侧栏和设置页都显示「新版本已下载」，点「重新加载」一次就跑上新版；再查显示「已是最新」；停掉服务器再查显示「没能下载，稍后再试」。慢速和卡住的下载只有 `web/src/sw.test.ts` 的单元测试（超时重试、并行、取不全不换页面），没有在真实慢网下验证；手机 PWA 切回前台的自动检查没有真机验证。

## 5. Android

都在模拟器（API 36，Google Play 镜像）上做，实测细节见 [android-notes.md](android-notes.md)。

**A1 到 A5**（2026-09-29，本机独立的 server 和 daemon）：

- 自动测试：`cd android && ./gradlew test` 跑 `vectors.json` 和反向向量 `kotlin-vectors.json`（Go 的 `internal/e2e`、网页的 `kotlin-vectors.test.ts` 也校验它）；`:core` 的集成测试在 JVM 上起真实的 Go server 和假 daemon，覆盖同步和推送、指令转发和机器离线、终端流、配对、server 重启后不丢不重
- 模拟器上走过：输码配对（电脑发起）和 App 发起、电脑输码加入，两边 emoji 一致；会话列表、项目页、聊天；发消息收到回复；审批在停靠区允许、附理由拒绝，在通知里允许和拒绝，推送点开直接进审批页；Claude Code 的多选提问；中断；结束和恢复；切换审批模式；在项目里新建 Codex 和 Claude Code（haiku）会话，信任目录的弹窗去终端用按键栏处理；终端的输入、按键栏、键盘弹出和旋转不改远端尺寸、适配和还原；server 停掉再起，断线期间的事件补齐且不重复；App 进程被杀后收推送、重开后先显示缓存；移除设备；亮色和暗色
- 推送：用真实的 Firebase 项目联调，server 用 FCM HTTP v1 发送，App 在后台和进程被杀后都收到并解密；通知默认只写通用文案，设置里打开后才有命令和按钮
- R8：`minified` 构建类型（release 加允许明文 HTTP）走过同样的流程
- 终端字体与字号的模拟器验收见 [android-notes §2](android-notes.md#2-termux-终端组件)
- 没覆盖：终端输出积压后断开重连的逻辑没有用真实的大量输出触发过；中文输入法、设了锁屏时的通知按钮

**v2 的画面**（2026-09-30，Claude Code 2.1.285 haiku，Codex 0.159.0 GPT-6-Luna low，隔离的 server、daemon，包名 `.v2dev` 的副本，debug 和 R8 的 `minified` 各走一遍）：

- 自动测试：`:core` 的 `Model.kt`（工具与队列状态）、`Prefs.kt`、`Images.kt` 有 JVM 测试；集成测试对真实 Go server 验了图片存取、两个客户端并发改项目偏好、客户端改名同步
- 真实 CLI：工具开始即显示和计时、并行调用、Claude Code 自己转到后台的命令标为待确认；独立中断入口；审批停靠和允许；Claude Code 多选「提交 N 项」、计划「继续规划」、Haiku 不提供自动模式；常用、移除和撤销、恢复，隐藏项目的会话仍在主页；设备目录、客户端改名、设备默认模型、额度页；Claude Code `Read` 的图片预览、大图、保存和分享；从手机给两个 CLI 各发两张图（带方向标记的 6.7 MB 照片压成 669 KB 并转正）、原图 8.9 MB 分块上传后转正且没有 EXIF、新建会话第一条消息带图、进程被杀后待发图片从加密存储恢复并重传
- 实测见 [android-notes §6](android-notes.md#6-v2-客户端)；v5 的复查见 [android-notes §7](android-notes.md#7-v5-界面复查)

**v7 的画面**（2026-09-30，示例数据）：仪器测试 `V7Gallery` 以固定示例数据渲染 29 个手机画面的亮暗两套，在 304×644 dp 的模拟器显示上截图，与设计稿同一画面并排核对，并在字号 130% 下检查不溢出，见 [android-notes §9](android-notes.md#9-v7-界面还原)。没有连真实 CLI。

**v8 的画面**（2026-09-30，示例数据）：JVM 测试覆盖待发状态、模式与项目设置冲突重做；`V8Gallery` 在 API 36 模拟器上通过 33 个亮暗画面测试，130% 字号下抽查截图，见 [android-notes §10](android-notes.md#10-v8-界面还原)。连真实 CLI 的部分见下一节。

## 6. 网页和 Android 同时操作（v8）

2026-09-30，隔离的 server、daemon 和 tmux，网页（本机 Chrome）和 Android（API 36 模拟器，`.v8e2e` 副本，配对码加入）同一账户，连真实 CLI（Codex 0.159.2 GPT-6-Luna low，Claude Code 2.1.285 haiku）：

- 新项目在项目设置里选 CLI、模型和「询问」后发第一句话直接新建，默认完全放行时不再确认；项目设置改动同步到另一端
- 运行中点 ↑ 插入这一轮、长按（网页 Alt+Enter）排到这一轮结束后，回复分别落在同一轮和下一轮
- 待发气泡就地取消（网页、Android），另一端的消息标出来源
- 中断后排队的消息暂停，点「发送」发出
- Claude Code 运行中收进它自己队列的消息（`HELD_BY_CLI`）用命令行撤回，确认之后没有发出；网页上这条路径与排队消息的取消用同一个指令

## 7. 会话里的图片（daemon、server）

2026-09-30，隔离的 server、daemon 和 tmux，Codex 0.159.0 GPT-6-Luna low，Claude Code 2.1.285 haiku，测试客户端是 `internal/remoteclient`：

- 两个 CLI 各新建一个第一条消息带图的会话；Codex 空闲时发两张图，Claude Code 运行中发两张图
- 3.8 MB 和 4.3 MB 的图显示版都在 1 MiB 以内；11.6 MB 带 GPS 的原图在两个 CLI 的记录里都没有 EXIF
- 电脑终端里显示 `[Image #N]`，模型都读出了图里的字
- Codex 的 `view_image`、生成图，Claude Code 的 `Read`，以及两个 CLI 在终端本地粘贴的图都带预览和大图
- 第二张图丢失时不提交并清空输入框；粘贴中途杀掉 daemon，重启后清空并记为失败；注入期间本地敲的键在提交后出现在新输入框里
- 18.9 MB 的 rollout 行照常解析
- server 数据库和 WAL 里没有正文、路径和图片明文

## 8. v9 以后的界面改动

这些改动两端都已实现，但验证只到下表的程度，没有逐屏对照界面稿，也没有连真实 CLI 复验。

| 改动 | 网页 | Android |
|---|---|---|
| 会话标题取首句并单行；「＋」直接选图片，模式、模型和上下文移到 ⋯ 菜单；额度、图片存储和外观精简；宽屏设置页限制列宽（v0.8.1 到 v0.8.6） | 本机 Chrome 1488 宽暗色下看过设置首页，其余只过自动测试 | 模拟器上跑过 `LayoutTest`，其余只过自动测试。只有设置、电脑详情和额度页限制了列宽 |
| 项目设置各子页的「完成」直接保存并关闭（v0.8.11） | 自动测试 | 自动测试 |
| 默认模型可以单独选推理强度 | 自动测试 | 自动测试 |
| 会话重命名（v20）。daemon 和中继只过自动测试，没有连真实 CLI 和线上 server 走过 | 自动测试，加演示页上点过：菜单、预填并全选、保存后导航栏更新 | 只过自动测试 |
| 分叉会话（v25）。daemon 在隔离环境里连真实 CLI 走过（2026-10-03，Codex 0.159.2 GPT-6-Luna low、Claude Code 2.1.287 haiku，本地命令 `remote-agent fork`）：没发过消息和运行中时拒绝；分叉后历史完整、不重复，新会话记得原对话，两边各自继续互不影响；分叉的分叉；结束后恢复；对已退出的会话分叉；Claude Code 分叉后没发消息就重启 daemon，再发消息历史仍不重复。没覆盖：经线上 server 从客户端发起、带图片的历史、推送 | 自动测试，加演示页上用 DOM 操作走过（宽屏 1680，暗色）：菜单项、置灰原因、「正在分叉…」、跳到新会话、分隔行链接回原会话、列表里的「分叉」；没有截图比对，手机宽度没看 | 单元测试和 lint，没有上模拟器 |
| 额度的重置时间写成「还有多久 · 哪一天几点」（v20） | 自动测试，加演示页上看过 | 只过自动测试 |
| 后台推送不可用时静默降级，状态写在通知设置分组下方 | 不涉及 | 本地夹具和模拟器布局，见 [android-notes §4](android-notes.md#4-fcm) |
| 新会话设置摘要改成按钮，只有完全放行标红 | 自动测试 | 自动测试 |
| Claude Code 的上下文窗口是项目设置首页的一行，默认 1M、可选标准 200k | 演示页点过 | 只过单元测试 |
| 客户端被移除后整屏提示「已被移出账户」，停止重连（v24）。没有在真机和线上 server 上走过 | 自动测试（假的登录回答），加演示页 `?demo&conn=removed` 上看过布局 | 自动测试：连真实 Go server，由另一个客户端移除后进入该状态；画面只编译过，没在模拟器上看 |
| 新建时在输入框旁选 CLI，项目设置里每个 CLI 各存一套（v26）。没有连真实 CLI 和线上 server 走过，两端之间的偏好同步也没有实测 | 自动测试，加演示页上走过（2026-10-04，手机 390 宽和宽屏 1200，暗色）：切换后摘要和导航栏跟着变、草稿保留，项目设置按 CLI 分标签，只装一个 CLI 的机器不显示切换；亮色没看 | 单元测试，instrumented 测试只编译，没有上模拟器 |
| 主页和宽屏侧栏改成按范围列出的会话时间线，带按标题筛选（v27）。没有连真实 CLI 和线上 server 走过 | 自动测试，加演示页上看过（2026-10-04，宽屏 1280 和手机 390，暗色）：全部项目和单个项目两种范围、范围菜单和弹层、宽屏主区域跟随范围；亮色、200% 字号和滚到底加载没看 | 单元测试，instrumented 测试只编译；没有上模拟器，范围标题、弹层、筛选框和大字号都没目测，演示画面（Gallery）没有加 v27 的画面 |
| 新会话的设置摘要：只写审批模式、机器和模型，只有一台机器时不写机器名，放不下时先省略机器名，宽屏上和 CLI 切换同高（v28） | 自动测试，加演示页上量过布局（2026-10-04，宽屏 1680，暗色）：摘要高 36；把机器名换成超长的，只有机器名被省略，模型完整（量的时候摘要里还有推理强度，之后才去掉）；手机宽度、亮色和 200% 字号没看 | 只改了拼接规则，单元测试；没有上模拟器 |
| 图标全部换成 Lucide，状态点画成实心圆（v29）。只换画法，没有连真实 CLI | 自动测试，加演示页上看过（2026-10-04，本机 Chrome）：手机 420 宽暗色的主页、设置、运行中的会话、排队的消息、终端按键栏、项目设置，宽屏 1280 亮色的侧栏和终端；手机亮色、200% 字号、图片查看器和配对页没看 | 单元测试和 lint；`Icons.kt` 的路径数据和 lucide-react 1.52.0 逐条比对一致；API 36 模拟器上用 `V8Gallery` 截图看过亮暗两套的主页、设置、项目设置、项目管理、运行中的会话和终端按键栏。`V8Gallery` 的 `terminal` 和 `queueSendMenu` 两项没通过（找「终端」找到两个节点、找不到「发送」），涉及的代码这次没有改，没有在改动前的版本上复跑确认；其余画面和大字号没看 |
| 协议版本 3：会话列表分页、信封内层压缩。线上升级走过一次（2026-10-05，v0.24.0）：已有数据库手工加列后新 server 正常启动，daemon 重连后 157 个会话里 151 个已结束的都补上了结束时间 | 自动测试：分页、按 ID 取、重连对账、取页与重列的先后、失败退避，用假的 server；压缩往返和 Go、Kotlin、浏览器之间的互通向量。线上账户里走过（2026-10-05，本机 Chrome，宽屏 1280，暗色，一个项目 81 个已结束会话）：第一屏 30 个并写「共 81 个」，滚到底取完全部；按标题筛选取完后给出结果；打开一个近 7 天前结束的会话能看到历史；运行中的会话照常更新。按 ID 取不在已取范围内的会话、多个项目、断线重连、Safari 都没看 | 单元测试，其中分页的 9 个连本仓库构建的 Go server 跑（120 个已结束加 2 个未结束）；压缩只在 JVM 上验证，没有在 Android 运行时上跑；没有上模拟器，「正在加载更早的会话…」「正在读取这个项目上次的设置…」等新文案没目测 |
| 断线提示分清本机网络不通、连不上服务器和电脑不在线（v30）。没有连真实 CLI 和线上 server 走过，也没有在真机上断网试过 | 自动测试（假的 WebSocket 和定时器：刚断开不下结论、10 秒后算连不上服务器、没网立即提示、状态词变「未连接」），加演示页上读过页面文字（2026-10-06，本机 Chrome 宽屏，`?demo&conn=nonetwork\|noserver\|reconnecting\|fail`）：提示条、侧栏状态词、导航栏第二行、详情展开和电脑不在线的会话；没有截图看样式，手机宽度没看 | 自动测试：连真实本地 Go server，停掉后先是等待、过了宽限算连不上服务器、状态变「未连接」，重启后恢复；系统报告没网时立即提示；解析失败的错误归为本机网络不通。提示条、详情弹层和状态卡只编译过，没有上模拟器或真机 |
| 会话的视图记在本机；宽屏会话顶栏并成一行；聊天开头只留一行「开始时间 · 目录」 | 已实现 | 只有视图记忆（没有宽屏布局，也没有会话开头这一行） |
| Android 已有更新或已下载时再次检查（v42）。四个状态见界面稿 #update-recheck | 不涉及 | 单元测试：`UpdaterTest` 覆盖换掉旧更新但不删除系统可能仍在读取的包、同一发布版保留已校验的包、失败保留原状态并只对手动检查写原因、重试、自动检查保持沉默、检查中不换屏幕上的版本、6 小时间隔；`UpdateTextTest` 覆盖文案。Pixel 3 上独立 `.updatetest` 应用的七个更新状态测试通过（含暗色 200% 字号），`V8Gallery` 四种画面亮暗共八张截图已生成并抽查；使用注入状态，没有连日常账户、覆盖安装正式 APK 或真实系统权限往返。权限返回等待检查及丢弃过期请求由单元测试和代码审查验证 |

## 9. 定时任务（daemon）

Go 测试覆盖绑定轮次、创建幂等、修订冲突、重启、dispatch 边界无法确认、遗漏周期、次数限制、取消和输入暂停；MCP 协议测试验证工具发现、参数传递及没有 server instructions。真实 Codex 和 Claude Code 上的执行验收见 [codex-notes.md](codex-notes.md) 和 [claude-code-notes.md](claude-code-notes.md)。

## 10. 复现时用得上的办法

- **网页的演示页**：`cd web && pnpm dev` 后打开 `/?demo`，用假数据看所有画面和状态，不连 server，不进构建。`&conn=`、`&term=closed`、`&empty=`、`&prefs=fail|unsupported`、`&devices=unsupported` 等参数切换各种情况，见 `web/src/dev/demo.ts`
- **在真实 CLI 上造出排队的消息**：先在终端里敲一段不发送的草稿，再从客户端发消息。daemon 不覆盖草稿，消息留在队列里
- **隔离环境**：验收用独立的 server、daemon（另一个 `REMOTE_AGENT_HOME`）和 tmux socket，不动日常使用的那一套；Android 用改了包名的副本
- **模型**：Codex 用 GPT-6-Luna 低档位，Claude Code 用 haiku，额度消耗最少

## 断网重连（2026-10-06，协议 4）

自动故障测试：Go relay 在本地 HTTP 升级成功但收不到 `Ready` 时超时重连，并补齐事件；server 在无账户活动时持续发送应用心跳。网页模拟不返回关闭回调、终端握手超时与取消、旧连接迟到帧、网络恢复时替换假在线连接、静默断线、历史补齐阻塞时心跳仍到达、登录和历史 RPC 不返回后超时恢复。Android 对真实本地 Go server 和本地 WebSocket 故障夹具验证就绪超时、登录超时、重复网络回调只留下一个当前流、在线换流后事件不丢不重。

在修复前版本运行新增测试，确认缺少就绪期限、网页关闭回调缺失、静默断线、登录及历史请求挂起和终端握手挂起均能被检出；Android 的重复网络回调测试实际打开 5 条流，修复后为 1 条。

通过 Go 全量测试及 relay/server race 测试、网页 428 项测试与构建、Android core 179 项、terminal 1 项、app 91 项测试及 debug APK 构建。上述自动故障由本地夹具模拟；线上验收见下文，真实手机 Wi-Fi/蜂窝切换尚未验证。

**线上端到端验收**（2026-10-06，v0.25.0，`ea7ba92`）：server 部署到 `vps.remote-agent.top` 的现有容器（API `ra.remote-agent.top`），正式网页部署到 Cloudflare Pages（`remote-agent.top`），本机 daemon 安装并重启。三端运行版本与源提交均核对一致；已有 tmux 会话重新挂接并继续运行。server 的旧镜像保留用于回退。

本机 Chrome 已配对客户端 → 线上 server → 本机 daemon → 真实 Codex 0.159.2（GPT-6-Luna low），专用空目录会话：

- 网页发送只要求回复标记的消息，收到 `RA-E2E-BASELINE-OK`。
- UTC 13:21:18–13:22:38 暂停 relay 容器 80 秒：TCP 保持但无应用帧，未切断电脑 Wi-Fi。期间本地 CLI 发消息并产生 `RA-E2E-DURING-OUTAGE-OK`。网页显示重连中，daemon 的 ping 超时使旧流失效；恢复容器后 daemon 在 13:22:56 连回，网页无刷新补齐离线回复。
- 网页再次发送，收到 `RA-E2E-AFTER-RECOVERY-OK`。另重启 relay，server 日志确认新终端流建立；从 Chrome 终端输入，收到 `RA-E2E-TERMINAL-OK`。
- 截止最后一轮结束，本地 37 条事件序号连续、无重复；四个标记回复在本地记录与网页中均各一次。

Android 正式 APK 的 SHA256SUMS 校验通过。现有模拟器的旧应用签名与正式 APK 不一致，保留应用数据并升级到同源码的 debug APK（versionCode 72）；没有卸载旧应用。模拟器窗口无法由当前 UI 工具控制，因此 Android 界面端到端、正式签名 APK 真机安装、真实手机 Wi-Fi/蜂窝切换均未验收，不能把此次 Chrome 结果当作这些项目通过。

**回复正文按 design-system 第 7 节重画**（2026-10-08，界面稿 v31）：两端改成同一种读法，由 `testdata/markdown/cases.json` 约束（块 43 条、行内 36 条、数量列 29 条、加粗小标题 9 条），网页的 `markdown.cases.test.ts` 和 Android 的 `MarkdownCasesTest` 逐条比对。

- 网页：`pnpm test` 558 项通过，构建通过。本机 Chrome 里用真实的 `Markdown` 组件和样式表渲染日常、拥挤、边界三组内容，看过 390 宽亮色和暗色、760 宽亮色，点过「展开全文」和「收起」。
- Android：`:app:testDebugUnitTest` 97 项通过；API 36 无窗口模拟器上 `MarkdownOnDeviceTest` 3 项（设备自己的正则读法和网页一致）和 `MarkdownTableLayoutTest` 10 项通过，看过亮色整页、嵌套和宽表格的截图。
- 没覆盖：没有连真实 CLI 的会话逐屏对照；复制按钮没有在真机和网页的真实点击下验证剪贴板内容；200% 字号只有表格的仪器测试覆盖；Android 上行内代码的底色不占排版宽度，紧贴字母时会伸到相邻字符下面。

**回复正文在真机上看过，并加大会折行的列表的项间距**（2026-10-08，v0.27.0 的正式 APK 装在 Pixel 3、Android 11 上，界面稿 v32）：配对后打开一个真实的 Claude Code 会话，看过标题、段落、列表、表格和行内代码，亮色。发现各项折成两三行的列表读起来像一整段，于是改成这类列表项间距 8，由共用样例里新增的 `looseList` 一组（20 条）约束；改后网页 `pnpm test` 579 项、Android `:app:testDebugUnitTest` 98 项通过。真机上仍没看过代码块、任务列表、diff 和暗色。

**新项目选好目录后就列出来**（2026-10-09，界面稿 v33）：只过自动测试。网页 `flows.test.tsx` 用演示数据走「新项目… → 选目录」，核对项目设置里存了所选机器、清掉页面内存后项目仍在选择器里、项目页写「这个项目还没有会话。」，以及选已有项目的目录不改它的项目设置；`paging.test.ts` 和 Android 的 `SessionPagingTest` 核对列表规则（有会话的项目不受影响、机器已不在的不列）。没覆盖：真实 server 上刷新和另一端同步、Android 界面。

**项目列表统一、移除不可撤销、连接检测**（2026-10-10，界面稿 v34）：

- 网页：`pnpm test` 587 项通过。`flows.test.tsx` 核对三处列表的分组、行文字和「新项目…」逐字相同，移除要先确认、取消不移除、确认后没有撤销，项目设置里的移除走同一个确认并回到主页，已移除的目录用「新项目」再加回到列表，断线时两项操作禁用；`conncheck.test.tsx` 核对三次取居中、慢的标「较慢」、服务器没回应时不测电脑。演示页（本机 Chrome，手机 390 宽，暗色）看过项目管理、⋯ 菜单、确认框、范围菜单、选项目、项目设置和连接检测；宽屏、亮色和 200% 字号没看
- Android：`:core:test` 和 `:app:testDebugUnitTest` 通过。API 36 模拟器上 `V8Gallery` 的 `scopeSheet`、`pickProject`、`manage`、`manageMenu`、`manageRemove`、`hiddenProject`、`newSettings`、`connCheck` 八项通过，看过亮色截图；连接检测在示例数据下只能看到服务器连不上的样子
- 真机（v0.28.0 的正式 APK 覆盖安装在 Pixel 3、Android 11 上，连线上 server 和真实账户，亮色）：范围菜单和项目管理列出 11 个项目，带搜索，没有常用项目时不写分组标题；⋯ 菜单和移除确认框打开正常，确认框点了取消，没有真的移除；设置 › 连接检测测出服务器 50 ms、电脑 146 ms。「再测一次」按钮在弹层里看不出底色，没有查原因
- 没覆盖：真机上真的移除再加回、从项目管理里加新项目；两端在另一客户端同时改列表时的表现；连接检测在弱网和服务器真的很慢时的读数；网页没有部署到线上，线上只核对了 `/healthz` 对网页域名带跨域头

## 11. iOS

### 消息文字选择（2026-10-10）

Xcode 27.0、iOS 27 的 iPhone 18 Pro 模拟器，从当前任务代码构建。聊天长按菜单使用最多四行的预览；「选择文字」打开 large sheet，标题固定，单个只读 `UITextView` 同时负责选区和滚动，不套在 `kitSheet` 的外层 `ScrollView` 里。

自动回归覆盖：320、390、744 pt 宽度下的长文视口与末段定位滚动；跨段落复制中文、组合字符、emoji 和 Markdown 原文；刷新颜色方案后保留选区和滚动位置；最大辅助字号；长回复的菜单预览高度。复制通过 UIKit 的 `copy` 实际读回模拟器剪贴板，不只是比较字符串。

检查通过：Go 全量测试、Swift Package 409 项、App 271 项（含参数化共 304 次），生成的协议代码无差异。界面测试通过共享窗口门锁串行使用前台 scene，避免选字、布局和隐私遮挡测试互相干扰；刷新时允许 TextKit 不到一行的布局调整，但不允许丢选区或跳回顶部。

隔离的本地 server、daemon 和真实 Codex（GPT-6-Luna low）：App 用 `session.draft` / `session.send` 发出消息，daemon 事件确认来自客户端，收到 20 段中文与 emoji 回复；用 `session.select` 触发和「选择文字」相同的函数，亮色、暗色截图确认回复内容与全高选字页面。测试结束退出临时账户，停止并清理隔离环境。

未覆盖：真机、手指长按菜单、拖动选区手柄越过屏幕边缘时的自动滚动、系统复制菜单的位置，以及 iOS 17。调试信号打开选字页面和程序设置 `selectedRange` 的验证不能替代这些手势验收。

### 客户端基础验收

2026-10-09，Xcode 27.0，iOS 27 模拟器（iPhone 17、iPhone 18 Pro、iPhone Air），没有真机。每一块各用一套隔离的 server 和 daemon（`ios/scripts/dev-env.sh`），连真实的 Codex（0.162.0，daemon 提示这个版本没验证过）。**这台机器上没有往模拟器注入触摸的工具**：下面的操作都是用调试信号触发和按钮相同的函数（清单在 `ios/RemoteAgent/App/Launch.swift` 开头），不是手指点的。

自动测试：`ios/scripts/check.sh` 通过。Swift Package 389 项（`RACore` 372 项，其中同步、配对、分页、指令和登录连本仓库构建的 Go server 跑；`RAMarkdown` 17 项，含共用的 `testdata/markdown/cases.json`），App 208 项。加密读 Go 和 Kotlin 写的三份向量。

走通的：

- **加入和设备**：用密钥加入、两个方向的配对码配对和取消；加第二台设备再移除；重命名本客户端；退出账户后钥匙串条目、缓存目录和偏好都清空
- **登录**：重启后沿用上次的 token，server 的 `tokens` 表里始终一行；手工删掉这一行后重启，App 丢掉旧的、重新登录
- **主页和项目**：浏览电脑上的真实目录，新建、移除、设为常用，改审批模式和强度，重启后都还在；启动一个会话后进入会话页，第一句话先显示成待发气泡
- **会话**：发消息、看回复和工具行；审批（停靠区出现请求，允许后收回，工具行记「已允许」）；中断；排队和取消；重命名；从 ⋯ 就地打开项目设置
- **终端**：画面、输入、中文和宽字符对齐；进入时适配一次，键盘弹出不改远端尺寸；还原和再适配；字号；进后台断开、回前台重连；server 和 daemon 各停一次后的提示和恢复；会话结束后切回聊天
- **图片**：一张 6016×6016、9.2 MB 的 HEIC 准备成 2047 px、不带位置信息的 JPEG 并传到电脑；带图新建会话和带图发送；重启后草稿从加密存储恢复并重传；看大图、保存到相册、分享
- **设置**：连接检测（服务器 1 ms、电脑 25 ms）；改审批超时后 daemon 的 `settings.json` 跟着变

示例数据：各画面用日常、拥挤、空白三组数据看过亮色、暗色和特大字号的截图；iPad 竖屏和 iPhone 横屏只看过主要画面。

没覆盖：

- 所有手势：下拉收键盘、拖离底部、长按菜单、左滑、边缘返回、大图的捏合、双击、平移和下滑关闭
- 系统选图器里真的选图之后的读取、从剪贴板粘贴图片
- 终端的硬件键盘、长按的选择 / 复制 / 粘贴菜单、真实旋转、真实拼音键盘；积压重连和不执行 OSC 52 只有单元测试
- 真实数据下的：在主页卡片上审批、回答提问、确认计划、分叉、结束和恢复、切换模型、agent 那边产生的图片、图片过期
- 切换审批模式在 Codex 0.162.0 上超时没切成（见 [plan.md](plan.md) 的「待确认 › iOS」）
- VoiceOver 实际朗读、减弱动态效果、iPad 横屏、iOS 17（部署目标）上的表现
- 真机：没有签名身份，没有装过


### 终端历史回看（2026-10-14）

- 真实 tmux、隔离 socket：进入终端前输出 2500 行，初次快照和改变尺寸后的快照均保留首尾行。
- 本地临时 server、账户、会话和真实 shell/cat：Go 客户端经加密中继打开 daemon 终端，读取旧历史，发送输入并收到进程输出，重连后仍能读到这些内容（`TestTerminalHistoryThroughRelay`）。没有连接日常账户。
- Chrome 扩展、本地 `?demo&term=history`：示例的 150 行历史可通过终端翻页操作回到 `history-001`；这是演示数据，不代表真实 CLI 的界面验收。
- iOS 27、iPhone 18 Pro 模拟器：`TerminalHostTests` 通过，组件加载超过一屏的历史后，程序设置回看位置并继续喂输出，位置保持；重新加载快照回到实时画面。没有验证手指滑动和真机。
- Android 11、Pixel 3：独立 `.termhistory` 测试应用的 `TerminalHistoryTest` 通过，组件回看时保留同一段文字，实时位置继续跟随输出。位置由测试代码设置，没有验证手指滑动。

边界：只恢复 tmux 仍保留且在快照预算内的输出，不是完整对话存档。Codex 的 inline 参数只影响之后启动或恢复的进程。


### 终端历史分段读取（2026-10-14）

- Go：本地临时 server、账户、daemon、tmux socket 和真实 shell/cat，经加密客户端逐页读取 2500 行，首尾、行数和顺序一致。读取中同时发输入并改变窗口尺寸，已打开的历史快照不变；重连重新捕获，能读到刚才发到进程的内容。分页 UTF-8、单页字节限制、超大单行、空历史、过期游标和文件生命周期另有回归测试。
- Chrome 扩展、本地演示数据：桌面和 390×844 手机宽度，第一段从最新记录开始；向上滚动从 `history-1001` 补到 `history-0801`、`history-0601`，保留当前文字位置，返回实时终端后恢复按键栏。示例总计 1200 行；不是网页连接真实 CLI 的验收。截图保存在工作区 `outputs/remote-agent-terminal-pages/`。
- Android：分页状态、超时重试、旧响应隔离及历史响应的积压计量通过自动测试；界面编译通过。未在真机上用手指验证新的历史阅读区，也没有对日常应用执行卸载测试。
- iOS 27、iPhone 18 Pro 模拟器：分页模型、空历史、只读输入拦截、历史响应与实时输出分离、补入旧内容后可见文字位置不变的测试通过。Swift Package 的 411 项测试通过，包含终端帧顺序和积压计量。没有真机手势验收。

协议版本为 5；daemon、中继和客户端需一起更新。已有 tmux pane 继续沿用创建时的历史保留量，新建 pane 才使用 100000 行上限。已丢弃历史无法恢复。

### 终端往上滑回看（2026-10-14，v40 界面稿，协议版本 6）

本地临时 server、账户、daemon 和隔离 tmux socket（`ios/scripts/dev-env.sh start --daemon`），真实 Claude Code 2.1.293 会话；在会话里打印 1500 行带颜色的输出后，三端分别打开终端。没有连接日常账户。

- 网页（本机 Chrome，1180×820，Vite 开发服务器，中继加 `--allowed-origins`）：按键栏的方向键和回车处理信任提示并发出命令，输出回到网页。滚轮向上进入回看：颜色和中文两格对齐与实时画面一致，滑到顶自动补齐并停在「已经是保留的最早记录」；空闲会话里胶囊保持「回到实时」，有输出时变为「有新输出」；滑到底继续往下、按按键栏的键都回到实时。触摸只用页面里合成的 TouchEvent 验证（往下拖进入回看，拖动被拦下不发给 CLI），没有真机触摸；iPad Safari 没看。
- Android（Pixel 3 真机，`.v2dev` 调试包，`adb reverse` 到本地中继）：手指往下拖进入回看，最新行在底部、上移几行打开；连续往上滑读到最早记录；滑到底再往下滑回到实时，胶囊回到实时。首轮真机测试发现阅读区行序颠倒，修复后复测通过。中文两格对齐、滚轮、首页失败重试只有自动测试。
- iOS（iPhone 18 Pro 模拟器，iOS 27）：`term.read` / `term.leave` 调试信号代替手指，回看的顺序、颜色、工具条和胶囊正确，`term.dump` 确认回到实时。没有验证手指拖动、触控板和真机。
- 自动测试：Go 抓取历史保留 SGR；三端按 `testdata/terminal/history-sgr.json` 解析颜色；首页失败留在实时画面并可重试、小幅拖动不进入回看、行序与快照一致。

协议版本为 6；daemon 和客户端需一起更新，旧客户端会被拒绝并提示更新。

### 比屏幕宽的终端（2026-10-15，v41 界面稿）

本地临时 server、账户、daemon 和隔离 tmux socket（`ios/scripts/dev-env.sh start --daemon`），真实 Claude Code 会话的 tmux 窗口里打印 30 行「wide NN x… ENDNN」的长行，用 `tmux resize-window` 把窗口改成 165、120、40 列；没有连接日常账户。

- Android（Pixel 3 真机，`.v2dev` 调试包，`adb reverse` 到本地中继）：165 列时字号停在 6，实时画面左右滑能看到行尾，往下拖进入回看；回看打开时停在实时画面的横向位置，平移到最右时 END 和实时画面在同一位置，底部出现横向滚动条；平移中途把窗口改窄不露出空白；键盘弹出和收起不丢横向位置；长按回看文字后横向拖动仍是选择。远端程序打开鼠标上报（DECSET 1000）时，点按发出鼠标序列，平移不发任何字节。横向滚轮和触控板没有设备能模拟，只有单元测试。
- 网页（本机 Chrome，Vite 开发服务器的演示数据）：滚轮横向平移，横向起手、中途往上漂移的一次滚动不会打开回看；回看从实时画面的横向位置打开；远端打开鼠标上报时横向滚动仍能平移（去掉修复对照过，会滚不动）。手机触摸没有真机验证。
- iOS：单元测试和 iPhone 18 Pro 模拟器上的 App 测试，`term.panLeft` / `term.panRight` 调试信号代替手指；没有验证手指拖动、触控板和真机。
- 自动测试：三端字号下限 6、按起手方向区分平移和回看、回看宽度不小于实时网格、滚轮方向锁定到停顿为止。

只改客户端，协议版本不变，旧客户端照常可用。


## daemon 更新

2026-10-15，macOS，本地隔离环境：自动测试覆盖清单/版本/平台/协议/存储/渠道校验、HTTPS 降级拒绝、附件大小和哈希、压缩包路径及链接拒绝、互斥锁、忙碌与持续空闲门槛、服务 PID 核对、失败目标单独持久化、回滚、更新前/替换后/提交中断的恢复，以及自动恢复次数上限。

`TestUpdateWithRealProcesses` 从独立测试程序构建两个版本，用真正的进程、Unix HTTP 健康接口和候选程序探测，走过下载、替换当前文件、重启并核对新版本/PID，以及新程序启动失败后恢复旧程序。服务管理器由测试适配器替代，没有使用日常账户、会话或中继，也没有启动真实 Codex/Claude Code。这是更新器流程验收，不是 Agent 端到端验收。

`RA_TEST_LAUNCHD=1 go test ./internal/updater -run TestLaunchAgentLoads -v` 在本机 GUI domain 加载独立测试 label，用生成的 plist 触发无副作用的记录程序，确认生成配置可加载并执行；测试主动 kickstart，未等待凌晨日历事件。测试 label 已卸载，不改日常 daemon/定时器。Linux 配置生成有自动测试，尚未实际运行 Linux user timer；没有验证真实断电、睡眠唤醒或长时间崩溃后恢复。自动更新默认关闭，本次未开启生产机器定时更新。

## 2026-10-16：daemon 退出结算与首条输出前中断

在独立 worktree 的修复代码上，用 `ios/scripts/dev-env.sh start --daemon` 启动隔离本地 server、daemon、账户、数据库、状态目录和 tmux socket，未连接线上环境或日常 daemon。真实 CLI 为 Claude Code 2.1.300 / haiku。Go 调试客户端注册到临时账户，通过 server 加密转发发送、中断、QUEUE 消息，并解密读回事件和会话元数据；未直接操作网页、Android 或 iOS。启动目录信任和每轮清空已恢复草稿由 tmux 调试触发。

先在未修代码上复现首条输出前中断：原生输入框恢复本轮原文，没有 transcript 结束记录，daemon 持续报 running。换成修复代码、重启隔离 daemon 后，旧轮次结算为空闲；通过调试客户端连续三次发送并立即中断，每次都在原生恢复输入框后收到唯一的 ABORTED 结束事件，来源保留客户端身份。随后发送 QUEUE 消息，收到真实模型的 `LIFECYCLE_RECOVERED` 回复和 COMPLETED 结束事件，客户端元数据为 IDLE。

退出无结束记录、退出标记先落盘的恢复快照、最终事件写入失败重试、Claude 持有输入的最终状态和持久化顺序由正式 Go 回归测试覆盖，两个 CLI 的遗留轮次都验证重放后不再阻塞。移除相应修复时，回归断言失败。验收命令为 `go test ./...` 和 `go test -race ./internal/daemon/... ./internal/claude/... ./internal/codex/...`。

未覆盖真实 Codex 的异常退出注入、真实 daemon 崩溃注入、API 错误、图片或超长原文恢复，以及浏览器和移动客户端界面。测试结束后清理隔离环境。

**软折行补验收（同日，Claude Code 2.1.300 / haiku）**：隔离环境将 tmux 终端调为 44 列、55 行，发送一条不含空白的中文长提示，首条输出前中断。原文在中文字之间折成五行；未修比较规则时仍报 RUNNING，修复后 daemon 重启恢复为 IDLE。Go 调试客户端再经本地 server 发送同样长提示并中断，读回 ABORTED（input restored by CLI）；随后 QUEUE 消息收到真实回复和 COMPLETED。清空多行恢复草稿由 tmux 调试触发，没有直接操作浏览器或手机。中文、长 token、原有空格和换行的多行提示均补了适配层和 daemon 回归测试，原比较规则下全部失败；全量 Go 测试及 daemon/Claude race 测试通过。仍未覆盖超过输入框识别行数范围的超长草稿和图片。

## 客户端状态推导共享向量

2026-10-16，macOS arm64，基线产品版本 0.36.5 的本地任务分支（未发布）：[`testdata/projection/`](../testdata/projection/README.md) 的 37 个场景、50 个观察点、183 个批次切点通过网页、Android core/app、iOS core 的共享检查。原生端核对完整重放、每个切点的 reducer 追加、EventProjection 更新和旧结果值的独立性；网页使用完整重放。异步问题还核对了三端 timeline/buildChat 中保留的聊天记录。Go remoteclient 没有领域 reducer，只检查相同输入的 protobuf JSON、序号和切点。共同规则见 [架构](architecture.md#客户端状态推导的共同规则)。

最终检查全部通过：`GOMAXPROCS=2 go test -p 2 ./...`；网页 `pnpm typecheck`、`pnpm test --maxWorkers=2`（716 项）及 `pnpm build`；Android `ANDROID_HOME=~/Library/Android/sdk ./gradlew --max-workers=2 -PappIdSuffix=.v2dev :core:test :app:testDebugUnitTest :app:lintDebug`（core 214、app 203 项单测，全部无跳过，lint 通过）；iOS `swift test --package-path ios/RemoteAgentCore -j 2`（421 项、63 个 suite）。检查按顺序执行，构建并发限制为 2。

8 处行为修正按提交分别撤掉生产代码后，共 16 次共享 runner 检查都因指定向量失败；随后恢复代码重新跑上述全套检查。覆盖入队边界、明确来源认领、工具 ID 配对与迟到追补、退出/分叉状态、含未决请求的 digest 消歧、显式后台状态及无起点后台结果。

本次只到自动测试：没有启动模拟器、没有直接界面操作或调试信号驱动的真实 CLI 链路；没有连接线上或日常会话。没有验证持久快照、序列化 continuation 恢复、导出裁剪/图片冻结或历史分页，不能把内存切点测试当成这些能力的验收。


### 反馈后的网页真实链路（2026-10-16）

在负责人 rebase 后的本地任务分支上，从本 worktree 构建隔离 server、daemon 和网页，用 `ios/scripts/dev-env.sh start --daemon` 的临时账户、独立数据库/状态/tmux socket。Playwright 1.64.1 启动独立无头 Chrome 155.0.8059.80，分别看 390×844 和 1440×1000；未连接用户 Chrome 或线上环境。真实 CLI 为 Codex 0.163.0 / GPT-6.1-Sol、Claude Code 2.1.300 / Opus 5.5。

两端均通过网页走了普通对话、审批批准与拒绝各一次、同步提问并回答、运行中插入及排队、中断后继续对话、压缩、停止后恢复和分叉后发送。批准的临时文件实际生成，拒绝的未生成；Claude 真实出现 HELD_BY_CLI → SUBMITTED；后台结果记录了 Codex STILL_RUNNING 和 Claude REQUESTED；压缩注解分别显示 2% → 3%、3% → 0%。两个独立浏览器客户端与本地 CLI 分别发送，网页正确区分当前客户端、另一客户端和本地终端。会话最初由隔离本地 CLI 创建，信任目录由隔离 tmux 按键处理；其余上述用户操作由 Playwright 点击真实网页或键盘快捷键触发，审批/回答/发送经过 server、daemon、原生 CLI 后回到网页。本地来源这一项的输入由本地 CLI 触发，不冒充网页操作。

46 个不同状态各保存手机/宽屏的刷新前后截图，另有最后 8 组一致性检查，共 200 张截图。最终两个源会话和两个分叉会话在两个视口下，完整页面 reload 前后的最后 30 行聊天记录及待处理状态严格相等，8/8 通过；不是逐像素比较。当前运行状态和复制历史的未知/后台注解没有混用。Codex 还观察到轮次结束后才到的结果，但它使用不同 call_id，保留独立记录；同 call_id 跨轮追补没有在本次真实 CLI 中命中，仍由共享向量覆盖。

冒烟发现并修正两项 Codex 0.163 适配问题：页脚 Plan mode 后带快捷键/警告而被旧的行尾识别漏掉；选择带 (current) 的现有权限不会再输出新的确认句。行为实测见 [Codex notes §4](codex-notes.md#4-审批模式)，分别有屏幕向量、脚本终端回归和共享事件向量，撤掉对应生产修复后测试失败。修正后在同一隔离网页环境连续完成重复 Ask、进入 Plan、离开 Plan，SettingsChanged 记录 ASK、PLAN、ASK。

恢复生产修复后，完整检查通过：Go 全量；网页 typecheck、718 项测试和 build；Android core 214、app 203 项及 lintDebug；iOS core 421 项、63 个 suite。共享语料为 39 场景、53 观察点、194 切点。构建并发限制为 2。测试结束调用 dev-env.sh stop，server、daemon、tmux、预览服务和独立 Chrome 均停止，临时账户状态和浏览器密钥状态文件已清理，保留截图/事件证据供审查。

本轮没有启动 Android/iOS 真机或模拟器，原生端仍只过自动测试；没有验收真实网络故障、持久快照、序列化 continuation 或历史分页。以上结果来自未发布的任务分支，不代表线上部署。

## 2026-10-16：daemon 事件提交与解析诊断

macOS、Codex CLI 0.163.0、当前 checkout 的 dev 构建；通过 `ios/scripts/dev-env.sh` 创建独立 server、账户、daemon、tmux socket 和 Codex home。操作由 Go `remoteclient` 调试程序发起，经过 server 的加密转发、daemon 和真实 CLI，再读取客户端解密后的结果与元数据；没有直接操作网页或原生 App。启动信任提示通过隔离 tmux 的按键处理。

将测试会话的事件日志放进独立 16 MiB 磁盘镜像并填满：远程排队返回「队列已保存，但事件没能记录，不要换 ID 重发」；切换推理强度在终端已变成 high，远程返回「电脑上已生效但没能记录」，元数据仍保留已提交的 low。释放磁盘后取消测试消息、恢复记录。只在该隔离 Codex home 的 rollout 追加一条字段类型错误和一条未知类型记录，再远程发送短消息：真实 CLI 回复、轮次结束继续同步，解析计数重启后不变。没有按无输出时间推断结束。存储和解析诊断的本机查询与客户端隔离验收见下文。

未覆盖：Claude 的真实故障注入、真实审批等待中的磁盘故障、网页/Android/iOS 的错误呈现和手势操作。它们的 daemon 提交边界由回归测试覆盖；本次不代表这些客户端的直接界面验收。

### 诊断仅本机可见（反馈修正）

相同的隔离 dev-env、Codex CLI 0.163.0 和 Go `remoteclient` 调试链路重新验收：先远程收发一轮，再向隔离 rollout 追加一条字段类型错误和一条未知类型，随后再远程收发一轮。客户端解密元数据的 `statusDetail` 仍为空，本机 `ls -a` 的独立诊断列显示 `parse failures=1 unknown=1`、最近位置和类型；客户端累计读到两次真实回复及两次正常轮次结束。停止后客户端元数据的 `statusDetail` 精确为 `stopped`，本机诊断仍可查。测试账户、进程、tmux 和 Codex home 均已清理。

自动回归覆盖带存储故障及解析诊断时 `info()` 的全部元数据不变、空状态/弹窗标题/`stopped` 原样保留、本机存储快照的保存/清除、诊断文件不可写时的限频日志，以及未知类型不告警、不唤醒同步。`go test ./...` 和指定 daemon/Claude/Codex race 检查通过；六组反向验证在撤掉对应修复后均以测试断言失败。未直接操作客户端界面，本轮未重复磁盘镜像注入；整个文件系统无法写入时，本机存储快照也可能无法保存，日志是其回退途径。

### 存储失败时的拒绝与已受理消息（第二轮反馈）

macOS、Codex CLI 0.163.0、`e9ccf793` 的 dev 构建，沿用独立 dev-env 的临时账户、数据库、tmux 和 CODEX_HOME。事件日志单独放在 16 MiB HFS+ 磁盘镜像，填充到真实 ENOSPC；队列和本机诊断留在另一文件系统。

- 通过真实 `remote-agent hook codex` 命令向正在管理的测试会话发 PermissionRequest（调试触发，不是模型原生发起的审批）。事件追加失败时，命令仍以 exit 0 输出 `behavior: deny` 的 JSON，没有因 RPC 错误丢掉拒绝 payload。请求、超时、退出、远程拒绝四种分支的 handler 回归也验证 payload 成功返回。
- 加长测试请求 ID 至 128 KiB，使排队和 SUBMITTING 事件跨过文件已有预分配块，稳定触发追加失败；实际发给模型的文本只有 34 字节。通过 Go remoteclient 加密转发受理消息后，磁盘上的队列仍为 WAITING、Inflight 为空；释放磁盘后只观察队列文件，没有重发、重启 daemon 或发送唤醒指令，该消息自动提交。客户端解密事件确认该请求的原生 UserMessage、SUBMITTED 和预期模型回复各出现一次，没有 UNCONFIRMED；含基线的一共两轮正常结束。

未知记录、坏 JSON、错误的 task_complete / turn_aborted 字段和诊断保存失败重试，均有组件回归验证之前暂存的 ImageView、用户图片仍正常发出。逐项换回修复前源码的反向测试都以断言失败确认。满盘复测发现的通用 inject 重复警告已另外去重，限频日志由最终代码的真实注入循环测试覆盖。全量 Go 与 daemon/Claude/Codex/tail race 检查通过。

已停止测试进程和 tmux、卸载并删除镜像、清理 dev-env 和临时认证副本。未直接操作客户端界面；本轮未验证模型原生审批界面在满盘时的完整交互，也没有 Claude 原生 CLI 故障注入。标记已经持久化而终端写入结果未知的崩溃窗口继续沿用原有保守恢复，不自动重发。

### 统一投影与队列提交（第三轮反馈）

系统检查了全部运行期直接提交、fork 初始化写入及 queue.json 保存路径。组件故障回归确认：两个真实 permission 调用的等待时间按请求事件设置、按解决事件切换和清除；推断结束与 transcript 结束各只投影一次，重启结果一致；首次图片阶段追加失败时，文件和内存均保持 WAITING，修复事件日志后图片和文字各提交一次，没有 restarted 或 UNCONFIRMED；解除暂停和 held 释放的通知失败后，持久 ready 状态仍会唤醒投递。暂停队列文件保存失败时，运行期和重启恢复都保持安全暂停，不写未保存的暂停通知，下次恢复重新保存。事件与队列保存错误共用本机限频诊断。

独立 dev-env、macOS、Codex CLI 0.163.0，当前工作区构建：Go remoteclient 经加密 server/daemon 向真实 Codex 完成一轮消息收发。随后通过生产 hook 命令调试触发审批；远程解密元数据确认待审批时 AttentionSince 非空，远程拒绝后清空，hook 成功返回 deny。不是客户端界面操作或模型原生审批触发。图片阶段与 held 故障由真实队列文件、事件存储和替身终端的组件测试验证，本轮未重复磁盘镜像或 Claude 原生 CLI 故障注入。

全量 Go 与 daemon/Claude/Codex/tail race 检查通过；八组反向验证均在撤掉相应修复后以断言失败。测试账户、进程、tmux、Codex home 和临时认证副本均已清理。

### 持久受理的调用方（第四轮反馈）

macOS、Codex CLI 0.163.0、当前 worktree 的 dev 构建；独立 dev-env 的账户、数据库、状态目录、tmux socket 和 CODEX_HOME。Go remoteclient 调试程序经 server 的加密转发、daemon 和真实 Codex 先完成一轮正常收发，再创建定时消息。没有操作网页或原生 App。

事件日志移入独立 16 MiB HFS+ 镜像；为排除文件预分配对满盘测试的影响，停止测试 daemon 后向日志追加一条无正文、无业务 kind 的元数据事件，以 JSON 空白补到 64 KiB 边界，再填满磁盘并启动测试 daemon。队列、任务文件和本机诊断留在另一文件系统。派发时消息队列保存成功、排队事件实际 ENOSPC：任务仍为 queued，active run 的请求 ID、会话 ID 和游标保留，reason 明确说明已排队但记录失败；立即再运行被拒绝。故障期间再重启测试 daemon，上述跟踪信息完全一致。

释放磁盘后没有重发或再次重启 daemon，消息自动进入真实 CLI。客户端解密事件确认定时输入的 UserMessage 和预期回复各一次，没有 UNCONFIRMED；任务记录一次 completed，恢复下一次调度。镜像、临时账户、进程、tmux、Codex home 和认证副本已清理。

组件回归覆盖新建会话首条消息受理后不被清理、图片不退回 pending、恢复输入和 held 释放统一受理标记、真实 MCP/HTTP 工具读取 queued 警告并拒绝重复执行、本地 send 输出原请求 ID。首条消息和 held 取消的精确提交边界使用 protobuf 编码失败注入，不代表真实 CLI 的这两条故障链路。全量 Go、daemon/Claude/Codex/tail/schedulemcp race 检查通过；九组反向验证均在撤掉修复后以断言失败。

未覆盖 Claude 原生 CLI 故障注入或三端直接界面验收。普通 RPC 目前不能同时携带成功结果和记录警告：三端仍按通用错误呈现，本地 send 仍以非零退出报告记录错误；本轮依约不改协议，该限制与后续协议设计见 [plan.md](plan.md#待确认)。

### 损坏诊断恢复与 transcript 水位（第五轮反馈，2026-10-17）

macOS、Codex CLI 0.163.0、当前 worktree 构建；通过 dev-env.sh 使用隔离的 server、账户、daemon、tmux socket 和 CODEX_HOME。Go remoteclient 经加密 server/daemon 操作真实 CLI，没有操作三端界面。先正常收发，再停止测试 daemon 并把 parse-health.json 写成截断 JSON；重启后会话仍可控制，远程发送得到真实回复，本机记录不含正文的诊断读取警告。之后追加一条未知记录，下一次诊断保存覆盖了坏文件。

将同一隔离 transcript 截短为一条字段类型错误记录：原诊断 End 为 105722，新错误 End 为 76，累计 unknown=1、failures=1，新位置/类别和本机警告均更新。再重启测试 daemon，诊断次数和最新位置保持不变，证明旧的持久事件 checkpoint 没有让这次截断被再次计数。最后回填隔离 transcript 并再次发送，真实 CLI 回复继续可读；回填重新导入了部分历史事件，因此该阶段不作为消息投递次数或历史无重复的验收。最终事件汇总包含五条用户/助手事件，而实际调试程序只发送了三条消息。

自动回归覆盖坏 JSON、截断 JSON、字段类型错误和不可读目录不影响会话恢复及发送/取消；同 inode 截断、同路径短/等长/更长文件替换；空文件重置持久化；重置保存失败后文件增长的重试；旧 checkpoint 加无关 daemon 事件后重启去重，以及新 transcript checkpoint 恢复使用。全量 Go、daemon/Claude/Codex/tail/eventlog/schedulemcp race 检查通过，七组反向验证撤掉相应修复后均断言失败。

已清理隔离环境、测试进程、tmux 和临时认证副本。没有验证 Claude 原生 CLI 或三端界面；没有覆盖 daemon 停机期间的等长替换，或同 inode 在两次读取之间截断后又长过旧位置的识别。这些没有 reader 重置信号的情况不在本轮保证内。

## 2026-10-17：Codex 首次进入 Plan

macOS、Codex CLI 0.163.0、当前 worktree 构建；`ios/scripts/dev-env.sh` 的独立 RA_DEV_DIR、临时账户、server 数据库、daemon 状态目录和 tmux socket。Go remoteclient 调试客户端经 server 加密转发请求，daemon 驱动真实 CLI，再由客户端接收结果；终端 capture-pane 保存状态变化。没有操作浏览器或原生 App，也没有连接线上服务或日常 remote-agent 账户。

未修复时，两组新建会话的首次进入分别为 4/5、9/10，两个失败都在约 4.2 秒后超时、重试成功。另用测试 MCP 的 initialize 回复闸门控制初始化：闸门关闭时稳定复现超时，释放后立即重试成功。外部 CLI 的具体行为见 [codex-notes.md 第 4 节](codex-notes.md#4-审批模式)。完整修复后，在相同的关闭闸门条件下约 281 毫秒内确认 Plan，确认之后才释放 MCP 回复；自然启动的 10 个新会话连续进入 Plan、退出到 Ask 全部成功，进入 10/10、退出 10/10，没有重试。

`testing/synctest` 与明确的命令接受/屏幕呈现同步点覆盖 MCP 初始化期间的切换和等待确认；另覆盖已在 Plan 时不写键、写入错误向上传播、带警告但无快捷键提示的 footer。分别撤掉命令修复和识别修复，对应回归均以断言失败。`GOMAXPROCS=2 go test -p=2 ./...` 与新增 Codex 用例的 race 检查通过。测试环境和临时进程、tmux 已清理；保留本机探针源码、终端快照、时间线和测试日志供复核。

未覆盖：网页/Android/iOS 直接界面操作、模型生成中的切换、MCP 仍在初始化时退出 Plan，以及其他 Codex 版本；没有向模型发送任务或验证后续同步提问流程。本次只本地提交，不推送、不发版。

### 运行中切换与本地草稿（反馈补充）

相同 macOS 与 Codex CLI 0.163.0，新的独立 RA_DEV_DIR；本轮同时隔离 CODEX_HOME，只复制登录凭证和模型缓存，使用最小配置，结束后删除凭证副本。由 Go remoteclient 经本地 server/daemon 发起，真实 CLI 执行 `time.sleep(40)` 的只读命令；本地草稿通过隔离 tmux 输入，不发送回车。没有直接操作客户端界面。

先在反馈修正前的构建各验证一次：普通模式运行中进入 Plan，原生提示 `/plan` 在任务运行中不可用；Plan 运行中退出时快捷键无效。两次请求都超时，输入框为空，客户端和原生记录均未多出 `/plan` 用户消息。具体 CLI 行为归属 [codex-notes.md 第 4 节](codex-notes.md#4-审批模式)。

增加运行状态检查后，两轮任务的原生回合时长分别为 44.616、44.193 秒；每轮工具确认已开始后，连续请求 5 次。运行中进入、退出各 5/5 立即返回 CLI 忙碌、保留原模式和空输入框；这是安全拒绝率，不是运行中切换成功率。草稿场景分别重复 5 次进入、5 次退出，全部拒绝，逐字保留草稿且模式不变。每个方向是在同一真实会话中连续重复，未声称五个独立模型回合。两个长任务各完成一次，客户端只有两条测试用户消息；原生 transcript 除启动上下文外也只有这两条任务，没有 `/plan`、steer 或排队的新增用户消息。任务结束后正常进入、退出均成功。

组件回归覆盖两个方向在 running 下零键写入、草稿在空闲/运行中均保留、已经处于 Plan 时的无操作请求、普通权限档位仍可在 running 下操作，以及拒绝后不投影 SettingsChanged；撤掉运行检查后，两方向的 adapter/daemon 回归均断言失败。原有 MCP 启动和 footer 识别回归继续通过。全量 `GOMAXPROCS=2 go test -p=2 ./...` 以及相关 Codex/daemon 用例的 race 检查通过。

本轮补齐了模型运行中和本地草稿的验证；其他 Codex 版本、直接手机/网页界面，以及 MCP 未初始化时退出 Plan 仍未覆盖。测试 session、server、daemon、tmux、数据库与 CODEX_HOME 均已清理；只本地提交，不推送、不发版。

## 2026-10-17：客户端开的会话经过登录 shell

macOS arm64，本 worktree 构建的隔离环境（`ios/scripts/dev-env.sh`，RA_DEV_DIR 放在 /tmp 下的短软链接后以满足 socket 路径长度），独立 server、临时账户、数据库、daemon 状态目录和 tmux socket；未连接线上服务、日常 daemon 或日常账户。客户端身份用本地 socket 的测试调用（Connect JSON 加协议头，不带 `Env`，与 relay 转发后的新建请求一致），从终端开的会话用 `dev-env.sh cli new`。测试 daemon 在 `agent.env` 里放 `ZDOTDIR` 指向临时 zsh 配置，CLI 用把环境写进文件的假脚本；真实 CLI 一节用真实的 Codex 0.163.0 和 Claude Code 2.1.300。

daemon 故意用过期 PATH 启动（`/tmp/ra-ls-stale/bin` 在前，里面放一个只在老位置的假 codex）。客户端新开的会话里，CLI 是登录 shell 按自己的 PATH 解析出的另一个假 codex，进程的环境里有测试配置 export 的标记变量和新 PATH，没有 daemon 的过期目录；daemon 日志按会话记下「the login shell resolved a session's CLI」。改测试配置的值后不重启 daemon 再开一个会话，新值生效，先前的会话不受影响。终端开的会话仍带发起终端的变量，看不到登录 shell 的测试标记。

失败两例：配置里 `exit 3` 时，会话立刻以「the login shell did not start the CLI」结束；配置里 `read` 等待输入时，daemon 在 1 分钟后（`ShellStartTimeout` 默认值）结束会话，原因为「the login shell did not start the CLI within 1m0s」。两次客户端读到的都是 EXITED 加该原因，没有停在「启动中」，对应的 tmux 会话也已消失。

用真实登录 shell 配置（不设 ZDOTDIR）：从客户端各开一个 Codex 和 Claude Code 会话，两个 CLI 都由 shell 解析到用户自己的 `~/.local/shims`，codex 进程跑在 `/Users/workstation/.nvm/versions/node/v22.23.3/bin/node` 上（`lsof` 读到的可执行文件，与新登录 shell 的 `node` 一致；daemon 自己 PATH 里的 node 还是升级前的 v22.17.0）。Codex 会话进入空闲并画出界面；Claude Code 停在信任目录的弹窗，界面已画出。没有发送任何消息，没有消耗模型额度。

回归测试覆盖起点环境（PATH 换成系统默认、身份和 locale 保留）、argv 与固定脚本、在真实登录 shell 里跑一遍脚本（找到和找不到两条路）、登录 shell 的取法（dscl 与 passwd 解析）、来源判定（带环境变量走直接启动、不带走登录 shell）、恢复和分叉沿用最初的启动方式、定时任务沿用来源会话的方式、学到 CLI 路径后版本检查与已装列表跟着走、卡住与找不到 CLI 的结束原因，以及 pty-host 的 `--clean-env` 只给出文件里的环境。六次反向验证（撤掉起点环境、来源判定两个方向、看门狗、退出原因、pty-host 的环境）都让对应回归断言失败。

未覆盖：fish（本机没装，脚本按 fish 的 `$argv` 惯例写，未实测）和 csh/tcsh 登录 shell；Windows；真实 iOS、网页或 Android 的界面操作，以及真实 CLI 发消息后的完整链路；daemon 重启时恰好停在启动中的登录 shell 会话（恢复路径的看门狗逻辑有单元测试，没有做成端到端）。测试 session、server、daemon、tmux 与临时配置均已清理；只本地提交，不推送、不发版。

## 2026-10-17：从终端开的会话按终端的 PATH 找 CLI

macOS arm64，本 worktree 构建的二进制，独立的 `REMOTE_AGENT_HOME`（/tmp 下短路径）、tmux socket 和 HOME，只起 daemon，不连 server；未碰日常 daemon、账户或 `~/.local/shims`。三个假 codex（`--version` 输出 `codex-cli 0.163.0`，其余情况把 `FROM=<标识>`、`$0` 和环境写进文件后 `exec sleep 600`）：daemon PATH 里一个（daemon），只在「终端 PATH」里一个（terminal），测试 HOME 的 `.zshrc` 加进 PATH 的一个（shell）。新建请求走本地 socket 的 Connect JSON 调用和本 worktree 的 `remote-agent new`，属于调试触发，没有经过 server 和客户端界面。

- 带 `env.PATH=<终端目录>:<daemon 目录>:/usr/bin:/bin` 的请求：会话跑的是 terminal 那个（`$0`、pty-host 的 argv 都是终端目录的路径），环境里有请求带的标记变量；daemon 日志记下「a session resolved its CLI」。从 PATH 只含终端目录的 shell 里跑 `remote-agent new`，结果相同
- 不带 `env` 的请求（客户端）：仍经登录 shell，跑 shell 那个，`shell-bin` 记的是它
- 请求的 PATH 里没有 codex：会话照常启动，用 daemon 当时记着的 CLI（这时已学成 shell 那个），日志有「the session's PATH has no such CLI; starting the daemon's」警告
- 停掉终端会话，先开一个客户端会话让 daemon 学到 shell 那个，再 `remote-agent resume`：恢复后跑的仍是 terminal 那个，按 `env.json` 里的 PATH 重新找到

单元测试覆盖 PATH 里第一个可执行的普通文件胜出（空项、相对路径、不可执行文件、同名目录都跳过）、显式配置的路径不被当成命令名查、找不到时回退、`doctor` 的会话（`agent.env` 设了 PATH 也）不变、恢复和分叉按保存的 PATH 找、定时任务从终端会话派生的新会话按来源的 PATH 找；逐项撤掉对应的判断后这些测试都会失败。未覆盖：分叉和定时任务的端到端（假 CLI 不产生 transcript，不能分叉）；Claude Code（同一段代码，只测了 codex）；Linux。测试进程、tmux 和临时目录均已清理。

## 2026-10-17：终端会话找不到 CLI 时失败

macOS arm64，本 worktree 的 Go 自动测试，加一轮本 worktree 构建的隔离环境（`ios/scripts/dev-env.sh start --daemon`，独立 `RA_DEV_DIR`、server、账户和 tmux socket；没有连日常 daemon 或账户）。dev-env 默认给 daemon 传 `--codex <绝对路径>`，属于「配置了路径」，不走 PATH 查找，所以端到端那一轮把 daemon 换成不带 `--codex` 重启（与 `daemon.sh install` 装的服务一致）。

- 从终端新建、恢复、分叉和从终端会话派生的定时新会话，在终端 PATH（或 `env.json` 里保存的 PATH）中找不到 codex 时直接失败，错误为 `codex was not found in the terminal's PATH; install it there or set REMOTE_AGENT_CODEX`（恢复、分叉和定时任务为 `the session's saved PATH`）。没有创建 tmux 会话；新建和分叉也没有留下会话目录。
- 没有 PATH 的环境与 PATH 里没有 codex 一样失败；定时任务连源会话的 `env.json` 缺失也按失败记录。
- 定时任务把失败记进执行历史，`reason` 是 `could_not_start_session: ` 加同一句错误。
- 反向验证：临时把 `terminalCLI` 改回返回 daemon 的 CLI，上述用例全部失败；恢复后 `go test ./...` 通过。
- 端到端：在 `PATH=/usr/bin:/bin:/usr/sbin:/sbin` 的 shell 里跑 `remote-agent codex`，终端打印 `remote-agent: codex was not found in the terminal's PATH; install it there or set REMOTE_AGENT_CODEX`，退出码 1，没有会话目录也没有 tmux 会话；PATH 前面放一个假 codex 再 `remote-agent new`，pty-host 的 argv 和画面都是这个假 codex，而不是 daemon 启动时找到的 `~/.local/shims/codex`。
- 未覆盖：客户端界面上恢复和分叉的报错展示、定时任务的实际派发链路（这几条只有单元测试）；Claude Code（同一段代码，只测了 codex）；Linux。测试 server、daemon、tmux 和临时目录均已清理。
