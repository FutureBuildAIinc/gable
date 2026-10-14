// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// Stable error codes (ADR 0001 §3). Clients branch on these, never on
// message text.
const (
	CodeBadRequest                = "bad_request"
	CodeValidationFailed          = "validation_failed"
	CodeUnsupportedQueryParameter = "unsupported_query_parameter"
	CodeUnauthorized              = "unauthorized"
	CodeForbidden                 = "forbidden"
	CodeNotFound                  = "not_found"
	CodeConflict                  = "conflict"
	CodeStaleRevision             = "stale_revision"
	CodeDuplicate                 = "duplicate"
	CodeIdempotencyInProgress     = "idempotency_in_progress"
	CodeInvalidStateTransition    = "invalid_state_transition"
	CodePreconditionFailed        = "precondition_failed"
	CodePreconditionRequired      = "precondition_required"
	CodeMethodNotAllowed          = "method_not_allowed"
	CodePayloadTooLarge           = "payload_too_large"
	CodeUnsupportedMediaType      = "unsupported_media_type"
	CodeIdempotencyKeyReused      = "idempotency_key_reused"
	CodeRateLimited               = "rate_limited"
	CodeUnavailable               = "unavailable"
	CodeInternalError             = "internal_error"
	// CodePaymentRequired is the 402 of a card charge that was declined or that
	// the gateway could not take, before anything was recorded.
	CodePaymentRequired = "payment_required"
	// CodeChargeNotReversed is the 502 of a card charge the gateway approved,
	// the system refused, and nothing gave back (ADR 0005 9.4).
	CodeChargeNotReversed = "charge_not_reversed"
)

// internalErrorMessage is the only message a 5xx body carries. A handler
// message on a 5xx is written while debugging an unexpected fault and can
// name tables, files, or drivers; it goes to the log with the request id,
// never to the client. This is the one carve-out on keeping the handler's
// message, and it exists so that rule is safe to follow everywhere else.
const internalErrorMessage = "internal error"

// FieldError is one entry of an error envelope's details. Most entries are
// about a field: Field is its path (a JSON path for body fields, the
// parameter name for query parameters, "cursor" for the cursor) and Message
// says what is wrong with it. An entry may instead be a blocker, a business
// reason the request failed that belongs to no one field (a credit hold, a
// linked document): then Field is empty and Code carries the reason's own
// stable code.
type FieldError struct {
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// Blocker builds one blocker details entry: a code and a message with no
// field, for the business reasons a request fails that are not about any
// one field.
func Blocker(code, msg string) FieldError {
	return FieldError{Code: code, Message: msg}
}

// errorEnvelope is the wire shape of every error response.
type errorEnvelope struct {
	Error errorBody `json:"error"`
	Meta  errorMeta `json:"meta"`
}

type errorBody struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Details []FieldError `json:"details,omitempty"`
}

type errorMeta struct {
	RequestID string `json:"request_id"`
}

// Error is the error value handlers return; WriteError renders it as the one
// envelope. It satisfies the error interface so it flows through ordinary
// handler returns.
type Error struct {
	Status  int
	Code    string
	Message string
	Details []FieldError
	// Operator marks a 5xx whose message is written for the client by
	// construction (it names no table, file or driver): WriteError keeps it
	// verbatim instead of substituting the fixed internal message. The one
	// user is charge_not_reversed, where finance must read which gateway
	// transaction to reconcile.
	Operator bool
}

func (e *Error) Error() string { return e.Message }

// BadRequest builds a 400 bad_request. A field-level problem should use a
// Validator (or CodeValidationFailed) instead so the details name the field.
func BadRequest(msg string, details ...FieldError) *Error {
	return &Error{Status: http.StatusBadRequest, Code: CodeBadRequest, Message: msg, Details: details}
}

// Unauthorized builds a 401 unauthorized.
func Unauthorized(msg string) *Error {
	return &Error{Status: http.StatusUnauthorized, Code: CodeUnauthorized, Message: msg}
}

// Forbidden builds a 403 forbidden.
func Forbidden(msg string) *Error {
	return &Error{Status: http.StatusForbidden, Code: CodeForbidden, Message: msg}
}

// NotFound builds a 404 not_found.
func NotFound(msg string) *Error {
	return &Error{Status: http.StatusNotFound, Code: CodeNotFound, Message: msg}
}

// Conflict builds a 409 conflict.
func Conflict(msg string) *Error {
	return &Error{Status: http.StatusConflict, Code: CodeConflict, Message: msg}
}

// StaleRevision builds a 409 stale_revision: the write was built on a
// revision the document has moved past (section 11's concurrency rule).
func StaleRevision(msg string) *Error {
	return &Error{Status: http.StatusConflict, Code: CodeStaleRevision, Message: msg}
}

// Duplicate builds a 409 duplicate, optionally carrying the blockers that
// name what the new value collides with.
func Duplicate(msg string, blockers ...FieldError) *Error {
	return &Error{Status: http.StatusConflict, Code: CodeDuplicate, Message: msg, Details: blockers}
}

// IdempotencyInProgress builds a 409 idempotency_in_progress: the same key
// is still executing its first request.
func IdempotencyInProgress(msg string) *Error {
	return &Error{Status: http.StatusConflict, Code: CodeIdempotencyInProgress, Message: msg}
}

// InvalidStateTransition builds a 409 invalid_state_transition, optionally
// carrying the blockers that name what blocks the transition.
func InvalidStateTransition(msg string, blockers ...FieldError) *Error {
	return &Error{Status: http.StatusConflict, Code: CodeInvalidStateTransition, Message: msg, Details: blockers}
}

// PreconditionFailed builds a 412 precondition_failed.
func PreconditionFailed(msg string) *Error {
	return &Error{Status: http.StatusPreconditionFailed, Code: CodePreconditionFailed, Message: msg}
}

// PreconditionRequired builds a 428 precondition_required: the write needs
// If-Match or a body revision and carries neither (section 11).
func PreconditionRequired(msg string) *Error {
	return &Error{Status: http.StatusPreconditionRequired, Code: CodePreconditionRequired, Message: msg}
}

// MethodNotAllowed builds a 405 method_not_allowed.
func MethodNotAllowed(msg string) *Error {
	return &Error{Status: http.StatusMethodNotAllowed, Code: CodeMethodNotAllowed, Message: msg}
}

// UnsupportedMediaType builds a 415 unsupported_media_type.
func UnsupportedMediaType(msg string) *Error {
	return &Error{Status: http.StatusUnsupportedMediaType, Code: CodeUnsupportedMediaType, Message: msg}
}

// PayloadTooLarge builds a 413 payload_too_large.
func PayloadTooLarge(msg string) *Error {
	return &Error{Status: http.StatusRequestEntityTooLarge, Code: CodePayloadTooLarge, Message: msg}
}

// IdempotencyKeyReused builds a 422 idempotency_key_reused: the key was
// stored against a different request fingerprint (section 9).
func IdempotencyKeyReused(msg string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: CodeIdempotencyKeyReused, Message: msg}
}

// Unavailable builds a 503 unavailable.
func Unavailable(msg string) *Error {
	return &Error{Status: http.StatusServiceUnavailable, Code: CodeUnavailable, Message: msg}
}

// RateLimited builds a 429 rate_limited.
func RateLimited(msg string) *Error {
	return &Error{Status: http.StatusTooManyRequests, Code: CodeRateLimited, Message: msg}
}

// requestID reads the request id the way the request id middleware records
// it: on the response header it sets, falling back to the incoming request
// header when the middleware has not run (direct handler tests).
func requestID(w http.ResponseWriter, r *http.Request) string {
	if id := w.Header().Get("X-Request-ID"); id != "" {
		return id
	}
	return r.Header.Get("X-Request-ID")
}

// WriteError writes the one error envelope (ADR 0001 §3).
//
// A *Error renders with its own status, code, message, and field details:
// the handler's message reaches the client verbatim (the fix for the legacy
// RespondError, which logged the handler message and wrote only the generic
// status text). Any other error is a 500 internal_error. Statuses of 500 and
// above substitute the fixed internal message in the body; the real message
// is logged with the request id either way, and 4xx responses are not logged
// at all: they are the client's, stated in full on the wire.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	// errors.As, not a type assertion: handlers wrap their returns
	// (fmt.Errorf with %w), and a wrapped *Error must keep its status
	// instead of collapsing into a 500.
	var e *Error
	if !errors.As(err, &e) || e == nil {
		e = &Error{Status: http.StatusInternalServerError, Code: CodeInternalError, Message: err.Error()}
	} else if e.Status == 0 {
		// A *Error built with no status is a server bug; it renders as a
		// 500 rather than panicking inside WriteHeader.
		e = &Error{Status: http.StatusInternalServerError, Code: CodeInternalError,
			Message: e.Message, Details: e.Details}
	}

	body := errorBody{Code: e.Code, Message: e.Message, Details: e.Details}
	if e.Status >= 500 {
		if !e.Operator {
			body.Message = internalErrorMessage
			body.Details = nil
		}
		slog.Error(e.Message,
			"status", e.Status,
			"method", r.Method,
			"path", r.URL.Path,
			"request_id", requestID(w, r),
		)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{
		Error: body,
		Meta:  errorMeta{RequestID: requestID(w, r)},
	})
}
