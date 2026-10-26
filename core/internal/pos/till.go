// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/gl"
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
// 0005 section 14.2 C2-5). The session's movements move the CASH figure: paid
// in raises it, paid out, drops and pickups lower it (movementNet, CP-08).
// Pure function for testability.
func expectedFromPayments(openingFloat httpx.Cents, tendered map[string]int64, cashRefunds, cardRefunds, movementNet int64) map[string]int64 {
	expected := make(map[string]int64, len(tendered)+1)
	for m, v := range tendered {
		expected[m] = v
	}
	expected["CASH"] = int64(openingFloat) + tendered["CASH"] - cashRefunds + movementNet
	// The CARD figure is written only where the session collected on cards:
	// a cash only session's report carries no card figure, as before (a
	// booked card refund always implies the tender that was collected).
	if cardTendered, ok := tendered["CARD"]; ok {
		expected["CARD"] = cardTendered - cardRefunds
	}
	return expected
}

// overShortTotal is the drawer's variance: counted less expected over the
// methods that are money in the drawer, CASH and CHECK, as the signed net the
// per-method lines post on the ledger. CARD never joins the sum (cards are
// not counted into the drawer), so a card figure can never offset a cash or
// check shortage (CP-08). The methods never hide inside the net either:
// over_short_by_method carries each method's own variance, and the posting
// moves the books by each of those figures, so an opposite-sign pair still
// posts both sides.
func overShortTotal(expected, counted map[string]int64) int64 {
	cashVar := counted["CASH"] - expected["CASH"]
	var checkVar int64
	if _, ok := counted["CHECK"]; ok {
		checkVar = counted["CHECK"] - expected["CHECK"]
	} else if expected["CHECK"] != 0 {
		checkVar = -expected["CHECK"]
	}
	return cashVar + checkVar
}

// overShortByMethod is the variance per drawer method (CP-08): what the
// count compared, method by method, so a report or sign off can show the
// cash shortage beside the check overage the net alone would hide. CHECK
// appears only when the session counted checks or expected them.
func overShortByMethod(expected, counted map[string]int64) map[string]int64 {
	out := map[string]int64{
		"CASH": counted["CASH"] - expected["CASH"],
	}
	if _, ok := counted["CHECK"]; ok || expected["CHECK"] != 0 {
		out["CHECK"] = counted["CHECK"] - expected["CHECK"]
	}
	return out
}

// hasDrawerVariance answers whether any drawer method carries a non-zero
// variance: such a count posts, even when the methods net to zero.
func hasDrawerVariance(byMethod map[string]int64) bool {
	for _, m := range []string{string(TenderCash), string(TenderCheck)} {
		if byMethod[m] != 0 {
			return true
		}
	}
	return false
}

// postTillOverShortByMethod posts the drawer's variance as one journal entry
// in which each non-zero drawer method's variance is its own pair of lines
// (CP-08): cash and check each move 1010 by their own counted figure, never
// by the net alone, so the ledger equals the money the drawer holds. A count
// with no variance on any method posts nothing.
func (s *Service) postTillOverShortByMethod(ctx context.Context, branchID, sessionID uuid.UUID, byMethod map[string]int64, actor string) (uuid.UUID, error) {
	var legs []gl.Leg
	for _, m := range []string{string(TenderCash), string(TenderCheck)} {
		v := byMethod[m]
		if v == 0 {
			continue
		}
		name := "Cash"
		if m == string(TenderCheck) {
			name = "Check"
		}
		if v < 0 {
			mag := -v
			legs = append(legs,
				gl.Leg{AccountCode: gl.AccountCodeOverShort, Description: fmt.Sprintf("%s short (%s)", name, m), Debit: mag},
				gl.Leg{AccountCode: gl.AccountCodeCash, Description: fmt.Sprintf("%s (%s)", name, m), Credit: mag})
		} else {
			mag := v
			legs = append(legs,
				gl.Leg{AccountCode: gl.AccountCodeCash, Description: fmt.Sprintf("%s (%s)", name, m), Debit: mag},
				gl.Leg{AccountCode: gl.AccountCodeOverShort, Description: fmt.Sprintf("%s over (%s)", name, m), Credit: mag})
		}
	}
	if len(legs) == 0 {
		return uuid.Nil, nil
	}
	day, err := s.repo.BranchToday(ctx, branchID)
	if err != nil {
		return uuid.Nil, err
	}
	if day.IsZero() {
		day = time.Now().UTC()
	}
	entry, err := s.ledger.PostEntry(ctx, gl.PostingInput{
		EntryDate:   day,
		Memo:        fmt.Sprintf("Till over/short - session %s", sessionID),
		Source:      gl.SourceAdjustment,
		SourceRefID: &sessionID,
		BranchID:    branchID,
		PostedBy:    actorOrSystem(actor),
		Legs:        legs,
	})
	if err != nil {
		return uuid.Nil, err
	}
	return entry.ID, nil
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
		// The branch's day lock, the one the day close takes, so a drawer never
		// opens between that close's check and its write; and a branch whose
		// business date is closed takes no new drawer (CP-09).
		if branch != nil {
			if err := s.repo.LockBranchDay(ctx, *branch); err != nil {
				return err
			}
			closed, err := s.repo.BranchDayClosed(ctx, *branch)
			if err != nil {
				return err
			}
			if closed {
				return conflict("day_closed", "the branch's business date is closed: no drawer opens until the next day")
			}
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
	// A counted session carries its variance per method (CP-08): the report
	// shows what the count compared, method by method.
	if session.Status != TillOpen {
		session.OverShortByMethod = overShortByMethod(session.ExpectedByMethod, session.CountedByMethod)
	}
	agg, err := s.repo.AggregateTillSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	movements, err := s.repo.ListTillMovements(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return &TillReport{
		Session:              *session,
		SaleCount:            agg.SaleCount,
		SalesTotalCents:      httpx.Cents(agg.SalesTotalCents),
		TaxTotalCents:        httpx.Cents(agg.TaxTotalCents),
		ChangeCents:          httpx.Cents(agg.ChangeCents),
		TenderedByMethod:     agg.TenderedByMethod,
		ExpectedByMethod:     expectedFromPayments(session.OpeningFloat, agg.TenderedByMethod, agg.CashRefundsCents, agg.CardRefundsCents, movementNetCash(agg.MovementTotals)),
		OverShortByMethod:    session.OverShortByMethod,
		RefundsByMethod:      refundsByMethod(agg),
		Movements:            movements,
		MovementTotalsByKind: agg.MovementTotals,
	}, nil
}

// refundsByMethod names the session's refunds per method, a method present
// only where the session refunded on it (a cash only session carries no CARD
// figure, as its expected map carries none).
func refundsByMethod(agg *TillAggregate) map[string]int64 {
	out := map[string]int64{}
	if agg.CashRefundsCents > 0 {
		out["CASH"] = agg.CashRefundsCents
	}
	if agg.CardRefundsCents > 0 {
		out["CARD"] = agg.CardRefundsCents
	}
	return out
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
		// A card terminal intent of this register still standing (claimed, at
		// the device, or an authorized sale leg the books have not yet taken)
		// refuses the close the same way (CP-05): its device may still move
		// money after the drawer was counted, or the terminal already holds an
		// authorized charge no sale has booked.
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
					fmt.Sprintf("a card terminal intent of this register is still open (intents %s): complete or void the open sale using an authorized leg, cancel a claim the terminal never reached at POST /api/v1/pos/terminal-intents/{id}/cancel, or wait for the worker's sweep (or a manager with finance or owner role at POST /api/v1/pos/terminal-intents/sweep) and have a manager (finance or owner) settle it before closing the drawer",
						strings.Join(ids, ", ")))
			}
		}
		// The count is complete and honest before anything is computed (CP-08):
		// cash is always counted, checks are counted when checks were tendered,
		// and a card figure, informational and not money in the drawer, is
		// refused unless it names exactly what the session collected (so it can
		// never hide or offset a drawer variance).
		if _, hasCash := countedByMethod[string(TenderCash)]; !hasCash {
			return httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: "counted_by_method.cash", Message: "is required: cash in the drawer must be counted"})
		}
		if report.ExpectedByMethod[string(TenderCheck)] > 0 {
			if _, hasCheck := countedByMethod[string(TenderCheck)]; !hasCheck {
				return httpx.BadRequest("one or more fields failed validation",
					httpx.FieldError{Field: "counted_by_method.check", Message: "is required when checks were tendered in the session"})
			}
		}
		if cardCounted, hasCard := countedByMethod[string(TenderCard)]; hasCard {
			if report.ExpectedByMethod[string(TenderCard)] == 0 {
				return httpx.BadRequest("one or more fields failed validation",
					httpx.FieldError{Field: "counted_by_method.card", Message: "no card tenders were collected in this session"})
			}
			if cardCounted != report.ExpectedByMethod[string(TenderCard)] {
				return httpx.BadRequest("one or more fields failed validation",
					httpx.FieldError{Field: "counted_by_method.card", Message: "must equal the card the session collected: cards are not money in the drawer"})
			}
		}
		overShort := overShortTotal(report.ExpectedByMethod, countedByMethod)
		// A count whose over or short passes the branch's variance threshold
		// is held for a manager (CP-08): the drawer is counted and out of
		// use, its variance is not on the ledger yet, and the sign off posts
		// it. Inside the threshold the close posts as ever.
		threshold, thresholdSet, err := s.repo.VarianceThreshold(ctx, report.Session.BranchID)
		if err != nil {
			return err
		}
		// The hold is per method as well as on the net: a shortage on cash
		// offset by an overage on checks is still a variance a manager must
		// sign off, never a clean net close (CP-08).
		hold := thresholdSet && abs64(overShort) > threshold
		if thresholdSet {
			for _, m := range []string{string(TenderCash), string(TenderCheck)} {
				exp := report.ExpectedByMethod[m]
				cnt, ok := countedByMethod[m]
				if ok || exp != 0 {
					if abs64(cnt-exp) > threshold {
						hold = true
					}
				}
			}
		}
		now := time.Now()
		session := report.Session
		session.Status = TillClosed
		if hold {
			session.Status = TillPendingSignoff
		}
		session.ClosedBy = actorPtr(actor)
		if thresholdSet {
			t := httpx.Cents(threshold)
			session.VarianceThreshold = &t
		}
		session.ClosedAt = httpx.PtrTimestamp(&now)
		session.ExpectedByMethod = report.ExpectedByMethod
		session.CountedByMethod = countedByMethod
		cents := httpx.Cents(overShort)
		session.OverShort = &cents
		session.OverShortByMethod = overShortByMethod(report.ExpectedByMethod, countedByMethod)
		session.Notes = notes
		// The over/short entry, inside the close's transaction, linked back
		// onto the session: each method's variance as its own pair of lines,
		// so the books move by what every drawer method counted.
		if s.ledger != nil && !hold && hasDrawerVariance(session.OverShortByMethod) {
			if session.BranchID == nil {
				return fmt.Errorf("the till session names no branch: its over/short cannot post")
			}
			glID, err := s.postTillOverShortByMethod(ctx, *session.BranchID, sessionID, session.OverShortByMethod, actor)
			if err != nil {
				return ledgerRefusal(err, "failed to post the drawer variance")
			}
			if glID != uuid.Nil {
				session.GLEntryID = &glID
			}
		}
		if err := s.repo.CloseTillSession(ctx, &session); err != nil {
			return err
		}
		report.Session = session
		report.OverShortByMethod = session.OverShortByMethod
		if hold {
			// the Z snapshot waits for the sign off, which freezes it
			if err := s.recordHeldClose(ctx, report, overShort, threshold, actor); err != nil {
				return err
			}
			out = report
			return nil
		}
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

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// GetZReport returns the frozen Z snapshot for a session.
func (s *Service) GetZReport(ctx context.Context, sessionID uuid.UUID) (*ZReport, error) {
	return s.repo.GetZReportBySession(ctx, sessionID)
}

// ListZReports lists Z snapshots for a register on a date.
func (s *Service) ListZReports(ctx context.Context, registerID string, date time.Time) ([]ZReport, error) {
	return s.repo.ListZReports(ctx, registerID, date)
}
