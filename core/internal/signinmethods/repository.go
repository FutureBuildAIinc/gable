// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package signinmethods

import (
	"context"
	"errors"
	"time"

	"github.com/gablelbm/gable/pkg/database"
)

// Repository reads and writes dealer_signin_methods. Every statement goes
// through GetExecutor(ctx), so calls inside a transaction run on the
// transaction's own connection, never the pool, and calls outside one use
// the pool.
type Repository struct {
	db *database.DB
}

// NewRepository validates the database handle.
func NewRepository(db *database.DB) (*Repository, error) {
	if db == nil {
		return nil, errors.New("signin methods: the repository requires a database")
	}
	return &Repository{db: db}, nil
}

// List answers the stored rows in the dealer's order. A code outside the
// vocabulary cannot be stored (the column check refuses it), so every row
// answers as it is.
func (r *Repository) List(ctx context.Context) ([]Row, time.Time, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		`SELECT method, sort_order, coalesce(oidc_issuer_url, ''), coalesce(oidc_client_id, ''), coalesce(oidc_label, ''),
		 updated_at
		 FROM dealer_signin_methods ORDER BY sort_order, method`)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()
	var out []Row
	var updated time.Time
	for rows.Next() {
		var row Row
		if err := rows.Scan(&row.Method, &row.SortOrder, &row.OIDCIssuerURL, &row.OIDCClientID, &row.OIDCLabel, &updated); err != nil {
			return nil, time.Time{}, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return out, updated, nil
}

// Replace rewrites the allowlist inside the caller's transaction. The new
// rows are written first and the rows not in the new set are deleted after,
// so the guard trigger (the table never goes empty) can only refuse an
// update whose own validation already failed, and a good update never trips
// it.
func (r *Repository) Replace(ctx context.Context, in []Input, now time.Time) error {
	exec := r.db.GetExecutor(ctx)
	keep := make([]string, len(in))
	for i, m := range in {
		keep[i] = m.Method
		if _, err := exec.Exec(ctx,
			`INSERT INTO dealer_signin_methods (method, sort_order, oidc_issuer_url, oidc_client_id, oidc_label, updated_at)
			 VALUES ($1, $2, nullif($3, ''), nullif($4, ''), nullif($5, ''), $6)
			 ON CONFLICT (method) DO UPDATE SET
			   sort_order = EXCLUDED.sort_order,
			   oidc_issuer_url = EXCLUDED.oidc_issuer_url,
			   oidc_client_id = EXCLUDED.oidc_client_id,
			   oidc_label = EXCLUDED.oidc_label,
			   updated_at = EXCLUDED.updated_at`,
			m.Method, i+1, m.OIDCIssuerURL, m.OIDCClientID, m.OIDCLabel, now); err != nil {
			return err
		}
	}
	if _, err := exec.Exec(ctx,
		`DELETE FROM dealer_signin_methods WHERE NOT (method = ANY($1))`, keep); err != nil {
		return err
	}
	return nil
}
