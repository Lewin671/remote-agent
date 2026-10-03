# Codex 实测记录

Codex adapter 依赖的行为都是实测出来的，会随 Codex 版本变化。本文只记录 Codex 实际怎么表现；remote-agent 据此怎么做见 [architecture.md](architecture.md) 第 6、7 节，还没测的见 [plan.md](plan.md) 的「待确认」。

没有特别标注的条目都是实测结果；推断或没测过的，会在句中标「推测」「未验证」「未测」。升级 Codex 后，涉及的条目要重新验证。

最近一次验证：codex-cli **0.159.0**（`remote-agent doctor` 全部通过），macOS，tmux 3.7c，2026-09-29。0.157.1 的结论同样成立，下面有差异的地方单独注明。

## 1. 数据从哪读

| 数据 | 来源 | 说明 |
|---|---|---|
| 模型、reasoning effort | rollout 的 `turn_context`：`model`、`effort` | 每轮开始时写一次 |
| 上下文占用 | rollout 的 `event_msg.token_count`：`info.last_token_usage.total_tokens / info.model_context_window` | 窗口大小以这里为准。`models_cache.json` 里写的是 272k，实际会话报的是 760k |
| 累计 token | 同上：`info.total_token_usage` | 含 `cached_input_tokens`、`reasoning_output_tokens` |
| 账户额度 | 同上：`rate_limits.primary`：`used_percent`、`window_minutes`、`resets_at`；另有 `plan_type` | 账号级数据，不属于某个会话。不是每个套餐都有两个窗口：0.159.2、`prolite` 套餐下客户端只显示出一个 7 天窗口（2026-09-30，由客户端显示推断，未直接核对 rollout 字段），所以客户端只显示上报了的窗口 |
| 轮次边界 | rollout 的 `event_msg.task_started`；结束是 `task_complete`，**被中断时是 `turn_aborted`**（`reason: "interrupted"`） | 两种结束事件都要处理，否则中断后会一直以为还在运行 |
| 消息、工具调用 | rollout 的 `response_item`：`message`、`reasoning`、`custom_tool_call`、`custom_tool_call_output` | |
| 可选模型和档位 | `~/.codex/models_cache.json`：`slug`、`display_name`、`supported_reasoning_levels`、`default_reasoning_level` | 每个模型支持的档位不同 |
| 输入是否已提交 | `~/.codex/history.jsonl`：每次成功提交追加一行 | rollout 文件要到第一次提交后才会创建，不能用它判断 |

rollout 路径：`~/.codex/sessions/YYYY/MM/DD/rollout-<时间>-<session id>.jsonl`。

**图片**：会话里出现的图片都以 base64 data URL 内嵌在 rollout 里，不是文件引用。按本机 0.105 到 0.159 的 rollout 统计（2026-09-29，只读历史记录，没有专门复现）：

| 来源 | 记录 | 说明 |
|---|---|---|
| 用户粘贴或附带的图 | `response_item.message`（`role: user`）的 `content` 里有 `input_image`，`image_url` 为 `data:image/png;base64,…` 或 `data:image/jpeg;base64,…` | 单张最大约 2.9 MB |
| 模型查看的图 | `function_call_output` 的 `output` 是数组，里面有 `input_image`（部分带 `detail`）；按 `call_id` 对应到 `function_call` | 来自 `view_image`（参数里有 `path`）、`js`（浏览器截图）、`wait`、MCP 截图工具等。单张最大约 2.2 MB，看起来是原图，没有缩小 |
| 模型生成的图 | `response_item.image_generation_call`：`id`、`status`、`revised_prompt`、`result`（PNG 的 base64，不带 data URL 前缀） | 只在 0.142 系列见到。实测 `status` 写的是 `generating` 但 `result` 已经完整；单张约 1.2 到 2.5 MB。同一张图另存在 `~/.codex/generated_images/<session id>/` 下 |

同一个文件被 `view_image` 看几次，rollout 里就存几份。

## 2. 切换模型和 reasoning effort

`/model` 不支持带参数，只能操作菜单。只在会话空闲时执行。

1. 输入 `/model`，回车 → 出现 `Select Model and Effort`，模型带编号，当前模型标 `(current)`，默认模型标 `(default)`。列出的是 `models_cache.json` 里 `visibility` 为 `list` 的模型，按 `priority` 排序
2. 按模型对应的编号 → **直接选中**，进入 `Select Reasoning Level for <模型>`
3. 档位列表：`Low`、`Medium`、`High`、`Extra high`，第 5 项 `More reasoning…` 是子菜单，里面是 `Max`、`Ultra`（有些模型没有 Ultra）
4. 用方向键移到目标档位，按 **`s`** → 屏幕出现 `Model changed to <模型> <档位> for this session only`

要点：
- **不能按回车确认档位**：底部提示是 `enter default · s session`，回车会把选择写进 `~/.codex/config.toml`，变成全局默认值；`s` 只改当前会话。实测按 `s` 后 `config.toml` 没有变化
- 在档位列表里直接按数字键会怎样，**没有验证**，推测会按全局默认生效
- 模型的顺序和数量随版本、账号变化，编号不是固定的
- 光标初始停在当前选中项，不在第一项，`›` 标出光标所在的行
- `codex resume <id> -m <模型> -c model_reasoning_effort=<档位>` 可以用新的模型和档位恢复会话。同一个选项不能给两次：`-m` 重复时报 `the argument '--model <MODEL>' cannot be used multiple times` 并退出（0.159.0）；`-c` 可以重复
- 按 `s` 以后，底部状态栏立刻变成新的模型和档位（如 `GPT-6-Luna high`）；rollout 要到下一轮开头才有 `thread_settings_applied` 和新的 `turn_context`（0.157.1 实测）

## 3. 审批 hook（PermissionRequest）

**hook 可以只对单个会话生效**：启动时用 `-c` 传入即可，不需要写全局的 `~/.codex/hooks.json`。实测命令：

```
codex -c 'hooks.PermissionRequest=[{matcher="*",hooks=[{type="command",command="<hook 命令>"}]}]'
```

- 不带这个参数直接运行的 `codex` **不会触发**这个 hook（实测：普通会话里同样发生了提权，由 `auto_review` 处理，hook 没有被调用）
- hook 通过 stdin 收到 JSON，通过 stdout 返回决定：`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"|"deny","message":"…"}}}`。返回 deny 后，界面显示 `Blocked by hook`，模型会看到 message
- 收到的 payload（已脱敏）：

  ```json
  {
    "session_id": "01a0ed1c-…",
    "turn_id": "01a0ed1d-…",
    "transcript_path": "~/.codex/sessions/2026/09/29/rollout-…-01a0ed1c-….jsonl",
    "cwd": "/private/tmp/ra-hooktest/work",
    "hook_event_name": "PermissionRequest",
    "model": "gpt-6-sol",
    "permission_mode": "default",
    "tool_name": "Bash",
    "tool_input": { "command": "printf 'hi' > hello.txt", "description": "May I create …?" }
  }
  ```

  **`transcript_path` 直接给出了 rollout 的路径**，可以用来把会话和 rollout 对应起来，见下面的 `SessionStart`
- **第一次使用需要信任**：启动时弹出 `Hooks need review`，选项有 `Review hooks`、`Trust all and continue`、`Continue without trusting (hooks won't run)`。信任后，config.toml 里会写入 `[hooks.state."/<session-flags>/config.toml:permission_request:0:0"]`，并记录 `trusted_hash`。只要 hook 的定义不变，以后就不会再问；**hook 命令一变（比如二进制的路径变了），就得重新信任**。每个位置（如 `permission_request:0:0`）只记一个 `trusted_hash`：两份路径不同的二进制交替启动会话，会互相顶掉信任，每次都问。命令写成 `"$RA_EXE" hook codex`、路径放进环境变量，hook 照常运行（Codex 用 shell 执行 hook 命令），换路径后 hash 不变（0.159.0 实测）
- **`SessionStart` hook 可以把会话和 rollout 对应起来**：同样用 `-c 'hooks.SessionStart=[{hooks=[{type="command",command="…"}]}]'` 传入。实测它不是在启动时触发，而是在**发出第一条消息时**触发，这时 rollout 文件已经创建好了。payload 为 `{"session_id","transcript_path","cwd","hook_event_name":"SessionStart","model","permission_mode","source":"startup"}`
- **hook 进程会继承 Codex 的环境变量**。实测：启动 Codex 时设置 `RA_SESSION`，`SessionStart` hook 能读到它，连同 `transcript_path` 一起上报；同一目录同时开两个会话，两边各自对应正确
- 信任记录里的 hash 只和 hook 的定义有关（命令字符串等），**和脚本内容无关**：改了脚本内容，没有要求重新信任
- 用了 `-c` 之后，Codex 会提示 `Running without the shared background server`，也就是改为嵌入模式运行，每个会话在自己的进程里。目前没发现功能上的差异
- **和 `auto_review` 的先后顺序：先调用 hook，hook 没给决定时再交给 `auto_review`。** 实测结果：hook 返回 deny，请求直接被拒绝，`auto_review` 不再参与；hook 什么都不输出、正常退出（exit 0）时，`auto_review` 接着处理，并批准了写 home 目录的请求
- `auto_review` 只有批准和拒绝两种结果，**不会转给用户**。被拒绝后，Codex 的系统提示要求模型"说明风险，向用户请求批准"，模型会在对话里直接问你；你在对话里明确授权后，`auto_review` 会把这条授权当作依据
- 实测完整流程：README 里写着"删除 home 下某个目录"，让模型按 README 初始化。模型**自己没有执行删除**，而是在对话里问"是否确认删除"；回复确认后，模型发起提权，hook 被调用且不表态，`auto_review` 批准，删除成功。也就是说，拿不准的操作会以普通对话的形式出现。`auto_review` 直接拒绝的情况没有复现出来
- **hook 超时**：hook 定义可以带 `timeout`（秒）。实测 `timeout=5`、hook 睡 15 秒：界面显示 `Hook failed` / `hook timed out after 5s`，随后**弹出原生审批框**，并没有直接拒绝。`timeout=99999` 时 hook 睡 130 秒，Codex 一直等到它返回；二进制里有 `clamping … hook timeout to …` 的字样，说明存在上限，具体数值未测。改 `timeout` 也算改了 hook 定义，要重新信任
- 在原生审批框里按 Esc：取消这次请求，并中断这一轮
- 注意：`workspace-write` 沙箱下 `/tmp` 默认可写，写 `/tmp` 不会触发提权。测试提权要写 home 目录之类的位置

## 4. 审批模式

Codex 的「审批模式」其实是两个互相独立的维度：

| 维度 | 取值 | 怎么切 | 从哪读 |
|---|---|---|---|
| 权限 | `/permissions` 菜单里的 3 档：`Ask for approval`、`Approve for me`、`Full Access` | 空闲时输入 `/permissions`，回车，方向键选，回车；屏幕出现 `Permission selection requested: <档位>` | rollout 的 `turn_context`：`approval_policy`、`sandbox_policy`、`approvals_reviewer`（`user` 或 `auto_review`）；`/status` 的 `Permissions` 一行 |
| 协作模式 | `Default`、`Plan` | Shift+Tab 开关 | `turn_context.collaboration_mode.mode`；屏幕右下角出现 `Plan mode` |

**计划模式怎么结束**：模型给出计划后，rollout 里先有一条 `item_completed`，`item.type` 为 `Plan`、`item.text` 是计划正文，再有一条正文包在 `<proposed_plan>` 里的助手消息，然后这一轮以 `task_complete` 正常结束。同时屏幕上弹出 `Implement this plan?`：`1. Yes, implement this plan`（切回 Default 并开始执行）、`2. Yes, clear context and implement`、`3. No, stay in Plan mode`，数字键直接选。选 1 后 Codex 自己发起新的一轮，用户消息是 `Implement the plan.`，`turn_context` 的 `collaboration_mode` 变回 `default`

- `/permissions` 的选择**只对当前会话生效**：实测切换后 `config.toml` 没有变化
- 三档和配置项的对应关系：`Ask for approval` 实测为 `on-request` + `workspace-write` + reviewer `user`；`Approve for me` 是 `on-request` + `workspace-write` + reviewer `auto_review`（本机默认值）；`Full Access` 是 `never` + `danger-full-access` + reviewer `user`
- 菜单标题是 `Update Model Permissions`，当前档位标 `(current)`。**按数字键直接选中**，不用方向键加回车。选 `Full Access` 会再弹一个 `Enable full access?`，`1. Yes, continue anyway` 才生效，`2. Cancel` 返回
- **一轮运行中也能用** `/permissions`：输入框在运行中照样可以输入斜杠命令并回车打开菜单。选中后屏幕立刻出现 `Permission selection requested`，但这一轮的 `turn_context` 不变，下一轮才按新档位记录；下一轮开头还会有一条 `event_msg.thread_settings_applied`
- 原生审批框的选项：`1. Yes, proceed (y)`、`2. Yes, and don't ask again for commands that start with <前缀> (p)`、`3. No, and tell Codex what to do differently (esc)`。hook 只能返回 allow 或 deny 加 message，做不到第 2 项
- **hook 不表态时的原生审批框**（0.159.0，2026-09-30 实测，`approvals_reviewer="user"`）：`PermissionRequest` hook 不输出、正常退出后，Codex 立即弹出原生审批框；按 `2` 选中「以后不再询问」，界面显示 `You approved codex to always run commands that start with <前缀>`，命令随即执行。实测 `touch ~/.ra-v8-always-1` 的前缀是整条命令。0.159.2（2026-09-30 实测）：提权命令在后台等审批，这一轮可以先结束（模型轮询后回复「仍在等待审批」，工具记为 `running`、后台），hook 仍在等待；所以不能在一轮结束时就把 Codex 的审批当成已在终端处理。同一天用 0.159.2 还三次看到命令执行完后界面一直停在 `Working`（超过 3 分钟，没有新输出），按 Esc 中断后恢复；原因未查（推测和接口或周额度有关）
- 在 `workspace-write` 下联网命令先在沙箱里失败，模型要再申请提权才会弹审批，不一定每次都会申请
- `Approve for me` 和 hook 的配合见第 3 节：hook 不表态时才交给 `auto_review`

## 5. 启动时的弹窗和环境

- 启动时的弹窗依次可能有：`Update available`（选 `2. Skip`）、`Trust this folder?`（按数字只会移动光标，**还要再按回车**才生效）、`Hooks need review`。每一个都会吞掉紧接着发的按键
- 能在启动时用 `-c` 关掉的：`check_for_update_on_startup=false`（`Update available`）；`[notice]` 下的开关，如 `hide_rate_limit_model_nudge`（快到限额时的 `Switch to a cheaper model?`）、`hide_full_access_warning`、`hide_world_writable_warning`、`hide_gpt5_1_migration_prompt`。键名取自二进制；本机 `config.toml` 里的 `hide_rate_limit_model_nudge = true` 是 TUI 自己写入的，说明这个键有效。其余几个没有逐个触发验证
- `Trust this folder?` **不能**用 `-c 'projects."<路径>".trust_level="trusted"'` 跳过（`/tmp` 和 `/private/tmp` 两种写法都试过），信任只从 `config.toml` 文件读
- `-a untrusted` 在这个版本里已经删除，`-a` 只接受 `on-request`、`never`
- Codex 要通过代理才能联网，而本机是靠 shell 函数 `ai_cli_with_clash_proxy` 设置的代理环境变量（`HTTP_PROXY` 等）。不经过这个 shell 函数启动的 Codex（比如由 launchd 拉起的进程启动）拿不到这些变量

## 6. 踩过的坑

| 现象 | 后果 |
|---|---|
| `/model gpt-6-sol high` 回车后被当成**普通消息**发给了模型 | 模型开始工作，消耗额度；Codex 还用这条消息自动给会话起了标题 |
| 首次在某个目录启动时弹出“Trust this folder?” | 紧接着发的回车被弹窗接住，等于替用户点了信任 |
| 刚启动时欢迎框里模型一栏显示 `loading` | 太早操作会读到旧画面 |
| 抓屏里混有历史对话、“Update available” 提示框、状态栏 | 全屏搜关键词会误判 |
| 跳过更新提示后，“Update available” 的文字框还留在屏幕上方 | 按全屏匹配的等待脚本以为弹窗还在，连续输入了 8 个 `2`，拼进了下一条消息 |
| 切换模型后，rollout 的 `turn_context` 要到下一轮才会记录新模型（0.157.1 实测，见第 2 节） | 只看 rollout 的话，显示会滞后一轮 |

对应的做法见 [architecture.md](architecture.md) 第 5、6 节。

- **`exec` 和 `wait` 的开始**（0.159.0，2026-09-30）：没跑命令的 `exec` 只有输出、没有描述条目，`wait` 等的是之前让出的 exec 单元；早先的 daemon 把它们报成没有结束的工具开始。现在按 `call_id` 配对，做法见 [architecture §6](architecture.md#6-接入-codex)

## 7. 做实测时的注意事项

- 用独立的 tmux socket（`tmux -L <名字>`），不影响平时用的 tmux，测完 `kill-server`
- 实测会改到真实的 `~/.codex/config.toml`，比如目录信任记录。测前记下文件的校验值，测后对比
- 不要在菜单里乱按回车，以免改掉全局默认值
- shell 里不要把 `tmux -L xxx` 存进变量再执行，zsh 不会自动按空格拆分

## 8. 中断和运行中的输入

| 按键 | 运行中 | 空闲时 |
|---|---|---|
| Esc | **中断当前这一轮**，屏幕出现 `Conversation interrupted`；输入框里的草稿**保留** | 输入框为空时，进入"编辑上一条消息"（界面提示） |
| Ctrl-C | 未测 | 第一次**清空输入框**；输入框为空时再按，**直接退出 Codex** |
| Enter | 输入框里的内容**立即发送**，插入到正在进行的这一轮 | 发送 |
| Tab | 把输入框里的内容**排队**，等这一轮结束再发（界面提示 `tab to queue message`） | 未测 |

- 中断只打断这一轮对话，**已经启动的后台命令还会继续运行**（实测 `sleep 60` 在中断后仍在跑，界面显示 `1 background terminal running`，要用 `/stop` 关掉）
- 中断后 rollout 记录的是 `turn_aborted`，不是 `task_complete`
- 不带 `-c` 启动时，Codex 用共享的后台服务运行，退出界面后这一轮**还在继续**（退出时提示 `Stop the current turn: run codex agents…`）。用 `-c` 启动时走嵌入模式，没有这个问题

## 9. 模型提问（request_user_input）

模型有两个向用户提问的工具，都会触发 `PreToolUse` / `PostToolUse` hook，`tool_name` 分别是 `request_user_input`、`request_user_input_async`。在 Plan 模式下实测。

| | `request_user_input` | `request_user_input_async` |
|---|---|---|
| 内容 | 1 个或多个问题，每个带 `header`、`id`、`question`、`options`（`label` + `description`） | 只有自由文本问题：`questions[].title` |
| 模型 | **停下来等**回答 | 不等，继续干活；工具立即返回 `{"accepted":true}` |
| 界面 | 输入框位置变成选择框，末尾自动加一项 `None of the above`（Tab 加备注） | 输入框上方显示 `? 1 question`，按 `shift+←` 进入回答框，`ctrl+]` 跳过 |
| 回答 | 按选项编号**直接提交**，不用再按回车（单个问题） | 在回答框里输入，回车 |

- 问题：`PreToolUse` hook 的 `tool_input` 和 rollout 里 `function_call` 的 `arguments`（JSON 字符串）内容相同；hook 的 `tool_use_id` 就是 rollout 的 `call_id`
- 答案：rollout 的 `function_call_output`，`output` 为 `{"answers":{"<id>":{"answers":["Python"]}}}`，另有一条 `retained_context`（`type: verified_answer`）；`PostToolUse` 的 `tool_response` 是同一个字符串
- `PreToolUse` 的输出字段只有 `permissionDecision`、`permissionDecisionReason`、`updatedInput`、`additionalContext`（取自二进制里的 schema）。`updatedInput` 只能改问题，没有答案字段，**hook 没法代替用户回答**
- **问题在等待回答时就写进了 rollout**：实测提问界面还显示着的时候，rollout 里已经有这次的 `function_call`，还没有 `function_call_output`
- `PreToolUse` 的 `matcher="request_user_input"` 能匹配到 `request_user_input`；是否也匹配 `request_user_input_async`，未测
- 异步提问：实测模型问完很快就结束了这一轮，回答框随之消失，之后输入的文字落进主输入框，回车后作为普通消息发出，模型能接上

## 10. 屏幕和输入

用 `capture-pane -p -e` 抓屏（保留颜色属性）时：

| 画面 | 样子 |
|---|---|
| 输入框提示符 | `\e[1m›\e[0m`（加粗） |
| 空输入框里的占位提示（如 `Ask Codex to do anything`） | 提示符后跟淡色字 `\e[2m…`；终端报告了背景色时，中间先有一段背景色，见下文 |
| 输入框里有草稿 | 提示符后跟普通字 |
| 对话里之前的用户消息 | `\e[1;2m› `（加粗加淡） |
| 列表里选中的行（信任目录、hook 确认、审批框、提问） | `\e[1;7m› 1. …`（加粗反色） |

- bracketed paste 的正文和回车一次写入，Codex 会正常提交，不需要在中间等待；多行正文的换行会保留
- 本地终端和远程注入同时打字的实测：远程注入时本地正好连续敲 10 个字符，远程消息原样提交，10 个字符完整出现在新的输入框里
- 一轮运行中，空输入框同样显示淡色的占位提示
- 从能报告背景色的真彩色终端（如 Ghostty）attach 过之后，Codex 会给输入框画背景色：提示符后面先是 `\e[48;2;57;57;57m` 和一个空格，然后才是 `\e[2m` 占位提示，草稿文字前面也一样。没有 attach 过的会话没有这段背景色（codex-cli 0.157.1，2026-09-29 实测）
- 0.159.0 起，输入框提示符写成 `\e[0;1m›`（先重置再加粗），不再是 `\e[1m›`；样式不变，写法变了。之前的用户消息仍是加粗加淡、选中行仍是加粗反色
- 0.159.0 在新目录里启动时，先画出输入框，随后才弹出 `Trust this folder?`，首次启动还会在上方画一个大的点阵 logo；状态栏末尾可能多一个转圈字符
- 输入框为空时按一次 Ctrl-C 就退出了 Codex（输入框有内容时，第一次是清空）
- 粘贴图片路径（0.159.0，2026-09-29 实测，用 `tmux send-keys -H` 写入 bracketed paste）：一次粘贴的内容只有一个图片文件的绝对路径时，输入框里变成 `[Image #N]`；路径后面带文字、或者路径里有空格时不转换。图片在**提交时**才读：粘贴后删掉文件再提交，rollout 里记的是 `Codex could not read the local image at …: No such file or directory`，模型看不到这张图。能读到时，rollout 的用户消息依次是 `<image name=[Image #1] path="…">`、`input_image`、`</image>`，最后是正文（带 `[Image #1]` 占位）。几张图片路径、文字和回车一次写入，Codex 照常提交，图片和文字的顺序不变。大图（同日实测）：4000×3000 的 PNG 被缩到 1824×1368 再内嵌；7.7 MB 的 1600×1600 噪点 PNG 原样内嵌，rollout 因此多出 10 MB 左右的 base64。两张模型都能看到
- 逐张粘贴多张图（0.159.0，GPT-6-Luna low，2026-09-29 实测）：每粘贴一次路径，输入框里**立即**多一个 `[Image #N]`（6 张 4 MB 的噪点 PNG 连着粘贴，50ms 后已经全部出现），占位之间 Codex 自己加一个空格；占位是青色（`\e[38;5;6m`），和正文颜色不同。编号在每个输入框里从 1 开始：清空输入框后再粘贴，又从 `[Image #1]` 起
- 认不出的图：内容不是图片的 `.png`、不存在的路径、没有扩展名的文件、HEIC 都不转换，路径按普通文字进入输入框，紧接在前一个占位后面。PNG、JPEG、GIF 都能转换
- 清空输入框：**Ctrl-U**（`\x15`）一次删掉整行，包括占位；删掉的图不会随后续消息提交（清空后粘贴一张绿图再提交，rollout 里只有这一张）。空输入框按 Ctrl-U 什么也不发生，不像 Ctrl-C 会退出。只清当前这一行：多行草稿要另想办法
- 运行中粘贴两张图、正文再回车：出现在 `Messages to be submitted after next tool call` 下面，`history.jsonl` 在回车时就记下 `[Image #1] [Image #2] 正文`，rollout 的用户消息要等下一次工具调用之后（实测晚 22 秒）才写入
- rollout 里的用户消息分两处：先是 `response_item` 的 `message`（`role: user`），依次是 `<image name=[Image #1] path="…">`、`input_image`（data URL）、`</image>` 和正文；随后的 `event_msg` `item_completed` 里 `UserMessage` 只有 `local_image`（`path`）和正文，正文带 `text_elements`，每个占位的 `byte_range` 和 `placeholder` 都写明了。图片的字节只在前一条里
- `view_image`：模型先写 `custom_tool_call`（`exec`，脚本里调用 `tools.view_image`），然后是 `item_completed` 的 `ImageView`（只有 `path`），**最后**才是 `custom_tool_call_output`，其中的 `output` 数组带 `input_image`。三者之间没有共同的 ID。0.116.0 的历史记录是 `function_call`（`view_image`）后跟 `function_call_output`，没有 `ImageView`
- 经 remote-agent 发图的实测（0.159.0，GPT-6-Luna low，2026-09-30）：3000×2000、3.8 MB 的 JPEG 在 rollout 里被缩成 1920×1280、约 850 KB 的 JPEG；5200×3900、11.6 MB 的 JPEG 缩成 1824×1368；1200×1200、4.3 MB 的噪点 PNG 和 1823×1367、9.8 MB 的 PNG 原样内嵌，两张放在一条消息里，rollout 的这一行有 18.9 MB。上传前去掉的 EXIF（含 GPS）在 rollout 里找不到。模型都读出了图里的字
- 新会话刚进入空闲时，有一次（隔离的 daemon 启动后的第一个 Codex 会话，启动后约 4 秒）粘贴的图片路径没有变成 `[Image #N]` 而是以文字留在输入框里；随后同一会话和新建的三个会话都正常，没能复现。daemon 因此让图片在输入框出现 3 秒后再粘贴
- 在终端里用 `tmux paste-buffer -p` 粘贴图片路径同样变成 `[Image #N]`；用 `tmux send-keys -H` 发 bracketed paste 的字节时，Codex 跑在 PTY 代理下面收不到这段粘贴（直接在 tmux 里跑 Codex 时可以），所以实测要么用 `paste-buffer -p`，要么经 PTY 代理的控制接口写入
- 生成图（0.142.5 的历史记录）：同一张图记两次，先是 `item_completed` 的 `Extension`（`kind: image_gen.generation`，`result` 为 PNG 的 base64，另有 `revisedPrompt`、`savedPath`），再是 `response_item` 的 `image_generation_call`，两者 `id` 相同，`status` 都是 `generating`



## 定时任务 MCP 接入实测

2026-09-30，Codex CLI 0.159.2、GPT-6-Luna low，使用独立的状态目录、tmux socket 和临时工作目录实测：单次 `-c mcp_servers.remote_agent=...` 注册 stdio MCP；没有注入会话指令。用户消息要求安排“本轮结束后 2 秒”，模型调用 `schedule_create`，daemon 在本轮结束后发出后续消息，同一对话记录了回复。

另以本地 schedule 命令创建一次性恢复任务，退出 CLI 并重启 daemon，到时间自动恢复，CLI 对话 ID 不变、消息收到回复；恢复后模型再次调用 `schedule_list` 成功。以固定周期和一次运行上限新开对话，也收到回复且只执行一次。以上直接验证工具调用、调度、恢复和新建，不代表完整 doctor 验收。MCP 的单次配置方式与配置项见 [官方文档](https://learn.chatgpt.com/docs/extend/mcp)。
