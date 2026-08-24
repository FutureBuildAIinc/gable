// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gablelbm/gable/internal/order"
)

// Sentinel errors for the capabilities added on top of migration 084.
//
// They exist so the handler can turn a refusal into the right status code
// without string-matching, and so the distinction that matters most survives
// the trip to the client: 404 "there is no such thing here" versus 409 "there
// is, and the answer is no".
//
// Nothing in this file distinguishes "belongs to another customer" from "does
// not exist". Both are ErrQuoteNotFound / ErrOrderNotFound / ErrDeliveryNotFound
// and both render as 404. A 403 on someone else's id is an existence oracle:
// it tells an authenticated contractor which ids belong to their competitors.
var (
	// ErrQuoteNotFound — no quote with that id belongs to the caller.
	ErrQuoteNotFound = errors.New("quote not found")

	// ErrQuoteNotDecidable — the quote exists but is not in a state the
	// customer may accept or decline (it has not been priced, or it is
	// already closed).
	ErrQuoteNotDecidable = errors.New("quote cannot be accepted or declined in its current state")

	// ErrOrderNotFound — no order with that id belongs to the caller.
	ErrOrderNotFound = errors.New("order not found")

	// ErrProjectNotFound — no project with that id belongs to the caller.
	ErrProjectNotFound = errors.New("project not found")

	// ErrDeliveryNotFound — no delivery with that id belongs to the caller.
	ErrDeliveryNotFound = errors.New("delivery not found")

	// ErrRescheduleRefused — the delivery exists but cannot be rescheduled
	// from the portal, because the truck is already rolling or the stop is
	// already done. See reschedule.go for why this is a refusal rather than
	// a best-effort write.
	ErrRescheduleRefused = errors.New("delivery cannot be rescheduled")

	// ErrCancelRefused — the order exists and is cancellable as far as the ERP
	// state machine is concerned, but its goods are already on a dispatched
	// route or delivered. The portal refuses; a dealer can still cancel from
	// the ERP side with a human deciding what happens to the pallet.
	ErrCancelRefused = errors.New("order cannot be cancelled from the portal")

	// ErrInvalidRequest — the payload was syntactically fine and semantically
	// wrong (a past date, an empty scope, an unknown unit of measure).
	ErrInvalidRequest = errors.New("invalid request")
)

// refusalReasons maps a sentinel onto a short, customer-safe explanation.
//
// httputil.RespondError deliberately replaces every message with a generic one
// ("Conflict") so internal detail cannot leak. That is right for a 500 and
// wrong for a refusal: a consumer building a stage machine has to be able to
// tell the contractor WHY, and "Conflict" is not an answer anyone can act on.
//
// The strings here are hand-written and contain no ids, no table names and no
// error text — only what a counter salesperson would say out loud. Anything
// not in this map gets the generic envelope, so an unexpected error can never
// reach a client through this path.
var refusalReasons = map[error]struct {
	code   string
	reason string
}{
	ErrQuoteNotDecidable: {"QUOTE_NOT_PRICED",
		"This quote has not been priced and sent by the dealer yet, or it has already been closed."},
	ErrRescheduleRefused: {"DELIVERY_COMMITTED",
		"This delivery can no longer be rescheduled from the portal — the load is already on a truck or the stop is complete. Call the dealer."},
	ErrCancelRefused: {"ORDER_IN_MOTION",
		"This order's goods are already on a dispatched route or delivered. Call the dealer."},
	order.ErrOrderAlreadyCancelled: {"ORDER_ALREADY_CANCELLED",
		"This order has already been cancelled."},
	order.ErrOrderNotCancellable: {"ORDER_NOT_CANCELLABLE",
		"A fulfilled order cannot be cancelled. Ask the dealer for a credit."},
}

// portalRefusal is the 409 envelope. It keeps httputil.ErrorResponse's shape —
// same `error.code`, `error.message`, `meta.request_id` — and adds `reason`,
// so a client that does not know about it is unaffected.
type portalRefusal struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Reason  string `json:"reason"`
	} `json:"error"`
	Meta struct {
		RequestID string `json:"request_id"`
	} `json:"meta"`
}

// writeRefusal renders a known refusal with its reason and reports true.
// Returns false for anything it does not recognise, so the caller falls back
// to the generic error path.
func writeRefusal(w http.ResponseWriter, r *http.Request, err error, status int) bool {
	if status != http.StatusConflict {
		return false
	}
	for sentinel, detail := range refusalReasons {
		if !errors.Is(err, sentinel) {
			continue
		}
		reqID := w.Header().Get("X-Request-ID")
		if reqID == "" {
			reqID = r.Header.Get("X-Request-ID")
		}
		slog.Warn("portal refusal",
			"error", err, "status", status, "method", r.Method, "path", r.URL.Path, "request_id", reqID)

		var body portalRefusal
		body.Error.Code = detail.code
		body.Error.Message = "Conflict"
		body.Error.Reason = detail.reason
		body.Meta.RequestID = reqID

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
		return true
	}
	return false
}

// statusForPortalError maps a sentinel onto an HTTP status. Anything
// unrecognised falls through to the caller's default, which is how an
// unexpected failure stays a 500 instead of being flattened into a 4xx that
// tells the client not to retry.
func statusForPortalError(err error, fallback int) int {
	switch {
	case errors.Is(err, ErrQuoteNotFound),
		errors.Is(err, ErrOrderNotFound),
		errors.Is(err, ErrProjectNotFound),
		errors.Is(err, ErrDeliveryNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrQuoteNotDecidable),
		errors.Is(err, ErrRescheduleRefused),
		errors.Is(err, ErrCancelRefused),
		// The ERP's own cancellation refusals. "Already cancelled" and
		// "fulfilled orders cannot be cancelled" are correct answers to a
		// reasonable question, not server faults, and a client needs to know
		// that retrying will never help.
		errors.Is(err, order.ErrOrderAlreadyCancelled),
		errors.Is(err, order.ErrOrderNotCancellable):
		return http.StatusConflict
	case errors.Is(err, ErrInvalidRequest):
		return http.StatusBadRequest
	default:
		return fallback
	}
}
