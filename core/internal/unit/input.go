// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package unit

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// codeRule is the unit code vocabulary every unit column holds (ADR 0006
// 2.1): one to six capital letters.
var codeRule = regexp.MustCompile(`^[A-Z]{1,6}$`)

// request is the POST and PUT body as it arrives: strings, pointers and raw
// JSON, parsed by ParseCreate and ParseUpdate with every problem collected
// into one 400. Fields the write does not apply (is_system, revision on
// create, code on update) are refused naming the field, the recipe's rule.
type request struct {
	Code       string          `json:"code"`
	Name       *string         `json:"name"`
	Dimension  *string         `json:"dimension"`
	StdUnitQty json.RawMessage `json:"std_unit_qty"`
	StdRefQty  json.RawMessage `json:"std_ref_qty"`
	IsSystem   *bool           `json:"is_system"`
	IsActive   *bool           `json:"is_active"`
	Revision   json.RawMessage `json:"revision"`
}

func (req *request) parseStdSize(v *httpx.Validator, d *Draft) {
	unitSent, hasUnit := v.Quantity("std_unit_qty", req.StdUnitQty, false)
	refSent, hasRef := v.Quantity("std_ref_qty", req.StdRefQty, false)
	switch {
	case hasUnit && hasRef:
		v.Check(unitSent > 0, "std_unit_qty", "must be greater than zero")
		v.Check(refSent > 0, "std_ref_qty", "must be greater than zero")
		d.StdUnitQty, d.StdRefQty, d.HasStdSize = &unitSent, &refSent, true
	case hasUnit != hasRef:
		missing := "std_ref_qty"
		if hasRef {
			missing = "std_unit_qty"
		}
		v.Check(false, missing, "is required: a standard size is a pair, std_unit_qty and std_ref_qty together")
	}
}

// ParseCreate validates the create body: the code, the name and the
// dimension are required, the standard size optional but whole.
func ParseCreate(r *http.Request) (*Draft, error) {
	var req request
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return nil, err
	}
	v := &httpx.Validator{}
	d := &Draft{}

	v.Required("code", req.Code)
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	v.Check(codeRule.MatchString(code), "code", "must be one to six capital letters, for example MBF or CWT")
	d.Code = code

	if req.Name == nil {
		v.Check(false, "name", "is required")
	} else {
		name := strings.TrimSpace(*req.Name)
		v.Check(len(name) >= 1 && len(name) <= 80, "name", "must be 1 to 80 characters")
		d.Name = &name
	}

	if req.Dimension == nil {
		v.Check(false, "dimension", "is required")
	} else {
		dim, ok := ParseDimension(*req.Dimension)
		v.Check(ok, "dimension", "must be one of: count, length, area, volume, weight, board_measure")
		if ok {
			d.Dimension = &dim
		}
	}

	if req.IsSystem != nil {
		v.Check(false, "is_system", "is read only: seeded units are system units, and a unit the dealer creates is not")
	}
	if !isAbsentRaw(req.Revision) {
		v.Check(false, "revision", "is not accepted on create: a new unit has no revision to precondition on")
	}
	req.parseStdSize(v, d)
	if req.IsActive != nil {
		d.IsActive = req.IsActive
	}

	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// ParseUpdate validates the PUT body: name, is_active, and the dimension
// and standard size while the unit allows them (the service holds the
// immutability rules). A body carrying code is refused naming it.
func ParseUpdate(r *http.Request) (*Draft, *int64, error) {
	var req request
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return nil, nil, err
	}
	v := &httpx.Validator{}
	d := &Draft{}

	if strings.TrimSpace(req.Code) != "" {
		v.Check(false, "code", "is immutable: the path names the unit")
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		v.Check(len(name) >= 1 && len(name) <= 80, "name", "must be 1 to 80 characters")
		d.Name = &name
	}
	if req.Dimension != nil {
		dim, ok := ParseDimension(*req.Dimension)
		v.Check(ok, "dimension", "must be one of: count, length, area, volume, weight, board_measure")
		if ok {
			d.Dimension = &dim
		}
	}
	if req.IsSystem != nil {
		v.Check(false, "is_system", "is read only")
	}
	req.parseStdSize(v, d)
	if req.IsActive != nil {
		d.IsActive = req.IsActive
	}
	var revision *int64
	if n, ok := v.Int("revision", req.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		revision = &n
	}
	if err := v.Err(); err != nil {
		return nil, nil, err
	}
	return d, revision, nil
}

func isAbsentRaw(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return t == "" || t == "null"
}
