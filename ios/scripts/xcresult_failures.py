#!/usr/bin/env python3
"""Print human-readable test failure assertions from an Xcode .xcresult bundle."""
from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

TOOL_TIMEOUT_SECONDS = 30


def _run_xcresulttool(subcommand: str, bundle: Path) -> dict | None:
    try:
        proc = subprocess.run(
            ["xcrun", "xcresulttool", "get", "test-results", subcommand, "--path", str(bundle)],
            capture_output=True,
            text=True,
            timeout=TOOL_TIMEOUT_SECONDS,
        )
    except subprocess.TimeoutExpired:
        sys.stderr.write(
            f"xcresult_failures: xcresulttool {subcommand} timed out after {TOOL_TIMEOUT_SECONDS}s\n"
        )
        return None
    if proc.returncode != 0:
        detail = (proc.stderr or proc.stdout).strip()
        sys.stderr.write(
            f"xcresult_failures: xcresulttool {subcommand} exited {proc.returncode}: {detail}\n"
        )
        return None
    try:
        return json.loads(proc.stdout)
    except json.JSONDecodeError as exc:
        sys.stderr.write(f"xcresult_failures: invalid JSON from xcresulttool {subcommand}: {exc}\n")
        return None


def _format_location(loc: dict | None) -> str:
    if not isinstance(loc, dict):
        return ""
    path = loc.get("filePath") or ""
    line = loc.get("lineNumber")
    if not path:
        return ""
    return f"{path}:{line}" if isinstance(line, int) and line > 0 else path


def _collect_locations(nodes: list[dict], out: dict[str, list[str]], parent_keys: tuple[str, ...] = ()) -> None:
    for node in nodes:
        if not isinstance(node, dict):
            continue
        keys = tuple(
            k for k in (node.get("nodeIdentifierURL"), node.get("nodeIdentifier"), node.get("name")) if k
        )
        loc = _format_location(node.get("sourceLocation"))
        if loc:
            node_type = (node.get("nodeType") or "").lower()
            target_keys = parent_keys if ("failure" in node_type and parent_keys) else keys
            for k in target_keys:
                bucket = out.setdefault(k, [])
                if loc not in bucket:
                    bucket.append(loc)
        children = node.get("children") or []
        if children:
            _collect_locations(children, out, keys or parent_keys)


def format_summary(data: dict, locations: dict[str, list[str]] | None = None) -> str:
    lines = []
    result = data.get("result", "unknown")
    passed = data.get("passedTests", 0)
    failed = data.get("failedTests", 0)
    skipped = data.get("skippedTests", 0)
    lines.append(
        f"xcresult summary: result={result} passed={passed} failed={failed} skipped={skipped}"
    )
    failures = data.get("testFailures") or []
    if not failures:
        lines.append("xcresult summary: no testFailures entries recorded")
        return "\n".join(lines) + "\n"
    locs = locations or {}
    for item in failures:
        ident = item.get("testIdentifierString") or item.get("testName") or "<unknown test>"
        target = item.get("targetName") or ""
        found_locs = (
            locs.get(item.get("testIdentifierURL") or "")
            or locs.get(item.get("testIdentifierString") or "")
            or locs.get(item.get("testName") or "")
            or []
        )
        loc_suffix = f" [{', '.join(found_locs)}]" if found_locs else ""
        header = f"FAIL {ident}" + (f" ({target})" if target else "") + loc_suffix
        lines.append(header)
        text = (item.get("failureText") or "").strip()
        if text:
            for line in text.splitlines():
                lines.append(f"  {line}")
    return "\n".join(lines) + "\n"


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        sys.stderr.write(f"usage: {argv[0]} <path-to-.xcresult>\n")
        return 2
    bundle = Path(argv[1])
    if not bundle.exists():
        sys.stderr.write(
            f"xcresult_failures: result bundle not found (build likely failed before tests ran): {bundle}\n"
        )
        return 0
    summary = _run_xcresulttool("summary", bundle)
    if summary is None:
        return 0
    locations: dict[str, list[str]] = {}
    if summary.get("testFailures"):
        tests_data = _run_xcresulttool("tests", bundle)
        if isinstance(tests_data, dict):
            _collect_locations(tests_data.get("testNodes") or [], locations)
    sys.stdout.write(format_summary(summary, locations))
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
