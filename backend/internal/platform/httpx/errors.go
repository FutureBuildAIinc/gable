// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"encoding/json"
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
	CodeRateLimited               = "rate_limited"
	CodeInternalError             = "internal_error"
)

// internalErrorMessage is the only message a 5xx body carries. A handler
// message on a 5xx is written while debugging an unexpected fault and can
// name tables, files, or drivers; it goes to the log with the request id,
// never to the client. This is the one carve-out on keeping the handler's
// message, and it exists so that rule is safe to follow everywhere else.
const internalErrorMessage = "internal error"

// FieldError is one entry of an error envelope's details: the offending
// field's path (a JSON path for body fields, the parameter name for query
// parameters, "cursor" for the cursor) and what is wrong with it.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
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
	e, ok := err.(*Error)
	if !ok {
		e = &Error{Status: http.StatusInternalServerError, Code: CodeInternalError, Message: err.Error()}
	}

	body := errorBody{Code: e.Code, Message: e.Message, Details: e.Details}
	if e.Status >= 500 {
		body.Message = internalErrorMessage
		body.Details = nil
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
