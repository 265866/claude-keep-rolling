#!/usr/bin/env bash
# Builds the plugin store release assets for VERSION into dist/.
# Run inside golang:1.26-bookworm so the libraries link against the same glibc
# as the official CLIProxyAPI image:
#   podman run --rm -v "$PWD":/src -w /src -e VERSION=0.1.0 docker.io/library/golang:1.26-bookworm ./scripts/release.sh
set -euo pipefail

id=claude-keep-rolling
version=${VERSION:-}
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
	echo "VERSION must be a dotted numeric version such as 0.1.0, got '$version'" >&2
	exit 1
fi

if ! command -v aarch64-linux-gnu-gcc >/dev/null || ! command -v zip >/dev/null; then
	apt-get update -qq
	apt-get install -y -qq gcc-aarch64-linux-gnu zip >/dev/null
fi

rm -rf dist
mkdir -p dist
for arch in amd64 arm64; do
	cc=gcc
	if [[ "$arch" == arm64 ]]; then
		cc=aarch64-linux-gnu-gcc
	fi
	out="dist/build/linux_$arch"
	mkdir -p "$out"
	CGO_ENABLED=1 GOOS=linux GOARCH=$arch CC=$cc \
		go build -buildmode=c-shared -buildvcs=false -trimpath -ldflags "-s -w -X main.pluginVersion=$version" -o "$out/$id.so" .
	rm -f "$out/$id.h"
	(cd "$out" && zip -q -X "../../${id}_${version}_linux_${arch}.zip" "$id.so")
done

(cd dist && sha256sum ./*.zip | sed 's# \./# #' >checksums.txt)
cat dist/checksums.txt
