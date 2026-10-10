// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"log/slog"
	"net/http"

	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/clientip"
	"github.com/gablelbm/gable/pkg/confirmgate"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/metrics"
	"github.com/gablelbm/gable/pkg/middleware"
)

// compile-time check that *audit.Logger satisfies confirmgate.AuditSink,
// so ChainDeps.AuditLog can carry either the platform logger or any
// sink the chain tests need.
var _ confirmgate.AuditSink = (*audit.Logger)(nil)

// ChainDeps holds the dependencies buildChain needs. Tests for any layer
// (the confirm gate, the feed, the machine-key core) build a mux, fill a
// ChainDeps and call buildChain; serve.Run also calls buildChain with the
// real dependencies, so the production chain and any test chain share one
// source of truth (review pr66-r1 P1-1, P1-2; review pr66-r2 P2-1).
type ChainDeps struct {
	// Mux is the innermost handler: every route the serve layer registers
	// ends here.
	Mux http.Handler
	// DB backs the idempotency layer and any handler that opens a
	// transaction.
	DB *database.DB
	// Logger carries the slog default the request logger and the recovery
	// middleware use.
	Logger *slog.Logger
	// AuditLog is the writer the confirm gate's refusal rows go through.
	// It is the confirmgate.AuditSink interface: production passes a
	// *audit.Logger; tests pass any sink implementation that records
	// entries in memory.
	AuditLog AuditSink
	// AuthMw is the JWT auth middleware; nil means AUTH_MODE=dev and the
	// machine-key core mounts standalone.
	AuthMw *middleware.AuthMiddleware
	// MachineKeyAuth is the standalone mount for AUTH_MODE=dev and the
	// dispatch target the JWT layer uses in production.
	MachineKeyAuth *middleware.MachineKeyAuth
	// RateLimitRPM is the per-IP request rate for the rate limit
	// middleware; 0 disables it (tests that drive a single flow).
	RateLimitRPM int
	// TrustedProxies is the IP trust list the request logger and the rate
	// limit consult when a forwarding header names a peer.
	TrustedProxies clientip.Trusted
}

// AuditSink is the interface the chain uses to write the gate's refusal
// rows. confirmgate.AuditSink is the same shape; it is declared here so
// ChainDeps does not import confirmgate to name the type.
type AuditSink = confirmgate.AuditSink

// buildChain wires the production middleware order into finalHandler. The
// builder pattern, applied bottom up, runs the wraps in execution order:
// outermost first, innermost last. A request hits them in the order
//
//	RequestLogger -> HTTPMetrics -> RequestID -> Recovery
//	  -> RateLimit -> CORS -> Actor -> [Auth or MachineKeyAuth]
//	  -> ConfirmGate -> MaxRequestSize -> Idempotency
//	  -> CacheControl -> Mux
//
// so the confirm gate sits INSIDE auth (the gate sees the key id the
// machine-key core set, so ADR 0007 5.4 "keyed requests are governed by
// scopes alone" holds — a keyed request carrying a marker reaches the
// handler) and OUTSIDE idempotency (a refused request never claims a
// key). The actor middleware stays outside auth on purpose: it copies
// the agent marker into the context the auth core, the gate and the
// audit logger read.
func buildChain(d ChainDeps) http.Handler {
	var finalHandler http.Handler = d.Mux

	// Cache-Control headers (innermost; runs after auth, before response).
	finalHandler = middleware.CacheControl(finalHandler)

	// Idempotency keys (POST/PUT with Idempotency-Key), one layer per
	// surface where the principal that scopes a claim is established. The
	// global layer here covers the ERP API only: it runs inside auth (the
	// JWT subject is the principal) and inside the request size limit, and
	// it skips /api/portal/v1/ and /api/integration/ because those surfaces
	// carry their own layer inside their auth chains.
	if d.DB != nil {
		finalHandler = middleware.Idempotency(d.DB)(finalHandler)
	}

	// The confirm gate (ADR 0007 section 5.4): the layer above
	// idempotency and below auth, so a refused request never claims a
	// key and a keyed request always reaches it. NOTE: the wraps build
	// bottom-up here, so in code the gate wrap comes AFTER idempotency
	// and BEFORE auth (which puts the gate ABOVE idempotency and BELOW
	// auth at runtime, which is the correct order).
	finalHandler = wrapConfirmGate(finalHandler, d.AuditLog)

	// Request size limit (10MB default).
	finalHandler = middleware.MaxRequestSize(10 << 20)(finalHandler)

	// Auth (JWT verification; a Bearer machine key dispatches to the
	// machine-key core inside it). In AUTH_MODE=dev the JWT layer is off but
	// machine keys still authenticate and scope check exactly as behind it.
	switch {
	case d.AuthMw != nil:
		finalHandler = d.AuthMw.Handler(finalHandler)
	case d.MachineKeyAuth != nil:
		finalHandler = d.MachineKeyAuth.Handler(finalHandler)
	}

	// Actor identity (agent headers -> context for audit attribution).
	// Outside auth on purpose: this middleware wraps auth, so it runs
	// before it and the context it builds flows through auth to the
	// handler; it records who acted, it never grants anything.
	finalHandler = actor.Middleware(finalHandler)

	// CORS — must be outside auth so OPTIONS preflight is handled before
	// auth.
	finalHandler = middleware.CORSMiddleware(finalHandler)

	// Rate limiting.
	if d.RateLimitRPM > 0 {
		finalHandler = middleware.RateLimit(d.RateLimitRPM, d.TrustedProxies)(finalHandler)
	}

	// Panic recovery.
	finalHandler = middleware.Recovery(d.Logger)(finalHandler)

	// Request ID generation.
	finalHandler = middleware.RequestID(finalHandler)

	// Prometheus HTTP metrics.
	finalHandler = metrics.HTTPMetrics(finalHandler)

	// Access logging (outermost — captures full request lifecycle).
	if d.Logger != nil {
		finalHandler = RequestLogger(d.Logger, d.TrustedProxies, finalHandler)
	}

	return finalHandler
}

// wrapConfirmGate wires the confirm gate's middleware against the
// audit sink. The gate is always mounted: a nil sink means "refuse
// exactly the same and write no row" (confirmgate.Middleware's own nil
// handling), never "no gate", so a chain built without a sink cannot
// fail open on the gated writes.
func wrapConfirmGate(next http.Handler, sink AuditSink) http.Handler {
	return confirmgate.Middleware(sink)(next)
}
