// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"context"
	"fmt"
	"strings"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/quote"

	"github.com/johnfercher/maroto/v2/pkg/components/text"
	"github.com/johnfercher/maroto/v2/pkg/consts/align"
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

// GenerateQuotePDF renders a quote as the priced offer a customer is sent.
func (s *Service) GenerateQuotePDF(ctx context.Context, q *quote.Quote, cust *customer.Customer) ([]byte, error) {
	b := s.branding(ctx, q.BranchID)
	m := newDoc()
	b.header(m, "QUOTE", 18)

	left := joinLines(fmt.Sprintf("Prepared for: %s", cust.Name), fmt.Sprintf("Account: %s", cust.AccountNumber), deref(cust.Address))
	if n := s.jobName(ctx, q.JobID); n != "" {
		left = append(left, "Job: "+n)
	}
	right := joinLines(fmt.Sprintf("Quote #: %s", q.Number), fmt.Sprintf("Date: %s", q.CreatedAt.Format("2006-01-02")))
	if q.ExpiresAt != nil {
		right = append(right, "Valid until: "+q.ExpiresAt.Format("2006-01-02"))
	}
	block(m, left, right, 10)

	m.AddRow(10,
		text.NewCol(4, "SKU / Description", props.Text{Style: fontstyle.Bold}),
		text.NewCol(2, "Qty", props.Text{Style: fontstyle.Bold, Align: align.Center}),
		text.NewCol(3, "Price", props.Text{Style: fontstyle.Bold, Align: align.Right}),
		text.NewCol(3, "Total", props.Text{Style: fontstyle.Bold, Align: align.Right}),
	)
	for i := range q.Lines {
		l := &q.Lines[i]
		desc := l.Description
		if l.SKU != "" {
			desc = fmt.Sprintf("%s - %s", l.SKU, l.Description)
		}
		priceUOM := ""
		if l.PriceUOM != "" {
			priceUOM = "/" + l.PriceUOM
		}
		m.AddRow(10,
			text.NewCol(4, desc, props.Text{Size: 9}),
			text.NewCol(2, l.Quantity.WireString()+" "+string(l.UOM), props.Text{Size: 9, Align: align.Center}),
			text.NewCol(3, fmt.Sprintf("$%s%s", l.UnitPrice.DecimalString(), priceUOM), props.Text{Size: 9, Align: align.Right}),
			text.NewCol(3, "$"+l.LineTotal.DecimalString(), props.Text{Size: 9, Align: align.Right}),
		)
	}
	if q.FreightCents != 0 {
		m.AddRow(10, text.NewCol(12, "FREIGHT: $"+q.FreightCents.DecimalString(), props.Text{Align: align.Right, Size: 10, Top: 3}))
	}
	m.AddRow(15, text.NewCol(12, "TOTAL: $"+q.TotalCents.DecimalString(), props.Text{Top: 5, Style: fontstyle.Bold, Align: align.Right, Size: 12}))
	return finish(m)
}

// PurchaseOrderNumber is the number a purchase order prints under: its own
// number (PO-000001), the one the desk and the API show. A row without one
// falls back to its id's first block.
func PurchaseOrderNumber(po *purchase_order.PurchaseOrder) string {
	if po.Number != "" {
		return po.Number
	}
	return "PO-" + strings.ToUpper(strings.SplitN(po.ID.String(), "-", 2)[0])
}

// GeneratePurchaseOrderPDF renders a purchase order for its vendor, under the
// identity of the yard that placed it.
func (s *Service) GeneratePurchaseOrderPDF(ctx context.Context, po *purchase_order.PurchaseOrder) ([]byte, error) {
	b := s.branding(ctx, po.BranchID)
	m := newDoc()
	b.header(m, "PURCHASE ORDER", 18)

	block(m, joinLines(fmt.Sprintf("Vendor: %s", po.VendorName)),
		joinLines(fmt.Sprintf("PO #: %s", PurchaseOrderNumber(po)), fmt.Sprintf("Date: %s", po.CreatedAt.Format("2006-01-02"))), 10)
	m.AddRow(10,
		text.NewCol(6, "Description", props.Text{Style: fontstyle.Bold}),
		text.NewCol(2, "Qty", props.Text{Style: fontstyle.Bold, Align: align.Center}),
		text.NewCol(2, "Cost", props.Text{Style: fontstyle.Bold, Align: align.Right}),
		text.NewCol(2, "Total", props.Text{Style: fontstyle.Bold, Align: align.Right}),
	)
	var total float64
	for i := range po.Lines {
		l := &po.Lines[i]
		ext := l.Quantity * l.Cost
		total += ext
		m.AddRow(10,
			text.NewCol(6, l.Description, props.Text{Size: 9}),
			text.NewCol(2, fmt.Sprintf("%g", l.Quantity), props.Text{Size: 9, Align: align.Center}),
			text.NewCol(2, fmt.Sprintf("$%.2f", l.Cost), props.Text{Size: 9, Align: align.Right}),
			text.NewCol(2, fmt.Sprintf("$%.2f", ext), props.Text{Size: 9, Align: align.Right}),
		)
	}
	m.AddRow(15, text.NewCol(12, fmt.Sprintf("TOTAL: $%.2f", total), props.Text{Top: 5, Style: fontstyle.Bold, Align: align.Right, Size: 12}))
	return finish(m)
}
