// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"encoding/json"
	"fmt"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/google/uuid"
)

// isAbsentRaw reports whether a raw JSON field was missing or null.
func isAbsentRaw(raw json.RawMessage) bool {
	s := string(raw)
	return s == "" || s == "null"
}

// startSaleRequest is POST /pos/transactions.
type startSaleRequest struct {
	RegisterID *string `json:"register_id"`
	CashierID  *string `json:"cashier_id"`
	CustomerID *string `json:"customer_id"`
}

func (r *startSaleRequest) parse() (*StartSale, error) {
	v := &httpx.Validator{}
	out := &StartSale{}
	if r.RegisterID != nil {
		out.RegisterID = *r.RegisterID
	}
	if out.RegisterID == "" {
		out.RegisterID = "REG-01"
	}
	if id, ok := v.UUID("cashier_id", r.CashierID, false); ok {
		out.CashierID = id
	}
	if id, ok := v.UUID("customer_id", r.CustomerID, false); ok {
		out.CustomerID = &id
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// StartSale is a parsed counter sale create.
type StartSale struct {
	RegisterID string
	CashierID  uuid.UUID
	CustomerID *uuid.UUID
}

// addLineRequest is POST /pos/transactions/{id}/items: one line in the shared
// shape (ADR 0005 section 2), parsed by salesdoc.ParseLines.
type addLineRequest struct {
	Line salesdoc.LineRequest `json:"line"`
}

func (r *addLineRequest) parse() ([]salesdoc.ParsedLine, error) {
	v := &httpx.Validator{}
	lines := salesdoc.ParseLines(v, []salesdoc.LineRequest{r.Line})
	if err := v.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// completeSaleRequest is POST /pos/transactions/{id}/complete: the tenders in
// cents (IN-1.2) and who collected the basket.
type completeSaleRequest struct {
	Tenders    []tenderRequest `json:"tenders"`
	PickedUpBy *string         `json:"picked_up_by"`
	Revision   json.RawMessage `json:"revision"`
}

type tenderRequest struct {
	Method      string          `json:"method"`
	AmountCents json.RawMessage `json:"amount_cents"`
	Reference   *string         `json:"reference"`
	TokenID     *string         `json:"token_id"`
}

func parseTenders(v *httpx.Validator, prefix string, tenders []tenderRequest) []TenderIn {
	out := make([]TenderIn, 0, len(tenders))
	for i := range tenders {
		t := &tenders[i]
		field := func(name string) string { return fmt.Sprintf("%s[%d].%s", prefix, i, name) }
		in := TenderIn{}
		switch t.Method {
		case "cash":
			in.Method = TenderCash
		case "check":
			in.Method = TenderCheck
		case "card":
			in.Method = TenderCard
		case "account":
			in.Method = TenderAccount
		default:
			v.Check(false, field("method"), "must be cash, check, card or account")
		}
		if n, ok := v.Int(field("amount_cents"), t.AmountCents, true); ok {
			v.Check(n > 0, field("amount_cents"), "must be greater than zero")
			in.AmountCents = httpx.Cents(n)
		}
		if t.Reference != nil {
			in.Reference = *t.Reference
		}
		if t.TokenID != nil {
			in.TokenID = *t.TokenID
		}
		out = append(out, in)
	}
	return out
}

func (r *completeSaleRequest) parse() ([]TenderIn, string, *int64, error) {
	v := &httpx.Validator{}
	if len(r.Tenders) == 0 {
		v.Check(false, "tenders", "name at least one tender")
	}
	tenders := parseTenders(v, "tenders", r.Tenders)
	pickedUpBy := ""
	if r.PickedUpBy != nil {
		pickedUpBy = *r.PickedUpBy
		if pickedUpBy != "" {
			v.Check(len(pickedUpBy) <= 200, "picked_up_by", "must be at most 200 characters")
		}
	}
	var revision *int64
	if !isAbsentRaw(r.Revision) {
		if n, ok := v.Int("revision", r.Revision, true); ok {
			v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
			revision = &n
		}
	}
	if err := v.Err(); err != nil {
		return nil, "", nil, err
	}
	return tenders, pickedUpBy, revision, nil
}

// voidSaleRequest is POST /pos/transactions/{id}/void.
type voidSaleRequest struct {
	Reason   *string         `json:"reason"`
	Revision json.RawMessage `json:"revision"`
}

func (r *voidSaleRequest) parse() (string, *int64, error) {
	v := &httpx.Validator{}
	reason := ""
	if r.Reason != nil {
		reason = *r.Reason
	}
	v.Required("reason", reason)
	var revision *int64
	if !isAbsentRaw(r.Revision) {
		if n, ok := v.Int("revision", r.Revision, true); ok {
			v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
			revision = &n
		}
	}
	if err := v.Err(); err != nil {
		return "", nil, err
	}
	return reason, revision, nil
}

// returnRequest is POST /pos/returns.
type returnRequest struct {
	RegisterID   *string             `json:"register_id"`
	OriginalSale *string             `json:"original_sale_id"`
	CustomerID   *string             `json:"customer_id"`
	RefundMethod string              `json:"refund_method"`
	Reason       *string             `json:"reason"`
	GatewayTxID  *string             `json:"gateway_tx_id"`
	Lines        []returnLineRequest `json:"lines"`
}

type returnLineRequest struct {
	ProductID   *string         `json:"product_id"`
	LineID      *string         `json:"line_id"`
	Description *string         `json:"description"`
	Quantity    json.RawMessage `json:"quantity"`
	UnitPrice   json.RawMessage `json:"unit_price_ten_thousandths"`
	Restock     *bool           `json:"restock"`
}

func (r *returnRequest) parse() (*ReturnIn, error) {
	v := &httpx.Validator{}
	out := &ReturnIn{}
	if r.RegisterID != nil {
		out.RegisterID = *r.RegisterID
	}
	if out.RegisterID == "" {
		out.RegisterID = "REG-01"
	}
	if id, ok := v.UUID("original_sale_id", r.OriginalSale, false); ok {
		out.OriginalSaleID = &id
	}
	if id, ok := v.UUID("customer_id", r.CustomerID, false); ok {
		out.CustomerID = &id
	}
	switch r.RefundMethod {
	case "":
		out.RefundMethod = RefundCash
	case "cash":
		out.RefundMethod = RefundCash
	case "card":
		out.RefundMethod = RefundCard
	case "account":
		out.RefundMethod = RefundAccount
	default:
		v.Check(false, "refund_method", "must be cash, card or account")
	}
	if r.Reason != nil {
		out.Reason = *r.Reason
	}
	v.Required("reason", out.Reason)
	if r.GatewayTxID != nil {
		out.GatewayTxID = *r.GatewayTxID
	}
	if len(r.Lines) == 0 {
		v.Check(false, "lines", "name at least one returned line")
	}
	for i := range r.Lines {
		l := &r.Lines[i]
		field := func(name string) string { return fmt.Sprintf("lines[%d].%s", i, name) }
		rl := ReturnLineIn{Restock: true}
		if id, ok := v.UUID(field("product_id"), l.ProductID, true); ok {
			rl.ProductID = &id
		}
		if id, ok := v.UUID(field("line_id"), l.LineID, false); ok {
			rl.SaleLineID = &id
		}
		if l.Description != nil {
			rl.Description = *l.Description
		}
		if q, ok := v.Quantity(field("quantity"), l.Quantity, true); ok {
			v.Check(q > 0, field("quantity"), "must be greater than zero")
			rl.Quantity = q
		}
		if !isAbsentRaw(l.UnitPrice) {
			if n, ok := v.Int(field("unit_price_ten_thousandths"), l.UnitPrice, true); ok {
				v.Check(n >= 0, field("unit_price_ten_thousandths"), "a unit price is never negative")
				p := httpx.Price(n)
				rl.UnitPrice = &p
			}
		}
		if l.Restock != nil {
			rl.Restock = *l.Restock
		}
		out.Lines = append(out.Lines, rl)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// openTillRequest is POST /pos/till/open.
type openTillRequest struct {
	RegisterID   *string         `json:"register_id"`
	OpeningFloat json.RawMessage `json:"opening_float_cents"`
}

func (r *openTillRequest) parse() (string, httpx.Cents, error) {
	v := &httpx.Validator{}
	register := ""
	if r.RegisterID != nil {
		register = *r.RegisterID
	}
	if register == "" {
		register = "REG-01"
	}
	var cents httpx.Cents
	if !isAbsentRaw(r.OpeningFloat) {
		if n, ok := v.Int("opening_float_cents", r.OpeningFloat, true); ok {
			v.Check(n >= 0, "opening_float_cents", "must not be negative")
			cents = httpx.Cents(n)
		}
	}
	if err := v.Err(); err != nil {
		return "", 0, err
	}
	return register, cents, nil
}

// closeTillRequest is POST /pos/till/{id}/close.
type closeTillRequest struct {
	CountedByMethod map[string]json.RawMessage `json:"counted_by_method"`
	Notes           *string                    `json:"notes"`
}

func (r *closeTillRequest) parse() (map[string]int64, string, error) {
	v := &httpx.Validator{}
	counted := make(map[string]int64, len(r.CountedByMethod))
	for m, raw := range r.CountedByMethod {
		if _, ok := ParseTenderMethod(m); !ok {
			v.Check(false, "counted_by_method."+m, "must be cash, check, card or account")
			continue
		}
		if isAbsentRaw(raw) {
			continue
		}
		if n, ok := v.Int("counted_by_method."+m, raw, true); ok {
			v.Check(n >= 0, "counted_by_method."+m, "must not be negative")
			counted[m] = n
		}
	}
	if err := v.Err(); err != nil {
		return nil, "", err
	}
	notes := ""
	if r.Notes != nil {
		notes = *r.Notes
	}
	return counted, notes, nil
}

// offlineSyncRequest is POST /pos/sync: a batch of completed sales captured
// offline, replayed through the same completion path.
type offlineSyncRequest struct {
	BatchID    *string              `json:"batch_id"`
	RegisterID *string              `json:"register_id"`
	Items      []offlineSaleRequest `json:"items"`
}

type offlineSaleRequest struct {
	ClientID        *string               `json:"client_id"`
	CashierID       *string               `json:"cashier_id"`
	CustomerID      *string               `json:"customer_id"`
	Items           []salesdoc.LineRequest `json:"items"`
	Tenders         []tenderRequest       `json:"tenders"`
	ClientCreatedAt json.RawMessage       `json:"client_created_at"`
}

func (r *offlineSyncRequest) parse() (*OfflineSync, error) {
	v := &httpx.Validator{}
	out := &OfflineSync{}
	if r.BatchID != nil {
		out.BatchID = *r.BatchID
	}
	v.Required("batch_id", out.BatchID)
	if r.RegisterID != nil {
		out.RegisterID = *r.RegisterID
	}
	v.Check(len(r.Items) > 0, "items", "cannot be empty")
	for i := range r.Items {
		it := &r.Items[i]
		field := func(name string) string { return fmt.Sprintf("items[%d].%s", i, name) }
		sale := OfflineSale{}
		if id, ok := v.UUID(field("client_id"), it.ClientID, true); ok {
			sale.ClientID = id
		}
		if id, ok := v.UUID(field("cashier_id"), it.CashierID, false); ok {
			sale.CashierID = id
		}
		if id, ok := v.UUID(field("customer_id"), it.CustomerID, false); ok {
			sale.CustomerID = &id
		}
		if !isAbsentRaw(it.ClientCreatedAt) {
			if ts, ok := v.Timestamp(field("client_created_at"), it.ClientCreatedAt, true); ok {
				sale.ClientCreatedAt = ts
			}
		}
		sale.Lines = salesdoc.ParseLines(v, it.Items)
		v.Check(len(it.Tenders) > 0, field("tenders"), "name at least one tender")
		sale.Tenders = parseTenders(v, field("tenders"), it.Tenders)
		out.Items = append(out.Items, sale)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
