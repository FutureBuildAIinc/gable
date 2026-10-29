// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer

import (
	"context"
	"fmt"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// ActorResolver names the acting principal for the rows that keep one: the
// alert's author and the one who ended it. It is optional; nil writes
// "unknown".
type ActorResolver interface {
	ActorID(ctx context.Context) string
}

// unknownActor is what an alert keeps when nothing named the caller.
const unknownActor = "unknown"

// actorID names the acting principal for a row that keeps one.
func (s *Service) actorID(ctx context.Context) string {
	if s.actor == nil {
		return unknownActor
	}
	if id := s.actor.ActorID(ctx); id != "" {
		return id
	}
	return unknownActor
}

// ListAlerts is one page of a customer's alerts, newest first; an unknown
// customer is a 404, not an empty page.
func (s *Service) ListAlerts(ctx context.Context, customerID uuid.UUID, f AlertFilter, wantTotal bool) (items []Alert, hasMore bool, total *int64, err error) {
	if _, err := s.Get(ctx, customerID); err != nil {
		return nil, false, nil, err
	}
	limit := f.Limit
	f.Limit = limit + 1
	rows, err := s.repo.ListAlerts(ctx, customerID, f)
	if err != nil {
		return nil, false, nil, err
	}
	rows, hasMore = page(rows, limit)
	if wantTotal {
		n, err := s.repo.CountAlerts(ctx, customerID, f)
		if err != nil {
			return nil, false, nil, err
		}
		total = &n
	}
	return rows, hasMore, total, nil
}

// AddAlert leaves an alert on the customer: the office's note or warning for
// the counter and the order desk. It is a customer edit, so it is guarded as
// one, audited as one and says so in the customer's event.
func (s *Service) AddAlert(ctx context.Context, customerID uuid.UUID, d *AlertDraft) (*Alert, error) {
	var out *Alert
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockCustomer(ctx, customerID); err != nil {
			return notFound(err)
		}
		cust, err := s.repo.GetCustomer(ctx, customerID)
		if err != nil {
			return notFound(err)
		}
		now := httpx.TimestampOf(s.now().UTC())
		a := &Alert{
			ID: uuid.New(), CustomerID: customerID, Kind: d.Kind, Text: d.Text,
			ShowAtCounter: d.ShowAtCounter, ShowAtOrders: d.ShowAtOrders,
			AcknowledgeRequired: d.AcknowledgeRequired,
			CreatedBy:           s.actorID(ctx), CreatedAt: now, Revision: 1,
		}
		if err := s.repo.InsertAlert(ctx, a); err != nil {
			return err
		}
		if out, err = s.repo.GetAlert(ctx, a.ID); err != nil {
			return notFound(err)
		}
		if err := s.auditAlert(ctx, cust, "customer.alert_added", out, nil); err != nil {
			return err
		}
		return s.record(ctx, cust, EventUpdated, PartAlert, map[string]any{"alert_id": out.ID, "action": "added"})
	})
	if err != nil {
		return nil, s.sharedOutcome(ctx, err)
	}
	return out, nil
}

// EndAlert calls an alert off: the row stays for the audit trail, its
// revision moves and the end says who called it off. The client's revision
// is the precondition, so an end of an alert that has already been ended is
// refused as stale.
func (s *Service) EndAlert(ctx context.Context, customerID, alertID uuid.UUID, pre Precondition) (*Alert, error) {
	if err := needRevision(pre); err != nil {
		return nil, err
	}
	var out *Alert
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockCustomer(ctx, customerID); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetAlert(ctx, alertID)
		if err != nil {
			return notFound(err)
		}
		if cur.CustomerID != customerID {
			return httpx.NotFound("customer alert not found")
		}
		if err := pre.check(cur.Revision); err != nil {
			return err
		}
		// The ship-to and contact rule: a bound key may not change a child of
		// a shared customer.
		if err := s.checkSharedChild(ctx, customerID, "alert"); err != nil {
			return err
		}
		cust, err := s.repo.GetCustomer(ctx, customerID)
		if err != nil {
			return notFound(err)
		}
		now := httpx.TimestampOf(s.now().UTC())
		endedBy := s.actorID(ctx)
		next := *cur
		next.EndedBy = &endedBy
		next.EndedAt = &now
		if err := s.repo.EndAlert(ctx, &next); err != nil {
			return err
		}
		if out, err = s.repo.GetAlert(ctx, alertID); err != nil {
			return notFound(err)
		}
		if err := s.auditAlert(ctx, cust, "customer.alert_ended", cur, out); err != nil {
			return err
		}
		return s.record(ctx, cust, EventUpdated, PartAlert, map[string]any{"alert_id": out.ID, "action": "ended"})
	})
	if err != nil {
		return nil, s.sharedOutcome(ctx, err)
	}
	return out, nil
}

// auditAlert writes the audit row of an alert's change, inside the
// transaction, naming the alert and what it says. The actor columns come
// from the context, as for every audited write.
func (s *Service) auditAlert(ctx context.Context, cust *Customer, action string, before, after *Alert) error {
	if s.audit == nil {
		return nil
	}
	changes := map[string]any{
		"alert_id":             before.ID,
		"account_number":       cust.AccountNumber,
		"kind":                 string(before.Kind),
		"text":                 before.Text,
		"show_at_counter":      before.ShowAtCounter,
		"show_at_orders":       before.ShowAtOrders,
		"acknowledge_required": before.AcknowledgeRequired,
		"created_by":           before.CreatedBy,
	}
	if after != nil {
		changes["ended_by"] = after.EndedBy
	}
	if err := s.audit.LogChange(ctx, action, cust.ID, changes); err != nil {
		return fmt.Errorf("failed to write audit log: %w", err)
	}
	return nil
}
