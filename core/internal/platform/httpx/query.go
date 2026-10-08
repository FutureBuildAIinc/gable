// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// StrictQuery checks a request's query parameters against the exact set the
// route declares (ADR 0001 §5) and returns them untouched when every name is
// known.
//
// A request carrying a parameter name the route does not accept gets a 400
// unsupported_query_parameter naming every unknown parameter, sorted, in one
// response. Silent filter no-ops are the failure this replaces: the inputs
// record a live ?status=sent that returned drafts because the filter was
// never implemented and the parameter was quietly ignored.
//
// The guard checks names only. Values (an empty status, a status outside the
// route's vocabulary) are the handler's validation, through Validator.Enum.
// A repeated known name passes with all its values, so repeatable filters
// (a types= filter) keep working; parameter names are case sensitive, and a
// misspelling (Limit for limit) is refused, not ignored.
func StrictQuery(r *http.Request, allowed ...string) (url.Values, error) {
	q := r.URL.Query()
	if len(q) == 0 {
		return q, nil
	}

	known := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		known[name] = true
	}

	var unknown []FieldError
	for name := range q {
		if !known[name] {
			unknown = append(unknown, FieldError{Field: name, Message: "unsupported query parameter"})
		}
	}
	if len(unknown) > 0 {
		// Map iteration order is random; sort so two callers sending the
		// same junk get byte-identical envelopes.
		sort.Slice(unknown, func(i, j int) bool { return unknown[i].Field < unknown[j].Field })
		return nil, &Error{
			Status:  http.StatusBadRequest,
			Code:    CodeUnsupportedQueryParameter,
			Message: "one or more query parameters are not supported by this route",
			Details: unknown,
		}
	}
	return q, nil
}

// IncludeTotal is the include name the package reserves: the ?include=total
// path of the list envelope (ADR 0001 §1). Routes add their own names to
// the parser's allowed list as they gain expansions.
const IncludeTotal = "total"

// IncludeSet is the parsed ?include= value: the names the request asked
// for, spelled exactly as the route declares them.
type IncludeSet struct {
	names map[string]bool
}

// Has reports whether the request asked for this include name.
func (s IncludeSet) Has(name string) bool { return s.names[name] }

// ParseInclude parses the comma separated include list against the names
// the route offers, plus the package's reserved names. Every entry must be
// a known name, spelled exactly: case variants, whitespace, empty entries,
// and repeats are a 400 validation_failed naming the include field, the
// same posture as every other parameter value this package checks.
func ParseInclude(raw string, allowed ...string) (IncludeSet, error) {
	known := map[string]bool{IncludeTotal: true}
	for _, name := range allowed {
		known[name] = true
	}

	set := IncludeSet{names: map[string]bool{}}
	for _, name := range strings.Split(raw, ",") {
		if !known[name] || set.names[name] {
			return IncludeSet{}, &Error{
				Status:  http.StatusBadRequest,
				Code:    CodeValidationFailed,
				Message: "include carries an unsupported name",
				Details: []FieldError{{Field: "include",
					Message: "must be a comma separated list of: " + strings.Join(sortedKeys(known), ", ")}},
			}
		}
		set.names[name] = true
	}
	return set, nil
}

// sortedKeys gives the parser's error message a stable, readable name list.
func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
