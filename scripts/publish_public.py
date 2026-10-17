#!/usr/bin/env python3
"""Publish scanned assets directly to a public Release; only drafts may change."""
import argparse
import hashlib
import json
import os
import re
import subprocess
import tempfile
from pathlib import Path

import public_scan

ROOT = Path(__file__).resolve().parents[1]
SEMVER = re.compile(r"v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)")


def gh(*args):
    return subprocess.check_output(["gh", *map(str, args)], text=True)


def version(tag):
    if not SEMVER.fullmatch(tag):
        raise ValueError("Expected a vMAJOR.MINOR.PATCH tag")
    return tuple(map(int, tag[1:].split(".")))


def sha256(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def validate(tag, dist, notes, web_public=None):
    version(tag)
    if not notes.is_file() or (web_public is not None and not web_public.is_dir()):
        raise ValueError("Missing release notes or tagged web/public directory")
    findings = public_scan.release(tag, dist, web_public) + public_scan.scan([dist, notes])
    if findings:
        raise ValueError("Not publishable: " + "; ".join(f"{p} {m}: {term}" for p, m, term in findings))
    files = {p.name: p for p in dist.iterdir()}
    checksums = {}
    for line in files["SHA256SUMS"].read_text().splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9.-]+)", line)
        if not match or match[2] in checksums:
            raise ValueError("Malformed or duplicate checksum entry")
        checksums[match[2]] = match[1]
    if set(checksums) != set(files) - {"SHA256SUMS", "latest.properties"}:
        raise ValueError("Checksums must name every payload exactly once")
    if any(sha256(files[name]) != digest for name, digest in checksums.items()):
        raise ValueError("A payload does not match SHA256SUMS")
    manifest = dict(line.split("=", 1) for line in files["latest.properties"].read_text().splitlines() if "=" in line)
    apk = f"remote-agent-{tag}.apk"
    if (manifest.get("version") != tag[1:] or manifest.get("apk") != apk
            or manifest.get("sha256") != checksums[apk]
            or manifest.get("size") != str(files[apk].stat().st_size)):
        raise ValueError("Manifest must describe this version and APK exactly")
    if "daemonUpdate" in manifest:
        if (manifest["daemonUpdate"] != "1" or manifest.get("daemonAutomatic") not in ("true", "false")
                or not re.fullmatch(r"[1-9][0-9]*", manifest.get("daemonState", ""))
                or not re.fullmatch(r"[1-9][0-9]*", manifest.get("protocol", ""))):
            raise ValueError("Invalid daemon update policy")
        for target in public_scan.TARGETS:
            name = f"remote-agent-{tag}-{target}.tar.gz"
            prefix = f"daemon.{target}."
            if (manifest.get(prefix + "sha256") != checksums[name]
                    or manifest.get(prefix + "size") != str(files[name].stat().st_size)):
                raise ValueError("Daemon manifest disagrees with checked release payloads")
    return files


def releases(repo):
    # Enumerating successfully distinguishes an absent release from an
    # authentication/network failure, and considers versions beyond page one.
    pages = json.loads(gh("api", f"repos/{repo}/releases?per_page=100", "--paginate", "--slurp"))
    return [release for page in pages for release in page]


def release_by_id(repo, tag, release_id):
    # The tag endpoint only returns published releases. Drafts are found
    # in the authenticated release list and then read by their stable ID.
    state = json.loads(gh("api", f"repos/{repo}/releases/{release_id}"))
    if state["tag_name"] != tag:
        raise ValueError("Release ID no longer identifies the expected tag")
    return state


def verify(repo, tag, files, release_id):
    state = release_by_id(repo, tag, release_id)
    names = [asset["name"] for asset in state["assets"]]
    if len(names) != len(files) or set(names) != set(files):
        raise ValueError("The remote Release must contain exactly the expected assets")
    # Read the uploaded bytes back, including both control files. The API's
    # digest is not present on older assets, so it cannot be the only check.
    with tempfile.TemporaryDirectory() as tmp:
        gh("release", "download", tag, "--repo", repo, "--dir", tmp)
        if any(sha256(Path(tmp) / name) != sha256(path) for name, path in files.items()):
            raise ValueError("Remote Release assets differ; refusing to publish or overwrite")
    return state


def publish(tag, dist, notes, repo, dry_run=False, web_public=None):
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repo):
        raise ValueError("RELEASES_REPO must be OWNER/REPO")
    files = validate(tag, dist, notes, web_public)
    existing = releases(repo)
    state = next((r for r in existing if r["tag_name"] == tag), None)
    if state and not state["draft"]:
        verified = verify(repo, tag, files, state["id"])
        if verified["draft"] or verified["prerelease"]:
            raise ValueError("Expected a published stable Release")
        print(f"{tag} is already published with exactly these files; unchanged")
        return
    latest = not any(not r["draft"] and not r["prerelease"]
                     and SEMVER.fullmatch(r["tag_name"]) and version(r["tag_name"]) > version(tag)
                     for r in existing)
    if dry_run:
        print(f"DRY_RUN: validated {len(files)} assets for {repo} {tag}; no writes")
        return
    with tempfile.TemporaryDirectory() as tmp:
        public_notes = Path(tmp) / "notes.md"
        public_notes.write_text(notes.read_text() + f"\n---\n\nInstall or upgrade: see the [README](https://github.com/{repo}#readme). Every payload is listed in `SHA256SUMS`.\n")
        findings = public_scan.scan([public_notes])
        if findings:
            raise ValueError("Public release notes failed the privacy scan")
        if state is None:
            request = Path(tmp) / "release.json"
            request.write_text(json.dumps({
                "tag_name": tag, "target_commitish": "main", "draft": True,
                "name": tag, "body": public_notes.read_text(),
            }))
            # Creation already returns the stable ID. A successful list
            # request can still omit the new draft; do not rediscover it there.
            state = json.loads(gh("api", f"repos/{repo}/releases", "--method", "POST", "--input", request))
            if (not isinstance(state, dict) or state.get("tag_name") != tag
                    or type(state.get("id")) is not int or state["id"] <= 0
                    or state.get("draft") is not True):
                raise ValueError("Create response must identify the expected draft Release")
        # Resume an interrupted draft, never change a published version.
        draft = release_by_id(repo, tag, state["id"])
        if not draft["draft"]:
            raise ValueError("Release is no longer a draft; refusing to upload")
        if any(asset["name"] not in files for asset in draft["assets"]):
            raise ValueError("Draft contains unexpected assets; inspect it before retrying")
        gh("release", "upload", tag, *files.values(), "--repo", repo, "--clobber")
        if not verify(repo, tag, files, state["id"])["draft"]:
            raise ValueError("Release became public before verification completed")
        gh("release", "edit", tag, "--repo", repo, "--draft=false", "--prerelease=false",
           f"--latest={str(latest).lower()}", "--title", tag, "--notes-file", public_notes)
        final = release_by_id(repo, tag, state["id"])
        if final["draft"] or final["prerelease"]:
            raise ValueError("Release was not published as stable")
    print(f"Published {tag} at https://github.com/{repo}/releases/tag/{tag}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tag")
    parser.add_argument("dist", nargs="?", type=Path, default=ROOT / "dist")
    parser.add_argument("notes", nargs="?", type=Path)
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--web-public", type=Path, help="Tagged source's web/public inventory when retrying an older build")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory() as tmp:
        notes = args.notes
        if notes is None:
            notes = Path(tmp) / "notes.md"
            notes.write_text(subprocess.check_output(["git", "tag", "--list", args.tag, "--format=%(contents)"], cwd=ROOT, text=True))
        publish(args.tag, args.dist.resolve(), notes.resolve(),
                os.environ.get("RELEASES_REPO", "Lewin671/remote-agent"), args.dry_run, args.web_public)


if __name__ == "__main__":
    main()
