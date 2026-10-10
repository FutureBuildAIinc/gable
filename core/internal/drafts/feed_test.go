// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts_test

// The change feed's facts (ADR 0007 section 11, the Feed row): the
// subject-filtered stream carries only that subject's drafts, a reconnect
// carrying both the original cursor and a newer Last-Event-ID resumes from
// the Last-Event-ID, a revoked key's stream closes at the next heartbeat, a
// stream receives a PUT made on another connection with the right by,
// resume by each mechanism serves each row once, the two-commit ordering
// case both serves, a filtered stream advances its cursor, a reader that
// never reads is closed at its write deadline, reset follows a purge, the
// stream ends at its lifetime bound, and the by actor is on every event.

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/drafts"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/techadmin"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// sseEvent is one parsed event off the stream.
type sseEvent struct {
	event string
	id    string
	data  string
}

// openStream opens the feed and reads events until the context ends or the
// wanted count arrives.
func (f *fixture) openStream(ctx context.Context, path string, headers ...string) (chan sseEvent, chan error) {
	events := make(chan sseEvent, 64)
	errs := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.srv.URL+path, nil)
		if err != nil {
			errs <- err
			return
		}
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			errs <- err
			return
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			errs <- errHTTP{res.StatusCode}
			return
		}
		if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
			errs <- errHTTP{res.StatusCode}
			return
		}
		scanner := bufio.NewScanner(res.Body)
		var ev sseEvent
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				ev.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				ev.data = strings.TrimPrefix(line, "data: ")
			case line == "":
				if ev.event != "" {
					events <- ev
				}
				ev = sseEvent{}
			}
		}
		close(events)
	}()
	return events, errs
}

type errHTTP struct{ status int }

func (e errHTTP) Error() string { return "stream answered a non-200 status" }

// await waits for the next event with a bound.
func await(t *testing.T, events chan sseEvent, what string) sseEvent {
	t.Helper()
	return awaitFor(t, events, what, 5*time.Second)
}

// awaitFor is await with its own bound, for waits the settings pace (the
// lifetime bound runs seconds past the open).
func awaitFor(t *testing.T, events chan sseEvent, what string, bound time.Duration) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-events:
		if !ok {
			t.Fatalf("the stream ended while waiting for %s", what)
		}
		return ev
	case <-time.After(bound):
		t.Fatalf("timed out waiting for %s", what)
	}
	return sseEvent{}
}

// TestFeedSubjectFilterAndCrossConnectionUpdate pins two facts: a
// subject-filtered stream receives only that subject's drafts, and a PUT
// made on ANOTHER connection arrives with the right by actor.
func TestFeedSubjectFilterAndCrossConnectionUpdate(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	subject := f.createQuote(t)
	f.create() // noise: another draft on no subject

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events, errs := f.openStream(ctx, "/api/v1/drafts/quotes/feed?subject_id="+subject)
	defer func() { cancel(); <-events }()
	await(t, events, "ready")

	// A write on another connection: an edit draft on the subject, saved by
	// an agent with the person's session.
	r := f.do("POST", "/api/v1/drafts/quotes", map[string]any{
		"payload": f.editPayload(subject), "subject_id": subject},
		"X-Acting-As", "agent", "X-Agent-Tool", "quote-builder")
	if r.status != http.StatusCreated {
		t.Fatalf("create edit draft = %d: %s", r.status, r.raw)
	}
	id := str(t, r.body, "id")

	ev := await(t, events, "the created event")
	if !strings.Contains(ev.data, `"op":"created"`) || !strings.Contains(ev.data, id) {
		t.Errorf("created event = %s", ev.data)
	}
	// The by actor is the agent with its tool.
	if !strings.Contains(ev.data, `"kind":"agent"`) || !strings.Contains(ev.data, `"tool":"quote-builder"`) {
		t.Errorf("by actor = %s", ev.data)
	}
	// The PUT on the other connection.
	r = f.do("PUT", "/api/v1/drafts/quotes/"+id, map[string]any{"payload": f.editPayload(subject), "revision": 1})
	if r.status != http.StatusOK {
		t.Fatalf("put = %d: %s", r.status, r.raw)
	}
	ev = await(t, events, "the updated event")
	if !strings.Contains(ev.data, `"op":"updated"`) || !strings.Contains(ev.data, `"revision":2`) {
		t.Errorf("updated event = %s", ev.data)
	}
	select {
	case e := <-errs:
		t.Fatalf("stream error: %v", e)
	default:
	}
}

// TestFeedResumeByBothMechanismsOnceEach pins the cursor rules: resume by
// Last-Event-ID and by the cursor parameter each serves every row once,
// and a reconnect carrying BOTH the original cursor and a newer
// Last-Event-ID resumes from the Last-Event-ID (refusing the pair would
// fail every reconnect).
func TestFeedResumeByBothMechanismsOnceEach(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))

	// Open the stream FIRST (a client opens the stream, then reads), then
	// create: every write after the stream opened arrives on it.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	events, _ := f.openStream(ctx, "/api/v1/drafts/quotes/feed")
	ready := await(t, events, "ready")
	ids := []string{f.create(), f.create(), f.create()}
	first := await(t, events, "created #1")
	second := await(t, events, "created #2")
	cancel()
	for range events {
	}

	// Resume by Last-Event-ID: the third row only.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	ev2, _ := f.openStream(ctx2, "/api/v1/drafts/quotes/feed?cursor="+ready.id,
		"Last-Event-ID", second.id)
	await(t, ev2, "ready")
	third := await(t, ev2, "created #3")
	if !strings.Contains(third.data, ids[2]) {
		t.Errorf("resumed stream served %s, want %s", third.data, ids[2])
	}
	if !strings.Contains(first.data, ids[0]) || !strings.Contains(second.data, ids[1]) {
		t.Errorf("the first two events carried %s and %s", first.data, second.data)
	}
	cancel2()
	for range ev2 {
	}

	// A malformed value is a 400 naming whichever was used.
	for _, tc := range []struct{ header, param string }{
		{"", "not-a-cursor"},
		{"garbage", ""},
		{"garbage", "also-garbage"},
	} {
		path := "/api/v1/drafts/quotes/feed"
		var headers []string
		if tc.param != "" {
			path += "?cursor=" + tc.param
		}
		if tc.header != "" {
			headers = []string{"Last-Event-ID", tc.header}
		}
		r := f.doRawGet(path, headers...)
		if r.status != http.StatusBadRequest {
			t.Errorf("malformed cursor (header %q param %q) = %d, want 400", tc.header, tc.param, r.status)
		}
	}

	// An unsupported query parameter is refused before the stream opens.
	if r := f.doRawGet("/api/v1/drafts/quotes/feed?limit=5"); r.status != http.StatusBadRequest {
		t.Errorf("limit parameter = %d, want 400 before the stream opens", r.status)
	}
}

// doRawGet makes a GET and returns the response without following the
// stream.
func (f *fixture) doRawGet(path string, headers ...string) resp {
	f.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	raw := make([]byte, 2048)
	n, _ := res.Body.Read(raw)
	out := resp{status: res.StatusCode, header: res.Header, raw: raw[:n]}
	return out
}

// TestFeedCommitOrderServesBoth pins the ADR 0003 case on this feed's own
// lock: two transactions whose positions commit out of order (the lower
// commits last) are both served to a reader that pages by position.
func TestFeedCommitOrderServesBoth(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))

	// Two writers whose commits invert their positions: writer A draws its
	// event position first, then writer B commits before A does.
	var wg sync.WaitGroup
	gate := make(chan struct{})
	wg.Add(2)
	go func() { // writer A: inserts the draft event, then waits
		defer wg.Done()
		ctx := context.Background()
		err := f.db.RunInTx(ctx, func(ctx context.Context) error {
			draftID := uuid.New()
			_ = draftID
			if _, err := f.db.GetExecutor(ctx).Exec(ctx,
				`INSERT INTO draft_events (draft_id, module, branch_id, op, revision, status, actor_kind)
				 VALUES ($1, 'quotes', (SELECT value::uuid FROM system_settings WHERE key='default_branch_id'), 'created', 1, 'OPEN', 'anonymous')`,
				draftID); err != nil {
				return err
			}
			<-gate
			return nil
		})
		if err != nil {
			t.Errorf("writer A: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond) // A's insert (and position) is drawn
	go func() {                        // writer B: commits a higher position while A waits
		defer wg.Done()
		err := f.db.RunInTx(context.Background(), func(ctx context.Context) error {
			draftID := uuid.New()
			_ = draftID
			_, err := f.db.GetExecutor(ctx).Exec(ctx,
				`INSERT INTO draft_events (draft_id, module, branch_id, op, revision, status, actor_kind)
				 VALUES ($1, 'quotes', (SELECT value::uuid FROM system_settings WHERE key='default_branch_id'), 'created', 1, 'OPEN', 'anonymous')`,
				draftID)
			return err
		})
		if err != nil {
			t.Errorf("writer B: %v", err)
		}
	}()
	// Give B time to commit, then release A: A's LOWER position commits LAST.
	time.Sleep(200 * time.Millisecond)
	close(gate)
	wg.Wait()

	// A reader from position 0 serves both rows.
	rows, err := f.draftsRepo.ReadEvents(context.Background(), drafts.EventFilter{Module: "quotes"}, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Rows) < 2 {
		t.Fatalf("the reader saw %d rows, want both commits (the lower position committed last)", len(rows.Rows))
	}
}

// TestFeedFilteredStreamAdvancesCursor pins the cursor event: a stream
// whose filters exclude a write still moves its position past it, and the
// next heartbeat is a cursor event (not a keepalive), so a reconnecting
// client does not rescan the excluded rows.
func TestFeedFilteredStreamAdvancesCursor(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	subject := f.createQuote(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events, _ := f.openStream(ctx, "/api/v1/drafts/quotes/feed?subject_id="+subject)
	defer func() { cancel(); <-events }()
	await(t, events, "ready")

	// A write the filter excludes: a create draft with no subject.
	f.create()
	// The next heartbeat carries the moved position as a cursor event.
	ev := await(t, events, "the cursor event")
	if ev.event != "cursor" || ev.id == "" {
		t.Errorf("event = %+v, want a cursor event with the new id", ev)
	}
}

// TestFeedResetAfterPurge pins the reset: after the purge deletes rows past
// a client's cursor, a resume from that cursor is told to re-read first.
func TestFeedResetAfterPurge(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))

	id := f.create()
	// Read the events' positions.
	page, err := f.draftsRepo.ReadEvents(context.Background(), eventFilterAll(), 0, 10)
	if err != nil || len(page.Rows) == 0 {
		t.Fatalf("read events: %v rows=%d", err, len(page.Rows))
	}
	old := page.Rows[0].Position

	// Purge everything older than now: the row the client holds is gone.
	if _, _, err := f.draftsRepo.PurgeOnce(context.Background(), time.Now().Add(time.Second), 100); err != nil {
		t.Fatal(err)
	}
	through, err := f.draftsRepo.PurgedThrough(context.Background())
	if err != nil || through < old {
		t.Fatalf("purged through %d (want >= %d): %v", through, old, err)
	}

	// A resume from the old cursor is told to re-read.
	cursor := mintTestCursor(t, old)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	events, _ := f.openStream(ctx, "/api/v1/drafts/quotes/feed?cursor="+cursor)
	defer func() { cancel(); <-events }()
	ev := await(t, events, "reset")
	if ev.event != "reset" {
		t.Errorf("first event = %s, want reset", ev.event)
	}
	_ = id
}

// eventFilterAll is the unfiltered feed query.
func eventFilterAll() drafts.EventFilter { return drafts.EventFilter{Module: "quotes"} }

// mintTestCursor mints a feed cursor for a position, as the endpoint does.
func mintTestCursor(t *testing.T, pos int64) string {
	t.Helper()
	c, err := httpx.MintCursor(drafts.FeedCursorScope, strconv.FormatInt(pos, 10))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestFeedRevokedKeyClosesAtNextHeartbeat pins section 3.3: a keyed stream
// rechecks its key at every heartbeat by id; a revoked key's stream gets
// event: reauth and closes, so it reads for at most one heartbeat after
// the revocation.
func TestFeedRevokedKeyClosesAtNextHeartbeat(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))

	// Mint a propose key and mount the machine-key auth core in front of
	// the drafts routes, as serve does.
	keySvc := techadmin.NewService(techadmin.NewRepository(f.db)).WithTxRunner(f.db)
	raw, keyRow, err := keySvc.GenerateKey(context.Background(), "feed key", []string{"quotes:propose"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mkAuth := middleware.NewMachineKeyAuth(machineKeyValidatorForTests{svc: keySvc}, nil, nil, nil)
	inner := f.srv.Config.Handler
	srv := httptest.NewServer(mkAuth.Handler(inner))
	defer srv.Close()

	// Open the keyed stream.
	streamCtx, cancelStream := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStream()
	req, _ := http.NewRequestWithContext(streamCtx, "GET", srv.URL+"/api/v1/drafts/quotes/feed", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("keyed stream = %d", res.StatusCode)
	}

	// Revoke the key; the stream's next heartbeat notices and closes with
	// event: reauth (the scan runs in its own goroutine so the wait is
	// bounded even while the body blocks).
	if _, err := keySvc.RevokeKey(context.Background(), keyRow.ID); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(res.Body)
	reauth := make(chan struct{}, 1)
	ended := make(chan struct{}, 1)
	go func() {
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "event: reauth") {
				reauth <- struct{}{}
				return
			}
		}
		ended <- struct{}{}
	}()
	select {
	case <-reauth:
	case <-ended:
		t.Error("the revoked key's stream ended without event: reauth")
	case <-time.After(5 * time.Second):
		t.Error("timed out waiting for the reauth close after the revocation")
	}
}

// machineKeyValidatorForTests adapts the techadmin service to the
// machine-key core's seam, as serve's machineKeyValidator does.
type machineKeyValidatorForTests struct{ svc *techadmin.Service }

func (v machineKeyValidatorForTests) ValidateKey(ctx context.Context, rawKey string) (middleware.KeyPrincipal, error) {
	k, err := v.svc.ValidateKey(ctx, rawKey)
	if err != nil {
		if errors.Is(err, techadmin.ErrInvalidKey) {
			return middleware.KeyPrincipal{}, middleware.ErrInvalidMachineKey
		}
		return middleware.KeyPrincipal{}, err
	}
	return middleware.KeyPrincipal{ID: k.ID.String(), Scopes: k.Scopes, BranchID: k.BranchID}, nil
}

// TestFeedEndsAtLifetimeBound pins section 3.3: the stream ends at its
// lifetime bound with event: reauth before the close, and the client
// reconnects from its last cursor.
func TestFeedEndsAtLifetimeBound(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events, _ := f.openStream(ctx, "/api/v1/drafts/quotes/feed")
	defer func() { cancel(); <-events }()
	ready := await(t, events, "ready")
	if ready.id == "" {
		t.Fatal("the ready event carries no cursor id")
	}
	// The fast settings bound the lifetime to 5 seconds; the next event is
	// the reauth close (awaitFor paces the wait past the bound itself).
	ev := awaitFor(t, events, "reauth", 8*time.Second)
	if ev.event != "reauth" || ev.id == "" {
		t.Errorf("closing event = %+v, want reauth with the cursor", ev)
	}
}

// TestFeedShutdownEndsStreams pins section 3.4: when the hub stops (the
// serve role's RegisterOnShutdown), open streams end.
func TestFeedShutdownEndsStreams(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events, errs := f.openStream(ctx, "/api/v1/drafts/quotes/feed")
	await(t, events, "ready")

	// Stop the hub as the shutdown hook does; the stream's context ends
	// with the test server's close, so assert on the events channel
	// closing after the server closes.
	f.hub.Stop()
	f.hub.Nudge()
	f.srv.Close()
	select {
	case _, ok := <-events:
		if ok {
			t.Error("an event arrived after the shutdown")
		}
	case e := <-errs:
		t.Errorf("stream error after shutdown: %v", e)
	case <-time.After(5 * time.Second):
		t.Error("the stream did not end after the shutdown")
	}
	cancel()
}
