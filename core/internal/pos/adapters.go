// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"fmt"

	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/tax"
	"github.com/google/uuid"
)

// PaymentGateway is the card-present terminal rail (the payment package's
// own interface).
type PaymentGateway = payment.PaymentGateway

// chargeApproved is the gateway status a tender needs to join a sale.
const chargeApproved = payment.GatewayStatusApproved

// inventoryErrInsufficient maps the inventory package's issue refusal.
var inventoryErrInsufficient = inventory.ErrInsufficientOnHand

// salesdocProductRef aliases the product slice the cost read answers.
type salesdocProductRef = salesdoc.ProductRef

// taxPreviewRequest builds the provider call for a sale's lines.
func taxPreviewRequest(sale *Sale, lines []salesdoc.Line, taxableCents httpx.Cents) *tax.TaxPreviewRequest {
	req := &tax.TaxPreviewRequest{
		CustomerID:   sale.CustomerID,
		DocumentType: "SalesInvoice",
	}
	for i := range lines {
		l := &lines[i]
		if l.LineTotal == nil || !l.Taxable {
			continue
		}
		req.Lines = append(req.Lines, tax.TaxLineInput{
			LineNumber:  i + 1,
			ItemCode:    productCode(l.ProductID),
			Description: l.Description,
			Quantity:    float64(derefQty(l.Quantity)) / 10000,
			Amount:      int64(*l.LineTotal),
		})
	}
	return req
}

func productCode(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// chargeRequest builds the gateway charge of one card tender, in the sale's
// own currency (never a hard coded one).
func chargeRequest(saleID uuid.UUID, t *TenderIn, currency string) payment.ChargeRequest {
	return payment.ChargeRequest{
		TokenID:     t.TokenID,
		AmountCents: int64(t.AmountCents),
		Currency:    currency,
		Description: fmt.Sprintf("POS sale %s", saleID),
		InvoiceID:   saleID.String(),
	}
}

// paymentNumberOf reads a payment's number through the database.
func paymentNumberOf(ctx context.Context, s *Service, id uuid.UUID) (string, error) {
	var number string
	err := s.db.GetExecutor(ctx).QueryRow(ctx, `SELECT number FROM payments WHERE id = $1`, id).Scan(&number)
	if err != nil {
		return "", nil // the number is display data; a miss never fails the sale
	}
	return number, nil
}
