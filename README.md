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

## Coverage

See [COVERAGE.md](COVERAGE.md) — every read flow from [hcb-v4-api-flows.csv](hcb-v4-api-flows.csv) mapped to client method → CLI command → MCP tool → test status.
