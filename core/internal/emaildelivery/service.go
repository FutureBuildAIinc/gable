// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package emaildelivery

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Service struct{ repo Repository }

func NewService(repo Repository) *Service { return &Service{repo: repo} }

// Record writes one delivery row. A sending route calls it before the send,
// with the queued status, so the row exists the moment the route answers.
func (s *Service) Record(ctx context.Context, e Entry) (*Delivery, error) {
	return s.repo.Insert(ctx, e)
}

// SetOutcome resolves one queued row with the send's outcome.
func (s *Service) SetOutcome(ctx context.Context, id uuid.UUID, status, senderAnswer, sendError string) error {
	return s.repo.SetOutcome(ctx, id, status, senderAnswer, sendError)
}

// List returns one page and whether more follow (the repository reads one
// extra row to know).
func (s *Service) List(ctx context.Context, f Filter, afterTime *time.Time, afterID *uuid.UUID, limit int) ([]Delivery, bool, error) {
	rows, err := s.repo.List(ctx, f, afterTime, afterID, limit+1)
	if err != nil {
		return nil, false, err
	}
	if len(rows) > limit {
		return rows[:limit], true, nil
	}
	return rows, false, nil
}

func (s *Service) Count(ctx context.Context, f Filter) (int64, error) { return s.repo.Count(ctx, f) }
