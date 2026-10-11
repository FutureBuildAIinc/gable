// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package unit_test

// The transaction proofs of the recipe (step 2) for the catalogue's writes:
// a failing event write rolls the mutation back; three contenders at pool
// size 4 on one revision have exactly one winner; and the gated saturation
// test proves no statement inside the transaction reaches for a second pool
// connection.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/internal/unit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
)

type failingEvents struct{}

func (f *failingEvents) Write(ctx context.Context, ev outbox.Event) error {
	return errors.New("the event write failed")
}

// TestUnitCreateEventRollsBack proves a create whose event write fails
// leaves no row: the event is the transaction's last statement.
func TestUnitCreateEventRollsBack(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	repo := unit.NewRepository(db)
	svc := unit.NewService(repo).WithTxRunner(db).WithOutbox(&failingEvents{})

	name := "Rollback unit"
	draft := &unit.Draft{Code: "RBLBK", Name: &name, Dimension: dimPtr("count")}
	if _, err := svc.Create(ctx, draft); err == nil {
		t.Fatal("the create fails when its event write fails")
	}
	if _, err := svc.GetUnit(ctx, "RBLBK"); err == nil {
		t.Fatal("the rolled back create left no unit")
	}
	// The code is free again: no residue of the failed transaction.
	svcOK := unit.NewService(repo).WithTxRunner(db)
	if _, err := svcOK.Create(ctx, draft); err != nil {
		t.Fatalf("the code is free after the rollback: %v", err)
	}
	_, _ = db.Pool.Exec(ctx, `DELETE FROM units WHERE code = 'RBLBK'`)
}

// TestUnitUpdateEventRollsBack proves the same for the update.
func TestUnitUpdateEventRollsBack(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	repo := unit.NewRepository(db)
	seed := unit.NewService(repo).WithTxRunner(db)
	name := "Rollback update unit"
	if _, err := seed.Create(ctx, &unit.Draft{Code: "RBLUPD", Name: &name, Dimension: dimPtr("count")}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(context.Background(), `DELETE FROM units WHERE code = 'RBLUPD'`) })

	failing := unit.NewService(repo).WithTxRunner(db).WithOutbox(&failingEvents{})
	newName := "A name that must not land"
	if _, err := failing.Update(ctx, "RBLUPD", &unit.Draft{Name: &newName}, "", int64Ptr(1)); err == nil {
		t.Fatal("the update fails when its event write fails")
	}
	got, err := seed.GetUnit(ctx, "RBLUPD")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != name || got.Revision != 1 {
		t.Fatalf("the rolled back update left nothing: name %q revision %d", got.Name, got.Revision)
	}
}

// TestUnitUpdateThreeContenders proves the revision precondition under
// contention: three racers on one unit at one revision, exactly one winner.
func TestUnitUpdateThreeContenders(t *testing.T) {
	db := testutil.RequireDB(t)
	testutil.RequireDBMaxConns(t, 4)
	ctx := context.Background()
	repo := unit.NewRepository(db)
	svc := unit.NewService(repo).WithTxRunner(db)

	name := "Contender unit"
	if _, err := svc.Create(ctx, &unit.Draft{Code: "RACER", Name: &name, Dimension: dimPtr("count")}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(context.Background(), `DELETE FROM units WHERE code = 'RACER'`) })

	var wg sync.WaitGroup
	winners := make(chan int, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			newName := "Contender unit renamed"
			if _, err := svc.Update(ctx, "RACER", &unit.Draft{Name: &newName}, "", int64Ptr(1)); err == nil {
				winners <- i
			}
		}(i)
	}
	wg.Wait()
	close(winners)
	if len(winners) != 1 {
		t.Fatalf("exactly one of three contenders on revision 1 wins, got %d", len(winners))
	}
}

// gatedTx is a TxRunner that makes the first `want` transactions meet inside
// their transactions before any of them runs a statement: each holds its one
// connection while it waits at the gate. With want equal to the pool size
// the pool is then empty, so any statement that goes to the pool instead of
// the transaction blocks forever (the quote module's shape).
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

// TestUnitWritesGatedSaturation is the test that fails when code inside a
// transaction uses the pool: as many concurrent writers as the pool has
// connections, each held inside its transaction at a shared gate until the
// pool is empty, then released to run their statements. A statement that
// reached for a second connection would leave four holders each waiting for
// a fifth that never frees, and the deadline would fire. Both kinds of write
// run: the create and the update.
func TestUnitWritesGatedSaturation(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo := unit.NewRepository(db)

	const contenders = 4
	seed := unit.NewService(repo).WithTxRunner(db)
	name := "Saturation unit"
	if _, err := seed.Create(ctx, &unit.Draft{Code: "SATUR", Name: &name, Dimension: dimPtr("count")}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM units WHERE code IN ('SATUR', 'SATCA', 'SATCB', 'SATCC', 'SATCD')`)
	})

	// phase runs one kind of write through one shared gate: all contenders
	// hold their transaction's connection at the gate, so the pool is empty
	// when the gate opens.
	phase := func(name string, run func(svc *unit.Service, i int) error) {
		t.Helper()
		svc := unit.NewService(repo).WithTxRunner(newGatedTx(db, contenders))
		var wg sync.WaitGroup
		errs := make(chan error, contenders)
		for i := 0; i < contenders; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if err := run(svc, i); err != nil {
					errs <- err
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		n := 0
		for err := range errs {
			t.Logf("%s contender: %v", name, err)
			n++
		}
		if ctx.Err() != nil {
			t.Fatalf("%s: four contenders at pool size 4 did not finish: a transaction waited on a second pool connection (%d errors seen)", name, n)
		}
	}
	phase("create", func(svc *unit.Service, i int) error {
		cname := fmt.Sprintf("SATC%c", rune('A'+i))
		_, err := svc.Create(ctx, &unit.Draft{Code: cname, Name: &cname, Dimension: dimPtr("count")})
		return err
	})
	// The updates all race one unit at one revision, so three of them lose
	// after the gate opens; each still runs its lock and read statements
	// inside its transaction, which is what the test saturates.
	var won int32
	phase("update", func(svc *unit.Service, i int) error {
		newName := "Saturation unit renamed"
		if _, err := svc.Update(ctx, "SATUR", &unit.Draft{Name: &newName}, "", int64Ptr(1)); err == nil {
			atomic.AddInt32(&won, 1)
		}
		return nil
	})
	if won != 1 {
		t.Errorf("exactly one of the four gated update contenders wins, got %d", won)
	}
}

func dimPtr(s string) *unit.Dimension {
	d, ok := unit.ParseDimension(s)
	if !ok {
		panic("bad dimension in test")
	}
	return &d
}

func int64Ptr(v int64) *int64 { return &v }
