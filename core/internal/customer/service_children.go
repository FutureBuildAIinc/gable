// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer

import (
	"context"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// page trims the extra row a list asked for, to learn whether more follow.
func page[T any](rows []T, limit int) ([]T, bool) {
	if len(rows) > limit {
		return rows[:limit], true
	}
	return rows, false
}

// ---- contacts ----

// ListContacts is one page of a customer's contacts; an unknown customer is
// a 404, not an empty page.
func (s *Service) ListContacts(ctx context.Context, customerID uuid.UUID, f ChildFilter, wantTotal bool) (items []Contact, hasMore bool, total *int64, err error) {
	if _, err := s.Get(ctx, customerID); err != nil {
		return nil, false, nil, err
	}
	limit := f.Limit
	f.Limit = limit + 1
	rows, err := s.repo.ListContacts(ctx, customerID, f)
	if err != nil {
		return nil, false, nil, err
	}
	rows, hasMore = page(rows, limit)
	if wantTotal {
		n, err := s.repo.CountContacts(ctx, customerID, f)
		if err != nil {
			return nil, false, nil, err
		}
		total = &n
	}
	return rows, hasMore, total, nil
}

func (s *Service) GetContact(ctx context.Context, id uuid.UUID) (*Contact, error) {
	c, err := s.repo.GetContact(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	return c, nil
}

func contactFrom(d *ContactDraft, id, customerID uuid.UUID, now httpx.Timestamp) *Contact {
	return &Contact{
		ID: id, CustomerID: customerID, FirstName: d.FirstName, LastName: d.LastName,
		Title: d.Title, Email: d.Email, Phone: d.Phone, Role: d.Role,
		IsPrimary: d.IsPrimary, IsActive: d.IsActive, CanPlaceOrders: d.CanPlaceOrders,
		OrderLimitCents: d.OrderLimitCents, UpdatedAt: now,
	}
}

func (s *Service) CreateContact(ctx context.Context, customerID uuid.UUID, d *ContactDraft) (*Contact, error) {
	var out *Contact
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockCustomer(ctx, customerID); err != nil {
			return notFound(err)
		}
		cust, err := s.repo.GetCustomer(ctx, customerID)
		if err != nil {
			return notFound(err)
		}
		now := httpx.TimestampOf(s.now().UTC())
		c := contactFrom(d, uuid.New(), customerID, now)
		c.CreatedAt = now
		if err := s.repo.InsertContact(ctx, c); err != nil {
			return err
		}
		if out, err = s.repo.GetContact(ctx, c.ID); err != nil {
			return notFound(err)
		}
		return s.record(ctx, cust, EventUpdated, PartContact, map[string]any{"contact_id": out.ID, "action": "created"})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) UpdateContact(ctx context.Context, id uuid.UUID, d *ContactDraft, pre Precondition) (*Contact, error) {
	if err := needRevision(pre); err != nil {
		return nil, err
	}
	var out *Contact
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockContact(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetContact(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := pre.check(cur.Revision); err != nil {
			return err
		}
		next := contactFrom(d, id, cur.CustomerID, httpx.TimestampOf(s.now().UTC()))
		if err := s.repo.UpdateContact(ctx, next); err != nil {
			return err
		}
		if out, err = s.repo.GetContact(ctx, id); err != nil {
			return notFound(err)
		}
		cust, err := s.repo.GetCustomer(ctx, cur.CustomerID)
		if err != nil {
			return notFound(err)
		}
		return s.record(ctx, cust, EventUpdated, PartContact, map[string]any{"contact_id": id, "action": "updated"})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) DeleteContact(ctx context.Context, id uuid.UUID, pre Precondition) error {
	if err := needRevision(pre); err != nil {
		return err
	}
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockContact(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetContact(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := pre.check(cur.Revision); err != nil {
			return err
		}
		if err := s.repo.DeleteContact(ctx, id); err != nil {
			return err
		}
		cust, err := s.repo.GetCustomer(ctx, cur.CustomerID)
		if err != nil {
			return notFound(err)
		}
		return s.record(ctx, cust, EventUpdated, PartContact, map[string]any{"contact_id": id, "action": "deleted"})
	})
}

// ---- ship-tos ----

func (s *Service) ListShipTos(ctx context.Context, customerID uuid.UUID, f ChildFilter, wantTotal bool) (items []ShipTo, hasMore bool, total *int64, err error) {
	if _, err := s.Get(ctx, customerID); err != nil {
		return nil, false, nil, err
	}
	limit := f.Limit
	f.Limit = limit + 1
	rows, err := s.repo.ListShipTos(ctx, customerID, f)
	if err != nil {
		return nil, false, nil, err
	}
	rows, hasMore = page(rows, limit)
	if wantTotal {
		n, err := s.repo.CountShipTosFiltered(ctx, customerID, f)
		if err != nil {
			return nil, false, nil, err
		}
		total = &n
	}
	return rows, hasMore, total, nil
}

func (s *Service) GetShipTo(ctx context.Context, id uuid.UUID) (*ShipTo, error) {
	st, err := s.repo.GetShipTo(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	return st, nil
}

func shipToFrom(d *ShipToDraft, id, customerID uuid.UUID, now httpx.Timestamp) *ShipTo {
	return &ShipTo{
		ID: id, CustomerID: customerID, Code: d.Code, Name: d.Name, Line1: d.Line1, Line2: d.Line2,
		City: d.City, Region: d.Region, PostalCode: d.PostalCode, Country: d.Country, Phone: d.Phone,
		DeliveryInstructions: d.DeliveryInstructions, TaxRatePercent: d.TaxRatePercent,
		IsDefault: d.IsDefault, IsActive: d.IsActive, UpdatedAt: now,
	}
}

// CreateShipTo adds a ship-to. The customer's first active ship-to becomes
// the default unless the request says otherwise; naming a default moves the
// default from the one that held it.
func (s *Service) CreateShipTo(ctx context.Context, customerID uuid.UUID, d *ShipToDraft) (*ShipTo, error) {
	var out *ShipTo
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockCustomer(ctx, customerID); err != nil {
			return notFound(err)
		}
		cust, err := s.repo.GetCustomer(ctx, customerID)
		if err != nil {
			return notFound(err)
		}
		now := httpx.TimestampOf(s.now().UTC())
		st := shipToFrom(d, uuid.New(), customerID, now)
		st.CreatedAt = now
		if !d.DefaultGiven && d.IsActive {
			n, err := s.repo.CountShipTos(ctx, customerID)
			if err != nil {
				return err
			}
			st.IsDefault = n == 0
		}
		if st.IsDefault {
			if err := s.repo.ClearDefaultShipTo(ctx, customerID, st.ID); err != nil {
				return err
			}
		}
		if err := s.repo.InsertShipTo(ctx, st); err != nil {
			return err
		}
		if out, err = s.repo.GetShipTo(ctx, st.ID); err != nil {
			return notFound(err)
		}
		return s.record(ctx, cust, EventUpdated, PartShipTo, map[string]any{"ship_to_id": out.ID, "action": "created"})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) UpdateShipTo(ctx context.Context, id uuid.UUID, d *ShipToDraft, pre Precondition) (*ShipTo, error) {
	if err := needRevision(pre); err != nil {
		return nil, err
	}
	var out *ShipTo
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockShipTo(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetShipTo(ctx, id)
		if err != nil {
			return notFound(err)
		}
		// The customer's row serialises default changes across its ship-tos.
		if err := s.repo.LockCustomer(ctx, cur.CustomerID); err != nil {
			return notFound(err)
		}
		if err := pre.check(cur.Revision); err != nil {
			return err
		}
		next := shipToFrom(d, id, cur.CustomerID, httpx.TimestampOf(s.now().UTC()))
		if next.IsDefault {
			if err := s.repo.ClearDefaultShipTo(ctx, cur.CustomerID, id); err != nil {
				return err
			}
		}
		if err := s.repo.UpdateShipTo(ctx, next); err != nil {
			return err
		}
		if out, err = s.repo.GetShipTo(ctx, id); err != nil {
			return notFound(err)
		}
		cust, err := s.repo.GetCustomer(ctx, cur.CustomerID)
		if err != nil {
			return notFound(err)
		}
		return s.record(ctx, cust, EventUpdated, PartShipTo, map[string]any{"ship_to_id": id, "action": "updated"})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---- payment terms ----

func (s *Service) ListTerms(ctx context.Context, f ChildFilter, wantTotal bool) (items []PaymentTerms, hasMore bool, total *int64, err error) {
	limit := f.Limit
	f.Limit = limit + 1
	rows, err := s.repo.ListTerms(ctx, f)
	if err != nil {
		return nil, false, nil, err
	}
	rows, hasMore = page(rows, limit)
	if wantTotal {
		n, err := s.repo.CountTerms(ctx, f)
		if err != nil {
			return nil, false, nil, err
		}
		total = &n
	}
	return rows, hasMore, total, nil
}

func (s *Service) GetTerms(ctx context.Context, id uuid.UUID) (*PaymentTerms, error) {
	t, err := s.repo.GetTerms(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	return t, nil
}

func termsFrom(d *TermsDraft, id uuid.UUID, now httpx.Timestamp) *PaymentTerms {
	return &PaymentTerms{
		ID: id, Code: d.Code, Name: d.Name, Kind: d.Kind, NetDays: d.NetDays, DayOfMonth: d.DayOfMonth,
		DiscountPercent: d.DiscountPercent, DiscountDays: d.DiscountDays, IsActive: d.IsActive, UpdatedAt: now,
	}
}

func (s *Service) CreateTerms(ctx context.Context, d *TermsDraft) (*PaymentTerms, error) {
	var out *PaymentTerms
	err := s.inTx(ctx, func(ctx context.Context) error {
		now := httpx.TimestampOf(s.now().UTC())
		t := termsFrom(d, uuid.New(), now)
		t.CreatedAt = now
		if err := s.repo.InsertTerms(ctx, t); err != nil {
			return err
		}
		var err error
		out, err = s.repo.GetTerms(ctx, t.ID)
		return notFound(err)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateTerms replaces a terms row on the client's revision. The code is
// fixed at create: customers and invoices cite the row by it. Deactivating
// terms in use is allowed; customers that hold them keep them, and no new
// customer can take them.
func (s *Service) UpdateTerms(ctx context.Context, id uuid.UUID, d *TermsDraft, pre Precondition) (*PaymentTerms, error) {
	if err := needRevision(pre); err != nil {
		return nil, err
	}
	var out *PaymentTerms
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockTerms(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetTerms(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := pre.check(cur.Revision); err != nil {
			return err
		}
		if d.Code != cur.Code {
			return fieldProblem("code", "cannot be changed: it is fixed when the terms are created")
		}
		if err := s.repo.UpdateTerms(ctx, termsFrom(d, id, httpx.TimestampOf(s.now().UTC()))); err != nil {
			return err
		}
		out, err = s.repo.GetTerms(ctx, id)
		return notFound(err)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
