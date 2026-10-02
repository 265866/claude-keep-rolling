#!/usr/bin/env bash
# Builds plugin store zips for VERSION into dist/, one per target in TARGETS.
#
# Linux and Windows targets build on Debian bookworm, so the Linux libraries link
# against the same glibc as the official CLIProxyAPI image:
#   docker run --rm -v "$PWD":/src -w /src -e VERSION=0.1.0 golang:1.26-bookworm ./scripts/release.sh
# Darwin targets build on macOS:
#   VERSION=0.1.0 TARGETS="darwin/arm64 darwin/amd64" ./scripts/release.sh
#
# checksums.txt covers the zips built by this run.
set -euo pipefail

id=claude-keep-rolling
version=${VERSION:-}
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
	echo "VERSION must be a dotted numeric version such as 0.1.0, got '$version'" >&2
	exit 1
fi
targets=${TARGETS:-linux/amd64 linux/arm64 windows/amd64}

if [[ "$(uname -s)" == Linux ]] && ! { command -v aarch64-linux-gnu-gcc && command -v x86_64-w64-mingw32-gcc && command -v zip; } >/dev/null; then
	apt-get update -qq
	apt-get install -y -qq gcc-aarch64-linux-gnu gcc-mingw-w64-x86-64 zip >/dev/null
fi

# go build stamps the commit into each library so a store reviewer can match it to
# the release tag. In a container the checkout belongs to another user, and git
# refuses it unless the directory is trusted. Trust it for this script only.
export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.directory GIT_CONFIG_VALUE_0="$PWD"

rm -rf dist
mkdir -p dist
for target in $targets; do
	goos=${target%/*}
	goarch=${target#*/}
	case "$target" in
	linux/amd64) cc=gcc ext=so ;;
	linux/arm64) cc=aarch64-linux-gnu-gcc ext=so ;;
	windows/amd64) cc=x86_64-w64-mingw32-gcc ext=dll ;;
	darwin/arm64) cc="clang -arch arm64" ext=dylib ;;
	darwin/amd64) cc="clang -arch x86_64" ext=dylib ;;
	*)
		echo "unsupported target $target" >&2
		exit 1
		;;
	esac
	out="dist/build/${goos}_$goarch"
	mkdir -p "$out"
	CGO_ENABLED=1 GOOS=$goos GOARCH=$goarch CC=$cc \
		go build -buildmode=c-shared -buildvcs=true -trimpath -ldflags "-s -w -X main.pluginVersion=$version" -o "$out/$id.$ext" .
	rm -f "$out/$id.h"
	(cd "$out" && zip -q -X "../../${id}_${version}_${goos}_${goarch}.zip" "$id.$ext")
done

(
	cd dist
	if command -v sha256sum >/dev/null; then
		sha256sum -- *.zip >checksums.txt
	else
		shasum -a 256 -- *.zip >checksums.txt
	fi
	cat checksums.txt
)
