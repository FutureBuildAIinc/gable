// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// PortalYardDTO is one yard the account may work with: a branch location the
// customer holds a customer_branches link to. Timezone is the yard's own zone
// (locations.timezone); every scheduling instant the API exchanges is UTC and
// the client renders it in this zone.
type PortalYardDTO struct {
	ID        uuid.UUID `json:"id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Timezone  string    `json:"timezone"`
	IsPrimary bool      `json:"is_primary"`
}

// PortalAccountDTO is the account (customer) the user belongs to.
type PortalAccountDTO struct {
	ID              uuid.UUID `json:"id"`
	Name            string    `json:"name"`
	AccountNumber   string    `json:"account_number"`
	PrimaryBranchID uuid.UUID `json:"primary_branch_id"`
}

// MeDTO is GET /api/portal/v1/me: the signed in user, the account, the yards
// the account holds, and the server resolved capabilities for the user's role.
// The capabilities are the same value the route guards resolve; a client that
// renders from them can never enable an action the server refuses.
type MeDTO struct {
	User         CustomerUser     `json:"user"`
	Account      PortalAccountDTO `json:"account"`
	Yards        []PortalYardDTO  `json:"yards"`
	Capabilities Capabilities     `json:"capabilities"`
}

// GetCustomerAccount reads the account row scoped by id (the customer id comes
// from the session claims, never from the request).
func (r *PostgresRepository) GetCustomerAccount(ctx context.Context, customerID uuid.UUID) (*PortalAccountDTO, error) {
	var a PortalAccountDTO
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT id, name, COALESCE(account_number, ''), primary_branch_id
		FROM customers WHERE id = $1
	`, customerID).Scan(&a.ID, &a.Name, &a.AccountNumber, &a.PrimaryBranchID)
	if err != nil {
		return nil, fmt.Errorf("failed to get account: %w", err)
	}
	return &a, nil
}

// ListCustomerYards reads the yards the account holds: the branches its
// customer_branches rows link to, the primary branch first.
func (r *PostgresRepository) ListCustomerYards(ctx context.Context, customerID uuid.UUID) ([]PortalYardDTO, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT l.id, COALESCE(l.code, ''), COALESCE(l.name, l.code, ''),
		       COALESCE(l.timezone, 'UTC'), (l.id = c.primary_branch_id)
		FROM customer_branches cb
		JOIN customers c ON c.id = cb.customer_id
		JOIN locations l ON l.id = cb.branch_id
		WHERE cb.customer_id = $1
		ORDER BY (l.id = c.primary_branch_id) DESC, l.code
	`, customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list yards: %w", err)
	}
	defer rows.Close()

	yards := make([]PortalYardDTO, 0)
	for rows.Next() {
		var y PortalYardDTO
		if err := rows.Scan(&y.ID, &y.Code, &y.Name, &y.Timezone, &y.IsPrimary); err != nil {
			return nil, fmt.Errorf("failed to scan yard: %w", err)
		}
		yards = append(yards, y)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("yards rows error: %w", err)
	}
	return yards, nil
}

// GetCustomerUserByID reads the caller's own user row, scoped to the customer.
func (r *PostgresRepository) GetCustomerUserByID(ctx context.Context, userID, customerID uuid.UUID) (*CustomerUser, error) {
	var u CustomerUser
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT id, customer_id, email, password_hash, name, role, status, created_at, updated_at
		FROM customer_users WHERE id = $1 AND customer_id = $2
	`, userID, customerID).Scan(
		&u.ID, &u.CustomerID, &u.Email, &u.PasswordHash, &u.Name, &u.Role, &u.Status,
		&u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	return &u, nil
}

// Me assembles the /me read. The capabilities come from the role the claims
// carry, which is the role the route guards resolve too; when the token's
// user row has moved on (a role change since login), the row's role wins for
// the user record and the answer stays honest by re-reading the row.
func (s *Service) Me(ctx context.Context, customerID uuid.UUID, userID uuid.UUID, claimsRole string) (*MeDTO, error) {
	role := claimsRole
	user, err := s.repo.GetCustomerUserByID(ctx, userID, customerID)
	if err == nil {
		role = user.Role
	} else if userID == uuid.Nil {
		user = &CustomerUser{
			CustomerID: customerID,
			Email:      "",
			Name:       "",
			Role:       claimsRole,
			Status:     "Active",
		}
	} else {
		return nil, err
	}

	account, err := s.repo.GetCustomerAccount(ctx, customerID)
	if err != nil {
		return nil, err
	}
	yards, err := s.repo.ListCustomerYards(ctx, customerID)
	if err != nil {
		return nil, err
	}

	return &MeDTO{
		User:         *user,
		Account:      *account,
		Yards:        yards,
		Capabilities: CapabilitiesFor(role),
	}, nil
}
