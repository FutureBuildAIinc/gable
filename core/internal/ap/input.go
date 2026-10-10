// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package ap

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/money"
	"github.com/google/uuid"
)

func trim(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

func parseDate(v *httpx.Validator, field, raw string, required bool) (string, bool) {
	if raw == "" {
		if required {
			v.Check(false, field, "is required as YYYY-MM-DD")
			return "", false
		}
		return "", true
	}
	if _, err := time.Parse("2006-01-02", raw); err != nil {
		v.Check(false, field, "must be YYYY-MM-DD")
		return "", false
	}
	return raw, true
}

// CreateLineRequest is one line of a bill.
type CreateLineRequest struct {
	Description          *string         `json:"description"`
	Quantity             json.RawMessage `json:"quantity"`
	UnitPriceTenThousand json.RawMessage `json:"unit_price_ten_thousandths"`
	GLAccountID          *string         `json:"gl_account_id"`
	PurchaseOrderLineID  *string         `json:"purchase_order_line_id"`
	ProductID            *string         `json:"product_id"`
}

// CreateRequest is the body of POST /api/v1/ap/invoices.
type CreateRequest struct {
	VendorID            *string             `json:"vendor_id"`
	BranchID            *string             `json:"branch_id"`
	VendorInvoiceNumber *string             `json:"vendor_invoice_number"`
	InvoiceDate         *string             `json:"invoice_date"`
	DueDate             *string             `json:"due_date"`
	POID                *string             `json:"po_id"`
	Currency            *string             `json:"currency"`
	TaxCents            json.RawMessage     `json:"tax_cents"`
	Notes               *string             `json:"notes"`
	Lines               []CreateLineRequest `json:"lines"`
}

// LineInput is one validated line. The extension is priced by the service
// through httpx.Extend, never sent by the client.
type LineInput struct {
	Description         string
	Quantity            httpx.Quantity
	UnitPrice           httpx.Price
	GLAccountID         *uuid.UUID
	PurchaseOrderLineID *uuid.UUID
	ProductID           *uuid.UUID
	LineTotalCents      httpx.Cents
}

// Input is a validated bill ready for the service.
type Input struct {
	VendorID            uuid.UUID
	BranchID            *uuid.UUID
	VendorInvoiceNumber string
	InvoiceDate         string
	DueDate             string
	POID                *uuid.UUID
	Currency            string
	TaxCents            httpx.Cents
	Notes               string
	Lines               []LineInput
}

// Parse validates a create request in one pass: every problem is one field
// error with its full path (ADR 0001 section 4). The status is never in the
// request: a created bill starts pending.
func (req *CreateRequest) Parse() (*Input, error) {
	v := &httpx.Validator{}
	in := &Input{}
	if id, ok := v.UUID("vendor_id", req.VendorID, true); ok {
		in.VendorID = id
	}
	if req.BranchID != nil {
		if id, ok := v.UUID("branch_id", req.BranchID, false); ok {
			in.BranchID = &id
		}
	}
	number := trim(req.VendorInvoiceNumber)
	v.Check(number != "", "vendor_invoice_number", "is required")
	v.Check(len(number) <= 64, "vendor_invoice_number", "must be at most 64 characters")
	in.VendorInvoiceNumber = number
	if d, ok := parseDate(v, "invoice_date", trim(req.InvoiceDate), true); ok {
		in.InvoiceDate = d
	}
	if d, ok := parseDate(v, "due_date", trim(req.DueDate), true); ok {
		in.DueDate = d
	}
	if req.POID != nil {
		if id, ok := v.UUID("po_id", req.POID, false); ok {
			in.POID = &id
		}
	}
	currency := trim(req.Currency)
	if currency == "" {
		currency = "USD" // the service replaces this with the setting's default
	}
	v.Check(len(currency) == 3 && currency == strings.ToUpper(currency), "currency", "must be an ISO 4217 code of three capital letters")
	in.Currency = currency
	if n, ok := v.Int("tax_cents", req.TaxCents, false); ok {
		v.Check(n >= 0, "tax_cents", "must not be negative")
		in.TaxCents = httpx.Cents(n)
	}
	in.Notes = trim(req.Notes)
	v.Check(len(in.Notes) <= 2000, "notes", "must be at most 2000 characters")

	v.Check(len(req.Lines) >= 1, "lines", "a bill carries at least one line")
	var subtotal httpx.Cents
	for i, l := range req.Lines {
		path := fmt.Sprintf("lines[%d]", i)
		line := LineInput{}
		desc := trim(l.Description)
		v.Check(desc != "", path+".description", "is required")
		v.Check(len(desc) <= 256, path+".description", "must be at most 256 characters")
		line.Description = desc
		if q, ok := v.Quantity(path+".quantity", l.Quantity, true); ok {
			v.Check(q > 0, path+".quantity", "must be more than zero")
			line.Quantity = q
		}
		if p, ok := v.Int(path+".unit_price_ten_thousandths", l.UnitPriceTenThousand, true); ok {
			v.Check(p >= 0, path+".unit_price_ten_thousandths", "must not be negative")
			line.UnitPrice = httpx.Price(p)
		}
		if l.GLAccountID != nil {
			if id, ok := v.UUID(path+".gl_account_id", l.GLAccountID, false); ok {
				line.GLAccountID = &id
			}
		}
		v.Check(line.GLAccountID != nil, path+".gl_account_id", "is required: the approve entry debits each line's account")
		if l.PurchaseOrderLineID != nil {
			if id, ok := v.UUID(path+".purchase_order_line_id", l.PurchaseOrderLineID, false); ok {
				line.PurchaseOrderLineID = &id
			}
		}
		if l.ProductID != nil {
			if id, ok := v.UUID(path+".product_id", l.ProductID, false); ok {
				line.ProductID = &id
			}
		}
		if line.GLAccountID != nil && line.Quantity != 0 && line.UnitPrice >= 0 {
			total, err := httpx.Extend(line.Quantity, 1, 1, line.UnitPrice)
			if err != nil {
				v.Check(false, path+".quantity", err.Error())
			} else {
				line.LineTotalCents = total
				subtotal += total
			}
		}
		in.Lines = append(in.Lines, line)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return in, nil
}

// TransitionRequest is the body of POST /api/v1/ap/invoices/{id}/transitions.
type TransitionRequest struct {
	To       *string         `json:"to"`
	Revision json.RawMessage `json:"revision"`
	Reason   *string         `json:"reason"`
}

// TransitionInput is a validated transition: the target status, the client's
// revision and the reason a void asks for.
type TransitionInput struct {
	To       Status
	Revision *int64
	Reason   string
}

// Parse validates a transition request in one pass.
func (req *TransitionRequest) Parse() (*TransitionInput, error) {
	v := &httpx.Validator{}
	in := &TransitionInput{}
	if req.To == nil {
		v.Check(false, "to", "is required")
	} else if s, ok := ParseStatus(*req.To); !ok {
		v.Check(false, "to", "must be one of: pending, approved, partial, paid, voided")
	} else {
		in.To = s
	}
	if n, ok := v.Int("revision", req.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		in.Revision = &n
	}
	in.Reason = trim(req.Reason)
	v.Check(len(in.Reason) <= 500, "reason", "must be at most 500 characters")
	if err := v.Err(); err != nil {
		return nil, err
	}
	return in, nil
}

// PayRequest is the body of POST /api/v1/ap/payments. The payment routes keep
// their request shape until their own conversion (ADR 0008 section 11); only
// the parsing is held to one pass here.
type PayRequest struct {
	VendorID    *string  `json:"vendor_id"`
	Amount      *float64 `json:"amount"`
	Method      *string  `json:"method"`
	CheckNumber *string  `json:"check_number"`
	Reference   *string  `json:"reference"`
	PaymentDate *string  `json:"payment_date"`
	InvoiceIDs  []string `json:"invoice_ids"`
}

// PayInput is a validated payment ready for the service.
type PayInput struct {
	VendorID    uuid.UUID
	AmountCents int64
	Method      PaymentMethod
	CheckNumber string
	Reference   string
	PaymentDate string
	InvoiceIDs  []uuid.UUID
}

// Parse validates a pay request in one pass.
func (req *PayRequest) Parse() (*PayInput, error) {
	v := &httpx.Validator{}
	in := &PayInput{}
	if id, ok := v.UUID("vendor_id", req.VendorID, true); ok {
		in.VendorID = id
	}
	if req.Amount == nil {
		v.Check(false, "amount", "is required")
	} else {
		v.Check(*req.Amount > 0, "amount", "must be more than zero")
		in.AmountCents = money.DollarsToCents(*req.Amount)
	}
	if req.Method == nil {
		v.Check(false, "method", "is required")
	} else {
		switch PaymentMethod(strings.ToUpper(trim(req.Method))) {
		case PaymentMethodCheck, PaymentMethodACH, PaymentMethodWire:
			in.Method = PaymentMethod(strings.ToUpper(trim(req.Method)))
		default:
			v.Check(false, "method", "must be one of: CHECK, ACH, WIRE")
		}
	}
	in.CheckNumber = trim(req.CheckNumber)
	in.Reference = trim(req.Reference)
	if d, ok := parseDate(v, "payment_date", trim(req.PaymentDate), true); ok {
		in.PaymentDate = d
	}
	for i, raw := range req.InvoiceIDs {
		id, ok := v.UUID(fmt.Sprintf("invoice_ids[%d]", i), &raw, true)
		if ok {
			in.InvoiceIDs = append(in.InvoiceIDs, id)
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return in, nil
}
