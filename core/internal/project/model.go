// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package project

import (
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// ProjectStatus is the closed vocabulary of a project. The storage
// vocabulary keeps its legacy casing (Active, Completed, Inactive from the
// 091 job merge); the wire vocabulary is lowercase and the mapping happens
// at the boundary (ADR 0001 section 6). A project's own writes accept
// active and completed; inactive is a storage value the 091 merge wrote and
// reads back as it is.
type ProjectStatus string

const (
	StatusActive    ProjectStatus = "active"
	StatusCompleted ProjectStatus = "completed"
	StatusInactive  ProjectStatus = "inactive"
)

// storageOf maps the wire vocabulary onto the storage vocabulary.
var storageOf = map[ProjectStatus]string{
	StatusActive:    "Active",
	StatusCompleted: "Completed",
	StatusInactive:  "Inactive",
}

// MarshalText writes the lowercase wire spelling.
func (s ProjectStatus) MarshalText() ([]byte, error) {
	return []byte(s), nil
}

// ParseProjectStatus accepts only the lowercase spellings a write may name.
func ParseProjectStatus(s string) (ProjectStatus, bool) {
	switch s {
	case "active":
		return StatusActive, true
	case "completed":
		return StatusCompleted, true
	}
	return "", false
}

// ProjectStatusNames lists the wire vocabulary, for error messages.
func ProjectStatusNames() []string { return []string{"active", "completed"} }

// fromStorage maps a storage status onto the wire's lowercase spelling;
// an unknown storage value lowercases rather than failing the read.
func fromStorage(s string) ProjectStatus {
	return ProjectStatus(strings.ToLower(s))
}

// maxName bounds a project name to its column.
const maxName = 255

// Project is one customer job, the wire shape of /api/portal/v1/projects.
type Project struct {
	ID         uuid.UUID       `json:"id"`
	CustomerID uuid.UUID       `json:"customer_id"`
	Name       string          `json:"name"`
	Status     ProjectStatus   `json:"status"`
	Revision   int64           `json:"revision"`
	CreatedAt  httpx.Timestamp `json:"created_at"`
	UpdatedAt  httpx.Timestamp `json:"updated_at"`
}

// ProjectItem is a summary of one document the project groups, on the
// project's wire: type and status lowercase, the total in integer cents.
type ProjectItem struct {
	ID         uuid.UUID       `json:"id"`
	Type       string          `json:"type"`
	Status     string          `json:"status"`
	TotalCents *int64          `json:"total_cents"`
	CreatedAt  httpx.Timestamp `json:"created_at"`
	Reference  string          `json:"reference"`
}

// ProjectDashboard aggregates the orders, deliveries and invoices grouped
// under one project.
type ProjectDashboard struct {
	Project    Project       `json:"project"`
	Orders     []ProjectItem `json:"orders"`
	Deliveries []ProjectItem `json:"deliveries"`
	Invoices   []ProjectItem `json:"invoices"`
}

// ErrNotFound reports that a read or write named a project that is not there
// (or belongs to another customer); the handler answers 404.
var ErrNotFound = errNotFound{}

type errNotFound struct{}

func (errNotFound) Error() string { return "project not found" }
