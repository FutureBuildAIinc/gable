// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package tint is the paint tint catalogue of row CP-16: the dealer's own
// bases, colorants, colours and per size formulas, the rescale between
// container sizes, and the saved colours per customer and job. No dispenser
// or colour matching device link; the catalogue holds only data the dealer
// owns or creates.
package tint

import (
	"errors"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Timestamp is the wire timestamp (RFC 3339 UTC, the platform's own format)
// with the database scan beside it, so a row reads in one pass.
type Timestamp struct{ time.Time }

// Scan reads a timestamptz.
func (t *Timestamp) Scan(v any) error {
	tv, ok := v.(time.Time)
	if !ok {
		return errors.New("a timestamp did not come from the database as one")
	}
	t.Time = tv
	return nil
}

// MarshalJSON writes the platform's wire format.
func (t Timestamp) MarshalJSON() ([]byte, error) { return httpx.TimestampOf(t.Time).MarshalJSON() }

// System is one manufacturer's tinting system: the dispense denominator that
// makes its colourant amount columns meaningful.
type System struct {
	ID              uuid.UUID `json:"id"`
	Code            string    `json:"code"`
	Name            string    `json:"name"`
	Manufacturer    string    `json:"manufacturer"`
	UnitDenominator int       `json:"unit_denominator"`
	UnitLabel       string    `json:"unit_label"`
	CreatedAt       Timestamp `json:"created_at"`
}

// Base is a base paint of a system, optionally the stocked products row the
// counter sells as the base can.
type Base struct {
	ID                   uuid.UUID  `json:"id"`
	TintSystemID         uuid.UUID  `json:"tint_system_id"`
	Code                 string     `json:"code"`
	Name                 string     `json:"name"`
	ManufacturerBaseCode string     `json:"manufacturer_base_code"`
	ProductID            *uuid.UUID `json:"product_id"`
	FillVolumeOZ         int        `json:"fill_volume_oz"`
	IsDeepest            bool       `json:"is_deepest"`
	CreatedAt            Timestamp  `json:"created_at"`
}

// Colorant is one colourant of a system.
type Colorant struct {
	ID               uuid.UUID `json:"id"`
	TintSystemID     uuid.UUID `json:"tint_system_id"`
	Code             string    `json:"code"`
	Name             string    `json:"name"`
	ManufacturerCode string    `json:"manufacturer_code"`
	ManufacturerUPC  *string   `json:"manufacturer_upc"`
	CreatedAt        Timestamp `json:"created_at"`
}

// Color is one colour: a manufacturer code when the colour came from a book,
// none when the store matched it. The code is an attribute, never a key.
type Color struct {
	ID               uuid.UUID  `json:"id"`
	TintSystemID     uuid.UUID  `json:"tint_system_id"`
	ManufacturerCode *string    `json:"manufacturer_code"`
	Name             string     `json:"name"`
	IsCustom         bool       `json:"is_custom"`
	CustomerID       *uuid.UUID `json:"customer_id"`
	Note             string     `json:"note"`
	CreatedAt        Timestamp  `json:"created_at"`
}

// Formula is one formula of a colour in a base at a container size, with its
// components in guide order.
type Formula struct {
	ID                uuid.UUID   `json:"id"`
	ColorID           uuid.UUID   `json:"color_id"`
	BaseID            uuid.UUID   `json:"base_id"`
	ProductID         *uuid.UUID  `json:"product_id"`
	ContainerSizeCode string      `json:"container_size_code"`
	ContainerVolumeOZ int         `json:"container_volume_oz"`
	TotalColorantOZ   string      `json:"total_colorant_oz"`
	Source            string      `json:"source"`
	Components        []Component `json:"components"`
	CreatedAt         Timestamp   `json:"created_at"`
}

// SavedColor is the store's own record of a colour a customer bought: the
// free text job note the counter writes on the spot, and the colour it
// names, so a repeat order gets the same formula.
type SavedColor struct {
	ID         uuid.UUID  `json:"id"`
	CustomerID uuid.UUID  `json:"customer_id"`
	ColorID    uuid.UUID  `json:"color_id"`
	Name       string     `json:"name"`
	JobNote    string     `json:"job_note"`
	Note       string     `json:"note"`
	ProjectID  *uuid.UUID `json:"project_id"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  Timestamp  `json:"created_at"`
}
