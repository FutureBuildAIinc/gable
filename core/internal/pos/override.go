// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// The kinds of manager override: what the manager waives for the one sale.
const (
	OverrideAccount      = "account"       // an on account sale the verdict blocks
	OverrideCheck        = "check"         // a check over the customer's check limit
	OverrideRefundMethod = "refund_method" // cash refunded against a sale that kept none
)

// devApprover names the approver of a request that carries no claims at all
// (AUTH_MODE=dev, the local development bypass), so the audit and the row
// never hold an empty name.
const devApprover = "dev-mode"

// overrideRoles may approve an override. The approver must also be a
// different person than the sale's cashier.
var overrideRoles = []string{"admin", "owner"}

// AccountOverride is a manager's approval of one sale: one act, a reason, a
// cap in cents, consumed by that sale's completion and never again.
type AccountOverride struct {
	ID         uuid.UUID        `json:"id"`
	SaleID     uuid.UUID        `json:"sale_id"`
	Kind       string           `json:"kind"`
	ApprovedBy string           `json:"approved_by"`
	Reason     string           `json:"reason"`
	CapCents   int64            `json:"cap_cents"`
	CreatedAt  httpx.Timestamp  `json:"created_at"`
	ConsumedAt *httpx.Timestamp `json:"consumed_at"`
}

// OverrideIn is a parsed override request.
type OverrideIn struct {
	Kind     string
	Reason   string
	CapCents int64
}

// overrideRequest is POST /pos/transactions/{id}/overrides.
type overrideRequest struct {
	Kind     *string         `json:"kind"`
	Reason   *string         `json:"reason"`
	CapCents json.RawMessage `json:"cap_cents"`
}

func (r *overrideRequest) parse() (*OverrideIn, error) {
	v := &httpx.Validator{}
	out := &OverrideIn{}
	if r.Kind != nil {
		out.Kind = *r.Kind
	}
	v.Check(out.Kind == OverrideAccount || out.Kind == OverrideCheck || out.Kind == OverrideRefundMethod,
		"kind", "must be account, check or refund_method")
	if r.Reason != nil {
		out.Reason = strings.TrimSpace(*r.Reason)
	}
	v.Required("reason", out.Reason)
	v.Check(len(out.Reason) <= 500, "reason", "must be at most 500 characters")
	if n, ok := v.Int("cap_cents", r.CapCents, true); ok {
		v.Check(n > 0, "cap_cents", "must be greater than zero")
		out.CapCents = n
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ApproveOverride records a manager's approval of one sale. The approver is
// a person other than the sale's cashier (the handler holds the role); the
// sale must still be open, and an account override needs a named customer.
func (s *Service) ApproveOverride(ctx context.Context, saleID uuid.UUID, in *OverrideIn, approver string) (*AccountOverride, error) {
	if approver == "" {
		return nil, httpx.Forbidden("an override is approved by a signed in manager")
	}
	if err := s.holdSaleBranch(ctx, saleID); err != nil {
		return nil, err
	}
	var out *AccountOverride
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, saleID); err != nil {
			return err
		}
		sale, err := s.repo.GetSale(ctx, saleID)
		if err != nil {
			return err
		}
		// an account or check override rides an open sale to its completion; a
		// refund_method override rides a completed one to its returns
		if in.Kind == OverrideRefundMethod {
			if sale.Status != StatusCompleted {
				return httpx.InvalidStateTransition(fmt.Sprintf("cannot approve a refund_method override on a %s sale: returns happen against a completed one", sale.Status.Status()))
			}
		} else if sale.Status != StatusOpen && sale.Status != StatusHeld {
			return httpx.InvalidStateTransition(fmt.Sprintf("cannot approve an override on a %s sale", sale.Status.Status()))
		}
		if strings.EqualFold(sale.CashierID.String(), approver) {
			return httpx.Forbidden("a manager other than the sale's own cashier approves an override")
		}
		if in.Kind == OverrideAccount && sale.CustomerID == nil {
			return conflict("walk_in_account", "an account override needs a named customer: the walk-in customer buys on the barrelhead")
		}
		o := &AccountOverride{SaleID: saleID, Kind: in.Kind, ApprovedBy: approver, Reason: in.Reason, CapCents: in.CapCents}
		if err := s.repo.InsertOverride(ctx, o); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "pos.override.approved", EntityType: "pos_transaction", EntityID: saleID, UserID: approver,
				Changes: map[string]any{"override_id": o.ID, "kind": o.Kind, "cap_cents": o.CapCents, "reason": o.Reason,
					"cashier_id": sale.CashierID, "customer_id": sale.CustomerID}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		out = o
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// consumeOverride spends the sale's live override of kind when it covers
// need cents, inside the completion's transaction: of any number of racing
// completions exactly one spends it. It reports whether one was spent.
func (s *Service) consumeOverride(ctx context.Context, saleID uuid.UUID, kind string, need int64, actor string) (bool, error) {
	o, ok, err := s.repo.ConsumeOverride(ctx, saleID, kind, need)
	if err != nil || !ok {
		return false, err
	}
	// two persons spend a refund_method override: the manager who approved it
	// cannot also be the one whose return or exchange consumes it. The error
	// rolls the consumption back with the caller's transaction.
	if kind == OverrideRefundMethod && actor != "" && actor != devApprover && strings.EqualFold(actor, o.ApprovedBy) {
		return false, httpx.Forbidden("a manager who approved a refund_method override cannot also make the cash return")
	}
	if s.auditLog != nil {
		if err := s.auditLog.Log(ctx, audit.Entry{
			Action: "pos.override.consumed", EntityType: "pos_transaction", EntityID: saleID, UserID: actor,
			Changes: map[string]any{"override_id": o.ID, "kind": kind, "cap_cents": o.CapCents, "used_cents": need,
				"approved_by": o.ApprovedBy, "reason": o.Reason}}); err != nil {
			return false, fmt.Errorf("failed to write the audit row: %w", err)
		}
	}
	return true, nil
}

// checkOnAccount is the one place a sale's account portion meets the
// verdict. consume false is the read before any card is charged (a live
// override that covers the amount passes); consume true runs under the
// customer's credit lock inside the transaction and spends the override.
// storeCredit is the credit memo cents the same sale spends (its own store
// credit leg, and an exchange's returned credit leg): those memos are open
// now and will not be once it completes, so they are added back before the
// verdict, to the exact cent the account ends owing.
func (s *Service) checkOnAccount(ctx context.Context, saleID, customerID uuid.UUID, facts CustomerFacts, amount, storeCredit int64, consume bool, actor string) error {
	open, err := s.repo.OpenReceivableCents(ctx, customerID)
	if err != nil {
		return err
	}
	open += storeCredit
	v := onAccountVerdict(verdictIn{currency: facts.Currency, creditLimit: facts.CreditLimitCents, open: open,
		termsKind: facts.TermsKind, amount: amount})
	if v.Verdict == VerdictAllowed {
		return nil
	}
	if overridable(v.Reason) {
		if consume {
			spent, err := s.consumeOverride(ctx, saleID, OverrideAccount, amount, actor)
			if err != nil {
				return err
			}
			if spent {
				return nil
			}
		} else {
			o, err := s.repo.LiveOverride(ctx, saleID, OverrideAccount)
			if err != nil {
				return err
			}
			if withOverride(v, amount, o, facts.Currency).Verdict == VerdictAllowed {
				return nil
			}
		}
	}
	return verdictError(v)
}

// ApproveOverrideHandler is POST /pos/transactions/{id}/overrides: a
// manager (admin or owner) other than the cashier approves one sale.
func (h *Handler) ApproveOverrideHandler(w http.ResponseWriter, r *http.Request) {
	if refuseMachineKey(w, r, "an override is approved by a person, never a machine key") {
		return
	}
	// A configured deployment always carries claims (the auth layer sets them
	// or answers 401); no claims and no machine key is the dev mode bypass,
	// which every role guard of the module lets through, so it approves as
	// "dev-mode": a person other than any cashier, never an empty name.
	approver := devApprover
	if claims := middleware.ClaimsFromContext(r.Context()); claims != nil {
		if claims.Subject == "" || !middleware.ClaimsHaveAnyRole(claims, overrideRoles...) {
			httpx.WriteError(w, r, httpx.Forbidden("an override is approved by a signed in manager"))
			return
		}
		approver = claims.Subject
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	var req overrideRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	o, err := h.service.ApproveOverride(r.Context(), id, in, approver)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, o)
}

// checkBuyer is the buyer roster rule: an account that asks for a named
// buyer is refused, in words naming the account, a sale on account that
// names none; a named buyer must be on the roster still and the sale's
// account portion must fit the buyer's own cap.
func (s *Service) checkBuyer(ctx context.Context, customerID uuid.UUID, facts CustomerFacts, buyerID *uuid.UUID, amount int64) error {
	if buyerID == nil {
		if facts.NamedBuyerRequired {
			return conflict("buyer_required", fmt.Sprintf(
				"%s asks for a named buyer on every sale on account: name one from the account's buyer roster before charging it", facts.Name))
		}
		return nil
	}
	b, ok, err := s.repo.BuyerOf(ctx, customerID, *buyerID)
	if err != nil {
		return err
	}
	if !ok {
		return conflict("buyer_not_authorized", fmt.Sprintf(
			"the buyer named on the sale is not an authorized buyer of %s: pick one from the account's buyer roster", facts.Name))
	}
	if b.CapCents != nil && amount > int64(*b.CapCents) {
		return conflict("buyer_cap", fmt.Sprintf("%s may put at most %s on %s's account in one sale and this sale puts %s",
			b.Name, moneyWords(facts.Currency, int64(*b.CapCents)), facts.Name, moneyWords(facts.Currency, amount)))
	}
	return nil
}
