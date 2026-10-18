// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package scheduling

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrWindowNotFound names a missing window for the handler's 404.
var ErrWindowNotFound = errors.New("schedule window not found")

// Service validates and writes the dealer's window definitions.
type Service struct {
	repo *Repository
}

// NewService creates the service.
func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// List lists windows, optionally narrowed to a yard and a kind.
func (s *Service) List(ctx context.Context, yardID *uuid.UUID, kind string) ([]Window, error) {
	if kind != "" && !ValidKind(kind) {
		return nil, fmt.Errorf("kind must be delivery or pickup")
	}
	return s.repo.List(ctx, yardID, kind)
}

// Get reads one window.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Window, error) {
	return s.repo.Get(ctx, id)
}

// Create validates and writes a new window.
func (s *Service) Create(ctx context.Context, req WindowRequest) (*Window, error) {
	if req.YardID == nil || req.Kind == nil || req.Weekday == nil ||
		req.StartTime == nil || req.EndTime == nil || req.Capacity == nil {
		return nil, fmt.Errorf("yard_id, kind, weekday, start_time, end_time and capacity are required")
	}
	w, err := s.validated(*req.YardID, *req.Kind, *req.Weekday, *req.StartTime, *req.EndTime,
		*req.Capacity, intPtrOr(req.CutoffMinutes, 0), true)
	if err != nil {
		return nil, err
	}
	ok, err := s.repo.YardExists(ctx, w.YardID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("unknown yard")
	}
	if err := s.repo.Create(ctx, w); err != nil {
		return nil, err
	}
	return w, nil
}

// Update changes the mutable fields of one window. A nil field keeps its
// current value; the yard never moves (a window at another yard is a new
// window, so a hold can never silently change yards under an order).
func (s *Service) Update(ctx context.Context, id uuid.UUID, req WindowRequest) (*Window, error) {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	w, err := s.validated(current.YardID,
		strPtrOr(req.Kind, current.Kind),
		intPtrOr(req.Weekday, current.Weekday),
		strPtrOr(req.StartTime, current.StartTime),
		strPtrOr(req.EndTime, current.EndTime),
		intPtrOr(req.Capacity, current.Capacity),
		intPtrOr(req.CutoffMinutes, current.CutoffMinutes),
		boolPtrOr(req.IsActive, current.IsActive))
	if err != nil {
		return nil, err
	}
	w.ID = id
	if err := s.repo.Update(ctx, w); err != nil {
		return nil, err
	}
	return s.repo.Get(ctx, id)
}

// Deactivate switches a window off; its holds keep their reference.
func (s *Service) Deactivate(ctx context.Context, id uuid.UUID) error {
	return s.repo.Deactivate(ctx, id)
}

func (s *Service) validated(yardID uuid.UUID, kind string, weekday int, start, end string, capacity, cutoff int, active bool) (*Window, error) {
	if !ValidKind(kind) {
		return nil, fmt.Errorf("kind must be delivery or pickup")
	}
	if weekday < 0 || weekday > 6 {
		return nil, fmt.Errorf("weekday must be 0 (Sunday) to 6 (Saturday)")
	}
	startT, err := time.Parse("15:04", start)
	if err != nil {
		return nil, fmt.Errorf("start_time must be hh:mm")
	}
	endT, err := time.Parse("15:04", end)
	if err != nil {
		return nil, fmt.Errorf("end_time must be hh:mm")
	}
	if !endT.After(startT) {
		return nil, fmt.Errorf("end_time must be after start_time")
	}
	if capacity <= 0 {
		return nil, fmt.Errorf("capacity must be positive")
	}
	if cutoff < 0 {
		return nil, fmt.Errorf("cutoff_minutes cannot be negative")
	}
	return &Window{
		YardID: yardID, Kind: kind, Weekday: weekday,
		StartTime: start, EndTime: end,
		Capacity: capacity, CutoffMinutes: cutoff, IsActive: active,
	}, nil
}

func intPtrOr(p *int, fallback int) int {
	if p == nil {
		return fallback
	}
	return *p
}

func strPtrOr(p *string, fallback string) string {
	if p == nil {
		return fallback
	}
	return *p
}

func boolPtrOr(p *bool, fallback bool) bool {
	if p == nil {
		return fallback
	}
	return *p
}
