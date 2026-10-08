// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// RULE (ADR 0001 §4): validation collects every field error in one pass and
// renders one 400 with all of them; a client fixing a form sees the whole
// list.
func TestValidatorCollectsAllFieldErrors(t *testing.T) {
	qty := -2
	v := &Validator{}
	v.Required("name", "")
	v.Enum("status", "SENT", "draft", "sent")
	v.Check(qty > 0, "quantity", "must be positive")

	err := v.Err()
	if err == nil {
		t.Fatal("Err() = nil, want a validation error")
	}
	e, ok := err.(*Error)
	if !ok {
		t.Fatalf("Err() is %T, want *Error", err)
	}
	if e.Status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", e.Status)
	}
	if e.Code != CodeValidationFailed {
		t.Errorf("code = %q, want %q", e.Code, CodeValidationFailed)
	}
	if len(e.Details) != 3 {
		t.Fatalf("details = %+v, want all three field errors", e.Details)
	}
	want := []FieldError{
		{Field: "name", Message: "is required"},
		{Field: "status", Message: "must be one of: draft, sent"},
		{Field: "quantity", Message: "must be positive"},
	}
	for i, w := range want {
		if e.Details[i] != w {
			t.Errorf("details[%d] = %+v, want %+v", i, e.Details[i], w)
		}
	}
}

// RULE: a clean validator returns nil.
func TestValidatorClean(t *testing.T) {
	v := &Validator{}
	v.Required("name", "fence pickets")
	v.Enum("status", "draft", "draft", "sent")
	v.Check(true, "sku", "unreachable")
	if err := v.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil", err)
	}
}

// RULE: whitespace-only is absent for Required.
func TestValidatorRequiredTreatsWhitespaceAsEmpty(t *testing.T) {
	v := &Validator{}
	v.Required("name", "   ")
	if err := v.Err(); err == nil {
		t.Fatal("whitespace-only value passed Required")
	}
}

// RULE: the validator's error flows straight into WriteError as the one
// envelope with the details in place.
func TestValidatorErrorWritesEnvelope(t *testing.T) {
	v := &Validator{}
	v.Required("uom", "")
	w := httptest.NewRecorder()
	WriteError(w, writeErrorRequest(), v.Err())

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	code, _, details, _ := decodeErrorBody(t, w)
	if code != CodeValidationFailed {
		t.Errorf("code = %q, want %q", code, CodeValidationFailed)
	}
	if len(details) != 1 || details[0].Field != "uom" {
		t.Errorf("details = %+v, want the uom field error", details)
	}
}
