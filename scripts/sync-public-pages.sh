#!/bin/sh
# Makes the public repository's main branch the content of
# deploy/releases-repo/: its pages, the installer, the self-hosting
# templates. Run by the Public pages workflow whenever
# that directory changes on main, so what people read and run there is what
# was tested here; no release is needed for it.
#
# Usage: scripts/sync-public-pages.sh
# RELEASES_DEPLOY_KEY is the private half of that repository's deploy key;
# RELEASES_REPO overrides where it is pushed. DRY_RUN=1 stops before pushing.
#
# The tree is scanned before anything is pushed. The push is an ordinary
# one: if that branch has moved since it was cloned, this fails rather than
# overwrite it.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
repo=${RELEASES_REPO:-git@github.com:Lewin671/remote-agent-releases.git}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
if [ -n "${RELEASES_DEPLOY_KEY:-}" ]; then
	(umask 077 && printf '%s\n' "$RELEASES_DEPLOY_KEY" >"$work/key")
	export GIT_SSH_COMMAND="ssh -i $work/key -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"
fi
git clone -q --depth 1 --branch main "$repo" "$work/repo"

# Only what git tracks here goes over: nothing a build or an editor left in the directory.
mkdir "$work/pages"
git -C "$ROOT" archive HEAD:deploy/releases-repo | tar -x -C "$work/pages"
python3 "$ROOT/scripts/public_scan.py" "$work/pages"

cd "$work/repo"
git rm -rq --ignore-unmatch .
cp -R "$work/pages/." .
git add -A
if git diff --cached --quiet; then
	echo "The public repository already has these pages"
	exit 0
fi
git diff --cached --stat
# The owner's public address at GitHub, never the one of whoever runs this.
# The message says nothing of the change here: commit subjects of this repository stay in it.
git -c user.name="Lewin671" -c user.email="37481138+Lewin671@users.noreply.github.com" commit -q -m "Update the pages"
if [ -n "${DRY_RUN:-}" ]; then
	echo "DRY_RUN: not pushed"
	exit 0
fi
git push -q "$repo" HEAD:main
echo "Updated the pages of $repo"
