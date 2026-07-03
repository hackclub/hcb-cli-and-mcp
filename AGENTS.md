# Agent & Contributor Guide

## ⚠️ This is a PUBLIC repository

Nothing private may be committed — not in files, not in git history:

- **No credentials, ever.** No access/refresh tokens, OAuth client IDs/secrets, or `credentials.json` contents. Credentials live only at `~/.config/hcb/credentials.json` (chmod 600), which is outside the repo.
- **No real personal data in fixtures.** `internal/hcbapi/testdata/` must contain only synthetic values. Fixtures are recorded from production by `scripts/record_fixtures.py`, which automatically runs `scripts/sanitize_fixtures.py` (fakes emails, names, addresses, birthdays, memos, filenames, last4, signed file URLs, and public-id suffixes; keeps only the public `hq` org slug/name). Never bypass the sanitizer; if you add a new fixture field that could carry personal data, extend the sanitizer's key lists first.
- **No real object IDs in code, scripts, or docs.** E2E drivers (`scripts/e2e_cli.sh`, `scripts/e2e-mcp`) discover IDs dynamically from the authenticated user's own data — keep it that way.
- **No signed storage URLs** (`hcb.hackclub.com/storage/...`, `rails/active_storage`). They grant access to real files without authentication.
- **Before committing**, run `scripts/check_public_safety.sh`. It greps the tree for emails, signed URLs, and token-shaped strings and must pass.
- If something private ever lands in a commit, do not just revert it — the history is public. Rewrite history before pushing, and rotate any exposed secret.

## What this project is

Read-only tooling for the [HCB](https://hcb.hackclub.com) v4 API, in Go:

- `internal/hcbapi` — shared client: OAuth (authorization-code login, auto-refresh with rotating refresh tokens), pagination, all read endpoints, signed-URL file downloads.
- `cmd/hcb` — CLI (cobra), JSON output.
- `cmd/hcb-mcp` — MCP server (official `modelcontextprotocol/go-sdk`): stdio by default, streamable HTTP with `--http` (bearer-token protected; used for the hosted deployment).

**Read-only is a hard rule.** This tool talks to a production banking platform. Only GET endpoints (plus the OAuth token endpoints) are allowed. Never add tools/commands that create, update, or delete anything on HCB.

## Working on it

```sh
go build ./... && go test ./...        # unit + component + MCP session tests (offline, fixture-backed)
go build -o bin/hcb ./cmd/hcb && go build -o bin/hcb-mcp ./cmd/hcb-mcp
./scripts/e2e_cli.sh                   # live E2E: every CLI command (needs `hcb login` first)
go run ./scripts/e2e-mcp               # live E2E: every MCP tool over stdio
python3 scripts/record_fixtures.py     # re-record fixtures (auto-sanitizes)
scripts/check_public_safety.sh         # must pass before every commit
```

- TDD: new endpoints get a fixture + a case in `internal/hcbapi/endpoints_test.go`, plus CLI command, MCP tool, and rows in `COVERAGE.md`.
- `COVERAGE.md` is the source of truth mapping every API read flow → client method → CLI command → MCP tool → verification status. Keep it accurate.
- Known upstream quirks are documented in COVERAGE.md (e.g. `GET /checks/:id` 403s for everyone; the device-authorization flow 500s in production, which is why login uses an authorization-code flow with a localhost callback).

## Auth model (for context)

Tokens come from HCB's OAuth (Doorkeeper). Access tokens last 2 hours; refresh tokens **rotate on every use**, so the client persists the new pair immediately after each refresh. A token without HCB's `restricted` scope has legacy full-token access gated by the user's own permissions. The default login requests only `read`, so it sees exactly what the logged-in user can see. `hcb login --admin` requests `restricted read admin:read organizations:read ledgers:read receipts:read user_lookup event_followers`, which requires an auditor/admin HCB account and matching scopes on the OAuth app.
