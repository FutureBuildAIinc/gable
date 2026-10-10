// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// Entry represents a single audit log record for a financial operation.
type Entry struct {
	Action     string
	EntityType string
	EntityID   uuid.UUID
	UserID     string
	Changes    map[string]interface{}
}

// Logger writes audit entries to the audit_log table.
type Logger struct {
	db *database.DB
}

// NewLogger creates a new audit Logger backed by the given database.
func NewLogger(db *database.DB) *Logger {
	return &Logger{db: db}
}

// Log writes an audit entry synchronously through the caller's executor,
// resolved by the database seam: inside a transaction the row joins that
// transaction and commits or rolls back with the mutation it describes;
// outside one it goes to the pool directly. Either way the write happens
// before Log returns, so an error is returned (and logged) rather than lost
// to a goroutine. Callers inside a transaction propagate the error so a
// failed audit write fails the mutation; callers with no transaction may
// ignore it — their mutation is already committed, and Log has logged it.
//
// The actor columns record who performed the write (user, key or agent),
// resolved from the context by pkg/actor.
func (l *Logger) Log(ctx context.Context, entry Entry) error {
	act := actor.FromContext(ctx)

	// Explicit attribution on the entry wins for the legacy user_id column
	// (some callers pass the actor explicitly, e.g. pricing exposure events);
	// actor_id always records what pkg/actor resolved for the request, so the
	// kind and the id on a row can never disagree. A machine key is never the
	// implicit source of user_id: that column meant "a user" until actor_kind
	// existed, and legacy reports grouping by it would list key ids among
	// users. The key id lives in actor_id only. The decision rests on the key
	// id being in the context, not on the actor's kind: an agent marker over
	// a keyed request rewrites the kind to agent while the principal is still
	// the key, and must not smuggle the key id into user_id either.
	userID := sanitiseString(entry.UserID)
	_, viaMachineKey := actor.KeyIDFromContext(ctx)
	if userID == "" && !viaMachineKey {
		userID = act.ID
	}
	// No attribution at all is stored as NULL, the value the 088 backfill
	// writes for the same rows, rather than an empty string.
	var userIDVal any
	if userID != "" {
		userIDVal = userID
	}
	var actorID any
	if act.ID != "" {
		actorID = sanitiseString(act.ID)
	}

	// Extract request ID from context
	requestID := sanitiseString(middleware.GetRequestID(ctx))

	// Marshal changes to JSON, then rewrite the marshalled bytes so a NUL
	// byte ANYWHERE in the changes (a top level string, a nested map, a
	// slice value, a map key, a struct field, or any future shape the
	// jsonb column accepts) never reaches the jsonb parser. json.Marshal
	// escapes every NUL byte in the input as the six byte sequence
	// `\u0000`, and the jsonb parser then rejects that sequence with
	// SQLSTATE 22P05 ("unsupported Unicode escape sequence"). Replacing
	// those six bytes with the seven byte sequence `\\u0000` in the
	// marshalled text turns the escape into `\\` (an escaped backslash,
	// a valid JSON escape for backslash) followed by the literal text
	// `u0000`; the jsonb parser reads the six characters `\u0000` and
	// the NUL byte is gone. The marker text `\\u0000` is itself NOT a
	// valid JSON escape of NUL (the `\u0000` is preceded by `\\`, so it
	// is no longer a `\u` escape; without the leading backslash `u0000`
	// is plain text). json.Marshal also coerces any invalid UTF-8 byte
	// in the input to U+FFFD, so a second pass over the bytes is enough.
	// This is the one place that prepares the changes jsonb for Postgres,
	// and the writer never mutates the caller's map: json.Marshal only
	// reads, and the rewrite is on the resulting bytes.
	var changesJSON []byte
	if entry.Changes != nil {
		var err error
		changesJSON, err = json.Marshal(entry.Changes)
		if err != nil {
			slog.Error("audit: failed to marshal changes", "error", err)
			changesJSON = nil
		}
		if len(changesJSON) > 0 {
			changesJSON = sanitiseNULEscape(changesJSON)
		}
	}

	// The plain string columns go to text, which rejects a NUL byte
	// outright (SQLSTATE 22021). Sanitise them through the same function
	// the JSON pass uses, so every string the writer hands to Postgres
	// passes through one shared place.
	action := sanitiseString(entry.Action)
	entityType := sanitiseString(entry.EntityType)

	var actingAs, tool any
	if act.Kind == actor.KindAgent {
		actingAs, tool = act.ActingAs, act.Tool
	}

	// Cancellation discipline, per the review's P2: inside a transaction the
	// row must live and die with that transaction's context; the transaction
	// is the mutation's, and a cancelled request cancels the mutation too.
	// With no transaction in ctx the mutation has already committed, so the
	// audit row must survive a client that disconnected right after — the
	// values ride along, but the cancellation does not, and a short timeout
	// bounds the write on its own.
	execCtx := ctx
	if !database.InTx(ctx) {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
	}

	_, err := l.db.GetExecutor(ctx).Exec(execCtx,
		`INSERT INTO audit_log (action, entity_type, entity_id, user_id, changes, request_id,
		                        actor_kind, actor_id, acting_as, tool)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		action, entityType, entry.EntityID, userIDVal, changesJSON, requestID,
		act.Kind, actorID, actingAs, tool,
	)
	if err != nil {
		slog.Error("audit: failed to write audit log",
			"action", entry.Action,
			"entity_type", entry.EntityType,
			"entity_id", entry.EntityID,
			"error", err,
		)
		return err
	}
	return nil
}

// The refused path is caller controlled: stored verbatim, one refused
// request writes an attacker sized audit_log row, and even a fully scopeless
// key converts cheap requests into disk exhaustion. The row therefore stores
// bounded copies, cut on a rune boundary and marked as truncated; the full
// path is served only to the server log line, which the auth layer writes
// for every refusal.
const (
	maxRefusalPathBytes  = 512
	maxRefusalScopeBytes = 128
)

// cutRunes cuts s to at most limit bytes without splitting a UTF-8 rune,
// reporting whether it cut anything.
func cutRunes(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// sanitiseString makes a string safe for a Postgres text column and for the
// jsonb marshalling that follows: a NUL byte (U+0000) is replaced with the
// visible marker `\u0000` (the JSON escape spelled out as literal text, so
// json.Marshal does not escape it back to `\u0000` and the jsonb parser does
// not see the escape it rejects) and any invalid UTF-8 sequence is replaced
// with U+FFFD. A refused path can carry whatever the URL contained, and a
// NUL or invalid UTF-8 in the path was the failure mode the review flagged:
// Postgres text rejects a NUL outright, and once json.Marshal had turned the
// NUL into the JSON escape sequence `\u0000` the jsonb parser rejected it
// with SQLSTATE 22P05 ("unsupported Unicode escape sequence"), so the audit
// row of a refused request whose path held a NUL was lost. The fix is one
// shared place: the audit writer calls sanitiseString on every string it
// hands to the database, so per-caller code keeps the verbatim value and the
// row stays whole.
func sanitiseString(s string) string {
	if len(s) == 0 {
		return s
	}
	if !strings.ContainsRune(s, '\x00') && utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			// Invalid UTF-8: the rune decoder returns RuneError with size 1
			// for a lone byte that begins no valid sequence. Replace with
			// U+FFFD and advance one byte so the writer keeps moving
			// through a run of bad bytes one at a time.
			b.WriteRune('\uFFFD')
			i++
		case r == '\x00':
			// NUL: append the visible marker so the jsonb parser sees
			// plain text, never the escape it rejects. The marker is the
			// JSON escape spelled out; the marshaller will encode the
			// backslashes once and the stored value reads "\u0000".
			b.WriteString(`\u0000`)
			i += size
		default:
			b.WriteRune(r)
			i += size
		}
	}
	return b.String()
}

// sanitiseStringChanged is sanitiseString plus a flag that says whether
// the input needed rewriting. The refusal path records the flag in the
// audit row's changes (`path_sanitised: true`) so an investigator can
// tell a sanitised NUL byte from a caller's literal `\u0000` text; the
// server log line carries the verbatim path, which is the second record.
func sanitiseStringChanged(s string) (string, bool) {
	out := sanitiseString(s)
	if out == s {
		return out, false
	}
	return out, true
}

// nulJSONEscape is the six bytes json.Marshal writes for a NUL byte in a
// string. The jsonb parser rejects this with SQLSTATE 22P05 ("unsupported
// Unicode escape sequence"). The replacement is the seven bytes `\\u0000`:
// the jsonb parser reads `\\` as an escaped backslash (one character)
// and the four characters `u0000` as plain text (no leading backslash, so
// no escape), producing the six characters `\u0000` with no NUL byte.
// The marker text itself is NOT a valid JSON escape of NUL (the `\u0000`
// is preceded by `\\`, so it is no longer a `\u` escape).
var (
	nulJSONEscape    = []byte(`\u0000`)
	nulJSONMarker    = []byte(`\\u0000`)
)

// sanitiseNULEscape rewrites every NUL escape in a json.Marshal output
// (the six bytes `\u0000`) with the seven byte sequence `\\u0000`, so the
// jsonb parser reads the six characters `\u0000` and no NUL byte reaches
// the jsonb value. The input is the marshalled JSON the writer is about to
// INSERT; invalid UTF-8 is already coerced to U+FFFD by json.Marshal, so
// one byte pass is enough. The input is not mutated.
//
// json.Marshal escapes a backslash in the input as `\\`, so a string that
// already holds the six character text `\u0000` (the marker sanitiseString
// writes) becomes the seven bytes `\\u0000` in the JSON output. A naive
// replace of the six byte pattern `\u0000` would also match the trailing
// six bytes of `\\u0000` and add a third backslash, turning the marker
// into `\\\u0000` (which the jsonb parser reads as `\\` (one character)
// plus `\u0000` (a NUL escape, which jsonb rejects)).
//
// The rewrite must skip the trailing six bytes of the marker (and the
// trailing six bytes of any caller text that already holds `\u0000`)
// while still firing for a real NUL byte that sits after one or more
// backslashes in the source. json.Marshal doubles every source backslash
// to `\\`, so the match's leading `\` and the source backslashes form a
// run of consecutive `\` bytes immediately before the match. The match
// itself counts as a run of one. Skipping when the run BEFORE the match
// is odd (the match's leading `\` is itself escaped, so the `\u` is not
// a Unicode escape) leaves the marker and any caller `\u0000` text alone,
// while the run before a real NUL is always even (0, 2, 4, ...) and the
// rewrite fires.
func sanitiseNULEscape(in []byte) []byte {
	if len(in) == 0 {
		return in
	}
	// One scan to find every `\u0000` whose run of preceding backslashes
	// (not counting the match's own leading `\`) is even.
	var positions []int
	for i := 0; i+len(nulJSONEscape) <= len(in); i++ {
		if !bytes.Equal(in[i:i+len(nulJSONEscape)], nulJSONEscape) {
			continue
		}
		k := 0
		for j := i - 1; j >= 0 && in[j] == '\\'; j-- {
			k++
		}
		if k%2 == 1 {
			// The match backslash is itself escaped, so the `\u` is
			// not a JSON Unicode escape and the bytes are caller text
			// (the sanitiser marker or a literal `\u0000` from the
			// caller). Leave them alone.
			continue
		}
		positions = append(positions, i)
	}
	if len(positions) == 0 {
		return in
	}
	// Build the rewritten bytes in one allocation; the input is not mutated.
	out := make([]byte, 0, len(in)+len(positions))
	prev := 0
	for _, p := range positions {
		out = append(out, in[prev:p]...)
		out = append(out, nulJSONMarker...)
		prev = p + len(nulJSONEscape)
	}
	out = append(out, in[prev:]...)
	return out
}

// AuditKeyRefusal records a refused machine-key request (a valid key refused
// for lacking a scope, for a user-only route, or for a path machine keys do
// not address). It implements the middleware package's KeyRefusalAuditor
// seam. The row's actor is the key: the ctx the auth core passes carries the
// key id, so actor_id is the key's id and user_id stays NULL (a key is never
// a user, even when agent headers rewrite actor_kind to agent). The stored
// path and scope are bounded (see maxRefusalPathBytes); a refusal answers
// before this write, so a bounded row carries everything the trail needs. A
// failure to write is logged and swallowed: the refusal verdict has already
// been served, and a full audit table must not turn a 403 into a 500.
func (l *Logger) AuditKeyRefusal(ctx context.Context, keyID, action, scope, method, path string) {
	id, err := uuid.Parse(keyID)
	if err != nil {
		// The id comes from the api_keys row the validator read; a
		// non-uuid here is a wiring fault worth a loud log line.
		slog.Error("audit: machine key refusal with non-uuid key id", "key_id", keyID, "action", action)
		id = uuid.Nil
	}
	// The 512 byte cap is on the stored path, so it must be cut AFTER
	// sanitising: a NUL byte becomes six characters in the stored value
	// (the marker is the six byte text `\u0000`), and an invalid UTF-8
	// byte may be replaced by the three byte U+FFFD, both of which would
	// push a path that fit pre-sanitise over the cap post-sanitise. The
	// cap protects the row from an attacker sized path; cutting on the
	// pre-sanitise length would let the stored row exceed the cap.
	storedPath, sanitised := sanitiseStringChanged(path)
	pathTruncated := false
	if len(storedPath) > maxRefusalPathBytes {
		storedPath, pathTruncated = cutRunes(storedPath, maxRefusalPathBytes)
	}
	storedScope, scopeTruncated := cutRunes(scope, maxRefusalScopeBytes)
	changes := map[string]interface{}{"method": method, "path": storedPath}
	if pathTruncated {
		changes["path_truncated"] = true
	}
	if sanitised {
		// A sanitised NUL is otherwise indistinguishable from a
		// caller's literal `\u0000` text: both read back as the six
		// characters `\u0000`. Flag it so an investigator can tell the
		// two apart (the server log line carries the verbatim path,
		// which is the other record).
		changes["path_sanitised"] = true
	}
	if scopeTruncated {
		changes["scope_truncated"] = true
	}
	if storedScope != "" {
		changes["scope"] = storedScope
	}
	if err := l.Log(ctx, Entry{
		Action:     action,
		EntityType: "api_key",
		EntityID:   id,
		Changes:    changes,
	}); err != nil {
		slog.Error("audit: failed to write machine key refusal", "action", action, "key_id", keyID, "error", err)
	}
}

// Drain is retained for graceful-shutdown callers: writes are synchronous
// now, so there is never anything in flight to wait for.
func (l *Logger) Drain() {}
