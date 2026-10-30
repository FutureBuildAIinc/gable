// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package tax

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// errAlreadyEnded is the end of a certificate that was ended before: a 409,
// not a miss.
var errAlreadyEnded = errors.New("the certificate was already ended")

// ExemptionRepo handles persistence of tax exemption certificates.
type ExemptionRepo interface {
	GetByCustomer(ctx context.Context, customerID uuid.UUID) ([]TaxExemption, error)
	Create(ctx context.Context, ex *TaxExemption) error
	Delete(ctx context.Context, id uuid.UUID) error
	// MarkInactive ends a certificate: it stays on file, no longer exempt.
	MarkInactive(ctx context.Context, id uuid.UUID) (bool, error)
	// CustomerExists answers whether the customer row exists, so the create
	// refuses an unknown one by name instead of a key violation.
	CustomerExists(ctx context.Context, customerID uuid.UUID) (bool, error)
	// CoveringNow reads the certificate that exempts the customer on its own
	// branch's business day: the same one rule (ExemptionCovering) the
	// counter and the orders read, so the preview and the sale cannot
	// disagree about "exempt now".
	CoveringNow(ctx context.Context, customerID uuid.UUID) (*ExemptionStanding, error)
	// CustomerBranchDay is the customer's primary branch's business day, the
	// day the exemption window reads.
	CustomerBranchDay(ctx context.Context, customerID uuid.UUID) (time.Time, error)
}

// PostgresExemptionRepo implements ExemptionRepo with PostgreSQL.
type PostgresExemptionRepo struct {
	db *database.DB
}

// NewExemptionRepo creates a new PostgresExemptionRepo.
func NewExemptionRepo(db *database.DB) *PostgresExemptionRepo {
	return &PostgresExemptionRepo{db: db}
}

func (r *PostgresExemptionRepo) GetByCustomer(ctx context.Context, customerID uuid.UUID) ([]TaxExemption, error) {
	query := `
		SELECT id, customer_id, exempt_reason, certificate_number, issuing_state,
		       effective_date, expiry_date, is_active, created_at
		FROM tax_exemptions
		WHERE customer_id = $1
		ORDER BY created_at DESC`

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, customerID)
	if err != nil {
		return nil, fmt.Errorf("query exemptions: %w", err)
	}
	defer rows.Close()

	var exemptions []TaxExemption
	for rows.Next() {
		var ex TaxExemption
		if err := rows.Scan(
			&ex.ID, &ex.CustomerID, &ex.ExemptReason, &ex.CertificateNumber,
			&ex.IssuingState, &ex.EffectiveDate, &ex.ExpiryDate, &ex.IsActive, &ex.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan exemption: %w", err)
		}
		exemptions = append(exemptions, ex)
	}
	return exemptions, nil
}

func (r *PostgresExemptionRepo) Create(ctx context.Context, ex *TaxExemption) error {
	if ex.ID == uuid.Nil {
		ex.ID = uuid.New()
	}
	if ex.CreatedAt.IsZero() {
		ex.CreatedAt = time.Now()
	}
	if ex.EffectiveDate.IsZero() {
		// "today" for a sale is the branch's business day, so the default the
		// certificate takes is that day, not the process's clock (CP-10).
		day, err := r.CustomerBranchDay(ctx, ex.CustomerID)
		if err != nil {
			return err
		}
		ex.EffectiveDate = day
	}

	query := `
		INSERT INTO tax_exemptions (id, customer_id, exempt_reason, certificate_number,
		                            issuing_state, effective_date, expiry_date, is_active, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	_, err := r.db.GetExecutor(ctx).Exec(ctx, query,
		ex.ID, ex.CustomerID, ex.ExemptReason, ex.CertificateNumber,
		ex.IssuingState, ex.EffectiveDate, ex.ExpiryDate, ex.IsActive, ex.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert exemption: %w", err)
	}
	return nil
}

func (r *PostgresExemptionRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `DELETE FROM tax_exemptions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete exemption: %w", err)
	}
	return nil
}

// MarkInactive ends a certificate: is_active false, the row kept. The bool
// answers "ended by this call": false with a nil error when no such id
// exists, and errAlreadyEnded when the certificate was already ended.
func (r *PostgresExemptionRepo) MarkInactive(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE tax_exemptions SET is_active = false WHERE id = $1 AND is_active`, id)
	if err != nil {
		return false, fmt.Errorf("end exemption: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	var exists bool
	if err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM tax_exemptions WHERE id = $1)`, id).Scan(&exists); err != nil {
		return false, fmt.Errorf("read exemption: %w", err)
	}
	if exists {
		return false, errAlreadyEnded
	}
	return false, nil
}

// CustomerExists answers whether the customer row exists.
func (r *PostgresExemptionRepo) CustomerExists(ctx context.Context, customerID uuid.UUID) (bool, error) {
	var exists bool
	if err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM customers WHERE id = $1)`, customerID).Scan(&exists); err != nil {
		return false, fmt.Errorf("query customer: %w", err)
	}
	return exists, nil
}

// CustomerBranchDay is the customer's primary branch's business day.
func (r *PostgresExemptionRepo) CustomerBranchDay(ctx context.Context, customerID uuid.UUID) (time.Time, error) {
	var day time.Time
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT (timezone(COALESCE(l.timezone, 'UTC'), now()))::date
		FROM customers c LEFT JOIN locations l ON l.id = c.primary_branch_id
		WHERE c.id = $1`, customerID).Scan(&day); err != nil {
		return day, fmt.Errorf("query the customer's branch day: %w", err)
	}
	return day, nil
}

// CoveringNow reads the customer's covering certificate on its primary
// branch's business day.
func (r *PostgresExemptionRepo) CoveringNow(ctx context.Context, customerID uuid.UUID) (*ExemptionStanding, error) {
	day, err := r.CustomerBranchDay(ctx, customerID)
	if err != nil {
		return nil, err
	}
	return ExemptionCovering(ctx, r.db.GetExecutor(ctx), customerID, day)
}
