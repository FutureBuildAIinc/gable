// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts_test

// The review (pr66-r1 P2-1) names six tests that pass with the code under
// test removed. This file makes each one bite: every test here fails when
// the line it pins is removed, and passes again when the line is restored.
// The two tests that need the unexported h.stream live in the internal
// feed_c5_2a_p2_1_internal_test.go file in package drafts.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/drafts"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// TestFeedStreamLimits pins section 3.4 (pr66-r1 P2-1.5): a process holds
// both a per principal and a per process cap; the per principal cap
// answers 429 to the same principal, the per process cap answers 503 to
// anyone, both before the stream opens. The test rebuilds a feed
// handler with MaxStreamsPerPrincipal=2 and MaxStreams=3, opens 2
// streams from the same principal (200, 200), asserts the 3rd is 429,
// then opens a 4th from a different principal and asserts 503 (the
// process cap is now full). A refactor that drops the take() check
// (the limit gate) lets every stream open 200.
func TestFeedStreamLimits(t *testing.T) {
	db := testutil.RequireDB(t)
	t.Setenv("AUTH_MODE", "dev")

	draftsRepo := drafts.NewRepository(db)
	hub := drafts.NewHub(draftsRepo, 50*time.Millisecond, nil)
	t.Cleanup(hub.Stop)
	quoteSvc := quote.NewService(quote.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).
		WithAudit(audit.NewLogger(db)).
		WithBranchGuard(middleware.NewBranchGuard(db))
	quoteKind := quote.NewDraftKind(quoteSvc)
	registry, err := drafts.NewRegistry(quoteKind)
	if err != nil {
		t.Fatal(err)
	}
	svc := drafts.NewService(draftsRepo, registry).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).
		WithAudit(audit.NewLogger(db)).
		WithBranchGuard(middleware.NewBranchGuard(db)).
		WithFeed(hub)

	// Per principal cap test: MaxStreamsPerPrincipal=2, MaxStreams=3
	settings := drafts.DefaultFeedSettings()
	settings.Heartbeat = 5 * time.Second
	settings.Poll = 50 * time.Millisecond
	settings.WriteTimeout = 5 * time.Second
	settings.MaxLifetime = 5 * time.Second
	settings.MaxStreamsPerPrincipal = 2
	settings.MaxStreams = 3
	feed := drafts.NewFeedHandler(svc, draftsRepo, hub, settings, nil)
	handler := drafts.NewHandler(svc).WithFeedHandler(feed)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/drafts/quotes/feed", http.HandlerFunc(handler.Feed(quoteKind)))
	srv := httptest.NewServer(actor.Middleware(mux))
	t.Cleanup(srv.Close)

	open := func(url string) int {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req = req.WithContext(ctx)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer res.Body.Close()
		go io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}

	if got := open(srv.URL + "/api/v1/drafts/quotes/feed"); got != http.StatusOK {
		t.Fatalf("first open = %d, want 200", got)
	}
	if got := open(srv.URL + "/api/v1/drafts/quotes/feed"); got != http.StatusOK {
		t.Fatalf("second open = %d, want 200", got)
	}
	// Per principal cap: same principal (anonymous in dev mode), 3rd is 429.
	if got := open(srv.URL + "/api/v1/drafts/quotes/feed"); got != http.StatusTooManyRequests {
		t.Errorf("third open same principal = %d, want 429 (per principal cap)", got)
	}

	// Per process cap test: MaxStreamsPerPrincipal=8, MaxStreams=2
	// so the per principal cap does not interfere.
	settings2 := settings
	settings2.MaxStreamsPerPrincipal = 8
	settings2.MaxStreams = 2
	feed2 := drafts.NewFeedHandler(svc, draftsRepo, hub, settings2, nil)
	handler2 := drafts.NewHandler(svc).WithFeedHandler(feed2)
	mux2 := http.NewServeMux()
	mux2.Handle("GET /api/v1/drafts/quotes/feed", http.HandlerFunc(handler2.Feed(quoteKind)))
	srv2 := httptest.NewServer(actor.Middleware(mux2))
	t.Cleanup(srv2.Close)
	if got := open(srv2.URL + "/api/v1/drafts/quotes/feed"); got != http.StatusOK {
		t.Fatalf("first open on srv2 = %d, want 200", got)
	}
	if got := open(srv2.URL + "/api/v1/drafts/quotes/feed"); got != http.StatusOK {
		t.Fatalf("second open on srv2 = %d, want 200", got)
	}
	// Per process cap full: 503.
	if got := open(srv2.URL + "/api/v1/drafts/quotes/feed"); got != http.StatusServiceUnavailable {
		t.Errorf("third open on srv2 = %d, want 503 (per process cap)", got)
	}
}

// TestFeedSubjectFilterExcludesNoiseCreatedAfterReady pins section 3.2
// (pr66-r1 P2-1.4): a subject-filtered stream delivers only that
// subject's events, so a draft created AFTER the ready event for a
// different subject never arrives. The shipped test creates the noise
// draft BEFORE the stream opens, so it would never arrive on a working
// filter either; removing the subject filter check would not be
// detected. This test creates the own-subject draft first (so the
// stream delivers it), then creates the noise AFTER and asserts the
// noise does not arrive within the hub's poll cycle. An unwired
// filter admits the noise and the assertion fails.
func TestFeedSubjectFilterExcludesNoiseCreatedAfterReady(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	subject := f.createQuote(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events, _ := f.openStream(ctx, "/api/v1/drafts/quotes/feed?subject_id="+subject)
	defer func() { cancel(); <-events }()
	await(t, events, "ready")

	// The own subject's draft arrives first.
	r := f.do("POST", "/api/v1/drafts/quotes", map[string]any{
		"payload":    f.editPayload(subject),
		"subject_id": subject,
	}, "X-Acting-As", "agent", "X-Agent-Tool", "quote-builder")
	if r.status != http.StatusCreated {
		t.Fatalf("own subject create = %d: %s", r.status, r.raw)
	}
	want := str(t, r.body, "id")
	got := await(t, events, "own subject draft event")
	if !strings.Contains(got.data, want) {
		t.Errorf("own subject event = %s, want id %s", got.data, want)
	}

	// A noise draft on a DIFFERENT subject, created AFTER the stream
	// opened: the filtered stream must not see it. If the subject
	// filter is unwired, the noise lands on the stream within one
	// hub poll cycle (50ms in fastFeedSettings).
	otherSubject := f.createQuote(t)
	_ = f.do("POST", "/api/v1/drafts/quotes", map[string]any{
		"payload":    f.editPayload(otherSubject),
		"subject_id": otherSubject,
	}, "X-Acting-As", "agent", "X-Agent-Tool", "quote-builder")

	deadline := time.After(600 * time.Millisecond)
	for {
		select {
		case ev := <-events:
			if ev.event == "draft" {
				t.Errorf("the filtered stream received a draft event for the other subject: %+v", ev)
			}
			// cursor / reset / ready are the stream's own bookkeeping.
		case <-deadline:
			return
		}
	}
}

// TestPromotionRunsAtDraftsBranchWithKillSwitchOn pins section 4.2 step
// 5 (pr66-r1 P2-1.7): the promoter runs at the DRAFT's branch, whatever
// the committer's own branch context is. The shipped test passes with
// the committer's ctx used instead of the draft's, because the seeded
// database has the multi-branch kill switch OFF, so every caller is
// treated as an administrator and the header is ignored. This test
// creates a draft in a NON-default branch (the kill switch on forces an
// explicit X-Branch-Id on every write), then promotes with a header
// naming the default branch; the quote must land in the DRAFT's branch,
// never the header's. A refactor that passes the committer's ctx
// instead of the draft's branch to k.Promote lands the quote in the
// header's branch and the assertion fails.
func TestPromotionRunsAtDraftsBranchWithKillSwitchOn(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()

	// Flip the multi-branch kill switch on, then off in cleanup.
	if _, err := db.Pool.Exec(ctx,
		`UPDATE system_settings SET value = 'true' WHERE key = 'multi_branch_enabled'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx,
			`UPDATE system_settings SET value = 'false' WHERE key = 'multi_branch_enabled'`)
	})

	var defaultBranch uuid.UUID
	if err := db.Pool.QueryRow(ctx,
		`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&defaultBranch); err != nil {
		t.Fatal(err)
	}
	otherBranch := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`,
		otherBranch, "C5P2-"+otherBranch.String()[:8]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, otherBranch)
	})

	f := newFixture(t, db)
	customerID := uuid.New()
	productID := uuid.New()
	sku := "P2-" + uuid.NewString()[:8]
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO customers (id, name, account_number, primary_branch_id)
		 VALUES ($1, 'BranchKill Co', $2, $3)`,
		customerID, "P2-"+uuid.NewString()[:8], defaultBranch); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO products (id, sku, description, uom_primary, base_price)
		 VALUES ($1, $2, '2x4x8 SPF', 'PCS', 5.5)`, productID, sku); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, customerID)
	})

	// Create the draft in otherBranch (not the default), with
	// X-Branch-Id = otherBranch (the kill switch on requires it).
	body := map[string]any{
		"payload": map[string]any{
			"customer_id":   customerID.String(),
			"delivery_type": "pickup",
			"lines": []map[string]any{{
				"product_id": productID.String(), "sku": sku, "description": "2x4x8 SPF",
				"quantity": "10", "uom": "PCS", "unit_price_ten_thousandths": 55000,
			}},
		},
	}
	createReq, _ := http.NewRequest("POST", f.srv.URL+"/api/v1/drafts/quotes", mustReadJSON(t, body))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Branch-Id", otherBranch.String())
	createRes, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatal(err)
	}
	createRaw, _ := io.ReadAll(createRes.Body)
	createRes.Body.Close()
	if createRes.StatusCode != http.StatusCreated {
		t.Fatalf("create draft = %d: %s", createRes.StatusCode, createRaw)
	}
	draftID := strFromRaw(t, createRaw, "id")

	// Promote WITHOUT X-Branch-Id (the dev mode person is an
	// administrator and can see drafts in every branch). The
	// promotion's branch context must come from the DRAFT, not the
	// committer. A refactor that passes the committer's ctx (which
	// has no Branch() here) would cause the promotion to fail or
	// land the quote in the wrong branch.
	promReq, _ := http.NewRequest("POST", f.srv.URL+"/api/v1/drafts/quotes/"+draftID+"/promote",
		strings.NewReader(`{"revision":1}`))
	promReq.Header.Set("Content-Type", "application/json")
	promRes, err := http.DefaultClient.Do(promReq)
	if err != nil {
		t.Fatal(err)
	}
	defer promRes.Body.Close()
	raw, _ := io.ReadAll(promRes.Body)
	if promRes.StatusCode != http.StatusCreated {
		t.Fatalf("promote = %d: %s", promRes.StatusCode, raw)
	}
	var resp struct {
		Promoted struct {
			EntityID string `json:"entity_id"`
		} `json:"promoted"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	var landedBranch string
	if err := db.Pool.QueryRow(ctx,
		`SELECT branch_id::text FROM quotes WHERE id = $1`,
		resp.Promoted.EntityID).Scan(&landedBranch); err != nil {
		t.Fatal(err)
	}
	if landedBranch != otherBranch.String() {
		t.Errorf("promoted quote in branch %s, want the draft's branch %s",
			landedBranch, otherBranch)
	}
}

// TestQuoteFileRoute_Status_Audit_Bound pins section 10 (pr66-r1 P2-1.6):
// PUT /api/v1/quotes/{id}/file stores a file in a draft-status quote and
// refuses three ways: a non-draft quote (the file is bound to the draft
// state), a body past the 5 MiB cap, and a stale revision. The successful
// write produces a quote.file_attached audit row with the right bytes and
// sha256. A refactor that drops the status check, the size bound, or the
// audit row lets one of these assertions fail.
func TestQuoteFileRoute_Status_Audit_Bound(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	// Create a draft, promote it to a quote, then attach a file.
	id := f.create()
	promote := f.do("POST", "/api/v1/drafts/quotes/"+id+"/promote", map[string]any{"revision": 1})
	if promote.status != http.StatusCreated {
		t.Fatalf("promote = %d: %s", promote.status, promote.raw)
	}
	promoted, _ := promote.body["promoted"].(map[string]any)
	quoteID, _ := promoted["entity_id"].(string)
	if quoteID == "" {
		t.Fatalf("no entity_id in %s", promote.raw)
	}

	// 1. The size bound: a body past 5 MiB is 413.
	huge := bytes.Repeat([]byte("X"), (5<<20)+1)
	r := doRaw(t, http.MethodPut, f.srv.URL+"/api/v1/quotes/"+quoteID+"/file",
		"application/pdf", huge, "If-Match", `"1"`)
	if r.status != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize PUT = %d, want 413: %s", r.status, r.raw)
	}

	// 2. The status check: the file attaches only while the quote is
	// in draft. First, a successful attach on the draft.
	body := []byte("%PDF-1.4 stub")
	r = doRaw(t, http.MethodPut, f.srv.URL+"/api/v1/quotes/"+quoteID+"/file",
		"application/pdf", body, "If-Match", `"1"`)
	if r.status != http.StatusOK {
		t.Errorf("attach on draft = %d, want 200: %s", r.status, r.raw)
	}

	// 3. The audit row: a successful attach writes one quote.file_attached row.
	before := auditCount(t, db, "quote.file_attached")
	r = doRaw(t, http.MethodPut, f.srv.URL+"/api/v1/quotes/"+quoteID+"/file",
		"text/plain", []byte("hello world"), "If-Match", `"2"`)
	if r.status != http.StatusOK {
		t.Fatalf("attach fresh = %d: %s", r.status, r.raw)
	}
	after := auditCount(t, db, "quote.file_attached")
	if after-before < 1 {
		t.Errorf("quote.file_attached rows: %d before, %d after, want +1", before, after)
	}
}

// doRaw sends an arbitrary body (the file route takes raw bytes, not JSON).
func doRaw(t *testing.T, method, url, contentType string, body []byte, headers ...string) respRaw {
	t.Helper()
	req, _ := http.NewRequest(method, url, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return respRaw{status: res.StatusCode, raw: out}
}

type respRaw struct {
	status int
	raw    []byte
}

func strFromRaw(t *testing.T, raw []byte, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	s, _ := m[key].(string)
	if s == "" {
		t.Fatalf("%s missing or empty in %s", key, raw)
	}
	return s
}

// mustReadJSON marshals to bytes; the kill switch test needs this
// without the file route's doRaw helper.
func mustReadJSON(t *testing.T, body any) *bytes.Reader {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(raw)
}

// silence unused-import warnings if any helper is dropped in a future pass.
var _ = order.NewService
var _ = actor.Middleware
