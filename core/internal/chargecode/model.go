// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package chargecode is the charge code master of ADR 0005 section 2.5: the
// fees a sales document can name on a charge line (freight, fuel,
// restocking, any other), each pointing at the revenue account its money
// posts to. A line snapshots the code's account at create, so a later edit
// of the code never moves posted revenue.
package chargecode

import (
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Code is one charge code row, the wire shape of /api/v1/charge-codes.
type Code struct {
	ID                 uuid.UUID       `json:"id"`
	Code               string          `json:"code"`
	Name               string          `json:"name"`
	RevenueAccountCode string          `json:"revenue_account_code"`
	Taxable            bool            `json:"taxable"`
	DefaultUnitPrice   *httpx.Price    `json:"default_unit_price_ten_thousandths"`
	IsActive           bool            `json:"is_active"`
	Revision           int64           `json:"revision"`
	CreatedAt          httpx.Timestamp `json:"created_at"`
	UpdatedAt          httpx.Timestamp `json:"updated_at"`
}

// ErrNotFound is the repository's answer for a code that does not exist; the
// service maps it to 404.
var ErrNotFound = errNotFound{}

type errNotFound struct{}

func (errNotFound) Error() string { return "charge code not found" }
