// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package signinmethods

import (
	"context"
	"errors"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// TxRunner runs fn in one transaction, joining the caller's when ctx has one.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// allowlistEntityID is the stable id the allowlist's audit rows are filed
// under: the allowlist is the dealer's one setting, without a UUID of its
// own.
var allowlistEntityID = uuid.MustParse("00000000-0000-0000-0000-000000000012")

// Service reads the allowlist, replaces it under audit, and answers the two
// questions the door and the sign in paths ask: the public config, and
// whether a method is allowed.
type Service struct {
	repo  *Repository
	tx    TxRunner
	audit *audit.Logger
}

// NewService validates its seams.
func NewService(repo *Repository, tx TxRunner, a *audit.Logger) (*Service, error) {
	if repo == nil {
		return nil, errors.New("signin methods: the service requires a repository")
	}
	if tx == nil {
		return nil, errors.New("signin methods: the service requires a transaction runner")
	}
	return &Service{repo: repo, tx: tx, audit: a}, nil
}

// rows answers the stored rows, or the safe default (email and password
// alone) when the table holds nothing, which the guard trigger already
// refuses; the reader never hands out an empty allowlist.
func (s *Service) rows(ctx context.Context) ([]Row, time.Time, error) {
	stored, updated, err := s.repo.List(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	if len(stored) == 0 {
		return []Row{{Method: MethodEmailPassword, SortOrder: 1}}, time.Time{}, nil
	}
	return stored, updated, nil
}

// Config answers the public auth config: the server's auth mode, the dealer's
// public display name and optional mark URL, and the allowed methods in the
// dealer's stored order.
func (s *Service) Config(ctx context.Context, authMode string) (*ConfigResponse, error) {
	rows, _, err := s.rows(ctx)
	if err != nil {
		return nil, err
	}
	name, markURL, err := s.repo.DealerBrand(ctx)
	if err != nil {
		return nil, err
	}
	out := &ConfigResponse{
		AuthMode:      authMode,
		DealerName:    name,
		DealerMarkURL: markURL,
		Methods:       make([]MethodView, 0, len(rows)),
	}
	for _, r := range rows {
		out.Methods = append(out.Methods, viewOf(r))
	}
	return out, nil
}

// Allowlist answers the admin read: the methods and the last write time.
func (s *Service) Allowlist(ctx context.Context) (*AllowlistResponse, error) {
	rows, updated, err := s.rows(ctx)
	if err != nil {
		return nil, err
	}
	out := &AllowlistResponse{Methods: make([]MethodView, 0, len(rows)), UpdatedAt: httpx.Timestamp{Time: updated}}
	for _, r := range rows {
		out.Methods = append(out.Methods, viewOf(r))
	}
	return out, nil
}

// Check validates a replacement allowlist. The list must hold at least one
// method, every code must be one of the six, no code twice, and the oidc
// label belongs to oidc alone. The issuer and client id are retired (ADR
// 0012 section 4): the issuer is configured in Appwrite, so the fields are
// refused for every method.
func Check(in []Input) error {
	if len(in) == 0 {
		return httpx.BadRequest("the allowlist must hold at least one sign in method",
			httpx.FieldError{Field: "methods", Message: "must not be empty"})
	}
	seen := make(map[string]bool, len(in))
	for _, m := range in {
		if !IsCode(m.Method) {
			return httpx.BadRequest("the method "+m.Method+" is not a sign in method this server knows",
				httpx.FieldError{Field: "methods", Message: "must be one of email_password, passkey, magic_link, google, microsoft, oidc"})
		}
		if seen[m.Method] {
			return httpx.BadRequest("the method "+m.Method+" appears twice",
				httpx.FieldError{Field: "methods", Message: "each method at most once"})
		}
		seen[m.Method] = true
		if m.OIDCIssuerURL != "" || m.OIDCClientID != "" {
			return httpx.BadRequest("the oidc issuer is configured in Appwrite, not in Gable",
				httpx.FieldError{Field: "methods", Message: "oidc_issuer_url and oidc_client_id are not accepted"})
		}
		if m.Method == MethodOIDC {
			continue
		}
		if m.OIDCIssuerURL != "" || m.OIDCClientID != "" || m.OIDCLabel != "" {
			return httpx.BadRequest("the oidc fields belong to the oidc method alone",
				httpx.FieldError{Field: "methods", Message: "only the oidc method carries oidc fields"})
		}
	}
	return nil
}

// Update replaces the allowlist in one transaction and audits the change,
// holding the before and after, the way Gable audits settings changes: a
// failed audit write fails the update.
func (s *Service) Update(ctx context.Context, in []Input) (*AllowlistResponse, error) {
	if err := Check(in); err != nil {
		return nil, err
	}
	if s.audit == nil {
		return nil, errors.New("signin methods: no audit logger")
	}
	now := time.Now().UTC()
	err := s.tx.RunInTx(ctx, func(ctx context.Context) error {
		if err := s.repo.Lock(ctx); err != nil {
			return err
		}
		before, _, err := s.repo.List(ctx)
		if err != nil {
			return err
		}
		if err := s.repo.Replace(ctx, in, now); err != nil {
			return err
		}
		after, _, err := s.repo.List(ctx)
		if err != nil {
			return err
		}
		return s.audit.Log(ctx, audit.Entry{
			Action:     "signin_methods.update",
			EntityType: "dealer_signin_methods",
			EntityID:   allowlistEntityID,
			Changes: map[string]any{
				"before": codesOf(before),
				"after":  codesOf(after),
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.Allowlist(ctx)
}

func codesOf(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Method)
	}
	return out
}

// Allowed answers whether the dealer's allowlist holds the method. A read
// failure answers false: sign in paths fail closed.
func (s *Service) Allowed(ctx context.Context, method string) bool {
	rows, _, err := s.rows(ctx)
	if err != nil {
		return false
	}
	for _, r := range rows {
		if r.Method == method {
			return true
		}
	}
	return false
}

// StartOutcome is the start route's answer for one method.
type StartOutcome int

const (
	// StartNotKnown: the code is not one of the six.
	StartNotKnown StartOutcome = iota
	// StartNotAllowed: the method is known but not in this dealer's allowlist.
	StartNotAllowed
	// StartNoRoute: the method needs no start route (the door's form).
	StartNoRoute
	// StartNotImplemented: allowed, rendered by the door, not built on this
	// server yet.
	StartNotImplemented
	// StartByNavigation: a provider redirect method, which the browser starts
	// by navigating (GET) to this route, served by the staff session layer.
	StartByNavigation
)

// Start judges a POST start attempt. Email and password has no start route;
// the provider redirect methods start by navigation (GET), and the magic
// link's POST is served by the staff session layer when Appwrite is
// configured; what reaches here for them, and for passkey, which is deferred,
// is the plain refusal.
func (s *Service) Start(ctx context.Context, method string) StartOutcome {
	if !IsCode(method) {
		return StartNotKnown
	}
	if !s.Allowed(ctx, method) {
		return StartNotAllowed
	}
	if method == MethodEmailPassword {
		return StartNoRoute
	}
	switch method {
	case MethodGoogle, MethodMicrosoft, MethodOIDC:
		return StartByNavigation
	}
	return StartNotImplemented
}
