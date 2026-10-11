// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// ErrChargeNotReversed: the card was charged, the system refused the payment,
// and neither the void nor the refund went through.
var ErrChargeNotReversed = errors.New("the card was charged and the charge could not be reversed")

// ChargeNotReversedError is the error CreateCard returns when the reversal
// failed. It carries the gateway transaction id finance reconciles and nothing
// else about the card; errors.Is(err, ErrChargeNotReversed) holds.
type ChargeNotReversedError struct {
	GatewayTxID string
	Cause       error
}

func (e *ChargeNotReversedError) Error() string {
	return fmt.Sprintf("%s (gateway transaction %s): %v", ErrChargeNotReversed, e.GatewayTxID, e.Cause)
}

func (e *ChargeNotReversedError) Is(target error) bool { return target == ErrChargeNotReversed }

func (e *ChargeNotReversedError) Unwrap() error { return e.Cause }

// Wire is the 502 the route answers: the card WAS charged and nothing gave it
// back, with the gateway transaction id and nothing else about the card.
func (e *ChargeNotReversedError) Wire() *httpx.Error {
	return &httpx.Error{Status: http.StatusBadGateway, Code: httpx.CodeChargeNotReversed, Operator: true,
		Message: fmt.Sprintf("the card was charged and the charge could not be reversed; finance must reconcile gateway transaction %s", e.GatewayTxID)}
}

// reversalTimeout bounds the gateway calls and the audit write of a reversal,
// which run detached from the request.
const reversalTimeout = 30 * time.Second

// chargeReversal is how a reversal of an approved charge ended.
type chargeReversal string

const (
	chargeVoided   chargeReversal = "voided"
	chargeRefunded chargeReversal = "refunded"
	chargeFailed   chargeReversal = "failed"
)

// CreateCard takes a card payment through the gateway (the charge is outside
// the transaction, as every gateway call is), then records it the way Create
// does. When the system refuses the payment after the gateway approved it (an
// invoice voided meanwhile, an application over what is open) the charge is
// reversed before the refusal returns; when the reversal fails too the route
// answers 502 charge_not_reversed.
func (s *Service) CreateCard(ctx context.Context, in *Input, who Caller) (*Payment, error) {
	if s.gateway == nil {
		return nil, httpx.Unavailable("payment gateway not configured: set RUN_PAYMENTS_API_KEY")
	}
	res, err := s.resolve(ctx, in)
	if err != nil {
		return nil, err
	}
	// An invoice that cannot take the payment is refused before the card is
	// charged (the locked reads inside the transaction repeat the checks).
	firstInvoice := ""
	for i, a := range in.Applications {
		f, err := s.repo.InvoiceFactsFor(ctx, a.InvoiceID)
		if err != nil {
			return nil, err
		}
		switch {
		case f == nil:
			return nil, fieldError(fmt.Sprintf("applications[%d].invoice_id", i), "no such invoice")
		case f.Status == "VOID":
			return nil, conflictBlocker("invoice_void", "the invoice is void: it takes no application")
		case a.AmountCents+a.DiscountCents > f.Open:
			return nil, conflictBlocker("exceeds_open_amount", fmt.Sprintf("invoice %s has %d cents open: %d cents were asked", f.Number, f.Open, a.AmountCents+a.DiscountCents))
		}
		if firstInvoice == "" {
			firstInvoice = a.InvoiceID.String()
		}
	}

	result, err := s.gateway.Charge(ctx, ChargeRequest{
		TokenID: in.TokenID, AmountCents: in.AmountCents, Currency: res.currency,
		Description: "Payment " + in.CustomerID.String()[:8], InvoiceID: firstInvoice, CustomerID: in.CustomerID.String(),
	})
	if err != nil {
		return nil, &httpx.Error{Status: http.StatusPaymentRequired, Code: httpx.CodePaymentRequired, Message: "card payment failed: the gateway could not be reached"}
	}
	if result.Status == GatewayStatusDeclined {
		return nil, &httpx.Error{Status: http.StatusPaymentRequired, Code: httpx.CodePaymentRequired, Message: "card declined"}
	}
	if result.Status != GatewayStatusApproved {
		return nil, &httpx.Error{Status: http.StatusPaymentRequired, Code: httpx.CodePaymentRequired, Message: "card payment failed: unexpected gateway status " + string(result.Status)}
	}

	card := &account.CardFacts{GatewayTxID: result.TransactionID, GatewayStatus: string(result.Status), TokenID: in.TokenID,
		Last4: result.CardLast4, Brand: result.CardBrand, AuthCode: result.AuthCode}
	in.Reference = "Run:" + result.TransactionID
	p, fx, err := s.record1(ctx, res, card, who)
	if err != nil {
		var he *httpx.Error
		refused := errors.As(err, &he) && he.Status < 500
		if refused || ctx.Err() != nil {
			// Nothing was recorded for an approved charge: the system refused
			// it (or the request ended before the transaction could open). Give
			// the money back before returning, so the customer is not charged
			// with no document. The reversal does not ride the request: a client
			// that gave up must not leave the card charged.
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reversalTimeout)
			defer cancel()
			outcome, cause := s.reverseCharge(rctx, result.TransactionID, in.CustomerID, in.AmountCents)
			if outcome == chargeFailed {
				return nil, &ChargeNotReversedError{GatewayTxID: result.TransactionID, Cause: cause}
			}
			if refused {
				he2 := *he
				he2.Message = fmt.Sprintf("%s: the card charge was %s", he.Message, outcome)
				return nil, &he2
			}
			return nil, fmt.Errorf("payment refused after the gateway approved the charge, and the card charge was %s: %w", outcome, err)
		}
		// Gateway charged but DB failed: log for manual reconciliation.
		s.logger.Error("CRITICAL: Gateway charged but DB commit failed",
			"gateway_tx_id", result.TransactionID, "customer_id", in.CustomerID, "amount_cents", in.AmountCents, "error", err)
		return nil, fmt.Errorf("payment recorded at gateway but failed to save: %w", err)
	}
	s.notifyPaid(fx)
	return p, nil
}

// reverseCharge undoes an approved charge that no payment record backs: the
// same-day void first, the refund when the void is refused (a settled
// capture). It logs the outcome and writes it to the audit log, in its own
// write after the rolled back transaction ended, naming the customer and the
// gateway transaction id (no card data). On failure it returns both causes for
// manual reconciliation; a failing audit write is logged and never hides the
// outcome from the caller.
func (s *Service) reverseCharge(ctx context.Context, gatewayTxID string, customerID uuid.UUID, amountCents int64) (chargeReversal, error) {
	outcome, cause := chargeVoided, error(nil)
	if _, voidErr := s.gateway.Void(ctx, gatewayTxID); voidErr != nil {
		s.logger.Info("Gateway void refused, refunding instead",
			"gateway_tx_id", gatewayTxID, "customer_id", customerID, "error", voidErr)
		if _, refundErr := s.gateway.Refund(ctx, gatewayTxID, amountCents); refundErr != nil {
			outcome, cause = chargeFailed, fmt.Errorf("void: %v; refund: %v", voidErr, refundErr)
		} else {
			outcome = chargeRefunded
		}
	}
	switch outcome {
	case chargeFailed:
		s.logger.Error("CRITICAL: Gateway charged, the system refused it, and the reversal failed",
			"gateway_tx_id", gatewayTxID, "customer_id", customerID, "amount_cents", amountCents, "error", cause)
	default:
		s.logger.Warn("Gateway charge reversed: the system refused the payment",
			"outcome", string(outcome), "gateway_tx_id", gatewayTxID, "customer_id", customerID, "amount_cents", amountCents)
	}
	if s.auditLog != nil {
		if err := s.auditLog.Log(ctx, audit.Entry{
			Action:     "payment.charge_reversal",
			EntityType: "customer",
			EntityID:   customerID,
			Changes: map[string]interface{}{
				"gateway_tx_id": gatewayTxID,
				"amount_cents":  amountCents,
				"outcome":       string(outcome),
			},
		}); err != nil {
			s.logger.Error("CRITICAL: the charge reversal audit row was not written",
				"gateway_tx_id", gatewayTxID, "customer_id", customerID, "outcome", string(outcome), "error", err)
		}
	}
	return outcome, cause
}

// RefundCredit pays a credit memo's open credit out, in cash terms (check,
// ACH, other) or back to a card through the gateway before the transaction.
// Finance roles only.
func (s *Service) RefundCredit(ctx context.Context, memoID uuid.UUID, in *RefundInput, pre Precondition, who Caller) (*Refund, error) {
	if err := who.finance("refunding a credit memo"); err != nil {
		return nil, err
	}
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	facts, err := s.repo.MemoFactsFor(ctx, memoID)
	if err != nil {
		return nil, err
	}
	if err := s.checkBranch(ctx, facts.Branch, "id"); err != nil {
		return nil, err
	}
	if err := pre.check(facts.Revision); err != nil {
		return nil, err
	}
	switch {
	case facts.Status != "OPEN" && facts.Status != "PARTIAL":
		return nil, httpx.InvalidStateTransition("only an open credit memo can be refunded: it is " + lowerStatus(facts.Status))
	case in.AmountCents > -facts.Open:
		return nil, conflictBlocker("exceeds_open_credit", fmt.Sprintf("the credit memo has %d cents of open credit: %d cents were asked", -facts.Open, in.AmountCents))
	}
	gatewayID := ""
	if in.Method == PaymentMethodCard {
		if s.gateway == nil {
			return nil, httpx.Unavailable("payment gateway not configured")
		}
		card, err := s.repo.GatewayFactsFor(ctx, *in.PaymentID)
		if err != nil {
			return nil, fieldError("payment_id", "no such payment")
		}
		if card.Method != PaymentMethodCard || card.GatewayTxID == "" {
			return nil, fieldError("payment_id", "must be a card payment taken through the gateway")
		}
		var customer uuid.UUID
		if err := s.db.GetExecutor(ctx).QueryRow(ctx, `SELECT customer_id FROM payments WHERE id = $1`, *in.PaymentID).Scan(&customer); err != nil || customer != facts.Customer {
			return nil, conflictBlocker("customer_mismatch", "the card payment and the credit memo belong to different customers")
		}
		if card.Currency != facts.Currency {
			return nil, conflictBlocker("currency_mismatch", "the card payment and the credit memo are in different currencies")
		}
		res, gerr := s.gateway.Refund(ctx, card.GatewayTxID, in.AmountCents)
		if gerr != nil {
			return nil, &httpx.Error{Status: http.StatusBadGateway, Code: httpx.CodeUnavailable, Message: "gateway refund failed: " + gerr.Error()}
		}
		gatewayID = res.TransactionID
	}
	var refundID uuid.UUID
	err = s.inTx(ctx, func(ctx context.Context) error {
		rev, err := s.repo.LockMemo(ctx, memoID)
		if err != nil {
			return err
		}
		if err := pre.check(rev); err != nil {
			return err
		}
		on, err := s.account.LocalDate(ctx, facts.Branch, s.now())
		if err != nil {
			return err
		}
		rid, fx, err := s.account.RefundCreditMemo(ctx, account.RefundCreditMemoIn{MemoID: memoID, AmountCents: in.AmountCents, Reason: in.Reason,
			Method: string(in.Method), GatewayRefundID: gatewayID, Actor: who.Actor, On: on})
		if err != nil {
			return err
		}
		refundID = rid
		if err := s.audit(ctx, audit.Entry{Action: "credit_memo.refunded", EntityType: "credit_memo", EntityID: memoID, UserID: who.Actor,
			Changes: map[string]interface{}{"refund_id": rid, "amount_cents": in.AmountCents, "method": string(in.Method), "reason": in.Reason}}); err != nil {
			return err
		}
		branch := facts.Branch
		if err := s.record(ctx, outbox.Event{Type: "credit_memo.refunded", EntityType: "credit_memo", EntityID: memoID, BranchID: &branch,
			Data: mustJSON(map[string]any{"number": facts.Number, "customer_id": facts.Customer, "currency": facts.Currency,
				"amount_cents": in.AmountCents, "refund_id": rid, "method": string(in.Method)})}); err != nil {
			return err
		}
		return s.recordAll(ctx, fx.Events())
	})
	if err != nil {
		if gatewayID != "" {
			s.logger.Error("CRITICAL: Gateway refunded but DB commit failed",
				"gateway_refund_id", gatewayID, "credit_memo_id", memoID, "amount_cents", in.AmountCents, "error", err)
		}
		return nil, err
	}
	var out Refund
	var amount int64
	var method string
	var created time.Time
	err = s.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT f.id, f.payment_id, f.credit_memo_id, ROUND(f.amount * 100)::bigint, f.reason, f.method, f.gateway_refund_id, f.status,
		       f.gl_entry_id, to_char(f.refunded_on, 'YYYY-MM-DD'), f.created_at
		FROM payment_refunds f WHERE f.id = $1`, refundID).
		Scan(&out.ID, &out.PaymentID, &out.CreditMemoID, &amount, &out.Reason, &method, &out.GatewayRefundID, &out.Status, &out.GLEntryID, &out.RefundedOn, &created)
	if err != nil {
		return nil, fmt.Errorf("failed to read the refund: %w", err)
	}
	out.AmountCents, out.Method, out.CreatedAt = httpx.Cents(amount), PaymentMethod(method), httpx.TimestampOf(created)
	return &out, nil
}

func lowerStatus(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
