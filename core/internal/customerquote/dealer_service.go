// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The dealer's leg. A dealer reads a contractor's customer quote only as a
// DealerProject (dealer.go): price wall 1. A draft is the contractor's own
// work and is not shared; a quote is shared from the moment it is sent.

func (s *Service) dealerProject(ctx context.Context, q *Quote) (DealerProject, error) {
	cname, err := s.store.CustomerName(ctx, q.CustomerID)
	if err != nil {
		return DealerProject{}, err
	}
	pname, err := s.store.ProjectName(ctx, q.ProjectID)
	if err != nil {
		return DealerProject{}, err
	}
	in := DealerInputs{ContractorName: cname, ProjectName: pname}
	if ob, err := s.store.OrderBriefOf(ctx, q.OrderID); err != nil {
		return DealerProject{}, err
	} else if ob != nil {
		in.Order = &DealerOrderRef{ID: ob.ID, Number: ob.Number, Status: strings.ToLower(ob.Status), TotalCents: ob.TotalCents}
	}
	return BuildDealerProject(q, in), nil
}

// DealerList lists the shared projects, most recently moved first. The page
// is one longer than limit when more follow.
func (s *Service) DealerList(ctx context.Context, limit int, after *time.Time, afterID uuid.UUID) ([]DealerProject, []*Quote, error) {
	qs, err := s.store.ListSent(ctx, limit, after, afterID)
	if err != nil {
		return nil, nil, err
	}
	out := make([]DealerProject, 0, len(qs))
	for _, q := range qs {
		d, err := s.dealerProject(ctx, q)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, d)
	}
	return out, qs, nil
}

// DealerGet reads one shared project with its approval record.
func (s *Service) DealerGet(ctx context.Context, id uuid.UUID) (*DealerProjectDetail, error) {
	q, err := s.store.GetQuote(ctx, id, nil, false)
	if err != nil {
		return nil, err
	}
	if q.Status == StatusDraft {
		return nil, ErrNotFound
	}
	d, err := s.dealerProject(ctx, q)
	if err != nil {
		return nil, err
	}
	log, err := s.store.ListLog(ctx, id)
	if err != nil {
		return nil, err
	}
	out := &DealerProjectDetail{DealerProject: d, Approvals: []DealerApproval{}}
	for _, e := range log {
		out.Approvals = append(out.Approvals, DealerApprovalOf(e))
	}
	return out, nil
}

// DealerAccept records the dealer's acceptance of a confirmed project, once.
func (s *Service) DealerAccept(ctx context.Context, who DealerActor, id uuid.UUID) (*DealerProjectDetail, error) {
	// The record keeps the desk user's id and display name, never the address.
	name, err := s.store.StaffName(ctx, who.Email)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = strings.TrimSpace(who.Name)
	}
	if name == "" || strings.Contains(name, "@") {
		name = UnknownDeskUser
	}
	who.Name, who.Email = name, ""
	err = s.db.RunInTx(ctx, func(ctx context.Context) error {
		q, err := s.store.GetQuote(ctx, id, nil, true)
		if err != nil {
			return err
		}
		if q.Status != StatusConfirmed || q.DealerAcceptedAt != nil {
			return ErrInvalidState
		}
		now := s.now()
		if ok, err := s.store.SetDealerAccepted(ctx, q.ID, now); err != nil {
			return err
		} else if !ok {
			return ErrInvalidState
		}
		return s.store.AppendLog(ctx, &ApprovalEntry{QuoteID: q.ID, QuoteRevision: q.Revision, Step: StepDealerAccepted,
			ActorKind: ActorDealer, ActorName: who.Name, ActorRef: who.Ref, OccurredAt: now})
	})
	if err != nil {
		return nil, err
	}
	return s.DealerGet(ctx, id)
}
