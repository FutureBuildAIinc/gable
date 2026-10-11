// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The shared customer rule (principal rulings 43, 44 and 45, ADR 0007 section
// 5.5): a customer held by the pin's branch and by another branch carries
// fields that govern the other branch's sales, credit and collections, and a
// key bound to the pin may not change them. The customer service decides per
// field and calls SharedCustomer for the verdict; this file holds the lookup,
// the request note the refusal row needs, and the audit seam for the dealer
// wide key that is allowed to make the change.

type requestNoteKey struct{}

type requestNote struct{ method, path string }

// RecordRequest notes the method and path of the request in its context, so
// a refusal written below the HTTP layer (the customer service holds the
// field level rule) can name the call in its key.branch_refused row.
func (w *KeyBranchWall) RecordRequest() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), requestNoteKey{}, requestNote{method: r.Method, path: r.URL.Path})
			next.ServeHTTP(rw, r.WithContext(ctx))
		})
	}
}

// SharedVerdict is the wall's answer for one customer and one caller.
type SharedVerdict struct {
	// Refuse is set for a branch bound key when another branch holds the
	// customer too; Branch is the lowest numbered such branch. The pin rule
	// comes first: a bound key is refused even when it carries the dedicated
	// scope.
	Refuse bool
	Branch uuid.UUID
	// RefuseCaller is set for a user without the finance or owner role and
	// for an unbound key without the customers:restricted scope, on a
	// customer another branch also holds (principal ruling item 48).
	RefuseCaller bool
	// Audit is set for an unbound key carrying the dedicated scope when two
	// or more branches hold the customer: its change goes through, audited
	// with the actor and the before and after values.
	Audit bool
}

// SharedAuditor writes the audit row of a dealer wide key's change to a
// shared customer's restricted fields. pkg/audit implements it.
type SharedAuditor interface {
	AuditSharedCustomerChange(ctx context.Context, keyID string, customer uuid.UUID, changes map[string]any) error
}

// SharedRefusalAuditor is the optional audit seam for the refusal of a user or
// an unbound key on a shared customer's restricted fields: those callers have
// no pin, so the key.branch_refused row does not fit them. The row's entity is
// the customer.
type SharedRefusalAuditor interface {
	AuditSharedCustomerRefusal(ctx context.Context, actorKind, actorID string, customer, branch uuid.UUID, fields []string, method, path string)
}

// callerHasRole reports whether the user claims hold one of the roles.
func callerHasRole(claims *UserClaims, roles ...string) bool {
	if claims == nil {
		return false
	}
	for _, role := range roles {
		if claims.Role == role {
			return true
		}
		for _, r := range claims.Roles {
			if r == role {
				return true
			}
		}
	}
	return false
}

// SharedCustomer answers how the caller stands to a customer's restricted
// fields (principal rulings 43 to 45 and item 48). A user passes with the
// finance or owner role and is refused without it, fail closed when no
// claims at all arrive; an unshared customer stays open to every caller. A
// branch bound key is refused when any branch besides the pin holds the
// customer, whatever its scopes. An unbound key is admitted only while it
// carries the customers:restricted scope, and its change of a shared
// customer is audited. The caller refusals name the lowest numbered branch
// holding the customer; the bound key's names the lowest numbered branch
// that is not the pin.
func (w *KeyBranchWall) SharedCustomer(ctx context.Context, customer uuid.UUID) (SharedVerdict, error) {
	if _, isKey := KeyIDFromContext(ctx); !isKey {
		if callerHasRole(ClaimsFromContext(ctx), "finance", "owner") {
			return SharedVerdict{}, nil
		}
		other, holders, err := w.sharedHolders(ctx, customer)
		if err != nil {
			return SharedVerdict{}, err
		}
		if holders < 2 {
			return SharedVerdict{}, nil
		}
		return SharedVerdict{RefuseCaller: true, Branch: other}, nil
	}
	if w.db == nil {
		return SharedVerdict{}, errors.New("key branch wall: no database for the shared customer lookup")
	}
	pin, bound := KeyBranchPin(ctx)
	if bound {
		var other uuid.UUID
		err := w.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT branch_id FROM customer_branches WHERE customer_id = $1 AND branch_id <> $2 ORDER BY branch_id LIMIT 1`,
			customer, pin).Scan(&other)
		if errors.Is(err, pgx.ErrNoRows) {
			return SharedVerdict{}, nil
		}
		if err != nil {
			return SharedVerdict{}, fmt.Errorf("key branch wall: shared customer lookup: %w", err)
		}
		return SharedVerdict{Refuse: true, Branch: other}, nil
	}
	other, holders, err := w.sharedHolders(ctx, customer)
	if err != nil {
		return SharedVerdict{}, err
	}
	if holders < 2 {
		return SharedVerdict{}, nil
	}
	scopes, _ := KeyScopesFromContext(ctx)
	if !scopeHeld(scopes, CustomerRestrictedScope) {
		return SharedVerdict{RefuseCaller: true, Branch: other}, nil
	}
	return SharedVerdict{Audit: true, Branch: other}, nil
}

// sharedHolders answers the lowest numbered branch holding the customer and
// how many branches hold it at all.
func (w *KeyBranchWall) sharedHolders(ctx context.Context, customer uuid.UUID) (uuid.UUID, int, error) {
	if w.db == nil {
		return uuid.Nil, 0, errors.New("key branch wall: no database for the shared customer lookup")
	}
	var other *uuid.UUID
	var holders int
	err := w.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT (SELECT branch_id FROM customer_branches WHERE customer_id = $1 ORDER BY branch_id LIMIT 1), COUNT(*)
		   FROM customer_branches WHERE customer_id = $1`, customer).Scan(&other, &holders)
	if err != nil {
		return uuid.Nil, 0, fmt.Errorf("key branch wall: shared customer lookup: %w", err)
	}
	if other != nil {
		return *other, holders, nil
	}
	return uuid.Nil, holders, nil
}

// RecordSharedRefusal writes the key.branch_refused row of a refusal the
// customer service served. It runs outside the refused write's transaction
// (which rolled back), with the method and path RecordRequest noted.
func (w *KeyBranchWall) RecordSharedRefusal(ctx context.Context) {
	pin, bound := KeyBranchPin(ctx)
	keyID, isKey := KeyIDFromContext(ctx)
	if !bound || !isKey || w.auditor == nil {
		return
	}
	note, _ := ctx.Value(requestNoteKey{}).(requestNote)
	w.auditor.AuditKeyBranchRefusal(ctx, keyID, pin, note.method, note.path)
}

// RecordSharedCallerRefusal writes the audit row of a refusal served to a user
// or an unbound key, outside the refused write's transaction. A bound key's
// refusal has its own row (RecordSharedRefusal).
func (w *KeyBranchWall) RecordSharedCallerRefusal(ctx context.Context, customer, branch uuid.UUID, fields []string) {
	a, ok := w.auditor.(SharedRefusalAuditor)
	if !ok {
		return
	}
	kind, actor := "user", ""
	if keyID, isKey := KeyIDFromContext(ctx); isKey {
		kind, actor = "key", keyID
	} else if claims := ClaimsFromContext(ctx); claims != nil {
		actor = claims.Subject
	}
	note, _ := ctx.Value(requestNoteKey{}).(requestNote)
	a.AuditSharedCustomerRefusal(ctx, kind, actor, customer, branch, fields, note.method, note.path)
}

// RecordSharedChange writes the audit row of an unbound key's change to a
// shared customer's restricted fields, inside the caller's transaction. A
// wall with no audit writer fails the change rather than let it go unrecorded.
func (w *KeyBranchWall) RecordSharedChange(ctx context.Context, customer uuid.UUID, changes map[string]any) error {
	keyID, isKey := KeyIDFromContext(ctx)
	if !isKey {
		return nil
	}
	a, ok := w.auditor.(SharedAuditor)
	if !ok {
		return errors.New("key branch wall: no audit writer for a shared customer change")
	}
	return a.AuditSharedCustomerChange(ctx, keyID, customer, changes)
}
