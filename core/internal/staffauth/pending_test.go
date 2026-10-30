// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
)

func filePending(t *testing.T, st *Store, state string, expires time.Duration) Pending {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	p := Pending{StateHash: hashID(state), Method: "google", ClientKind: "web", ExpiresAt: now.Add(expires)}
	if err := st.CreatePending(context.Background(), p, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.db.Pool.Exec(context.Background(), `DELETE FROM pending_sign_ins WHERE state_hash = $1`, p.StateHash)
	})
	return p
}

func TestPendingAtRestHoldsNoPlainState(t *testing.T) {
	db := testutil.RequireDB(t)
	st := NewStore(db)
	state, _ := newSessionID()
	filePending(t, st, state, time.Minute)
	var n int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM pending_sign_ins WHERE state_hash = $1`, []byte(state)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("the plain state keys a row")
	}
}

func TestClaimPendingIsSingleUseAndExpires(t *testing.T) {
	db := testutil.RequireDB(t)
	st := NewStore(db)
	ctx := context.Background()
	state, _ := newSessionID()
	p := filePending(t, st, state, time.Minute)
	now := time.Now().UTC()
	if _, ok, err := st.ClaimPending(ctx, p.StateHash, now); err != nil || !ok {
		t.Fatalf("first claim ok = %v err = %v, want a claim", ok, err)
	}
	if _, ok, _ := st.ClaimPending(ctx, p.StateHash, now); ok {
		t.Fatal("a state claimed twice")
	}
	late, _ := newSessionID()
	q := filePending(t, st, late, time.Minute)
	if _, ok, _ := st.ClaimPending(ctx, q.StateHash, now.Add(2*time.Minute)); ok {
		t.Fatal("a state past its expiry was claimed")
	}
	if _, ok, _ := st.ClaimPending(ctx, hashID("never-filed"), now); ok {
		t.Fatal("a state that was never filed was claimed")
	}
}

// Three contenders at pool size 4 on one state: exactly one claim wins, and
// the stored used flag is true afterward.
func TestClaimPendingConcurrentOneWinner(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	st := NewStore(db)
	ctx := context.Background()
	for round := 0; round < 20; round++ {
		state, _ := newSessionID()
		p := filePending(t, st, state, time.Minute)
		var wins atomic.Int32
		var wg sync.WaitGroup
		wg.Add(3)
		for i := 0; i < 3; i++ {
			go func() {
				defer wg.Done()
				if _, ok, err := st.ClaimPending(ctx, p.StateHash, time.Now().UTC()); err == nil && ok {
					wins.Add(1)
				}
			}()
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("round %d: %d claims won, want exactly 1", round, wins.Load())
		}
		var used bool
		if err := db.Pool.QueryRow(ctx, `SELECT used FROM pending_sign_ins WHERE state_hash = $1`, p.StateHash).Scan(&used); err != nil || !used {
			t.Fatalf("stored used = %v err = %v, want true", used, err)
		}
	}
}

func TestSweepDeletesExpiredPending(t *testing.T) {
	db := testutil.RequireDB(t)
	st := NewStore(db)
	ctx := context.Background()
	state, _ := newSessionID()
	p := filePending(t, st, state, time.Minute)
	if _, err := st.Sweep(ctx, time.Now().UTC().Add(time.Hour), time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM pending_sign_ins WHERE state_hash = $1`, p.StateHash).Scan(&n); err != nil || n != 0 {
		t.Fatalf("expired pending rows left = %d err = %v", n, err)
	}
}
