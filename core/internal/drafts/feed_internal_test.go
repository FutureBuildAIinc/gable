// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts

// The per write deadline (ADR 0007 section 3.4): a client that does not
// drain its socket is closed, because every write to the client sets its own
// deadline through http.ResponseController.SetWriteDeadline and a failed
// write ends the stream. The kernel only blocks a write once the socket's
// buffers are full, and their size is not test controlled (send autotuning
// reaches megabytes), so this proof drives the stream against a writer that
// simulates a full socket: writes block until the deadline the stream set
// lapses, then fail as a kernel write timeout does. A stream that forgot the
// deadline would block forever and fail the bound.

import (
	"context"
	"net/http"
	httptest "net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
)

// stalledWriter is a ResponseWriter whose socket stopped draining: the first
// writes go through, then every write blocks until the deadline the stream
// set through the response controller lapses and fails like a write timeout.
type stalledWriter struct {
	mu       sync.Mutex
	header   http.Header
	deadline time.Time
	wrote    int
	setDl    bool
}

func (s *stalledWriter) Header() http.Header { return s.header }

func (s *stalledWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.wrote++
	n := len(p)
	allow := s.wrote <= 2 // the response head and the ready event
	dl := s.deadline
	set := s.setDl
	s.mu.Unlock()
	if allow {
		return n, nil
	}
	if !set || dl.IsZero() {
		// No deadline was set before the blocked write: block like a full
		// socket with no timeout, which the test's bound catches.
		time.Sleep(5 * time.Second)
		return 0, os.ErrDeadlineExceeded
	}
	if left := time.Until(dl); left > 0 {
		time.Sleep(left)
	}
	return 0, os.ErrDeadlineExceeded
}

func (s *stalledWriter) Flush() {}

func (s *stalledWriter) WriteHeader(int) {}

func (s *stalledWriter) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadline = t
	s.setDl = true
	return nil
}

// TestStreamClosesStalledClientAtWriteDeadline pins section 3.4: the stream
// sets its own write deadline before each write and ends when the write
// fails, instead of holding the connection on a client that never reads.
func TestStreamClosesStalledClientAtWriteDeadline(t *testing.T) {
	db := testutil.RequireDB(t)
	repo := NewRepository(db)
	hub := NewHub(repo, 20*time.Millisecond, nil)
	defer hub.Stop()
	s := DefaultFeedSettings()
	s.Heartbeat = 20 * time.Millisecond
	s.WriteTimeout = 150 * time.Millisecond
	h := NewFeedHandler(nil, repo, hub, s, nil)

	w := &stalledWriter{header: http.Header{}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/drafts/quotes/feed", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.stream(w, req.WithContext(ctx), EventFilter{Module: "quotes"}, -1)
	}()
	// The write timeout is 150ms and the heartbeat wakes the loop every
	// 20ms, so a compliant stream ends well inside the bound; one that
	// blocks without a deadline holds it until the 5s sleep ends.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream is still running against a client that never drains")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.setDl {
		t.Error("the stream wrote without ever setting a write deadline")
	}
}
