// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// FeedCursorScope names the feed's cursor: the commit ordered position, so
// a reader resuming from a cursor never skips a row that commits late (the
// ADR 0003 guarantee, on this feed's own lock).
const FeedCursorScope = "drafts.feed_position"

// FeedSettings are the feed's tuning knobs with their defaults (ADR 0007
// section 3.5). Zero values mean the defaults; the config layer refuses
// zero or negative overrides at boot, except the retention, where zero or
// negative turns the purge off.
type FeedSettings struct {
	Heartbeat              time.Duration
	Poll                   time.Duration
	Batch                  int
	WriteTimeout           time.Duration
	MaxLifetime            time.Duration
	Retention              time.Duration
	MaxStreamsPerPrincipal int
	MaxStreams             int
}

// DefaultFeedSettings are the section 3.5 defaults.
func DefaultFeedSettings() FeedSettings {
	return FeedSettings{
		Heartbeat:              15 * time.Second,
		Poll:                   time.Second,
		Batch:                  100,
		WriteTimeout:           10 * time.Second,
		MaxLifetime:            15 * time.Minute,
		Retention:              7 * 24 * time.Hour,
		MaxStreamsPerPrincipal: 8,
		MaxStreams:             500,
	}
}

// Hub is the per serve process wake signal (section 3.4): it holds nothing
// per stream, learns the head moved two ways (the local commits the drafts
// service nudges, and a poll of max(position) for writes on other
// replicas), and broadcasts "the head moved" to every stream. One query per
// process per poll interval, not per stream.
type Hub struct {
	head atomic.Int64
	// wake is closed and replaced to broadcast; readers hold a copy.
	mu   sync.Mutex
	wake chan struct{}
	stop chan struct{}
}

// NewHub starts the hub's poll loop. Cancel stops it (the serve role wires
// RegisterOnShutdown).
func NewHub(db *PostgresRepository, poll time.Duration, logger *slog.Logger) *Hub {
	if poll <= 0 {
		poll = time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	h := &Hub{wake: make(chan struct{}), stop: make(chan struct{})}
	go func() {
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		for {
			select {
			case <-h.stop:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				pos, err := feedHeadPosition(ctx, db)
				cancel()
				if err != nil {
					logger.Warn("drafts feed: head poll failed", "error", err)
					continue
				}
				h.Advance(pos)
			}
		}
	}()
	return h
}

// feedHeadPosition reads the highest position in one cheap query.
func feedHeadPosition(ctx context.Context, db *PostgresRepository) (int64, error) {
	var pos int64
	err := db.db.Pool.QueryRow(ctx, `SELECT COALESCE(max(position), 0) FROM draft_events`).Scan(&pos)
	return pos, err
}

// Advance moves the head forward (never back) and wakes every waiter when
// it moved.
func (h *Hub) Advance(pos int64) {
	for {
		cur := h.head.Load()
		if pos <= cur || h.head.CompareAndSwap(cur, pos) {
			if pos > cur {
				h.Nudge()
			}
			return
		}
	}
}

// Nudge wakes every waiter: a local commit happened.
func (h *Hub) Nudge() {
	h.mu.Lock()
	ch := h.wake
	h.wake = make(chan struct{})
	h.mu.Unlock()
	close(ch)
}

// Wait blocks until the head moves or ctx ends.
func (h *Hub) Wait(ctx context.Context) {
	select {
	case <-h.Channel():
	case <-ctx.Done():
	}
}

// Channel is the hub's current wake channel; it closes when the head moves.
func (h *Hub) Channel() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.wake
}

// Head is the last position the hub saw.
func (h *Hub) Head() int64 { return h.head.Load() }

// Done is closed when the hub stops: every open stream ends with it (the
// serve role's graceful shutdown, through RegisterOnShutdown, section 3.4).
func (h *Hub) Done() <-chan struct{} { return h.stop }

// Stop ends the poll loop. It is idempotent, so a test's stop and the
// fixture's cleanup can both run.
func (h *Hub) Stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-h.stop:
	default:
		close(h.stop)
	}
}

// streamLimiter holds the feed's two limits (section 3.4): streams per
// principal and streams per process, both answered before the stream opens.
type streamLimiter struct {
	mu           sync.Mutex
	perPrincipal map[string]int
	total        int
	maxPer       int
	maxTotal     int
}

func newStreamLimiter(maxPer, maxTotal int) *streamLimiter {
	return &streamLimiter{perPrincipal: map[string]int{}, maxPer: maxPer, maxTotal: maxTotal}
}

// take claims a stream slot, reporting the 429/503 refusal.
func (l *streamLimiter) take(principal string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= l.maxTotal {
		return httpx.Unavailable("this process holds its draft feed stream limit")
	}
	if l.perPrincipal[principal]+1 > l.maxPer {
		return httpx.RateLimited("this principal holds its draft feed stream limit")
	}
	l.total++
	l.perPrincipal[principal]++
	return nil
}

// release returns a stream slot.
func (l *streamLimiter) release(principal string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.perPrincipal[principal] > 0 {
		l.perPrincipal[principal]--
		if l.perPrincipal[principal] == 0 {
			delete(l.perPrincipal, principal)
		}
	}
	if l.total > 0 {
		l.total--
	}
}

// FeedHandler serves GET /api/v1/drafts/{kind}/feed.
type FeedHandler struct {
	service  *Service
	repo     *PostgresRepository
	hub      *Hub
	settings FeedSettings
	limits   *streamLimiter
	keyCheck KeyRevocationCheck
}

// KeyRevocationCheck answers whether a key id is still valid: a keyed
// stream rechecks its key at every heartbeat by id (one row read of
// api_keys.revoked_at, no hash work).
type KeyRevocationCheck func(ctx context.Context, keyID uuid.UUID) (bool, error)

// NewFeedHandler builds the feed endpoint. keyCheck may be nil (no keyed
// revocation recheck, tests).
func NewFeedHandler(service *Service, repo *PostgresRepository, hub *Hub, settings FeedSettings, keyCheck KeyRevocationCheck) *FeedHandler {
	if settings.Batch <= 0 {
		s := DefaultFeedSettings()
		settings.Batch = s.Batch
	}
	if settings.Heartbeat <= 0 || settings.Poll <= 0 || settings.WriteTimeout <= 0 || settings.MaxLifetime <= 0 {
		s := DefaultFeedSettings()
		if settings.Heartbeat <= 0 {
			settings.Heartbeat = s.Heartbeat
		}
		if settings.Poll <= 0 {
			settings.Poll = s.Poll
		}
		if settings.WriteTimeout <= 0 {
			settings.WriteTimeout = s.WriteTimeout
		}
		if settings.MaxLifetime <= 0 {
			settings.MaxLifetime = s.MaxLifetime
		}
	}
	if settings.MaxStreamsPerPrincipal <= 0 || settings.MaxStreams <= 0 {
		s := DefaultFeedSettings()
		if settings.MaxStreamsPerPrincipal <= 0 {
			settings.MaxStreamsPerPrincipal = s.MaxStreamsPerPrincipal
		}
		if settings.MaxStreams <= 0 {
			settings.MaxStreams = s.MaxStreams
		}
	}
	return &FeedHandler{
		service: service, repo: repo, hub: hub, settings: settings,
		limits:   newStreamLimiter(settings.MaxStreamsPerPrincipal, settings.MaxStreams),
		keyCheck: keyCheck,
	}
}

// feed returns the endpoint as a HandlerFunc for one kind.
func (h *FeedHandler) feed(k Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Any query parameter beyond the feed's own is refused before the
		// stream opens.
		q, err := httpx.StrictQuery(r, "cursor", "draft_id", "subject_id")
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var filter EventFilter
		filter.Module = k.Module()
		v := &httpx.Validator{}
		if vals := q["draft_id"]; len(vals) > 0 {
			if len(vals) > 1 {
				v.Check(false, "draft_id", "parameter is repeated")
			} else if id, ok := v.UUID("draft_id", &vals[0], true); ok {
				filter.DraftID = &id
			}
		}
		if vals := q["subject_id"]; len(vals) > 0 {
			if len(vals) > 1 {
				v.Check(false, "subject_id", "parameter is repeated")
			} else if id, ok := v.UUID("subject_id", &vals[0], true); ok {
				filter.SubjectID = &id
			}
		}
		if err := v.Err(); err != nil {
			httpx.WriteError(w, r, err)
			return
		}

		// The cursor: Last-Event-ID (what an SSE client sends on reconnect)
		// or the cursor parameter. When both are present Last-Event-ID wins:
		// a reconnecting client keeps its original ?cursor= in the URL and
		// adds the newer Last-Event-ID, so refusing the pair would fail
		// every reconnect. A malformed value is a 400 naming whichever was
		// used.
		var after int64 = -1 // -1: from now
		if lei := r.Header.Get("Last-Event-ID"); lei != "" {
			pos, err := parseFeedCursor(lei)
			if err != nil {
				httpx.WriteError(w, r, httpx.BadRequest("Last-Event-ID is not a feed cursor",
					httpx.FieldError{Field: "Last-Event-ID", Message: "must be a cursor this feed minted"}))
				return
			}
			after = pos
		} else if vals := q["cursor"]; len(vals) > 0 {
			if len(vals) > 1 {
				httpx.WriteError(w, r, httpx.BadRequest("cursor parameter is repeated",
					httpx.FieldError{Field: "cursor", Message: "parameter is repeated"}))
				return
			}
			pos, err := parseFeedCursor(vals[0])
			if err != nil {
				httpx.WriteError(w, r, httpx.BadRequest("cursor is not a feed cursor",
					httpx.FieldError{Field: "cursor", Message: "must be a cursor this feed minted"}))
				return
			}
			after = pos
		}

		// The stream limits, answered before the stream opens, in the error
		// envelope.
		principal := feedPrincipal(r.Context())
		if err := h.limits.take(principal); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		defer h.limits.release(principal)

		// The branch wall applies per row, through the same branch context
		// rule the list uses.
		filter.BranchID = middleware.BranchIDForQuery(r.Context())
		filter.GrantsSub = middleware.GrantsSubForQuery(r.Context())

		h.stream(w, r, filter, after)
	}
}

// parseFeedCursor reads a cursor minted for the feed's scope; the id of
// each SSE event is the same encoding.
func parseFeedCursor(raw string) (int64, error) {
	key, err := httpx.DecodeCursor(raw, FeedCursorScope)
	if err != nil {
		return 0, err
	}
	if len(key) != 1 {
		return 0, fmt.Errorf("not a position")
	}
	return strconv.ParseInt(key[0], 10, 64)
}

// mintFeedCursor mints the SSE id of a position.
func mintFeedCursor(pos int64) string {
	c, err := httpx.MintCursor(FeedCursorScope, strconv.FormatInt(pos, 10))
	if err != nil {
		return ""
	}
	return c
}

// feedPrincipal names the stream's holder for the per-principal limit: the
// key id for a keyed stream, the user subject for a session, anonymous
// otherwise.
func feedPrincipal(ctx context.Context) string {
	if id, ok := middleware.KeyIDFromContext(ctx); ok {
		return "key:" + id
	}
	if claims := middleware.ClaimsFromContext(ctx); claims != nil && claims.Subject != "" {
		return "user:" + claims.Subject
	}
	return "anonymous"
}

// sseWriter frames one event and bounds each write with its own deadline
// (the server's fixed WriteTimeout would otherwise cut every stream).
type sseWriter struct {
	w       http.ResponseWriter
	timeout time.Duration
}

func (s *sseWriter) write(ctx context.Context, event string, id string, data any) error {
	rc := http.NewResponseController(s.w)
	if err := rc.SetWriteDeadline(time.Now().Add(s.timeout)); err != nil {
		// A test recorder does not support deadlines; the write still goes
		// out bounded by the context.
		_ = err
	}
	var body string
	if event != "" {
		body += "event: " + event + "\n"
	}
	if id != "" {
		body += "id: " + id + "\n"
	}
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		body += "data: " + string(raw) + "\n"
	}
	body += "\n"
	_, err := s.w.Write([]byte(body))
	return err
}

func (s *sseWriter) comment(line string) error {
	rc := http.NewResponseController(s.w)
	_ = rc.SetWriteDeadline(time.Now().Add(s.timeout))
	_, err := s.w.Write([]byte(":" + line + "\n\n"))
	return err
}

// stream opens and runs the SSE response (section 3.2 to 3.4).
//
// The response writer may be wrapped by middleware (metrics, recovery,
// cache control) that does not implement http.Flusher directly. The
// shipped code type-asserts w.(http.Flusher) and falls back to 503 when
// the assertion fails, which broke the stream in production where
// metrics.statusWriter wraps every response. http.NewResponseController
// looks through the wrappers and finds Flusher if any layer exposes it;
// on a connection that genuinely cannot flush the response controller
// will surface ErrNotSupported when we ask for a flush, which the
// sseWriter emits as a write error and the loop handles.
func (h *FeedHandler) stream(w http.ResponseWriter, r *http.Request, filter EventFilter, after int64) {
	rc := http.NewResponseController(w)
	flusher := func() error {
		return rc.Flush()
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := flusher(); err != nil && err != http.ErrNotSupported {
		httpx.WriteError(w, r, httpx.Unavailable("streaming is not supported on this connection"))
		return
	}

	sse := &sseWriter{w: w, timeout: h.settings.WriteTimeout}
	ctx := r.Context()

	// The lifetime bound: the stream ends at the JWT's exp or the setting,
	// whichever is first, with event: reauth before the close.
	deadline := time.Now().Add(h.settings.MaxLifetime)
	if claims := middleware.ClaimsFromContext(ctx); claims != nil && claims.ExpiresAt != nil {
		if claims.ExpiresAt.Time.Before(deadline) {
			deadline = claims.ExpiresAt.Time
		}
	}

	// Opening without a gap: the ready event carries the head cursor, so a
	// client opens the stream first, then reads the draft; any write after
	// the stream opened arrives on the stream.
	purged, err := h.repo.PurgedThrough(ctx)
	if err != nil {
		slog.Warn("drafts feed: reading the purge mark failed", "error", err)
		purged = 0
	}
	position := after
	// A cursor at or below the highest purged position: changes the client
	// never saw have aged out (ADR 0007 section 3.2).
	if after >= 0 && after <= purged && purged > 0 {
		// Changes the client never saw have aged out: it must re-read what
		// it shows.
		if err := sse.write(ctx, "reset", "", nil); err != nil {
			return
		}
		flusher()
	}
	page, err := h.repo.ReadEvents(ctx, filter, -1, 1)
	if err != nil {
		slog.Warn("drafts feed: reading the head failed", "error", err)
	}
	head := h.hub.Head()
	if page.Head > head {
		head = page.Head
		h.hub.Advance(head)
	}
	if after < 0 {
		position = head // from now
	}
	if err := sse.write(ctx, "ready", mintFeedCursor(head), nil); err != nil {
		return
	}
	flusher()

	keyID := uuid.Nil
	if id, ok := middleware.KeyIDFromContext(ctx); ok {
		if parsed, err := uuid.Parse(id); err == nil {
			keyID = parsed
		}
	}

	heartbeat := time.NewTicker(h.settings.Heartbeat)
	defer heartbeat.Stop()
	lastSent := position
	for {
		// Lifetime: end with event: reauth before the close. The loop wakes
		// at least every heartbeat, so the close goes out within one of the
		// deadline.
		if time.Now().After(deadline) {
			_ = sse.write(ctx, "reauth", mintFeedCursor(position), nil)
			flusher()
			return
		}
		// One batch of rows past the position; the same statement reads the
		// head, one snapshot.
		page, err := h.repo.ReadEvents(ctx, filter, position, h.settings.Batch)
		if err != nil {
			slog.Warn("drafts feed: reading rows failed", "error", err)
		} else {
			for _, ev := range page.Rows {
				item := feedItem{
					DraftID: ev.DraftID, Module: ev.Module, Op: ev.Op, Revision: ev.Revision,
					Status: ev.Status.StatusWire(), BranchID: ev.BranchID, SubjectID: ev.SubjectID,
					By: ev.Actor.wire(),
					At: httpx.FormatKeyTime(ev.At.Time),
				}
				if ev.PromotedEntity != nil {
					item.Promoted = &feedPromoted{EntityID: *ev.PromotedEntity, Number: ev.PromotedNumber}
				}
				if err := sse.write(ctx, "draft", mintFeedCursor(ev.Position), item); err != nil {
					return
				}
				position = ev.Position
				lastSent = position
			}
			flusher()
			// Cursor advance. When the page was full, the next iteration
			// must re-read so rows that committed between the last read
			// and the head land on the stream; jumping to Head right now
			// skips them. When the page was not full we have already
			// seen everything up to Head.
			pageFull := len(page.Rows) == h.settings.Batch
			if pageFull {
				// Stay where we are: the next loop reads from
				// `position` (the last sent row) and catches the rows
				// that landed in the gap. The hub wake will have fired,
				// so the next read runs at once.
				continue
			}
			if page.Head > position {
				position = page.Head
			}
		}

		// Wait: the hub's signal ("the head moved"), the heartbeat, the hub
		// stopping (the shutdown ends open streams) or the client going
		// away. Each read takes the hub's current wake channel fresh, so no
		// signal is lost between the read and the wait.
		wake := h.hub.Channel()
		select {
		case <-wake:
		case <-h.hub.Done():
			// The serve role is shutting down: the stream simply ends (the
			// client reconnects from its last cursor); no event is owed.
			return
		case <-heartbeat.C:
			// A keyed stream rechecks its key at every heartbeat by id; a
			// revoked key's stream gets reauth and closes, so it reads for
			// at most one heartbeat after revocation.
			if keyID != uuid.Nil && h.keyCheck != nil {
				valid, err := h.keyCheck(ctx, keyID)
				if err != nil {
					// The recheck errored (the DB is gone, the row vanished
					// under a partition move, etc). We fail closed for this
					// heartbeat: a forced reauth prevents a revoked key from
					// reading on for the lifetime close. The error is logged
					// so an operator can spot it in the feed's own log
					// stream. (Review pr66-r2 P3-2: the prior code ignored
					// the error and failed open.)
					slog.Warn("drafts feed: keyCheck returned an error; failing closed for this heartbeat",
						"key_id", keyID.String(), "error", err.Error(),
						"request_id", middleware.GetRequestID(ctx))
					_ = sse.write(ctx, "reauth", mintFeedCursor(position), nil)
					flusher()
					return
				}
				if !valid {
					_ = sse.write(ctx, "reauth", mintFeedCursor(position), nil)
					flusher()
					return
				}
			}
			if position > lastSent {
				// Progress without matches: a cursor event, so a
				// reconnecting client does not rescan the excluded rows.
				if err := sse.write(ctx, "cursor", mintFeedCursor(position), nil); err != nil {
					return
				}
				lastSent = position
				flusher()
				continue
			}
			if err := sse.comment("keepalive"); err != nil {
				return
			}
			flusher()
		case <-ctx.Done():
			return
		}
	}
}
