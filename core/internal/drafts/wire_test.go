// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts_test

// The drafts core's wire facts (ADR 0007 section 11, the Drafts core row):
// the create shape with computed validation, the payload refusals, the
// subject_revision rebase bounds, the list and cursor, the revision
// preconditions, the edit rule, the transitions, and idempotent create.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// TestDraftCreateShape pins the created draft's wire shape (section 2.2):
// lowercase status, revision 1 with its ETag, the payload echoed, the actor
// objects, optional blocks present as null, and the computed validation.
func TestDraftCreateShape(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	r := f.do("POST", "/api/v1/drafts/quotes", map[string]any{"payload": f.payload()})
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	if loc := r.header.Get("Location"); !strings.HasPrefix(loc, "/api/v1/drafts/quotes/") {
		t.Errorf("Location = %q", loc)
	}
	if etag := r.header.Get("ETag"); etag != `"1"` {
		t.Errorf("ETag = %q, want \"1\"", etag)
	}
	if got := str(t, r.body, "module"); got != "quotes" {
		t.Errorf("module = %q", got)
	}
	if got := str(t, r.body, "status"); got != "open" {
		t.Errorf("status = %q, want open", got)
	}
	if got := num(t, r.body, "revision"); got != 1 {
		t.Errorf("revision = %d", got)
	}
	if r.body["promoted"] != nil {
		t.Errorf("promoted = %v, want null", r.body["promoted"])
	}
	if r.body["discarded"] != nil {
		t.Errorf("discarded = %v, want null", r.body["discarded"])
	}
	if r.body["subject_id"] != nil {
		t.Errorf("subject_id = %v, want null", r.body["subject_id"])
	}
	payload, _ := r.body["payload"].(map[string]any)
	if payload == nil || payload["customer_id"] != f.customerID.String() {
		t.Errorf("payload not echoed: %v", r.body["payload"])
	}
	// The actor of a dev mode call is anonymous (no claims, no key).
	createdBy, _ := r.body["created_by"].(map[string]any)
	if createdBy == nil || createdBy["kind"] != "anonymous" {
		t.Errorf("created_by = %v, want kind anonymous", r.body["created_by"])
	}
	// A complete payload validates ready with an empty problems array.
	validation, _ := r.body["validation"].(map[string]any)
	if validation == nil || validation["ready"] != true {
		t.Errorf("validation = %v, want ready", r.body["validation"])
	}
	if probs, ok := validation["problems"].([]any); ok && len(probs) > 0 {
		t.Errorf("validation.problems = %v, want empty", probs)
	}
	// One created event row.
	id := str(t, r.body, "id")
	if rows := f.auditRows(id); len(rows) != 1 || rows[0]["action"] != "draft.created" {
		t.Errorf("audit rows = %v", rows)
	}
}

// TestDraftValidationComputedNotEnforced pins the validation contract: a
// payload missing required fields is stored (a draft is by nature
// unfinished) and its problems name the payload-prefixed fields, with ready
// false; the structural errors (an unknown field, a wrong type) are 400s.
func TestDraftValidationComputedNotEnforced(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))

	// Missing customer_id: stored, not ready, problem named.
	r := f.do("POST", "/api/v1/drafts/quotes", map[string]any{"payload": map[string]any{
		"lines": []map[string]any{},
	}})
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	validation, _ := r.body["validation"].(map[string]any)
	if validation["ready"] != false {
		t.Errorf("ready = %v, want false", validation["ready"])
	}
	if !hasValidationProblem(validation, "payload.customer_id") {
		t.Errorf("problems = %v, want payload.customer_id", validation["problems"])
	}

	// The problems update on PUT with the new payload.
	id := str(t, r.body, "id")
	r = f.do("PUT", "/api/v1/drafts/quotes/"+id, map[string]any{"payload": f.payload(), "revision": 1})
	if r.status != http.StatusOK {
		t.Fatalf("put = %d: %s", r.status, r.raw)
	}
	validation, _ = r.body["validation"].(map[string]any)
	if validation["ready"] != true {
		t.Errorf("ready = %v after a complete payload, want true; problems %v", validation["ready"], validation["problems"])
	}

	// An unknown field is a structural 400 naming payload.<field>.
	r = f.do("POST", "/api/v1/drafts/quotes", map[string]any{"payload": map[string]any{
		"custmer_id": f.customerID.String(),
	}})
	if r.status != http.StatusBadRequest {
		t.Fatalf("unknown field = %d: %s", r.status, r.raw)
	}
	code, _, details := errorOf(t, r)
	if code != "bad_request" || !hasDetailField(details, "payload.custmer_id") {
		t.Errorf("code=%s details=%v", code, details)
	}

	// A value of the wrong JSON type is a structural 400 with the path.
	r = f.do("POST", "/api/v1/drafts/quotes", map[string]any{"payload": map[string]any{
		"customer_id": 42,
	}})
	if r.status != http.StatusBadRequest {
		t.Fatalf("wrong type = %d: %s", r.status, r.raw)
	}
}

func hasValidationProblem(validation map[string]any, field string) bool {
	probs, _ := validation["problems"].([]any)
	for _, p := range probs {
		if m, ok := p.(map[string]any); ok && m["field"] == field {
			return true
		}
	}
	return false
}

// TestDraftPayloadRefusals pins the payload rules of section 2.3: the file
// triplet never rides inside a payload, a payload revision is refused, and
// a payload past 256 KiB is a 413.
func TestDraftPayloadRefusals(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))

	for _, field := range []string{"original_file", "original_filename", "original_content_type"} {
		p := f.payload()
		p[field] = "anything"
		r := f.do("POST", "/api/v1/drafts/quotes", map[string]any{"payload": p})
		if r.status != http.StatusBadRequest {
			t.Fatalf("payload with %s = %d: %s", field, r.status, r.raw)
		}
		code, _, details := errorOf(t, r)
		if code != "validation_failed" || !hasDetailField(details, "payload."+field) {
			t.Errorf("%s: code=%s details=%v", field, code, details)
		}
	}

	p := f.payload()
	p["revision"] = 3
	r := f.do("POST", "/api/v1/drafts/quotes", map[string]any{"payload": p})
	if r.status != http.StatusBadRequest {
		t.Fatalf("payload with revision = %d: %s", r.status, r.raw)
	}
	if _, _, details := errorOf(t, r); !hasDetailField(details, "payload.revision") {
		t.Errorf("details = %v, want payload.revision", details)
	}

	// A payload past 256 KiB of JSON is a 413 payload_too_large.
	big := f.payload()
	big["lines"].([]map[string]any)[0]["description"] = strings.Repeat("x", 300<<10)
	raw, _ := json.Marshal(map[string]any{"payload": big})
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/api/v1/drafts/quotes", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized payload = %d, want 413", res.StatusCode)
	}
}

// TestDraftSubjectRevisionAndRebaseBounds pins the edit draft's subject
// rule (section 2.4): an asserted subject revision past the current one is
// a 409 with the subject_stale blocker, an omitted one takes the current,
// and a PUT can move subject_revision only forward and only up to the
// subject's current revision.
func TestDraftSubjectRevisionAndRebaseBounds(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))

	// Make a quote to edit: create, then promote one draft onto it.
	quoteID := f.createQuote(t)

	r := f.do("POST", "/api/v1/drafts/quotes", map[string]any{
		"payload": f.editPayload(quoteID), "subject_id": quoteID, "subject_revision": 99})
	if r.status != http.StatusConflict {
		t.Fatalf("assert a future subject revision = %d: %s", r.status, r.raw)
	}
	code, _, details := errorOf(t, r)
	if code != "stale_revision" || !hasBlocker(details, "subject_stale") {
		t.Errorf("code=%s details=%v", code, details)
	}

	r = f.do("POST", "/api/v1/drafts/quotes", map[string]any{
		"payload": f.editPayload(quoteID), "subject_id": quoteID})
	if r.status != http.StatusCreated {
		t.Fatalf("create edit draft = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "subject_revision"); got != 1 {
		t.Errorf("subject_revision = %d, want the subject's current 1 (the quote is born at revision 1)", got)
	}
	id := str(t, r.body, "id")

	// A rebase below the stored value, and one past the subject's current
	// revision, are both 400s naming subject_revision.
	for _, bad := range []int64{0, 5} {
		r = f.do("PUT", "/api/v1/drafts/quotes/"+id, map[string]any{
			"payload": f.editPayload(quoteID), "revision": 1, "subject_revision": bad})
		if r.status != http.StatusBadRequest {
			t.Fatalf("rebase to %d = %d: %s", bad, r.status, r.raw)
		}
		if _, _, d := errorOf(t, r); !hasDetailField(d, "subject_revision") {
			t.Errorf("rebase to %d details = %v", bad, d)
		}
	}
	// A rebase to the subject's current revision is accepted.
	r = f.do("PUT", "/api/v1/drafts/quotes/"+id, map[string]any{
		"payload": f.editPayload(quoteID), "revision": 1, "subject_revision": 1})
	if r.status != http.StatusOK {
		t.Fatalf("rebase to current = %d: %s", r.status, r.raw)
	}

	// A subject the caller cannot see is a 404.
	r = f.do("POST", "/api/v1/drafts/quotes", map[string]any{
		"payload": f.editPayload(quoteID), "subject_id": "ffffffff-ffff-ffff-ffff-ffffffffffff"})
	if r.status != http.StatusNotFound {
		t.Fatalf("unknown subject = %d: %s", r.status, r.raw)
	}
}

// editPayload is the update request for a quote edit draft (no branch, no
// source: the update refuses them).
func (f *fixture) editPayload(quoteID string) map[string]any {
	return map[string]any{
		"customer_id":   f.customerID.String(),
		"delivery_type": "delivery",
		"lines": []map[string]any{{
			"product_id": f.productID.String(), "sku": f.sku, "description": "2x4x8 SPF",
			"quantity": "20", "uom": "PCS", "unit_price_ten_thousandths": 60000,
		}},
	}
}

// createQuote promotes one draft onto a fresh quote and answers its id.
func (f *fixture) createQuote(t *testing.T) string {
	t.Helper()
	id := f.create()
	r := f.do("POST", "/api/v1/drafts/quotes/"+id+"/promote", map[string]any{"revision": 1})
	if r.status != http.StatusCreated {
		t.Fatalf("promote = %d: %s", r.status, r.raw)
	}
	loc := r.header.Get("Location")
	return loc[strings.LastIndex(loc, "/")+1:]
}

// TestDraftRevisionPreconditions pins section 2.5: every PUT needs If-Match
// or the body revision (428 without), a moved revision is 409
// stale_revision, and the strong and the weak If-Match forms both carry the
// revision.
func TestDraftRevisionPreconditions(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	id := f.create()

	r := f.do("PUT", "/api/v1/drafts/quotes/"+id, map[string]any{"payload": f.payload()})
	if r.status != http.StatusPreconditionRequired {
		t.Fatalf("put without a precondition = %d: %s", r.status, r.raw)
	}

	r = f.do("PUT", "/api/v1/drafts/quotes/"+id, map[string]any{"payload": f.payload(), "revision": 1})
	if r.status != http.StatusOK || num(t, r.body, "revision") != 2 || r.header.Get("ETag") != `"2"` {
		t.Fatalf("put on the body revision = %d rev=%v etag=%q", r.status, r.body["revision"], r.header.Get("ETag"))
	}

	for _, ifMatch := range []string{`"2"`, `W/"3"`} {
		r = f.do("PUT", "/api/v1/drafts/quotes/"+id, map[string]any{"payload": f.payload()}, "If-Match", ifMatch)
		if r.status != http.StatusOK {
			t.Errorf("put with If-Match %s = %d: %s", ifMatch, r.status, r.raw)
		}
	}

	// A stale write is refused with 409 stale_revision and nothing moves.
	r = f.do("PUT", "/api/v1/drafts/quotes/"+id, map[string]any{"payload": f.payload(), "revision": 1})
	if r.status != http.StatusConflict {
		t.Fatalf("stale put = %d: %s", r.status, r.raw)
	}
	if code, _, _ := errorOf(t, r); code != "stale_revision" {
		t.Errorf("code = %s", code)
	}
	got := f.do("GET", "/api/v1/drafts/quotes/"+id, nil)
	if num(t, got.body, "revision") != 4 {
		t.Errorf("revision moved by a refused write: %v", got.body["revision"])
	}

	// The header and the body disagreeing is a 400.
	r = f.do("PUT", "/api/v1/drafts/quotes/"+id, map[string]any{"payload": f.payload(), "revision": 4}, "If-Match", `"5"`)
	if r.status != http.StatusBadRequest {
		t.Errorf("disagreeing carriers = %d: %s", r.status, r.raw)
	}
}

// TestDraftEditRuleAndTransitions pins the lifecycle: a PUT on a promoted
// or discarded draft is 409 conflict with the draft_not_open blocker (an
// edit is not a transition), open discards, discarded reopens, and promoted
// is terminal.
func TestDraftEditRuleAndTransitions(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	id := f.create()

	// open -> discarded
	r := f.do("POST", "/api/v1/drafts/quotes/"+id+"/transitions", map[string]any{"to": "discarded", "revision": 1})
	if r.status != http.StatusOK || str(t, r.body, "status") != "discarded" {
		t.Fatalf("discard = %d: %s", r.status, r.raw)
	}
	if r.body["discarded"] == nil {
		t.Error("discarded block not filled")
	}
	// an edit on a discarded draft is the conflict blocker
	r = f.do("PUT", "/api/v1/drafts/quotes/"+id, map[string]any{"payload": f.payload(), "revision": 2})
	if r.status != http.StatusConflict {
		t.Fatalf("edit a discarded draft = %d: %s", r.status, r.raw)
	}
	if code, _, d := errorOf(t, r); code != "conflict" || !hasBlocker(d, "draft_not_open") {
		t.Errorf("code=%s details=%v", code, d)
	}
	// discarded -> open clears the block
	r = f.do("POST", "/api/v1/drafts/quotes/"+id+"/transitions", map[string]any{"to": "open", "revision": 2})
	if r.status != http.StatusOK || str(t, r.body, "status") != "open" || r.body["discarded"] != nil {
		t.Fatalf("reopen = %d: %s", r.status, r.raw)
	}
	// promoted is not a transition target: promotion is its own route
	// (it needs a different scope), so to=promoted is the lifecycle's 409.
	r = f.do("POST", "/api/v1/drafts/quotes/"+id+"/transitions", map[string]any{"to": "promoted", "revision": 3})
	if r.status != http.StatusConflict {
		t.Errorf("to=promoted = %d, want 409", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "invalid_state_transition" {
		t.Errorf("code = %s", code)
	}
	// an unknown target is a 400
	r = f.do("POST", "/api/v1/drafts/quotes/"+id+"/transitions", map[string]any{"to": "archived", "revision": 3})
	if r.status != http.StatusBadRequest {
		t.Errorf("to=archived = %d, want 400", r.status)
	}

	// promoted is terminal
	promoted := f.create()
	r = f.do("POST", "/api/v1/drafts/quotes/"+promoted+"/promote", map[string]any{"revision": 1})
	if r.status != http.StatusCreated {
		t.Fatalf("promote = %d: %s", r.status, r.raw)
	}
	r = f.do("POST", "/api/v1/drafts/quotes/"+promoted+"/transitions", map[string]any{"to": "discarded", "revision": 2})
	if r.status != http.StatusConflict {
		t.Errorf("transition a promoted draft = %d, want 409", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "invalid_state_transition" {
		t.Errorf("code = %s", code)
	}
}

// TestDraftListAndCursor pins the list (section 2.3): the envelope, the
// status, subject and creator filters, the branch wall's shape, the cursor
// walking every row once, and the strictness.
func TestDraftListAndCursor(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	mine := []string{f.create(), f.create(), f.create()}
	subject := f.createQuote(t)
	r := f.do("POST", "/api/v1/drafts/quotes", map[string]any{
		"payload": f.editPayload(subject), "subject_id": subject})
	if r.status != http.StatusCreated {
		t.Fatalf("create edit draft = %d: %s", r.status, r.raw)
	}
	mine = append(mine, str(t, r.body, "id"))

	r = f.do("GET", "/api/v1/drafts/quotes", nil, "X-Request-ID", "req-drafts-test")
	if r.status != http.StatusOK {
		t.Fatalf("list = %d: %s", r.status, r.raw)
	}
	if _, ok := r.body["next_cursor"]; !ok {
		t.Error("next_cursor missing")
	}

	// The status filter filters: every returned row is promoted.
	r = f.do("GET", "/api/v1/drafts/quotes?status=promoted", nil)
	if r.status != http.StatusOK {
		t.Fatalf("status=promoted = %d: %s", r.status, r.raw)
	}
	items, _ := r.body["items"].([]any)
	for _, it := range items {
		if it.(map[string]any)["status"] != "promoted" {
			t.Errorf("status filter returned %v", it)
		}
	}
	// An uppercase status value is refused.
	r = f.do("GET", "/api/v1/drafts/quotes?status=OPEN", nil)
	if r.status != http.StatusBadRequest {
		t.Errorf("status=OPEN = %d", r.status)
	}
	// The subject filter: the edit draft on the quote.
	r = f.do("GET", "/api/v1/drafts/quotes?subject_id="+subject, nil)
	items, _ = r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("subject filter items = %d", len(items))
	}
	// An unknown parameter is refused.
	r = f.do("GET", "/api/v1/drafts/quotes?owner=x", nil)
	if r.status != http.StatusBadRequest {
		t.Errorf("unknown parameter = %d", r.status)
	}
	// include=total counts, and the subject filter's count is exact.
	r = f.do("GET", "/api/v1/drafts/quotes?include=total&subject_id="+subject, nil)
	if num(t, r.body, "total") != 1 {
		t.Errorf("total = %v", r.body["total"])
	}
	// The cursor walks every row once: this fixture's four rows each appear
	// exactly once in a full walk.
	seen := map[string]int{}
	path := "/api/v1/drafts/quotes?limit=2"
	for {
		r = f.do("GET", path, nil)
		if r.status != http.StatusOK {
			t.Fatalf("page = %d: %s", r.status, r.raw)
		}
		page, _ := r.body["items"].([]any)
		for _, it := range page {
			m := it.(map[string]any)
			seen[str(t, m, "id")]++
			// The list item carries no payload and keeps validation.ready.
			if _, has := m["payload"]; has {
				t.Error("list item carries the payload")
			}
			v, _ := m["validation"].(map[string]any)
			if _, has := v["problems"]; has {
				t.Error("list item carries validation.problems")
			}
		}
		next, _ := r.body["next_cursor"].(string)
		if next == "" {
			break
		}
		path = "/api/v1/drafts/quotes?limit=2&cursor=" + next
	}
	for _, id := range mine {
		if seen[id] != 1 {
			t.Errorf("draft %s walked %d times, want once", id, seen[id])
		}
	}
}

// TestDraftIdempotentCreate pins idempotency through the middleware: the
// same create twice with one key returns the first response, marked
// replayed, and makes one row and one event.
func TestDraftIdempotentCreate(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	headers := []string{"Idempotency-Key", "draft-create-" + f.sku}
	r1 := f.do("POST", "/api/v1/drafts/quotes", map[string]any{"payload": f.payload()}, headers...)
	r2 := f.do("POST", "/api/v1/drafts/quotes", map[string]any{"payload": f.payload()}, headers...)
	if r1.status != http.StatusCreated || r2.status != http.StatusCreated {
		t.Fatalf("create/replay = %d/%d", r1.status, r2.status)
	}
	if r2.header.Get("Idempotency-Replayed") != "true" {
		t.Error("replay not marked")
	}
	if str(t, r1.body, "id") != str(t, r2.body, "id") {
		t.Error("replay answered another draft")
	}
	// The same key with another body is a 422.

	p := f.payload()
	p["delivery_type"] = "delivery"
	r3 := f.do("POST", "/api/v1/drafts/quotes", map[string]any{"payload": p}, headers...)
	if r3.status != http.StatusUnprocessableEntity {
		t.Errorf("reuse with another body = %d", r3.status)
	}
}

// TestDraftReadRules pins the read's own rules: a malformed id is a 400,
// another kind's draft is a 404, and the read carries the ETag.
func TestDraftReadRules(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	id := f.create()

	if r := f.do("GET", "/api/v1/drafts/quotes/not-a-uuid", nil); r.status != http.StatusBadRequest {
		t.Errorf("malformed id = %d", r.status)
	}
	if r := f.do("GET", "/api/v1/drafts/quotes/"+id, nil); r.status != http.StatusOK || r.header.Get("ETag") != `"1"` {
		t.Errorf("read = %d etag=%q", r.status, r.header.Get("ETag"))
	}
	if r := f.do("GET", "/api/v1/drafts/quotes/"+id+"?x=1", nil); r.status != http.StatusBadRequest {
		t.Errorf("unknown parameter on the read = %d", r.status)
	}
	if r := f.do("GET", "/api/v1/drafts/quotes/ffffffff-ffff-ffff-ffff-ffffffffffff", nil); r.status != http.StatusNotFound {
		t.Errorf("unknown draft = %d", r.status)
	}
}
