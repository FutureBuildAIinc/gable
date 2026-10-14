// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// decodeErrorBody parses a written error envelope into the shapes a client
// sees, so assertions run against the wire form rather than Go structs.
func decodeErrorBody(t *testing.T, w *httptest.ResponseRecorder) (code, message string, details []FieldError, requestID string) {
	t.Helper()
	var env struct {
		Error struct {
			Code    string       `json:"code"`
			Message string       `json:"message"`
			Details []FieldError `json:"details"`
		} `json:"error"`
		Meta struct {
			RequestID string `json:"request_id"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not the error envelope: %v (body: %s)", err, w.Body.String())
	}
	if env.Meta.RequestID == "" {
		t.Errorf("meta.request_id is empty; every error carries one")
	}
	return env.Error.Code, env.Error.Message, env.Error.Details, env.Meta.RequestID
}

func writeErrorRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/things", nil)
	r.Header.Set("X-Request-ID", "req-test")
	return r
}

// RULE (ADR 0001 §3): every error response is the one envelope, with the
// handler's own code, message, and field details, and the request id.
func TestWriteErrorWithBadRequestError(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, writeErrorRequest(), BadRequest("line 1: uom is required",
		FieldError{Field: "lines[0].uom", Message: "is required"}))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	code, message, details, _ := decodeErrorBody(t, w)
	if code != "bad_request" {
		t.Errorf("code = %q, want bad_request", code)
	}
	// The specific fix this package exists for: the handler's message reaches
	// the client, where httputil.RespondError wrote only "Bad Request".
	if message != "line 1: uom is required" {
		t.Errorf("message = %q, want the handler's message", message)
	}
	if len(details) != 1 || details[0].Field != "lines[0].uom" || details[0].Message != "is required" {
		t.Errorf("details = %+v, want the one field error", details)
	}
}

// RULE: the request id comes from the response header the request id
// middleware sets, falling back to the incoming request header.
func TestWriteErrorRequestID(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "resp-123")
	WriteError(w, writeErrorRequest(), BadRequest("nope"))
	_, _, _, id := decodeErrorBody(t, w)
	if id != "resp-123" {
		t.Errorf("request_id = %q, want the response header value", id)
	}

	w2 := httptest.NewRecorder()
	r := writeErrorRequest()
	r.Header.Set("X-Request-ID", "req-456")
	WriteError(w2, r, BadRequest("nope"))
	_, _, _, id2 := decodeErrorBody(t, w2)
	if id2 != "req-456" {
		t.Errorf("request_id = %q, want the incoming header value", id2)
	}
}

// RULE: an unknown error is a 500 in the same envelope, and a 5xx never
// puts the handler's message in the body (it may name internals; it goes to
// the log).
func TestWriteErrorInternal(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, writeErrorRequest(), errors.New("pq: connection refused at 10.0.0.4:5432"))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	code, message, details, id := decodeErrorBody(t, w)
	if code != CodeInternalError {
		t.Errorf("code = %q, want %q", code, CodeInternalError)
	}
	if message != internalErrorMessage {
		t.Errorf("message = %q, want the fixed %q", message, internalErrorMessage)
	}
	if details != nil {
		t.Errorf("details = %+v, want none on a 500", details)
	}
	if id == "" {
		t.Errorf("request_id missing on a 500, where it matters most")
	}
}

// RULE: a *Error carrying a 5xx gets the same substitution: the client sees
// the fixed message, the original goes only to the log.
func TestWriteErrorCarries5xxWithoutLeakingMessage(t *testing.T) {
	e := &Error{Status: http.StatusBadGateway, Code: "internal_error", Message: "upstream tax vendor returned html"}
	w := httptest.NewRecorder()
	WriteError(w, writeErrorRequest(), e)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
	_, message, details, _ := decodeErrorBody(t, w)
	if message != internalErrorMessage {
		t.Errorf("message = %q, want the fixed 5xx message", message)
	}
	if details != nil {
		t.Errorf("details = %+v, want none on a 5xx", details)
	}
}

// RULE: a *Error wrapped by a handler keeps its status and code: a return of
// fmt.Errorf around Conflict answers 409 with its message, not a 500.
func TestWriteErrorUnwrapsWrappedErrors(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, writeErrorRequest(),
		fmt.Errorf("create quote: %w", Conflict("a quote with this number already exists")))

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusConflict)
	}
	code, message, _, _ := decodeErrorBody(t, w)
	if code != CodeConflict {
		t.Errorf("code = %q, want %q", code, CodeConflict)
	}
	if message != "a quote with this number already exists" {
		t.Errorf("message = %q, want the wrapped Error's message", message)
	}
}

// RULE: a *Error built with no status is a server bug, and it renders as a
// 500 internal_error rather than panicking inside WriteHeader.
func TestWriteErrorZeroStatusDefaultsTo500(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, writeErrorRequest(), &Error{Message: "status was never set"})

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	code, message, details, _ := decodeErrorBody(t, w)
	if code != CodeInternalError {
		t.Errorf("code = %q, want %q", code, CodeInternalError)
	}
	if message != internalErrorMessage {
		t.Errorf("message = %q, want the fixed 5xx message", message)
	}
	if details != nil {
		t.Errorf("details = %+v, want none on a 500", details)
	}
}

// RULE: the code table. Each constructor pairs an HTTP status with its
// stable lowercase code.
func TestErrorConstructors(t *testing.T) {
	cases := []struct {
		err    *Error
		status int
		code   string
	}{
		{BadRequest("m"), http.StatusBadRequest, CodeBadRequest},
		{Unauthorized("m"), http.StatusUnauthorized, CodeUnauthorized},
		{Forbidden("m"), http.StatusForbidden, CodeForbidden},
		{NotFound("m"), http.StatusNotFound, CodeNotFound},
		{Conflict("m"), http.StatusConflict, CodeConflict},
		{RateLimited("m"), http.StatusTooManyRequests, CodeRateLimited},
		{MethodNotAllowed("m"), http.StatusMethodNotAllowed, CodeMethodNotAllowed},
		{UnsupportedMediaType("m"), http.StatusUnsupportedMediaType, CodeUnsupportedMediaType},
		{PayloadTooLarge("m"), http.StatusRequestEntityTooLarge, CodePayloadTooLarge},
		{PreconditionFailed("m"), http.StatusPreconditionFailed, CodePreconditionFailed},
		{PreconditionRequired("m"), http.StatusPreconditionRequired, CodePreconditionRequired},
		{StaleRevision("m"), http.StatusConflict, CodeStaleRevision},
		{Duplicate("m"), http.StatusConflict, CodeDuplicate},
		{IdempotencyInProgress("m"), http.StatusConflict, CodeIdempotencyInProgress},
		{InvalidStateTransition("m"), http.StatusConflict, CodeInvalidStateTransition},
		{IdempotencyKeyReused("m"), http.StatusUnprocessableEntity, CodeIdempotencyKeyReused},
		{Unavailable("m"), http.StatusServiceUnavailable, CodeUnavailable},
	}
	for _, tc := range cases {
		if tc.err.Status != tc.status {
			t.Errorf("status = %d, want %d", tc.err.Status, tc.status)
		}
		if tc.err.Code != tc.code {
			t.Errorf("code = %q, want %q", tc.err.Code, tc.code)
		}
	}
}

// RULE: Error satisfies the error interface so it flows through ordinary
// handler returns.
func TestErrorImplementsError(t *testing.T) {
	var err error = NotFound("thing 3f not found")
	if err.Error() != "thing 3f not found" {
		t.Errorf("Error() = %q", err.Error())
	}
}

// RULE: a details entry may be a blocker, carrying a code and a message
// with no field, for the business reasons a request fails that are not
// about any one field (a credit hold, a linked document). On the wire such
// an entry has no field key at all, never an empty one.
func TestBlockerDetailsCarryCodeWithoutField(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, writeErrorRequest(), InvalidStateTransition(
		"the order cannot ship", Blocker("uninvoiced_lines", "2 lines have no invoice")))

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusConflict)
	}
	var env struct {
		Error struct {
			Details []json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("body: %v", err)
	}
	if len(env.Error.Details) != 1 {
		t.Fatalf("details = %s, want one entry", w.Body.String())
	}
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(env.Error.Details[0], &entry); err != nil {
		t.Fatalf("entry: %v", err)
	}
	if _, has := entry["field"]; has {
		t.Errorf("blocker entry carries a field key: %s", env.Error.Details[0])
	}
	if string(entry["code"]) != `"uninvoiced_lines"` {
		t.Errorf("code = %s, want uninvoiced_lines", entry["code"])
	}
	if string(entry["message"]) != `"2 lines have no invoice"` {
		t.Errorf("message = %s", entry["message"])
	}
}

// RULE (C2-4): the one 5xx written for the client, a card charge the system
// could not give back, keeps its message (it names a gateway transaction id and
// nothing else), under its own code; every other 5xx stays masked.
func TestWriteErrorOperatorMessageSurvives5xx(t *testing.T) {
	e := &Error{Status: http.StatusBadGateway, Code: CodeChargeNotReversed, Operator: true,
		Message: "the card was charged and could not be reversed; reconcile gateway transaction gw-1"}
	w := httptest.NewRecorder()
	WriteError(w, writeErrorRequest(), e)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
	code, message, _, id := decodeErrorBody(t, w)
	if code != "charge_not_reversed" || message != e.Message || id == "" {
		t.Errorf("code %q message %q request_id %q, want the code, the message verbatim and a request id", code, message, id)
	}
}
