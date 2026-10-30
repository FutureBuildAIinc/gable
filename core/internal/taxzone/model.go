// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package taxzone is Gable's own tax zone tables (CP-10): a zone maps a place
// of receipt (a postal code, a postal code range, or a Canadian province) to
// a jurisdiction and its rate parts. The parts are stored, never the combined
// rate, and every row carries its effective window so a rate that changes is
// closed and re opened, never overwritten.
package taxzone

import (
	"github.com/google/uuid"
)

// ZoneKind says how a zone's boundary is read: a postal code zone matches the
// ship to's postal code; a province zone matches the ship to's region.
const (
	KindPostal   = "POSTAL"
	KindProvince = "PROVINCE"
)

// Sourcing says which sales a zone answers: an origin zone answers pickups
// from its own area, a destination zone answers deliveries into it.
const (
	SourcingOrigin      = "ORIGIN"
	SourcingDestination = "DESTINATION"
	SourcingBoth        = "BOTH"
)

// PartName is one rate part: the state, county, city and district parts of a
// US stack, or the goods tax and provincial sales tax parts of a Canadian one.
const (
	PartState           = "STATE"
	PartCounty          = "COUNTY"
	PartCity            = "CITY"
	PartDistrict        = "DISTRICT"
	PartGoodsTax        = "GOODS_TAX"
	PartProvincialSales = "PROVINCIAL_SALES_TAX"
)

// RatePart is one part of a zone's rate with its own effective window.
type RatePart struct {
	Name          string `json:"name"`
	Rate          string `json:"rate"` // the decimal string "0.060000"
	EffectiveFrom string `json:"effective_from"`
	EffectiveTo   string `json:"effective_to,omitempty"`
}

// Boundary is the postal code, or postal code range, a zone covers. Low
// equals high for an exact code; the extension pair narrows a range to the
// plus four when the zone knows it.
type Boundary struct {
	Country       string `json:"country"`
	PostalLow     string `json:"postal_code_low"`
	PostalHigh    string `json:"postal_code_high"`
	ExtensionLow  string `json:"extension_low,omitempty"`
	ExtensionHigh string `json:"extension_high,omitempty"`
}

// Zone is one jurisdiction's zone: what it covers, who collects, from when to
// when, and where the rate came from.
type Zone struct {
	ID               uuid.UUID  `json:"id"`
	Country          string     `json:"country"`
	JurisdictionCode string     `json:"jurisdiction_code"`
	JurisdictionType string     `json:"jurisdiction_type"` // STA, CTY, CIT, STJ, PROV
	JurisdictionName string     `json:"jurisdiction_name"`
	StateCode        string     `json:"state_code,omitempty"`
	CountyCode       string     `json:"county_code,omitempty"`
	PlaceCode        string     `json:"place_code,omitempty"`
	ProvinceCode     string     `json:"province_code,omitempty"`
	ZoneKind         string     `json:"zone_kind"`
	Sourcing         string     `json:"sourcing"`
	EffectiveFrom    string     `json:"effective_from"`
	EffectiveTo      *string    `json:"effective_to"`
	DataSource       string     `json:"data_source"`
	DataVersion      string     `json:"data_version"`
	Rates            []RatePart `json:"rates"`
	Boundaries       []Boundary `json:"boundaries"`
}

// CombinedRate is the sum of the zone's rate parts rendered as the decimal
// string the resolver takes. It is computed, never stored, so the parts and
// the combined number cannot drift apart.
func (z *Zone) CombinedRate() (string, bool) {
	sum := int64(0)
	any := false
	for i := range z.Rates {
		scaled, ok, err := parseRate(z.Rates[i].Rate)
		if err != nil || !ok {
			return "", false
		}
		sum += scaled
		any = true
	}
	if !any {
		return "", false
	}
	return renderRate(sum), true
}

// Part is one entry of the snapshot a document freezes at the sale.
type Part struct {
	Name string `json:"name"`
	Rate string `json:"rate"`
}

// Snapshot is what a document records when a zone answered: the zone id, the
// parts, the combined rate and where the data came from. The snapshot matters
// more than the id, because the zone row can change later and the sale's rate
// may not.
type Snapshot struct {
	ZoneID       uuid.UUID `json:"zone_id"`
	Parts        []Part    `json:"parts"`
	CombinedRate string    `json:"combined_rate"`
	DataSource   string    `json:"data_source"`
}
