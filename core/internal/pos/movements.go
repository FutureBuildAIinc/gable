// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// EventTillMovement is the event of a recorded movement (ADR 0005 section 12).
const EventTillMovement = "till.movement_recorded"

// MovementKind is what moved: cash to the safe, cash taken by a second
// person, cash in from outside a sale, cash out for an expense. Lowercase in
// storage and on the wire (ADR 0001).
type MovementKind string

const (
	MovementSafeDrop MovementKind = "safe_drop"
	MovementPickup   MovementKind = "pickup"
	MovementPaidIn   MovementKind = "paid_in"
	MovementPaidOut  MovementKind = "paid_out"
)

// lowersCash reports whether the kind takes cash out of the drawer.
func (k MovementKind) lowersCash() bool { return k != MovementPaidIn }

// movementReason is one code of the closed list a paid in or paid out names,
// with the account the other side of its entry posts to. A code absent from
// a kind's column is not a reason for that kind.
type movementReason struct {
	paidOutAccount string
	paidInAccount  string
}

// movementReasons is the closed lowercase reason list. Paid out debits an
// expense account; paid in credits an income (or contra expense) account, the
// drawer cash on the other side.
var movementReasons = map[string]movementReason{
	"supplies":      {paidOutAccount: "5020"},
	"freight":       {paidOutAccount: "5020"},
	"fuel":          {paidOutAccount: "5020"},
	"postage":       {paidOutAccount: "5020"},
	"meals":         {paidOutAccount: "5020"},
	"repairs":       {paidOutAccount: "5020"},
	"fee_received":  {paidInAccount: "4030"},
	"vendor_refund": {paidInAccount: "5020"},
	"other":         {paidOutAccount: "5020", paidInAccount: "4090"},
}

// MovementReasonCodes lists the closed set, for the contract and the screen.
func MovementReasonCodes(kind MovementKind) []string {
	var out []string
	for _, code := range []string{"supplies", "freight", "fuel", "postage", "meals", "repairs", "fee_received", "vendor_refund", "other"} {
		r := movementReasons[code]
		if (kind == MovementPaidOut && r.paidOutAccount != "") || (kind == MovementPaidIn && r.paidInAccount != "") {
			out = append(out, code)
		}
	}
	return out
}

// TillMovement is one movement of cash in or out of a drawer.
type TillMovement struct {
	ID            uuid.UUID       `json:"id"`
	TillSessionID uuid.UUID       `json:"till_session_id"`
	RegisterID    string          `json:"register_id"`
	BranchID      uuid.UUID       `json:"branch_id"`
	Kind          MovementKind    `json:"kind"`
	AmountCents   httpx.Cents     `json:"amount_cents"`
	ReasonCode    *string         `json:"reason_code"`
	Note          string          `json:"note"`
	Actor         string          `json:"actor"`
	TakenBy       *string         `json:"taken_by"`
	GLEntryID     *uuid.UUID      `json:"gl_entry_id"`
	CreatedAt     httpx.Timestamp `json:"created_at"`
}

// MovementIn is a parsed movement request.
type MovementIn struct {
	Kind        MovementKind
	AmountCents httpx.Cents
	ReasonCode  string
	Note        string
	TakenBy     string
}

// cashEffect is the movement's effect on the drawer's expected cash.
func (m *TillMovement) cashEffect() int64 {
	if m.Kind.lowersCash() {
		return -int64(m.AmountCents)
	}
	return int64(m.AmountCents)
}

// movementNetCash sums the movements' effect on the drawer's expected cash.
func movementNetCash(totals map[string]int64) int64 {
	return totals[string(MovementPaidIn)] - totals[string(MovementPaidOut)] -
		totals[string(MovementSafeDrop)] - totals[string(MovementPickup)]
}

// movementLegs is the entry a movement posts, by account code: cash out of
// the drawer to undeposited funds (a drop or a pickup) or to the reason's
// expense account (paid out), cash into the drawer from the reason's income
// account (paid in).
func movementLegs(m *TillMovement) []gl.Leg {
	amount := int64(m.AmountCents)
	reason := ""
	if m.ReasonCode != nil {
		reason = *m.ReasonCode
	}
	switch m.Kind {
	case MovementSafeDrop, MovementPickup:
		return []gl.Leg{
			{AccountCode: "1015", Description: "Undeposited funds", Debit: amount},
			{AccountCode: gl.AccountCodeCash, Description: "Cash out of the drawer", Credit: amount},
		}
	case MovementPaidOut:
		return []gl.Leg{
			{AccountCode: movementReasons[reason].paidOutAccount, Description: "Paid out: " + reason, Debit: amount},
			{AccountCode: gl.AccountCodeCash, Description: "Cash out of the drawer", Credit: amount},
		}
	default:
		return []gl.Leg{
			{AccountCode: gl.AccountCodeCash, Description: "Cash into the drawer", Debit: amount},
			{AccountCode: movementReasons[reason].paidInAccount, Description: "Paid in: " + reason, Credit: amount},
		}
	}
}

// RecordMovement records a drop, pickup, paid in or paid out against an open
// till session: the row, its journal entry, the audit row and the event in
// one transaction. The session is held FOR SHARE against the close's FOR
// UPDATE, so a movement lands in the count or is refused, never after it.
//
// A pickup is different (CP-08, PR 178 sweep P2-2): the taker, a second
// signed-in person, confirms under their own subject first (confirmed true,
// nothing moves yet), and the cashier's later record of that confirmed
// session, amount and taker is the movement: its stored taken_by is the
// taker's authenticated subject, never a name the cashier typed. A cashier
// recording a pickup no taker confirmed is refused and nothing is written.
func (s *Service) RecordMovement(ctx context.Context, sessionID uuid.UUID, in MovementIn, actor string) (*TillMovement, bool, error) {
	if err := s.holdTillBranch(ctx, sessionID); err != nil {
		return nil, false, err
	}
	var out *TillMovement
	var confirmed bool
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockTillSession(ctx, sessionID, false); err != nil {
			return err
		}
		session, err := s.repo.GetTillSession(ctx, sessionID)
		if err != nil {
			return err
		}
		if session.Status != TillOpen {
			return conflict("session_closed",
				"the till session is not open: its cash has been counted and no movement can be recorded in it")
		}
		if session.BranchID == nil {
			return conflict("register_without_branch", "the session's register belongs to no branch")
		}
		takenBy := ""
		if in.Kind == MovementPickup {
			taker := strings.TrimSpace(in.TakenBy)
			cashier := session.CashierID.String()
			if actor == "" || taker == "" || strings.EqualFold(taker, cashier) {
				return conflict("pickup_needs_second_person",
					"a pickup is cash taken by a second person: the cashier cannot take it alone")
			}
			if strings.EqualFold(actor, taker) {
				// the signed-in taker confirms taking the cash: the approval
				// waits, unconsumed, for the cashier's record
				if err := s.repo.RecordPickupApproval(ctx, sessionID, int64(in.AmountCents), actor); err != nil {
					return err
				}
				taker = actor
				out = &TillMovement{TillSessionID: sessionID, RegisterID: session.RegisterID, BranchID: *session.BranchID,
					Kind: in.Kind, AmountCents: in.AmountCents, Actor: actor, TakenBy: &taker}
				confirmed = true
				return nil
			}
			// the cashier (or anyone) records it: only the taker's own
			// confirmation of this session and amount lets the cash go
			approved, ok, err := s.repo.ConsumePickupApproval(ctx, sessionID, int64(in.AmountCents), taker)
			if err != nil {
				return err
			}
			if !ok {
				return conflict("pickup_needs_second_person",
					"a pickup is cash taken by a second signed-in person: the taker must confirm the pickup under their own login before the movement is recorded")
			}
			takenBy = approved
		}
		m := &TillMovement{ID: uuid.New(), TillSessionID: sessionID, RegisterID: session.RegisterID,
			BranchID: *session.BranchID, Kind: in.Kind, AmountCents: in.AmountCents, Note: strings.TrimSpace(in.Note), Actor: actor}
		if in.ReasonCode != "" {
			code := in.ReasonCode
			m.ReasonCode = &code
		}
		if in.Kind == MovementPickup {
			m.TakenBy = &takenBy
		}
		if s.ledger != nil {
			day, err := s.repo.BranchToday(ctx, m.BranchID)
			if err != nil {
				return err
			}
			entry, err := s.ledger.PostEntry(ctx, gl.PostingInput{
				EntryDate: day, Memo: fmt.Sprintf("Till %s: session %s", strings.ReplaceAll(string(m.Kind), "_", " "), sessionID),
				Source: gl.SourceAdjustment, SourceRefID: &m.ID, BranchID: m.BranchID, PostedBy: actorOrSystem(actor), Legs: movementLegs(m),
			})
			if err != nil {
				return ledgerRefusal(err, "failed to post the movement")
			}
			if entry != nil {
				m.GLEntryID = &entry.ID
			}
		}
		if err := s.repo.CreateTillMovement(ctx, m); err != nil {
			return err
		}
		if s.auditLog != nil {
			changes := map[string]any{"kind": string(m.Kind), "amount_cents": int64(m.AmountCents),
				"till_session_id": sessionID, "register_id": m.RegisterID}
			if m.ReasonCode != nil {
				changes["reason_code"] = *m.ReasonCode
			}
			if m.TakenBy != nil {
				changes["taken_by"] = *m.TakenBy
			}
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "pos.till.movement", EntityType: "till_movement",
				EntityID: m.ID, UserID: actor, Changes: changes}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		branch := m.BranchID
		if err := s.recordEvent(ctx, outbox.Event{Type: EventTillMovement, EntityType: "till_movement",
			EntityID: m.ID, BranchID: &branch, Data: eventJSON(map[string]any{
				"till_session_id": sessionID, "register_id": m.RegisterID, "kind": string(m.Kind),
				"amount_cents": int64(m.AmountCents),
			})}); err != nil {
			return err
		}
		out = m
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, confirmed, nil
}

// actorOrSystem names the poster on the ledger line: the person, or the
// system when the call carries no identity (a dev mode caller).
func actorOrSystem(actor string) string {
	if actor == "" {
		return "system"
	}
	return actor
}

// ListMovements pages a session's movements, oldest first, through the
// caller's wall: a session the wall hides is a missing one.
func (s *Service) ListMovements(ctx context.Context, sessionID uuid.UUID, after *time.Time, afterID *uuid.UUID, limit int) ([]TillMovement, bool, error) {
	if _, err := s.repo.GetTillSession(ctx, sessionID); err != nil {
		return nil, false, err
	}
	return s.repo.ListTillMovementsPage(ctx, sessionID, after, afterID, limit)
}

// ledgerRefusal maps the ledger's refusals of a posting to the 409 that tells
// the operator what to mend: a closed fiscal period (the 077 trigger's
// message, however the poster wrapped it) and a code the chart lacks. Any
// other error is the act's own failure.
func ledgerRefusal(err error, what string) error {
	switch {
	case errors.Is(err, gl.ErrPeriodClosed) || strings.Contains(err.Error(), "closed fiscal period"):
		return conflict("period_closed", "the entry would be dated into a closed fiscal period")
	case errors.Is(err, gl.ErrAlreadyReversed) || errors.Is(err, gl.ErrNotPosted):
		return conflict("deposit_entry_reversed", "the deposit entry is already reversed or no longer posted")
	case errors.Is(err, gl.ErrAccountMissing):
		return conflict("account_missing", "the chart of accounts lacks an account this entry posts to: "+err.Error())
	}
	return fmt.Errorf("%s: %w", what, err)
}
