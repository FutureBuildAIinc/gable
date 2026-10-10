// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"strings"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// PaymentMethod is the storage vocabulary (UPPERCASE in the database CHECK),
// lowercase on the wire (ADR 0005 9.1). ACCOUNT stays for history and is
// refused on new payments: charging to account is not a payment.
type PaymentMethod string

const (
	PaymentMethodCash    PaymentMethod = "CASH"
	PaymentMethodCard    PaymentMethod = "CARD"
	PaymentMethodCheck   PaymentMethod = "CHECK"
	PaymentMethodACH     PaymentMethod = "ACH"
	PaymentMethodOther   PaymentMethod = "OTHER"
	PaymentMethodAccount PaymentMethod = "ACCOUNT"
)

// MarshalText writes the lowercase wire name.
func (m PaymentMethod) MarshalText() ([]byte, error) { return []byte(strings.ToLower(string(m))), nil }

// recordableMethods are the methods a client may name on POST /payments: a card
// payment is taken through POST /payments/card.
var recordableMethods = []string{"cash", "check", "ach", "other"}

// ParseMethod maps a lowercase wire name to a method a payment can be recorded
// with.
func ParseMethod(name string) (PaymentMethod, bool) {
	for _, n := range recordableMethods {
		if name == n {
			return PaymentMethod(strings.ToUpper(name)), true
		}
	}
	return "", false
}

// Status is the payment lifecycle: posted, then voided (terminal).
type Status string

const (
	StatusPosted Status = "POSTED"
	StatusVoided Status = "VOIDED"
)

func (s Status) Status() string { return strings.ToLower(string(s)) }

// MarshalText writes the lowercase wire name.
func (s Status) MarshalText() ([]byte, error) { return []byte(s.Status()), nil }

// ParseStatus maps a lowercase wire name to its status.
func ParseStatus(name string) (Status, bool) {
	switch name {
	case "posted", "voided":
		return Status(strings.ToUpper(name)), true
	}
	return "", false
}

// Summary is a payment's head: a list item, and the head of the full document.
// The invoice a legacy payment named is not on the wire: applications are the
// truth (ADR 0005 9.1).
type Summary struct {
	ID             uuid.UUID        `json:"id"`
	Number         string           `json:"number"`
	CustomerID     uuid.UUID        `json:"customer_id"`
	CustomerName   string           `json:"customer_name"`
	BranchID       uuid.UUID        `json:"branch_id"`
	Status         Status           `json:"status"`
	Revision       int64            `json:"revision"`
	Currency       string           `json:"currency"`
	Method         PaymentMethod    `json:"method"`
	AmountCents    httpx.Cents      `json:"amount_cents"`
	UnappliedCents httpx.Cents      `json:"unapplied_cents"`
	ReceivedOn     string           `json:"received_on"`
	Reference      *string          `json:"reference"`
	Notes          *string          `json:"notes"`
	OrderID        *uuid.UUID       `json:"order_id"`
	JobID          *uuid.UUID       `json:"job_id"`
	GLEntryID      *uuid.UUID       `json:"gl_entry_id"`
	CardLast4      *string          `json:"card_last4"`
	CardBrand      *string          `json:"card_brand"`
	GatewayTxID    *string          `json:"gateway_tx_id"`
	AuthCode       *string          `json:"auth_code"`
	VoidedAt       *httpx.Timestamp `json:"voided_at"`
	VoidedBy       *string          `json:"voided_by"`
	VoidReason     *string          `json:"void_reason"`
	CreatedAt      httpx.Timestamp  `json:"created_at"`
	UpdatedAt      httpx.Timestamp  `json:"updated_at"`
}

// Payment is the full document: the head, its applications and its refunds.
type Payment struct {
	Summary
	Applications []account.Application `json:"applications"`
	Refunds      []Refund              `json:"refunds"`
}

// Refund is a refund of unapplied cash (or, from a credit memo, of its credit).
type Refund struct {
	ID              uuid.UUID       `json:"id"`
	PaymentID       *uuid.UUID      `json:"payment_id"`
	CreditMemoID    *uuid.UUID      `json:"credit_memo_id"`
	AmountCents     httpx.Cents     `json:"amount_cents"`
	Reason          *string         `json:"reason"`
	Method          PaymentMethod   `json:"method"`
	GatewayRefundID *string         `json:"gateway_refund_id"`
	Status          string          `json:"status"`
	GLEntryID       *uuid.UUID      `json:"gl_entry_id"`
	RefundedOn      string          `json:"refunded_on"`
	CreatedAt       httpx.Timestamp `json:"created_at"`
}

// PaymentIntentResponse returns the public key Runner.js needs on the frontend.
type PaymentIntentResponse struct {
	PublicKey   string      `json:"public_key"`
	AmountCents httpx.Cents `json:"amount_cents"`
}
