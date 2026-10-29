// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer

import (
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// AlertKind is what an alert is: a note to read, or a warning to act on.
type AlertKind string

const (
	// AlertNote is read and carried on with.
	AlertNote AlertKind = "note"
	// AlertWarning is read and acted on; it may ask to be acknowledged.
	AlertWarning AlertKind = "warning"
)

// alertKindNames is the wire vocabulary.
var alertKindNames = []string{string(AlertNote), string(AlertWarning)}

// Alert is a message the office left on a customer for the counter and the
// order desk. It is a part of the customer: added, seen and ended on the
// customer's own routes.
//
// ShowAtCounter puts it in the counter's customer panel, ShowAtOrders puts it
// on the order page. AcknowledgeRequired asks the counter to read it and say
// so before the sale is taken; only a warning may ask, and the table refuses
// the flag on a note.
//
// An alert is ended, not deleted: EndedAt and EndedBy keep the record of who
// called it off. The revision moves when it is ended, so an end of an alert
// that has already been ended is refused as stale.
type Alert struct {
	ID                  uuid.UUID        `json:"id"`
	CustomerID          uuid.UUID        `json:"customer_id"`
	Kind                AlertKind        `json:"kind"`
	Text                string           `json:"text"`
	ShowAtCounter       bool             `json:"show_at_counter"`
	ShowAtOrders        bool             `json:"show_at_orders"`
	AcknowledgeRequired bool             `json:"acknowledge_required"`
	CreatedBy           string           `json:"created_by"`
	CreatedAt           httpx.Timestamp  `json:"created_at"`
	EndedBy             *string          `json:"ended_by"`
	EndedAt             *httpx.Timestamp `json:"ended_at"`
	Revision            int64            `json:"revision"`
}

// AlertDraft is a validated alert to add.
type AlertDraft struct {
	Kind                AlertKind
	Text                string
	ShowAtCounter       bool
	ShowAtOrders        bool
	AcknowledgeRequired bool
}

// alertTextMax is the longest alert text, in characters.
const alertTextMax = 500

// AlertRequest is the body of POST /api/v1/customers/{id}/alerts.
type AlertRequest struct {
	Kind                *string `json:"kind"`
	Text                *string `json:"text"`
	ShowAtCounter       *bool   `json:"show_at_counter"`
	ShowAtOrders        *bool   `json:"show_at_orders"`
	AcknowledgeRequired *bool   `json:"acknowledge_required"`
}

// Parse reads the body into a draft. The flags are false when left out: an
// alert speaks where it is told to, not everywhere.
func (req *AlertRequest) Parse() (*AlertDraft, error) {
	v := &httpx.Validator{}
	d := &AlertDraft{}
	switch {
	case req.Kind == nil:
		v.Check(false, "kind", "is required")
	default:
		kind := strings.TrimSpace(*req.Kind)
		v.Enum("kind", kind, alertKindNames...)
		if kind == string(AlertNote) || kind == string(AlertWarning) {
			d.Kind = AlertKind(kind)
		}
	}
	d.Text = reqText(v, "text", req.Text, alertTextMax)
	if req.ShowAtCounter != nil {
		d.ShowAtCounter = *req.ShowAtCounter
	}
	if req.ShowAtOrders != nil {
		d.ShowAtOrders = *req.ShowAtOrders
	}
	if req.AcknowledgeRequired != nil {
		d.AcknowledgeRequired = *req.AcknowledgeRequired
	}
	// Only a warning may ask to be acknowledged: a note is read and carried
	// on with, so asking for a signature on it would be a mistake.
	if d.AcknowledgeRequired && d.Kind != AlertWarning {
		v.Check(false, "acknowledge_required", "is only asked of a warning: a note is read and carried on with")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// EndAlertRequest is not a body: ending an alert changes nothing about it
// but that it is over, so the end carries no fields of its own. Its
// precondition is the alert's revision, sent as If-Match.
