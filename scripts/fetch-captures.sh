#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
#
# Fetches the extended capture corpus: recorded traffic that is not
# redistributed in this repository (testdata/captures/README.md). Every file
# is pinned by source commit and SHA-256; a mismatch fails. The files land in
# .cache/captures (git-ignored), where the capture tests find them.
set -euo pipefail

cd "$(dirname "$0")/.."
dest=.cache/captures
copley_rev=be9ed7055eb88a4b933788b3e05de62bcfe7b4b1
copley_url="https://raw.githubusercontent.com/CopleyControlsOfficial/CANopen-Message-Log-Examples/${copley_rev}"

# <destination path>|<URL-encoded source path>|<sha256>
files=(
	"copley/Absolute Move|Absolute%20Move|48867c719f74f05c4025a2dc1c657d8a3b897851b4cb28a130a8b0751ade008c"
	"copley/Relative Move|Relative%20Move|272ee369bc8d2fb0f735e381446823ab64276b8406d91cb840bd4529d687eee6"
	"copley/Linkage Home and Move|Linkage%20Home%20and%20Move|f6b3ed19af2a38465c4ea4eaad8e481f9a4cf69c90eb2e7b980ffb7ccc9782ed"
	"copley/PVT Move|PVT%20Move|51af83d4d139cd9195aa68c27abe0d6066e9bdef3734de4e66f4232170cea357"
	"copley/CANopen.eds|CANopen.eds|c0c06a698a4bff3ba8071add865dc4314759116fd948df6fedd9ba2a1ce492b7"
)

sha256() {
	if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

for entry in "${files[@]}"; do
	IFS='|' read -r path src want <<<"$entry"
	out="$dest/$path"
	if [ -f "$out" ] && [ "$(sha256 "$out")" = "$want" ]; then
		continue
	fi
	mkdir -p "$(dirname "$out")"
	curl -fsSL --retry 3 -o "$out.tmp" "$copley_url/$src"
	got="$(sha256 "$out.tmp")"
	if [ "$got" != "$want" ]; then
		rm -f "$out.tmp"
		echo "fetch-captures: $path: SHA-256 $got, want $want" >&2
		exit 1
	fi
	mv "$out.tmp" "$out"
	echo "fetched $path"
done
