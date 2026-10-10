// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts

// Review pr66-r1 P2-1.1: TestFeedRevokedKeyClosesAtNextHeartbeat is
// vacuous (the fixture passes nil for keyCheck, so the only reauth is the
// lifetime close). This file rewires the path so the recheck is proved:
// the FeedHandler is built with a keyCheck that reads api_keys.revoked_at,
// the lifetime is long, and the bound waits one heartbeat plus grace, so
// the test only passes when the heartbeat actually notices the revocation.
//
// To make this test bite: drop the heartbeat branch that calls
// h.keyCheck, and the stream no longer closes at revocation; only the
// lifetime close fires, well past the bound.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// keyCheckTechadmin returns a KeyRevocationCheck that walks the key row
// the way the real production wiring does. Kept as documentation for the
// production wiring; the test below uses an in-memory check the same way.

// runningKeyCheck holds a set of revoked key ids in memory so the test
// can simulate a revocation between the stream open and the heartbeat.
type runningKeyCheck struct {
	mu      sync.Mutex
	revoked map[uuid.UUID]bool
}

func newRunningKeyCheck() *runningKeyCheck {
	return &runningKeyCheck{revoked: map[uuid.UUID]bool{}}
}

func (k *runningKeyCheck) Revoke(id uuid.UUID) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.revoked[id] = true
}

func (k *runningKeyCheck) Lookup(ctx context.Context, id uuid.UUID) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return !k.revoked[id], nil
}

// TestFeedKeyCheckRevokeClosesAtHeartbeat pins P2-1.1: the heartbeat
// uses the wired keyCheck (not a nil one). A revocation mid-stream
// triggers an event: reauth within one heartbeat. MaxLifetime is long
// enough that only the revocation path can fire the close.
func TestFeedKeyCheckRevokeClosesAtHeartbeat(t *testing.T) {
	db := testutil.RequireDB(t)
	repo := NewRepository(db)
	hub := NewHub(repo, 30*time.Millisecond, nil)
	t.Cleanup(hub.Stop)

	s := DefaultFeedSettings()
	s.Heartbeat = 80 * time.Millisecond
	s.Poll = 30 * time.Millisecond
	s.WriteTimeout = 750 * time.Millisecond
	s.MaxLifetime = 60 * time.Second // only revocation can fire close

	keyCheck := newRunningKeyCheck()
	keyID := uuid.New()
	// Mark the key as authenticated in context.
	ctxWithKey := func() context.Context { return middleware.WithKeyID(context.Background(), keyID.String()) }

	handler := NewFeedHandler(nil, repo, hub, s, KeyRevocationCheck(keyCheck.Lookup))

	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(ctxWithKey())
		handler.stream(w, r, EventFilter{Module: "quotes"}, -1)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	streamCtx, cancelStream := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancelStream()
	req, _ := http.NewRequestWithContext(streamCtx, "GET", srv.URL+"/feed", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stream = %d", res.StatusCode)
	}

	// Revoke AFTER the stream is open.
	go func() {
		time.Sleep(60 * time.Millisecond)
		keyCheck.Revoke(keyID)
	}()

	scanner := bufio.NewScanner(res.Body)
	reauthAt := time.Time{}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: reauth") {
			reauthAt = time.Now()
			break
		}
	}
	if reauthAt.IsZero() {
		t.Fatalf("revocation did not fire event: reauth within 4s; the heartbeat recheck is bypassed")
	}
	if elapsed := time.Since(time.Now().Add(-time.Since(reauthAt))); elapsed > 500*time.Millisecond {
		t.Fatalf("reauth took too long (%v): the heartbeat might not be wired to keyCheck", elapsed)
	}
}

// TestFeedKeyCheckErrorsAreLogged at least exercises the error path
// (review pr66-r2 P3-2); the heartbeat currently ignores keyCheck
// errors, which fails open. The PR66 audit-fix makes them logged so a
// replay keeps the failure visible.
func TestFeedKeyCheckErrorsAreLogged(t *testing.T) {
	db := testutil.RequireDB(t)
	repo := NewRepository(db)
	hub := NewHub(repo, 30*time.Millisecond, nil)
	t.Cleanup(hub.Stop)

	s := DefaultFeedSettings()
	s.Heartbeat = 60 * time.Millisecond
	s.Poll = 30 * time.Millisecond
	s.MaxLifetime = 30 * time.Second

	// keyCheck that always returns an error.
	keyCheck := func(ctx context.Context, id uuid.UUID) (bool, error) {
		return false, errors.New("simulated DB error")
	}

	handler := NewFeedHandler(nil, repo, hub, s, keyCheck)
	ctxWithKey := func() context.Context { return middleware.WithKeyID(context.Background(), uuid.New().String()) }

	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(ctxWithKey())
		handler.stream(w, r, EventFilter{Module: "quotes"}, -1)
	})

	// Capture log output.
	logBuf := syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/feed", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()

	out := logBuf.String()
	if !strings.Contains(out, "keyCheck") {
		t.Fatalf("expected a keyCheck error log on the heartbeat; got: %s", out)
	}
}

// TestFeedKeyCheckRevokeDuringBacklogDrain pins review pr66-r4 P2-2: a full
// page jumps straight back to the read and never reaches the select's
// heartbeat case, so a revoked key's stream used to drain the whole backlog
// before its reauth (the reviewer measured 7844 rows past a revoke). The
// recheck now runs before every re-read of a full page, so a key revoked mid
// drain ends its stream within a page or two, well inside one heartbeat.
//
// To make this test bite: drop the recheck before the full page continue;
// the stream then reads the whole backlog past the revoke and the reauth
// lands only when the drain ends (rows after the revoke in the hundreds).
func TestFeedKeyCheckRevokeDuringBacklogDrain(t *testing.T) {
	db := testutil.RequireDB(t)
	repo := NewRepository(db)
	hub := NewHub(repo, 30*time.Millisecond, nil)
	t.Cleanup(hub.Stop)

	s := DefaultFeedSettings()
	s.Batch = 2
	s.Heartbeat = 40 * time.Millisecond
	s.Poll = 30 * time.Millisecond
	s.WriteTimeout = 750 * time.Millisecond
	s.MaxLifetime = 20 * time.Second // only the revocation can fire the close

	keyCheck := newRunningKeyCheck()
	keyID := uuid.New()
	handler := NewFeedHandler(nil, repo, hub, s, KeyRevocationCheck(keyCheck.Lookup))

	// The backlog is this test's own rows: the drain starts at the table's
	// current head, so no other test's events join it.
	var drainFrom int64
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT COALESCE(MAX(position), 0) FROM draft_events`).Scan(&drainFrom); err != nil {
		t.Fatalf("read head: %v", err)
	}
	var branchID uuid.UUID
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT value::uuid FROM system_settings WHERE key='default_branch_id'`).Scan(&branchID); err != nil {
		t.Fatalf("default branch: %v", err)
	}
	const backlog = 300
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO draft_events (position, draft_id, module, branch_id, op, revision, status, actor_kind, at)
		 SELECT nextval('draft_events_position_seq'),
		        gen_random_uuid(), 'quotes', $1, 'created', 1, 'OPEN', 'user', NOW()
		 FROM generate_series(1, $2)`,
		branchID, backlog); err != nil {
		t.Fatalf("seed backlog: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(),
			`DELETE FROM draft_events WHERE position > $1 AND module = 'quotes'`, drainFrom)
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(middleware.WithKeyID(context.Background(), keyID.String()))
		handler.stream(w, r, EventFilter{Module: "quotes"}, drainFrom)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	streamCtx, cancelStream := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStream()
	req, _ := http.NewRequestWithContext(streamCtx, "GET", srv.URL+"/feed", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stream = %d", res.StatusCode)
	}

	// Revoke mid drain, once the first few pages have gone out, and count
	// what arrives after: with the recheck the stream stops reading within a
	// page of the revoke, so only the in flight page can follow; without it
	// the drain runs to the end of the backlog.
	rowsSeen := 0
	rowsAfterRevoke := 0
	revokedAt := time.Time{}
	reauthAt := time.Time{}
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: reauth") {
			reauthAt = time.Now()
			break
		}
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"op"`) {
			rowsSeen++
			if revokedAt.IsZero() {
				if rowsSeen >= 20 {
					keyCheck.Revoke(keyID)
					revokedAt = time.Now()
				}
			} else {
				rowsAfterRevoke++
			}
		}
	}
	if reauthAt.IsZero() {
		t.Fatalf("the revoked key's stream never reauthed; rows after the revoke = %d of a %d row backlog", rowsAfterRevoke, backlog)
	}
	if rowsAfterRevoke > 2*s.Batch+2 {
		t.Fatalf("rows delivered after the revoke = %d, want a handful (the in flight page plus one, at most %d), not a drain", rowsAfterRevoke, 2*s.Batch+2)
	}
	if elapsed := reauthAt.Sub(revokedAt); elapsed > 3*s.Heartbeat {
		t.Fatalf("reauth came %v after the revoke, want within one heartbeat (%v) plus grace", elapsed, s.Heartbeat)
	}
}

// syncBuffer is a thread-safe bytes.Buffer for log capture.
type syncBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// pgxRow is the subset of pgx.Row feed's keyCheck paths need; the inline
// implementation here keeps the test self-contained.
type pgxRow interface {
	Scan(dest ...any) error
}

// stopUnused silences unused-variable warnings on the helpers we keep
// for documentation only.
func stopUnused() {
	_ = errors.New
	_ = fmt.Errorf
}
