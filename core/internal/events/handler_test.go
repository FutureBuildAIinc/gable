// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package events

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// The feed is a list route on the ADR 0001 rules: the list envelope, keyset
// cursors over position order (commit order, per ADR 0003), strict query
// parameters, the one error envelope. These tests exercise the handler
// directly; the role gate is registration-time middleware, like every
// module route.

func newTestHandler(t *testing.T) (*Handler, *outbox.Writer) {
	t.Helper()
	db := testutil.RequireDB(t)
	if _, err := db.Pool.Exec(context.Background(),
		`TRUNCATE events_outbox, event_subscriber_cursors, event_subscriber_parked`); err != nil {
		t.Fatalf("truncate outbox: %v", err)
	}
	return NewHandler(db), outbox.NewWriter(db, "test-org")
}

func writeEvent(t *testing.T, w *outbox.Writer, typ string, data string) outbox.Event {
	t.Helper()
	ev := outbox.Event{
		ID:         uuid.New(),
		Type:       typ,
		Org:        "test-org",
		EntityType: "quote",
		EntityID:   uuid.New(),
		Data:       json.RawMessage(data),
		At:         time.Date(2026, 2, 3, 4, 5, 6, 123456000, time.UTC),
	}
	if err := w.Write(context.Background(), ev); err != nil {
		t.Fatalf("write %s: %v", typ, err)
	}
	return ev
}

func get(t *testing.T, h *Handler, query string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events"+query, nil)
	rec := httptest.NewRecorder()
	h.List(rec, req)
	var env envelope
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("response is not JSON: %v\n%s", err, rec.Body)
		}
	}
	return rec, env
}

// envelope mirrors the wire list envelope for assertions.
type envelope struct {
	Items      []json.RawMessage `json:"items"`
	NextCursor *string           `json:"next_cursor"`
	Limit      int               `json:"limit"`
	Total      *int64            `json:"total,omitempty"`
}

type item struct {
	EventID  string  `json:"event_id"`
	Type     string  `json:"type"`
	Org      string  `json:"org"`
	BranchID *string `json:"branch_id"`
	Entity   struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"entity"`
	Data json.RawMessage `json:"data"`
	At   string          `json:"at"`
}

func decodeItems(t *testing.T, env envelope) []item {
	t.Helper()
	items := make([]item, 0, len(env.Items))
	for _, raw := range env.Items {
		var it item
		if err := json.Unmarshal(raw, &it); err != nil {
			t.Fatalf("item is not JSON: %v", err)
		}
		items = append(items, it)
	}
	return items
}

// The happy path: committed events come back in the list envelope, in
// position order, each item the snake_case event envelope with an RFC 3339
// UTC timestamp at microsecond precision.
func TestList_ServesTheEnvelopeInOrder(t *testing.T) {
	h, w := newTestHandler(t)

	a := writeEvent(t, w, "quote.exposure.flagged", `{"a":1}`)
	b := writeEvent(t, w, "order.confirmed", `{"b":2}`)

	rec, env := get(t, h, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	items := decodeItems(t, env)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if items[0].EventID != a.ID.String() || items[1].EventID != b.ID.String() {
		t.Errorf("events out of order: %s then %s", items[0].EventID, items[1].EventID)
	}
	first := items[0]
	if first.Type != "quote.exposure.flagged" || first.Org != "test-org" {
		t.Errorf("type/org = %s/%s", first.Type, first.Org)
	}
	if first.Entity.Kind != "quote" || first.Entity.ID != a.EntityID.String() {
		t.Errorf("entity = %+v, want the quote and its id", first.Entity)
	}
	if first.BranchID != nil {
		t.Errorf("branch_id = %v, want null", *first.BranchID)
	}
	if got := strings.TrimSpace(string(first.Data)); got != `{"a": 1}` && got != `{"a":1}` {
		t.Errorf("data = %s, want the row's JSON", got)
	}
	// RFC 3339 UTC with the Z at full microsecond precision (ADR 0001
	// section 12 and the cursor key time format).
	if first.At != "2026-02-03T04:05:06.123456Z" {
		t.Errorf("at = %s, want 2026-02-03T04:05:06.123456Z", first.At)
	}
	if env.Limit != 50 {
		t.Errorf("limit = %d, want the default 50 echoed", env.Limit)
	}
	if env.NextCursor == nil {
		t.Fatal("next_cursor = null on a short page, want the tail cursor (the feed always returns one)")
	}
}

// The tail cursor is a resumption point, not a page marker: a feed read to
// its tail with room to spare still returns next_cursor, and a later commit
// is the only thing the next read with that cursor serves (the reviewer's
// two-step probe from fix round 1).
func TestList_TailCursorServesOnlyNewEvents(t *testing.T) {
	h, w := newTestHandler(t)

	for i := 0; i < 3; i++ {
		writeEvent(t, w, "test.tail", fmt.Sprintf(`{"i":%d}`, i))
	}

	_, env := get(t, h, "?limit=50")
	if len(env.Items) != 3 {
		t.Fatalf("first read served %d items, want 3", len(env.Items))
	}
	if env.NextCursor == nil {
		t.Fatal("next_cursor = null on a page shorter than the limit, want the tail cursor")
	}

	fresh := writeEvent(t, w, "test.tail", `{"i":3}`)
	_, env = get(t, h, "?limit=50&cursor="+*env.NextCursor)
	items := decodeItems(t, env)
	if len(items) != 1 || items[0].EventID != fresh.ID.String() {
		t.Fatalf("read from the tail cursor served %v, want exactly the one new event %s", items, fresh.ID)
	}
	if env.NextCursor == nil {
		t.Fatal("next_cursor = null on the second page, want the new tail cursor")
	}
}

// Pagination: each page carries limit items, the next cursor resumes past
// the last served row, every event is served exactly once per walk, and an
// earlier cursor serves the same events again (a cursor is a position, not
// a consumption claim).
func TestList_PagesWithCursorsExactlyOnce(t *testing.T) {
	h, w := newTestHandler(t)
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, writeEvent(t, w, "test.page", fmt.Sprintf(`{"i":%d}`, i)).ID.String())
	}

	served := map[string]int{}
	query := "?limit=2"
	pages := 0
	for {
		rec, env := get(t, h, query)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		items := decodeItems(t, env)
		if len(items) == 0 {
			// The empty page after the tail: the walk ends on what it
			// served, not on a null cursor (the feed always returns one).
			break
		}
		if env.NextCursor == nil {
			t.Fatal("next_cursor = null on a non-empty page")
		}
		for _, it := range items {
			served[it.EventID]++
		}
		pages++
		query = "?limit=2&cursor=" + *env.NextCursor
	}
	if pages != 3 {
		t.Errorf("walked %d pages, want 3", pages)
	}
	for _, id := range ids {
		if served[id] != 1 {
			t.Errorf("event %s served %d times in one walk, want exactly 1", id, served[id])
		}
	}

	// From the first page's cursor again, the later events are served a
	// second time; that is what replay means on this feed.
	_, first := get(t, h, "?limit=2")
	if first.NextCursor == nil {
		t.Fatal("first page has no next cursor")
	}
	_, again := get(t, h, "?limit=50&cursor="+*first.NextCursor)
	for _, it := range decodeItems(t, again) {
		if served[it.EventID] != 1 {
			t.Fatalf("cursor %s served %s, which the walk already consumed", *first.NextCursor, it.EventID)
		}
	}
	if len(again.Items) != 3 {
		t.Errorf("re-read from the first cursor served %d events, want the remaining 3", len(again.Items))
	}
}

// An empty feed is an empty page: items is [], never null, and the page
// still carries next_cursor (the request's cursor echoed back, position 0
// for a first read), so a poller can adopt it without special casing.
func TestList_EmptyFeedIsEmptyArray(t *testing.T) {
	h, _ := newTestHandler(t)
	rec, env := get(t, h, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if env.Items == nil || len(env.Items) != 0 {
		t.Errorf("items = %v, want an empty array", env.Items)
	}
	if env.NextCursor == nil {
		t.Error("next_cursor = null on an empty feed, want the echoed cursor")
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("body does not carry an empty items array: %s", rec.Body)
	}
}

// A malformed cursor is a 400 naming cursor, never a quiet first page.
func TestList_MalformedCursorIs400(t *testing.T) {
	h, _ := newTestHandler(t)
	for _, cursor := range []string{"not-a-cursor", "////", "eyJ2IjoxLCJvIjoiZXZlbnRzLnBvc2l0aW9uIiwiaiI6WzFd", "eyJ2IjoyLCJvIjoiZXZlbnRzLnBvc2l0aW9uIiwiazpbIjEiXX0"} {
		rec, _ := get(t, h, "?cursor="+cursor)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("cursor %q: status = %d, want 400", cursor, rec.Code)
			continue
		}
		if e := parseError(t, rec); e.Error.Code != "bad_request" || !namesField(e, "cursor") {
			t.Errorf("cursor %q: error = %+v, want bad_request naming cursor", cursor, e)
		}
	}
}

// An unknown query parameter is a 400 naming it: a silent no-op filter on
// this feed would hide events from exactly the agents that poll it.
func TestList_UnknownParameterIs400(t *testing.T) {
	h, _ := newTestHandler(t)
	rec, _ := get(t, h, "?foo=1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	e := parseError(t, rec)
	if e.Error.Code != "unsupported_query_parameter" || !namesField(e, "foo") {
		t.Errorf("error = %+v, want unsupported_query_parameter naming foo", e)
	}
}

// The limit is strict: malformed or out of range is a 400 naming limit.
func TestList_LimitIsStrict(t *testing.T) {
	h, _ := newTestHandler(t)
	for _, q := range []string{"?limit=abc", "?limit=0", "?limit=201", "?limit=1.5", "?limit="} {
		rec, _ := get(t, h, q)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, rec.Code)
		}
	}
	rec, env := get(t, h, "?limit=1")
	if rec.Code != http.StatusOK || env.Limit != 1 {
		t.Errorf("limit=1: status %d, echoed %d, want 200 and 1", rec.Code, env.Limit)
	}
}

// The types filter serves only the listed types, repeatable and comma
// separated; an unknown name shape is a 400 naming types.
func TestList_TypesFilter(t *testing.T) {
	h, w := newTestHandler(t)
	writeEvent(t, w, "quote.exposure.flagged", `{"n":1}`)
	writeEvent(t, w, "order.confirmed", `{"n":2}`)
	writeEvent(t, w, "order.cancelled", `{"n":3}`)

	rec, env := get(t, h, "?types=order.confirmed,order.cancelled")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	items := decodeItems(t, env)
	if len(items) != 2 {
		t.Fatalf("filtered items = %d, want 2", len(items))
	}
	for _, it := range items {
		if strings.HasPrefix(it.Type, "quote.") {
			t.Errorf("unlisted type %s passed the filter", it.Type)
		}
	}

	rec, env = get(t, h, "?types=order.confirmed&types=order.cancelled")
	if rec.Code != http.StatusOK || len(decodeItems(t, env)) != 2 {
		t.Errorf("repeated types: status %d, want 200 and both events", rec.Code)
	}

	rec, _ = get(t, h, "?types=Order.Confirmed")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("uppercase type: status = %d, want 400", rec.Code)
	}
	rec, _ = get(t, h, "?types=")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty types: status = %d, want 400", rec.Code)
	}
	e := parseError(t, rec)
	if !namesField(e, "types") {
		t.Errorf("error = %+v, want it to name types", e)
	}
}

// total appears only under ?include=total, and an unknown include name is a
// 400 naming include.
func TestList_IncludeTotal(t *testing.T) {
	h, w := newTestHandler(t)
	writeEvent(t, w, "quote.exposure.flagged", `{"n":1}`)
	writeEvent(t, w, "order.confirmed", `{"n":2}`)

	_, env := get(t, h, "")
	if env.Total != nil {
		t.Errorf("total = %v on an ordinary page, want it absent", *env.Total)
	}
	_, env = get(t, h, "?include=total")
	if env.Total == nil || *env.Total != 2 {
		t.Errorf("total = %v, want 2", env.Total)
	}
	_, env = get(t, h, "?include=total&types=order.confirmed")
	if env.Total == nil || *env.Total != 1 {
		t.Errorf("filtered total = %v, want 1", env.Total)
	}
	rec, _ := get(t, h, "?include=bogus")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("include=bogus: status = %d, want 400", rec.Code)
	}
}

// errorEnvelope mirrors the wire error shape for assertions.
type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details []struct {
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"details"`
	} `json:"error"`
	Meta struct {
		RequestID string `json:"request_id"`
	} `json:"meta"`
}

func parseError(t *testing.T, rec *httptest.ResponseRecorder) errorEnvelope {
	t.Helper()
	var e errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("error body is not JSON: %v\n%s", err, rec.Body)
	}
	return e
}

func namesField(e errorEnvelope, field string) bool {
	for _, d := range e.Error.Details {
		if d.Field == field {
			return true
		}
	}
	return false
}
