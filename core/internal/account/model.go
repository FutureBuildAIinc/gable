// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

import (
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// TransactionType is the subledger row type (ADR 0005 section 9.3), the
// customer_transactions.type CHECK over eight values.
type TransactionType string

const (
	TransactionTypeInvoice    TransactionType = "INVOICE"
	TransactionTypePayment    TransactionType = "PAYMENT"
	TransactionTypeAdjustment TransactionType = "ADJUSTMENT"
	TransactionTypeRefund     TransactionType = "REFUND"
	TransactionTypeCreditMemo TransactionType = "CREDIT_MEMO"
	TransactionTypeDiscount   TransactionType = "DISCOUNT"
	TransactionTypeWriteOff   TransactionType = "WRITE_OFF"
	TransactionTypeReversal   TransactionType = "REVERSAL"
)

// The source kinds a subledger row names with its reference id.
const (
	sourceInvoice     = "invoice"
	sourceCreditMemo  = "credit_memo"
	sourceApplication = "application"
	sourceRefund      = "refund"
)

// ApplicationKind is ar_applications.kind.
type ApplicationKind string

const (
	KindPayment    ApplicationKind = "PAYMENT"
	KindCreditMemo ApplicationKind = "CREDIT_MEMO"
	KindDiscount   ApplicationKind = "DISCOUNT"
	KindWriteOff   ApplicationKind = "WRITE_OFF"
)

// Lowercase wire name of a kind.
func (k ApplicationKind) MarshalText() ([]byte, error) { return []byte(lower(string(k))), nil }

// Event types the core writes (ADR 0005 section 12). The owners of the
// documents write their own (payment.recorded, payment.voided, ...).
const (
	EventPaymentApplied     = "payment.applied"
	EventPaymentUnapplied   = "payment.unapplied"
	EventInvoicePartial     = "invoice.partial"
	EventInvoicePaid        = "invoice.paid"
	EventInvoiceWrittenOff  = "invoice.written_off"
	EventInvoiceReopened    = "invoice.reopened"
	EventCreditPartial      = "credit_memo.partial"
	EventCreditApplied      = "credit_memo.applied"
	EventCreditReopened     = "credit_memo.reopened"
	EventCustomerUpdated    = "customer.updated"
	customerBalancePartName = "balance"
)

// Transaction is a subledger row on the wire (GET /api/v1/accounts/{id}/transactions).
type Transaction struct {
	ID                uuid.UUID       `json:"id"`
	CustomerID        uuid.UUID       `json:"customer_id"`
	Type              TransactionType `json:"type"`
	AmountCents       httpx.Cents     `json:"amount_cents"`
	BalanceAfterCents httpx.Cents     `json:"balance_after_cents"`
	Currency          string          `json:"currency"`
	SourceKind        *string         `json:"source_kind"`
	ReferenceID       *uuid.UUID      `json:"reference_id"`
	Description       string          `json:"description"`
	CreatedAt         httpx.Timestamp `json:"created_at"`
}

// Summary is GET /api/v1/accounts/{id}.
type Summary struct {
	CustomerID       uuid.UUID    `json:"customer_id"`
	Currency         string       `json:"currency"`
	BalanceCents     httpx.Cents  `json:"balance_cents"`
	CreditLimitCents *httpx.Cents `json:"credit_limit_cents"`
	AvailableCredit  *httpx.Cents `json:"available_credit_cents"`
	UnappliedCents   httpx.Cents  `json:"unapplied_cents"`
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// date formats a business date.
func date(t time.Time) string { return t.Format("2006-01-02") }
