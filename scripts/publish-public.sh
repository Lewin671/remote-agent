#!/bin/sh
# Publish a release to RELEASES_REPO (default: this repository) with GH_TOKEN.
# Usage: publish-public.sh TAG [DIST [NOTES]] [--dry-run]
# DIST and NOTES may come from a tagged checkout while this tool comes
# from the current workflow revision, so an old release can be retried.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
exec python3 "$ROOT/scripts/publish_public.py" "$@"
