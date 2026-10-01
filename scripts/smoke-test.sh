#!/usr/bin/env bash
# Loads a plugin zip into a real CLIProxyAPI release and checks that the plugin
# registers with VERSION, serves its page, and keeps its data behind the
# management key. CPA_ASSET is the CLIProxyAPI release asset suffix for the runner.
#   VERSION=0.1.0 CPA_ASSET=linux_amd64.tar.gz ./scripts/smoke-test.sh dist/claude-keep-rolling_0.1.0_linux_amd64.zip
set -euo pipefail

zip=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
cpa_version=${CPA_VERSION:-8.0.8}
asset="CLIProxyAPI_${cpa_version}_${CPA_ASSET:?CPA_ASSET is required}"
port=18317
key=smoke-test-key
base=http://127.0.0.1:$port
work=$(mktemp -d)
py=$(command -v python3 || command -v python)

unpack() {
	"$py" -c 'import sys, tarfile, zipfile
src, dst = sys.argv[1], sys.argv[2]
(zipfile.ZipFile(src) if src.endswith(".zip") else tarfile.open(src)).extractall(dst)' "$1" "$2"
}

gh release download "v$cpa_version" -R router-for-me/CLIProxyAPI -p "$asset" -D "$work"
unpack "$work/$asset" "$work/cpa"
mkdir -p "$work/data/plugins" "$work/data/auths"
unpack "$zip" "$work/data/plugins"
bin=$work/cpa/cli-proxy-api
[[ -f "$bin.exe" ]] && bin=$bin.exe
chmod +x "$bin"

# Relative paths keep the config valid for native Windows binaries under Git Bash.
cat >"$work/data/config.yaml" <<EOF
host: "127.0.0.1"
port: $port
auth-dir: "auths"
remote-management:
  secret-key: "$key"
  disable-control-panel: true
plugins:
  enabled: true
  dir: "plugins"
  configs:
    claude-keep-rolling:
      enabled: true
EOF

cd "$work/data"
"$bin" -config config.yaml >"$work/cpa.log" 2>&1 &
pid=$!
cd - >/dev/null
trap 'kill "$pid" 2>/dev/null || true' EXIT

fail() {
	echo "FAIL: $1" >&2
	echo "--- CLIProxyAPI log (first crash lines) ---" >&2
	grep -n -m 5 -E 'fatal|panic|runtime/cgo|SIG[A-Z]+' "$work/cpa.log" >&2 || true
	echo "--- CLIProxyAPI log (tail) ---" >&2
	tail -n 40 "$work/cpa.log" >&2 || true
	exit 1
}

state=""
for _ in $(seq 60); do
	state=$(curl -sf -H "Authorization: Bearer $key" "$base/v0/management/claude-keep-rolling/state" || true)
	[[ -n "$state" ]] && break
	kill -0 "$pid" 2>/dev/null || fail "CLIProxyAPI exited"
	sleep 1
done
[[ -n "$state" ]] || fail "plugin state route never answered"
grep -q "\"version\":\"$VERSION\"" <<<"$state" || fail "state does not report version $VERSION: $state"
echo "ok: plugin registered and reports version $VERSION"

curl -sf "$base/v0/resource/plugins/claude-keep-rolling/status" | grep -q '<select id="model">' || fail "status page not served"
echo "ok: status page served"

code=$(curl -s -o /dev/null -w '%{http_code}' "$base/v0/management/claude-keep-rolling/state")
[[ "$code" == 401 ]] || fail "state route answered $code without the management key"
echo "ok: state route requires the management key"
