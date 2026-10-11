// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice_test

import (
	"context"

	"github.com/gablelbm/gable/internal/invoice"
)

// makeLegacy writes an invoice the way the counter's account charge does:
// CreateInvoice then PostInvoiceToLedger inside the caller's transaction.
func makeLegacy(ctx context.Context, f *fixture) (*legacyDoc, error) {
	inv := &invoice.LegacyInvoice{CustomerID: f.customerID, BranchID: f.branchID,
		Lines: []invoice.LegacyLine{{ProductID: f.productID, Quantity: 2, PriceEach: 550}}}
	if err := f.invoices.CreateInvoice(ctx, inv); err != nil {
		return nil, err
	}
	if err := f.invoices.PostInvoiceToLedger(ctx, inv); err != nil {
		return nil, err
	}
	return &legacyDoc{id: inv.ID.String(), totalCents: inv.TotalAmount}, nil
}
