// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package location

import (
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// LocationType represents the hierarchy level of a location, stored
// UPPERCASE, lowercase on the wire (ADR 0001 section 6).
type LocationType string

const (
	LocTypeBranch LocationType = "BRANCH"
	LocTypeZone   LocationType = "ZONE"
	LocTypeAisle  LocationType = "AISLE"
	LocTypeRack   LocationType = "RACK"
	LocTypeShelf  LocationType = "SHELF"
	LocTypeBin    LocationType = "BIN"
	LocTypeYard   LocationType = "YARD"
)

// MarshalText writes the lowercase wire name.
func (t LocationType) MarshalText() ([]byte, error) {
	return []byte(strings.ToLower(string(t))), nil
}

// ParseLocationType maps a lowercase wire name to its type. Any other
// spelling, the legacy uppercase included, is not a type.
func ParseLocationType(name string) (LocationType, bool) {
	for _, t := range []LocationType{LocTypeBranch, LocTypeZone, LocTypeAisle, LocTypeRack, LocTypeShelf, LocTypeBin, LocTypeYard} {
		if strings.ToLower(string(t)) == name {
			return t, true
		}
	}
	return "", false
}

// Location represents a node in the location hierarchy. A node with
// Type=BRANCH and ParentID=nil is a top-level branch; every other row has a
// parent and a denormalized BranchID (kept up to date by a DB trigger).
// Optional fields are present with null, never omitted (ADR 0001 section 12).
type Location struct {
	ID          uuid.UUID    `json:"id"`
	ParentID    *uuid.UUID   `json:"parent_id"`
	Path        string       `json:"path"`
	Type        LocationType `json:"type"`
	Code        string       `json:"code"`
	Description *string      `json:"description"`

	// Branch-only metadata. Non-branch rows leave these fields null.
	Name                *string    `json:"name"`
	Address             *string    `json:"address"`
	City                *string    `json:"city"`
	State               *string    `json:"state"`
	Zip                 *string    `json:"zip"`
	Phone               *string    `json:"phone"`
	TaxJurisdictionCode *string    `json:"tax_jurisdiction_code"`
	DefaultTaxRate      *float64   `json:"default_tax_rate"`
	Timezone            *string    `json:"timezone"`
	Active              bool       `json:"active"`
	BranchID            *uuid.UUID `json:"branch_id"`

	Revision  int64           `json:"revision"`
	CreatedAt httpx.Timestamp `json:"created_at"`
	UpdatedAt httpx.Timestamp `json:"updated_at"`
}

// IsBranch reports whether this row is a top-level branch.
func (l *Location) IsBranch() bool {
	return l.Type == LocTypeBranch
}

// BranchSummary is the lightweight projection used by selectors and
// /me/branches responses.
type BranchSummary struct {
	ID       uuid.UUID `json:"id"`
	Code     string    `json:"code"`
	Name     string    `json:"name"`
	Active   bool      `json:"active"`
	IsHome   bool      `json:"is_home,omitempty"` // populated by /me/branches
	Timezone string    `json:"timezone,omitempty"`
}

// UserLocation describes a user's grant to a single branch.
type UserLocation struct {
	UserSub   string    `json:"user_sub"`
	BranchID  uuid.UUID `json:"branch_id"`
	IsHome    bool      `json:"is_home"`
	GrantedAt time.Time `json:"granted_at"`
	GrantedBy string    `json:"granted_by,omitempty"`
}
