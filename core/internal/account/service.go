// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

import (
	"context"
	"fmt"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// EventRecorder writes a domain event into the transactional outbox, as the
// LAST statement of the mutation's transaction (ADR 0003 section 2).
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// TxRunner runs fn inside one transaction, joining the caller's when ctx
// already carries one. *database.DB satisfies it.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// WithTxRunner replaces the database as the transaction runner of the HTTP
// level acts (a test gates transactions with it; serve leaves the database).
func (s *Service) WithTxRunner(tx TxRunner) *Service { s.tx = tx; return s }

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx != nil {
		return s.tx.RunInTx(ctx, fn)
	}
	return s.db.RunInTx(ctx, fn)
}

// WithAuditLog sets the audit logger the HTTP level acts write through.
func (s *Service) WithAuditLog(l *audit.Logger) *Service { s.auditLog = l; return s }

// WithOutbox sets the event writer of the HTTP level acts.
func (s *Service) WithOutbox(e EventRecorder) *Service { s.events = e; return s }

// RecordEvents writes the events of an act through the outbox, in order.
// Owners of an act call it last inside the act's transaction.
func (s *Service) RecordEvents(ctx context.Context, evs []outbox.Event) error {
	if s.events == nil {
		return nil
	}
	for _, ev := range evs {
		if err := s.events.Write(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

// FinanceRoles may write off an invoice, void a payment and reverse any
// application (ADR 0005 sections 9.2 and 9.4).
func FinanceRole(role string) bool {
	switch strings.ToLower(role) {
	case "admin", "owner", "finance":
		return true
	}
	return false
}

// ReverseRequest is the body of POST /api/v1/ar/applications/{id}/reverse.
type ReverseRequest struct {
	Reason string
	Actor  string
	Role   string
}

// ReverseApplication reverses one live application in one transaction: the
// core's reversal, the audit row, then the events last. A role outside the
// finance roles is refused when the caller has one; an in process caller with
// none (a machine key, a test) is not.
func (s *Service) ReverseApplication(ctx context.Context, id uuid.UUID, req ReverseRequest) (*Application, error) {
	if req.Role != "" && !FinanceRole(req.Role) {
		return nil, httpx.Forbidden("reversing an application needs the admin, owner or finance role")
	}
	var out *Application
	err := s.inTx(ctx, func(ctx context.Context) error {
		app, err := s.GetApplication(ctx, id)
		if err != nil {
			return err
		}
		branch := uuid.Nil
		if err := s.ex(ctx).QueryRow(ctx, `SELECT branch_id FROM invoices WHERE id = $1`, app.InvoiceID).Scan(&branch); err != nil {
			return fmt.Errorf("failed to read the invoice's branch: %w", err)
		}
		on, err := s.LocalDate(ctx, branch, s.now())
		if err != nil {
			return err
		}
		fx, err := s.Reverse(ctx, ReverseIn{ApplicationID: id, Reason: req.Reason, Actor: req.Actor, On: on})
		if err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "application.reversed", EntityType: "ar_application", EntityID: id, UserID: req.Actor,
				Changes: map[string]interface{}{"kind": lower(string(app.Kind)), "invoice_id": app.InvoiceID, "amount_cents": int64(app.AmountCents),
					"reason": req.Reason, "reversed_ids": fx.ReversedIDs}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		if out, err = s.GetApplication(ctx, id); err != nil {
			return err
		}
		return s.RecordEvents(ctx, fx.Events())
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
