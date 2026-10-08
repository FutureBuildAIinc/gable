// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package governance

import (
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// RFCStatus is the lifecycle. The storage vocabulary is already the wire
// vocabulary (lowercase snake_case, ADR 0001 section 6): the column carries
// no CHECK and the module maps nothing.
type RFCStatus string

const (
	RFCStatusDraft    RFCStatus = "draft"
	RFCStatusReview   RFCStatus = "review"
	RFCStatusApproved RFCStatus = "approved"
	RFCStatusRejected RFCStatus = "rejected"
)

// statusNames is the wire vocabulary in lifecycle order.
var statusNames = []string{"draft", "review", "approved", "rejected"}

// ParseStatus accepts only the lowercase spelling.
func ParseStatus(name string) (RFCStatus, bool) {
	for _, n := range statusNames {
		if name == n {
			return RFCStatus(n), true
		}
	}
	return "", false
}

// StatusNames lists the vocabulary, for error messages.
func StatusNames() []string { return append([]string{}, statusNames...) }

// RFCSummary is the list item: everything but the body, so a list page never
// drags the content along.
type RFCSummary struct {
	ID        uuid.UUID       `json:"id"`
	Number    string          `json:"number"`
	Title     string          `json:"title"`
	Status    RFCStatus       `json:"status"`
	AuthorID  *uuid.UUID      `json:"author_id"`
	Revision  int64           `json:"revision"`
	CreatedAt httpx.Timestamp `json:"created_at"`
	UpdatedAt httpx.Timestamp `json:"updated_at"`
}

// RFC is the full document: the summary plus the problem, the proposal and
// the generated content. Content is null on rows the legacy seed wrote
// without one; the problem and the proposal are required on create, so their
// column's legacy NULLs read as the empty string.
type RFC struct {
	RFCSummary

	ProblemStatement string  `json:"problem_statement"`
	ProposedSolution string  `json:"proposed_solution"`
	Content          *string `json:"content"`
}
