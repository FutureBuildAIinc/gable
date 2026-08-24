// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"fmt"
	"math"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/google/uuid"
)

type Service struct {
	repo   Repository
	catSvc *CategoryPricingService
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// WithCategoryPricing enables the category-aware pricing engine.
// When set, step 5 of the waterfall uses category rules instead of hardcoded tier multipliers.
func (s *Service) WithCategoryPricing(catSvc *CategoryPricingService) {
	s.catSvc = catSvc
}

// CalculatePrice implements a 6-level pricing waterfall:
// 1. Contract Price (SKU + Customer specific)
// 2. Job-Level Override (project-specific pricing)
// 3. Promotional/Sale Price (time-bound)
// 4. Quantity Break (volume discount)
// 5. Customer Price Level/Tier
// 6. Base Retail
func (s *Service) CalculatePrice(ctx context.Context, cust *customer.Customer, productID uuid.UUID, basePrice float64) (CalculatedPrice, error) {
	return s.CalculatePriceWithQty(ctx, cust, productID, basePrice, 1, nil)
}

func (s *Service) CalculatePriceWithQty(ctx context.Context, cust *customer.Customer, productID uuid.UUID, basePrice float64, quantity float64, jobID *uuid.UUID) (CalculatedPrice, error) {
	// 1. Check Contract Price (highest priority - specific customer+product agreement)
	contract, err := s.repo.GetContract(ctx, cust.ID, productID)
	if err != nil {
		return CalculatedPrice{}, err
	}
	if contract != nil {
		discountPct := 0.0
		if basePrice > 0 {
			discountPct = (basePrice - contract.ContractPrice) / basePrice * 100
		}
		return CalculatedPrice{
			ProductID:     productID,
			OriginalPrice: basePrice,
			FinalPrice:    contract.ContractPrice,
			DiscountPct:   math.Round(discountPct*100) / 100,
			Source:        SourceContract,
			Details:       "Specific Contract Price",
		}, nil
	}

	// 2-4. Check pricing rules (job override, promotional, quantity break)
	custID := &cust.ID
	rules, err := s.repo.GetMatchingRules(ctx, productID, custID, jobID, quantity)
	if err != nil {
		// Rules table may not exist yet - fall through to tier pricing
		rules = nil
	}

	if winner, finalPrice, details, ok := selectRule(rules, basePrice); ok {
		source := SourceRetail
		switch winner.RuleType {
		case RuleTypeJobOverride:
			source = SourceJobOverride
		case RuleTypePromotional:
			source = SourcePromotional
		case RuleTypeQuantityBreak:
			source = SourceQuantityBreak
		}

		discountPct := 0.0
		if basePrice > 0 {
			discountPct = (basePrice - finalPrice) / basePrice * 100
		}

		return CalculatedPrice{
			ProductID:     productID,
			OriginalPrice: basePrice,
			FinalPrice:    math.Round(finalPrice*100) / 100,
			DiscountPct:   math.Round(discountPct*100) / 100,
			Source:        source,
			Details:       details,
		}, nil
	}

	// 5a. Check Category-Based Pricing (if enabled)
	if s.catSvc != nil {
		resolved, catErr := s.catSvc.ResolveEffectivePrice(ctx, cust.ID, string(cust.Tier), productID)
		if catErr == nil && resolved != nil && resolved.Rule != nil {
			finalPrice := s.catSvc.ApplyRule(resolved.Rule, basePrice, resolved.CostPrice)

			// Margin floor protection
			if resolved.Rule.MarginFloorPct != nil && basePrice > 0 {
				minPrice := basePrice * (1 - *resolved.Rule.MarginFloorPct/100)
				if finalPrice < minPrice {
					finalPrice = minPrice
				}
			}

			discountPct := 0.0
			if basePrice > 0 {
				discountPct = (basePrice - finalPrice) / basePrice * 100
			}

			catSource := SourceCategoryTier
			if resolved.Rule.TargetType == TargetTypeAccount {
				catSource = SourceCategoryAccount
			}

			return CalculatedPrice{
				ProductID:     productID,
				OriginalPrice: basePrice,
				FinalPrice:    math.Round(finalPrice*100) / 100,
				DiscountPct:   math.Round(discountPct*100) / 100,
				Source:        catSource,
				Details:       fmt.Sprintf("%s (%s)", resolved.Rule.CategoryName, resolved.MatchType),
			}, nil
		}
	}

	// 5b. Check Price Level (Tier) — hardcoded fallback when category pricing is disabled or no rule matches
	multiplier := 1.0
	details := ""
	source := SourceRetail

	if cust.PriceLevel != nil {
		multiplier = cust.PriceLevel.Multiplier
		details = fmt.Sprintf("%s (Level)", cust.PriceLevel.Name)
		source = SourceTier
	} else if cust.Tier != "" && cust.Tier != customer.TierRetail {
		switch cust.Tier {
		case customer.TierSilver:
			multiplier = 0.90
			details = "Silver Tier (10%)"
		case customer.TierGold:
			multiplier = 0.85
			details = "Gold Tier (15%)"
		case customer.TierPlatinum:
			multiplier = 0.80
			details = "Platinum Tier (20%)"
		}
		if multiplier < 1.0 {
			source = SourceTier
		}
	}

	if source == SourceTier {
		final := basePrice * multiplier
		discountPct := (1 - multiplier) * 100
		return CalculatedPrice{
			ProductID:     productID,
			OriginalPrice: basePrice,
			FinalPrice:    final,
			DiscountPct:   math.Round(discountPct*100) / 100,
			Source:        SourceTier,
			Details:       details,
		}, nil
	}

	// 6. Retail
	return CalculatedPrice{
		ProductID:     productID,
		OriginalPrice: basePrice,
		FinalPrice:    basePrice,
		DiscountPct:   0,
		Source:        SourceRetail,
		Details:       "Base Retail Price",
	}, nil
}

// applyRule computes the price `rule` produces for basePrice, including margin
// floor protection, and the human-readable detail string that goes with it.
//
// ok is false when the rule names no pricing action at all (no fixed price, no
// discount, no markup). Such a rule is inert: the waterfall skips it and looks
// at the next candidate, which is what the original inline loop did with its
// `continue`.
func applyRule(rule PricingRule, basePrice float64) (finalPrice float64, details string, ok bool) {
	details = rule.Name

	switch {
	case rule.FixedPrice != nil:
		finalPrice = *rule.FixedPrice
	case rule.DiscountPct != nil:
		finalPrice = basePrice * (1 - *rule.DiscountPct/100)
	case rule.MarkupPct != nil:
		finalPrice = basePrice * (1 + *rule.MarkupPct/100)
	default:
		return 0, "", false
	}

	// Margin floor protection.
	if rule.MarginFloorPct != nil && basePrice > 0 {
		minPrice := basePrice * (1 - *rule.MarginFloorPct/100)
		if finalPrice < minPrice {
			finalPrice = minPrice
			details = fmt.Sprintf("%s (margin floor applied)", details)
		}
	}

	return finalPrice, details, true
}

// selectRule picks which of the candidate rules GetMatchingRules returned
// actually prices the line, and returns the price and details it produces.
//
// The candidates arrive ordered `priority DESC, rule_type ASC`, and BOTH keys
// carry meaning that has to survive:
//
//   - priority is the dealer's override lever. A rule at priority 50 is meant
//     to beat everything below it, full stop, even when something cheaper
//     exists. Ranking globally by price would silently delete that lever.
//   - rule_type ASC happens to sort JOB_OVERRIDE < PROMOTIONAL <
//     QUANTITY_BREAK, which is exactly steps 2, 3 and 4 of the documented
//     waterfall. Within one priority band the earlier step wins.
//
// What the ordering does NOT rank is two rules that agree on both keys, and
// that is the whole bug: two QUANTITY_BREAK rules on the same product at the
// same priority tie, the tie is broken by whatever order Postgres returns the
// rows in, and taking the first one means a contractor buying 100 can be
// charged the 20+ price. The deeper rung is unreachable, not merely unlikely.
//
// So the tie — and ONLY the tie — is broken here. The leading candidate fixes
// the (priority, rule_type) band; if that band is QUANTITY_BREAK, every other
// candidate in the same band is considered and the best one wins. Everything
// outside the band is left alone, so priority still overrides and job/promo
// rules still outrank breaks.
//
// "Best" inside the band is the price the customer pays, lowest first, with a
// deeper rung breaking a price tie. The intent of a quantity break is that
// buying more costs less per unit, so on any coherently configured ladder the
// deepest applicable rung IS the cheapest and the two readings agree. They only
// diverge on a ladder someone has misconfigured — a 100+ rung priced above the
// 20+ rung sitting next to it — and there we deliberately refuse to charge the
// larger buyer more than the smaller one. Handing the qty-100 order to the
// shallower-but-cheaper rung is the same price the customer would get by
// splitting the order in five, so honouring it costs the dealer nothing that
// the dealer was not already going to lose, and it keeps the ladder
// VolumeBreaks advertises consistent with the engine that bills it.
func selectRule(rules []PricingRule, basePrice float64) (PricingRule, float64, string, bool) {
	lead := -1
	var bestPrice float64
	var bestDetails string
	for i, r := range rules {
		price, details, ok := applyRule(r, basePrice)
		if !ok {
			continue // inert rule — no pricing action defined
		}
		lead, bestPrice, bestDetails = i, price, details
		break
	}
	if lead < 0 {
		return PricingRule{}, 0, "", false
	}

	best := rules[lead]
	if best.RuleType != RuleTypeQuantityBreak {
		return best, bestPrice, bestDetails, true
	}

	for _, r := range rules[lead+1:] {
		if r.RuleType != RuleTypeQuantityBreak || r.Priority != best.Priority {
			continue // different band — priority and the waterfall order decide
		}
		price, details, ok := applyRule(r, basePrice)
		if !ok {
			continue
		}
		if price < bestPrice || (price == bestPrice && r.MinQuantity > best.MinQuantity) {
			best, bestPrice, bestDetails = r, price, details
		}
	}

	return best, bestPrice, bestDetails, true
}

func (s *Service) CreateRule(ctx context.Context, rule *PricingRule) error {
	return s.repo.CreateRule(ctx, rule)
}

func (s *Service) ListRules(ctx context.Context) ([]PricingRule, error) {
	return s.repo.ListRules(ctx)
}
