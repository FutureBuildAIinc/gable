// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"context"
	"fmt"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// Store reads the two small facts a form takes from the database directly:
// the issuing yard's block (the branch location's own columns) and a job's
// name. It satisfies YardReader and JobReader.
type Store struct{ db *database.DB }

func NewStore(db *database.DB) *Store { return &Store{db: db} }

func (s *Store) Yard(ctx context.Context, branch uuid.UUID) (*Yard, error) {
	var name, address, city, state, zip, phone, code *string
	err := s.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT name, address, city, state, zip, phone, code FROM locations WHERE id = $1`, branch).
		Scan(&name, &address, &city, &state, &zip, &phone, &code)
	if err != nil {
		return nil, fmt.Errorf("read the yard: %w", err)
	}
	y := &Yard{Address: str(address), City: str(city), State: str(state), Zip: str(zip), Phone: str(phone)}
	if y.Name = str(name); y.Name == "" {
		y.Name = str(code)
	}
	return y, nil
}

func (s *Store) JobName(ctx context.Context, id uuid.UUID) (string, error) {
	var name string
	if err := s.db.GetExecutor(ctx).QueryRow(ctx, `SELECT name FROM projects WHERE id = $1`, id).Scan(&name); err != nil {
		return "", fmt.Errorf("read the job: %w", err)
	}
	return name, nil
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
