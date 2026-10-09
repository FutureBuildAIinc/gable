// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

type Service struct {
	db            *database.DB
	repo          Repository
	invoiceRepo   invoice.Repository
	account       account.Service
	gateway       PaymentGateway // Run Payments (or nil for non-card payments)
	publicKey     string         // Run Payments public key for Runner.js (static fallback)
	keyStore      *KeyStore      // Optional: DB-first key resolution (Tech Admin settable)
	brainNotifier *BrainNotifier // FB Brain financial engine notifier (or nil)
	brainOrgID    string         // Brain org_id for this tenant
	auditLog      *audit.Logger
	logger        *slog.Logger
}

func NewService(db *database.DB, repo Repository, invoiceRepo invoice.Repository, accountService account.Service) *Service {
	return &Service{
		db:          db,
		repo:        repo,
		invoiceRepo: invoiceRepo,
		account:     accountService,
		logger:      slog.Default(),
	}
}

// WithGateway sets the payment gateway (Run Payments) and returns the service for chaining.
func (s *Service) WithGateway(gw PaymentGateway, publicKey string) *Service {
	s.gateway = gw
	s.publicKey = publicKey
	return s
}

// WithBrainNotifier sets the FB Brain financial notifier and returns the service for chaining.
// When set, successfully paid invoices will fire an async notification to Brain's 10bps engine.
func (s *Service) WithBrainNotifier(n *BrainNotifier, orgID string) *Service {
	s.brainNotifier = n
	s.brainOrgID = orgID
	return s
}

// WithAuditLog sets the audit logger for financial operation tracking.
func (s *Service) WithAuditLog(l *audit.Logger) *Service {
	s.auditLog = l
	return s
}

// WithKeyStore enables DB-first gateway credential resolution so keys set
// at runtime (system_settings via Tech Admin) take effect without restart.
func (s *Service) WithKeyStore(ks *KeyStore) *Service {
	s.keyStore = ks
	return s
}

// GetPublicKey returns the Run Payments public key for frontend Runner.js integration.
func (s *Service) GetPublicKey() string {
	if s.keyStore != nil {
		if pk := s.keyStore.Resolve().PublicKey; pk != "" {
			return pk
		}
	}
	return s.publicKey
}

// ErrInvoiceVoid is the refusal to record a payment against a void invoice.
var ErrInvoiceVoid = errors.New("the invoice is void: it takes no payment")

// ErrChargeNotReversed: the card was charged, the invoice refused the payment,
// and neither the void nor the refund went through.
var ErrChargeNotReversed = errors.New("the card was charged and the charge could not be reversed")

// ChargeNotReversedError is the error ProcessCardPayment returns when the
// reversal failed. It carries the gateway transaction id finance reconciles
// and nothing else about the card; errors.Is(err, ErrChargeNotReversed) holds.
type ChargeNotReversedError struct {
	GatewayTxID string
	Cause       error
}

func (e *ChargeNotReversedError) Error() string {
	return fmt.Sprintf("%s (gateway transaction %s): %v", ErrChargeNotReversed, e.GatewayTxID, e.Cause)
}

func (e *ChargeNotReversedError) Is(target error) bool { return target == ErrChargeNotReversed }

func (e *ChargeNotReversedError) Unwrap() error { return e.Cause }

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

// lockInvoice reads the invoice under its row lock (ADR 0005 section 11,
// step 4) so a payment and an invoice void serialize: the void checks for
// payments under the same lock, and a payment never lands on a void invoice.
// C2-4 moves this into the AR core with the rest of the payment acts.
func (s *Service) lockInvoice(ctx context.Context, id uuid.UUID) (*invoice.Invoice, error) {
	var inv *invoice.Invoice
	var err error
	if l, ok := s.invoiceRepo.(interface {
		LockInvoice(ctx context.Context, id uuid.UUID) (*invoice.Invoice, error)
	}); ok {
		inv, err = l.LockInvoice(ctx, id)
	} else {
		inv, err = s.invoiceRepo.GetInvoice(ctx, id)
	}
	if err != nil {
		return nil, err
	}
	if inv.Status == invoice.InvoiceStatusVoid {
		return nil, ErrInvoiceVoid
	}
	return inv, nil
}

// ProcessPayment handles cash, check, and account payments (non-gateway).
func (s *Service) ProcessPayment(ctx context.Context, invoiceID uuid.UUID, amountCents int64, method PaymentMethod, ref, notes string) (*Payment, error) {
	if amountCents <= 0 {
		return nil, fmt.Errorf("payment amount must be positive")
	}

	var p *Payment

	err := s.db.RunInTx(ctx, func(ctx context.Context) error {
		inv, err := s.lockInvoice(ctx, invoiceID)
		if errors.Is(err, ErrInvoiceVoid) {
			return err
		}
		if err != nil {
			return fmt.Errorf("invoice not found: %w", err)
		}

		p = &Payment{
			InvoiceID: invoiceID,
			Amount:    amountCents,
			Method:    method,
			Reference: ref,
			Notes:     notes,
		}

		if err := s.repo.CreatePayment(ctx, p); err != nil {
			return err
		}

		_, err = s.account.PostTransaction(ctx, inv.CustomerID, account.TransactionTypePayment, -amountCents, &p.ID, "Payment "+ref)
		if err != nil {
			return fmt.Errorf("failed to post to account ledger: %w", err)
		}

		if err := s.updateInvoiceStatus(ctx, invoiceID, inv); err != nil {
			return err
		}

		// Audit log: inside the transaction, so it shares the payment's fate
		// — a rolled back payment leaves no audit row.
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action:     "payment.processed",
				EntityType: "payment",
				EntityID:   p.ID,
				Changes: map[string]interface{}{
					"invoice_id":   invoiceID,
					"amount_cents": amountCents,
					"method":       string(method),
					"reference":    ref,
				},
			}); err != nil {
				return fmt.Errorf("failed to write audit log: %w", err)
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return p, nil
}

// ProcessCardPayment handles card payments through the Run Payments gateway.
func (s *Service) ProcessCardPayment(ctx context.Context, invoiceID uuid.UUID, tokenID string, amountCents int64, notes string) (*Payment, error) {
	if amountCents <= 0 {
		return nil, fmt.Errorf("payment amount must be positive")
	}
	if s.gateway == nil {
		return nil, fmt.Errorf("payment gateway not configured — set RUN_PAYMENTS_API_KEY")
	}

	// A void invoice takes no payment: refuse before the card is charged (the
	// locked read inside the transaction below repeats the check).
	if pre, err := s.invoiceRepo.GetInvoice(ctx, invoiceID); err == nil && pre.Status == invoice.InvoiceStatusVoid {
		return nil, ErrInvoiceVoid
	}

	// 1. Charge through Run Payments
	result, err := s.gateway.Charge(ctx, ChargeRequest{
		TokenID:     tokenID,
		AmountCents: amountCents,
		Currency:    "USD",
		Description: fmt.Sprintf("Invoice %s", invoiceID.String()[:8]),
		InvoiceID:   invoiceID.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("gateway charge failed: %w", err)
	}

	if result.Status == GatewayStatusDeclined {
		return nil, fmt.Errorf("card declined: %s", result.AuthCode)
	}
	if result.Status != GatewayStatusApproved {
		return nil, fmt.Errorf("unexpected gateway status: %s", result.Status)
	}

	// 2. Record payment in our DB within a transaction
	var p *Payment
	refused := false
	began := false
	err = s.db.RunInTx(ctx, func(ctx context.Context) error {
		began = true
		inv, err := s.lockInvoice(ctx, invoiceID)
		if errors.Is(err, ErrInvoiceVoid) {
			refused = true
			return err
		}
		if err != nil {
			refused = true
			return fmt.Errorf("invoice not found: %w", err)
		}

		p = &Payment{
			InvoiceID:     invoiceID,
			Amount:        amountCents,
			Method:        PaymentMethodCard,
			Reference:     fmt.Sprintf("Run:%s", result.TransactionID),
			Notes:         notes,
			GatewayTxID:   result.TransactionID,
			GatewayStatus: string(result.Status),
			TokenID:       tokenID,
			CardLast4:     result.CardLast4,
			CardBrand:     result.CardBrand,
			AuthCode:      result.AuthCode,
		}

		if err := s.repo.CreatePayment(ctx, p); err != nil {
			return err
		}

		_, err = s.account.PostTransaction(ctx, inv.CustomerID, account.TransactionTypePayment, -amountCents, &p.ID, "Card Payment "+result.CardBrand+" ***"+result.CardLast4)
		if err != nil {
			return fmt.Errorf("failed to post to account ledger: %w", err)
		}

		if err := s.updateInvoiceStatus(ctx, invoiceID, inv); err != nil {
			return err
		}

		// Audit log: inside the transaction, so it shares the payment's fate
		// — a rolled back payment leaves no audit row.
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action:     "payment.processed",
				EntityType: "payment",
				EntityID:   p.ID,
				Changes: map[string]interface{}{
					"invoice_id":    invoiceID,
					"amount_cents":  amountCents,
					"method":        string(PaymentMethodCard),
					"gateway_tx_id": result.TransactionID,
					"card_brand":    result.CardBrand,
					"card_last4":    result.CardLast4,
				},
			}); err != nil {
				return fmt.Errorf("failed to write audit log: %w", err)
			}
		}
		return nil
	})

	if err != nil && (refused || !began) {
		// Nothing was recorded for an approved charge: the invoice refused
		// it (a void committed during the call) or the transaction never
		// opened (the request ended during the call). Give the money back
		// before returning, so the customer is not charged with no document.
		// The reversal does not ride the request: a client that gave up must
		// not leave the card charged.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reversalTimeout)
		defer cancel()
		outcome, cause := s.reverseCharge(rctx, result.TransactionID, invoiceID, amountCents)
		if outcome == chargeFailed {
			return nil, &ChargeNotReversedError{GatewayTxID: result.TransactionID, Cause: cause}
		}
		return nil, fmt.Errorf("payment refused after the gateway approved the charge, and the card charge was %s: %w", outcome, err)
	}
	if err != nil {
		// Gateway charged but DB failed — log for manual reconciliation
		s.logger.Error("CRITICAL: Gateway charged but DB commit failed",
			"gateway_tx_id", result.TransactionID,
			"invoice_id", invoiceID,
			"amount_cents", amountCents,
			"error", err,
		)
		return nil, fmt.Errorf("payment recorded at gateway but failed to save: %w", err)
	}

	return p, nil
}

// reverseCharge undoes an approved charge that no payment record backs: the
// same-day void first, the refund when the void is refused (a settled
// capture). It logs the outcome and writes it to the audit log, in its own
// write after the rolled back transaction ended, naming the invoice and the
// gateway transaction id (no card data). On failure it returns both causes for
// manual reconciliation.
func (s *Service) reverseCharge(ctx context.Context, gatewayTxID string, invoiceID uuid.UUID, amountCents int64) (chargeReversal, error) {
	outcome, cause := chargeVoided, error(nil)
	if _, voidErr := s.gateway.Void(ctx, gatewayTxID); voidErr != nil {
		s.logger.Info("Gateway void refused, refunding instead",
			"gateway_tx_id", gatewayTxID, "invoice_id", invoiceID, "error", voidErr)
		if _, refundErr := s.gateway.Refund(ctx, gatewayTxID, amountCents); refundErr != nil {
			outcome, cause = chargeFailed, fmt.Errorf("void: %v; refund: %v", voidErr, refundErr)
		} else {
			outcome = chargeRefunded
		}
	}
	switch outcome {
	case chargeFailed:
		s.logger.Error("CRITICAL: Gateway charged, the invoice refused it, and the reversal failed",
			"gateway_tx_id", gatewayTxID, "invoice_id", invoiceID, "amount_cents", amountCents, "error", cause)
	default:
		s.logger.Warn("Gateway charge reversed: the invoice refused the payment",
			"outcome", string(outcome), "gateway_tx_id", gatewayTxID, "invoice_id", invoiceID, "amount_cents", amountCents)
	}
	if s.auditLog != nil {
		if err := s.auditLog.Log(ctx, audit.Entry{
			Action:     "payment.charge_reversal",
			EntityType: "invoice",
			EntityID:   invoiceID,
			Changes: map[string]interface{}{
				"gateway_tx_id": gatewayTxID,
				"amount_cents":  amountCents,
				"outcome":       string(outcome),
			},
		}); err != nil {
			s.logger.Error("CRITICAL: the charge reversal audit row was not written",
				"gateway_tx_id", gatewayTxID, "invoice_id", invoiceID, "outcome", string(outcome), "error", err)
		}
	}
	return outcome, cause
}

// RefundPayment issues a full or partial refund on a completed card payment.
func (s *Service) RefundPayment(ctx context.Context, paymentID uuid.UUID, amountCents int64, reason string) (*Refund, error) {
	if amountCents <= 0 {
		return nil, fmt.Errorf("refund amount must be positive")
	}
	if s.gateway == nil {
		return nil, fmt.Errorf("payment gateway not configured")
	}

	// Look up the original payment to get the gateway transaction ID
	original, err := s.repo.GetPaymentByID(ctx, paymentID)
	if err != nil {
		return nil, fmt.Errorf("original payment not found: %w", err)
	}

	if original.GatewayTxID == "" {
		return nil, fmt.Errorf("payment %s has no gateway transaction — only card payments can be refunded", paymentID)
	}

	if amountCents > original.Amount {
		return nil, fmt.Errorf("refund amount (%d cents) exceeds original payment (%d cents)", amountCents, original.Amount)
	}

	// Process refund through gateway using the original transaction ID
	result, err := s.gateway.Refund(ctx, original.GatewayTxID, amountCents)
	if err != nil {
		return nil, fmt.Errorf("gateway refund failed: %w", err)
	}

	// Persist the refund record within a transaction
	var refund *Refund
	err = s.db.RunInTx(ctx, func(ctx context.Context) error {
		// Look up invoice to get the customer ID for the ledger entry
		inv, err := s.invoiceRepo.GetInvoice(ctx, original.InvoiceID)
		if err != nil {
			return fmt.Errorf("invoice not found for refund ledger: %w", err)
		}

		refund = &Refund{
			PaymentID:       paymentID,
			Amount:          amountCents,
			Reason:          reason,
			GatewayRefundID: result.TransactionID,
			Status:          "COMPLETE",
		}

		if err := s.repo.CreateRefund(ctx, refund); err != nil {
			return fmt.Errorf("failed to persist refund: %w", err)
		}

		// Post the refund as a credit to the customer's account ledger (positive = credit back)
		_, err = s.account.PostTransaction(ctx, inv.CustomerID, account.TransactionTypePayment, amountCents, &refund.ID, "Refund: "+reason)
		if err != nil {
			return fmt.Errorf("failed to post refund to account ledger: %w", err)
		}

		// Audit log: inside the transaction, so it shares the refund's fate
		// — a refund that fails to save leaves no audit row.
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action:     "payment.refunded",
				EntityType: "refund",
				EntityID:   refund.ID,
				Changes: map[string]interface{}{
					"payment_id":   paymentID,
					"amount_cents": amountCents,
					"reason":       reason,
					"gateway_id":   result.TransactionID,
				},
			}); err != nil {
				return fmt.Errorf("failed to write audit log: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		// Gateway refunded but DB failed — log for manual reconciliation
		s.logger.Error("CRITICAL: Gateway refunded but DB commit failed",
			"gateway_refund_id", result.TransactionID,
			"payment_id", paymentID,
			"amount_cents", amountCents,
			"error", err,
		)
		return nil, fmt.Errorf("refund processed at gateway but failed to save: %w", err)
	}

	return refund, nil
}

// GetHistory returns all payments for an invoice.
func (s *Service) GetHistory(ctx context.Context, invoiceID uuid.UUID) ([]Payment, error) {
	return s.repo.GetPaymentsByInvoiceID(ctx, invoiceID)
}

// updateInvoiceStatus recalculates and updates the invoice status based on total payments.
func (s *Service) updateInvoiceStatus(ctx context.Context, invoiceID uuid.UUID, inv *invoice.Invoice) error {
	payments, err := s.repo.GetPaymentsByInvoiceID(ctx, invoiceID)
	if err != nil {
		return fmt.Errorf("failed to get payment history: %w", err)
	}

	var totalPaid int64
	for _, pay := range payments {
		totalPaid += pay.Amount
	}

	if totalPaid >= int64(inv.TotalCents) {
		inv.Status = invoice.InvoiceStatusPaid
		if inv.PaidAt == nil {
			now := httpx.TimestampOf(time.Now())
			inv.PaidAt = &now
		}
	} else if totalPaid > 0 {
		inv.Status = invoice.InvoiceStatusPartial
		inv.PaidAt = nil
	} else {
		inv.Status = invoice.InvoiceStatusUnpaid
		inv.PaidAt = nil
	}

	if err := s.invoiceRepo.UpdateInvoice(ctx, inv); err != nil {
		return fmt.Errorf("failed to update invoice status: %w", err)
	}

	// Notify FB Brain's financial engine when an invoice is fully paid.
	if inv.Status == invoice.InvoiceStatusPaid && s.brainNotifier != nil {
		s.brainNotifier.notifyInvoicePaid(s.brainOrgID, inv.ID, int64(inv.TotalCents))
	}

	return nil
}
