// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The key branch wall (ADR 0007 section 5.5). The machine key core pins a
// branch bound key's request to its branch and refuses a foreign X-Branch-Id
// itself; the branch middleware then holds every route that mounts it to the
// pin. A route that mounts no branch middleware escaped the pin: it acted on
// whatever branch its body or path named. The wall closes that: mounted on
// such a route, it reads the pin (branchctx.KeyBranchFromContext), compares
// the branch the request names, and refuses a bound key that names another
// branch with 403 forbidden and the key.branch_refused audit row, the same
// verdict and row the X-Branch-Id rule writes. A user and an unbound key
// name no pin, so the wall passes them through unchanged, and the pin holds
// whatever the multi_branch_enabled switch says, as on the middleware's own
// routes. The check lives here, once, not in each handler.

// KeyBranchPin returns the branch a machine key is bound to, the pin the
// branch middleware and this wall hold a request to. ok is false for a user
// and an unbound key.
func KeyBranchPin(ctx context.Context) (uuid.UUID, bool) {
	return branchctx.KeyBranchFromContext(ctx)
}

// KeyBranchWall holds branch bound machine keys to their pin on routes that
// mount no branch middleware. A nil database or auditor only loses the
// location lookup or the audit row, never the refusal.
type KeyBranchWall struct {
	db      *database.DB
	auditor BranchRefusalAuditor
}

// NewKeyBranchWall builds the wall. db backs the location row lookup
// (LocationRowBranch); auditor writes the key.branch_refused row and may be
// nil (the refusal is still served).
func NewKeyBranchWall(db *database.DB, auditor BranchRefusalAuditor) *KeyBranchWall {
	return &KeyBranchWall{db: db, auditor: auditor}
}

// NamedBranch wraps a handler whose path names a branch directly: the given
// path value ("branch_id", or "id" on the branch routes) is the branch the
// request acts on.
func (w *KeyBranchWall) NamedBranch(pathValue string) func(http.Handler) http.Handler {
	return w.wrap(pathValue, "", func(ctx context.Context, r *http.Request) (uuid.UUID, bool, error) {
		raw := r.PathValue(pathValue)
		if raw == "" {
			return uuid.Nil, false, nil
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			return uuid.Nil, false, nil // the handler answers the malformed id
		}
		return id, true, nil
	})
}

// BodyBranch wraps a handler whose JSON body names a branch in branch_id. The
// wall decodes the body into the same branch_id field and the same uuid.UUID
// the handler decodes, and only a body that is exactly one complete JSON value
// reaches that comparison: a body the handler's own json.Decoder would read
// only in part (a second value or trailing bytes after the first) or not at
// all is refused for a bound key, so the wall and the handler can never
// disagree about what a body names. A body that parses and names no branch
// (absent, null) passes through to the handler's own validation. The body is
// restored for the handler.
func (w *KeyBranchWall) BodyBranch() func(http.Handler) http.Handler {
	return w.wrap("branch_id", "", func(ctx context.Context, r *http.Request) (uuid.UUID, bool, error) {
		var body struct {
			BranchID uuid.UUID `json:"branch_id"`
		}
		if err := decodeOneJSON(r, &body); err != nil {
			return uuid.Nil, false, err
		}
		if body.BranchID == uuid.Nil {
			return uuid.Nil, false, nil
		}
		return body.BranchID, true, nil
	})
}

// errRefused tells wrap the request is refused for a bound key rather than
// failed: a body the wall cannot read as one complete JSON value.
var errRefused = errors.New("key branch wall: refused")

// decodeOneJSON reads the whole body, restores it for the handler, and decodes
// it into v only when it is exactly one complete JSON value. Anything else
// (unparsable, truncated, trailing data after the first value) answers
// errRefused.
func decodeOneJSON(r *http.Request, v any) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("key branch wall: read body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if err := json.Unmarshal(raw, v); err != nil {
		return errRefused
	}
	return nil
}

// RowBranch wraps a handler whose path names a row ("id") whose branch
// branchOf resolves: a location, a quote. A row that does not exist, or that
// belongs to no branch, names no branch and passes to the handler's own
// answer. A lookup failure answers 500 rather than failing open.
func (w *KeyBranchWall) RowBranch(branchOf func(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)) func(http.Handler) http.Handler {
	return w.wrap("id", "", func(ctx context.Context, r *http.Request) (uuid.UUID, bool, error) {
		raw := r.PathValue("id")
		if raw == "" {
			return uuid.Nil, false, nil
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			return uuid.Nil, false, nil // the handler answers the malformed id
		}
		branch, err := branchOf(ctx, id)
		if err != nil {
			return uuid.Nil, false, err
		}
		if branch == nil {
			return uuid.Nil, false, nil
		}
		return *branch, true, nil
	})
}

// LocationRowBranch is RowBranch over the locations tree: the row the path
// names stands for its branch (itself when it is a branch, else its
// branch_id), the same lookup the payload guard applies to a parent_id.
func (w *KeyBranchWall) LocationRowBranch() func(http.Handler) http.Handler {
	return w.RowBranch(w.branchOfLocation)
}

func (w *KeyBranchWall) branchOfLocation(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
	if w.db == nil {
		return nil, errors.New("key branch wall: no database for the location lookup")
	}
	var branch *uuid.UUID
	err := w.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT CASE WHEN type = 'BRANCH' THEN COALESCE(branch_id, id) ELSE branch_id END
		   FROM locations WHERE id = $1`, id).Scan(&branch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("key branch wall: location branch lookup: %w", err)
	}
	return branch, nil
}

// RefuseBound wraps a handler a branch bound key may never reach: a route
// that administers the branch directory or acts across every branch and so
// cannot be confined to one. reason says why in the refusal message.
func (w *KeyBranchWall) RefuseBound(reason string) func(http.Handler) http.Handler {
	return w.wrap("", reason, func(ctx context.Context, r *http.Request) (uuid.UUID, bool, error) {
		// A branch is named that no pin can equal, so every bound key is
		// refused whatever the request carries.
		return uuid.Nil, true, nil
	})
}

// wrap builds the wrapper: no pin passes (a user, an unbound key), the pin's
// own branch passes, any other branch is refused with its audit row. field
// names the request part that named the branch in the refusal's details;
// reason, when not empty, extends the refusal message.
func (w *KeyBranchWall) wrap(field, reason string, named func(ctx context.Context, r *http.Request) (uuid.UUID, bool, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			pin, ok := KeyBranchPin(r.Context())
			if !ok {
				next.ServeHTTP(rw, r)
				return
			}
			id, has, err := named(r.Context(), r)
			if err != nil {
				if errors.Is(err, errRefused) {
					w.refuse(r.Context(), rw, r, pin, field, reason)
					return
				}
				slog.Error("key branch wall: branch lookup failed", "error", err, "method", r.Method, "path", r.URL.Path)
				respondAuthError(rw, r, http.StatusInternalServerError, "internal_error", "branch lookup failed")
				return
			}
			if has && id != pin {
				w.refuse(r.Context(), rw, r, pin, field, reason)
				return
			}
			next.ServeHTTP(rw, r)
		})
	}
}

// refuse writes the 403 and its audit row. The refusal is the ADR envelope
// the machine key refusals answer in, naming the field that named the branch;
// a failure to write the row is logged inside the auditor and swallowed, as
// the refusal verdict has already been served.
func (w *KeyBranchWall) refuse(ctx context.Context, rw http.ResponseWriter, r *http.Request, pin uuid.UUID, field, reason string) {
	if keyID, ok := KeyIDFromContext(ctx); ok && w.auditor != nil {
		w.auditor.AuditKeyBranchRefusal(ctx, keyID, pin, r.Method, r.URL.Path)
	}
	message := "a branch bound key may act only on its own branch"
	if reason != "" {
		message = message + ": " + reason
	}
	var details []string
	if field != "" {
		details = []string{field}
	}
	writeAuthError(rw, r, http.StatusForbidden, "forbidden", message, details)
}

// writeAuthError writes an auth layer refusal in the ADR envelope with
// optional detail strings, the shape respondAuthError writes.
func writeAuthError(w http.ResponseWriter, r *http.Request, status int, code, message string, details []string) {
	reqID := w.Header().Get("X-Request-ID")
	if reqID == "" {
		reqID = r.Header.Get("X-Request-ID")
	}
	var body authErrorBody
	body.Error.Code = code
	body.Error.Message = message
	body.Error.Details = details
	body.Meta.RequestID = reqID
	slog.Warn("auth refusal", "code", code, "status", status, "method", r.Method, "path", r.URL.Path, "request_id", reqID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
