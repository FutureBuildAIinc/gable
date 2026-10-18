// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Price wall 1 (contractor to dealer). The dealer's desk and API read a
// contractor's customer quote only through the types in this file. They carry
// what the contractor shares by default (ruling 5a): the project, the job
// site, the customer and the site contact, the selection status and the
// material pipeline at the dealer's own prices. They have no field for a
// markup rule, basis points, a labour or overhead line, a margin, or any
// price the contractor charges their customer.

// DealerProject is one contractor project as the dealer sees it.
type DealerProject struct {
	ID               uuid.UUID            `json:"id"`
	Reference        string               `json:"reference"`
	Contractor       DealerContractor     `json:"contractor"`
	Project          *DealerProjectRef    `json:"project"`
	JobSiteAddress   string               `json:"job_site_address"`
	Customer         Contact              `json:"customer"`
	SiteContact      DealerSiteContact    `json:"site_contact"`
	Status           string               `json:"status"`
	Selections       DealerSelectionCount `json:"selections"`
	Materials        []DealerMaterial     `json:"materials"`
	MaterialCents    int64                `json:"material_cents"`
	Order            *DealerOrderRef      `json:"order"`
	DealerAcceptedAt *httpx.Timestamp     `json:"dealer_accepted_at"`
	UpdatedAt        httpx.Timestamp      `json:"updated_at"`
}

// DealerContractor names the dealer's customer who owns the quote.
type DealerContractor struct {
	CustomerID uuid.UUID `json:"customer_id"`
	Name       string    `json:"name"`
}

// DealerProjectRef is the contractor's project the quote hangs from.
type DealerProjectRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// DealerSiteContact is the person at the job site.
type DealerSiteContact struct {
	Name  string `json:"name"`
	Phone string `json:"phone,omitempty"`
}

// DealerSelectionCount is the customer's progress through the selections.
type DealerSelectionCount struct {
	Approved int `json:"approved"`
	Declined int `json:"declined"`
	Pending  int `json:"pending"`
}

// DealerMaterial is one material line at the dealer's own price: what the
// contractor pays the dealer, which the dealer already knows.
type DealerMaterial struct {
	SKU             string         `json:"sku"`
	Description     string         `json:"description"`
	Quantity        httpx.Quantity `json:"quantity"`
	UOM             string         `json:"uom"`
	DealerCostCents int64          `json:"dealer_cost_cents"`
	Selection       string         `json:"selection"`
}

// DealerOrderRef is the converted order, at the dealer's prices.
type DealerOrderRef struct {
	ID         uuid.UUID `json:"id"`
	Number     string    `json:"number"`
	Status     string    `json:"status"`
	TotalCents int64     `json:"total_cents"`
}

// DealerProjectDetail adds the approval record.
type DealerProjectDetail struct {
	DealerProject
	Approvals []DealerApproval `json:"approvals"`
}

// DealerApproval is one step of the approval record as the dealer reads it:
// who and when, and the hash of what the customer decided. The decision's
// own figures are the contractor's.
type DealerApproval struct {
	Step           string          `json:"step"`
	ActorKind      string          `json:"actor_kind"`
	ActorName      string          `json:"actor_name"`
	OccurredAt     httpx.Timestamp `json:"occurred_at"`
	SnapshotSHA256 string          `json:"snapshot_sha256,omitempty"`
}

// DealerInputs are what the dealer view needs besides the quote.
type DealerInputs struct {
	ContractorName string
	ProjectName    string
	Order          *DealerOrderRef
}

// BuildDealerProject builds the dealer's view of a quote. The materials are
// every line (with the customer's decision), at the cost the dealer charged.
func BuildDealerProject(q *Quote, in DealerInputs) DealerProject {
	d := DealerProject{
		ID: q.ID, Reference: q.Reference,
		Contractor:     DealerContractor{CustomerID: q.CustomerID, Name: in.ContractorName},
		JobSiteAddress: q.JobSiteAddress,
		Customer:       q.EndCustomer,
		SiteContact:    DealerSiteContact{Name: q.SiteContact.Name, Phone: q.SiteContact.Phone},
		Status:         string(q.Status),
		Materials:      []DealerMaterial{},
		Order:          in.Order,
		UpdatedAt:      httpx.TimestampOf(q.UpdatedAt),
	}
	if q.ProjectID != nil {
		d.Project = &DealerProjectRef{ID: *q.ProjectID, Name: in.ProjectName}
	}
	if q.DealerAcceptedAt != nil {
		ts := httpx.TimestampOf(*q.DealerAcceptedAt)
		d.DealerAcceptedAt = &ts
	}
	for _, l := range q.Lines {
		switch l.Selection {
		case SelectionApproved:
			d.Selections.Approved++
		case SelectionDeclined:
			d.Selections.Declined++
		default:
			d.Selections.Pending++
		}
		d.Materials = append(d.Materials, DealerMaterial{SKU: l.SKU, Description: l.Description, Quantity: l.Quantity,
			UOM: l.UOM, DealerCostCents: l.CostCents, Selection: string(l.Selection)})
	}
	for _, l := range IncludedLines(q.TotalsInput().Lines, q.States()) {
		d.MaterialCents += l.CostCents
	}
	return d
}

// DealerApprovalOf is the dealer's view of a log row.
func DealerApprovalOf(e ApprovalEntry) DealerApproval {
	return DealerApproval{Step: e.Step, ActorKind: e.ActorKind, ActorName: e.ActorName,
		OccurredAt: httpx.TimestampOf(e.OccurredAt), SnapshotSHA256: e.SnapshotSHA256}
}
