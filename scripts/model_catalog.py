#!/usr/bin/env python3
"""Keep the model catalog built into the daemon in step with Claude Code's.

Claude Code publishes the catalog its /model menu is built from. The daemon
carries a trimmed copy (internal/claude/builtin-catalog.json) so that it
never has to fetch one while running; this script refreshes that copy, and
release-task.sh runs `check` before every release.

    python3 scripts/model_catalog.py check  [--proxy URL]   # exit 1 when the copy is out of date
    python3 scripts/model_catalog.py update [--proxy URL]   # rewrite the copy

The catalog is only ever fetched through an HTTP proxy on this machine
(127.0.0.1, ::1 or localhost): --proxy, or else HTTPS_PROXY / https_proxy.
Without one the script stops; it never connects directly. The request is a
CONNECT tunnel through that proxy, so NO_PROXY cannot route it around it.
"""

import argparse
import http.client
import ipaddress
import json
import os
import sys
import urllib.parse

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TARGET = os.path.join(ROOT, "internal", "claude", "builtin-catalog.json")
HOST = "downloads.claude.ai"
PATH = "/model-catalog/v1/catalog.json"
SOURCE = f"https://{HOST}{PATH}"
SCHEMA_VERSION = 1
SURFACE = "cc"


class CatalogError(Exception):
    pass


def local_proxy(arg, env=os.environ):
    """The (host, port) of the local HTTP proxy to fetch through."""
    url = arg or env.get("HTTPS_PROXY") or env.get("https_proxy")
    if not url:
        raise CatalogError("no proxy: pass --proxy http://127.0.0.1:PORT or set HTTPS_PROXY; the catalog is never fetched directly")
    p = urllib.parse.urlsplit(url if "://" in url else "http://" + url)
    if p.scheme != "http":
        raise CatalogError(f"proxy {url!r}: only an http:// proxy is supported")
    host = p.hostname or ""
    try:
        local = ipaddress.ip_address(host).is_loopback
    except ValueError:
        local = host == "localhost"
    if not local:
        raise CatalogError(f"proxy {url!r} is not on this machine; use one on 127.0.0.1, ::1 or localhost")
    if not p.port:
        raise CatalogError(f"proxy {url!r} has no port")
    return host, p.port


def fetch(proxy):
    host, port = proxy
    conn = http.client.HTTPSConnection(host, port, timeout=30)
    conn.set_tunnel(HOST, 443)
    try:
        conn.request("GET", PATH, headers={"User-Agent": "remote-agent-model-catalog"})
        r = conn.getresponse()
        body = r.read()
    except OSError as e:
        raise CatalogError(f"fetch {SOURCE} through proxy {host}:{port}: {e}") from e
    finally:
        conn.close()
    if r.status != 200:
        raise CatalogError(f"fetch {SOURCE}: HTTP {r.status}")
    try:
        return json.loads(body)
    except ValueError as e:
        raise CatalogError(f"{SOURCE} is not JSON: {e}") from e


def trim_row(m):
    """The fields the daemon reads, in the shape it reads them."""
    runtime = m.get("runtime") or {}
    efforts = runtime.get("effort_levels")
    if not efforts:
        thinking = m.get("thinking") or {}
        efforts = [o["id"] for o in thinking.get("effort_options") or [] if o.get("id")]
    row = {"id": m["id"], "name": m["name"]}
    for k in ("short_name", "description", "section", "min_claude_code_version", "offered_on", "auto_permission_mode"):
        if m.get(k):
            row[k] = m[k]
    if m.get("disabled"):
        row["disabled"] = True
    rt = {}
    if efforts:
        rt["effort_levels"] = list(efforts)
        if runtime.get("default_effort"):
            rt["default_effort"] = runtime["default_effort"]
    if runtime.get("max_input_tokens"):
        rt["max_input_tokens"] = runtime["max_input_tokens"]
    if rt:
        row["runtime"] = rt
    return row


def trim(doc):
    """The built-in copy of the published catalog: Claude Code's terminal
    surface only, each row cut to what the daemon uses."""
    if doc.get("schema_version") != SCHEMA_VERSION:
        raise CatalogError(f"catalog schema_version {doc.get('schema_version')!r}, this script reads {SCHEMA_VERSION}; check the format before updating")
    configs = ((doc.get("surfaces") or {}).get(SURFACE) or {}).get("model_selector_config") or []
    config = next((c for c in configs if c.get("id") == SURFACE), None)
    if not config or not config.get("models"):
        raise CatalogError(f"catalog has no models for surface {SURFACE!r}")
    rows = []
    for m in config["models"]:
        if not m.get("id") or not m.get("name"):
            raise CatalogError(f"catalog row without an id or a name: {m!r:.200}")
        rows.append(trim_row(m))
    return {"source": SOURCE, "issued_at": doc.get("issued_at"), "version": doc.get("version"), "models": rows}


def diff(old, new):
    """Lines describing how the rows changed; empty when they did not.
    issued_at and version are not compared: the catalog is re-issued with
    the same rows."""
    before = {m["id"]: m for m in (old or {}).get("models", [])}
    after = {m["id"]: m for m in new["models"]}
    out = [f"+ {i} ({after[i]['name']})" for i in after if i not in before]
    out += [f"- {i} ({before[i]['name']})" for i in before if i not in after]
    out += [f"~ {i}" for i in after if i in before and before[i] != after[i]]
    order_old = [i for i in before if i in after]
    order_new = [i for i in after if i in before]
    if not out and order_old != order_new:
        out.append("order: " + ", ".join(order_new))
    return out


def read_target():
    try:
        with open(TARGET, encoding="utf-8") as f:
            return json.load(f)
    except FileNotFoundError:
        return None


def write_target(cat):
    with open(TARGET, "w", encoding="utf-8") as f:
        json.dump(cat, f, ensure_ascii=False, indent=2)
        f.write("\n")


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("command", choices=("check", "update"))
    ap.add_argument("--proxy", help="local HTTP proxy, e.g. http://127.0.0.1:7890 (default: HTTPS_PROXY)")
    args = ap.parse_args(argv)
    try:
        new = trim(fetch(local_proxy(args.proxy)))
    except CatalogError as e:
        print(f"model_catalog: {e}", file=sys.stderr)
        return 2
    old = read_target()
    changes = diff(old, new)
    rel = os.path.relpath(TARGET, ROOT)
    if args.command == "check":
        if not changes:
            print(f"{rel} matches the published catalog ({new['issued_at']})")
            return 0
        print(f"{rel} is out of date:", *changes, sep="\n  ")
        print("run: python3 scripts/model_catalog.py update, then commit it")
        return 1
    if not changes and old is not None:
        print(f"{rel} already matches the published catalog; left as it is")
        return 0
    write_target(new)
    print(f"updated {rel} to the catalog issued {new['issued_at']}:", *(changes or ["(new file)"]), sep="\n  ")
    return 0


if __name__ == "__main__":
    sys.exit(main())
