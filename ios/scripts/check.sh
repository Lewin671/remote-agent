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
#   IOS_DESTINATION   the xcodebuild destination; by default the iPhone 18
#                     Pro simulator, or the first iPhone simulator there is
#                     when this Mac has none of that name.
set -u

ios=$(cd "$(dirname "$0")/.." && pwd)
out=$(mktemp -t ra-ios-check)
trap 'rm -f "$out"' EXIT

fail() {
    echo "check.sh: $1" >&2
    cat "$out" >&2
    exit 1
}

# 1
(cd "$ios/RemoteAgentCore" && swift test) >"$out" 2>&1 || fail "the package's tests failed (swift test in ios/RemoteAgentCore)"

# 2
command -v buf >/dev/null 2>&1 || { : >"$out"; fail "buf is not installed; it generates ios/RemoteAgentCore/Sources/RAProto"; }
(cd "$ios" && buf generate) >"$out" 2>&1 || fail "buf generate failed"
git -C "$ios" status --porcelain -- RemoteAgentCore/Sources/RAProto >"$out" 2>&1
if [ -s "$out" ]; then
    fail "the generated protocol code is not current: run \`cd ios && buf generate\` and commit what it changes"
fi

# 3
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
xcodebuild -quiet -project "$ios/RemoteAgent.xcodeproj" -scheme RemoteAgent -destination "$destination" build test >"$out" 2>&1 \
    || fail "the app did not build or its tests failed (xcodebuild build test, $destination)"
