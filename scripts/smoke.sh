#!/usr/bin/env bash
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
#
# The smoke run of the local stack: step 3 of the refactor's exit test against
# the stack `make up` started. Uses curl, jq (python3 when jq is missing) and
# psql inside the Postgres container. Prints each check and exits non zero on
# the first failure.
#
#   make up && make smoke && make down
#
# GABLE_WEB_PORT (default 8080) is the host port of the web service.
set -euo pipefail

cd "$(dirname "$0")/.."
BASE="http://127.0.0.1:${GABLE_WEB_PORT:-8080}"
PG_USER="${POSTGRES_USER:-gable_user}"
PG_DB="${POSTGRES_DB:-gable_db}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass() { echo "ok   $1"; }
fail() { echo "FAIL $1" >&2; [ -n "${2:-}" ] && echo "     $2" >&2; exit 1; }

# json <expr> reads JSON on stdin and prints the jq expression's raw result.
if command -v jq >/dev/null 2>&1; then
  json() { jq -r "$1"; }
else
  # python3 stand in for the few jq forms used below: .a.b[0].c paths,
  # `| length`, and `// empty`.
  json() {
    python3 -c '
import json, re, sys
expr = sys.argv[1]
data = json.load(sys.stdin)
length = expr.endswith("| length")
expr = re.sub(r"\s*\|\s*length$", "", expr)
default_empty = "// empty" in expr
expr = expr.replace("// empty", "").strip()
cur = data
try:
    for part in re.findall(r"\.([A-Za-z_][A-Za-z0-9_]*)|\[(\d+)\]", expr):
        cur = cur[part[0]] if part[0] else cur[int(part[1])]
except (KeyError, IndexError, TypeError):
    cur = None
if length:
    cur = len(cur)
if cur is None:
    print("" if default_empty else "null")
elif isinstance(cur, bool):
    print("true" if cur else "false")
elif isinstance(cur, (dict, list)):
    print(json.dumps(cur))
else:
    print(cur)
' "$1"
  }
fi

# psql_q runs one query inside the Postgres container.
psql_q() { docker compose exec -T postgres psql -U "$PG_USER" -d "$PG_DB" -At -c "$1"; }

# --- the web tier -----------------------------------------------------------

code=$(curl -sS -o "$TMP/door.html" -w '%{http_code}' "$BASE/")
[ "$code" = 200 ] || fail "front door: GET / is 200" "got $code"
grep -q '/door/' "$TMP/door.html" || fail "front door: GET / serves the front door bundle" "no /door/ asset in the page"
! grep -q '/assets/' "$TMP/door.html" || fail "front door: GET / is not the desk" "page carries a desk asset"
pass "the front door is served at /"

code=$(curl -sS -o "$TMP/apps.json" -w '%{http_code}' "$BASE/api/v1/apps")
[ "$code" = 200 ] || fail "front door catalog: GET /api/v1/apps is 200" "got $code"
enabled=$(json '.apps | length' < "$TMP/apps.json")
[ "$enabled" -gt 0 ] || fail "front door catalog lists apps" "empty catalog"
grep -q '"enabled": *true' "$TMP/apps.json" || fail "front door catalog has an enabled app" "none enabled"
pass "the catalog the door reads lists $enabled apps, at least one enabled"

code=$(curl -sS -o "$TMP/home.html" -w '%{http_code}' "$BASE/home")
[ "$code" = 200 ] && grep -q '/assets/' "$TMP/home.html" && ! grep -q '/door/' "$TMP/home.html" \
  || fail "the desk opens: GET /home serves the desk" "status $code"
pass "the desk opens at /home"

# --- a quote through the API ------------------------------------------------

first_id() { # path -> id of the first row (bare array, items or data envelope)
  curl -fsS "$BASE$1" | python3 -c '
import json, sys
d = json.load(sys.stdin)
rows = d if isinstance(d, list) else d.get("items") or d.get("data") or []
print(rows[0]["id"])'
}
BRANCH=$(first_id /api/v1/branches)
CUSTOMER=$(first_id /api/v1/customers)
PRODUCT=$(first_id /api/v1/products)
[ -n "$BRANCH" ] && [ -n "$CUSTOMER" ] && [ -n "$PRODUCT" ] || fail "seed data present (branch, customer, product)"
pass "seed data present (a branch, a customer, a product)"

KEY="smoke-$(date +%s)-$$"
cat > "$TMP/quote.json" <<JSON
{"branch_id":"$BRANCH","customer_id":"$CUSTOMER","delivery_type":"pickup",
 "lines":[{"product_id":"$PRODUCT","description":"smoke line","quantity":"4","unit_price_ten_thousandths":55000}]}
JSON

code=$(curl -sS -o "$TMP/q1.json" -w '%{http_code}' -X POST "$BASE/api/v1/quotes" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: $KEY" --data @"$TMP/quote.json")
[ "$code" = 201 ] || fail "create a quote: POST /api/v1/quotes is 201" "got $code: $(cat "$TMP/q1.json")"
QID=$(json '.id' < "$TMP/q1.json")
number=$(json '.number' < "$TMP/q1.json")
status=$(json '.status' < "$TMP/q1.json")
echo "$number" | grep -Eq '^Q-[0-9]{6,}$' || fail "the quote has a document number like Q-000123" "number is '$number'"
[ "$status" = "$(echo "$status" | tr 'A-Z' 'a-z')" ] && [ -n "$status" ] || fail "the quote status is lowercase" "status is '$status'"
python3 - "$TMP/q1.json" <<'PY' || fail "every _cents field of the quote is an integer"
import json, sys
q = json.load(open(sys.argv[1]))
vals = []
def walk(o):
    if isinstance(o, dict):
        for k, v in o.items():
            if k.endswith("_cents"):
                vals.append(v)
            walk(v)
    elif isinstance(o, list):
        for v in o:
            walk(v)
walk(q)
assert vals, "no _cents field on the quote"
assert all(isinstance(v, int) and not isinstance(v, bool) for v in vals), vals
PY
pass "a created quote has number $number, status '$status' and integer _cents money"

code=$(curl -sS -o "$TMP/list.json" -w '%{http_code}' "$BASE/api/v1/quotes?limit=1")
[ "$code" = 200 ] || fail "list quotes is 200" "got $code"
python3 - "$BASE" "$QID" <<'PY' || fail "the quote appears in the cursor envelope"
import json, sys, urllib.request
base, qid = sys.argv[1], sys.argv[2]
cursor, pages, found = None, 0, False
while True:
    url = base + "/api/v1/quotes?limit=100" + ("&cursor=" + cursor if cursor else "")
    d = json.load(urllib.request.urlopen(url))
    assert isinstance(d.get("items"), list), "no items array"
    assert "next_cursor" in d, "no next_cursor"
    pages += 1
    found = found or any(q["id"] == qid for q in d["items"])
    cursor = d["next_cursor"]
    if found or not cursor:
        break
assert found, "quote not listed after %d pages" % pages
PY
pass "the quote is listed in the cursor envelope (items, next_cursor)"

code=$(curl -sS -o "$TMP/open.html" -w '%{http_code}' "$BASE/quotes/$QID")
[ "$code" = 200 ] && grep -q '/assets/' "$TMP/open.html" || fail "/quotes/{id} opens the desk directly" "status $code"
pass "/quotes/$QID opens directly (nginx serves the desk)"

# --- validation ---------------------------------------------------------------

code=$(curl -sS -o "$TMP/uom.json" -w '%{http_code}' -X POST "$BASE/api/v1/quotes" \
  -H 'Content-Type: application/json' \
  --data "{\"branch_id\":\"$BRANCH\",\"customer_id\":\"$CUSTOMER\",\"delivery_type\":\"pickup\",\"lines\":[{\"description\":\"no unit\",\"quantity\":\"1\",\"unit_price_ten_thousandths\":100}]}")
[ "$code" = 400 ] || fail "a line with no uom and no product is 400" "got $code: $(cat "$TMP/uom.json")"
grep -q 'lines\[0\]\.uom' "$TMP/uom.json" || fail "the 400 names the field lines[0].uom" "$(cat "$TMP/uom.json")"
pass "a line with neither a unit of measure nor a product is 400 naming lines[0].uom"

# --- idempotency across a core restart --------------------------------------

docker compose restart core >/dev/null
for _ in $(seq 1 60); do
  curl -fsS "$BASE/api/v1/apps" >/dev/null 2>&1 && break
  sleep 1
done
curl -fsS "$BASE/api/v1/apps" >/dev/null || fail "core is back after the restart"
code=$(curl -sS -D "$TMP/q2.hdr" -o "$TMP/q2.json" -w '%{http_code}' -X POST "$BASE/api/v1/quotes" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: $KEY" --data @"$TMP/quote.json")
[ "$code" = 201 ] || fail "the replay after a core restart is 201" "got $code: $(cat "$TMP/q2.json")"
cmp -s "$TMP/q1.json" "$TMP/q2.json" || fail "the replay returns the first response byte for byte" "$(cat "$TMP/q2.json")"
made=$(psql_q "SELECT count(*) FROM quotes WHERE id = '$QID'")
total=$(psql_q "SELECT count(*) FROM quotes WHERE id = '$(json '.id' < "$TMP/q2.json")'")
[ "$made" = 1 ] && [ "$total" = 1 ] || fail "the replay made no second quote" "rows: $made / $total"
same=$(psql_q "SELECT count(*) FROM quotes WHERE number = '$number'")
[ "$same" = 1 ] || fail "one quote under the number $number" "rows: $same"
pass "the same create with one Idempotency-Key, a core restart between, returned the first response and made one quote"

# --- the events feed ----------------------------------------------------------

walk_feed() { # prints "<count of quote.created for $QID> <last cursor>" walking from the feed's start
  python3 - "$BASE" "$QID" <<'PY'
import json, sys, urllib.request
base, qid = sys.argv[1], sys.argv[2]
cursor, count = "", 0
while True:
    url = base + "/api/v1/events?limit=200" + ("&cursor=" + cursor if cursor else "")
    d = json.load(urllib.request.urlopen(url))
    for e in d["items"]:
        if e["type"] == "quote.created" and e["entity"]["id"] == qid:
            count += 1
    if cursor == d["next_cursor"] or not d["items"]:
        cursor = d["next_cursor"]
        break
    cursor = d["next_cursor"]
print(count, cursor)
PY
}
# The worker is not needed for the feed (it reads the outbox table directly).
read -r seen cursor < <(walk_feed)
[ "$seen" = 1 ] || fail "quote.created is read exactly once from the start of the feed" "read $seen times"
pass "quote.created for the quote is read exactly once walking GET /api/v1/events?cursor= from its start"
after=$(curl -fsS "$BASE/api/v1/events?limit=200&cursor=$cursor")
[ "$(json '.items | length' <<< "$after")" = 0 ] || fail "reading again from the returned cursor yields nothing new" "$after"
pass "reading again from the returned cursor yields nothing new"

# --- a scoped machine key -----------------------------------------------------

code=$(curl -sS -o "$TMP/key.json" -w '%{http_code}' -X POST "$BASE/api/v1/admin/keys" \
  -H 'Content-Type: application/json' --data '{"name":"smoke-read-only","scopes":["quotes:read"]}')
[ "$code" = 201 ] || [ "$code" = 200 ] || fail "mint a scoped key (quotes:read)" "got $code"
APIKEY=$(json '.api_key' < "$TMP/key.json")
KEYID=$(json '.key.id' < "$TMP/key.json")
[ -n "$APIKEY" ] && [ "$APIKEY" != null ] && [ -n "$KEYID" ] && [ "$KEYID" != null ] || fail "the minted key carries api_key and key.id"
code=$(curl -sS -o "$TMP/refused.json" -w '%{http_code}' -X POST "$BASE/api/v1/quotes" \
  -H "Authorization: Bearer $APIKEY" -H 'Content-Type: application/json' --data @"$TMP/quote.json")
[ "$code" = 403 ] || fail "a key without quotes:write is refused with 403" "got $code: $(cat "$TMP/refused.json")"
pass "a call with a key lacking quotes:write is refused with 403"
audit=$(psql_q "SELECT action || '|' || actor_kind || '|' || actor_id::text FROM audit_log WHERE entity_type = 'api_key' AND entity_id = '$KEYID'::uuid ORDER BY created_at, id")
[ "$audit" = "key.scope_refused|key|$KEYID" ] || fail "the refusal is audited with the key's id" "audit rows: '$audit'"
pass "the refusal is audited: key.scope_refused with actor_kind key and actor_id $KEYID"

echo "smoke: all checks passed"
