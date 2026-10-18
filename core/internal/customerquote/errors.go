// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"errors"
	"net/http"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// Error codes the customer quote routes add to the platform's.
const (
	CodeLinkUnavailable     = "link_unavailable"
	CodeCodeRequired        = "code_required"
	CodeCodeInvalid         = "code_invalid"
	CodeCodeExpired         = "code_expired"
	CodeCodeLocked          = "code_locked"
	CodeCodeBudgetExhausted = "code_budget_exhausted"
	CodeSelectionsChanged   = "selections_changed"
	CodeIncomplete          = "selections_incomplete"
	CodeCodesUnavailable    = "codes_unavailable"
)

// wireErr maps a domain error onto the platform's one error envelope. An
// unknown link and a quote that is not the caller's are the same bare 404.
func wireErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrLinkUnknown):
		return httpx.NotFound("not found")
	case errors.Is(err, ErrForbidden):
		return httpx.Forbidden("your role may not quote customers")
	case errors.Is(err, ErrInvalidState):
		return httpx.InvalidStateTransition("the quote is not in a state that allows this")
	case errors.Is(err, ErrSelectionsChanged):
		return &httpx.Error{Status: http.StatusConflict, Code: CodeSelectionsChanged,
			Message: "the selections changed since you viewed the quote: review the updated total and sign again",
			Details: []httpx.FieldError{{Field: "selections_version", Code: CodeSelectionsChanged, Message: "review the updated total and sign again"}}}
	case errors.Is(err, ErrLinkUnavailable):
		return &httpx.Error{Status: http.StatusGone, Code: CodeLinkUnavailable, Message: "this link has expired"}
	case errors.Is(err, ErrRateLimited):
		return httpx.RateLimited("too many requests: wait a minute and try again")
	case errors.Is(err, ErrIncomplete):
		return httpx.BadRequest("every selection needs a decision before you sign off",
			httpx.FieldError{Code: CodeIncomplete, Message: "approve or decline every selection, and choose one in each group"})
	case errors.Is(err, ErrNothingChosen):
		return httpx.BadRequest("nothing is approved: approve a selection, or decline the quote",
			httpx.FieldError{Code: CodeIncomplete, Message: "nothing is approved"})
	case errors.Is(err, ErrCodeRequired):
		return httpx.BadRequest("a verification code is required",
			httpx.FieldError{Field: "code", Code: CodeCodeRequired, Message: "request a code and enter it"})
	case errors.Is(err, ErrCodeWrong):
		return httpx.BadRequest("the verification code is not right",
			httpx.FieldError{Field: "code", Code: CodeCodeInvalid, Message: "is not right"})
	case errors.Is(err, ErrCodeExpired), errors.Is(err, ErrCodeConsumed):
		return httpx.BadRequest("the verification code has expired or was used",
			httpx.FieldError{Field: "code", Code: CodeCodeExpired, Message: "request a new code"})
	case errors.Is(err, ErrCodeLocked):
		return httpx.BadRequest("too many wrong codes: request a new code",
			httpx.FieldError{Field: "code", Code: CodeCodeLocked, Message: "request a new code"})
	case errors.Is(err, ErrCodeBudgetExhausted):
		return &httpx.Error{Status: http.StatusConflict, Code: CodeCodeBudgetExhausted,
			Message: "too many codes or wrong guesses on this link: ask the contractor to send a fresh link",
			Details: []httpx.FieldError{{Field: "code", Code: CodeCodeBudgetExhausted, Message: "ask the contractor to send a fresh link"}}}
	case errors.Is(err, ErrCodesUnavailable):
		return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict, Message: CodesUnavailableMessage,
			Details: []httpx.FieldError{{Code: CodeCodesUnavailable, Message: CodesUnavailableMessage}}}
	case errors.Is(err, ErrDeliveryFailed):
		return httpx.Unavailable("the code could not be delivered")
	}
	return err
}
