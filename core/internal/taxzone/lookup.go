// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package taxzone

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// ErrZoneNotFound is not an error a caller surfaces: a place of receipt that
// no zone covers simply falls through to the typed rate and then the branch
// rate, the resolution order CP-10 sets out.
var ErrZoneNotFound = errors.New("no tax zone covers this place of receipt")

// rateScale mirrors salesdoc's rate scale: NUMERIC(9,6) read as a decimal
// string, summed as a scaled integer, never through float64.
const rateScale = 6

// parseRate parses a decimal rate string into its scale 6 integer.
func parseRate(s string) (int64, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, nil
	}
	neg := strings.HasPrefix(s, "-")
	body := strings.TrimPrefix(s, "-")
	intPart, fracPart := body, ""
	if i := strings.IndexByte(body, '.'); i >= 0 {
		intPart, fracPart = body[:i], body[i+1:]
	}
	if intPart == "" {
		return 0, false, errors.New("not a plain decimal")
	}
	var value int64
	for _, d := range intPart {
		if d < '0' || d > '9' {
			return 0, false, errors.New("not a plain decimal")
		}
		value = value*10 + int64(d-'0')
	}
	used := len(fracPart)
	if used > rateScale {
		used = rateScale
	}
	for i, d := range fracPart {
		if d < '0' || d > '9' {
			return 0, false, errors.New("not a plain decimal")
		}
		if i < used {
			value = value*10 + int64(d-'0')
		} else if d != '0' {
			return 0, false, errors.New("precision beyond the rate's scale")
		}
	}
	for i := 0; i < rateScale-used; i++ {
		value *= 10
	}
	if neg {
		return -value, true, nil
	}
	return value, true, nil
}

// renderRate renders a scale 6 integer back as its decimal string.
func renderRate(scaled int64) string {
	neg := ""
	if scaled < 0 {
		neg = "-"
		scaled = -scaled
	}
	whole, frac := scaled/1000000, scaled%1000000
	return fmt.Sprintf("%s%d.%06d", neg, whole, frac)
}

// SplitPostal splits a postal code into its base and its plus four extension
// ("98109-1234" becomes "98109" and "1234"); a code with no extension leaves
// it empty. Canadian codes keep their shape: "V0A 1A0" keeps the space, and
// the ranges that cover it are stored in the same shape.
func SplitPostal(code string) (base, ext string) {
	code = strings.TrimSpace(code)
	if i := strings.IndexByte(code, '-'); i >= 0 {
		return strings.TrimSpace(code[:i]), strings.TrimSpace(code[i+1:])
	}
	return code, ""
}

// LookupInput is a sale's place of receipt: the country and postal code of
// the ship to, its region (a province for a Canadian zone), and the date the
// sale is made (a zone outside its effective window never matches).
type LookupInput struct {
	Country string
	Postal  string
	Region  string
	On      time.Time
}

// Lookup resolves a place of receipt to the zone that covers it, through the
// caller's executor so it runs as well inside a transaction as out. The
// matching order: a boundary whose range contains the base postal code, with
// a zone whose effective window covers the date; a province zone matches the
// region instead. A place that more than one zone covers takes the lowest
// combined rate in the area, the rule the states publish. A place no zone
// covers answers ErrZoneNotFound.
func Lookup(ctx context.Context, db *database.DB, in LookupInput) (*Snapshot, error) {
	exec := db.GetExecutor(ctx)
	base, ext := SplitPostal(in.Postal)
	country := strings.ToUpper(strings.TrimSpace(in.Country))
	on := in.On.Format("2006-01-02")

	// The zones whose boundary contains the base code (a range, or an exact
	// code as the degenerate range), plus the province zones of the region,
	// whose effective window covers the date. Extensions narrow only when the
	// boundary carries them: a plus four never disqualifies a five digit
	// boundary, because the five digit fallback is the published rule.
	rows, err := exec.Query(ctx, `
		SELECT z.id, z.data_source, z.data_version,
		       COALESCE((SELECT SUM(r.rate) FROM tax_zone_rates r
		                 WHERE r.zone_id = z.id AND r.effective_from <= $3::date
		                   AND (r.effective_to IS NULL OR r.effective_to >= $3::date)), 0)::text AS combined
		FROM tax_zones z
		WHERE z.effective_from <= $3::date
		  AND (z.effective_to IS NULL OR z.effective_to >= $3::date)
		  AND EXISTS (SELECT 1 FROM tax_zone_rates r
		              WHERE r.zone_id = z.id AND r.effective_from <= $3::date
		                AND (r.effective_to IS NULL OR r.effective_to >= $3::date))
		  AND (
		        (z.zone_kind = 'POSTAL'
		         AND EXISTS (SELECT 1 FROM tax_zone_boundaries b
		                     WHERE b.zone_id = z.id AND b.country = $1
		                       AND b.postal_code_low <= $2
		                       AND (b.postal_code_high = '' OR b.postal_code_high >= $2)
		                       AND (b.extension_low = '' OR b.extension_high = '' OR $4 = ''
		                            OR (b.extension_low <= $4 AND b.extension_high >= $4))))
		     OR (z.zone_kind = 'PROVINCE' AND z.country = $1 AND z.province_code = $5)
		  )
		ORDER BY combined ASC, z.jurisdiction_code ASC, z.id ASC`,
		country, base, on, ext, strings.TrimSpace(in.Region))
	if err != nil {
		return nil, fmt.Errorf("failed to look up the tax zone: %w", err)
	}
	defer rows.Close()

	// The first row is the lowest combined rate in the area: the state rule
	// for a postal code that straddles districts. The result set is drained
	// and closed before the parts are read: inside a transaction the two
	// queries share one connection.
	var (
		id  uuid.UUID
		src string
		ver string
	)
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to read the tax zone: %w", err)
		}
		rows.Close()
		return nil, ErrZoneNotFound
	}
	if err := rows.Scan(&id, &src, &ver, new(string)); err != nil {
		rows.Close()
		return nil, fmt.Errorf("failed to read the tax zone: %w", err)
	}
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("failed to read the tax zone: %w", err)
	}
	rows.Close()

	// The parts the snapshot freezes: the live parts of the winning zone.
	partRows, err := exec.Query(ctx, `
		SELECT r.part_name, r.rate::text FROM tax_zone_rates r
		WHERE r.zone_id = $1 AND r.effective_from <= $2::date
		  AND (r.effective_to IS NULL OR r.effective_to >= $2::date)
		ORDER BY r.part_name`, id, on)
	if err != nil {
		return nil, fmt.Errorf("failed to read the zone's rate parts: %w", err)
	}
	defer partRows.Close()
	parts := []Part{}
	for partRows.Next() {
		var p Part
		if err := partRows.Scan(&p.Name, &p.Rate); err != nil {
			return nil, fmt.Errorf("failed to read a zone rate part: %w", err)
		}
		parts = append(parts, p)
	}
	provenance := strings.TrimSpace(src)
	if ver != "" {
		provenance = strings.TrimSpace(provenance + " " + ver)
	}
	return &Snapshot{
		ZoneID:       id,
		Parts:        parts,
		CombinedRate: combinedOf(parts),
		DataSource:   provenance,
	}, nil
}

// combinedOf sums the parts as scaled integers and renders the decimal
// string, so the snapshot's combined rate is exactly the sum of its parts.
func combinedOf(parts []Part) string {
	sum := int64(0)
	for i := range parts {
		if scaled, ok, err := parseRate(parts[i].Rate); err == nil && ok {
			sum += scaled
		}
	}
	return renderRate(sum)
}
