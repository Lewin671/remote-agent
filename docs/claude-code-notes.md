# Claude Code 实测记录

本文记录 remote-agent 接入 Claude Code 所依据的实测结果。写法和 [codex-notes.md](codex-notes.md) 一样：只写 Claude Code 实际怎么表现；remote-agent 据此的设计见 [architecture.md](architecture.md) 第 7、16 节，还没测的见 [plan.md](plan.md) 的「待确认」。

没有特别标注的条目都是实测结果，推断或没测过的会在句中标「推测」「未验证」。升级 Claude Code 后，涉及的条目要重新验证。

最近一次验证：Claude Code **2.1.284**，macOS，tmux 3.7c，2026-09-29。实测在独立的 tmux socket 里进行，会话用 `--settings <文件>` 注入 hook，模型用 `--model haiku`（部分条目用 sonnet，已注明）。本机 `~/.claude/settings.json` 的 `permissions.defaultMode` 是 `bypassPermissions`、`skipDangerousModePermissionPrompt` 为 true、`language` 为中文，下文凡是受它们影响的地方会注明。

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
| `type: "user"`，带 `isMeta: true` | Claude Code 自己加的提示（如 `[Your previous response had no visible output…]`、`<local-command-caveat>`），不是用户说的 |
| `type: "user"`，正文是 `<command-name>/model</command-name>…`、`<local-command-stdout>…</local-command-stdout>` | 斜杠命令和它的输出，没有 `origin`。`/model` 的输出如 `Set model to Sonnet 5.5 for this session only with low effort` |
| `type: "user"`，`isCompactSummary: true` | 压缩后的摘要 |
| `type: "user"`，`content` 里是 `tool_result` 块 | 工具结果：`tool_use_id`、`content`、`is_error`；同一行的 `toolUseResult` 是结构化结果（Bash 为 `stdout`、`stderr`、`interrupted`；AskUserQuestion 为 `questions`、`answers`） |
| `type: "user"`，`content` 里是 text 块 `[Request interrupted by user]` 或 `[Request interrupted by user for tool use]` | 这一轮**被中断** |
| `type: "assistant"` | 模型输出，**每个内容块一行**（`thinking`、`text`、`tool_use`），同一次回复的几行 `message.id` 相同。带 `message.model`（完整 ID，如 `claude-haiku-4-5-20251001`）、`message.usage`、`perTurnEffort`（haiku 为 null，sonnet 为 `medium` 等） |
| `type: "system"`，`subtype: "turn_duration"`，`durationMs` | 一轮**正常结束**。之前还有一条 `stop_hook_summary` |
| `type: "system"`，`subtype: "compact_boundary"`，`compactMetadata.trigger`（`manual`/`auto`）、`preTokens`、`postTokens` | 上下文已压缩 |
| `type: "attachment"`，`attachment.type: "queued_command"`，`prompt`，`origin.kind: "human"` | 运行中发来的消息被并入当前这一轮（第 10 节） |
| `type: "queue-operation"`，`operation: "enqueue"`/`"remove"`，`content` | 运行中按回车排队的消息；`remove` 带 `reason`（如 `absorbed_mid_turn`） |
| `type: "permission-mode"`，`permissionMode` | 权限模式，切换后在下一次写 transcript 时记一条。**恢复会话时记的是上次的模式，不是这次启动的模式**（实测以 manual 恢复，记的是 acceptEdits） |
| 其他：`attachment`（环境、语言、工具列表、CLAUDE.md 等）、`mode`、`atis-latch`、`last-prompt`、`ai-title`、`agent-name`、`file-history-snapshot`、`cost-state` | 和聊天内容无关 |

- **轮次结束有两种写法**：正常结束是 `turn_duration`；运行中按 Esc 打断时**只有** `[Request interrupted by user…]`，没有 `turn_duration`。在审批框或计划框里按 Esc（或选 No）时两条都有，先中断文字、后 `turn_duration`
- **并行工具**：一次回复里的几个 `tool_use` 各占一行、紧挨着写，结果各自一行 `tool_result`，按 `tool_use_id` 对应，完成顺序不一定和调用顺序相同
- **`tool_use` 在工具开始前就写入**：`sleep 30` 运行中去读，transcript 里已经有它的 `tool_use`，还没有 `tool_result`
- **但 AskUserQuestion 不一定**：一次两问的提问，选择框已经显示了 70 多秒，transcript 里还只有用户消息，这次回复的 `thinking` 和 `tool_use` 直到回答之后才和 `tool_result` 一起写入。同一会话里之前的单问提问、以及注册了 `PreToolUse` hook 的会话里，问题显示时 `tool_use` 已经在 transcript 里。什么情况下会延迟，没有查清；`PreToolUse` hook 每次都在选择框出现前收到这次调用
- 被中断的文字回复照样写一行 `assistant` text，`stop_reason` 为 null，内容是中断前生成的部分
- 上下文占用：`usage.input_tokens + cache_creation_input_tokens + cache_read_input_tokens`。transcript 里没有窗口大小。按状态栏的百分比反推：haiku 4.5 是 200k，sonnet 5.5 是 1M；其他模型推测也是 1M，未验证
- 账户额度不在 transcript 里（状态栏脚本的输入里有 `rate_limits.five_hour` 等，那是 statusLine 命令的输入，不落盘）
- Plan 模式下即使用 `--model haiku` 启动，模型也变成了 `claude-sonnet-5-5`（`perTurnEffort: medium`），退出 Plan 模式后回到 haiku
- `~/.claude/history.jsonl` 也记用户输入（包括斜杠命令），但多行粘贴只记 `[Pasted text #1 +11 lines]`，正文在 `pastedContents` 里；transcript 里是完整正文

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
  - auto 要模型支持：haiku 下状态栏右侧显示 `auto mode unavailable for this model`，循环里没有 auto；sonnet 下有
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
  - 两种情况下第 2、3 项都是 `2. Yes, manually approve edits`、`3. Tell Claude what to change`（下面一行 `shift+tab to approve with this feedback`）
- 按 2：计划被批准（`tool_result` 为 `User has approved your plan…`，另有 `plan_mode_exit` 附件），模式回到 manual，模型开始执行
- 按 3：光标移到第 3 项，它变成输入框，打的字替换掉选项文字（`❯ 3. make it a single step`），回车后这是一次带说明的拒绝：`tool_result` 为 `…the user said:\nmake it a single step`，模型留在 Plan 模式重新规划
- 按 Esc：拒绝并中断这一轮，留在 Plan 模式，记 `[Request interrupted by user for tool use]` 和 `turn_duration`
- 没有「清空上下文再执行」这一项（2.1.284）

## 9. 切换模型和 effort

- `/model` 回车打开 `Select model`：`1. Default (recommended)`、`2. Opus`、`3. Fable`、`4. Sonnet`、`5. Haiku`，每行后面是显示名和说明（`Opus 5.5 · Best for everyday, complex tasks`），当前模型带 `✔`，光标（`❯`）停在当前模型上。列表随账号和版本变化
- 列表下面是 effort 行：`◐ Medium effort (default) ←/→ to adjust`，按 → 依次是 `● High effort`、`◉ xHigh effort`、`◈ Max effort`、`○ Low effort`，再回到 Medium，首尾相接。haiku 显示 `○ Effort not supported for Haiku`
- 底部：`Enter to set as default · s to use this session only · Esc to cancel`。和 Codex 一样，**回车改全局默认值，`s` 只改当前会话**；按 `s` 后 `~/.claude/settings.json` 不变
- **按数字键等于移到那一行再按回车**（走「设为默认」这条路），不能用
- 对话已有缓存时，按 `s` 后还会弹 `Switch model?` / `❯ 1. Yes, switch to Haiku 4.5` / `2. No, go back`，按 1 确认后仍然只改当前会话
- 成功后对话里出现 `⎿  Set model to Sonnet 5.5 for this session only with low effort`（haiku 没有 effort 部分；effort 没变时也不写这一段），transcript 里也有同样文字的 `<local-command-stdout>`；状态栏右侧显示 `○ low · /effort`
- 下一次回复的 `assistant` 记录里 `message.model`、`perTurnEffort` 随之变化

## 10. 中断和运行中的输入

| 按键 | 运行中 | 空闲时 |
|---|---|---|
| Esc | 中断这一轮：工具运行中时，这个工具记为被拒绝（`User rejected tool use`），另记 `[Request interrupted by user for tool use]`；生成文字时记 `[Request interrupted by user]`。都没有 `turn_duration`，也不触发 Stop hook | 未测；按两次会打开回退菜单（推测，未验证） |
| Enter | 输入框里的消息**排队**：transcript 记 `queue-operation` `enqueue`，输入框上方显示这条消息，占位提示变成 `Press up to edit queued messages`。工具结束时它被并入当前这一轮（`queue-operation` `remove`，`reason: absorbed_mid_turn`，再有一条 `queued_command` 附件），模型在同一轮里回应 | 发送 |

- 中断时正在运行的 `sleep 30` 界面显示 `Interrupted · What should Claude do instead?`；后台是否继续运行，未测

## 11. 压缩和恢复

- `/compact`：输入框上方显示 `Compacting conversation… (22s)`，完成后显示 `Compacted (ctrl+o to see full summary)`。transcript 依次是 `/compact` 这条用户记录（没有 `origin`）、`compact_boundary`、`isCompactSummary` 的摘要、斜杠命令的记录；hook 依次是 `PreCompact`、`SessionStart`（`source: "compact"`）。这次压缩用了 41 秒，`preTokens` 36057 → `postTokens` 3605
- `/exit` 退出，触发 `SessionEnd`
- `claude --resume <id>`：同一个会话 ID、同一个 transcript，接着追加；`SessionStart` 的 `source` 为 `resume`；屏幕上重新画出之前的对话

## 12. 做实测时的注意事项

- 用独立的 tmux socket，测完 `kill-server`；hook 用 `--settings` 注入，不要改 `~/.claude/settings.json`，测前测后对比它的校验值
- 信任目录会写进 `~/.claude.json`（和 Codex 的信任写进 `config.toml` 一样，是全局状态）
- 测试用 `--model haiku` 省额度；但 haiku 没有 auto 模式和 effort，这两项要用 sonnet 测
- 不要在菜单里按数字键或回车，以免改掉全局默认模型
