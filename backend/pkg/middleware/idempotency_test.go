// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/config"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- test helpers ---------------------------------------------------------

// dbRequired mirrors testutil's GABLE_TEST_REQUIRE_DB contract for the local
// pool builder below: CI sets it so a broken database cannot silently skip.
func dbRequired() bool {
	switch os.Getenv(testutil.RequireDBEnv) {
	case "", "0", "false", "FALSE", "no":
		return false
	default:
		return true
	}
}

func skipUnlessDB(t *testing.T, err error) {
	t.Helper()
	if dbRequired() {
		t.Fatalf("%s is set but the database is unreachable: %v", testutil.RequireDBEnv, err)
	}
	t.Skipf("%s (%v)", testutil.SkipReason, err)
}

// requireDBMaxConns is testutil.RequireDB with a caller-chosen MaxConns, so
// the concurrency proof below can run against a pool capped at 4 exactly as
// the item requires. Every test constructs its own pool; none share one.
func requireDBMaxConns(t *testing.T, maxConns int32) *database.DB {
	t.Helper()

	cfg, err := config.Load()
	if err != nil {
		skipUnlessDB(t, err)
		return nil
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	pc := database.DefaultPoolConfig()
	poolCfg.MaxConns = maxConns
	poolCfg.MinConns = pc.MinConns
	poolCfg.MaxConnLifetime = pc.MaxConnLifetime
	poolCfg.MaxConnIdleTime = pc.MaxConnIdleTime
	poolCfg.HealthCheckPeriod = pc.HealthCheckPeriod
	poolCfg.ConnConfig.ConnectTimeout = 3 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		skipUnlessDB(t, err)
		return nil
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		skipUnlessDB(t, err)
		return nil
	}
	db := &database.DB{Pool: pool}
	t.Cleanup(db.Close)
	return db
}

// newKey returns a fresh idempotency key so tests never see each other's rows.
func newKey() string { return "test-" + uuid.NewString() }

// newPrincipalRequest builds a request whose context carries JWT claims for
// the given subject, the identity the auth middleware would have injected.
func newPrincipalRequest(t *testing.T, method, target, body, subject string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if subject != "" {
		claims := &UserClaims{}
		claims.Subject = subject
		r = r.WithContext(context.WithValue(r.Context(), UserContextKey, claims))
	}
	return r
}

// countingHandler counts calls and echoes a fixed response.
func countingHandler(calls *int32, status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	})
}

// blockingHandler signals start, then waits for release before responding.
// The start channel is the "first request is in progress" gate the 409 and
// concurrency tests need.
func blockingHandler(started chan<- struct{}, release <-chan struct{}, calls *int32, status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		started <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	})
}

// serve runs one request through the middleware chain and returns the
// recorder.
func serve(t *testing.T, mw func(http.Handler) http.Handler, h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	mw(h).ServeHTTP(w, r)
	return w
}

// deleteKeyRows removes this test's rows so a failed assertion cannot leak
// state into the next test even though keys are unique.
func deleteKeyRows(t *testing.T, db *database.DB, principal, key string) {
	t.Helper()
	t.Cleanup(func() {
		_, err := db.Pool.Exec(context.Background(),
			`DELETE FROM idempotency_keys WHERE principal = $1 AND key = $2`, principal, key)
		if err != nil {
			t.Logf("cleanup delete for key %q: %v", key, err)
		}
	})
}

// --- replay after restart -------------------------------------------------

// The behaviour this item exists for: a claim and its stored response live in
// Postgres, so a new middleware instance on the same database (what a process
// restart is) replays the stored response instead of re-running the handler.
func TestIdempotency_ReplayAfterRestart(t *testing.T) {
	db := testutil.RequireDB(t)
	key := newKey()
	subject := uuid.NewString()
	principal := "user:" + subject
	deleteKeyRows(t, db, principal, key)

	var calls int32
	body := `{"quote":"Q-000123"}`
	r1 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", body, subject)
	r1.Header.Set(IdempotencyHeader, key)
	w1 := serve(t, Idempotency(db), countingHandler(&calls, http.StatusCreated, body), r1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first request: status = %d, want %d", w1.Code, http.StatusCreated)
	}
	if calls != 1 {
		t.Fatalf("first request: handler calls = %d, want 1", calls)
	}

	// A second middleware instance on the same database is the restart.
	r2 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", body, subject)
	r2.Header.Set(IdempotencyHeader, key)
	w2 := serve(t, Idempotency(db), countingHandler(&calls, http.StatusCreated, body), r2)

	if w2.Code != http.StatusCreated {
		t.Fatalf("replay after restart: status = %d, want %d", w2.Code, http.StatusCreated)
	}
	if got := w2.Body.String(); got != body {
		t.Fatalf("replay after restart: body = %q, want the stored %q", got, body)
	}
	if got := w2.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("replay after restart: Content-Type = %q, want application/json", got)
	}
	if w2.Header().Get(IdempotencyReplayedHeader) != "true" {
		t.Fatalf("replay after restart: %s header = %q, want true",
			IdempotencyReplayedHeader, w2.Header().Get(IdempotencyReplayedHeader))
	}
	if calls != 1 {
		t.Fatalf("replay after restart: handler calls = %d, want 1 (the stored response, not the handler)", calls)
	}
}

// --- concurrency: pool 4, three contenders, exactly one write --------------

// The claim must make exactly one handler run under contention. The pool is
// capped at 4 and three contenders pile onto the key while the first request
// is inside the handler; each must get a 409 and none may reach the handler.
// The claim statements are short autocommitted statements, never a
// transaction held across the handler, so a pool of 4 cannot deadlock against
// pool-size-minus-one contenders.
func TestIdempotency_ConcurrentSingleWrite(t *testing.T) {
	db := requireDBMaxConns(t, 4)
	key := newKey()
	subject := uuid.NewString()
	principal := "user:" + subject
	deleteKeyRows(t, db, principal, key)

	started := make(chan struct{}, 8)
	release := make(chan struct{})
	var calls int32
	h := blockingHandler(started, release, &calls, http.StatusCreated, `{"ok":true}`)
	mw := Idempotency(db)

	newReq := func() *http.Request {
		r := newPrincipalRequest(t, http.MethodPost, "/api/v1/orders", `{"line":1}`, subject)
		r.Header.Set(IdempotencyHeader, key)
		return r
	}

	// The first request holds the claim inside the handler. firstDone gates
	// every read of firstW and of the table: the completion write happens
	// after ServeHTTP returns, inside the goroutine.
	firstDone := make(chan struct{})
	firstW := httptest.NewRecorder()
	go func() {
		defer close(firstDone)
		mw(h).ServeHTTP(firstW, newReq())
	}()
	<-started

	// Three contenders arrive while the first is in progress.
	const contenders = 3
	results := make(chan *httptest.ResponseRecorder, contenders)
	for i := 0; i < contenders; i++ {
		go func() {
			w := httptest.NewRecorder()
			mw(h).ServeHTTP(w, newReq())
			results <- w
		}()
	}
	for i := 0; i < contenders; i++ {
		w := <-results
		if w.Code != http.StatusConflict {
			t.Errorf("contender %d: status = %d, want %d (body %s)", i, w.Code, http.StatusConflict, w.Body.String())
		}
		var er httputil.ErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &er); err != nil {
			t.Errorf("contender %d: 409 body is not the error envelope: %v", i, err)
		} else if !strings.Contains(strings.ToLower(er.Error.Message), "idempotency") {
			t.Errorf("contender %d: 409 message = %q, want it to name the idempotency key", i, er.Error.Message)
		}
	}

	close(release)
	<-firstDone
	if firstW.Code != http.StatusCreated {
		t.Fatalf("first request: status = %d, want %d", firstW.Code, http.StatusCreated)
	}

	if calls != 1 {
		t.Fatalf("handler calls under contention = %d, want exactly 1", calls)
	}

	// Exactly one claim row exists for the key, in the complete state.
	var rows int
	var state string
	err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*), min(state::text) FROM idempotency_keys WHERE principal = $1 AND key = $2`,
		principal, key).Scan(&rows, &state)
	if err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 1 || state != "complete" {
		t.Fatalf("rows for key = %d state %q, want 1 row in state complete", rows, state)
	}
}

// --- 409 while in progress -------------------------------------------------

func TestIdempotency_InProgressConflict(t *testing.T) {
	db := testutil.RequireDB(t)
	key := newKey()
	subject := uuid.NewString()
	principal := "user:" + subject
	deleteKeyRows(t, db, principal, key)

	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var calls int32
	h := blockingHandler(started, release, &calls, http.StatusCreated, `{"ok":1}`)

	r1 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, subject)
	r1.Header.Set(IdempotencyHeader, key)
	go Idempotency(db)(h).ServeHTTP(httptest.NewRecorder(), r1)
	<-started

	r2 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, subject)
	r2.Header.Set(IdempotencyHeader, key)
	w := serve(t, Idempotency(db), h, r2)
	if w.Code != http.StatusConflict {
		t.Fatalf("second request while in progress: status = %d, want %d", w.Code, http.StatusConflict)
	}
	close(release)
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
}

// --- 422 on fingerprint mismatch -------------------------------------------

// A key reused with a different request is a client bug; it must never be
// answered with the stored response for that other request.
func TestIdempotency_FingerprintMismatch(t *testing.T) {
	db := testutil.RequireDB(t)
	key := newKey()
	subject := uuid.NewString()
	principal := "user:" + subject
	deleteKeyRows(t, db, principal, key)

	var calls int32
	mw := Idempotency(db)

	first := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, subject)
	first.Header.Set(IdempotencyHeader, key)
	if w := serve(t, mw, countingHandler(&calls, http.StatusCreated, `{"id":1}`), first); w.Code != http.StatusCreated {
		t.Fatalf("first request: status = %d", w.Code)
	}

	differentBody := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":2}`, subject)
	differentBody.Header.Set(IdempotencyHeader, key)
	w := serve(t, mw, countingHandler(&calls, http.StatusCreated, `{"id":2}`), differentBody)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("same key different body: status = %d, want %d", w.Code, http.StatusUnprocessableEntity)
	}
	var er httputil.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &er); err != nil {
		t.Fatalf("422 body is not the error envelope: %v", err)
	}
	if !strings.Contains(strings.ToLower(er.Error.Message), "idempotency") {
		t.Fatalf("422 message = %q, want it to name the idempotency key", er.Error.Message)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1 (a mismatch must not reach the handler)", calls)
	}

	// A different path with the same key is a different request too.
	differentPath := newPrincipalRequest(t, http.MethodPost, "/api/v1/orders", `{"a":1}`, subject)
	differentPath.Header.Set(IdempotencyHeader, key)
	if w := serve(t, mw, countingHandler(&calls, http.StatusCreated, `{}`), differentPath); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("same key different path: status = %d, want %d", w.Code, http.StatusUnprocessableEntity)
	}
}

// A different fingerprint is rejected even while the original is still in
// progress: the contender must not wait for a response it must not receive.
func TestIdempotency_FingerprintMismatchWhileInProgress(t *testing.T) {
	db := testutil.RequireDB(t)
	key := newKey()
	subject := uuid.NewString()
	principal := "user:" + subject
	deleteKeyRows(t, db, principal, key)

	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var calls int32
	h := blockingHandler(started, release, &calls, http.StatusCreated, `{"ok":1}`)
	mw := Idempotency(db)

	r1 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, subject)
	r1.Header.Set(IdempotencyHeader, key)
	go mw(h).ServeHTTP(httptest.NewRecorder(), r1)
	<-started

	r2 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":999}`, subject)
	r2.Header.Set(IdempotencyHeader, key)
	if w := serve(t, mw, h, r2); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("different body while in progress: status = %d, want %d", w.Code, http.StatusUnprocessableEntity)
	}
	close(release)
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
}

// --- 5xx releases the claim -------------------------------------------------

func TestIdempotency_ServerErrorReleasesClaim(t *testing.T) {
	db := testutil.RequireDB(t)
	key := newKey()
	subject := uuid.NewString()
	principal := "user:" + subject
	deleteKeyRows(t, db, principal, key)

	var calls int32
	statuses := []int{http.StatusInternalServerError, http.StatusCreated}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := statuses[min(int(atomic.AddInt32(&calls, 1))-1, len(statuses)-1)]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		fmt.Fprint(w, `{"try":`+fmt.Sprint(calls)+`}`)
	})
	mw := Idempotency(db)

	r1 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, subject)
	r1.Header.Set(IdempotencyHeader, key)
	if w := serve(t, mw, h, r1); w.Code != http.StatusInternalServerError {
		t.Fatalf("first request: status = %d, want 500", w.Code)
	}

	r2 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, subject)
	r2.Header.Set(IdempotencyHeader, key)
	if w := serve(t, mw, h, r2); w.Code != http.StatusCreated {
		t.Fatalf("retry after 5xx: status = %d, want %d (the claim must be released so the retry runs)", w.Code, http.StatusCreated)
	}
	if calls != 2 {
		t.Fatalf("handler calls = %d, want 2 (the 5xx released the claim)", calls)
	}

	// Once a 2xx is stored, the next retry replays it.
	r3 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, subject)
	r3.Header.Set(IdempotencyHeader, key)
	if w := serve(t, mw, h, r3); w.Code != http.StatusCreated || w.Header().Get(IdempotencyReplayedHeader) != "true" {
		t.Fatalf("replay after success: status = %d replayed = %q", w.Code, w.Header().Get(IdempotencyReplayedHeader))
	}
	if calls != 2 {
		t.Fatalf("handler calls after replay = %d, want 2", calls)
	}
}

// A 4xx is not a stored outcome either (the in-memory store cached 2xx only),
// so a client that fixes its payload and reuses the key re-runs the handler.
func TestIdempotency_ClientErrorNotCached(t *testing.T) {
	db := testutil.RequireDB(t)
	key := newKey()
	subject := uuid.NewString()
	principal := "user:" + subject
	deleteKeyRows(t, db, principal, key)

	var calls int32
	mw := Idempotency(db)
	r := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, subject)
	r.Header.Set(IdempotencyHeader, key)
	if w := serve(t, mw, countingHandler(&calls, http.StatusBadRequest, `{"error":1}`), r); w.Code != http.StatusBadRequest {
		t.Fatalf("first request: status = %d", w.Code)
	}

	r2 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, subject)
	r2.Header.Set(IdempotencyHeader, key)
	if w := serve(t, mw, countingHandler(&calls, http.StatusBadRequest, `{"error":1}`), r2); w.Code != http.StatusBadRequest {
		t.Fatalf("retry after 4xx: status = %d, want 400 re-run", w.Code)
	}
	if calls != 2 {
		t.Fatalf("handler calls = %d, want 2 (a 4xx is not a stored outcome)", calls)
	}
}

// --- header names -----------------------------------------------------------

// Idempotency-Key is the canonical header; X-Idempotency-Key remains an alias
// that addresses the same claim, so an existing client keeps working.
func TestIdempotency_HeaderAlias(t *testing.T) {
	db := testutil.RequireDB(t)
	key := newKey()
	legacyKey := newKey()
	subject := uuid.NewString()
	principal := "user:" + subject
	deleteKeyRows(t, db, principal, key)
	deleteKeyRows(t, db, principal, legacyKey)

	var calls int32
	mw := Idempotency(db)

	// Claim through the legacy alias.
	r1 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, subject)
	r1.Header.Set(LegacyIdempotencyHeader, legacyKey)
	if w := serve(t, mw, countingHandler(&calls, http.StatusCreated, `{"legacy":1}`), r1); w.Code != http.StatusCreated {
		t.Fatalf("legacy header first request: status = %d", w.Code)
	}
	// Replay through the canonical name: the same claim.
	r2 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, subject)
	r2.Header.Set(IdempotencyHeader, legacyKey)
	if w := serve(t, mw, countingHandler(&calls, http.StatusCreated, `{"legacy":1}`), r2); w.Code != http.StatusCreated ||
		w.Header().Get(IdempotencyReplayedHeader) != "true" {
		t.Fatalf("canonical header on a legacy claim: status = %d replayed = %q", w.Code, w.Header().Get(IdempotencyReplayedHeader))
	}

	// With both headers present the canonical one wins.
	r3 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, subject)
	r3.Header.Set(IdempotencyHeader, key)
	r3.Header.Set(LegacyIdempotencyHeader, "should-be-ignored")
	if w := serve(t, mw, countingHandler(&calls, http.StatusCreated, `{"canon":1}`), r3); w.Code != http.StatusCreated {
		t.Fatalf("canonical + legacy first request: status = %d", w.Code)
	}
	var legacyIgnored int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM idempotency_keys WHERE principal = $1 AND key = $2`,
		principal, "should-be-ignored").Scan(&legacyIgnored); err != nil {
		t.Fatalf("count legacy-only rows: %v", err)
	}
	if legacyIgnored != 0 {
		t.Fatalf("rows claimed under the ignored legacy header = %d, want 0", legacyIgnored)
	}
	r4 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, subject)
	r4.Header.Set(IdempotencyHeader, key)
	r4.Header.Set(LegacyIdempotencyHeader, "should-be-ignored")
	if w := serve(t, mw, countingHandler(&calls, http.StatusCreated, `{"canon":1}`), r4); w.Code != http.StatusCreated ||
		w.Header().Get(IdempotencyReplayedHeader) != "true" {
		t.Fatalf("canonical precedence replay: status = %d replayed = %q", w.Code, w.Header().Get(IdempotencyReplayedHeader))
	}
}

// --- scope parity with the in-memory middleware ------------------------------

func TestIdempotency_ScopeParity(t *testing.T) {
	db := testutil.RequireDB(t)
	key := newKey()
	subject := uuid.NewString()
	principal := "user:" + subject
	deleteKeyRows(t, db, principal, key)

	var calls int32
	mw := Idempotency(db)

	// Only POST and PUT participate: a keyed GET is a pass-through.
	getReq := newPrincipalRequest(t, http.MethodGet, "/api/v1/quotes", "", subject)
	getReq.Header.Set(IdempotencyHeader, key)
	if w := serve(t, mw, countingHandler(&calls, http.StatusOK, `[]`), getReq); w.Code != http.StatusOK {
		t.Fatalf("keyed GET: status = %d, want pass-through 200", w.Code)
	}
	var none int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM idempotency_keys WHERE principal = $1 AND key = $2`, principal, key).Scan(&none); err != nil {
		t.Fatalf("count after GET: %v", err)
	}
	if none != 0 {
		t.Fatalf("rows after a keyed GET = %d, want 0 (GETs are not claimed)", none)
	}

	// A POST without a key passes through uncached.
	noKeyReq := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, subject)
	if w := serve(t, mw, countingHandler(&calls, http.StatusCreated, `{}`), noKeyReq); w.Code != http.StatusCreated {
		t.Fatalf("POST without a key: status = %d, want pass-through", w.Code)
	}

	// The handler must see the exact body the fingerprint was computed from.
	var seenBody string
	bodyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		b, _ := io.ReadAll(r.Body)
		seenBody = string(b)
		w.WriteHeader(http.StatusCreated)
	})
	bodyReq := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"payload":"exact"}`, subject)
	bodyReq.Header.Set(IdempotencyHeader, key)
	if w := serve(t, mw, bodyHandler, bodyReq); w.Code != http.StatusCreated {
		t.Fatalf("body POST: status = %d", w.Code)
	}
	if seenBody != `{"payload":"exact"}` {
		t.Fatalf("handler saw body %q, want the original body", seenBody)
	}
}

// One principal's key never replays another's response: the claim is keyed on
// the caller.
func TestIdempotency_PrincipalScoping(t *testing.T) {
	db := testutil.RequireDB(t)
	key := newKey()
	s1 := uuid.NewString()
	s2 := uuid.NewString()
	deleteKeyRows(t, db, "user:"+s1, key)
	deleteKeyRows(t, db, "user:"+s2, key)

	var calls int32
	h := countingHandler(&calls, http.StatusCreated, `{"owner":"first"}`)
	mw := Idempotency(db)

	r1 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, s1)
	r1.Header.Set(IdempotencyHeader, key)
	if w := serve(t, mw, h, r1); w.Code != http.StatusCreated {
		t.Fatalf("first principal: status = %d", w.Code)
	}

	// Same key, same body, different caller: it is a different claim.
	r2 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, s2)
	r2.Header.Set(IdempotencyHeader, key)
	w := serve(t, mw, h, r2)
	if w.Code != http.StatusCreated {
		t.Fatalf("second principal: status = %d, want 201 (not a replay of another caller's response)", w.Code)
	}
	if w.Header().Get(IdempotencyReplayedHeader) == "true" {
		t.Fatalf("second principal: response was replayed across principals")
	}
	if calls != 2 {
		t.Fatalf("handler calls = %d, want 2 (each principal runs its own request)", calls)
	}
}

// Under AUTH_MODE=dev the caller is the fixed dev principal; without an
// identity and without dev mode, keyed requests pass through uncached rather
// than joining a shared anonymous namespace.
func TestIdempotency_DevAndAnonymousPrincipals(t *testing.T) {
	db := testutil.RequireDB(t)

	t.Setenv("AUTH_MODE", "dev")
	key := newKey()
	deleteKeyRows(t, db, "dev", key)

	var calls int32
	mw := Idempotency(db)
	h := countingHandler(&calls, http.StatusCreated, `{"dev":true}`)

	r1 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, "")
	r1.Header.Set(IdempotencyHeader, key)
	if w := serve(t, mw, h, r1); w.Code != http.StatusCreated {
		t.Fatalf("dev first request: status = %d", w.Code)
	}
	r2 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, "")
	r2.Header.Set(IdempotencyHeader, key)
	if w := serve(t, mw, h, r2); w.Code != http.StatusCreated || w.Header().Get(IdempotencyReplayedHeader) != "true" {
		t.Fatalf("dev replay: status = %d replayed = %q", w.Code, w.Header().Get(IdempotencyReplayedHeader))
	}
	if calls != 1 {
		t.Fatalf("dev replay: handler calls = %d, want 1", calls)
	}

	// Outside dev mode, an anonymous keyed request is never cached: there is
	// no principal to scope the claim to.
	t.Setenv("AUTH_MODE", "")
	anonKey := newKey()
	deleteKeyRows(t, db, "dev", anonKey)
	var anonCalls int32
	anonHandler := countingHandler(&anonCalls, http.StatusCreated, `{"anon":true}`)
	for i := 0; i < 2; i++ {
		r := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, "")
		r.Header.Set(IdempotencyHeader, anonKey)
		if w := serve(t, mw, anonHandler, r); w.Code != http.StatusCreated {
			t.Fatalf("anonymous request %d: status = %d, want pass-through 201", i, w.Code)
		}
	}
	if anonCalls != 2 {
		t.Fatalf("anonymous requests: handler calls = %d, want 2 (no caching without a principal)", anonCalls)
	}
	var rows int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM idempotency_keys WHERE key = $1`, anonKey).Scan(&rows); err != nil {
		t.Fatalf("count anonymous rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("rows claimed anonymously = %d, want 0", rows)
	}
}

// If the database is unreachable the middleware fails open: keyed requests
// still reach the handler, uncached. Idempotency is a safety layer, not an
// availability gate.
func TestIdempotency_FailOpenWhenDBDown(t *testing.T) {
	db := requireDBMaxConns(t, 2)
	db.Close() // this test's own pool, not a shared one

	var calls int32
	h := countingHandler(&calls, http.StatusCreated, `{"fail":1}`)
	mw := Idempotency(db)
	r := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, "user-failopen")
	r.Header.Set(IdempotencyHeader, newKey())
	w := serve(t, mw, h, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("request with a dead database: status = %d, want pass-through 201", w.Code)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
	if w.Header().Get(IdempotencyReplayedHeader) == "true" {
		t.Fatalf("a dead database must not produce a replay marker")
	}
}

// A body over the request-size limit is rejected before a claim is made; the
// middleware reads the body through MaxRequestSize's limit, so the stack's own
// 10 MB cap is the cap a keyed request meets.
func TestIdempotency_OversizedBodyRejected(t *testing.T) {
	db := testutil.RequireDB(t)
	key := newKey()
	subject := uuid.NewString()
	principal := "user:" + subject
	deleteKeyRows(t, db, principal, key)

	var calls int32
	r := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", "", subject)
	r.Body = io.NopCloser(bytes.NewReader(make([]byte, 11<<20)))
	r.ContentLength = 11 << 20
	r.Header.Set(IdempotencyHeader, key)

	handler := countingHandler(&calls, http.StatusCreated, `{}`)
	w := httptest.NewRecorder()
	MaxRequestSize(10<<20)(Idempotency(db)(handler)).ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
	if calls != 0 {
		t.Fatalf("handler calls = %d, want 0", calls)
	}
}

// --- the query string is part of the request ----------------------------------

// A key binds to one request, and two requests that differ only in their
// query string are different requests: routes here use query flags (dry runs,
// force flags), so a key reused with a different query string must not replay
// the first response. Two spellings of the SAME query, in different orders,
// are the same request and do replay.
func TestIdempotency_QueryStringInFingerprint(t *testing.T) {
	db := testutil.RequireDB(t)
	key := newKey()
	reorderKey := newKey()
	subject := uuid.NewString()
	principal := "user:" + subject
	deleteKeyRows(t, db, principal, key)
	deleteKeyRows(t, db, principal, reorderKey)

	var calls int32
	mw := Idempotency(db)
	h := countingHandler(&calls, http.StatusCreated, `{"q":true}`)

	first := newPrincipalRequest(t, http.MethodPost, "/api/v1/orders?dry_run=true", `{"line":1}`, subject)
	first.Header.Set(IdempotencyHeader, key)
	if w := serve(t, mw, h, first); w.Code != http.StatusCreated {
		t.Fatalf("dry_run=true: status = %d, want 201", w.Code)
	}

	// Same key, same body, different query flag: a different request.
	second := newPrincipalRequest(t, http.MethodPost, "/api/v1/orders?dry_run=false", `{"line":1}`, subject)
	second.Header.Set(IdempotencyHeader, key)
	w := serve(t, mw, h, second)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("same key different query: status = %d, want %d (the query string is part of the request)", w.Code, http.StatusUnprocessableEntity)
	}
	if w.Header().Get(IdempotencyReplayedHeader) == "true" {
		t.Fatalf("same key different query: response was replayed across different query strings")
	}

	// Reordered spellings of one query are the same request: a retry replays.
	r1 := newPrincipalRequest(t, http.MethodPost, "/api/v1/orders?flag=2&other=1", `{"line":1}`, subject)
	r1.Header.Set(IdempotencyHeader, reorderKey)
	if w := serve(t, mw, h, r1); w.Code != http.StatusCreated {
		t.Fatalf("reorder first request: status = %d, want 201", w.Code)
	}
	r2 := newPrincipalRequest(t, http.MethodPost, "/api/v1/orders?other=1&flag=2", `{"line":1}`, subject)
	r2.Header.Set(IdempotencyHeader, reorderKey)
	if w := serve(t, mw, h, r2); w.Code != http.StatusCreated || w.Header().Get(IdempotencyReplayedHeader) != "true" {
		t.Fatalf("reordered query retry: status = %d replayed = %q, want 201 replayed=true", w.Code, w.Header().Get(IdempotencyReplayedHeader))
	}
}

// --- error codes and the wire envelope -----------------------------------------

// The 409 and 422 this layer answers with carry the wire contract's machine
// codes (idempotency_in_progress, idempotency_key_reused) in the standard
// error envelope with a details array and the request id in meta, so clients
// branch on the code rather than the message text.
func TestIdempotency_ErrorCodesAndEnvelope(t *testing.T) {
	db := testutil.RequireDB(t)
	key := newKey()
	subject := uuid.NewString()
	principal := "user:" + subject
	deleteKeyRows(t, db, principal, key)

	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var calls int32
	h := blockingHandler(started, release, &calls, http.StatusCreated, `{"ok":1}`)
	mw := Idempotency(db)

	r1 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, subject)
	r1.Header.Set(IdempotencyHeader, key)
	go mw(h).ServeHTTP(httptest.NewRecorder(), r1)
	<-started

	envelope := func(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var e map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
			t.Fatalf("body %q is not the error envelope: %v", w.Body.String(), err)
		}
		return e
	}
	codeOf := func(t *testing.T, e map[string]any) string {
		t.Helper()
		errObj, ok := e["error"].(map[string]any)
		if !ok {
			t.Fatalf("envelope has no error object: %v", e)
		}
		c, _ := errObj["code"].(string)
		return c
	}

	// 409 while in progress: idempotency_in_progress, details present.
	r2 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, subject)
	r2.Header.Set(IdempotencyHeader, key)
	r2.Header.Set("X-Request-ID", "req-409")
	conflict := serve(t, mw, h, r2)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("in progress: status = %d, want 409", conflict.Code)
	}
	e := envelope(t, conflict)
	if c := codeOf(t, e); c != "idempotency_in_progress" {
		t.Fatalf("409 code = %q, want idempotency_in_progress", c)
	}
	errObj := e["error"].(map[string]any)
	details, ok := errObj["details"].([]any)
	if !ok {
		t.Fatalf("409 envelope has no details array: %v", errObj)
	}
	if len(details) != 0 {
		t.Fatalf("409 details = %v, want an empty array", details)
	}
	meta, ok := e["meta"].(map[string]any)
	if !ok || meta["request_id"] != "req-409" {
		t.Fatalf("409 meta = %v, want request_id req-409", e["meta"])
	}
	close(release)

	// Key reused with a different request: idempotency_key_reused.
	r3 := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":"different"}`, subject)
	r3.Header.Set(IdempotencyHeader, key)
	r3.Header.Set("X-Request-ID", "req-422")
	reused := serve(t, mw, h, r3)
	if reused.Code != http.StatusUnprocessableEntity {
		t.Fatalf("key reused: status = %d, want 422", reused.Code)
	}
	e = envelope(t, reused)
	if c := codeOf(t, e); c != "idempotency_key_reused" {
		t.Fatalf("422 code = %q, want idempotency_key_reused", c)
	}
	errObj = e["error"].(map[string]any)
	if _, ok := errObj["details"].([]any); !ok {
		t.Fatalf("422 envelope has no details array: %v", errObj)
	}
	if meta, ok := e["meta"].(map[string]any); !ok || meta["request_id"] != "req-422" {
		t.Fatalf("422 meta = %v, want request_id req-422", e["meta"])
	}
}

// --- 3xx outcomes are stored, with their Location -------------------------------

// A 2xx or 3xx outcome is stored and replayed; a replay carries the stored
// Location header (a replayed 201 or 302 without it points the client at
// nothing) along with the status, Content-Type and body. Set-Cookie is never
// stored, so it can never be replayed.
func TestIdempotency_RedirectAndLocationReplay(t *testing.T) {
	db := testutil.RequireDB(t)
	key := newKey()
	createdKey := newKey()
	subject := uuid.NewString()
	principal := "user:" + subject
	deleteKeyRows(t, db, principal, key)
	deleteKeyRows(t, db, principal, createdKey)

	mw := Idempotency(db)
	var redirectCalls int32
	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&redirectCalls, 1)
		w.Header().Set("Location", "https://example.test/elsewhere")
		w.WriteHeader(http.StatusFound)
	})
	r1 := newPrincipalRequest(t, http.MethodPost, "/api/v1/orders", `{"go":1}`, subject)
	r1.Header.Set(IdempotencyHeader, key)
	if w := serve(t, mw, redirect, r1); w.Code != http.StatusFound {
		t.Fatalf("redirect first request: status = %d, want 302", w.Code)
	}
	r2 := newPrincipalRequest(t, http.MethodPost, "/api/v1/orders", `{"go":1}`, subject)
	r2.Header.Set(IdempotencyHeader, key)
	w := serve(t, mw, redirect, r2)
	if w.Code != http.StatusFound || w.Header().Get(IdempotencyReplayedHeader) != "true" {
		t.Fatalf("redirect retry: status = %d replayed = %q, want a stored 302 replayed", w.Code, w.Header().Get(IdempotencyReplayedHeader))
	}
	if got := w.Header().Get("Location"); got != "https://example.test/elsewhere" {
		t.Fatalf("redirect retry: Location = %q, want the stored Location", got)
	}
	if redirectCalls != 1 {
		t.Fatalf("redirect handler calls = %d, want 1 (the 302 is a stored outcome)", redirectCalls)
	}

	// A 201 carrying Location replays with it too.
	var createdCalls int32
	created := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&createdCalls, 1)
		w.Header().Set("Location", "/api/v1/orders/42")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":42}`)
	})
	c1 := newPrincipalRequest(t, http.MethodPost, "/api/v1/orders", `{"make":1}`, subject)
	c1.Header.Set(IdempotencyHeader, createdKey)
	if w := serve(t, mw, created, c1); w.Code != http.StatusCreated {
		t.Fatalf("created first request: status = %d, want 201", w.Code)
	}
	c2 := newPrincipalRequest(t, http.MethodPost, "/api/v1/orders", `{"make":1}`, subject)
	c2.Header.Set(IdempotencyHeader, createdKey)
	w = serve(t, mw, created, c2)
	if w.Code != http.StatusCreated || w.Header().Get(IdempotencyReplayedHeader) != "true" {
		t.Fatalf("created retry: status = %d replayed = %q, want a stored 201 replayed", w.Code, w.Header().Get(IdempotencyReplayedHeader))
	}
	if got := w.Header().Get("Location"); got != "/api/v1/orders/42" {
		t.Fatalf("created retry: Location = %q, want the stored Location", got)
	}
	if createdCalls != 1 {
		t.Fatalf("created handler calls = %d, want 1", createdCalls)
	}
}

// --- retention purge ---------------------------------------------------------

func TestIdempotency_PurgeExpired(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()

	// Two expired rows (one complete, one a dead claim) and one live row.
	seed := []struct {
		key     string
		state   string
		expires time.Duration
	}{
		{newKey(), "complete", -time.Hour},
		{newKey(), "in_progress", -time.Hour},
		{newKey(), "complete", time.Hour},
	}
	for _, s := range seed {
		_, err := db.Pool.Exec(ctx,
			`INSERT INTO idempotency_keys (principal, key, fingerprint, state, expires_at)
			 VALUES ('purge-test', $1, 'fp', $2, now() + $3::interval)`,
			s.key, s.state, fmt.Sprintf("%d seconds", int(s.expires.Seconds())))
		if err != nil {
			t.Fatalf("seed row: %v", err)
		}
		t.Cleanup(func() {
			_, _ = db.Pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE principal = 'purge-test' AND key = $1`, s.key)
		})
	}

	// Batch size 1 forces the batching loop: two expired rows, two batches.
	deleted, err := PurgeExpiredIdempotencyKeys(ctx, db, 1)
	if err != nil {
		t.Fatalf("PurgeExpiredIdempotencyKeys: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2", deleted)
	}

	var left int
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM idempotency_keys WHERE principal = 'purge-test'`).Scan(&left); err != nil {
		t.Fatalf("count after purge: %v", err)
	}
	if left != 1 {
		t.Fatalf("rows after purge = %d, want 1 (the live row stays)", left)
	}
	var state string
	if err := db.Pool.QueryRow(ctx,
		`SELECT state FROM idempotency_keys WHERE principal = 'purge-test'`).Scan(&state); err != nil {
		t.Fatalf("surviving row: %v", err)
	}
	if state != "complete" {
		t.Fatalf("surviving row state = %q, want complete (live)", state)
	}

	// A second purge with nothing expired is a no-op.
	deleted, err = PurgeExpiredIdempotencyKeys(ctx, db, 500)
	if err != nil {
		t.Fatalf("second purge: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("second purge deleted = %d, want 0", deleted)
	}
}

// A died-mid-handler claim (in_progress, lease expired) is takeable again: the
// retry runs rather than 409ing forever against a claim nobody holds.
func TestIdempotency_ExpiredClaimIsTakeable(t *testing.T) {
	db := testutil.RequireDB(t)
	key := newKey()
	subject := uuid.NewString()
	principal := "user:" + subject
	deleteKeyRows(t, db, principal, key)
	ctx := context.Background()

	// Simulate a process that died mid-handler: a stale in_progress row.
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO idempotency_keys (principal, key, fingerprint, state, expires_at)
		 VALUES ($1, $2, $3, 'in_progress', now() - interval '1 minute')`,
		principal, key, requestFingerprint(http.MethodPost, "/api/v1/quotes", "", []byte(`{"a":1}`)))
	if err != nil {
		t.Fatalf("seed stale claim: %v", err)
	}

	var calls int32
	mw := Idempotency(db)
	r := newPrincipalRequest(t, http.MethodPost, "/api/v1/quotes", `{"a":1}`, subject)
	r.Header.Set(IdempotencyHeader, key)
	w := serve(t, mw, countingHandler(&calls, http.StatusCreated, `{"retry":true}`), r)
	if w.Code != http.StatusCreated {
		t.Fatalf("retry against an expired claim: status = %d, want 201", w.Code)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
}

// --- scheduler ---------------------------------------------------------------

// fakeIdempotencySettings is a map-backed settings reader so the scheduler's
// unit tests need no Postgres.
type fakeIdempotencySettings struct {
	values map[string]string
}

func (f *fakeIdempotencySettings) Get(_ context.Context, key string) (string, bool, error) {
	v, ok := f.values[key]
	return v, ok, nil
}

func TestIdempotencyScheduler_EnabledByDefault(t *testing.T) {
	s := newIdempotencySchedulerWithSettings(nil, &fakeIdempotencySettings{values: map[string]string{}})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer s.Stop()
	if !s.enabled {
		t.Fatalf("enabled: want true by default (retention runs unless opted out)")
	}
	if entries := len(s.cron.Entries()); entries != 1 {
		t.Fatalf("cron entries: want 1, got %d", entries)
	}
}

func TestIdempotencyScheduler_OptOutAndBadCron(t *testing.T) {
	s := newIdempotencySchedulerWithSettings(nil, &fakeIdempotencySettings{values: map[string]string{
		settingIdempotencyPurgeEnabled: "false",
	}})
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	if s.enabled || len(s.cron.Entries()) != 0 {
		t.Fatalf("opt-out: enabled = %v entries = %d, want false/0", s.enabled, len(s.cron.Entries()))
	}

	bad := newIdempotencySchedulerWithSettings(nil, &fakeIdempotencySettings{values: map[string]string{
		settingIdempotencyPurgeCron: "not a cron",
	}})
	if err := bad.Start(context.Background()); err == nil {
		t.Fatalf("Start() with an invalid cron expression: want error, got nil")
	}
}
