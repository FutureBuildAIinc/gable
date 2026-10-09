// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package millwork

import (
	"encoding/json"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// maxPriceAdjustment bounds the adjustment to what the cents field and the
// NUMERIC(10,2) column can hold.
const maxPriceAdjustment = 99_999_999_999

// Request is the body of POST /millwork/options. Fields decode as strings
// and raw JSON, and Parse turns them into a Draft, collecting every problem
// into one 400 (ADR 0001 section 4).
type Request struct {
	Category             *string         `json:"category"`
	Name                 *string         `json:"name"`
	PriceAdjustmentCents json.RawMessage `json:"price_adjustment_cents"`
	Attributes           json.RawMessage `json:"attributes"`
}

// Draft is a validated request.
type Draft struct {
	Category             string
	Name                 string
	PriceAdjustmentCents int64
	Attributes           json.RawMessage
}

// Parse validates the request in one pass.
func (req *Request) Parse() (*Draft, error) {
	v := &httpx.Validator{}
	d := &Draft{}
	v.Required("category", deref(req.Category))
	if req.Category != nil {
		cat := strings.TrimSpace(*req.Category)
		v.Check(len(cat) > 0, "category", "is required")
		v.Check(len(cat) <= maxCategory, "category", "must be at most 50 characters")
		d.Category = cat
	}
	v.Required("name", deref(req.Name))
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		v.Check(len(name) > 0, "name", "is required")
		v.Check(len(name) <= maxName, "name", "must be at most 100 characters")
		d.Name = name
	}
	if n, ok := v.Int("price_adjustment_cents", req.PriceAdjustmentCents, true); ok {
		v.Check(n >= -maxPriceAdjustment && n <= maxPriceAdjustment, "price_adjustment_cents",
			"must be between -99999999999 and 99999999999 cents")
		d.PriceAdjustmentCents = n
	}
	if !isAbsent(req.Attributes) {
		d.Attributes = req.Attributes
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func isAbsent(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return t == "" || t == "null"
}
