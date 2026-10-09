// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staff_test

// The transaction proofs of the staff module (ADR 0003 section 2 and the
// lane rule on transactions): a mutation, its audit row and its event are
// one fact, and every transaction runs on its own connection, never
// reaching for a second one from the pool.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/staff"
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

func countRows(t *testing.T, db *database.DB, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := db.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func seedStaff(t *testing.T, db *database.DB) *staff.Service {
	t.Helper()
	return staff.NewService(staff.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
}

func mkCreate(email string) *staff.ParsedCreate {
	return &staff.ParsedCreate{Email: email, FullName: "Tx Staff", Role: "staff", Active: true}
}

// RULE (ADR 0003 section 3): a mutation whose event cannot be recorded does
// not happen. A failing outbox rolls the create, the update and the grant
// back: no row, no revision move, no event.
func TestFailedEventWriteRollsBackEveryWrite(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	good := seedStaff(t, db)
	created, err := good.CreateStaff(ctx, mkCreate("tx-good-"+uuid.NewString()[:8]+"@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dropStaff(t, db, created.ID) })

	bad := staff.NewService(staff.NewRepository(db)).WithOutbox(failingEvents{}).WithTxRunner(db)
	email := "tx-bad-" + uuid.NewString()[:8] + "@example.com"
	if _, err := bad.CreateStaff(ctx, mkCreate(email)); err == nil {
		t.Fatal("CreateStaff succeeded though its event could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM staff WHERE email = $1`, email); n != 0 {
		t.Errorf("%d rows survived a rolled back create", n)
	}

	rev := created.Revision
	role := "yard"
	if _, err := bad.UpdateStaff(ctx, created.ID, &staff.ParsedUpdate{Role: &role}, staff.Precondition{Revision: &rev}); err == nil {
		t.Fatal("UpdateStaff succeeded though its event could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM staff WHERE id = $1 AND role = 'yard'`, created.ID); n != 0 {
		t.Error("the role change survived a rolled back update")
	}
	if n := countRows(t, db, `SELECT count(*) FROM staff WHERE id = $1 AND revision <> 1`, created.ID); n != 0 {
		t.Error("the revision moved on a rolled back update")
	}

	if _, err := bad.GrantModule(ctx, created.ID, "ai_lm", "", staff.Precondition{Revision: &rev}); err == nil {
		t.Fatal("GrantModule succeeded though its event could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM module_grants WHERE staff_id = $1`, created.ID); n != 0 {
		t.Error("the grant survived a rolled back write")
	}

	// The revoke: a grant made for real, then a revoke whose event write
	// fails, leaves the grant in place and the revision unmoved.
	if _, err := good.GrantModule(ctx, created.ID, "ai_lm", "", staff.Precondition{Revision: &rev}); err != nil {
		t.Fatal(err)
	}
	rev++ // the grant moved the document to 2
	if _, err := bad.RevokeModule(ctx, created.ID, "ai_lm", staff.Precondition{Revision: &rev}); err == nil {
		t.Fatal("RevokeModule succeeded though its event could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM module_grants WHERE staff_id = $1 AND module_id = 'ai_lm'`, created.ID); n != 1 {
		t.Error("the grant did not survive a rolled back revoke")
	}
	if n := countRows(t, db, `SELECT count(*) FROM staff WHERE id = $1 AND revision <> 2`, created.ID); n != 0 {
		t.Error("the revision moved on a rolled back revoke")
	}

	// The kill switch: a failed event write leaves the flag and its revision
	// anchor exactly as they were.
	flagRev := int64(1) // a missing anchor row reads as revision 1
	_ = db.Pool.QueryRow(ctx, `SELECT revision FROM admin_revisions WHERE resource = 'admin.modules.ai_lm'`).Scan(&flagRev)
	if _, err := bad.SetModuleEnabled(ctx, "ai_lm", false, staff.Precondition{Revision: &flagRev}); err == nil {
		t.Fatal("SetModuleEnabled succeeded though its event could not be written")
	}
	if n := countRows(t, db,
		`SELECT count(*) FROM system_settings WHERE key = 'modules.ai_lm.enabled' AND value = 'true'`); n != 1 {
		t.Error("the module flag moved on a rolled back toggle")
	}
	if n := countRows(t, db,
		`SELECT count(*) FROM admin_revisions WHERE resource = 'admin.modules.ai_lm' AND revision <> $1`, flagRev); n != 0 {
		t.Error("the module revision anchor moved on a rolled back toggle")
	}
}

// The audit row rides in the transaction with the act: a grant whose audit
// write fails leaves no grant row.
func TestFailedAuditWriteRollsBackTheGrant(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	good := seedStaff(t, db)
	created, err := good.CreateStaff(ctx, mkCreate("tx-audit-"+uuid.NewString()[:8]+"@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dropStaff(t, db, created.ID) })

	rev := created.Revision
	bad := staff.NewService(staff.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithAuditLog(errAudit{})
	if _, err := bad.GrantModule(ctx, created.ID, "ai_lm", "", staff.Precondition{Revision: &rev}); err == nil {
		t.Fatal("GrantModule succeeded though its audit row could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM module_grants WHERE staff_id = $1`, created.ID); n != 0 {
		t.Error("the grant survived a rolled back audit write")
	}
	if n := countRows(t, db, `SELECT count(*) FROM staff WHERE id = $1 AND revision <> 1`, created.ID); n != 0 {
		t.Error("the revision moved on a rolled back audit write")
	}
}

// errAudit is an audit sink whose every write fails.
type errAudit struct{}

func (errAudit) Log(context.Context, audit.Entry) error { return errors.New("audit insert failed") }

func dropStaff(t *testing.T, db *database.DB, id uuid.UUID) {
	t.Helper()
	_, _ = db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'staff' AND entity_id = $1`, id)
	_, _ = db.Pool.Exec(context.Background(), `DELETE FROM module_grants WHERE staff_id = $1`, id)
	_, _ = db.Pool.Exec(context.Background(), `DELETE FROM staff WHERE id = $1`, id)
}

// Three contenders at pool size 4 (the lane rule on transactions): three
// goroutines create staff at once, then three race one revision, while a
// reader pages the list on the same four connections. Exactly one racer
// wins the shared revision.
func TestConcurrency_Pool4ThreeContenders(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	svc := seedStaff(t, db)
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
			st, err := svc.CreateStaff(ctx, mkCreate("tx-race-"+uuid.NewString()[:8]+"@example.com"))
			if err != nil {
				errs <- err
				return
			}
			mu.Lock()
			ids = append(ids, st.ID)
			mu.Unlock()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent create: %v", err)
	}
	for _, id := range ids {
		t.Cleanup(func() { dropStaff(t, db, id) })
	}

	target := ids[0]
	var winners, stale int
	var rmu sync.Mutex
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rev := int64(1)
			_, err := svc.UpdateStaff(ctx, target, &staff.ParsedUpdate{Role: &[]string{"racer"}[0]}, staff.Precondition{Revision: &rev})
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

	// One module flag, the same race (the staff module owns the kill
	// switches): the anchor exists first (one real toggle to the opposite
	// value, so the row and its revision are live), then three racers hold
	// the current revision and exactly one wins. Without the FOR UPDATE in
	// LockModuleRevision every racer reads the same revision from the
	// existing row and all win (a lost update).
	var seeded string
	_ = db.Pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = 'modules.ai_lm.enabled'`).Scan(&seeded)
	flagRev := int64(1)
	_ = db.Pool.QueryRow(ctx, `SELECT revision FROM admin_revisions WHERE resource = 'admin.modules.ai_lm'`).Scan(&flagRev)
	if _, err := svc.SetModuleEnabled(ctx, "ai_lm", seeded != "true", staff.Precondition{Revision: &flagRev}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if seeded == "" {
			_, _ = db.Pool.Exec(context.Background(), `DELETE FROM system_settings WHERE key = 'modules.ai_lm.enabled'`)
		} else {
			_, _ = db.Pool.Exec(context.Background(),
				`INSERT INTO system_settings (key, value, updated_at) VALUES ('modules.ai_lm.enabled', $1, NOW())
				 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, seeded)
		}
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM admin_revisions WHERE resource = 'admin.modules.ai_lm'`)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'module'`)
	})
	mod, err := svc.ModuleByID(ctx, "ai_lm")
	if err != nil {
		t.Fatal(err)
	}
	var flagWins, flagStale int
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.SetModuleEnabled(ctx, "ai_lm", seeded == "true", staff.Precondition{Revision: &mod.Revision})
			rmu.Lock()
			defer rmu.Unlock()
			var he *httpx.Error
			switch {
			case err == nil:
				flagWins++
			case errors.As(err, &he) && he.Status == http.StatusConflict:
				flagStale++
			default:
				t.Errorf("module racer: unexpected error %v", err)
			}
		}()
	}
	wg.Wait()
	if flagWins != 1 || flagStale != contenders-1 {
		t.Errorf("module flag winners=%d stale=%d, want exactly one winner and %d refused", flagWins, flagStale, contenders-1)
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
			if _, _, _, err := svc.ListStaff(ctx, staff.ListFilter{Limit: 5}, false); err != nil {
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
			cur, err := svc.GetStaff(ctx, id)
			if err != nil {
				t.Errorf("get: %v", err)
				return
			}
			rev := cur.Revision
			if _, err := svc.GrantModule(ctx, id, "ai_lm", "", staff.Precondition{Revision: &rev}); err != nil {
				t.Errorf("grant: %v", err)
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
// before any of them runs a statement. With want equal to the pool size the
// pool is then empty, so any statement that goes to the pool instead of the
// transaction blocks forever.
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
	plain := seedStaff(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var seeded []uuid.UUID
	for i := 0; i < 4; i++ {
		st, err := plain.CreateStaff(ctx, mkCreate("tx-sat-"+uuid.NewString()[:8]+"@example.com"))
		if err != nil {
			t.Fatal(err)
		}
		seeded = append(seeded, st.ID)
		t.Cleanup(func() { dropStaff(t, db, st.ID) })
	}

	phase := func(name string, run func(svc *staff.Service, i int) error) {
		t.Helper()
		s := staff.NewService(staff.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(newGatedTx(db, 4))
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
	phase("create", func(s *staff.Service, i int) error {
		st, err := s.CreateStaff(ctx, mkCreate("tx-satc-"+uuid.NewString()[:8]+"@example.com"))
		if err == nil {
			t.Cleanup(func() { dropStaff(t, db, st.ID) })
		}
		return err
	})
	phase("update", func(s *staff.Service, i int) error {
		rev := int64(1)
		_, err := s.UpdateStaff(ctx, seeded[i], &staff.ParsedUpdate{Role: &[]string{"sat"}[0]}, staff.Precondition{Revision: &rev})
		return err
	})
	phase("grant", func(s *staff.Service, i int) error {
		rev := int64(2)
		_, err := s.GrantModule(ctx, seeded[i], "ai_lm", "", staff.Precondition{Revision: &rev})
		return err
	})
	phase("revoke", func(s *staff.Service, i int) error {
		rev := int64(3)
		_, err := s.RevokeModule(ctx, seeded[i], "ai_lm", staff.Precondition{Revision: &rev})
		return err
	})
	phase("module flag", func(s *staff.Service, i int) error {
		rev := int64(i + 1)
		enabled := i%2 == 0
		_, err := s.SetModuleEnabled(ctx, "ai_lm", enabled, staff.Precondition{Revision: &rev})
		var he *httpx.Error
		if errors.As(err, &he) && he.Status == http.StatusConflict {
			return nil // the flag is one anchor: a stale racer is expected
		}
		return err
	})

	// The kill switch is shared state other suites read: put it back.
	_, _ = db.Pool.Exec(context.Background(),
		`INSERT INTO system_settings (key, value, updated_at) VALUES ('modules.ai_lm.enabled', 'true', NOW())
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`)
	_, _ = db.Pool.Exec(context.Background(), `DELETE FROM admin_revisions WHERE resource LIKE 'admin.modules.%'`)
	_, _ = db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'module'`)
}
