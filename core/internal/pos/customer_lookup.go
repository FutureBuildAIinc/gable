// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"fmt"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
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
}

// namedCustomer is the counter's one rule for which customers a caller may
// name: the customer wall (the same predicate the lookup filters by), a
// hidden or inactive customer answered with the module's 404 and nothing
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
// customer is named before the first line, because the lines price for the
// customer (price level, tax exemption, currency); the purchase order and
// the job may change while the sale is open.
func (s *Service) AttachCustomer(ctx context.Context, saleID uuid.UUID, in AttachCustomerIn, actor string) (*Sale, error) {
	if err := s.holdSaleBranch(ctx, saleID); err != nil {
		return nil, err
	}
	if len(in.CustomerPO) > 100 {
		return nil, invalid("customer_po", "must be at most 100 characters")
	}
	if in.CustomerID == nil && (in.CustomerPO != "" || in.JobID != nil) {
		return nil, invalid("customer_id", "name the customer before a purchase order or a job")
	}
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
		currency = facts.Currency
	} else {
		_, def, err := s.repo.WalkInCustomer(ctx)
		if err != nil {
			return nil, err
		}
		currency = def
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
		same := (sale.CustomerID == nil && in.CustomerID == nil) ||
			(sale.CustomerID != nil && in.CustomerID != nil && *sale.CustomerID == *in.CustomerID)
		if !same {
			lines, err := s.repo.GetLines(ctx, saleID)
			if err != nil {
				return err
			}
			if len(lines) > 0 {
				return httpx.InvalidStateTransition(
					"the customer is named before the first line: the lines price for the customer, so start a new sale to change it")
			}
		}
		var po *string
		if in.CustomerPO != "" {
			p := in.CustomerPO
			po = &p
		}
		if err := s.repo.SetSaleAccount(ctx, saleID, in.CustomerID, currency, po, in.JobID); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "pos.sale.customer_attached", EntityType: "pos_transaction", EntityID: saleID, UserID: actor,
				Changes: map[string]any{"customer_id": in.CustomerID, "customer_po": in.CustomerPO, "job_id": in.JobID},
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

// ListRegisters is the counter's register list: the registers the caller's
// branch reach holds. The wall is the repository's.
func (s *Service) ListRegisters(ctx context.Context) ([]CounterRegister, error) {
	return s.repo.ListRegisters(ctx)
}
