#!/usr/bin/env python3
"""Record truncated+redacted fixtures from production HCB for hcbapi unit tests.

Usage: python3 scripts/record_fixtures.py
Reads ~/.config/hcb/credentials.json; writes internal/hcbapi/testdata/*.json.
Read-only: only GET requests.
"""
import json
import os
import sys
import urllib.parse
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "internal", "hcbapi", "testdata")
CREDS = json.load(open(os.path.expanduser("~/.config/hcb/credentials.json")))
BASE = CREDS["base_url"]
TOKEN = CREDS["access_token"]


def get(path, **params):
    url = f"{BASE}{path}"
    if params:
        url += "?" + urllib.parse.urlencode(params)
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {TOKEN}"})
    with urllib.request.urlopen(req) as resp:
        return json.load(resp)


def truncate(node, max_items=2):
    if isinstance(node, list):
        return [truncate(x, max_items) for x in node[:max_items]]
    if isinstance(node, dict):
        return {k: truncate(v, max_items) for k, v in node.items()}
    return node


REDACT_KEYS = {"account_number", "routing_number", "swift_bic_code"}


def redact(node):
    if isinstance(node, list):
        return [redact(x) for x in node]
    if isinstance(node, dict):
        out = {}
        for k, v in node.items():
            if k in REDACT_KEYS and isinstance(v, str):
                out[k] = "REDACTED"
            elif k.endswith("email") and isinstance(v, str):
                out[k] = "redacted@example.com"
            else:
                out[k] = redact(v)
        return out
    return node


def save(name, data):
    os.makedirs(OUT, exist_ok=True)
    with open(os.path.join(OUT, name), "w") as f:
        json.dump(redact(truncate(data)), f, indent=2)
        f.write("\n")
    print(f"  wrote {name}")


def try_save(name, fn):
    try:
        data = fn()
        save(name, data)
        return data
    except Exception as e:
        print(f"  SKIP {name}: {e}")
        return None


def main():
    save("user.json", get("/api/v4/user", expand="shipping_address,billing_address"))
    orgs = get("/api/v4/user/organizations")
    save("user_organizations.json", orgs)
    org_ids = [o["id"] for o in orgs]
    slug_by_id = {o["id"]: o["slug"] for o in orgs}
    hq = next((o["id"] for o in orgs if o["slug"] == "hq"), org_ids[0])

    save("organization.json", get(f"/api/v4/organizations/{hq}",
                                  expand="balance_cents,users,reporting,account_number"))
    try_save("balance_by_date.json", lambda: get(f"/api/v4/organizations/{hq}/balance_by_date"))
    try_save("followers.json", lambda: get(f"/api/v4/organizations/{hq}/followers"))
    subs = None
    for oid in org_ids:
        try:
            subs = get(f"/api/v4/organizations/{oid}/sub_organizations")
            if subs:
                print(f"  (sub_organizations from {slug_by_id[oid]})")
                break
        except Exception:
            continue
    save("sub_organizations.json", subs or [])

    txns = get(f"/api/v4/organizations/{hq}/transactions", limit=5)
    save("transactions.json", txns)
    txn_id = txns["data"][0]["id"]
    save("transaction.json", get(f"/api/v4/transactions/{txn_id}"))
    try_save("memo_suggestions.json",
             lambda: get(f"/api/v4/organizations/{hq}/transactions/{txn_id}/memo_suggestions"))
    save("missing_receipt.json", get("/api/v4/user/transactions/missing_receipt", limit=2))

    save("receipts_bin.json", get("/api/v4/receipts"))
    # find a transaction with receipts for a non-empty fixture
    receipts = []
    rtxn = txn_id
    for tx in txns["data"]:
        if not tx.get("missing_receipt", True):
            try:
                r = get("/api/v4/receipts", transaction_id=tx["id"])
                if r:
                    receipts, rtxn = r, tx["id"]
                    break
            except Exception:
                continue
    print(f"  (receipts from {rtxn}: {len(receipts)})")
    save("receipts_txn.json", receipts or get("/api/v4/receipts", transaction_id=txn_id))
    try_save("comments.json", lambda: get("/api/v4/comments", transaction_id=txn_id))

    tags = try_save("tags.json", lambda: get("/api/v4/tags", event_id=hq)) or []
    if tags:
        try_save("tag.json", lambda: get(f"/api/v4/tags/{tags[0]['id']}"))

    save("user_cards.json", get("/api/v4/user/cards"))
    org_cards = try_save("org_cards.json", lambda: get(f"/api/v4/organizations/{hq}/cards",
                                                       expand="user,total_spent_cents")) or []
    cards = get("/api/v4/user/cards") or org_cards
    if cards:
        cid = cards[0]["id"]
        try_save("card.json", lambda: get(f"/api/v4/cards/{cid}", expand="organization,user,total_spent_cents"))
        try_save("card_transactions.json", lambda: get(f"/api/v4/cards/{cid}/transactions", limit=3))
    try_save("card_designs.json", lambda: get("/api/v4/cards/card_designs"))

    my_grants = get("/api/v4/user/card_grants")
    save("user_card_grants.json", my_grants)
    grant, grant_org = None, None
    if my_grants:
        grant = my_grants[0]["id"]
        grant_org = my_grants[0].get("organization_id")
    else:
        for oid in org_ids:
            try:
                gs = get(f"/api/v4/organizations/{oid}/card_grants")
                if gs:
                    grant, grant_org = gs[0]["id"], oid
                    break
            except Exception:
                continue
    if grant_org:
        try_save("org_card_grants.json", lambda: get(f"/api/v4/organizations/{grant_org}/card_grants"))
    if grant:
        try_save("card_grant.json", lambda: get(f"/api/v4/card_grants/{grant}",
                                                expand="balance_cents,disbursements"))
        try_save("card_grant_transactions.json",
                 lambda: get(f"/api/v4/card_grants/{grant}/transactions", limit=3))

    save("user_invitations.json", get("/api/v4/user/invitations"))
    try_save("org_invitations.json", lambda: get(f"/api/v4/organizations/{hq}/invitations"))

    def first_nonempty(name, path_fn, detail=None):
        for oid in org_ids:
            try:
                data = path_fn(oid)
            except Exception:
                continue
            if data:
                print(f"  ({name} from {slug_by_id[oid]})")
                save(name, data)
                if detail:
                    detail(data)
                return
        print(f"  ({name}: none found anywhere, saving empty)")
        save(name, [])

    first_nonempty("checks.json", lambda oid: get("/api/v4/checks", event_id=oid))
    first_nonempty("check_deposits.json", lambda oid: get("/api/v4/check_deposits", event_id=oid),
                   detail=lambda ds: try_save("check_deposit.json",
                                              lambda: get(f"/api/v4/check_deposits/{ds[0]['id']}")))
    first_nonempty("sponsors.json", lambda oid: get("/api/v4/sponsors", event_id=oid),
                   detail=lambda ss: try_save("sponsor.json",
                                              lambda: get(f"/api/v4/sponsors/{ss[0]['id']}")))
    first_nonempty("invoices.json", lambda oid: get("/api/v4/invoices", event_id=oid),
                   detail=lambda vs: try_save("invoice.json",
                                              lambda: get(f"/api/v4/invoices/{vs[0]['id']}")))

    save("available_icons.json", get("/api/v4/user/available_icons"))
    try_save("token_info.json", lambda: get("/api/v4/oauth/token/info"))
    print("done — now sanitizing for public repo safety")
    import subprocess
    subprocess.run([sys.executable, os.path.join(ROOT, "scripts", "sanitize_fixtures.py")], check=True)


if __name__ == "__main__":
    sys.exit(main())
