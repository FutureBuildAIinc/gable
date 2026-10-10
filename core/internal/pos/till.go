// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// expectedFromPayments computes what the drawer should hold per method: the
// CASH expectation is the opening float plus the session's cash payments
// (already net of change; change is never subtracted a second time) less
// cash refunds; the other methods are the pass-through of what the session's
// sales collected (ADR 0005 section 14.2 C2-5). Pure function for
// testability.
func expectedFromPayments(openingFloat httpx.Cents, tendered map[string]int64, cashRefunds int64) map[string]int64 {
	expected := make(map[string]int64, len(tendered)+1)
	for m, v := range tendered {
		expected[m] = v
	}
	expected["CASH"] = int64(openingFloat) + tendered["CASH"] - cashRefunds
	return expected
}

// overShortTotal sums counted less expected across the methods that were
// counted. Methods not counted are skipped (a dealer may only count cash).
func overShortTotal(expected, counted map[string]int64) int64 {
	var total int64
	for method, c := range counted {
		total += c - expected[method]
	}
	return total
}

// OpenTill opens a drawer session on a register.
func (s *Service) OpenTill(ctx context.Context, registerID string, cashierID uuid.UUID, openingFloat httpx.Cents, actor string) (*TillSession, error) {
	if existing, err := s.repo.GetOpenTillSession(ctx, registerID); err == nil && existing != nil {
		return nil, conflict("till_open", fmt.Sprintf("register %s already has an open till session", registerID))
	}
	var out *TillSession
	err := s.inTx(ctx, func(ctx context.Context) error {
		branch, err := s.repo.GetRegisterBranch(ctx, registerID)
		if err != nil {
			return err
		}
		session := &TillSession{RegisterID: registerID, BranchID: branch, CashierID: cashierID,
			Status: TillOpen, OpeningFloat: openingFloat,
			ExpectedByMethod: map[string]int64{}, CountedByMethod: map[string]int64{}}
		if err := s.repo.CreateTillSession(ctx, session); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "pos.till.opened", EntityType: "till_session", EntityID: session.ID, UserID: actor,
				Changes: map[string]any{"register_id": registerID, "opening_float_cents": int64(openingFloat)},
			}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		if session.BranchID != nil {
			branch := *session.BranchID
			if err := s.recordEvent(ctx, outbox.Event{Type: EventTillOpened, EntityType: "till_session",
				EntityID: session.ID, BranchID: &branch, Data: eventJSON(map[string]any{
					"register_id": registerID, "status": TillOpen.Status(),
					"opening_float_cents": int64(openingFloat), "cashier_id": cashierID,
				})}); err != nil {
				return err
			}
		}
		out = session
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CurrentTill returns the register's open session, or nil.
func (s *Service) CurrentTill(ctx context.Context, registerID string) (*TillSession, error) {
	return s.repo.GetOpenTillSession(ctx, registerID)
}

// TillReport builds the live X-report for a session.
func (s *Service) TillReport(ctx context.Context, sessionID uuid.UUID) (*TillReport, error) {
	session, err := s.repo.GetTillSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	agg, err := s.repo.AggregateTillSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return &TillReport{
		Session:          *session,
		SaleCount:        agg.SaleCount,
		SalesTotalCents:  httpx.Cents(agg.SalesTotalCents),
		TaxTotalCents:    httpx.Cents(agg.TaxTotalCents),
		ChangeCents:      httpx.Cents(agg.ChangeCents),
		TenderedByMethod: agg.TenderedByMethod,
		ExpectedByMethod: expectedFromPayments(session.OpeningFloat, agg.TenderedByMethod, agg.CashRefundsCents),
	}, nil
}

// CloseTill closes the session: computes the expected per method from the
// session's payments, records the counted amounts, and posts the over/short
// entry inside the close's own transaction (the money rule: no GL entry
// outside the act that causes it).
func (s *Service) CloseTill(ctx context.Context, sessionID uuid.UUID, countedByMethod map[string]int64, notes, actor string) (*TillReport, error) {
	var out *TillReport
	err := s.inTx(ctx, func(ctx context.Context) error {
		report, err := s.TillReport(ctx, sessionID)
		if err != nil {
			return err
		}
		if report.Session.Status != TillOpen {
			return httpx.InvalidStateTransition("the till session is not open")
		}
		overShort := overShortTotal(report.ExpectedByMethod, countedByMethod)
		now := time.Now()
		session := report.Session
		session.Status = TillClosed
		session.ClosedAt = httpx.PtrTimestamp(&now)
		session.ExpectedByMethod = report.ExpectedByMethod
		session.CountedByMethod = countedByMethod
		cents := httpx.Cents(overShort)
		session.OverShort = &cents
		session.Notes = notes
		// The over/short entry, inside the close's transaction, linked back
		// onto the session.
		if s.ledger != nil && overShort != 0 {
			glID, err := s.ledger.PostTillOverShort(ctx, sessionID, overShort)
			if err != nil {
				return fmt.Errorf("failed to post the drawer variance: %w", err)
			}
			if glID != uuid.Nil {
				session.GLEntryID = &glID
			}
		}
		if err := s.repo.CloseTillSession(ctx, &session); err != nil {
			return err
		}
		report.Session = session
		// The frozen Z snapshot, in the same transaction.
		if err := s.generateZReport(ctx, report); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "pos.till.closed", EntityType: "till_session", EntityID: sessionID, UserID: actor,
				Changes: map[string]any{
					"register_id": session.RegisterID, "over_short_cents": overShort,
					"sales_total_cents": aggSales(report), "sale_count": report.SaleCount,
				}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		if session.BranchID != nil {
			branch := *session.BranchID
			if err := s.recordEvent(ctx, outbox.Event{Type: EventTillClosed, EntityType: "till_session",
				EntityID: sessionID, BranchID: &branch, Data: eventJSON(map[string]any{
					"register_id": session.RegisterID, "status": TillClosed.Status(),
					"from_status": TillOpen.Status(), "over_short_cents": overShort,
					"sale_count": report.SaleCount,
				})}); err != nil {
				return err
			}
		}
		out = report
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func aggSales(r *TillReport) int64 { return int64(r.SalesTotalCents) }

// GetZReport returns the frozen Z snapshot for a session.
func (s *Service) GetZReport(ctx context.Context, sessionID uuid.UUID) (*ZReport, error) {
	return s.repo.GetZReportBySession(ctx, sessionID)
}

// ListZReports lists Z snapshots for a register on a date.
func (s *Service) ListZReports(ctx context.Context, registerID string, date time.Time) ([]ZReport, error) {
	return s.repo.ListZReports(ctx, registerID, date)
}
