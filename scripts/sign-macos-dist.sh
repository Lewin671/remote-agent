#!/bin/sh
# Signs the macOS daemons in dist/ with a Developer ID and has Apple notarize
# them, repacking each archive the way scripts/build-dist.sh made it.
#
# Usage: scripts/sign-macos-dist.sh      (on macOS, after scripts/build-dist.sh)
#
# The identity is RA_CODESIGN_IDENTITY, or the first "Developer ID
# Application" one in the keychain search list. Notarization uses an App
# Store Connect API key: RA_NOTARY_KEY (path of the .p8), RA_NOTARY_KEY_ID
# and RA_NOTARY_ISSUER.
#
# The identifier is the one scripts/install.sh signs with, so macOS keeps the
# privacy permissions it granted when a release replaces a source build.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
: "${RA_NOTARY_KEY:?}" "${RA_NOTARY_KEY_ID:?}" "${RA_NOTARY_ISSUER:?}"
identity=${RA_CODESIGN_IDENTITY:-$(security find-identity -v -p codesigning |
	sed -n 's/.*"\(Developer ID Application: [^"]*\)".*/\1/p' | head -n 1)}
[ -n "$identity" ] || {
	echo "sign-macos-dist: no Developer ID Application identity" >&2
	exit 1
}
tag=v$(sed -n 's/^version=//p' version.properties)
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

names=
for arch in arm64 amd64; do
	name=remote-agent-$tag-darwin-$arch
	tar -xzf "dist/$name.tar.gz" -C "$stage"
	codesign --force --sign "$identity" --identifier com.github.lewin671.remote-agent \
		--options runtime --timestamp "$stage/$name/remote-agent"
	codesign --verify --strict "$stage/$name/remote-agent"
	names="$names $name"
done

# One submission for both. A bare executable cannot carry a stapled ticket;
# Gatekeeper looks the notarization up online.
# shellcheck disable=SC2086
(cd "$stage" && zip -qr notarize.zip $names)
xcrun notarytool submit "$stage/notarize.zip" --key "$RA_NOTARY_KEY" \
	--key-id "$RA_NOTARY_KEY_ID" --issuer "$RA_NOTARY_ISSUER" --wait --timeout 20m >"$stage/notary.txt"
cat "$stage/notary.txt"
grep -q '^ *status: Accepted$' "$stage/notary.txt" || {
	echo "sign-macos-dist: notarization was not accepted" >&2
	exit 1
}

for name in $names; do
	COPYFILE_DISABLE=1 tar -C "$stage" --uid 0 --gid 0 --uname root --gname root \
		-czf "dist/$name.tar.gz" "$name"
done
echo "signed and notarized:$names"
