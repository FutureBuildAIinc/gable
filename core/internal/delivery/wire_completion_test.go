// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package delivery_test

// Defect 2 of the PR 80 reviews, pinned on the wire end to end. PR 80
// review round 1 P1-3 found that TransitionRoute accepted `completed`
// from a DRAFT route and re-completed an already COMPLETED route,
// writing a second route.completed event downstream callers could not
// de-duplicate. The route completion state machine now refuses
// completion from any status other than IN_TRANSIT, with the module's
// "invalid_state" blocker, so only an in_transit route completes,
// once.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// detailsByCode reports which code values appear in the envelope's
// details, since the invalid_state refusal's details carry a Code, not
// a Field.
func (f *fixture) detailsByCode(t *testing.T, r resp) map[string]bool {
	t.Helper()
	env, _ := r.body["error"].(map[string]any)
	details, _ := env["details"].([]any)
	out := map[string]bool{}
	for _, d := range details {
		if m, ok := d.(map[string]any); ok {
			if code, ok := m["code"].(string); ok {
				out[code] = true
			}
		}
	}
	return out
}

// Defect 2 (PR 80 review round 1 P1-3): a draft route cannot
// complete. The base accepted `completed` from any non-cancelled
// status, which let a draft route complete and write a
// route.completed event downstream callers could not de-duplicate.
// The fix is a state machine check: only IN_TRANSIT can complete,
// every other status answers 409 invalid_state.
func TestRouteComplete_FromDraftIs409InvalidState(t *testing.T) {
	f := newFixture(t)
	route, _, _ := f.seedStop(t, f.branch)

	// Count the events before the refused complete so the assertion
	// is on the delta, not the seeded route's life history.
	before := f.countEvents(t, route, "route.completed")

	res := f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/transitions",
		`{"to":"completed","revision":1}`, nil)
	if res.status != http.StatusConflict {
		t.Fatalf("complete from DRAFT = %d %s, want 409", res.status, res.raw)
	}
	if got := f.errCode(t, res); got != httpx.CodeInvalidStateTransition {
		t.Errorf("code = %s, want %s", got, httpx.CodeInvalidStateTransition)
	}
	if !f.detailsByCode(t, res)["invalid_state"] {
		t.Errorf("blockers = %v, want invalid_state", f.detailsByCode(t, res))
	}

	// No route.completed event written despite the attempt.
	if got := f.countEvents(t, route, "route.completed"); got != before {
		t.Errorf("route.completed events before=%d after=%d; the refusal must not write one", before, got)
	}
}

// Defect 2 (PR 80 review round 1 P1-3): completing an already
// COMPLETED route is refused with 409 invalid_state. The base
// accepted a second complete and wrote a second route.completed
// event.
func TestRouteComplete_FromCompletedIs409InvalidState(t *testing.T) {
	f := newFixture(t)
	route, stop, _ := f.seedStop(t, f.branch)

	// Drive the route through dispatch, fail the only stop, then
	// complete.
	if res := f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/transitions",
		`{"to":"in_transit","revision":1}`, nil); res.status != http.StatusOK {
		t.Fatalf("dispatch = %d %s", res.status, res.raw)
	}
	if res := f.do(t, http.MethodPost, "/api/v1/delivery/deliveries/"+stop.String()+"/transitions",
		`{"to":"failed","revision":1}`, nil); res.status != http.StatusOK {
		t.Fatalf("stop failed = %d %s", res.status, res.raw)
	}
	// After dispatch, the route's revision is 2; stop failed moves
	// the stop's revision, not the route's. Complete from in_transit
	// succeeds once: revision moves 2 -> 3.
	res := f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/transitions",
		`{"to":"completed","revision":2}`, nil)
	if res.status != http.StatusOK {
		t.Fatalf("first complete = %d %s, want 200", res.status, res.raw)
	}

	// A second complete from COMPLETED is refused; the
	// route.completed event count must stay at exactly one.
	if got := f.countEvents(t, route, "route.completed"); got != 1 {
		t.Fatalf("route.completed events after first complete = %d, want 1", got)
	}

	res = f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/transitions",
		`{"to":"completed","revision":3}`, nil)
	if res.status != http.StatusConflict {
		t.Fatalf("complete from COMPLETED = %d %s, want 409", res.status, res.raw)
	}
	if got := f.errCode(t, res); got != httpx.CodeInvalidStateTransition {
		t.Errorf("code = %s, want %s", got, httpx.CodeInvalidStateTransition)
	}
	if !f.detailsByCode(t, res)["invalid_state"] {
		t.Errorf("blockers = %v, want invalid_state", f.detailsByCode(t, res))
	}

	// The refused second complete writes no second route.completed
	// event.
	if got := f.countEvents(t, route, "route.completed"); got != 1 {
		t.Errorf("route.completed events after refused re-complete = %d, want 1", got)
	}
}

// Defect 2 (PR 80 review round 1 P1-3): the allowed path. An
// in_transit route with every stop terminal can complete exactly
// once and writes the route.completed event. The test runs the
// production dispatch + complete sequence to pin the allowed
// transition against the database.
func TestRouteComplete_FromInTransitAllowedOnce(t *testing.T) {
	f := newFixture(t)
	route, stop, _ := f.seedStop(t, f.branch)

	// Dispatch the route and then the stop. The completion path
	// needs an in_transit route with every stop terminal.
	if res := f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/transitions",
		`{"to":"in_transit","revision":1}`, nil); res.status != http.StatusOK {
		t.Fatalf("dispatch = %d %s", res.status, res.raw)
	}
	if res := f.do(t, http.MethodPost, "/api/v1/delivery/deliveries/"+stop.String()+"/transitions",
		`{"to":"delivered","revision":1,"pod_proof_url":"https://x/y.png","pod_signed_by":"x"}`, nil); res.status != http.StatusOK {
		t.Fatalf("stop delivered = %d %s", res.status, res.raw)
	}

	res := f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/transitions",
		`{"to":"completed","revision":2}`, nil)
	if res.status != http.StatusOK {
		t.Fatalf("complete from IN_TRANSIT = %d %s", res.status, res.raw)
	}
	if res.body["status"] != "completed" {
		t.Errorf("status = %v, want completed", res.body["status"])
	}

	if got := f.countEvents(t, route, "route.completed"); got != 1 {
		t.Errorf("route.completed events = %d, want 1", got)
	}

	// Defect 2's silent hazard: a replay that answered 200 wrote a
	// second route.completed event. The next call from the same
	// client (a stale retry, a double-click) must be refused.
	res = f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/transitions",
		`{"to":"completed","revision":3}`, nil)
	if res.status != http.StatusConflict {
		t.Fatalf("replay = %d %s, want 409", res.status, res.raw)
	}
	if got := f.errCode(t, res); got != httpx.CodeInvalidStateTransition {
		t.Errorf("replay code = %s, want invalid_state_transition", got)
	}
}
