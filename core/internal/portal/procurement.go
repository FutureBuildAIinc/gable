// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	ProcurementStatusPlanning  = "PLANNING"
	ProcurementStatusQuoting   = "QUOTING"
	ProcurementStatusOrdered   = "ORDERED"
	ProcurementStatusFulfilled = "FULFILLED"
	ProcurementStatusCancelled = "CANCELLED"
)

var validProcurementTransitions = map[string][]string{
	ProcurementStatusPlanning:  {ProcurementStatusQuoting, ProcurementStatusCancelled},
	ProcurementStatusQuoting:   {ProcurementStatusOrdered, ProcurementStatusCancelled},
	ProcurementStatusOrdered:   {ProcurementStatusFulfilled, ProcurementStatusCancelled},
	ProcurementStatusFulfilled: {},
	ProcurementStatusCancelled: {},
}

// ProcurementLineDTO is one line on a named material list.
type ProcurementLineDTO struct {
	ID                       uuid.UUID  `json:"id"`
	ProjectID                uuid.UUID  `json:"project_id"`
	LineNo                   int        `json:"line_no"`
	ProductID                *uuid.UUID `json:"product_id,omitempty"`
	Description              string     `json:"description"`
	QtyEntered               float64    `json:"qty_entered"`
	UOM                      string     `json:"uom"`
	Notes                    string     `json:"notes"`
	IndicativeUnitPriceCents *int64     `json:"indicative_unit_price_cents"`
	IndicativeLineTotalCents *int64     `json:"indicative_line_total_cents"`
	CreatedAt                time.Time  `json:"created_at"`
}

// ProcurementProjectDTO is a contractor's named material list (procurement board).
type ProcurementProjectDTO struct {
	ID        uuid.UUID            `json:"id"`
	Name      string               `json:"name"`
	Status    string               `json:"status"`
	CreatedBy *uuid.UUID           `json:"created_by,omitempty"`
	QuoteID   *uuid.UUID           `json:"quote_id,omitempty"`
	CreatedAt time.Time            `json:"created_at"`
	UpdatedAt time.Time            `json:"updated_at"`
	Lines     []ProcurementLineDTO `json:"lines"`
}

type CreateProcurementProjectRequest struct {
	Name string `json:"name"`
}

type UpdateProcurementProjectRequest struct {
	Name   *string `json:"name,omitempty"`
	Status *string `json:"status,omitempty"`
}

type UpsertProcurementLineRequest struct {
	ProductID   *uuid.UUID `json:"product_id,omitempty"`
	Description string     `json:"description"`
	QtyEntered  float64    `json:"qty_entered"`
	UOM         string     `json:"uom"`
	Notes       string     `json:"notes"`
}

// ListProcurementProjects returns all named material lists for customerID.
func (s *Service) ListProcurementProjects(ctx context.Context, customerID uuid.UUID) ([]ProcurementProjectDTO, error) {
	pgRepo, ok := s.pgRepo()
	if !ok || pgRepo == nil {
		return []ProcurementProjectDTO{}, nil
	}
	exec := pgRepo.db.GetExecutor(ctx)

	rows, err := exec.Query(ctx, `
		SELECT id, name, status, created_by, quote_id, created_at, updated_at
		FROM procurement_projects
		WHERE customer_id = $1
		ORDER BY created_at DESC
	`, customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list material lists: %w", err)
	}
	defer rows.Close()

	out := make([]ProcurementProjectDTO, 0)
	for rows.Next() {
		var p ProcurementProjectDTO
		if err := rows.Scan(&p.ID, &p.Name, &p.Status, &p.CreatedBy, &p.QuoteID, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.Lines = []ProcurementLineDTO{}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CreateProcurementProject creates a new named material list in PLANNING state.
func (s *Service) CreateProcurementProject(ctx context.Context, customerID uuid.UUID, createdBy *uuid.UUID, req CreateProcurementProjectRequest) (*ProcurementProjectDTO, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalidRequest)
	}
	pgRepo, ok := s.pgRepo()
	if !ok || pgRepo == nil {
		return nil, ErrProcurementNotFound
	}
	exec := pgRepo.db.GetExecutor(ctx)

	var p ProcurementProjectDTO
	var creator *uuid.UUID
	if createdBy != nil && *createdBy != uuid.Nil {
		creator = createdBy
	}
	err := exec.QueryRow(ctx, `
		INSERT INTO procurement_projects (customer_id, name, status, created_by)
		VALUES ($1, $2, 'PLANNING', $3)
		RETURNING id, name, status, created_by, quote_id, created_at, updated_at
	`, customerID, name, creator).Scan(
		&p.ID, &p.Name, &p.Status, &p.CreatedBy, &p.QuoteID, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create material list: %w", err)
	}
	p.Lines = []ProcurementLineDTO{}
	return &p, nil
}

// GetProcurementProject loads a named material list and its lines for customerID.
func (s *Service) GetProcurementProject(ctx context.Context, customerID, projectID uuid.UUID) (*ProcurementProjectDTO, error) {
	pgRepo, ok := s.pgRepo()
	if !ok || pgRepo == nil {
		return nil, ErrProcurementNotFound
	}
	exec := pgRepo.db.GetExecutor(ctx)

	var p ProcurementProjectDTO
	err := exec.QueryRow(ctx, `
		SELECT id, name, status, created_by, quote_id, created_at, updated_at
		FROM procurement_projects
		WHERE id = $1 AND customer_id = $2
	`, projectID, customerID).Scan(
		&p.ID, &p.Name, &p.Status, &p.CreatedBy, &p.QuoteID, &p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProcurementNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read material list: %w", err)
	}

	lineRows, err := exec.Query(ctx, `
		SELECT id, project_id, line_no, product_id, description, qty_entered::float8, uom,
		       COALESCE(notes, ''), indicative_unit_price_cents, indicative_line_total_cents, created_at
		FROM procurement_project_lines
		WHERE project_id = $1
		ORDER BY line_no ASC, created_at ASC
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to read material list lines: %w", err)
	}
	defer lineRows.Close()

	p.Lines = make([]ProcurementLineDTO, 0)
	for lineRows.Next() {
		var l ProcurementLineDTO
		if err := lineRows.Scan(
			&l.ID, &l.ProjectID, &l.LineNo, &l.ProductID, &l.Description,
			&l.QtyEntered, &l.UOM, &l.Notes,
			&l.IndicativeUnitPriceCents, &l.IndicativeLineTotalCents, &l.CreatedAt,
		); err != nil {
			return nil, err
		}
		p.Lines = append(p.Lines, l)
	}
	if err := lineRows.Err(); err != nil {
		return nil, err
	}
	return &p, nil
}

// UpdateProcurementProject updates a material list's name and/or status.
func (s *Service) UpdateProcurementProject(ctx context.Context, customerID, projectID uuid.UUID, req UpdateProcurementProjectRequest) (*ProcurementProjectDTO, error) {
	cur, err := s.GetProcurementProject(ctx, customerID, projectID)
	if err != nil {
		return nil, err
	}

	newName := cur.Name
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			return nil, fmt.Errorf("%w: name cannot be empty", ErrInvalidRequest)
		}
		newName = trimmed
	}

	newStatus := cur.Status
	if req.Status != nil && *req.Status != cur.Status {
		target := strings.ToUpper(strings.TrimSpace(*req.Status))
		allowed := validProcurementTransitions[cur.Status]
		ok := false
		for _, a := range allowed {
			if a == target {
				ok = true
				break
			}
		}
		if !ok {
			return nil, ErrProcurementLocked
		}
		newStatus = target
	}

	pgRepo, _ := s.pgRepo()
	_, err = pgRepo.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE procurement_projects
		SET name = $3, status = $4, updated_at = NOW()
		WHERE id = $1 AND customer_id = $2
	`, projectID, customerID, newName, newStatus)
	if err != nil {
		return nil, fmt.Errorf("failed to update material list: %w", err)
	}
	return s.GetProcurementProject(ctx, customerID, projectID)
}

// DeleteProcurementProject deletes a material list if it is in PLANNING or CANCELLED.
func (s *Service) DeleteProcurementProject(ctx context.Context, customerID, projectID uuid.UUID) error {
	cur, err := s.GetProcurementProject(ctx, customerID, projectID)
	if err != nil {
		return err
	}
	if cur.Status != ProcurementStatusPlanning && cur.Status != ProcurementStatusCancelled {
		return ErrProcurementLocked
	}
	pgRepo, _ := s.pgRepo()
	_, err = pgRepo.db.GetExecutor(ctx).Exec(ctx, `
		DELETE FROM procurement_projects WHERE id = $1 AND customer_id = $2
	`, projectID, customerID)
	return err
}

// AddProcurementLine appends a line to a PLANNING material list.
func (s *Service) AddProcurementLine(ctx context.Context, customerID, projectID uuid.UUID, req UpsertProcurementLineRequest) (*ProcurementLineDTO, error) {
	cur, err := s.GetProcurementProject(ctx, customerID, projectID)
	if err != nil {
		return nil, err
	}
	if cur.Status != ProcurementStatusPlanning {
		return nil, ErrProcurementLocked
	}
	desc := strings.TrimSpace(req.Description)
	if desc == "" || req.QtyEntered <= 0 {
		return nil, fmt.Errorf("%w: description and positive qty_entered are required", ErrInvalidRequest)
	}
	uom := strings.ToUpper(strings.TrimSpace(req.UOM))
	if uom == "" {
		uom = "EA"
	}
	if !validUOMs[uom] {
		return nil, fmt.Errorf("%w: unknown uom %q", ErrInvalidRequest, uom)
	}

	pgRepo, _ := s.pgRepo()
	exec := pgRepo.db.GetExecutor(ctx)

	var l ProcurementLineDTO
	err = exec.QueryRow(ctx, `
		INSERT INTO procurement_project_lines (project_id, line_no, product_id, description, qty_entered, uom, notes)
		VALUES (
			$1,
			COALESCE((SELECT MAX(line_no) FROM procurement_project_lines WHERE project_id = $1), 0) + 1,
			$2, $3, $4, $5, $6
		)
		RETURNING id, project_id, line_no, product_id, description, qty_entered::float8, uom,
		          COALESCE(notes, ''), indicative_unit_price_cents, indicative_line_total_cents, created_at
	`, projectID, req.ProductID, desc, req.QtyEntered, uom, strings.TrimSpace(req.Notes)).Scan(
		&l.ID, &l.ProjectID, &l.LineNo, &l.ProductID, &l.Description,
		&l.QtyEntered, &l.UOM, &l.Notes,
		&l.IndicativeUnitPriceCents, &l.IndicativeLineTotalCents, &l.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to add material list line: %w", err)
	}
	return &l, nil
}

// UpdateProcurementLine updates a line on a PLANNING material list.
func (s *Service) UpdateProcurementLine(ctx context.Context, customerID, projectID, lineID uuid.UUID, req UpsertProcurementLineRequest) (*ProcurementLineDTO, error) {
	cur, err := s.GetProcurementProject(ctx, customerID, projectID)
	if err != nil {
		return nil, err
	}
	if cur.Status != ProcurementStatusPlanning {
		return nil, ErrProcurementLocked
	}
	desc := strings.TrimSpace(req.Description)
	if desc == "" || req.QtyEntered <= 0 {
		return nil, fmt.Errorf("%w: description and positive qty_entered are required", ErrInvalidRequest)
	}
	uom := strings.ToUpper(strings.TrimSpace(req.UOM))
	if uom == "" {
		uom = "EA"
	}
	if !validUOMs[uom] {
		return nil, fmt.Errorf("%w: unknown uom %q", ErrInvalidRequest, uom)
	}

	pgRepo, _ := s.pgRepo()
	exec := pgRepo.db.GetExecutor(ctx)

	var l ProcurementLineDTO
	err = exec.QueryRow(ctx, `
		UPDATE procurement_project_lines
		SET product_id = $3, description = $4, qty_entered = $5, uom = $6, notes = $7
		WHERE id = $1 AND project_id = $2
		RETURNING id, project_id, line_no, product_id, description, qty_entered::float8, uom,
		          COALESCE(notes, ''), indicative_unit_price_cents, indicative_line_total_cents, created_at
	`, lineID, projectID, req.ProductID, desc, req.QtyEntered, uom, strings.TrimSpace(req.Notes)).Scan(
		&l.ID, &l.ProjectID, &l.LineNo, &l.ProductID, &l.Description,
		&l.QtyEntered, &l.UOM, &l.Notes,
		&l.IndicativeUnitPriceCents, &l.IndicativeLineTotalCents, &l.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProcurementNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update material list line: %w", err)
	}
	return &l, nil
}

// DeleteProcurementLine deletes a line from a PLANNING material list.
func (s *Service) DeleteProcurementLine(ctx context.Context, customerID, projectID, lineID uuid.UUID) error {
	cur, err := s.GetProcurementProject(ctx, customerID, projectID)
	if err != nil {
		return err
	}
	if cur.Status != ProcurementStatusPlanning {
		return ErrProcurementLocked
	}
	pgRepo, _ := s.pgRepo()
	tag, err := pgRepo.db.GetExecutor(ctx).Exec(ctx, `
		DELETE FROM procurement_project_lines WHERE id = $1 AND project_id = $2
	`, lineID, projectID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrProcurementNotFound
	}
	return nil
}

// SubmitProcurementRFQ prices lines indicatively via pricing.Resolve, creates
// a portal quote request for the dealer, and transitions PLANNING -> QUOTING.
func (s *Service) SubmitProcurementRFQ(ctx context.Context, customerID, projectID uuid.UUID) (*ProcurementProjectDTO, error) {
	cur, err := s.GetProcurementProject(ctx, customerID, projectID)
	if err != nil {
		return nil, err
	}
	if cur.Status != ProcurementStatusPlanning {
		return nil, ErrProcurementLocked
	}
	if len(cur.Lines) == 0 {
		return nil, fmt.Errorf("%w: cannot submit an empty material list for RFQ", ErrInvalidRequest)
	}

	pgRepo, _ := s.pgRepo()
	exec := pgRepo.db.GetExecutor(ctx)

	quoteLines := make([]QuoteRequestLine, 0, len(cur.Lines))
	for _, l := range cur.Lines {
		if l.ProductID != nil {
			if price, _, ok := s.resolveCustomerPrice(ctx, customerID, *l.ProductID, l.UOM, l.QtyEntered); ok && price > 0 {
				unitCents := int64(math.Round(price * 100))
				totalCents := int64(math.Round(price * l.QtyEntered * 100))
				_, _ = exec.Exec(ctx, `
					UPDATE procurement_project_lines
					SET indicative_unit_price_cents = $2, indicative_line_total_cents = $3
					WHERE id = $1
				`, l.ID, unitCents, totalCents)
			}
		}
		quoteLines = append(quoteLines, QuoteRequestLine{
			ProductID:   l.ProductID,
			Description: l.Description,
			Quantity:    l.QtyEntered,
			UOM:         l.UOM,
			Note:        l.Notes,
		})
	}

	q, err := s.CreateQuoteRequest(ctx, customerID, CreateQuoteRequest{
		Notes: "Material list RFQ: " + cur.Name,
		Lines: quoteLines,
	})
	if err != nil {
		return nil, err
	}

	_, err = exec.Exec(ctx, `
		UPDATE procurement_projects
		SET status = 'QUOTING', quote_id = $3, updated_at = NOW()
		WHERE id = $1 AND customer_id = $2
	`, projectID, customerID, q.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to mark material list as QUOTING: %w", err)
	}

	return s.GetProcurementProject(ctx, customerID, projectID)
}
