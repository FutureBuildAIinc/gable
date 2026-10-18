// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"context"
)

// PaymentGateway abstracts payment processing so we can swap providers
// (Run Payments, Stripe, etc.) without changing business logic.
type PaymentGateway interface {
	// Charge creates a payment charge against a tokenized card.
	// amount is in cents. Returns a GatewayResult with the transaction details.
	Charge(ctx context.Context, req ChargeRequest) (*GatewayResult, error)

	// Capture captures a previously authorized charge.
	Capture(ctx context.Context, gatewayTxID string, amountCents int64) (*GatewayResult, error)

	// Void cancels a previously authorized but uncaptured charge.
	Void(ctx context.Context, gatewayTxID string) (*GatewayResult, error)

	// Refund issues a full or partial refund on a captured charge.
	Refund(ctx context.Context, gatewayTxID string, amountCents int64) (*GatewayResult, error)
}

// ChargeRequest contains the parameters needed to charge a card.
type ChargeRequest struct {
	TokenID     string // PCI-compliant token from Runner.js
	AmountCents int64  // Amount in cents
	Currency    string // ISO 4217 (default "USD")
	Description string // e.g., "Invoice #1234"
	InvoiceID   string // Reference back to our invoice
	CustomerID  string // Reference back to our customer
	// IdempotencyKey, when set, is the provider's own dedupe key: a retried
	// charge under it is the same charge at the provider, never a second one
	// (the terminal intent ledger mints one key per card leg).
	IdempotencyKey string
}

// IdempotentGateway is the idempotency key extension of PaymentGateway a
// gateway implements when its provider accepts keys on refunds and voids
// too. The terminal ledger sends every leg under a key; a gateway that does
// not implement it keeps the plain PaymentGateway calls.
type IdempotentGateway interface {
	PaymentGateway
	// RefundWithKey refunds a captured charge under an idempotency key.
	RefundWithKey(ctx context.Context, gatewayTxID string, amountCents int64, key string) (*GatewayResult, error)
	// VoidWithKey cancels an authorized charge under an idempotency key.
	VoidWithKey(ctx context.Context, gatewayTxID string, key string) (*GatewayResult, error)
}

// RefundUnderKey refunds through the idempotency key extension when the
// gateway implements it, and through the plain call when it does not.
func RefundUnderKey(ctx context.Context, gw PaymentGateway, gatewayTxID string, amountCents int64, key string) (*GatewayResult, error) {
	if ig, ok := gw.(IdempotentGateway); ok && key != "" {
		return ig.RefundWithKey(ctx, gatewayTxID, amountCents, key)
	}
	return gw.Refund(ctx, gatewayTxID, amountCents)
}

// VoidUnderKey voids through the idempotency key extension when the gateway
// implements it, and through the plain call when it does not.
func VoidUnderKey(ctx context.Context, gw PaymentGateway, gatewayTxID string, key string) (*GatewayResult, error) {
	if ig, ok := gw.(IdempotentGateway); ok && key != "" {
		return ig.VoidWithKey(ctx, gatewayTxID, key)
	}
	return gw.Void(ctx, gatewayTxID)
}

// GatewayResult is the normalized response from any payment gateway.
type GatewayResult struct {
	TransactionID string         // Gateway's unique transaction identifier
	Status        GatewayStatus  // APPROVED, DECLINED, ERROR, VOIDED, REFUNDED
	AuthCode      string         // Authorization code from the processor
	CardLast4     string         // Last 4 digits of the card used
	CardBrand     string         // VISA, MASTERCARD, AMEX, DISCOVER, etc.
	AmountCents   int64          // Amount actually charged/refunded
	RawResponse   map[string]any // Full gateway response for debugging
}

// GatewayStatus represents the status of a gateway transaction.
type GatewayStatus string

const (
	GatewayStatusApproved GatewayStatus = "APPROVED"
	GatewayStatusDeclined GatewayStatus = "DECLINED"
	GatewayStatusError    GatewayStatus = "ERROR"
	GatewayStatusVoided   GatewayStatus = "VOIDED"
	GatewayStatusRefunded GatewayStatus = "REFUNDED"
	GatewayStatusPending  GatewayStatus = "PENDING"
)

// GatewayConfig holds the configuration for a payment gateway.
type GatewayConfig struct {
	APIKey      string
	PublicKey   string
	MID         string // Run merchant ID (sent as the `mid` header + charge body field)
	BaseURL     string
	Environment string // "sandbox" or "production"
}
