#!/bin/sh
# worktree.sh: create an isolated git worktree according to AGENTS.md conventions,
# warm up web dependencies, and print targeted testing guidance.
# Usage: scripts/dev/worktree.sh <task_name>
set -eu

repo_root=$(git rev-parse --show-toplevel)
common_dir=$(git rev-parse --git-common-dir)
main_project=$(cd "$common_dir/.." && pwd)
workspace_root=$(cd "$main_project/../.." && pwd)

die() {
	echo "worktree.sh: $1" >&2
	exit 1
}

task="${1:-}"
if [ -z "$task" ]; then
	die "missing task name. Usage: $0 <task_name>"
fi

# Sanitize task name (alphanumeric, dashes, underscores)
case "$task" in
wt/*) task="${task#wt/}" ;;
esac
clean_task=$(printf '%s' "$task" | tr -c 'a-zA-Z0-9._-' '-')
if [ "$clean_task" != "$task" ]; then
	echo "worktree.sh: sanitized task name to '$clean_task'"
	task="$clean_task"
fi

branch="wt/$task"
target_dir="$workspace_root/worktrees/remote-agent/$task"

if [ -d "$target_dir" ]; then
	die "worktree directory already exists: $target_dir"
fi

if git show-ref --verify --quiet "refs/heads/$branch"; then
	die "branch '$branch' already exists. Delete it with 'git branch -D $branch' or pick another name."
fi

echo "==> [1/3] Fetching origin/main..."
git fetch origin main >/dev/null 2>&1 || echo "warning: git fetch failed; using existing origin/main"

echo "==> [2/3] Creating worktree at $target_dir (branch $branch)..."
mkdir -p "$(dirname "$target_dir")"
git worktree add -b "$branch" "$target_dir" origin/main

echo "==> [3/3] Warming up web dependencies..."
if command -v pnpm >/dev/null 2>&1 && [ -f "$target_dir/web/pnpm-lock.yaml" ]; then
	echo "    Installing web dependencies via pnpm store (offline / hard-links)..."
	(cd "$target_dir/web" && pnpm install --prefer-offline --frozen-lockfile >/dev/null 2>&1) || true
	echo "    Web node_modules is hot and ready."
else
	echo "    Web node_modules skipped (pnpm unavailable or no lockfile)."
fi

echo
echo "============================================================"
echo "  Worktree created: $target_dir"
echo "  Branch:           $branch"
echo "============================================================"
echo
echo "=== Cache Reuse & Isolation Guidelines ==="
echo "1. Gradle (Android):"
echo "   - SAFE to reuse: global cache (~/.gradle/caches) and running daemon (--build-cache)."
echo "   - DO NOT share/symlink: android/.gradle/ or build/ directories (corrupts cache across trees)."
echo "2. SwiftPM (iOS):"
echo "   - SAFE to reuse: Xcode DerivedData (~/Library/Developer/Xcode/DerivedData) and SPM cache."
echo "   - DO NOT share/symlink: .build/ directories (embeds worktree-specific absolute paths)."
echo
echo "=== Targeted Testing Commands (Only test what you touched!) ==="
echo "  Web:"
echo "    cd web && pnpm test src/views/flows.test.tsx      # single flow test"
echo "    cd web && pnpm typecheck                          # typecheck only"
echo "    cd web && pnpm dev --host 127.0.0.1 --port 0      # local preview"
echo "  Go:"
echo "    go test -v ./internal/daemon/...                  # single package"
echo "    go test -v -run TestFoo ./internal/...            # single test"
echo "    # Reserve full 'GOFLAGS=-p=1 go test ./...' for final delivery!"
echo "  Android:"
echo "    cd android && ./gradlew --build-cache :core:testDebugUnitTest"
echo "    cd android && ./gradlew --build-cache :terminal:testReleaseUnitTest"
echo "  iOS:"
echo "    cd ios/RemoteAgentCore && swift test              # core unit tests"
echo "    cd ios/RemoteAgentCore && swift test --filter <TestName>"
echo
echo "=== Development Lifecycle ==="
echo "  1. Check environment:    scripts/dev/preflight.sh"
echo "  2. Start isolated E2E:   scripts/dev/e2e-up.sh"
echo "  3. Stop isolated E2E:    scripts/dev/e2e-down.sh"
echo "  4. Cleanup worktree:     git worktree remove $target_dir && git branch -D $branch"
echo "============================================================"
