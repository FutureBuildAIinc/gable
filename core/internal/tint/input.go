// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package tint

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// maxBaseName is the paint_bases.name column's CHECK bound, in characters.
const maxBaseName = 120

// unprocessable is a 422 whose details name the one field the caller must fix.
func unprocessable(field, message string) *httpx.Error {
	return &httpx.Error{Status: http.StatusUnprocessableEntity, Code: httpx.CodeValidationFailed,
		Message: "one or more fields failed validation",
		Details: []httpx.FieldError{{Field: field, Message: message}}}
}

// checkBaseName refuses a name the column would, before the store is reached.
func checkBaseName(name string) error {
	if utf8.RuneCountInString(name) > maxBaseName {
		return unprocessable("name", "must be at most 120 characters")
	}
	return nil
}

// systemRequest is POST /api/v1/tint/systems.
type systemRequest struct {
	Code            *string `json:"code"`
	Name            *string `json:"name"`
	Manufacturer    *string `json:"manufacturer"`
	UnitDenominator *int    `json:"unit_denominator"`
	UnitLabel       *string `json:"unit_label"`
}

func (r *systemRequest) parse() (*SystemDraft, error) {
	v := &httpx.Validator{}
	d := &SystemDraft{}
	if r.Code != nil {
		d.Code = *r.Code
	}
	v.Required("code", d.Code)
	v.Required("name", deref(r.Name))
	if r.Name != nil {
		d.Name = *r.Name
	}
	if r.Manufacturer != nil {
		d.Manufacturer = *r.Manufacturer
	}
	if r.UnitLabel != nil {
		d.UnitLabel = *r.UnitLabel
	}
	if r.UnitDenominator != nil {
		d.UnitDenominator = *r.UnitDenominator
	}
	v.Check(r.UnitDenominator != nil && ValidDenominator(d.UnitDenominator), "unit_denominator",
		"must be one of the trade's dispense denominators: 32, 48, 64, 128, 192, 384, 1024")
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// baseRequest is POST /api/v1/tint/bases.
type baseRequest struct {
	TintSystemID         *string `json:"tint_system_id"`
	Code                 *string `json:"code"`
	Name                 *string `json:"name"`
	ManufacturerBaseCode *string `json:"manufacturer_base_code"`
	ProductID            *string `json:"product_id"`
	FillVolumeOZ         *int    `json:"fill_volume_oz"`
	IsDeepest            *bool   `json:"is_deepest"`
}

func (r *baseRequest) parse() (*BaseDraft, error) {
	v := &httpx.Validator{}
	d := &BaseDraft{}
	if id, ok := v.UUID("tint_system_id", r.TintSystemID, true); ok {
		d.TintSystemID = id
	}
	if r.Code != nil {
		d.Code = *r.Code
	}
	if r.Name != nil {
		d.Name = *r.Name
	}
	v.Required("code", d.Code)
	v.Required("name", d.Name)
	if err := checkBaseName(d.Name); err != nil {
		return nil, err
	}
	if r.ManufacturerBaseCode != nil {
		d.ManufacturerBaseCode = *r.ManufacturerBaseCode
	}
	if id, ok := v.UUID("product_id", r.ProductID, false); ok {
		d.ProductID = &id
	}
	if r.FillVolumeOZ != nil {
		d.FillVolumeOZ = *r.FillVolumeOZ
	}
	v.Check(r.FillVolumeOZ != nil && d.FillVolumeOZ > 0, "fill_volume_oz",
		"must be a positive whole number of ounces")
	if r.IsDeepest != nil {
		d.IsDeepest = *r.IsDeepest
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// colorantRequest is POST /api/v1/tint/colorants.
type colorantRequest struct {
	TintSystemID     *string `json:"tint_system_id"`
	Code             *string `json:"code"`
	Name             *string `json:"name"`
	ManufacturerCode *string `json:"manufacturer_code"`
	ManufacturerUPC  *string `json:"manufacturer_upc"`
}

func (r *colorantRequest) parse() (*ColorantDraft, error) {
	v := &httpx.Validator{}
	d := &ColorantDraft{}
	if id, ok := v.UUID("tint_system_id", r.TintSystemID, true); ok {
		d.TintSystemID = id
	}
	if r.Code != nil {
		d.Code = *r.Code
	}
	if r.Name != nil {
		d.Name = *r.Name
	}
	v.Required("code", d.Code)
	v.Required("name", d.Name)
	if r.ManufacturerCode != nil {
		d.ManufacturerCode = *r.ManufacturerCode
	}
	if r.ManufacturerUPC != nil {
		d.ManufacturerUPC = r.ManufacturerUPC
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// colorRequest is POST /api/v1/tint/colors.
type colorRequest struct {
	TintSystemID     *string `json:"tint_system_id"`
	ManufacturerCode *string `json:"manufacturer_code"`
	Name             *string `json:"name"`
	IsCustom         *bool   `json:"is_custom"`
	CustomerID       *string `json:"customer_id"`
	Note             *string `json:"note"`
}

func (r *colorRequest) parse() (*ColorDraft, error) {
	v := &httpx.Validator{}
	d := &ColorDraft{}
	if id, ok := v.UUID("tint_system_id", r.TintSystemID, true); ok {
		d.TintSystemID = id
	}
	if r.ManufacturerCode != nil && strings.TrimSpace(*r.ManufacturerCode) != "" {
		d.ManufacturerCode = r.ManufacturerCode
	}
	if r.Name != nil {
		d.Name = *r.Name
	}
	v.Required("name", d.Name)
	if r.IsCustom != nil {
		d.IsCustom = *r.IsCustom
	}
	if id, ok := v.UUID("customer_id", r.CustomerID, false); ok {
		d.CustomerID = &id
	}
	if r.Note != nil {
		d.Note = *r.Note
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// componentRequest is one component of a formula create.
type componentRequest struct {
	ColorantID  string `json:"colorant_id"`
	WholeOZ     *int   `json:"whole_oz"`
	Coarse      *int   `json:"increments_coarse"`
	Fine        *int   `json:"increments_fine"`
	Denominator *int   `json:"unit_denominator"`
}

// formulaRequest is POST /api/v1/tint/formulas.
type formulaRequest struct {
	ColorID           *string            `json:"color_id"`
	BaseID            *string            `json:"base_id"`
	ProductID         *string            `json:"product_id"`
	ContainerSizeCode *string            `json:"container_size_code"`
	ContainerVolumeOZ *int               `json:"container_volume_oz"`
	Source            *string            `json:"source"`
	Components        []componentRequest `json:"components"`
}

func (r *formulaRequest) parse() (*FormulaDraft, error) {
	v := &httpx.Validator{}
	d := &FormulaDraft{}
	if id, ok := v.UUID("color_id", r.ColorID, true); ok {
		d.ColorID = id
	}
	if id, ok := v.UUID("base_id", r.BaseID, true); ok {
		d.BaseID = id
	}
	if id, ok := v.UUID("product_id", r.ProductID, false); ok {
		d.ProductID = &id
	}
	if r.ContainerSizeCode != nil {
		d.ContainerSizeCode = *r.ContainerSizeCode
	}
	v.Required("container_size_code", d.ContainerSizeCode)
	if r.ContainerVolumeOZ != nil {
		d.ContainerVolumeOZ = *r.ContainerVolumeOZ
	}
	v.Check(r.ContainerVolumeOZ != nil && d.ContainerVolumeOZ > 0, "container_volume_oz",
		"must be a positive whole number of ounces")
	d.Source = "manual"
	if r.Source != nil && *r.Source != "" {
		d.Source = *r.Source
	}
	v.Check(d.Source == "book" || d.Source == "match" || d.Source == "manual", "source",
		"must be one of: book, match, manual")
	for i := range r.Components {
		c := &r.Components[i]
		id, ok := v.UUID(componentField(i, "colorant_id"), &c.ColorantID, true)
		if !ok {
			continue
		}
		amount := Amount{Denominator: -1}
		if c.Denominator != nil {
			amount.Denominator = *c.Denominator
		}
		if c.WholeOZ != nil {
			amount.WholeOZ = *c.WholeOZ
		}
		if c.Coarse != nil {
			amount.Coarse = *c.Coarse
		}
		if c.Fine != nil {
			amount.Fine = *c.Fine
		}
		if err := amount.Validate(); err != nil {
			v.Check(false, componentField(i, "unit_denominator"), err.Error())
			continue
		}
		d.Components = append(d.Components, StoredComponent{ColorantID: id, Amount: amount})
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

func componentField(i int, name string) string {
	return "components[" + strconv.Itoa(i) + "]." + name
}

// rescaleRequest is POST /api/v1/tint/formulas/{id}/rescale.
type rescaleRequest struct {
	TargetVolumeOZ *int `json:"target_volume_oz"`
}

func (r *rescaleRequest) parse() (int, error) {
	v := &httpx.Validator{}
	target := 0
	if r.TargetVolumeOZ != nil {
		target = *r.TargetVolumeOZ
	}
	v.Check(r.TargetVolumeOZ != nil && target > 0, "target_volume_oz",
		"must be a positive whole number of ounces")
	if err := v.Err(); err != nil {
		return 0, err
	}
	return target, nil
}

// savedColorRequest is POST /api/v1/tint/saved-colors.
type savedColorRequest struct {
	CustomerID *string `json:"customer_id"`
	ColorID    *string `json:"color_id"`
	Name       *string `json:"name"`
	JobNote    *string `json:"job_note"`
	Note       *string `json:"note"`
	ProjectID  *string `json:"project_id"`
}

func (r *savedColorRequest) parse() (*SavedColorDraft, error) {
	v := &httpx.Validator{}
	d := &SavedColorDraft{}
	if id, ok := v.UUID("customer_id", r.CustomerID, true); ok {
		d.CustomerID = id
	}
	if id, ok := v.UUID("color_id", r.ColorID, true); ok {
		d.ColorID = id
	}
	if r.Name != nil {
		d.Name = *r.Name
	}
	v.Required("name", d.Name)
	if r.JobNote != nil {
		d.JobNote = *r.JobNote
	}
	if r.Note != nil {
		d.Note = *r.Note
	}
	if id, ok := v.UUID("project_id", r.ProjectID, false); ok {
		d.ProjectID = &id
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

// baseUpdateRequest is PATCH /api/v1/tint/bases/{id}. Every field is optional;
// product_id is raw so an absent key (leave the link) differs from null (clear it).
type baseUpdateRequest struct {
	Name         *string         `json:"name"`
	ProductID    json.RawMessage `json:"product_id"`
	FillVolumeOZ *int            `json:"fill_volume_oz"`
	IsDeepest    *bool           `json:"is_deepest"`
}

func (r *baseUpdateRequest) parse() (*BaseUpdate, error) {
	v := &httpx.Validator{}
	u := &BaseUpdate{Name: r.Name, FillVolumeOZ: r.FillVolumeOZ, IsDeepest: r.IsDeepest}
	if r.ProductID != nil {
		u.SetProduct = true
		if string(r.ProductID) != "null" {
			var raw string
			if err := json.Unmarshal(r.ProductID, &raw); err != nil {
				v.Check(false, "product_id", "must be a product id or null")
			} else if id, ok := v.UUID("product_id", &raw, true); ok {
				u.ProductID = &id
			}
		}
	}
	if r.Name != nil {
		v.Required("name", *r.Name)
		if err := checkBaseName(*r.Name); err != nil {
			return nil, err
		}
	}
	if r.FillVolumeOZ != nil {
		v.Check(*r.FillVolumeOZ > 0, "fill_volume_oz", "must be a positive whole number of ounces")
	}
	v.Check(u.SetProduct || r.Name != nil || r.FillVolumeOZ != nil || r.IsDeepest != nil,
		"body", "names no field to change")
	if err := v.Err(); err != nil {
		return nil, err
	}
	return u, nil
}
