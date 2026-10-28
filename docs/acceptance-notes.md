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


## 设备加入批准（协议 8，2026-10-18 UTC）

隔离临时账户、独立数据库和 daemon 状态目录、独立 tmux socket；中继和网页使用动态分配的 loopback 端口，未连接日常账户。直接在本机 Chrome 的网页表单填配对码，网页与 Go CLI 的四个图案一致，CLI 明确允许后网页登记成功；数据库只新增相应候选身份，准入记录已消费。随后从网页选择临时目录并启动真实 Codex，会话收到固定验收消息并将固定回复返回网页。此流程由界面操作触发，未向日常会话注入消息。

自动检查包括保留根密钥但更换设备身份的再次登记攻击、候选身份/类型替换、过期、取消、批准方撤销、并发 bootstrap、响应丢失的原样重试、登记回滚以及服务重启后取消持久批准；Go、Web、Android 和 iOS 使用共同的固定合成向量验证批准签名与身份绑定的配对 transcript。

这些结果只证明设备加入门禁及其正常链路，不证明未来密文的密码学撤销。未在 Android/iOS 真机直接完成新流程的界面验收；原生核心通过真实临时 Go server 的集成测试验证。内容密钥世代与不可信中继下的撤销完成条件尚待实现，见 [plan](plan.md#设备撤销的加密边界)。

配对批准重试额外验证：网页模拟服务端提交后响应丢失，重试必须发送相同的批准及配对密文；Go server、Android、iOS 实测交付后的同一次批准仍可确认。iOS 另外在真实临时中继下模拟交付确认网络失败，候选方返回的已登记身份仍可登录。

批准失败界面补充自动检查：Web 直接点击允许和失败条的重试按钮，Android/iOS 调用界面使用的状态模型，均确认网络失败后重用同一批准且不取消旧操作；过期后明确换码并取消旧操作，不沿用上次同意。Web 另覆盖断网重连保留操作；原生状态模型覆盖退出与换码，Android 另覆盖迟到回调不能完成新操作。未在原生真机点击这些失败按钮。

## 设备安全域（协议 9，发布前验收）

2026-10-18，未发布工作树。固定授权端、随机成员时期密钥、完整对象授权与自动执行隔离的设计见 [architecture §9](architecture.md#9-账户设备和密钥)。下面记录已实际验证的范围，不代表已部署。

- Go 全量通过（20 个包、1011 个测试及子测试），核心授权和中继的定向 `-race` 并发检查通过；包含空闲执行端同步隔离、删除完成证明、伪造旧 root 控制、持久受理、时钟回拨、真实处理器的去重与撤销后拒绝。`govulncheck` 未发现代码调用到的已知漏洞。
- 网页类型检查、构建、依赖审计及 760 项测试通过，包含未知请求重新授权失败时仍保留同一请求、旧图片单独标记未验证的回归。使用真实 Go 授权端生成签名和 HPKE；模拟恶意中继提供假授权域时，候选端未经本机图案确认或明确拒绝都不会保存信任状态。
- Android 核心 228 项、终端 7 项、App 206 项、正式构建的 lint 与编译通过。独立测试包在 Android 11 真机运行 3 项实际 Keystore 测试，验证账户包装、计数器跨实例持久化、通知子密钥修复和损坏安全状态拒绝覆盖；没有覆盖日常使用的应用数据。
- iOS 核心 437 项通过；iPhone 18 Pro / iOS 27 模拟器 App 310 项通过，含本机图案确认的等待与拒绝、真实 Keychain 中的计数器和通知子密钥、损坏状态拒绝覆盖。统一设备入口、旧图片标记、跨时期重试及测试窗口激活后，全套 310 项再次通过。
- 本机 Chrome 扩展直接操作隔离的本地中继、daemon、独立 tmux 与真实 Codex 0.163.0。候选浏览器和 CLI 核对相同四个图案，先在候选端确认，再由发起方批准。网页终端输入到达真实 CLI；网页聊天发送后收到 `SECURITY_E2E_WEB_OK`。
- 同一环境增加两台临时执行电脑。网页一次点击删除其中一台，显示等待保留电脑；另一台保留电脑上线后后台自动安装隔离，页面变为“设备移除已完成”，没有逐台人工确认。核对被移除设备登记已清除、墓碑存在，该设备的本地 CLI 重新登录被拒；原会话在新时期继续收到 `SECURITY_E2E_AFTER_REMOVAL_OK`。

手动升级另用旧版二进制建立格式 3 的临时账户与一条会话，再执行文档中的 3→4→5 SQL；设备公钥、登记签名与会话行保持不变。新版拒绝未手动升级的第 1 代状态；手动改为第 2 代并执行安全初始化后，原 root、设备 seed 与身份全部保留，新的公开创世状态成功固定到中继。这个存储演练使用占位程序，真实 CLI 链路由上一组独立验收覆盖。

四端 473 份生成文件与 proto 完全一致。全部运行材料和截图保留在任务工作区的验收输出，不含入库的真实账户密钥。完整改动的独立审查与正式产物状态以对应源码提交和 Release 记录为准。本轮没有验证真实恶意服务器入侵、硬件失陷或备份回滚；这些也不能由一次端到端操作证明。管理权转交与灾难恢复未实现，旧格式历史仍只读且未验证。


## 协议 10：A–H 结构调整（2026-10-18，本地候选）

本记录针对尚未提交、尚未发布的协议 10 工作树。设计见 [architecture §9](architecture.md#9-账户设备和密钥)，默认升级路线见 [self-hosting](self-hosting.md#upgrading-to-the-phone-authority-protocol-10)。

- Go：`gofmt -l internal cmd` 无输出；`go vet ./...`、`go test ./... -count=1` 通过。保留持久受理日志、最多一次执行、执行 gate、严格链延伸和恢复测试。
- 网页：`pnpm typecheck`、`pnpm test`（774 项）、`pnpm build` 通过。
- Android：核心 265 项、App 209 项通过；`:app:lintDebug`、`:app:assembleDebug`、`:app:assembleMinified` 通过。并行构建曾耗尽临时磁盘空间，清理本工作树的增量编译缓存后，以单 worker、关闭 Kotlin 增量编译完成同一组检查。
- iOS：`ios/scripts/check.sh` 的核心测试与 `buf generate` 通过；洁净检查因本次协议生成文件尚未提交而失败。本任务禁止提交，因此保留这些改动，并单独执行脚本的 App 构建与测试步骤；复用现有 iPhone 18 Pro 模拟器，301 项 App 测试通过。
- 新增回归覆盖独立租约的 72 小时上限、续期不增长成员链且不影响待恢复请求、发布响应丢失与配对批准后重启、等候候选确认收件、当前 checkpoint 的密文持久化与无历史前缀读取、租约到期暂停内容发布并在续期后恢复、吊销保留档案与显式删除，以及跨四端的普通手机交接向量。Android 交接另经真实 Go 中继与合成客户端走过一遍。

这些是隔离的自动测试和模拟器测试。新功能没有额外做真实 CLI 与手指操作的端到端验收，没有操作真手机、日常 daemon、生产中继或现有部署。高级账户保留路线未做手工迁移演练。界面稿源文件已核对；Chrome 的本地文件预览被浏览器 URL 策略拒绝，未完成亮暗主题和 200% 字号的浏览器目视验收，也未切换内置浏览器。

## 任意客户端管理：网页批次 C（2026-10-20，本地候选）

当前 checkout 的网页管理界面接入批次 B 的 API，协议仍为 10。本记录不代表三端联合功能已发布。

- `pnpm install --frozen-lockfile`、`pnpm test --maxWorkers=4`、`pnpm build` 和 `go test ./...` 通过。界面回归覆盖管理端/普通 Client/Machine 的入口，换码和交接 offer 的离线保存确认、保存失败、未完成操作、配对取消响应丢失及管理端退出提示。
- 本机 Chrome 扩展与 `playwright-cli`：用 `?demo` 核对 390 与 1440 宽、亮暗主题、管理/非管理/Machine、配对码与二维码/SAS、账户安全、交接准备、档案、恢复待处理及未登录入口。新 v48 管理界面稿核对亮暗；账户安全页另用调试注入把字号放大到 200%，不等同于系统字号真机验收。
- 产品链路从独立 `RA_DEV_DIR` 的 `dev-env.sh start --no-account` 开始：临时账户/数据库/状态目录/tmux socket/自动分配端口，显式允许临时网页 origin。Chrome 直接操作创建账户、确认恢复码保存、发配对码；CLI 先确认 SAS，网页再批准，测试电脑加入并启动 daemon。会话由测试 CLI 创建，网页直接发送无文件操作的简短消息，经本地 server → daemon → tmux → 真实 Codex 0.163.0，网页收到约定回复。没有用调试调用代替消息发送或配对批准。
- 同一临时账户在网页移除电脑，确认原档案仍可进入重接；再经单独确认删除记录，中继电脑记录数为 0。网页退出时核对唯一管理端的针对性提示，再清除临时浏览器密钥。测试结束关闭测试标签并清理隔离环境；未操作生产中继或日常 daemon。

未覆盖：档案实际重接、六条跨端有向交接、Android/iOS 真机、48 小时恢复的真实等待及全部重启矩阵、真实 Web Push 的允许/拒绝/后台投递、浏览器部分/全部存储损坏和操作系统字号。保存失败等故障由自动测试注入；demo 是视觉检查，不能代替上述真实链路。界面与文案规则见 [design-system](design-system.md#8-文案)，当前界面稿为 [v51](design/client-v51.html)。


## Claude 模型菜单短标签（2026-10-23，本地候选）

macOS、本 worktree 构建的 daemon、Claude Code 2.1.300；使用 `scripts/acceptance-claude.sh` 的独立状态目录、tmux socket 和临时工作目录。通过本地 CLI socket 驱动真实 tmux 会话，没有经过 server，也没有直接操作网页、Android 或 iOS，没有操作日常 daemon 或会话。

- 基线复现模型切换、切换事件记录和切回失败，实际错误为 `timed out waiting for the cursor on Sonnet 5.5`；菜单画面与外部行为见 [claude-code-notes §9](claude-code-notes.md#9-切换模型和-effort)。修复后的两次完整脚本均为 `all checks passed`，第二次包含移动后再次核对完整模型名的保护。
- 最终脚本覆盖消息和事件顺序、批准/拒绝、单选/多选、中断、本地草稿排队、模型与 effort 切换和切回、设置文件校验值不变、权限模式切换、计划批准、停止/恢复及 daemon 重启。模型切换事件和切回是脚本的无输出断言，失败会计入结果。
- `TestSetModelShortLabels2_1_300` 和 `TestModelRowShortLabels2_1_300` 使用两张真实菜单 fixture 和现有目录 fixture，验证短标签、最后一行说明保留、Default 行排除及同家族不同版本不混淆；`TestSetModelRechecksShortLabelDetail` 验证移到目标行后名称不符时不确认。分别撤掉对应修复后测试失败，恢复后通过；新控制流程测试使用 `testing/synctest`。
- `go test ./internal/claude -count=1`、`go test ./...` 和 `git diff --check` 通过。临时 daemon、tmux 和状态目录已清理；未修改协议或存储格式，没有推送、发布或部署。未验证其他 Claude Code 版本、短标签菜单的小窗口折行，或 CLI 本次未列完整缓存目录的原因。

### 定时任务第一阶段

2026-10-24，macOS，协议 11；当前 worktree 构建，未发布。后端和协议范围见 [architecture §18](architecture.md#18-定时任务)。

- 自动测试：`go test ./...`；`go test -race ./internal/daemon ./internal/server ./internal/relay -run 'TestSchedule|TestCalendar|TestMCP|TestTaskOwned'`。覆盖纽约春季不存在时刻、秋季两次相同时刻只取早一次、上海、工作日、补跑、截止及每天一次；任务会话隐藏和接手、退出与恢复、运行中不关 CLI；通知的可用条件、三次上限及跨重启计数、final 摘要；MCP 递归委托和周期最小间隔；现有布局 6 库直接启动、默认分页排除/按 ID 查询、旧修订不能改归属，以及 heads 不一致后重传。
- 网页：`cd web && pnpm typecheck && pnpm test`，84 个文件、1065 项通过。Android：`cd android && ./gradlew :core:test :app:testDebugUnitTest` 通过。iOS core：`cd ios/RemoteAgentCore && swift test`，83 个 suite、527 项通过。iOS App：`xcodebuild -quiet -project ios/RemoteAgent.xcodeproj -scheme RemoteAgent -destination 'platform=iOS Simulator,name=iPhone 18 Pro' -parallel-testing-enabled NO build test`，328 项通过，零失败。三端验证新字段解析、通知白名单和既有列表对任务归属的处理，没有新增定时界面。
- iOS App 验收修正了终端测试的传输替身：原来的真实 socket 指向拒绝连接的端口，会在首次发送后停止，导致后续帧断言失败；改用只接受帧的内存 socket 后，保留的行为断言及全套 App 测试通过。默认并行模拟器测试启动曾停在 Xcode 收集诊断，最终以串行方式完成；本次启动的模拟器已关闭。
- 上一轮变异验证的范围：当时只检查了日历解析、截止、关闭方法内部、接手、通知次数及重放、MCP 周期下限及运行期禁令、结束标注、server 排除和 relay 推送抑制这 11 个指定位置，没有删除实际创建路径的归属赋值，也没有断开生产调度循环的关闭调用。因此它不能证明创建和自动关闭的接线已经覆盖；这两个缺口由下方第一轮反馈的生产路径测试补齐。编译错误不算检出。
- 真实 CLI：`ios/scripts/dev-env.sh start --daemon` 在独立 `RA_DEV_DIR` 启动本地 server/daemon、临时账户、数据库、状态目录和 tmux socket；`RA_DEV_DIR="$RA_DEV_DIR" go run ./scripts/acceptance-schedule` 再建立可控的临时管理客户端和测试电脑，用原生 Claude Code 2.1.300、`claude-haiku-5-5` 走加密 Forward → server → daemon → tmux → CLI → 加密事件回读。创建约两分钟后运行的 UTC 日历任务，CLI 实际调用一次 `notify_user`，运行记录标记已通知，结束后 CLI 退出；默认分页里没有该会话，按 ID 能解密读到会话和历史、通知事件与结束标注；再用客户端恢复并发送一条消息，接手后出现在默认列表。最终一次完整验收通过，环境已停止并清理。
- 操作边界：这是 Go 协议客户端的自动验收，不是网页、Android 或 iOS 的直接界面操作。Claude 的该次 MCP 工具审批在隔离终端里单次允许，没有持久添加免审批规则。没有接线上中继、日常 Remote Agent 账户或既有会话；真实 CLI 使用已登录的 Claude 凭据并消耗少量额度。
- 未覆盖：真实 Codex 的这套新增流程、真手机、FCM/Web Push 实际送达、真实夏令时切换及休眠；失败/中断关闭与 daemon 重启补传由自动测试覆盖。`notify_user` 成功只证明已持久记录供投递，不证明用户收到。

复现时从当前 checkout 在同一个 shell 执行，需已登录的 Claude Code、tmux 和 Go：

```sh
export RA_DEV_DIR=$(mktemp -d)
trap 'ios/scripts/dev-env.sh stop' EXIT
ios/scripts/dev-env.sh start --daemon
go run ./scripts/acceptance-schedule
```


### 定时任务第一轮反馈

2026-10-24，协议仍为未发布的 11，产品版本未改。此轮只在任务 worktree 提交，没有推送、发布或部署；设计现状见 [architecture §18](architecture.md#18-定时任务)。

| 反馈 | 修法与回归测试 | 实际检出的变异数 |
|---|---|---|
| 1 | 自动派发按 ScheduledAt 的当地日期占用，skipped 不占；下一次只依据 LastCalendarDate。`TestCalendarCatchupOccupiesOccurrenceDateOnly` 覆盖跨午夜补跑、跳过及重启 | 2 |
| 2 | 手动日历试跑保存自动游标，不消耗自动日期或次数，达到自动 max_runs 后仍可手动试跑；until 仍是硬截止。`TestCalendarManualRunPreservesAutomaticOccurrence` 使用 max_runs=1 | 5 |
| 3 | 创建/修改拒绝过去的 until，过期任务不能立即运行，改钟点后马上公布真正下一次。`TestCalendarDeadlineAndEditBoundaries` | 3 |
| 4 | 无用户消息的 CLI 后续轮继承持久上下文、MCP 禁令和结束标注；后续通知仍更新原运行记录。`TestScheduledBackgroundFollowupKeepsContext`、`TestHumanInterjectionEndsMCPRestrictionAndFollowupNoticesRemainRecorded` | 1 |
| 5 | 只认 CLIENT/LOCAL，明确恢复由真实 ResumeSession RPC 写带来源的恢复事件，接手可重放。`TestTakeoverNeedsVerifiedHumanOrExplicitResume`、`TestExplicitResumeRPCClaimsScheduledSession` | 2 |
| 6 | 用户中途插话清上下文和本轮定时标注，该轮恢复普通结束推送规则。`TestHumanInterjectionMakesScheduledTurnOrdinary`，relay 的无标注事件规则由既有推送测试覆盖 | 1 |
| 7 | transcript 未成功读完就推迟关闭。`TestScheduledClosureWaitsForReadableTranscript` | 1 |
| 8 | 用户受理和关闭共用生命周期顺序，先受理就接手，关闭已开始就等待并恢复；失败后退避五秒，新提交事件可提前唤醒。`TestScheduledCloseAndHumanAdmissionHaveOneOrder`、`TestScheduledCloseBacksOffUntilNewEvent` | 2 |
| 9 | 活跃或历史 uncertain 运行都排除自动关闭，并先观察运行状态再决定清理。`TestUncertainRunNeverClosesAutomatically` | 1 |
| 10 | 游标和恢复内部状态照常持久化，但不推进任务 CAS/机器任务修订；客户端可见变化仍推进。`TestScheduleRevisionIgnoresProgressCursors`，并核对既有 late cursor 测试 | 1 |
| 11 | 三端保留任务审批等待处理项和项目计数，仅排除普通进行中/已结束列表；网页待处理卡强制带项目。网页 flows、Android/iOS Timeline 回归 | 7 |
| 12 | 最后一条 final 的完整文本去掉 Markdown 标题符号、合并空白后截前 200 字。`TestRunSummaryUsesWholeFinalAnswer` | 1 |
| 13 | MCP run/resume 也校验周期下限。`TestMCPRunAndResumeRecheckMinimumIntervals` | 1 |
| 14 | 核实隐藏会话仍计入试用账户 200 个会话的限额；超限从 server 经 relay 明确回传 relay_session_limit，运行持久失败并停止自动续跑。server 实际填满隐藏会话、relay HTTP 回调和 daemon 持久重启测试分别覆盖 | 3 |
| 15 | scope 与 architecture 明确披露明文定时通知类别及普通结束推送模式的侧信道。`TestScheduledPushMetadataDisclosure`；变异删除整个披露段落 | 2 |
| 16 | `TestScheduledCreationWritesOwnership` 直接走真实创建和落盘；`TestRealScheduleLoopClosesCompletedCLI` 启动真实调度循环，不直接调用关闭方法。旧记录的覆盖说法已收窄 | 2 |

变异脚本为 `python3 scripts/check-schedule-regressions.py --clients`，共 35 个独立位置；逐次恢复源文件，全部由实际断言失败检出，不把编译错误或没有跑到测试算通过。网页、Android、iOS 的待处理和项目投影分别做了删除修复的验证。文档的两次变异删除整个隐私披露行。

最终验证：`go test ./...`，以及 `go test -race ./internal/daemon ./internal/relay ./internal/server -run 'TestSchedule|TestCalendar|TestMCP|TestHumanInterjection|TestTakeover|TestRelaySession|TestHiddenSessions|TestUncertain|TestExplicitResumeRPC|TestRealScheduleLoop|TestRunSummary'`；`cd web && pnpm typecheck && pnpm test`（84 文件、1066 项）；`cd android && ./gradlew :core:test :app:testDebugUnitTest`；`cd ios/RemoteAgentCore && swift test`（83 suite、528 项）；串行 `xcodebuild -quiet -project ios/RemoteAgent.xcodeproj -scheme RemoteAgent -destination 'platform=iOS Simulator,name=iPhone 18 Pro' -parallel-testing-enabled NO build test`（328 项、零失败）。

真实 CLI 复验使用上方同一复现命令、独立的 RA_DEV_DIR、本地 server/daemon、临时账户、状态和 tmux socket，Claude Code 2.1.300 / Haiku 5.5。Go 加密客户端创建日历任务，经 server、daemon、真实 CLI 调用 notify_user，运行结束退出；默认列表隐藏、按 ID 回读、恢复回复接手全部通过。新增检查在允许那一次 MCP 调用之前，先从中继按活跃运行会话 ID 解密读回 TaskOwned=true 的会话，并确认其中有审批/提问/计划或弹窗等待字段；三端回归测试确认这份数据进入「需要处理」及项目计数。没有将该检查冒称为直接操作客户端界面。

临时环境已清理，模拟器测试已结束。未覆盖的仍是真手机、实际 FCM/Web Push 送达、真实 Codex 的这套新增链路，以及真实休眠和夏令时切换；日期边界、并发、重启及结果不确定场景由自动测试覆盖。没有改变账户配额，也没有声称按 ID 取回的数据属于 server 默认列表。


### 定时任务第二轮反馈

2026-10-24，仍为未发布的协议 11，产品版本未改。以下验证在任务 worktree 完成，仅本地提交，没有推送、发版或部署；当前设计见 [architecture §18](architecture.md#18-定时任务)。

| 反馈 | 修复验证与回归入口 | 本轮实际检出的变异数 |
|---|---|---|
| 1 | `TestOrdinaryHumanReplayDoesNotWriteMetadata` 重放 300 条人工 QUEUED，以 session.json 的原始字节哨兵检出重复写入；普通会话的内存上下文仍会清除。既有接手测试核对任务归属变化 | 1 |
| 2 | `TestColdListIncludesLiveTaskOwnedApprovals` 从真实 server 的默认 ListRemoteSessions 读取；网页 paging、Android/iOS DirectoryRefresh 从加密列表冷启动并再刷新，核对待处理、项目归属及普通列表过滤，未直接填 remote.sessions。SQL 及三端待处理/项目投影分别删除修复 | 7 |
| 3 | `TestRecurringResumeAndManualFailures` 覆盖 interval/calendar 恢复、max_runs 和各类手动结果；`TestManualTrialFailureBoundaries` 走派发失败、输入暂停后取消、无法确认、中断以及显式暂停/取消的生产路径。手动无法确认不会迟到接回并覆盖自动计划；显式取消保留空游标 | 5 |
| 4 | `TestSkippedManualTrialRestoresAutomaticDispatch` 真实走试跑过期跳过路径，再走自动派发和 max_runs，核对 RunNow、Manual、Dispatches 和当地日期 | 1 |
| 5 | `TestMCPRestrictionUsesCurrentTurnNotQueue` 对 create/update/run 分别核对：当前人工轮后排定时消息仍允许、当前定时轮后排人工消息仍拒绝；既有新开会话保护测试保留 | 2 |
| 6 | `TestHumanAdmissionBoundsRunningResumeRetry` 用 tmux 仍存在而 Exited=true 的状态核对 errRunning 和恰好两次尝试。5 秒 context 仅防回退后无限重试，不把短耗时作为成功标准 | 1 |
| 7 | `TestPublicationRefusalBeforeAndAfterAdmission` 覆盖报告早于身份回写、已排队、已开始三种顺序，核对持久失败、队列撤回和真实控制写入中的中断键。`TestScheduledCreationWritesOwnership` 核对创建时持久化运行 ID；`TestQuotaStopRetriesAndDoesNotInterruptHuman` 让真实调度循环处理 Enter 后迟到的确认，并核对人工轮不受中断 | 4 |
| 8 | `TestMCPDispatchReportsMinimumIntervalReason` 分别验证 interval 和重复 after_turn 的专用原因；既有权限变化原因测试仍通过 | 1 |
| 9 | `TestFollowupSurvivesLaterSkippedRun` 实际插入较新 skipped 后重载任务，验证原运行的通知、摘要、运行 ID 和幂等游标；同时覆盖任务仍调度及已终结两种状态 | 2 |

本轮新增 24 个变异位置，连同上一轮 35 个共 59 个均已实际验证。入口为 `python3 scripts/check-schedule-regressions.py --clients`，也可用 `--only r2-` 定位本轮。此次完整跑过原有及新增位置，后加的显式取消位置和收紧后的 Android 项目断言另行定向复验；未把编译失败当成检出。Android 项目测试先断言项目数再访问 single，避免把空集合异常算作断言证据；本轮 Android 变异要求输出实际 AssertionError。

最终检查全部通过：

- `go test ./...`。
- `go test -race ./internal/daemon ./internal/relay ./internal/server -run 'Test(Schedule|Calendar|MCP|Human|Takeover|RelaySession|HiddenSessions|Uncertain|ExplicitResumeRPC|RealScheduleLoop|RunSummary|OrdinaryHumanReplay|RecurringResume|SkippedManual|PublicationRefusal|QuotaStop|Followup|ColdList|TaskOwnedPaging|ManualTrial)' -count=1`。
- 网页 `pnpm typecheck && pnpm test`：84 文件、1067 项。
- Android `./gradlew :core:test :app:testDebugUnitTest`：Core 379 项、App 301 项，零失败。
- iOS Core `swift test`：83 suite、529 项。

真实 CLI 验收为本地隔离 server/daemon、临时账户与状态目录、独立 tmux socket，Claude Code 2.1.300 / Haiku 5.5。`scripts/acceptance-schedule` 经加密 Forward 创建日历任务；到点前重启一次 daemon，确认保存的任务仍可读取并派发；真实 MCP 审批已挂起时再重启一次，先通过加密 GetLiveSnapshot 确认重放后的会话 TaskOwned=true 且 PendingApprovals>0，再用没有会话缓存或订阅的新 Go 加密客户端从默认 ListRemoteSessions（未传 session_ids）读回并解密，确认该会话仍在待处理数据里。随后单次批准 notify_user，核对通知、摘要、CLI 退出、已结束默认分页排除、按 ID 历史和人工恢复回复接手。全部通过。

复现时保持同一 shell，让退出清理生效：

```sh
export RA_DEV_DIR="$(mktemp -d)"
./ios/scripts/dev-env.sh start --daemon
trap './ios/scripts/dev-env.sh stop' EXIT
go run ./scripts/acceptance-schedule
```

临时环境已清理。上述操作是 Go 合成客户端和 tmux 审批触发，未声称直接操作三端产品界面；三端真实列表解密及筛选由客户端回归测试验证。未覆盖真手机、FCM/Web Push 实际送达、真实 Codex 同链路、真实休眠和夏令时切换。


### 定时任务第三轮结构重做

2026-10-24，未发布的协议仍为 11，产品版本未改。只在任务 worktree 本地提交，没有推送、发版或部署。目标结构见 [architecture §18](architecture.md#18-定时任务)。前两轮记录保留为当时的验证；其中影子状态和满额专用控制的实现、测试预期与变异位点已被本轮替换。

| 反馈 | 结构及删除项 | 回归与实际变异 |
|---|---|---|
| A | `ManualRequest` 保存独立请求时间/ID，与自动计划共用唯一 Active；所有触发类型的额外执行均不计 max_runs。删除 RunNow、全部 Automatic 字段和 restoreManualSchedule；手动生命周期不保存或还原自动状态，也不触发自动迟到接回 | `TestManualExecutionLifecycleMatrix` 为 4 种触发 × 11 个边界，含请求重启、dispatching 重启、queued 重启、派发失败、跳过、输入暂停与各种结束结果；`TestManualControlMatrix` 为 4 种触发 × 待发/已开始 × 暂停/暂停恢复/取消。另覆盖状态准入、uncertain 原迟到游标、权限变化拒绝、双向串行和补跑窗口、暂停恢复不重复占日历日期。14 个变异位置 |
| B | 满额只关联新开会话，通过创建时持久的 ScheduledRunID 找到 Active 或恢复记录；failed/relay_session_limit 记录交给原自动关闭 worker。删除 stopUnpublishedScheduledRuns、专用撤回/中断及 interruptRunBy；沿用同一读记录、归属检查、生命周期排序和失败退避 | `TestPublicationRefusalUsesScheduledClosure` 为自动/手动 × 报告在关联前、关联前重启、已排队、已开始、已结束；检查持久失败、幂等及关闭。`TestRelayQuotaIgnoresResumeSession`、`TestQuotaClosureReusesReadAndTakeoverSafety`、`TestQuotaClosureDoesNotBlockScheduleLoop` 验证 resume 不入路径、读失败/接手不关、ctlMu 被占用不阻塞调度。8 个变异位置 |
| C7 | interval 首次时间固定为相位锚点；恢复保留未来游标，否则沿原相位向上取点，跳过已消费的自动时刻，忽略手动历史 | `TestIntervalResumePreservesPhase` 覆盖显式 At/初始延迟、未来/过期/空游标、有无手动历史及整点边界；`TestIntervalInitialDelayPhaseSurvivesEditAndRestart` 覆盖修改定义后保存、重启及再次计算。4 个变异位置 |
| C8 | 两端全部项目的进行中计数排除 taskOwned，需要处理计数继续包含 | Android/iOS `allProjectCountsExcludeQuietTasksButIncludeApprovals` 核对直接计数、全部项目菜单、待处理列表和进行中列表。2 个变异位置 |

变异脚本仍为 `python3 scripts/check-schedule-regressions.py --clients`。本轮新增/重做 28 个位置，加上仍适用且已重跑的 45 个，共 73 个全部实际检出；完整执行 71 个后，新增的迟到准入和恢复满额关联两个位置分别定向验证。每次恢复原文件，编译失败不算检出。旧机制对应的变异已删除，没有保留针对已删除字段或函数的检查。

最终验证：

- `go test ./...` 通过。并行验证时一条既有 tmux 子进程握手测试曾超时，单独全量重跑通过；没有改动该测试。
- daemon、relay、server、schedule 的相关 `-race` 通过；新增恢复满额关联另跑 daemon 的满额/关闭相关 race。
- 网页 `pnpm typecheck && pnpm test`：84 文件、1067 项通过。
- Android `./gradlew :core:test :app:testDebugUnitTest`：Core 380 项、App 301 项，零失败。
- iOS Core `swift test`：83 suite、530 项通过。

真实 CLI 使用独立 RA_DEV_DIR、临时账户、本地 server/daemon 和独立 tmux socket，Claude Code 2.1.300 / Haiku 5.5。运行 `scripts/acceptance-schedule`，经加密 Forward 创建每天的日历任务，max_runs=1：

1. 到点前重启 daemon，读回保存的任务，再请求立即运行。
2. 比较手动请求和完成前后的自动状态、原因、NextRunAt、Dispatches、LastCalendarDate，全部相同；手动运行已完成且 CLI 退出，自动计数仍为 0。
3. 手动审批挂起时再重启 daemon；加密 GetLiveSnapshot 确认 TaskOwned=true、PendingApprovals>0，新的无缓存客户端从默认 ListRemoteSessions 冷读到待处理数据，然后单次批准 notify_user。
4. 原定当天自动时刻照常派发真实 CLI，再次核对审批与通知；结束时共两条运行记录、一次自动派发、自动当地日期与原计划相同、任务按 max_runs=1 完成。
5. 核对自动 CLI 退出、已结束默认列表排除、按 ID 读回通知和带定时标注的结束事件、用户恢复回复后重新进入默认列表。

以上全部通过，临时环境已清理。调用来自 Go 合成加密客户端，审批由 tmux 按键触发，未声称直接操作三端产品界面；需要处理筛选由三端测试验证。没有额外验证真实满额账户下的 CLI 关闭、真实 Codex 同链路、真手机或实际推送送达。


### 定时任务第四轮反馈

2026-10-24，仍是未发布的协议 11，未改产品版本。只在任务 worktree 本地提交，没有推送、发版或部署。此轮修正第三轮把 interval 相位写进用户定义的做法；当前设计见 [architecture §18](architecture.md#18-定时任务)。

| 反馈 | 修法、回归入口与证据 | 本轮实际变异数 |
|---|---|---|
| 1 | 相位独立保存在 IntervalPhase，触发定义不再被派生 at 改写；只改非时间字段保持相位，更新用 nextInterval 越过已消费点，派发前另核对上一次自动非跳过 ScheduledAt。`TestIntervalDefinitionRoundTripDoesNotRedispatch` 为省略/显式 at × 客户端/MCP × 无尾记录/手动/跳过，含保存重启、时区偏移原值和只改标题；`TestAutomaticDispatchRejectsConsumedOccurrence` 独立塞入陈旧游标验证最后防线 | 3 |
| 2 | 手动派发前再查状态：lateRuns 或 uncertain 继续等待；自动任务失败或终结后把原排队请求记为带原因的 skipped。迟到确认及保存失败回滚都按运行 ID，不依赖最后一条历史。`TestPendingManualWaitsForAutomaticConfirmation`、`TestPendingManualSkipsWhenAutomaticStops`、`TestLateConfirmationFindsRunByID`、`TestLateConfirmationRollbackKeepsOriginalRunID` 覆盖等待、失效、尾部插入手动/跳过记录及落盘失败 | 5 |
| 3 | resume_session 的锁前阶段只选择目标；取得生命周期锁后再检查 Exited，复用持锁的恢复入口并在同一顺序下受理输入，旧 worker 排空期间仍按既有规则释放锁、重新检查代际。`TestScheduledResumeRechecksAfterAutomaticClose` 用真实关闭方法和 barrier 复现选择后已关闭、关闭持锁时准入等待两种顺序，并验证旧关闭请求不影响恢复后的代际 | 1 |
| 4 | 选择持久队列作为接手事实来源：公共队列保存入口在成功受理或已替换的受理不确定边界接手，启动时也检查 Queue/Held。这样关闭路径不需要重复检查队列，QUEUED 事件丢失和元数据写入未完成都可修复；只改归属，不解除当前轮定时上下文。`TestAcceptedHumanQueueOwnsSessionWithoutQueuedEvent` 覆盖 CLIENT/LOCAL、系统/未知/nil 来源和事件失败/目录同步失败/正常保存；`TestCanonicalHumanQueueRepairsOwnershipOnRestore`、`TestRejectedHumanQueueDoesNotTakeOwnership` 覆盖恢复及未受理反例 | 3 |
| 5 | failed 的 at、非重复 after_turn 在 resume 时返回 FailedPrecondition，明确提示额外立即执行或修改定义。为使提示可执行，允许在这类失败后显式请求一次手动重试，以 RetryFailed 标明意图，不保存/还原自动计划；失败前已排队的普通请求仍跳过，失败的周期任务仍须恢复或改定义。`TestFailedOneShotResumeRequiresExplicitRetry`、`TestOneShotPendingBeforeFailureIsNotExplicitRetry`、`TestFailedRecurringRunStillRequiresResume` 验证错误不改变任务、提示可执行、自动次数不变和准入边界 | 3 |

`python3 scripts/check-schedule-regressions.py --clients` 的当前 88 个位置全部实际检出，本轮新增 15 个；每组原测试先通过，再临时变异，并在 finally 恢复源文件。相同测试命令在一次运行中只做一次原测试检查；编译失败不算检出。本轮保留的三端需要处理、项目与全部项目计数回归也在脚本中通过原测试和变异验证。

最终 `go test ./...` 全部通过；daemon、relay、server、schedule 的相关 `-race` 通过，包含本轮 interval、挂起请求、迟到回滚、关闭竞争、队列接手及失败一次性任务测试。MCP 正常测试随 Go 全量通过。本轮未改网页、Android、iOS 产品代码，没有额外重跑完整客户端套件。

真实验收使用独立 RA_DEV_DIR、临时账户、本地 server/daemon、独立 tmux socket，Claude Code 2.1.300 / Haiku 5.5，入口仍为 `scripts/acceptance-schedule`。日历任务的手动额外运行、当天原定自动运行和 max_runs=1、审批中重启、默认 ListRemoteSessions 冷读待处理数据、notify_user、CLI 退出、历史回读和人工接手均通过。

新增 interval 真实链路：通过加密 Forward 创建每小时一次的任务，让真实 CLI 完成首次运行；从接口读回整份 Spec，只改标题，再连同原触发配置、原 at 和修订号回交 update。连续观察四秒、覆盖多个真实调度 tick，仍然只有一次派发和原来的运行 ID，NextRunAt 与编辑前相同且晚于已完成 ScheduledAt，触发定义没有被改写。随后确认 CLI 退出并取消未来调度。此路径与原日历链路全部通过，临时环境已清理。

以上为 Go 合成加密客户端与 tmux 审批触发，没有冒称直接操作产品界面。人工存储故障和关闭并发由可控回归测试验证，未在真实 CLI 环境人为破坏磁盘；真手机、真实 Codex 同链路与实际推送送达未覆盖。

### 定时任务第五轮反馈

2026-10-24，仍是未发布的协议 11，产品版本未改。只在任务 worktree 本地提交，没有推送、发版或部署。当前行为见 [architecture §18](architecture.md#18-定时任务)。

| 反馈 | 修法与回归证据 | 本轮新增实际变异数 |
|---|---|---|
| 1 | 同时刻防重复派发只用于 interval、calendar、重复 after_turn。失败 at 的定义原样回交或只改标题后，仍派发原 ScheduledAt 并记录新运行；resume 的错误提示继续提供立即运行和修改定义两条路。`TestFailedAtDefinitionUpdateRetriesSameOccurrence` 验证两条定义回交路径；原周期去重和相位测试保留 | 1 |
| 2 | ManualRequest 的 RetryFailed 意图传入运行记录并跨重启保留。失败一次性任务的显式重试正常结束后，仍处于 failed 的任务改为 completed，原因 explicit_retry_succeeded；失败、中断保留原计划，用户期间的暂停或取消同样保留。`TestFailedOneShotResumeRequiresExplicitRetry` 核对成功结果；`TestFailedOneShotRetryOutcome` 为 at/非重复 after_turn × 成功/失败/中断 × 无控制/暂停/取消，覆盖运行前后重启、自动计数不变、完成后拒绝再次重试。普通手动执行矩阵继续核对不改变自动计划 | 3 |
| 3 | 后续扫描与待跟踪判断共用同一个上下文条件：模型已收到定时输入、有会话、上下文尚未关闭，不依赖运行结果。未收到输入的投递仍独占确认游标，防止后续扫描吃掉迟到确认事件。`TestNonCompletedScheduledFollowups` 为 interrupted/failed × 非终态/取消终态 × CLIENT 输入/LOCAL 输入/明确恢复/退出/另一运行接管，核对通知次数与正文、final 摘要、任务和机器修订、重启持久化、重复扫描不重计及上下文结束后不再计入；原迟到确认测试验证游标边界 | 3 |

本轮新增 7 个变异位置均实际检出；另重跑第四轮 15 个位置，全部检出。每组原测试先通过，再临时去掉修复或恢复错误条件；编译失败不算检出，源文件随后恢复。入口为 `python3 scripts/check-schedule-regressions.py --only r5-`（第 3 条补充边界后重跑 `--only r5-03`）和 `--only r4-`。本轮没有重跑全部客户端变异或客户端完整套件，三端产品代码未改。

最终 `go test ./...` 通过。首次全量执行检出了第 3 条扩大扫描范围后吞掉迟到确认游标的回归；补上「模型已收到定时输入」的上下文起点条件后，原迟到确认、手动请求和按 ID 接回测试及全量均通过。没有放宽原测试预期。

相关 race 检查通过：`go test -race ./internal/daemon ./internal/relay ./internal/server ./internal/schedule -run 'Schedule|Scheduled|Followup|FailedOneShot|FailedAt|Manual|OneShot|LateConfirmation|PendingManual|Interval|Calendar|MCP|TaskOwned' -count=1`，四个包均通过，覆盖本轮三个反馈和相关迟到确认路径。

真实 Claude 补测使用独立 RA_DEV_DIR、临时账户、本地 server/daemon、独立 tmux socket，Claude Code 2.1.300 / Haiku 5.5。临时 Go 合成加密客户端创建普通会话和 at/resume_session 任务；定时消息让 Bash 在后台等待外部文件，再在前台运行另一个等待命令。通过加密 Interrupt 中断前台轮，随后在测试环境创建后台等待的文件，没有发送新的人工消息。审批由 tmux 按键触发。

两次都观察到原运行保持 interrupted、任务保持 paused，后台完成自己开的后续轮摘要记回原运行。第一次 notify_user 也成功，原运行保存通知次数 1、正文和 final 摘要；临时程序因反复解码未清空旧 Active 指针而卡在等待，结果从隔离 daemon 存储核对，不能算自动脚本全通过。修正临时程序后再跑，第二次模型把中断理解为用户拒绝继续，主动不调用 notify_user，虽然后台后续轮及其摘要仍正常记回；因此不声称真实 CLI 完整通知场景已稳定自动验收。定向回归以可控事件覆盖通知、摘要、可见修订和全部上下文终点。没有重跑整套日历/手动/自动真实 CLI 链路，没有直接操作产品界面，也未验证实际推送送达。

两套临时环境和临时验收程序均已清理。

### 定时任务第六轮反馈：前台通知

2026-10-24，未改协议和产品版本，只在任务 worktree 提交，没有推送、发版或部署。定时结果的前台显示规则见 [architecture §12](architecture.md#12-推送通知)。Android 另将当前导航目的地接入 Activity 的 STARTED 生命周期，离开会话页、进入后台或离开已登录界面时清除可见会话；不使用后台保留的导航栈作为可见依据。网页在同一个窗口上同时核对可见、聚焦和精确解码后的会话路由，多窗口不能把一个窗口的聚焦和另一个窗口的路由拼起来。

Android `ForegroundPushTest` 直接调用实际 FCM 回调，并通过 Robolectric NotificationManager 核对系统通知：前台/后台 × 首页/目标会话/其他会话 × 定时/待处理/轮次结束/账户安全四种类型，共 24 个组合，含定时通用正文及点击跳转会话。`VisiblePushSessionTest` 覆盖切换会话、非会话页面、进入后台、回到前台与移除 UI；现有 DisplayTest 和 ApprovalAttemptTest 保留通知隐私和本地偏好验证。

网页 `sw.test.ts` 执行实际 service worker push listener：四种通知 × 首页/其他会话/目标会话/未聚焦/隐藏/ID 前缀/子路由/编码路由/无效编码，并覆盖多窗口、无会话和无效 URL；已有账户安全的前台同步、后台通知点击和隐藏窗口测试继续通过。`sw.test.ts` 与 `push.test.ts` 共 48 项通过，`pnpm --dir web typecheck` 通过。

`python3 scripts/check-schedule-regressions.py --clients --only r6-` 的 8 个新位置全部实际检出：Android 恢复旧前台拦截、去掉当前会话例外、去掉后台条件、去掉生命周期清除；网页恢复旧前台拦截、去掉当前会话例外、去掉焦点条件、去掉可见性条件。每组原测试先通过，变异后必须产生断言失败，编译失败不算检出，最后恢复原文件。恢复后网页相关 48 项和类型检查再次通过；Android `./gradlew :app:testDebugUnitTest` 全部 303 项通过，零失败。

本轮为 Robolectric 和 service worker 单元测试，没有重走真实 CLI 或测试 FCM/Web Push 服务的实际送达、真机系统通知显示；iOS 和 Go 产品代码未改。

### 定时任务第七轮反馈：按不可变创建来源清理

2026-10-24，只在任务 worktree 提交，协议和产品版本未改，没有推送、发版或部署。采用会话元数据里已有的不可变创建来源，不新增存储字段或迁移；当前规则见 [architecture §18](architecture.md#18-定时任务)。

逐处检查 schedule 相关的定义读取，修正以下三处：

| 位置 | 结果 |
|---|---|
| `scheduledClosures` | 不再按任务当前目标过滤历史。结束、满额拒绝和 uncertain 三张表以任务/运行 ID 为键，表内会话 ID 再与创建元数据匹配；后来的其他运行不影响创建运行的清理 |
| `SessionPublicationFailed` | 按会话创建时保存的任务/运行 ID 关联 Active 或历史记录，不再要求当前目标是 new_session；改目标后到达的旧报告仍持久记录，重复报告保持幂等 |
| `sessionHasScheduledRunLocked` | 收到派发消息前的 MCP 预防限制同样按创建任务/运行 ID 精确判断，删除对当前目标类型的依赖 |

其余读取已核对：`launchScheduledSession`、`queueScheduledSession` 用派发前复制的独立任务快照；公开 update 在 Active 存在时拒绝替换定义，所以 `watchScheduledRun`/`endScheduledRun` 的活动执行期间不会换定义。`prepareSchedule`、`nextSchedule`、相位/日历计算、截止与 MCP 延迟准入读取当前定义是安排未来执行；`lastAutomaticRun` 按运行自己的 Manual/Status 判断。`notifyScheduled` 读当前标题只是缺省通知标题的展示规则，不用于判定历史执行类型、关联或清理。会话创建元数据只在 `createByContext` 初始化，恢复和人工接手均保留这对 ID。

`TestRejectedCreationSurvivesTargetEdit` 覆盖自动/手动 × 排队/运行中 × 直接/重启：先收到满额拒绝并选中 closer，清理完成前通过公开 update 改成 resume_session，连续三次清理扫描仍保留原拒绝标记，最后沿既有 killIf 关闭 CLI。`TestLatePublicationRefusalAfterTargetEdit` 覆盖自动/手动 × 直接/重启：运行先结束并改目标，再收到旧会话的满额报告，原运行被正确记录失败，重复报告不增加修订号，忙碌的原 CLI 仍被清理。`TestClosureUsesCreationRunIdentity` 覆盖创建运行和后续运行的 completed/uncertain/满额失败互不覆盖；`TestPreDeliveryRestrictionUsesCreationIdentity` 直接验证预防限制只依赖创建来源并核对任务/运行 ID 不匹配的反例。旧测试的合成会话补齐真实创建元数据；普通 resume 保留的是先前创建运行 ID，而非把本次 resume ID 伪装成创建来源。

变异入口：`python3 scripts/check-schedule-regressions.py --only r7-`，5 个新位置均实际检出，包括恢复两处当前目标过滤、错误选择后续运行、混用其他运行的 uncertain 和恢复 MCP 的当前目标判断。此前的 `--only r3-B-` 8 个满额路径变异，以及 `--only 09-uncertain`，也全部实际检出；因清理表已改为创建 ID 键，相关旧变异位点同步更新。每个原测试先通过，变异后必须断言失败，编译失败不算检出，源文件随后恢复。

本轮使用 Go 隔离 harness、可控事件和伪终端验证，没有重走真实 CLI、中继真实满额账户或产品界面。网页、Android、iOS 产品代码未改。

最终 `go test ./...` 全量通过。相关 race 命令 `go test -race ./internal/daemon ./internal/relay ./internal/server ./internal/schedule -run 'Schedule|Scheduled|Publication|Quota|Creation|LateConfirmation|Manual|Interval|Calendar|MCP' -count=1` 四个包均通过，包含本轮改目标、迟到报告、重启和创建来源隔离，以及既有读记录、接手、生命周期竞争与清理退避测试。

### 定时任务第八轮反馈：通知准入先追赶 transcript

2026-10-24，仍是未发布的协议 11，产品版本未改；只在任务 worktree 提交，没有推送、发版或部署。通知准入规则见 [architecture §18](architecture.md#主动通知与客户端发现)。

`notifyScheduled` 先在会话读取锁内读完并提交可用 transcript，再在同一读取顺序中判断上下文并追加通知；读取或提交失败返回 Unavailable，明确要求 retry notify_user，不使用上一次轮询的归属或次数。任务标题快照仍在取得会话锁之前读取，保留原有锁顺序。

`TestNotifyUserReadsTranscriptBeforeAdmission` 使用实际 Codex/Claude parser、临时 transcript 和事件提交路径，覆盖两种 CLI × 首次定时轮/旧定时轮切换到新运行。测试只把输入置于已投递、尚未读到 receipt 的边界，MCP 前不调用轮询或 PermissionRequest hook；Claude 记录使用 bypassPermissions。旧轮已经用满三次通知时，新轮的首次调用仍成功，通知事件和运行计数都归属新任务、新运行及新 turn，旧轮仍保留三次。

`TestNotifyUserRequiresCommittedTranscript` 覆盖 transcript 路径不可读和事件日志提交失败：缓存里仍是合法定时轮，但文件已有新的人工输入，通知必须先返回可重试错误、不增加次数、不按旧轮提交通知。恢复文件或日志后再次调用，先提交人工接手记录，再按普通轮明确拒绝，证明恢复后也不使用陈旧上下文。

失效的 `r2-05-current-turn` 已改为当前创建来源条件：任务 ID、运行 ID 与会话创建元数据匹配；变异删掉这两项关联，让普通轮后排队的 resume 执行错误套用预防限制，仍由原 `TestMCPRestrictionUsesCurrentTurnNotQueue` 检出。本轮新增变异分别去掉主动读取和忽略读取失败，要求两个新测试产生断言失败；编译失败不算检出。

`python3 scripts/check-schedule-regressions.py --clients` 不带 `--only` 完整执行到结束、退出码 0：87 个 Go 与 23 个网页/Android/iOS 变异，共 110 个全部检出。执行后逐一核对结果名与全部定义一致，所有变异目标均已恢复。

本轮回归使用隔离 Go harness 与合成的原生 CLI transcript，没有真实 CLI、产品界面或推送服务送达验收；三端产品代码未改。

最终 `go test ./...` 全量通过。daemon、relay、server、schedule 的相关 race 检查通过，筛选包含本轮 `TestNotifyUser*`；同一筛选在 schedulemcp 包未匹配测试，已补跑 `go test -race ./internal/schedulemcp -count=1`，全包通过。

### 定时任务第九轮反馈：统一请求准入

2026-10-24，只在任务 worktree 提交，协议和产品版本未改，没有推送、发版或部署。完整请求清单、每处处理及不依赖实时定时身份的路径说明集中在 [architecture：定时上下文的请求准入](architecture.md#定时上下文的请求准入)。

抽出唯一的 `session.admitScheduledContext`，读取与第八轮 notify_user 相同的 `readMu`，读完并提交全部可用记录后才执行判断回调；失败返回明确可重试的 Unavailable。绑定会话的 Schedule 统一从外层进入，之后才取得 scheduleMu，通知不再有独立读取分支。显式用户绑定调用同样追赶记录，机器级管理权限保留；没有绑定会话、只读持久任务定义的机器查询不臆造一个实时会话准入。

除四个 MCP 写操作和 notify_user 外，MCP 的 list/get/pause/cancel、Hook 的 SessionStart/PermissionRequest/有事件的 PreToolUse、Interrupt 的两次当前轮判断，以及 Stop/Resume/自动恢复的退出结算均复用该函数。权限等待不持有读取锁。退出结算从 bool 改为 error，把读取失败沿 Stop/Resume 请求原样传回，不能被转换成普通“未完成”判断或继续使用旧标注。原生事件提交归约、历史标注推送筛选、不可变创建来源和持久队列接手均按清单单列，未混同实时准入。

回归证据：

- `TestMCPAdmissionReadsTranscriptBeforeEveryMutation`：真实 Codex/Claude parser × create/update/resume/run，各用新的普通 resume_session，没有创建来源兜底；定时输入已在 transcript 但未轮询。四个 MCP 操作全部返回明确的定时上下文 PermissionDenied，任务状态不变，当前身份导入为正确任务/运行。
- `TestAllScheduleOperationsRequireCommittedAdmission`：九个 Schedule 操作 × transcript 读取失败/事件提交失败，均返回 Unavailable，不改任务或通知计数；恢复后先提交人工接手，再按新上下文执行，notify_user 按普通轮拒绝。
- `TestContextRequestPathsShareAdmission`：Interrupt、Hook 事件、退出结算各覆盖先于轮询的成功路径和读取失败；结算生成的缺失 TurnEnded 带正确任务/运行标注，失败不发控制键。
- `TestHookStartAndPermissionRequireAdmission`：SessionStart 和 PermissionRequest 的读取失败不会被忽略。
- `TestScheduledContextKeepsInspectionAndStoppingAvailable`：定时轮的 list/get/pause/cancel 仍可用且先导入当前身份。
- `TestOwnerScheduleKeepsAuthorityAfterFreshAdmission`：用户 CLI 的绑定调用也刷新状态，保持管理权；源会话读取失败返回 Unavailable，机器级无绑定查询仍可用。

第八轮的通知先于轮询、旧通知次数用满后的新运行归属、读取/提交失败与恢复回归继续通过。新增 8 个变异分别断开 Schedule 接线、跳过公共读取、忽略公共读取失败，以及断开 Interrupt、PreToolUse、退出结算、PermissionRequest、SessionStart 的公共准入接线；每组要求原测试通过、变异后出现断言失败，编译失败不算检出。

验证结果：`go test ./...` 全量通过；`go test -race ./internal/daemon ./internal/relay ./internal/server ./internal/schedule ./internal/schedulemcp` 五个包全部通过。`python3 scripts/check-schedule-regressions.py --clients` 不带 `--only` 完整执行到退出码 0，95 个 Go 与 23 个三端变异，共 118 个全部实际检出，包含本轮 8 个。执行后核对全部结果名与定义完全一致，所有变异目标均已恢复。

本轮使用隔离 harness、实际原生 parser、合成 transcript 和本地 Connect handler 测试；没有重走真实 CLI、实际推送送达或产品界面，三端产品代码未改。

### 定时任务第十轮反馈：安全控制与通知身份

2026-10-24，只在任务 worktree 提交，协议和产品版本未改，没有推送、发版或部署。入口清单已按本轮原则重写，见 [定时上下文准入](architecture.md#定时上下文的请求准入)：严格读取只覆盖 create/update/resume/run 与 notify_user，不能因为读取失败禁用中断、审批、hooks 或停止调度。

| 反馈 | 修法与回归 |
|---|---|
| 1 | Interrupt 两次刷新改为尽力读取，失败记日志，按缓存 running 发 Esc。`TestInterruptSurvivesTranscriptFault` 覆盖读取与事件提交失败；第九轮的新鲜轮次读取测试继续保留 |
| 2 | SessionStart、PermissionRequest、PreToolUse 改为尽力刷新，失败不返回 Unavailable；缓存中的远程审批照常受理。`TestRemoteApprovalSurvivesTranscriptReadFault` 在读取目录失败时收到真实审批卡并由 resolve 允许；`TestHooksRefreshOrUseCachedContext` 保留新鲜导入和失败缓存两种验证 |
| 3 | list/get/pause/cancel 不进入 transcript 准入；严格任务写操作在读取锁与调度锁共同持有时完成判断，释放读取锁后继续持调度锁准备与保存。`TestScheduleSafetyOperationsBypassTranscript` 在读取锁被占用且 transcript 不可读时验证本地/MCP 四类操作；`TestScheduleSaveReleasesTranscriptLock` 阻塞 schedules.json 保存，验证读取锁可用而其他任务控制不能超越保存；本地四类严格操作与第八、九轮的先于轮询/读取提交失败测试仍通过。退出结算保留改造前的未决输入安全规则，不再包装为 MCP 身份准入 |
| 4 | 发消息触发的恢复使用系统来源，生命周期锁覆盖恢复与队列持久受理；恢复本身不清归属。失败重启的 CLI 重新允许原关闭流程处理，成功以既有持久队列接手为准；明确点击恢复仍接手。`TestImplicitResumeWaitsForDurableInputOwnership` 覆盖 CLIENT/LOCAL × 图片过期/额度满/队列保存失败/成功/事件追加失败/目录同步不确定，并核对落盘归属与实际关闭；`TestExplicitResumeStillClaimsTaskSession` 保留明确恢复语义 |
| 5 | Web tag 增加公开事件去重键，类别仍在末尾；FCM 同时携带 event，Android 用会话/event 的 tag 区分显示，普通通知数字 ID/替换不变。`TestScheduledPushDisplayIdentity`、`TestScheduledFCMIncludesEventIdentity` 核对不同事件共存、同事件重投及实际 FCM 数据；网页实际 worker 测试核对保留 tag 和前台规则；Android `ScheduledPushIdentityTest` 通过 NotificationManager 确认两条结果共存、重投不新增、普通通知仍替换 |

第九轮中要求安全操作因读取失败返回 Unavailable 的测试预期已按本轮产品决定替换，保留新鲜读取的正向覆盖。对应变异同步改为检验新鲜读取或恢复原退出结算的读前边界；没有保留与本轮决定冲突的预期。

本轮验证：`go test ./...` 全量通过；`go test -race ./internal/daemon ./internal/relay ./internal/server ./internal/schedule ./internal/schedulemcp` 五个包全包通过。网页 `pnpm --dir web typecheck` 与 `pnpm --dir web exec vitest run src/sw.test.ts src/push.test.ts` 通过，共 49 个测试；Android `./gradlew :app:testDebugUnitTest` 全量通过，304 个测试，无失败、错误或跳过。

`python3 scripts/check-schedule-regressions.py --clients` 不带 `--only` 完整执行到结束，退出码 0：103 个 Go 与 24 个网页/Android/iOS 变异，共 127 个全部实际检出，包括本轮新增的 9 个。新增位点覆盖中断和审批恢复严格读取、调度安全操作恢复准入、任务落盘重新持有读取锁、隐式恢复提前接手与未重新交给关闭流程、Web tag 丢失事件键、FCM 缺少事件键、Android tag 丢失事件键。每组先通过原测试，变异后要求断言失败，编译失败不算检出；最终核对结果名与全部定义完全一致，源码恢复后只有本轮文档待提交。

环境为隔离 Go harness、实际原生 transcript parser、伪终端、网页实际 worker 脚本与 Robolectric NotificationManager。FCM 测试使用本地假服务核对实际请求数据；没有重走真实 CLI、真实推送服务送达或真机产品界面，iOS 产品代码未改。


### 定时任务第十一轮反馈：运行中的显式恢复接手

2026-10-24，只在任务 worktree 提交，协议和产品版本未改，没有推送、发版或部署。归属规则见 [定时任务的触发与执行](architecture.md#触发与执行)。

`resumeContextBy` 遇到 errRunning 时，若来源明确为 CLIENT/LOCAL 且会话仍 TaskOwned，在尚未释放 lifecycleMu 时复用 claimOwnershipLocked 保存人工接手，并返回现有会话。它不另起 CLI，不清除当前轮次定时上下文；没有任务归属、系统或未知来源的已运行恢复仍返回原错误。

`TestExplicitResumeClaimsRejectedImplicitLifetime` 经真实恢复与队列受理路径制造 CLIENT/LOCAL × 图片过期/额度满/队列保存失败，再冻结下一个调度 tick 前的窗口：先选出关闭候选，再通过 ResumeSession handler 显式恢复，核对成功响应、TaskOwned 落盘解除、创建 ID 保留、没有新增终端命令、新扫描不再选中，以及此前已选中的候选执行 killIf 时也放弃关闭。`TestRunningResumeTakeoverRequiresHumanOwnedTask` 核对系统/未知来源与普通会话的行为不变。新增变异 r11-explicit-running-takeover 禁用这条接手分支，要求原测试通过、变异后断言失败。

`go test ./...` 全量通过；`go test -race ./internal/daemon ./internal/relay ./internal/server ./internal/schedule ./internal/schedulemcp` 五个包全部通过。本轮定向变异已实际检出。

`python3 scripts/check-schedule-regressions.py --clients` 不带 `--only` 完整执行到结束，退出码 0：104 个 Go 与 24 个网页/Android/iOS 变异，共 128 个全部实际检出，包含本轮新增位点。原测试先通过，变异后要求断言失败，编译失败不算检出；执行后逐项核对结果名与全部定义完全一致，所有源码变异目标均已恢复。

验证环境为隔离 Go harness、本地 Connect handler、伪终端和合成 transcript；没有真实 CLI、真机界面或推送送达验收，三端产品代码未改。


### 定时结果通知标题：按执行来源补齐

2026-10-24，只在任务 worktree 提交，协议和产品版本未改，没有推送、发版或部署。标题规则见 [architecture §18](architecture.md#主动通知与客户端发现)，签名推送与冷启动行为见 [§12](architecture.md#12-推送通知)。

`TestScheduledNotifyTitleUsesExecutionSource` 覆盖 new_session/resume_session × 自动/手动 × 活动运行/修改定义后/修改定义后重启，并分别发送缺省标题与 agent 显式标题。运行预留、来源落盘和输入队列受理走实际 daemon 路径，CLI 创建用既有隔离会话模拟；测试先结束执行、把目标类型与任务名称一并改掉，再在原会话无新人工输入的后续轮次通知，验证来源取自该次运行而非新定义。恢复对话使用用户重命名，区别于首条输入推导的自动标题；新开任务名使用多字节字符并超过 60 字，核对按字符截断。原有先于轮询通知与重启计数测试的标题预期按恢复对话规则更新。

`TestScheduledNotificationTitleSurvivesColdDelivery` 重放已持久通知事件，在会话元数据为空或标题已变的情况下封装并实际解密、验签 NotifyContent，确认最终标题和来源事件序号保持不变，冷客户端不用自行推断目标类型。MCP 工具说明同步更新，客户端与 protobuf 字段未改。

`python3 scripts/check-schedule-regressions.py --only notify-title-` 完整检出 7 个定向变异：恢复对话误用任务名、按当前目标选择类型、按当前任务名显示、忽略显式 title、自动/手动派发漏写快照，以及签名内容重新依赖会话元数据。每组原测试先通过、变异后出现断言失败，编译失败不算检出；源码随后恢复。

最终 `go test ./...` 全量通过；`go test -race ./internal/daemon ./internal/relay ./internal/schedule ./internal/schedulemcp` 四个包全包通过。静态核对全部 135 个既有和新增变异目标仍存在；本轮实际执行的是上述 7 个定向变异，没有将静态检查算作完整脚本验收。

使用隔离 Go harness、合成 transcript、本地签名与加密测试；没有真实 CLI、真机冷启动或推送服务送达验收。web、android、ios 目录未修改。


### 无来源任务会话：登录 shell 派发与重启恢复

2026-10-24，只在任务 worktree 提交，协议和产品版本未改，没有推送、发版或部署。启动来源规则见 [architecture §18](architecture.md#触发与执行)，环境和 CLI 解析统一沿用 [§6](architecture.md#6-接入-codex)。

根因在派发时的来源分类：没有 SourceSession 时仍写 Shell=false，创建走直接启动并保存不含 PATH 的调用方环境。首次派发使用 daemon 已找到的 CLI 路径，非 clean-env 的 PTY 还可能继承 daemon 环境，所以首次启动可以成功；恢复按 Shell=false 的既有规则在保存的 PATH 查找 CLI，才确定报错。恢复的严格 PATH 查找本身没有改。无来源任务现经同一 createByContext 的客户端登录 shell 分支创建并保存 Shell=true；来源会话存在时的登录 shell/终端 PATH 继承规则保留。

`TestScheduledClientEnvironmentSurvivesDaemonRestart` 覆盖 Codex/Claude × 无来源/客户端来源 × 保存环境过期/缺失：断言登录 shell 的 clean-env 和命令名参数、非空起点 PATH、agent.env 叠加，且不传 daemon 缓存的 CLI 绝对路径；停止后由全新 Daemon 从磁盘恢复，再走现有显式恢复路径，验证启动方式仍正确、环境重新生成。既有终端来源成功查找和 PATH 缺失时拒绝的测试继续通过。`python3 scripts/check-schedule-regressions.py --only schedule-client-environment` 实际检出恢复旧 Shell=false 分类的变异：原测试通过，变异后断言失败，源码恢复。

真实 CLI 验收使用本机 Claude Code（`claude --version` 为 2.1.300）、Haiku `claude-haiku-5-5`、本 worktree 构建的修前与修后二进制。两侧各用短路径、独立的 RA_DEV_DIR、临时账户、本地中继、状态目录和 tmux socket；测试前保留修前二进制，未修改日常 daemon 或现有会话。通过 `go run ./scripts/acceptance-schedule --environment` 的合成客户端经加密 Forward 创建任务（SessionId/SourceSession 均为空），真实 Claude 收到排队提示并返回指定回复，原生 transcript 通过 daemon 和 server 回到合成客户端，执行完成后由原关闭流程结束 CLI；全程 daemon 只带 daemon 子命令，没有 --claude，并清除 REMOTE_AGENT_CLAUDE 覆盖。

| 阶段 | 真实结果 |
|---|---|
| 修前 | 首次派发收到真实回复并自动关闭；磁盘 Shell=false 且保存环境无 PATH；重启 daemon 后，客户端 ResumeSession 明确失败，提示 claude was not found in the session's saved PATH |
| 修后 | 首次派发由登录 shell 解析真实 Claude，Shell=true 且保存的是非空起点 PATH；自动关闭后重启 daemon，客户端恢复成功，再发送消息并收到第二次真实回复；随后停止会话，确认 tmux 中已无该 CLI |

新增验收模式通过 RA_DEV_AGENT_BIN 选择修前/修后二进制，RA_DEV_EXPECT_ENV_FAILURE=1 要求修前出现上述确切失败，不把任意启动错误算作复现。首次启动测试曾遇到临时中继进程生命周期和 macOS Unix socket 路径过长两个 harness 问题，修正为同一受控进程内保活、短路径 RA_DEV_DIR 后重新完整执行；这些失败不计入产品复现。两次最终验收均退出 0，临时中继、daemon、tmux 和状态目录已清理。

本轮真实链路由合成加密客户端触发，未重新操作网页或 Android 产品界面、未验收实际推送送达；web/android/ios 目录均未修改。既有日历审批验收未在本轮重跑，实际运行的是新增的环境验收模式。

最终 `go test ./...` 全量通过；`go test -race ./internal/daemon ./internal/relay ./internal/schedule` 三个包全包通过。


### 任务改名：保留执行与调度状态

2026-10-24，只在任务 worktree 提交，协议和产品版本未改，没有推送、发版或部署。方案与理由、仅标题差异的边界见 [architecture §18：修改与改名](architecture.md#修改与改名)。

选择在现有 update 中识别改名，客户端继续提交完整定义，不新增操作或协议字段。分支放在迟到执行确认、Active 检查和 prepareSchedule 之前，仍经过原 update 的严格 transcript 准入、任务归属与修订检查；MCP 的权限检查在副本上执行，不改写原委托。改名只更新 Spec.Title 和任务 Revision，其余任务字段与 lateRuns 关联保留。标题校验与普通定义准备共用，存储失败仍区分文件未替换与目录同步不确定。

`TestTitleOnlyUpdatePreservesExecutionState` 覆盖 CLIENT/LOCAL × 暂停、失败周期任务、失败的一次性任务、uncertain、运行中：前置状态走现有派发/暂停/排队/结束/无法确认路径，保留真实的手动请求和执行记录；逐字比较整个返回任务与期望值，除标题和修订号外不允许任何字段变化，再从磁盘重新读取比较。uncertain 场景预先写入迟到的 SUBMITTED 报告，改名自身不能接回或丢掉其关联，之后正常确认仍可执行。

`TestTitleOnlyUpdatePreservesDelegation` 覆盖用户和 MCP 改名，验证 Source/SourceSession/ScopedSession/ScopedMode 不变；MCP 当前权限变化也不重新冻结原委托。`TestTitleOnlyUpdateValidatesAndPersists` 覆盖空白/超长标题、过期修订号、原子写入失败和目录同步不确定。`TestOtherDefinitionChangesKeepUpdateSemantics` 分别改变消息、触发器、目标和补跑窗口，确认仍重排并按用户修改接管；活动执行存在时替换执行定义仍拒绝。

第五轮的一次性失败更新重试回归改为实际修改消息，保留原来完全相同定义更新的回归；不再把纯改名当重试。第四轮相位回归同时改消息和标题，继续验证完整定义替换的相位计算，避免新改名分支绕开原变异目标。

`python3 scripts/check-schedule-regressions.py --only title-update-` 完整检出 4 个新增变异：禁用改名分支、丢弃迟到运行关联、忽略其余定义字段、在原任务上重新冻结委托。另执行 `--only r4-01-update`，原相位变异也实际检出。原测试先通过，变异后要求断言失败，编译失败不算检出；源码恢复。全部 140 个变异位点的目标字符串静态核对仍存在，未将静态核对算作完整变异脚本执行。

最终 `go test ./...` 全量通过；`go test -race ./internal/daemon ./internal/schedule ./internal/schedulemcp ./internal/relay` 四个包全部通过。

本轮使用隔离 Go harness 和 Schedule 命令分派路径；没有真实 CLI、产品界面或推送送达验收。web、android、ios 目录未改。

## 定时任务：网页第二阶段（2026-10-24，本地候选）

基于协议 11、产品版本 0.45.7 的开发 checkout；仅网页与界面稿，没有发布或部署。使用本机 Google Chrome 156.0.8078.48 的独立无头配置，由 Playwright 驱动；真实 CLI 为 Claude Code 2.1.300 / Haiku 4.5。界面见 [client-v51 · 定时任务](design/client-v51.html#schedules)。

- 环境由 `RA_DEV_DIR="$(mktemp -d /tmp/ra-web.XXXXXX)" ios/scripts/dev-env.sh start --no-account` 创建；测试过程沿用该目录。网页通过 setup token 建临时账户，核对配对图案后用 `ios/scripts/dev-env.sh join CODE --daemon` 加入电脑。中继端口由脚本分配；独立数据库、daemon 状态与 tmux socket，不连接线上、不使用日常中继账户。网页使用 `pnpm --dir web build` 的本地预览，不使用演示数据。
- 390 和 1280 宽各创建一个几分钟后触发的每日新开对话任务，选择 Haiku 4.5 / 询问。两次均由时钟自动触发，未用 run 指令或改系统时间代替。通过网页批准真实 MCP `notify_user`，对话显示通知事件；任务列表与详情显示“通知了你”及正文。两次自动调度计数均为 1。
- 两个宽度均从运行记录进入对应会话；结束后通过已有的“恢复会话”流程启动真实 CLI，再发一条消息并收到真实回复，随后在会话列表看到已接手的会话。通过对话的“定时发送…”菜单创建当前对话任务，确认定时条与管理入口；最终构建的新间隔任务的首次计划时间为一个完整间隔之后（未等到该时刻执行）。另有一轮当前对话的定时消息经真实 CLI 完成、没有通知，出现在任务记录中。
- 新建任务的来源会话已在真实 daemon 的任务记录中核对；当前项目、机器与 CLI 的来源在加密请求中传递。删除验证任务先弹确认，再调用 cancel；后续计划消失，原会话保留。暂停、恢复、冲突、失败分组与稳定请求重试另有组件/状态测试。
- 停止测试 daemon、保留本地中继后重新加载网页，仍显示 5 个缓存任务及真实通知结果，提示上次状态；详情的立即运行、删除与编辑均禁用。缓存测试还核对磁盘只保存已验证的加密列表响应。
- 已保存手机/宽屏、亮/暗主题的任务列表、详情、新建页和对话定时条，共 16 张截图；另有接手、离线和大字号截图。大字号检查实际把任务行主标题由 15px 提到 30px，两种目标宽度未出现文档横向溢出；不等同于完整的系统辅助功能验收。截图随任务报告交付，不放进公开仓库。

调试触发与限制：

- 测试目录必须短。首次使用系统临时目录时，pty-host 的 Unix socket 超出 macOS 路径限制，CLI 未启动；改用短目录后从新账户重跑。首次目录与后续隔离环境均在验收结束后清理。
- 首次进入新测试目录的 Claude 文件夹信任提示通过独立 tmux socket 的按键确认；其余上述创建、审批、运行记录导航、恢复、回复、删除和定时发送均为浏览器界面操作。运行结果由真实 CLI 产生，没有向前端插入通知或会话数据。
- 此节的恢复与回复验收使用显式 `--claude` 覆盖，曾复现的无来源会话环境问题当时未由网页修复。后端现已修正登录 shell 派发，未指定 CLI 路径的真实网页复验见下方「后端修复后的网页复验」。网页在存在同项目/机器/CLI 来源时仍传入来源。
- 未做真手机、真实 Web Push 服务投递、睡眠补跑、跨夏令时或多台真实电脑联合验收；通知偏好、service worker 跳转、跨时区换算、修订与重连、静默轮折叠及多机器状态合并以自动测试为证。没有用演示页或单元测试代替上述真实 CLI 链路。

检查结果：`pnpm --dir web typecheck`、`pnpm --dir web test`、`pnpm --dir web build`、`go test ./...` 全部通过；网页 87 个测试文件、1136 项测试。任务分组、当天无通知合计、下次时间文案与静默轮折叠分别移除实现后，对应测试均失败；还覆盖运行记录按 ID 获取、短/虚拟长历史定位、离线缓存、修订冲突、创建重试、投递取消和宽度切换时的草稿保留。

末次复核追加了迟到通知、自主后续回复、跨短轮结束的工具分组、内容过大占位和跨日折叠边界：先用旧实现复现断言失败，再修复并通过。普通定时消息的排队/确认与空闲元数据仍能正常折叠。这些边界使用已记录事件的单元测试验证，未用真实 CLI 刻意制造。

## 定时任务：各端写法对齐（2026-10-24，本地候选）

按协调规则在 v49 原文件补充「各端统一的写法」，未另出版本；网页采用任务名设置行与会话改名同款的确认框，外地日历时区只在「什么时候」值后追加 IANA 名称。

- `pnpm --dir web typecheck`、`pnpm --dir web test`、`pnpm --dir web build` 通过；网页 87 个文件、1146 项测试。新增检查覆盖可选任务名与首句默认、改名的修订检查及同名不写入、草稿保留、当前对话位置、同区/异区与时区别名、时区保存、异常结果优先显示原因，以及停止任务的改名保护。
- 配对测试的异步二维码断言已完成压力复现与修复，见下节；重跑通过不作为偶发失败已解决的证据。
- 本机 Chrome 独立无头配置、390 / 1280 宽、亮 / 暗主题，使用开发示例 Remote 的合成任务检查网页真实组件。任务列表、详情、新建页与定时条的 16 张截图覆盖原文件；v49 手机/宽屏截图与改名、外地时区等补充截图一并更新。核对了任务名通过 create/update 的完整定义提交；这轮没有连接线上或日常账户，也没有重跑前一节的真实 CLI 调度验收。当前截图为文案和布局的示例数据，不能作为新的真实调度或推送送达证据。
- 此节曾在独立短 `RA_DEV_DIR` 复现旧后端的纯改名重新启用问题：创建明天执行的任务 → pause → 只改 title 后 update，状态从 paused 变为 scheduled；没有等到执行时刻，也未启动会话 CLI。测试清除了宿主的 `RA_SESSION`，环境已清理。后端纯改名分支与网页解除限制的复验见下方「后端修复后的网页复验」；当前网页不再保留按任务状态、活动执行或手动请求禁用改名的限制，同名保存仍不发更新。

## 配对界面：异步二维码断言（2026-10-24）

目标用例为 `web/src/views/security-ui.test.tsx` 的 `requires the candidate to confirm SAS before authority approval and waits for delivery`。刚 fetch 的 `origin/main` 为 `c7a831a3`；其目标测试、二维码组件、依赖清单、锁文件和 Vitest 配置与本分支修前完全一致。在独立 detached checkout 中测试，未改跟踪文件，仅链接同一套已安装依赖。

环境为 Node 22.23.3、pnpm 10.19.0、Vitest 5.0.2，10 核 CPU。每组使用 8 个 CPU 压力进程，每个进程自限 900 秒，组结束后统一终止并回收。每次新启动 Vitest，保留默认测试超时；整文件命令为 `pnpm exec vitest run src/views/security-ui.test.tsx --maxWorkers=1 --reporter=json --outputFile=<逐次报告>`，在 `web/` 下执行。

| 条件 | 执行次数 | 失败次数 |
|---|---:|---:|
| 未修改的 origin/main，只运行目标用例（加 `-t`） | 40 | 0（未复现的对照） |
| 未修改的 origin/main，整文件 | 40 | 1 |
| 本分支修复后，同命令、同压力运行整文件 | 40 | 0 |

修前第 23 次整文件运行在原第 208 行失败：图片查询为 `null`，与初次偶发失败一致，其他用例没有失败。根因是测试在点击的 `act` 返回后立即断言，而组件通过独立的 `import("qrcode-generator").then(...)` 生成二维码；React 工作队列清空不保证外部模块已加载。测试改为默认 `vi.waitFor` 等待实际二维码图片出现，每次尝试用 `act` 刷新 React 工作；不加固定 sleep，不调大超时，产品组件未改。

修后压力组共 840 次用例执行全部通过，无 React act 警告；另行执行 `pnpm --dir web typecheck`、`pnpm --dir web test`（87 文件、1146 项）、`pnpm --dir web build` 全部通过。逐次 JSON 报告、失败栈、源码指纹和重复运行脚本保存在本地验收输出中；临时 checkout 和所有压力进程已清理，未连接线上或日常账户。

## 定时任务：后端修复后的网页复验（2026-10-24）

rebase 后核对了两边的验收记录：后端原文全部保留，原有内容只更新一处 v48 → v49 链接；网页第二阶段、各端写法和配对测试三节均只保留一份。基于后端 `b8c7ef85` 的纯改名分支，网页去掉按停止状态、活动执行和手动请求禁用改名的条件，保留离线、正在保存和未确认请求保护。组件测试覆盖 scheduled、paused、completed、cancelled、failed、uncertain，以及 dispatching / queued / running 的活动运行和手动请求，核对发送完整定义且仅标题变化，继续使用原修订号。

核对 daemon 的通知事件与 NotifyContent 路径后，网页没有依赖任务当前定义或会话缓存推断通知标题的逻辑需要删除：聊天只显示事件正文，Web Push 使用中继给出的通用文案。v49 更正了此前「当前对话通知也用任务名」的规则和示例，统一采用新开对话的派发任务名、当前对话的会话显示标题、显式 title 优先；最终标题由 daemon 持久记录，客户端不重新推断。

真实链路使用本 checkout 构建的中继和 daemon、产品构建的网页预览、本机 Google Chrome 独立无头配置、Claude Code 2.1.300 / Haiku 5.5。独立短路径 `RA_DEV_DIR`，临时账户、数据库、状态目录、项目和 tmux socket；清除宿主 `RA_SESSION` 与 CLI 路径覆盖。网页建管理账户并核对配对图案，电脑加入后 daemon 以裸 `daemon` 参数启动，没有 `--claude` 或其他 CLI 路径参数。

- 网页从目录选择器添加全新项目，没有先创建来源会话；任务定义的 SourceSession 为空。通过网页新建名为「网页自然触发验收」的每日新开对话任务，选 Haiku 5.5 / 询问，在本机 America/Los_Angeles 的 11:34 创建，选择当天 11:37。时钟在 11:37:00.094 自动派发，自动次数为 1；未用 run、改系统时间或注入事件触发。
- agent 请求 `notify_user`，不提供 title。网页终端的回车按钮批准真实 CLI 的原生审批，事件经 daemon、中继回到网页，正文「网页自然触发通过」，运行记录为 completed / 通知了你。通过本地 CLI 只读事件核对，daemon 填入的最终标题为「网页自然触发验收」。未验收外部推送服务送达。
- 运行完成并自动关闭后，重新从网页运行记录进入该次对话，点击「恢复会话」，发送消息；真实 Haiku 回复「网页恢复回复通过」。返回会话列表可见该对话，点开仍是相同会话；本地事件存在真实 agent 回复，元数据 Shell=true、TaskOwned=false。没有通过调试入口恢复或发送消息。
- 随后通过网页暂停该任务，再从「任务名」设置行改为「暂停任务改名验收」；刷新仍显示已暂停且有「恢复」操作。比较磁盘保存的完整任务，除 Spec.Title 和 Revision 外全部相同，包括状态、原因、调度、来源和已完成的通知记录；没有重新派发。
- 首次目录信任提示仅通过隔离 tmux socket 确认新测试目录，这是本轮唯一直接给 CLI 发送的调试按键；创建、通知审批、运行记录导航、恢复、回复、暂停和改名均为网页界面操作。临时中继起初缺少预览来源的 CORS 放行，配对用交互终端结束也曾使后台 daemon 退出；修正测试环境来源和进程保活后完成上述完整链路，这两项不计为产品验收通过。

真实流程在 Chrome 默认窗口完成；另对 390 / 1280 宽的暂停改名和接手列表重截截图，并更新 v49 的亮暗边界与通知示例。截图、任务改名前后 JSON、真实事件和验证摘要保存在本地验收输出，未写入公开仓库。未重跑真手机、跨夏令时或多台真实电脑联测。

最终 `pnpm --dir web typecheck`、`pnpm --dir web test`（87 文件、1155 项）、`pnpm --dir web build`、`go test ./...` 全部通过。本轮没有改 Go、协议或产品版本；测试环境验收结束后清理，没有推送、发版或部署。

## 定时任务：三端整合（2026-10-24）

环境：`wt/scheduled-tasks-integration`，整合起点 `76e21788`；产品版本 0.45.7、协议 11。macOS arm64，Go 1.26.10、Node 22.23.3、pnpm 10.19.0、Xcode 27.0（27A266a）与 Swift 6.4。App 测试只指定已有的 iPhone 18 Pro / iOS 27.0 模拟器，没有新建模拟器或安装真机。本轮复核代码和各端验收报告，并在整合 checkout 跑自动检查；没有再启动三端真实 CLI 界面链路。

各端先前证据在此合并，不能算成本轮重新完成端到端验收：

| 客户端 | 已有操作与证据 | 限制 |
|---|---|---|
| 网页 | 见上方「网页第二阶段」与「后端修复后的网页复验」：本地独立 server/daemon/账户、真实 Claude Code，网页创建每日任务并等待自然触发、通知审批、从记录打开任务会话、恢复回复及接手、暂停改名；390/1280 宽与离线缓存已检查 | Chrome 独立无头配置由 Playwright 操作；后续文案截图有示例数据，不作为新的真实调度证据；没有真机或外部 Web Push 送达 |
| Android | 独立本地环境与 API 36 模拟器、Claude Code 2.1.300 / Haiku：通过 adb input/UIAutomator 操作界面建账户并配对、创建每日任务并自然触发通知、恢复回复/接手、当前对话任务及两次静默轮折叠、暂停/恢复/取消和冷启动离线缓存 | 后续 v49 的 15 项模拟器页面检查使用固定任务/会话数据，另有普通/200% 字号布局单元测试；不是新的真实 CLI 验收。独立开发包未启用 Firebase，没有 FCM 投递或真机锁屏证据 |
| iOS | 独立本地环境、原有 iPhone 18 Pro / iOS 27.0、Claude Code 2.1.300 / Haiku：创建每日任务并自然触发通知、按 ID 打开记录、恢复回复/接手、创建当前对话任务、立即额外运行和冷启动密文缓存；v49 重拍用真实 CLI 手动运行生成结果 | App 动作由 DEBUG signal/启动参数调用同一产品逻辑，没有直接点按；v49 重拍未再等待自然触发。静默折叠主要由投影/边界/变异测试覆盖，未额外跑两次真实静默自动轮；没有 APNs、真机或 iPad 验收 |

三端已实现独立定时入口、任务管理和运行历史、对话定时条与静默轮折叠、加密离线快照和修订订阅。核对后端 `ef453e06` 及 Android 当前通知路径，初次 Android 报告所列冷启动标题缺口已修复：最终标题由 daemon 按派发来源写入通知事件，Android 使用签名内容，不再按缓存改写。该结论有代码和自动测试支持，不代表 FCM 已实际送达。功能设计归 [architecture §18](architecture.md#18-定时任务)，全部后续验收事项归 [plan.md 的待确认](plan.md#待确认)。

本轮完整检查：

| 命令（未注明目录时从仓库根执行） | 结果 |
|---|---|
| `go test ./...` | 通过，退出码 0 |
| `pnpm --dir web typecheck` | 通过，退出码 0 |
| `pnpm --dir web test` | 87 个文件、1155 项测试通过，退出码 0 |
| `pnpm --dir web build` | 通过，退出码 0；已有静态/动态导入同一模块的分块提示 |
| `cd android && ./gradlew :core:test :app:testDebugUnitTest :app:assembleDebug` | 通过，退出码 0；core 393 项、app 326 项（修前 324 项），失败/错误/跳过均为 0，debug APK 构建成功 |
| `cd ios/RemoteAgentCore && swift test` | 541 项、84 个 suite 通过，退出码 0 |
| `xcodebuild -quiet -disableAutomaticPackageResolution -project ios/RemoteAgent.xcodeproj -scheme RemoteAgent -destination 'platform=iOS Simulator,name=iPhone 18 Pro' -resultBundlePath <独立结果目录>/ios-app.xcresult build test` | 实际以该已有模拟器的 ID 指定同一目标；退出码 0，339 项测试通过（动态参数展开后 377 次执行），0 失败/跳过。xcresult 的 build 状态 succeeded、errorCount=0；quiet 日志出现“command failed with exit code 0”文字，未将它视为编译失败或靠重跑消除 |
| `python3 scripts/check-schedule-regressions.py --clients` | 修复后完整复查 140 项通过（后端 116、客户端 24），退出码 0；原始测试基线通过后逐项验证断言能捕获变异，结束后源码恢复 |
| `python3 scripts/public_scan.py web/dist deploy/self-host` | 通过，退出码 0 |
| `python3 scripts/public_scan.py android/app/build/outputs/apk/debug/app-debug.apk` | 通过，退出码 0；仅检查本次 debug 产物，不代表签名发布产物已检查 |
| `git ls-files -z \| xargs -0 python3 scripts/public_scan.py` | 原始退出码 1，共 16 条命中，逐项核对后没有私有名单命中：12 条为允许的官方域名，3 条为扫描器及其测试自身的规则字面量，1 条为既有 `CLAUDE.md → AGENTS.md` 符号链接。官方域名在 v49 的命中沿自 v48，其余均在 origin/main 已存在。该脚本按发布包约束检查，不能直接把全源码的退出码当作产物检查通过 |
| `git diff --check` | 通过 |

首轮 `python3 scripts/check-schedule-regressions.py --clients` 在 139 项通过后退出 1：`r10-05-android-tag` 去掉 tag 中的事件键后，`ScheduledPushIdentityTest` 仍通过。根因是 Android 客户端把事件键加入数字通知 ID，旧用例的两个事件恰有不同哈希，无法发现 tag 丢失。新增两个有行为断言的用例后，进一步复现点击目标被覆盖：不同会话相同 `scheduled:7`，以及同一会话两个合法数字序号的哈希碰撞，均复用了同一个 PendingIntent；Intent 的 extras 不参与身份判断。修前专项 4 项中 2 项失败，分别得到错误的会话与错误的轮次。

这是整合分支 Android 定时通知路径引入的缺陷：通知 tag 已能区分事件，但点击 Intent 只用事件键的数字哈希。修复提交 `a21e9815` 将会话 ID 和完整事件键加入定时点击 Intent 的 action，普通通知行为保留；专项 4 项通过，完整 Android 检查也通过。`--clients --only r10-05-android-tag` 用新增碰撞用例核对原变异确实触发断言，不靠重跑掩盖失败。全量变异复查结果见上表。以上为 Robolectric 系统行为测试，不代表真实 FCM 送达或真机通知点击已验收。

公开检查读取本机实际 deny 名单，未使用空名单或伪造条目；扫描日志只以规则编号报告私有条目。本轮没有打发布包，因此未执行要求完整 Release 文件集合的 `--release TAG DIST` 模式，也未核对正式签名 APK、跨平台压缩包和附件清单。

既往 iOS v49 报告中的两项偶发失败均已通过后续专项复现定位：`RetainedIdentityTests.logoutClearsPendingBootstrapAndDoesNotResumeIt()` 是测试共享全局生命周期通知造成干扰；`ResumedLivePollingTests.resumedMetadataRestartsPollingWithoutResubscribing()` 是测试提前断言。复现、修复和重复运行证据见下方两项专项记录，首次整合全量通过本身不作为修复证据。未新增真实推送送达、真机界面、iPad、多台真实电脑、实际睡眠/DST 或客户端 Codex 路线的验收证据。

检查日志和 xcresult 保存在本地独立输出目录，不提交原日志、截图、测试账户或配对材料。未推送、打 tag、发版、部署或修改 `version.properties`。

### iOS 恢复后轮询：等待异步订阅操作结束（2026-10-24）

问题是 `ResumedLivePollingTests.resumedMetadataRestartsPollingWithoutResubscribing()` 第 190 行对 `liveJobs["s1"] == nil` 的即时断言。验证起点 `3d4a74e3`，目标测试和 `RemoteLive.swift` 与本地 `origin/main` 相同；macOS arm64、Swift 6.4、Go 1.26.10。使用 macOS 上的 `swift test`，不启动新的模拟器，不连接线上。

先用 `cd ios/RemoteAgentCore && swift test --filter ResumedLivePollingTests` 构建，再启动 40 个独立进程逐次执行 `swift test --skip-build --filter ResumedLivePollingTests`。保留外层 `ServerTests` 的 `.serialized` 与原有 suite 顺序，不打乱、不并发多个重复进程、不加额外 CPU 压力。每次两个测试，使用各自的临时 Go server、账户和状态；同一轮 App 单 suite 对照检查在已有 iPhone 模拟器运行，没有额外压力进程。

| 条件 | suite 运行次数 | 目标测试失败次数 |
|---|---:|---:|
| 原始代码 | 40 | 2（第 2、37 次） |
| 修复后，同命令与条件 | 40 | 0 |

两次修前失败均为同一行的 task 非空断言，没有其他断言或超时；记录问题后，原测试继续执行的恢复元数据与真实加密 live snapshot 链路仍完成。根因是 `events()` 和 `updates()` 把 touch/followers 生命周期操作异步排队；`finishEndedPollingForTest()` 只等待取得时的一个 task。稍后排队的操作可以创建另一个检查到会话已结束就退出的 task，此时瞬间看到非空是合法的中间状态，不表示恢复轮询失效。

只把这条即时断言改为现有 `eventually` 条件等待，明确等到该会话的 polling task 清空。没有修改产品轮询逻辑、增加固定 sleep 或调大现有等待工具的超时；保留原订阅、单 follower、恢复元数据和原订阅接收 live snapshot 的验证。

`cd ios/RemoteAgentCore && swift test` 全量通过：541 项、84 个 suite，退出码 0；默认需单独开启的真实 daemon 用例仍跳过。40 次修前与 40 次修后逐次日志、退出码和源文件指纹保存在本地专项输出，不提交运行日志或临时账户材料。

### iOS bootstrap：隔离测试的生命周期通知（2026-10-24）

目标为 `RetainedIdentityTests.logoutClearsPendingBootstrapAndDoesNotResumeIt()` 的 `.untrusted`。App 复现起点 `3d4a74e3`，下表原始重复运行使用同一份未修改的 App 测试产物，AppModel、bootstrap 与测试源文件指纹保持不变；交叉影响探针在复现后另行添加。RACore 测试中的条件等待改动不参与 App 构建。Xcode 27.0（27A266a）、Swift 6.4，仅使用已有 iPhone 18 Pro / iOS 27.0；不创建模拟器，不连接线上或使用日常账户。

App 先 `build test` 确认筛选和重复功能确实执行目标，再以 `test-without-building` 使用同一份未修改的产物。没有 shuffle 或失败自动重试。前五组显式指定 `-parallel-testing-enabled NO`，是不同调度配置的对照；最后去掉该覆盖，使用与首次验收一致的 scheme 默认调度，才复现原错误。

| 修前条件 | 目标运行次数 | 目标失败次数 |
|---|---:|---:|
| RetainedIdentity suite，40 次各自重启进程，无额外压力 | 40 | 0 |
| RetainedIdentity + AuthorityVault suites，同一进程，显式关闭 Xcode 并行，无额外压力 | 40 | 0 |
| 同上，8 个有限 CPU 压力进程 | 40 | 0 |
| 加 AppModel suite，10 个有限 CPU 压力进程 | 40 | 0 |
| 完整 App，显式关闭 Xcode 并行，8 个有限 CPU 压力进程 | 3 | 0 |
| RetainedIdentity + AuthorityVault suites，scheme 默认调度，无额外压力 | 40 | 10 |

压力进程不超过本机 10 核，设有 CPU 时间上限，组结束后统一终止和回收；默认调度的复现与修后对比均没有额外压力进程。前五组只说明这些条件未复现，不能据其通过排除默认调度下的竞争。

主要对比命令为：

```sh
xcodebuild -quiet -disableAutomaticPackageResolution \
  -project ios/RemoteAgent.xcodeproj -scheme RemoteAgent \
  -destination 'platform=iOS Simulator,name=iPhone 18 Pro' \
  -only-testing:RemoteAgentTests/RetainedIdentityTests \
  -only-testing:RemoteAgentTests/AuthorityVaultTests \
  -test-iterations 40 -test-repetition-relaunch-enabled NO \
  -resultBundlePath <独立输出目录>/result.xcresult test-without-building
```

实际固定到该已有模拟器的 ID。xcresult 的 Repetition 节点核对目标确实运行 40 次，修前为 30 次通过、10 次 `.untrusted`；两个 suite 共 400 次测试运行，390 通过、10 失败，命令退出码 65。十次失败都在准备 bootstrap 的 `prepareCreation`，尚未执行 logout。

根因是 `AuthorityVaultTests` 用 `NotificationCenter.default` 广播合成的 UIApplication 失活/受保护数据不可用通知，同时 AppModel 测试 fixture 也订阅同一个全局中心。通知会关闭另一测试刚开启的 signing gate，`AuthorityBootstrap` 正确拒绝准备操作；这是跨测试干扰，不是登出后错误恢复，也不是签名验证应被放宽。为单独确认因果，在原生命周期测试中加入一个无关模型及已允许的 ticket，再广播通知并检查该 ticket：修前该确定性检查同样抛出 `.untrusted`。

修复仅增加通知中心的可注入依赖：AppModel 的生产默认仍为 `NotificationCenter.default`，fixture 各持有独立中心，生命周期测试向自己的中心发通知。保留自身失活/锁屏必须撤销签名的全部断言，并保留无关模型 ticket 仍有效的回归检查。没有改变 gate、bootstrap、logout 或恢复的安全逻辑，修复没有添加串行化约束、固定 sleep 或提高超时。

| 默认调度、同一组两个 suite、无额外压力 | 目标运行次数 | 目标失败次数 |
|---|---:|---:|
| 修前 | 40 | 10 |
| 修后，相同命令与条件 | 40 | 0 |

修后 xcresult 核对目标 40 次全部通过，两个 suite 共 400 次测试运行全部通过、0 跳过，退出码 0。修复构建后另有两个 suite 的 10 项检查通过，包含交叉影响回归；完整 App 的 `xcodebuild -quiet -disableAutomaticPackageResolution -project ios/RemoteAgent.xcodeproj -scheme RemoteAgent -destination 'platform=iOS Simulator,name=iPhone 18 Pro' -resultBundlePath <独立输出目录>/full.xcresult build test` 也通过：339 项（动态参数展开后 377 次执行）、0 失败/跳过，退出码 0。

修前默认调度组结束测试后，Xcode 曾停在 `simctl diagnose` 的诊断收集；调度日志显示测试进程已结束，进程采样定位为 `collectSimulatorDiagnostics → simCtlDiagnose` 等待。本轮只停止该诊断子进程，让 Xcode 写完结果，保留真实的退出码 65 和全部 40 次测试结果，没有把收集停顿算成目标测试超时，也没有靠重跑消除那 10 次失败。专项日志、xcresult、源文件指纹和重复运行脚本仅保存在本地输出。

### 客户端最终评审修复与三端对照（2026-10-24）

本轮沿用整合分支、产品 0.45.7 / 协议 11。先更新 [v51「各端统一的写法」](design/client-v51.html#schedules)，再逐端修改；该节是文案与行为的维护位置。以下记录本轮检查所覆盖的实际行为，不增加真实 CLI、推送或真机验收结论。

| 缺陷 | 修复与回归证据 |
|---|---|
| 网页静默轮遇 SUBMITTED 就不折叠 | `foldScheduled` 只阻止失败、无法确认、取消、需要终端、CLI 持有和 paused 输入。事件测试使用 daemon 的 userMessage → SUBMITTED → reply → turnEnded 顺序，同时核对异常输入仍可见。该顺序依据 `inject.go`，不是新增的真实 CLI 录制。 |
| 网页自动任务名留空白、移除电脑后留缓存 | 第一句规范化后 trim；回归含句号前空白。移除电脑时清理加密快照及请求状态，晚到的 list 不得重建缓存；回归检查存储删除和晚响应。 |
| Android 删除后残留、运行跳到上一轮 | repository 对删除状态做可见性过滤，取消成功返回列表；Compose 回归走实际取消和导航。运行游标为 `(start_seq > 0 ? start_seq : after_seq) + 1`，两个分支都有断言。 |
| Android interval 编辑丢相位 | 同间隔保留原 `at` / `delay_seconds`；新间隔延迟一个完整周期。只改次数时保留原截止日期；回归检查生成的完整 trigger。 |
| Android / iOS 用保存时的最新修订号及 spec | 打开编辑器时固定完整任务快照，以该 revision / spec 提交。Android Compose 在编辑期间注入另一端修订，再检查实际请求；iOS 检查编辑草稿生成的命令。冲突提示和重读路径保留。 |
| Android 缓存未保留原签名密文 | Remote 保存原始已验证 envelope 和 request ID，离线重新核对成员、来源签名和响应位置；repository 仅持有内存明文。加密本地测试 server / daemon 回归检查原密文和签名不变、错误 request / 签名被拒绝、冷读和移除后的清理；Room 回归检查只删除对应电脑的任务快照。 |
| Android 行测试只输入预写文案 | `TaskRow` 接收真实任务及 RemoteState；测试从共享 JSON 推导频率、位置、结果和状态，再检查组件。 |
| iOS 时区更改带走当地时刻 | 时间草稿保存当地日期、时、分组件，改变时区只改变解释方式；覆盖每天、一次性时间、截止时间及 DST 缺失时刻，未实际等待 DST 切换。 |
| iOS 新建把 plan 改 ask | 保留 plan，禁用创建并解释原因；草稿回归要求不能生成新对话执行 spec。 |
| iOS 手动刷新和修订读取竞争 | 所有读取共用按电脑的读槽，刷新等待已有读取及修订追读；令牌防止旧读取清掉新任务。受控 continuation 回归同时请求两次手动刷新并推进修订，检查最大并发为 1、最终数据来自最新读取。 |
| iOS 历史加载后不重试运行定位 | 同时观察行 ID、loaded 和待定位运行；真实 UIHostingController 回归保持行 ID 不变，只推进 loaded，确认观察器再次执行。 |
| iOS 手动暂停警告、成功运行被写成失败 | 用户暂停不挂原因横幅；系统停下及暂停期间到期才显示原因。注意组第三行只推导原因，运行本身仍保留成功结果。回归输入任务状态推导实际显示策略和结果。 |
| iOS interval 相位与运行游标 | 同间隔保留完整 trigger，改一个结束值保留另一结束值；运行定位使用带 after_seq 回退的 firstSeq。RACore 回归覆盖。 |

三端共同读取 `testdata/schedules/clients.json` 核对分组、可见性和原因。界面回归另覆盖时间与时区、双结束条件、创建者、空摘要、执行中及手动请求的禁用、离线状态、新任务无来源会话和 plan 默认值；没有把预写的行标签传给组件来代替状态推导。

新增的 iOS 串行读取回归首次全量执行挂起了一次：固定次数的释放循环可能在后续请求真正进入前结束，留下 continuation。测试进程采样和测试日志核对只有该新增测试未结束，随后停止该次运行；不是原有 bootstrap 或 polling 问题，也不把停止后的退出码当作通过。改为等待“首个读取已挂起”“两个手动刷新均已进入”“修订追读已挂起”三个明确条件，只悬挂前两个读取，逐个恢复，后续读取直接完成。没有固定 sleep 或增加超时。修后 `ScheduleStoreTests` 以 scheme 默认调度重复 40 次，12 项 × 40 = 480 次执行、0 失败/跳过；修后完整 App 也通过。该证据验证本轮新增测试的控制方式，前两项既有偶发问题的专项证据仍见上节。

截图使用同一份编造的任务数据：停下、两个进行中、暂停期间到期、用户删除和结束任务；详情为「检查 PR 回复」，新建采用同名、同消息、project / Claude Code / 自动 / haiku / 每小时，对话为「修复登录超时」。网页通过本机 Chrome 的开发演示，Android 通过现有 API 36 模拟器的 instrumentation gallery，iOS 只使用已有 iPhone 18 Pro / iOS 27 模拟器的 DEBUG fixture 与 route 启动参数。它们渲染实际页面，但没有发送任务给真实 CLI；调试装载和直接界面填写分别记录在本地截图输出。四个亮色画面覆盖原输出，另生成四张三端并排图；截图和比较图仅存本地输出，不把本机路径或图像提交到公开仓库。

首次检查的其余失败均已定位：Android 两条既有断言仍期待旧离线/次日文案，随统一规则更新；新增 plan 默认值用例起初未声明 `defaultModeSource`，产品按“未知默认值”处理是正确的，补齐 fixture 的 CLI_CONFIG 来源后通过。删除过滤变异起初使测试在 `.single()` 抛异常，未被计为断言验证通过，现先显式断言数量。截图驱动还修正了时间浮层误选背景输入框的问题，并检查任务名和键盘收起条件；这些不是产品偶发失败。

最后复核还覆盖了“已有会话占位对象，但 info 尚未加载”：三端在元数据到达前统一显示「在对话里」，不会把占位标题当成对话名。回归输入真实的未加载 SessionView；iOS 另推进到已加载状态，核对恢复为真实标题。固定截图的目标已加载，所以该修正不改变截图内容。

变异工具的 iOS App 检查原先从 quiet 标准输出寻找断言正文，但 Xcode 只打印失败用例名。核对 xcresult 后，改为解析结构化 testFailures，要求指定用例确实出现 expectation failure；运行定位用例的等待工具以 `Issue.record` 报告条件失败，仅接受它的明确条件「loaded history retries jump」；没有用“测试退出非零”或“任意其他用例失败”代替。检查用临时 xcresult，原测试基线必须实际执行且通过；有意制造断言失败的变异关闭 verbose 系统诊断收集，保留测试结果和断言。正常完整 App 检查仍使用原有默认设置。

变异结束后，再次从还原后的 checkout 完成全量检查与正常构建，以下最终命令均已核对退出码：

| 命令（未注明目录时从仓库根执行） | 最终结果 |
|---|---|
| `go test ./...` | 通过，退出码 0 |
| `pnpm --dir web typecheck` | 通过，退出码 0 |
| `pnpm --dir web test` | 87 个文件、1169 项通过，退出码 0 |
| `pnpm --dir web build` | 通过，退出码 0；仍有既有的 authority-setup 静态/动态导入分块提示 |
| `cd android && ./gradlew :core:test :app:testDebugUnitTest :app:assembleDebug` | 通过，退出码 0；core 398 项、app 332 项，失败/错误/跳过均为 0，debug APK 构建成功 |
| `cd ios/RemoteAgentCore && swift test` | 退出码 0；Swift Testing 报告 543 项、84 个 suite 通过，另有 RAMarkdown 17 项和 XCTest 1 项通过；需额外开启的真实 daemon 用例仍按默认条件跳过 |
| `xcodebuild -quiet -project ios/RemoteAgent.xcodeproj -scheme RemoteAgent -destination 'platform=iOS Simulator,name=iPhone 18 Pro' -resultBundlePath <独立输出目录>/app.xcresult build test` | 退出码 0；345 项（动态参数展开后 383 次执行）通过，0 失败/跳过。只使用已有 iPhone 18 Pro / iOS 27.0 |
| 同一 App 命令，加 `-only-testing:RemoteAgentTests/ScheduleStoreTests -test-iterations 40` | 退出码 0；12 项 × 40 = 480 次通过，0 失败/跳过 |
| `python3 scripts/check-schedule-regressions.py --clients` | 完整 175 项通过，退出码 0：后端 116、客户端 59（本轮新增 35）；原测试基线通过后逐项恢复缺陷并核对断言失败，结束后源码已还原 |
| `python3 scripts/public_scan.py web/dist deploy/self-host android/app/build/outputs/apk/debug/app-debug.apk` | 通过，退出码 0；检查本次正常构建的网页包、自部署模板和 debug APK，不代表签名发布产物已检查 |
| 全部跟踪及未忽略源码逐文件交给 `python3 scripts/public_scan.py` | 1912 个文件，原始退出码 1；仍为前次整合的 16 条既有提示（12 条允许的官方域名、3 条扫描规则字面量、1 条既有符号链接），没有私有名单命中。未修改扫描规则或放行名单 |
| Android `ScheduleComparisonGallery` instrumentation | 4 项通过；检查实际页面、任务名与键盘收起条件，再保存截图 |
| `git diff --check` | 通过 |

结果、xcresult 和截图仅保存在本地输出。三端各四张亮色原图已覆盖，并有列表、详情、新建、对话定时条四张并排比较图及输入/截图指纹。Chrome 主题与视口已恢复，临时 Android 测试包和模拟器进程已清理；USB 真机没有操作。没有新增真实推送、真机、iPad、实际睡眠/DST 切换或客户端 Codex 全链路验收。未推送、打 tag、发版、部署或修改 `version.properties`。

### 三端四图：文案、颜色与定时条收尾（2026-10-24）

产品 0.45.7 / 协议 11，整合分支本地候选。逐张复核列表、详情、新建、对话定时条的三端并排图，先在 [v51「各端统一的写法」](design/client-v51.html#schedule-shared-copy) 明确规则，再修三端。新建说明逐字统一；详情副标题只留状态；Android 注意组原因改为 muted；iOS 定时条补 Lucide clock，作为装饰对读屏隐藏。

额外发现并修正的差异：iOS 新建标签去掉「（可选）」（仍允许留空）；详情任务类型及创建者说明对齐完整句子，运行记录标题补实际条数；网页详情消息正文改为 15 号 fg，并去掉副标题额外的彩色状态点；CLI 分段选择选中项为 fg 字和浅底，未选项为 muted（网页去蓝色，iOS 局部设置原生 UISegmentedControl 的文字色，保留原生选择行为）。Android 当前对话任务的创建说明也对齐 v49。

| 本轮命令 | 结果 |
|---|---|
| `pnpm --dir web typecheck`、`pnpm --dir web test`、`pnpm --dir web build` | 最终全部退出码 0；87 个文件、1170 项通过，保留既有 authority-setup 分块提示 |
| `cd android && ./gradlew :core:test :app:testDebugUnitTest :app:assembleDebug :app:assembleDebugAndroidTest` | 通过；增加颜色回归后再跑前三项，core 398、app 333，失败/错误/跳过均为 0 |
| Android `ScheduleComparisonGallery` instrumentation | 4 项通过，从共享 JSON 渲染真实四屏并重截 |
| `xcodebuild -quiet -project ios/RemoteAgent.xcodeproj -scheme RemoteAgent -destination 'platform=iOS Simulator,name=iPhone 18 Pro' -derivedDataPath <临时构建目录> -resultBundlePath <独立输出目录>/app.xcresult build test` | 最终退出码 0；345 项、参数展开后 383 次执行，0 失败/跳过；结构化构建结果 0 错误、0 警告，仅用已有 iPhone 18 Pro / iOS 27 |
| `python3 scripts/public_scan.py web/dist deploy/self-host android/app/build/outputs/apk/debug/app-debug.apk` | 退出码 0；只代表本地正常构建产物及自部署模板，不代表签名发布产物 |
| 将本轮修改的 14 个源码/文档文件交给 `python3 scripts/public_scan.py` | 原始退出码 1；仅 acceptance-notes 与 v49 中既有的允许官方域名，与本轮开始前核对一致，没有私有名单命中；未修改扫描规则 |
| `git diff --check` | 通过 |

网页新增回归检查实际新建页面的完整说明；Android 从共享任务推导真实注意行，再检查原因 Text 的实际布局文字色；iOS 在真实宿主窗口内检查原生分段控件的普通/选中颜色及切换后的配置。网页说明、Android 原因颜色各临时恢复旧实现后，对应断言确实失败，源码随后恢复，最终正常测试和构建通过。Android 新测试初稿读到了合并语义节点中的标题色，改为未合并节点定位原因 Text；这是确定的测试定位问题，没有靠重跑掩盖。iOS 初次计数引用用了不存在的属性，改用既有 history；新增原生控件协调器明确限定 MainActor，最终构建无新增警告。未发生需要以重跑解释的测试偶发失败。

三端各四张亮色原图和四张并排图已覆盖，网页另更新 1280 宽版本；输入仍为 `testdata/schedules/clients.json`，并刷新本地截图 SHA-256 清单。网页使用 Chrome 的真实点击与填写；普通视口截图被设备预览缩放后，改用完整页面截图并核对 390×844，未用缩放后的错误截图交付。Android 使用现有 API 36 模拟器 instrumentation；iOS 使用 DEBUG fixture / route 启动参数。截图是合成固定数据，不能代替真实 CLI 验收。未新增真机、iPad、推送送达、睡眠或 DST 实测。本轮是客户端显示收尾，没有重跑 Go、RACore 或完整 175 项变异检查，前节证据保持原范围。

只在 `wt/scheduled-tasks-integration` 提交，没有 push、tag、发版、部署或修改 `version.properties`。Chrome 主题、视口与临时测试设备在截图后恢复。

### 客户端最终复审收尾（2026-10-24）

产品 0.45.7 / 协议 11，`wt/scheduled-tasks-integration` 本地候选。先补 [v51「各端统一的写法」](design/client-v51.html#schedule-shared-copy)，再按最终复审逐项处理；以下是代码及测试证据，不新增真实 CLI、推送或真机验收结论。

| 复审项 | 修复与回归证据 |
|---|---|
| 1 非日历时间的时区 | Android 不再从时间戳偏移推导时区，打开编辑器使用本机时区。输入 `Z` 和数字偏移的时间戳，再转为每天，断言发送 IANA 名称。网页、iOS 没有同类偏移推导，另补相同输入的回归。 |
| 2 锁定电脑 | Android 编辑入口传锁定标志；网页模型/权限和目录选择均固定在任务电脑，选目录不再修改项目偏好；iOS 沿用固定电脑入口。真实组件回归检查机器不可选。截图又确认复用的项目设置仍露出全局项目操作，网页和 Android 已在任务编辑中隐藏它们、改写标题和说明，并覆盖实际组件。 |
| 3 电脑自己的默认值 | 网页固定启动配置来源；Android/iOS 在任务编辑中只向选择器提供该电脑的会话和项目设置。两个合成电脑使用不同 CLI、模型和权限，检查可见选项、实际配置推导及网页提交的目标；不是预写文案测试。 |
| 4 修订冲突 | Android 改名冲突关闭旧弹窗；iOS 用冲突信号关闭各编辑入口；网页统一提示并释放被 daemon 明确拒绝的旧请求。中继的未知 abort 仍保留原请求，不能按冲突丢弃。回归覆盖重读、关闭、请求槽和未知结果的区别；iOS 观察器先等出现再触发冲突。 |
| 5 固定默认权限 | Android 创建时解析所选 CLI 默认权限为明确模式；三端在默认值未知时均要求明确选择，网页同步禁用创建按钮，iOS 不再生成空权限的新任务。回归使用有/无来源标记的真实 AgentInfo，检查生成定义及创建状态。 |
| 6 双重结束条件 | 网页初始选项改为次数，三端均保留原截止值；iOS 不再因单独换时区而重算截止绝对时刻。回归同时给次数和截止日期，改时区/次数后检查完整 trigger；原相位回归继续覆盖 interval。 |
| 7 目标读取不占列表槽 | 网页异步加载未就绪目标，不等待它才完成列表读取/保存；iOS 发现同源等待，也移到独立、去重并随账户/电脑取消的任务。受控挂起目标读取时，验证列表刷新及修订追读仍能完成。 |
| 8 详情目标占位 | 三端在缺目标或 info 未就绪时使用统一占位；加载后恢复标题。网页检查实际详情 DOM，原生检查从真实 SessionView 推导的结果。 |
| 9 界面稿示例 | 纠正停下状态、注意组纯原因、完整日期词、离线灰点/说明、运行中禁用立即运行、模型行及记录计数；补 DOM 回归检查示意画面，恢复容量失败的旧写法时断言失败。 |

变异检查的原测试基线必须通过，恢复一个缺陷后必须出现断言失败。原有 175 项加本轮 23 项，共 198 个不同变异均取得通过证据。过程分段保留：`python3 scripts/check-schedule-regressions.py --clients` 在 143 项通过后因旧定位字符串多一个括号退出 1；修正定位并增加全量预检后，以 `--clients --only final-`、`--clients --only r2-`、`--clients --only r2-task-settings-only` 完成覆盖，后两组最终退出 0。iOS 条件等待以 `Issue.record` 报告失败，检查器只接受指定用例及明确条件，不能把编译错误或无关失败当成发现缺陷。界面稿追加核对后另跑 `--clients --only final-r2-design`，退出 0。Go 串行夹具修正后另跑 `--only r3-A-serialize`，退出 0。没有把分组结果描述为单次全量命令退出 0。

首次 `go test ./...` 还发现两项测试时钟问题，均未修改 Go 产品代码：

- `TestManualAndAutomaticSerialize` 是整合后端新增测试。它把 NextRunAt 设为任意一分钟后，触发器却仍写每天 23:59；UTC 23:56 附近，调度器会正确选择更新的 23:59 occurrence，使“应跳过”的断言不成立。原样两项各重复 40 次均未失败；把旧夹具放入 UTC 23:56 的虚拟时钟，串行测试 40/40 失败。修正为下一完整分钟及一致的日历触发器，同一虚拟场景重复 40 次，0 失败。`time.Sleep` 只推进虚拟时钟，不等待墙钟。
- `TestPermissionAllowDenyTimeout` 与本地 origin/main 基线相同。全量中允许分支越过真实 200ms 截止而失败，原样重复 40 次未再复现。代码确认测试把允许/拒绝断言绑定到墙钟预算；调度或 I/O 延迟可以合法导致过期，未进一步区分两者。按项目已有 `synctest` 做法控制时钟，用 Wait 等请求发布并进入等待，再验证允许、拒绝和原来的 200ms 超时；没有增加超时或固定 sleep。修后按原执行顺序两项各重复 40 次，均 0 失败。测试改动有明确时钟依据，未把未再次复现说成已复现。

| 最终正常检查（未注明目录时从仓库根执行） | 结果 |
|---|---|
| `go test ./...` | 修正上述测试后退出 0；Go 产品源码未改 |
| `pnpm --dir web typecheck`、`pnpm --dir web test`、`pnpm --dir web build` | 退出 0；88 文件、1180 项通过，保留既有 authority-setup 分块提示。界面稿最后追加的断言另跑目标文件通过，并再次核对类型与构建 |
| `cd android && ./gradlew :core:test :app:testDebugUnitTest :app:assembleDebug :app:assembleDebugAndroidTest` | 退出 0；core 400、app 338，0 失败/错误/跳过，正常 debug APK 与测试 APK 构建通过 |
| `cd ios/RemoteAgentCore && swift test` | 退出 0；545 项/84 suite，另有 RAMarkdown 17 项及 XCTest 1 项通过；真实 daemon 用例仍按默认门槛跳过 |
| `xcodebuild -quiet -project ios/RemoteAgent.xcodeproj -scheme RemoteAgent -destination 'platform=iOS Simulator,name=iPhone 18 Pro' -derivedDataPath <临时构建目录> -resultBundlePath <独立输出目录>/app.xcresult build test` | 退出 0；350 项、参数展开 388 次，0 失败/跳过，仅用已有 iPhone 18 Pro / iOS 27。补 DEBUG 截图入口后再次全量通过 |
| 同一 App 命令选择 `ScheduleStoreTests` / `ScheduleAlignmentTests`，加 `-test-iterations 40` | 21 项 × 40 = 840 次，0 失败/跳过；包含读取槽、冲突观察及配置边界 |
| Android `ScheduleComparisonGallery` instrumentation | 修正截图驱动后 5 项 × 3 轮 = 15 次通过；列表固定读取共享 JSON，弹层可见后等待窗口提交绘制，无固定 sleep。初稿误读网络与旧帧的截图已覆盖，不算产品偶发失败 |
| `python3 scripts/public_scan.py web/dist deploy/self-host android/app/build/outputs/apk/debug/app-debug.apk` | 退出 0；只代表本地正常网页包、自部署模板与 debug APK，不代表签名发布产物 |
| 本轮 30 个修改文件逐个交给 `python3 scripts/public_scan.py` | 原始退出码 1；只命中 acceptance-notes 与 v49 中既有的允许官方域名，已和本轮起点核对，没有私有名单命中；未修改扫描规则 |
| `git diff --check` | 通过 |

四张原并排图、各端主画面及网页宽屏图已覆盖，另补运行位置编辑器对照；固定输入仍为 `testdata/schedules/clients.json`。网页是 Chrome 直接点击/填写；Android 是 instrumentation；iOS 主画面由 DEBUG fixture/route 展示，附加编辑器由现有 DEBUG 通知机制调用同一个入口，再用 simctl 截图。当前控制工具未接入 Simulator 原生窗口，未把该画面记为直接点击验收。图片与 SHA-256 清单仅在本地输出。

未新增真实推送送达、真机/iPad、两台真实电脑、实际睡眠/DST 或客户端 Codex 完整路线验收。只在整合分支本地提交，未 push、tag、发版、部署或修改 `version.properties`。

清理：Chrome 测试页关闭且主题/视口恢复；Android 临时包卸载、模拟器退出；已有 iPhone 模拟器恢复为关闭状态；本地网页开发服务器停止。保留本地日志、xcresult、固定数据图片和指纹供复核。

### 定时任务末轮 5 项 P2 修复（2026-10-24，整合分支）

- Android 系统取消（`cancelled/message_cancelled`）：循环任务通过提交完整定义重新安排，一次任务先打开时间编辑器；明确删除保持可用。Compose 测试验证操作、修订号和删除返回。分别移除恢复与删除修复后，测试均以断言失败检出（`check-schedule-regressions.py --clients --only r3-cancelled`，2/2）。网页与 iOS 已有同样的重新安排分支，未改动协议或 daemon。
- Android 运行位置编辑器：任务设置完成时返回当前 CLI 及它的配置，详情回调按返回的 CLI 保存。Compose 测试从 Claude 切到 Codex、选择完全放行后检查发出的定义；恢复旧回调或旧返回值各自都会断言失败（`--only r3-selected-cli`、`--only r3-return-cli`）。网页和 iOS 保存的是当前配置，没有按打开时 CLI 回取的分支。
- 三端静默折叠：Android 的区间在该轮 `turnEnded` 截止，轮间聊天行自然成为边界；iOS 补齐设置、提问回答、终端上下文等边界；网页已有设置边界，补齐提问回答、终端上下文和额外轮结束边界。测试覆盖模型、权限、压缩及其他轮间事件，原文保留；网页与 iOS 的去修复检查已检出断言失败，Android 同类检查及最终全量结果见本节末尾。
- 截止显示按 v49 统一：保留绝对时刻，换时区立即重算输入值；显式修改截止后再换时区也如此。网页区分显示换算与截止编辑；Android 不再清空已有截止并按新时区换算日期；iOS 直接保存截止 `Date`，避免夏令时回拨的重叠时刻被拆分再还原成另一次。新增三端时间模型/编辑器测试（含 iOS 回拨重叠案例）；Android 两处、网页一处去修复断言检查均检出。实际设备睡眠和真实夏令时切换未验证。
- 网页间隔：未编辑时保留原整数秒及相位，编辑后的分钟换算仅消除浮点误差，再校验正整数秒。31、62、123 秒逐项验证原值保存、加一分钟和真正非整数秒拒绝；去掉修复时断言失败。Android/iOS 的整数单位换算没有该浮点路径，补了相同秒数的整数、初始延迟与相位保留测试。

截止显示另外做了原文件验证：临时换回该修复提交之前的 iOS `ScheduleTiming.swift`，同一组 `swift test --filter SchedulesTests` 出现 2 个失败用例、7 条断言失败（含截止显示、回拨重叠时刻、手动修改后换时区）；恢复修复后 18 项全部通过。没有用编译失败代替负向证明。

最终检查的代码为上述 5 个独立修复提交，产品版本与协议版本均未修改。命令在仓库根目录执行，表内注明子目录的除外；Xcode 使用独立临时构建目录和 xcresult，只使用已有 iPhone 18 Pro。

| 检查命令 | 结果 |
|---|---|
| `go test ./...` | 退出 0，全部包通过 |
| `pnpm --dir web typecheck` | 退出 0 |
| `pnpm --dir web test` | 88 个文件、1189 项通过 |
| `pnpm --dir web build` | 退出 0，网页包构建成功 |
| Android 目录：`./gradlew :core:test :app:testDebugUnitTest :app:assembleDebug` | 退出 0；core 402 项、app 343 项，0 失败/错误/跳过；Debug APK 构建成功 |
| `ios/RemoteAgentCore` 目录：`swift test` | 退出 0；Swift Testing 548 项，0 失败；真实 daemon 用例 1 项按环境开关跳过 |
| `xcodebuild -quiet -project ios/RemoteAgent.xcodeproj -scheme RemoteAgent -destination 'platform=iOS Simulator,name=iPhone 18 Pro' build test`（加独立临时 `-derivedDataPath`、`-resultBundlePath`） | 最终 App 测试 350 项通过，0 失败、0 跳过；xcresult 为 Passed |
| `python3 scripts/check-schedule-regressions.py --clients` | 同一次完整执行退出 0，210/210 检出预期断言失败：后端 116 项、客户端 94 项；本轮新增 12 项，覆盖全部 5 个问题及相关三端路径 |
| `python3 scripts/public_scan.py web/dist deploy/self-host android/app/build/outputs/apk/debug/app-debug.apk` | 退出 0；仅代表本地正常网页产物、自部署模板和 Debug APK |
| 本轮变更的受跟踪文件交给 `python3 scripts/public_scan.py` | 原始退出码 1；只报两份文档既有官方域名，数量与修复前一致，没有私有名单命中；未改扫描规则 |
| `git diff --check` | 通过 |

本轮是自动测试和模拟器验收，未补真实 CLI 的客户端端到端链路、真机推送送达、真机/iPad、两台真实电脑、实际睡眠或实际夏令时切换。回拨时间单元测试不能代替真实系统切换验收。只在整合分支本地提交，未 push、tag、发版、部署或改 `version.properties`。

### 定时任务撤销、重试与工具折叠复审（2026-10-24，整合分支）

- 撤销电脑：iOS 的公开状态单独提供当前签名机器成员集合，任务 Store 据此筛选，撤销时清除任务、错误、读取标记与待完成读取；归档机器元数据用于历史会话。密文缓存新增按名称删除，在线成员更新和冷启动都清理已移除电脑的任务快照；缓存读取前后及写入完成后核对当前成员，阻止旧成员的历史签名快照或迟到写入恢复任务。SQLite 按名称删除、在线撤销、冷启动、读取和写入期间撤销均有测试。
- 撤销相关自查补修：网页缓存解密返回后重新核对成员，网页和 Android 在挂起的缓存写入完成后重新清理被撤销电脑；Android 的迟到缓存/列表异常也不能重建已移除的快照状态。各端的正常离线缓存保留测试仍通过。网页、Android 既有的在线撤销与冷启动清理路径已核对，未新增降级或历史成员推断分支。
- 输入重试：在 lifecycle 锁内、决定是否隐式恢复之前先查持久化输入 ID；已退出会话的重复输入直接确认已受理，活着的会话仍唤醒已受理队列。就绪入口也在退出检查前确认已受理 ID，覆盖系统输入重试。测试从磁盘重载去重记录，覆盖本地/客户端/系统来源、已接手/未接手，以及每组 8 个并发重试，确认元数据、队列、事件序号、图片消费和 tmux 调用均无重复副作用。移除两处修复分别触发断言失败。
- 幂等自查：中继 `handleCall` 的安全受理日志在 execute 前处理重复信封；定时任务创建键在容量检查及创建前返回原结果；输入的额度预留、图片消费和所有权接手仍在输入去重之后。显式恢复属于独立操作；保留了现有的队列受理后唤醒及事件写失败测试。未发现需要在这些路径增加重复执行的分支。
- 静默折叠：网页与 Android 复用聊天工具成功判断，failed 或非零退出码的工具结果阻止整轮折叠；iOS 原有失败判断补了同样的负向覆盖。三端另外保护轮内设置变更、回答、图像/分叉、实际终端设置变化、未完成及后台工具等应保持可见的内容；正常已完成、成功工具的静默轮仍能折叠，规则写进 v49。
- 折叠自查发现跨轮结果问题：工具在上一轮开始、下一轮才失败，结果会绘回旧起点；只检查结果所在轮仍可能隐藏旧行。三端同一结构的测试在修复前各失败 1 项；修复要求工具结果序号不晚于该轮结束序号，对应测试修复后通过。修复前后均为确定性的断言验证，没有通过固定 sleep、延长超时或偶然重跑掩盖。
- 最后核对补修 Android 底层缓存读取：暂停 `blob` 后撤销电脑，旧实现会在历史解密后返回旧任务字节，新增用例修复前失败 1 项。读取完成后再次核对签名记录，读取前发现已撤销也清掉快照；写入检查同步使用当前签名记录，避免目录内存映射更新期间的窗口。基线通过，移除读取后检查时断言失败。

最终正常检查与负向回归：全部在整合 worktree 执行，产品版本和协议版本未改。Xcode 使用独立临时构建及结果目录，只选已有 iPhone 18 Pro；未创建其他测试机型。

| 命令 | 结果 |
|---|---|
| `go test ./...` | 退出 0，全部包通过 |
| `go test -race ./internal/daemon ./internal/relay ./internal/schedule` | 退出 0，三个相关包通过，无 race 报告 |
| `pnpm --dir web typecheck` | 退出 0 |
| `pnpm --dir web test` | 88 个文件、1201 项通过 |
| `pnpm --dir web build` | 退出 0 |
| Android 目录：`./gradlew :core:test :app:testDebugUnitTest :app:assembleDebug` | 最后一次补修后退出 0；core 405 项、app 346 项，0 失败/错误/跳过；Debug APK 构建成功 |
| `ios/RemoteAgentCore` 目录：`swift test` | 最终 Swift Testing 554 项、0 失败，真实 daemon/Codex 用例 1 项按环境开关跳过；另有 Markdown 17 项及 XCTest 1 项通过 |
| `xcodebuild -quiet -project ios/RemoteAgent.xcodeproj -scheme RemoteAgent -destination 'platform=iOS Simulator,name=iPhone 18 Pro' build test`（带独立临时 `-derivedDataPath` 和 `-resultBundlePath`） | 跨轮工具修复后的最终 xcresult 为 Passed，351 项通过，0 失败、0 跳过 |
| `python3 scripts/check-schedule-regressions.py --clients` | 最终同一次完整执行退出 0，240/240 个变体被预期断言检出：后端 118、客户端 122；本轮新增 30。正常基线均通过，编译错误不算检出；补 Android 底层读取检查前中断的执行不计入这个结果 |
| `python3 scripts/public_scan.py web/dist deploy/self-host android/app/build/outputs/apk/debug/app-debug.apk` | 最后一次 Android 构建后退出 0；仅代表正常本地网页产物、自部署模板和 Debug APK |
| 本轮变更的受跟踪文件交给 `python3 scripts/public_scan.py` | 原始退出码 1，只报两份文档原有的官方域名，未新增私有名单命中；未改扫描规则 |
| `git diff --check` | 通过 |

本轮自查覆盖三端任务列表、角标、在线/冷启动缓存、挂起读写及错误回调；后端本地与中继输入、持久化受理、额度/图片/所有权顺序，以及中继安全受理日志和任务创建键；三端折叠的轮内/轮间独立行、失败/未完成/后台工具、跨轮结果绘回旧起点。自查结论限于这些代码路径与自动测试；未新增真机推送、真机/iPad、实际睡眠/DST 或真实 CLI 客户端端到端路线验收。只在整合分支本地提交，未 push、tag、发版、部署或改 `version.properties`。

### 新建定时任务切换电脑的配置复审（2026-10-24，整合分支）

Android 在切换电脑后清空编辑集，设置弹层显示新电脑默认值，但原完成操作没有配置项，创建页据此复制了旧电脑配置。新增 Compose 操作测试在修复前 2 项均断言失败：一项实际选择项目、换电脑、不改设置、完成并创建，检查发出的电脑、CLI、模型、effort 和权限；另一项在新电脑有两个 CLI 时切到另一 CLI 后直接完成，检查返回的是弹层显示的那一份配置。修复让定时任务设置始终返回当前电脑及选中 CLI 的完整配置，创建页不再用旧配置补空结果；两项修复后通过。

iOS 同一路径也有问题：配置页换电脑时用项目继承取值，可能从旧电脑的保存设置中取回模型、effort 和完全放行权限。选择动作改用切换电脑的配置重建规则，保留项目路径，按新电脑重选可用 CLI、清空模型/effort/显式权限，再在创建时解析新电脑默认权限。参数化测试覆盖新电脑只装另一个 CLI，以及两个 CLI 都装的情形，核对配置页显示来源和实际任务 target；修复前该用例失败，修复后通过。

网页的切换动作已清空模型、effort、权限并重选可用 CLI；操作回归从旧电脑的指定模型/high/完全放行切到另一电脑，不改设置直接完成，确认页面显示与实际创建请求均为新电脑的 CLI、默认模型/effort、询问权限。此路径未发现需要修改的产品代码。

三端各新增一项去修复检查：恢复 Android 空编辑集返回、恢复 iOS 项目继承、让网页重新携带旧配置，均由对应回归断言检出；编译错误不作为检出证据。

最终检查（同一修复代码，未改产品/协议版本）：

| 命令 | 结果 |
|---|---|
| Android 目录：`./gradlew :core:test :app:testDebugUnitTest :app:assembleDebug` | 退出 0；core 405 项、app 348 项，0 失败/错误/跳过；Debug APK 构建成功 |
| `pnpm --dir web typecheck` | 退出 0 |
| `pnpm --dir web test` | 88 个文件、1202 项通过 |
| `pnpm --dir web build` | 退出 0 |
| `xcodebuild -quiet -project ios/RemoteAgent.xcodeproj -scheme RemoteAgent -destination 'platform=iOS Simulator,name=iPhone 18 Pro' build test`（使用独立临时 `-derivedDataPath`、`-resultBundlePath`） | 最终 xcresult 为 Passed，352 项通过，0 失败、0 跳过；仅使用已有 iPhone 18 Pro |
| `python3 scripts/check-schedule-regressions.py --clients` | 同一次完整执行退出 0，243/243 个去修复变体被预期断言检出；后端 118、客户端 125；包含本轮新增的三端检查，正常基线均通过 |
| `python3 scripts/public_scan.py web/dist deploy/self-host android/app/build/outputs/apk/debug/app-debug.apk` | 退出 0；仅代表正常本地网页产物、自部署模板和 Debug APK |
| 本轮变更的受跟踪文件交给 `python3 scripts/public_scan.py` | 原始退出码 1，仅报 acceptance-notes 中原有的官方域名，无新增私有名单命中；未改扫描规则 |
| `git diff --check` | 通过 |

证据范围：Android 为 Compose 实际操作创建页并捕获任务请求；网页为 DOM 操作创建页并捕获请求；iOS 为配置页选择动作绑定草稿、核对显示来源和提交 spec，并跑完整 App 测试。未补真机或真实 CLI 端到端验收。只在整合分支本地提交，未 push、tag、发版、部署或改 `version.properties`；测试结束关闭已有 iPhone 模拟器。

### 定时任务夏令时输入与重复时刻复审（2026-10-24，整合分支）

Android 一次性时间原来直接用 `atZone()`，会把 America/Los_Angeles 的 `2027-03-14 02:30` 挪到 03:30。现在先检查当地时刻的有效偏移；没有偏移时拒绝保存，提示与网页、iOS 逐字一致：「这个时区在该时刻发生夏令时跳转，请换一个时间」。一次性时间只在选中该类型时转换，不对日历规则的钟点套用一次性输入校验。Android 截止选择器只有日期，按所选日 23:59:59 做同样的校验；测试覆盖 Pacific/Apia 整天被跳过的日期，以及其前一天仍能正常截止，避免通过次日午夜转换静默移日。已有精确截止时间仍保留绝对时刻。

三端核对还发现两处重复时刻差异并一起修复：网页从 UTC 反复逼近当地时刻，在柏林时区会先命中较晚一次，改为核对跳转两侧偏移并选最早的有效绝对时刻；iOS 的一次性时间已取较早一次，新编辑或直接选用默认截止时间原来可能保留较晚一次，现在也取较早一次。原样打开的既有截止时间、以及只切换时区时，不重新选择重复时刻。v49「各端统一的写法」已写明这条边界及统一提示。

回归覆盖洛杉矶 `2027-11-07 01:30` → `08:30Z`，柏林 `2027-10-31 02:30` → `00:30Z`，均为较早一次。后端原日历实现按 UTC 顺序扫描，已经取较早一次；本轮只补这两个日期的 Go 用例，未改后端调度。网页另用实际时间弹层 DOM 操作验证一次性与截止输入：春季空缺时不提交、显示统一提示，秋季重复时提交较早一次。iOS 核对草稿选择值、默认值、提交 trigger 和未编辑既有截止时间；Android 核对实际转换后的 trigger。

修复前新增测试均先失败：Android 空缺时刻测试 1 项断言失败；网页转换测试 32 项通过、柏林重复时刻 1 项失败（得到 `01:30Z` 而非 `00:30Z`）；iOS 新编辑截止、默认截止分别在修复前各有 1 项测试失败，均在洛杉矶和柏林的显示值及提交值断言上失败。没有用编译错误代替回归证据。

最终检查（产品及协议版本未改）：

| 命令 | 结果 |
|---|---|
| Android 目录：`./gradlew :core:test :app:testDebugUnitTest :app:assembleDebug` | 退出 0；core 407 项、app 348 项，0 失败/错误/跳过；Debug APK 构建成功 |
| `pnpm --dir web typecheck` | 退出 0 |
| `pnpm --dir web test` | 88 个文件、1205 项通过 |
| `pnpm --dir web build` | 退出 0 |
| RACore 目录：`swift test` | 555 项、85 个 suite，退出 0；1 项真实 daemon/CLI 测试按条件跳过，其余通过 |
| `xcodebuild -quiet -project ios/RemoteAgent.xcodeproj -scheme RemoteAgent -destination 'platform=iOS Simulator,name=iPhone 18 Pro' build test`（独立临时 `-derivedDataPath`、`-resultBundlePath`） | 最终 xcresult 为 Passed，352 项通过，0 失败/跳过；仅使用已有 iPhone 18 Pro |
| `go test ./internal/schedule -run TestCalendarNext -count=1` | 退出 0；包含新增的洛杉矶、柏林较早一次用例 |
| `python3 scripts/check-schedule-regressions.py --clients` | 同一次完整执行退出 0，249/249 个去修复变体被预期断言检出；后端 118、客户端 131，所有正常基线通过；本轮新增 6 项均检出（Android 空缺一次性/截止日期、Android 较早一次、网页较早一次、iOS 编辑截止/默认截止较早一次）；结束后逐文件哈希核对所有临时改写均已恢复 |
| `python3 scripts/public_scan.py web/dist deploy/self-host android/app/build/outputs/apk/debug/app-debug.apk` | 退出 0 |
| 本轮变更的受跟踪文件交给 `python3 scripts/public_scan.py` | 原始退出码 1，仅报 acceptance-notes 与 v49 原有的官方域名；两份文档的官方域名数量均未变，没有新增私有名单命中，未改扫描规则 |

`git diff --check` 通过，`version.properties` 与本轮开始时完全一致。检查结束确认已有 iPhone 模拟器处于关闭状态。

本轮是日期换算与客户端自动测试，未跨越真实系统夏令时切换，未补真机选择器或真实 CLI 端到端验收；计划中的实际睡眠/DST 验收仍未完成。仅在整合分支本地提交，未 push、tag、发版、部署或改 `version.properties`。

## 定时任务：通知未读角标与自动关闭原因（2026-10-28，基于 v0.46.0 的未发布工作区）

按 [v51 统一文案](design/client-v51.html#schedule-shared-copy) 实现三端入口角标。后台已有的加密任务列表包含运行 ID 和通知标记；未新增请求、server 行为或协议版本变更。自动关闭通过新增的 `SessionState.scheduled_closed` 明确区分，`detail` 仍为 `stopped`；daemon 持久化最近关闭原因，并覆盖保存元数据后、写入退出事件前中断的恢复路径。

| 检查 | 结果 |
|---|---|
| `go test ./...` | 24 个有测试的包通过，0 失败；另有 8 个包无测试 |
| 网页：`pnpm run typecheck`、`pnpm exec vitest run`、`pnpm run build` | 类型检查、生产构建通过；89 个文件、1210 项测试通过，0 失败 |
| Android：`./gradlew --no-daemon --no-configuration-cache :core:test :app:testDebugUnitTest -PdevBuild=true` | Core 412 项、App 348 项通过，0 失败/跳过 |
| RACore：`swift test` | 报告 556 项、86 个 suite，通过；其中独立真实 daemon/CLI 测试按条件跳过，另用下述隔离验收覆盖本次链路 |
| iOS App：`xcodebuild build test`，RemoteAgent scheme、已有 iPhone 18 Pro | xcresult 为 Passed，355 项通过，0 失败/跳过；未创建模拟器 |
| 临时移除新增行为后运行新增测试，再逐字节恢复源码 | Go 自动关闭标记 1 项、网页 3 项、Android Core 3 项和 App 文案 1 项、iOS Core 1 项、iOS 角标 2 项和文案 1 项均出现预期断言失败；不以编译错误计为检出。恢复后以上正常检查通过 |
| 隔离环境：`ios/scripts/dev-env.sh start --no-account`，`go run ./scripts/acceptance-schedule` | 本 checkout 构建 server/daemon，临时账户、独立数据库、状态目录与 tmux socket；真实 Claude Code 调用 `notify_user`，22 项断言通过。加密客户端取回自动退出标记，daemon 重启后仍为 true，恢复会话再手动结束后为 false |

客户端测试覆盖首次有效列表不提示历史通知、运行从未通知变为已通知、打开定时列表清除通知未读、列表停留期间的新结果、与异常任务取并集去重、用户取消/删除后不保留角标，以及跨启动持久化和中继/客户端身份/电脑隔离。网页组件测试另外从后台列表刷新走到实际列表挂载和离开；Android 测试覆盖 repository 的角标流和离线保留；iOS 测试覆盖 account-scoped store 与 UserDefaults。

真实 CLI 验收由合成客户端通过加密 Forward 发起，经过 server、daemon、原生 CLI 后回到客户端；不是产品界面点击。未覆盖系统推送投递、Android/iOS 真机界面或真实设备上的跨启动操作。临时服务与 tmux 环境已清理，仅使用已有的 iPhone 18 Pro，结束时已关闭。未提交、推送、打 tag、发布、部署或修改产品/协议版本。

## 文件附件：Phase A（2026-10-28）

范围为 capability 控制的文件协议、Go daemon、网页及四端生成代码；Android / iOS 没有修改手写发送界面。未发布、未部署，没有触碰日常 remote-agent daemon 或账户。

### 自动检查

- `buf generate`，以及分别在 `web` 执行 `pnpm gen`、在 `android` 和 `ios` 执行 `buf generate`：四端生成成功，协议版本不变。
- `go test ./...`：通过。新增文件测试覆盖安全名字、NFC 和 UTF-8 截断、大小/格式/哈希/归属、原样完成、混合受理与回滚、缺文件启动前拒绝、组合正文和带图片确认、两个 CLI 解析出的路径行、文件配额与清理保护、过期、重启队列和 CLI 暂存恢复。
- 网页 `pnpm typecheck`、`pnpm test`（90 个测试文件、1223 个测试）、`pnpm build`：通过。新增测试覆盖能力菜单、文件选图保留原样、拖放/粘贴分流、独立数量与字节限制、失败重试、到期和换机器重传、纯文件发送/新建、不确定新建请求保持文件 ID、恢复本机源 File、共享排队事件、精确隐藏路径行和文件条目状态。

### 真实 CLI 链路

用当前 checkout 构建，独立 `RA_DEV_DIR`，由 `ios/scripts/dev-env.sh start --no-account` 启动临时数据库和本机中继；`scripts/acceptance-files` 新建临时管理账户、签发测试机器成员，独立状态目录与 tmux socket 起测试 daemon。复用本机已认证的真实 CLI 安装与 CLI 配置，测试的是新建会话；不使用日常 remote-agent 身份或已有会话。Go 客户端通过签名加密 Forward，经 server → relay → daemon → tmux/真实 CLI，反向解密读取会话事件；没有直接调用本地 Send RPC 代替客户端链路。

可复跑命令（目录示例由测试者选择，不使用脚本默认共享目录；启动和 runner 放同一 shell 保住子进程）：

```sh
export RA_DEV_DIR=$(mktemp -d /tmp/ra-files-acceptance.XXXXXX)
ios/scripts/dev-env.sh start --no-account
RA_FILES_LARGE=1 go run ./scripts/acceptance-files
ios/scripts/dev-env.sh stop
```

| 条件 | Claude Code 2.1.300 / Haiku 5.5 | Codex 0.165.0 / gpt-6.1-sol |
|---|---|---|
| 文字＋小 TXT，作为新会话首条消息 | 原文末尾 `@` 路径；已确认；Agent 返回 TXT 标记；事件有 files | 原文末尾纯路径；已确认；Agent 工具读取 TXT 标记；事件有 files |
| 仅 CSV＋ZIP，无用户文字 | 文件顺序和两行 `@` 路径正确；已确认；事件带两文件 | 文件顺序和两行纯路径正确；已确认；事件带两文件 |
| 下一条文字要求读取上一条 CSV / ZIP | 两个标记均读出，证明提交后仍可读取；ZIP 用工具 | 两个标记均读出，证明 sandbox 内工具能读取上传目录 |
| 图片＋PDF＋文字 | 图片占位＋文字＋PDF 的 `@` 行；已确认；事件同时带 image upload ID 与 file ID；读出 PDF 标记 | 图片占位＋文字＋PDF 纯路径行；已确认；两种附件事件正确；读出 PDF 标记 |

两端六组各跑通两次，均有 SUBMITTED 和匹配的 UserMessage.request_id，没有 FAILED / NEEDS_TERMINAL。Codex 实际 TurnContext 为 workspace-write、on-request、user reviewer，没有放宽 sandbox 或额外可写目录。第二轮增加精确 100 MiB 随机二进制文件，经 768 KiB 分块加密上传，完成后本机 SHA-256 与源数据一致，未触发中继单请求或账户累计限额；文件留在 pending，由测试环境最终清理。

第一轮 Claude 的 ZIP/PDF 读取审批由隔离 tmux 终端允许，是调试操作；第二轮脚本自动从事件读取审批并通过加密 ResolveApproval 允许，两端完整重跑通过。文件和消息内容始终从 Go 客户端发出。测试会话逐个 StopSession，上传目录由会话退出逻辑删除；runner 结束停止测试 daemon 和 tmux，最后用 dev-env `stop` 清理临时中继、数据库与状态目录。

### 界面与未覆盖

在本机 Google Chrome 原生窗口里打开当前 React 附件组件的临时预览，检查 375px 与宽屏列、亮暗主题、＋菜单、长文件名中间省略（保留尾部扩展名）、大小、进度、失败重试和移除，以及已发送文件条目；临时页面、Vite 服务和标签页已清理。Chrome 扩展 host 未连接，没有使用内置浏览器。组件检查使用本地夹具；消息端到端由 Go 客户端驱动，未声称通过网页实际选择器连接真实 CLI 发送。

未覆盖：Android / iOS 手写界面及真机、网页刷新后的系统重新选择、真实移动系统拖放/粘贴、真实网络断连续传、长时间自然过期、磁盘实际写满和 1 GiB 用满、跨真实 daemon 重启的端到端排队恢复。对应逻辑以自动测试覆盖；100 MiB 验证为 Go 加密客户端，未测浏览器同时选择六个 100 MiB 文件的内存峰值。后续进度与待实测范围统一见 [plan.md](plan.md)。

### 文件附件复审回归（2026-10-28）

基于 Phase A 复审修正文件正文和持久化。当前文件行以 [session-files.md](session-files.md) 为准：Claude 的路径加双引号和末尾标记，Codex 加固定前缀；有图片而无用户文字时，用两个换行隔开图片占位与文件行。以下为实际新运行的结果，不以旧版三组验收代替边界验证。

独立 `RA_DEV_DIR`、当前 checkout 的本地 server/daemon、临时账户、独立状态与 tmux，继续使用加密 Go 客户端经 server → daemon → 真实 CLI。运行的入口：

```sh
RA_DEV_DIR=/tmp/ra-files-feedback-final ios/scripts/dev-env.sh start --no-account
RA_DEV_DIR=/tmp/ra-files-feedback-final RA_FILES_FEEDBACK_ONLY=1 go run ./scripts/acceptance-files
RA_DEV_DIR=/tmp/ra-files-feedback-final ios/scripts/dev-env.sh stop

RA_DEV_DIR=/tmp/ra-files-feedback-quoted-final ios/scripts/dev-env.sh start --no-account
RA_DEV_DIR=/tmp/ra-files-feedback-quoted-final RA_FILES_AGENT=claude go run ./scripts/acceptance-files
RA_DEV_DIR=/tmp/ra-files-feedback-quoted-final ios/scripts/dev-env.sh stop

RA_DEV_DIR=/tmp/ra-files-feedback-codex-final ios/scripts/dev-env.sh start --no-account
RA_DEV_DIR=/tmp/ra-files-feedback-codex-final RA_FILES_AGENT=codex RA_FILES_LARGE=1 go run ./scripts/acceptance-files
RA_DEV_DIR=/tmp/ra-files-feedback-codex-final ios/scripts/dev-env.sh stop
```

第一个运行覆盖两端 PNG 文件边界和长文字；第二个使用最终引用形式完整重跑 Claude 的原三组、两组 PNG、长文字和特殊文件名；第三个完整重跑 Codex 的原三组、两组 PNG、长文字，并再验 100 MiB 随机文件哈希，全部通过。Claude 2.1.300 / Haiku 5.5，Codex 0.165.0 / gpt-6.1-sol，Codex 仍为 workspace-write/on-request/user reviewer。文件和消息从加密客户端发送；必要的测试夹具工具读取审批由客户端 ResolveApproval 允许。探索旧引用补全行为时曾在隔离终端手动加末尾标记及允许读取，最终完整运行不依赖这些调试动作。

| 场景 | 实际结果 |
|---|---|
| 单 PNG 从「文件」发送，无文字 | 两端都是 images=0/files=1，原文保留文件行，UserMessage 匹配请求并 SUBMITTED |
| 一张图片＋单 PNG 文件，无文字 | 两端都是 images=1/files=1，只有图片产生占位；文件行和占位分行，已确认 |
| 2272 字符、32 行用户文字＋TXT | 两端经原生长粘贴后（未单独截图折叠占位），完整 user.text 与组合正文逐字比对成功，已确认，返回 FILE_LONG_TOKEN_BIRCH_374；用户文字 SHA-256 为 `3bdc947162a55eaafef07dff6a5289fffa38d6f81fa34d4a8270ccc57a35ecea` |
| Claude 无扩展名、非 ASCII 和符号结尾 | `笔记`、`a+`、`x-`、`末尾.中文` 四个 filename 都产生精确的原生 attachment.type=file，并返回四个 NAME_TOKEN；未加引号的失败解析及最终形式见 [Claude 实测](claude-code-notes.md#文件路径引用) |
| Claude 原生单行 `@…/lone.png` | 一次 Enter 后 8 秒无用户提交，留下文件补全；仅作观察，随后停止隔离会话，不把它当成功发送 |

实际正文的动态目录和 ID 替换为占位符，其余字符原样保留（JSON 中 `\n` 是换行，`\"` 是引号）：

```json
"File: <uploads>/<session>/files/<file1>/as-file.png"
"[Image #1] \n\nFile: <uploads>/<session>/files/<file1>/as-file.png"
"File: @\"<uploads>/<session>/files/<file1>/as-file.png\" (file)"
"[Image #2]\n\nFile: @\"<uploads>/<session>/files/<file1>/as-file.png\" (file)"
```

自动回归新增：首次领取归属与失败重用不退回已有附件、批量失败恢复全部旧引用、完成和移动的目录祖先 fsync 及故障注入、完成记录可见但同步失败时保留两份链接并在重试时重新同步、哈希锁外并发与 abort/re-begin 的文件身份复核、组合标记文件名、清理空目录、Held 运行时/重启引用一致、图片与文件行分隔、网页精确隐藏带引号的文件行、文件夹/不可读文件拒绝，以及菜单选择/外部点击/Escape 关闭。

`go test ./...`、网页 `pnpm typecheck`、`pnpm test --maxWorkers=2`、`pnpm build` 通过。网页为 90 个测试文件、1228 个测试；首次默认并发运行的无关 lazy-qr 测试超时，单独复跑及限制并发的全套通过。真实 CLI 的用户正文与传输校验没有放宽。临时环境与会话均清理；未做真机或直接网页选择器的新增端到端验收。持续会话文件不可回收而可能填满配额的已接受限制记在 plan.md。

### 文件附件第二轮复审与 QR 测试稳定性（2026-10-28）

本轮没有改文件正文组合、CLI 注入或提交确认，也没有修改 Android/iOS 手写上传实现，因此按复审要求不重复完整真实 CLI 验收。

- 逆向移动在 mkdir、rename 后同步等阶段失败时，保留会话归属与持久化回退意图；启动恢复、GC 和下一次领取先完成返回 pending，才允许重新受理。测试同时覆盖图片/文件、字节仍在会话目录/已经回到 pending，以及崩溃前尚未清掉的拒绝请求引用。
- GC 遇到完成记录可见但元数据 fsync 曾失败的文件，先重新同步完成记录，仍失败就保留 `.part`，成功才删除原始副本。未完成会话创建的文件已经消失时，release 清掉归属，使记录按未使用期限过期；普通领取与待回退两种状态均验证。
- Complete 丢回复，随后 Status 为 RECEIVING 且全部字节已持久化时，网页以同一 transfer_id 再发 Complete；回归验证只传一次字节，不开启新传输。供原生客户端遵循的规则写在 [session-files.md](session-files.md)。
- 新会话首条文件没有本机副本时，放回输入框产生明确的失败条目，提示重新选择并禁止发送。只补文档和回归，未重设计首条附件缓存；取舍仍在 plan.md 待确认。
- `files_test.go` 的 Protobuf 请求副本改用 proto.Clone；`go vet ./...` 通过。

**QR 超时根因与修正证据**：`lazy-qr.test.tsx` 是既有测试（文件最初加入于 `7e33bc83`），不是本轮文件上传新增。它在组件触发动态加载后，再次 import qrcode-generator 作为完成屏障；这个屏障依赖真实的 Vite 动态模块加载耗时，而不直接等待 UI 所依赖的异步操作。向旧测试的模块工厂注入 6 秒延迟，日志停在 `QR_TEST waiting for dynamic module barrier`，工厂尚未完成，测试在 5018 ms 触发默认 5000 ms 超时；这复现了可导致超时的加载依赖，不把当时机器忙的推测当成已采集的历史调用栈。

修正把 QR 生成提为保留动态 import 的异步操作，视图测试控制其 pending/resolve：未生成配对码前不调用、等待中不显示图片、无关更新不重新生成、完成后显示正确 URL。测试不再用第二次模块 import 等待，不加固定睡眠，不提高超时，不降低默认并发。构建仍把 qrcode-generator 放在动态 chunk。

随后在默认 vitest 配置下连续执行 10 次全套，每次 90 个测试文件、1230 个测试全部通过，合计 12300 次测试；QR 单测耗时为 31.93–147.65 ms。每次仅增加报告器参数：

```sh
pnpm --dir web test --reporter=json --outputFile=/tmp/ra-file-upload-feedback-2-web-runs/run-N.json
```

其中 N 依次为 1 到 10，没有 maxWorkers 或 testTimeout 覆盖。另跑 `pnpm --dir web typecheck`、`pnpm --dir web build`、`go test ./...` 与 `go vet ./...`，全部通过。受控失败日志和各轮 JSON 报告位于临时目录，未把测试日志原文提交到仓库。

### 附件状态转换与草稿失效复审（2026-10-28）

本轮在 `wt/file-upload` 本地临时目录运行自动测试；没有改 CLI 正文、粘贴和确认规则，没有重复真实 CLI 或浏览器界面端到端验收，也没有修改 Android/iOS 的上传实现。状态与恢复约束维护在 [session-files.md](session-files.md#上传记录与字节的状态转换)。

- 修复前，扩展既有退回测试到未启动会话 release：文件 / 图片 × mkdir、rename、意图记录 fsync、pending fsync、session fsync × 启动 / GC，新增 20 个场景全部失败；同一测试的 untake 场景原本通过。修复后两种入口共 40 个场景通过。
- `TestUploadTransitionFaultMatrix` 覆盖 Begin、Put、Complete、take、untake、release、Returning 恢复、正向 reconcile、Abort、过期、会话退出，以及图片取消和 LRU。每个步骤前注入 I/O 失败，或在真实操作完成后 panic 跳过调用方撤回；再用全新 uploads 实例分别执行启动或 GC 恢复，共 560 个场景。断言字节保留、拒绝的附件可重用、删除可重试，以及 rename 前已有持久 owner、清除 owner 前两侧目录均已同步、unlink 原始副本前完成记录已经提交。这是进程中断边界测试，不模拟磁盘断电后的文件系统回滚。
- 另测未启动会话在返回意图、两侧目录同步、清除 owner 失败时保留 Starting 元数据并重试；会话清理不能删除尚未返回的图片或文件；图片完成记录原子替换后 fsync 失败时保留目标与 `.part`，启动 / GC 重同步后再移除 `.part`。
- 网页用两个 Tray：第二个草稿阻塞全局文件上传槽位，分别触发第一个的 A → B → A、重试、过期、恢复、移除；在同一调用栈和 abort 回调里检查 ready 为 false、旧 ID 已清除，释放槽位后只接收新 generation 的完成结果。新增 5 个场景在旧实现全部失败，修复后通过；无本机副本的图片恢复也立即清除历史 ID。
- 网页 `pnpm typecheck`、默认并发的 `pnpm test`（90 文件、1235 测试）、`pnpm build` 通过；`go vet ./...` 通过。`go test ./...` 通过（daemon 包 186.103 秒）。
