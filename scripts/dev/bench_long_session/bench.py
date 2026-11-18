#!/usr/bin/env python3
"""Repeatable local long-session performance benchmark using isolated dev-env."""
import argparse
import json
import os
import signal
import socket
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
DEV_ENV = ROOT / "ios/scripts/dev-env.sh"
E2E_DOWN = ROOT / "scripts/dev/e2e-down.sh"
SEED_GO = ROOT / "scripts/dev/bench_long_session/seed.go"
MEASURE_JS = ROOT / "scripts/dev/bench_long_session/measure.js"

CLEAN_KEYS = [
    "REMOTE_AGENT_HOME",
    "REMOTE_AGENT_TMUX_SOCKET",
    "REMOTE_AGENT_SERVER",
    "REMOTE_AGENT_CODEX",
    "RA_DEV_AUTHORITY",
    "PLAYWRIGHT_MCP_BROWSER",
]


def clean_env(dev_dir: Path) -> dict:
    env = os.environ.copy()
    for k in list(env.keys()):
        if k in CLEAN_KEYS or k.startswith("PLAYWRIGHT_MCP_"):
            env.pop(k, None)
    env["RA_DEV_DIR"] = str(dev_dir)
    env["CC"] = "/usr/bin/clang"
    env["RA_DEV_ALLOWED_ORIGINS"] = "http://localhost:*,http://127.0.0.1:*"
    return env


def run(cmd, env=None, capture=False, timeout=300):
    return subprocess.run(
        cmd,
        cwd=str(ROOT),
        env=env,
        text=True,
        capture_output=capture,
        check=True,
        timeout=timeout,
    )


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def build_web(env: dict):
    print("==> Building web & WASM core...")
    run(["pnpm", "--dir", "web", "build"], env=env, timeout=300)


def stop_web(dev_dir: Path):
    pid_file = dev_dir / "web.pid"
    if pid_file.exists():
        try:
            pid = int(pid_file.read_text().strip())
            os.kill(pid, signal.SIGTERM)
            time.sleep(0.3)
        except Exception:
            pass
        pid_file.unlink(missing_ok=True)


def start_web(dev_dir: Path, env: dict) -> str:
    stop_web(dev_dir)
    relay_url = (dev_dir / "url").read_text().strip()
    port_file = dev_dir / "web.port"
    port = int(port_file.read_text().strip()) if port_file.exists() else free_port()
    port_file.write_text(str(port))

    wenv = env.copy()
    wenv["RA_PROXY"] = relay_url
    log_path = dev_dir / "web.log"
    with open(log_path, "w") as lf:
        proc = subprocess.Popen(
            ["pnpm", "--dir", "web", "exec", "vite", "preview", "--host", "127.0.0.1", "--port", str(port), "--strictPort"],
            cwd=str(ROOT),
            env=wenv,
            stdin=subprocess.DEVNULL,
            stdout=lf,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    (dev_dir / "web.pid").write_text(str(proc.pid))
    web_url = f"http://127.0.0.1:{port}"
    (dev_dir / "web.url").write_text(web_url)
    for _ in range(50):
        try:
            with urllib.request.urlopen(web_url, timeout=1) as resp:
                if resp.status == 200:
                    return web_url
        except Exception:
            time.sleep(0.1)
    raise RuntimeError(f"vite preview did not start; see {log_path}")


def find_chromium_exe() -> str:
    cache = Path.home() / "Library/Caches/ms-playwright"
    if cache.exists():
        for pattern in (
            "chromium-*/chrome-mac-*/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
            "chromium_headless_shell-*/chrome-headless-shell-*/chrome-headless-shell",
            "chromium-*/chrome-linux*/chrome",
        ):
            hits = sorted(cache.glob(pattern))
            if hits:
                return str(hits[-1])
    mac_chrome = Path("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
    if mac_chrome.exists():
        return str(mac_chrome)
    return ""


def write_pw_config(dev_dir: Path) -> Path:
    cfg_path = dev_dir / "pw-headless.json"
    launch = {"headless": True}
    exe = find_chromium_exe()
    if exe:
        launch["executablePath"] = exe
    cfg_path.write_text(
        json.dumps(
            {
                "browser": {
                    "browserName": "chromium",
                    "isolated": True,
                    "launchOptions": launch,
                }
            }
        )
        + "\n"
    )
    return cfg_path


def pw(session: str, args: list[str], env: dict, timeout=120) -> str:
    res = run(["playwright-cli", f"-s={session}", *args], env=env, capture=True, timeout=timeout)
    return res.stdout


def cmd_setup(args):
    dev_dir = Path(args.dir)
    env = clean_env(dev_dir)
    if dev_dir.exists():
        run([str(E2E_DOWN), str(dev_dir)], env=env, timeout=60)
    subprocess.run(["playwright-cli", f"-s={args.session}", "close"], env=env, capture_output=True)

    web_port = free_port()
    env["RA_DEV_ALLOWED_ORIGINS"] = f"http://127.0.0.1:{web_port},http://localhost:{web_port}"
    print(f"==> Starting isolated relay in {dev_dir} (allowed origin port {web_port})...")
    run([str(DEV_ENV), "start", "--no-account"], env=env, timeout=180)
    (dev_dir / "web.port").write_text(str(web_port))
    build_web(env)
    web_url = start_web(dev_dir, env)
    setup_token = (dev_dir / "setup-token").read_text().strip()
    relay_url = (dev_dir / "url").read_text().strip()
    cfg_path = write_pw_config(dev_dir)

    print(f"==> Creating web authority account at {web_url} (relay {relay_url})...")
    pw(args.session, ["open", web_url, "--config", str(cfg_path)], env, timeout=60)

    setup_js = f"""async page => {{
      await page.setViewportSize({{ width: 1440, height: 900 }});
      await page.locator('.entry-row').first().click();
      await page.locator('input[placeholder*="ra."]').fill({json.dumps(relay_url)});
      await page.locator('button.btn.pri').click();
      await page.locator('input[type="checkbox"]').check();
      await page.locator('input[aria-label="Setup token"]').fill({json.dumps(setup_token)});
      await page.locator('button.btn.pri').click();
      await page.waitForSelector('.desk, .home, .empty, .set', {{ timeout: 30000 }});
      await page.evaluate(() => {{ location.hash = '#/settings/add-device'; }});
      await page.waitForSelector('button.btn.pri', {{ timeout: 15000 }});
      await page.locator('button.btn.pri').click();
      await page.waitForSelector('.pair-code', {{ timeout: 15000 }});
      const raw = await page.locator('.pair-code').textContent();
      return raw.replace(/\\s+/g, '');
    }}"""
    code = json.loads(pw(args.session, ["--raw", "run-code", setup_js], env, timeout=60).strip())
    print(f"==> Pairing code: {code}")

    print("==> Joining daemon...")
    join_proc = subprocess.Popen(
        [str(DEV_ENV), "join", code, "--daemon"],
        cwd=str(ROOT),
        env=env,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    assert join_proc.stdin is not None
    join_proc.stdin.write("y\n")
    join_proc.stdin.flush()

    approve_js = """async page => {
      await page.waitForSelector('.emoji', { timeout: 30000 });
      await page.locator('input[type="checkbox"]').check();
      await page.locator('button.btn.pri').click();
      await page.waitForSelector('.banner', { timeout: 30000 });
      return true;
    }"""
    try:
        pw(args.session, ["--raw", "run-code", approve_js], env, timeout=60)
    except Exception:
        join_proc.kill()
        out, _ = join_proc.communicate()
        print("JOIN OUTPUT:\n" + out, file=sys.stderr)
        raise
    join_out, _ = join_proc.communicate(timeout=60)
    if join_proc.returncode != 0:
        raise RuntimeError(f"join failed: {join_out}")

    # Stop daemon, seed session into initialized home, then restart daemon so it uploads everything
    dpid_file = dev_dir / "daemon.pid"
    if dpid_file.exists():
        try:
            os.kill(int(dpid_file.read_text().strip()), signal.SIGTERM)
            time.sleep(0.5)
        except Exception:
            pass
    sock_file = dev_dir / "home/daemon.sock"
    sock_file.unlink(missing_ok=True)

    print(f"==> Seeding {args.turns}-turn session ({args.sid}) into {dev_dir}/home...")
    run(["go", "run", str(SEED_GO), "--home", str(dev_dir / "home"), "--sid", args.sid, "--turns", str(args.turns)], env=env, timeout=60)

    print("==> Starting daemon to upload seeded session...")
    denv = env.copy()
    denv["REMOTE_AGENT_HOME"] = str(dev_dir / "home")
    denv["REMOTE_AGENT_TMUX_SOCKET"] = (dev_dir / "tmux-socket").read_text().strip()
    with open(dev_dir / "daemon.log", "a") as dlf:
        dproc = subprocess.Popen(
            [str(dev_dir / "bin/remote-agent"), "daemon", "--codex", "/usr/bin/true"],
            cwd=str(dev_dir),
            env=denv,
            stdin=subprocess.DEVNULL,
            stdout=dlf,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    dpid_file.write_text(str(dproc.pid))
    for _ in range(100):
        if sock_file.exists():
            break
        time.sleep(0.1)

    open_sess_js = f"""async page => {{
      await page.evaluate(() => {{ location.hash = '#/s/{args.sid}'; }});
      await page.waitForSelector('.body.chat', {{ timeout: 30000 }});
      await page.waitForFunction(() => {{
        const el = document.querySelector('.body.chat');
        const rows = document.querySelectorAll('[data-item-index]');
        return el && !el.classList.contains('mo-hold') && rows.length > 0;
      }}, {{ timeout: 120000 }});
      return await page.evaluate(() => document.querySelectorAll('[data-item-index]').length);
    }}"""
    rows = json.loads(pw(args.session, ["--raw", "run-code", open_sess_js], env, timeout=150).strip())
    print(f"==> Setup complete! Session {args.sid} uploaded & rendered ({rows} visible rows).")


def cmd_measure(args):
    dev_dir = Path(args.dir)
    env = clean_env(dev_dir)
    if not (dev_dir / "url").exists():
        raise RuntimeError(f"dev_dir {dev_dir} is not started; run setup first")
    build_web(env)
    web_url = start_web(dev_dir, env)
    cfg_path = write_pw_config(dev_dir)
    target_url = f"{web_url}/#/s/{args.sid}"

    alive = subprocess.run(["playwright-cli", f"-s={args.session}", "eval", "1"], env=env, capture_output=True).returncode == 0
    if not alive:
        pw(args.session, ["open", target_url, "--config", str(cfg_path)], env, timeout=60)
    else:
        pw(args.session, ["goto", target_url], env, timeout=60)
    # Wait initial sync settle before reload measurement
    pw(args.session, ["--raw", "run-code", "async page => { await page.waitForSelector('.body.chat', { timeout: 30000 }); await page.waitForTimeout(1000); return true; }"], env, timeout=60)

    raw_out = pw(args.session, ["--raw", "run-code", "--filename", str(MEASURE_JS)], env, timeout=180)
    data = json.loads(raw_out)
    out_path = Path(args.out)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(data, indent=2) + "\n")

    fm = data["finalMetrics"]
    print("\n=== Long Session Benchmark Summary ===")
    print(f"  domLoadedMs:       {data['domLoadedMs']} ms")
    print(f"  chatMountMs:       {data['chatMountMs']} ms")
    print(f"  interactiveMs:     {data['interactiveMs']} ms")
    print(f"  CLS:               {fm['cls']}")
    print(f"  LongTasks:         {fm['longTaskCount']} (total {fm['longTaskTotalMs']} ms)")
    print(f"  Cold view(Sess):   {fm['wasm']['coldView']['sessionViewCount']} calls, {fm['wasm']['coldView']['sessionViewTotalMs']} ms, maxOut {fm['wasm']['coldView']['sessionViewMaxOutBytes']} B")
    print(f"  ScrollUp frames:   {fm['frames']['scrollUp']}")
    print(f"  ScrollUp view(S):  {fm['wasm']['scrollUpView']['sessionViewCount']} calls, {fm['wasm']['scrollUpView']['sessionViewTotalMs']} ms, max {fm['wasm']['scrollUpView']['sessionViewMaxMs']} ms, maxOut {fm['wasm']['scrollUpView']['sessionViewMaxOutBytes']} B")
    print(f"  ScrollDown frames: {fm['frames']['scrollDown']}")
    print(f"  ScrollDown view(S):{fm['wasm']['scrollDownView']['sessionViewCount']} calls, {fm['wasm']['scrollDownView']['sessionViewTotalMs']} ms, max {fm['wasm']['scrollDownView']['sessionViewMaxMs']} ms, maxOut {fm['wasm']['scrollDownView']['sessionViewMaxOutBytes']} B")
    print(f"  Top snap:          {data['topSnap']}")
    print(f"  Saved to:          {out_path}\n")


def cmd_teardown(args):
    dev_dir = Path(args.dir)
    env = clean_env(dev_dir)
    subprocess.run(["playwright-cli", f"-s={args.session}", "close"], env=env, capture_output=True)
    run([str(E2E_DOWN), str(dev_dir)], env=env, timeout=60)


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--dir", default="/tmp/rad-lperf")
    p.add_argument("--sid", default="long-bench-0001")
    p.add_argument("--session", default="lperf")
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("setup")
    s.add_argument("--turns", type=int, default=350)
    m = sub.add_parser("measure")
    m.add_argument("--out", required=True)
    sub.add_parser("teardown")
    args = p.parse_args()
    if args.cmd == "setup":
        cmd_setup(args)
    elif args.cmd == "measure":
        cmd_measure(args)
    elif args.cmd == "teardown":
        cmd_teardown(args)


if __name__ == "__main__":
    main()
