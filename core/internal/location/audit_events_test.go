// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package location_test

// Every location write carries its audit row and its outbox event in the
// same transaction (review r2, N-4): a failing event write, or a failing
// audit write, rolls the write back; a good write records exactly one of
// each. Three concurrent creates at pool size 4 finish (the lane rule on
// transactions).

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/location"
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

func wired(t *testing.T, db *database.DB) *location.Service {
	return location.NewService(location.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithAudit(audit.NewLogger(db))
}

func branchDraft() *location.Location {
	name := "AUD-" + uuid.NewString()[:10]
	return &location.Location{Code: name, Name: &name, Type: location.LocTypeBranch, Path: name, Active: true}
}

func cleanLocation(t *testing.T, db *database.DB, ids ...uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	for _, id := range ids {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id = $1`, id)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_id = $1`, id)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, id)
	}
}

func countRows(t *testing.T, db *database.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestLocationWrites_FailedEventRollsThemBack: with the outbox failing, the
// create, the update and the archive refuse and leave the row exactly as it
// was.
func TestLocationWrites_FailedEventRollsThemBack(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	badEvents := location.NewService(location.NewRepository(db)).
		WithOutbox(failingEvents{}).WithTxRunner(db).WithAudit(audit.NewLogger(db))

	draft := branchDraft()
	if err := badEvents.CreateLocation(ctx, draft); err == nil {
		t.Fatal("the create succeeded though its event could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM locations WHERE code = $1`, draft.Code); n != 0 {
		t.Fatalf("%d locations survived a rolled back create", n)
	}

	good := wired(t, db)
	created := branchDraft()
	if err := good.CreateLocation(ctx, created); err != nil {
		t.Fatal(err)
	}
	defer cleanLocation(t, db, created.ID)

	if err := badEvents.UpdateLocation(ctx, &location.Location{
		ID: created.ID, Code: created.Code, Name: created.Name, Type: created.Type,
		Path: created.Path, Description: created.Description, Active: true,
	}, created.Revision); err == nil {
		t.Fatal("the update succeeded though its event could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM audit_log WHERE entity_type = 'location' AND entity_id = $1`, created.ID); n != 1 {
		t.Fatalf("%d audit rows, want the 1 of the create alone", n)
	}
	if err := badEvents.DeleteLocation(ctx, created.ID, created.Revision); err == nil {
		t.Fatal("the archive succeeded though its event could not be written")
	}
	if active := countRows(t, db, `SELECT count(*) FROM locations WHERE id = $1 AND active`, created.ID); active != 1 {
		t.Fatal("the archive survived a rolled back write")
	}
}

// TestLocationWrites_FailedAuditRollsTheWriteBack: the audit row is in the
// same transaction; its failure refuses the write too.
func TestLocationWrites_FailedAuditRollsTheWriteBack(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	badAudit := location.NewService(location.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithAudit(failingAudit{})

	draft := branchDraft()
	if err := badAudit.CreateLocation(ctx, draft); err == nil {
		t.Fatal("the create succeeded though its audit row could not be written")
	}
	if n := countRows(t, db, `SELECT count(*) FROM locations WHERE code = $1`, draft.Code); n != 0 {
		t.Fatalf("%d locations survived a rolled back create", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM events_outbox WHERE entity_id = $1`, draft.ID); n != 0 {
		t.Fatalf("%d events survived a rolled back create, want 0", n)
	}
}

// TestLocationWrites_RecordAuditAndEvent: a good create, update and archive
// record one audit row and one event each, in the module's vocabulary.
func TestLocationWrites_RecordAuditAndEvent(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := wired(t, db)

	created := branchDraft()
	if err := svc.CreateLocation(ctx, created); err != nil {
		t.Fatal(err)
	}
	defer cleanLocation(t, db, created.ID)
	if err := svc.UpdateLocation(ctx, &location.Location{
		ID: created.ID, Code: created.Code, Name: created.Name, Type: created.Type,
		Path: created.Path, Description: created.Description, Active: true,
	}, created.Revision); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteLocation(ctx, created.ID, created.Revision+1); err != nil {
		t.Fatal(err)
	}

	assertValues := func(query string, want map[string]bool, what string) {
		t.Helper()
		rows, err := db.Pool.Query(ctx, query, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := map[string]bool{}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
			got[v] = true
		}
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", what, got, want)
		}
		for k := range want {
			if !got[k] {
				t.Fatalf("%s: got %v, want %v (missing %q)", what, got, want, k)
			}
		}
	}
	assertValues(`SELECT action FROM audit_log WHERE entity_type = 'location' AND entity_id = $1`,
		map[string]bool{"location.created": true, "location.updated": true, "location.archived": true}, "audit actions")
	assertValues(`SELECT type FROM events_outbox WHERE entity_type = 'location' AND entity_id = $1`,
		map[string]bool{"location.created": true, "location.updated": true, "location.archived": true}, "event types")
}

// TestLocationWrites_Pool4ThreeContenders: three concurrent creates at pool
// size 4 each finish, each with its row and its event (a transaction that
// reached for a second connection would leave the fourth holder waiting).
func TestLocationWrites_Pool4ThreeContenders(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	svc := wired(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	drafts := make([]*location.Location, 3)
	for i := range drafts {
		drafts[i] = branchDraft()
		t.Cleanup(func() { cleanLocation(t, db, drafts[i].ID) })
	}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := range drafts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := svc.CreateLocation(ctx, drafts[i]); err != nil {
				errs <- err
				return
			}
			if n := countRows(t, db, `SELECT count(*) FROM events_outbox WHERE entity_type = 'location' AND entity_id = $1`, drafts[i].ID); n != 1 {
				errs <- errors.New("contender's event missing")
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if ctx.Err() != nil {
		t.Fatal("three creators at pool size 4 did not finish")
	}
}
