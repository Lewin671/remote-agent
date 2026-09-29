# Claude Code 实测记录

记录 Claude Code 的实际行为，写法和 [codex-notes.md](codex-notes.md) 一样：只写带版本号的事实，据此的做法写在 [scope.md](scope.md) 或 [architecture.md](architecture.md)。Claude Code 在 MVP 之后才接入（见 scope.md），这里先存下顺带测到的结果。

最近一次验证：Claude Code **2.1.284**，macOS，2026-09-29。

## 1. 模型提问（AskUserQuestion）

- 触发 `PreToolUse` hook，`tool_name` 为 `AskUserQuestion`，`tool_input.questions[]` 里带问题和选项（`label`）
- **hook 可以直接回答**：返回 `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{...原 tool_input,"answers":{"<问题原文>":"<选项 label>"}}}}`，界面不再弹出选择框，直接显示 `User answered Claude's questions: · <问题> → <答案>`，模型按这个答案继续
- 推论（未实测）：hook 要等手机回答才返回的话，本地终端在这段时间里看不到这个问题
- 实测时会话处于 bypass permissions 模式；其他权限模式下是否一样，没有验证
