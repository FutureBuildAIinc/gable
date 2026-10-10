// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package unit is the unit catalogue of ADR 0006 section 2: one row per
// unit the dealer uses, the seeded units locked, dealers adding their own.
// The unit set service (the product module) and the quote module read the
// catalogue through the units package's arithmetic; this module owns the
// rows themselves.
package unit

import (
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// Dimension is a unit's dimension (ADR 0006 2.2): the storage vocabulary is
// uppercase, the wire lowercase.
type Dimension string

const (
	Count        Dimension = "COUNT"
	Length       Dimension = "LENGTH"
	Area         Dimension = "AREA"
	Volume       Dimension = "VOLUME"
	Weight       Dimension = "WEIGHT"
	BoardMeasure Dimension = "BOARD_MEASURE"
)

// dimensionNames is the wire vocabulary.
var dimensionNames = map[string]Dimension{
	"count": Count, "length": Length, "area": Area,
	"volume": Volume, "weight": Weight, "board_measure": BoardMeasure,
}

// MarshalText writes the lowercase wire name (ADR 0001 section 6).
func (d Dimension) MarshalText() ([]byte, error) { return []byte(strings.ToLower(string(d))), nil }

// ParseDimension maps a lowercase wire name to its dimension. Any other
// spelling is not a dimension.
func ParseDimension(name string) (Dimension, bool) {
	d, ok := dimensionNames[name]
	return d, ok
}

// Unit is a catalogue row on the wire (ADR 0006 sections 2.1 and 6): the
// code every unit column holds, the dimension, the standard size as two
// nullable decimal strings (both or neither), the system lock and the
// active flag. No unit is ever deleted; the write routes offer
// deactivation only.
type Unit struct {
	Code       string          `json:"code"`
	Name       string          `json:"name"`
	Dimension  Dimension       `json:"dimension"`
	StdUnitQty *httpx.Quantity `json:"std_unit_qty"`
	StdRefQty  *httpx.Quantity `json:"std_ref_qty"`
	IsSystem   bool            `json:"is_system"`
	IsActive   bool            `json:"is_active"`
	Revision   int64           `json:"revision"`
	CreatedAt  httpx.Timestamp `json:"created_at"`
	UpdatedAt  httpx.Timestamp `json:"updated_at"`
}
