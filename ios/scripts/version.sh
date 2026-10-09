#!/bin/sh
# The product version of the iOS app. version.properties at the repository
# root is the one place it is written, shared with the other clients; this
# reads it, with the commit the build is made from.
#
#   version.sh                      prints both as build settings
#   version.sh --stamp Info.plist   writes them into a built Info.plist
#
# The app target's "Stamp the version" build phase runs the second form on
# every build, so the project file carries no version of its own
# (MARKETING_VERSION there is a placeholder). The Settings screen reads
# CFBundleShortVersionString and RASourceCommit (AppInfo.swift).
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
version=$(sed -n 's/^version=//p' "$root/version.properties")
case "$version" in
    [0-9]*.[0-9]*.[0-9]*) ;;
    *) echo "version.sh: no version= in $root/version.properties" >&2; exit 1 ;;
esac

# RA_SOURCE_COMMIT lets a build made outside a checkout say where it is from.
commit=${RA_SOURCE_COMMIT:-}
if [ -z "$commit" ]; then
    if commit=$(git -C "$root" rev-parse --short=12 HEAD 2>/dev/null); then
        [ -z "$(git -C "$root" status --porcelain 2>/dev/null)" ] || commit="$commit-dirty"
    else
        commit=unknown
    fi
fi

if [ "${1:-}" = "--stamp" ]; then
    plist=${2:?version.sh --stamp needs the path of a built Info.plist}
    /usr/bin/plutil -replace CFBundleShortVersionString -string "$version" "$plist"
    /usr/bin/plutil -replace RASourceCommit -string "$commit" "$plist"
else
    echo "MARKETING_VERSION = $version"
    echo "RA_SOURCE_COMMIT = $commit"
fi
