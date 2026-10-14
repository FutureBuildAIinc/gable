// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dealerprofile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// TxRunner runs fn in one transaction, joining the caller's when ctx has one.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// profileEntityID is the stable id the profile's audit rows are filed under:
// the profile is one row without a UUID of its own.
var profileEntityID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

type Service struct {
	repo  Repository
	tx    TxRunner
	audit *audit.Logger
}

func NewService(repo Repository, tx TxRunner, a *audit.Logger) *Service {
	return &Service{repo: repo, tx: tx, audit: a}
}

func (s *Service) Get(ctx context.Context) (*Profile, error) { return s.repo.Get(ctx) }

func (s *Service) GetLogo(ctx context.Context) (*Logo, error) { return s.repo.GetLogo(ctx) }

func fields(p *Profile) map[string]any {
	return map[string]any{
		"legal_name": p.LegalName, "trading_name": p.TradingName, "phone": p.Phone,
		"email": p.Email, "website": p.Website, "remit_to": p.RemitTo, "has_logo": p.LogoURL != nil,
	}
}

// Update applies the writable fields under the revision precondition and
// writes the audit row, holding the before and after, in the same
// transaction: a failed audit write fails the update.
func (s *Service) Update(ctx context.Context, in Input, ifMatch string, bodyRevision *int64) (*Profile, error) {
	var out *Profile
	err := s.tx.RunInTx(ctx, func(ctx context.Context) error {
		cur, err := s.repo.Lock(ctx)
		if err != nil {
			return err
		}
		if err := httpx.CheckRevision(cur.Revision, ifMatch, bodyRevision); err != nil {
			return err
		}
		next, err := s.repo.Update(ctx, in)
		if err != nil {
			return err
		}
		out = next
		return s.audit.Log(ctx, audit.Entry{
			Action: "dealer_profile.update", EntityType: "dealer_profile", EntityID: profileEntityID,
			Changes: map[string]any{"before": fields(cur), "after": fields(next)},
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetLogo stores the image under the same precondition and audits it; the
// audit row records the size and a digest of the image, never the bytes.
func (s *Service) SetLogo(ctx context.Context, logo Logo, ifMatch string, bodyRevision *int64) (*Profile, error) {
	if s.audit == nil {
		return nil, errors.New("dealer profile: no audit logger")
	}
	var out *Profile
	err := s.tx.RunInTx(ctx, func(ctx context.Context) error {
		cur, err := s.repo.Lock(ctx)
		if err != nil {
			return err
		}
		if err := httpx.CheckRevision(cur.Revision, ifMatch, bodyRevision); err != nil {
			return err
		}
		before := map[string]any{"has_logo": cur.LogoURL != nil}
		if old, err := s.repo.GetLogo(ctx); err == nil {
			before["bytes"], before["sha256"], before["content_type"] = len(old.Bytes), digest(old.Bytes), old.ContentType
		}
		next, err := s.repo.SetLogo(ctx, logo)
		if err != nil {
			return err
		}
		out = next
		return s.audit.Log(ctx, audit.Entry{
			Action: "dealer_profile.logo", EntityType: "dealer_profile", EntityID: profileEntityID,
			Changes: map[string]any{
				"before": before,
				"after":  map[string]any{"has_logo": true, "bytes": len(logo.Bytes), "sha256": digest(logo.Bytes), "content_type": logo.ContentType},
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func digest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
