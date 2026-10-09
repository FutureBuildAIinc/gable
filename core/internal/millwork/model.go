// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package millwork

import (
	"encoding/json"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Column bounds the option catalog's own columns impose on its fields.
const (
	maxCategory = 50
	maxName     = 100
)

// Option is one catalog option, the wire shape of /api/v1/millwork/options.
// The price adjustment is integer cents (ADR 0001 section 7): a positive
// option adds to the configured price, a negative one discounts it, and no
// float ever carries it. attributes is whatever JSON the dealer sent, stored
// as sent and present as null when the create carried none.
type Option struct {
	ID                   uuid.UUID       `json:"id"`
	Category             string          `json:"category"`
	Name                 string          `json:"name"`
	PriceAdjustmentCents int64           `json:"price_adjustment_cents"`
	Attributes           json.RawMessage `json:"attributes"`
	Revision             int64           `json:"revision"`
	CreatedAt            httpx.Timestamp `json:"created_at"`
	UpdatedAt            httpx.Timestamp `json:"updated_at"`
}

// ErrNotFound reports that a read or write named an option that is not
// there; the handler answers 404.
var ErrNotFound = errNotFound{}

type errNotFound struct{}

func (errNotFound) Error() string { return "millwork option not found" }
