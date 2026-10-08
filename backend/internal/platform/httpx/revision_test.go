// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"net/http/httptest"
	"testing"
)

func rev(n int64) *int64 { return &n }

// RULE (ADR 0001 §11): an If-Match header carries the revision the client
// last saw, quoted, strong or weak; nothing else is a revision.
func TestIfMatchRevision(t *testing.T) {
	ok := []struct {
		raw  string
		want int64
	}{
		{`"3"`, 3},
		{`W/"3"`, 3},
		{`"0"`, 0},
		{`"9223372036854775807"`, 9223372036854775807},
	}
	for _, tc := range ok {
		got, ok := IfMatchRevision(tc.raw)
		if !ok || got != tc.want {
			t.Errorf("IfMatchRevision(%q) = %d, %v; want %d, true", tc.raw, got, ok, tc.want)
		}
	}
	for _, raw := range []string{
		``, `3`, `*`, `"abc"`, `"3`, `3"`, `""`, `" 3"`, `"3 "`, `"+3"`, `"03"`,
		`W/3`, `W/"3`, `"3","4"`, `"99999999999999999999"`,
	} {
		if got, ok := IfMatchRevision(raw); ok {
			t.Errorf("IfMatchRevision(%q) = %d, true; want not a revision", raw, got)
		}
	}
}

// RULE: the document's revision is written as the response ETag, in the
// strong quoted form If-Match reads.
func TestWriteRevisionETag(t *testing.T) {
	w := httptest.NewRecorder()
	WriteRevisionETag(w, 7)
	if got := w.Header().Get("ETag"); got != `"7"` {
		t.Errorf("ETag = %q, want the quoted revision", got)
	}
}

// RULE: a write needs the client's revision, through If-Match or the body;
// a missing revision is 428, a stale one 409 stale_revision, and a header
// that is not a revision, or two revisions that disagree, is a 400.
func TestCheckRevision(t *testing.T) {
	cases := []struct {
		name   string
		want   string
		status int
	}{
		{"no revision at all", CodePreconditionRequired, 428},
		{"stale If-Match", CodeStaleRevision, 409},
		{"stale body revision", CodeStaleRevision, 409},
		{"garbage If-Match", CodeBadRequest, 400},
		{"If-Match and body disagree", CodeBadRequest, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			switch tc.name {
			case "no revision at all":
				err = CheckRevision(5, "", nil)
			case "stale If-Match":
				err = CheckRevision(5, `"4"`, nil)
			case "stale body revision":
				err = CheckRevision(5, "", rev(4))
			case "garbage If-Match":
				err = CheckRevision(5, `not-a-revision`, nil)
			case "If-Match and body disagree":
				err = CheckRevision(5, `"5"`, rev(6))
			}
			e, ok := err.(*Error)
			if !ok {
				t.Fatalf("err = %v, want *Error", err)
			}
			if e.Code != tc.want || e.Status != tc.status {
				t.Errorf("code = %q status = %d, want %q %d", e.Code, e.Status, tc.want, tc.status)
			}
		})
	}
}

// RULE: matching revisions, by either carrier, pass.
func TestCheckRevisionPasses(t *testing.T) {
	if err := CheckRevision(5, `"5"`, nil); err != nil {
		t.Errorf("strong If-Match: %v", err)
	}
	if err := CheckRevision(5, `W/"5"`, nil); err != nil {
		t.Errorf("weak If-Match: %v", err)
	}
	if err := CheckRevision(5, "", rev(5)); err != nil {
		t.Errorf("body revision: %v", err)
	}
	if err := CheckRevision(5, `"5"`, rev(5)); err != nil {
		t.Errorf("both carriers agreeing: %v", err)
	}
}
