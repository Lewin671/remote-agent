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
# simulator at a time; `check.sh perf` runs simulator frame-rate benchmarks;
# `check.sh full` is everything except perf. `core` and `app` are for
# iterating.
set -u
mode=${1:-std}
case "$mode" in std|full|core|app|ui|perf) ;; *) echo "usage: check.sh [core|app|ui|perf|full]" >&2; exit 2 ;; esac

ios=$(cd "$(dirname "$0")/.." && pwd)
out=$(mktemp -t ra-ios-check)
result_dir=$(mktemp -d -t ra-ios-xcresult)
sim_name= sim_id= udid=
trap 'shutdown_started_simulator; rm -rf "$out" "$result_dir"' EXIT
trap 'exit 130' INT
trap 'exit 143' HUP TERM

# Prints the UDID of the first booted simulator whose name and UDID match the
# given sed patterns.
booted_simulator() {
    xcrun simctl list devices 2>/dev/null | sed -n "s/^ *$1 (\($2\)) (Booted).*/\1/p" | head -n 1
}

# xcodebuild boots the destination simulator and leaves it running. A booted
# simulator left alone for days keeps crashing system processes (PosterBoard)
# and macOS stacks a crash dialog on the desktop for each one, so shut it down
# again when this run was the one that started it.
shutdown_started_simulator() {
    [ -n "$sim_name" ] && [ -z "$udid" ] || return 0
    started=$(booted_simulator "$sim_name" "$sim_id")
    [ -z "$started" ] || xcrun simctl shutdown "$started" >/dev/null 2>&1 || true
}

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

# 0: ensure the shared Go core xcframework is current (skips when up to date).
# RA_CORE_BUILD lets the script tests record the step instead of running gomobile.
"${RA_CORE_BUILD:-$ios/../scripts/core/build.sh}" ios >"$out" 2>&1 || fail "failed to build Corebind.xcframework"

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
scheme=RemoteAgent
set --
case "$mode" in
    std|app) set -- -only-testing:RemoteAgentTests ;;
    ui) set -- -only-testing:RemoteAgentUITests ;;
    perf) scheme=RemoteAgentPerfTests; set -- -only-testing:RemoteAgentPerfTests ;;
esac
[ -z "${IOS_DERIVED_DATA_PATH:-}" ] || set -- "$@" -derivedDataPath "$IOS_DERIVED_DATA_PATH"
# A run that was killed leaves its test host running inside the simulator, and
# the next xcodebuild on that simulator then waits for it indefinitely with no
# output. Stop any app of ours left on the target before testing.
# udid stays empty when the target is not running yet; then the EXIT trap
# shuts down whatever xcodebuild boots for it.
case "$destination" in
    *id=*) sim_name='.*'; sim_id=${destination##*id=}; sim_id=${sim_id%%,*} ;;
    *name=*) sim_name=${destination##*name=}; sim_name=${sim_name%%,*}; sim_id='[0-9A-F-]\{36\}' ;;
esac
[ -z "$sim_name" ] || udid=$(booted_simulator "$sim_name" "$sim_id")
if [ -n "$udid" ]; then
    for bundle in io.github.lewin671.remoteagent io.github.lewin671.remoteagent.dev io.github.lewin671.remoteagent.uitests.xctrunner io.github.lewin671.remoteagent.perftests.xctrunner; do
        xcrun simctl terminate "$udid" "$bundle" >/dev/null 2>&1 || true
    done
fi
result_bundle="$result_dir/test.xcresult"
if ! xcodebuild -quiet -test-timeouts-enabled YES -default-test-execution-time-allowance 180 -maximum-test-execution-time-allowance 180 -collect-test-diagnostics never -parallel-testing-enabled NO -jobs "${IOS_BUILD_JOBS:-4}" -project "$ios/RemoteAgent.xcodeproj" -scheme "$scheme" -destination "$destination" -resultBundlePath "$result_bundle" "$@" test >"$out" 2>&1; then
    python3 "$ios/scripts/xcresult_failures.py" "$result_bundle" >>"$out" 2>&1 || true
    fail "the app did not build or its tests failed (xcodebuild build test, $destination)"
fi
