// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

func newRow(t *testing.T, id string) Session {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	return Session{
		IDHash: hashID(id), Sub: Subject("p", id), AppwriteUserID: "u-" + id, ClientKind: "web",
		Roles: []string{"sales"}, RolesReadAt: now, CreatedAt: now, IdleSeconds: 3600,
		AbsoluteExpiresAt: now.Add(12 * time.Hour),
	}
}

func TestStoreSessionAtRestHoldsNoPlainID(t *testing.T) {
	db := testutil.RequireDB(t)
	st := NewStore(db)
	ctx := context.Background()
	id, _ := newSessionID()
	row := newRow(t, id)
	if err := st.Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM staff_sessions WHERE id_hash = $1`, row.IDHash) })
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM staff_sessions WHERE id_hash = $1`, []byte(id)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("the plain session id keys a row")
	}
	got, err := st.Get(ctx, hashID(id))
	if err != nil || got.Sub != row.Sub || len(got.Roles) != 1 {
		t.Fatalf("Get = %+v, %v", got, err)
	}
}

// Three contenders at pool size 4 on one session: a role replacement, a
// second replacement and a revoke. Whatever the order, the end state is
// revoked, and a replacement that lost the race never reanimates the row.
func TestStoreConcurrentRevokeAndReplace(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	st := NewStore(db)
	ctx := context.Background()
	for round := 0; round < 20; round++ {
		id, _ := newSessionID()
		row := newRow(t, id)
		if err := st.Create(ctx, row); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			_, _ = st.ReplaceRoles(ctx, row.IDHash, []string{"admin"}, now.Add(time.Second))
		}()
		go func() {
			defer wg.Done()
			_, _ = st.ReplaceRoles(ctx, row.IDHash, []string{"finance"}, now.Add(2*time.Second))
		}()
		go func() { defer wg.Done(); _, _ = st.Revoke(ctx, row.IDHash, "removed", now) }()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatal("three contenders at pool size 4 did not finish")
		}
		got, err := st.Get(ctx, row.IDHash)
		if err != nil {
			t.Fatal(err)
		}
		if got.RevokedAt == nil {
			t.Fatalf("round %d: the revoke was lost", round)
		}
		if again, _ := st.Revoke(ctx, row.IDHash, "second", now); again {
			t.Fatal("a second revoke claimed the row again")
		}
		_, _ = db.Pool.Exec(ctx, `DELETE FROM staff_sessions WHERE id_hash = $1`, row.IDHash)
	}
}

func TestStoreConcurrentCreatesKeepDistinctRows(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	st := NewStore(db)
	ctx := context.Background()
	sub := uuid.New()
	var rows []Session
	for i := 0; i < 3; i++ {
		id, _ := newSessionID()
		r := newRow(t, id)
		r.Sub = sub
		rows = append(rows, r)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM staff_sessions WHERE sub = $1`, sub) })
	var wg sync.WaitGroup
	for _, r := range rows {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := st.Create(ctx, r); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM staff_sessions WHERE sub = $1`, sub).Scan(&n); err != nil || n != 3 {
		t.Fatalf("rows = %d, %v; want 3", n, err)
	}
}

func TestSubjectIsStable(t *testing.T) {
	if Subject("p", "u1") != Subject("p", "u1") {
		t.Fatal("same user, different sub")
	}
	if Subject("p", "u1") == Subject("p", "u2") || Subject("p", "u1") == Subject("q", "u1") {
		t.Fatal("different user or project, same sub")
	}
}

// A replacement that arrives after the revoke changes nothing: the stored
// roles stay what they were and the call reports no change.
func TestStoreReplaceAfterRevokeChangesNothing(t *testing.T) {
	db := testutil.RequireDB(t)
	st := NewStore(db)
	ctx := context.Background()
	id, _ := newSessionID()
	row := newRow(t, id)
	if err := st.Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM staff_sessions WHERE id_hash = $1`, row.IDHash) })
	now := time.Now().UTC()
	if ok, err := st.Revoke(ctx, row.IDHash, "removed", now); !ok || err != nil {
		t.Fatalf("revoke = %v, %v", ok, err)
	}
	if ok, err := st.ReplaceRoles(ctx, row.IDHash, []string{"owner"}, now.Add(time.Minute)); ok || err != nil {
		t.Fatalf("replace after revoke = %v, %v; want false", ok, err)
	}
	got, _ := st.Get(ctx, row.IDHash)
	if len(got.Roles) != 1 || got.Roles[0] != "sales" {
		t.Fatalf("roles after a late replace = %v", got.Roles)
	}
}

func TestStoreSweepRemovesOnlyTheDead(t *testing.T) {
	db := testutil.RequireDB(t)
	st := NewStore(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	live, _ := newSessionID()
	expired, _ := newSessionID()
	old, _ := newSessionID()
	rl, re, ro := newRow(t, live), newRow(t, expired), newRow(t, old)
	re.AbsoluteExpiresAt = now.Add(-time.Hour)
	for _, r := range []Session{rl, re, ro} {
		if err := st.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, r := range []Session{rl, re, ro} {
			_, _ = db.Pool.Exec(ctx, `DELETE FROM staff_sessions WHERE id_hash = $1`, r.IDHash)
		}
	})
	if _, err := st.Revoke(ctx, ro.IDHash, "sign_out", now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Sweep(ctx, now, now.Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(ctx, rl.IDHash); err != nil {
		t.Fatalf("the live session was swept: %v", err)
	}
	for _, r := range []Session{re, ro} {
		if _, err := st.Get(ctx, r.IDHash); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a dead session survived the sweep: %v", err)
		}
	}
}
