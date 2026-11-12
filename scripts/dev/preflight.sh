#!/bin/sh
# preflight.sh: verify environment before starting a remote-agent dev task.
# Checks RA_DEV_DIR length, session env vars, disk watermark, ports, tools,
# Android device screen state, and iOS simulator availability.
exec python3 - "$@" << 'PYEOF'
import argparse
import os
import re
import shutil
import socket
import subprocess
import sys
from pathlib import Path

MAX_SAFE_DIR_LEN = 70  # AF_UNIX max is 104; home/daemon.sock takes 17 chars.
RELAY_MIN_FREE_BYTES = 2 * (1024 ** 3)  # 2 GiB server low watermark
WARN_FREE_BYTES = 3 * (1024 ** 3)  # 3 GiB safe watermark

POLLUTING_ENV_VARS = [
    "REMOTE_AGENT_HOME",
    "REMOTE_AGENT_TMUX_SOCKET",
    "REMOTE_AGENT_SERVER",
    "REMOTE_AGENT_CODEX",
    "RA_SETUP_TOKEN",
    "RA_DEV_AUTHORITY",
]


class Reporter:
    def __init__(self, quiet=False):
        self.quiet = quiet
        self.fails = 0
        self.warns = 0
        self.passes = 0
        self.next_steps = []

    def pass_(self, title, msg):
        self.passes += 1
        if not self.quiet:
            print(f"[\033[32mPASS\033[0m] {title}: {msg}")

    def fail(self, title, msg, remedy=None):
        self.fails += 1
        print(f"[\033[31mFAIL\033[0m] {title}: {msg}")
        if remedy:
            self.next_steps.append(remedy)

    def warn(self, title, msg, remedy=None):
        self.warns += 1
        if not self.quiet:
            print(f"[\033[33mWARN\033[0m] {title}: {msg}")
        if remedy:
            self.next_steps.append(remedy)

    def info(self, title, msg):
        if not self.quiet:
            print(f"[\033[34mINFO\033[0m] {title}: {msg}")


def check_tool(name, version_cmd):
    try:
        proc = subprocess.run(version_cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=False)
        if proc.returncode == 0:
            out = (proc.stdout.strip() or proc.stderr.strip()).splitlines()
            ver = out[0] if out else "available"
            return True, ver
        return False, f"exited {proc.returncode}"
    except FileNotFoundError:
        return False, "not installed"


def check_port(port):
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            s.bind(("127.0.0.1", port))
            return True
        except OSError:
            return False


def get_disk_free(path):
    p = Path(path).resolve()
    while not p.exists() and p.parent != p:
        p = p.parent
    total, used, free = shutil.disk_usage(p)
    return free, p


def generate_short_dir():
    import tempfile
    prefix = "/tmp/rad-"
    return tempfile.mkdtemp(prefix=prefix)


def check_android(reporter, wake=False, target_device=None):
    adb_ok, _ = check_tool("adb", ["adb", "version"])
    if not adb_ok:
        reporter.info("Android", "adb not found; skipping Android checks")
        return

    try:
        proc = subprocess.run(["adb", "devices"], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=False)
        lines = [line.strip() for line in proc.stdout.splitlines() if line.strip() and not line.startswith("List of devices")]
        devices = []
        for line in lines:
            parts = line.split()
            if len(parts) >= 2:
                devices.append((parts[0], parts[1]))

        if not devices:
            reporter.info("Android", "no adb devices connected")
            return

        # Select target device
        selected = None
        if target_device:
            for serial, state in devices:
                if serial == target_device:
                    selected = (serial, state)
                    break
            if not selected:
                reporter.fail("Android device", f"device {target_device} not found in adb devices")
                return
        else:
            selected = devices[0]

        serial, state = selected
        if state != "device":
            reporter.fail("Android device", f"{serial} is in state '{state}' (need 'device')", remedy=f"adb reconnect or authorize on {serial}")
            return

        # Inspect power / wakefulness
        pwr = subprocess.run(["adb", "-s", serial, "shell", "dumpsys", "power"], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=False)
        awake = "mWakefulness=Awake" in pwr.stdout or "Display Power: state=ON" in pwr.stdout

        # Inspect lockscreen / keyguard
        trust = subprocess.run(["adb", "-s", serial, "shell", "dumpsys", "trust"], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=False)
        locked = False
        if "deviceLocked=1" in trust.stdout or "deviceLocked=true" in trust.stdout:
            locked = True

        if awake and not locked:
            reporter.pass_("Android device", f"{serial} online, screen awake & unlocked")
        elif awake and locked:
            reporter.warn("Android device", f"{serial} online, screen awake but locked", remedy=f"adb -s {serial} shell input keyevent 82")
        else:
            if wake:
                subprocess.run(["adb", "-s", serial, "shell", "input", "keyevent", "224"], check=False)
                subprocess.run(["adb", "-s", serial, "shell", "input", "keyevent", "82"], check=False)
                reporter.pass_("Android device", f"{serial} woke screen and sent unlock key")
            else:
                reporter.warn("Android device", f"{serial} online but screen asleep", remedy=f"Run preflight.sh --wake or: adb -s {serial} shell input keyevent 224")

    except Exception as e:
        reporter.warn("Android device check", f"failed to query device: {e}")


def check_simulator(reporter, target_sim=None):
    xcrun_ok, _ = check_tool("xcrun", ["xcrun", "simctl", "help"])
    if not xcrun_ok:
        reporter.info("iOS Simulator", "xcrun simctl not available")
        return

    try:
        proc = subprocess.run(["xcrun", "simctl", "list", "devices", "available", "-j"], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=False)
        if proc.returncode != 0:
            reporter.info("iOS Simulator", "unable to list simulators via simctl")
            return
        import json
        data = json.loads(proc.stdout)
        all_devs = []
        for runtime, dev_list in data.get("devices", {}).items():
            for d in dev_list:
                all_devs.append(d)

        if target_sim:
            found = [d for d in all_devs if d.get("name") == target_sim or d.get("udid") == target_sim]
            if not found:
                reporter.fail("iOS Simulator", f"specified simulator '{target_sim}' not found", remedy=f"xcrun simctl list devices")
            else:
                d = found[0]
                reporter.pass_("iOS Simulator", f"'{d.get('name')}' ({d.get('state')}) ready")
        else:
            booted = [d for d in all_devs if d.get("state") == "Booted"]
            if booted:
                reporter.pass_("iOS Simulator", f"{len(booted)} simulator(s) booted: {booted[0].get('name')}")
            else:
                reporter.info("iOS Simulator", f"{len(all_devs)} simulator(s) installed (none booted)")
    except Exception as e:
        reporter.info("iOS Simulator", f"simctl check skipped: {e}")


def main():
    parser = argparse.ArgumentParser(description="Preflight environment checks for remote-agent dev tasks.")
    parser.add_argument("--dir", default=os.environ.get("RA_DEV_DIR"), help="RA_DEV_DIR path to check")
    parser.add_argument("--make-dir", action="store_true", help="Create a short unique directory and print export command")
    parser.add_argument("--port", type=int, help="Check specific port availability")
    parser.add_argument("--wake", action="store_true", help="Send wake keyevent to Android device if asleep")
    parser.add_argument("--device", help="Android device serial")
    parser.add_argument("--simulator", help="iOS simulator name or UDID")
    parser.add_argument("--quiet", action="store_true", help="Only show failures and remedies")
    args = parser.parse_args()

    reporter = Reporter(quiet=args.quiet)

    # 1. RA_DEV_DIR length & uniqueness
    dev_dir = args.dir
    if args.make_dir:
        dev_dir = generate_short_dir()
        reporter.pass_("RA_DEV_DIR created", f"{dev_dir} (length: {len(dev_dir)})")
        reporter.next_steps.append(f"export RA_DEV_DIR={dev_dir}")
    elif dev_dir:
        length = len(dev_dir)
        if length > MAX_SAFE_DIR_LEN:
            suggested = f"/tmp/rad-{os.getpid()}"
            reporter.fail("RA_DEV_DIR path length", f"{length} chars exceeds safe limit ({MAX_SAFE_DIR_LEN} chars for AF_UNIX 104-byte bound)",
                          remedy=f"export RA_DEV_DIR={suggested}")
        else:
            reporter.pass_("RA_DEV_DIR", f"{dev_dir} (length: {length} <= {MAX_SAFE_DIR_LEN})")
    else:
        suggested = f"/tmp/rad-{os.getpid()}"
        reporter.info("RA_DEV_DIR", f"not set; recommend: {suggested}")
        reporter.next_steps.append(f"export RA_DEV_DIR={suggested}")

    # 2. Inherited session variables
    polluted = []
    for var in POLLUTING_ENV_VARS:
        if os.environ.get(var):
            polluted.append(var)
    if polluted:
        remedy_cmd = f"unset {' '.join(polluted)}"
        reporter.fail("Inherited session env", f"polluted by: {', '.join(polluted)}", remedy=remedy_cmd)
    else:
        reporter.pass_("Session env", "clean (no leaked REMOTE_AGENT_* or RA_* credentials)")

    if os.environ.get("TMUX"):
        reporter.warn("TMUX environment", "inside active tmux session (daemon will use an isolated socket)")

    # 3. Disk space & relay watermark
    check_dir = dev_dir or "/tmp"
    free_bytes, checked_mount = get_disk_free(check_dir)
    free_gib = free_bytes / (1024 ** 3)
    if free_bytes < RELAY_MIN_FREE_BYTES:
        reporter.fail("Disk space", f"{free_gib:.2f} GiB free on {checked_mount} (relay low watermark is 2.00 GiB; server will refuse accounts)",
                      remedy="Free up disk space on this volume before running tests")
    elif free_bytes < WARN_FREE_BYTES:
        reporter.warn("Disk space", f"{free_gib:.2f} GiB free on {checked_mount} (approaching 2.00 GiB watermark)")
    else:
        reporter.pass_("Disk space", f"{free_gib:.2f} GiB free on {checked_mount} (relay minimum: 2.00 GiB)")

    # 4. Port availability
    if not check_port(0):
        reporter.fail("Loopback ports", "cannot bind ephemeral port on 127.0.0.1", remedy="Check local firewall or network restrictions")
    else:
        reporter.pass_("Loopback port bind", "127.0.0.1 ephemeral ports bindable")

    if args.port:
        if check_port(args.port):
            reporter.pass_("Port check", f"port {args.port} is available")
        else:
            reporter.fail("Port check", f"port {args.port} is already in use", remedy=f"Choose another port or kill the process on {args.port}")

    # 5. Core developer tools
    tools = [
        ("tmux", ["tmux", "-V"]),
        ("go", ["go", "version"]),
        ("pnpm", ["pnpm", "--version"]),
        ("adb", ["adb", "version"]),
        ("xcrun", ["xcrun", "simctl", "help"]),
    ]
    for name, cmd in tools:
        ok, ver = check_tool(name, cmd)
        if ok:
            reporter.pass_(f"Tool {name}", ver)
        else:
            reporter.warn(f"Tool {name}", f"{ver}", remedy=f"Ensure {name} is installed and in PATH")

    # 6. Android device check
    check_android(reporter, wake=args.wake, target_device=args.device)

    # 7. iOS Simulator check
    check_simulator(reporter, target_sim=args.simulator)

    # Summary
    if not args.quiet:
        print()
        print(f"Preflight summary: {reporter.passes} passed, {reporter.warns} warnings, {reporter.fails} failed.")

    if reporter.next_steps:
        print("\nNext steps:")
        for step in reporter.next_steps:
            print(f"  {step}")

    sys.exit(1 if reporter.fails > 0 else 0)


if __name__ == "__main__":
    main()
PYEOF
