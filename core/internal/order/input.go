// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/google/uuid"
)

// Request is the JSON body of POST /orders and PUT /orders/{id}. Header
// fields that carry a number or an identifier decode loosely and are parsed
// by Parse; the lines are parsed by salesdoc.ParseLines, the one line parse
// every sales document shares (ADR 0005 section 2.7).
type Request struct {
	BranchID              *string                `json:"branch_id"`
	CustomerID            *string                `json:"customer_id"`
	QuoteID               *string                `json:"quote_id"`
	JobID                 *string                `json:"job_id"`
	DeliveryType          *string                `json:"delivery_type"`
	ShipToID              *string                `json:"ship_to_id"`
	CustomerPO            *string                `json:"customer_po"`
	OrderedByContactID    *string                `json:"ordered_by_contact_id"`
	SalespersonID         *string                `json:"salesperson_id"`
	ScheduledDeliveryDate *string                `json:"scheduled_delivery_date"`
	Revision              json.RawMessage        `json:"revision"`
	Lines                 []salesdoc.LineRequest `json:"lines"`
}

// Draft is a validated request ready for the service to price.
type Draft struct {
	BranchID              *uuid.UUID
	CustomerID            uuid.UUID
	JobID                 *uuid.UUID
	DeliveryType          DeliveryType
	ShipToID              *uuid.UUID
	CustomerPO            *string
	OrderedByContactID    *uuid.UUID
	SalespersonID         *uuid.UUID
	ScheduledDeliveryDate *string
	Revision              *int64
	Lines                 []salesdoc.ParsedLine
}

// datePattern is a business date (ADR 0001 section 12): YYYY-MM-DD.
var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// Parse validates a create request in one pass.
func (req *Request) Parse() (*Draft, error) { return req.parse(false) }

// ParseUpdate is Parse for PUT /orders/{id}: a PUT replaces the header
// fields and the lines and applies nothing else, so a field it does not
// apply is a 400 naming it, never a silent drop.
func (req *Request) ParseUpdate() (*Draft, error) { return req.parse(true) }

func (req *Request) parse(update bool) (*Draft, error) {
	v := &httpx.Validator{}
	d := &Draft{DeliveryType: DeliveryDelivery}

	if update {
		for _, f := range []struct {
			name    string
			present bool
		}{
			{"branch_id", req.BranchID != nil},
			{"quote_id", req.QuoteID != nil},
			{"currency", false},
		} {
			v.Check(!f.present, f.name, "cannot be changed by an edit: it is fixed when the order is created")
		}
	} else {
		v.Check(isAbsentRaw(req.Revision), "revision", "is not accepted on create: a new order has no revision to precondition on")
	}

	if id, ok := v.UUID("branch_id", req.BranchID, false); ok && !update {
		d.BranchID = &id
	}
	if id, ok := v.UUID("customer_id", req.CustomerID, true); ok {
		d.CustomerID = id
	}
	// A quote's order is made by converting the quote (ADR 0005 section 5.8);
	// a quote_id here would link a manual order to a quote and then block the
	// real convert with already_converted.
	if !update {
		v.Check(req.QuoteID == nil, "quote_id", "is not accepted here: convert the quote with POST /api/v1/quotes/{id}/convert")
	}
	if id, ok := v.UUID("job_id", req.JobID, false); ok {
		d.JobID = &id
	}
	if id, ok := v.UUID("ship_to_id", req.ShipToID, false); ok {
		d.ShipToID = &id
	}
	if id, ok := v.UUID("ordered_by_contact_id", req.OrderedByContactID, false); ok {
		d.OrderedByContactID = &id
	}
	if id, ok := v.UUID("salesperson_id", req.SalespersonID, false); ok {
		d.SalespersonID = &id
	}
	if req.CustomerPO != nil {
		po := strings.TrimSpace(*req.CustomerPO)
		if po != "" {
			v.Check(len(po) <= 100, "customer_po", "must be at most 100 characters")
			d.CustomerPO = &po
		}
	}
	if req.ScheduledDeliveryDate != nil {
		date := strings.TrimSpace(*req.ScheduledDeliveryDate)
		if date != "" {
			v.Check(datePattern.MatchString(date), "scheduled_delivery_date", "must be a date as YYYY-MM-DD")
			d.ScheduledDeliveryDate = &date
		}
	}
	if req.DeliveryType != nil {
		switch dt, ok := ParseDeliveryType(*req.DeliveryType); {
		case ok:
			d.DeliveryType = dt
		default:
			v.Check(false, "delivery_type", "must be one of: pickup, delivery")
		}
	}
	if update {
		if n, ok := v.Int("revision", req.Revision, false); ok {
			v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
			d.Revision = &n
		}
	}

	d.Lines = salesdoc.ParseLines(v, req.Lines)

	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

func isAbsentRaw(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return t == "" || t == "null"
}

// TransitionRequest is the body of POST /orders/{id}/transitions.
type TransitionRequest struct {
	To       *string         `json:"to"`
	Revision json.RawMessage `json:"revision"`
	// The transition's own fields (ADR 0005 5.2): a cancel's or a close
	// short's reason, a manual hold's note.
	Reason   *string `json:"reason"`
	HoldNote *string `json:"hold_note"`
}
