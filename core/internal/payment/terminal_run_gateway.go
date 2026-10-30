// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"context"
	"fmt"
)

// RunTerminalGateway is the counter's way back to a Run card (CP-05 B): a
// card return or void goes through the Run driver's own void-or-refund, the
// call shaped to Run's public page. The leg's idempotency key goes as
// user_identifier, a void sends no amount, and a key is never sent twice.
// A Run C is DECLINED; a B or any other result is ERROR, an unknown.
//
// It is only a way back: the sale is the terminal's, so Charge and Capture
// are refused. Calls without a key are refused too, since an unkeyed
// reversal could not be tied to its retry (RefundUnderKey and VoidUnderKey
// pass the key whenever the caller has one).
type RunTerminalGateway struct {
	term *RunTerminal
}

// NewRunTerminalGateway wraps the Run driver as the counter's way back.
func NewRunTerminalGateway(term *RunTerminal) *RunTerminalGateway {
	return &RunTerminalGateway{term: term}
}

var _ IdempotentGateway = (*RunTerminalGateway)(nil)

// Charge is refused: a Run card sale is taken on the terminal.
func (g *RunTerminalGateway) Charge(context.Context, ChargeRequest) (*GatewayResult, error) {
	return nil, fmt.Errorf("run terminal gateway: a card sale is taken on the terminal, not charged here")
}

// Capture is refused: Run captures at the terminal.
func (g *RunTerminalGateway) Capture(context.Context, string, int64) (*GatewayResult, error) {
	return nil, fmt.Errorf("run terminal gateway: separate capture is not supported")
}

// Void is refused without a key.
func (g *RunTerminalGateway) Void(context.Context, string) (*GatewayResult, error) {
	return nil, fmt.Errorf("run terminal gateway: a void is sent only under an idempotency key")
}

// Refund is refused without a key.
func (g *RunTerminalGateway) Refund(context.Context, string, int64) (*GatewayResult, error) {
	return nil, fmt.Errorf("run terminal gateway: a refund is sent only under an idempotency key")
}

// VoidWithKey voids the whole transaction under the key.
func (g *RunTerminalGateway) VoidWithKey(ctx context.Context, gatewayTxID, key string) (*GatewayResult, error) {
	return g.reverse(ctx, TerminalKindVoid, gatewayTxID, 0, key, GatewayStatusVoided)
}

// RefundWithKey refunds amountCents of the transaction under the key.
func (g *RunTerminalGateway) RefundWithKey(ctx context.Context, gatewayTxID string, amountCents int64, key string) (*GatewayResult, error) {
	return g.reverse(ctx, TerminalKindRefund, gatewayTxID, amountCents, key, GatewayStatusRefunded)
}

func (g *RunTerminalGateway) reverse(ctx context.Context, kind, transID string, amountCents int64, key string, done GatewayStatus) (*GatewayResult, error) {
	if key == "" {
		return nil, fmt.Errorf("run terminal gateway: a %s is sent only under an idempotency key", kind)
	}
	res, err := g.term.reverse(ctx, kind, transID, amountCents, key)
	if err != nil {
		return nil, err
	}
	out := &GatewayResult{TransactionID: res.TransID, AuthCode: res.RespCode, AmountCents: amountCents}
	if out.TransactionID == "" {
		out.TransactionID = transID
	}
	switch res.Result {
	case "A":
		out.Status = done
	case "C":
		// Run said no: the money did not move, a definite decline.
		out.Status = GatewayStatusDeclined
	default:
		// B (retry) or anything else says nothing about the money: unknown.
		// It answers status ERROR with a nil error (CP-05 round 3b), which
		// every caller reads as unknown: an error would send the counter's
		// reversal on to a refund on top of a void that may have gone
		// through, and a double reversal costs more than a leg left open.
		out.Status = GatewayStatusError
		out.RawResponse = map[string]any{"result": res.Result, "resp_code": res.RespCode, "resp_text": res.RespText}
	}
	return out, nil
}
