// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// SimulatedTerminalGateway is the card gateway a demo stack wires beside the
// simulated terminal (CP-05 B): the counter's returns, voids and reversals of
// a simulated card leg go back to the card through it, so the demo and the
// Playwright run can refund to the card with no processor. It puts back only
// the simulated terminal's own legs (a reference the simulator minted); any
// other reference is declined, so it never pretends a real card was refunded.
// It charges nothing: the counter's card legs are the terminal's. A
// production configuration never wires it.
type SimulatedTerminalGateway struct {
	mu      sync.Mutex
	byKey   map[string]*GatewayResult
	refunds []SimulatedRefund
}

// SimulatedRefund is one put back the simulated gateway made.
type SimulatedRefund struct {
	GatewayTxID string
	AmountCents int64 // 0 for a void
	Void        bool
}

// NewSimulatedTerminalGateway builds the gateway of a demo stack's simulator.
func NewSimulatedTerminalGateway() *SimulatedTerminalGateway {
	return &SimulatedTerminalGateway{byKey: map[string]*GatewayResult{}}
}

// simulatedRefPrefix marks a reference the simulated terminal minted.
const simulatedRefPrefix = "simterm_"

// Refunds lists the put backs made, in order.
func (g *SimulatedTerminalGateway) Refunds() []SimulatedRefund {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]SimulatedRefund, len(g.refunds))
	copy(out, g.refunds)
	return out
}

// Charge is refused: a simulated card leg is the terminal's, never a token's.
func (g *SimulatedTerminalGateway) Charge(context.Context, ChargeRequest) (*GatewayResult, error) {
	return nil, fmt.Errorf("the simulated terminal gateway takes no token charge: the card leg is the terminal's")
}

// Capture is refused for the same reason.
func (g *SimulatedTerminalGateway) Capture(context.Context, string, int64) (*GatewayResult, error) {
	return nil, fmt.Errorf("the simulated terminal gateway captures nothing")
}

// Void cancels a simulated leg.
func (g *SimulatedTerminalGateway) Void(ctx context.Context, gatewayTxID string) (*GatewayResult, error) {
	return g.VoidWithKey(ctx, gatewayTxID, "")
}

// Refund puts amountCents back on a simulated leg.
func (g *SimulatedTerminalGateway) Refund(ctx context.Context, gatewayTxID string, amountCents int64) (*GatewayResult, error) {
	return g.RefundWithKey(ctx, gatewayTxID, amountCents, "")
}

// VoidWithKey cancels a simulated leg; a retried key answers the same result.
func (g *SimulatedTerminalGateway) VoidWithKey(_ context.Context, gatewayTxID string, key string) (*GatewayResult, error) {
	return g.putBack(gatewayTxID, 0, true, key), nil
}

// RefundWithKey refunds a simulated leg; a retried key answers the same result.
func (g *SimulatedTerminalGateway) RefundWithKey(_ context.Context, gatewayTxID string, amountCents int64, key string) (*GatewayResult, error) {
	return g.putBack(gatewayTxID, amountCents, false, key), nil
}

func (g *SimulatedTerminalGateway) putBack(gatewayTxID string, amountCents int64, void bool, key string) *GatewayResult {
	g.mu.Lock()
	defer g.mu.Unlock()
	if key != "" {
		if res, ok := g.byKey[key]; ok {
			return res
		}
	}
	res := &GatewayResult{TransactionID: "simrefund_" + gatewayTxID, AmountCents: amountCents}
	switch {
	case !strings.HasPrefix(gatewayTxID, simulatedRefPrefix):
		res.Status = GatewayStatusDeclined
	case void:
		res.Status = GatewayStatusVoided
	default:
		res.Status = GatewayStatusRefunded
	}
	if res.Status != GatewayStatusDeclined {
		g.refunds = append(g.refunds, SimulatedRefund{GatewayTxID: gatewayTxID, AmountCents: amountCents, Void: void})
	}
	if key != "" {
		g.byKey[key] = res
	}
	return res
}

var _ IdempotentGateway = (*SimulatedTerminalGateway)(nil)
