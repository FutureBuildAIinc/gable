// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func strictQueryRequest(t *testing.T, rawQuery string) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodGet, "/api/v1/things?"+rawQuery, nil)
}

// RULE (ADR 0001 §5): a route accepts exactly the parameters it declares;
// known names pass through with their values untouched.
func TestStrictQueryAllowsKnownParams(t *testing.T) {
	q, err := StrictQuery(strictQueryRequest(t, "status=sent&limit=10"), "status", "limit")
	if err != nil {
		t.Fatalf("StrictQuery: %v", err)
	}
	if q.Get("status") != "sent" || q.Get("limit") != "10" {
		t.Errorf("values = %v, want them passed through", q)
	}
}

// RULE: an empty query string is fine.
func TestStrictQueryEmpty(t *testing.T) {
	if _, err := StrictQuery(strictQueryRequest(t, ""), "status"); err != nil {
		t.Fatalf("StrictQuery on empty query: %v", err)
	}
}

// RULE: an unknown parameter name is a 400 unsupported_query_parameter
// naming the parameter, so a silent filter no-op cannot hide a client bug.
func TestStrictQueryRefusesUnknownParam(t *testing.T) {
	_, err := StrictQuery(strictQueryRequest(t, "status=sent&customerId=x"), "status")
	e := cursorErr(t, err) // same 400 shape contract as the cursor family
	if e.Code != CodeUnsupportedQueryParameter {
		t.Errorf("code = %q, want %q", e.Code, CodeUnsupportedQueryParameter)
	}
	if len(e.Details) != 1 || e.Details[0].Field != "customerId" {
		t.Errorf("details = %+v, want customerId named", e.Details)
	}
	if e.Details[0].Message != "unsupported query parameter" {
		t.Errorf("message = %q, want the fixed wording", e.Details[0].Message)
	}
}

// RULE: every unknown parameter is named in one response, not one per round
// trip, and in a deterministic order.
func TestStrictQueryNamesEveryUnknownParam(t *testing.T) {
	_, err := StrictQuery(strictQueryRequest(t, "zeta=1&alpha=2&beta=3"), "status")
	e := cursorErr(t, err)
	if len(e.Details) != 3 {
		t.Fatalf("details = %+v, want all three unknown names", e.Details)
	}
	if e.Details[0].Field != "alpha" || e.Details[1].Field != "beta" || e.Details[2].Field != "zeta" {
		t.Errorf("details = %+v, want sorted names", e.Details)
	}
}

// RULE: parameter names are case sensitive: Limit is not limit, and the
// misspelling is refused rather than ignored.
func TestStrictQueryIsCaseSensitive(t *testing.T) {
	_, err := StrictQuery(strictQueryRequest(t, "Limit=10"), "limit")
	e := cursorErr(t, err)
	if len(e.Details) != 1 || e.Details[0].Field != "Limit" {
		t.Errorf("details = %+v, want Limit named", e.Details)
	}
}

// RULE: a repeated known name is a value question for the handler (repeatable
// filters exist, for example a types= filter), not a name question for the
// guard: it passes.
func TestStrictQueryPassesRepeatedKnownParam(t *testing.T) {
	q, err := StrictQuery(strictQueryRequest(t, "types=a&types=b"), "types")
	if err != nil {
		t.Fatalf("StrictQuery: %v", err)
	}
	if got := q["types"]; len(got) != 2 {
		t.Errorf("types = %v, want both values", got)
	}
}

// RULE: the guard's error is the one error envelope like every other.
func TestStrictQueryErrorWritesEnvelope(t *testing.T) {
	_, err := StrictQuery(strictQueryRequest(t, "nope=1"), "status")
	w := httptest.NewRecorder()
	WriteError(w, writeErrorRequest(), err)
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	code, _, details, _ := decodeErrorBody(t, w)
	if code != CodeUnsupportedQueryParameter {
		t.Errorf("code = %q, want %q", code, CodeUnsupportedQueryParameter)
	}
	if len(details) != 1 || details[0].Field != "nope" {
		t.Errorf("details = %+v, want the parameter named", details)
	}
}
