# Claude Code 实测记录

Claude Code 在 MVP 之后（P3）才接入，本文先存下为了让设计兼容它而顺带测到的结果。写法和 [codex-notes.md](codex-notes.md) 一样：只写 Claude Code 实际怎么表现；remote-agent 据此的设计见 [architecture.md](architecture.md) 第 7 节，还没测的见 [plan.md](plan.md) 的「待确认」。

没有特别标注的条目都是实测结果，推断或没测过的会在句中标出。接入前要按当时的版本重新验证。

最近一次验证：Claude Code **2.1.284**，macOS，2026-09-29。

## 1. 权限模式

当时没有记下复现步骤。

| 模式 | 行为 |
|---|---|
| manual | 每一步需要权限的操作都询问 |
| auto | 由分类器代为判断 |
| plan | 只读和出计划 |
| accept edits | 自动接受文件修改 |
| bypass | 所有操作都不审批；只有启动时允许了才能切到这个模式 |

- 用 Shift+Tab 切换模式，只在空闲时验证过
- `PermissionRequest` hook 可以返回允许或拒绝，拒绝时可以附一句说明

## 2. 模型提问（AskUserQuestion）

- 触发 `PreToolUse` hook，`tool_name` 为 `AskUserQuestion`，`tool_input.questions[]` 里带问题和选项（`label`）
- **hook 可以直接回答**：返回 `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{...原 tool_input,"answers":{"<问题原文>":"<选项 label>"}}}}`，界面不再弹出选择框，直接显示 `User answered Claude's questions: · <问题> → <答案>`，模型按这个答案继续
- 推论（未实测）：hook 要等手机回答才返回的话，本地终端在这段时间里看不到这个问题
- 实测时会话处于 bypass permissions 模式；其他权限模式下是否一样，没有验证
