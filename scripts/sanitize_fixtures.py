#!/usr/bin/env python3
"""Sanitize recorded fixtures so they are safe for a PUBLIC repository.

Replaces all personal/sensitive values with synthetic ones while preserving
JSON shape exactly: emails, names, addresses, birthdays, signed file URLs,
memos, filenames, card last4, and public-id suffixes (prefixes kept, so tests
that rely on id shape still work; references stay consistent via a global map).

Kept as-is: the `hq` org slug/name (used by tests; HCB HQ is a public,
transparent org), object types, statuses, booleans, amounts, dates.

Run automatically at the end of record_fixtures.py; can be run standalone:
    python3 scripts/sanitize_fixtures.py
"""
import hashlib
import json
import os
import re
import sys

TESTDATA = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                        "internal", "hcbapi", "testdata")

ID_RE = re.compile(r"^([a-z]{2,4})_[A-Za-z0-9]{4,}$")
URL_RE = re.compile(r"^https?://")

EMAIL_KEYS = {"email", "contact_email", "recipient_email"}
MEMO_KEYS = {"memo", "custom_memo", "payment_for", "purpose", "instructions",
             "item_description", "description", "invite_message", "message",
             "keyword_lock"}
ADDRESS_VALUES = {
    "address_line1": "1 Example St", "line1": "1 Example St",
    "address_line2": "", "line2": "",
    "address_city": "Exampleville", "city": "Exampleville",
    "address_state": "VT", "state": "VT",
    "address_zip": "05401", "zip": "05401",
    "postal_code": "05401", "address_postal_code": "05401",
    "address_country": "US",
}
NAME_KEYS = {"name", "recipient_name", "shipping_name", "smart_name", "bank_name", "to"}
KEEP_NAMES = {"Hack Club HQ", "HCB", "Hack Club"}

_id_map = {}


def fake_id(value: str) -> str:
    if value in _id_map:
        return _id_map[value]
    prefix = value.split("_", 1)[0]
    digest = hashlib.sha256(value.encode()).hexdigest()[:6]
    fake = f"{prefix}_x{digest}"
    _id_map[value] = fake
    return fake


def fake_url(key: str) -> str:
    ext = "png" if key in {"icon", "avatar", "preview_url", "logo_url", "front_url",
                           "back_url", "background_image"} else "pdf"
    return f"https://hcb.example.com/files/example-{key or 'file'}.{ext}"


def sanitize(node, key=""):
    if isinstance(node, dict):
        return {k: sanitize(v, k) for k, v in node.items()}
    if isinstance(node, list):
        return [sanitize(v, key) for v in node]
    if isinstance(node, str):
        if key in EMAIL_KEYS:
            return "user@example.com"
        if key == "birthday":
            return "2000-01-01"
        if key in ADDRESS_VALUES:
            return ADDRESS_VALUES[key]
        if key in NAME_KEYS and node not in KEEP_NAMES:
            return "Example Name"
        if key in MEMO_KEYS and node:
            return "Example memo"
        if key == "filename":
            return "example-receipt.pdf"
        if key == "last4":
            return "1234"
        if key == "slug" and node != "hq":
            return "example-org"
        if URL_RE.match(node):
            return fake_url(key)
        if ID_RE.match(node) and node != "hq":
            return fake_id(node)
        # catch bare emails anywhere else
        if "@" in node and "." in node.split("@")[-1] and " " not in node:
            return "user@example.com"
        return node
    return node


def main():
    changed = 0
    for fname in sorted(os.listdir(TESTDATA)):
        if not fname.endswith(".json"):
            continue
        path = os.path.join(TESTDATA, fname)
        with open(path) as f:
            data = json.load(f)
        clean = sanitize(data)
        with open(path, "w") as f:
            json.dump(clean, f, indent=2)
            f.write("\n")
        changed += 1
    print(f"sanitized {changed} fixture files in {TESTDATA}")

    # safety net: fail loudly if anything sensitive-looking survived
    bad = []
    email_re = re.compile(r'[a-zA-Z0-9._%+-]+@(?!example\.com)[a-zA-Z0-9.-]+\.[a-z]{2,}')
    for fname in sorted(os.listdir(TESTDATA)):
        text = open(os.path.join(TESTDATA, fname)).read()
        if "hackclub.com/storage" in text or "rails/active_storage" in text:
            bad.append(f"{fname}: signed storage URL")
        for m in email_re.findall(text):
            bad.append(f"{fname}: email {m}")
    if bad:
        print("SANITIZATION INCOMPLETE:", *bad, sep="\n  ")
        sys.exit(1)
    print("safety check passed: no emails or signed URLs remain")


if __name__ == "__main__":
    main()
