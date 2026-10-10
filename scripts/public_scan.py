#!/usr/bin/env python3
"""Refuses to publish files that name our own deployment or carry a key.

Usage: scripts/public_scan.py PATH...

Everything handed to the public repository goes through this first: the
release's files and that repository's own pages. Archives and the APK are
read member by member, and text is looked for in UTF-16 as well, which is
how an APK stores its resources. This repository is private, so the list
below may name what must stay out.
"""
import gzip
import io
import re
import sys
import tarfile
import zipfile
from pathlib import Path

# Our hosts, addresses and machines; a CI runner's paths; private keys.
DENIED = [
    "workstation",
    "example-cn-vps",
    "foxmail",
    "alibabaweijun",
    "DEVICE001",
    "/home/runner/",
    "PRIVATE KEY-----",
    "RA_SETUP_TOKEN=[A-Za-z0-9_-]{8,}",
]
PATTERNS = [(term, re.compile(term.encode(), re.I)) for term in DENIED]
PATTERNS += [(term, re.compile(term.encode("utf-16-le"), re.I)) for term in DENIED if term.isascii() and not re.search(r"[\[{]", term)]


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
    found = []
    for root in map(Path, paths):
        files = [root] if root.is_file() else sorted(p for p in root.rglob("*") if p.is_file() and ".git" not in p.relative_to(root).parts[:1])
        for file in files:
            found += [(str(file), name, why) for name, why in members(file, file.read_bytes())]
            for member, data in blobs(file):
                for term, pattern in PATTERNS:
                    if pattern.search(data) and (str(file), member, term) not in found:
                        found.append((str(file), member, term))
    return found


def main():
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    missing = [p for p in sys.argv[1:] if not Path(p).exists()]
    if missing:
        sys.exit("Nothing to scan at: " + ", ".join(missing))
    found = scan(sys.argv[1:])
    for file, member, term in found:
        print(f"{file}{' › ' + member if member else ''}: {term}", file=sys.stderr)
    if found:
        sys.exit("Not publishable: remove what is listed above")


if __name__ == "__main__":
    main()
