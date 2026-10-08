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

// RULE (ADR 0001 §1): the include parameter is a comma separated list;
// total is reserved by the package, and the route adds its own names.
func TestParseInclude(t *testing.T) {
	s, err := ParseInclude("total")
	if err != nil {
		t.Fatalf("ParseInclude(total): %v", err)
	}
	if !s.Has(IncludeTotal) {
		t.Error("total not in the set")
	}
	if s.Has("product_summary") {
		t.Error("product_summary in the set without being asked for")
	}

	s, err = ParseInclude("total,product_summary", "product_summary")
	if err != nil {
		t.Fatalf("ParseInclude with a route name: %v", err)
	}
	if !s.Has(IncludeTotal) || !s.Has("product_summary") {
		t.Errorf("set = %+v, want both names", s)
	}
}

// RULE: an include value the route does not offer is a 400 naming the
// include field, in the envelope like every other refusal. Names are case
// sensitive, empty entries and repeats are refused, and nothing is trimmed.
func TestParseIncludeRefuses(t *testing.T) {
	cases := []string{
		"",
		"total,",
		",total",
		"total,,x",
		"total,total",
		"Total",
		" total",
		"total ",
		"total,product_summary", // product_summary not offered by this route
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			_, err := ParseInclude(raw)
			e := cursorErr(t, err)
			if e.Code != CodeValidationFailed {
				t.Errorf("code = %q, want %q", e.Code, CodeValidationFailed)
			}
			if len(e.Details) != 1 || e.Details[0].Field != "include" {
				t.Errorf("details = %+v, want the include field named", e.Details)
			}
		})
	}
}
