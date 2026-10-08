// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package configurator

import (
	"encoding/json"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Rule defines a dependency constraint between product attributes. For
// example: Species="SYP" allows Treatment="Treatable". The attribute names
// and values are the seeded rule matrix's own vocabulary (Species, Grade,
// Treatment, Dimensions): data the dealer's catalog owns, not a closed
// vocabulary the product owns, so they keep their spelling as given.
type Rule struct {
	ID             uuid.UUID       `json:"id"`
	AttributeType  string          `json:"attribute_type"`
	AttributeValue string          `json:"attribute_value"`
	DependsOnType  string          `json:"depends_on_type"`
	DependsOnValue string          `json:"depends_on_value"`
	IsAllowed      bool            `json:"is_allowed"`
	ErrorMessage   *string         `json:"error_message"`
	CreatedAt      httpx.Timestamp `json:"created_at"`
	UpdatedAt      httpx.Timestamp `json:"updated_at"`
}

// Preset is a pre-built product template.
type Preset struct {
	ID          uuid.UUID       `json:"id"`
	Name        string          `json:"name"`
	Description *string         `json:"description"`
	ProductType string          `json:"product_type"`
	Config      Config          `json:"config"`
	IsActive    bool            `json:"is_active"`
	CreatedAt   httpx.Timestamp `json:"created_at"`
	UpdatedAt   httpx.Timestamp `json:"updated_at"`
}

// Config is a preset's attribute selections, carried as the JSON the dealer
// stored.
type Config = json.RawMessage

// ValidationConflict describes a single rule violation.
type ValidationConflict struct {
	AttributeType  string `json:"attribute_type"`
	AttributeValue string `json:"attribute_value"`
	DependsOnType  string `json:"depends_on_type"`
	DependsOnValue string `json:"depends_on_value"`
	Message        string `json:"message"`
}

// ValidateResponse contains the validation result. conflicts is always an
// array, never omitted or null (ADR 0001 section 1's rule for collections,
// applied to a computed answer).
type ValidateResponse struct {
	Valid     bool                 `json:"valid"`
	Conflicts []ValidationConflict `json:"conflicts"`
}

// BuildSKUResponse contains the generated non-stock SKU.
type BuildSKUResponse struct {
	SKU         string `json:"sku"`
	Description string `json:"description"`
}

// AvailableOption is a single allowed value with its verdict.
type AvailableOption struct {
	Value   string  `json:"value"`
	Allowed bool    `json:"allowed"`
	Message *string `json:"message"`
}
