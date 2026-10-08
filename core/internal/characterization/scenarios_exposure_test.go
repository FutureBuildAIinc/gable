// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

// Exposure group: the pricing module's quote-exposure surface (lumber index
// movement vs quoted prices). It runs after the quote group and reuses that
// group's quote: a manual quote with no lumber-index exposure sits in state
// OK, so the acknowledge and override writes answer the service's
// "already cleared" refusal - which is the honest base behaviour for a clean
// quote and pins the guard itself. The reads and the event-ledger writes
// succeed and pin their shapes. Under AUTH_MODE=dev the exposure helpers treat
// the caller as owner with the dev actor id.

func exposureGroups() []groupDef {
	return []groupDef{{
		name: "exposure",
		steps: []stepDef{
			{name: "exposure.get", method: "GET", path: "/api/v1/quotes/{myQuote}/exposure"},
			// Records an ACK_REQUESTED event on the quote's ledger.
			{name: "exposure.request_ack", method: "POST", path: "/api/v1/quotes/{myQuote}/exposure/request-ack"},
			// The ledger now carries the event from the step above.
			{name: "exposure.get_after_request_ack", method: "GET", path: "/api/v1/quotes/{myQuote}/exposure"},
			// Notes and method are valid; the quote's exposure is clear, so
			// the write is refused.
			{
				name:   "exposure.acknowledge.already_cleared",
				method: "POST",
				path:   "/api/v1/quotes/{myQuote}/exposure/acknowledge",
				body:   map[string]any{"method": "EMAIL", "notes": "customer confirmed over email"},
			},
			// Same guard on the owner override (dev mode is owner).
			{
				name:   "exposure.override.already_cleared",
				method: "POST",
				path:   "/api/v1/quotes/{myQuote}/exposure/override",
				body:   map[string]any{"notes": "golden characterisation override"},
			},
			// Dry-run re-quote preview; persists nothing.
			{name: "exposure.escalate_now", method: "POST", path: "/api/v1/quotes/{myQuote}/exposure/escalate-now"},
			// The at-risk book: dev mode is owner, owner=all is allowed.
			{name: "exposure.list", method: "GET", path: "/api/v1/quotes/exposure?owner=all"},
			{name: "exposure.list.summary", method: "GET", path: "/api/v1/quotes/exposure?owner=all&summary=true"},
		},
	}}
}
