// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer_test

// The transaction proofs of the customer module (ADR 0003 section 2 and the
// lane rule on transactions): a mutation and its event are one fact, and every
// transaction runs on its own connection, never reaching for a second one from
// the pool. The concurrency tests run the whole service at pool size 4, so a
// pool use inside a transaction deadlocks the test instead of passing on a
// roomy pool.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type failingEvents struct{}

func (failingEvents) Write(context.Context, outbox.Event) error {
	return errors.New("outbox insert failed")
}

type txFixture struct {
	t      *testing.T
	db     *database.DB
	prefix string
	n      atomic.Int32
}

func newTxFixture(t *testing.T, db *database.DB) *txFixture {
	t.Helper()
	f := &txFixture{t: t, db: db, prefix: "TX-" + uuid.NewString()[:8] + "-"}
	t.Cleanup(func() {
		ctx := context.Background()
		pat := f.prefix + "%"
		ids := `SELECT id FROM customers WHERE account_number LIKE $1`
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'customer' AND entity_id IN (`+ids+`)`, pat)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_ship_tos WHERE customer_id IN (`+ids+`)`, pat)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_contacts WHERE customer_id IN (`+ids+`)`, pat)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id IN (`+ids+`)`, pat)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE account_number LIKE $1`, pat)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM payment_terms WHERE code LIKE $1`, f.prefix+"%")
	})
	return f
}

func (f *txFixture) draft() *customer.Draft {
	return &customer.Draft{
		AccountNumber: fmt.Sprintf("%s%04d", f.prefix, f.n.Add(1)), Name: "Tx Test Co",
		Tier: customer.TierRetail, IsActive: true,
	}
}

func (f *txFixture) service(events customer.EventRecorder, tx customer.TxRunner) *customer.Service {
	return customer.NewService(customer.NewRepository(f.db)).WithOutbox(events).WithTxRunner(tx)
}

func (f *txFixture) good() *customer.Service {
	return f.service(outbox.NewWriter(f.db, ""), f.db)
}

func (f *txFixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func rev(n int64) customer.Precondition { return customer.Precondition{Revision: &n} }

func shipDraft(code string) *customer.ShipToDraft {
	return &customer.ShipToDraft{Code: code, Name: "site " + code, Line1: "1 Main St", IsActive: true}
}

func contactDraft(first string) *customer.ContactDraft {
	return &customer.ContactDraft{FirstName: first, LastName: "Tx", IsActive: true, CanPlaceOrders: true}
}

func termsDraft(code string) *customer.TermsDraft {
	n := 30
	return &customer.TermsDraft{Code: code, Name: "terms " + code, Kind: customer.TermsNetDays, NetDays: &n, IsActive: true}
}

// RULE (ADR 0003 section 3): a mutation whose event cannot be recorded does
// not happen. A failing outbox rolls a create back: no customer, no branch
// mirror, no event.
func TestCreate_FailedEventWriteRollsBackTheCustomer(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newTxFixture(t, testutil.RequireDB(t))
	d := f.draft()
	if _, err := f.service(failingEvents{}, f.db).Create(context.Background(), d); err == nil {
		t.Fatal("Create succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM customers WHERE account_number = $1`, d.AccountNumber); n != 0 {
		t.Errorf("%d customers survived a rolled back create", n)
	}
	if n := f.count(`SELECT count(*) FROM customer_branches cb LEFT JOIN customers c ON c.id = cb.customer_id WHERE c.id IS NULL`); n != 0 {
		t.Errorf("%d orphaned branch mirror rows", n)
	}
}

// Every other kind of write rolls back with its event: the row, its revision
// and any side effect on other rows are exactly as they were.
func TestEveryWriteKind_FailedEventWriteRollsItBack(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newTxFixture(t, testutil.RequireDB(t))
	ctx := context.Background()
	good, bad := f.good(), f.service(failingEvents{}, f.db)

	c, err := good.Create(ctx, f.draft())
	if err != nil {
		t.Fatal(err)
	}
	first, err := good.CreateShipTo(ctx, c.ID, shipDraft("A")) // the default
	if err != nil {
		t.Fatal(err)
	}
	second, err := good.CreateShipTo(ctx, c.ID, shipDraft("B"))
	if err != nil {
		t.Fatal(err)
	}
	contact, err := good.CreateContact(ctx, c.ID, contactDraft("Pat"))
	if err != nil {
		t.Fatal(err)
	}
	customerRev := func() int64 {
		return int64(f.count(`SELECT revision FROM customers WHERE id = $1`, c.ID))
	}
	startRev := customerRev()

	d := f.draft()
	d.AccountNumber, d.Name = c.AccountNumber, "Renamed"
	sp := &customer.SalespersonDraft{}
	policy := &customer.PolicyDraft{Policy: customer.PolicyMode(customer.PolicyRequireAck), ThresholdPercent: 70000}

	steps := map[string]func() error{
		"update": func() error { _, err := bad.Update(ctx, c.ID, d, rev(startRev)); return err },
		"salesperson": func() error {
			_, err := bad.SetSalesperson(ctx, c.ID, sp, rev(startRev))
			return err
		},
		"policy": func() error { _, err := bad.SetEscalationPolicy(ctx, c.ID, policy, rev(startRev)); return err },
		"ship-to create": func() error {
			s := shipDraft("C")
			s.IsDefault, s.DefaultGiven = true, true
			_, err := bad.CreateShipTo(ctx, c.ID, s)
			return err
		},
		"ship-to update": func() error {
			s := shipDraft("B")
			s.IsDefault = true // moves the default away from A: that must roll back too
			_, err := bad.UpdateShipTo(ctx, second.ID, s, rev(second.Revision))
			return err
		},
		"contact create": func() error { _, err := bad.CreateContact(ctx, c.ID, contactDraft("Zed")); return err },
		"contact update": func() error {
			_, err := bad.UpdateContact(ctx, contact.ID, contactDraft("Changed"), rev(contact.Revision))
			return err
		},
		"contact delete": func() error { return bad.DeleteContact(ctx, contact.ID, rev(contact.Revision)) },
	}
	for name, run := range steps {
		if err := run(); err == nil {
			t.Errorf("%s succeeded though its event could not be written", name)
		}
	}

	if got := customerRev(); got != startRev {
		t.Errorf("customer revision = %d after rolled back writes, want %d", got, startRev)
	}
	if n := f.count(`SELECT count(*) FROM customers WHERE id = $1 AND name = 'Renamed'`, c.ID); n != 0 {
		t.Error("the rename survived")
	}
	if n := f.count(`SELECT count(*) FROM customer_ship_tos WHERE customer_id = $1`, c.ID); n != 2 {
		t.Errorf("%d ship-tos, want the 2 made before", n)
	}
	var defaultCode string
	if err := f.db.Pool.QueryRow(ctx, `SELECT code FROM customer_ship_tos WHERE customer_id = $1 AND is_default`, c.ID).Scan(&defaultCode); err != nil || defaultCode != "A" {
		t.Errorf("default ship-to = %q (%v), want A: a rolled back default move must restore it", defaultCode, err)
	}
	if got := f.count(`SELECT revision FROM customer_ship_tos WHERE id = $1`, first.ID); got != 1 {
		t.Errorf("ship-to A revision = %d, want 1", got)
	}
	if n := f.count(`SELECT count(*) FROM customer_contacts WHERE customer_id = $1`, c.ID); n != 1 {
		t.Errorf("%d contacts, want the 1 made before", n)
	}
	if n := f.count(`SELECT count(*) FROM customer_contacts WHERE id = $1 AND first_name = 'Pat' AND revision = 1`, contact.ID); n != 1 {
		t.Error("the contact was changed or deleted")
	}
	if n := f.count(`SELECT count(*) FROM events_outbox WHERE entity_id = $1`, c.ID); n != 4 {
		t.Errorf("%d events for the customer, want the 4 of its good writes", n)
	}
}

// Three contenders at pool size 4 (the lane rule on transactions): creates,
// then three racers on one revision with exactly one winner, then mixed
// writers on distinct customers beside a reader. Every transaction holds one
// connection for its whole length; if any reached for a second from the pool,
// four connections could not serve three contenders and a reader, and the
// deadline would fire.
func TestConcurrency_Pool4ThreeContenders(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newTxFixture(t, db)
	svc := f.good()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const contenders, each = 3, 4

	// 1. Concurrent creates: distinct rows, one event each.
	var mu sync.Mutex
	var created []*customer.Customer
	var wg sync.WaitGroup
	errs := make(chan error, contenders*each)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				c, err := svc.Create(ctx, f.draft())
				if err != nil {
					errs <- err
					return
				}
				mu.Lock()
				created = append(created, c)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent create: %v", err)
	}
	if len(created) != contenders*each {
		t.Fatalf("%d customers created, want %d", len(created), contenders*each)
	}
	if n := f.count(`SELECT count(*) FROM events_outbox WHERE type = 'customer.created' AND entity_id IN (SELECT id FROM customers WHERE account_number LIKE $1)`, f.prefix+"%"); n != contenders*each {
		t.Errorf("%d customer.created events for %d customers", n, contenders*each)
	}

	// 2. Three racers, one revision: exactly one wins, the others see 409.
	target := created[0]
	var winners, stale int
	var rmu sync.Mutex
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := f.draft()
			d.AccountNumber = target.AccountNumber
			_, err := svc.Update(ctx, target.ID, d, rev(1))
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
	if n := f.count(`SELECT count(*) FROM events_outbox WHERE entity_id = $1 AND type = 'customer.updated'`, target.ID); n != 1 {
		t.Errorf("%d customer.updated events for the raced customer, want 1", n)
	}

	// 3. Mixed writers on distinct customers while a reader pages the list.
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
			if _, _, _, err := svc.ListCustomers(ctx, customer.ListFilter{Limit: 5, Query: f.prefix}, true); err != nil {
				readerDone <- err
				return
			}
		}
	}()
	for i := 1; i <= contenders; i++ {
		c := created[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			ship, err := svc.CreateShipTo(ctx, c.ID, shipDraft("S"))
			if err != nil {
				t.Errorf("ship-to: %v", err)
				return
			}
			if _, err := svc.UpdateShipTo(ctx, ship.ID, shipDraft("S"), rev(ship.Revision)); err != nil {
				t.Errorf("ship-to update: %v", err)
			}
			contact, err := svc.CreateContact(ctx, c.ID, contactDraft("Pat"))
			if err != nil {
				t.Errorf("contact: %v", err)
				return
			}
			if err := svc.DeleteContact(ctx, contact.ID, rev(contact.Revision)); err != nil {
				t.Errorf("contact delete: %v", err)
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
// waiting for a fifth that never frees, and the deadline would fire.
func TestConcurrency_Pool4SaturationNeedsNoSecondConnection(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newTxFixture(t, db)
	events := outbox.NewWriter(db, "")
	plain := f.service(events, db)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	const contenders = 4
	var seed []*customer.Customer
	var ships []*customer.ShipTo
	var contacts []*customer.Contact
	var terms []*customer.PaymentTerms
	for i := 0; i < 3*contenders; i++ {
		c, err := plain.Create(ctx, f.draft())
		if err != nil {
			t.Fatal(err)
		}
		seed = append(seed, c)
	}
	for i := 0; i < contenders; i++ {
		s, err := plain.CreateShipTo(ctx, seed[i].ID, shipDraft("S"))
		if err != nil {
			t.Fatal(err)
		}
		ships = append(ships, s)
		ct, err := plain.CreateContact(ctx, seed[i].ID, contactDraft("Pat"))
		if err != nil {
			t.Fatal(err)
		}
		contacts = append(contacts, ct)
		tm, err := plain.CreateTerms(ctx, termsDraft(fmt.Sprintf("%sT%d", f.prefix, i)))
		if err != nil {
			t.Fatal(err)
		}
		terms = append(terms, tm)
	}

	phase := func(name string, run func(svc *customer.Service, i int) error) {
		t.Helper()
		svc := f.service(events, newGatedTx(db, contenders))
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

	phase("create", func(svc *customer.Service, i int) error {
		_, err := svc.Create(ctx, f.draft())
		return err
	})
	phase("update", func(svc *customer.Service, i int) error {
		d := f.draft()
		d.AccountNumber = seed[contenders+i].AccountNumber
		_, err := svc.Update(ctx, seed[contenders+i].ID, d, rev(1))
		return err
	})
	phase("salesperson", func(svc *customer.Service, i int) error {
		_, err := svc.SetSalesperson(ctx, seed[2*contenders+i].ID, &customer.SalespersonDraft{}, rev(1))
		return err
	})
	phase("escalation policy", func(svc *customer.Service, i int) error {
		_, err := svc.SetEscalationPolicy(ctx, seed[contenders+i].ID,
			&customer.PolicyDraft{Policy: customer.PolicyMode(customer.PolicyRequireAck), ThresholdPercent: 80000}, rev(2))
		return err
	})
	phase("ship-to create", func(svc *customer.Service, i int) error {
		s := shipDraft("N")
		s.IsDefault, s.DefaultGiven = true, true
		_, err := svc.CreateShipTo(ctx, seed[i].ID, s)
		return err
	})
	phase("ship-to update", func(svc *customer.Service, i int) error {
		_, err := svc.UpdateShipTo(ctx, ships[i].ID, shipDraft("S"), rev(ships[i].Revision+1))
		return err
	})
	phase("contact create", func(svc *customer.Service, i int) error {
		_, err := svc.CreateContact(ctx, seed[i].ID, contactDraft("Kim"))
		return err
	})
	phase("contact update", func(svc *customer.Service, i int) error {
		_, err := svc.UpdateContact(ctx, contacts[i].ID, contactDraft("Changed"), rev(contacts[i].Revision))
		return err
	})
	phase("contact delete", func(svc *customer.Service, i int) error {
		return svc.DeleteContact(ctx, contacts[i].ID, rev(contacts[i].Revision+1))
	})
	phase("terms create", func(svc *customer.Service, i int) error {
		_, err := svc.CreateTerms(ctx, termsDraft(fmt.Sprintf("%sN%d", f.prefix, i)))
		return err
	})
	phase("terms update", func(svc *customer.Service, i int) error {
		_, err := svc.UpdateTerms(ctx, terms[i].ID, termsDraft(terms[i].Code), rev(terms[i].Revision))
		return err
	})
}
