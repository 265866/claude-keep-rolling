# Claude Keep Rolling

A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plugin that keeps Claude five-hour usage windows rolling.

A Claude window starts with the first message and resets five hours later. This plugin sends a tiny message through each selected Claude account one minute after its window resets, so a new window is always running.

## How it works

- Each ping goes through CLIProxyAPI's own Claude executor, pinned to one account.
- Claude's reply includes the window's reset time (`anthropic-ratelimit-unified-5h-reset`). The next ping is scheduled one minute after that time.
- If a ping fails, the plugin retries in 15 minutes.
- Disabled accounts are skipped.
- The request has no tools, so the model can only reply with text. Replies are capped at 1 token.
- CLIProxyAPI keeps a disabled plugin loaded without telling it, so the plugin checks its `enabled` flag in `config.yaml` before each round. Turning it off stops pings within 30 seconds. If the config is not a readable file, pings continue until CLIProxyAPI restarts.
- Ping history is kept in memory. After a restart, every selected account is pinged once to learn its reset time.

## Install

Requires CLIProxyAPI v8 with plugins enabled.

Once the plugin is listed in the official plugin store, install it from **Plugin Store** in the Management Center.

Until then, add this repository's registry as a store source, then install it from **Plugin Store**:

```yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/265866/claude-keep-rolling/main/registry.json"
```

To install by hand, download the zip for your platform from the [latest release](https://github.com/265866/claude-keep-rolling/releases/latest). Put `claude-keep-rolling.so` in your plugins directory, then enable it:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    claude-keep-rolling:
      enabled: true
```

## Use

Open **Claude Keep Rolling** in the Management Center sidebar.

- **Accounts**: check the accounts to keep rolling. **Select all** includes accounts you add later. Unchecking any account switches to only the accounts you have checked.
- **Settings**: the model and prompt for each ping. The model dropdown lists the Claude models CLIProxyAPI offers for your accounts. The default is Claude Haiku 4.5.
- **Ping now** sends a ping immediately.

The page reuses the Management Center's saved management key. If you are not logged in with **Remember password**, the page asks for the key and keeps it for the current tab only.

Settings are saved in `config.yaml`:

```yaml
claude-keep-rolling:
  enabled: true
  model: claude-haiku-4-5-20251001  # default
  prompt: hi                         # default
  select_all: true                   # default
  accounts: []                       # account IDs, used when select_all is false
```

## Build

The plugin is a Go `c-shared` library. Build it on Debian bookworm so it links against the same glibc as the official CLIProxyAPI image:

```sh
podman run --rm -v "$PWD":/src -w /src -e VERSION=0.1.0 docker.io/library/golang:1.26-bookworm ./scripts/release.sh
```

This writes the store release assets to `dist/`: one zip per platform (`linux_amd64`, `linux_arm64`) and `checksums.txt`. The version is stamped into the library at build time.

## Releases

Releases are automatic. Every push to `main` runs the checks, and if there are release-worthy commits since the last tag, publishes a GitHub release with the zips and `checksums.txt`. CLIProxyAPI installs from the latest release.

The version comes from [Conventional Commits](https://www.conventionalcommits.org/) since the last tag (see `scripts/next-version.sh`):

| Commit | Release |
| --- | --- |
| `feat: ...` | Minor, for example 0.1.0 to 0.2.0 |
| `fix: ...` or `perf: ...` | Patch, for example 0.1.0 to 0.1.1 |
| `feat!: ...` or a `BREAKING CHANGE:` footer | Minor below 1.0.0, major after |
| Anything else (`docs`, `ci`, `chore`, ...) | No release |

Changes land through pull requests that are squash-merged, so the PR title becomes the commit on `main` and decides the release.

Run the tests with:

```sh
go test ./internal/...
```

## Account risk

Anthropic restricts using Claude subscription logins through third-party tools. Using CLIProxyAPI with Claude accounts, and sending scheduled pings through them, may put those accounts at risk. Use at your own discretion.

## License

[MIT](LICENSE)
