// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package techadmin_test

// The transaction proofs of the tech admin module (ADR 0003 section 2 and
// the lane rule on transactions): a mutation, its audit row and its event
// are one fact, and every transaction runs on its own connection, never
// reaching for a second one from the pool. The concurrency test runs the
// whole service at pool size 4 with three contenders, so a pool use inside a
// transaction deadlocks the test instead of passing on a roomy pool.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/techadmin"
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

// staleOK reports whether err is a precondition refusal the race provokes on
// purpose (one anchor row, several racers): the saturation proof is that
// every transaction FINISHES, not that every racer wins.
func staleOK(err error) bool {
	var he *httpx.Error
	return errors.As(err, &he) && (he.Status == http.StatusConflict || he.Status == http.StatusPreconditionRequired)
}

func countRows(t *testing.T, db *database.DB, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := db.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func cleanSettings(t *testing.T, db *database.DB) {
	t.Helper()
	ctx := context.Background()
	_, _ = db.Pool.Exec(ctx, `DELETE FROM system_settings WHERE key IN ('openrouter_api_key','openrouter_base_url','openrouteservice_api_key')`)
	_, _ = db.Pool.Exec(ctx, `DELETE FROM admin_revisions WHERE resource LIKE 'admin.settings.%'`)
	_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'admin_settings'`)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM system_settings WHERE key IN ('openrouter_api_key','openrouter_base_url','openrouteservice_api_key')`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM admin_revisions WHERE resource LIKE 'admin.settings.%'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'admin_settings'`)
	})
}

// RULE (ADR 0003 section 3): a mutation whose event cannot be recorded does
// not happen. A failing outbox rolls the mint back: no key row, no event.
func TestCreateKey_FailedEventWriteRollsBackTheMint(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	svc := techadmin.NewService(techadmin.NewRepository(db)).WithOutbox(failingEvents{}).WithTxRunner(db)
	if _, _, err := svc.GenerateKey(context.Background(), "rollback me", []string{"quotes:read"}); err == nil {
		t.Fatal("GenerateKey succeeded though its event could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM api_keys WHERE name = 'rollback me'`); n != 0 {
		t.Errorf("%d keys survived a rolled back mint", n)
	}
}

// The same rule on the settings save: a failed event write leaves the rows,
// the anchor and the revision exactly as they were.
func TestSaveSettings_FailedEventWriteRollsBackTheSave(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	cleanSettings(t, db)
	svc := techadmin.NewService(techadmin.NewRepository(db)).WithOutbox(failingEvents{}).WithTxRunner(db)
	one := int64(1)
	if _, err := svc.SaveAISettings(context.Background(), "sk-or-x", nil, techadmin.Precondition{Revision: &one}); err == nil {
		t.Fatal("SaveAISettings succeeded though its event could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM system_settings WHERE key = 'openrouter_api_key'`); n != 0 {
		t.Errorf("the api key row survived a rolled back save")
	}
	if n := countRows(t, db, `SELECT count(*) FROM admin_revisions WHERE resource = 'admin.settings.ai'`); n != 0 {
		t.Errorf("the revision anchor survived a rolled back save")
	}
}

// The same rule on the revoke: a failed event write leaves the key unrevoked
// and writes no audit row.
func TestRevokeKey_FailedEventWriteRollsBackTheRevoke(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	good := techadmin.NewService(techadmin.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	_, key, err := good.GenerateKey(context.Background(), "revoke rollback me", []string{"quotes:read"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'api_key' AND entity_id = $1`, key.ID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id = $1`, key.ID)
	})
	svc := techadmin.NewService(techadmin.NewRepository(db)).
		WithOutbox(failingEvents{}).WithTxRunner(db).WithAuditLog(audit.NewLogger(db))
	if _, err := svc.RevokeKey(context.Background(), key.ID); err == nil {
		t.Fatal("RevokeKey succeeded though its event could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM api_keys WHERE id = $1 AND revoked_at IS NOT NULL`, key.ID); n != 0 {
		t.Errorf("the revoke survived a rolled back event write")
	}
	if n := countRows(t, db, `SELECT count(*) FROM audit_log WHERE action = 'key.revoked' AND entity_id = $1`, key.ID); n != 0 {
		t.Errorf("%d key.revoked audit rows survived the rollback", n)
	}
}

// And on the settings delete: a failed event write leaves the override rows
// and the revision anchor exactly as they were.
func TestDeleteSettings_FailedEventWriteRollsBackTheDelete(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	cleanSettings(t, db)
	good := techadmin.NewService(techadmin.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	one := int64(1)
	if _, err := good.SaveAISettings(context.Background(), "sk-or-keep", nil, techadmin.Precondition{Revision: &one}); err != nil {
		t.Fatal(err)
	}
	svc := techadmin.NewService(techadmin.NewRepository(db)).WithOutbox(failingEvents{}).WithTxRunner(db)
	two := int64(2)
	if err := svc.DeleteAISettings(context.Background(), techadmin.Precondition{Revision: &two}); err == nil {
		t.Fatal("DeleteAISettings succeeded though its event could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM system_settings WHERE key = 'openrouter_api_key'`); n != 1 {
		t.Errorf("the api key row did not survive a rolled back delete (count = %d, want 1)", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM admin_revisions WHERE resource = 'admin.settings.ai' AND revision = 2`); n != 1 {
		t.Errorf("the revision anchor moved on a rolled back delete (rows at revision 2 = %d, want 1)", n)
	}
}

// A failed audit write fails the mutation too: the row is part of the act.
func TestCreateKey_FailedAuditWriteFailsTheMint(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	svc := techadmin.NewService(techadmin.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithAuditLog(failingAudit{})
	if _, _, err := svc.GenerateKey(context.Background(), "audit rollback me", []string{}); err == nil {
		t.Fatal("GenerateKey succeeded though its audit row could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM api_keys WHERE name = 'audit rollback me'`); n != 0 {
		t.Errorf("%d keys survived a rolled back mint", n)
	}
}

// Three contenders at pool size 4 (the lane rule on transactions): three
// goroutines mint keys at once, then three race one settings revision, then
// writers mix with a reader. Every transaction holds exactly one connection
// for its whole length; if any reached for a second from the pool, four
// connections could not serve three contenders and a reader, and the
// deadline below would fire. Exactly one racer wins the shared revision.
func TestConcurrency_Pool4ThreeContenders(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	cleanSettings(t, db)
	svc := techadmin.NewService(techadmin.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
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
			_, key, err := svc.GenerateKey(ctx, "contender", []string{})
			if err != nil {
				errs <- err
				return
			}
			mu.Lock()
			ids = append(ids, key.ID)
			mu.Unlock()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent mint: %v", err)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'api_key' AND entity_id = $1`, id)
			_, _ = db.Pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id = $1`, id)
		}
	})
	if n := countRows(t, db, `SELECT count(*) FROM events_outbox WHERE type = 'key.created' AND entity_id = ANY($1)`, ids); n != contenders {
		t.Errorf("%d key.created events for %d keys", n, contenders)
	}

	// The anchor exists before the race (one save), so the racers meet on the
	// FOR UPDATE in LockRevision rather than on the INSERT that creates the
	// row: with the anchor already present, the lock is the only thing that
	// makes the losers see the bumped revision instead of all reading the
	// same one and every racer winning (a lost update).
	one := int64(1)
	if _, err := svc.SaveAISettings(ctx, "sk-or-anchor", nil, techadmin.Precondition{Revision: &one}); err != nil {
		t.Fatal(err)
	}
	anchored, err := svc.GetAISettings(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Three racers, one settings revision: exactly one wins, the others 409.
	var winners, stale int
	var rmu sync.Mutex
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.SaveAISettings(ctx, "sk-or-race", nil, techadmin.Precondition{Revision: &anchored.Revision})
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

	// Writers on distinct revisions while a reader lists keys on the same
	// four connections. The writers race the one routing anchor, so all but
	// the last take the stale refusal the race provokes; the proof is that
	// every one of them finishes on its own connection.
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
			if _, _, _, err := svc.ListKeys(ctx, techadmin.ListFilter{Limit: 5}, false); err != nil {
				readerDone <- err
				return
			}
		}
	}()
	var won int32
	cur, err := svc.GetRoutingSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rev := cur.Revision + int64(i)
			_, err := svc.SaveRoutingSettings(ctx, "ors-race", techadmin.Precondition{Revision: &rev})
			if staleOK(err) {
				return
			}
			if err != nil {
				t.Errorf("routing save at revision %d: %v", rev, err)
				return
			}
			atomic.AddInt32(&won, 1)
		}(i)
	}
	wg.Wait()
	close(stop)
	if err := <-readerDone; err != nil {
		t.Errorf("reader: %v", err)
	}
	if won == 0 {
		t.Error("no writer succeeded on the shared anchor")
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
// reached for a second connection from the pool would leave four holders
// each waiting for a fifth that never frees, and the deadline would fire.
func TestConcurrency_Pool4SaturationNeedsNoSecondConnection(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	cleanSettings(t, db)
	events := outbox.NewWriter(db, "")
	svc := techadmin.NewService(techadmin.NewRepository(db)).WithOutbox(events).WithTxRunner(db)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	seed := func() uuid.UUID {
		_, key, err := svc.GenerateKey(ctx, "saturation", []string{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'api_key' AND entity_id = $1`, key.ID)
			_, _ = db.Pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id = $1`, key.ID)
		})
		return key.ID
	}
	keyIDs := [4]uuid.UUID{}
	for i := range keyIDs {
		keyIDs[i] = seed()
	}
	routing := techadmin.NewService(techadmin.NewRepository(db)).WithOutbox(events).WithTxRunner(db)
	one := int64(1)
	if _, err := routing.SaveRoutingSettings(ctx, "ors-seed", techadmin.Precondition{Revision: &one}); err != nil {
		t.Fatal(err)
	}
	// The routing anchor now sits at revision 2; the settings phases below
	// race one anchor, so exactly one racer wins and the others take the
	// stale refusal the race provokes on purpose.
	anchor := func() int64 {
		var rev int64
		if err := db.Pool.QueryRow(ctx, `SELECT revision FROM admin_revisions WHERE resource = 'admin.settings.routing'`).Scan(&rev); err != nil {
			t.Fatal(err)
		}
		return rev
	}

	phase := func(name string, run func(svc *techadmin.Service, i int) error) {
		t.Helper()
		s := techadmin.NewService(techadmin.NewRepository(db)).WithOutbox(events).WithTxRunner(newGatedTx(db, 4))
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := run(s, i); err != nil && !staleOK(err) {
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
	phase("mint", func(s *techadmin.Service, i int) error {
		_, key, err := s.GenerateKey(ctx, "sat", []string{})
		if err == nil {
			_, _ = db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'api_key' AND entity_id = $1`, key.ID)
			_, _ = db.Pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id = $1`, key.ID)
		}
		return err
	})
	phase("revoke", func(s *techadmin.Service, i int) error {
		_, err := s.RevokeKey(ctx, keyIDs[i])
		return err
	})
	phase("settings save", func(s *techadmin.Service, i int) error {
		rev := anchor()
		_, err := s.SaveRoutingSettings(ctx, "ors-sat", techadmin.Precondition{Revision: &rev})
		return err
	})
	phase("settings delete", func(s *techadmin.Service, i int) error {
		rev := anchor()
		return s.DeleteRoutingSettings(ctx, techadmin.Precondition{Revision: &rev})
	})
}
