// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package tint

// The dealer's tint charge rule (row CP-16). The rule belongs to the dealer
// and follows the tint, not only the container: the research's first pricing
// consequence says a flat per container charge is what the tint catalogue
// vendors and the big box stores run, while a dealer who mixes its own paint
// prices by the colorant the mix takes and by the base it takes it in (a
// light mix, a medium mix and a deep mix are three prices, not one). So the
// rule is a flat figure per container plus a list of tiers, and a tier names
// the base tier it covers and the lowest colorant total it covers.
//
// The rule is read from two system_settings rows, the flat figure and the tier
// list, so a dealer writes both through one settings resource and the counter
// reads one rule.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The settings rows the rule lives in.
const (
	// SettingTintChargeCents is the flat figure per container, in cents.
	SettingTintChargeCents = "pos.tint_charge_cents"
	// SettingTintChargeTiers is the tier list, as JSON.
	SettingTintChargeTiers = "pos.tint_charge_tiers"
)

// The base tiers a tint can be mixed in.
const (
	// BaseTierFlat is the ordinary base of a system.
	BaseTierFlat = "flat"
	// BaseTierDeep is the deepest base of a system, the one that takes the
	// most colorant.
	BaseTierDeep = "deep"
)

// ChargeTier is one tier of the rule: the base tier it covers ("" is any
// base), the lowest colorant total in fluid ounces it covers ("" is any
// total) and the charge in cents.
type ChargeTier struct {
	BaseTier      string `json:"base_tier"`
	MinColorantOZ string `json:"min_colorant_oz"`
	ChargeCents   int64  `json:"charge_cents"`
}

// ChargeRule is the dealer's charge rule: the flat figure per container and
// the tiers that may cover it.
type ChargeRule struct {
	// FlatCents is the charge per container when no tier covers the tint.
	FlatCents int64 `json:"flat_cents"`
	// Tiers are the dealer's tiers.
	Tiers []ChargeTier `json:"tiers"`
}

// Minimum reads the tier's colorant floor exactly: an empty floor is any
// total, a floor of zero is any total too.
func (t ChargeTier) Minimum() (*big.Rat, bool) {
	s := strings.TrimSpace(t.MinColorantOZ)
	if s == "" {
		return new(big.Rat), true
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() < 0 {
		return nil, false
	}
	return r, true
}

// Charge finds the charge for a tint: the colorant total in fluid ounces and
// the base tier ("" when the base cannot be resolved). The most specific tier
// wins: a tier naming the base tier and a colorant floor beats a tier naming
// the base tier alone, which beats a tier naming a colorant floor alone;
// among equals the higher floor wins. A tint no tier covers is charged the
// flat figure, which is also the answer when the dealer has set no tiers (the
// dominant retail model, folding the tint into the shelf price).
func (r ChargeRule) Charge(totalOZ *big.Rat, baseTier string) int64 {
	best := -1
	bestScore := -1
	var bestMin *big.Rat
	for i, t := range r.Tiers {
		if t.BaseTier != "" && (baseTier == "" || t.BaseTier != baseTier) {
			continue
		}
		min, ok := t.Minimum()
		if !ok {
			continue
		}
		if totalOZ != nil && totalOZ.Cmp(min) < 0 {
			continue
		}
		score := 0
		if t.BaseTier != "" {
			score += 2
		}
		if strings.TrimSpace(t.MinColorantOZ) != "" {
			score++
		}
		if score > bestScore || (score == bestScore && bestMin != nil && min.Cmp(bestMin) > 0) {
			best, bestScore, bestMin = i, score, min
		}
	}
	if best < 0 {
		return r.FlatCents
	}
	return r.Tiers[best].ChargeCents
}

// RuleSource is the one method a repository needs to read the rule's rows.
type RuleSource interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ReadChargeRule reads the dealer's rule from its two settings rows: the flat
// figure per container and the tier list. A row that is not there is no
// figure, never an error; a row that is there and malformed is an error, so a
// broken setting never silently charges the wrong figure.
func ReadChargeRule(ctx context.Context, q RuleSource) (ChargeRule, error) {
	rule := ChargeRule{}
	flat, err := readSetting(ctx, q, SettingTintChargeCents)
	if err != nil {
		return rule, err
	}
	if flat != "" {
		n, perr := strconv.ParseInt(flat, 10, 64)
		if perr != nil || n < 0 {
			return rule, fmt.Errorf("the %s setting is not a whole number of cents from 0 up", SettingTintChargeCents)
		}
		rule.FlatCents = n
	}
	tiers, err := readSetting(ctx, q, SettingTintChargeTiers)
	if err != nil {
		return rule, err
	}
	if tiers != "" {
		var list []ChargeTier
		if jerr := json.Unmarshal([]byte(tiers), &list); jerr != nil {
			return rule, fmt.Errorf("the %s setting is not a tier list: %w", SettingTintChargeTiers, jerr)
		}
		rule.Tiers = list
	}
	return rule, nil
}

// readSetting reads one settings row, empty when it is not there.
func readSetting(ctx context.Context, q RuleSource, key string) (string, error) {
	var value *string
	err := q.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to read the %s setting: %w", key, err)
	}
	if value == nil {
		return "", nil
	}
	return strings.TrimSpace(*value), nil
}
