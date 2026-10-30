// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dealerprofile

import (
	"context"
	"fmt"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/jackc/pgx/v5"
)

// Repository is the store of the one profile row. Every statement goes
// through the database seam's executor, so inside a transaction it is the
// transaction.
type Repository interface {
	Get(ctx context.Context) (*Profile, error)
	// Lock reads the row FOR UPDATE.
	Lock(ctx context.Context) (*Profile, error)
	Update(ctx context.Context, in Input) (*Profile, error)
	GetLogo(ctx context.Context) (*Logo, error)
	SetLogo(ctx context.Context, logo Logo) (*Profile, error)
}

type PostgresRepository struct{ db *database.DB }

func NewRepository(db *database.DB) *PostgresRepository { return &PostgresRepository{db: db} }

const columns = `legal_name, trading_name, phone, email, website, remit_to, locale, gst_hst_number, gst_hst_not_registered,
	CASE WHEN logo IS NULL THEN NULL ELSE '` + LogoPath + `' END, revision, updated_at`

func scan(row pgx.Row) (*Profile, error) {
	var p Profile
	var updated = new(httpx.Timestamp)
	if err := row.Scan(&p.LegalName, &p.TradingName, &p.Phone, &p.Email, &p.Website, &p.RemitTo, &p.Locale, &p.GSTHSTNumber, &p.GSTHSTNotRegistered, &p.LogoURL, &p.Revision, &updated.Time); err != nil {
		return nil, err
	}
	p.UpdatedAt = *updated
	return &p, nil
}

func (r *PostgresRepository) Get(ctx context.Context) (*Profile, error) {
	p, err := scan(r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT `+columns+` FROM dealer_profile WHERE id = 1`))
	if err != nil {
		return nil, fmt.Errorf("read dealer profile: %w", err)
	}
	return p, nil
}

func (r *PostgresRepository) Lock(ctx context.Context) (*Profile, error) {
	p, err := scan(r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT `+columns+` FROM dealer_profile WHERE id = 1 FOR UPDATE`))
	if err != nil {
		return nil, fmt.Errorf("lock dealer profile: %w", err)
	}
	return p, nil
}

func (r *PostgresRepository) Update(ctx context.Context, in Input) (*Profile, error) {
	p, err := scan(r.db.GetExecutor(ctx).QueryRow(ctx, `
		UPDATE dealer_profile SET legal_name = $1, trading_name = $2, phone = $3, email = $4,
		       website = $5, remit_to = $6, locale = $7, gst_hst_number = COALESCE($8, gst_hst_number),
		       gst_hst_not_registered = COALESCE($9, gst_hst_not_registered),
		       revision = revision + 1, updated_at = NOW()
		 WHERE id = 1 RETURNING `+columns,
		in.LegalName, in.TradingName, in.Phone, in.Email, in.Website, in.RemitTo, in.Locale, in.GSTHSTNumber, in.GSTHSTNotRegistered))
	if err != nil {
		return nil, fmt.Errorf("update dealer profile: %w", err)
	}
	return p, nil
}

func (r *PostgresRepository) GetLogo(ctx context.Context) (*Logo, error) {
	var l Logo
	var ct *string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT logo, logo_content_type FROM dealer_profile WHERE id = 1`).Scan(&l.Bytes, &ct)
	if err != nil {
		return nil, fmt.Errorf("read dealer logo: %w", err)
	}
	if l.Bytes == nil || ct == nil {
		return nil, ErrNotFound
	}
	l.ContentType = *ct
	return &l, nil
}

func (r *PostgresRepository) SetLogo(ctx context.Context, logo Logo) (*Profile, error) {
	p, err := scan(r.db.GetExecutor(ctx).QueryRow(ctx, `
		UPDATE dealer_profile SET logo = $1, logo_content_type = $2, revision = revision + 1, updated_at = NOW()
		 WHERE id = 1 RETURNING `+columns, logo.Bytes, logo.ContentType))
	if err != nil {
		return nil, fmt.Errorf("set dealer logo: %w", err)
	}
	return p, nil
}
