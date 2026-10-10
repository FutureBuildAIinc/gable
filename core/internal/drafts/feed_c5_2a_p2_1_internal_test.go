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

	// One draft event for the stream to pick up: insert a draft row
	// directly so the feed's ReadEvents sees it.
	module := "quotes"
	draftID := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO drafts (id, module, payload, revision, status, branch_id, created_by_kind, created_by_id, updated_by_kind, updated_by_id)
		 VALUES ($1, $2, '{}'::jsonb, 1, 'OPEN', (SELECT value::uuid FROM system_settings WHERE key='default_branch_id'), 'user', 'test', 'user', 'test')`,
		draftID, module); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM drafts WHERE id = $1`, draftID)
	})

	repo := NewRepository(db)
	hub := NewHub(repo, 20*time.Millisecond, nil)
	defer hub.Stop()
	settings := DefaultFeedSettings()
	settings.Heartbeat = 20 * time.Millisecond
	settings.WriteTimeout = 150 * time.Millisecond
	h := NewFeedHandler(nil, repo, hub, settings, nil)

	w := &stalledAtThirdWriter{header: http.Header{}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/drafts/quotes/feed", nil)
	reqCtx, cancelReq := context.WithCancel(ctx)
	defer cancelReq()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.stream(w, req.WithContext(reqCtx), EventFilter{Module: module}, -1)
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

// stalledAtThirdWriter is the stalledWriter shape for the first-draft
// variant: writes 1 and 2 (response head + ready event) go through;
// write 3 (the first draft event) blocks until the deadline the stream
// set through the response controller lapses, then fails as a kernel
// write timeout does.
type stalledAtThirdWriter struct {
	mu       sync.Mutex
	header   http.Header
	wrote    int
	deadline time.Time
	setDl    bool
}

func (s *stalledAtThirdWriter) Header() http.Header { return s.header }
func (s *stalledAtThirdWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.wrote++
	n := len(p)
	wrote := s.wrote
	dl := s.deadline
	set := s.setDl
	s.mu.Unlock()
	if wrote <= 2 {
		return n, nil
	}
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
func (s *stalledAtThirdWriter) Flush()               {}
func (s *stalledAtThirdWriter) WriteHeader(int)      {}
func (s *stalledAtThirdWriter) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadline = t
	s.setDl = true
	return nil
}
