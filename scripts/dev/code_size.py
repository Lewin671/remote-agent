#!/usr/bin/env python3
"""Count hand-written product and test lines per client (read-only dev tool).

Acceptance metric for moving client logic into the shared core (clientcore):
the product lines of web + android + ios + clientcore should shrink, and the
record should state each client's own change.

  code_size.py                       current working tree (tracked + untracked, not ignored)
  code_size.py --ref v0.93.0         a commit/tag, read straight from the git object store
  code_size.py --base v0.81.3        compare --base with the working tree and show deltas
  code_size.py --ref A --base B      compare B (base) with A
  code_size.py --json ...            machine-readable output

Counts physical lines (blank lines and comments included), so the numbers
move with code volume rather than style. Nothing is written and no network is
used. Rules live in TARGETS below; the unit test is scripts/code_size_test.py.
"""
import argparse
import json
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

# --- Counting rules (the only place they are defined) -----------------------
# Generated code is excluded from both product and test counts: it is emitted
# by protoc/buf/scripts/i18n.py from schemas, so it grows and shrinks with the
# schema rather than with how much logic a client maintains, and including it
# would hide or fake a real migration. A path is classified in this order:
# generated or excluded -> ignored; test -> "test"; otherwise -> "product".
# Matching is case-sensitive on paths except the "generated" filename check.


def _generated_name(path):
    return "generated" in path.rsplit("/", 1)[-1].lower()


class Target:
    def __init__(self, name, prefixes, exts, ignored, test, reference=False):
        self.name, self.prefixes, self.exts = name, prefixes, exts
        self.ignored, self.test, self.reference = ignored, test, reference

    def classify(self, path):
        """Return "product", "test" or None (not this target / excluded)."""
        if not path.startswith(self.prefixes) or not path.endswith(self.exts) or self.ignored(path):
            return None
        return "test" if self.test(path) else "product"


TARGETS = [
    # Web: web/src .ts/.tsx; web/src/dev is dev-only scaffolding, not shipped UI.
    Target("web", ("web/src/",), (".ts", ".tsx"),
           ignored=lambda p: p.startswith(("web/src/gen/", "web/src/i18n/generated", "web/src/dev/")),
           test=lambda p: ".test." in p.rsplit("/", 1)[-1]),
    # Android: any .kt; "/test" covers src/test, androidTest is named explicitly.
    Target("android", ("android/",), (".kt",),
           ignored=lambda p: "/gen/" in p or _generated_name(p),
           test=lambda p: "/test" in p or "/androidTest/" in p),
    # iOS: any .swift; SwiftPM .build, protobuf output and generated wrappers are out.
    Target("ios", ("ios/",), (".swift",),
           ignored=lambda p: "/.build/" in p or p.endswith(".pb.swift") or _generated_name(p),
           test=lambda p: "Tests" in p),
    # Shared Go core.
    Target("clientcore", ("clientcore/",), (".go",),
           ignored=lambda p: "/gen/" in p,
           test=lambda p: p.endswith("_test.go")),
    # Root module (daemon, server, CLI): reference only, not part of the client total.
    Target("root-go", ("cmd/", "internal/", "shared/"), (".go",),
           ignored=lambda p: "/gen/" in p or p.endswith(".pb.go") or _generated_name(p),
           test=lambda p: p.endswith("_test.go"), reference=True),
]
# ----------------------------------------------------------------------------

PREFIXES = tuple(prefix for t in TARGETS for prefix in t.prefixes)


def git(*args, input=None, text=True):
    return subprocess.run(["git", "-C", str(ROOT), *args], input=input, check=True,
                          stdout=subprocess.PIPE, text=text).stdout


def physical_lines(data):
    return data.count(b"\n") + (1 if data and not data.endswith(b"\n") else 0)


def classify(path):
    for target in TARGETS:
        kind = target.classify(path)
        if kind:
            return target.name, kind
    return None


def tree_blobs(ref):
    """(path, sha) of regular files under the counted prefixes in a commit."""
    out = git("ls-tree", "-r", "-z", ref, "--", *[p.rstrip("/") for p in PREFIXES])
    for entry in filter(None, out.split("\0")):
        meta, path = entry.split("\t", 1)
        mode, kind, sha = meta.split()
        if kind == "blob" and mode != "120000" and classify(path):
            yield path, sha


def count_ref(ref):
    blobs = list(tree_blobs(ref))
    # One cat-file process for every blob: "<sha> blob <size>\n<bytes>\n" records.
    out = subprocess.run(["git", "-C", str(ROOT), "cat-file", "--batch"], input="".join(s + "\n" for _, s in blobs).encode(),
                         check=True, stdout=subprocess.PIPE).stdout
    pos, items = 0, []
    for path, _ in blobs:
        end = out.index(b"\n", pos)
        size = int(out[pos:end].split()[2])
        items.append((path, out[end + 1:end + 1 + size]))
        pos = end + 1 + size + 1
    return items


def count_worktree():
    names = git("ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", *[p.rstrip("/") for p in PREFIXES]).split("\0")
    items = []
    for path in sorted(set(filter(None, names))):
        full = ROOT / path
        if classify(path) and full.is_file() and not full.is_symlink():
            items.append((path, full.read_bytes()))
    return items


def summarize(items):
    result = {t.name: {"product": 0, "test": 0, "product_files": 0, "test_files": 0} for t in TARGETS}
    for path, data in items:
        name, kind = classify(path)
        result[name][kind] += physical_lines(data)
        result[name][kind + "_files"] += 1
    total = {k: sum(result[t.name][k] for t in TARGETS if not t.reference) for k in ("product", "test", "product_files", "test_files")}
    return {"clients": result, "clients_total": total}


def measure(ref):
    if ref is None:
        return {"ref": None, "commit": None, **summarize(count_worktree())}
    commit = git("rev-parse", "--verify", "--quiet", ref + "^{commit}").strip()
    return {"ref": ref, "commit": commit, **summarize(count_ref(commit))}


def rows(report):
    for t in TARGETS:
        yield t.name + (" (ref)" if t.reference else ""), report["clients"][t.name]
    yield "clients total", report["clients_total"]


def deltas(head, base):
    out = {t.name: {k: head["clients"][t.name][k] - base["clients"][t.name][k] for k in ("product", "test")} for t in TARGETS}
    out["clients_total"] = {k: head["clients_total"][k] - base["clients_total"][k] for k in ("product", "test")}
    return out


def label(report):
    return "worktree" if report["ref"] is None else f'{report["ref"]} ({report["commit"][:9]})'


def table(head, base):
    lines = []
    if base:
        lines += [f"base: {label(base)}", f"head: {label(head)}", ""]
        lines.append(f'{"":<18}{"----- product -----":>33}  {"------- test -------":>33}')
        lines.append(f'{"":<18}{"base":>10}{"head":>10}{"delta":>10}{"":>3}{"base":>10}{"head":>10}{"delta":>10}')
        for (name, h), (_, b) in zip(rows(head), rows(base)):
            lines.append(f"{name:<18}" + "".join(f"{b[k]:>10,}{h[k]:>10,}{h[k] - b[k]:>+10,}{'':>3}" for k in ("product", "test")).rstrip())
    else:
        lines += [f"source: {label(head)}", "", f'{"":<18}{"product":>10}{"test":>10}']
        for name, h in rows(head):
            lines.append(f'{name:<18}{h["product"]:>10,}{h["test"]:>10,}')
    lines += ["", "Physical lines (comments and blanks included); generated code excluded; root-go is reference only."]
    return "\n".join(lines)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0] + " Counts physical lines (comments and blanks included).",
                                     formatter_class=argparse.RawDescriptionHelpFormatter,
                                     epilog="Rules: see TARGETS at the top of this script.")
    parser.add_argument("--ref", help="git ref to measure instead of the working tree")
    parser.add_argument("--base", help="git ref to compare against; prints both sides and the change")
    parser.add_argument("--json", action="store_true", help="print machine-readable JSON")
    args = parser.parse_args(argv)
    try:
        head = measure(args.ref)
        base = measure(args.base) if args.base else None
    except subprocess.CalledProcessError as error:
        print(f"git failed: {' '.join(map(str, error.cmd))}", file=sys.stderr)
        return 2
    if args.json:
        report = {"head": head, "base": base}
        if base:
            report["delta"] = deltas(head, base)
        print(json.dumps(report, indent=2))
    else:
        print(table(head, base))
    return 0


if __name__ == "__main__":
    sys.exit(main())
