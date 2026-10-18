// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// expectedFromPayments computes what the drawer should hold per method: the
// CASH expectation is the opening float plus the session's cash payments
// (already net of change; change is never subtracted a second time) less
// cash refunds; the CARD figure, informational (cards are not counted into
// the drawer), names what the session net collected on cards, its still
// completed sales' card tenders less the void refunds a resolution booked
// (r12 P3-2), the same net the CASH figure carries for returns; the other
// methods are the pass-through of what the session's sales collected (ADR
// 0005 section 14.2 C2-5). Pure function for testability.
func expectedFromPayments(openingFloat httpx.Cents, tendered map[string]int64, cashRefunds, cardRefunds int64) map[string]int64 {
	expected := make(map[string]int64, len(tendered)+1)
	for m, v := range tendered {
		expected[m] = v
	}
	expected["CASH"] = int64(openingFloat) + tendered["CASH"] - cashRefunds
	// The CARD figure is written only where the session collected on cards:
	// a cash only session's report carries no card figure, as before (a
	// booked card refund always implies the tender that was collected).
	if cardTendered, ok := tendered["CARD"]; ok {
		expected["CARD"] = cardTendered - cardRefunds
	}
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
		ExpectedByMethod: expectedFromPayments(session.OpeningFloat, agg.TenderedByMethod, agg.CashRefundsCents, agg.CardRefundsCents),
	}, nil
}

// CloseTill closes the session: computes the expected per method from the
// session's payments, records the counted amounts, and posts the over/short
// entry inside the close's own transaction (the money rule: no GL entry
// outside the act that causes it).
func (s *Service) CloseTill(ctx context.Context, sessionID uuid.UUID, countedByMethod map[string]int64, notes, actor string) (*TillReport, error) {
	// An act on a session of a branch outside the caller's grants is refused
	// by name before anything runs (SEC-NOHEADER-1); a session that does not
	// exist, or that the caller's branch context hides, answers the 404.
	if err := s.holdTillBranch(ctx, sessionID); err != nil {
		return nil, err
	}
	var out *TillReport
	err := s.inTx(ctx, func(ctx context.Context) error {
		// The session row first, FOR UPDATE: a completion or a void holding
		// it FOR SHARE waits here, so the aggregate below counts every sale
		// that landed and no sale lands while the drawer is being counted.
		if err := s.repo.LockTillSession(ctx, sessionID, true); err != nil {
			return err
		}
		report, err := s.TillReport(ctx, sessionID)
		if err != nil {
			return err
		}
		if report.Session.Status != TillOpen {
			return httpx.InvalidStateTransition("the till session is not open")
		}
		// A card refund claim of one of the session's sales still standing
		// refuses the close (eighth review P2-2): its terminal call may still
		// move money after the drawer was counted, and the void or return
		// behind it would then be refused session_closed with the card
		// already refunded. Under the session lock, before anything is
		// counted.
		claims, err := s.repo.SessionPendingRefundIntentIDs(ctx, sessionID)
		if err != nil {
			return err
		}
		if len(claims) > 0 {
			ids := make([]string, 0, len(claims))
			for _, id := range claims {
				ids = append(ids, id.String())
			}
			return conflict("refund_in_flight",
				fmt.Sprintf("a card refund claim of one of this session's sales is still in flight (claims %s): settle each at POST /api/v1/pos/refund-intents/{id}/resolve before closing the drawer",
					strings.Join(ids, ", ")))
		}
		// A card terminal intent of this register still standing (claimed or
		// at the device) refuses the close the same way (CP-05): its device
		// may still move money after the drawer was counted, and the manager
		// item behind it would name a closed session's register.
		if s.terminalWired() {
			intents, err := s.terminalLedger.OpenIntentsAtRegister(ctx, report.Session.RegisterID)
			if err != nil {
				return err
			}
			if len(intents) > 0 {
				ids := make([]string, 0, len(intents))
				for _, id := range intents {
					ids = append(ids, id.String())
				}
				return conflict("terminal_intent_in_flight",
					fmt.Sprintf("a card terminal intent of this register is still in flight (intents %s): cancel a claim the terminal never reached at POST /api/v1/pos/terminal-intents/{id}/cancel, and sweep the lapsed ones at POST /api/v1/pos/terminal-intents/sweep, before closing the drawer",
						strings.Join(ids, ", ")))
			}
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
