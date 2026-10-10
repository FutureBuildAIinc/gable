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

// The audit writer is the single shared place that prepares every string
// going into an audit_log row for Postgres text and jsonb. A NUL byte or
// invalid UTF-8 anywhere in the row (any string column, any depth of the
// changes map, any key name in the changes map, any value of any type the
// jsonb column accepts) must not lose the row. Each case below calls
// audit.Logger.Log against Postgres and asserts the row lands; before the
// sanitiser is made total these calls return a SQLSTATE 22P05 ("unsupported
// Unicode escape sequence") or 22021 ("invalid byte sequence") and the row
// is lost. The test runs as a table so a future shape that was missed here
// (a new type the jsonb column accepts, for instance) is one more row.

type nulShape struct {
	name    string
	changes map[string]interface{}
}

func TestLog_NULInAnyShapeStillWritesRow(t *testing.T) {
	db := testutil.RequireDB(t)
	logger := audit.NewLogger(db)

	// Every shape the reviewers listed: top level string, nested map,
	// []string, []interface{}, map[string]string, a map key, a struct field.
	// The struct field goes through json.Marshal (a struct's exported fields
	// become JSON keys and values) and is exercised by the same code path.
	shapes := []nulShape{
		{
			name: "top level string value",
			changes: map[string]interface{}{
				"path": "/api/v1/admin/a\x00b",
			},
		},
		{
			name: "nested map[string]interface{}",
			changes: map[string]interface{}{
				"outer": map[string]interface{}{"inner": "c\x00d"},
			},
		},
		{
			name: "[]string",
			changes: map[string]interface{}{
				"list": []string{"e\x00f"},
			},
		},
		{
			name: "[]interface{}",
			changes: map[string]interface{}{
				"list": []interface{}{"g\x00h"},
			},
		},
		{
			name: "map[string]string",
			changes: map[string]interface{}{
				"sub": map[string]string{"k": "i\x00j"},
			},
		},
		{
			name: "map key containing NUL",
			changes: map[string]interface{}{
				"k\x00y": "v",
			},
		},
		{
			name: "struct field holding a string",
			changes: map[string]interface{}{
				"sub": struct{ Name string }{Name: "k\x00l"},
			},
		},
	}

	for _, tc := range shapes {
		t.Run(tc.name, func(t *testing.T) {
			entityID := uuid.New()
			err := logger.Log(context.Background(), audit.Entry{
				Action:     "key.scope_refused",
				EntityType: "api_key",
				EntityID:   entityID,
				Changes:    tc.changes,
			})
			if err != nil {
				t.Fatalf("Log returned %v, want nil: a NUL in shape %q must not lose the row", err, tc.name)
			}
			var path string
			row := db.Pool.QueryRow(context.Background(),
				`SELECT changes::text FROM audit_log WHERE entity_id = $1 AND action = 'key.scope_refused'`,
				entityID)
			if err := row.Scan(&path); err != nil {
				t.Fatalf("no audit_log row for entity_id=%s, shape %q: %v", entityID, tc.name, err)
			}
			if strings.ContainsRune(path, '\x00') {
				t.Fatalf("stored changes still contains a NUL byte: %q", path)
			}
		})
	}
}

// The plain string columns (Action, EntityType, UserID) are text in
// Postgres: a NUL in any of them rejects the INSERT outright. The
// sanitiser must pass through them too. The row's stored Action reads back
// with the visible marker, not the raw NUL.
func TestLog_NULInPlainStringColumns(t *testing.T) {
	db := testutil.RequireDB(t)
	logger := audit.NewLogger(db)

	cols := []struct {
		name    string
		set     func(e *audit.Entry)
		getPath string
	}{
		{
			name: "Action",
			set: func(e *audit.Entry) {
				e.Action = "key.\x00refused"
				e.EntityType = "api_key"
			},
			getPath: "action",
		},
		{
			name: "EntityType",
			set: func(e *audit.Entry) {
				e.Action = "key.scope_refused"
				e.EntityType = "api\x00key"
			},
			getPath: "entity_type",
		},
		{
			name: "UserID",
			set: func(e *audit.Entry) {
				e.Action = "key.scope_refused"
				e.EntityType = "api_key"
				e.UserID = "us\x00er"
			},
			getPath: "user_id",
		},
	}

	for _, tc := range cols {
		t.Run(tc.name, func(t *testing.T) {
			entityID := uuid.New()
			entry := audit.Entry{EntityID: entityID}
			tc.set(&entry)
			err := logger.Log(context.Background(), entry)
			if err != nil {
				t.Fatalf("Log returned %v, want nil: a NUL in %s must not lose the row", err, tc.name)
			}
			var got string
			if err := db.Pool.QueryRow(context.Background(),
				`SELECT `+tc.getPath+` FROM audit_log WHERE entity_id = $1`, entityID).
				Scan(&got); err != nil {
				t.Fatalf("no audit_log row for entity_id=%s: %v", entityID, err)
			}
			if strings.ContainsRune(got, '\x00') {
				t.Fatalf("%s still contains a NUL byte: %q", tc.name, got)
			}
		})
	}
}

// A real NUL byte after one or more backslashes in any changes value (any
// depth, any key) must still write the row. json.Marshal escapes every
// backslash in the input as two bytes (`\\`), so a NUL after n backslashes
// in the source marshals to 2n backslashes before the six byte `\u0000`
// pattern. The fix counts the run of backslashes before the match and
// rewrites only when the run is even. This table pins the behaviour for
// the three shapes the review lists (top level value, nested value, map
// key) and the four runs (0, 1, 2, 3 input backslashes before a real NUL).
// It also pins the companion case: genuine `\u0000` text in the source
// (six characters, no real NUL) with 0 to 3 extra backslashes is stored
// unaltered, so the sanitiser does not corrupt caller text.
func TestLog_NULAfterBackslashRunStillWritesRow(t *testing.T) {
	db := testutil.RequireDB(t)
	logger := audit.NewLogger(db)

	// Build the source value with n backslashes before a real NUL.
	backslashesBefore := func(n int) string {
		return strings.Repeat("\\", n) + "\x00b"
	}
	// Genuine `\u0000` text (six characters) plus n extra source
	// backslashes. No real NUL anywhere.
	genuineText := func(n int) string {
		return strings.Repeat("\\", n) + `\u0000` + "b"
	}

	// Stored-value expectations. The NUL byte is replaced by the visible
	// marker text `\u0000` (six characters). For n source backslashes,
	// the stored value therefore has n source `\` plus one marker `\`
	// (the leading `\` of the marker), totalling n+1 backslashes. The
	// expectInput strings are raw literals so each `\` is one character.
	cases := []struct {
		name        string
		changes     map[string]interface{}
		// readParts is the jsonb path (split into segments) used in
		// the `changes #>> $1` query, where pgx maps a Go []string to
		// the text[] the operator expects. Each segment is a single
		// key name in the jsonb map.
		readParts    []string
		expectInput string
	}{
		// Real NUL cases: stored value must NOT contain a NUL byte.
		// The stored value is the source with the NUL replaced by the
		// visible marker (a total of n+1 backslashes before u0000b).
		{name: "value, 0 backslashes before NUL",
			changes:     map[string]interface{}{"v": "a" + backslashesBefore(0)},
			readParts:   []string{"v"},
			expectInput: `a` + `\` + `u0000b`},
		{name: "value, 1 backslash before NUL",
			changes:     map[string]interface{}{"v": "a" + backslashesBefore(1)},
			readParts:   []string{"v"},
			expectInput: `a` + `\\` + `u0000b`},
		{name: "value, 2 backslashes before NUL",
			changes:     map[string]interface{}{"v": "a" + backslashesBefore(2)},
			readParts:   []string{"v"},
			expectInput: `a` + `\\\` + `u0000b`},
		{name: "value, 3 backslashes before NUL",
			changes:     map[string]interface{}{"v": "a" + backslashesBefore(3)},
			readParts:   []string{"v"},
			expectInput: `a` + `\\\\` + `u0000b`},
		{name: "nested value, 1 backslash before NUL",
			changes: map[string]interface{}{
				"outer": map[string]interface{}{"v": "a" + backslashesBefore(1)},
			},
			readParts:   []string{"outer", "v"},
			expectInput: `a` + `\\` + `u0000b`},
		{name: "nested value, 2 backslashes before NUL",
			changes: map[string]interface{}{
				"outer": map[string]interface{}{"v": "a" + backslashesBefore(2)},
			},
			readParts:   []string{"outer", "v"},
			expectInput: `a` + `\\\` + `u0000b`},
		{name: "map key, 1 backslash before NUL",
			changes:     map[string]interface{}{"k" + backslashesBefore(1): "v"},
			readParts:   []string{"k" + `\\` + `u0000b`},
			expectInput: "v"},
		{name: "map key, 2 backslashes before NUL",
			changes:     map[string]interface{}{"k" + backslashesBefore(2): "v"},
			readParts:   []string{"k" + `\\\` + `u0000b`},
			expectInput: "v"},
		// Genuine text cases: stored verbatim (no NUL ever, so the
		// sanitiser must leave the marshalled bytes alone).
		{name: "genuine \\u0000 text, 0 extra",
			changes:     map[string]interface{}{"v": genuineText(0)},
			readParts:   []string{"v"},
			expectInput: `\` + `u0000b`},
		{name: "genuine \\u0000 text, 1 extra",
			changes:     map[string]interface{}{"v": genuineText(1)},
			readParts:   []string{"v"},
			expectInput: `\\` + `u0000b`},
		{name: "genuine \\u0000 text, 2 extra",
			changes:     map[string]interface{}{"v": genuineText(2)},
			readParts:   []string{"v"},
			expectInput: `\\\` + `u0000b`},
		{name: "genuine \\u0000 text, 3 extra",
			changes:     map[string]interface{}{"v": genuineText(3)},
			readParts:   []string{"v"},
			expectInput: `\\\\` + `u0000b`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entityID := uuid.New()
			err := logger.Log(context.Background(), audit.Entry{
				Action:     "key.scope_refused",
				EntityType: "api_key",
				EntityID:   entityID,
				Changes:    tc.changes,
			})
			if err != nil {
				t.Fatalf("Log returned %v, want nil: a NUL after backslashes must not lose the row", err)
			}
			// The #>> jsonb path operator takes a text[] (each
			// element a single key); pgx maps a Go []string directly
			// to text[], so the path elements reach the server
			// unchanged. The query returns the unescaped text at
			// any depth.
			var got string
			if err := db.Pool.QueryRow(context.Background(),
				`SELECT changes#>>$1 FROM audit_log WHERE entity_id = $2 AND action = 'key.scope_refused'`,
				tc.readParts, entityID).Scan(&got); err != nil {
				t.Fatalf("no audit_log row for entity_id=%s, path=%v: %v", entityID, tc.readParts, err)
			}
			if strings.ContainsRune(got, '\x00') {
				t.Fatalf("stored value for path %v still contains a NUL byte: %q", tc.readParts, got)
			}
			if got != tc.expectInput {
				t.Fatalf("stored value %q, want %q", got, tc.expectInput)
			}
		})
	}
}