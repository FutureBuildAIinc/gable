// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package configurator

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// Selections are the attribute choices a validation or an options read
// carries: a map from attribute type to the chosen value.
type Selections map[string]string

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// AllRules returns the complete rule set for the frontend to display.
func (s *Service) AllRules(ctx context.Context) ([]Rule, error) {
	return s.repo.AllRules(ctx)
}

// Presets returns the active presets, optionally filtered by product type.
func (s *Service) Presets(ctx context.Context, productType string) ([]Preset, error) {
	return s.repo.Presets(ctx, productType)
}

// ValidateConfig checks a full set of attribute selections against the rule
// matrix. The answer is always a response: valid true with no conflicts, or
// valid false with one conflict per violated rule, deterministically
// ordered.
func (s *Service) ValidateConfig(ctx context.Context, selections Selections) (*ValidateResponse, error) {
	resp := &ValidateResponse{Valid: true, Conflicts: []ValidationConflict{}}

	type pairKey struct{ attr, dep string }
	checked := map[pairKey]bool{}

	for _, attrType := range sortedSelectionKeys(selections) {
		attrValue := selections[attrType]
		for _, depType := range sortedSelectionKeys(selections) {
			if attrType == depType {
				continue
			}
			depValue := selections[depType]
			key := pairKey{attrType + "=" + attrValue, depType + "=" + depValue}
			if checked[key] {
				continue
			}
			checked[key] = true
			rules, err := s.repo.AllowedValues(ctx, attrType, depType, depValue)
			if err != nil {
				return nil, fmt.Errorf("failed to check rules for %s=%s: %w", attrType, attrValue, err)
			}
			for _, rule := range rules {
				if rule.AttributeValue == attrValue && !rule.IsAllowed {
					resp.Valid = false
					msg := fmt.Sprintf("%s '%s' is not compatible with %s '%s'", attrType, attrValue, depType, depValue)
					if rule.ErrorMessage != nil && *rule.ErrorMessage != "" {
						msg = *rule.ErrorMessage
					}
					resp.Conflicts = append(resp.Conflicts, ValidationConflict{
						AttributeType:  attrType,
						AttributeValue: attrValue,
						DependsOnType:  depType,
						DependsOnValue: depValue,
						Message:        msg,
					})
				}
			}
		}
	}
	return resp, nil
}

// GetAvailableOptions returns the values an attribute may take under the
// current selections, deterministically ordered by value. With no
// constraining selection the static defaults answer.
func (s *Service) GetAvailableOptions(ctx context.Context, attributeType string, selections Selections) ([]AvailableOption, error) {
	if len(selections) == 0 {
		return s.staticOptions(attributeType), nil
	}
	optionMap := map[string]*AvailableOption{}
	for _, depType := range sortedSelectionKeys(selections) {
		rules, err := s.repo.AllowedValues(ctx, attributeType, depType, selections[depType])
		if err != nil {
			return nil, fmt.Errorf("failed to get options: %w", err)
		}
		for _, rule := range rules {
			var msg *string
			if rule.ErrorMessage != nil {
				m := *rule.ErrorMessage
				msg = &m
			}
			if existing, ok := optionMap[rule.AttributeValue]; ok {
				if !rule.IsAllowed {
					existing.Allowed = false
					if msg != nil {
						existing.Message = msg
					}
				}
				continue
			}
			optionMap[rule.AttributeValue] = &AvailableOption{Value: rule.AttributeValue, Allowed: rule.IsAllowed, Message: msg}
		}
	}
	if len(optionMap) == 0 {
		return s.staticOptions(attributeType), nil
	}
	values := make([]string, 0, len(optionMap))
	for v := range optionMap {
		values = append(values, v)
	}
	sort.Strings(values)
	out := make([]AvailableOption, 0, len(values))
	for _, v := range values {
		out = append(out, *optionMap[v])
	}
	return out, nil
}

// BuildSKU generates a non-stock SKU from validated selections. Conflicting
// selections are a 400 whose details carry one blocker per conflict.
func (s *Service) BuildSKU(ctx context.Context, productType string, selections Selections) (*BuildSKUResponse, error) {
	valResp, err := s.ValidateConfig(ctx, selections)
	if err != nil {
		return nil, err
	}
	if !valResp.Valid {
		blockers := make([]httpx.FieldError, 0, len(valResp.Conflicts))
		for _, c := range valResp.Conflicts {
			blockers = append(blockers, httpx.Blocker("config_conflict", c.Message))
		}
		return nil, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
			Message: fmt.Sprintf("cannot build a SKU: the selections have %d conflict(s)", len(valResp.Conflicts)),
			Details: blockers}
	}

	parts := []string{"NS"} // Non-Stock prefix
	typeAbbrev := map[string]string{
		"Lumber": "LBR",
		"Door":   "DR",
		"Trim":   "TRM",
		"Panel":  "PNL",
	}
	if abbr, ok := typeAbbrev[productType]; ok {
		parts = append(parts, abbr)
	} else {
		pt := strings.ToUpper(productType)
		if len(pt) > 3 {
			pt = pt[:3]
		}
		parts = append(parts, pt)
	}

	order := []string{"Species", "Grade", "Treatment", "Dimensions"}
	for _, key := range order {
		if val, ok := selections[key]; ok && val != "" && val != "None" {
			parts = append(parts, strings.ToUpper(strings.ReplaceAll(val, " ", "-")))
		}
	}
	sku := strings.Join(parts, "-")

	descParts := []string{productType}
	for _, key := range order {
		if val, ok := selections[key]; ok && val != "" && val != "None" {
			descParts = append(descParts, val)
		}
	}
	return &BuildSKUResponse{SKU: sku, Description: strings.Join(descParts, " ")}, nil
}

// staticOptions returns the defaults when no constraint applies.
func (s *Service) staticOptions(attributeType string) []AvailableOption {
	defaults := map[string][]string{
		"ProductType": {"Lumber", "Door", "Trim", "Panel"},
		"Species":     {"SYP", "Douglas Fir", "Cedar", "Hem-Fir", "SPF"},
		"Grade":       {"#1", "#2", "#3", "Stud", "Select Structural", "Clear", "STK", "Structural", "Appearance"},
		"Treatment":   {"None", "Treatable", "Fire Retardant", "Borate"},
		"Dimensions":  {"2x4", "2x6", "2x8", "2x10", "2x12", "4x4", "1x4", "1x6"},
	}
	values, ok := defaults[attributeType]
	if !ok {
		return []AvailableOption{}
	}
	out := make([]AvailableOption, len(values))
	for i, v := range values {
		out[i] = AvailableOption{Value: v, Allowed: true}
	}
	return out
}
