// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package chargecode

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// codePattern is a charge code (ADR 0005 section 2.5): one to sixteen
// uppercase letters, digits or underscores.
var codePattern = regexp.MustCompile(`^[A-Z0-9_]{1,16}$`)

// Request is the body of POST /charge-codes and PUT /charge-codes/{id}.
// Numeric fields decode as raw JSON and are parsed by Parse, so a bad value
// is a field error, not a decoder's pathless failure.
type Request struct {
	Code               *string         `json:"code"`
	Name               *string         `json:"name"`
	RevenueAccountCode *string         `json:"revenue_account_code"`
	Taxable            *bool           `json:"taxable"`
	DefaultUnitPrice   json.RawMessage `json:"default_unit_price_ten_thousandths"`
	IsActive           *bool           `json:"is_active"`
	Revision           json.RawMessage `json:"revision"`
}

// Draft is a validated request.
type Draft struct {
	Code               string
	Name               string
	RevenueAccountCode string
	Taxable            bool
	DefaultUnitPrice   *httpx.Price
	IsActive           bool
	Revision           *int64
}

// Parse validates the request in one pass.
func (req *Request) Parse(update bool) (*Draft, error) {
	v := &httpx.Validator{}
	d := &Draft{IsActive: true}
	if req.Code != nil {
		v.Check(codePattern.MatchString(*req.Code), "code",
			"must be one to sixteen capital letters, digits or underscores")
		d.Code = *req.Code
	}
	if !update {
		v.Required("code", d.Code)
	}
	v.Required("name", deref(req.Name))
	if req.Name != nil {
		d.Name = *req.Name
	}
	v.Required("revenue_account_code", deref(req.RevenueAccountCode))
	if req.RevenueAccountCode != nil {
		v.Check(accountPattern.MatchString(*req.RevenueAccountCode), "revenue_account_code",
			"must be an account code of one to six digits, for example 4020")
		d.RevenueAccountCode = *req.RevenueAccountCode
	}
	if req.Taxable != nil {
		d.Taxable = *req.Taxable
	}
	if req.IsActive != nil {
		d.IsActive = *req.IsActive
	}
	if n, ok := v.Int("default_unit_price_ten_thousandths", req.DefaultUnitPrice, false); ok {
		v.Check(n >= 0, "default_unit_price_ten_thousandths", "a unit price is never negative")
		p := httpx.Price(n)
		d.DefaultUnitPrice = &p
	}
	if n, ok := v.Int("revision", req.Revision, false); ok && update {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		d.Revision = &n
	}
	if update {
		v.Check(req.Code == nil, "code", "cannot be changed: it is what documents name")
	} else {
		v.Check(isAbsentRaw(req.Revision), "revision", "is not accepted on create: a new code has no revision to precondition on")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// accountPattern is a GL account code.
var accountPattern = regexp.MustCompile(`^[0-9]{1,6}$`)

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func isAbsentRaw(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return t == "" || t == "null"
}

// Service is the charge code master's behaviour.
type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service { return &Service{repo: repo} }

func (s *Service) List(ctx context.Context, includeInactive bool) ([]Code, error) {
	return s.repo.List(ctx, includeInactive)
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Code, error) {
	c, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	return c, nil
}

func (s *Service) Create(ctx context.Context, d *Draft) (*Code, error) {
	c := &Code{
		ID: uuid.New(), Code: d.Code, Name: d.Name, RevenueAccountCode: d.RevenueAccountCode,
		Taxable: d.Taxable, DefaultUnitPrice: d.DefaultUnitPrice, IsActive: d.IsActive,
	}
	if err := s.repo.Create(ctx, c); err != nil {
		return nil, err
	}
	return s.repo.Get(ctx, c.ID)
}

// Update replaces a code on the client's revision. The code itself never
// changes: documents name it.
func (s *Service) Update(ctx context.Context, id uuid.UUID, d *Draft, ifMatch string) (*Code, error) {
	if ifMatch == "" && d.Revision == nil {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	if err := s.repo.Lock(ctx, id); err != nil {
		return nil, notFound(err)
	}
	cur, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	if err := httpx.CheckRevision(cur.Revision, ifMatch, d.Revision); err != nil {
		return nil, err
	}
	c := &Code{
		ID: id, Name: d.Name, RevenueAccountCode: d.RevenueAccountCode, Taxable: d.Taxable,
		DefaultUnitPrice: d.DefaultUnitPrice, IsActive: d.IsActive,
	}
	if err := s.repo.Update(ctx, c); err != nil {
		return nil, err
	}
	return s.repo.Get(ctx, id)
}

func notFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound(ErrNotFound.Error())
	}
	return err
}
