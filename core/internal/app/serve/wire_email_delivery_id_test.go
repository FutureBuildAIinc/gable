// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/gablelbm/gable/internal/testutil"
)

// Every email route's 202 names the delivery row it queued (CP-14): the desk
// follows a send by that id, so a route that answers none cannot be followed.
// The id read from the 202 must be the row of the document the route sent.
func TestEmailRoutesAnswerTheQueuedRowsID(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	A := f.branchA.String()
	ctx := context.Background()

	quoteA := uuid.New()
	seedWallQuote(t, db, quoteA, f.branchA, f.docCust, "WLI-"+quoteA.String()[:8])
	t.Cleanup(func() {
		f.docHandler.Wait()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM email_deliveries WHERE document_id IN ($1, $2, $3)`, quoteA, f.docCust, f.invA)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE id = $1`, quoteA)
	})

	for _, c := range []struct {
		name, path, role, docType string
		doc                       uuid.UUID
	}{
		{"statement", "/api/v1/ar/customers/" + f.docCust.String() + "/statement/email", "finance", "statement", f.docCust},
		{"quote", "/api/v1/quotes/" + quoteA.String() + "/email", "sales", "quote", quoteA},
		{"invoice", "/api/v1/invoices/" + f.invA.String() + "/email", "finance", "invoice", f.invA},
	} {
		t.Run(c.name, func(t *testing.T) {
			status, body := f.callBody(t, "POST", c.path, "", c.role, "u-a", A)
			if status != http.StatusAccepted {
				t.Fatalf("email %s: %d %s, want 202", c.name, status, body)
			}
			var got struct {
				Status     string `json:"status"`
				DeliveryID string `json:"delivery_id"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decode %s: %v", body, err)
			}
			id, err := uuid.Parse(got.DeliveryID)
			if err != nil || got.Status != "queued" {
				t.Fatalf("202 body %s: want status queued and a delivery_id", body)
			}
			f.docHandler.Wait()
			var docType string
			var docID uuid.UUID
			if err := db.Pool.QueryRow(ctx, `SELECT document_type, document_id FROM email_deliveries WHERE id = $1`, id).Scan(&docType, &docID); err != nil {
				t.Fatalf("the id names no delivery row: %v", err)
			}
			if docType != c.docType || docID != c.doc {
				t.Errorf("row %s is %s %s, want %s %s", id, docType, docID, c.docType, c.doc)
			}
		})
	}
}
