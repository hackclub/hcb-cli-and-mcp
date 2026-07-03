#!/usr/bin/env bash
# Public-repo safety check: fail if anything private-looking is in the tree.
# Run before every commit (see AGENTS.md).
set -u
cd "$(dirname "$0")/.."
SELF="scripts/check_public_safety.sh"
bad=0

flag() { echo "UNSAFE: $1"; bad=1; }

# 1. Real emails (anything not @example.com / noreply@anthropic.com)
hits=$(grep -rInoE '[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-z]{2,}' \
  --include="*.go" --include="*.json" --include="*.md" --include="*.sh" --include="*.py" --include="*.csv" \
  . 2>/dev/null | grep -v "$SELF" | grep -vE '@example\.com|noreply@anthropic\.com|user@|owner@' || true)
[ -n "$hits" ] && flag "email addresses found:" && echo "$hits" | head -10

# 2. Signed storage URLs (real URLs only — pattern mentions in docs/tools are fine)
hits=$(grep -rIn "https://hcb.hackclub.com/storage\|https://[a-z0-9]*\.cloudfront\.net" \
  --include="*.go" --include="*.json" --include="*.md" --include="*.sh" --include="*.py" . 2>/dev/null | grep -v "$SELF" || true)
[ -n "$hits" ] && flag "signed storage / CDN URLs found:" && echo "$hits" | head -10

# 3. Token-shaped strings (hcb_ live tokens are ~40+ chars; allow short doc placeholders)
hits=$(grep -rInoE '"?hcb_[A-Za-z0-9]{20,}' \
  --include="*.go" --include="*.json" --include="*.md" --include="*.sh" --include="*.py" . 2>/dev/null | grep -v "$SELF" || true)
[ -n "$hits" ] && flag "possible live tokens found:" && echo "$hits" | head -10

# 4. OAuth client secrets / credentials files
hits=$(grep -rIn "client_secret\"\s*:\s*\"[A-Za-z0-9]" --include="*.json" . 2>/dev/null || true)
[ -n "$hits" ] && flag "client_secret value in a JSON file:" && echo "$hits" | head -5
[ -f credentials.json ] && flag "credentials.json in repo root"

# 5. Birthdays / addresses that escaped sanitization (fixture-specific)
hits=$(grep -rIn '"birthday": "(19|20)[0-9]{2}-' internal/hcbapi/testdata 2>/dev/null | grep -v '2000-01-01' || true)
[ -n "$hits" ] && flag "real birthday in fixtures:" && echo "$hits"

if [ "$bad" -eq 0 ]; then
  echo "public safety check passed"
else
  exit 1
fi
