// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// The C2-2b transaction proofs at pool size 4 (ADR 0005 section 11, the lane
// rule on transactions): every new kind of write holds as many contenders as
// the pool has connections inside their transactions, and nothing reaches for a
// second connection; two orders confirming against the same two products in
// opposite line order finish without deadlock; three fulfilments of one order
// bill each allocated unit once.

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type poolWorld struct {
	db         *database.DB
	customerID uuid.UUID
	products   []uuid.UUID
	svc        func(tx order.TxRunner) *order.Service
}

// newPoolWorld seeds a customer, n stocked products (1000 each at 3.00 cost)
// and builds services over a pool of 4.
func newPoolWorld(t *testing.T, n int) *poolWorld {
	t.Helper()
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	ctx := context.Background()
	w := &poolWorld{db: db, customerID: uuid.New()}
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	must := func(sql string, args ...any) {
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must(`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'Pool Co', $2, `+branch+`)`, w.customerID, "POOL-"+uuid.NewString()[:8])
	must(`UPDATE locations SET default_tax_rate = 0.088750 WHERE id = ` + branch)
	yard := uuid.New()
	must(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, `+branch+`, `+branch+`)`, yard, "PL-"+yard.String()[:8])
	for i := 0; i < n; i++ {
		p := uuid.New()
		w.products = append(w.products, p)
		must(`INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost) VALUES ($1, $2, 'pool stud', 'PCS', 5.50, 3.00)`, p, "POOL-"+p.String()[:8])
		must(`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 1000, 0)`, p, yard)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_lines WHERE journal_entry_id IN (SELECT e.id FROM gl_journal_entries e JOIN invoices i ON e.source_ref_id = i.id WHERE i.customer_id = $1)`, w.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_entries WHERE source_ref_id IN (SELECT id FROM invoices WHERE customer_id = $1)`, w.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id IN (SELECT id FROM orders WHERE customer_id = $1) OR entity_id IN (SELECT id FROM invoices WHERE customer_id = $1)`, w.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_transactions WHERE customer_id = $1`, w.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM invoice_lines WHERE invoice_id IN (SELECT id FROM invoices WHERE customer_id = $1)`, w.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM invoices WHERE customer_id = $1`, w.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = $1)`, w.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM orders WHERE customer_id = $1`, w.customerID)
		for _, p := range w.products {
			_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id = $1`, p)
			_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, p)
		}
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE location_id = $1`, yard)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, yard)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, w.customerID)
	})
	glSvc := gl.NewService(gl.NewRepository(db), nil, slog.Default())
	inv := invoice.NewService(invoice.NewRepository(db), glSvc, account.NewService(db, glSvc, slog.Default()), db)
	w.svc = func(tx order.TxRunner) *order.Service {
		return order.NewService(order.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(tx).
			WithAuditLog(audit.NewLogger(db)).
			WithInventory(inventory.NewService(inventory.NewRepository(db))).WithInvoices(inv)
	}
	return w
}

// draft is a delivery order of the given product quantities, in line order.
func (w *poolWorld) draft(lines ...struct {
	product uuid.UUID
	qty     string
}) *order.Draft {
	d := &order.Draft{CustomerID: w.customerID, DeliveryType: order.DeliveryDelivery}
	for _, l := range lines {
		pid := l.product
		q, _ := httpx.ParseQuantity(l.qty)
		d.Lines = append(d.Lines, salesdoc.ParsedLine{LineType: salesdoc.LineProduct, ProductID: &pid, Quantity: q})
	}
	return d
}

type lineSpec = struct {
	product uuid.UUID
	qty     string
}

// RULE (ADR 0005 11, 14.2): two orders confirming against the same two
// products in opposite line order finish without deadlock, repeatedly: the
// allocation takes inventory in (product id, line id) order whatever the line
// order is.
func TestOppositeLineOrderConfirmsDoNotDeadlock(t *testing.T) {
	w := newPoolWorld(t, 2)
	svc := w.svc(w.db)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	p, q := w.products[0], w.products[1]
	for round := 0; round < 12; round++ {
		a, err := svc.Create(ctx, w.draft(lineSpec{p, "3"}, lineSpec{q, "3"}), "tx")
		if err != nil {
			t.Fatal(err)
		}
		b, err := svc.Create(ctx, w.draft(lineSpec{q, "4"}, lineSpec{p, "4"}), "tx")
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, o := range []*order.Order{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				rev := int64(1)
				if _, err := svc.Transition(ctx, o.ID, order.StatusConfirmed, order.Precondition{Revision: &rev}, order.TransitionBody{}); err != nil {
					t.Errorf("round %d confirm: %v", round, err)
				}
			}()
		}
		close(start)
		wg.Wait()
		if ctx.Err() != nil {
			t.Fatalf("round %d: the confirms deadlocked", round)
		}
	}
}

// RULE (ADR 0005 14.2): three fulfilments of one order at pool size 4 bill each
// allocated unit once: nine allocated units, three racers each billing three.
func TestThreeFulfilmentsBillEachAllocatedUnitOnce(t *testing.T) {
	w := newPoolWorld(t, 1)
	svc := w.svc(w.db)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	o, err := svc.Create(ctx, w.draft(lineSpec{w.products[0], "9"}), "tx")
	if err != nil {
		t.Fatal(err)
	}
	rev := int64(1)
	o, err = svc.Transition(ctx, o.ID, order.StatusConfirmed, order.Precondition{Revision: &rev}, order.TransitionBody{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Fulfil(ctx, o.ID, nil, order.FulfilRequest{
				Lines: []order.FulfilLineRequest{{OrderLineID: o.Lines[0].ID, Quantity: 3 * salesdoc.One}}, Actor: "tx"})
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("fulfilment: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("the fulfilments did not finish: a transaction waited on a second pool connection")
	}
	var invoices int
	var billed, fulfilled, allocated, onHand, onAlloc string
	if err := w.db.Pool.QueryRow(ctx, `SELECT count(*), COALESCE(sum(l.quantity), 0)::text FROM invoices i JOIN invoice_lines l ON l.invoice_id = i.id WHERE i.order_id = $1`, o.ID).Scan(&invoices, &billed); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Pool.QueryRow(ctx, `SELECT quantity_fulfilled::text, quantity_allocated::text FROM order_lines WHERE id = $1`, o.Lines[0].ID).Scan(&fulfilled, &allocated); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Pool.QueryRow(ctx, `SELECT quantity::text, allocated::text FROM inventory WHERE product_id = $1`, w.products[0]).Scan(&onHand, &onAlloc); err != nil {
		t.Fatal(err)
	}
	if invoices != 3 || billed != "9.0000" || fulfilled != "9.0000" || allocated != "0.0000" || onHand != "991.0000" || onAlloc != "0.0000" {
		t.Errorf("%d invoices billing %s; line fulfilled %s allocated %s; inventory %s/%s; want 3 invoices, 9 billed once, 991 on hand, nothing allocated", invoices, billed, fulfilled, allocated, onHand, onAlloc)
	}
	// Invoice numbers are the C2-3 gapless counter's; here: three distinct invoices, one entry each.
	var entries int
	if err := w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM gl_journal_entries e JOIN invoices i ON i.id = e.source_ref_id WHERE i.order_id = $1`, o.ID).Scan(&entries); err != nil || entries != 3 {
		t.Errorf("%d journal entries (%v), want 3", entries, err)
	}
}

// Saturation: as many contenders as the pool has connections, held inside
// their transactions at a gate, for each new kind of write (confirm with
// allocation, on demand allocation, fulfilment, the queue serves). A
// transaction that reached for a second connection would leave four holders
// each waiting for a fifth that never frees, and the deadline would fire.
func TestNewWritesAtPool4NeedNoSecondConnection(t *testing.T) {
	w := newPoolWorld(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	plain := w.svc(w.db)
	const contenders = 4
	var seed []*order.Order
	for i := 0; i < 3*contenders; i++ {
		o, err := plain.Create(ctx, w.draft(lineSpec{w.products[0], "2"}), "tx")
		if err != nil {
			t.Fatal(err)
		}
		seed = append(seed, o)
	}
	phase := func(name string, run func(svc *order.Service, i int) error) {
		t.Helper()
		gated := newGatedTx(&dbHandle{w.db}, contenders)
		svc := w.svc(gated)
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
	phase("confirm with allocation", func(svc *order.Service, i int) error {
		_, err := svc.Transition(ctx, seed[i].ID, order.StatusConfirmed, order.Precondition{Revision: &rev}, order.TransitionBody{})
		return err
	})
	phase("allocate on demand", func(svc *order.Service, i int) error {
		// confirmed orders have nothing on back order: a clean no-op in a transaction
		r := int64(2)
		_, err := svc.AllocateOrder(ctx, seed[i].ID, order.Precondition{Revision: &r}, "tx")
		return err
	})
	phase("fulfilment", func(svc *order.Service, i int) error {
		_, err := svc.Fulfil(ctx, seed[i].ID, nil, order.FulfilRequest{Actor: "tx"})
		return err
	})
	// The queue serves: requests for the back ordered orders of the next four.
	for i := contenders; i < 2*contenders; i++ {
		if _, err := w.db.Pool.Exec(ctx, `UPDATE inventory SET quantity = 0 WHERE product_id = $1`, w.products[0]); err != nil {
			t.Fatal(err)
		}
		r := int64(1)
		if _, err := plain.Transition(ctx, seed[i].ID, order.StatusConfirmed, order.Precondition{Revision: &r}, order.TransitionBody{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.db.Pool.Exec(ctx, `UPDATE inventory SET quantity = quantity + 500 WHERE product_id = $1`, w.products[0]); err != nil {
		t.Fatal(err)
	}
	branch := uuid.Nil
	if err := w.db.Pool.QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	if err := w.db.RunInTx(ctx, func(ctx context.Context) error {
		_, err := order.NewRepository(w.db).QueueAllocationRequests(ctx, branch, w.products)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	phase("allocation queue serve", func(svc *order.Service, i int) error {
		_, err := svc.ServeAllocationRequest(ctx)
		return err
	})
	var left int
	if err := w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM order_allocation_requests WHERE order_id = ANY($1::uuid[])`, orderIDs(seed)).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d allocation requests left after four serves", left)
	}
	_ = errors.New
}

func orderIDs(os []*order.Order) []uuid.UUID {
	out := make([]uuid.UUID, len(os))
	for i := range os {
		out[i] = os[i].ID
	}
	return out
}
