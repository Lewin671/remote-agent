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

## 3. 踩过的坑

| 现象 | 后果 | 对策 |
|---|---|---|
| `/model gpt-6-sol high` 回车后被当成**普通消息**发给了模型 | 模型开始工作，消耗额度；Codex 还用这条消息自动给会话起了标题 | 客户端不把 `/` 开头的内容直接注入：常用命令做成按钮，其余先提醒 |
| 首次在某个目录启动时弹出“Trust this folder?” | 紧接着发的回车被弹窗接住，等于替用户点了信任 | 注入前先抓屏确认没有弹窗 |
| 刚启动时欢迎框里模型一栏显示 `loading` | 太早操作会读到旧画面 | 轮询屏幕，等到预期画面出现再操作，并设超时；不要用固定的 sleep |
| 抓屏里混有历史对话、“Update available” 提示框、状态栏 | 全屏搜关键词会误判 | 只认屏幕底部的菜单标题和选项区域 |
| 切换模型后，rollout 的 `turn_context` 要到下一轮才会记录新模型（推测，**未验证**） | 客户端显示滞后 | 先按屏幕上的确认结果更新显示，下一轮再用 rollout 校对 |

## 4. 做实测时的注意事项

- 用独立的 tmux socket（`tmux -L <名字>`），不影响平时用的 tmux，测完 `kill-server`
- 实测会改到真实的 `~/.codex/config.toml`，比如目录信任记录。测前记下文件的校验值，测后对比
- 不要在菜单里乱按回车，以免改掉全局默认值
- shell 里不要把 `tmux -L xxx` 存进变量再执行，zsh 不会自动按空格拆分；Go 代码里一律传参数数组
