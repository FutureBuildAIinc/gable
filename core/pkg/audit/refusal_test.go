// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package audit_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// A refused request whose path held a NUL byte must record the marker
// visibly AND mark the row as sanitised, so an investigator can tell a
// NUL byte from a caller's literal `\u0000` text. A clean refusal path
// must NOT carry the flag.
func TestAuditKeyRefusal_FlagsPathAsSanitisedWhenNULPresent(t *testing.T) {
	db := testutil.RequireDB(t)
	logger := audit.NewLogger(db)

	// A path with a NUL byte: the row must mark path_sanitised.
	keyID := uuid.New()
	logger.AuditKeyRefusal(context.Background(), keyID.String(), "key.scope_refused", "admin:settings", "GET", "/api/v1/admin/\x00")
	var sanitised, truncated bool
	var path string
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT changes->>'path',
		        COALESCE((changes->>'path_sanitised')::boolean, false),
		        COALESCE((changes->>'path_truncated')::boolean, false)
		   FROM audit_log WHERE entity_id = $1 AND action = 'key.scope_refused'`, keyID).
		Scan(&path, &sanitised, &truncated); err != nil {
		t.Fatalf("no key.scope_refused row: %v", err)
	}
	if strings.ContainsRune(path, '\x00') {
		t.Errorf("stored path still contains a NUL byte: %q", path)
	}
	if !strings.Contains(path, `\u0000`) {
		t.Errorf("stored path %q does not contain the visible marker", path)
	}
	if !sanitised {
		t.Errorf("a NUL in the path must mark the row as sanitised; path_sanitised = false")
	}
	if truncated {
		t.Errorf("a 22-byte path with one NUL must not be marked as truncated; path_truncated = true")
	}

	// A clean path: no flag, no truncation, the path verbatim.
	keyID = uuid.New()
	logger.AuditKeyRefusal(context.Background(), keyID.String(), "key.scope_refused", "admin:settings", "GET", "/api/v1/admin/settings/ai")
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT changes->>'path',
		        COALESCE((changes->>'path_sanitised')::boolean, false),
		        COALESCE((changes->>'path_truncated')::boolean, false)
		   FROM audit_log WHERE entity_id = $1 AND action = 'key.scope_refused'`, keyID).
		Scan(&path, &sanitised, &truncated); err != nil {
		t.Fatalf("no key.scope_refused row: %v", err)
	}
	if path != "/api/v1/admin/settings/ai" {
		t.Errorf("clean path stored as %q, want it verbatim", path)
	}
	if sanitised {
		t.Errorf("clean path must not be marked sanitised; path_sanitised = true")
	}
	if truncated {
		t.Errorf("clean path must not be marked truncated; path_truncated = true")
	}
}

// The 512 byte cap on the stored path must be applied AFTER the sanitiser
// has run, not before. A 256 byte path of NULs would fit pre-sanitise at
// 256 bytes but grow to 1536 bytes post-sanitise (each NUL becomes six
// characters). Cutting pre-sanitise lets the stored row exceed the cap;
// cutting post-sanitise keeps the stored row at 512 bytes. The cap is
// stated in maxRefusalPathBytes; the comment on the constant must say so.
func TestAuditKeyRefusal_TruncationIsAfterSanitising(t *testing.T) {
	db := testutil.RequireDB(t)
	logger := audit.NewLogger(db)

	// 256 NUL bytes pre-sanitise = 256 bytes; post-sanitise = 1536 bytes.
	// The cap is 512 bytes, so the stored path must be at most 512 bytes,
	// path_truncated must be true, and the path must contain the marker.
	keyID := uuid.New()
	path256 := strings.Repeat("\x00", 256)
	logger.AuditKeyRefusal(context.Background(), keyID.String(), "key.scope_refused", "admin:settings", "GET", path256)

	var path string
	var truncated, sanitised bool
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT changes->>'path',
		        COALESCE((changes->>'path_truncated')::boolean, false),
		        COALESCE((changes->>'path_sanitised')::boolean, false)
		   FROM audit_log WHERE entity_id = $1 AND action = 'key.scope_refused'`, keyID).
		Scan(&path, &truncated, &sanitised); err != nil {
		t.Fatalf("no key.scope_refused row: %v", err)
	}
	if len(path) > 512 {
		t.Errorf("stored path is %d bytes, want at most 512 (truncation must happen after sanitising)", len(path))
	}
	if !truncated {
		t.Errorf("256 NUL bytes pre-sanitise must be marked as truncated post-sanitise; path_truncated = false")
	}
	if !sanitised {
		t.Errorf("a path of NUL bytes must be marked as sanitised; path_sanitised = false")
	}
	if strings.ContainsRune(path, '\x00') {
		t.Errorf("stored path still contains a NUL byte: %q", path)
	}
}

// The 512 byte cap is applied on a rune boundary, but the six character
// marker `\u0000` is six ASCII chars (each is its own rune). A cut that
// lands inside a marker would store a path ending with a partial marker
// (the first 1 to 5 chars of `\u0000`): plain text, valid UTF-8, at most
// 512 bytes, flagged both `path_sanitised` and `path_truncated` — so not
// harmful, but ambiguous to a reader. The fix steps the cut back to before
// the partial marker. The cap may be undershot (the path becomes shorter
// than 512), which is allowed.
func TestAuditKeyRefusal_TruncationDoesNotEndInsideMarker(t *testing.T) {
	db := testutil.RequireDB(t)
	logger := audit.NewLogger(db)

	cases := []struct {
		name string
		path string
	}{
		// 509 `a` then one NUL: post-sanitise the marker adds six
		// chars to 509, total 515. A 512 byte cut lands 3 chars into
		// the marker, leaving a stored path ending `\u0`. The fix
		// steps the cut back so the path ends before the marker.
		{name: "cut lands 3 chars into marker (\\u0)",
			path: strings.Repeat("a", 509) + "\x00"},
		// 510 `a` then one NUL: post-sanitise 516. Cut at 512 lands
		// 2 chars into the marker, ending `\u`. The fix steps back.
		{name: "cut lands 2 chars into marker (\\u)",
			path: strings.Repeat("a", 510) + "\x00"},
		// 511 `a` then one NUL: post-sanitise 517. Cut at 512 lands
		// 1 char into the marker, ending `\`. The fix steps back.
		{name: "cut lands 1 char into marker (\\)",
			path: strings.Repeat("a", 511) + "\x00"},
		// 600 NULs: post-sanitise 3600. 85 full markers = 510 chars,
		// the 86th marker contributes 2 chars (`\u`). Stored path
		// ends `\u`. The fix steps back to the last full marker.
		{name: "cut lands in many markers, ending \\u",
			path: strings.Repeat("\x00", 600)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keyID := uuid.New()
			logger.AuditKeyRefusal(context.Background(), keyID.String(),
				"key.scope_refused", "admin:settings", "GET", tc.path)

			var storedPath string
			if err := db.Pool.QueryRow(context.Background(),
				`SELECT changes->>'path' FROM audit_log
				   WHERE entity_id = $1 AND action = 'key.scope_refused'`, keyID).
				Scan(&storedPath); err != nil {
				t.Fatalf("no key.scope_refused row: %v", err)
			}
			if len(storedPath) > 512 {
				t.Errorf("stored path is %d bytes, want at most 512", len(storedPath))
			}
			if strings.ContainsRune(storedPath, '\x00') {
				t.Errorf("stored path still contains a NUL byte: %q", storedPath)
			}
			// The stored path must not end inside the marker: the
			// last 1 to 5 chars must not be the first 1 to 5 chars
			// of `\u0000`. A full marker (all 6 chars) is fine; an
			// empty suffix is also fine.
			marker := `\u0000`
			for n := 1; n <= 5; n++ {
				if len(storedPath) >= n && strings.HasSuffix(storedPath, marker[:n]) {
					t.Errorf("stored path %q ends inside the marker (last %d chars match the first %d chars of %q)",
						storedPath, n, n, marker)
				}
			}
		})
	}
}
