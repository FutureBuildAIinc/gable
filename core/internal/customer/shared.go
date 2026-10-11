// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// The shared customer rule (principal rulings 43, 44 and 45). A customer held
// by several branches carries fields that govern the other branches' sales,
// credit and collections. A key bound to one branch may change only the fields
// that are plainly branch local on such a customer; every other field is
// restricted, so a field added to the record is restricted until it is listed
// as branch local here (deny by default). Only a dealer wide key changes a
// restricted field of a shared customer, and the change is audited with the
// actor and the before and after values. A customer held by the pin alone, a
// user, and every unshared customer are unchanged.
//
// Ship-tos and contacts carry no branch column: for a shared customer every
// existing one is shared, so a bound key reads them and may add new ones but
// may not change or delete any.

// branchLocalFields are the customer header fields a bound key may change on a
// shared customer: contact channels that belong to whoever deals with the
// customer. Everything else a header write can change is restricted.
var branchLocalFields = map[string]bool{"email": true, "phone": true}

// restrictedFields filters the changed field names of a header write down to
// the restricted ones.
func restrictedFields(changed []string) []string {
	var out []string
	for _, f := range changed {
		if !branchLocalFields[f] {
			out = append(out, f)
		}
	}
	return out
}

// SharedVerdict is a SharedGuard's answer for one customer and one caller.
type SharedVerdict struct {
	Refuse bool      // a branch bound key, and another branch holds the customer
	Branch uuid.UUID // the other branch the refusal names
	// RefuseCaller: a user without the finance or owner role, or an unbound
	// key without the customers:restricted scope, and the customer is shared
	// (principal ruling item 48).
	RefuseCaller bool
	Audit        bool // a scoped dealer wide key, and several branches hold the customer
}

// SharedGuard decides how the caller stands to a shared customer. The
// customer package cannot import pkg/middleware (which imports this package
// for partner auth), so customerguard adapts the key branch wall to it. A nil
// guard (unit tests) leaves every write unrestricted.
type SharedGuard interface {
	Shared(ctx context.Context, customerID uuid.UUID) (SharedVerdict, error)
	// RecordRefusal writes the refusal's audit row; it runs after the refused
	// write's transaction has rolled back.
	RecordRefusal(ctx context.Context)
	// RecordCallerRefusal writes the audit row of a refusal served to a user
	// or an unbound key (no pin, so no key.branch_refused row); it runs after
	// the refused write's transaction has rolled back.
	RecordCallerRefusal(ctx context.Context, customerID, branch uuid.UUID, fields []string)
	// RecordChange writes the audit row of a dealer wide key's restricted
	// change, inside the write's transaction.
	RecordChange(ctx context.Context, customerID uuid.UUID, changes map[string]any) error
}

// WithSharedGuard wires the shared customer rule.
func (s *Service) WithSharedGuard(g SharedGuard) *Service {
	s.shared = g
	return s
}

// sharedRefusal is the 403 a bound key gets on a restricted write; it carries
// the marker the outer method turns into the refusal's audit row.
type sharedRefusal struct {
	err *httpx.Error
	// caller marks a refusal served to a user or an unbound key; the rest
	// is what its audit row names.
	caller   bool
	customer uuid.UUID
	branch   uuid.UUID
	fields   []string
}

func (e *sharedRefusal) Error() string { return e.err.Message }

func refusedOn(branch uuid.UUID, fields ...string) *sharedRefusal {
	return refusedSharing(branch, fields...)
}

// refusedCallerOn is the refusal a caller without the finance or owner role
// (a user) or without the customers:restricted scope (an unbound key) gets on
// a shared customer's restricted fields: the bound key's words, naming the
// lowest numbered branch that holds the customer (principal ruling item 48,
// ADR 0007 section 5.5). Its audit row is the caller refusal row.
func refusedCallerOn(customer, branch uuid.UUID, fields ...string) *sharedRefusal {
	ref := refusedSharing(branch, fields...)
	ref.caller, ref.customer, ref.branch, ref.fields = true, customer, branch, fields
	return ref
}

// sharedRefusalWords is the one rule sentence every shared customer refusal
// carries, whoever the caller is.
const sharedRefusalWords = "a branch bound key may act only on its own branch"

func refusedSharing(branch uuid.UUID, fields ...string) *sharedRefusal {
	sort.Strings(fields)
	details := make([]httpx.FieldError, 0, len(fields))
	for _, f := range fields {
		details = append(details, httpx.FieldError{Field: f, Code: httpx.CodeForbidden, Message: "shared with another branch"})
	}
	return &sharedRefusal{err: &httpx.Error{Status: http.StatusForbidden, Code: httpx.CodeForbidden,
		Message: sharedRefusalWords + ": the customer is also held by branch " + branch.String(),
		Details: details}}
}

// sharedOutcome turns a refusal raised inside a write's transaction into the
// plain 403 and writes its audit row, now that the transaction is gone.
func (s *Service) sharedOutcome(ctx context.Context, err error) error {
	var ref *sharedRefusal
	if errors.As(err, &ref) {
		if s.shared != nil {
			if ref.caller {
				s.shared.RecordCallerRefusal(ctx, ref.customer, ref.branch, ref.fields)
			} else {
				s.shared.RecordRefusal(ctx)
			}
		}
		return ref.err
	}
	return err
}

// checkRestricted applies the rule to a write that changes the restricted
// fields given, with their before and after values. It returns a refusal for
// a bound key on a shared customer and writes the audit row for a dealer wide
// key. A write that changes no restricted field is not asked.
func (s *Service) checkRestricted(ctx context.Context, id uuid.UUID, changes map[string]any) error {
	if s.shared == nil || len(changes) == 0 {
		return nil
	}
	verdict, err := s.shared.Shared(ctx, id)
	if err != nil {
		return err
	}
	fields := make([]string, 0, len(changes))
	for f := range changes {
		fields = append(fields, f)
	}
	if verdict.Refuse {
		return refusedOn(verdict.Branch, fields...)
	}
	if verdict.RefuseCaller {
		return refusedCallerOn(id, verdict.Branch, fields...)
	}
	if verdict.Audit {
		return s.shared.RecordChange(ctx, id, changes)
	}
	return nil
}

// RefusedOnChild is the bound key's 403 on a shared customer's child record,
// for a module outside this package that serves the refusal itself.
func RefusedOnChild(branch uuid.UUID, field string) error {
	return refusedOn(branch, field).err
}

// checkSharedChild applies the ship-to and contact rule: a bound key may not
// change or delete one on a shared customer. No audit row for anyone else.
func (s *Service) checkSharedChild(ctx context.Context, customerID uuid.UUID, field string) error {
	if s.shared == nil {
		return nil
	}
	verdict, err := s.shared.Shared(ctx, customerID)
	if err != nil {
		return err
	}
	if verdict.Refuse {
		return refusedOn(verdict.Branch, field)
	}
	return nil
}
