// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesteam

import (
	"context"
	"fmt"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Repository is the slice of sales-team persistence the handler needs. It is
// declared here, next to the only implementation, so the HTTP layer can be
// exercised against a fake roster instead of requiring Postgres.
// *PostgresRepository satisfies it as-is.
type Repository interface {
	List(ctx context.Context) ([]SalesPerson, error)
	Get(ctx context.Context, id uuid.UUID) (*SalesPerson, error)
}

// PostgresRepository implements Repository against Postgres.
type PostgresRepository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

func (r *PostgresRepository) List(ctx context.Context) ([]SalesPerson, error) {
	query := `SELECT id, name, email, phone, role, is_active, created_at, updated_at
		FROM sales_team WHERE is_active = true ORDER BY name`

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list sales team: %w", err)
	}
	defer rows.Close()

	var people []SalesPerson
	for rows.Next() {
		var p SalesPerson
		if err := rows.Scan(&p.ID, &p.Name, &p.Email, &p.Phone, &p.Role, &p.IsActive, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan salesperson: %w", err)
		}
		people = append(people, p)
	}
	return people, nil
}

func (r *PostgresRepository) Get(ctx context.Context, id uuid.UUID) (*SalesPerson, error) {
	query := `SELECT id, name, email, phone, role, is_active, created_at, updated_at
		FROM sales_team WHERE id = $1`

	var p SalesPerson
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query, id).Scan(
		&p.ID, &p.Name, &p.Email, &p.Phone, &p.Role, &p.IsActive, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("salesperson not found")
		}
		return nil, fmt.Errorf("failed to get salesperson: %w", err)
	}
	return &p, nil
}
