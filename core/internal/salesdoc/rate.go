// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesdoc

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// RateToPercent widens a stored rate ("0.088750") to the wire's percent
// string ("8.875"): the rate is scale 6, the percent at most 4 fraction
// digits, the shift exact. Orders, invoices and credit memos all read their
// rate this way.
func RateToPercent(rate string) (string, error) {
	scaled, _, err := ParseTaxRate(rate)
	if err != nil {
		return "", err
	}
	// percent = rate x 100, so the percent's scale 4 integer IS the rate's
	// scale 6 integer: 0.088750 is 88750 at both (8.8750 percent).
	abs := scaled
	if abs < 0 {
		abs = -abs
	}
	out := fmt.Sprintf("%d.%04d", abs/10000, abs%10000)
	if strings.Contains(out, ".") {
		out = strings.TrimSuffix(strings.TrimRight(out, "0"), ".")
	}
	if out == "" {
		out = "0"
	}
	if scaled < 0 && out != "0" {
		out = "-" + out
	}
	return out, nil
}

// ShipToSnapshot is the address captured at confirm (ADR 0005 section 5.1),
// stored as JSONB on the order and copied to its invoices, and read back as
// the wire's ship_to object.
type ShipToSnapshot struct {
	ID                   uuid.UUID `json:"id"`
	Code                 string    `json:"code"`
	Name                 string    `json:"name"`
	Line1                string    `json:"line1"`
	Line2                *string   `json:"line2"`
	City                 string    `json:"city"`
	Region               string    `json:"region"`
	PostalCode           string    `json:"postal_code"`
	Country              *string   `json:"country"`
	Phone                *string   `json:"phone"`
	DeliveryInstructions *string   `json:"delivery_instructions"`
}
