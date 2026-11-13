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

Interrupt the controller to stop only its recorded daemon/server and isolated tmux PIDs.
Extract sanitized operation/effect counts from `results.jsonl` and
`checks.jsonl`, then remove the entire task directory including copied native
credentials, raw logs, binaries and observer files. Do not retain credentials,
full native screens or decrypted events in a public report.


For a generation-3 migration exercise, extract `v0.73.2` into a separate `/tmp`
checkout and pass its path as `prepare.py --v3-source /tmp/BASELINE --copy-auth`.
This builds the original daemon and a fixture initializer using the original
storage implementation. Start the controller with
`RA_TRANSPORT_V3_BINARY=$RA_DEV_DIR/bin/remote-agent-v3`. It enrolls a fresh
machine in the local relay and initializes it with that original implementation.
Run create/send on both CLIs, then POST `/stop-daemon` through the guarded
controller. After all original signed deadlines have elapsed (the v3 phase uses
30 seconds), POST `/upgrade`: it runs the manual tool in dry-run and real modes,
starts the current daemon, and verifies that the last original mutation's
unchanged envelope returns unknown. The same in-memory client, credentials and
sessions are retained. New `/rpc` calls then verify continued operation without
pairing. These endpoints exist only in this explicit test controller.

Use absolute `RA_REAL_CLAUDE` and `RA_REAL_CODEX` paths when installed launch
wrappers resolve binaries relative to HOME. The native HOME is deliberately
isolated. Initial native trust/onboarding can require `drive.onboard`; this is
separate from tool approvals, which must use signed `resolveApproval` commands.

Protocol-15 acceptance can use the actual Web `Remote` RPC path. After starting
this controller, run `RA_TRANSPORT_WEB=1 pnpm --dir web exec vitest run
src/test/transport-acceptance.test.ts` with the same explicit isolated environment.
The helper reads only the disposable `web-bootstrap.json`, publishes a guarded
loopback `web-client-url`, and forwards controller operations through the real
Web signing, HTTP, result verification and presentation code. POST `/stop` to
that helper with `X-Transport-Test` from `control-token` to stop it before stopping
the controller. Neither helper is part of product builds.

For an original v3 storage initialization followed by manual migration before
starting the current daemon, build with `--v3-source` and start the controller
with `RA_TRANSPORT_INIT_V3=1`. The original initializer creates the real v3
schema; dry-run and migration must leave `account.json` unchanged and publish
generation 4. This checks preserved pairing identity, not an old daemon's CLI
execution before migration. It is distinct from the old-binary live migration
exercise above, which also requires a matching old relay/client protocol.

The acceptance overlay also supports an `admission-clock` file containing epoch
milliseconds. This changes only the admission/retention clock, never the host or
native CLI clock. Use it after all ordinary flows, query before the retained
request's deadline plus ten minutes, then advance past that boundary and submit
another fresh mutation to prune the expired summary. The original ID must stay
unknown and must not execute again. Advancing the durable floor intentionally
prevents returning that disposable environment to an earlier clock; finish and
remove the environment afterwards. This is debug-triggered boundary coverage.
