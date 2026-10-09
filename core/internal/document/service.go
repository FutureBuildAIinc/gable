// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"context"
	"fmt"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/salesdoc"

	"github.com/johnfercher/maroto/v2"
	"github.com/johnfercher/maroto/v2/pkg/components/text"
	"github.com/johnfercher/maroto/v2/pkg/config"
	"github.com/johnfercher/maroto/v2/pkg/consts/align"
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

type Service struct {
	productRepo product.Repository
}

func NewService(productRepo product.Repository) *Service {
	return &Service{productRepo: productRepo}
}

func (s *Service) GenerateInvoicePDF(ctx context.Context, inv *invoice.Invoice, cust *customer.Customer) ([]byte, error) {
	m := maroto.New(config.NewBuilder().
		WithPageNumber().
		Build())

	title := "GABLE LBM - INVOICE"
	if inv.Status == invoice.InvoiceStatusVoid {
		title = "GABLE LBM - INVOICE - VOID"
	}
	m.AddRow(20,
		text.NewCol(12, title, props.Text{
			Size:  18,
			Style: fontstyle.Bold,
			Align: align.Center,
		}),
	)

	m.AddRow(20,
		text.NewCol(6, fmt.Sprintf("Bill To: %s\nAccount: %s", cust.Name, cust.AccountNumber), props.Text{Size: 10}),
		text.NewCol(6, fmt.Sprintf("Invoice #: %s\nDate: %s", inv.Number, inv.InvoiceDate), props.Text{Size: 10, Align: align.Right}),
	)

	m.AddRow(10,
		text.NewCol(4, "SKU / Description", props.Text{Style: fontstyle.Bold}),
		text.NewCol(2, "Qty", props.Text{Style: fontstyle.Bold, Align: align.Center}),
		text.NewCol(3, "Price", props.Text{Style: fontstyle.Bold, Align: align.Right}),
		text.NewCol(3, "Total", props.Text{Style: fontstyle.Bold, Align: align.Right}),
	)

	for i := range inv.Lines {
		line := &inv.Lines[i]
		desc := line.Description
		if line.SKU != nil && *line.SKU != "" {
			desc = fmt.Sprintf("%s - %s", *line.SKU, line.Description)
		}
		// A text line is a note; a kit's components are shown with their kit
		// at no charge; every other line prints its quantity, its price per
		// price unit and its extension, exactly as the invoice stores them.
		if line.Quantity == nil || line.UnitPrice == nil || line.LineTotal == nil {
			m.AddRow(10, text.NewCol(12, desc, props.Text{Size: 9}))
			continue
		}
		uom, priceUOM := "", ""
		if line.UOM != nil {
			uom = " " + *line.UOM
		}
		if line.PriceUOM != nil {
			priceUOM = "/" + *line.PriceUOM
		}
		m.AddRow(10,
			text.NewCol(4, desc, props.Text{Size: 9}),
			text.NewCol(2, line.Quantity.WireString()+uom, props.Text{Size: 9, Align: align.Center}),
			text.NewCol(3, fmt.Sprintf("$%s%s", line.UnitPrice.DecimalString(), priceUOM), props.Text{Size: 9, Align: align.Right}),
			text.NewCol(3, "$"+line.LineTotal.DecimalString(), props.Text{Size: 9, Align: align.Right}),
		)
	}

	// Subtotal and tax are shown separately, or the line extensions visibly do
	// not add up to TOTAL DUE; the tax as its own line is also a statutory
	// requirement in the GST/HST jurisdictions this product targets.
	m.AddRow(10,
		text.NewCol(12, "SUBTOTAL: $"+inv.SubtotalCents.DecimalString(), props.Text{
			Top:   5,
			Align: align.Right,
			Size:  10,
		}),
	)

	taxLabel := "TAX"
	if inv.TaxRatePercent != nil {
		taxLabel = fmt.Sprintf("TAX @ %s%%", *inv.TaxRatePercent)
	}
	m.AddRow(10,
		text.NewCol(12, fmt.Sprintf("%s: $%s", taxLabel, inv.TaxCents.DecimalString()), props.Text{
			Align: align.Right,
			Size:  10,
		}),
	)

	m.AddRow(15,
		text.NewCol(12, "TOTAL DUE: $"+inv.TotalCents.DecimalString(), props.Text{
			Top:   5,
			Style: fontstyle.Bold,
			Align: align.Right,
			Size:  12,
		}),
	)

	// Mock "Pay Now" Link
	m.AddRow(10,
		text.NewCol(12, fmt.Sprintf("PAY ONLINE: https://app.gable.com/pay/%s", inv.ID), props.Text{
			Top:   2,
			Style: fontstyle.Italic,
			Align: align.Center,
			Size:  10,
			Color: &props.Color{Red: 0, Green: 0, Blue: 255},
		}),
	)

	doc, err := m.Generate()
	if err != nil {
		return nil, err
	}
	return doc.GetBytes(), nil
}

func (s *Service) GeneratePickTicketPDF(ctx context.Context, o *order.Order, cust *customer.Customer) ([]byte, error) {
	m := maroto.New(config.NewBuilder().
		WithPageNumber().
		Build())

	m.AddRow(20,
		text.NewCol(12, "PICK TICKET", props.Text{
			Size:  24,
			Style: fontstyle.Bold,
			Align: align.Center,
		}),
	)

	m.AddRow(20,
		text.NewCol(6, fmt.Sprintf("Customer: %s\nJob: %s", cust.Name, "N/A"), props.Text{Size: 10}),
		text.NewCol(6, fmt.Sprintf("Order #: %s\nDate: %s", o.ID, o.CreatedAt.Format("2006-01-02")), props.Text{Size: 10, Align: align.Right}),
	)

	m.AddRow(10,
		text.NewCol(8, "SKU / Description", props.Text{Style: fontstyle.Bold}),
		text.NewCol(4, "Qty to Pick", props.Text{Style: fontstyle.Bold, Align: align.Center}),
	)

	for i := range o.Lines {
		line := &o.Lines[i]
		// A pick ticket prints the goods: text notes and charge lines have
		// nothing to pick.
		if line.LineType != salesdoc.LineProduct && line.LineType != salesdoc.LineComponent {
			continue
		}
		desc := line.Description
		uom := "EA"
		if line.UOM != nil {
			uom = *line.UOM
		}
		if line.SKU != nil && *line.SKU != "" {
			desc = fmt.Sprintf("%s - %s", *line.SKU, desc)
		}

		m.AddRow(18,
			text.NewCol(8, desc, props.Text{Size: 12}),
			text.NewCol(4, fmt.Sprintf("%s [%s]", line.Quantity.WireString(), uom), props.Text{Size: 14, Style: fontstyle.Bold, Align: align.Center}),
		)
	}

	doc, err := m.Generate()
	if err != nil {
		return nil, err
	}
	return doc.GetBytes(), nil
}
