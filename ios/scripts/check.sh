#!/bin/sh
# Checks the iOS client in one go:
#
#   1. the package's tests (swift test in RemoteAgentCore, on the Mac);
#   2. that the generated protocol code is what `buf generate` writes now;
#   3. the app: built and its tests run on a simulator.
#
# It says nothing when all of it passes. When a step fails it prints that
# step's output and stops.
#
#   IOS_BUILD_JOBS    compiler jobs (default 4, for shared development machines).
#   IOS_DESTINATION   the xcodebuild destination; by default the iPhone 18
#                     Pro simulator, or the first iPhone simulator there is
#                     when this Mac has none of that name.
# With no argument it runs every step except UI automation: the touch tests
# drive a simulator for minutes, stall when the machine is busy, and only tell
# something new when the app's views or the tests themselves changed. Run
# `check.sh ui` for those changes and before an App Store submission, one
# simulator at a time; `check.sh full` is everything. `core` and `app` are for
# iterating.
set -u
mode=${1:-std}
case "$mode" in std|full|core|app|ui) ;; *) echo "usage: check.sh [core|app|ui|full]" >&2; exit 2 ;; esac

ios=$(cd "$(dirname "$0")/.." && pwd)
out=$(mktemp -t ra-ios-check)
trap 'rm -f "$out"' EXIT

fail() {
    echo "check.sh: $1" >&2
    cat "$out" >&2
    exit 1
}

# Validate the checked-in catalog and its native compiler output too.
python3 "$ios/../scripts/i18n.py" generate --check >"$out" 2>&1 || fail "localized resources are stale"
resources=$(mktemp -d -t ra-ios-messages)
xcrun xcstringstool compile "$ios/RemoteAgentCore/Localization/Localizable.xcstrings" --output-directory "$resources" >"$out" 2>&1
compiled=$?
rm -rf "$resources"
[ "$compiled" = 0 ] || fail "the String Catalog did not compile"

# 1
if [ "$mode" = std ] || [ "$mode" = full ] || [ "$mode" = core ]; then
    set --
    [ -z "${IOS_SWIFT_BUILD_PATH:-}" ] || set -- --scratch-path "$IOS_SWIFT_BUILD_PATH"
    (cd "$ios/RemoteAgentCore" && swift test --jobs "${IOS_BUILD_JOBS:-4}" "$@") >"$out" 2>&1 || fail "the package's tests failed (swift test in ios/RemoteAgentCore)"
fi

# 2: acceptance always verifies generation, regardless of changed paths.
if [ "$mode" = std ] || [ "$mode" = full ]; then
    command -v buf >/dev/null 2>&1 || { : >"$out"; fail "buf is not installed; it generates ios/RemoteAgentCore/Sources/RAProto"; }
    (cd "$ios" && buf generate) >"$out" 2>&1 || fail "buf generate failed"
    git -C "$ios" status --porcelain -- RemoteAgentCore/Sources/RAProto >"$out" 2>&1
    if [ -s "$out" ]; then
        fail "the generated protocol code is not current: run \`cd ios && buf generate\` and commit what it changes"
    fi
fi

# 3
[ "$mode" != core ] || exit 0
default="iPhone 18 Pro"
destination=${IOS_DESTINATION:-}
if [ -z "$destination" ]; then
    devices=$(xcrun simctl list devices available 2>/dev/null)
    if printf '%s\n' "$devices" | grep -q "^ *$default ("; then
        destination="platform=iOS Simulator,name=$default"
    else
        # A booted iPhone first, so that no other simulator has to start.
        id=$(printf '%s\n' "$devices" | sed -n 's/^ *iPhone[^(]* (\([0-9A-F-]\{36\}\)) (Booted).*/\1/p' | head -n 1)
        [ -n "$id" ] || id=$(printf '%s\n' "$devices" | sed -n 's/^ *iPhone[^(]* (\([0-9A-F-]\{36\}\)).*/\1/p' | head -n 1)
        [ -n "$id" ] || { printf '%s\n' "$devices" >"$out"; fail "no iPhone simulator is available; set IOS_DESTINATION"; }
        destination="platform=iOS Simulator,id=$id"
    fi
fi
# Component runs select tests explicitly; full keeps the complete scheme.
# The test action builds its targets. A preceding build repeats the app build
# action (including the always-run version stamp) without adding coverage.
set --
case "$mode" in
    std|app) set -- -only-testing:RemoteAgentTests ;;
    ui) set -- -only-testing:RemoteAgentUITests ;;
esac
[ -z "${IOS_DERIVED_DATA_PATH:-}" ] || set -- "$@" -derivedDataPath "$IOS_DERIVED_DATA_PATH"
xcodebuild -quiet -test-timeouts-enabled YES -default-test-execution-time-allowance 180 -maximum-test-execution-time-allowance 180 -collect-test-diagnostics never -parallel-testing-enabled NO -jobs "${IOS_BUILD_JOBS:-4}" -project "$ios/RemoteAgent.xcodeproj" -scheme RemoteAgent -destination "$destination" "$@" test >"$out" 2>&1 \
    || fail "the app did not build or its tests failed (xcodebuild build test, $destination)"
