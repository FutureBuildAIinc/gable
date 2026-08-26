// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// Capability 8 is the order change feed: GET /orders?since=<cursor> returns
// what has moved since the cursor, and the response's LatestUpdatedAt is the
// cursor to send back next time. A consumer polling it must never miss a
// change — that is the entire promise, and it is what let the portal consumer
// delete its own de-duplication workaround.
//
// The page size and the ordering live in SQL
// (ListOrdersByCustomerFiltered), so this is DB-gated. It skips cleanly when
// Postgres is unreachable.

// newFeedFixture builds one customer with `n` orders whose created_at and
// updated_at are set independently — which is what the change feed's two
// columns are in production: created_at is fixed at insert, updated_at moves on
// every ERP status write.
//
// Order i is created at base+i hours (so a higher i is newer by created_at) and
// updated at the caller's discretion via updatedAt.
func newFeedFixture(t *testing.T, n int, updatedAt func(i int) time.Time) (*PostgresRepository, uuid.UUID, []uuid.UUID) {
	t.Helper()
	db := testutil.RequireDB(t)
	ctx := context.Background()

	customerID := uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}

	exec(`INSERT INTO customers (id, name, account_number, primary_branch_id)
	      VALUES ($1, $2, $3, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`,
		customerID, "Feed fixture "+customerID.String()[:8], "FEED-"+customerID.String()[:8])

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ids := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		id := uuid.New()
		ids = append(ids, id)
		exec(`INSERT INTO orders (id, customer_id, status, total_amount, branch_id, created_at, updated_at)
		      VALUES ($1, $2, 'CONFIRMED', 100.00,
		              (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'), $3, $4)`,
			id, customerID, base.Add(time.Duration(i)*time.Hour), updatedAt(i))
	}

	t.Cleanup(func() {
		c := context.Background()
		if _, err := db.Pool.Exec(c, `DELETE FROM order_lines WHERE order_id = ANY($1)`, ids); err != nil {
			t.Errorf("teardown order_lines: %v", err)
		}
		if _, err := db.Pool.Exec(c, `DELETE FROM orders WHERE customer_id = $1`, customerID); err != nil {
			t.Errorf("teardown orders: %v", err)
		}
		if _, err := db.Pool.Exec(c, `DELETE FROM customers WHERE id = $1`, customerID); err != nil {
			t.Errorf("teardown customer: %v", err)
		}
	})

	return NewRepository(db), customerID, ids
}

// feedService wires a Service over a real repository with no collaborators —
// ListOrdersFiltered touches nothing else.
func feedService(repo Repository) *Service {
	return NewService(repo, testSecret, testLogger(), nil, nil, nil, nil, nil)
}

// CORRECTNESS: polling the feed with the cursor it just returned must
// eventually deliver every changed order. Nothing may be skipped.
//
// It used to fail. The query was
//
//	... WHERE o.updated_at > $since ORDER BY o.created_at DESC LIMIT 50
//
// so the page was truncated by CREATED_AT while the cursor was UPDATED_AT. When
// more than 50 orders had changed, the 50 most recently CREATED won the page,
// and LatestUpdatedAt was computed over just those. An order that was created
// long ago but updated recently — a two-year-old job whose status finally moved
// — ranked below the cut, and the cursor then advanced past its updated_at. The
// next poll excluded it for good.
//
// The fixture below is the minimal shape of that: one old-but-just-updated
// order behind 50 newer ones.
//
// The feed is now ordered by updated_at ascending, so the cap truncates the
// tail rather than the middle, and the page's trailing tie group is completed
// (FETCH FIRST ... WITH TIES) so the cursor is a watermark rather than a guess.
func TestChangeFeed_CursorNeverSkipsAnUpdatedOrder(t *testing.T) {
	const page = 50

	// Order 0 is the old one: oldest created_at, and updated EARLIER than the
	// 50 newer orders — so once the cursor advances to their updated_at, it is
	// past order 0's.
	oldUpdate := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	newUpdate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	repo, customerID, ids := newFeedFixture(t, page+1, func(i int) time.Time {
		if i == 0 {
			return oldUpdate
		}
		return newUpdate
	})
	svc := feedService(repo)
	ctx := context.Background()

	// First poll: everything is newer than the epoch cursor.
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seen := map[uuid.UUID]bool{}

	for poll := 0; poll < 5; poll++ {
		res, err := svc.ListOrdersFiltered(ctx, customerID, OrderListFilter{Since: &since})
		if err != nil {
			t.Fatalf("poll %d: %v", poll, err)
		}
		for _, o := range res.Orders {
			seen[o.ID] = true
		}
		if res.LatestUpdatedAt == nil {
			break // the feed says there is nothing more
		}
		since = *res.LatestUpdatedAt
	}

	for i, id := range ids {
		if !seen[id] {
			t.Errorf("order %d (%s) never reached a polling consumer", i, id)
		}
	}
}

// CORRECTNESS: the inverse of the characterization that used to pin the loss
// above. Same fixture — 51 orders, 50 of which share one updated_at — and the
// three assertions are each the opposite of what they were:
//
//   - the oldest-created order IS in the first page, because the feed is
//     ordered by updated_at and it is the oldest change;
//   - the cursor is a watermark, so the 50 orders that share the newest
//     updated_at are ALL in the page even though that overshoots the 50-row
//     cap (FETCH FIRST ... WITH TIES) — a plain LIMIT would have cut one of
//     them off below a cursor that had already passed it;
//   - the second poll is therefore legitimately empty, because nothing was
//     left behind, not because something was lost.
func TestChangeFeed_FullPageCompletesItsTrailingTieGroup(t *testing.T) {
	const page = 50

	oldUpdate := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	newUpdate := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	repo, customerID, ids := newFeedFixture(t, page+1, func(i int) time.Time {
		if i == 0 {
			return oldUpdate
		}
		return newUpdate
	})
	svc := feedService(repo)
	ctx := context.Background()

	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	res, err := svc.ListOrdersFiltered(ctx, customerID, OrderListFilter{Since: &since})
	if err != nil {
		t.Fatalf("ListOrdersFiltered: %v", err)
	}

	if len(res.Orders) != page+1 {
		t.Fatalf("first page has %d orders, want all %d — the %d rows tied on the newest "+
			"updated_at must not be split across the cap", len(res.Orders), page+1, page)
	}
	found := false
	for _, o := range res.Orders {
		if o.ID == ids[0] {
			found = true
		}
	}
	if !found {
		t.Error("the oldest-created order is missing from the first page; the feed must be ordered by updated_at, " +
			"and its change is the oldest one after the cursor")
	}
	if res.Orders[0].ID != ids[0] {
		t.Errorf("first row is %s, want the oldest change %s — the feed is updated_at ASC so the LIMIT "+
			"truncates the newest tail, not the oldest head", res.Orders[0].ID, ids[0])
	}

	if res.LatestUpdatedAt == nil {
		t.Fatal("no cursor was returned")
	}
	if !res.LatestUpdatedAt.UTC().Equal(newUpdate) {
		t.Errorf("cursor = %s, want the page's newest updated_at %s", res.LatestUpdatedAt.UTC(), newUpdate)
	}

	next, err := svc.ListOrdersFiltered(ctx, customerID, OrderListFilter{Since: res.LatestUpdatedAt})
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if len(next.Orders) != 0 {
		t.Errorf("second poll returned %d orders, want 0 — everything at or below the cursor was already delivered", len(next.Orders))
	}
}

// CORRECTNESS: the paging half of the same promise. With more changed orders
// than fit in one page and no two sharing an updated_at, the cap really does
// truncate, and a consumer that keeps polling with the returned cursor walks
// the whole set in order without repeats.
//
// This is the case the tie fixture above cannot exercise: there, the trailing
// tie group pulls the whole set into one page.
func TestChangeFeed_PagesForwardThroughMoreChangesThanOnePageHolds(t *testing.T) {
	const page = 50
	const total = page + 7

	// Every order gets its own updated_at, deliberately anti-correlated with
	// created_at: the most recently created order changed longest ago. Under
	// created_at ordering the first page would be exactly the rows the cursor
	// is about to skip.
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	repo, customerID, ids := newFeedFixture(t, total, func(i int) time.Time {
		return base.Add(time.Duration(total-i) * time.Minute)
	})
	svc := feedService(repo)
	ctx := context.Background()

	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seen := map[uuid.UUID]int{}
	polls := 0

	for polls < 5 {
		res, err := svc.ListOrdersFiltered(ctx, customerID, OrderListFilter{Since: &since})
		if err != nil {
			t.Fatalf("poll %d: %v", polls, err)
		}
		polls++
		if len(res.Orders) == 0 {
			break
		}
		if polls == 1 && len(res.Orders) != page {
			t.Fatalf("first page has %d orders, want the %d-row cap — the page must truncate here",
				len(res.Orders), page)
		}
		for _, o := range res.Orders {
			seen[o.ID]++
		}
		if res.LatestUpdatedAt == nil {
			t.Fatal("a non-empty page carried no cursor")
		}
		since = *res.LatestUpdatedAt
	}

	for i, id := range ids {
		switch seen[id] {
		case 1:
		case 0:
			t.Errorf("order %d (%s) never reached a polling consumer", i, id)
		default:
			t.Errorf("order %d (%s) was delivered %d times; the cursor must be strictly greater-than", i, id, seen[id])
		}
	}
}

// CORRECTNESS: a consumer that polls with the cursor does NOT get the row that
// produced it back again. This is the strictly-greater-than half of the
// contract and it does hold.
func TestChangeFeed_CursorDoesNotReDeliverItsOwnRow(t *testing.T) {
	updated := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	repo, customerID, _ := newFeedFixture(t, 3, func(int) time.Time { return updated })
	svc := feedService(repo)
	ctx := context.Background()

	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first, err := svc.ListOrdersFiltered(ctx, customerID, OrderListFilter{Since: &since})
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if len(first.Orders) != 3 {
		t.Fatalf("first poll returned %d orders, want 3", len(first.Orders))
	}
	if first.LatestUpdatedAt == nil {
		t.Fatal("no cursor was returned")
	}

	second, err := svc.ListOrdersFiltered(ctx, customerID, OrderListFilter{Since: first.LatestUpdatedAt})
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if len(second.Orders) != 0 {
		t.Errorf("second poll re-delivered %d rows, want 0 — the comparison must be strictly greater-than", len(second.Orders))
	}
	if second.ETag == first.ETag {
		t.Error("an empty second page shares the first page's ETag, so a cache would serve stale data")
	}
}

// CORRECTNESS: the feed is scoped to one customer even with a cursor. A
// `since` filter must not widen the tenant scope.
func TestChangeFeed_IsScopedToTheCustomer(t *testing.T) {
	updated := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	repo, mine, _ := newFeedFixture(t, 2, func(int) time.Time { return updated })
	_, theirs, theirIDs := newFeedFixture(t, 2, func(int) time.Time { return updated })
	svc := feedService(repo)
	ctx := context.Background()

	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	res, err := svc.ListOrdersFiltered(ctx, mine, OrderListFilter{Since: &since})
	if err != nil {
		t.Fatalf("ListOrdersFiltered: %v", err)
	}

	other := map[uuid.UUID]bool{}
	for _, id := range theirIDs {
		other[id] = true
	}
	for _, o := range res.Orders {
		if other[o.ID] {
			t.Errorf("customer %s's feed contains customer %s's order %s", mine, theirs, o.ID)
		}
	}
	if len(res.Orders) != 2 {
		t.Errorf("feed returned %d orders, want this customer's 2", len(res.Orders))
	}
}
