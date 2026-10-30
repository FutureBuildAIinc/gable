// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote_test

// QE-01-b: a dealer revises a sent quote; it keeps its number, becomes the
// next version, and every sent version is kept and readable (Colton's ruling,
// the same model as the portal's Revise). A send writes its version's one
// immutable snapshot row inside the send's transaction; the revise revokes
// any live share link and hands the quote back to the builder as a draft;
// the order keeps converting from the one quote id.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// sentQuote creates a quote and sends it, answering its id and the revision
// the send left. The send is the desk's own route.
func sentQuote(t *testing.T, f *fixture, lines ...map[string]any) (id string, rev int64) {
	t.Helper()
	created := f.create(lines...)
	id = str(t, created.body, "id")
	if num(t, created.body, "version_number") != 1 {
		t.Fatalf("a new quote's version_number = %v, want 1", created.body["version_number"])
	}
	sent := f.do("POST", "/api/v1/quotes/"+id+"/transitions", map[string]any{"to": "sent", "revision": 1})
	if sent.status != 200 {
		t.Fatalf("send = %d: %s", sent.status, sent.raw)
	}
	return id, num(t, sent.body, "revision")
}

// revisionRows reads the quote's stored snapshot rows, lowest version first.
func revisionRows(t *testing.T, f *fixture, quoteID string) []map[string]any {
	t.Helper()
	rows, err := f.db.Pool.Query(context.Background(),
		`SELECT version_number, snapshot, COALESCE(created_by, ''), reason, created_at
		FROM quote_revisions WHERE quote_id = $1 ORDER BY version_number`, quoteID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var version int
		var snapshot []byte
		var createdBy, reason string
		var createdAt time.Time
		if err := rows.Scan(&version, &snapshot, &createdBy, &reason, &createdAt); err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		dec := json.NewDecoder(bytes.NewReader(snapshot))
		dec.UseNumber()
		if err := dec.Decode(&body); err != nil {
			t.Fatalf("snapshot of v%d is not JSON: %v", version, err)
		}
		out = append(out, map[string]any{
			"version_number": version, "snapshot": body, "created_by": createdBy,
			"reason": reason, "created_at": createdAt,
		})
	}
	return out
}

// snapshotTotal reads a snapshot row's total_cents as an int64.
func snapshotTotal(t *testing.T, row map[string]any) int64 {
	t.Helper()
	snap := row["snapshot"].(map[string]any)
	n, ok := snap["total_cents"].(json.Number)
	if !ok {
		t.Fatalf("snapshot total_cents = %T (%v), want an integer", snap["total_cents"], snap["total_cents"])
	}
	v, err := n.Int64()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// snapshotQty reads a snapshot's first line quantity as a string.
func snapshotQty(t *testing.T, row map[string]any) string {
	t.Helper()
	snap := row["snapshot"].(map[string]any)
	lines := snap["lines"].([]any)
	q, ok := lines[0].(map[string]any)["quantity"].(string)
	if !ok {
		t.Fatalf("snapshot line quantity = %T (%v), want a decimal string", lines[0].(map[string]any)["quantity"], lines[0].(map[string]any)["quantity"])
	}
	return q
}

// RULE (Colton's ruling): a sent quote revised stays Q-NNNNNN and becomes the
// next version. The quote's id and number never move, version_number goes to
// 2, the quote returns to DRAFT for the builder, and the act writes its own
// quote.revised event inside its transaction.
func TestQE01_ReviseSentKeepsNumberBumpsVersion(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, rev := sentQuote(t, f)
	number := str(t, f.do("GET", "/api/v1/quotes/"+id, nil).body, "number")

	r := f.do("POST", "/api/v1/quotes/"+id+"/revise", map[string]any{"revision": rev, "reason": "price the joists again"})
	if r.status != 200 {
		t.Fatalf("revise = %d: %s", r.status, r.raw)
	}
	if str(t, r.body, "id") != id || str(t, r.body, "number") != number {
		t.Errorf("revise moved the quote: id=%v number=%v, want the same %s %s", r.body["id"], r.body["number"], id, number)
	}
	if str(t, r.body, "status") != "draft" {
		t.Errorf("status after revise = %v, want draft", r.body["status"])
	}
	if num(t, r.body, "version_number") != 2 {
		t.Errorf("version_number after revise = %v, want 2", r.body["version_number"])
	}
	if num(t, r.body, "revision") != rev+1 || r.header.Get("ETag") != fmt.Sprintf(`"%d"`, rev+1) {
		t.Errorf("revision after revise = %v ETag %q, want %d", r.body["revision"], r.header.Get("ETag"), rev+1)
	}
	// A revise clears the lifecycle stamps of the version it takes back, so
	// the next version's send stamps its own.
	if r.body["sent_at"] != nil {
		t.Errorf("sent_at after revise = %v, want null", r.body["sent_at"])
	}
	if ev := eventsFor(t, f.db, id); fmt.Sprint(ev) != "[quote.created quote.sent quote.revised]" {
		t.Errorf("events = %v, want [quote.created quote.sent quote.revised]", ev)
	}
	// A refused revise needs the same precondition any write needs.
	if r := f.do("POST", "/api/v1/quotes/"+id+"/revise", nil); r.status != 428 {
		t.Errorf("revise with no precondition = %d, want 428: %s", r.status, r.raw)
	}
}

// RULE: a REJECTED or EXPIRED quote revises the same way, from its own state.
func TestQE01_ReviseRejectedAndExpired(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	rejectedID, rev := sentQuote(t, f)
	if r := f.do("POST", "/api/v1/quotes/"+rejectedID+"/transitions", map[string]any{"to": "rejected", "revision": rev}); r.status != 200 {
		t.Fatalf("reject = %d: %s", r.status, r.raw)
	}
	r := f.do("POST", "/api/v1/quotes/"+rejectedID+"/revise", map[string]any{"revision": rev + 1})
	if r.status != 200 || str(t, r.body, "status") != "draft" || num(t, r.body, "version_number") != 2 {
		t.Errorf("revise of a rejected quote = %d %v v%v, want 200 draft v2: %s", r.status, r.body["status"], r.body["version_number"], r.raw)
	}

	// An EXPIRED quote revises too; its old Valid until stays, and the
	// re-dated send is QE-01-a's rule, not the revise's.
	expiredID := sentPastQuote(t, f)
	svc := wireQuoteService(f)
	if _, err := svc.SweepExpired(branchctx.WithSystem(context.Background()), time.Now().UTC(), 100); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := quoteState(t, f, uuid.MustParse(expiredID)); got != "expired" {
		t.Fatalf("the aged quote is %s, want expired", got)
	}
	r = f.do("POST", "/api/v1/quotes/"+expiredID+"/revise", map[string]any{"revision": currentRevision(t, f, uuid.MustParse(expiredID))})
	if r.status != 200 || str(t, r.body, "status") != "draft" || num(t, r.body, "version_number") != 2 {
		t.Errorf("revise of an expired quote = %d %v v%v, want 200 draft v2: %s", r.status, r.body["status"], r.body["version_number"], r.raw)
	}
	if r.body["expires_at"] == nil {
		t.Error("the revise wiped expires_at; only a new Valid until from the builder may move it")
	}
}

// RULE: the snapshot is written when a version is SENT: each send of a
// version writes its one row, a re-send of the same version adds none, and
// the row holds the header and lines exactly as sent, money in the integer
// units of the wire.
func TestQE01_SendWritesOneSnapshot(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, rev := sentQuote(t, f, f.line("10"))

	rows := revisionRows(t, f, id)
	if len(rows) != 1 || rows[0]["version_number"].(int) != 1 {
		t.Fatalf("after the first send the snapshot rows = %v, want exactly v1", rows)
	}
	if got := snapshotQty(t, rows[0]); got != "10" {
		t.Errorf("v1 snapshot quantity = %s, want 10", got)
	}
	if got := snapshotTotal(t, rows[0]); got != 5500 {
		t.Errorf("v1 snapshot total_cents = %d, want 5500", got)
	}
	sentAt := rows[0]["created_at"].(time.Time)

	// A re-send of the same version (reject, reopen, send again) adds no row.
	if r := f.do("POST", "/api/v1/quotes/"+id+"/transitions", map[string]any{"to": "rejected", "revision": rev}); r.status != 200 {
		t.Fatalf("reject = %d: %s", r.status, r.raw)
	}
	if r := f.do("POST", "/api/v1/quotes/"+id+"/transitions", map[string]any{"to": "draft", "revision": rev + 1}); r.status != 200 {
		t.Fatalf("reopen = %d: %s", r.status, r.raw)
	}
	if r := f.do("POST", "/api/v1/quotes/"+id+"/transitions", map[string]any{"to": "sent", "revision": rev + 2}); r.status != 200 {
		t.Fatalf("re-send = %d: %s", r.status, r.raw)
	}
	rows = revisionRows(t, f, id)
	if len(rows) != 1 {
		t.Fatalf("after a re-send of the same version the snapshot rows = %d, want 1", len(rows))
	}
	if !rows[0]["created_at"].(time.Time).Equal(sentAt) {
		t.Error("the re-send touched the first send's row; a sent version is never rewritten")
	}

	// A revise to v2, a changed line, and the v2 send writes the second row.
	if r := f.do("POST", "/api/v1/quotes/"+id+"/revise", map[string]any{"revision": currentRevision(t, f, uuid.MustParse(id))}); r.status != 200 {
		t.Fatalf("revise = %d: %s", r.status, r.raw)
	}
	edit := f.createBody(f.line("20"))
	if r := f.do("PUT", "/api/v1/quotes/"+id, edit, "If-Match", fmt.Sprintf(`"%d"`, currentRevision(t, f, uuid.MustParse(id)))); r.status != 200 {
		t.Fatalf("edit = %d: %s", r.status, r.raw)
	}
	if r := f.do("POST", "/api/v1/quotes/"+id+"/transitions", map[string]any{"to": "sent", "revision": currentRevision(t, f, uuid.MustParse(id))}); r.status != 200 {
		t.Fatalf("send v2 = %d: %s", r.status, r.raw)
	}
	rows = revisionRows(t, f, id)
	if len(rows) != 2 || rows[0]["version_number"].(int) != 1 || rows[1]["version_number"].(int) != 2 {
		t.Fatalf("after the v2 send the snapshot rows = %v, want v1 and v2", rows)
	}
	if got := snapshotQty(t, rows[0]); got != "10" {
		t.Errorf("v1 snapshot quantity after the v2 send = %s, want 10 (v1 unchanged)", got)
	}
	if got := snapshotQty(t, rows[1]); got != "20" {
		t.Errorf("v2 snapshot quantity = %s, want 20", got)
	}
	if got := snapshotTotal(t, rows[1]); got != 11000 {
		t.Errorf("v2 snapshot total_cents = %d, want 11000", got)
	}
}

// RULE: the snapshot rows are immutable. The database itself refuses an
// update, whatever path tried it.
func TestQE01_SnapshotImmutable(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, _ := sentQuote(t, f)

	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE quote_revisions SET reason = 'tampered', snapshot = '{}'::jsonb WHERE quote_id = $1`, id); err == nil {
		t.Fatal("an update of a sent version's snapshot row was accepted; the rows must be immutable")
	} else {
		t.Logf("the update was refused: %v", err)
	}
	// And nothing changed: one row, its snapshot intact.
	rows := revisionRows(t, f, id)
	if len(rows) != 1 || snapshotQty(t, rows[0]) != "10" {
		t.Errorf("after the refused update the rows = %v, want the one intact v1", rows)
	}
}

// seedLiveShareLink inserts a live dealer share token for the quote, as the
// share link route would, and answers its row id.
func seedLiveShareLink(t *testing.T, f *fixture, quoteID string, live bool) string {
	t.Helper()
	sum := sha256.Sum256([]byte("teststub" + quoteID))
	revokedAt, revokedReason := any(nil), any(nil)
	if !live {
		revokedAt, revokedReason = time.Now().UTC().Add(-time.Minute), "revoked"
	}
	var id uuid.UUID
	if err := f.db.Pool.QueryRow(context.Background(),
		`INSERT INTO pro_portal_share_tokens (dealer_quote_id, token_hash, expires_at, revoked_at, revoked_reason)
		VALUES ($1, $2, NOW() + INTERVAL '1 day', $3, $4) RETURNING id`,
		quoteID, hex.EncodeToString(sum[:]), revokedAt, revokedReason).Scan(&id); err != nil {
		t.Fatalf("seed share link: %v", err)
	}
	return id.String()
}

// RULE: a revise revokes any live share link of the quote, so the contractor
// cannot accept a version the dealer has taken back.
func TestQE01_ReviseRevokesShareLink(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, rev := sentQuote(t, f)

	live := seedLiveShareLink(t, f, id, true)
	dead := seedLiveShareLink(t, f, id, false)

	if r := f.do("POST", "/api/v1/quotes/"+id+"/revise", map[string]any{"revision": rev}); r.status != 200 {
		t.Fatalf("revise = %d: %s", r.status, r.raw)
	}
	var reason string
	var revokedAt *time.Time
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT revoked_reason, revoked_at FROM pro_portal_share_tokens WHERE id = $1`, live).Scan(&reason, &revokedAt); err != nil {
		t.Fatal(err)
	}
	if revokedAt == nil || reason != "revised" {
		t.Errorf("the live link after revise: revoked_at=%v reason=%q, want revoked with reason revised", revokedAt, reason)
	}
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT revoked_reason FROM pro_portal_share_tokens WHERE id = $1`, dead).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "revoked" {
		t.Errorf("the already dead link's reason = %q, want the revoked it already was", reason)
	}
}

// RULE: an ACCEPTED or converted quote cannot be revised (409, plain words),
// and a draft needs no revise: it is already with the builder.
func TestQE01_ReviseAcceptedRefused(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	branchID := f.ownBranch("0.088750")

	acceptedID, rev := sentQuote(t, f)
	if r := f.do("POST", "/api/v1/quotes/"+acceptedID+"/transitions", map[string]any{"to": "accepted", "revision": rev}); r.status != 200 {
		t.Fatalf("accept = %d: %s", r.status, r.raw)
	}
	r := f.do("POST", "/api/v1/quotes/"+acceptedID+"/revise", map[string]any{"revision": rev + 1})
	if r.status != http.StatusConflict {
		t.Fatalf("revise of an accepted quote = %d, want 409: %s", r.status, r.raw)
	}
	code, message, _ := errorOf(t, r)
	if code != httpx.CodeInvalidStateTransition {
		t.Errorf("code = %s, want %s", code, httpx.CodeInvalidStateTransition)
	}
	if message != "an accepted quote cannot be revised" {
		t.Errorf("message = %q, want the plain words", message)
	}

	// A converted quote is an accepted quote: the same refusal, and no order
	// was touched.
	body := f.createBody(f.line("10"))
	body["branch_id"] = branchID.String()
	created := f.do("POST", "/api/v1/quotes", body)
	if created.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.status, created.raw)
	}
	convertID := str(t, created.body, "id")
	if r := f.do("POST", "/api/v1/quotes/"+convertID+"/convert", nil, "If-Match", `"1"`); r.status != 201 {
		t.Fatalf("convert = %d: %s", r.status, r.raw)
	}
	r = f.do("POST", "/api/v1/quotes/"+convertID+"/revise", map[string]any{"revision": currentRevision(t, f, uuid.MustParse(convertID))})
	if r.status != http.StatusConflict {
		t.Errorf("revise of a converted quote = %d, want 409: %s", r.status, r.raw)
	}
	if _, m, _ := errorOf(t, r); m != "an accepted quote cannot be revised" {
		t.Errorf("converted quote message = %q", m)
	}

	// A draft is refused with its own words: it is edited, not revised.
	draftID := str(t, f.create().body, "id")
	r = f.do("POST", "/api/v1/quotes/"+draftID+"/revise", map[string]any{"revision": 1})
	if r.status != http.StatusConflict {
		t.Fatalf("revise of a draft = %d, want 409: %s", r.status, r.raw)
	}
	if _, m, _ := errorOf(t, r); m != "only sent, rejected and expired quotes can be revised; a draft is already editable" {
		t.Errorf("draft message = %q", m)
	}
}

// RULE: the order keeps converting from the one quote id: a revised and
// re-sent quote converts as the same quote, and the order names it.
func TestQE01_RevisedQuoteConvertsFromSameID(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	branchID := f.ownBranch("0.088750")

	body := f.createBody(f.line("10"))
	body["branch_id"] = branchID.String()
	created := f.do("POST", "/api/v1/quotes", body)
	if created.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.status, created.raw)
	}
	id := str(t, created.body, "id")
	number := str(t, created.body, "number")
	if r := f.do("POST", "/api/v1/quotes/"+id+"/transitions", map[string]any{"to": "sent", "revision": 1}); r.status != 200 {
		t.Fatalf("send = %d: %s", r.status, r.raw)
	}
	if r := f.do("POST", "/api/v1/quotes/"+id+"/revise", map[string]any{"revision": 2}); r.status != 200 {
		t.Fatalf("revise = %d: %s", r.status, r.raw)
	}
	edit := f.createBody(f.line("30"))
	edit["branch_id"] = branchID.String()
	if r := f.do("PUT", "/api/v1/quotes/"+id, edit, "If-Match", fmt.Sprintf(`"%d"`, currentRevision(t, f, uuid.MustParse(id)))); r.status != 200 {
		t.Fatalf("edit = %d: %s", r.status, r.raw)
	}
	if r := f.do("POST", "/api/v1/quotes/"+id+"/transitions", map[string]any{"to": "sent", "revision": currentRevision(t, f, uuid.MustParse(id))}); r.status != 200 {
		t.Fatalf("send v2 = %d: %s", r.status, r.raw)
	}
	c := f.do("POST", "/api/v1/quotes/"+id+"/convert", nil, "If-Match", fmt.Sprintf(`"%d"`, currentRevision(t, f, uuid.MustParse(id))))
	if c.status != 201 {
		t.Fatalf("convert = %d: %s", c.status, c.raw)
	}
	if got := str(t, c.body, "quote_id"); got != id {
		t.Errorf("the order's quote_id = %s, want the one quote id %s", got, id)
	}
	g := f.do("GET", "/api/v1/quotes/"+id, nil)
	if str(t, g.body, "number") != number || num(t, g.body, "version_number") != 2 {
		t.Errorf("after the convert the quote = %s v%v, want %s v2", g.body["number"], g.body["version_number"], number)
	}
}

// RULE: GET /quotes/{id}/revisions lists the versions with their snapshots,
// newest first, in the list envelope, behind the quote's own branch wall.
func TestQE01_RevisionsList(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, rev := sentQuote(t, f, f.line("10"))

	empty := f.do("GET", "/api/v1/quotes/"+id+"/revisions", nil)
	if empty.status != 200 {
		t.Fatalf("revisions of a once sent quote = %d: %s", empty.status, empty.raw)
	}
	if items, ok := empty.body["items"].([]any); !ok || len(items) != 1 {
		t.Fatalf("revisions after v1 = %#v, want the one v1 row (%s)", empty.body["items"], empty.raw)
	}

	// Revise, change the line, send v2: the list shows both, newest first,
	// each with its snapshot.
	if r := f.do("POST", "/api/v1/quotes/"+id+"/revise", map[string]any{"revision": rev}); r.status != 200 {
		t.Fatalf("revise = %d: %s", r.status, r.raw)
	}
	if r := f.do("PUT", "/api/v1/quotes/"+id, f.createBody(f.line("20")), "If-Match", fmt.Sprintf(`"%d"`, currentRevision(t, f, uuid.MustParse(id)))); r.status != 200 {
		t.Fatalf("edit = %d: %s", r.status, r.raw)
	}
	if r := f.do("POST", "/api/v1/quotes/"+id+"/transitions", map[string]any{"to": "sent", "revision": currentRevision(t, f, uuid.MustParse(id))}); r.status != 200 {
		t.Fatalf("send v2 = %d: %s", r.status, r.raw)
	}

	list := f.do("GET", "/api/v1/quotes/"+id+"/revisions", nil)
	if list.status != 200 {
		t.Fatalf("revisions = %d: %s", list.status, list.raw)
	}
	items := list.body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("revisions = %d items, want 2 (%s)", len(items), list.raw)
	}
	top := items[0].(map[string]any)
	if num(t, top, "version_number") != 2 {
		t.Errorf("newest revision = v%v, want v2 first", top["version_number"])
	}
	snap := top["snapshot"].(map[string]any)
	if snap["number"] != str(t, f.do("GET", "/api/v1/quotes/"+id, nil).body, "number") {
		t.Errorf("snapshot number = %v", snap["number"])
	}
	if snap["total_cents"].(json.Number).String() != "11000" {
		t.Errorf("v2 snapshot total_cents = %v, want 11000", snap["total_cents"])
	}
	if snapLines, ok := snap["lines"].([]any); !ok || len(snapLines) != 1 {
		t.Errorf("v2 snapshot lines = %#v", snap["lines"])
	} else if q := snapLines[0].(map[string]any)["quantity"]; q != "20" {
		t.Errorf("v2 snapshot line quantity = %v, want 20", q)
	}
	if top["created_by"] == nil {
		t.Error("the revision row does not carry created_by")
	}
	// The envelope's strictness: a parameter the route does not declare is a
	// 400 naming it, and so is a foreign cursor.
	if r := f.do("GET", "/api/v1/quotes/"+id+"/revisions?foo=1", nil); r.status != 400 {
		t.Errorf("revisions?foo=1 = %d, want 400: %s", r.status, r.raw)
	}
	foreign, err := httpx.MintCursor("quotes.created_at_id_desc", "1", "x")
	if err != nil {
		t.Fatal(err)
	}
	if r := f.do("GET", "/api/v1/quotes/"+id+"/revisions?cursor="+foreign, nil); r.status != 400 {
		t.Errorf("a foreign cursor = %d, want 400", r.status)
	}
	// An unknown quote's revisions are its 404.
	if r := f.do("GET", "/api/v1/quotes/"+uuid.NewString()+"/revisions", nil); r.status != 404 {
		t.Errorf("revisions of an unknown quote = %d, want 404", r.status)
	}
}

// RULE (the race): three revises of one sent quote contend on the quote's
// row; exactly one wins, and the stored version goes up by exactly one. Pool
// size 4, three contenders.
func TestQE01_ReviseRaceThreeContendersOneVersion(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	ctx := branchctx.WithSystem(context.Background())
	svc := wireQuoteService(f)

	d := &quote.Draft{CustomerID: f.customerID, DeliveryType: quote.DeliveryPickup, Source: "manual",
		Lines: []quote.DraftLine{{ProductID: &f.productID, SKU: f.sku, Description: "2x4x8 SPF",
			Quantity: 10000, UOM: "PCS", PriceUOM: "PCS", UOMQty: 10000, PriceUOMQty: 10000, UnitPrice: 55000}}}
	q, err := svc.Create(ctx, d)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.UpdateState(ctx, q.ID, quote.QuoteStateSent); err != nil {
		t.Fatalf("send: %v", err)
	}
	rev := currentRevision(t, f, q.ID)

	var winners, stale int
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			r := int64(rev)
			_, err := svc.Revise(c, q.ID, "race", quote.Precondition{Revision: &r})
			mu.Lock()
			defer mu.Unlock()
			var he *httpx.Error
			switch {
			case err == nil:
				winners++
			case errors.As(err, &he) && he.Status == http.StatusConflict:
				stale++
			default:
				t.Errorf("revise contender: unexpected error %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if winners != 1 || stale != 2 {
		t.Errorf("winners=%d stale=%d, want exactly one winner and two refused", winners, stale)
	}
	// The stored figure the act writes, not a figure recomputed on read.
	var version int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT version_number FROM quotes WHERE id = $1`, q.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Errorf("stored version_number = %d, want exactly 2", version)
	}
	if n := len(revisionRows(t, f, q.ID.String())); n != 1 {
		t.Errorf("snapshot rows after the race = %d, want the one v1", n)
	}
	if ev := eventsFor(t, f.db, q.ID.String()); fmt.Sprint(ev) != "[quote.created quote.sent quote.revised]" {
		t.Errorf("events after the race = %v", ev)
	}
}

// RULE (the branch wall): both new routes sit behind the record branch rule.
// A caller with no branch context and no system mark is refused; a marked
// system caller passes; a caller held to another branch is refused.
func TestQE01_ReviseAndRevisionsBranchWall(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	ctx := context.Background()

	svc := quote.NewService(quote.NewRepository(f.db)).WithOutbox(outbox.NewWriter(f.db, "")).WithTxRunner(f.db).
		WithBranchGuard(middleware.NewBranchGuard(f.db))
	q := seedQuote(t, svc, f.customerID, f.productID, f.sku)
	sys := branchctx.WithSystem(ctx)
	if err := svc.UpdateState(sys, q.ID, quote.QuoteStateSent); err != nil {
		t.Fatalf("send as system caller: %v", err)
	}

	rev := currentRevision(t, f, q.ID)
	// No branch context: both routes fail closed.
	if _, err := svc.Revise(ctx, q.ID, "", quote.Precondition{Revision: &rev}); err == nil {
		t.Fatal("Revise with no branch context and no system mark: nil error, want refused")
	} else {
		var herr *httpx.Error
		if !errors.As(err, &herr) || herr.Status != 403 {
			t.Fatalf("Revise with no branch context: %v, want the 403 forbidden error", err)
		}
	}
	if _, err := svc.ListRevisions(ctx, q.ID, 50, nil); err == nil {
		t.Fatal("ListRevisions with no branch context and no system mark: nil error, want refused")
	}

	// A caller held to another branch: refused the same way.
	other := uuid.New()
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, other, "QR-"+other.String()[:8]); err != nil {
		t.Fatalf("seed branch: %v", err)
	}
	t.Cleanup(func() { _, _ = f.db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, other) })
	bound := branchctx.With(ctx, &branchctx.Context{UserSub: "u-qr", BranchID: &other})
	if _, err := svc.Revise(bound, q.ID, "", quote.Precondition{Revision: &rev}); err == nil {
		t.Fatal("Revise from a caller held to another branch: nil error, want refused")
	}
	if _, err := svc.ListRevisions(bound, q.ID, 50, nil); err == nil {
		t.Fatal("ListRevisions from a caller held to another branch: nil error, want refused")
	}

	// The marked system caller revises and reads the versions.
	if _, err := svc.Revise(sys, q.ID, "", quote.Precondition{Revision: &rev}); err != nil {
		t.Fatalf("Revise as a marked system caller: %v", err)
	}
	revs, err := svc.ListRevisions(sys, q.ID, 50, nil)
	if err != nil {
		t.Fatalf("ListRevisions as a marked system caller: %v", err)
	}
	if len(revs) != 1 || revs[0].VersionNumber != 1 {
		t.Fatalf("revisions = %v, want the one v1", revs)
	}
}
