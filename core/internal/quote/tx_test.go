// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote_test

// The transaction proofs of the quote module (ADR 0003 section 2 and the
// lane rule on transactions): a mutation and its event are one fact, and
// every transaction runs on its own connection, never reaching for a second
// one from the pool. The concurrency test runs the whole service at pool
// size 4 with three contenders, so a pool use inside a transaction deadlocks
// the test instead of passing on a roomy pool.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type failingEvents struct{}

func (failingEvents) Write(context.Context, outbox.Event) error {
	return errors.New("outbox insert failed")
}

func seedCustomerAndProduct(t *testing.T, db *database.DB) (customerID, productID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	customerID, productID = uuid.New(), uuid.New()
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Tx Test Co', $2, `+branch+`)`, customerID, "TX-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, 'Tx stud', 'EA', 5)`, productID, "TX-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id IN (SELECT id FROM quotes WHERE customer_id = $1)`, customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1`, customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, customerID)
	})
	return customerID, productID
}

func draftFor(customerID, productID uuid.UUID) *quote.Draft {
	return &quote.Draft{
		CustomerID: customerID, DeliveryType: quote.DeliveryPickup, Source: "manual",
		Lines: []quote.DraftLine{{
			ProductID: &productID, SKU: "TX", Description: "stud", Quantity: 20000, UOM: product.UOM_EA,
			PriceUOM: "EA", UOMQty: 10000, PriceUOMQty: 10000, UnitPrice: 50000,
		}},
	}
}

func countRows(t *testing.T, db *database.DB, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := db.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// RULE (ADR 0003 section 3): a mutation whose event cannot be recorded does
// not happen. A failing outbox rolls the whole create back: no quote, no
// lines, no event.
func TestCreate_FailedEventWriteRollsBackTheQuote(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	customerID, productID := seedCustomerAndProduct(t, db)

	svc := quote.NewService(quote.NewRepository(db)).WithOutbox(failingEvents{}).WithTxRunner(db)
	if _, err := svc.Create(context.Background(), draftFor(customerID, productID)); err == nil {
		t.Fatal("Create succeeded though its event could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM quotes WHERE customer_id = $1`, customerID); n != 0 {
		t.Errorf("%d quotes survived a rolled back create", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM quote_lines WHERE sku = 'TX' AND product_id = $1`, productID); n != 0 {
		t.Errorf("%d lines survived a rolled back create", n)
	}
}

// A failing event rolls a transition back too: the status, the timestamps and
// the revision are exactly as they were.
func TestTransition_FailedEventWriteRollsBackTheTransition(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	customerID, productID := seedCustomerAndProduct(t, db)
	ctx := context.Background()

	good := quote.NewService(quote.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	q, err := good.Create(ctx, draftFor(customerID, productID))
	if err != nil {
		t.Fatal(err)
	}

	bad := quote.NewService(quote.NewRepository(db)).WithOutbox(failingEvents{}).WithTxRunner(db)
	rev := int64(1)
	if _, err := bad.Transition(ctx, q.ID, quote.QuoteStateSent, quote.Precondition{Revision: &rev}); err == nil {
		t.Fatal("Transition succeeded though its event could not be written")
	}
	var state string
	var revision int64
	var sentAt *time.Time
	if err := db.Pool.QueryRow(ctx, `SELECT state::text, revision, sent_at FROM quotes WHERE id = $1`, q.ID).Scan(&state, &revision, &sentAt); err != nil {
		t.Fatal(err)
	}
	if state != "DRAFT" || revision != 1 || sentAt != nil {
		t.Errorf("after a rolled back transition: state=%s revision=%d sent_at=%v", state, revision, sentAt)
	}
	if n := countRows(t, db, `SELECT count(*) FROM events_outbox WHERE entity_id = $1 AND type = 'quote.sent'`, q.ID); n != 0 {
		t.Errorf("%d quote.sent events survived", n)
	}
}

// The document number of a rolled back create is abandoned, never reused
// (ADR 0001 section 8: sequences are not transactional, gaps are expected).
func TestCreate_RolledBackNumberIsNotReused(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	customerID, productID := seedCustomerAndProduct(t, db)
	ctx := context.Background()

	bad := quote.NewService(quote.NewRepository(db)).WithOutbox(failingEvents{}).WithTxRunner(db)
	_, _ = bad.Create(ctx, draftFor(customerID, productID))
	good := quote.NewService(quote.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	a, err := good.Create(ctx, draftFor(customerID, productID))
	if err != nil {
		t.Fatal(err)
	}
	b, err := good.Create(ctx, draftFor(customerID, productID))
	if err != nil {
		t.Fatal(err)
	}
	if a.Number == b.Number {
		t.Errorf("two quotes share the number %s", a.Number)
	}
}

// Three contenders at pool size 4 (the lane rule on transactions): three
// goroutines create quotes at once, then three race one transition on the same
// revision, then three move distinct quotes. Every transaction holds exactly
// one connection for its whole length; if any reached for a second from the
// pool, four connections could not serve three contenders and a reader, and
// the deadline below would fire. Exactly one racer wins the shared revision.
func TestConcurrency_Pool4ThreeContenders(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	customerID, productID := seedCustomerAndProduct(t, db)
	svc := quote.NewService(quote.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const contenders, each = 3, 4

	// 1. Concurrent creates: distinct numbers, one event each.
	var mu sync.Mutex
	var created []*quote.Quote
	var wg sync.WaitGroup
	errs := make(chan error, contenders*each)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				q, err := svc.Create(ctx, draftFor(customerID, productID))
				if err != nil {
					errs <- err
					return
				}
				mu.Lock()
				created = append(created, q)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent create: %v", err)
	}
	seen := map[string]bool{}
	for _, q := range created {
		if seen[q.Number] {
			t.Errorf("number %s minted twice", q.Number)
		}
		seen[q.Number] = true
	}
	if len(created) != contenders*each {
		t.Fatalf("%d quotes created, want %d", len(created), contenders*each)
	}
	if n := countRows(t, db, `SELECT count(*) FROM events_outbox WHERE type = 'quote.created' AND entity_id IN (SELECT id FROM quotes WHERE customer_id = $1)`, customerID); n != contenders*each {
		t.Errorf("%d quote.created events for %d quotes", n, contenders*each)
	}

	// 2. Three racers, one revision: exactly one wins, the others see 409.
	target := created[0]
	var winners, stale int
	var rmu sync.Mutex
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rev := int64(1)
			_, err := svc.Transition(ctx, target.ID, quote.QuoteStateSent, quote.Precondition{Revision: &rev})
			rmu.Lock()
			defer rmu.Unlock()
			var he *httpx.Error
			switch {
			case err == nil:
				winners++
			case errors.As(err, &he) && he.Status == http.StatusConflict:
				stale++
			default:
				t.Errorf("racer: unexpected error %v", err)
			}
		}()
	}
	wg.Wait()
	if winners != 1 || stale != contenders-1 {
		t.Errorf("winners=%d stale=%d, want exactly one winner and %d refused", winners, stale, contenders-1)
	}
	if n := countRows(t, db, `SELECT count(*) FROM events_outbox WHERE entity_id = $1 AND type = 'quote.sent'`, target.ID); n != 1 {
		t.Errorf("%d quote.sent events for the raced quote, want 1", n)
	}

	// 3. Three contenders on distinct quotes, mixed with edits, while a reader
	// pages the list on the same four connections.
	stop := make(chan struct{})
	readerDone := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				readerDone <- nil
				return
			default:
			}
			if _, _, _, err := svc.ListQuotes(ctx, quote.ListFilter{Limit: 5, CustomerID: &customerID}, true); err != nil {
				readerDone <- err
				return
			}
		}
	}()
	for i := 1; i <= contenders; i++ {
		q := created[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			rev := int64(1)
			edited, err := svc.Update(ctx, q.ID, draftFor(customerID, productID), quote.Precondition{Revision: &rev})
			if err != nil {
				t.Errorf("update: %v", err)
				return
			}
			rev = edited.Revision
			if _, err := svc.Transition(ctx, q.ID, quote.QuoteStateAccepted, quote.Precondition{Revision: &rev}); err != nil {
				t.Errorf("transition: %v", err)
			}
		}()
	}
	wg.Wait()
	close(stop)
	if err := <-readerDone; err != nil {
		t.Errorf("reader: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("the contenders did not finish inside the deadline: a transaction waited on a second pool connection")
	}
}

// gatedTx is a TxRunner that makes the first `want` transactions meet inside
// their transactions before any of them runs a statement: each holds its one
// connection while it waits at the gate. With want equal to the pool size the
// pool is then empty, so any statement that goes to the pool instead of the
// transaction blocks forever. Without the gate the overlap would be luck.
type gatedTx struct {
	db      *database.DB
	want    int32
	entered int32
	gate    sync.WaitGroup
}

func newGatedTx(db *database.DB, want int) *gatedTx {
	g := &gatedTx{db: db, want: int32(want)}
	g.gate.Add(want)
	return g
}

func (g *gatedTx) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return g.db.RunInTx(ctx, func(txCtx context.Context) error {
		if atomic.AddInt32(&g.entered, 1) <= g.want {
			g.gate.Done()
			g.gate.Wait()
		}
		return fn(txCtx)
	})
}

// Saturation: as many contenders as the pool has connections, held inside
// their transactions at a gate, for each kind of write. A transaction that
// reached for a second connection from the pool would leave four holders each
// waiting for a fifth that never frees, and the deadline would fire. This is
// the test that fails when code inside a transaction uses the pool.
func TestConcurrency_Pool4SaturationNeedsNoSecondConnection(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	customerID, productID := seedCustomerAndProduct(t, db)
	events := outbox.NewWriter(db, "")
	// The convert needs the order service; the real one over the same
	// database joins the gated transactions.
	orderEvents := outbox.NewWriter(db, "")
	orderSvc := order.NewService(order.NewRepository(db)).WithOutbox(orderEvents).WithTxRunner(db)
	plain := quote.NewService(quote.NewRepository(db)).WithOutbox(events).WithTxRunner(db).WithOrderCreator(orderSvc)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const contenders = 4
	var seed []*quote.Quote
	for i := 0; i < 3*contenders; i++ {
		q, err := plain.Create(ctx, draftFor(customerID, productID))
		if err != nil {
			t.Fatal(err)
		}
		seed = append(seed, q)
	}

	phase := func(name string, run func(svc *quote.Service, i int) error) {
		t.Helper()
		svc := quote.NewService(quote.NewRepository(db)).WithOutbox(events).WithTxRunner(newGatedTx(db, contenders)).WithOrderCreator(orderSvc)
		var wg sync.WaitGroup
		errs := make(chan error, contenders)
		for i := 0; i < contenders; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := run(svc, i); err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("%s: %v", name, err)
		}
		if ctx.Err() != nil {
			t.Fatalf("%s: four contenders at pool size 4 did not finish: a transaction waited on a second pool connection", name)
		}
	}

	phase("create", func(svc *quote.Service, i int) error {
		_, err := svc.Create(ctx, draftFor(customerID, productID))
		return err
	})
	phase("update", func(svc *quote.Service, i int) error {
		rev := int64(1)
		_, err := svc.Update(ctx, seed[i].ID, draftFor(customerID, productID), quote.Precondition{Revision: &rev})
		return err
	})
	phase("transition", func(svc *quote.Service, i int) error {
		rev := int64(2)
		_, err := svc.Transition(ctx, seed[i].ID, quote.QuoteStateSent, quote.Precondition{Revision: &rev})
		return err
	})
	phase("convert", func(svc *quote.Service, i int) error {
		rev := int64(3)
		_, err := svc.Convert(ctx, seed[i].ID, quote.Precondition{Revision: &rev})
		return err
	})
	phase("in process transition", func(svc *quote.Service, i int) error {
		return svc.UpdateState(ctx, seed[contenders+i].ID, quote.QuoteStateRejected)
	})
}
