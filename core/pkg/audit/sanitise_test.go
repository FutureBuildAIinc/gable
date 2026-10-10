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
// a NUL in a text column is also rejected, so the writer replaces both
// before the marshalled row reaches the database (a NUL with the visible
// marker `\u0000` and an invalid byte with U+FFFD). The unit test
// exercises the three input shapes the audit trail meets: ordinary text,
// a NUL byte, and invalid UTF-8.

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

// sanitiseNULEscape operates on the bytes json.Marshal writes for a changes
// value. A NUL byte in the input becomes the six bytes `\u0000` in the
// marshalled output, which the jsonb parser rejects with SQLSTATE 22P05.
// The function rewrites every `\u0000` it finds to the seven byte marker
// `\\u0000` so the jsonb parser reads the six characters `\u0000` and no
// NUL byte reaches the jsonb value.
//
// The trick: a NUL byte in the source marshals to `\u0000` with no preceding
// backslash, while a caller's literal `\u0000` text marshals to `\\u0000`
// (each `\` becomes `\\`). The function MUST skip the trailing six bytes
// of `\\u0000` so genuine caller text is stored unaltered. The skip is
// wrong when it counts only ONE byte: a real NUL after one or more input
// backslashes marshals to TWO, FOUR or SIX backslashes before `\u0000`,
// and the byte-before test fires for every pair, losing the row. The fix
// counts the run of backslashes before the match and skips only when the
// run is ODD (the match's own leading `\` is itself escaped). This table
// pins the fix: rows for 0 to 3 input backslashes before a real NUL (the
// even-run rewrite must fire), and rows for genuine `\u0000` text with 0
// to 3 extra backslashes (must be stored unaltered).
//
// Test inputs are the marshalled byte sequences. Each Go raw-string `\` is
// one byte. A NUL byte in the source marshals to the six bytes `\u0000`
// with NO preceding backslash; an input backslash marshals to `\\`, so a
// source with n backslashes before the NUL produces 2n backslashes before
// the `\u0000` in the marshalled bytes. A genuine `\u0000` text in the
// source marshals to `\\u0000` (one source `\` becomes two marshalled
// backslashes), so a source with n extra `\` chars produces 2n+1
// backslashes before the `\u0000` match (always odd).
func TestSanitiseNULEscape_BackslashRunBeforeRealNULIsRewritten(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Real NUL: 0 source backslashes before it. Marshalled bytes
		// have 0 backslashes before the `\u0000` match (even).
		{name: "real NUL, 0 input backslashes",
			in:   `{"v":"a\u0000b"}`,
			want: `{"v":"a\\u0000b"}`},
		// Real NUL: 1 source backslash before it. Marshalled bytes
		// have 2 backslashes before the match (even).
		{name: "real NUL, 1 input backslash",
			in:   `{"v":"a\\\u0000b"}`,
			want: `{"v":"a\\\\u0000b"}`},
		// Real NUL: 2 source backslashes before it. Marshalled bytes
		// have 4 backslashes before the match (even).
		{name: "real NUL, 2 input backslashes",
			in:   `{"v":"a\\\\\u0000b"}`,
			want: `{"v":"a\\\\\\u0000b"}`},
		// Real NUL: 3 source backslashes before it. Marshalled bytes
		// have 6 backslashes before the match (even).
		{name: "real NUL, 3 input backslashes",
			in:   `{"v":"a\\\\\\\u0000b"}`,
			want: `{"v":"a\\\\\\\\u0000b"}`},
		// Genuine `\u0000` text in source (6 chars). Marshalled bytes
		// have 2 backslashes before the match (the source `\` doubled);
		// the run is 2 (even). The current code skips because the byte
		// before the match is `\`. The fix skips only when odd, so 2 is
		// even and would REWRITE. That is wrong: the genuine text MUST
		// be stored unaltered. Hold on: the actual marshalled form has
		// TWO `\\` then `u0000`. The match is `\u0000`, found at the
		// second `\`. Before it: one `\` (the first of `\\`). k = 1
		// (odd). Skip. So this case has 1 backslash before the match.
		{name: "genuine \\u0000 text, 0 extra",
			in:   `{"v":"a\\u0000b"}`,
			want: `{"v":"a\\u0000b"}`},
		// Genuine text: 1 extra source `\` (source `\\u0000`, 7 chars).
		// Marshalled bytes have 4 backslashes then `u0000`; the match
		// sits after THREE backslashes. k = 3 (odd). Skip.
		{name: "genuine \\u0000 text, 1 extra",
			in:   `{"v":"a\\\\u0000b"}`,
			want: `{"v":"a\\\\u0000b"}`},
		// Genuine text: 2 extra source `\` (source `\\\u0000`, 8 chars).
		// Marshalled bytes have 6 backslashes then `u0000`; the match
		// sits after FIVE backslashes. k = 5 (odd). Skip.
		{name: "genuine \\u0000 text, 2 extra",
			in:   `{"v":"a\\\\\\u0000b"}`,
			want: `{"v":"a\\\\\\u0000b"}`},
		// Genuine text: 3 extra source `\` (source `\\\\u0000`, 9 chars).
		// Marshalled bytes have 8 backslashes then `u0000`; the match
		// sits after SEVEN backslashes. k = 7 (odd). Skip.
		{name: "genuine \\u0000 text, 3 extra",
			in:   `{"v":"a\\\\\\\\u0000b"}`,
			want: `{"v":"a\\\\\\\\u0000b"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(sanitiseNULEscape([]byte(tc.in)))
			if got != tc.want {
				t.Errorf("sanitiseNULEscape(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
