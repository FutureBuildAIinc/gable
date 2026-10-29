// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package tint

import (
	"context"
	"errors"
	"math/big"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// TxRunner runs fn inside one transaction, joining the caller's when ctx
// already carries one. The catalogue's writes run inside one: a formula and
// its colorant amounts are one fact, and a half written formula (a row with
// no amounts) would be sold at the counter.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Service is the catalogue's business logic: the creates, the reads, the
// rescale and the saved colours.
type Service struct {
	repo Repository
	tx   TxRunner
}

func NewService(repo Repository) *Service { return &Service{repo: repo} }

// WithTxRunner gives the service the transaction runner its writes run in.
func (s *Service) WithTxRunner(tx TxRunner) *Service { s.tx = tx; return s }

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx == nil {
		return fn(ctx)
	}
	return s.tx.RunInTx(ctx, fn)
}

func httpErr(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.NotFound("no such tint row")
	case errors.Is(err, ErrDuplicate):
		return &httpx.Error{Status: 409, Code: httpx.CodeDuplicate,
			Message: "a tint row with this key already exists"}
	}
	return err
}

func (s *Service) ListSystems(ctx context.Context) ([]System, error) { return s.repo.ListSystems(ctx) }

func (s *Service) CreateSystem(ctx context.Context, d *SystemDraft) (*System, error) {
	var out *System
	err := s.inTx(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.repo.CreateSystem(ctx, d)
		return err
	})
	return out, httpErr(err)
}

func (s *Service) ListBases(ctx context.Context, systemID, productID *uuid.UUID) ([]Base, error) {
	return s.repo.ListBases(ctx, systemID, productID)
}

func (s *Service) CreateBase(ctx context.Context, d *BaseDraft) (*Base, error) {
	var out *Base
	err := s.inTx(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.repo.CreateBase(ctx, d)
		return err
	})
	return out, httpErr(err)
}

func (s *Service) ListColorants(ctx context.Context, systemID *uuid.UUID) ([]Colorant, error) {
	return s.repo.ListColorants(ctx, systemID)
}

func (s *Service) CreateColorant(ctx context.Context, d *ColorantDraft) (*Colorant, error) {
	var out *Colorant
	err := s.inTx(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.repo.CreateColorant(ctx, d)
		return err
	})
	return out, httpErr(err)
}

func (s *Service) ListColors(ctx context.Context, systemID *uuid.UUID, q string) ([]Color, error) {
	return s.repo.ListColors(ctx, systemID, q)
}

func (s *Service) CreateColor(ctx context.Context, d *ColorDraft) (*Color, error) {
	var out *Color
	err := s.inTx(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.repo.CreateColor(ctx, d)
		return err
	})
	return out, httpErr(err)
}

func (s *Service) ListFormulas(ctx context.Context, colorID, baseID *uuid.UUID, sizeCode string) ([]Formula, error) {
	return s.repo.ListFormulas(ctx, colorID, baseID, sizeCode)
}

func (s *Service) GetFormula(ctx context.Context, id uuid.UUID) (*Formula, error) {
	out, err := s.repo.GetFormula(ctx, id)
	return out, httpErr(err)
}

// CreateFormula stores a formula with its components. The rule the research
// pins: a component's denominator must be the one its tint system declares,
// so two systems in one catalogue do not collide.
func (s *Service) CreateFormula(ctx context.Context, d *FormulaDraft) (*Formula, error) {
	if d.Source == "" {
		d.Source = "manual"
	}
	den, err := s.repo.SystemDenominatorOfColor(ctx, d.ColorID)
	if err != nil {
		return nil, httpErr(err)
	}
	for _, c := range d.Components {
		if c.Amount.Denominator != den {
			return nil, httpx.BadRequest("one or more components failed validation",
				httpx.FieldError{Field: "components",
					Message: "a component's unit_denominator must be the one its tint system declares"})
		}
		if err := c.Amount.Validate(); err != nil {
			return nil, httpx.BadRequest("one or more components failed validation",
				httpx.FieldError{Field: "components", Message: err.Error()})
		}
	}
	var out *Formula
	err = s.inTx(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.repo.CreateFormula(ctx, d)
		return err
	})
	return out, httpErr(err)
}

// RescaleFormula reads a formula and rescales it to a target container
// volume: each component multiplied by the exact volume ratio and rounded
// independently to a whole count of its own dispense unit, with the rounding
// flagged so the counter can override it.
func (s *Service) RescaleFormula(ctx context.Context, id uuid.UUID, targetVolumeOZ int) (*Formula, []Rescaled, bool, error) {
	f, err := s.repo.GetFormula(ctx, id)
	if err != nil {
		return nil, nil, false, httpErr(err)
	}
	comps := make([]Component, 0, len(f.Components))
	for _, c := range f.Components {
		comps = append(comps, c)
	}
	rescaled, anyRounded, rerr := Rescale(comps, f.ContainerVolumeOZ, targetVolumeOZ)
	if rerr != nil {
		return nil, nil, false, httpx.BadRequest(rerr.Error())
	}
	return f, rescaled, anyRounded, nil
}

func (s *Service) ListSavedColors(ctx context.Context, customerID *uuid.UUID, jobNote string) ([]SavedColor, error) {
	return s.repo.ListSavedColors(ctx, customerID, jobNote)
}

func (s *Service) CreateSavedColor(ctx context.Context, d *SavedColorDraft) (*SavedColor, error) {
	var out *SavedColor
	err := s.inTx(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.repo.CreateSavedColor(ctx, d)
		return err
	})
	return out, httpErr(err)
}

func (s *Service) DeleteSavedColor(ctx context.Context, id uuid.UUID) error {
	err := s.inTx(ctx, func(ctx context.Context) error {
		return s.repo.DeleteSavedColor(ctx, id)
	})
	return httpErr(err)
}

// Charge reads the dealer's tint charge rule as the counter shows it: the
// charge for a tint with the given colorant total (in the system's own whole
// units) and base tier. Without a tint's numbers the answer is the flat
// figure, the dealer's default.
func (s *Service) Charge(ctx context.Context, units *int64, denominator int, baseTier string) (*ChargeView, error) {
	rule, err := s.repo.ChargeRule(ctx)
	if err != nil {
		return nil, err
	}
	if units == nil || denominator <= 0 {
		return &ChargeView{TintChargeCents: rule.FlatCents}, nil
	}
	total := new(big.Rat).SetFrac(big.NewInt(*units), big.NewInt(int64(denominator)))
	return &ChargeView{TintChargeCents: rule.Charge(total, baseTier)}, nil
}
