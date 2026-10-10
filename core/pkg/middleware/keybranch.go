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
// failed: a body the wall cannot read as one complete JSON value. It is
// exported as ErrBodyRefused for resolvers built outside this package.
var errRefused = errors.New("key branch wall: refused")

// ErrBodyRefused is the error DecodeOneJSONBody answers for a body that is
// not exactly one complete JSON value.
var ErrBodyRefused = errRefused

// DecodeOneJSONBody reads the whole body, restores it for the handler, and
// decodes it into v only when it is exactly one complete JSON value. It
// answers ErrBodyRefused for anything else, so a resolver that hands it back
// refuses a bound key the body the handler would have read only in part.
func DecodeOneJSONBody(r *http.Request, v any) error {
	return decodeOneJSON(r, v)
}

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

// ErrRowNamesNoBranch tells RowBranch the row the path names exists but
// belongs to no branch (a legacy shape the denorm trigger refuses today):
// such a row is outside every pin, so a bound key is refused it.
var ErrRowNamesNoBranch = errors.New("key branch wall: the row names no branch")

// RowBranch wraps a handler whose path names a row ("id") whose branch
// branchOf resolves: a location, a quote. A row that does not exist names no
// branch and passes to the handler's own answer; a row that exists but
// belongs to no branch (ErrRowNamesNoBranch) is outside every pin and is
// refused. A lookup failure answers 500 rather than failing open.
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
		if errors.Is(err, ErrRowNamesNoBranch) {
			return uuid.Nil, true, nil
		}
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
	if branch == nil {
		return nil, ErrRowNamesNoBranch
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

// CustomerBranch wraps a handler whose request writes one or more customers'
// data: the tax exemption writes and the customer priced rules. A branch
// bound key may reach it only while every customer the request names holds
// the pin among that customer's branches (customer_branches), the same
// visibility the customer module holds a caller's branch context to;
// anything else is the 403 refusal with its key.branch_refused row, the
// details naming customer_id. A request that names no customer passes
// through: what it writes is not scoped to one. A resolver failure answers
// 500 rather than failing open, and a resolver may hand back
// DecodeOneJSONBody's ErrBodyRefused to refuse a body the handler would have
// read only in part.
func (w *KeyBranchWall) CustomerBranch(customersOf func(ctx context.Context, r *http.Request) ([]uuid.UUID, error)) func(http.Handler) http.Handler {
	return w.wrap("customer_id", "", func(ctx context.Context, r *http.Request) (uuid.UUID, bool, error) {
		customers, err := customersOf(ctx, r)
		if err != nil {
			return uuid.Nil, false, err
		}
		if len(customers) == 0 {
			return uuid.Nil, false, nil
		}
		ok, err := w.customersHoldPin(ctx, customers)
		if err != nil {
			return uuid.Nil, false, err
		}
		if !ok {
			// A customer outside the pin: the nil branch the wrap refuses.
			return uuid.Nil, true, nil
		}
		return uuid.Nil, false, nil
	})
}

// CustomerBodyBranch is CustomerBranch over a JSON body that names the
// customer in customer_id: one object or an array of them (a bulk body),
// decoded whole exactly as the handler's own decoder would have to.
func (w *KeyBranchWall) CustomerBodyBranch() func(http.Handler) http.Handler {
	return w.CustomerBranch(func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		return bodyCustomers(r)
	})
}

// CustomerRowBranch is CustomerBranch over the customer the row the path
// names belongs to. customerOf resolves that customer; a row that does not
// exist or that belongs to no customer names nothing and passes to the
// handler's own answer.
func (w *KeyBranchWall) CustomerRowBranch(customerOf func(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)) func(http.Handler) http.Handler {
	return w.CustomerBranch(func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		raw := r.PathValue("id")
		if raw == "" {
			return nil, nil
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, nil // the handler answers the malformed id
		}
		customer, err := customerOf(ctx, id)
		if err != nil {
			return nil, err
		}
		if customer == nil {
			return nil, nil
		}
		return []uuid.UUID{*customer}, nil
	})
}

// bodyCustomers reads the customer_ids a body names, one object or an array
// of them. An id that does not parse names nothing the wall can hold, and
// the handler's own validation answers it.
func bodyCustomers(r *http.Request) ([]uuid.UUID, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("key branch wall: read body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	collect := func(id string, out []uuid.UUID) []uuid.UUID {
		if parsed, err := uuid.Parse(id); err == nil {
			out = append(out, parsed)
		}
		return out
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '[' {
		var many []struct {
			CustomerID string `json:"customer_id"`
		}
		if err := json.Unmarshal(raw, &many); err != nil {
			return nil, errRefused
		}
		out := make([]uuid.UUID, 0, len(many))
		for _, m := range many {
			out = collect(m.CustomerID, out)
		}
		return out, nil
	}
	var one struct {
		CustomerID string `json:"customer_id"`
	}
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, errRefused
	}
	if one.CustomerID == "" {
		return nil, nil
	}
	return collect(one.CustomerID, nil), nil
}

// customersHoldPin answers whether every customer's branch set
// (customer_branches) holds the pin. A customer with no row there holds no
// branch at all, so a bound key is refused it.
func (w *KeyBranchWall) customersHoldPin(ctx context.Context, customers []uuid.UUID) (bool, error) {
	if w.db == nil {
		return false, errors.New("key branch wall: no database for the customer branch lookup")
	}
	pin, _ := KeyBranchPin(ctx)
	distinct := make(map[uuid.UUID]struct{}, len(customers))
	for _, c := range customers {
		distinct[c] = struct{}{}
	}
	ids := make([]uuid.UUID, 0, len(distinct))
	for c := range distinct {
		ids = append(ids, c)
	}
	var holding int
	err := w.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT COUNT(DISTINCT customer_id) FROM customer_branches WHERE branch_id = $1 AND customer_id = ANY($2)`,
		pin, ids).Scan(&holding)
	if err != nil {
		return false, fmt.Errorf("key branch wall: customer branch lookup: %w", err)
	}
	return holding == len(ids), nil
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
