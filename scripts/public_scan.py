#!/usr/bin/env python3
"""Refuses to publish files that name our own deployment or carry a key.

Usage: scripts/public_scan.py [--release TAG DIST] PATH...

Everything handed to the public repository goes through this first: the
release's files and that repository's own pages. Archives and the APK are
read member by member, and text is looked for in UTF-16 as well, which is
how an APK stores its resources. Files are also refused by name, and with
--release a packaged release must be exactly the files a release has.

This repository is public, so what names our own deployment is not listed
here: those terms come from RA_PUBLIC_DENY, one per line (an Actions secret
in CI), or when it is unset from ~/.config/remote-agent/public-deny. Without
them nothing is publishable, and a finding names such a term only by number.
"""
import gzip
import io
import os
import re
import sys
import tarfile
import zipfile
from pathlib import Path

# A CI runner's paths, keys, and the official site of this project.
DENIED = [
    "remote-agent.top",
    "/home/runner/",
    "PRIVATE KEY-----",
    "RA_SETUP_TOKEN=[A-Za-z0-9_-]{8,}",
]


def private_terms():
    """Our hosts, addresses and machines, from outside the repository."""
    text = os.environ.get("RA_PUBLIC_DENY")
    if text is None:
        file = Path.home() / ".config/remote-agent/public-deny"
        text = file.read_text() if file.is_file() else ""
    return [t.strip() for t in text.splitlines() if t.strip() and not t.strip().startswith("#")]


PRIVATE = private_terms()
PATTERNS = [(term, re.compile(term.encode(), re.I)) for term in DENIED]
PATTERNS += [(term, re.compile(term.encode("utf-16-le"), re.I)) for term in DENIED if term.isascii() and not re.search(r"[\[{]", term)]
for i, term in enumerate(PRIVATE, 1):
    PATTERNS += [(f"private term {i}", re.compile(re.escape(term.encode(enc)), re.I)) for enc in ("utf-8", "utf-16-le")]


# Files that have no place in a release, whatever they contain: settings and
# state of a deployment, keys, and what a build leaves beside its output.
UNWANTED = re.compile(
    r"(^|/)(\.env(\..*)?|account\.json|google-services\.json|signing\.properties|fcm-service-account\.json|\.DS_Store|\._[^/]*)$"
    r"|\.(db|db-wal|db-shm|sqlite|jks|keystore|pem|p12|key|log|map)$", re.I)


def members(path, data):
    """What is wrong with an archive's list of members, by name alone."""
    if data[:2] == b"\x1f\x8b":
        with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as t:
            entries = [(m.name, m.isfile() or m.isdir()) for m in t.getmembers()]
    elif data[:4] == b"PK\x03\x04":
        with zipfile.ZipFile(path) as z:
            entries = [(name, True) for name in z.namelist()]
    else:
        return
    for name, plain in entries:
        if name.startswith("/") or ".." in name.split("/"):
            yield name, "a path outside the archive"
        elif not plain:
            yield name, "a link or special file"
        elif UNWANTED.search(name):
            yield name, "a file that is never published"


def blobs(path):
    """The file's bytes, and what is packed inside it."""
    data = path.read_bytes()
    yield "", data
    if data[:2] == b"\x1f\x8b":
        # A tar's headers (owner names) are scanned along with its members.
        yield "(unpacked)", gzip.decompress(data)
    elif data[:4] == b"PK\x03\x04":
        with zipfile.ZipFile(path) as z:
            for info in z.infolist():
                yield info.filename, info.filename.encode() + b"\n" + z.read(info)


def scan(paths):
    """(file, member, term) for everything found under paths."""
    # Without our own terms a scan would pass what it exists to stop.
    found = [] if PRIVATE else [("", "", "no private terms: set RA_PUBLIC_DENY or write ~/.config/remote-agent/public-deny")]
    for root in map(Path, paths):
        entries = [root] if not root.is_dir() else sorted(p for p in root.rglob("*") if ".git" not in p.relative_to(root).parts[:1])
        files = []
        for entry in entries:
            # A link is published as the name of what it points at, or as its contents.
            if entry.is_symlink():
                found.append((str(entry), "", "a link"))
            elif entry.is_file():
                files.append(entry)
        for file in files:
            # By name first: a file of a deployment is refused whatever it says.
            if UNWANTED.search(file.name if file == root else file.relative_to(root).as_posix()):
                found.append((str(file), "", "a file that is never published"))
            found += [(str(file), name, why) for name, why in members(file, file.read_bytes())]
            for member, data in blobs(file):
                for term, pattern in PATTERNS:
                    if pattern.search(data) and (str(file), member, term) not in found:
                        found.append((str(file), member, term))
    return found


TARGETS = ("darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64")
# What a build adds to the files of web/public: the page, its headers, the
# notices, and scripts and styles named with the hash of their content.
WEB_BUILT = {"index.html", "_headers", "licenses/THIRD_PARTY_NOTICES.txt"}
WEB_ASSET = re.compile(r"assets/[\w.-]+-[\w-]{8}\.(js|css)")


def web_files(public=None):
    """The files every web archive holds: web/public as it is here, and what a build adds."""
    public = Path(public) if public is not None else Path(__file__).resolve().parents[1] / "web" / "public"
    return WEB_BUILT | {p.relative_to(public).as_posix() for p in public.rglob("*") if p.is_file() and p.name != ".DS_Store"}


def release(tag, dist, web_public=None):
    """What is wrong with a packaged release as a whole: its files are exactly
    the ones a release has, and each archive holds exactly what belongs in it."""
    daemon = {"remote-agent", "remote-agent-service", "THIRD_PARTY_NOTICES.txt"}
    server = {"remote-agent-server", "Dockerfile", ".dockerignore", "compose.yaml", "Caddyfile", "env.example", "THIRD_PARTY_NOTICES.txt"}
    archives = {f"remote-agent-{tag}-{t}": daemon for t in TARGETS}
    archives |= {f"remote-agent-server-{tag}-{t}": server for t in TARGETS if t.startswith("linux")}
    archives[f"remote-agent-web-{tag}"] = None
    expected = {name + ".tar.gz" for name in archives} | {f"remote-agent-{tag}.apk", "SHA256SUMS", "latest.properties"}
    present = {p.name for p in Path(dist).iterdir()}
    found = [(str(Path(dist) / name), "", "not a file of a release") for name in sorted(present - expected)]
    found += [(str(Path(dist) / name), "", "missing from the release") for name in sorted(expected - present)]
    for name, contents in archives.items():
        file = Path(dist) / (name + ".tar.gz")
        if not file.is_file():
            continue
        with tarfile.open(file, "r:gz") as t:
            inside = {m.name for m in t.getmembers() if not m.isdir()}
        if not all(m.startswith(name + "/") for m in inside):
            found.append((str(file), "", "files outside its one directory"))
            continue
        inside = {m[len(name) + 1:] for m in inside}
        if contents is None:
            fixed = web_files(web_public)
            assets = {m for m in inside if WEB_ASSET.fullmatch(m)}
            wrong = sorted((inside - assets) ^ fixed) + ([] if assets else ["assets/"])
        else:
            wrong = sorted(inside ^ contents)
        found += [(str(file), m, "not what this archive holds") for m in wrong]
    return found


def main():
    args = sys.argv[1:]
    found = []
    # --release TAG DIR: also check DIR as that release, complete and exact.
    if args[:1] == ["--release"] and len(args) >= 3:
        found = release(args[1], args[2])
        args = args[2:]
    if not args or args[0].startswith("-"):
        sys.exit(__doc__)
    missing = [p for p in args if not Path(p).exists()]
    if missing:
        sys.exit("Nothing to scan at: " + ", ".join(missing))
    found += scan(args)
    for file, member, term in found:
        print(f"{file}{' › ' + member if member else ''}: {term}", file=sys.stderr)
    if found:
        sys.exit("Not publishable: remove what is listed above")


if __name__ == "__main__":
    main()
