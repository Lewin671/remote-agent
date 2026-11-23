#!/usr/bin/env python3
"""Prepare a task-completion release, or validate a tag in CI. No scheduled releases."""
import os
import argparse
import hashlib
import json
import re
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
VERSION = ROOT / "version.properties"
SEMVER = re.compile(r"v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)")
# What a release's files are made of: the sources, what is packed next to
# the binaries, and the scripts that decide how they are built. The tools
# that test, version and publish a release are not in it, nor are the
# installer and the guides, which are read from main.
PRODUCT_PATHS = ("i18n/", "scripts/i18n_templates/", "cmd/", "internal/", "shared/", "gen/", "proto/", "clientcore/", "android/", "web/", "deploy/self-host/",
                 "ios/RemoteAgent/", "ios/RemoteAgentCore/", "ios/RemoteAgent.xcodeproj/")
PRODUCT_FILES = {"go.mod", "go.sum", "buf.yaml", "buf.gen.yaml", "buf.gen.lite.yaml", "scripts/daemon.sh", "scripts/build-go.sh", "scripts/build-dist.sh", "scripts/notices.py",
                 # Writes the signing and Firebase settings the APK is built with.
                 "scripts/android-ci.py", "scripts/i18n.py", "ios/scripts/version.sh", "scripts/core/build.sh"}


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True).strip()


def read_version(text=None):
    values = dict(line.split("=", 1) for line in (VERSION.read_text() if text is None else text).splitlines() if "=" in line)
    version, code = values["version"], int(values["androidVersionCode"])
    if not SEMVER.fullmatch("v" + version) or not 0 < code <= 2100000000:
        raise ValueError("Invalid product version or Android version code")
    return version, code


def release_tags():
    return sorted((t for t in git("tag", "--merged", "HEAD").splitlines() if SEMVER.fullmatch(t)),
                  key=lambda t: tuple(map(int, t[1:].split("."))))


def product_change(path):
    if path in {"i18n/lint-baseline.json", "i18n/lint-allowlist.json"}:
        return False  # Validation debt/justifications do not enter product resources.
    # The web build type-checks all of web/src, its tests included, so a
    # broken test there fails the release as surely as broken code.
    if path.startswith("web/src/"):
        return True
    tests = path.endswith(("_test.go", "_test.py", ".test.ts", ".test.tsx", ".spec.ts", ".spec.tsx")) or any(part in path for part in ("/src/test/", "/src/androidTest/", "/tests/", "/Tests/", "/testdata/"))
    return not tests and not path.endswith(".md") and (path in PRODUCT_FILES or path.startswith(PRODUCT_PATHS))


def changed_since(tag):
    paths = git("diff", "--name-only", tag, "HEAD").splitlines() if tag else git("ls-files").splitlines()
    return any(product_change(p) for p in paths)


def prepare(kind):
    if git("status", "--porcelain"):
        raise ValueError("Commit task changes before preparing a release")
    tags = release_tags()
    previous = tags[-1] if tags else None
    # RA_REPUBLISH=1: the last tag's release never came out and what fixed it
    # was a test, which is no product change, so the next version carries it.
    if not changed_since(previous) and os.environ.get("RA_REPUBLISH") != "1":
        return {"eligible": False, "reason": "No product changes since the last tag"}
    version, code = read_version()
    if previous and previous != "v" + version:
        raise ValueError("main version must match the latest release tag before bumping")
    check_relay_automatic_policy(previous)
    major, minor, patch = map(int, version.split("."))
    if kind == "major":
        major, minor, patch = major + 1, 0, 0
    elif kind == "minor":
        minor, patch = minor + 1, 0
    else:
        patch += 1
    version = f"{major}.{minor}.{patch}"
    if git("tag", "--list", "v" + version):
        raise ValueError("The next version tag already exists; do not move or replace it")
    read_version(f"version={version}\nandroidVersionCode={code + 1}\n")
    VERSION.write_text(f"version={version}\nandroidVersionCode={code + 1}\n")
    return {"eligible": True, "version": version, "tag": "v" + version, "androidVersionCode": code + 1}


def validate(tag):
    version, code = read_version()
    if not SEMVER.fullmatch(tag) or tag != "v" + version:
        raise ValueError("Release tag must match version.properties exactly")
    if git("rev-list", "-n", "1", tag) != git("rev-parse", "HEAD"):
        raise ValueError("Build must use the tagged source commit")
    previous = [t for t in release_tags() if t != tag]
    if previous:
        last = previous[-1]
        if tuple(map(int, tag[1:].split("."))) <= tuple(map(int, last[1:].split("."))):
            raise ValueError("Product version must increase")
        _, old_code = read_version(git("show", last + ":version.properties"))
        if code <= old_code:
            raise ValueError("Android versionCode must increase")
        check_relay_automatic_policy(last)
    return {"version": version, "tag": tag, "androidVersionCode": code, "commit": git("rev-parse", "HEAD")}


def notes(tag):
    metadata = validate(tag)
    previous = [t for t in release_tags() if t != tag]
    print(f"Source: `{metadata['commit']}`\n\nAndroid versionCode: `{metadata['androidVersionCode']}`\n")
    print("Release contains the signed APK, the daemon and server for each platform, the web client and SHA256SUMS. Product version and source revision remain shared across all components. Publishing does not deploy live services.\n")
    print("## Release and upgrade notes\n")
    print(git("tag", "--list", tag, "--format=%(contents)"))
    print("\n## Changes\n")
    rev = previous[-1] + "..HEAD" if previous else "HEAD"
    print(git("log", "--format=- %s (%h)", rev))


def peer_protocol_version(text=None):
    source = (ROOT / "internal/protocol/protocol.go").read_text() if text is None else text
    # Pre-split tags used `const Version`; tighten to `const PeerVersion` after
    # the first tag containing internal/relayproto is published.
    match = re.search(r"^const (?:Peer)?Version = (\d+)$", source, re.M)
    if not match:
        raise ValueError("Peer protocol version not found")
    return int(match.group(1))


def relay_protocol_version(text=None):
    source = (ROOT / "internal/relayproto/relayproto.go").read_text() if text is None else text
    match = re.search(r"^const Version = (\d+)$", source, re.M)
    if not match:
        raise ValueError("Relay protocol version not found")
    return int(match.group(1))


def tag_relay_protocol_version(tag):
    # Pre-split tags had no internal/relayproto and stored the shared protocol
    # version in internal/protocol/protocol.go. Remove that fallback after the
    # first tag containing internal/relayproto is published.
    for rel, parser in (
        ("internal/relayproto/relayproto.go", relay_protocol_version),
        ("internal/protocol/protocol.go", peer_protocol_version),
    ):
        res = subprocess.run(["git", "show", f"{tag}:{rel}"], cwd=ROOT, text=True, capture_output=True)
        if res.returncode == 0:
            return parser(res.stdout)
    return None


def read_daemon_policy():
    policy_file = ROOT / "internal/updater/policy.json"
    if not policy_file.exists():
        return None
    policy = json.loads(policy_file.read_text())
    if type(policy.get("state")) is not int or policy["state"] < 1 or type(policy.get("automatic")) is not bool:
        raise ValueError("Invalid daemon update policy")
    return policy


def check_relay_automatic_policy(previous_tag):
    if not previous_tag:
        return
    policy = read_daemon_policy()
    if policy is None:
        return
    old_relay = tag_relay_protocol_version(previous_tag)
    if old_relay is None:
        return
    new_relay = relay_protocol_version()
    if new_relay != old_relay and policy["automatic"]:
        raise ValueError(
            f"Relay protocol version changed ({old_relay} -> {new_relay}) since {previous_tag}; "
            "internal/updater/policy.json must set automatic=false"
        )


def property_value(text):
    return text.strip().replace("\\", "\\\\").replace("\r", "").replace("\n", "\\n")


def manifest(tag, apk):
    """What the app reads to find a newer release: key=value lines, read on Android as java.util.Properties."""
    metadata = validate(tag)
    data = Path(apk).read_bytes()
    fields = {
        "version": metadata["version"],
        "versionCode": metadata["androidVersionCode"],
        "protocol": peer_protocol_version(),
        "relayProtocol": relay_protocol_version(),
        "apk": f"remote-agent-{tag}.apk",
        "sha256": hashlib.sha256(data).hexdigest(),
        "size": len(data),
        # The tag's message is written for users: what changed and what upgrading needs.
        "notes": git("tag", "--list", tag, "--format=%(contents)"),
    }
    policy = read_daemon_policy()
    if policy is not None:
        dist = Path(apk).parent
        sums = {}
        for line in (dist / "SHA256SUMS").read_text().splitlines():
            match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9.-]+)", line)
            if not match or match[2] in sums:
                raise ValueError("Malformed or duplicate release checksum")
            sums[match[2]] = match[1]
        if sums.get(fields["apk"]) != fields["sha256"]:
            raise ValueError("APK checksum disagrees with SHA256SUMS")
        fields.update(daemonUpdate=1, daemonState=policy["state"], daemonAutomatic=str(policy["automatic"]).lower())
        for target in ("darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64"):
            name = f"remote-agent-{tag}-{target}.tar.gz"
            archive = dist / name
            digest = sums.get(name)
            if not digest or hashlib.sha256(archive.read_bytes()).hexdigest() != digest:
                raise ValueError("Daemon checksum disagrees with SHA256SUMS")
            fields[f"daemon.{target}.sha256"] = digest
            fields[f"daemon.{target}.size"] = archive.stat().st_size
    return "".join(f"{k}={property_value(str(v))}\n" for k, v in fields.items())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    p = sub.add_parser("prepare")
    p.add_argument("kind", choices=["patch", "minor", "major"])
    for command in ("validate", "notes"):
        sub.add_parser(command).add_argument("tag")
    p = sub.add_parser("manifest")
    p.add_argument("tag")
    p.add_argument("apk")
    args = parser.parse_args()
    if args.command == "notes":
        notes(args.tag)
    elif args.command == "manifest":
        print(manifest(args.tag, args.apk), end="")
    else:
        print(json.dumps(prepare(args.kind) if args.command == "prepare" else validate(args.tag)))


if __name__ == "__main__":
    main()
