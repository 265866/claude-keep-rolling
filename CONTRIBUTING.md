# Contributing

## Layout

| Path | Contents |
| --- | --- |
| `plugin.go` | Registration, management routes and the plugin page |
| `roller.go` | The scheduler that pings each account |
| `internal/rolling` | Host-independent rules: settings, account selection and ping timing |
| `main.go`, `entry.c`, `abi.h` | The C ABI between CLIProxyAPI and the plugin |
| `status.html` | The plugin page, embedded in the library |
| `test/faulthost` | A minimal Go host that loads the plugin the way CLIProxyAPI does and checks that it still recovers its own faults |

## Test

```sh
go test ./...
```

The root package and `test/faulthost` use cgo and need a C compiler. Without one, `go test ./internal/...` runs the scheduling rules only. CI runs gofumpt, `go mod tidy`, vet, golangci-lint, `go test -race ./...` and govulncheck in `golang:1.26-bookworm`.

## Build

The plugin is a Go `c-shared` library, and the version is stamped into it at build time. `scripts/release.sh` writes one store zip per target, plus `checksums.txt`, to `dist/`.

Build the Linux and Windows libraries on Debian bookworm, so the Linux ones link against the same glibc as the official CLIProxyAPI image:

```sh
docker run --rm -v "$PWD":/src -w /src -e VERSION=0.1.0 golang:1.26-bookworm ./scripts/release.sh
```

Build the macOS libraries on a Mac:

```sh
VERSION=0.1.0 TARGETS="darwin/arm64 darwin/amd64" ./scripts/release.sh
```

`scripts/smoke-test.sh` loads a built zip into a real CLIProxyAPI release and checks that the plugin registers, serves its page, keeps its data behind the management key, and, where it can read the process memory, does not leak across calls. CI runs it on all five platforms against the latest CLIProxyAPI v8 release, and runs `test/faulthost` on Linux and macOS.

## Releases

Every push to `main` runs the checks, then builds and smoke-tests all five platforms. If there are release-worthy commits since the last tag, it publishes a GitHub release with the zips and `checksums.txt`. CLIProxyAPI installs from the latest release.

The version comes from [Conventional Commits](https://www.conventionalcommits.org/) since the last tag (see `scripts/next-version.sh`):

| Commit | Release |
| --- | --- |
| `feat: ...` | Minor, for example 0.1.0 to 0.2.0 |
| `fix: ...` or `perf: ...` | Patch, for example 0.1.0 to 0.1.1 |
| `feat!: ...` or a `BREAKING CHANGE:` footer | Minor below 1.0.0, major after |
| Anything else (`docs`, `ci`, `refactor`, ...) | No release |

Changes land through squash-merged pull requests. The PR title becomes the commit subject and decides the release, and the PR description becomes the commit body, so keep it short.

## Platform notes

### Intel Macs

On darwin/amd64, every Go runtime keeps its current goroutine in the same thread-local slot. CLIProxyAPI and a Go plugin are two runtimes in one process, so each would mistake the other's state for its own. This is why Go plugins without a workaround crash CLIProxyAPI on Intel Macs. `entry.c` swaps the slot on every call between the two runtimes and hands the fault and preemption signal handlers back to CLIProxyAPI. This has consequences:

- The plugin must be built with the same Go minor version as CLIProxyAPI (1.26 for v8). When CLIProxyAPI moves to a new Go minor version, update `setup-go` and the bookworm image in CI to match.
- The plugin has no async preemption on Intel Macs, so it must not fork or exec (no `os/exec`).
- A Go plugin loaded after this one takes the signal handlers back.

### Disabled plugins

CLIProxyAPI keeps a disabled plugin loaded without telling it. Before each round, the plugin reads `config.yaml` itself (`rolling.PluginEnabled`) and skips the round if it is disabled. If the config is not a readable file, it logs a warning once and keeps pinging.
