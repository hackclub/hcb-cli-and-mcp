#!/usr/bin/env bash
# Manual E2E: run every CLI command against production HCB (read-only).
# Requires local credentials (`hcb login`). IDs are discovered dynamically from
# data the authenticated user can read — nothing is hardcoded.
#
# Usage: scripts/e2e_cli.sh [path-to-hcb-binary]
#   HCB_E2E_ORG  override the org used for org-scoped checks (default: hq)
set -u
B="${1:-./bin/hcb}"
ORG="${HCB_E2E_ORG:-hq}"
OUT=$(mktemp -d)
pass=0; fail=0; skip=0

# --- discover ids from readable data ---
eval "$(python3 - "$B" "$ORG" <<'EOF'
import json, subprocess, sys
B, ORG = sys.argv[1], sys.argv[2]

def run(*args):
    p = subprocess.run([B, *args], capture_output=True, text=True)
    if p.returncode != 0:
        return None
    try:
        return json.loads(p.stdout)
    except Exception:
        return None

def first_id(data):
    if isinstance(data, dict):
        data = data.get("data", [])
    return data[0]["id"] if data else ""

out = {}
out["TXN"] = ""
out["RTXN"] = ""  # transaction that has receipts
txns = run("transactions", ORG, "--limit", "25") or {}
for tx in txns.get("data", []):
    out["TXN"] = out["TXN"] or tx["id"]
    if not out["RTXN"] and not tx.get("missing_receipt", True):
        receipts = run("receipts", "--transaction", tx["id"])
        if receipts:
            out["RTXN"] = tx["id"]
if not out["RTXN"]:
    out["RTXN"] = out["TXN"]

out["TAG"] = first_id(run("tags", ORG))
out["CARD"] = first_id(run("cards"))
out["GRANT"] = first_id(run("grants", "--org", ORG))
out["CHECK"] = first_id(run("checks", ORG))
out["DEPOSIT"] = first_id(run("check-deposits", ORG))
out["SPONSOR"] = first_id(run("sponsors", ORG))
out["INVOICE"] = first_id(run("invoices", ORG))
for k, v in out.items():
    print(f'{k}="{v}"')
EOF
)"
echo "discovered: TXN=$TXN RTXN=$RTXN TAG=$TAG CARD=$CARD GRANT=$GRANT CHECK=$CHECK DEPOSIT=$DEPOSIT SPONSOR=$SPONSOR INVOICE=$INVOICE"

check() { # name, expectation ("ok" or "err"), cmd...
  local name="$1" expect="$2"; shift 2
  for arg in "$@"; do
    if [ -z "$arg" ]; then echo "SKIP  $name (no id discovered)"; skip=$((skip+1)); return; fi
  done
  if "$@" >"$OUT/last.out" 2>"$OUT/last.err"; then got=ok; else got=err; fi
  if [ "$got" = "$expect" ]; then
    echo "PASS  $name"
    pass=$((pass+1))
  else
    echo "FAIL  $name (expected $expect, got $got)"
    sed 's/^/      /' "$OUT/last.err" | head -3
    fail=$((fail+1))
  fi
}

MY_ID=$($B user | python3 -c "import json,sys; print(json.load(sys.stdin)['id'])")

check "auth status"            ok  $B auth status
check "auth refresh"           ok  $B auth refresh
check "user"                   ok  $B user
check "user --expand"          ok  $B user --expand shipping_address,billing_address --avatar-size 64
check "icons"                  ok  $B icons
check "lookup by id (needs admin:read -> err)" err $B lookup "$MY_ID"
check "lookup by email (err)"  err $B lookup user@example.com
check "orgs"                   ok  $B orgs
check "orgs --expand balance"  ok  $B orgs --expand balance_cents
check "org"                    ok  $B org $ORG
check "org --expand all"       ok  $B org $ORG --expand balance_cents,users,account_number,reporting
check "balance-history"        ok  $B balance-history $ORG
check "followers"              ok  $B followers $ORG
check "sub-orgs"               ok  $B sub-orgs $ORG
check "transactions"           ok  $B transactions $ORG --limit 3
check "transactions filters"   ok  $B transactions $ORG --limit 3 --type card_charge --expenses --min 1 --max 100000 --start 2020-01-01
check "transactions search"    ok  $B transactions $ORG --limit 3 --search a
check "transaction"            ok  $B transaction "$TXN"
check "transaction --org"      ok  $B transaction "$TXN" --org $ORG
check "memo-suggestions"       ok  $B memo-suggestions $ORG "$TXN"
check "missing-receipts"       ok  $B missing-receipts --limit 3
check "receipts (bin)"         ok  $B receipts
check "receipts --transaction" ok  $B receipts --transaction "$RTXN"
check "receipt-download"       ok  $B receipt-download "$RTXN" --out "$OUT/receipts"
check "receipt-download --preview" ok $B receipt-download "$RTXN" --out "$OUT/receipts" --preview
check "comments"               ok  $B comments "$TXN"
check "tags"                   ok  $B tags $ORG
check "tag"                    ok  $B tag "$TAG"
check "cards (mine)"           ok  $B cards
check "cards --org"            ok  $B cards --org $ORG --expand user
check "card"                   ok  $B card "$CARD" --expand organization,total_spent_cents
check "card-transactions"      ok  $B card-transactions "$CARD" --limit 3
check "card-transactions --missing-receipts" ok $B card-transactions "$CARD" --missing-receipts --limit 3
check "card-designs"           ok  $B card-designs
check "card-designs --org"     ok  $B card-designs --org $ORG
check "grants (mine)"          ok  $B grants
check "grants --org"           ok  $B grants --org $ORG --expand balance_cents
check "grant"                  ok  $B grant "$GRANT" --expand balance_cents,disbursements
check "grant-transactions"     ok  $B grant-transactions "$GRANT" --limit 3
check "invitations (mine)"     ok  $B invitations
check "invitations --org"      ok  $B invitations --org $ORG
check "invitation (unknown -> err)" err $B invitation ivt_doesnotexist
check "checks"                 ok  $B checks $ORG
check "check (upstream 403 bug -> err)" err $B check "$CHECK"
check "check-deposits"         ok  $B check-deposits $ORG
check "check-deposit"          ok  $B check-deposit "$DEPOSIT"
check "sponsors"               ok  $B sponsors $ORG
check "sponsor"                ok  $B sponsor "$SPONSOR"
check "invoices"               ok  $B invoices $ORG
check "invoice"                ok  $B invoice "$INVOICE"

# file downloads: org logo + check deposit image via generic download
ICON_URL=$($B org $ORG | python3 -c "import json,sys; print(json.load(sys.stdin).get('icon') or '')")
check "download org logo"      ok  $B download "$ICON_URL" --out "$OUT/files"
FRONT_URL=""
if [ -n "$DEPOSIT" ]; then
  FRONT_URL=$($B check-deposit "$DEPOSIT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('front_url') or '')")
fi
check "download deposit image" ok  $B download "$FRONT_URL" --out "$OUT/files" --name front.png

echo
echo "downloaded files:"
ls -la "$OUT/receipts" "$OUT/files" 2>/dev/null | sed 's/^/  /'
echo
echo "RESULT: $pass passed, $fail failed, $skip skipped"
[ "$fail" -eq 0 ]
