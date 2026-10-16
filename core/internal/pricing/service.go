// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// Event types the module writes to the outbox.
const (
	EventRuleCreated = "pricing_rule.created"
)

type Service struct {
	repo       Repository
	resolveRepo ResolveRepository // the resolve side reads; wire with WithRepository
	catSvc     *CategoryPricingService
	events     EventRecorder // optional; nil records nothing (unit tests)
	tx         TxRunner      // optional; nil runs each write unwrapped (unit tests)
	audits     AuditLogger   // optional; nil writes no audit rows (unit tests)
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// WithOutbox wires the recorder of the pricing rule events.
func (s *Service) WithOutbox(events EventRecorder) *Service {
	s.events = events
	return s
}

// WithTxRunner wires the transaction wrapper the rule writes use, so the
// write, its audit row and its event are one transactional fact.
func (s *Service) WithTxRunner(tx TxRunner) *Service {
	s.tx = tx
	return s
}

// WithAudit wires the audit rows of the pricing rule writes.
func (s *Service) WithAudit(a AuditLogger) *Service {
	s.audits = a
	return s
}

// inTx runs fn in one transaction when a runner is wired.
func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx == nil {
		return fn(ctx)
	}
	return s.tx.RunInTx(ctx, fn)
}

// WithCategoryPricing enables the category-aware pricing engine.
// When set, step 5 of the waterfall uses category rules instead of hardcoded tier multipliers.
func (s *Service) WithCategoryPricing(catSvc *CategoryPricingService) {
	s.catSvc = catSvc
}

// The compatibility entry points (ADR 0006 section 9.1): the signatures and
// result shapes cycle 2 built against, answered over Resolve. The customer
// argument is read for its id only; the float64 base price argument is
// ignored (Resolve reads the reference itself, the value callers pass
// today); the branch comes from the request's branch context. Under the
// stocking unit hold every answer is per the stocking unit, and an answer
// in any other unit is the adapters' assertion ErrPriceUnitNotStock,
// unreachable through the database and proved by a test with a fake
// repository.

// CalculatePrice is the one quantity call.
func (s *Service) CalculatePrice(ctx context.Context, cust *customer.Customer, productID uuid.UUID, basePrice float64) (CalculatedPrice, error) {
	return s.CalculatePriceWithQty(ctx, cust, productID, basePrice, 1, nil)
}

// CalculatePriceWithQty prices one line of any quantity.
func (s *Service) CalculatePriceWithQty(ctx context.Context, cust *customer.Customer, productID uuid.UUID, basePrice float64, quantity float64, jobID *uuid.UUID) (CalculatedPrice, error) {
	scaled, err := s.CalculateScaled(ctx, cust, productID, basePrice, quantity, jobID)
	if err != nil {
		return CalculatedPrice{}, err
	}
	return CalculatedPrice{
		ProductID:     productID,
		OriginalPrice: basePrice,
		FinalPrice:    scaled.Float64(),
		DiscountPct:   discountPct(scaled.Reference, scaled.Price),
		Source:        scaled.Source,
		Details:       scaled.Details,
	}, nil
}

// CalculateScaled is the same waterfall with the exact scale 4 answer.
func (s *Service) CalculateScaled(ctx context.Context, cust *customer.Customer, productID uuid.UUID, basePrice float64, quantity float64, jobID *uuid.UUID) (ScaledPrice, error) {
	if s.resolveRepo == nil {
		return ScaledPrice{}, fmt.Errorf("the pricing engine has no resolve repository wired")
	}
	qrat, err := ratOfFloat(quantity)
	if err != nil {
		return ScaledPrice{}, fmt.Errorf("the quantity is not a number: %w", err)
	}
	qty, err := quantityOfRat(qrat)
	if err != nil {
		return ScaledPrice{}, err
	}
	req := Request{
		ProductID: productID,
		Quantity:  qty,
		JobID:     jobID,
		BranchID:  middleware.BranchIDForQuery(ctx),
	}
	if cust != nil {
		req.CustomerID = cust.ID
	}
	res, err := s.Resolve(ctx, req)
	if err != nil {
		return ScaledPrice{}, err
	}
	// The hold: every price is per the stocking unit until C3-2B, so today's
	// callers stay right. An answer in another unit never leaves here.
	facts, err := s.resolveRepo.ProductPricingFacts(ctx, productID)
	if err != nil {
		return ScaledPrice{}, err
	}
	if res.PriceUOM != facts.StockUOM {
		return ScaledPrice{}, ErrPriceUnitNotStock
	}
	return ScaledPrice{
		Price:     res.UnitPrice,
		Source:    res.PriceBasis,
		Details:   res.Details,
		Reference: res.ReferencePrice,
	}, nil
}

// discountPct is the compatibility answer's display percentage: the exact
// discount from the reference price to the answer, as a percentage rounded
// to two places, half away from zero.
func discountPct(reference, final httpx.Price) float64 {
	ref := ratOfPrice(reference)
	if ref.Sign() <= 0 {
		return 0
	}
	pct := new(big.Rat).Sub(ref, ratOfPrice(final))
	pct.Quo(pct, ref)
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
// misconfigured (a 100+ rung priced above the 20+ rung sitting next to it)
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

// CreateRule writes a new pricing rule with its audit row and its
// pricing_rule.created event in one transaction: a failed audit or event
// write fails the create (the recipe's rule).
func (s *Service) CreateRule(ctx context.Context, rule *PricingRule) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.CreateRule(ctx, rule); err != nil {
			return err
		}
		if s.audits != nil {
			changes := map[string]any{
				"name": rule.Name, "rule_type": strings.ToLower(string(rule.RuleType)), "revision": rule.Revision,
			}
			if err := s.audits.Log(ctx, AuditEntry{
				Action: EventRuleCreated, EntityType: "pricing_rule", EntityID: rule.ID.String(), Changes: changes,
			}); err != nil {
				return err
			}
		}
		if s.events == nil {
			return nil
		}
		raw, err := json.Marshal(map[string]any{
			"name": rule.Name, "rule_type": strings.ToLower(string(rule.RuleType)), "revision": rule.Revision,
		})
		if err != nil {
			return err
		}
		return s.events.Write(ctx, outbox.Event{
			Type: EventRuleCreated, EntityType: "pricing_rule", EntityID: rule.ID, Data: raw,
		})
	})
}

func (s *Service) ListRules(ctx context.Context) ([]PricingRule, error) {
	return s.repo.ListRules(ctx)
}

// ListRulesPage is the rules list's keyset page: limit rows after the
// cursor, `created_at DESC, id DESC`. A repository fault is the caller's 500.
func (s *Service) ListRulesPage(ctx context.Context, after *RuleCursor, limit int) ([]PricingRule, error) {
	return s.repo.ListRulesPage(ctx, after, limit)
}

func (s *Service) CountRules(ctx context.Context) (int64, error) {
	return s.repo.CountRules(ctx)
}
