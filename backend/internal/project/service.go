// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package project

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Service encapsulates project business logic.
type Service struct {
	repo Repository
}

// NewService creates a new project service.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// CreateProject creates a new project for a customer.
func (s *Service) CreateProject(ctx context.Context, customerID uuid.UUID, req CreateProjectRequest) (*Project, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("project name is required")
	}

	p := Project{
		ID:         uuid.New(),
		CustomerID: customerID,
		Name:       req.Name,
		Status:     "Active",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if err := s.repo.CreateProject(ctx, p); err != nil {
		return nil, err
	}

	return &p, nil
}

// ListProjects returns all projects for a customer.
func (s *Service) ListProjects(ctx context.Context, customerID uuid.UUID) ([]Project, error) {
	return s.repo.ListProjects(ctx, customerID)
}

// GetProjectDashboard retrieves a project and its associated documents.
func (s *Service) GetProjectDashboard(ctx context.Context, projectID, customerID uuid.UUID) (*ProjectDashboardDTO, error) {
	proj, err := s.repo.GetProject(ctx, projectID, customerID)
	if err != nil {
		return nil, err
	}

	orders, deliveries, invoices, err := s.repo.GetProjectEntities(ctx, projectID, customerID)
	if err != nil {
		return nil, err
	}

	return &ProjectDashboardDTO{
		Project:    *proj,
		Orders:     orders,
		Deliveries: deliveries,
		Invoices:   invoices,
	}, nil
}

// UpdateProject modifies an existing project.
func (s *Service) UpdateProject(ctx context.Context, projectID, customerID uuid.UUID, req UpdateProjectRequest) (*Project, error) {
	proj, err := s.repo.GetProject(ctx, projectID, customerID)
	if err != nil {
		return nil, err
	}

	// The same invariant CreateProject enforces above. `{"name":""}` is a
	// non-nil pointer to the empty string, so without this check it applies
	// straight through and leaves the project nameless — a rule that only held
	// for the first second of a project's life. The portal's job picker lists
	// projects by name, and a blank row cannot be selected or told apart from
	// its neighbours.
	if req.Name != nil {
		if *req.Name == "" {
			return nil, fmt.Errorf("project name is required")
		}
		proj.Name = *req.Name
	}
	if req.Status != nil {
		if *req.Status != "Active" && *req.Status != "Completed" {
			return nil, fmt.Errorf("invalid status")
		}
		proj.Status = *req.Status
	}

	if err := s.repo.UpdateProject(ctx, *proj); err != nil {
		return nil, err
	}

	return proj, nil
}
