// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts

// c5-2a feed proofs (review pr66-r1 P1-2, P1-3; review pr66-r2 P2-3,
// P2-4). The shipped feed tests build their own chain and never drive
// the stream through serve's metrics wrapper, so two P1s hid:
//
//  1. TestFeedFlusherBehindMetrics pins P1-2: serve wraps the response
//     writer in metrics.statusWriter (and any other middleware that
//     does not implement Flush). The shipped stream's type assertion
//     w.(http.Flusher) fails and the stream answers 503 in production.
//     The fix uses http.NewResponseController(w).Flush(), which looks
//     through the wrappers. This test wraps the stream in
//     metrics.HTTPMetrics (the production wrapper) and reads events; if
//     the fix is absent the wrapped writer does not implement Flush
//     and the stream answers 503.
//
//  2. TestFeedBatchFullServesAllRows pins P1-3: with Batch=2 and 5
//     pending rows the stream must serve all 5. The shipped code reads
//     the full batch, then jumps position to page.Head, so the next
//     read starts at the head and skips the rows that landed between
//     the last read and the head. The fix advances to head only when
//     the page was not full and re-reads at once when it was.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/metrics"
	"github.com/google/uuid"
)

// readSSE consumes one SSE stream up to want events or until ctx lapses
// and returns the parsed events (id and data joined by a tab) or the
// non-200 status the server returned.
func readSSE(ctx context.Context, url string, want int) (events []string, statusCode int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-Request-ID", "req-feed-c5-2a")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	statusCode = res.StatusCode
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		return nil, res.StatusCode, fmt.Errorf("stream status %d body=%s", res.StatusCode, string(raw))
	}
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var data bytes.Buffer
	var id string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if data.Len() > 0 {
				events = append(events, id+"|"+data.String())
				if len(events) >= want {
					return events, statusCode, nil
				}
			}
			data.Reset()
		case strings.HasPrefix(line, "id: "):
			id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			data.WriteString(strings.TrimPrefix(line, "data: "))
		}
	}
	return events, statusCode, nil
}

// c5_2aFeedFixture wires a feed through the production metrics
// wrapper. The flusher test wraps the chain in metrics.HTTPMetrics (the
// wrapper the server emits); the cursor test wraps only the inner
// handler so the test can control batching.
type c5_2aFeedFixture struct {
	srv *httptest.Server
	hub *Hub
}

// c5_2aFeedSettings shortens the heartbeat so c5-2a feed tests run
// promptly; matches the fastFeedSettings the wire tests use.
func c5_2aFeedSettings() FeedSettings {
	s := DefaultFeedSettings()
	s.Heartbeat = 40 * time.Millisecond
	s.Poll = 20 * time.Millisecond
	s.WriteTimeout = 750 * time.Millisecond
	s.MaxLifetime = 5 * time.Second
	return s
}

func newC5_2aFeed(t *testing.T, batch int) *c5_2aFeedFixture {
	t.Helper()
	db := testutil.RequireDB(t)
	repo := NewRepository(db)
	hub := NewHub(repo, 20*time.Millisecond, nil)
	t.Cleanup(hub.Stop)
	s := c5_2aFeedSettings()
	s.Batch = batch
	s.Heartbeat = 40 * time.Millisecond
	s.Poll = 20 * time.Millisecond
	s.WriteTimeout = 750 * time.Millisecond
	s.MaxLifetime = 8 * time.Second
	h := NewFeedHandler(nil, repo, hub, s, nil)

	mux := http.NewServeMux()
	mux.Handle("/api/v1/drafts/quotes/feed", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.stream(w, r, EventFilter{Module: "quotes"}, -1)
	}))
	chain := metrics.HTTPMetrics(mux)
	srv := httptest.NewServer(chain)
	t.Cleanup(srv.Close)
	return &c5_2aFeedFixture{srv: srv, hub: hub}
}

// TestFeedFlusherBehindMetrics proves P1-2 (review pr66-r1 P1-2). The
// feed stream is mounted behind metrics.HTTPMetrics (the wrapper serve
// emits). When the feed's stream code type-asserts w.(http.Flusher) the
// assertion fails: the wrapper has no Flush method, so the stream
// answers 503 and writes nothing. With the fix
// (http.NewResponseController(w).Flush()) the stream looks through the
// wrapper, calls Flush, and streams events to the client.
func TestFeedFlusherBehindMetrics(t *testing.T) {
	f := newC5_2aFeed(t, 100)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.srv.URL+"/api/v1/drafts/quotes/feed", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Request-ID", "req-feed-c5-2a")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("stream status = %d body=%s; flusher failed through the metrics wrapper", res.StatusCode, string(raw))
	}

	// Read events off the wire using a scanner; cancel at the first
	// ready event (the minimum the feed sends after the flusher fix).
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	gotEvent := false
	for scanner.Scan() {
		line := scanner.Text()
		if line == "event: ready" {
			gotEvent = true
			cancel()
			break
		}
	}
	if !gotEvent {
		t.Fatalf("stream did not send a ready event before deadline (the flusher fix did not reach the response writer)")
	}
}

// TestFeedBatchFullServesAllRows proves P1-3 (review pr66-r1 P1-3).
// With Batch=2 and 5 pending rows the stream must serve all 5 in
// order. The shipped code reads the full batch, then jumps position to
// page.Head (the high-water mark of the table). The next read starts at
// head, finds nothing, and the rows that landed in the gap are lost.
// The fix advances to head only when the page was not full; when the
// page was full it re-reads at once.
//
// To force the cursor skip, the test inserts all 5 rows atomically in
// one SQL statement after the stream is open, so a single batch read
// sees two of them with a head far past the rest; the next read
// finds nothing only if the cursor jumped to head.
func TestFeedBatchFullServesAllRows(t *testing.T) {
	db := testutil.RequireDB(t)
	repo := NewRepository(db)
	hub := NewHub(repo, 30*time.Millisecond, nil)
	t.Cleanup(hub.Stop)
	s := c5_2aFeedSettings()
	s.Batch = 2
	s.Heartbeat = 40 * time.Millisecond
	s.Poll = 30 * time.Millisecond
	s.WriteTimeout = 750 * time.Millisecond
	s.MaxLifetime = 6 * time.Second
	h := NewFeedHandler(nil, repo, hub, s, nil)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/drafts/quotes/feed", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.stream(w, r, EventFilter{Module: "quotes"}, -1)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var branchID uuid.UUID
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT value::uuid FROM system_settings WHERE key='default_branch_id'`).Scan(&branchID); err != nil {
		t.Fatalf("default branch: %v", err)
	}

	// Open stream FIRST so every subsequent write arrives on it.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/drafts/quotes/feed", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer res.Body.Close()

	// Wait for the stream to read its first (empty) batch and post the
	// ready event with head=current. This guarantees the inserts
	// below all happen AFTER the stream's initial ReadEvents, so the
	// cursor bug can fire on the next read.
	time.Sleep(120 * time.Millisecond)

	// Insert 5 rows atomically in one statement. Sequential inserts
	// in a tight loop let the hub fire and re-read between each, which
	// hides the bug; one statement makes all 5 land before the
	// stream's next read.
	wantDrafts := 5
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO draft_events (position, draft_id, module, branch_id, op, revision, status, actor_kind, at)
		 SELECT nextval('draft_events_position_seq'),
		        gen_random_uuid(), 'quotes', $1, 'created', 1, 'OPEN', 'user', NOW()
		 FROM generate_series(1, $2)`,
		branchID, wantDrafts); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Read events until cancel or wantDrafts drafts arrive.
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var data bytes.Buffer
	deliveredDrafts := []string{}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if ev := data.String(); ev != "" && strings.Contains(ev, `"op"`) {
				deliveredDrafts = append(deliveredDrafts, ev)
				if len(deliveredDrafts) >= wantDrafts {
					cancel()
				}
			}
			data.Reset()
		case strings.HasPrefix(line, "data: "):
			data.WriteString(strings.TrimPrefix(line, "data: "))
		}
	}

	if len(deliveredDrafts) < wantDrafts {
		t.Fatalf("delivered %d draft events, want %d; the cursor jumped to head after the first full batch", len(deliveredDrafts), wantDrafts)
	}
}

// keep imports used.
var _ = sync.Mutex{}
var _ = uuid.Nil
