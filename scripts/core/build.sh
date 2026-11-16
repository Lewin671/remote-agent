#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CORE_ROOT="$ROOT/clientcore"
OUT_ROOT="${RA_CORE_OUT:-$ROOT/build/core}"

usage() {
  echo "usage: $0 <android|android-host|ios|web|all>" >&2
  exit 2
}

if [[ $# -ne 1 ]]; then
  usage
fi

TARGET="$1"
CLEANUP_DIRS=()

cleanup() {
  local d
  for d in "${CLEANUP_DIRS[@]:-}"; do
    rm -rf "$d"
  done
}
trap cleanup EXIT

ensure_gomobile_tools() {
  if [[ -n "${CORE_TOOL_BIN:-}" ]]; then
    return
  fi
  CORE_TOOL_BIN="$(mktemp -d "${TMPDIR:-/tmp}/ra-core-tools.XXXXXX")"
  CLEANUP_DIRS+=("$CORE_TOOL_BIN")
  (cd "$CORE_ROOT" && go build -o "$CORE_TOOL_BIN/gobind" golang.org/x/mobile/cmd/gobind)
  (cd "$CORE_ROOT" && go build -o "$CORE_TOOL_BIN/gomobile" golang.org/x/mobile/cmd/gomobile)
  export PATH="$CORE_TOOL_BIN:$PATH"
}

find_java_home() {
  if [[ -n "${JAVA_HOME:-}" && -f "$JAVA_HOME/include/jni.h" ]]; then
    echo "$JAVA_HOME"
    return
  fi
  if [[ "$(uname -s)" == "Darwin" ]] && command -v /usr/libexec/java_home >/dev/null 2>&1; then
    local jh
    jh="$(/usr/libexec/java_home -v 17 2>/dev/null || /usr/libexec/java_home 2>/dev/null || true)"
    if [[ -n "$jh" && -f "$jh/include/jni.h" ]]; then
      echo "$jh"
      return
    fi
  fi
  if command -v javac >/dev/null 2>&1; then
    local jc jh
    jc="$(python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$(command -v javac)")"
    jh="${jc%/bin/javac}"
    if [[ -f "$jh/include/jni.h" ]]; then
      echo "$jh"
      return
    fi
  fi
  echo "JDK with include/jni.h not found (set JAVA_HOME)" >&2
  exit 1
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
  if [[ -z "${RA_CORE_FORCE:-}" && -f "$out/core.wasm" && -f "$out/wasm_exec.js" ]] && \
     cmp -s "$wasm_exec" "$out/wasm_exec.js" && \
     [[ ! "$ROOT/scripts/core/build.sh" -nt "$out/core.wasm" ]] && \
     [[ -z "$(find "$CORE_ROOT" -type f \( -name '*.go' -o -name 'go.mod' -o -name 'go.sum' \) -newer "$out/core.wasm" | head -n 1)" ]]; then
    return 0
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
  local host_cc="${CC:-}"
  if [[ -z "$host_cc" && "$(uname -s)" == "Darwin" && -x /usr/bin/clang ]]; then
    host_cc="/usr/bin/clang"
  fi
  (
    cd "$CORE_ROOT"
    env ${host_cc:+CC="$host_cc"} CGO_LDFLAGS="${CGO_LDFLAGS:-} -Wl,-z,max-page-size=16384" \
      "$CORE_TOOL_BIN/gomobile" bind \
      -target=android/arm64,android/amd64 \
      -androidapi=26 \
      -javapkg=io.github.lewin671.remoteagent \
      -trimpath \
      -ldflags="-s -w" \
      -o "$out/corebind.aar" \
      ./corebind
  )
  # gomobile emits `-keep class <javapkg>.**`, which matches our entire app package.
  # Narrow it to the generated corebind package so R8 still shrinks :app and :core.
  python3 - "$out/corebind.aar" <<'PY'
import sys, zipfile, os, tempfile
aar = sys.argv[1]
fd, tmp = tempfile.mkstemp(dir=os.path.dirname(aar), suffix=".aar")
os.close(fd)
with zipfile.ZipFile(aar, "r") as src, zipfile.ZipFile(tmp, "w", compression=zipfile.ZIP_DEFLATED) as dst:
    for info in src.infolist():
        data = src.read(info.filename)
        if info.filename == "proguard.txt":
            text = data.decode("utf-8").replace(
                "-keep class io.github.lewin671.remoteagent.** { *; }",
                "-keep class io.github.lewin671.remoteagent.corebind.** { *; }",
            )
            data = text.encode("utf-8")
        dst.writestr(info, data)
os.chmod(tmp, 0o644)
os.replace(tmp, aar)
PY
  echo "built android: $out/corebind.aar"
}

build_android_host() {
  ensure_gomobile_tools
  local java_home uname_s jni_os lib_name
  java_home="$(find_java_home)"
  uname_s="$(uname -s)"
  case "$uname_s" in
    Darwin)
      jni_os="darwin"
      lib_name="libgojni.dylib"
      ;;
    Linux)
      jni_os="linux"
      lib_name="libgojni.so"
      ;;
    *)
      echo "unsupported host OS for android-host: $uname_s" >&2
      exit 1
      ;;
  esac
  if [[ ! -d "$java_home/include/$jni_os" ]]; then
    echo "JNI platform headers not found under $java_home/include/$jni_os" >&2
    exit 1
  fi

  local work
  work="$(mktemp -d "${TMPDIR:-/tmp}/ra-core-host.XXXXXX")"
  CLEANUP_DIRS+=("$work")

  (
    cd "$CORE_ROOT"
    "$CORE_TOOL_BIN/gobind" \
      -lang=go,java \
      -javapkg=io.github.lewin671.remoteagent \
      -outdir="$work/out" \
      ./corebind
  )

  python3 - "$work" "$ROOT" "$CORE_ROOT" <<'PY'
import glob, os, re, shutil, sys
work, repo_root, core_root = sys.argv[1], sys.argv[2], sys.argv[3]
gobind_dir = os.path.join(work, "out", "src", "gobind")

# Rename *_android.{go,c} so the host GOOS compiles them, while keeping *_android.h
# filenames intact because seq.h / corebind.h / universe.h include them by name.
for p in glob.glob(os.path.join(gobind_dir, "*_android.go")) + glob.glob(os.path.join(gobind_dir, "*_android.c")):
    os.rename(p, p.replace("_android.", "_host."))

seq_host_go = os.path.join(gobind_dir, "seq_host.go")
with open(seq_host_go, "r", encoding="utf-8") as f:
    text = f.read()
text = text.replace("//#cgo LDFLAGS: -llog\n", "")
with open(seq_host_go, "w", encoding="utf-8") as f:
    f.write(text)

seq_go = os.path.join(gobind_dir, "seq.go")
with open(seq_go, "r", encoding="utf-8") as f:
    text = f.read()
text = (
    text.replace("// #cgo android CFLAGS: -D__GOBIND_ANDROID__\n", "// #cgo CFLAGS: -D__GOBIND_ANDROID__\n")
        .replace("// #cgo darwin CFLAGS: -D__GOBIND_DARWIN__\n", "")
)
with open(seq_go, "w", encoding="utf-8") as f:
    f.write(text)

shim_android = os.path.join(work, "shim", "android")
os.makedirs(shim_android, exist_ok=True)
with open(os.path.join(shim_android, "log.h"), "w", encoding="utf-8") as f:
    f.write(
        '#pragma once\n'
        '#include <stdio.h>\n'
        '#define ANDROID_LOG_INFO 4\n'
        '#define ANDROID_LOG_FATAL 7\n'
        '#define __android_log_print(prio, tag, ...) do { fprintf(stderr, "%s: ", tag); fprintf(stderr, __VA_ARGS__); fprintf(stderr, "\\n"); } while (0)\n'
    )

with open(os.path.join(core_root, "go.mod"), "r", encoding="utf-8") as f:
    core_gomod = f.read()
go_ver = re.search(r"^go\s+(\S+)", core_gomod, re.M).group(1)
mobile_ver = re.search(r"golang\.org/x/mobile\s+(\S+)", core_gomod).group(1)

mod_dir = os.path.join(work, "mod")
shutil.copytree(gobind_dir, os.path.join(mod_dir, "gobind"))
with open(os.path.join(mod_dir, "go.mod"), "w", encoding="utf-8") as f:
    f.write(
        f"module gobindhost\n\n"
        f"go {go_ver}\n\n"
        f"require (\n"
        f"\tgithub.com/Lewin671/remote-agent/clientcore v0.0.0\n"
        f"\tgolang.org/x/mobile {mobile_ver}\n"
        f")\n\n"
        f"replace (\n"
        f"\tgithub.com/Lewin671/remote-agent/clientcore => {core_root}\n"
        f"\tgithub.com/Lewin671/remote-agent => {repo_root}\n"
        f")\n"
    )
shutil.copy(os.path.join(core_root, "go.sum"), os.path.join(mod_dir, "go.sum"))
PY

  local out="$OUT_ROOT/android-host"
  mkdir -p "$out"
  local host_cc="${CC:-}"
  if [[ -z "$host_cc" && "$uname_s" == "Darwin" && -x /usr/bin/clang ]]; then
    host_cc="/usr/bin/clang"
  fi
  local cflags="-Wno-incompatible-pointer-types -I$work/shim -I$java_home/include -I$java_home/include/$jni_os"
  (
    cd "$work/mod"
    GOPROXY=off go mod tidy
    env CGO_ENABLED=1 ${host_cc:+CC="$host_cc"} CGO_CFLAGS="${CGO_CFLAGS:-} $cflags" \
      go build -trimpath -ldflags="-s -w" -buildmode=c-shared -o "$work/$lib_name" ./gobind
  )
  cp -f "$work/$lib_name" "$out/$lib_name"
  echo "built android-host: $out/$lib_name"
}

build_ios() {
  ensure_gomobile_tools
  local out="$OUT_ROOT/ios"
  mkdir -p "$out"
  rm -rf "$out/Corebind.xcframework"
  (
    cd "$CORE_ROOT"
    "$CORE_TOOL_BIN/gomobile" bind \
      -target=ios/arm64,iossimulator/arm64 \
      -iosversion=17.0 \
      -trimpath \
      -ldflags="-s -w" \
      -o "$out/Corebind.xcframework" \
      ./corebind
  )
  echo "built ios: $out/Corebind.xcframework"
}

case "$TARGET" in
  web)
    build_web
    ;;
  android)
    build_android
    ;;
  android-host)
    build_android_host
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
