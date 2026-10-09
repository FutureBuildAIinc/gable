// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package crm_test

// The transaction proofs of the crm module (ADR 0003 section 2 and the lane
// rule on transactions): a mutation, its audit row and its event are one
// fact, and every transaction runs on its own connection, never reaching for
// a second one from the pool. The concurrency tests run the whole service at
// pool size 4, so a pool use inside a transaction deadlocks the test instead
// of passing on a roomy pool.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/crm"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type failingEvents struct{}

func (failingEvents) Write(context.Context, outbox.Event) error {
	return errors.New("outbox insert failed")
}

type failingAudit struct{}

func (failingAudit) Log(context.Context, audit.Entry) error {
	return errors.New("audit insert failed")
}

type txFixture struct {
	t        *testing.T
	db       *database.DB
	customer uuid.UUID
	prefix   string
	branch   uuid.UUID
}

func newTxFixture(t *testing.T, db *database.DB) *txFixture {
	t.Helper()
	f := &txFixture{t: t, db: db, customer: uuid.New(), prefix: "TXCRM-" + uuid.NewString()[:8]}
	ctx := context.Background()
	if err := db.Pool.QueryRow(ctx,
		`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&f.branch); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, $2, $3, $4)`,
		f.customer, "Tx Crm "+f.customer.String()[:8], f.prefix, f.branch); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, f.customer, f.branch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = db.Pool.Exec(c, `DELETE FROM events_outbox WHERE entity_type = 'activity' AND entity_id IN (SELECT id FROM crm_activities WHERE customer_id = $1)`, f.customer)
		_, _ = db.Pool.Exec(c, `DELETE FROM audit_log WHERE entity_type = 'activity' AND entity_id IN (SELECT id FROM crm_activities WHERE customer_id = $1)`, f.customer)
		_, _ = db.Pool.Exec(c, `DELETE FROM crm_activities WHERE customer_id = $1`, f.customer)
		_, _ = db.Pool.Exec(c, `DELETE FROM customer_branches WHERE customer_id = $1`, f.customer)
		_, _ = db.Pool.Exec(c, `DELETE FROM customers WHERE id = $1`, f.customer)
	})
	return f
}

func (f *txFixture) service(events crm.EventRecorder, tx crm.TxRunner) *crm.Service {
	return crm.NewService(crm.NewRepository(f.db)).
		WithOutbox(events).WithTxRunner(tx).WithAudit(audit.NewLogger(f.db))
}

func (f *txFixture) good() *crm.Service {
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

func rev(n int64) crm.Precondition { return crm.Precondition{Revision: &n} }

func note(text string) *crm.Draft {
	return &crm.Draft{ActivityType: crm.ActivityNote, Description: text}
}

// RULE (ADR 0003 section 3): a mutation whose event cannot be recorded does
// not happen. A failing outbox rolls each kind of write back: no row, no
// audit row, no event.
func TestFailedEventWriteRollsBackEachWrite(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newTxFixture(t, testutil.RequireDB(t))
	svcFail := f.service(failingEvents{}, f.db)
	plain := f.good()
	ctx := context.Background()

	if _, err := svcFail.Create(ctx, f.customer, note("rolled back")); err == nil {
		t.Error("create succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM crm_activities WHERE customer_id = $1`, f.customer); n != 0 {
		t.Errorf("%d activities survived a rolled back create", n)
	}

	created, err := plain.Create(ctx, f.customer, note("to update"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svcFail.Update(ctx, created.ID, note("nope"), rev(1)); err == nil {
		t.Error("update succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM crm_activities WHERE id = $1 AND description = 'nope'`, created.ID); n != 0 {
		t.Error("an update survived a rolled back event")
	}
	if n := f.count(`SELECT revision FROM crm_activities WHERE id = $1`, created.ID); n != 1 {
		t.Errorf("revision moved to %d on a rolled back update", n)
	}

	if err := svcFail.Delete(ctx, created.ID, rev(1)); err == nil {
		t.Error("delete succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM crm_activities WHERE id = $1`, created.ID); n != 1 {
		t.Error("a delete survived a rolled back event")
	}
}

// The same rule for the audit row, for each kind of write: a mutation whose
// audit row cannot be written does not happen.
func TestFailedAuditWriteRollsBackEachWrite(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newTxFixture(t, testutil.RequireDB(t))
	svc := crm.NewService(crm.NewRepository(f.db)).
		WithOutbox(outbox.NewWriter(f.db, "")).WithTxRunner(f.db).WithAudit(failingAudit{})
	if _, err := svc.Create(context.Background(), f.customer, note("no audit")); err == nil {
		t.Error("create succeeded though its audit row could not be written")
	}
	if n := f.count(`SELECT count(*) FROM crm_activities WHERE customer_id = $1`, f.customer); n != 0 {
		t.Errorf("%d activities survived a rolled back create", n)
	}

	created, err := f.good().Create(context.Background(), f.customer, note("to update"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(context.Background(), created.ID, note("nope"), rev(1)); err == nil {
		t.Error("update succeeded though its audit row could not be written")
	}
	if n := f.count(`SELECT count(*) FROM crm_activities WHERE id = $1 AND description = 'nope'`, created.ID); n != 0 {
		t.Error("an update survived a rolled back audit row")
	}
	if n := f.count(`SELECT revision FROM crm_activities WHERE id = $1`, created.ID); n != 1 {
		t.Errorf("revision moved to %d on a rolled back update", n)
	}
	if err := svc.Delete(context.Background(), created.ID, rev(1)); err == nil {
		t.Error("delete succeeded though its audit row could not be written")
	}
	if n := f.count(`SELECT count(*) FROM crm_activities WHERE id = $1`, created.ID); n != 1 {
		t.Error("a delete survived a rolled back audit row")
	}
}

// Three contenders at pool size 4 (the lane rule on transactions): concurrent
// creates get distinct rows and one event each, three racers on one revision
// have exactly one winner, and mixed writers beside a reader finish.
func TestConcurrency_Pool4ThreeContenders(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newTxFixture(t, db)
	svc := f.good()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const contenders, each = 3, 4

	// 1. Concurrent creates: distinct rows, one event each.
	var wg sync.WaitGroup
	errs := make(chan error, contenders*each)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				if _, err := svc.Create(ctx, f.customer, note("contender")); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent create: %v", err)
	}
	if n := f.count(`SELECT count(*) FROM crm_activities WHERE customer_id = $1`, f.customer); n != contenders*each {
		t.Fatalf("%d activities created, want %d", n, contenders*each)
	}
	if n := f.count(`SELECT count(*) FROM events_outbox WHERE type = 'activity.created' AND entity_id IN (SELECT id FROM crm_activities WHERE customer_id = $1)`, f.customer); n != contenders*each {
		t.Errorf("%d activity.created events for %d activities", n, contenders*each)
	}

	// 2. Three racers, one revision: exactly one wins, the others see 409.
	made, err := svc.Create(ctx, f.customer, note("raced"))
	if err != nil {
		t.Fatal(err)
	}
	var won, stale, failed atomic.Int32
	var raceWG sync.WaitGroup
	for i := 0; i < contenders; i++ {
		raceWG.Add(1)
		go func() {
			defer raceWG.Done()
			_, err := svc.Update(ctx, made.ID, note("winner"), rev(1))
			switch {
			case err == nil:
				won.Add(1)
			default:
				if e, ok := err.(*httpx.Error); ok && e.Status == http.StatusConflict {
					stale.Add(1)
				} else {
					f.t.Errorf("racer: %v", err)
					failed.Add(1)
				}
			}
		}()
	}
	raceWG.Wait()
	if won.Load() != 1 || stale.Load() != contenders-1 || failed.Load() != 0 {
		t.Errorf("%d winners, %d stale, %d failed; want 1, %d, 0", won.Load(), stale.Load(), failed.Load(), contenders-1)
	}

	// 3. Mixed writers on distinct rows beside a reader.
	first, err := svc.Create(ctx, f.customer, note("first"))
	if err != nil {
		t.Fatal(err)
	}
	doomed := make([]uuid.UUID, 0, contenders)
	for i := 0; i < contenders; i++ {
		d, err := svc.Create(ctx, f.customer, note("doomed"))
		if err != nil {
			t.Fatal(err)
		}
		doomed = append(doomed, d.ID)
	}
	wg = sync.WaitGroup{}
	mixedErrs := make(chan error, contenders*3)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Three writers race on one revision: the losers' 409 is the
			// contract working, not a failure.
			if _, err := svc.Update(ctx, first.ID, note("mixed"), rev(2)); err != nil {
				if e, ok := err.(*httpx.Error); !ok || e.Status != http.StatusConflict {
					mixedErrs <- err
				}
			}
		}()
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := svc.Delete(ctx, doomed[i], rev(1)); err != nil {
				mixedErrs <- err
			}
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.Get(ctx, first.ID); err != nil {
				mixedErrs <- err
			}
		}()
	}
	wg.Wait()
	close(mixedErrs)
	for err := range mixedErrs {
		t.Fatalf("mixed writers: %v", err)
	}
}

// gatedTx opens as many transactions as the pool has connections and holds
// each INSIDE its transaction at a gate before fn runs. The pool is then
// empty, so any statement that goes to the pool instead of the transaction
// blocks forever. Without the gate the overlap would be luck.
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
	events := outbox.NewWriter(f.db, "")
	plain := f.service(events, f.db)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	const contenders = 4
	var seeds []*crm.Activity
	for i := 0; i < contenders; i++ {
		a, err := plain.Create(ctx, f.customer, note("seed"))
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, a)
	}

	phase := func(name string, run func(svc *crm.Service, i int) error) {
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
	}

	phase("create", func(svc *crm.Service, _ int) error {
		_, err := svc.Create(ctx, f.customer, note("gated"))
		return err
	})
	phase("update", func(svc *crm.Service, i int) error {
		_, err := svc.Update(ctx, seeds[i].ID, note("gated"), rev(1))
		return err
	})
	phase("delete", func(svc *crm.Service, i int) error {
		return svc.Delete(ctx, seeds[i].ID, rev(2))
	})
}
