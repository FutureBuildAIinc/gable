// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/units"
	"github.com/google/uuid"
)

// Request is one line's pricing question (ADR 0006 section 5.3): the
// customer (zero for an anonymous read, which runs rungs 5 and 6), the
// product, the branch, the line's quantity and unit, and the job. An empty
// UOM takes the product's default sale unit.
type Request struct {
	CustomerID uuid.UUID
	ProductID  uuid.UUID
	BranchID   *uuid.UUID
	Quantity   httpx.Quantity
	UOM        string
	JobID      *uuid.UUID
	// On is the branch's business date the cost side reads at; zero takes
	// the engine's now.
	On time.Time
	// SkipRules skips rung 2 (promotions, quantity breaks and job prices)
	// and leaves every other rung as it stands: the offline price sheet's
	// answer (ADR 0011 section 5.4). The live path never sets it.
	SkipRules bool
	// Level, with no customer named, answers the level's own resolution:
	// its category rules, then its price, then the branch base, then the
	// base, never an account's contract or rules. It is the offline sheet's
	// fallback row for an account the sheet carries no row of its own for
	// (ADR 0011 section 5.4). The live path never sets it.
	Level *PricingProgram
}

// Resolved is the engine's answer: the price and its unit, the line's
// conversion pair for the request's unit and the price's unit (section 3.3),
// where the price came from, the rule or row that won (null for derived and
// base), and the reference price with its unit, so an override shows against
// it.
type Resolved struct {
	// UOM is the request's effective unit: the one sent, or the product's
	// default sale unit when the request named none.
	UOM               string
	UnitPrice         httpx.Price
	PriceUOM          string
	UOMQty            httpx.Quantity
	PriceUOMQty       httpx.Quantity
	PriceBasis        PricingSource
	Details           string
	RuleID            *uuid.UUID
	ReferencePrice    httpx.Price
	ReferencePriceUOM string
}

// WithRepository substitutes the resolve side reads. Optional: the service
// answers through the same Postgres database the writes use when none is
// wired.
func (s *Service) WithRepository(r ResolveRepository) *Service {
	s.resolveRepo = r
	return s
}

// resolveAnswer carries one rung's raw winner to the answer assembly: the
// exact or already rounded price, its unit, the basis, the details and the
// row or rule that won.
type resolveAnswer struct {
	price    httpx.Price
	uom      string
	basis    PricingSource
	details  string
	winnerID *uuid.UUID
}

// Resolve is the one entry point every module prices through (ADR 0006
// section 5.3). The rungs, first match wins: the contract; the pricing
// rules; the category rules by account, profile then level; the customer's
// level, explicit then derived from its basis; the branch's base; the
// product's base. Before any rung the engine computes the reference price
// (the branch's base price row when it has one, else the product's base) and
// the stocking quantity of the request.
func (s *Service) Resolve(ctx context.Context, req Request) (Resolved, error) {
	if s == nil || s.resolveRepo == nil {
		return Resolved{}, fmt.Errorf("pricing resolve repository is not wired")
	}
	if req.Level != nil && req.CustomerID != uuid.Nil {
		return Resolved{}, fmt.Errorf("a request names a customer or a level, never both")
	}
	facts, err := s.resolveRepo.ProductPricingFacts(ctx, req.ProductID)
	if err != nil {
		return Resolved{}, err
	}

	// The reference price: the product_prices row (level null, the branch)
	// if present, else the product's base, each with its own unit.
	ref := facts.BasePrice
	refUOM := facts.PriceUOM
	var refRow *ProductPrice
	if req.BranchID != nil {
		refRow, err = s.resolveRepo.ProductPrice(ctx, req.ProductID, nil, req.BranchID)
		if err != nil {
			return Resolved{}, err
		}
	}
	if refRow != nil {
		ref = refRow.UnitPrice
		refUOM = refRow.PriceUOM
	}
	refRat := ratOfPrice(ref)

	uom := req.UOM
	if uom == "" {
		uom = facts.SaleUOM
	}

	// The branch's business date the cost rung reads its vendor costs at.
	on := req.On
	if on.IsZero() {
		on = time.Now()
	}

	// The stocking quantity of the request, which quantity breaks compare
	// against (the request's quantity converted into the stocking unit,
	// exact).
	stockQty := req.Quantity
	if uom != facts.StockUOM {
		uRow, err := s.resolveRepo.UnitSetRow(ctx, req.ProductID, uom)
		if err != nil {
			return Resolved{}, err
		}
		if uRow == nil {
			return Resolved{}, fmt.Errorf("the unit %s is not a row of the product's unit set", uom)
		}
		stockQty, err = units.ConvertStock(req.Quantity, uom, facts.StockUOM, uRow.PairOf())
		if err != nil {
			return Resolved{}, err
		}
	}

	custID := &req.CustomerID
	if req.CustomerID == uuid.Nil {
		custID = nil
	}

	// Rung 1: the contract, the branch's row before the all branch row.
	if custID != nil {
		contract, err := s.resolveRepo.ContractForBranch(ctx, *custID, req.ProductID, req.BranchID)
		if err != nil {
			return Resolved{}, err
		}
		if contract != nil {
			return s.assemble(ctx, req, uom, facts, ref, refUOM, resolveAnswer{
				price:    contract.ContractPrice,
				uom:      contract.PriceUOM,
				basis:    SourceContract,
				details:  "Specific Contract Price",
				winnerID: &contract.ID,
			})
		}
	}

	// Rung 2: the pricing rules (job override, promotional, quantity break),
	// by today's selectRule. A fixed price is in its own unit; a percent off
	// or a markup acts on the reference price, in the reference's unit.
	var rules []PricingRule
	if !req.SkipRules {
		rules, err = s.repo.GetMatchingRules(ctx, req.ProductID, custID, req.JobID, stockQty)
		if err != nil {
			rules = nil
		}
	}
	// The tie among same band quantity break rules compares exact rationals
	// per stocking unit (ADR 0006 section 5.3): two fixed prices may sit in
	// different units, and only the per stocking unit rational compares them.
	if winner, priceRat, details, ok := s.selectRuleResolved(ctx, req.ProductID, rules, refRat, refUOM); ok {
		ans := resolveAnswer{uom: refUOM, details: details, winnerID: &winner.ID}
		if winner.FixedPrice != nil {
			ans.price = *winner.FixedPrice
			// A fixed price is per its own unit; a row written before the
			// units of C3-2A (the column still null) is per the product's
			// price unit.
			ans.uom = winner.PriceUOM
			if ans.uom == "" {
				ans.uom = facts.PriceUOM
			}
		} else {
			ans.price = priceOfRat(priceRat)
		}
		switch winner.RuleType {
		case RuleTypeJobOverride:
			ans.basis = SourceJobOverride
		case RuleTypePromotional:
			ans.basis = SourcePromotional
		default:
			ans.basis = SourceQuantityBreak
		}
		return s.assemble(ctx, req, uom, facts, ref, refUOM, ans)
	}

	// Rung 3: the category rules, account, then the profile's, then the
	// level's, each exact category then ancestor. A level's own resolution
	// names no account and no profile, so only the level's rules match.
	if custID != nil || req.Level != nil {
		program, err := s.programOf(ctx, req, custID)
		if err != nil {
			return Resolved{}, err
		}
		if program != nil {
			ans, ok, err := s.categoryAnswer(ctx, program, on, req, facts, refRat, refUOM)
			if err != nil {
				return Resolved{}, err
			}
			if ok {
				return s.assemble(ctx, req, uom, facts, ref, refUOM, ans)
			}
		}
	}

	// Rung 4: the customer's level, explicit then derived. Rungs 5 and 6
	// fall through when the customer is anonymous, when its profile or level
	// is inactive, and when a cost basis has no cost to read.
	if custID != nil || req.Level != nil {
		program, err := s.programOf(ctx, req, custID)
		if err != nil {
			return Resolved{}, err
		}
		if program != nil && program.LevelActive {
			ans, ok, err := s.levelAnswer(ctx, program, on, req, facts, refRat, refUOM)
			if err != nil {
				return Resolved{}, err
			}
			if ok {
				return s.assemble(ctx, req, uom, facts, ref, refUOM, ans)
			}
		}
	}

	// Rung 5: the branch base (level null, the branch). The reference was
	// read from that row at the top, so it answers as stored.
	if refRow != nil {
		return s.assemble(ctx, req, uom, facts, ref, refUOM, resolveAnswer{
			price:   refRow.UnitPrice,
			uom:     refRow.PriceUOM,
			basis:   SourceBranch,
			details: "Branch Price",
		})
	}

	// Rung 6: the base.
	return s.assemble(ctx, req, uom, facts, ref, refUOM, resolveAnswer{
		price:   facts.BasePrice,
		uom:     facts.PriceUOM,
		basis:   SourceBase,
		details: "Base Retail Price",
	})
}

// programOf is the pricing program rungs 3 and 4 read: the customer's own,
// or, for a level's own resolution, the level with no account and no
// profile, so no account or profile rule can match.
func (s *Service) programOf(ctx context.Context, req Request, custID *uuid.UUID) (*PricingProgram, error) {
	if req.Level != nil {
		p := *req.Level
		p.ProfileID = uuid.Nil
		return &p, nil
	}
	return s.resolveRepo.CustomerProgram(ctx, *custID)
}

// CustomerProgram is the customer's pricing program (its profile and
// level), nil when it holds none: the offline sheet keys an account's
// fallback rows by its level.
func (s *Service) CustomerProgram(ctx context.Context, customerID uuid.UUID) (*PricingProgram, error) {
	if s == nil || s.resolveRepo == nil {
		return nil, fmt.Errorf("pricing resolve repository is not wired")
	}
	return s.resolveRepo.CustomerProgram(ctx, customerID)
}

// categoryAnswer runs the category rung: account, then profile, then level,
// each exact category then ancestor. ok is false when no rule matched. A
// markdown acts on the reference price in its unit; a fixed price is per the
// product's own price unit; a markup or margin on the cost answers in the
// stocking unit when the cost is positive, and otherwise, as today, a markup
// acts on the reference price and a margin answers the reference unchanged.
func (s *Service) categoryAnswer(ctx context.Context, program *PricingProgram, on time.Time, req Request, facts *ProductPricingFacts, refRat *big.Rat, refUOM string) (resolveAnswer, bool, error) {
	if s.catSvc == nil {
		return resolveAnswer{}, false, nil
	}
	rule, matchType, err := s.catSvc.ResolveProgramRule(ctx, req.CustomerID, program.ProfileID, program.LevelID, req.ProductID)
	if err != nil {
		return resolveAnswer{}, false, err
	}
	if rule == nil {
		return resolveAnswer{}, false, nil
	}
	costPrice, err := s.catSvc.ProductCost(ctx, req.ProductID)
	if err != nil {
		return resolveAnswer{}, false, err
	}
	basis := SourceCategoryProfile
	if rule.TargetType == TargetTypeAccount {
		basis = SourceCategoryAccount
	} else if rule.TargetType == TargetTypeLevel {
		basis = SourceCategoryLevel
	}
	ans := resolveAnswer{basis: basis, details: fmt.Sprintf("%s (%s)", rule.CategoryName, matchType), winnerID: &rule.ID}
	price := s.catSvc.ApplyRule(rule, refRat, ratOfPrice(costPrice))
	switch rule.RuleType {
	case CategoryRuleFixed:
		ans.uom = facts.PriceUOM
	case CategoryRuleMarkup, CategoryRuleMargin:
		if ratOfPrice(costPrice).Sign() > 0 {
			ans.uom = facts.StockUOM
		} else {
			ans.uom = refUOM
		}
	default:
		ans.uom = refUOM
	}
	if rule.MarginFloorPct != nil && refRat.Sign() > 0 {
		// The floor lives in the reference's unit and the price may not
		// (a cost markup answers in the stocking unit, a fixed rule price
		// in its own), so the comparison converts both to an exact rational
		// per stocking unit; the winner keeps its own value and unit
		// (ADR 0006 section 5.3).
		floor := applyMarkdown(refRat, ratOfQuantity(*rule.MarginFloorPct))
		pricePerStock, err := s.perStock(ctx, req.ProductID, priceOfRat(price), ans.uom)
		if err != nil {
			return resolveAnswer{}, false, err
		}
		floorPerStock, err := s.perStock(ctx, req.ProductID, priceOfRat(floor), refUOM)
		if err != nil {
			return resolveAnswer{}, false, err
		}
		if pricePerStock.Cmp(floorPerStock) < 0 {
			price = floor
			ans.uom = refUOM
			ans.details = fmt.Sprintf("%s (margin floor applied)", ans.details)
		}
	}
	ans.price = priceOfRat(price)
	return ans, true, nil
}

// levelAnswer runs the customer level rung: the profile's level's explicit
// price at the branch, else its explicit price at every branch, else the
// price derived from the level's basis. ok is false when the rung answers
// nothing and the next rung takes over (a cost basis with no cost to read,
// named in the reason).
func (s *Service) levelAnswer(ctx context.Context, program *PricingProgram, on time.Time, req Request, facts *ProductPricingFacts, refRat *big.Rat, refUOM string) (resolveAnswer, bool, error) {
	details := fmt.Sprintf("%s (Level)", program.LevelName)
	adjust := ratOfQuantity(program.LevelAdjust)

	if req.BranchID != nil {
		p, err := s.resolveRepo.ProductPrice(ctx, req.ProductID, &program.LevelID, req.BranchID)
		if err != nil {
			return resolveAnswer{}, false, err
		}
		if p != nil {
			return resolveAnswer{price: p.UnitPrice, uom: p.PriceUOM, basis: SourceLevel, details: details, winnerID: &p.ID}, true, nil
		}
	}
	p, err := s.resolveRepo.ProductPrice(ctx, req.ProductID, &program.LevelID, nil)
	if err != nil {
		return resolveAnswer{}, false, err
	}
	if p != nil {
		return resolveAnswer{price: p.UnitPrice, uom: p.PriceUOM, basis: SourceLevel, details: details, winnerID: &p.ID}, true, nil
	}

	// Derived. A derived price is computed exactly and rounded once to scale
	// 4 (R4.2); a level whose derivation yields exactly the reference price
	// (a zero percent BASE level) still answers level.
	switch program.LevelBasis {
	case "BASE":
		return resolveAnswer{price: priceOfRat(applyMarkupPercent(refRat, adjust)), uom: refUOM, basis: SourceLevel, details: details}, true, nil
	case "AVERAGE_COST":
		if facts.AverageUnitCost > 0 {
			return resolveAnswer{price: priceOfRat(applyMarkupPercent(ratOfPrice(facts.AverageUnitCost), adjust)), uom: facts.StockUOM, basis: SourceLevel, details: details}, true, nil
		}
		return resolveAnswer{}, false, nil
	case "REPLACEMENT_COST":
		if facts.PrimaryVendorID != nil {
			cost, err := s.resolveRepo.VendorCost(ctx, *facts.PrimaryVendorID, req.ProductID, req.BranchID, nil, on)
			if err != nil {
				return resolveAnswer{}, false, err
			}
			// The hold is lifted (C3-2B): a replacement cost answers in the
			// vendor cost's own purchase unit, whatever unit that is, and
			// the line's pair carries the conversion.
			if cost != nil && cost.UnitCost > 0 {
				return resolveAnswer{price: priceOfRat(applyMarkupPercent(ratOfPrice(cost.UnitCost), adjust)), uom: cost.PurchaseUOM, basis: SourceLevel, details: details}, true, nil
			}
		}
		// Falling back to the average cost, which the AVERAGE_COST arm
		// above answers when the product carries one.
		if facts.AverageUnitCost > 0 {
			return resolveAnswer{price: priceOfRat(applyMarkupPercent(ratOfPrice(facts.AverageUnitCost), adjust)), uom: facts.StockUOM,
				basis: SourceLevel, details: details + " (replacement cost unavailable: average cost)"}, true, nil
		}
		return resolveAnswer{}, false, nil
	}
	return resolveAnswer{}, false, nil
}

// assemble resolves the answer's conversion pair for the request's unit and
// the price's unit, and returns the engine's answer.
func (s *Service) assemble(ctx context.Context, req Request, uom string, facts *ProductPricingFacts, ref httpx.Price, refUOM string, ans resolveAnswer) (Resolved, error) {
	if int64(ans.price) > int64(httpx.QuantityMax) || int64(ans.price) < -int64(httpx.QuantityMax) {
		return Resolved{}, fmt.Errorf("a price beyond 99999999.9999 cannot be answered")
	}
	pair := units.Pair{A: httpx.Quantity(10_000), B: httpx.Quantity(10_000)}
	if ans.uom != uom {
		uRow, err := s.resolveRepo.UnitSetRow(ctx, req.ProductID, uom)
		if err != nil {
			return Resolved{}, err
		}
		pRow, err := s.resolveRepo.UnitSetRow(ctx, req.ProductID, ans.uom)
		if err != nil {
			return Resolved{}, err
		}
		if uRow == nil {
			return Resolved{}, fmt.Errorf("the unit %s is not a row of the product's unit set", uom)
		}
		if pRow == nil {
			return Resolved{}, fmt.Errorf("the price unit %s is not a row of the product's unit set", ans.uom)
		}
		up, err := units.ResolveLinePair(uRow.PairOf(), pRow.PairOf())
		if err != nil {
			return Resolved{}, err
		}
		pair = up
	}
	return Resolved{
		UOM:               uom,
		UnitPrice:         ans.price,
		PriceUOM:          ans.uom,
		UOMQty:            pair.A,
		PriceUOMQty:       pair.B,
		PriceBasis:        ans.basis,
		Details:           ans.details,
		RuleID:            ans.winnerID,
		ReferencePrice:    ref,
		ReferencePriceUOM: refUOM,
	}, nil
}
