# Local transport acceptance

This is an explicit, quota-consuming acceptance driver for real Claude Code and
Codex. It is not run by `go test`. Never point it at normal Remote Agent state,
an existing tmux socket, or a remote relay. Antigravity is not started.

Set a new task-specific `RA_DEV_DIR` below `/tmp`, explicitly set and verify
`REMOTE_AGENT_HOME=$RA_DEV_DIR/bootstrap`, then run `prepare.py --copy-auth`.
The optional flag copies only native authentication/model cache and minimal
settings. It does not copy sessions, project rules or MCP configuration. The
native CLIs use separate `CODEX_HOME`, `CLAUDE_CONFIG_DIR`, `HOME`, and login-shell
configuration. Native startup trust/onboarding is debug-triggered; tool
approvals, questions and plan decisions are encrypted commands.

`prepare.py` builds this checkout's real server and daemon. An acceptance-only
Go build overlay wraps the real relay backend's Send/SetModel/ResolveApproval
callbacks with a file barrier and execution counter. Embedding the concrete
backend preserves optional owner snapshots and disk notifications. No fake CLI,
SDK execution, product API or wire change is introduced. Default product builds
have no observer. The barrier is after durable admission and before entering the
real local handler; the test does not claim to cut between particular native
keystrokes. The controller drops only its proxy's main WebSocket, waits for a
replacement authenticated Ready, retries the original envelope while held,
releases the original callback, and checks the cached result and execution count.

Run `bin/controller` with the same explicit bootstrap environment. If needed,
provide absolute `RA_REAL_CLAUDE` / `RA_REAL_CODEX` executables and the local model
proxy environment. The controller validates/prints isolated `REMOTE_AGENT_HOME`
before every test binary invocation, starts a local server on an automatic port,
creates an independent account and tmux socket, and writes a bearer-protected
loopback test-control URL under the task root. Logs and results remain private.

`drive.py claude` / `drive.py codex` create and send a first message. `matrix.py
<cli> <stage>` runs `models`, `busy`, `approvals`, `questions`, `plans`, `cuts`, and
`files-lifecycle`. Plan triggering is reported separately when unavailable.
For the busy proof the native shell writes a PID marker before blocking; the
question/approval observations come from the signed live owner snapshot, not
terminal text. Native tool events without turn IDs use the sequence window
anchored by the exact authenticated input request ID.

Interrupt the controller to stop only its daemon/server and isolated tmux server.
Extract sanitized operation/effect counts from `results.jsonl` and
`checks.jsonl`, then remove the entire task directory including copied native
credentials, raw logs, binaries and observer files. Do not retain credentials,
full native screens or decrypted events in a public report.
