#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CORE_ROOT="$ROOT/clientcore"
OUT_ROOT="${RA_CORE_OUT:-$ROOT/build/core}"

usage() {
  echo "usage: $0 <android|ios|web|all>" >&2
  exit 2
}

if [[ $# -ne 1 ]]; then
  usage
fi

TARGET="$1"

ensure_gomobile_tools() {
  if [[ -n "${CORE_TOOL_BIN:-}" ]]; then
    return
  fi
  CORE_TOOL_BIN="$(mktemp -d "${TMPDIR:-/tmp}/ra-core-tools.XXXXXX")"
  trap 'rm -rf "$CORE_TOOL_BIN"' EXIT
  (cd "$CORE_ROOT" && go build -o "$CORE_TOOL_BIN/gobind" golang.org/x/mobile/cmd/gobind)
  (cd "$CORE_ROOT" && go build -o "$CORE_TOOL_BIN/gomobile" golang.org/x/mobile/cmd/gomobile)
  export PATH="$CORE_TOOL_BIN:$PATH"
}

find_android_sdk() {
  local sdk="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}}"
  if [[ ! -d "$sdk" ]]; then
    echo "Android SDK not found at $sdk" >&2
    exit 1
  fi
  export ANDROID_HOME="$sdk"
  export ANDROID_SDK_ROOT="$sdk"
  if [[ -z "${ANDROID_NDK_HOME:-}" && -d "$sdk/ndk" ]]; then
    local ndk
    ndk="$(ls -1 "$sdk/ndk" | sort -V | tail -n 1)"
    if [[ -n "$ndk" ]]; then
      export ANDROID_NDK_HOME="$sdk/ndk/$ndk"
    fi
  fi
}

build_web() {
  local out="$OUT_ROOT/web"
  mkdir -p "$out"
  local goroot
  goroot="$(go env GOROOT)"
  local wasm_exec="$goroot/lib/wasm/wasm_exec.js"
  if [[ ! -f "$wasm_exec" ]]; then
    wasm_exec="$goroot/misc/wasm/wasm_exec.js"
  fi
  if [[ ! -f "$wasm_exec" ]]; then
    echo "wasm_exec.js not found under GOROOT=$goroot" >&2
    exit 1
  fi
  (
    cd "$CORE_ROOT"
    GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o "$out/core.wasm" ./cmd/corewasm
  )
  cp -f "$wasm_exec" "$out/wasm_exec.js"
  echo "built web: $out/core.wasm ($out/wasm_exec.js)"
}

build_android() {
  ensure_gomobile_tools
  find_android_sdk
  local out="$OUT_ROOT/android"
  mkdir -p "$out"
  (
    cd "$CORE_ROOT"
    CGO_LDFLAGS="${CGO_LDFLAGS:-} -Wl,-z,max-page-size=16384" \
      "$CORE_TOOL_BIN/gomobile" bind \
      -target=android/arm64,android/amd64 \
      -androidapi=26 \
      -javapkg=io.github.lewin671.remoteagent \
      -trimpath \
      -ldflags="-s -w" \
      -o "$out/corebind.aar" \
      ./corebind
  )
  echo "built android: $out/corebind.aar"
}

ios_up_to_date() {
  local xcf="$1"
  local plist="$xcf/Info.plist"
  [[ "${RA_CORE_FORCE:-0}" != "1" ]] || return 1
  [[ -f "$plist" ]] || return 1
  [[ -f "$xcf/ios-arm64/Corebind.framework/Corebind" ]] || return 1
  [[ -f "$xcf/ios-arm64-simulator/Corebind.framework/Corebind" ]] || return 1
  [[ -f "$xcf/macos-arm64/Corebind.framework/Corebind" ]] || return 1
  [[ "$ROOT/scripts/core/build.sh" -nt "$plist" ]] && return 1
  local newer
  newer="$(find "$CORE_ROOT" -type f \( -name '*.go' -o -name 'go.mod' -o -name 'go.sum' \) -newer "$plist" | head -n 1)"
  [[ -z "$newer" ]]
}

build_ios() {
  local out="$OUT_ROOT/ios"
  local xcf="$out/Corebind.xcframework"
  if ios_up_to_date "$xcf"; then
    return 0
  fi
  if [[ -z "${CC:-}" && -x /usr/bin/clang ]]; then
    export CC=/usr/bin/clang
  fi
  ensure_gomobile_tools
  mkdir -p "$out"
  rm -rf "$xcf"
  (
    cd "$CORE_ROOT"
    "$CORE_TOOL_BIN/gomobile" bind \
      -target=ios/arm64,iossimulator/arm64,macos/arm64 \
      -iosversion=17.0 \
      -trimpath \
      -ldflags="-s -w" \
      -o "$xcf" \
      ./corebind
  )
  echo "built ios: $xcf"
}

case "$TARGET" in
  web)
    build_web
    ;;
  android)
    build_android
    ;;
  ios)
    build_ios
    ;;
  all)
    build_web
    build_android
    build_ios
    ;;
  *)
    usage
    ;;
esac
