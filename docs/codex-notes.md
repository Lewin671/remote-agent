# Codex 实测记录

Codex adapter 依赖的行为都是实测出来的，会随 Codex 版本变化。每条都注明验证时的版本；升级 Codex 后，涉及的条目要重新验证。

最近一次验证：codex-cli **0.157.1**，macOS，tmux 3.7c，2026-09-29。

## 1. 数据从哪读

| 数据 | 来源 | 说明 |
|---|---|---|
| 模型、reasoning effort | rollout 的 `turn_context`：`model`、`effort` | 每轮开始时写一次 |
| 上下文占用 | rollout 的 `event_msg.token_count`：`info.last_token_usage.total_tokens / info.model_context_window` | 窗口大小以这里为准。`models_cache.json` 里写的是 272k，实际会话报的是 760k |
| 累计 token | 同上：`info.total_token_usage` | 含 `cached_input_tokens`、`reasoning_output_tokens` |
| 账户额度 | 同上：`rate_limits.primary`：`used_percent`、`window_minutes`、`resets_at`；另有 `plan_type` | 账号级数据，不属于某个会话 |
| 轮次边界 | rollout 的 `event_msg.task_started` / `task_complete` | |
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
- 在档位列表里直接按数字键会怎样，**没有验证**，可能按全局默认生效，所以只用方向键
- 编号不能写死：模型的顺序和数量随版本、账号变化。先抓屏，按模型名找到编号
- 光标初始停在当前选中项，不在第一项。要从屏幕上读出 `›` 所在的行，再算按几下
- 以屏幕上出现 `for this session only` 为成功；认不出菜单或结果对不上，就按 Esc 退出并报错
- 兜底：退出 Codex，用 `codex resume <id> -m <模型> -c model_reasoning_effort=<档位>` 重新启动

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

  **`transcript_path` 直接给出了 rollout 的路径**，可以用来把会话和 rollout 对应起来。同样的字段很可能也出现在 `SessionStart` hook 里，但尚未验证
- **第一次使用需要信任**：启动时弹出 `Hooks need review`，选项有 `Review hooks`、`Trust all and continue`、`Continue without trusting (hooks won't run)`。信任后，config.toml 里会写入 `[hooks.state."/<session-flags>/config.toml:permission_request:0:0"]`，并记录 `trusted_hash`。只要 hook 的定义不变，以后就不会再问。所以 **hook 命令必须固定**，二进制的路径一变，就得重新信任
- 用了 `-c` 之后，Codex 会提示 `Running without the shared background server`，也就是改为嵌入模式运行，每个会话在自己的进程里。目前没发现功能上的差异
- **和 `auto_review` 的先后顺序：先调用 hook，hook 没给决定时再交给 `auto_review`。** 实测结果：hook 返回 deny，请求直接被拒绝，`auto_review` 不再参与；hook 什么都不输出、正常退出（exit 0）时，`auto_review` 接着处理，并批准了写 home 目录的请求
- `auto_review` 只有批准和拒绝两种结果，**不会转给用户**。被拒绝后，Codex 的系统提示要求模型"说明风险，向用户请求批准"，模型会在对话里直接问你；你在对话里明确授权后，`auto_review` 会把这条授权当作依据（依据提示词推断，**被拒绝的情况尚未实测**）
- 注意：`workspace-write` 沙箱下 `/tmp` 默认可写，写 `/tmp` 不会触发提权。测试提权要写 home 目录之类的位置

## 4. 启动时的弹窗和环境

- 启动时的弹窗依次可能有：`Update available`（选 `2. Skip`）、`Trust this folder?`（按数字只会移动光标，**还要再按回车**才生效）、`Hooks need review`。每一个都会吞掉紧接着发的按键
- `-a untrusted` 在这个版本里已经删除，`-a` 只接受 `on-request`、`never`
- Codex 要通过代理才能联网，而本机是靠 shell 函数 `ai_cli_with_clash_proxy` 设置的代理环境变量（`HTTP_PROXY` 等）。daemon 启动 Codex 时要带上这些变量；尤其是 daemon 由 launchd 启动时，读不到 shell 里的配置

## 5. 踩过的坑

| 现象 | 后果 | 对策 |
|---|---|---|
| `/model gpt-6-sol high` 回车后被当成**普通消息**发给了模型 | 模型开始工作，消耗额度；Codex 还用这条消息自动给会话起了标题 | 客户端不把 `/` 开头的内容直接注入：常用命令做成按钮，其余先提醒 |
| 首次在某个目录启动时弹出“Trust this folder?” | 紧接着发的回车被弹窗接住，等于替用户点了信任 | 注入前先抓屏确认没有弹窗 |
| 刚启动时欢迎框里模型一栏显示 `loading` | 太早操作会读到旧画面 | 轮询屏幕，等到预期画面出现再操作，并设超时；不要用固定的 sleep |
| 抓屏里混有历史对话、“Update available” 提示框、状态栏 | 全屏搜关键词会误判 | 只认屏幕底部的菜单标题和选项区域 |
| 切换模型后，rollout 的 `turn_context` 要到下一轮才会记录新模型（推测，**未验证**） | 客户端显示滞后 | 先按屏幕上的确认结果更新显示，下一轮再用 rollout 校对 |

## 6. 做实测时的注意事项

- 用独立的 tmux socket（`tmux -L <名字>`），不影响平时用的 tmux，测完 `kill-server`
- 实测会改到真实的 `~/.codex/config.toml`，比如目录信任记录。测前记下文件的校验值，测后对比
- 不要在菜单里乱按回车，以免改掉全局默认值
- shell 里不要把 `tmux -L xxx` 存进变量再执行，zsh 不会自动按空格拆分；Go 代码里一律传参数数组
