# HCB v4 API — user flow catalog

`hcb-v4-api-flows.csv` catalogs 103 user flows across the [HCB](https://hcb.hackclub.com) v4 REST API, one row per task a client might perform.

## Columns

- **category / flow** — human grouping and the task the row documents. Several flows can share one endpoint (e.g. the `expand=` variants of getting an organization); flows spanning multiple calls are marked "(composite)" in the flow name.
- **method** — HTTP method(s). `X then Y` is a multi-step sequence within one flow; `X or Y` means alternative methods.
- **endpoint** — a route template when it starts with `/`. Values not starting with `/` are not routes: they name signed-URL fields returned inside response objects (fetch those URLs directly, no Authorization header), multi-step chains (`→`), or meta-rows ("any v4 read endpoint"). Placeholders: `:org` is an organization public id (`org_…`) or slug; `:id` is the row's resource public id (`txn_…`, `crd_…`, `rct_…`, …).
- **oauth_scope_restricted_tokens** — what a *restricted* OAuth token needs:
  - a scope name (e.g. `ledgers:read`) — required on restricted tokens; unrestricted (`read`/`write`) tokens also pass;
  - `none` — the endpoint declares no granular scope, so restricted tokens are blocked entirely; unrestricted and legacy tokens work;
  - `n/a` — not a v4 bearer-token endpoint (web UI page, OAuth client-credential endpoint, or direct signed-URL download).
- **min_role_or_permission** — the minimum org role (reader < member < manager) or other principal that passes the endpoint's policy.
- **key_params** — notable path/query/body params. Query params live here, not in the endpoint column.
- **notes** — response shape, behavior details, caveats, and known production bugs.

## Provenance

Compiled from the open-source HCB codebase ([hackclub/hcb](https://github.com/hackclub/hcb)) and verified against production on 2026-07-02/03. Exact figures spot-checked against source: 2-hour access-token expiry (`config/initializers/doorkeeper.rb`), $500.00 sudo-mode cap on API ACH/check sends (`SudoModeHandler::THRESHOLD_CENTS`), pagination default 25 / max 100, 50 MB receipt upload limit, 5-minute `balance_by_date` cache.

Known production bugs are annotated inline on their rows: the OAuth device authorization flow 500s with valid client credentials, and `GET /api/v4/checks/:id` always 403s (`IncreaseCheckPolicy` defines no `show?`).

Values will drift as HCB evolves; re-verify before relying on exact figures.
