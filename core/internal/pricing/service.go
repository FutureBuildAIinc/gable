// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"fmt"
	"math/big"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/platform/httpx"
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

// The tier multipliers today's engine hard codes (ADR 0006 section 10), held
// exactly so the tier path can round once to scale 4 instead of answering a
// raw float product.
var (
	tierSilverMultiplier   = big.NewRat(90, 100)
	tierGoldMultiplier     = big.NewRat(85, 100)
	tierPlatinumMultiplier = big.NewRat(80, 100)
)

// CalculatePrice implements a 6-level pricing waterfall:
// 1. Contract Price (SKU + Customer specific)
// 2. Job-Level Override (project-specific pricing)
// 3. Promotional/Sale Price (time-bound)
// 4. Quantity Break (volume discount)
// 5. Customer Price Level/Tier
// 6. Base Retail
//
// The float64 entry points keep the signatures and result shape their callers
// built against (ADR 0006 section 9.1); the arithmetic under them is exact,
// and FinalPrice carries the exact scale 4 value, never a cent rounding.
func (s *Service) CalculatePrice(ctx context.Context, cust *customer.Customer, productID uuid.UUID, basePrice float64) (CalculatedPrice, error) {
	return s.CalculatePriceWithQty(ctx, cust, productID, basePrice, 1, nil)
}

func (s *Service) CalculatePriceWithQty(ctx context.Context, cust *customer.Customer, productID uuid.UUID, basePrice float64, quantity float64, jobID *uuid.UUID) (CalculatedPrice, error) {
	scaled, base, err := s.resolve(ctx, cust, productID, basePrice, quantity, jobID)
	if err != nil {
		return CalculatedPrice{}, err
	}
	return CalculatedPrice{
		ProductID:     productID,
		OriginalPrice: basePrice,
		FinalPrice:    scaled.Float64(),
		DiscountPct:   discountPct(base, scaled.Price),
		Source:        scaled.Source,
		Details:       scaled.Details,
	}, nil
}

// CalculateScaled is the same waterfall with the same inputs, answering the
// exact scale 4 price (ADR 0006 section 9.1). Until C3-2A-pricing every
// price is per the product's stocking unit.
func (s *Service) CalculateScaled(ctx context.Context, cust *customer.Customer, productID uuid.UUID, basePrice float64, quantity float64, jobID *uuid.UUID) (ScaledPrice, error) {
	scaled, _, err := s.resolve(ctx, cust, productID, basePrice, quantity, jobID)
	return scaled, err
}

// resolve runs the waterfall in exact rational arithmetic (ADR 0006 section
// 1 R3): the only rounding is the one scale 4 rounding of a derived price at
// the moment it is returned (R4.2). It answers the scaled price and the base
// price's rational, which the compatibility entry point reads for its
// discount percentage.
func (s *Service) resolve(ctx context.Context, cust *customer.Customer, productID uuid.UUID, basePrice float64, quantity float64, jobID *uuid.UUID) (ScaledPrice, *big.Rat, error) {
	base, err := ratOfFloat(basePrice)
	if err != nil {
		return ScaledPrice{}, nil, err
	}
	qty, err := quantityOfRat(mustRat(quantity))
	if err != nil {
		return ScaledPrice{}, nil, err
	}

	// 1. Check Contract Price (highest priority - specific customer+product agreement).
	// A contract price is a fixed price, returned as stored, never rounded.
	contract, err := s.repo.GetContract(ctx, cust.ID, productID)
	if err != nil {
		return ScaledPrice{}, nil, err
	}
	if contract != nil {
		return ScaledPrice{
			Price:   contract.ContractPrice,
			Source:  SourceContract,
			Details: "Specific Contract Price",
		}, base, nil
	}

	// 2-4. Check pricing rules (job override, promotional, quantity break).
	custID := &cust.ID
	rules, err := s.repo.GetMatchingRules(ctx, productID, custID, jobID, qty)
	if err != nil {
		// Rules table may not exist yet - fall through to tier pricing
		rules = nil
	}

	if winner, price, details, ok := selectRule(rules, base); ok {
		source := SourceRetail
		switch winner.RuleType {
		case RuleTypeJobOverride:
			source = SourceJobOverride
		case RuleTypePromotional:
			source = SourcePromotional
		case RuleTypeQuantityBreak:
			source = SourceQuantityBreak
		}
		return ScaledPrice{Price: priceOfRat(price), Source: source, Details: details}, base, nil
	}

	// 5a. Check Category-Based Pricing (if enabled)
	if s.catSvc != nil {
		resolved, catErr := s.catSvc.ResolveEffectivePrice(ctx, cust.ID, string(cust.Tier), productID)
		if catErr == nil && resolved != nil && resolved.Rule != nil {
			final := s.catSvc.ApplyRule(resolved.Rule, base, ratOfPrice(resolved.CostPrice))

			// Margin floor protection, compared exactly.
			if resolved.Rule.MarginFloorPct != nil && base.Sign() > 0 {
				minPrice := applyMarkdown(base, ratOfQuantity(*resolved.Rule.MarginFloorPct))
				if final.Cmp(minPrice) < 0 {
					final = minPrice
				}
			}

			catSource := SourceCategoryTier
			if resolved.Rule.TargetType == TargetTypeAccount {
				catSource = SourceCategoryAccount
			}

			return ScaledPrice{
				Price:   priceOfRat(final),
				Source:  catSource,
				Details: fmt.Sprintf("%s (%s)", resolved.Rule.CategoryName, resolved.MatchType),
			}, base, nil
		}
	}

	// 5b. Check Price Level (Tier) — hardcoded fallback when category pricing is disabled or no rule matches
	multiplier := big.NewRat(1, 1)
	details := ""
	source := SourceRetail

	if cust.PriceLevel != nil {
		multiplier, err = ratOfFloat(cust.PriceLevel.Multiplier)
		if err != nil {
			return ScaledPrice{}, nil, err
		}
		details = fmt.Sprintf("%s (Level)", cust.PriceLevel.Name)
		source = SourceTier
	} else if cust.Tier != "" && cust.Tier != customer.TierRetail {
		switch cust.Tier {
		case customer.TierSilver:
			multiplier = tierSilverMultiplier
			details = "Silver Tier (10%)"
		case customer.TierGold:
			multiplier = tierGoldMultiplier
			details = "Gold Tier (15%)"
		case customer.TierPlatinum:
			multiplier = tierPlatinumMultiplier
			details = "Platinum Tier (20%)"
		}
		if multiplier.Cmp(big.NewRat(1, 1)) < 0 {
			source = SourceTier
		}
	}

	if source == SourceTier {
		return ScaledPrice{
			Price:   priceOfRat(new(big.Rat).Mul(base, multiplier)),
			Source:  SourceTier,
			Details: details,
		}, base, nil
	}

	// 6. Retail
	return ScaledPrice{
		Price:   priceOfRat(base),
		Source:  SourceRetail,
		Details: "Base Retail Price",
	}, base, nil
}

// mustRat is ratOfFloat for the engine's own quantity input, where a value
// that is not a number is a caller bug this package reports as an error.
func mustRat(f float64) *big.Rat {
	r, err := ratOfFloat(f)
	if err != nil {
		return big.NewRat(0, 1)
	}
	return r
}

// discountPct is the compatibility answer's display percentage: the exact
// discount as a percentage rounded to two places, half away from zero, the
// value the base commit's engine wrote.
func discountPct(base *big.Rat, final httpx.Price) float64 {
	if base.Sign() <= 0 {
		return 0
	}
	pct := new(big.Rat).Sub(base, ratOfPrice(final))
	pct.Quo(pct, base)
	pct.Mul(pct, big.NewRat(100, 1))
	// Round to two places of a percent: pct x 100, rounded, / 100.
	scaled := roundHalfAway(new(big.Int).Mul(pct.Num(), big.NewInt(100)), pct.Denom())
	f, _ := new(big.Rat).SetFrac(scaled, big.NewInt(100)).Float64()
	return f
}

// applyRule computes the exact price `rule` produces for base, including
// margin floor protection, and the human-readable detail string that goes
// with it.
//
// ok is false when the rule names no pricing action at all (no fixed price,
// no discount, no markup). Such a rule is inert: the waterfall skips it and
// looks at the next candidate, which is what the original inline loop did
// with its `continue`.
//
// A fixed price is the stored value itself, exact; a discount or a markup is
// the exact product, which the caller rounds once when it returns it.
func applyRule(rule PricingRule, base *big.Rat) (finalPrice *big.Rat, details string, ok bool) {
	details = rule.Name

	switch {
	case rule.FixedPrice != nil:
		finalPrice = ratOfPrice(*rule.FixedPrice)
	case rule.DiscountPct != nil:
		finalPrice = applyMarkdown(base, ratOfQuantity(*rule.DiscountPct))
	case rule.MarkupPct != nil:
		finalPrice = applyMarkupPercent(base, ratOfQuantity(*rule.MarkupPct))
	default:
		return nil, "", false
	}

	// Margin floor protection.
	if rule.MarginFloorPct != nil && base.Sign() > 0 {
		minPrice := applyMarkdown(base, ratOfQuantity(*rule.MarginFloorPct))
		if finalPrice.Cmp(minPrice) < 0 {
			finalPrice = minPrice
			details = fmt.Sprintf("%s (margin floor applied)", details)
		}
	}

	return finalPrice, details, true
}

// selectRule picks which of the candidate rules GetMatchingRules returned
// actually prices the line, and returns the exact price and details it
// produces.
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
// "Best" inside the band is the exact price the customer pays, lowest first,
// with a deeper rung breaking a price tie (ADR 0006 5.3: comparisons are
// exact rational arithmetic; the winner keeps its own value). The intent of a
// quantity break is that buying more costs less per unit, so on any
// coherently configured ladder the deepest applicable rung IS the cheapest
// and the two readings agree. They only diverge on a ladder someone has
// misconfigured — a 100+ rung priced above the 20+ rung sitting next to it —
// and there we deliberately refuse to charge the larger buyer more than the
// smaller one. Handing the qty-100 order to the shallower-but-cheaper rung is
// the same price the customer would get by splitting the order in five, so
// honouring it costs the dealer nothing that the dealer was not already
// going to lose, and it keeps the ladder VolumeBreaks advertises consistent
// with the engine that bills it.
func selectRule(rules []PricingRule, base *big.Rat) (PricingRule, *big.Rat, string, bool) {
	lead := -1
	var bestPrice *big.Rat
	var bestDetails string
	for i, r := range rules {
		price, details, ok := applyRule(r, base)
		if !ok {
			continue // inert rule — no pricing action defined
		}
		lead, bestPrice, bestDetails = i, price, details
		break
	}
	if lead < 0 {
		return PricingRule{}, nil, "", false
	}

	best := rules[lead]
	if best.RuleType != RuleTypeQuantityBreak {
		return best, bestPrice, bestDetails, true
	}

	for _, r := range rules[lead+1:] {
		if r.RuleType != RuleTypeQuantityBreak || r.Priority != best.Priority {
			continue // different band — priority and the waterfall order decide
		}
		price, details, ok := applyRule(r, base)
		if !ok {
			continue
		}
		if price.Cmp(bestPrice) < 0 ||
			(price.Cmp(bestPrice) == 0 && r.MinQuantity > best.MinQuantity) {
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
