#!/usr/bin/env python3
"""Writes the license texts of what a distributed build contains.

Usage: scripts/notices.py go PACKAGE     (GOOS and GOARCH pick the platform)
       scripts/notices.py web

The binaries and the web bundle carry other people's code, whose licenses
ask that their text goes along. Only what is linked or bundled is listed,
by name and version: no path of the machine that built it.
"""
import json
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
TEXTS = ("LICENSE", "LICENCE", "COPYING", "NOTICE")


def texts(directory):
    """The license files at the top of a module or package, in a fixed order."""
    files = sorted(p for p in Path(directory).iterdir() if p.is_file() and p.name.upper().startswith(TEXTS))
    return "\n".join(p.read_text(errors="replace").strip() for p in files)


def section(name, text):
    if not text:
        sys.exit(f"{name} has no license text to include")
    return f"{'=' * 78}\n{name}\n{'=' * 78}\n\n{text}\n\n"


def go(package):
    listed = subprocess.check_output(
        ["go", "list", "-deps", "-f", "{{with .Module}}{{if not .Main}}{{.Path}}@{{.Version}}\t{{.Dir}}{{end}}{{end}}", package],
        cwd=ROOT, text=True)
    modules = sorted(set(line for line in listed.splitlines() if line))
    goroot = subprocess.check_output(["go", "env", "GOROOT"], text=True).strip()
    out = section("Go and its standard library", texts(goroot))
    for line in modules:
        name, directory = line.split("\t")
        out += section(name, texts(directory))
    return out


def web():
    listed = json.loads(subprocess.check_output(["pnpm", "licenses", "list", "--prod", "--json"], cwd=ROOT / "web", text=True))
    packages = sorted((p["name"], p) for group in listed.values() for p in group)
    out = ""
    for name, p in packages:
        for version, directory in sorted(zip(p["versions"], p["paths"])):
            # Some packages are published without the file; what they declare stands in for it.
            text = texts(directory) or f"Published without a license file. Declared license: {p['license']}. Source: {p.get('homepage') or 'https://www.npmjs.com/package/' + name}"
            out += section(f"{name}@{version} ({p['license']})", text)
    return out


def main():
    if sys.argv[1:2] == ["go"] and len(sys.argv) == 3:
        print(go(sys.argv[2]), end="")
    elif sys.argv[1:] == ["web"]:
        print(web(), end="")
    else:
        sys.exit(__doc__)


if __name__ == "__main__":
    main()
