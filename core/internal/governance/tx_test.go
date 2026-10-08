// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package governance_test

// The transaction proofs of the governance module (ADR 0003 section 2 and
// the lane rule on transactions): a mutation, its audit row and its event
// are one fact, and every transaction runs on its own connection, never
// reaching for a second one from the pool.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/governance"
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

func countRows(t *testing.T, db *database.DB, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := db.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func goodService(t *testing.T, db *database.DB) *governance.Service {
	t.Helper()
	return governance.NewService(governance.NewRepository(db), governance.NewTemplateAIProvider()).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
}

func mkParsed() *governance.ParsedCreate {
	return &governance.ParsedCreate{
		Title:            "Tx RFC " + uuid.NewString()[:6],
		ProblemStatement: "p",
		ProposedSolution: "s",
	}
}

// RULE (ADR 0003 section 3): a mutation whose event cannot be recorded does
// not happen. A failing outbox rolls the create, the update and the
// transition back: no row, no status move, no event. The number a rolled
// back create minted is abandoned, never reused.
func TestFailedEventWriteRollsBackEveryWrite(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	good := goodService(t, db)
	created, err := good.DraftRFC(ctx, mkParsed())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dropRFC(t, db, created.ID.String()) })

	bad := governance.NewService(governance.NewRepository(db), governance.NewTemplateAIProvider()).
		WithOutbox(failingEvents{}).WithTxRunner(db)
	if _, err := bad.DraftRFC(ctx, mkParsed()); err == nil {
		t.Fatal("DraftRFC succeeded though its event could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM rfcs WHERE title LIKE 'Tx RFC %'`); n != 1 {
		t.Errorf("%d rows survived rolled back creates (the good one alone)", n)
	}

	rev := created.Revision
	title := "rolled back"
	if _, err := bad.UpdateRFC(ctx, created.ID, &governance.ParsedUpdate{Title: &title}, governance.Precondition{Revision: &rev}); err == nil {
		t.Fatal("UpdateRFC succeeded though its event could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM rfcs WHERE id = $1 AND revision <> 1`, created.ID); n != 0 {
		t.Error("the revision moved on a rolled back update")
	}
	if _, err := bad.Transition(ctx, created.ID, governance.RFCStatusReview, governance.Precondition{Revision: &rev}); err == nil {
		t.Fatal("Transition succeeded though its event could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM rfcs WHERE id = $1 AND status <> 'draft'`, created.ID); n != 0 {
		t.Error("the status moved on a rolled back transition")
	}

	// The number a rolled back create minted is not reused.
	next, err := good.DraftRFC(ctx, mkParsed())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dropRFC(t, db, next.ID.String()) })
	if next.Number == created.Number {
		t.Errorf("two RFCs share the number %s", next.Number)
	}
}

// Three contenders at pool size 4 (the lane rule on transactions): three
// goroutines draft RFCs at once, then three race one revision, while a
// reader pages the list. Exactly one racer wins.
func TestConcurrency_Pool4ThreeContenders(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	svc := goodService(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const contenders = 3
	var mu sync.Mutex
	var ids []uuid.UUID
	var wg sync.WaitGroup
	errs := make(chan error, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rfc, err := svc.DraftRFC(ctx, mkParsed())
			if err != nil {
				errs <- err
				return
			}
			mu.Lock()
			ids = append(ids, rfc.ID)
			mu.Unlock()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent create: %v", err)
	}
	for _, id := range ids {
		t.Cleanup(func() { dropRFC(t, db, id.String()) })
	}
	if n := countRows(t, db, `SELECT count(DISTINCT number) FROM rfcs WHERE id = ANY($1)`, ids); n != contenders {
		t.Errorf("%d distinct numbers for %d creates", n, contenders)
	}

	target := ids[0]
	var winners, stale int
	var rmu sync.Mutex
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rev := int64(1)
			_, err := svc.Transition(ctx, target, governance.RFCStatusReview, governance.Precondition{Revision: &rev})
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
			if _, _, _, err := svc.ListRFCs(ctx, governance.ListFilter{Limit: 5}, false); err != nil {
				readerDone <- err
				return
			}
		}
	}()
	for i := 1; i <= contenders; i++ {
		id := ids[i%len(ids)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			cur, err := svc.GetRFC(ctx, id)
			if err != nil {
				t.Errorf("get: %v", err)
				return
			}
			rev := cur.Revision
			if _, err := svc.UpdateRFC(ctx, id, &governance.ParsedUpdate{}, governance.Precondition{Revision: &rev}); err != nil {
				t.Errorf("update: %v", err)
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

// gatedTx meets the first `want` transactions inside their transactions
// before any of them runs a statement.
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
// their transactions at a gate, for each kind of write.
func TestConcurrency_Pool4SaturationNeedsNoSecondConnection(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	plain := goodService(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var seeded []uuid.UUID
	for i := 0; i < 4; i++ {
		rfc, err := plain.DraftRFC(ctx, mkParsed())
		if err != nil {
			t.Fatal(err)
		}
		seeded = append(seeded, rfc.ID)
		t.Cleanup(func() { dropRFC(t, db, rfc.ID.String()) })
	}

	phase := func(name string, run func(svc *governance.Service, i int) error) {
		t.Helper()
		s := governance.NewService(governance.NewRepository(db), governance.NewTemplateAIProvider()).
			WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(newGatedTx(db, 4))
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := run(s, i); err != nil {
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
	phase("create", func(s *governance.Service, i int) error {
		rfc, err := s.DraftRFC(ctx, mkParsed())
		if err == nil {
			t.Cleanup(func() { dropRFC(t, db, rfc.ID.String()) })
		}
		return err
	})
	phase("update", func(s *governance.Service, i int) error {
		rev := int64(1)
		_, err := s.UpdateRFC(ctx, seeded[i], &governance.ParsedUpdate{}, governance.Precondition{Revision: &rev})
		return err
	})
	phase("transition", func(s *governance.Service, i int) error {
		rev := int64(2)
		_, err := s.Transition(ctx, seeded[i], governance.RFCStatusReview, governance.Precondition{Revision: &rev})
		return err
	})
}
