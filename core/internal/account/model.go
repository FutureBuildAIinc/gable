// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

import (
	"time"

	"github.com/google/uuid"
)

type TransactionType string

const (
	TransactionTypeInvoice    TransactionType = "INVOICE"
	TransactionTypePayment    TransactionType = "PAYMENT"
	TransactionTypeAdjustment TransactionType = "ADJUSTMENT"
	TransactionTypeRefund     TransactionType = "REFUND"
	// The credit memo and reversal rows of ADR 0005 section 9.3 (the type
	// CHECK has allowed them since C2-2): a posted credit memo is a negative
	// CREDIT_MEMO row; a voided invoice or credit memo is the opposite REVERSAL.
	TransactionTypeCreditMemo TransactionType = "CREDIT_MEMO"
	TransactionTypeReversal   TransactionType = "REVERSAL"
)

type CustomerTransaction struct {
	ID           uuid.UUID       `json:"id"`
	CustomerID   uuid.UUID       `json:"customer_id"`
	Type         TransactionType `json:"type"`
	Amount       int64           `json:"amount"`        // Cents
	BalanceAfter int64           `json:"balance_after"` // Cents
	ReferenceID  *uuid.UUID      `json:"reference_id"`
	Description  string          `json:"description"`
	CreatedAt    time.Time       `json:"created_at"`
}

type AccountSummary struct {
	CustomerID      uuid.UUID `json:"customer_id"`
	BalanceDue      int64     `json:"balance_due"`      // Cents
	CreditLimit     int64     `json:"credit_limit"`     // Cents
	AvailableCredit int64     `json:"available_credit"` // Cents
}
