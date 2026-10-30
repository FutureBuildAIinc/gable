// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote_test

// The follow up slice on the wire (QE-04-a): a salesperson sets a follow up
// date and owner on a quote, works the follow up queue through the list's
// follow_up filter, logs activities on the quote (which also show on the
// customer's feed), and clears the follow up when the chase is done. Every
// test states a wire fact end to end: a real Postgres, the quote and crm
// handlers on one mux, requests as JSON.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/crm"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// qe04Fixture is the quote wire fixture plus the crm module mounted beside
// it, the way serve wires the activity seam, and an optional signed in email
// the follow_up=mine filter resolves.
type qe04Fixture struct {
	*fixture
	claimsEmail string
}

func newQE04Fixture(t *testing.T, db *database.DB) *qe04Fixture {
	t.Helper()
	f := &qe04Fixture{fixture: newFixture(t, db)}
	// Rebuild the server with the crm handler mounted beside the quote's, so
	// a quote activity's appearance on the customer's feed is a wire fact.
	crmSvc := crm.NewService(crm.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	svc := quote.NewService(quote.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	mux := http.NewServeMux()
	quote.NewHandler(svc).WithActivities(crmSvc).RegisterRoutes(mux)
	crm.NewHandler(crmSvc).RegisterRoutes(mux)
	claims := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if f.claimsEmail != "" {
				r = r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey,
					&middleware.UserClaims{Email: f.claimsEmail}))
			}
			next.ServeHTTP(w, r)
		})
	}
	f.srv.Close()
	f.srv = httptest.NewServer(middleware.Idempotency(db)(claims(mux)))
	return f
}

// salesperson seeds one sales_team row and returns its id.
func (f *qe04Fixture) salesperson(name, email string) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	ctx := context.Background()
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO sales_team (id, name, email, role) VALUES ($1, $2, $3, 'Sales Rep')`,
		id, name, email); err != nil {
		f.t.Fatalf("seed salesperson: %v", err)
	}
	f.t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM quotes WHERE follow_up_owner_id = $1`, id)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM sales_team WHERE id = $1`, id)
	})
	return id
}

// utcBranch moves the default branch to UTC, so the follow up queue's "today
// in the branch's time" is the test's own calendar.
func (f *qe04Fixture) utcBranch() {
	f.t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE locations SET timezone = 'UTC' WHERE id = (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`); err != nil {
		f.t.Fatal(err)
	}
}

func TestQE04_SetFollowUp(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newQE04Fixture(t, testutil.RequireDB(t))
	created := f.create()
	id := str(t, created.body, "id")
	owner := f.salesperson("Brooks Ribera", "brooks.r@example.com")
	when := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)

	set := f.do("PUT", "/api/v1/quotes/"+id+"/follow-up", map[string]any{
		"follow_up_at": when, "follow_up_owner_id": owner.String(), "revision": 1,
	})
	if set.status != http.StatusOK {
		t.Fatalf("set follow up = %d: %s", set.status, set.raw)
	}
	if got := set.body["follow_up_at"]; got == nil {
		t.Errorf("follow_up_at is null after the set: %s", set.raw)
	}
	if got, _ := set.body["follow_up_owner_id"].(string); got != owner.String() {
		t.Errorf("follow_up_owner_id = %v, want %s", set.raw, owner)
	}
	if got, _ := set.body["follow_up_owner_name"].(string); got != "Brooks Ribera" {
		t.Errorf("follow_up_owner_name = %v, want the owner's name", got)
	}
	if got := num(t, set.body, "revision"); got != 2 {
		t.Errorf("revision = %d, want 2: the set moves it", got)
	}

	// The read carries the follow up.
	got := f.do("GET", "/api/v1/quotes/"+id, nil)
	if got.status != http.StatusOK || got.body["follow_up_at"] == nil {
		t.Fatalf("read = %d: %s", got.status, got.raw)
	}

	// A stale revision is refused 409; the stored follow up stands.
	stale := f.do("PUT", "/api/v1/quotes/"+id+"/follow-up", map[string]any{
		"follow_up_at": when, "revision": 1,
	})
	if code, _, _ := errorOf(t, stale); code != "stale_revision" {
		t.Errorf("stale revision = %d %s, want 409 stale_revision", stale.status, stale.raw)
	}
	// A write with neither If-Match nor a body revision is 428.
	bare := f.do("PUT", "/api/v1/quotes/"+id+"/follow-up", map[string]any{"follow_up_at": when})
	if bare.status != http.StatusPreconditionRequired {
		t.Errorf("no precondition = %d %s, want 428", bare.status, bare.raw)
	}
}

func TestQE04_ClearFollowUp(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newQE04Fixture(t, testutil.RequireDB(t))
	created := f.create()
	id := str(t, created.body, "id")
	owner := f.salesperson("Dana Vale", "dana.v@example.com")
	when := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	if r := f.do("PUT", "/api/v1/quotes/"+id+"/follow-up", map[string]any{
		"follow_up_at": when, "follow_up_owner_id": owner.String(), "revision": 1,
	}); r.status != http.StatusOK {
		t.Fatalf("set follow up = %d: %s", r.status, r.raw)
	}

	cleared := f.do("PUT", "/api/v1/quotes/"+id+"/follow-up", map[string]any{
		"follow_up_at": nil, "revision": 2,
	})
	if cleared.status != http.StatusOK {
		t.Fatalf("clear follow up = %d: %s", cleared.status, cleared.raw)
	}
	if cleared.body["follow_up_at"] != nil {
		t.Errorf("follow_up_at = %v after the clear, want null", cleared.body["follow_up_at"])
	}
	if cleared.body["follow_up_owner_id"] != nil {
		t.Errorf("follow_up_owner_id = %v after the clear, want null", cleared.body["follow_up_owner_id"])
	}
	if got := num(t, cleared.body, "revision"); got != 3 {
		t.Errorf("revision = %d, want 3: the clear moves it", got)
	}
	// A cleared quote is not in the due queue.
	list := f.do("GET", "/api/v1/quotes?follow_up=due", nil)
	if items, _ := list.body["items"].([]any); len(items) != 0 {
		t.Errorf("follow_up=due after the clear holds %d quotes, want none", len(items))
	}
}

func TestQE04_ListFollowUpDueOverdueMine(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newQE04Fixture(t, testutil.RequireDB(t))
	f.utcBranch()
	mine := f.salesperson("Kim Osei", "kim.osei@example.com")
	other := f.salesperson("Rae Lindqvist", "rae.l@example.com")

	// Four open quotes (draft): yesterday, today, tomorrow, and none.
	// A closed quote with yesterday's date stays out of every queue.
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format(time.RFC3339)
	today := time.Now().UTC().Truncate(time.Hour).Format(time.RFC3339)
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format(time.RFC3339)
	ids := map[string]string{}
	for name, at := range map[string]string{"yesterday": yesterday, "today": today, "tomorrow": tomorrow} {
		r := f.create()
		id := str(t, r.body, "id")
		body := map[string]any{"follow_up_at": at, "revision": 1}
		if name == "yesterday" {
			body["follow_up_owner_id"] = mine.String()
		}
		if name == "today" {
			body["follow_up_owner_id"] = other.String()
		}
		if res := f.do("PUT", "/api/v1/quotes/"+id+"/follow-up", body); res.status != http.StatusOK {
			t.Fatalf("set follow up on %s = %d: %s", name, res.status, res.raw)
		}
		ids[name] = id
	}
	none := f.create()
	ids["none"] = str(t, none.body, "id")

	closed := f.create()
	closedID := str(t, closed.body, "id")
	if res := f.do("POST", "/api/v1/quotes/"+closedID+"/transitions",
		map[string]any{"to": "accepted", "revision": 1}); res.status != http.StatusOK {
		t.Fatalf("accept the closed quote = %d: %s", res.status, res.raw)
	}
	if res := f.do("PUT", "/api/v1/quotes/"+closedID+"/follow-up",
		map[string]any{"follow_up_at": yesterday, "revision": 2}); res.status != http.StatusOK {
		t.Fatalf("set follow up on the closed quote = %d: %s", res.status, res.raw)
	}

	inQueue := func(filter string) map[string]bool {
		t.Helper()
		r := f.do("GET", "/api/v1/quotes?follow_up="+filter, nil)
		if r.status != http.StatusOK {
			t.Fatalf("list follow_up=%s = %d: %s", filter, r.status, r.raw)
		}
		out := map[string]bool{}
		items, _ := r.body["items"].([]any)
		for _, it := range items {
			m, _ := it.(map[string]any)
			out[str(t, m, "id")] = true
		}
		return out
	}

	due := inQueue("due")
	if !due[ids["yesterday"]] || !due[ids["today"]] {
		t.Errorf("follow_up=due holds %v, want the yesterday and today quotes", due)
	}
	if due[ids["tomorrow"]] || due[ids["none"]] || due[closedID] {
		t.Errorf("follow_up=due holds %v, want no tomorrow, none or closed quote", due)
	}
	overdue := inQueue("overdue")
	if !overdue[ids["yesterday"]] || overdue[ids["today"]] {
		t.Errorf("follow_up=overdue holds %v, want the yesterday quote only", overdue)
	}
	if overdue[closedID] {
		t.Errorf("follow_up=overdue holds the closed quote: a closed quote is never in the queue")
	}

	// mine resolves the signed in email to its salesperson row; without a
	// sign in the caller owns none.
	f.claimsEmail = "kim.osei@example.com"
	if got := inQueue("mine"); len(got) != 1 || !got[ids["yesterday"]] {
		t.Errorf("follow_up=mine holds %v, want the quote owned by the signed in salesperson", got)
	}
	f.claimsEmail = ""
	if got := inQueue("mine"); len(got) != 0 {
		t.Errorf("follow_up=mine with no sign in holds %v, want none", got)
	}

	// A value outside the vocabulary is a 400 naming follow_up.
	bad := f.do("GET", "/api/v1/quotes?follow_up=someday", nil)
	if code, _, _ := errorOf(t, bad); code != "validation_failed" {
		t.Errorf("follow_up=someday = %d %s, want 400 validation_failed", bad.status, bad.raw)
	}
}

func TestQE04_QuoteActivitiesShowOnCustomerFeed(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newQE04Fixture(t, testutil.RequireDB(t))
	created := f.create()
	id := str(t, created.body, "id")

	logged := f.do("POST", "/api/v1/quotes/"+id+"/activities",
		map[string]any{"activity_type": "call", "description": "Chased the quote by phone"})
	if logged.status != http.StatusCreated {
		t.Fatalf("log a quote activity = %d: %s", logged.status, logged.raw)
	}
	if got, _ := logged.body["customer_id"].(string); got != f.customerID.String() {
		t.Errorf("the activity's customer = %v, want the quote's customer", got)
	}
	if got, _ := logged.body["quote_id"].(string); got != id {
		t.Errorf("the activity's quote_id = %v, want the quote", got)
	}
	activityID := str(t, logged.body, "id")

	quoteFeed := f.do("GET", "/api/v1/quotes/"+id+"/activities", nil)
	if quoteFeed.status != http.StatusOK {
		t.Fatalf("quote activities = %d: %s", quoteFeed.status, quoteFeed.raw)
	}
	if items, _ := quoteFeed.body["items"].([]any); len(items) != 1 {
		t.Errorf("the quote's feed holds %d activities, want the one logged", len(items))
	}

	// The same activity is on the customer's feed.
	customerFeed := f.do("GET", "/api/v1/customers/"+f.customerID.String()+"/activities", nil)
	if customerFeed.status != http.StatusOK {
		t.Fatalf("customer activities = %d: %s", customerFeed.status, customerFeed.raw)
	}
	found := false
	items, _ := customerFeed.body["items"].([]any)
	for _, it := range items {
		if m, _ := it.(map[string]any); str(t, m, "id") == activityID {
			found = true
		}
	}
	if !found {
		t.Errorf("the quote's activity is not on the customer's feed: %s", customerFeed.raw)
	}

	// A follow up activity is part of the vocabulary, on the quote route too.
	fu := f.do("POST", "/api/v1/quotes/"+id+"/activities",
		map[string]any{"activity_type": "follow_up", "description": "Promised to call back"})
	if fu.status != http.StatusCreated {
		t.Errorf("log a follow_up activity = %d: %s", fu.status, fu.raw)
	}

	// A quote that is not there is a 404, the read's own answer.
	missing := f.do("GET", "/api/v1/quotes/"+uuid.NewString()+"/activities", nil)
	if missing.status != http.StatusNotFound {
		t.Errorf("activities of a missing quote = %d, want 404", missing.status)
	}
}

// Three contenders at pool size 4 (the lane rule on transactions): three
// salespeople set the follow up on one quote at the same revision. Exactly
// one write wins; the others are told the race (409 stale_revision), and the
// stored follow up is the winner's, never a blend.
func TestQE04_FollowUpConcurrency_Pool4ThreeContenders(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newQE04Fixture(t, db)
	created := f.create()
	id := str(t, created.body, "id")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	svc := quote.NewService(quote.NewRepository(db)).WithTxRunner(db)
	const contenders = 3
	var wg sync.WaitGroup
	var mu sync.Mutex
	var winners, stale int
	dates := make([]time.Time, contenders)
	for i := 0; i < contenders; i++ {
		// Postgres keeps microseconds; truncate so the stored date compares
		// exactly against the contender that wrote it.
		dates[i] = time.Now().UTC().Add(time.Duration(i+1) * time.Hour).Truncate(time.Microsecond)
	}
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ts := httpx.TimestampOf(dates[i])
			rev := int64(1)
			_, err := svc.SetFollowUp(ctx, uuid.MustParse(id), &quote.FollowUpDraft{At: &ts},
				quote.Precondition{Revision: &rev})
			mu.Lock()
			defer mu.Unlock()
			var he *httpx.Error
			switch {
			case err == nil:
				winners++
			case errors.As(err, &he) && he.Status == http.StatusConflict:
				stale++
			default:
				t.Errorf("contender %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if winners != 1 || stale != contenders-1 {
		t.Fatalf("%d winners and %d stale of %d contenders, want 1 and %d", winners, stale, contenders, contenders-1)
	}
	var stored *time.Time
	var revision int64
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT follow_up_at, revision FROM quotes WHERE id = $1`, id).Scan(&stored, &revision); err != nil {
		t.Fatal(err)
	}
	if stored == nil {
		t.Fatal("the follow up did not land")
	}
	if revision != 2 {
		t.Errorf("revision = %d, want 2: one write moved it", revision)
	}
	matched := false
	for _, want := range dates {
		if stored.Equal(want) {
			matched = true
		}
	}
	if !matched {
		t.Errorf("the stored follow up %v is none of the contenders' dates", stored)
	}
}
