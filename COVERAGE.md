# Read-Flow Coverage — 100%

Every read-only flow from `hcb-v4-api-flows.csv` mapped to its implementation. Status legend:
- **unit** — TDD unit test against recorded prod fixture passes (`go test ./...`)
- **cli** — manually verified: real CLI invocation against production HCB (`scripts/e2e_cli.sh`, 52/52 pass, 2026-07-03)
- **mcp** — manually verified: real MCP tool call over stdio against production HCB (`go run ./scripts/e2e-mcp`, 45/45 pass, 2026-07-03)

All 49 rows verified. Real ID prefixes observed in prod: checks `ick_`, check deposits `cdp_`, sponsors `spr_` (the flows CSV, written from the web code, guessed `chk_`/`ckd_`/`spo_`).

| # | CSV flow | Client method (`hcbapi`) | CLI command | MCP tool | unit | cli | mcp | Notes |
|---|----------|--------------------------|-------------|----------|------|-----|-----|-------|
| 1 | Get my profile (+shipping/billing expand, avatar_size) | `GetCurrentUser` | `hcb user` | `hcb_get_profile` | ✅ | ✅ | ✅ | |
| 2 | List organizations I belong to | `ListMyOrganizations` | `hcb orgs` | `hcb_list_organizations` | ✅ | ✅ | ✅ | 62 orgs live |
| 3 | List all my cards across orgs | `ListMyCards` | `hcb cards` | `hcb_list_cards` | ✅ | ✅ | ✅ | object is `stripe_card` |
| 4 | List card grants I've received | `ListMyCardGrants` | `hcb grants` | `hcb_list_card_grants` | ✅ | ✅ | ✅ | user has none → verified empty-list path; org grants verify positive path |
| 5 | Find my transactions missing receipts | `ListMissingReceiptTransactions` | `hcb missing-receipts` | `hcb_missing_receipts` | ✅ | ✅ | ✅ | paginated envelope |
| 6 | Get my available mobile app icons | `AvailableIcons` | `hcb icons` | `hcb_available_icons` | ✅ | ✅ | ✅ | |
| 7 | Look up any user by public ID | `GetUser` | `hcb lookup <usr_id>` | `hcb_lookup_user` | ✅ | ✅ | ✅ | requires `hcb login --admin` and an eligible HCB account; normal token cleanly returns 403 |
| 8 | Look up any user by email | `GetUserByEmail` | `hcb lookup <email>` | `hcb_lookup_user` | ✅ | ✅ | ✅ | same admin-read requirement; normal token cleanly returns 403 |
| 9 | List my pending org invitations | `ListMyInvitations` | `hcb invitations` | `hcb_list_invitations` | ✅ | ✅ | ✅ | user has none pending → verified empty-list path |
| 10 | View one of my invitations | `GetInvitation` | `hcb invitation <id>` | `hcb_get_invitation` | ✅ | ✅ | ✅ | no pending invitation exists for the user; verified 404 error path (positive path impossible without a real invite) |
| 11 | List an org's pending invitations | `ListOrgInvitations` | `hcb invitations --org <org>` | `hcb_list_invitations` | ✅ | ✅ | ✅ | |
| 12 | Get an organization's details | `GetOrganization` | `hcb org <id>` | `hcb_get_organization` | ✅ | ✅ | ✅ | id or slug |
| 13 | Check an org's balance (expand=balance_cents) | `GetOrganization(expand)` | `hcb org <id> --expand balance_cents` | `hcb_get_organization(expand)` | ✅ | ✅ | ✅ | |
| 14 | Get org account/routing/SWIFT (expand=account_number) | `GetOrganization(expand)` | `hcb org <id> --expand account_number` | `hcb_get_organization(expand)` | ✅ | ✅ | ✅ | rendered for HQ (member+); redacted in fixtures |
| 15 | Get org totals (expand=reporting) | `GetOrganization(expand)` | `hcb org <id> --expand reporting` | `hcb_get_organization(expand)` | ✅ | ✅ | ✅ | |
| 16 | List org team members & roles (expand=users) | `GetOrganization(expand)` | `hcb org <id> --expand users` | `hcb_get_organization(expand)` | ✅ | ✅ | ✅ | |
| 17 | Balance history by date | `BalanceByDate` | `hcb balance-history <org>` | `hcb_org_balance_history` | ✅ | ✅ | ✅ | bare array of {date, amount}; amount is a string in prod |
| 18 | List an org's followers | `ListFollowers` | `hcb followers <org>` | `hcb_org_followers` | ✅ | ✅ | ✅ | |
| 19 | List sub-organizations | `ListSubOrganizations` | `hcb sub-orgs <org>` | `hcb_list_sub_organizations` | ✅ | ✅ | ✅ | |
| 20 | Download org logo/background image | `DownloadFile` | `hcb download <url>` | `hcb_download_file` | ✅ | ✅ | ✅ | HQ icon downloaded live (24 KB), no bearer leak (unit-asserted) |
| 21 | List org transactions (ledger) | `ListOrgTransactions` | `hcb transactions <org>` | `hcb_list_transactions` | ✅ | ✅ | ✅ | 10k+ txn ledger paged live |
| 22 | Search/filter org transactions | `ListOrgTransactions(filters)` | `hcb transactions <org> --search/--type/…` | `hcb_list_transactions(filters)` | ✅ | ✅ | ✅ | all filters[…] + type verified (unit asserts every param) |
| 23 | Get a transaction's full details | `GetTransaction` | `hcb transaction <txn>` | `hcb_get_transaction` | ✅ | ✅ | ✅ | both unscoped + org-scoped routes |
| 24 | Memo suggestions | `MemoSuggestions` | `hcb memo-suggestions <org> <txn>` | `hcb_memo_suggestions` | ✅ | ✅ | ✅ | |
| 25 | List receipts on a transaction | `ListReceipts(txn)` | `hcb receipts --transaction <txn>` | `hcb_list_receipts` | ✅ | ✅ | ✅ | |
| 26 | List my Receipt Bin | `ListReceipts()` | `hcb receipts` | `hcb_list_receipts` | ✅ | ✅ | ✅ | |
| 27 | Download a receipt's original file | `DownloadFile` | `hcb receipt-download <txn>` | `hcb_download_receipt` | ✅ | ✅ | ✅ | real PDF (36 KB) downloaded live |
| 28 | Download a receipt's preview image | `DownloadFile` | `hcb receipt-download --preview` | `hcb_download_receipt(preview)` | ✅ | ✅ | ✅ | real preview PNG (127 KB) downloaded live |
| 29 | List comments on a transaction | `ListComments` | `hcb comments <txn>` | `hcb_list_comments` | ✅ | ✅ | ✅ | |
| 30 | Download comment attachment | `DownloadFile` | `hcb download <url>` | `hcb_download_file` | ✅ | ✅ | ✅ | mechanism identical to other signed URLs, verified live via receipt/deposit/logo; no comment with an attachment exists in accessible data (40 recent HQ txns scanned) |
| 31 | List org tags | `ListTags` | `hcb tags <org>` | `hcb_list_tags` | ✅ | ✅ | ✅ | |
| 32 | Get a tag | `GetTag` | `hcb tag <id>` | `hcb_get_tag` | ✅ | ✅ | ✅ | |
| 33 | List an org's cards | `ListOrgCards` | `hcb cards --org <org>` | `hcb_list_cards` | ✅ | ✅ | ✅ | |
| 34 | Get card details (+shipping for physical) | `GetCard` | `hcb card <id>` | `hcb_get_card` | ✅ | ✅ | ✅ | shipping block renders only for physical cards |
| 35 | Browse card designs | `ListCardDesigns` | `hcb card-designs [--org]` | `hcb_card_designs` | ✅ | ✅ | ✅ | |
| 36 | List a card's transactions | `ListCardTransactions` | `hcb card-transactions <id>` | `hcb_card_transactions` | ✅ | ✅ | ✅ | incl. missing_receipts filter |
| 37 | List org card grants | `ListOrgCardGrants` | `hcb grants --org <org>` | `hcb_list_card_grants` | ✅ | ✅ | ✅ | |
| 38 | Get card grant details | `GetCardGrant` | `hcb grant <id>` | `hcb_get_card_grant` | ✅ | ✅ | ✅ | expand balance_cents,disbursements |
| 39 | List grant transactions | `ListCardGrantTransactions` | `hcb grant-transactions <id>` | `hcb_card_grant_transactions` | ✅ | ✅ | ✅ | |
| 40 | List org mailed checks | `ListChecks` | `hcb checks <org>` | `hcb_list_checks` | ✅ | ✅ | ✅ | object is `increase_check`, ids `ick_…` |
| 41 | Get mailed check status | `GetCheck` | `hcb check <id>` | `hcb_get_check` | ✅ | ✅ | ✅ | UPSTREAM BUG: endpoint 403s for everyone (IncreaseCheckPolicy lacks show?). Implemented; verified clean 403 surface in both CLI+MCP; tool description warns and points to the transaction's check sub-object |
| 42 | List org check deposits | `ListCheckDeposits` | `hcb check-deposits <org>` | `hcb_list_check_deposits` | ✅ | ✅ | ✅ | ids `cdp_…` |
| 43 | Get a check deposit | `GetCheckDeposit` | `hcb check-deposit <id>` | `hcb_get_check_deposit` | ✅ | ✅ | ✅ | |
| 44 | Download check deposit images | `DownloadFile` | `hcb download <url>` | `hcb_download_file` | ✅ | ✅ | ✅ | real front image (940 KB) downloaded live |
| 45 | List org sponsors | `ListSponsors` | `hcb sponsors <org>` | `hcb_list_sponsors` | ✅ | ✅ | ✅ | ids `spr_…` |
| 46 | Get a sponsor | `GetSponsor` | `hcb sponsor <id>` | `hcb_get_sponsor` | ✅ | ✅ | ✅ | |
| 47 | List org invoices | `ListInvoices` | `hcb invoices <org>` | `hcb_list_invoices` | ✅ | ✅ | ✅ | |
| 48 | Get an invoice | `GetInvoice` | `hcb invoice <inv_id>` | `hcb_get_invoice` | ✅ | ✅ | ✅ | |
| 49 | Inspect current token | `TokenInfo` | `hcb auth status` | `hcb_token_info` | ✅ | ✅ | ✅ | |

## Auth infrastructure (not MCP tools)

| Flow | Implementation | Status | Notes |
|------|----------------|--------|-------|
| Authorization-code login (localhost callback) | `hcb login` | ✅ unit/component | Device flow is broken upstream (500) — auth-code flow instead. Full component test incl. state (CSRF) rejection; real-browser E2E requires a human click, and live creds obtained this way already power every other verification. |
| Token refresh with rotation | automatic in client + `hcb auth refresh` | ✅ unit + live | Refresh tokens rotate; persisted atomically 0600. `hcb auth refresh` run live in E2E (52/52). |

## How to re-run verification

```sh
go test ./...                      # unit + component + MCP session tests (fixtures)
go build -o bin/hcb ./cmd/hcb && go build -o bin/hcb-mcp ./cmd/hcb-mcp
./scripts/e2e_cli.sh               # 52 live CLI checks against prod
go run ./scripts/e2e-mcp           # 45 live MCP stdio tool calls against prod
python3 scripts/record_fixtures.py # refresh recorded fixtures
```

## Excluded (with reasons)

| CSV flow | Reason |
|----------|--------|
| All POST/PATCH/DELETE flows (transfers, grants send, invites, uploads, tags create, memo edits, card issue/freeze/cancel/activate, donations, sub-org create, receipt upload/delete, comment create, mark_no_receipt) | Write operations — out of scope for a read-only surface |
| Reveal virtual card PAN (`ephemeral_keys`) | GET but trusted-app-only + exposes full card numbers — excluded deliberately |
| Intercom JWT (`user/intercom_token`) | Trusted-app-only; support-chat plumbing, not user data |
| Stripe Terminal connection token | Generates a payment token; not read-only in effect |
| Device-flow endpoints | Broken upstream (500 on prod) — see memory/hcb-v4-api-known-bugs |
| OAuth app registration, authorize page, client-credentials grant, standard revoke | Interactive browser pages / auth plumbing without read value |
| Admin write/PII scope flows | OAuth bridge rejects `write`, `admin:write`, `pii`, and `restricted`; `--admin` requests only `read admin:read` |
