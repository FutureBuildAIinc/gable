// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package configurator

import (
	"context"
	"fmt"
	"sort"

	"github.com/gablelbm/gable/internal/millwork"
	"github.com/gablelbm/gable/internal/platform/httpx"
)

// Selections are the attribute choices a validation or an options read
// carries: a map from attribute type to the chosen value.
type Selections map[string]string

// ConfiguredBuilder is the seam to the configured item. The millwork
// service satisfies it: it prices a configuration from the stored options,
// builds its deterministic SKU, finds or creates the catalog row it becomes
// and answers the canonical configuration it did all that from.
//
// The seam exists so the configurator owns no pricing of its own. The
// configurator knows the rule matrix (which choices go together); the
// millwork catalog knows the money (what the choices cost).
type ConfiguredBuilder interface {
	BuildConfigured(ctx context.Context, productType string, selections map[string]string, width, height *int) (*millwork.ConfiguredItem, error)
}

type Service struct {
	repo    *Repository
	builder ConfiguredBuilder
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// WithConfiguredBuilder wires the configured item's pricing and catalog row.
func (s *Service) WithConfiguredBuilder(b ConfiguredBuilder) *Service {
	s.builder = b
	return s
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

// BuildSKU prices a configuration from the stored options, builds its
// deterministic non-stock SKU and finds or creates the catalog row it
// becomes. Conflicting selections are a 400 whose details carry one blocker
// per conflict.
//
// The money is the server's: the client sends the choices and reads the
// price. A configured item's price is computed from the option catalog, in
// integer arithmetic, and no client-sent price is ever taken.
func (s *Service) BuildSKU(ctx context.Context, productType string, selections Selections, width, height *int) (*BuildSKUResponse, error) {
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
	if s.builder == nil {
		return nil, httpx.BadRequest("the configured item cannot be priced",
			httpx.FieldError{Field: "product_type", Message: "the option catalog is not wired"})
	}
	item, err := s.builder.BuildConfigured(ctx, productType, selections, width, height)
	if err != nil {
		return nil, err
	}
	return &BuildSKUResponse{
		SKU:           item.SKU,
		Description:   item.Description,
		ProductID:     item.ProductID,
		UnitPrice:     item.UnitPrice,
		UnitCost:      item.UnitCost,
		Configuration: item.Configuration,
	}, nil
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
