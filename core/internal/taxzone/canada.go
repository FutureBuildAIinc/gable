// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package taxzone

// CP-14 A1: a Canadian document charges, freezes and prints its tax part by
// part. BC's Provincial Sales Tax Regulation s. 85(1) requires PST as a
// separate item on every receipt, bill or invoice, and every Canadian
// document prints GST as its own line (item 363). So a Canadian branch's
// document always has parts: a pickup takes the branch's own zone, a
// delivery its destination's zone, and a document with no zone answering is
// refused rather than printed wrong. A US branch never comes here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// CountryCanada is the country whose documents carry tax parts.
const CountryCanada = "CA"

// BlockerPartsNotConfigured is the refusal's blocker code.
const BlockerPartsNotConfigured = "tax_parts_not_configured"

// Place is a place of receipt: a country, a postal code and a region (a
// province for a Canadian place).
type Place struct {
	Country string
	Postal  string
	Region  string
}

// provinceNames are the names a clerk reads in a refusal.
var provinceNames = map[string]string{
	"AB": "Alberta", "BC": "British Columbia", "MB": "Manitoba", "NB": "New Brunswick",
	"NL": "Newfoundland and Labrador", "NS": "Nova Scotia", "NT": "the Northwest Territories",
	"NU": "Nunavut", "ON": "Ontario", "PE": "Prince Edward Island", "QC": "Quebec",
	"SK": "Saskatchewan", "YT": "Yukon",
}

// ProvinceName names a province by its code ("BC" is British Columbia); a
// code it does not know is answered as typed, and an empty one as "this
// address".
func ProvinceName(code string) string {
	c := strings.ToUpper(strings.TrimSpace(code))
	if n, ok := provinceNames[c]; ok {
		return n
	}
	if c == "" {
		return "this address"
	}
	return c
}

// PartsNotConfiguredError is a Canadian document no zone answers: the
// destination's province for a delivery, the branch's own for a pickup.
type PartsNotConfiguredError struct {
	Province    string
	Destination bool
}

func (e *PartsNotConfiguredError) Error() string {
	return "no tax zone covers " + ProvinceName(e.Province)
}

// ClerkWords is what the clerk reads (item 365): the province, and the two
// ways forward for a delivery.
func (e *PartsNotConfiguredError) ClerkWords() string {
	if e.Destination {
		return "No tax zone covers " + ProvinceName(e.Province) + ": ask an admin to add one, or take the sale as a pickup."
	}
	return "No tax zone covers " + ProvinceName(e.Province) + ", where this branch is: ask an admin to add the province's tax zone before selling from this branch."
}

// HTTP is the refusal on the wire: 409 with the blocker
// tax_parts_not_configured, the same shape as tax_rate_not_configured.
func (e *PartsNotConfiguredError) HTTP() *httpx.Error {
	return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
		Message: e.ClerkWords(),
		Details: []httpx.FieldError{httpx.Blocker(BlockerPartsNotConfigured, e.ClerkWords())}}
}

// AsPartsNotConfigured answers the refusal on the wire when err is one, so
// each document path can turn it into its 409 in one line.
func AsPartsNotConfigured(err error) (*httpx.Error, bool) {
	var e *PartsNotConfiguredError
	if errors.As(err, &e) {
		return e.HTTP(), true
	}
	return nil, false
}

// CanadaInput is one document's facts: its branch and business day, and for
// a delivery that names a ship-to, the ship-to's place and the zone the
// caller's lookup found for it (nil when none).
type CanadaInput struct {
	BranchID    uuid.UUID
	On          time.Time
	ShipTo      *Place
	Destination *Snapshot
}

// CanadaResult is the rule's answer. Canadian false means nothing changes:
// the document takes the US path byte for byte. Otherwise Zone is the zone
// whose parts the document charges (the goods tax part dropped when the
// dealer is not registered for GST/HST), and Destination says whether it
// answers the ship-to (ZONE_RATE) or the branch (BRANCH_RATE).
type CanadaResult struct {
	Canadian    bool
	Zone        *Snapshot
	Destination bool
}

// BranchPlace reads a branch's place: its own country, else the dealer's
// country.default (CP-17a), its postal code and its province.
func BranchPlace(ctx context.Context, db *database.DB, branchID uuid.UUID) (Place, error) {
	var p Place
	err := db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COALESCE(NULLIF(l.country_code, ''),
		                COALESCE((SELECT value FROM system_settings WHERE key = 'country.default'), '')),
		       COALESCE(l.zip, ''), COALESCE(l.state, '')
		FROM locations l WHERE l.id = $1`, branchID).Scan(&p.Country, &p.Postal, &p.Region)
	if err != nil {
		return p, fmt.Errorf("failed to read the branch's place: %w", err)
	}
	p.Country = strings.ToUpper(strings.TrimSpace(p.Country))
	return p, nil
}

// Canada runs the CP-14 rule for one document. A branch outside Canada
// answers Canadian false and reads nothing else. A Canadian delivery naming
// a ship-to takes its destination's zone, and none is refused (a delivery's
// place of supply is the destination, so the branch's zone could charge the
// wrong province's PST); anything else takes the branch's own zone, and none
// is refused.
func Canada(ctx context.Context, db *database.DB, in CanadaInput) (CanadaResult, error) {
	place, err := BranchPlace(ctx, db, in.BranchID)
	if err != nil {
		return CanadaResult{}, err
	}
	if place.Country != CountryCanada {
		return CanadaResult{}, nil
	}
	out := CanadaResult{Canadian: true}
	if in.ShipTo != nil {
		if in.Destination == nil {
			return out, &PartsNotConfiguredError{Province: in.ShipTo.Region, Destination: true}
		}
		out.Zone, out.Destination = in.Destination.clone(), true
	} else {
		zone, err := Lookup(ctx, db, LookupInput{Country: place.Country, Postal: place.Postal, Region: place.Region, On: in.On})
		if errors.Is(err, ErrZoneNotFound) {
			return out, &PartsNotConfiguredError{Province: place.Region}
		}
		if err != nil {
			return out, err
		}
		out.Zone = zone
	}
	if out.Zone.Country == "" {
		out.Zone.Country = CountryCanada
	}
	var notRegistered bool
	if err := db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT COALESCE((SELECT gst_hst_not_registered FROM dealer_profile WHERE id = 1), FALSE)`).Scan(&notRegistered); err != nil {
		return out, fmt.Errorf("failed to read the dealer's GST/HST registration: %w", err)
	}
	if notRegistered {
		out.Zone.dropPart(PartGoodsTax)
	}
	return out, nil
}

func (s *Snapshot) clone() *Snapshot {
	c := *s
	c.Parts = append([]Part(nil), s.Parts...)
	return &c
}

// dropPart removes a part and recomputes the combined rate from what is left.
func (s *Snapshot) dropPart(name string) {
	kept := s.Parts[:0]
	for _, p := range s.Parts {
		if p.Name != name {
			kept = append(kept, p)
		}
	}
	s.Parts = kept
	s.CombinedRate = combinedOf(kept)
}

// Price computes each part's tax on the taxable base, rounded on its own
// (salesdoc.TaxParts), sets each part's cents and answers their sum: the
// document's tax.
func (s *Snapshot) Price(taxable httpx.Cents) (httpx.Cents, error) {
	rates := make([]int64, len(s.Parts))
	for i := range s.Parts {
		r, ok, err := parseRate(s.Parts[i].Rate)
		if err != nil || !ok {
			return 0, fmt.Errorf("the zone's %s part has no readable rate", s.Parts[i].Name)
		}
		rates[i] = r
	}
	var sum httpx.Cents
	for i, c := range salesdoc.TaxParts(taxable, rates) {
		cents := int64(c)
		s.Parts[i].Cents = &cents
		sum += c
	}
	return sum, nil
}

// labels are the printed names of a Canadian part (CP-14 plan section 2.3):
// GOODS_TAX is GST, or HST in a harmonized province; the provincial part is
// PST in BC and SK, RST in MB. Quebec has no entry: its rendering waits for
// the first Quebec dealer (item 362), so it prints the part's name.
var hstProvinces = map[string]bool{"ON": true, "NB": true, "NL": true, "NS": true, "PE": true}

var provincialLabels = map[string]string{"BC": "PST", "SK": "PST", "MB": "RST"}

// Label is a part's printed name on a document of the country and province.
func Label(country, province, part string) string {
	if country != CountryCanada {
		return part
	}
	province = strings.ToUpper(province)
	switch part {
	case PartGoodsTax:
		if province == "QC" {
			return part
		}
		if hstProvinces[province] {
			return "HST"
		}
		return "GST"
	case PartProvincialSales:
		if l, ok := provincialLabels[province]; ok {
			return l
		}
	}
	return part
}

// TaxLine is one printed tax line of a document (the wire's tax_lines).
type TaxLine struct {
	Part        string `json:"part"`
	Label       string `json:"label"`
	RatePercent string `json:"rate_percent"`
	Cents       int64  `json:"cents"`
	Exempt      bool   `json:"exempt"`
}

// Lines reads a document's frozen parts (its tax_rate_parts) into its tax
// lines, GST first then the provincial part. A document with no frozen parts,
// or with parts that carry no cents (a US zone), has none: nil.
func Lines(raw []byte) []TaxLine {
	if len(raw) == 0 {
		return nil
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil || snap.Country != CountryCanada {
		return nil
	}
	out := []TaxLine{}
	for _, want := range []bool{true, false} {
		for _, p := range snap.Parts {
			if (p.Name == PartGoodsTax) != want || p.Cents == nil {
				continue
			}
			pct, err := salesdoc.RateToPercent(p.Rate)
			if err != nil {
				pct = p.Rate
			}
			out = append(out, TaxLine{Part: p.Name, Label: Label(snap.Country, snap.Province, p.Name),
				RatePercent: pct, Cents: *p.Cents})
		}
	}
	return out
}

// Charge prices a resolved document's tax and names the zone it freezes. A
// Canadian document whose zone answered (its branch's own, or a delivery's
// destination) is priced part by part and freezes the zone with each part's
// cents; every other document keeps the one rounding of salesdoc.TaxAt and
// freezes a zone only when a delivery's zone answered (CP-10), exactly as
// before.
func Charge(canadian bool, zone *Snapshot, resolved salesdoc.ResolvedTax, taxable httpx.Cents) (httpx.Cents, *Snapshot, error) {
	if canadian && zone != nil && (resolved.Source == salesdoc.TaxSourceBranchRate || resolved.Source == salesdoc.TaxSourceZoneRate) {
		priced := zone.clone()
		sum, err := priced.Price(taxable)
		if err != nil {
			return 0, nil, err
		}
		return sum, priced, nil
	}
	if resolved.Rate == nil {
		return 0, nil, nil
	}
	scaled, _, err := salesdoc.ParseTaxRate(*resolved.Rate)
	if err != nil {
		return 0, nil, err
	}
	var frozen *Snapshot
	if resolved.Source == salesdoc.TaxSourceZoneRate {
		frozen = zone
	}
	return salesdoc.TaxAt(taxable, scaled), frozen, nil
}

// UnzonedProvince is one province of Canadian ship-tos that no zone covers:
// a delivery there is refused until an admin adds its zone (item 365).
type UnzonedProvince struct {
	Province string `json:"province"`
	Name     string `json:"name"`
	ShipTos  int64  `json:"ship_tos"`
}

// Setup is the Tech Admin tax setup panel's standing warnings (CP-14 A1,
// items 364 and 365): whether the dealer sells from a Canadian branch, whether
// it charges GST with no GST/HST number on file, and the provinces of its
// active Canadian ship-tos that no zone covers on the day.
type Setup struct {
	Canadian            bool              `json:"canadian"`
	GSTHSTNumberMissing bool              `json:"gst_hst_number_missing"`
	UnzonedProvinces    []UnzonedProvince `json:"unzoned_provinces"`
}

// ReadSetup reads the standing warnings on the day. A dealer with no
// Canadian branch has none.
func (r *Repository) ReadSetup(ctx context.Context, on time.Time) (*Setup, error) {
	exec := r.db.GetExecutor(ctx)
	out := &Setup{UnzonedProvinces: []UnzonedProvince{}}
	if err := exec.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM locations l WHERE l.type = 'BRANCH'
		                 AND UPPER(COALESCE(NULLIF(l.country_code, ''),
		                     COALESCE((SELECT value FROM system_settings WHERE key = 'country.default'), ''))) = 'CA'),
		       COALESCE((SELECT NOT gst_hst_not_registered AND gst_hst_number = '' FROM dealer_profile WHERE id = 1), FALSE)`).
		Scan(&out.Canadian, &out.GSTHSTNumberMissing); err != nil {
		return nil, fmt.Errorf("failed to read the tax setup: %w", err)
	}
	if !out.Canadian {
		out.GSTHSTNumberMissing = false
		return out, nil
	}
	// The same matching as Lookup: a live province zone of the region, or a
	// live postal zone whose range holds the code.
	rows, err := exec.Query(ctx, `
		SELECT UPPER(TRIM(COALESCE(s.region, ''))) AS province, COUNT(*)
		FROM customer_ship_tos s
		WHERE s.is_active AND UPPER(TRIM(COALESCE(s.country, ''))) = 'CA'
		  AND NOT EXISTS (
		    SELECT 1 FROM tax_zones z
		    WHERE z.effective_from <= $1::date AND (z.effective_to IS NULL OR z.effective_to >= $1::date)
		      AND EXISTS (SELECT 1 FROM tax_zone_rates r WHERE r.zone_id = z.id AND r.effective_from <= $1::date
		                    AND (r.effective_to IS NULL OR r.effective_to >= $1::date))
		      AND ((z.zone_kind = 'PROVINCE' AND z.country = 'CA' AND z.province_code = TRIM(COALESCE(s.region, '')))
		        OR (z.zone_kind = 'POSTAL' AND EXISTS (SELECT 1 FROM tax_zone_boundaries b
		              WHERE b.zone_id = z.id AND b.country = 'CA'
		                AND b.postal_code_low <= TRIM(COALESCE(s.postal_code, ''))
		                AND (b.postal_code_high = '' OR b.postal_code_high >= TRIM(COALESCE(s.postal_code, '')))))))
		GROUP BY 1 ORDER BY 1`, on.Format("2006-01-02"))
	if err != nil {
		return nil, fmt.Errorf("failed to read the unzoned ship-tos: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var u UnzonedProvince
		if err := rows.Scan(&u.Province, &u.ShipTos); err != nil {
			return nil, fmt.Errorf("failed to read an unzoned province: %w", err)
		}
		u.Name = ProvinceName(u.Province)
		out.UnzonedProvinces = append(out.UnzonedProvinces, u)
	}
	return out, rows.Err()
}
