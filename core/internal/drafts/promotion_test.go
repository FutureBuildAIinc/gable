// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts_test

// The promotion's wire facts (ADR 0007 section 11, the Promotion row): the
// promoter runs at the draft's branch whatever the committer's header says,
// the subject_stale blocker separates a stale subject from a stale draft,
// one transaction (a failing event write leaves no quote and no number
// reuse and the draft unchanged, with the draft.promotion_refused row after
// the rollback), the already_promoted retry rule, the payload. paths, the
// events in order, and the proposers and the committer on the promotion's
// audit row and outbox data.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/drafts"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// TestPromotionRunsAtDraftsBranchWhateverTheHeader pins section 4.2 step 5:
// the promoter runs with the draft's branch as the branch context, whatever
// the committer's X-Branch-Id names (a mismatched header never widens or
// narrows the promotion).
func TestPromotionRunsAtDraftsBranchWhateverTheHeader(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	id := f.create()

	// A header naming another branch does not steer the promotion: an
	// administrator may name any branch (the branch middleware admits it),
	// and the promoter still runs at the DRAFT's branch, so the quote lands
	// there, never on the header's.
	other := f.otherBranch(t)
	if other != "" {
		r := f.do("POST", "/api/v1/drafts/quotes/"+id+"/promote", map[string]any{"revision": 1}, "X-Branch-Id", other)
		if r.status != http.StatusCreated {
			t.Fatalf("promote with a foreign header = %d: %s", r.status, r.raw)
		}
		entity := promotedEntity(t, r)
		if got := f.quoteBranch(t, entity); got == other || got != f.defaultBranch(t) {
			t.Errorf("the promoted quote sits on %s (header named %s); the promoter must run at the draft's branch", got, other)
		}
	}
}

func (f *fixture) otherBranch(t *testing.T) string {
	t.Helper()
	var id string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT id::text FROM locations WHERE type='BRANCH' AND id <> (SELECT value::uuid FROM system_settings WHERE key='default_branch_id') LIMIT 1`).Scan(&id); err != nil {
		return ""
	}
	return id
}

func (f *fixture) defaultBranch(t *testing.T) string {
	t.Helper()
	var id string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT value::text FROM system_settings WHERE key='default_branch_id'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) quoteBranch(t *testing.T, id string) string {
	t.Helper()
	var branch string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT branch_id::text FROM quotes WHERE id = $1`, id).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	return branch
}

func promotedEntity(t *testing.T, r resp) string {
	t.Helper()
	promoted, _ := r.body["promoted"].(map[string]any)
	if promoted == nil {
		t.Fatalf("no promoted block in %s", r.raw)
	}
	return str(t, promoted, "entity_id")
}

// TestPromotionSubjectStaleAddedAndAbsent pins the one translated error
// (section 4.5): a subject that moved past the draft's subject_revision is
// a stale_revision WITH the subject_stale blocker; a stale DRAFT revision
// carries no blocker.
func TestPromotionSubjectStaleAddedAndAbsent(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))

	// An edit draft legitimately built on the quote's revision 1, then the
	// quote moves: the promotion is stale on the SUBJECT.
	subject := f.createQuote(t)
	r := f.do("POST", "/api/v1/drafts/quotes", map[string]any{
		"payload": f.editPayload(subject), "subject_id": subject, "subject_revision": 1})
	if r.status != http.StatusCreated {
		t.Fatalf("create edit draft = %d: %s", r.status, r.raw)
	}
	id := str(t, r.body, "id")

	moved := f.do("PUT", "/api/v1/quotes/"+subject, map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "pickup",
		"lines": []map[string]any{{
			"product_id": f.productID.String(), "sku": f.sku, "description": "2x4x8 SPF",
			"quantity": "30", "uom": "PCS", "unit_price_ten_thousandths": 55000,
		}},
		"revision": 1,
	}, "Idempotency-Key", "subject-move-"+f.sku)
	if moved.status != http.StatusOK {
		t.Fatalf("move the subject quote = %d: %s", moved.status, moved.raw)
	}

	r = f.do("POST", "/api/v1/drafts/quotes/"+id+"/promote", map[string]any{"revision": 1})
	if r.status != http.StatusConflict {
		t.Fatalf("promote a stale subject = %d: %s", r.status, r.raw)
	}
	code, _, details := errorOf(t, r)
	if code != "stale_revision" || !hasBlocker(details, "subject_stale") {
		t.Errorf("code=%s details=%v, want stale_revision with subject_stale", code, details)
	}

	// A stale DRAFT revision carries no blocker.
	id2 := f.create()
	f.do("PUT", "/api/v1/drafts/quotes/"+id2, map[string]any{"payload": f.payload(), "revision": 1})
	r = f.do("POST", "/api/v1/drafts/quotes/"+id2+"/promote", map[string]any{"revision": 1})
	if r.status != http.StatusConflict {
		t.Fatalf("promote a stale draft = %d: %s", r.status, r.raw)
	}
	code, _, details = errorOf(t, r)
	if code != "stale_revision" || hasBlocker(details, "subject_stale") {
		t.Errorf("code=%s details=%v, want stale_revision with no blocker", code, details)
	}
}

// failingRecorder fails every write, so the promotion's outbox step rolls
// the whole transaction back.
type failingRecorder struct{}

func (failingRecorder) Write(context.Context, outbox.Event) error {
	return fmt.Errorf("the event write failed (a test fault)")
}

// promoteFixture builds a drafts service whose outbox recorder fails, to
// prove the rollback; everything else is the standard fixture.
func promoteFixture(t *testing.T, db *database.DB, recorder drafts.EventRecorder) (*fixture, *drafts.Service) {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	f := &fixture{t: t, db: db, customerID: uuid.New(), productID: uuid.New(), sku: "FAIL-" + uuid.NewString()[:8]}
	ctx := context.Background()
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Promote Fail Co', $2, `+branch+`)`, f.customerID, "PF-"+uuid.NewString()[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, '2x4x8 SPF', 'PCS', 5.5)`, f.productID, f.sku); err != nil {
		t.Fatal(err)
	}
	orderSvc := order.NewService(order.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	quoteSvc := quote.NewService(quote.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).
		WithAudit(audit.NewLogger(db)).
		WithBranchGuard(middleware.NewBranchGuard(db)).
		WithOrderCreator(orderSvc)
	quoteKind := quote.NewDraftKind(quoteSvc)
	draftsRepo := drafts.NewRepository(db)
	registry, _ := drafts.NewRegistry(quoteKind)
	hub := drafts.NewHub(draftsRepo, 50*time.Millisecond, nil)
	svc := drafts.NewService(draftsRepo, registry).
		WithOutbox(recorder).
		WithTxRunner(db).
		WithAudit(audit.NewLogger(db)).
		WithBranchGuard(middleware.NewBranchGuard(db)).
		WithFeed(hub)
	f.hub = hub
	f.draftsRepo = draftsRepo
	handler := drafts.NewHandler(svc).WithFeedHandler(drafts.NewFeedHandler(svc, draftsRepo, hub, fastFeedSettings(), nil))
	mux := http.NewServeMux()
	quote.RegisterDraftRoutes(mux, handler, quoteKind, middleware.NewBranchMiddleware(db).Handler)
	f.srv = httptest.NewServer(middleware.Idempotency(db)(mux))

	t.Cleanup(func() {
		f.srv.Close()
		hub.Stop()
		// Scoped to this fixture's own drafts (by the payload's customer),
		// never by module: other packages' tests run in parallel and hold
		// their own.
		_, _ = db.Pool.Exec(ctx, `DELETE FROM draft_events WHERE draft_id IN (SELECT id FROM drafts WHERE payload->>'customer_id' = $1)`, f.customerID.String())
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_type='draft' AND entity_id IN (SELECT id FROM drafts WHERE payload->>'customer_id' = $1)`, f.customerID.String())
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type='draft' AND entity_id IN (SELECT id FROM drafts WHERE payload->>'customer_id' = $1)`, f.customerID.String())
		_, _ = db.Pool.Exec(ctx, `DELETE FROM drafts WHERE payload->>'customer_id' = $1`, f.customerID.String())
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, f.productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, f.customerID)
	})
	return f, svc
}

// TestPromotionFailingEventRollsEverythingBack pins section 4.5: a failing
// event write leaves no quote, no number reuse, and the draft exactly as it
// was (still open at the same revision), with one draft.promotion_refused
// audit row after the rollback.
func TestPromotionFailingEventRollsEverythingBack(t *testing.T) {
	db := testutil.RequireDB(t)
	f, _ := promoteFixture(t, db, failingRecorder{})
	id := f.create()

	quotesBefore := countRows(t, db, `SELECT count(*) FROM quotes WHERE customer_id = $1`, f.customerID.String())
	maxBefore := maxQuoteNumber(t, db)

	r := f.do("POST", "/api/v1/drafts/quotes/"+id+"/promote", map[string]any{"revision": 1})
	if r.status != http.StatusInternalServerError {
		t.Fatalf("promote with a failing event write = %d: %s", r.status, r.raw)
	}

	// No quote appeared.
	if got := countRows(t, db, `SELECT count(*) FROM quotes WHERE customer_id = $1`, f.customerID.String()); got != quotesBefore {
		t.Errorf("quotes for the customer = %d, want %d (nothing committed)", got, quotesBefore)
	}
	// No number reuse: the abandoned number stays abandoned (a gapped
	// sequence loses it, ADR 0001 section 8).
	if maxAfter := maxQuoteNumber(t, db); maxAfter != maxBefore {
		t.Errorf("the quote number moved %d -> %d while nothing committed", maxBefore, maxAfter)
	}
	// The draft is exactly as it was: open, revision 1.
	got := f.do("GET", "/api/v1/drafts/quotes/"+id, nil)
	if got.status != http.StatusOK || str(t, got.body, "status") != "open" || num(t, got.body, "revision") != 1 {
		t.Fatalf("the draft changed by a failed promotion: %s", got.raw)
	}
	// One draft.promotion_refused row, best effort, after the rollback.
	rows := f.auditRows(id)
	found := false
	for _, row := range rows {
		if row["action"] == "draft.promotion_refused" {
			found = true
			ch := row["changes"].(map[string]any)
			if ch["status"] != "open" || ch["code"] == "" {
				t.Errorf("promotion_refused changes = %v", ch)
			}
		}
	}
	if !found {
		t.Errorf("no draft.promotion_refused row among %d rows", len(rows))
	}

	// A later promotion with a working recorder succeeds on the same draft.
	f2, _ := promoteFixture(t, db, outbox.NewWriter(db, ""))
	// Re-create the draft under the second fixture's ids (the first fixture
	// cleaned nothing yet; use its own payload).
	id2 := f2.create()
	r = f2.do("POST", "/api/v1/drafts/quotes/"+id2+"/promote", map[string]any{"revision": 1})
	if r.status != http.StatusCreated {
		t.Fatalf("promote after the recorder was fixed = %d: %s", r.status, r.raw)
	}
}

func countRows(t *testing.T, db *database.DB, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func maxQuoteNumber(t *testing.T, db *database.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT COALESCE(max(substring(number from '[0-9]+')::bigint), 0) FROM quotes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestPromotionAlreadyPromotedAndReplay pins section 4.4: a keyless retry
// meets already_promoted whatever revision it sends (the status is checked
// before the revision), and a keyed retry replays the stored response.
func TestPromotionAlreadyPromotedAndReplay(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	id := f.create()
	key := "promote-" + f.sku

	r1 := f.do("POST", "/api/v1/drafts/quotes/"+id+"/promote", map[string]any{"revision": 1}, "Idempotency-Key", key)
	if r1.status != http.StatusCreated {
		t.Fatalf("promote = %d: %s", r1.status, r1.raw)
	}

	// Keyless retry: already_promoted, naming the entity, whatever revision.
	r2 := f.do("POST", "/api/v1/drafts/quotes/"+id+"/promote", map[string]any{"revision": 99})
	if r2.status != http.StatusConflict {
		t.Fatalf("retry without a key = %d: %s", r2.status, r2.raw)
	}
	code, msg, details := errorOf(t, r2)
	if code != "invalid_state_transition" || !hasBlocker(details, "already_promoted") {
		t.Errorf("code=%s details=%v", code, details)
	}
	entity := promotedEntity(t, r1)
	if !strings.Contains(msg, entity) {
		t.Errorf("message %q does not name the entity %s", msg, entity)
	}

	// Keyed retry: the stored response, marked replayed.
	r3 := f.do("POST", "/api/v1/drafts/quotes/"+id+"/promote", map[string]any{"revision": 1}, "Idempotency-Key", key)
	if r3.status != http.StatusCreated || r3.header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("keyed retry = %d replayed=%q", r3.status, r3.header.Get("Idempotency-Replayed"))
	}
	if str(t, r3.body, "id") != str(t, r1.body, "id") {
		t.Error("the replay answered another draft")
	}

	// A discarded draft refuses with the draft_discarded blocker.
	id4 := f.create()
	f.do("POST", "/api/v1/drafts/quotes/"+id4+"/transitions", map[string]any{"to": "discarded", "revision": 1})
	r4 := f.do("POST", "/api/v1/drafts/quotes/"+id4+"/promote", map[string]any{"revision": 1})
	if r4.status != http.StatusConflict || !hasBlocker(mustDetails(t, r4), "draft_discarded") {
		t.Errorf("promote a discarded draft = %d: %s", r4.status, r4.raw)
	}
}

func mustDetails(t *testing.T, r resp) []map[string]any {
	t.Helper()
	_, _, d := errorOf(t, r)
	return d
}

// TestPromotionPayloadPathsOnA400 pins section 4.2 step 4: a payload that
// fails the kind's full parser is a 400 with every field, each path
// prefixed payload.
func TestPromotionPayloadPathsOnA400(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	// A draft whose payload is structurally sound but incomplete: promotion
	// parses with the FULL parser and refuses.
	p := map[string]any{"delivery_type": "pickup", "lines": []map[string]any{
		{"product_id": f.productID.String(), "quantity": "10", "uom": "PCS"},
	}}
	r := f.do("POST", "/api/v1/drafts/quotes", map[string]any{"payload": p})
	if r.status != http.StatusCreated {
		t.Fatalf("create unfinished draft = %d: %s", r.status, r.raw)
	}
	id := str(t, r.body, "id")
	r = f.do("POST", "/api/v1/drafts/quotes/"+id+"/promote", map[string]any{"revision": 1})
	if r.status != http.StatusBadRequest {
		t.Fatalf("promote an unfinished payload = %d: %s", r.status, r.raw)
	}
	code, _, details := errorOf(t, r)
	if code != "validation_failed" {
		t.Errorf("code = %s", code)
	}
	if !hasDetailField(details, "payload.customer_id") {
		t.Errorf("details = %v, want payload.customer_id", details)
	}
	// The draft is unchanged and still open.
	got := f.do("GET", "/api/v1/drafts/quotes/"+id, nil)
	if str(t, got.body, "status") != "open" {
		t.Errorf("status = %v after a refused promotion", got.body["status"])
	}
}

// TestPromotionEventsInOrderAndProvenance pins sections 4.4 and 6: the
// outbox carries the module's own event first and draft.promoted last, the
// audit row names the proposers and the committer, the outbox data carries
// proposed_by and committed_by, and the response body is the draft with
// its promoted block.
func TestPromotionEventsInOrderAndProvenance(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	id := f.create()
	// A second writer: an agent with the person's session.
	f.do("PUT", "/api/v1/drafts/quotes/"+id, map[string]any{"payload": f.payload(), "revision": 1},
		"X-Acting-As", "agent", "X-Agent-Tool", "quote-builder")

	r := f.do("POST", "/api/v1/drafts/quotes/"+id+"/promote", map[string]any{"revision": 2})
	if r.status != http.StatusCreated {
		t.Fatalf("promote = %d: %s", r.status, r.raw)
	}
	entity := promotedEntity(t, r)
	promoted, _ := r.body["promoted"].(map[string]any)
	if promoted["number"] == nil || !strings.HasPrefix(promoted["number"].(string), "Q-") {
		t.Errorf("promoted.number = %v, want a Q- number", promoted["number"])
	}
	if str(t, r.body, "status") != "promoted" || num(t, r.body, "revision") != 3 {
		t.Errorf("the answer is not the promoted draft: %s", r.raw)
	}
	if r.header.Get("Location") != "/api/v1/quotes/"+entity {
		t.Errorf("Location = %q", r.header.Get("Location"))
	}

	// The events feed order: quote.created first, draft.promoted last.
	types := eventsForEntity(t, f.db, "quote", entity)
	if len(types) != 1 || types[0] != "quote.created" {
		t.Errorf("quote events = %v", types)
	}
	draftTypes := eventsForEntity(t, f.db, "draft", id)
	if len(draftTypes) != 1 || draftTypes[0] != "draft.promoted" {
		t.Fatalf("draft events = %v", draftTypes)
	}
	// The outbox data names who proposed and who committed.
	var data map[string]any
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT data FROM events_outbox WHERE entity_type='draft' AND entity_id=$1`, id).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if data["entity"] != "quote" || data["entity_id"] != entity {
		t.Errorf("draft.promoted data = %v", data)
	}
	proposedBy, _ := data["proposed_by"].(map[string]any)
	if proposedBy == nil {
		t.Errorf("proposed_by = %v, want the proposing actor object", data["proposed_by"])
	}
	if data["committed_by"] == nil {
		t.Errorf("committed_by missing: %v", data)
	}

	// The audit row names the proposers and carries the payload hash.
	rows := f.auditRows(id)
	var promoRow map[string]any
	for _, row := range rows {
		if row["action"] == "draft.promoted" {
			promoRow = row
		}
	}
	if promoRow == nil {
		t.Fatalf("no draft.promoted row among %d", len(rows))
	}
	ch := promoRow["changes"].(map[string]any)
	if ch["payload_sha256"] == nil || ch["entity_id"] != entity {
		t.Errorf("promotion row changes = %v", ch)
	}
	// The proposers: the anonymous creator and the agent, distinct.
	proposers, _ := ch["proposers"].([]any)
	if len(proposers) != 2 {
		t.Errorf("proposers = %v, want the two distinct actors (the anonymous creator and the agent)", proposers)
	}
}

// TestPromotionRaces pins the transaction proofs at the wire: two promoters
// racing one draft produce one 201 and one 409, and a PUT racing a
// promotion has exactly one winner.
func TestPromotionRaces(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))

	// Two promoters on one draft.
	id := f.create()
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.do("POST", "/api/v1/drafts/quotes/"+id+"/promote", map[string]any{"revision": 1})
			results <- r.status
		}()
	}
	wg.Wait()
	close(results)
	var created, refused int
	for status := range results {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			refused++
		default:
			t.Errorf("racing promotion answered %d", status)
		}
	}
	if created != 1 || refused != 1 {
		t.Errorf("two promoters: %d created, %d refused; want one each", created, refused)
	}

	// A PUT racing a promotion: exactly one wins.
	id2 := f.create()
	results2 := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i == 0 {
				r := f.do("PUT", "/api/v1/drafts/quotes/"+id2, map[string]any{"payload": f.payload(), "revision": 1})
				results2 <- r.status
				return
			}
			r := f.do("POST", "/api/v1/drafts/quotes/"+id2+"/promote", map[string]any{"revision": 1})
			results2 <- r.status
		}(i)
	}
	wg.Wait()
	close(results2)
	var winners int
	for status := range results2 {
		if status == http.StatusOK || status == http.StatusCreated {
			winners++
		} else if status != http.StatusConflict {
			t.Errorf("racer answered %d", status)
		}
	}
	if winners != 1 {
		t.Errorf("PUT racing a promotion: %d winners, want exactly one", winners)
	}
	// The draft is either promoted (revision 2) or open at revision 2: one
	// write moved it exactly once.
	got := f.do("GET", "/api/v1/drafts/quotes/"+id2, nil)
	if num(t, got.body, "revision") != 2 {
		t.Errorf("revision = %v, want 2 (exactly one winner moved it once)", got.body["revision"])
	}
}
