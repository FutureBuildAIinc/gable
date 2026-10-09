// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package audit

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The audit writer is the single shared place that prepares every string
// going into an audit_log row for Postgres text and jsonb. A caller
// controlled value (the refused path, the scope) can carry a NUL byte or
// invalid UTF-8; the jsonb parser rejects \u0000 with SQLSTATE 22P05, and
// a NUL in a text column is also rejected, so the writer strips both before
// the marshalled row reaches the database. The unit test exercises the
// three input shapes the audit trail meets: ordinary text, a NUL byte, and
// invalid UTF-8.

func TestSanitiseString_OrdinaryTextIsUnchanged(t *testing.T) {
	cases := []string{
		"/api/v1/admin/settings/ai",
		"quotes:read",
		"",
		"a",
		"key-name-123",
	}
	for _, in := range cases {
		if got := sanitiseString(in); got != in {
			t.Errorf("sanitiseString(%q) = %q, want it unchanged", in, got)
		}
	}
}

func TestSanitiseString_NULIsReplacedVisibly(t *testing.T) {
	in := "/api/v1/admin/\x00"
	got := sanitiseString(in)
	if strings.ContainsRune(got, '\x00') {
		t.Fatalf("sanitiseString kept a NUL byte: %q", got)
	}
	if !strings.Contains(got, `\u0000`) {
		t.Fatalf("sanitiseString(%q) = %q, want the NUL replaced visibly (the literal text \\u0000)", in, got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("sanitiseString(%q) = %q, want valid UTF-8", in, got)
	}
	// The marker is appended in place; the surrounding bytes survive so the
	// row still tells the operator where in the path the NUL sat.
	if !strings.HasPrefix(got, "/api/v1/admin/") {
		t.Fatalf("sanitiseString(%q) = %q, want the path prefix kept", in, got)
	}
}

func TestSanitiseString_MultipleNULsAreAllReplaced(t *testing.T) {
	in := "\x00api\x00/v1\x00"
	got := sanitiseString(in)
	if strings.ContainsRune(got, '\x00') {
		t.Fatalf("sanitiseString kept a NUL byte: %q", got)
	}
	if strings.Count(got, `\u0000`) != 3 {
		t.Fatalf("sanitiseString(%q) = %q, want 3 \\u0000 markers", in, got)
	}
}

func TestSanitiseString_InvalidUTF8IsReplacedWithFFFD(t *testing.T) {
	// 0xFF alone is not valid UTF-8: a continuation byte without a leader.
	in := "/api/v1/\xff"
	got := sanitiseString(in)
	if !utf8.ValidString(got) {
		t.Fatalf("sanitiseString produced invalid UTF-8: %q", got)
	}
	if strings.ContainsRune(got, '\uFFFD') != true {
		// The replacement character is what utf8.DecodeRuneInString yields
		// for a single bad byte; the writer's contract is "invalid UTF-8
		// becomes the replacement character", so a row stays valid for
		// Postgres text and jsonb both.
		t.Fatalf("sanitiseString(%q) = %q, want the U+FFFD replacement character", in, got)
	}
}

func TestSanitiseString_OrdinaryMultibyteIsPreserved(t *testing.T) {
	in := "/api/v1/quotes/\u20ac" // the euro sign, three bytes
	got := sanitiseString(in)
	if got != in {
		t.Errorf("sanitiseString(%q) = %q, want ordinary multibyte preserved", in, got)
	}
}