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
	// customer too; Branch is the lowest numbered such branch.
	Refuse bool
	Branch uuid.UUID
	// Audit is set for an unbound key when two or more branches hold the
	// customer: its change goes through, audited with the actor and the
	// before and after values.
	Audit bool
}

// SharedAuditor writes the audit row of a dealer wide key's change to a
// shared customer's restricted fields. pkg/audit implements it.
type SharedAuditor interface {
	AuditSharedCustomerChange(ctx context.Context, keyID string, customer uuid.UUID, changes map[string]any) error
}

// SharedCustomer answers how the caller stands to a customer's restricted
// fields. A user passes unchanged (the zero verdict); a branch bound key is
// refused when any branch besides the pin holds the customer; an unbound key
// is audited when more than one branch holds it.
func (w *KeyBranchWall) SharedCustomer(ctx context.Context, customer uuid.UUID) (SharedVerdict, error) {
	if _, isKey := KeyIDFromContext(ctx); !isKey {
		return SharedVerdict{}, nil
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
	var branches int
	if err := w.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT COUNT(*) FROM customer_branches WHERE customer_id = $1`, customer).Scan(&branches); err != nil {
		return SharedVerdict{}, fmt.Errorf("key branch wall: shared customer lookup: %w", err)
	}
	return SharedVerdict{Audit: branches > 1}, nil
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
