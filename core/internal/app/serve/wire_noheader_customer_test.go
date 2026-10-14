// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The counter's customer through the wire (round 21 of the counter's pull
// request): a customer is named at the create and in the offline sync by the
// same rule the lookup and the attach follow, the customer branch wall. A
// cashier granted only A, with no header, who names B's customer is answered
// like a missing customer and nothing is written; a branch bound key keeps its
// pin on both routes.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNoHeader_CounterNamesACustomerThroughTheWall(t *testing.T) {
	f := newKeyPinFixture(t)
	noheaderGrant(t, f, "pin-cashier", f.branchA)
	noheaderTaxRate(t, f, f.branchA)
	noheaderStock(t, f)
	regNA := noheaderRegister(t, f, f.branchA, f.yardA)

	named := func(customer uuid.UUID) int {
		return noheaderCount(t, f, `SELECT COUNT(*) FROM pos_transactions WHERE customer_id = $1`, customer)
	}
	foreignBefore := named(f.custB)
	status, buf := f.cashierNoHeader(t, "POST", "/api/v1/pos/transactions",
		fmt.Sprintf(`{"register_id":%q,"customer_id":%q}`, regNA, f.custB.String()))
	if status != http.StatusNotFound {
		t.Errorf("no header create naming B's customer: %d %s, want 404", status, buf)
	}
	if after := named(f.custB); after != foreignBefore {
		t.Errorf("the refused create wrote %d sales naming B's customer", after-foreignBefore)
	}
	status, buf = f.cashierNoHeader(t, "POST", "/api/v1/pos/transactions",
		fmt.Sprintf(`{"register_id":%q,"customer_id":%q}`, regNA, f.custID.String()))
	if status != http.StatusCreated {
		t.Errorf("no header create naming A's customer: %d %s, want 201", status, buf)
	}

	// The offline sync: the item naming B's customer is an error, written
	// nowhere; the item naming A's customer syncs.
	sync := func(customer uuid.UUID) (string, string) {
		client := uuid.NewString()
		return client, fmt.Sprintf(`{"batch_id":"cw-%s","register_id":%q,"items":[{"client_id":%q,"cashier_id":%q,`+
			`"customer_id":%q,"items":[{"product_id":%q,"quantity":"1"}],`+
			`"tenders":[{"method":"account","amount_cents":105}],"client_created_at":%q}]}`,
			uuid.NewString()[:8], regNA, client, uuid.NewString(), customer.String(),
			f.posProd.String(), time.Now().UTC().Format(time.RFC3339))
	}
	invoicesBefore := noheaderCount(t, f, `SELECT COUNT(*) FROM invoices WHERE customer_id = $1`, f.custB)
	client, req := sync(f.custB)
	status, buf = f.cashierNoHeader(t, "POST", "/api/v1/pos/sync", req)
	if status != http.StatusOK && status != http.StatusForbidden {
		t.Errorf("no header sync naming B's customer: %d %s", status, buf)
	}
	if strings.Contains(string(buf), `"synced_count":1`) {
		t.Errorf("no header sync of B's customer synced: %s", buf)
	}
	if n := noheaderCount(t, f, `SELECT COUNT(*) FROM pos_transactions WHERE id = $1`, client); n != 0 {
		t.Errorf("the refused sync item wrote its sale")
	}
	if after := noheaderCount(t, f, `SELECT COUNT(*) FROM invoices WHERE customer_id = $1`, f.custB); after != invoicesBefore {
		t.Errorf("the refused sync item posted %d invoices to B's customer", after-invoicesBefore)
	}
	_, req = sync(f.custID)
	status, buf = f.cashierNoHeader(t, "POST", "/api/v1/pos/sync", req)
	if status != http.StatusOK || !strings.Contains(string(buf), `"synced_count":1`) {
		t.Errorf("no header sync of A's customer: %d %s, want the sale synced", status, buf)
	}

	// The bound key keeps its pin: a machine key is no cashier, and the
	// foreign customer is held at the wall besides; nothing is written.
	foreignBefore = named(f.custB)
	keyStatus, keyBuf := f.key(t, f.bound, "POST", "/api/v1/pos/transactions",
		fmt.Sprintf(`{"register_id":%q,"customer_id":%q}`, regNA, f.custB.String()), nil)
	if keyStatus != http.StatusForbidden {
		t.Errorf("bound key create naming B's customer: %d %s, want 403", keyStatus, keyBuf)
	}
	if after := named(f.custB); after != foreignBefore {
		t.Errorf("the bound key's create wrote %d sales naming B's customer", after-foreignBefore)
	}
	_, req = sync(f.custB)
	keyStatus, keyBuf = f.key(t, f.bound, "POST", "/api/v1/pos/sync", req, nil)
	if keyStatus != http.StatusForbidden {
		t.Errorf("bound key sync naming B's customer: %d %s, want 403", keyStatus, keyBuf)
	}
}
