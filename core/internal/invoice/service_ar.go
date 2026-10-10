// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice

import (
	"context"
	"fmt"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// The acts of ADR 0005 section 9.4 that settle an invoice from the invoice side:
// a credit memo applied to invoices, a write off, and the invoice's applications
// read. The money moves in the AR core; this layer holds the branch wall, the
// roles and the revision, and writes the audit row and the events last.

// CreditApplyLine is one invoice a credit memo is applied to.
type CreditApplyLine struct {
	InvoiceID   uuid.UUID
	AmountCents int64
}

// ApplyCreditMemo applies an open credit memo's credit to invoices.
func (s *Service) ApplyCreditMemo(ctx context.Context, id uuid.UUID, lines []CreditApplyLine, pre Precondition, body Transition) (*CreditMemo, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	st, err := s.store()
	if err != nil {
		return nil, err
	}
	if s.account == nil {
		return nil, fmt.Errorf("invoice: the AR core is not wired")
	}
	for i, l := range lines {
		if _, err := st.GetInvoice(ctx, l.InvoiceID); err != nil {
			return nil, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed, Message: "a referenced record does not exist",
				Details: []httpx.FieldError{{Field: fmt.Sprintf("applications[%d].invoice_id", i), Message: "no such invoice"}}}
		}
	}
	var out *CreditMemo
	err = s.inTx(ctx, func(ctx context.Context) error {
		cm, err := st.LockCreditMemo(ctx, id) // section 11, step 3
		if err != nil {
			return notFound(err)
		}
		if err := s.checkBranch(ctx, cm.BranchID, "credit memo"); err != nil {
			return err
		}
		if err := pre.check(cm.Revision); err != nil {
			return err
		}
		date, err := st.BranchLocalDate(ctx, cm.BranchID, s.now())
		if err != nil {
			return err
		}
		apply := make([]account.ApplyLine, len(lines))
		for i, l := range lines {
			apply[i] = account.ApplyLine{InvoiceID: l.InvoiceID, AmountCents: l.AmountCents}
		}
		fx, err := s.account.ApplyCreditMemo(ctx, account.ApplyCreditMemoIn{MemoID: id, Lines: apply, On: date, Actor: body.Actor})
		if err != nil {
			return mapPostingError(err)
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "credit_memo.applied", EntityType: "credit_memo", EntityID: id, UserID: body.Actor,
				Changes: map[string]interface{}{"applications": len(lines), "application_ids": fx.ApplicationIDs}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		if out, err = st.GetCreditMemo(ctx, id); err != nil {
			return err
		}
		return s.recordEffects(ctx, fx)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// WriteOff writes an amount of an open invoice off as bad debt. Finance roles
// only; the reason is required.
func (s *Service) WriteOff(ctx context.Context, id uuid.UUID, amountCents int64, reason string, pre Precondition, body Transition) (*Invoice, error) {
	if err := requireFinance(body.Role, "writing off an invoice"); err != nil {
		return nil, err
	}
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	st, err := s.store()
	if err != nil {
		return nil, err
	}
	if s.account == nil {
		return nil, fmt.Errorf("invoice: the AR core is not wired")
	}
	var out *Invoice
	err = s.inTx(ctx, func(ctx context.Context) error {
		inv, err := st.LockInvoice(ctx, id) // section 11, step 4
		if err != nil {
			return notFound(err)
		}
		if err := s.checkBranch(ctx, inv.BranchID, "invoice"); err != nil {
			return err
		}
		if err := pre.check(inv.Revision); err != nil {
			return err
		}
		date, err := st.BranchLocalDate(ctx, inv.BranchID, s.now())
		if err != nil {
			return err
		}
		fx, err := s.account.WriteOff(ctx, account.WriteOffIn{InvoiceID: id, AmountCents: amountCents, Reason: reason, Actor: body.Actor, On: date})
		if err != nil {
			return mapPostingError(err)
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "invoice.written_off", EntityType: "invoice", EntityID: id, UserID: body.Actor,
				Changes: map[string]interface{}{"number": inv.Number, "amount_cents": amountCents, "reason": reason}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		if out, err = st.GetInvoice(ctx, id); err != nil {
			return err
		}
		return s.recordEffects(ctx, fx)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListApplications answers the invoice's applications, oldest first, held to the
// branch wall through the invoice read.
func (s *Service) ListApplications(ctx context.Context, invoiceID uuid.UUID) ([]account.Application, error) {
	if _, err := s.repo.GetInvoice(ctx, invoiceID); err != nil {
		return nil, notFound(err)
	}
	if s.account == nil {
		return nil, fmt.Errorf("invoice: the AR core is not wired")
	}
	return s.account.ListApplications(ctx, account.AppFilter{InvoiceID: &invoiceID})
}
