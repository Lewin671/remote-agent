#!/bin/sh
# Run automatically by the agent once the user's feature/fix task is complete and verified.
# Usage: scripts/release-task.sh patch|minor|major "release and upgrade summary"
# GitHub performs the full cross-platform checks before publishing the tag.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
kind=${1:?usage: release-task.sh patch|minor|major summary}
summary=${2:?provide the release changes and any upgrade requirements}
if [ -n "$(git status --porcelain)" ]; then
    echo "Commit task changes before release; the worktree must be clean" >&2
    exit 1
fi
# Verify setup before changing a version or pushing a tag. Names only, never values.
secrets=$(gh secret list --json name)
printf '%s' "$secrets" | python3 -c 'import json,sys; names={s["name"] for s in json.load(sys.stdin)}; missing={"ANDROID_SIGNING", "ANDROID_GOOGLE_SERVICES", "RA_PUBLIC_DENY"}-names; sys.exit("Configure GitHub release secrets first: " + ", ".join(sorted(missing))) if missing else None'
git fetch origin
# Rebase only when main has moved: rebasing onto an ancestor would flatten the
# branch's merge commits and replay them as conflicts.
# Rebase conflicts stop here for the agent to report, never resolve automatically.
if ! git merge-base --is-ancestor origin/main HEAD; then
    git rebase origin/main
fi
# The daemon's built-in model catalog must match the one Claude Code publishes.
# It is fetched only through a proxy on this machine (--proxy or HTTPS_PROXY);
# when it is out of date, update and commit it first. RA_SKIP_CATALOG_CHECK=1
# releases without the check.
if [ "${RA_SKIP_CATALOG_CHECK:-}" != 1 ]; then
    python3 scripts/model_catalog.py check || {
        echo "Model catalog check failed: run python3 scripts/model_catalog.py update, commit it, and release again" >&2
        exit 1
    }
fi
python3 scripts/i18n.py generate --check
python3 scripts/i18n.py lint
result=$(python3 scripts/release.py prepare "$kind")
if [ "$(printf '%s' "$result" | python3 -c 'import json,sys; print(json.load(sys.stdin)["eligible"])')" = False ]; then
    printf '%s\n' "$result"
    exit 0
fi
tag=$(printf '%s' "$result" | python3 -c 'import json,sys; print(json.load(sys.stdin)["tag"])')
git add version.properties
git commit -m "Release $tag" -m "$summary"
# The tests of the release tools decide eligibility themselves. Run slow modules
# in parallel while keeping public_scan_test before publish_public_test so the
# test-only RA_PUBLIC_DENY fixture initializes before publish_public imports it.
env -u RA_REPUBLISH PYTHONPATH="$ROOT/scripts" python3 - <<'PY'
import concurrent.futures, glob, os, subprocess, sys

paired = ["public_scan_test", "publish_public_test"]
all_mods = [os.path.splitext(os.path.basename(p))[0] for p in sorted(glob.glob("scripts/*_test.py"))]
groups = [paired] + [[m] for m in all_mods if m not in paired]
with concurrent.futures.ThreadPoolExecutor(max_workers=min(8, len(groups))) as pool:
    results = list(pool.map(
        lambda mods: (mods, subprocess.run([sys.executable, "-m", "unittest", *mods], capture_output=True, text=True)),
        groups,
    ))
failed = False
for mods, res in results:
    if res.returncode != 0:
        failed = True
        sys.stderr.write(f"=== {' '.join(mods)} ===\n{res.stdout}{res.stderr}\n")
if failed:
    sys.exit(1)
print(f"scripts tests: {len(all_mods)} modules passed")
PY
# Release binaries are built with CGO_ENABLED=0 (scripts/build-dist.sh); test the same way,
# so a local C toolchain without a usable SDK cannot fail the release. Omit -count=1 so
# repeated local runs reuse Go's build and test cache for unchanged packages.
CGO_ENABLED=0 go test -parallel=8 ./...
git tag -a "$tag" -m "$summary"
python3 scripts/release.py validate "$tag"
# If main moved during tests this fails without publishing either ref.
# Do not move a pushed tag. Report conflicts; do not force push.
git push --atomic origin HEAD:main "refs/tags/$tag"
printf 'Pushed %s; monitor the Release workflow before reporting success.\n' "$tag"
