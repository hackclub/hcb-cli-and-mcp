# hcb-mcp

Read-only [HCB](https://hcb.hackclub.com) v4 API tooling: a shared Go client, an `hcb` CLI, and an `hcb-mcp` MCP (stdio) server.

```
internal/hcbapi   shared API client: auth + refresh rotation, pagination, endpoints, file downloads
cmd/hcb           CLI (cobra) — JSON output
cmd/hcb-mcp       MCP server (official modelcontextprotocol/go-sdk): stdio, or HTTP with --http
```

> ⚠️ **Public repo.** No credentials, tokens, real personal data, or signed
> storage URLs may be committed — see [AGENTS.md](AGENTS.md) and run
> `scripts/check_public_safety.sh` before committing.

## Install

With [Homebrew](https://brew.sh) (macOS and Linux):

```sh
brew tap hackclub/hcb https://github.com/hackclub/hcb-cli-and-mcp
brew install hcb
```

This installs both binaries: the `hcb` CLI and the `hcb-mcp` server. Then:

```sh
hcb login          # authorize in your browser
hcb orgs           # list your organizations
```

Binaries are built by GitHub Actions ([release workflow](.github/workflows/release.yml))
via GoReleaser — **every push to `main` automatically cuts a new patch
release** (macOS + Linux, arm64 + amd64) and regenerates the Homebrew formula
in [`Formula/hcb.rb`](Formula/). Push a `vX.Y.Z` tag for a minor/major bump.
Prebuilt tarballs are on the
[releases page](https://github.com/hackclub/hcb-cli-and-mcp/releases).

From source: `go build ./cmd/hcb` (Go 1.26+).

## Updating

`hcb` keeps itself up to date: at most once a day, a brew-installed `hcb`
checks the latest GitHub release in a detached background process and, if
newer, runs `brew upgrade` for you — you'll see a one-line notice on the next
run. It never delays the command you actually typed.

- Update manually: `hcb upgrade` (or `brew upgrade hackclub/hcb/hcb`)
- Check what you're running: `hcb version`
- Opt out of automatic updates: `export HCB_NO_AUTO_UPDATE=1`

Self-update only activates for Homebrew installs of tagged releases — `dev`
builds from source never auto-update.

## Auth

Credentials live at `~/.config/hcb/credentials.json` (0600):

```json
{
  "base_url": "https://hcb.hackclub.com",
  "client_id": "…", "client_secret": "…",
  "access_token": "hcb_…", "refresh_token": "…",
  "created_at": 1751500000, "expires_in": 7200
}
```

- `hcb login` runs the authorization-code flow with a localhost:8910 callback (HCB's device-flow endpoint 500s in production).
- Access tokens last 2h; the client auto-refreshes and **persists the rotated refresh token immediately** — HCB rotates refresh tokens on every use.

## Build & test

```sh
go build ./...
go test ./...
go run ./cmd/hcb user
```

## MCP server

**Local (stdio)** — for Claude Code / Claude Desktop:

```sh
claude mcp add hcb /path/to/bin/hcb-mcp
```

**Hosted (streamable HTTP)** — the same binary serves `/mcp` over HTTP:

```sh
MCP_AUTH_TOKEN=<random-secret> hcb-mcp --http :8080
```

- Endpoint: `POST /mcp` with `Authorization: Bearer <token>` (or `?key=<token>` for clients that can't set headers). `GET /healthz` is unauthenticated.
- Container bootstrap: set `HCB_CREDENTIALS_JSON` to seed `/data/credentials.json` on first boot (a persistent volume — HCB rotates refresh tokens, so credentials must outlive restarts). See the [Dockerfile](Dockerfile).

Two auth modes, usable together:

1. **Shared secret** (`MCP_AUTH_TOKEN`): a request presenting the secret uses the *server-owned* HCB credentials.
2. **Per-user OAuth** (`HCB_OAUTH_CLIENT_ID` + `HCB_OAUTH_CLIENT_SECRET`): the server hosts the MCP OAuth discovery flow — `/.well-known/oauth-protected-resource`, RFC 8414 metadata, a dynamic-client-registration stub (HCB's Doorkeeper has no DCR, so it returns the pre-registered app), and a token proxy that injects the client secret server-side. OAuth-capable MCP clients (e.g. claude.ai custom connectors) send users through HCB's real authorize page and then call `/mcp` with the user's own HCB token, so **each user sees exactly what their HCB account can see**. Any other bearer token presented to `/mcp` is likewise forwarded upstream as that caller's HCB token. The redirect URIs the client uses (e.g. `https://claude.ai/api/mcp/auth_callback`) must be registered on the HCB OAuth app.

## Coverage

See [COVERAGE.md](COVERAGE.md) — every read flow from [hcb-v4-api-flows.csv](hcb-v4-api-flows.csv) mapped to client method → CLI command → MCP tool → test status.
