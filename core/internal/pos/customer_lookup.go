// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"fmt"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// The counter's account standing words (the screen words them): the
// customer's credit as the charge tender will read it.
const (
	StandingGood     = "good"
	StandingNoLimit  = "no_limit"
	StandingNoCredit = "no_credit"
	StandingOver     = "over_limit"
)

// standOf fills the account's available credit and its standing from the
// credit limit and the open receivable the credit check reads (ADR 0005
// section 5.3): no limit is unlimited, a limit of zero is no credit, and an
// open receivable over the limit is over it.
func standOf(c *CounterCustomer, open int64) {
	c.OpenReceivableCents = httpx.Cents(open)
	switch {
	case c.CreditLimitCents == nil:
		c.AvailableCreditCents = nil
		c.Standing = StandingNoLimit
	case int64(*c.CreditLimitCents) <= 0:
		zero := httpx.Cents(0)
		c.AvailableCreditCents = &zero
		c.Standing = StandingNoCredit
	default:
		avail := httpx.Cents(int64(*c.CreditLimitCents) - open)
		c.AvailableCreditCents = &avail
		c.Standing = StandingGood
		if avail < 0 {
			c.Standing = StandingOver
		}
	}
}

// FindCustomers is the counter's customer lookup: by name or account number,
// the account's standing beside each. The reach is the repository's: every
// customer to a user (ruling A at the counter), a bound key's pin to a key.
func (s *Service) FindCustomers(ctx context.Context, q string) ([]CounterCustomer, error) {
	q = strings.TrimSpace(q)
	if len(q) < 2 {
		return []CounterCustomer{}, nil
	}
	found, err := s.repo.FindCounterCustomers(ctx, q, 10)
	if err != nil {
		return nil, err
	}
	for i := range found {
		open, err := s.repo.OpenReceivableCents(ctx, found[i].ID)
		if err != nil {
			return nil, err
		}
		standOf(&found[i], open)
	}
	return found, nil
}

// AttachCustomerIn names who the sale is for and what the account asks the
// sale to carry.
type AttachCustomerIn struct {
	CustomerID *uuid.UUID
	CustomerPO string
	JobID      *uuid.UUID
	// BuyerContactID is the roster buyer the sale is for; the account may
	// ask for one on every sale on account.
	BuyerContactID *uuid.UUID
	// ShipToID and DeliveryType are the delivered sale's answer (CP-10):
	// a ship to names where the goods go, so the tax is sourced to its
	// zone instead of the branch.
	ShipToID     *uuid.UUID
	DeliveryType string // pickup or delivery; empty leaves the sale as it is
}

// namedCustomer is the counter's one rule for which customers a caller may
// name: ruling A (a user names any yard's customer; a branch bound key only its
// pin's, the same predicate the lookup filters by), a hidden or inactive
// customer answered with the module's 404 and nothing
// written. The attach, the create, the offline sync and the completion all
// name a customer through it, so a change of rule is a change here only.
func (s *Service) namedCustomer(ctx context.Context, id uuid.UUID) (*CounterCustomer, CustomerFacts, error) {
	c, found, err := s.repo.CounterCustomerByID(ctx, id)
	if err != nil {
		return nil, CustomerFacts{}, err
	}
	if !found {
		return nil, CustomerFacts{}, httpx.NotFound("customer not found")
	}
	facts, err := s.repo.CustomerFacts(ctx, id)
	if err != nil {
		return nil, CustomerFacts{}, err
	}
	return c, facts, nil
}

// AttachCustomer names the sale's customer, purchase order and job. The
// customer may be named or changed while the sale is open, lines rung or not
// (CP-10: the account is often named at Pay, and an exempt account must be
// attachable there). The lines price for the customer, so when the customer
// moves on a sale that already has lines every line is priced again for the
// customer now standing on the sale (a price the cashier overrode keeps its
// own price and its author), and the totals follow: the tax honours the
// attached customer's exemption and detaching restores the walk-in's prices
// and its tax.
func (s *Service) AttachCustomer(ctx context.Context, saleID uuid.UUID, in AttachCustomerIn, actor string) (*Sale, error) {
	if err := s.holdSaleBranch(ctx, saleID); err != nil {
		return nil, err
	}
	if len(in.CustomerPO) > 100 {
		return nil, invalid("customer_po", "must be at most 100 characters")
	}
	if in.CustomerID == nil && (in.CustomerPO != "" || in.JobID != nil || in.BuyerContactID != nil) {
		return nil, invalid("customer_id", "name the customer before a purchase order, a job or a buyer")
	}
	// A ship to is the named customer's own and rides a delivery (CP-10).
	if in.ShipToID != nil {
		if in.CustomerID == nil {
			return nil, invalid("customer_id", "a ship to needs the customer named: the walk-in customer carries none")
		}
		ok, err := s.repo.ShipToBelongsTo(ctx, *in.ShipToID, *in.CustomerID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, invalid("ship_to_id", "no such ship to for this customer")
		}
	}
	// walkIn is the walk-in customer's id when the sale goes back to one, for
	// the reprice the detach owes (the engine prices the walk-in's tier).
	walkIn := uuid.Nil
	currency := ""
	if in.CustomerID != nil {
		c, facts, err := s.namedCustomer(ctx, *in.CustomerID)
		if err != nil {
			return nil, err
		}
		if in.JobID != nil {
			ok := false
			for _, j := range c.Jobs {
				if j.ID == *in.JobID {
					ok = true
				}
			}
			if !ok {
				return nil, invalid("job_id", "is not an open job of this customer")
			}
		}
		if in.BuyerContactID != nil {
			if _, ok, err := s.repo.BuyerOf(ctx, *in.CustomerID, *in.BuyerContactID); err != nil {
				return nil, err
			} else if !ok {
				return nil, invalid("buyer_contact_id",
					fmt.Sprintf("is not an authorized buyer of %s: pick one from the account's buyer roster", facts.Name))
			}
		}
		currency = facts.Currency
	} else {
		id, def, err := s.repo.WalkInCustomer(ctx)
		if err != nil {
			return nil, err
		}
		currency = def
		walkIn = id
	}
	var out *Sale
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, saleID); err != nil {
			return err
		}
		sale, err := s.repo.GetSale(ctx, saleID)
		if err != nil {
			return err
		}
		if sale.Status != StatusOpen && sale.Status != StatusHeld {
			return httpx.InvalidStateTransition(fmt.Sprintf("cannot change the customer of a %s sale", sale.Status.Status()))
		}
		var po *string
		if in.CustomerPO != "" {
			p := in.CustomerPO
			po = &p
		}
		if err := s.repo.SetSaleAccount(ctx, saleID, in.CustomerID, currency, po, in.JobID); err != nil {
			return err
		}
		if err := s.repo.SetSaleBuyer(ctx, saleID, in.BuyerContactID); err != nil {
			return err
		}
		// The delivery answer follows the customer now standing on the sale:
		// a detach clears it, a plain re attach resets to a pickup, and a
		// request naming the customer's ship to sets the delivery it names
		// (a ship to with no delivery type is a delivery: it names where the
		// goods go). The departed customer's ship to never stays the sale's
		// place of receipt.
		var newShipTo *uuid.UUID
		newDelivery := ""
		switch {
		case in.CustomerID == nil:
		case in.ShipToID != nil:
			newShipTo, newDelivery = in.ShipToID, in.DeliveryType
			if newDelivery == "" {
				newDelivery = "delivery"
			}
		case in.DeliveryType != "":
			newDelivery = in.DeliveryType
		}
		if err := s.repo.SetSaleDelivery(ctx, saleID, newShipTo, newDelivery); err != nil {
			return err
		}
		// the tax follows the delivery answer too: a changed ship to or a
		// switch between pickup and delivery moves the zone, so the stored
		// totals are rebuilt the same way a moved customer rebuilds them
		deliveryChanged := !sameShipTo(sale.ShipToID, newShipTo) || !sameDelivery(sale.DeliveryType, newDelivery)
		sale.ShipToID, sale.DeliveryType = newShipTo, newDelivery
		// the lines and the totals follow the customer now standing on the
		// sale, when the customer moved on a sale with lines: the engine
		// prices for that customer (the account's level, the walk-in's
		// retail), and the tax honours its exemption
		customerMoved := (sale.CustomerID == nil) != (in.CustomerID == nil) ||
			(sale.CustomerID != nil && in.CustomerID != nil && *sale.CustomerID != *in.CustomerID)
		if customerMoved || deliveryChanged {
			lines, err := s.repo.GetLines(ctx, saleID)
			if err != nil {
				return err
			}
			if len(lines) > 0 {
				sale.CustomerID = in.CustomerID
				if in.CustomerID == nil {
					sale.WalkInID = walkIn
				}
				if customerMoved {
					if err := s.repriceSaleLines(ctx, sale, lines, actor); err != nil {
						return err
					}
				}
				if err := s.recalcTotals(ctx, sale); err != nil {
					return err
				}
			}
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "pos.sale.customer_attached", EntityType: "pos_transaction", EntityID: saleID, UserID: actor,
				Changes: map[string]any{"customer_id": in.CustomerID, "customer_po": in.CustomerPO, "job_id": in.JobID,
					"buyer_contact_id": in.BuyerContactID},
			}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		out, err = s.GetSale(ctx, saleID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// repriceSaleLines prices every top-level line of the sale again, for the
// customer now standing on it: the engine's customer level is a rung of every
// line's price, so the account is charged its own price and the walk-in its
// own. A price the cashier overrode (or typed on a charge line) keeps its
// price and its author, the way a quantity change keeps them; a kit's
// components follow their kit; a text line carries no price to follow.
func (s *Service) repriceSaleLines(ctx context.Context, sale *Sale, lines []salesdoc.Line, actor string) error {
	for i := range lines {
		old := &lines[i]
		if old.ParentLineID != nil || old.LineType == salesdoc.LineText || old.Quantity == nil {
			continue
		}
		line, _, err := s.priceLine(ctx, sale, parsedFromLine(old, *old.Quantity), actor)
		if err != nil {
			return err
		}
		line.ID = old.ID
		// whoever set the price keeps the credit for it: naming the account
		// does not make the cashier the author of a manager's override
		line.PriceAdjustedBy = old.PriceAdjustedBy
		family, err := s.explodeAndExtend(ctx, []salesdoc.Line{line})
		if err != nil {
			return err
		}
		// the line and its components take the places the old family held
		var places []int
		for j := range lines {
			if lines[j].ID == old.ID || (lines[j].ParentLineID != nil && *lines[j].ParentLineID == old.ID) {
				places = append(places, lines[j].Position)
			}
		}
		for j := range family {
			if j < len(places) {
				family[j].Position = places[j]
			} else {
				family[j].Position = places[len(places)-1] + (j - len(places) + 1)
			}
		}
		if err := s.reAddLine(ctx, sale.ID, old, family); err != nil {
			return err
		}
	}
	return nil
}

// ListRegisters is the counter's register list: the registers the caller's
// branch reach holds. The wall is the repository's.
func (s *Service) ListRegisters(ctx context.Context) ([]CounterRegister, error) {
	regs, err := s.repo.ListRegisters(ctx)
	if err != nil {
		return nil, err
	}
	for i := range regs {
		regs[i].CardTerminal = s.cardTerminalAt(regs[i].BranchID)
	}
	return regs, nil
}

func sameShipTo(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// sameDelivery compares delivery answers as the column stores them: an empty
// answer and a pickup are one answer, and case does not matter.
func sameDelivery(a, b string) bool {
	norm := func(v string) string {
		if v == "" {
			return "pickup"
		}
		return strings.ToLower(v)
	}
	return norm(a) == norm(b)
}
