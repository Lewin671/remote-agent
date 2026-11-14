#!/usr/bin/env python3
"""Compare first authenticated relay-command latency from two exact commits.

Uses disposable checkouts, accounts, databases, daemon homes and tmux sockets.
No native CLI session is created and no daily Remote Agent state is read. The
existing local acceptance controller supplies the real relay/daemon and an
ephemeral Web identity; the timer includes Web signing, forwarding, the real
GetSettings handler and authenticated result verification. It excludes login,
daemon startup, UI rendering and model response time.

Run full checks first and run this without other builds. Default order is ABBA,
200 measured first calls plus 20 warmups per batch, 400 samples per commit.
The iMac acceptance uses the same script; results on another host are reference
values only. Go, pnpm, tmux and the repository's normal CLI prerequisites must
be available. The script installs frozen web dependencies in its own checkouts.
"""
import argparse
import json
import math
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import subprocess
import tempfile
import threading
import time

BASELINE = "dff1cb11aff370f31e0b57248ccd917216cdd51d"
TEMPLATES = Path(__file__).resolve().with_suffix("")


def checked(command, *, cwd=None, env=None, log=None, timeout=600):
    if log:
        with open(log, "w") as output:
            result = subprocess.run(command, cwd=cwd, env=env, stdout=output, stderr=subprocess.STDOUT, timeout=timeout)
        if result.returncode:
            # The measurement test deliberately reports only generic errors;
            # preserve its diagnostics before disposable checkouts are removed.
            detail = "\n" + Path(log).read_text()[-12000:] if Path(log).name == "measure.log" else ""
            raise RuntimeError(f"step_failed: {command[0]} exit={result.returncode}{detail}")
        return ""
    return subprocess.check_output(command, cwd=cwd, env=env, text=True, timeout=timeout).strip()


def disk_check():
    volume = "/System/Volumes/Data" if Path("/System/Volumes/Data").exists() else "/tmp"
    if shutil.disk_usage(volume).free < 15 * 1024**3:
        raise RuntimeError("less than 15 GiB available")


def isolated_env(runtime):
    assert runtime.resolve().is_relative_to(Path("/tmp").resolve())
    for name in ("bootstrap", "bin", "trace", "candidate/machine", "candidate/native-home", "candidate/native-codex", "candidate/native-claude", "candidate/native-agy", "candidate/project", "candidate/outside"):
        (runtime/name).mkdir(parents=True, exist_ok=True, mode=0o700)
    env = dict(os.environ, RA_DEV_DIR=str(runtime), REMOTE_AGENT_HOME=str(runtime/"bootstrap"), CGO_ENABLED="0", GOFLAGS="", TMPDIR="/tmp")
    for key in ("RA_TRANSPORT_INIT_V3", "RA_TRANSPORT_V3_BINARY", "RA_TRANSPORT_WEB", "RA_PROXY", "RA_OFFICIAL_RELEASE", "VITE_SAME_ORIGIN_API"):
        env.pop(key, None)
    assert env["REMOTE_AGENT_HOME"] == str(runtime/"bootstrap")
    return env


def processes():
    rows = {}
    for line in checked(["ps", "-axo", "pid=,ppid=,stat=,lstart="]).splitlines():
        fields = line.split()
        if len(fields) >= 8:
            rows[int(fields[0])] = (int(fields[1]), fields[2], " ".join(fields[3:]))
    return rows


def remember_tree(pid):
    table = processes()
    owned = {pid}
    # Traverse a fixed process snapshot; this is not a readiness polling loop.
    for _ in range(len(table)):
        added = {child for child, row in table.items() if row[0] in owned}
        if added.issubset(owned):
            break
        owned |= added
    return {child: table[child][2] for child in owned if child in table}


def stop_owned(process, ledger, runtime):
    ledger.update(remember_tree(process.pid))
    starts = runtime/"owned-starts.jsonl"
    if starts.exists():
        for line in starts.read_text().splitlines():
            entry = json.loads(line)
            ledger.setdefault(entry["pid"], entry["started"])
    if process.poll() is None:
        process.send_signal(signal.SIGINT)
        try:
            process.wait(timeout=45)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=10)
    def survivors():
        table = processes()
        return [pid for pid, started in ledger.items() if pid in table and table[pid][2] == started and not table[pid][1].startswith("Z")]
    remaining = survivors()
    for pid in remaining:
        try: os.kill(pid, signal.SIGTERM)
        except ProcessLookupError: pass
    if remaining:
        time.sleep(0.5)
        remaining = survivors()
        for pid in remaining:
            try: os.kill(pid, signal.SIGKILL)
            except ProcessLookupError: pass
        if remaining: time.sleep(0.2)
    if survivors():
        raise RuntimeError("owned process cleanup incomplete")


def percentile(values, quantile):
    ordered = sorted(values)
    return ordered[max(0, math.ceil(quantile * len(ordered)) - 1)]


def stats(values):
    return {"n": len(values), "p50_ms": percentile(values, .50), "p95_ms": percentile(values, .95)}


def template(name, output, replacements):
    text = (TEMPLATES/name).read_text()
    for key, value in replacements.items():
        text = text.replace(key, json.dumps(str(value)))
    output.write_text(text)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--baseline", default=BASELINE)
    parser.add_argument("--candidate", default="HEAD")
    parser.add_argument("--samples", type=int, default=200)
    parser.add_argument("--warmup", type=int, default=20)
    parser.add_argument("--rounds", type=int, default=2)
    parser.add_argument("--settle-seconds", type=int, default=0, help="Idle after both builds and before measurement")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--label", default="local-reference")
    args = parser.parse_args()
    if not (1 <= args.samples <= 10000 and 0 <= args.warmup <= 1000 and 1 <= args.rounds <= 6):
        parser.error("invalid sample/warmup/round count")
    if not 0 <= args.settle_seconds <= 600:
        parser.error("invalid settling interval")
    repo = args.repo.resolve()
    commits = {label: checked(["git", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}"], cwd=repo) for label, ref in [("baseline", args.baseline), ("candidate", args.candidate)]}
    assert all(re.fullmatch(r"[0-9a-f]{40}", commit) for commit in commits.values())
    if args.candidate == "HEAD" and checked(["git", "status", "--porcelain", "--untracked-files=no"], cwd=repo):
        parser.error("commit the candidate's tracked changes before measuring HEAD")
    disk_check()
    root = Path(tempfile.mkdtemp(prefix="ra-0b-perf-", dir="/tmp")).resolve()
    checkouts, binaries, samples, batches = {}, {}, {label: [] for label in commits}, []
    result = None
    cleanup_ok = True
    tool_versions = {}
    try:
        for label, commit in commits.items():
            disk_check()
            checkout = root/(label+"-source")
            checked(["git", "worktree", "add", "--detach", str(checkout), commit], cwd=repo)
            checkouts[label] = checkout
            build = root/(label+"-build")
            env = isolated_env(build)
            if not tool_versions:
                tool_versions = {"go": checked(["go", "version"], env=env), "node": checked(["node", "--version"], env=env), "pnpm": checked(["pnpm", "--version"], env=env)}
            checked(["pnpm", "--dir", str(checkout/"web"), "install", "--frozen-lockfile"], env=env, log=build/"install.log")
            # Only the test controller's socket name is instrumented. This
            # excludes stale PID-named test sockets from a previous invocation.
            controller = checkout/"scripts/acceptance-transport/main.go"
            source = controller.read_text()
            needle = '"ra-t4-" + fmt.Sprint(os.Getpid())'
            assert source.count(needle) == 1
            instrumented = build/"controller.go"
            source = source.replace(needle, '"ra-t4-" + filepath.Base(root) + "-" + fmt.Sprint(os.Getpid())')
            assert source.count("must(cmd.Start())") == 1
            source = source.replace("must(cmd.Start())", 'must(cmd.Start())\n benchmarkRecordProcess(env["RA_DEV_DIR"], cmd.Process.Pid)')
            source += r'''
func benchmarkRecordProcess(root string, pid int) {
 raw, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
 if err != nil { return }
 row, _ := json.Marshal(map[string]any{"pid":pid,"started":strings.Join(strings.Fields(string(raw)), " ")})
 f, err := os.OpenFile(filepath.Join(root,"owned-starts.jsonl"),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600)
 if err != nil { return }
 _, _ = f.Write(append(row, '\n')); _ = f.Close()
}
'''
            instrumented.write_text(source)
            overlay = build/"overlay.json"
            overlay.write_text(json.dumps({"Replace": {str(controller): str(instrumented)}}))
            flags = f"-X github.com/Lewin671/remote-agent/internal/buildinfo.Version=dev -X github.com/Lewin671/remote-agent/internal/buildinfo.Commit={commit}"
            for output, package in [("controller", "./scripts/acceptance-transport"), ("remote-agent-server", "./cmd/remote-agent-server"), ("remote-agent", "./cmd/remote-agent")]:
                disk_check()
                extra = ["-overlay", str(overlay)] if output == "controller" else []
                checked(["go", "build", "-mod=readonly", "-trimpath", "-ldflags", flags, *extra, "-o", str(build/"bin"/output), package], cwd=checkout, env=env, log=build/(output+"-build.log"))
            binaries[label] = build/"bin"
        if args.settle_seconds:
            print(json.dumps({"phase": "settle", "seconds": args.settle_seconds}), flush=True)
            time.sleep(args.settle_seconds)
        for number in range(args.rounds):
            order = list(commits) if number % 2 == 0 else list(reversed(commits))
            for label in order:
                disk_check()
                runtime = root/f"run-{label}-{number+1}-{root.name.rsplit('-',1)[-1]}"
                env = isolated_env(runtime)
                env.update(RA_BENCH_SAMPLES=str(args.samples), RA_BENCH_WARMUP=str(args.warmup))
                for binary in binaries[label].iterdir():
                    (runtime/"bin"/binary.name).symlink_to(binary)
                checkout = checkouts[label]
                (runtime/"node_modules").symlink_to(checkout/"web/node_modules", target_is_directory=True)
                template("web.test.ts.in", runtime/"web.test.ts", {"__REMOTE__": checkout/"web/src/remote", "__SECURITY_FIXTURE__": checkout/"web/src/test/security-fixture", "__CRYPTO__": checkout/"web/src/crypto"})
                template("vitest.config.ts.in", runtime/"vitest.config.ts", {"__VITE_CONFIG__": checkout/"web/vite.config.ts", "__RUNTIME__": runtime, "__CHECKOUT__": checkout})
                ready, status = threading.Event(), {"ready": False}
                process = subprocess.Popen([str(runtime/"bin/controller")], cwd=checkout, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, start_new_session=True)
                ledger = remember_tree(process.pid)
                def consume(proc=process, location=runtime, event=ready, state=status):
                    with open(location/"controller.log", "w") as output:
                        for line in proc.stdout:
                            output.write(line)
                            if "READY isolated transport controller" in line:
                                state["ready"] = True
                                event.set()
                    event.set()
                reader = threading.Thread(target=consume, daemon=True)
                reader.start()
                print(json.dumps({"phase": "measure", "commit": commits[label], "batch": number+1}), flush=True)
                try:
                    if not ready.wait(45) or not status["ready"]:
                        raise RuntimeError("isolated controller did not become ready")
                    ledger.update(remember_tree(process.pid))
                    (runtime/"owned-pids.json").write_text(json.dumps(ledger))
                    checked(["pnpm", "--dir", str(checkout/"web"), "exec", "vitest", "run", "--config", str(runtime/"vitest.config.ts")], cwd=checkout, env=env, log=runtime/"measure.log", timeout=1800)
                    values = json.loads((runtime/"latencies.json").read_text())
                    assert len(values) == args.samples and all(isinstance(value, (int, float)) and math.isfinite(value) and value >= 0 for value in values)
                    samples[label].extend(values)
                    batches.append({"label": label, "batch": number+1, **stats(values)})
                finally:
                    try:
                        stop_owned(process, ledger, runtime)
                    except Exception:
                        cleanup_ok = False
                        raise
                    reader.join(timeout=1)
                    shutil.rmtree(runtime)
        summaries = {label: {"commit": commits[label], **stats(values)} for label, values in samples.items()}
        increment = summaries["candidate"]["p95_ms"] - summaries["baseline"]["p95_ms"]
        result = {"label": args.label, "tools": tool_versions, "method": "fresh Web Remote first GetSettings; authenticated local relay and real daemon; nearest-rank percentiles; login/startup/UI/model time excluded", "platform": {"os": platform.system(), "version": platform.mac_ver()[0], "arch": platform.machine()}, "warmup_per_batch": args.warmup, "settle_seconds": args.settle_seconds, "batches": batches, **summaries, "p95_increment_ms": increment, "under_5_ms": increment < 5, "samples_ms": samples, "cleanup": "owned processes stopped; disposable accounts and checkouts removed"}
    finally:
        if cleanup_ok:
            for checkout in reversed(list(checkouts.values())):
                checked(["git", "worktree", "remove", str(checkout)], cwd=repo)
            shutil.rmtree(root)
        else:
            print(json.dumps({"cleanup_incomplete": str(root)}), flush=True)
    assert result is not None
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, indent=2)+"\n")
    print(json.dumps({key: value for key, value in result.items() if key != "samples_ms"}, indent=2))
    return 0 if result["under_5_ms"] else 2


if __name__ == "__main__":
    raise SystemExit(main())
