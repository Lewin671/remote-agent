# Claude Code 实测记录

本文记录 remote-agent 接入 Claude Code 所依据的实测结果。写法和 [codex-notes.md](codex-notes.md) 一样：只写 Claude Code 实际怎么表现；remote-agent 据此的设计见 [architecture.md](architecture.md) 第 7、16 节，还没测的见 [plan.md](plan.md) 的「待确认」。

没有特别标注的条目都是实测结果，推断或没测过的会在句中标「推测」「未验证」。升级 Claude Code 后，涉及的条目要重新验证。

最近一次验证：Claude Code **2.1.300**，macOS，tmux 3.7c，2026-10-17；在此之前是 2.1.287（2026-10-03）和 2.1.284（2026-09-29）。实测在独立的 tmux socket 里进行，会话用 `--settings <文件>` 注入 hook，模型用 `--model haiku`（部分条目用 sonnet，已注明）。本机 `~/.claude/settings.json` 的 `permissions.defaultMode` 是 `bypassPermissions`、`skipDangerousModePermissionPrompt` 为 true、`language` 为中文，下文凡是受它们影响的地方会注明。`permissions.defaultMode` 是 bypass 时，`FullAccess` 为真（第 5 节），启动时用 `--permission-mode default` 仍会进入 manual。

标注了版本的条目以该版本为准；升级后重新验证过的会在版本号里带上新版本。

## 1. 启动

| 参数 | 作用 |
|---|---|
| `--settings <文件或 JSON>` | 叠加一份设置，只对这次启动生效。里面的 `hooks` 和 `~/.claude/settings.json` 里的 hook **一起运行**（实测两边的 Stop hook 都被调用）。不需要像 Codex 那样先信任 hook |
| `--session-id <uuid>` | 指定 Claude Code 的会话 ID，transcript 路径随之确定（第 2 节） |
| `--resume <id>` | 恢复会话：沿用同一个会话 ID，**追加写同一个 transcript** |
| `--model <别名或全名>` | `opus`、`fable`、`sonnet`、`haiku` 等别名，或完整的模型 ID |
| `--effort <档位>` | `low`、`medium`、`high`、`xhigh`、`max` |
| `--permission-mode <模式>` | `acceptEdits`、`auto`、`bypassPermissions`、`manual`、`dontAsk`、`plan`；`default` 也接受，就是 manual |
| `--allow-dangerously-skip-permissions` | 把 bypass 加进 Shift+Tab 的循环，但不直接进入 |
| `--bare`、`--safe-mode` | 按 `--help` 的说明，不运行 settings 里的 hook（未实测） |
| `--add-dir`、`--allowedTools`、`--disallowedTools`、`--mcp-config`、`--tools`、`--betas`、`--file` | 按 `--help`，取多个值（`<tools...>`），一直取到下一个选项 |

- **信任目录**：第一次在某个目录启动时弹出 `Accessing workspace:` / `Quick safety check: Is this a project you created or one you trust?`，选项是 `❯ No, exit`、`Yes, I trust this folder`，**光标默认停在 `No, exit`**，这时按回车会直接退出。按数字键没有反应，要按 ↓ 再回车。信任记录写在 `~/.claude.json` 的 `projects["<真实路径>"].hasTrustDialogAccepted`，路径是解析过软链接的（`/tmp/x` 记成 `/private/tmp/x`）
- 信任之前 hook 都不运行；信任之后立刻触发 `SessionStart`（第 3 节）
- 启动完成后画出欢迎框（版本、模型、目录）和输入框。没有看到更新提示之类的其他启动弹窗
- 以 bypass 模式启动时的风险确认弹窗，本机因为 `skipDangerousModePermissionPrompt` 为 true 没有出现；其他机器上会不会出现、长什么样，未验证
- `claude --version` 输出 `2.1.284 (Claude Code)`
- 本机的 `claude` 是个 zsh 函数，负责设置代理；脚本或 daemon 启动的 Claude Code 拿不到这些变量，要自己带上（和 codex-notes §5 一样）

## 2. transcript

路径：`~/.claude/projects/<目录>/<会话 ID>.jsonl`，`<目录>` 是工作目录的真实路径把 `/` 换成 `-`（`/private/tmp/ra-cc-work` → `-private-tmp-ra-cc-work`）。文件在**第一次发消息时**才创建；hook 的 payload 里直接带 `transcript_path`，不用自己拼。

每行一条 JSON，`type` 字段区分：

| 记录 | 含义 |
|---|---|
| `type: "user"`，`message.content` 是字符串，带 `origin: {kind: "human"}`、`promptSource: "typed"`、`promptId`、`permissionMode` | 用户发的一条消息，也就是**一轮的开始**。远程用 bracketed paste 注入的消息同样记为 `typed`；多行正文原样保留 |
| `type: "user"`，`origin: {kind: "task-notification"}`，`promptSource: "system"` | 后台工作结束的通知，Claude Code 自己开的一轮（§10） |
| `type: "user"`，带 `isMeta: true` | Claude Code 自己加的提示（如 `[Your previous response had no visible output…]`、`<local-command-caveat>`），不是用户说的 |
| `type: "user"`，正文是 `<command-name>/model</command-name>…`、`<local-command-stdout>…</local-command-stdout>` | 斜杠命令和它的输出，没有 `origin`。`/model` 的输出如 `Set model to Sonnet 5.5 for this session only with low effort` |
| `type: "user"`，`isCompactSummary: true` | 压缩后的摘要 |
| `type: "user"`，`content` 里是 `tool_result` 块 | 工具结果：`tool_use_id`、`content`、`is_error`；同一行的 `toolUseResult` 是结构化结果（Bash 为 `stdout`、`stderr`、`interrupted`；AskUserQuestion 为 `questions`、`answers`） |
| `type: "user"`，`content` 里是 text 块 `[Request interrupted by user]` 或 `[Request interrupted by user for tool use]` | 这一轮**被中断** |
| `type: "assistant"` | 模型输出，**每个内容块一行**（`thinking`、`text`、`tool_use`），同一次回复的几行 `message.id` 相同。带 `message.model`（完整 ID，如 `claude-haiku-4-5-20251001`）、`message.usage`、`perTurnEffort`（haiku 为 null，sonnet 为 `medium` 等）。`thinking` 块里有一种是**旁白**（2.1.287 实测，Opus 5.5、Fable 5.1 会写）：模型写给用户看的进度说明，终端把它当普通消息显示，不折叠成 `Thought for …`。它和思考的 JSON 字段完全一样，只有 `signature` 里的标签不同：base64 解开是 protobuf，字段 2 → 1 → 8 是字符串 `narration`（思考是 `thinking`）。旁白通常是同一次回复的第二个块（`apiBlockIndex: 1`），但也见过排在第一个的，不能靠位置判断 |
| `type: "system"`，`subtype: "turn_duration"`，`durationMs` | 一轮**正常结束**。之前还有一条 `stop_hook_summary` |
| `type: "system"`，`subtype: "compact_boundary"`，`compactMetadata.trigger`（`manual`/`auto`）、`preTokens`、`postTokens` | 上下文已压缩 |
| `type: "attachment"`，`attachment.type: "queued_command"`，`prompt`，`origin.kind: "human"` | 运行中发来的消息被并入当前这一轮（第 10 节） |
| `type: "queue-operation"`，`operation: "enqueue"`/`"remove"`，`content` | 运行中按回车排队的消息；`remove` 带 `reason`（如 `absorbed_mid_turn`） |
| `type: "permission-mode"`，`permissionMode` | 权限模式，切换后在下一次写 transcript 时记一条。**恢复会话时记的是上次的模式，不是这次启动的模式**（实测以 manual 恢复，记的是 acceptEdits） |
| 其他：`attachment`（环境、语言、工具列表、CLAUDE.md 等）、`mode`、`atis-latch`、`last-prompt`、`ai-title`、`agent-name`、`file-history-snapshot`、`cost-state` | 和聊天内容无关 |

- **有结束记录的轮次有两种写法**（2.1.284；2.1.300 首条输出前中断的例外见第 10 节）：正常结束是 `turn_duration`；运行中按 Esc 打断时**只有** `[Request interrupted by user…]`，没有 `turn_duration`。在审批框或计划框里按 Esc（或选 No）时两条都有，先中断文字、后 `turn_duration`
- **并行工具**：一次回复里的几个 `tool_use` 各占一行、紧挨着写，结果各自一行 `tool_result`，按 `tool_use_id` 对应，完成顺序不一定和调用顺序相同
- **`tool_use` 在工具开始前就写入**：`sleep 30` 运行中去读，transcript 里已经有它的 `tool_use`，还没有 `tool_result`
- **但 AskUserQuestion 不一定**：一次两问的提问，选择框已经显示了 70 多秒，transcript 里还只有用户消息，这次回复的 `thinking` 和 `tool_use` 直到回答之后才和 `tool_result` 一起写入。同一会话里之前的单问提问、以及注册了 `PreToolUse` hook 的会话里，问题显示时 `tool_use` 已经在 transcript 里。什么情况下会延迟，没有查清；`PreToolUse` hook 每次都在选择框出现前收到这次调用
- 被中断的文字回复照样写一行 `assistant` text，`stop_reason` 为 null，内容是中断前生成的部分
- 上下文占用：`usage.input_tokens + cache_creation_input_tokens + cache_read_input_tokens`。transcript 里没有窗口大小。按状态栏的百分比反推：haiku 4.5 是 200k，sonnet 5.5 是 1M。`/context` 直接写出窗口（2.1.287，Claude Max 账户，2026-10-02）：`--model opus`、`fable`、`sonnet` 都是 `…/1m tokens`，不需要 `[1m]` 后缀；`--model 'opus[1m]'` 同样是 1M，只是标题和模型 ID 变成 `Opus 5.5 (1M context)`、`claude-opus-5-5[1m]`；`sonnet[1m]`、`fable[1m]` 和不带后缀时显示一致。`haiku[1m]` 能启动且显示 1M，是否真能用到没有验证。其他套餐下不带后缀是不是 1M，未验证。环境变量 `CLAUDE_CODE_DISABLE_1M_CONTEXT=1` 把窗口改回标准的 200k（同一版本和账户）：`--model opus`、`sonnet`、`fable` 和不带 `--model` 时 `/context` 都是 `…/200k tokens`；它和 `opus[1m]` 一起用时标题变成 `opus[1m]`，并提示自动压缩把会话保持在 200k 内，所以两者不要同时用
- **图片**以 base64 内嵌在 transcript 里（2.1.280 到 2.1.284 的历史记录统计，2026-09-29）。用户粘贴的图：`type: "user"` 的 `content` 变成数组，正文里是 `[Image #1]` 占位，另有 `{type: "image", source: {type: "base64", media_type, data}}` 块，同一行带 `imagePasteIds`；单张最大约 480 KB。模型用 `Read` 读图片文件时，`tool_result.content` 是同样的 image 块，`toolUseResult` 为 `{type: "image", file: {base64, type, originalSize, dimensions}}`，`dimensions` 里有原始尺寸和实际尺寸，长边超过 2000px 的会被缩小到 2000px；单张最大约 380 KB。本机记录里返回图片的工具只有 `Read`；Claude Code 自己不生成图片
- 账户额度不在 transcript 里（状态栏脚本的输入里有 `rate_limits.five_hour` 等，那是 statusLine 命令的输入，不落盘）
- **官方文档核对（2026-09-30，非本机实测）**：[statusLine 文档](https://code.claude.com/docs/en/statusline) 列出 `rate_limits.five_hour`、`seven_day` 的 `used_percentage` 与 `resets_at`（Unix 秒）。额度在首次 API 响应后才出现；Pro / Max 订阅或配置支出限额的 Claude apps gateway 可提供相关字段，各窗口可单独缺失，过了重置时间会移除。API 计费不能套用订阅窗口；缺字段也不能证明是 API 计费。接入 remote-agent 的采集链路见下一条的实测。
- **statusLine 实测（2.1.285，haiku，Max 订阅登录，2026-09-30）**：用 `--settings` 给会话设的 `statusLine`（`type: command`）会取代用户设置里的，用户自己的命令不再运行，所以要包一层：包装命令把输入原样交给用户的命令，输出和退出码照传，终端上的状态栏和原来一样，输入框的识别不受影响；用户没有状态栏时包装命令什么都不输出，界面只剩模式行，识别同样正常。输入 JSON 的键有 `session_id`、`transcript_path`、`cwd`、`model`（`id`、`display_name`）、`workspace`、`version`、`cost`、`context_window`、`prompt_cache` 等；**第一条回复之前没有 `rate_limits`**，回复之后才有 `{"five_hour":{"used_percentage":80,"resets_at":1790774400},"seven_day":{…}}`（`resets_at` 是 Unix 秒）。输入里没有账户、套餐和登录方式：这些只在 Claude Code 自己的 `~/.claude.json` 的 `oauthAccount`（`accountUuid`、`organizationUuid`、`organizationType` 如 `claude_max` 等）里。状态栏在每次界面更新和 `refreshInterval` 时重跑，额度数字只在有新回复时变
- Plan 模式下即使用 `--model haiku` 启动，模型也变成了 `claude-sonnet-5-5`（`perTurnEffort: medium`），退出 Plan 模式后回到 haiku
- `~/.claude/history.jsonl` 也记用户输入（包括斜杠命令），但多行粘贴只记 `[Pasted text #1 +11 lines]`，正文在 `pastedContents` 里；transcript 里是完整正文
- **折叠的粘贴在 transcript 里带标签**（2.1.287，本机两条记录，2026-10-02）：输入框里显示成 `[Pasted text #N +M lines]` 的粘贴（两条都是 4 行以上；3 行的没有折叠，按原文记录），`type: "user"` 的 `content` 是 `"\n\n<pasted_content id=\"94cd\">\n正文\n</pasted_content id=\"94cd\">\n"`，开闭标签带同一个 id，同一会话的两条 id 相同。remote-agent 发消息用的就是 bracketed paste，所以多行消息都会这样记录；解析时去掉标签。和手打文字混在一起、运行中排队（`queue-operation`、`queued_command`）时的写法未实测，按同样格式处理

## 3. hook

`--settings` 里按事件注册：`{"hooks":{"PermissionRequest":[{"matcher":"*","hooks":[{"type":"command","command":"…","timeout":3600}]}], …}}`，`matcher` 可以写成 `AskUserQuestion|ExitPlanMode` 这样的多选。hook 进程继承 Claude Code 的环境变量（实测能读到启动时设置的 `RA_SESSION`）；命令经 shell 执行，`"$RA_EXE" hook claude` 里的变量会展开。

| 事件 | 什么时候触发 | payload 里有用的字段 |
|---|---|---|
| `SessionStart` | **信任目录之后、第一次发消息之前**就触发（`source: "startup"`）；压缩后再触发一次（`"compact"`）；恢复时 `"resume"`，另带 `context_tokens` | `session_id`、`transcript_path`、`cwd`、`model` |
| `UserPromptSubmit` | 用户消息提交时 | `prompt`、`prompt_id`、`permission_mode` |
| `PreToolUse` | 每次调用工具前，包括 AskUserQuestion、ExitPlanMode | `tool_name`、`tool_input`、`tool_use_id` |
| `PermissionRequest` | 要弹权限框时，见第 6 节 | `tool_name`、`tool_input`、`permission_mode`、`permission_suggestions`；**没有 `tool_use_id`** |
| `PostToolUse` / `PostToolUseFailure` | 工具结束后 | `tool_response`、`duration_ms` |
| `Stop` | 一轮正常结束；被中断时**不触发** | `last_assistant_message` |
| `Notification` | 权限框出现约 6 秒后 | |
| `PreCompact` | 压缩前 | `trigger` |
| `SessionEnd` | 退出时 | |

- payload 里的 `permission_mode` 用内部名字：`default`（manual）、`acceptEdits`、`plan`、`bypassPermissions`
- 同一时刻有几个工具并行时，`PreToolUse` 各触发一次

## 4. 屏幕

用 `capture-pane -p -e` 抓屏。输入框上下各有一条横线（`─`，颜色 244），上面那条的右端可能带会话名（如 `─── create-hello-txt ─`）。

| 画面 | 样子 |
|---|---|
| 空输入框 | 横线下一行是 `❯` + 不换行空格（U+00A0），后面只有一个反色的空格（Claude Code 自己画的光标）。**没有淡色的占位提示** |
| 输入框里有草稿 | `❯` + U+00A0 后跟普通颜色的字，光标（反色空格）在末尾 |
| 多行草稿 | 两条横线之间有多行，第二行起以两个空格缩进 |
| 运行中且有排队消息 | 占位提示 `Press up to edit queued messages`：第一个字符反色（光标），其余淡色（`\e[0;2m`） |
| 运行中 | 输入框的 `❯` 变成灰色（`\e[38;5;246m`），横线上方有一行转圈的状态，如 `· Fermenting… (8s · ↓ 118 tokens)` |
| 之前的用户消息 | `❯ ` 带背景色（`\e[48;5;237m`），不紧挨横线 |
| 列表里选中的行（权限框、提问、计划、菜单） | ` ❯ 1. Yes`：前面有空格，`❯` 是颜色 153，**不反色** |
| 输入框下方 | 先是 statusLine（本机是自定义脚本，内容因人而异），最后是模式行，如 `⏸ manual mode on · ← for agents` |

- 模式行下面偶尔会多一行提示（如 `tmux focus-events off · add 'set -g focus-events on'…`），模式行不一定是最后一行
- 弹窗出现时输入框整个消失，横线变成弹窗的上边框
- bracketed paste 的正文和回车一次写入，Claude Code 正常提交，多行正文完整保留

## 5. 权限模式

模式行的文字：

| 模式 | 模式行 | payload / transcript 里的名字 |
|---|---|---|
| manual | `⏸ manual mode on` | `default` |
| accept edits | `⏵⏵ accept edits on (shift+tab to cycle)` | `acceptEdits` |
| plan | `⏸ plan mode on (shift+tab to cycle)` | `plan` |
| bypass | `⏵⏵ bypass permissions on (shift+tab to cycle)` | `bypassPermissions` |
| auto | `⏵⏵ auto mode on (shift+tab to cycle)` | `auto`（推测，未在 payload 里见到） |

- **Shift+Tab 按固定顺序循环**：manual → accept edits → plan → bypass → auto → manual。不可用的模式跳过：
  - bypass 只有在以 bypass 启动（本机是 `defaultMode` 设成了 bypass）、带 `--permission-mode bypassPermissions` 或带 `--allow-dangerously-skip-permissions` 时才在循环里。以 `--permission-mode auto` 或 `default` 启动的会话，循环里没有 bypass；恢复时带 `--permission-mode default` 也一样
  - auto 要模型支持：haiku 下状态栏右侧显示 `auto mode unavailable for this model`，循环里没有 auto；sonnet 下有。这句提示在 Shift+Tab 跳过 auto 之后也会出现，并停留几秒以上：实测 haiku 下切换模式后马上用 `/model` 换到 sonnet，daemon 7 秒后读到的状态栏里仍有这句，一分钟后再看已经没有了，所以它不能说明当前模型有没有 auto
  - 2.1.293（Claude Max，2026-10-09，`--setting-sources project,local --model haiku`）：别名 haiku 是 Haiku 5.5，循环是 manual → accept edits → plan → auto → manual，**有 auto**；`/model` 里它也有 effort 行（`◐ Medium effort (default)`）。所以「haiku 没有 auto、没有 effort」只对 Haiku 4.5 成立
  - 项目里的设置不能把 bypass 设成默认（同一次实测，用户级设置用 `--setting-sources project,local` 排除在外）：`.claude/settings.local.json` 写 `permissions.defaultMode: "bypassPermissions"`（再加 `skipDangerousModePermissionPrompt: true` 也一样），或写在 `.claude/settings.json` 里，会话都以 manual 启动，没有任何提示，循环里也没有 bypass；同一个文件写 `acceptEdits` 则生效。排除掉用户级设置后它会不会退回用户级的 `defaultMode`，未验证
- **运行中也能 Shift+Tab**：实测一轮运行时连按两次，模式行从 bypass 变到 manual、再到 accept edits
- **没有反向的按键**，模式只能沿上面的顺序往前走。`/plan`（`Enable plan mode or view the current session plan`）空闲时直接进入 Plan 模式，对话里记 `Enabled plan mode`，不经过别的模式；运行中输入 `/plan` 只会排队，这一轮结束后才生效。没有找到直接离开 Plan 模式或切到其他模式的命令（`/permissions` 管的是允许和拒绝规则）
- 切换不写 `~/.claude/settings.json`（实测前后校验值相同）

## 6. 审批（PermissionRequest）

- **什么时候触发**：只在 Claude Code 要弹权限框时触发。manual 下 Bash 触发；bypass 下 Bash 不触发（实测两个并行 Bash 都只有 Pre/PostToolUse）；auto 下分类器放行的命令不触发（实测 `touch`、`curl`、`curl … | sh` 都显示 `Allowed by auto mode classifier`，没有 PermissionRequest）。auto 下被分类器拦下的请求会不会弹框、会不会触发 hook，没能复现，未验证
- **AskUserQuestion 和 ExitPlanMode 也会触发 PermissionRequest**（manual 下实测；Plan 模式下 ExitPlanMode 也触发）
- **原生权限框和 hook 同时出现**：hook 阻塞等待时，终端里照常显示权限框。两边谁先回答算谁的：
  - hook 先返回 `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`：权限框立即消失，工具执行，界面显示 `Allowed by PermissionRequest hook`
  - hook 返回 `"behavior":"deny","message":"…"`：界面显示 `Error: <message>` 和 `Denied by PermissionRequest hook`，`tool_result` 的内容就是 message（`is_error: true`），模型接着回复，这一轮正常结束
  - 在终端里先选 `1. Yes`：工具执行，**hook 进程不会被结束**，会一直等下去，它之后返回什么都不再起作用
  - 在终端里先选 `3. No` 或按 Esc：拒绝并中断这一轮，hook 进程被结束
  - hook 什么都不输出、正常退出：权限框照常等人回答
- 权限框的样子：`Bash command` / 命令 / 说明 / `Do you want to proceed?` / `❯ 1. Yes` / `2. Yes, and always allow access to <目录> from this project` / `3. No` / `Esc to cancel · Tab to amend`。文件写入是 `Create file` / `Do you want to create hello.txt?`，第 2 项是 `Yes, and switch to accept edits …`。按数字键直接选中
- **ExitPlanMode 的 PermissionRequest 返回 allow 不起作用**：hook 返回 allow 后进程退出，计划框仍然留在屏幕上等人选择（实测等了 10 秒以上）。计划只能在框里选（第 8 节）

## 7. 模型提问（AskUserQuestion）

- 调用时先写 transcript 的 `tool_use`（`input.questions[]`：`question`、`header`、`options[]`（`label`、`description`）、`multiSelect`），再触发 `PreToolUse`（manual 下还有 `PermissionRequest`），然后弹出选择框。**问题在等待回答时就在 transcript 里**，没有单独的 ID，用 `tool_use` 的 ID
- 选择框：第一行是问题标签（`☐ Language`，多个问题时 `←  ☐ Color  ☐ Fruit  ✔ Submit  →`），然后是问题、带编号的选项和说明，末尾自动加 `Type something.` 和 `Chat about this`，底部 `Enter to select · ↑/↓ to navigate · Esc to cancel`
- **按数字键直接选中**：只有一个问题时按下就提交；多个问题时按下就跳到下一个问题（标签变成 `☒`），最后一个问题答完进入 `Review your answers` / `Ready to submit your answers?` / `❯ 1. Submit answers` / `2. Cancel`，再按 1 提交
- **多选**（`multiSelect: true`）：选项显示成 `1. [ ] Apple`，数字键切换勾选（`[✔]`），按 → 进入提交页，再按 1 提交
- **答案**：`tool_result` 的 `toolUseResult.answers` 是 `{"<问题原文>":"<选项 label>"}`，多选时是逗号加空格连起来的一个字符串（`"Apple, Plum"`）；`content` 是一句 `Your questions have been answered: "…"="…"…`
- **两条路的实测对比**：
  - **让原生选择框照常出现、从外面按数字键回答**：可行。本地终端里看得到问题，本地先答或远程先答都会写 `tool_result`，从 transcript 就能知道已经答了
  - **阻塞 `PreToolUse` hook，由 hook 返回答案**：hook 返回 `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{…原 tool_input,"answers":{"<问题原文>":"<label>"}}}}` 后，界面不弹选择框，直接显示 `User answered Claude's questions: · <问题> → <答案>`，模型按这个答案继续（manual 模式下实测，之后也没有再弹权限框）。但是 **hook 阻塞期间终端里只有 `(running PreToolUse hook · 8s …)`，看不到问题**，本地没法回答，只能按 Esc 中断
- 按 Esc：拒绝这次调用并中断这一轮（和权限框一样，推测；实测的是计划框和权限框）

## 8. 计划（ExitPlanMode）

- Plan 模式下，模型先把计划写进 `~/.claude/plans/<名字>.md`（`Write` 工具，Plan 模式下不需要审批），再调用 `ExitPlanMode`，`input.plan` 是计划正文，`input.planFilePath` 是文件路径。先写 transcript 的 `tool_use`，再触发 `PreToolUse` 和 `PermissionRequest`
- 屏幕：`Ready to code?` / `Here is Claude's plan:` / 计划正文 / `Claude has written up a plan and is ready to execute. Would you like to proceed?`，选项随可用的模式变化：
  - bypass 可用时：`1. Yes, and switch to BYPASS PERMISSIONS (no further prompts) for this session`
  - bypass 不可用时：`1. Yes, auto-accept edits`
  - bypass 不可用、auto 可用时（sonnet，以 `--permission-mode default` 启动）：`1. Yes, and use auto mode`
  - 两种情况下第 2、3 项都是 `2. Yes, manually approve edits`、`3. Tell Claude what to change`（下面一行 `shift+tab to approve with this feedback`）
- 按 2：计划被批准（`tool_result` 为 `User has approved your plan…`，另有 `plan_mode_exit` 附件），模式回到 manual，模型开始执行
- 按 3：光标移到第 3 项，它变成输入框，打的字替换掉选项文字（`❯ 3. make it a single step`），回车后这是一次带说明的拒绝：`tool_result` 为 `…the user said:\nmake it a single step`，模型留在 Plan 模式重新规划
- 按 Esc：拒绝并中断这一轮，留在 Plan 模式，记 `[Request interrupted by user for tool use]` 和 `turn_duration`
- 没有「清空上下文再执行」这一项（2.1.284）

## 9. 切换模型和 effort

- `/model` 回车打开 `Select model`：`1. Default (recommended)`、`2. Opus`、`3. Fable`、`4. Sonnet`、`5. Haiku`，每行后面是显示名和说明（`Opus 5.5 · Best for everyday, complex tasks`），当前模型带 `✔`，光标（`❯`）停在当前模型上。列表随账号和版本变化：2.1.287（Claude Max，2026-10-02）列的是带版本号的 `Opus 5.5`、`Fable 5.1`、`Sonnet 5.5`、`Haiku 4.5`，后面还有旧版本模型，没有单独的 1M 上下文选项；2.1.293（Claude Max，2026-10-09）又是每个别名一行：`1. Default (recommended)`、`2. Opus`、`3. Fable`、`4. Sonnet`、`5. Haiku`，后面的显示名是 `Opus 5.5`、`Fable 5.1`、`Sonnet 5.5`、`Haiku 5.5`，没有旧版本；2.1.300（Claude Max，2026-10-17）也是每个别名一行，别名行之后另有带版本号的 `Haiku 4.5`、`Sonnet 5`、`Opus 5`、`Fable 5`、`Opus 4.8`，末尾一行 `⏷ +3 models` 表示还有更多（截图见 `internal/claude/testdata/model/model-2.1.300.txt`）
- 列表下面是 effort 行：`◐ Medium effort (default) ←/→ to adjust`。档位从低到高是 low、medium、high、xhigh、max（2.1.300 实测，用 `--model sonnet` 的会话；`←/→` 每按一次走一档，**两端停住、不循环**：在 Max 再按 → 仍是 Max，在 Low 再按 ← 仍是 Low）。2.1.284 到 2.1.287 的实测是 `→` 从 Medium 依次经过 High、xHigh、Max、Low，再回到 Medium，**首尾相接**；档位顺序相同，只是当时会循环。两端是同一台机器上两次实测，不排除中间版本改过，取舍见 [architecture.md](architecture.md) 第 16 节。haiku：2.1.284 的 Haiku 4.5 显示 `○ Effort not supported for Haiku`，2.1.300 的别名 haiku 是 Haiku 5.5，有 effort 行，状态栏右侧显示 `○ low · /effort`（和 2.1.293 一致，见第 5 节）
- 底部：`Enter to set as default · s to use this session only · Esc to cancel`。和 Codex 一样，**回车改全局默认值，`s` 只改当前会话**；按 `s` 后 `~/.claude/settings.json` 不变
- **按数字键等于移到那一行再按回车**（走「设为默认」这条路），不能用
- 对话已有缓存时，按 `s` 后还会弹 `Switch model?` / `❯ 1. Yes, switch to Haiku 4.5` / `2. No, go back`，按 1 确认后仍然只改当前会话
- 成功后对话里出现 `⎿  Set model to Sonnet 5.5 for this session only with low effort`（haiku 没有 effort 部分；effort 没变时也不写这一段），状态栏右侧显示 `○ low · /effort`。transcript 里是一条没有 `origin` 的用户记录，正文为 `<local-command-stdout>…</local-command-stdout>`，文字相同，只是模型名和 effort 各用反引号括起来（``Set model to `Sonnet 5.5` for this session only with `low` effort``，2.1.284 的记录）；回车设为默认时是 ``Set model to `Fable 5.1` and saved as your default for new sessions``（2.1.258 到 2.1.280 的本机记录，名字可能带 `(1M context)`、`(default)`）。这条记录的时间戳是执行命令的时刻，排在下一条用户消息之前
- 窄窗口下的折行（2.1.293，46 列，2026-10-09）：菜单每行的说明折成几行，缩进到说明那一列；末行提示折成 `Enter to set as default · s to use this` / `session only · Esc to cancel`；成功的那句话折成 `  ⎿  Set model to Fable 5.1 for this session` / `     only with xhigh effort`，续行缩进 5 格，和正文对齐
- 下一次回复的 `assistant` 记录里 `message.model`、`perTurnEffort` 随之变化

## 10. 中断和运行中的输入

| 按键 | 运行中 | 空闲时 |
|---|---|---|
| Esc | 中断这一轮：工具运行中时，这个工具记为被拒绝（`User rejected tool use`），另记 `[Request interrupted by user for tool use]`；生成文字时记 `[Request interrupted by user]`。都没有 `turn_duration`，也不触发 Stop hook | 未测；按两次会打开回退菜单（推测，未验证） |
| Enter | 输入框里的消息**排队**：transcript 记 `queue-operation` `enqueue`，输入框上方显示这条消息，占位提示变成 `Press up to edit queued messages`。工具结束时它被并入当前这一轮（`queue-operation` `remove`，`reason: absorbed_mid_turn`，再有一条 `queued_command` 附件），模型在同一轮里回应 | 发送 |

- **首条输出前中断**（2.1.300，haiku，2026-10-16，本地隔离环境实测）：用户消息写入后立即发 Esc，CLI 返回非忙碌输入框，并把本轮原文恢复为草稿。transcript 保留这条 `user` 和请求元数据，却没有 assistant 回复、`[Request interrupted by user…]` 或 `turn_duration`。修复前本地命令仍报 running；修复后通过 Go 调试客户端经本地 server 连续三次复现相同原生状态，并收到中断结束事件，随后 QUEUE 消息正常完成。另在 44 列、55 行的终端中复测中文长提示，原文在中文字之间软折成五行；修正空白比较后，中断和 daemon 重启恢复均可结算。此项为调试触发，未直接操作浏览器或手机；API 错误、图片和超长草稿未验证。
- **撤回排队的消息**（2.1.285，haiku，2026-09-30 实测）：运行中有排队消息时按 ↑，所有排队的消息一起回到输入框，多条之间换行，transcript 记一条 `queue-operation` `popAll`（实测 `content` 只有最后一条）。这时 Ctrl-U 清空输入框，这些消息就不会再发出：这一轮照常结束，之后也不会作为新的一轮提交。要撤回其中一条，只能全部拉回、清空，再把其余的重新回车排队
- 中断时正在运行的 `sleep 30` 界面显示 `Interrupted · What should Claude do instead?`；后台是否继续运行，未测
- **Bash 工具拦截独立的 `sleep`**（2.1.300，haiku，2026-10-17 实测）：让模型「只运行 `sleep 30`」时，Bash 调用立刻返回错误 `<tool_use_error>Blocked: standalone sleep 30. To wait for a condition, use Monitor with an until-loop (e.g. `until <check>; do sleep 2; done`). To wait for a command you started, use run_in_background: true. Do not chain shorter sleeps to work around this block.</tool_use_error>`，这一轮在几秒内以 `turn_duration` 结束，模型改成建议用户在终端敲 `! sleep 30`。所以「发一条命令让它一直跑到被中断」不能再用独立 `sleep`；用工具自己建议的 `until [ -f <不会出现的文件> ]; do sleep 2; done` 实测能一直运行、也能被 Esc 中断（2.1.284 到 2.1.287 上 `sleep 30` 可以照常运行，notes 前述条目）。`sleep` 在 auto 模式下的分类器行为未测

粘贴图片路径（2.1.284，haiku，2026-09-29 实测，用 `tmux send-keys -H` 写入 bracketed paste）：

- 一次粘贴的内容**只有**一个图片文件的绝对路径时，输入框里变成 `[Image #N]`，效果和在电脑上粘贴图片相同：提交后 transcript 的用户记录带 image 块和 `imagePasteIds`，模型能看到图。路径后面带了文字的不转换，按普通文字处理。编号在会话里一直累加，删掉的不复用
- 图片在**粘贴时**就读入：粘贴后删掉文件，再提交照样带图
- 转换是**异步**的：图片路径、文字和回车一次写入时，文字先出现在输入框里，`[Image #N]` 随后追加到末尾，回车被吞掉，消息留在输入框里没有提交。分开写入、等 `[Image #N]` 出现后再写文字和回车，就能正常提交
- 大图会被处理后再存（2026-09-29 实测）：粘贴 4000×3000 的 PNG，transcript 里是 2000×1500 的 PNG；粘贴 7.7 MB 的 1600×1600 噪点 PNG，transcript 里是约 470 KB 的 JPEG，尺寸不变。两张模型都能看到
- 运行中粘贴图片再回车同样会排队。这时 `queued_command` 附件的 `prompt` 不是字符串，而是 text 块加 image 块的数组，同一条附件带 `imagePasteIds`；`queue-operation` 的 `content` 仍是字符串（`[Image #5]这张图…`）
- 逐张粘贴（2.1.284，haiku，2026-09-29 实测）：小图 0.1 秒内出现 `[Image #N]`，4 MB 的 1200×1200 噪点 PNG 约 0.5 秒，9 MB 的 2000×1500 约 1.3 秒；占位之间加一个空格，正文紧贴在最后一个占位后面（`[Image #3]这张图…`），占位和正文同一种样式。连着粘贴 5 张不等待，也都依次出现
- 认不出的图：内容不是图片的 `.png`、不存在的路径都不转换，路径按普通文字接在前一个占位后面，没有提示
- 清空输入框：**Ctrl-U** 一次删掉整行，包括占位；删掉的图不随后续消息提交（transcript 的 `imagePasteIds` 只有后来那张），编号继续累加不复用。Ctrl-U 在运行中同样安全，不像 Esc 会中断这一轮
- 运行中排队的消息显示在输入框**上方**，同样以 `❯ [Image #10] [Image #11]…` 开头，所以找新出现的占位只能看两条横线之间的输入框，不能搜整个屏幕。输入框随后显示淡色的 `Press up to edit queued messages`
- 经 remote-agent 发图的实测（2.1.285，haiku，2026-09-30；Claude Code 在实测期间从 2.1.284 自动更新）：转换中的输入框是空的，看不到路径，转换完才出现占位；5200×3900、11.6 MB 的 JPEG 在 transcript 里是 2000×1500、约 360 KB，上传前去掉的 EXIF（含 GPS）不在 transcript 里。运行中发两张图，`queued_command` 在当前工具调用结束后写入，模型读出了两张图的字。haiku 收到粘贴的图后还会自己用 `Read` 再读一次这个文件
- 只有图、没有文字的消息（2.1.285，2026-09-30 实测）：输入框里只有 `[Image #N]` 时，单独按回车照常提交；但先写一段空的 bracketed paste（`\e[200~\e[201~`）再按回车，消息留在输入框里不提交。Codex 0.159.0 两种写法都会提交
- 提交的证据：空闲时是 `type: user` 的记录，`imagePasteIds` 就是屏幕上的编号，另有一条 `isMeta` 的 `[Image: source: <路径>]`；运行中是 `queue-operation` 的 `enqueue`（`content` 带占位的原文）和随后的 `queued_command` 附件（`prompt` 数组带 image 块，`imagePasteIds` 同上），附件要等当前工具调用结束才写

后台工作（haiku 实测）：

- Bash 的 `run_in_background`：工具结果显示 `Running in the background (↓ to manage)`；这一轮的结束行变成 `✻ Worked for 5s · done 4:54 PM · 1 shell still running`，这一行在命令结束后仍留在屏幕上；**模式行**多出 `· 1 shell`（如 `⏵⏵ accept edits on · 1 shell · ← for agents`），命令结束后消失
- 后台子代理（Agent 工具的 `run_in_background`）：输入框上方显示 `✻ Waiting for 1 background agent to finish`，模式行下面列出 `⏺ main` 和每个子代理（`◯ general-purpose  <说明>  6s · ↓ 12.9k tokens`）。结束后列表消失，模式行里剩 `/tasks to see subagents`。启动它的那一轮照常以 `turn_duration` 结束，记录里带 `pendingBackgroundAgentCount: 1`
- Claude Code 也会自己把长命令转到后台（2.1.285，haiku 并行跑 `sleep 45` 实测）：Bash 调用随即返回，结果正文以 `Command running in background with ID:` 开头，调用的输入里只有命令、没有 `run_in_background`，所以只能从结果看出它转到了后台
- 后台工作结束时，Claude Code 自己发起新的一轮：transcript 先有 `queue-operation`，再有一条正文为 `<task-notification>…` 的用户记录，`origin.kind` 为 `task-notification`、`promptSource` 为 `system`，然后模型回复，以 `turn_duration` 结束
- Shift+Tab 一次连续写入多个（`tmux send-keys -H` 一次发三个 `\e[Z`）：全部生效，模式行直接显示走完三步后的模式

## 11. 压缩、恢复和分叉

- `/compact`：输入框上方显示 `Compacting conversation… (22s)`，完成后显示 `Compacted (ctrl+o to see full summary)`。transcript 依次是 `/compact` 这条用户记录（没有 `origin`）、`compact_boundary`、`isCompactSummary` 的摘要、斜杠命令的记录；hook 依次是 `PreCompact`、`SessionStart`（`source: "compact"`）。这次压缩用了 41 秒，`preTokens` 36057 → `postTokens` 3605
- `/exit` 退出，触发 `SessionEnd`
- `claude --resume <id>`：同一个会话 ID、同一个 transcript，接着追加；`SessionStart` 的 `source` 为 `resume`；屏幕上重新画出之前的对话
- `claude --resume <id> --fork-session --session-id <新 uuid>`（2.1.287，haiku，2026-10-03）：以新的会话 ID 接着原对话，原 transcript 不动；`--session-id` 在交互模式和 `-p` 下都生效。屏幕上重新画出之前的对话。**新 transcript 要到发出第一条消息时才出现**，这之前磁盘上没有它
  - 新 transcript 的内容：开头几行是新会话自己的 `mode`、`permission-mode` 等没有 `uuid` 的记录，接着是原会话全部记录的副本（`uuid`、`parentUuid`、时间戳都和原来一样，只有 `sessionId` 换成新的），然后才是新消息。没有标明「分叉自哪个会话」的字段
  - 分叉出的会话再分叉，新 transcript 里的副本包含两层的全部记录，`uuid` 仍然不变
  - 还没发过消息的会话没有 transcript，不能分叉

## 12. 做实测时的注意事项

- 用独立的 tmux socket，测完 `kill-server`；hook 用 `--settings` 注入，不要改 `~/.claude/settings.json`，测前测后对比它的校验值
- 信任目录会写进 `~/.claude.json`（和 Codex 的信任写进 `config.toml` 一样，是全局状态）
- 测试用 `--model haiku` 省额度；但 haiku 没有 auto 模式和 effort，这两项要用 sonnet 测
- 不要在菜单里按数字键或回车，以免改掉全局默认模型


## 定时任务 MCP 接入实测

2026-09-30，Claude Code 2.1.285、haiku，使用独立的状态目录、tmux socket 和临时工作目录实测：通过启动时的 `--mcp-config` 注册 stdio MCP，没有注入会话指令。用户消息要求安排“本轮结束后 2 秒”，模型调用 `schedule_create`，daemon 在本轮结束后发出消息，同一对话收到回复。

另以本地 schedule 命令创建一次性恢复任务，退出 CLI 并重启 daemon，到时间自动恢复，CLI 对话 ID 不变、消息收到回复；定时新开对话也收到回复。托管会话环境中的 `CLAUDE_CODE_DISABLE_CRON` 为 `1`；官方说明该变量禁用会话调度、cron 工具和 `/loop`（[scheduled-tasks](https://code.claude.com/docs/en/scheduled-tasks#disable-scheduled-tasks)）。本次没有进一步让模型调用 cron 来验证拒绝，也没有修改全局环境。恢复后模型再次调用 `schedule_list` 成功；其他用户 MCP 的实际连接尚未逐一实测，配置合并由单元测试覆盖。
