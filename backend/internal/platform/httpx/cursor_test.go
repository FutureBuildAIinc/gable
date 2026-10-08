// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testScope = "quotes.created_at"

func cursorErr(t *testing.T, err error) *Error {
	t.Helper()
	if err == nil {
		t.Fatal("err = nil, want a 400")
	}
	e, ok := err.(*Error)
	if !ok {
		t.Fatalf("err is %T, want *Error", err)
	}
	if e.Status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", e.Status)
	}
	return e
}

// RULE (ADR 0001 §2): a minted cursor round trips with its ordering scope
// and its keyset tuple intact.
func TestCursorRoundTrip(t *testing.T) {
	raw, err := MintCursor(testScope, "2026-01-02T03:04:05Z", "3f9c2b1e")
	if err != nil {
		t.Fatalf("MintCursor: %v", err)
	}
	key, err := DecodeCursor(raw, testScope)
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if len(key) != 2 || key[0] != "2026-01-02T03:04:05Z" || key[1] != "3f9c2b1e" {
		t.Fatalf("key = %v, want the minted tuple", key)
	}
}

// RULE: the cursor is opaque: the encoding is base64url of JSON, never the
// bare key material.
func TestCursorIsOpaqueOnTheWire(t *testing.T) {
	raw, err := MintCursor(testScope, "sortvalue", "id")
	if err != nil {
		t.Fatalf("MintCursor: %v", err)
	}
	if strings.Contains(raw, "sortvalue") || strings.Contains(raw, testScope) {
		t.Errorf("cursor %q leaks its key or scope as plaintext", raw)
	}
	if _, err := base64.RawURLEncoding.DecodeString(raw); err != nil {
		t.Errorf("cursor %q is not unpadded base64url: %v", raw, err)
	}
}

// RULE: a present but malformed cursor is a 400, never silently the first
// page. Every corruption a client can send is refused.
func TestDecodeCursorRefusesMalformed(t *testing.T) {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	cases := []struct {
		name string
		raw  string
	}{
		{"not base64", "!!!not-base64!!!"},
		{"padded base64", strings.TrimRight(b64(`{"v":1,"o":"`+testScope+`","k":["a"]}`), "=") + "=="},
		{"not json", b64("hello")},
		{"json but not an object", b64(`[1,2]`)},
		{"unknown field", b64(`{"v":1,"o":"` + testScope + `","k":["a"],"x":1}`)},
		{"wrong version", b64(`{"v":2,"o":"` + testScope + `","k":["a"]}`)},
		{"missing keyset", b64(`{"v":1,"o":"` + testScope + `"}`)},
		{"empty keyset", b64(`{"v":1,"o":"` + testScope + `","k":[]}`)},
		{"non-string keyset part", b64(`{"v":1,"o":"` + testScope + `","k":[7]}`)},
		{"empty keyset part", b64(`{"v":1,"o":"` + testScope + `","k":[""]}`)},
		{"control character in part", b64(`{"v":1,"o":"` + testScope + `","k":["a\x00b"]}`)},
		{"newline in part", b64(`{"v":1,"o":"` + testScope + `","k":["a\nb"]}`)},
		{"too many parts", b64(`{"v":1,"o":"` + testScope + `","k":["1","2","3","4","5","6","7","8","9"]}`)},
		{"oversize payload", b64(`{"v":1,"o":"` + testScope + `","k":["` + strings.Repeat("a", maxCursorDecodedBytes) + `"]}`)},
		{"trailing bytes after the object", b64(`{"v":1,"o":"` + testScope + `","k":["a"]}garbage`)},
		{"trailing second value", b64(`{"v":1,"o":"` + testScope + `","k":["a"]} {"x":1}`)},
		{"trailing whitespace", b64(`{"v":1,"o":"` + testScope + `","k":["a"]} `)},
		{"uppercase field names", b64(`{"V":1,"O":"` + testScope + `","K":["a"]}`)},
		{"mixed case field names", b64(`{"v":1,"o":"` + testScope + `","k":["a"],"K":["b"]}`)},
		{"duplicate field", b64(`{"v":1,"v":1,"o":"` + testScope + `","k":["a"]}`)},
		{"non canonical spacing", b64(`{"v": 1,"o":"` + testScope + `","k":["a"]}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeCursor(tc.raw, testScope)
			e := cursorErr(t, err)
			if e.Code != CodeBadRequest {
				t.Errorf("code = %q, want %q", e.Code, CodeBadRequest)
			}
			if len(e.Details) != 1 || e.Details[0].Field != "cursor" {
				t.Errorf("details = %+v, want the cursor field named", e.Details)
			}
		})
	}
}

// RULE: a cursor minted under one ordering scope does not resume a list
// under a different one.
func TestDecodeCursorScopeMismatch(t *testing.T) {
	raw, err := MintCursor("orders.created_at", "x", "y")
	if err != nil {
		t.Fatalf("MintCursor: %v", err)
	}
	_, derr := DecodeCursor(raw, testScope)
	e := cursorErr(t, derr)
	if e.Code != CodeBadRequest {
		t.Errorf("code = %q, want %q", e.Code, CodeBadRequest)
	}
}

// RULE: MintCursor refuses to mint what DecodeCursor would refuse: the
// shapes must stay symmetric so no handler can mint an unloadable cursor.
func TestMintCursorValidatesItsInputs(t *testing.T) {
	if _, err := MintCursor("", "a"); err == nil {
		t.Error("empty scope minted")
	}
	if _, err := MintCursor("bad\nscope", "a"); err == nil {
		t.Error("scope with a control character minted")
	}
	if _, err := MintCursor(testScope); err == nil {
		t.Error("empty keyset minted")
	}
	if _, err := MintCursor(testScope, ""); err == nil {
		t.Error("empty key part minted")
	}
	if _, err := MintCursor(testScope, "a\nb"); err == nil {
		t.Error("newline in key part minted")
	}
	if _, err := MintCursor(testScope, strings.Repeat("a", maxCursorKeyPartBytes+1)); err == nil {
		t.Error("oversize key part minted")
	}
}

// RULE: the cursor carries nothing up its sleeve: minting the same scope and
// key twice yields the same token.
func TestCursorIsDeterministic(t *testing.T) {
	a, err := MintCursor(testScope, "x", "y")
	if err != nil {
		t.Fatal(err)
	}
	b, err := MintCursor(testScope, "x", "y")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("same inputs minted %q and %q", a, b)
	}
}

// RULE: MintCursor refuses to mint a cursor DecodeCursor would refuse on
// size. The bound is checked on the marshalled payload, not on the raw
// parts: many parts, or characters JSON escapes, grow the payload past the
// decode bound even when every part alone is within its own.
func TestMintCursorRefusesPayloadPastTheDecodeBound(t *testing.T) {
	parts := make([]string, maxCursorKeyParts)
	for i := range parts {
		parts[i] = strings.Repeat("a", maxCursorKeyPartBytes)
	}
	if _, err := MintCursor(testScope, parts...); err == nil {
		t.Error("eight full-size parts minted; the marshalled payload is past the decode bound")
	}
	// Each < becomes six bytes once JSON escapes it, so a 200 character key
	// marshals far past the bound while staying under the per-part limit.
	if _, err := MintCursor(testScope, strings.Repeat("<", 200)); err == nil {
		t.Error("escape-heavy key minted; the marshalled payload is past the decode bound")
	}
}

// RULE: the largest keyset the per-part rules allow that still fits the
// decoded bound mints, decodes, and hands back the same keyset: the mint
// and decode bounds agree.
func TestMintCursorLargestFittingKeyRoundTrips(t *testing.T) {
	fullParts := func(n int) []string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = strings.Repeat("a", maxCursorKeyPartBytes)
		}
		return parts
	}
	fits := 0
	for p := 1; p <= maxCursorKeyParts; p++ {
		payload, err := json.Marshal(cursorPayload{V: cursorVersion, O: testScope, K: fullParts(p)})
		if err != nil {
			t.Fatal(err)
		}
		if len(payload) > maxCursorDecodedBytes {
			break
		}
		fits = p
	}
	if fits == 0 {
		t.Fatal("not even one full-size part fits the decoded bound")
	}

	parts := fullParts(fits)
	raw, err := MintCursor(testScope, parts...)
	if err != nil {
		t.Fatalf("largest fitting keyset did not mint: %v", err)
	}
	key, err := DecodeCursor(raw, testScope)
	if err != nil {
		t.Fatalf("largest fitting keyset did not decode: %v", err)
	}
	if len(key) != fits {
		t.Fatalf("key has %d parts, want %d", len(key), fits)
	}
	for i, part := range key {
		if part != parts[i] {
			t.Errorf("key[%d] differs from the minted part", i)
		}
	}
}

func pageRequest(t *testing.T, rawQuery string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/things?"+rawQuery, nil)
	return r
}

// RULE: no parameters is the first page at the default limit.
func TestParseListQueryDefaults(t *testing.T) {
	page, err := ParseListQuery(pageRequest(t, ""), testScope)
	if err != nil {
		t.Fatalf("ParseListQuery: %v", err)
	}
	if page.Limit != DefaultPageLimit {
		t.Errorf("limit = %d, want %d", page.Limit, DefaultPageLimit)
	}
	if page.Key != nil {
		t.Errorf("key = %v, want nil on the first page", page.Key)
	}
}

// RULE: limit is an integer in [1, MaxPageLimit]; 1 and the max are valid.
func TestParseListQueryLimitBounds(t *testing.T) {
	for _, raw := range []string{"limit=1", "limit=200"} {
		page, err := ParseListQuery(pageRequest(t, raw), testScope)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if page.Limit != map[string]int{"limit=1": 1, "limit=200": 200}[raw] {
			t.Errorf("%s: limit = %d", raw, page.Limit)
		}
	}
}

// RULE (ADR 0001 §2, strictness): a limit the server will not honor is a
// 400 naming the field, not a silent clamp. Unparseable is bad_request;
// out of range is validation_failed.
func TestParseListQueryLimitRefused(t *testing.T) {
	cases := []struct {
		raw  string
		code string
	}{
		{"limit=0", CodeValidationFailed},
		{"limit=-5", CodeValidationFailed},
		{"limit=201", CodeValidationFailed},
		{"limit=abc", CodeBadRequest},
		{"limit=12.5", CodeBadRequest},
		{"limit=", CodeBadRequest},
		{"limit=%2B5", CodeBadRequest},  // a plus sign
		{"limit=%205", CodeBadRequest},  // leading space
		{"limit=5%20", CodeBadRequest},  // trailing space
		{"limit=05", CodeBadRequest},    // leading zero
		{"limit=1e1", CodeBadRequest},   // exponent notation
		{"limit=" + strings.Repeat("9", 30), CodeValidationFailed}, // past int range, a range refusal
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			_, err := ParseListQuery(pageRequest(t, tc.raw), testScope)
			e := cursorErr(t, err)
			if e.Code != tc.code {
				t.Errorf("code = %q, want %q", e.Code, tc.code)
			}
			if len(e.Details) != 1 || e.Details[0].Field != "limit" {
				t.Errorf("details = %+v, want the limit field named", e.Details)
			}
		})
	}
}

// RULE: a valid cursor resumes with its keyset decoded.
func TestParseListQueryWithCursor(t *testing.T) {
	raw, err := MintCursor(testScope, "zzz", "aaa")
	if err != nil {
		t.Fatal(err)
	}
	page, err := ParseListQuery(pageRequest(t, "cursor="+raw+"&limit=25"), testScope)
	if err != nil {
		t.Fatalf("ParseListQuery: %v", err)
	}
	if len(page.Key) != 2 || page.Key[0] != "zzz" {
		t.Errorf("key = %v, want the decoded tuple", page.Key)
	}
	if page.Limit != 25 {
		t.Errorf("limit = %d, want 25", page.Limit)
	}
}

// RULE: a repeated cursor parameter is ambiguous and refused.
func TestParseListQueryDuplicateCursor(t *testing.T) {
	raw, _ := MintCursor(testScope, "a")
	_, err := ParseListQuery(pageRequest(t, "cursor="+raw+"&cursor="+raw), testScope)
	cursorErr(t, err)
}

// RULE: a repeated limit parameter is ambiguous and refused.
func TestParseListQueryDuplicateLimit(t *testing.T) {
	_, err := ParseListQuery(pageRequest(t, "limit=10&limit=20"), testScope)
	e := cursorErr(t, err)
	if len(e.Details) != 1 || e.Details[0].Field != "limit" {
		t.Errorf("details = %+v, want the limit field named", e.Details)
	}
}
