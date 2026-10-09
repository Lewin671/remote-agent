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
printf '%s' "$secrets" | python3 -c 'import json,sys; names={s["name"] for s in json.load(sys.stdin)}; missing={"ANDROID_SIGNING", "ANDROID_GOOGLE_SERVICES", "RELEASES_DEPLOY_KEY"}-names; sys.exit("Configure GitHub release secrets first: " + ", ".join(sorted(missing))) if missing else None'
git fetch origin
# Rebase conflicts stop here for the agent to report, never resolve automatically.
git rebase origin/main
result=$(python3 scripts/release.py prepare "$kind")
if [ "$(printf '%s' "$result" | python3 -c 'import json,sys; print(json.load(sys.stdin)["eligible"])')" = False ]; then
    printf '%s\n' "$result"
    exit 0
fi
tag=$(printf '%s' "$result" | python3 -c 'import json,sys; print(json.load(sys.stdin)["tag"])')
git add version.properties
git commit -m "Release $tag" -m "$summary"
python3 -m unittest discover -s scripts -p '*_test.py'
go test ./...
git tag -a "$tag" -m "$summary"
python3 scripts/release.py validate "$tag"
# If main moved during tests this fails without publishing either ref.
# Do not move a pushed tag. Report conflicts; do not force push.
git push --atomic origin HEAD:main "refs/tags/$tag"
printf 'Pushed %s; monitor the Release workflow before reporting success.\n' "$tag"
