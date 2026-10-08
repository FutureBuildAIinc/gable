// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// The transaction proofs of the order module (ADR 0003 section 2 and the
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
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// dbHandle is the thin handle the tests' helpers take: the pool for fixture
// writes, the transaction runner for the gated saturation runner.
type dbHandle struct{ db *database.DB }

func (h *dbHandle) Exec(ctx context.Context, sql string, args ...any) (tag int64, err error) {
	commandTag, err := h.db.Pool.Exec(ctx, sql, args...)
	return commandTag.RowsAffected(), err
}

func (h *dbHandle) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return h.db.Pool.QueryRow(ctx, sql, args...)
}

func (h *dbHandle) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return h.db.RunInTx(ctx, fn)
}

type failingEvents struct{}

func (failingEvents) Write(context.Context, outbox.Event) error {
	return errors.New("outbox insert failed")
}

func seedForTx(t *testing.T, db *dbHandle) (customerID, productID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	customerID, productID = uuid.New(), uuid.New()
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Tx Order Co', $2, `+branch+`)`, customerID, "TXO-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, 'Tx stud', 'EA', 5)`, productID, "TXO-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	// A migrated, unseeded database has no branch tax rate; the resolver
	// refuses an order without one, so the fixtures set it.
	if _, err := db.Exec(ctx, `UPDATE locations SET default_tax_rate = 0.088750
		WHERE id = (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`); err != nil {
		t.Fatalf("seed branch rate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'order' AND entity_id IN (SELECT id FROM orders WHERE customer_id = $1)`, customerID)
		_, _ = db.Exec(ctx, `DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = $1)`, customerID)
		_, _ = db.Exec(ctx, `DELETE FROM orders WHERE customer_id = $1`, customerID)
		_, _ = db.Exec(ctx, `DELETE FROM products WHERE id = $1`, productID)
		_, _ = db.Exec(ctx, `DELETE FROM customers WHERE id = $1`, customerID)
	})
	return customerID, productID
}

func draftFor(customerID, productID uuid.UUID) *order.Draft {
	qty := salesdoc.One * 2
	return &order.Draft{
		CustomerID:   customerID,
		DeliveryType: order.DeliveryPickup,
		Lines:        []salesdoc.ParsedLine{{LineType: salesdoc.LineProduct, ProductID: &productID, Quantity: qty}},
	}
}

// pricedDraftFor is draftFor with a price override and a percent discount, so
// the line audits (order.line_price_overridden, order.line_discounted) are
// written inside the transaction under test.
func pricedDraftFor(customerID, productID uuid.UUID) *order.Draft {
	d := draftFor(customerID, productID)
	price := httpx.Price(40000) // the product lists at 5.00, 50000
	pct := salesdoc.One * 10
	d.Lines[0].UnitPrice = &price
	d.Lines[0].OverrideReason = "match a competitor"
	d.Lines[0].DiscountPercent = &pct
	d.Lines[0].DiscountReason = "volume"
	return d
}

func countRows(t *testing.T, db *dbHandle, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// RULE (ADR 0003 section 3): a mutation whose event cannot be recorded does
// not happen, for each kind of write.
func TestFailedEventWriteRollsBackEachWrite(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	customerID, productID := seedForTx(t, &dbHandle{db})
	ctx := context.Background()

	// Create.
	// Every service wires the audit logger, so the "no audit rows survived"
	// assertions below can fail: the line audits (override, discount) and the
	// confirm's audit row are written through the transaction, before the
	// event that fails.
	auditLog := audit.NewLogger(db)
	lineAudits := func() int {
		return countRows(t, &dbHandle{db}, `SELECT count(*) FROM audit_log
			WHERE action IN ('order.line_price_overridden', 'order.line_discounted') AND changes->>'product_id' = $1`, productID.String())
	}
	bad := order.NewService(order.NewRepository(db)).WithOutbox(failingEvents{}).WithTxRunner(db).WithAuditLog(auditLog)
	if _, err := bad.Create(ctx, pricedDraftFor(customerID, productID), "tx"); err == nil {
		t.Fatal("Create succeeded though its event could not be written")
	}
	if n := countRows(t, &dbHandle{db}, `SELECT count(*) FROM orders WHERE customer_id = $1`, customerID); n != 0 {
		t.Errorf("%d orders survived a rolled back create", n)
	}
	if n := lineAudits(); n != 0 {
		t.Errorf("%d line audit rows survived a rolled back create", n)
	}

	// The good service for the writes that need a live order. Its create
	// writes the two line audits, the control that shows the assertions
	// above can fail.
	good := order.NewService(order.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithAuditLog(auditLog)
	o, err := good.Create(ctx, pricedDraftFor(customerID, productID), "tx")
	if err != nil {
		t.Fatal(err)
	}
	if n := lineAudits(); n != 2 {
		t.Fatalf("a committed create wrote %d line audit rows, want 2 (override and discount)", n)
	}

	// Update.
	badUpdate := order.NewService(order.NewRepository(db)).WithOutbox(failingEvents{}).WithTxRunner(db).WithAuditLog(auditLog)
	rev := int64(1)
	if _, err := badUpdate.Update(ctx, o.ID, pricedDraftFor(customerID, productID), order.Precondition{Revision: &rev}, "tx"); err == nil {
		t.Fatal("Update succeeded though its event could not be written")
	}
	if n := lineAudits(); n != 2 {
		t.Errorf("%d line audit rows after a rolled back update, want the create's 2 and none added", n)
	}
	var status string
	var revision int64
	if err := db.Pool.QueryRow(ctx, `SELECT status, revision FROM orders WHERE id = $1`, o.ID).Scan(&status, &revision); err != nil {
		t.Fatal(err)
	}
	if status != "DRAFT" || revision != 1 {
		t.Errorf("after a rolled back update: status=%s revision=%d, want DRAFT 1", status, revision)
	}

	// The confirm transition.
	if _, err := badUpdate.Transition(ctx, o.ID, order.StatusConfirmed, order.Precondition{Revision: &rev}, order.TransitionBody{}); err == nil {
		t.Fatal("Transition succeeded though its event could not be written")
	}
	if err := db.Pool.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1`, o.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "DRAFT" {
		t.Errorf("after a rolled back confirm: status=%s, want DRAFT", status)
	}
	// The confirm writes order.confirmed to the audit log before its event;
	// the create's two line audits are the only rows that may exist.
	if n := countRows(t, &dbHandle{db}, `SELECT count(*) FROM audit_log WHERE entity_type = 'order' AND entity_id = $1 AND action = 'order.confirmed'`, o.ID); n != 0 {
		t.Errorf("%d audit rows survived a rolled back confirm", n)
	}

	// The document number of a rolled back create is abandoned (ADR 0001
	// section 8: sequences are not transactional, gaps are expected).
	a, err := good.Create(ctx, draftFor(customerID, productID), "tx")
	if err != nil {
		t.Fatal(err)
	}
	b, err := good.Create(ctx, draftFor(customerID, productID), "tx")
	if err != nil {
		t.Fatal(err)
	}
	if a.Number == b.Number {
		t.Errorf("two orders share the number %s", a.Number)
	}
}

// Three contenders at pool size 4 (the lane rule on transactions): three
// goroutines create orders at once, then three race one transition on the
// same revision, then three move distinct orders while a reader pages the
// list on the same four connections. Exactly one racer wins the shared
// revision.
func TestConcurrencyPool4ThreeContenders(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	customerID, productID := seedForTx(t, &dbHandle{db})
	svc := order.NewService(order.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const contenders, each = 3, 3

	// 1. Concurrent creates: distinct numbers, one event each.
	var mu sync.Mutex
	var created []*order.Order
	var wg sync.WaitGroup
	errs := make(chan error, contenders*each)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				o, err := svc.Create(ctx, draftFor(customerID, productID), "tx")
				if err != nil {
					errs <- err
					return
				}
				mu.Lock()
				created = append(created, o)
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
	for _, o := range created {
		if seen[o.Number] {
			t.Errorf("number %s minted twice", o.Number)
		}
		seen[o.Number] = true
	}
	if len(created) != contenders*each {
		t.Fatalf("%d orders created, want %d", len(created), contenders*each)
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
			_, err := svc.Transition(ctx, target.ID, order.StatusConfirmed, order.Precondition{Revision: &rev}, order.TransitionBody{})
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
	if n := countRows(t, &dbHandle{db}, `SELECT count(*) FROM events_outbox WHERE entity_id = $1 AND type = 'order.confirmed'`, target.ID); n != 1 {
		t.Errorf("%d order.confirmed events for the raced order, want 1", n)
	}

	// 3. Three contenders on distinct orders, mixed with edits, while a
	// reader pages the list on the same four connections.
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
			if _, _, _, err := svc.ListOrders(ctx, order.ListFilter{Limit: 5, CustomerID: &customerID}, true); err != nil {
				readerDone <- err
				return
			}
		}
	}()
	for i := 1; i <= contenders; i++ {
		o := created[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			rev := int64(1)
			if _, err := svc.Update(ctx, o.ID, draftFor(customerID, productID), order.Precondition{Revision: &rev}, "tx"); err != nil {
				t.Errorf("update: %v", err)
				return
			}
			rev = 2
			if _, err := svc.Transition(ctx, o.ID, order.StatusCancelled, order.Precondition{Revision: &rev}, order.TransitionBody{Reason: "tx race"}); err != nil {
				t.Errorf("cancel: %v", err)
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
// transaction blocks forever.
type gatedTx struct {
	db      *dbHandle
	want    int32
	entered int32
	gate    sync.WaitGroup
}

func newGatedTx(db *dbHandle, want int) *gatedTx {
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
// reached for a second connection from the pool would leave four holders
// each waiting for a fifth that never frees, and the deadline would fire.
func TestConcurrencyPool4SaturationNeedsNoSecondConnection(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	customerID, productID := seedForTx(t, &dbHandle{db})
	events := outbox.NewWriter(db, "")
	plain := order.NewService(order.NewRepository(db)).WithOutbox(events).WithTxRunner(db)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	const contenders = 4
	var seed []*order.Order
	for i := 0; i < 2*contenders; i++ {
		o, err := plain.Create(ctx, draftFor(customerID, productID), "tx")
		if err != nil {
			t.Fatal(err)
		}
		seed = append(seed, o)
	}

	phase := func(name string, run func(svc *order.Service, i int) error) {
		t.Helper()
		svc := order.NewService(order.NewRepository(db)).WithOutbox(events).WithTxRunner(newGatedTx(&dbHandle{db}, contenders))
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

	rev := int64(1)
	phase("create", func(svc *order.Service, i int) error {
		_, err := svc.Create(ctx, draftFor(customerID, productID), "tx")
		return err
	})
	phase("update", func(svc *order.Service, i int) error {
		_, err := svc.Update(ctx, seed[i].ID, draftFor(customerID, productID), order.Precondition{Revision: &rev}, "tx")
		return err
	})
	phase("transition", func(svc *order.Service, i int) error {
		_, err := svc.Transition(ctx, seed[contenders+i].ID, order.StatusConfirmed, order.Precondition{Revision: &rev}, order.TransitionBody{})
		return err
	})
	phase("in process confirm", func(svc *order.Service, i int) error {
		_, err := svc.ConfirmInProcess(ctx, seed[i].ID)
		return err
	})
}

// RULE (recipe step 6): the branch wall applies to the get and the lock, not
// only the list. A branch-bound context sees another branch's order as a
// 404-shaped ErrNotFound.
func TestRepositoryBranchWall(t *testing.T) {
	db := testutil.RequireDB(t)
	customerID, productID := seedForTx(t, &dbHandle{db})
	ctx := context.Background()
	repo := order.NewRepository(db)
	svc := order.NewService(repo).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	o, err := svc.Create(ctx, draftFor(customerID, productID), "tx")
	if err != nil {
		t.Fatal(err)
	}
	walled := middleware.WithBranchContext(ctx, &middleware.BranchContext{BranchID: ptrUUID(uuid.New())})
	if _, err := repo.GetOrder(walled, o.ID); !errors.Is(err, order.ErrNotFound) {
		t.Errorf("a second branch's get = %v, want ErrNotFound", err)
	}
	if err := repo.LockOrder(walled, o.ID); !errors.Is(err, order.ErrNotFound) {
		t.Errorf("a second branch's lock = %v, want ErrNotFound", err)
	}
}

func ptrUUID(id uuid.UUID) *uuid.UUID { return &id }
