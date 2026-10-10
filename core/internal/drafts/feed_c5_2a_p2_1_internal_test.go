// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts

// Internal tests for the two P2-1 fixes that need the unexported
// h.stream method directly: the token exp close and the stalled first
// draft event. The external tests (feed_c5_2a_p2_1_test.go) cover the
// other four P2-1 items.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// TestStreamEndsAtTokenExp pins section 3.3 (pr66-r1 P2-1.3): the
// stream ends at the JWT's exp (the lifetime bound is a backstop, not
// the trigger), and the close carries event: reauth. The shipped
// test only covers the lifetime path; the exp path is never reached
// because the drafts fixture runs in AUTH_MODE=dev, so no JWT claims
// are in the context. This test drives h.stream() directly with a
// context carrying UserClaims whose ExpiresAt is one heartbeat away,
// and asserts the stream ends with reauth well inside the lifetime
// bound. A refactor that drops the ExpiresAt check would let the
// stream run to the lifetime backstop instead.
func TestStreamEndsAtTokenExp(t *testing.T) {
	db := testutil.RequireDB(t)
	repo := NewRepository(db)
	hub := NewHub(repo, 20*time.Millisecond, nil)
	defer hub.Stop()
	settings := DefaultFeedSettings()
	settings.Heartbeat = 30 * time.Millisecond
	settings.WriteTimeout = 200 * time.Millisecond
	settings.MaxLifetime = 30 * time.Second // the backstop, well past the exp
	h := NewFeedHandler(nil, repo, hub, settings, nil)

	// The claims' exp is 200ms from now: the stream must end with
	// event: reauth, not wait for the 30s lifetime backstop.
	exp := time.Now().Add(200 * time.Millisecond)
	claims := &middleware.UserClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "test-sub",
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = context.WithValue(ctx, middleware.UserContextKey, claims)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/drafts/quotes/feed", nil).WithContext(ctx)

	w := &captureWriter{header: http.Header{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.stream(w, req, EventFilter{Module: "quotes"}, -1)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream did not end at the JWT's exp")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.sawReauth {
		t.Errorf("the stream ended without event: reauth; events seen: %v", w.events)
	}
}

// TestStreamClosesStalledClientAtFirstDraftEvent pins section 3.4
// (pr66-r1 P2-1.10): the per-write deadline must apply to event
// writes, not only the heartbeat comment. The shipped test uses a
// stalled writer that allows the response head and the ready event
// and then blocks; the first thing after the ready is a heartbeat
// comment, so the test passes even if the event path forgot the
// deadline. This test creates a draft event for the stream to read
// before opening, so the next write after the ready is a draft
// event; an event path without a deadline blocks the stream past
// the bound.
func TestStreamClosesStalledClientAtFirstDraftEvent(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()

	// One draft event for the stream to pick up: insert a
	// draft_events row directly so the feed's ReadEvents sees it.
	// The feed reads from draft_events, not drafts, so inserting a
	// draft alone does not produce a stream event.
	module := "quotes"
	draftID := uuid.New()
	var branchID uuid.UUID
	if err := db.Pool.QueryRow(ctx,
		`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&branchID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO draft_events (position, draft_id, module, branch_id, op, revision, status, actor_kind, actor_id)
		 VALUES (1, $1, $2, $3, 'created', 1, 'OPEN', 'user', 'test')`,
		draftID, module, branchID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM draft_events WHERE draft_id = $1`, draftID)
	})

	repo := NewRepository(db)
	hub := NewHub(repo, 20*time.Millisecond, nil)
	defer hub.Stop()
	settings := DefaultFeedSettings()
	settings.Heartbeat = 20 * time.Millisecond
	settings.WriteTimeout = 150 * time.Millisecond
	h := NewFeedHandler(nil, repo, hub, settings, nil)

	w := &stalledAtDraftWriter{header: http.Header{}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/drafts/quotes/feed", nil)
	reqCtx, cancelReq := context.WithCancel(ctx)
	defer cancelReq()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// after = 0 so the loop reads from position 0 and finds the
		// draft event. after = -1 would set position = head after the
		// first read, excluding the just-inserted draft.
		h.stream(w, req.WithContext(reqCtx), EventFilter{Module: module}, 0)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream held the event write past the deadline")
	}
}

// captureWriter is a ResponseWriter that records every event the stream
// writes and never blocks.
type captureWriter struct {
	mu        sync.Mutex
	header    http.Header
	events    []string
	sawReauth bool
}

func (c *captureWriter) Header() http.Header { return c.header }
func (c *captureWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	line := string(p)
	if strings.HasPrefix(line, "event: reauth") {
		c.sawReauth = true
	}
	c.events = append(c.events, line)
	return len(p), nil
}
func (c *captureWriter) WriteHeader(int) {}
func (c *captureWriter) Flush()          {}

// stalledAtDraftWriter is the stalledWriter shape for the first-draft
// variant: writes that are NOT a draft event go through (the response
// head, the ready event, the heartbeat comments). The first write
// that contains "event: draft" blocks until the deadline the stream
// set through the response controller lapses, then fails as a kernel
// write timeout does. An event path without a SetWriteDeadline lets
// the write block past the test bound.
type stalledAtDraftWriter struct {
	mu       sync.Mutex
	header   http.Header
	deadline time.Time
	setDl    bool
}

func (s *stalledAtDraftWriter) Header() http.Header { return s.header }
func (s *stalledAtDraftWriter) Write(p []byte) (int, error) {
	body := string(p)
	if !strings.Contains(body, "event: draft") {
		// Head, ready, comment, cursor, reset: all non-draft.
		return len(p), nil
	}
	// First draft event: block until the deadline the stream set.
	s.mu.Lock()
	dl := s.deadline
	set := s.setDl
	s.mu.Unlock()
	if !set || dl.IsZero() {
		// No deadline: block past the test bound.
		time.Sleep(5 * time.Second)
		return 0, http.ErrHandlerTimeout
	}
	if left := time.Until(dl); left > 0 {
		time.Sleep(left)
	}
	return 0, http.ErrHandlerTimeout
}
func (s *stalledAtDraftWriter) Flush()          {}
func (s *stalledAtDraftWriter) WriteHeader(int) {}
func (s *stalledAtDraftWriter) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadline = t
	s.setDl = true
	return nil
}

// Unwrap exposes the writer to http.NewResponseController so the
// controller's SetWriteDeadline reaches the stalled writer
// directly. Without Unwrap, the controller may report
// http.ErrNotSupported for deadline ops on a custom writer.
func (s *stalledAtDraftWriter) Unwrap() http.ResponseWriter { return s }
