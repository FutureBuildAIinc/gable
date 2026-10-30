// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/invoice"
	"github.com/google/uuid"
)

// The change feed, extended from orders to invoices, quotes and deliveries
// (the pro portal's Part 7): the same pair of cache primitives on every list
// a phone carries offline, so a 30 second poll costs a 304. The rules are the
// order feed's rules (orders.go): `since` is strictly greater-than on
// updated_at, a `since` page is ordered updated_at ascending and
// tie-completed (FETCH FIRST ... WITH TIES) so the cursor cannot lose a row,
// and the ETag hashes the tenant and the filter in so it can never match
// across customers or queries.

// ListResult is the shared page shape: the rows plus the ETag and the cursor.
type ListResult[T any] struct {
	Rows            []T
	ETag            string
	LatestUpdatedAt *time.Time
}

// listETag is orderListETag's sibling for the new feeds: the same fields,
// namespaced by the feed's own tag so a validator from one feed can never
// match another's response.
func listETag(feed string, customerID uuid.UUID, since *time.Time, count int, latest *time.Time) string {
	h := sha256.New()
	fmt.Fprintf(h, "f=%s;c=%s;", feed, customerID)
	if since != nil {
		fmt.Fprintf(h, "s=%d;", since.UTC().UnixNano())
	}
	fmt.Fprintf(h, "n=%d;", count)
	if latest != nil {
		fmt.Fprintf(h, "t=%d;", latest.UTC().UnixNano())
	}
	return `"` + hex.EncodeToString(h.Sum(nil)[:16]) + `"`
}

// InvoiceListResult is a page of invoices plus the cache primitives.
type InvoiceListResult = ListResult[PortalInvoiceDTO]

// DeliveryListResult is a page of deliveries plus the cache primitives.
type DeliveryListResult = ListResult[PortalDeliveryDTO]

// QuoteListResult is a page of quotes plus the cache primitives.
type QuoteListResult = ListResult[PortalQuoteDTO]

// ListInvoicesFiltered narrows the invoice list by the change feed cursor.
func (s *Service) ListInvoicesFiltered(ctx context.Context, customerID uuid.UUID, since *time.Time) (*InvoiceListResult, error) {
	rows, err := s.repo.ListInvoicesByCustomerFiltered(ctx, customerID, since)
	if err != nil {
		return nil, err
	}
	latest := latestDTOUpdated(rows, func(d PortalInvoiceDTO) time.Time { return d.UpdatedAt })
	return &InvoiceListResult{
		Rows:            rows,
		LatestUpdatedAt: latest,
		ETag:            listETag("invoices", customerID, since, len(rows), latest),
	}, nil
}

// ListDeliveriesFiltered narrows the delivery list by the change feed cursor.
func (s *Service) ListDeliveriesFiltered(ctx context.Context, customerID uuid.UUID, since *time.Time) (*DeliveryListResult, error) {
	rows, err := s.repo.ListDeliveriesByCustomerFiltered(ctx, customerID, since)
	if err != nil {
		return nil, err
	}
	latest := latestDTOUpdated(rows, func(d PortalDeliveryDTO) time.Time { return d.UpdatedAt })
	return &DeliveryListResult{
		Rows:            rows,
		LatestUpdatedAt: latest,
		ETag:            listETag("deliveries", customerID, since, len(rows), latest),
	}, nil
}

// ListQuotesFiltered narrows the quote list by the change feed cursor.
func (s *Service) ListQuotesFiltered(ctx context.Context, customerID uuid.UUID, since *time.Time) (*QuoteListResult, error) {
	rows, err := s.repo.ListPortalQuotesFiltered(ctx, customerID, since)
	if err != nil {
		return nil, err
	}
	latest := latestDTOUpdated(rows, func(d PortalQuoteDTO) time.Time { return d.UpdatedAt })
	return &QuoteListResult{
		Rows:            rows,
		LatestUpdatedAt: latest,
		ETag:            listETag("quotes", customerID, since, len(rows), latest),
	}, nil
}

// latestDTOUpdated is latestOf over an explicit field read per DTO kind.
func latestDTOUpdated[T any](rows []T, at func(T) time.Time) *time.Time {
	var latest *time.Time
	for i := range rows {
		t := at(rows[i])
		if latest == nil || t.After(*latest) {
			v := t
			latest = &v
		}
	}
	return latest
}

// --- the filtered queries ----------------------------------------------------

const invoicePageSize = 50

// ListInvoicesByCustomerFiltered is the one invoice-list query; the legacy
// unfiltered list delegates to it.
func (r *PostgresRepository) ListInvoicesByCustomerFiltered(ctx context.Context, customerID uuid.UUID, since *time.Time) ([]PortalInvoiceDTO, error) {
	query := `
		SELECT i.id, i.order_id, i.status, i.total_amount, i.subtotal, i.tax_amount,
		       (SELECT pt.code FROM payment_terms pt WHERE pt.id = i.payment_terms_id), i.due_date, i.paid_at, i.created_at,
		       i.number, ` + invoice.OverdueExpr("i") + `, i.updated_at
		FROM invoices i
		WHERE i.customer_id = $1
	`
	args := []interface{}{customerID}
	if since != nil {
		query += ` AND i.updated_at > $2 ORDER BY i.updated_at ASC FETCH FIRST ` +
			fmt.Sprint(invoicePageSize) + ` ROWS WITH TIES`
		args = append(args, *since)
	} else {
		query += ` ORDER BY i.created_at DESC LIMIT ` + fmt.Sprint(invoicePageSize)
	}

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list invoices: %w", err)
	}
	defer rows.Close()

	invoices := make([]PortalInvoiceDTO, 0)
	for rows.Next() {
		var inv PortalInvoiceDTO
		if err := rows.Scan(
			&inv.ID, &inv.OrderID, &inv.Status, &inv.TotalAmount,
			&inv.Subtotal, &inv.TaxAmount, &inv.PaymentTerms,
			&inv.DueDate, &inv.PaidAt, &inv.CreatedAt, &inv.Number, &inv.IsOverdue, &inv.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan invoice: %w", err)
		}
		inv.Lines = make([]PortalLineDTO, 0)
		invoices = append(invoices, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration error: %w", err)
	}

	return invoices, nil
}

const deliveryPageSize = 50

// ListDeliveriesByCustomerFiltered is the one delivery-list query; the legacy
// unfiltered list delegates to it.
func (r *PostgresRepository) ListDeliveriesByCustomerFiltered(ctx context.Context, customerID uuid.UUID, since *time.Time) ([]PortalDeliveryDTO, error) {
	query := `
		SELECT d.id, d.order_id, d.status, d.pod_proof_url, d.pod_signed_by, d.pod_timestamp,
		       d.created_at, o.id::text,
		       dr.name, dr.phone_number, dr.photo_url,
		       v.name, v.photo_url,
		       rt.scheduled_date, d.estimated_arrival,
		       c.address,
		       d.stop_sequence, d.delivery_instructions,
		       (SELECT COUNT(*) FROM deliveries WHERE route_id = d.route_id),
		       d.updated_at, d.scheduled_start, d.scheduled_end
		FROM deliveries d
		JOIN orders o ON d.order_id = o.id
		JOIN customers c ON o.customer_id = c.id
		LEFT JOIN delivery_routes rt ON d.route_id = rt.id
		LEFT JOIN drivers dr ON rt.driver_id = dr.id
		LEFT JOIN vehicles v ON rt.vehicle_id = v.id
		WHERE o.customer_id = $1
	`
	args := []interface{}{customerID}
	if since != nil {
		query += ` AND d.updated_at > $2 ORDER BY d.updated_at ASC FETCH FIRST ` +
			fmt.Sprint(deliveryPageSize) + ` ROWS WITH TIES`
		args = append(args, *since)
	} else {
		query += ` ORDER BY d.created_at DESC LIMIT ` + fmt.Sprint(deliveryPageSize)
	}

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list deliveries: %w", err)
	}
	defer rows.Close()

	deliveries := make([]PortalDeliveryDTO, 0)
	for rows.Next() {
		var d PortalDeliveryDTO
		if err := rows.Scan(
			&d.ID, &d.OrderID, &d.Status, &d.PODProofURL, &d.PODSignedBy,
			&d.PODTimestamp, &d.CreatedAt, &d.OrderNumber,
			&d.DriverName, &d.DriverPhone, &d.DriverPhotoURL,
			&d.VehicleName, &d.VehiclePhotoURL,
			&d.ScheduledDate, &d.EstimatedArrival,
			&d.DeliveryAddress,
			&d.StopSequence, &d.DeliveryInstructions,
			&d.TotalStops, &d.UpdatedAt,
			&d.ScheduledStart, &d.ScheduledEnd,
		); err != nil {
			return nil, fmt.Errorf("failed to scan delivery: %w", err)
		}
		deliveries = append(deliveries, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration error: %w", err)
	}

	return deliveries, nil
}

// writeFeedHeaders sets the feed pair and answers a matching If-None-Match
// with a bodiless 304; otherwise the rows are written as the body. It is the
// one place the three new feeds do their conditional GET, so they cannot
// drift from the order feed's behaviour.
func writeFeedHeaders[T any](w http.ResponseWriter, r *http.Request, etag string, latest *time.Time, rows []T) {
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache, must-revalidate")
	if latest != nil {
		w.Header().Set("X-Portal-Latest-Change", latest.UTC().Format(time.RFC3339Nano))
	}
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	portalWriteJSON(w, rows)
}
