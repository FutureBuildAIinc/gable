// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package taxzone

import (
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// createRequest is POST /api/v1/tax/zones.
type createRequest struct {
	Country          *string      `json:"country"`
	JurisdictionCode *string      `json:"jurisdiction_code"`
	JurisdictionType *string      `json:"jurisdiction_type"`
	JurisdictionName *string      `json:"jurisdiction_name"`
	StateCode        *string      `json:"state_code"`
	CountyCode       *string      `json:"county_code"`
	PlaceCode        *string      `json:"place_code"`
	ProvinceCode     *string      `json:"province_code"`
	ZoneKind         *string      `json:"zone_kind"`
	Sourcing         *string      `json:"sourcing"`
	EffectiveFrom    *string      `json:"effective_from"`
	DataSource       *string      `json:"data_source"`
	DataVersion      *string      `json:"data_version"`
	Rates            []rateIn     `json:"rates"`
	Boundaries       []boundaryIn `json:"boundaries"`
}

type rateIn struct {
	Name          string  `json:"name"`
	Rate          string  `json:"rate"`
	EffectiveFrom string  `json:"effective_from"`
	EffectiveTo   *string `json:"effective_to"`
}

type boundaryIn struct {
	Country       string `json:"country"`
	PostalLow     string `json:"postal_code_low"`
	PostalHigh    string `json:"postal_code_high"`
	ExtensionLow  string `json:"extension_low"`
	ExtensionHigh string `json:"extension_high"`
}

type closeRequest struct {
	EffectiveTo string `json:"effective_to"`
}

func (r *createRequest) parse() (*Draft, error) {
	v := &httpx.Validator{}
	d := &Draft{}
	text := func(p *string) string {
		if p == nil {
			return ""
		}
		return strings.TrimSpace(*p)
	}
	d.Country = strings.ToUpper(text(r.Country))
	v.Check(len(d.Country) == 2, "country", "is required, as a two letter code")
	d.JurisdictionCode = text(r.JurisdictionCode)
	v.Required("jurisdiction_code", d.JurisdictionCode)
	d.JurisdictionType = strings.ToUpper(text(r.JurisdictionType))
	v.Check(contains(jurisdictionTypes, d.JurisdictionType), "jurisdiction_type",
		"must be STA, CTY, CIT, STJ or PROV")
	d.JurisdictionName = text(r.JurisdictionName)
	d.StateCode = text(r.StateCode)
	d.CountyCode = text(r.CountyCode)
	d.PlaceCode = text(r.PlaceCode)
	d.ProvinceCode = strings.ToUpper(text(r.ProvinceCode))
	d.ZoneKind = strings.ToUpper(text(r.ZoneKind))
	if d.ZoneKind == "" {
		d.ZoneKind = KindPostal
	}
	v.Check(d.ZoneKind == KindPostal || d.ZoneKind == KindProvince, "zone_kind",
		"must be POSTAL or PROVINCE")
	d.Sourcing = strings.ToUpper(text(r.Sourcing))
	if d.Sourcing == "" {
		d.Sourcing = SourcingDestination
	}
	v.Check(contains(sourcingValues, d.Sourcing), "sourcing", "must be ORIGIN, DESTINATION or BOTH")
	from, err := parseDate("effective_from", text(r.EffectiveFrom), true)
	if err != nil {
		return nil, err
	}
	d.EffectiveFrom = from
	d.DataSource = text(r.DataSource)
	d.DataVersion = text(r.DataVersion)
	if d.ZoneKind == KindProvince {
		v.Check(d.ProvinceCode != "", "province_code", "is required for a province zone")
	}
	for i := range r.Rates {
		in := &r.Rates[i]
		name := strings.ToUpper(strings.TrimSpace(in.Name))
		v.Check(contains(partNames, name), "rates.name",
			"must be STATE, COUNTY, CITY, DISTRICT, GOODS_TAX or PROVINCIAL_SALES_TAX")
		scaled, ok, perr := parseRate(in.Rate)
		if perr != nil || !ok {
			v.Check(false, "rates.rate", "must be a decimal rate between 0 and 1, such as 0.06")
		} else {
			v.Check(scaled >= 0, "rates.rate", "must not be negative")
		}
		pfrom, err := parseDate("rates.effective_from", in.EffectiveFrom, true)
		if err != nil {
			return nil, err
		}
		part := RatePart{Name: name, Rate: strings.TrimSpace(in.Rate), EffectiveFrom: pfrom}
		if in.EffectiveTo != nil && strings.TrimSpace(*in.EffectiveTo) != "" {
			pto, err := parseDate("rates.effective_to", *in.EffectiveTo, true)
			if err != nil {
				return nil, err
			}
			part.EffectiveTo = pto
		}
		d.Rates = append(d.Rates, part)
	}
	v.Check(len(d.Rates) > 0, "rates", "name at least one rate part")
	for i := range r.Boundaries {
		in := &r.Boundaries[i]
		low := strings.TrimSpace(in.PostalLow)
		v.Check(low != "", "boundaries.postal_code_low", "is required")
		high := strings.TrimSpace(in.PostalHigh)
		if high == "" {
			high = low
		}
		country := strings.ToUpper(strings.TrimSpace(in.Country))
		if country == "" {
			country = d.Country
		}
		d.Boundaries = append(d.Boundaries, Boundary{
			Country: country, PostalLow: low, PostalHigh: high,
			ExtensionLow: strings.TrimSpace(in.ExtensionLow), ExtensionHigh: strings.TrimSpace(in.ExtensionHigh),
		})
	}
	if d.ZoneKind == KindPostal {
		v.Check(len(d.Boundaries) > 0, "boundaries",
			"a postal code zone names at least one postal code or range")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}
