# Codex 实测记录

Codex adapter 依赖的行为都是实测出来的，会随 Codex 版本变化。本文只记录 Codex 实际怎么表现；remote-agent 据此怎么做见 [architecture.md](architecture.md) 第 6、7 节，还没测的见 [plan.md](plan.md) 的「待确认」。

没有特别标注的条目都是实测结果；推断或没测过的，会在句中标「推测」「未验证」「未测」。升级 Codex 后，涉及的条目要重新验证。

最近一次验证：codex-cli **0.157.1**，macOS，tmux 3.7c，2026-09-29。

## 1. 数据从哪读

| 数据 | 来源 | 说明 |
|---|---|---|
| 模型、reasoning effort | rollout 的 `turn_context`：`model`、`effort` | 每轮开始时写一次 |
| 上下文占用 | rollout 的 `event_msg.token_count`：`info.last_token_usage.total_tokens / info.model_context_window` | 窗口大小以这里为准。`models_cache.json` 里写的是 272k，实际会话报的是 760k |
| 累计 token | 同上：`info.total_token_usage` | 含 `cached_input_tokens`、`reasoning_output_tokens` |
| 账户额度 | 同上：`rate_limits.primary`：`used_percent`、`window_minutes`、`resets_at`；另有 `plan_type` | 账号级数据，不属于某个会话 |
| 轮次边界 | rollout 的 `event_msg.task_started`；结束是 `task_complete`，**被中断时是 `turn_aborted`**（`reason: "interrupted"`） | 两种结束事件都要处理，否则中断后会一直以为还在运行 |
| 消息、工具调用 | rollout 的 `response_item`：`message`、`reasoning`、`custom_tool_call`、`custom_tool_call_output` | |
| 可选模型和档位 | `~/.codex/models_cache.json`：`slug`、`display_name`、`supported_reasoning_levels`、`default_reasoning_level` | 每个模型支持的档位不同 |
| 输入是否已提交 | `~/.codex/history.jsonl`：每次成功提交追加一行 | rollout 文件要到第一次提交后才会创建，不能用它判断 |

rollout 路径：`~/.codex/sessions/YYYY/MM/DD/rollout-<时间>-<session id>.jsonl`。

## 2. 切换模型和 reasoning effort

`/model` 不支持带参数，只能操作菜单。只在会话空闲时执行。

1. 输入 `/model`，回车 → 出现 `Select Model and Effort`，模型带编号，当前模型标 `(current)`
2. 按模型对应的编号 → **直接选中**，进入 `Select Reasoning Level for <模型>`
3. 档位列表：`Low`、`Medium`、`High`、`Extra high`，第 5 项 `More reasoning…` 是子菜单，里面是 `Max`、`Ultra`（有些模型没有 Ultra）
4. 用方向键移到目标档位，按 **`s`** → 屏幕出现 `Model changed to <模型> <档位> for this session only`

要点：
- **不能按回车确认档位**：底部提示是 `enter default · s session`，回车会把选择写进 `~/.codex/config.toml`，变成全局默认值；`s` 只改当前会话。实测按 `s` 后 `config.toml` 没有变化
- 在档位列表里直接按数字键会怎样，**没有验证**，推测会按全局默认生效
- 模型的顺序和数量随版本、账号变化，编号不是固定的
- 光标初始停在当前选中项，不在第一项，`›` 标出光标所在的行
- `codex resume <id> -m <模型> -c model_reasoning_effort=<档位>` 可以用新的模型和档位恢复会话

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
- **第一次使用需要信任**：启动时弹出 `Hooks need review`，选项有 `Review hooks`、`Trust all and continue`、`Continue without trusting (hooks won't run)`。信任后，config.toml 里会写入 `[hooks.state."/<session-flags>/config.toml:permission_request:0:0"]`，并记录 `trusted_hash`。只要 hook 的定义不变，以后就不会再问；**hook 命令一变（比如二进制的路径变了），就得重新信任**
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

- `/permissions` 的选择**只对当前会话生效**：实测切换后 `config.toml` 没有变化
- 三档和配置项的对应关系：`Ask for approval` 实测为 `on-request` + `workspace-write` + reviewer `user`；`Approve for me` 对应 `approvals_reviewer = "auto_review"`（本机默认值）；`Full Access` 未实测
- 原生审批框的选项：`1. Yes, proceed (y)`、`2. Yes, and don't ask again for commands that start with <前缀> (p)`、`3. No, and tell Codex what to do differently (esc)`。hook 只能返回 allow 或 deny 加 message，做不到第 2 项
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
| 切换模型后，rollout 的 `turn_context` 要到下一轮才会记录新模型（推测，**未验证**） | 只看 rollout 的话，显示会滞后一轮 |

对应的做法见 [architecture.md](architecture.md) 第 5、6 节。

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
- 输入框为空时按一次 Ctrl-C 就退出了 Codex（输入框有内容时，第一次是清空）

