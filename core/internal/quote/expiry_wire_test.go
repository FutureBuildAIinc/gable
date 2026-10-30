// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote_test

// QE-01-a: a quote carries an expiry a dealer sees, and an expired quote
// cannot be sent, accepted or converted until it is re-dated (the principal's
// strict ruling: no owner or admin override). The refusal is one rule in one
// place, the quote service, and every path answers the same words: the desk's
// transition and convert as a 409 with the blocker quote_expired, the sweep
// moves SENT quotes past their date through the service's own transition, and
// a re-dated send re-takes the price snapshot.

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// theWords is the ruling's refusal, the day as the desk shows dates (the
// branch's own calendar, the month a short word).
const theWords = "This quote expired on %s: revise it with a new valid until date to send or convert it"

var theWordsRe = regexp.MustCompile(
	`^This quote expired on [A-Z][a-z]{2} \d{1,2}, \d{4}: revise it with a new valid until date to send or convert it$`)

// expiringBody is a create body whose Valid until is ts.
func expiringBody(f *fixture, ts time.Time, lines ...map[string]any) map[string]any {
	body := f.createBody(lines...)
	body["expires_at"] = ts.UTC().Format(time.RFC3339)
	return body
}

// wireQuoteService builds the service exactly as the wire fixture does, for
// the in-process callers the sweep and the race test drive directly.
func wireQuoteService(f *fixture) *quote.Service {
	orderSvc := order.NewService(order.NewRepository(f.db)).
		WithOutbox(outbox.NewWriter(f.db, "")).WithTxRunner(f.db)
	return quote.NewService(quote.NewRepository(f.db)).
		WithOutbox(outbox.NewWriter(f.db, "")).WithTxRunner(f.db).WithOrderCreator(orderSvc)
}

// pastDatedDraft creates a draft whose Valid until is already past, and marks
// the words check against the desk's own transition route.
func pastDatedDraft(t *testing.T, f *fixture) (id, path string) {
	t.Helper()
	past := time.Now().UTC().Add(-24 * time.Hour)
	created := f.do("POST", "/api/v1/quotes", expiringBody(f, past))
	if created.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.status, created.raw)
	}
	return str(t, created.body, "id"), "/api/v1/quotes/" + str(t, created.body, "id") + "/transitions"
}

// sentPastQuote creates a quote, sends it while its date is still good, then
// moves its Valid until into the past as time alone would (a sent quote takes
// no edits), and answers the quote's id.
func sentPastQuote(t *testing.T, f *fixture) string {
	t.Helper()
	good := time.Now().UTC().Add(24 * time.Hour)
	created := f.do("POST", "/api/v1/quotes", expiringBody(f, good))
	if created.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.status, created.raw)
	}
	id := str(t, created.body, "id")
	if r := f.do("POST", "/api/v1/quotes/"+id+"/transitions", map[string]any{"to": "sent", "revision": 1}); r.status != 200 {
		t.Fatalf("send = %d: %s", r.status, r.raw)
	}
	// The revision moved to 2 on the send.
	past := time.Now().UTC().Add(-time.Hour)
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE quotes SET expires_at = $1 WHERE id = $2`, past, id); err != nil {
		t.Fatalf("age the quote past its date: %v", err)
	}
	return id
}

// assertExpiredRefusal checks the desk's 409: the ruling's words as the
// message and the blocker quote_expired in the details.
func assertExpiredRefusal(t *testing.T, name string, r resp) {
	t.Helper()
	if r.status != http.StatusConflict {
		t.Fatalf("%s = %d: %s", name, r.status, r.raw)
	}
	code, message, details := errorOf(t, r)
	if code != httpx.CodeInvalidStateTransition {
		t.Errorf("%s code = %s, want %s", name, code, httpx.CodeInvalidStateTransition)
	}
	if !theWordsRe.MatchString(message) {
		t.Errorf("%s message = %q, want the ruling's words", name, message)
	}
	found := false
	for _, d := range details {
		if d["code"] == "quote_expired" {
			found = true
			if msg, _ := d["message"].(string); !theWordsRe.MatchString(msg) {
				t.Errorf("%s blocker message = %q, want the ruling's words", name, msg)
			}
		}
	}
	if !found {
		t.Errorf("%s details = %v, want the quote_expired blocker", name, details)
	}
}

// RULE (QE-01-a, the principal's strict ruling): a draft whose Valid until is
// at or before now cannot be sent, and the refusal says what to do.
func TestQE01_ExpiredQuoteRefusesSend(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, path := pastDatedDraft(t, f)

	r := f.do("POST", path, map[string]any{"to": "sent", "revision": 1})
	assertExpiredRefusal(t, "send of an expired draft", r)

	// The refusal left the quote exactly as it was.
	g := f.do("GET", "/api/v1/quotes/"+id, nil)
	if str(t, g.body, "status") != "draft" {
		t.Errorf("status after the refused send = %v, want draft", g.body["status"])
	}
	// And no event claims a send happened.
	if ev := eventsFor(t, f.db, id); fmt.Sprint(ev) != "[quote.created]" {
		t.Errorf("events after the refused send = %v, want only quote.created", ev)
	}
}

// RULE: an expired quote cannot be accepted, from a draft or from a sent
// quote, with the same words.
func TestQE01_ExpiredQuoteRefusesAccept(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	_, draftPath := pastDatedDraft(t, f)
	assertExpiredRefusal(t, "accept of an expired draft",
		f.do("POST", draftPath, map[string]any{"to": "accepted", "revision": 1}))

	sentID := sentPastQuote(t, f)
	assertExpiredRefusal(t, "accept of a sent quote past its date",
		f.do("POST", "/api/v1/quotes/"+sentID+"/transitions", map[string]any{"to": "accepted", "revision": 2}))

	g := f.do("GET", "/api/v1/quotes/"+sentID, nil)
	if str(t, g.body, "status") != "sent" {
		t.Errorf("status after the refused accept = %v, want sent", g.body["status"])
	}
}

// RULE: an expired quote cannot convert, from a draft or from a sent quote.
func TestQE01_ExpiredQuoteRefusesConvert(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	// The convert prices its tax before the transaction opens, so the quote
	// needs a branch with a rate, exactly as the convert wire test's does.
	branchID := f.ownBranch("0.088750")
	withBranch := func(body map[string]any) map[string]any {
		body["branch_id"] = branchID.String()
		return body
	}

	past := time.Now().UTC().Add(-24 * time.Hour)
	created := f.do("POST", "/api/v1/quotes", withBranch(expiringBody(f, past)))
	if created.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.status, created.raw)
	}
	draftID := str(t, created.body, "id")
	assertExpiredRefusal(t, "convert of an expired draft",
		f.do("POST", "/api/v1/quotes/"+draftID+"/convert", nil, "If-Match", `"1"`))

	good := time.Now().UTC().Add(24 * time.Hour)
	created = f.do("POST", "/api/v1/quotes", withBranch(expiringBody(f, good)))
	if created.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.status, created.raw)
	}
	sentID := str(t, created.body, "id")
	if r := f.do("POST", "/api/v1/quotes/"+sentID+"/transitions", map[string]any{"to": "sent", "revision": 1}); r.status != 200 {
		t.Fatalf("send = %d: %s", r.status, r.raw)
	}
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE quotes SET expires_at = $1 WHERE id = $2`, time.Now().UTC().Add(-time.Hour), sentID); err != nil {
		t.Fatal(err)
	}
	assertExpiredRefusal(t, "convert of a sent quote past its date",
		f.do("POST", "/api/v1/quotes/"+sentID+"/convert", nil, "If-Match", `"2"`))

	// No order was created by either refusal.
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM orders WHERE quote_id = ANY($1)`, []uuid.UUID{uuid.MustParse(draftID), uuid.MustParse(sentID)}).Scan(&n); err != nil || n != 0 {
		t.Errorf("orders from the refused converts = %d (err %v), want 0", n, err)
	}
}

// RULE (the date in the branch's own time, as the desk shows dates): the
// refusal's day is the branch's calendar day of the expiry instant, not UTC's.
func TestQE01_ExpiryDayIsTheBranchsOwn(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	ctx := context.Background()

	// A branch in Vancouver: 03:00 UTC is the evening before there.
	branchID := uuid.New()
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO locations (id, type, code, default_tax_rate, timezone) VALUES ($1, 'BRANCH', $2, 0.088750, 'America/Vancouver')`,
		branchID, "QE-ZONE-"+branchID.String()[:8]); err != nil {
		t.Fatalf("seed branch: %v", err)
	}
	f.t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM quotes WHERE branch_id = $1`, branchID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, branchID)
	})

	// 2026-10-15T03:00:00Z is Oct 14, 2026 in Vancouver.
	body := expiringBody(f, time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC))
	body["branch_id"] = branchID.String()
	created := f.do("POST", "/api/v1/quotes", body)
	if created.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.status, created.raw)
	}
	id := str(t, created.body, "id")

	r := f.do("POST", "/api/v1/quotes/"+id+"/transitions", map[string]any{"to": "sent", "revision": 1})
	_, message, _ := errorOf(t, r)
	want := fmt.Sprintf(theWords, "Oct 14, 2026")
	if message != want {
		t.Errorf("message = %q, want %q (the branch's own day, not UTC's)", message, want)
	}
}

// RULE (re-dating): an EXPIRED quote reopens to DRAFT, and sending it again
// after a new Valid until re-takes the price snapshot: a fresh baseline row,
// not the old one.
func TestQE01_ReopenRedateSendRetakesSnapshot(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	ctx := branchctx.WithSystem(context.Background())

	// A commodity product on a market index: the lines a snapshot baselines.
	indexID := uuid.New()
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO market_indices (id, name, source, current_value, previous_value, unit, index_code)
		VALUES ($1, 'QE Expiry Framing', 'RANDOM_LENGTHS', 480, 470, 'MBF', 'QE-EXPIRY-' || $2::text)`,
		indexID, indexID.String()[:8]); err != nil {
		t.Fatalf("seed market index: %v", err)
	}
	commodity := uuid.New()
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO products (id, sku, description, uom_primary, base_price, is_commodity, market_index_id)
		VALUES ($1, $2, 'QE expiry SPF stud', 'PCS', 5.5, TRUE, $3)`,
		commodity, "QE-EXP-"+commodity.String()[:8], indexID); err != nil {
		t.Fatalf("seed commodity product: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM price_escalators WHERE quote_line_id IN (SELECT id FROM quote_lines WHERE quote_id IN (SELECT id FROM quotes WHERE customer_id = $1))`, f.customerID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM market_indices WHERE id = $1`, indexID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, commodity)
	})

	snapshotSvc := pricing.NewSnapshotService(
		pricing.NewEscalatorRepository(f.db), pricing.NewExposureRepository(f.db),
		quote.NewRepository(f.db), f.db, nil)
	svc := wireQuoteService(f)
	svc.WithSnapshotService(snapshotSvc)

	line := &quote.DraftLine{ProductID: &commodity, SKU: "QE-EXP", Description: "commodity stud",
		Quantity: 10000, UOM: "PCS", PriceUOM: "PCS", UOMQty: 10000, PriceUOMQty: 10000, UnitPrice: 75000}
	future := time.Now().UTC().Add(24 * time.Hour)
	q, err := svc.Create(ctx, &quote.Draft{CustomerID: f.customerID, DeliveryType: quote.DeliveryPickup,
		Source: "manual", ExpiresAt: httpx.PtrTimestamp(&future), Lines: []quote.DraftLine{*line}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.UpdateState(ctx, q.ID, quote.QuoteStateSent); err != nil {
		t.Fatalf("send: %v", err)
	}

	// The first baseline.
	first := escalatorState(t, f, q.ID)
	if len(first) != 1 || !first[0].active {
		t.Fatalf("first baseline = %+v, want one active row", first)
	}

	// Time passes; the sweep expires it; the dealer reopens and re-dates.
	past := time.Now().UTC().Add(-time.Hour)
	if _, err := f.db.Pool.Exec(ctx, `UPDATE quotes SET expires_at = $1 WHERE id = $2`, past, q.ID); err != nil {
		t.Fatalf("age the quote: %v", err)
	}
	if _, err := svc.SweepExpired(ctx, time.Now().UTC(), 100); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if err := svc.UpdateState(ctx, q.ID, quote.QuoteStateExpired); err == nil ||
		!strings.Contains(err.Error(), "cannot transition") {
		// EXPIRED to EXPIRED must refuse: at most once per quote.
		t.Fatalf("re-expiring an expired quote = %v, want the lifecycle refusal", err)
	}
	if err := svc.UpdateState(ctx, q.ID, quote.QuoteStateDraft); err != nil {
		t.Fatalf("reopen to draft: %v", err)
	}
	fresh := time.Now().UTC().Add(48 * time.Hour)
	if _, err := svc.Update(ctx, q.ID, &quote.Draft{CustomerID: f.customerID, DeliveryType: quote.DeliveryPickup,
		Source: "manual", ExpiresAt: httpx.PtrTimestamp(&fresh), Lines: []quote.DraftLine{*line}},
		quote.Precondition{Revision: nil, IfMatch: fmt.Sprintf(`"%d"`, currentRevision(t, f, q.ID))}); err != nil {
		t.Fatalf("re-date: %v", err)
	}
	if err := svc.UpdateState(ctx, q.ID, quote.QuoteStateSent); err != nil {
		t.Fatalf("send again: %v", err)
	}

	second := escalatorState(t, f, q.ID)
	if len(second) != 2 {
		t.Fatalf("baselines after the re-dated send = %d rows, want 2 (the old one and the fresh one)", len(second))
	}
	var old, freshRow *escalatorRow
	for i := range second {
		if second[i].active {
			freshRow = &second[i]
		} else {
			old = &second[i]
		}
	}
	if old == nil || freshRow == nil {
		t.Fatalf("baselines after the re-dated send = %+v, want one active fresh row and the old one inactive", second)
	}
	if freshRow.id == old.id {
		t.Errorf("the fresh baseline reuses the old row %s", freshRow.id)
	}
	if !freshRow.recordedAt.After(old.recordedAt) {
		t.Errorf("the fresh baseline was recorded at %v, not after the old one's %v", freshRow.recordedAt, old.recordedAt)
	}
}

type escalatorRow struct {
	id         string
	active     bool
	recordedAt time.Time
}

func escalatorState(t *testing.T, f *fixture, quoteID uuid.UUID) []escalatorRow {
	t.Helper()
	rows, err := f.db.Pool.Query(context.Background(),
		`SELECT e.id, e.is_active, e.base_index_recorded_at
		FROM price_escalators e JOIN quote_lines l ON l.id = e.quote_line_id
		WHERE l.quote_id = $1 ORDER BY e.base_index_recorded_at`, quoteID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []escalatorRow
	for rows.Next() {
		var r escalatorRow
		if err := rows.Scan(&r.id, &r.active, &r.recordedAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func currentRevision(t *testing.T, f *fixture, quoteID uuid.UUID) int64 {
	t.Helper()
	var rev int64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT revision FROM quotes WHERE id = $1`, quoteID).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	return rev
}

// RULE (the sweep): SENT quotes past their expires_at move to EXPIRED through
// the service's own transition, so the event rides the act's transaction; a
// draft past its date, an accepted quote past its date and an undated sent
// quote stay as they are, and a second pass changes nothing.
func TestQE01_SweepExpiresSentPastDate(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	ctx := branchctx.WithSystem(context.Background())
	svc := wireQuoteService(f)

	past := time.Now().UTC().Add(-2 * time.Hour)
	future := time.Now().UTC().Add(24 * time.Hour)
	mk := func(expires *time.Time, send bool) uuid.UUID {
		d := &quote.Draft{CustomerID: f.customerID, DeliveryType: quote.DeliveryPickup, Source: "manual",
			Lines: []quote.DraftLine{{ProductID: &f.productID, SKU: f.sku, Description: "2x4x8 SPF",
				Quantity: 10000, UOM: "PCS", PriceUOM: "PCS", UOMQty: 10000, PriceUOMQty: 10000, UnitPrice: 55000}}}
		if expires != nil {
			d.ExpiresAt = httpx.PtrTimestamp(expires)
		}
		q, err := svc.Create(ctx, d)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if send {
			if err := svc.UpdateState(ctx, q.ID, quote.QuoteStateSent); err != nil {
				t.Fatalf("send: %v", err)
			}
		}
		return q.ID
	}
	// Age a quote past its date as time alone would: a sent or accepted quote
	// takes no edits, so the move is the clock's.
	age := func(id uuid.UUID) {
		t.Helper()
		if _, err := f.db.Pool.Exec(context.Background(),
			`UPDATE quotes SET expires_at = $1 WHERE id = $2`, past, id); err != nil {
			t.Fatal(err)
		}
	}

	sentPast := mk(&future, true) // sent while good, then aged: the sweep's one target
	age(sentPast)
	draftPast := mk(&past, false)   // a draft past its date: the sweep leaves it
	sentUndated := mk(nil, true)    // a sent quote with no date: left alone
	sentFuture := mk(&future, true) // still good: left alone
	acceptedPast := mk(&future, false)
	if err := svc.UpdateState(ctx, acceptedPast, quote.QuoteStateAccepted); err != nil {
		t.Fatalf("accept: %v", err)
	}
	age(acceptedPast) // accepted before its date passed: left alone

	moved, err := svc.SweepExpired(ctx, time.Now().UTC(), 100)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if moved != 1 {
		t.Errorf("the sweep moved %d quotes, want 1", moved)
	}
	for id, want := range map[uuid.UUID]string{
		sentPast:     "expired",
		draftPast:    "draft",
		sentUndated:  "sent",
		sentFuture:   "sent",
		acceptedPast: "accepted",
	} {
		if got := quoteState(t, f, id); got != want {
			t.Errorf("quote %s state after the sweep = %s, want %s", id, got, want)
		}
	}
	// The expiry rode the act's own transaction: one quote.expired event, no other new events.
	if ev := eventsFor(t, f.db, sentPast.String()); fmt.Sprint(ev) != "[quote.created quote.sent quote.expired]" {
		t.Errorf("events of the swept quote = %v, want [quote.created quote.sent quote.expired]", ev)
	}

	// A second pass changes nothing.
	before := map[uuid.UUID]string{}
	for _, id := range []uuid.UUID{sentPast, draftPast, sentUndated, sentFuture, acceptedPast} {
		before[id] = quoteState(t, f, id)
	}
	movedAgain, err := svc.SweepExpired(ctx, time.Now().UTC(), 100)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if movedAgain != 0 {
		t.Errorf("the second sweep moved %d quotes, want 0", movedAgain)
	}
	for _, id := range []uuid.UUID{sentPast, draftPast, sentUndated, sentFuture, acceptedPast} {
		if got := quoteState(t, f, id); got != before[id] {
			t.Errorf("quote %s state after the second sweep = %s, want %s", id, got, before[id])
		}
	}
}

func quoteState(t *testing.T, f *fixture, id uuid.UUID) string {
	t.Helper()
	var state string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT state FROM quotes WHERE id = $1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return strings.ToLower(state)
}

// RULE (the race): the sweep and a convert of the same quote contend on the
// quote's row; exactly one wins, and an order exists only if the convert won.
// Pool size 4, three contenders.
func TestQE01_SweepAgainstConvertAtBoundary(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	ctx := branchctx.WithSystem(context.Background())
	svc := wireQuoteService(f)

	// Round A: the quote is already past its date, so the rule decides the
	// race: the converts are refused and the sweep expires it. The branch
	// carries a tax rate so a convert that wins can price its order.
	rateBranch := f.ownBranch("0.088750")
	seed := func(sentWith time.Time, thenAge bool) uuid.UUID {
		d := &quote.Draft{CustomerID: f.customerID, BranchID: &rateBranch, DeliveryType: quote.DeliveryPickup, Source: "manual",
			ExpiresAt: httpx.PtrTimestamp(&sentWith),
			Lines: []quote.DraftLine{{ProductID: &f.productID, SKU: f.sku, Description: "2x4x8 SPF",
				Quantity: 10000, UOM: "PCS", PriceUOM: "PCS", UOMQty: 10000, PriceUOMQty: 10000, UnitPrice: 55000}}}
		q, err := svc.Create(ctx, d)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := svc.UpdateState(ctx, q.ID, quote.QuoteStateSent); err != nil {
			t.Fatalf("send: %v", err)
		}
		if thenAge {
			if _, err := f.db.Pool.Exec(context.Background(),
				`UPDATE quotes SET expires_at = $1 WHERE id = $2`, time.Now().UTC().Add(-time.Minute), q.ID); err != nil {
				t.Fatal(err)
			}
		}
		return q.ID
	}

	round := func(id uuid.UUID) (expired bool, orders int) {
		var wg sync.WaitGroup
		start := make(chan struct{})
		contend := func(fn func()) {
			defer wg.Done()
			<-start
			fn()
		}
		wg.Add(3)
		go contend(func() {
			c, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if _, err := svc.SweepExpired(c, time.Now().UTC(), 10); err != nil {
				t.Errorf("the sweep contender: %v", err)
			}
		})
		convert := func() {
			c, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			// A contender's loss (a stale revision against the other convert,
			// the expiry refusal against the sweep) is the race's material;
			// the stored state below is the assertion.
			_, _ = svc.Convert(c, id, quote.Precondition{IfMatch: fmt.Sprintf(`"%d"`, currentRevision(t, f, id))})
		}
		go contend(convert)
		go contend(convert)
		close(start)
		wg.Wait()
		state := quoteState(t, f, id)
		var n int
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT count(*) FROM orders WHERE quote_id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return state == "expired", n
	}

	// Round A: past its date, the sweep must win and no order may exist.
	pastID := seed(time.Now().UTC().Add(24*time.Hour), true)
	expired, orders := round(pastID)
	if !expired || orders != 0 {
		t.Errorf("round A: expired=%v orders=%d, want the sweep to win and no order", expired, orders)
	}

	// Round B: on the boundary, either side may win, never both and never
	// neither: an order exists only with an accepted quote, an expired quote
	// never has one.
	boundaryID := seed(time.Now().UTC().Add(200*time.Millisecond), false)
	expired, orders = round(boundaryID)
	if orders > 1 {
		t.Errorf("round B: %d orders for one quote, want at most 1", orders)
	}
	if expired && orders != 0 {
		t.Errorf("round B: the quote expired AND %d orders exist; exactly one winner", orders)
	}
	if !expired && orders != 1 {
		t.Errorf("round B: neither side won (expired=%v orders=%d)", expired, orders)
	}
}
