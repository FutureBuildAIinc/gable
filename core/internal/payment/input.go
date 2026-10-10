// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// ApplicationRequest is one invoice a payment settles.
type ApplicationRequest struct {
	InvoiceID     *string         `json:"invoice_id"`
	AmountCents   json.RawMessage `json:"amount_cents"`
	DiscountCents json.RawMessage `json:"discount_cents"`
}

// CreateRequest is the body of POST /api/v1/payments. A payment with no
// applications is unapplied cash held in 2200.
type CreateRequest struct {
	CustomerID   *string              `json:"customer_id"`
	BranchID     *string              `json:"branch_id"`
	AmountCents  json.RawMessage      `json:"amount_cents"`
	Method       *string              `json:"method"`
	Reference    *string              `json:"reference"`
	Notes        *string              `json:"notes"`
	ReceivedOn   *string              `json:"received_on"`
	OrderID      *string              `json:"order_id"`
	JobID        *string              `json:"job_id"`
	Applications []ApplicationRequest `json:"applications"`
}

// CardRequest is the body of POST /api/v1/payments/card: the same fields, a
// token from the gateway's tokenizer instead of a method.
type CardRequest struct {
	CustomerID   *string              `json:"customer_id"`
	BranchID     *string              `json:"branch_id"`
	AmountCents  json.RawMessage      `json:"amount_cents"`
	TokenID      *string              `json:"token_id"`
	Notes        *string              `json:"notes"`
	OrderID      *string              `json:"order_id"`
	JobID        *string              `json:"job_id"`
	Applications []ApplicationRequest `json:"applications"`
}

// Input is a validated payment ready for the service.
type Input struct {
	CustomerID   uuid.UUID
	BranchID     *uuid.UUID
	AmountCents  int64
	Method       PaymentMethod
	TokenID      string
	Reference    string
	Notes        string
	ReceivedOn   *time.Time
	OrderID      *uuid.UUID
	JobID        *uuid.UUID
	Applications []account.ApplyLine
}

func trim(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

func parseApplications(v *httpx.Validator, reqs []ApplicationRequest) []account.ApplyLine {
	out := make([]account.ApplyLine, 0, len(reqs))
	for i, a := range reqs {
		path := fmt.Sprintf("applications[%d]", i)
		var line account.ApplyLine
		if id, ok := v.UUID(path+".invoice_id", a.InvoiceID, true); ok {
			line.InvoiceID = id
		}
		if n, ok := v.Int(path+".amount_cents", a.AmountCents, true); ok {
			v.Check(n >= 1, path+".amount_cents", "must be 1 cent or more")
			line.AmountCents = n
		}
		if n, ok := v.Int(path+".discount_cents", a.DiscountCents, false); ok {
			v.Check(n >= 0, path+".discount_cents", "must not be negative")
			line.DiscountCents = n
		}
		out = append(out, line)
	}
	return out
}

func parseCommon(v *httpx.Validator, customer, branch, order, job *string, amount json.RawMessage, apps []ApplicationRequest) *Input {
	in := &Input{}
	if id, ok := v.UUID("customer_id", customer, true); ok {
		in.CustomerID = id
	}
	if branch != nil {
		if id, ok := v.UUID("branch_id", branch, false); ok {
			in.BranchID = &id
		}
	}
	if n, ok := v.Int("amount_cents", amount, true); ok {
		v.Check(n >= 1, "amount_cents", "must be 1 cent or more")
		in.AmountCents = n
	}
	if order != nil {
		if id, ok := v.UUID("order_id", order, false); ok {
			in.OrderID = &id
		}
	}
	if job != nil {
		if id, ok := v.UUID("job_id", job, false); ok {
			in.JobID = &id
		}
	}
	in.Applications = parseApplications(v, apps)
	var cash int64
	for _, l := range in.Applications {
		cash += l.AmountCents
	}
	v.Check(cash <= in.AmountCents || in.AmountCents < 1, "applications", "apply more than the payment's amount")
	return in
}

// Parse validates a create request in one pass: every problem is one field
// error with its full path.
func (req *CreateRequest) Parse() (*Input, error) {
	v := &httpx.Validator{}
	in := parseCommon(v, req.CustomerID, req.BranchID, req.OrderID, req.JobID, req.AmountCents, req.Applications)
	name := trim(req.Method)
	if req.Method == nil {
		v.Check(false, "method", "is required")
	} else if name == "card" {
		v.Check(false, "method", "a card payment is taken through POST /api/v1/payments/card")
	} else if name == "account" {
		v.Check(false, "method", "charging to account is not a payment")
	} else if m, ok := ParseMethod(name); ok {
		in.Method = m
	} else {
		v.Check(false, "method", "must be one of: "+strings.Join(recordableMethods, ", "))
	}
	in.Reference, in.Notes = trim(req.Reference), trim(req.Notes)
	v.Check(len(in.Reference) <= 128, "reference", "must be at most 128 characters")
	v.Check(len(in.Notes) <= 2000, "notes", "must be at most 2000 characters")
	if req.ReceivedOn != nil {
		t, err := time.Parse("2006-01-02", *req.ReceivedOn)
		if err != nil || t.Format("2006-01-02") != *req.ReceivedOn {
			v.Check(false, "received_on", "must be a date, YYYY-MM-DD")
		} else {
			in.ReceivedOn = &t
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return in, nil
}

// Parse validates a card request.
func (req *CardRequest) Parse() (*Input, error) {
	v := &httpx.Validator{}
	in := parseCommon(v, req.CustomerID, req.BranchID, req.OrderID, req.JobID, req.AmountCents, req.Applications)
	in.Method = PaymentMethodCard
	in.TokenID = trim(req.TokenID)
	v.Check(in.TokenID != "", "token_id", "is required")
	in.Notes = trim(req.Notes)
	v.Check(len(in.Notes) <= 2000, "notes", "must be at most 2000 characters")
	if err := v.Err(); err != nil {
		return nil, err
	}
	return in, nil
}

// ApplyRequest is the body of POST /payments/{id}/applications.
type ApplyRequest struct {
	Applications []ApplicationRequest `json:"applications"`
	Revision     json.RawMessage      `json:"revision"`
}

// ParseApply validates an apply request.
func (req *ApplyRequest) Parse() ([]account.ApplyLine, *int64, error) {
	v := &httpx.Validator{}
	v.Check(len(req.Applications) > 0, "applications", "name at least one invoice")
	lines := parseApplications(v, req.Applications)
	var rev *int64
	if n, ok := v.Int("revision", req.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		rev = &n
	}
	if err := v.Err(); err != nil {
		return nil, nil, err
	}
	return lines, rev, nil
}

// RefundRequest is the body of POST /payments/{id}/refunds and of
// POST /credit-memos/{id}/refunds.
type RefundRequest struct {
	AmountCents json.RawMessage `json:"amount_cents"`
	Reason      *string         `json:"reason"`
	Method      *string         `json:"method"`
	PaymentID   *string         `json:"payment_id"`
	Revision    json.RawMessage `json:"revision"`
}

// RefundInput is a validated refund.
type RefundInput struct {
	AmountCents int64
	Reason      string
	Method      PaymentMethod
	PaymentID   *uuid.UUID
	Revision    *int64
}

// Parse validates a refund of a payment's unapplied cash.
func (req *RefundRequest) Parse() (*RefundInput, error) {
	v := &httpx.Validator{}
	in := &RefundInput{}
	if n, ok := v.Int("amount_cents", req.AmountCents, true); ok {
		v.Check(n >= 1, "amount_cents", "must be 1 cent or more")
		in.AmountCents = n
	}
	in.Reason = trim(req.Reason)
	v.Check(in.Reason != "", "reason", "is required")
	v.Check(len(in.Reason) <= 500, "reason", "must be at most 500 characters")
	v.Check(req.Method == nil, "method", "is not accepted: a payment is refunded the way it was taken")
	v.Check(req.PaymentID == nil, "payment_id", "is not accepted on a payment refund")
	if n, ok := v.Int("revision", req.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		in.Revision = &n
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return in, nil
}

// ParseCredit validates a refund of a credit memo's open credit: the method is
// required, and a card refund names the card payment it goes back to.
func (req *RefundRequest) ParseCredit() (*RefundInput, error) {
	v := &httpx.Validator{}
	in := &RefundInput{}
	if n, ok := v.Int("amount_cents", req.AmountCents, true); ok {
		v.Check(n >= 1, "amount_cents", "must be 1 cent or more")
		in.AmountCents = n
	}
	in.Reason = trim(req.Reason)
	v.Check(in.Reason != "", "reason", "is required")
	v.Check(len(in.Reason) <= 500, "reason", "must be at most 500 characters")
	name := trim(req.Method)
	if req.Method == nil {
		v.Check(false, "method", "is required")
	} else if name == "card" {
		in.Method = PaymentMethodCard
		if id, ok := v.UUID("payment_id", req.PaymentID, true); ok {
			in.PaymentID = &id
		}
	} else if m, ok := ParseMethod(name); ok {
		in.Method = m
		v.Check(req.PaymentID == nil, "payment_id", "is only for a card refund")
	} else {
		v.Check(false, "method", "must be one of: card, "+strings.Join(recordableMethods, ", "))
	}
	if n, ok := v.Int("revision", req.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		in.Revision = &n
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return in, nil
}

// TransitionRequest is the body of POST /payments/{id}/transitions.
type TransitionRequest struct {
	To       *string         `json:"to"`
	Revision json.RawMessage `json:"revision"`
	Reason   *string         `json:"reason"`
}
