# Relay first-command measurement

Run from a clean candidate checkout after its full checks. This uses the same
driver for the fixed protocol-15 baseline and the candidate; it never fetches,
pushes, publishes, installs a daemon, or opens a normal Remote Agent account.

```sh
python3 scripts/measure-relay.py \
  --baseline dff1cb11aff370f31e0b57248ccd917216cdd51d \
  --candidate HEAD --samples 200 --warmup 20 --rounds 2 \
  --label local-reference --output /tmp/relay-latency.json
```

On the designated iMac use the same command, optionally labeling the output
`--label imac`. After a rebase, pass the exact commit beneath the task commits
as `--baseline`, and record both resolved hashes; a moving branch name is not
a frozen baseline. For a shared measurement host, add `--settle-seconds 60`
to leave an idle minute after both builds and before the first batch. Acquire
the host's scheduling lock and verify that other work does not disturb the
settling interval or the measured batches. The label is descriptive; it does
not certify acceptance.
Go, pnpm, tmux and the usual Claude/Codex CLI prerequisites must be on PATH.
If these are HOME-relative wrapper scripts, set `RA_REAL_CODEX` and
`RA_REAL_CLAUDE` to the actual installed executables before running. The isolated
HOME intentionally has no normal user shell configuration.
No model session is created and native authentication is not copied. Dependencies
are installed with the frozen lockfile in disposable detached worktrees.

Each sample is the first `GetSettings` call of a fresh production Web `Remote`,
with an already authenticated temporary identity and a ready local daemon.
Timing covers signing, the loopback acceptance proxy, the real relay, daemon
dispatch and handler, and verification of the signed result. It excludes login,
startup, UI rendering, and model response time. This is a debug-triggered API
measurement, not a UI or native CLI response-time measurement.

Two batches per commit run in ABBA order. Each batch discards its fixed 20 warmup
calls, then retains every one of its 200 measured calls. Failed calls fail the
run; they are not removed as outliers. JSON contains per-batch and pooled
nearest-rank p50/p95, sample counts, raw numeric durations, commit IDs, and tool
versions. Exit code 2 means the measured p95 increase was at least 5 ms.

Do not overlap this run with builds or other load tests. The script requires
15 GiB free before heavy steps, assigns ports automatically, explicitly sets
and verifies isolated `REMOTE_AGENT_HOME` for every binary process, and builds
with `CGO_ENABLED=0`. Only its recorded process IDs are stopped. Its accounts,
databases, credentials, binaries and checkouts are removed on completion. If
process cleanup cannot be confirmed, it reports the retained temporary root
instead of deleting the process ledger.
