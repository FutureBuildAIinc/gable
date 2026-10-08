// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"net/http"
	"strings"
)

// Validator collects field errors across a whole request and renders them as
// one 400 (ADR 0001 §4): a client fixing a form sees every problem, not one
// per round trip. The zero value is ready to use.
//
//	v := &httpx.Validator{}
//	v.Required("name", req.Name)
//	v.Enum("status", req.Status, "draft", "sent")
//	if err := v.Err(); err != nil {
//		httpx.WriteError(w, r, err)
//		return
//	}
type Validator struct {
	details []FieldError
}

// Check records a field error when ok is false. The message states what is
// wrong with the field ("is required", "must be a UUID"), not how to fix the
// request.
func (v *Validator) Check(ok bool, field, message string) {
	if !ok {
		v.details = append(v.details, FieldError{Field: field, Message: message})
	}
}

// Required records a field error when value is empty or whitespace.
func (v *Validator) Required(field, value string) {
	v.Check(strings.TrimSpace(value) != "", field, "is required")
}

// Enum records a field error when value is not one of the allowed values,
// naming the vocabulary in the message so the client can self-correct.
func (v *Validator) Enum(field, value string, allowed ...string) {
	for _, a := range allowed {
		if value == a {
			return
		}
	}
	v.Check(false, field, "must be one of: "+strings.Join(allowed, ", "))
}

// Err returns nil when every check passed, and a 400 validation_failed Error
// carrying every collected field error otherwise. The result goes straight
// to WriteError.
func (v *Validator) Err() error {
	if len(v.details) == 0 {
		return nil
	}
	return &Error{
		Status:  http.StatusBadRequest,
		Code:    CodeValidationFailed,
		Message: "one or more fields failed validation",
		Details: v.details,
	}
}
