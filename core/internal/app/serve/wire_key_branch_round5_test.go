// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// Fix round 5 of the branch bound key pin: the findings of reviews 10 and 11
// and the principal's item 48 ruling, every leg through the real serve chain
// with bodies that pass validation, each foreign leg shadowed by an A control
// and measured by the rows that name the twin customer, never by whole table
// counts.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// round5Fixture is the pin fixture with the branch tax rates and the walled
// twins seeded, keeping the first mint (the pos and pricing scopes) available
// beside the document family mint.
func round5Fixture(t *testing.T) (*keyPinFixture, *walledTwins, string) {
	t.Helper()
	f := newKeyPinFixture(t)
	firstBound := f.bound
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE locations SET default_tax_rate = 0.05 WHERE id IN ($1, $2)`, f.branchA, f.branchB); err != nil {
		t.Fatalf("seed the branch tax rate: %v", err)
	}
	return f, f.seedWalledTwins(t), firstBound
}

// TestKeyBranchPin_PosSyncCustomerWall is round 10 P1-1: the offline sync
// replays whole sales whose bodies name a customer, so a key pinned to A
// posting a sale on a customer held only by B (its ACCOUNT tender invoicing
// that customer at branch A) is refused at the wall with nothing written. The
// A control completes the same sale on the pin's own customer, and the user
// keeps the reach.
func TestKeyBranchPin_PosSyncCustomerWall(t *testing.T) {
	f, _, firstBound := round5Fixture(t)
	// The register's completion deducts stock from the default (null location)
	// row; the fixture's teardown removes every inventory row of the product.
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO inventory (product_id, location_id, location, quantity) VALUES ($1, NULL, '', 50)`, f.posProd); err != nil {
		t.Fatalf("seed stock: %v", err)
	}
	client := uuid.New()
	freshClient := func() string { client = uuid.New(); return client.String() }
	sync := func(register, customer string) string {
		return fmt.Sprintf(`{"batch_id":"pin-%s","register_id":%q,"items":[{"client_id":%q,"register_id":%q,"cashier_id":%q,`+
			`"customer_id":%q,"items":[{"product_id":%q,"quantity":1,"uom":"EA"}],`+
			`"tenders":[{"method":"ACCOUNT","amount":1.08}],"client_created_at":%q}]}`,
			uuid.NewString()[:8], register, freshClient(), register, uuid.NewString(), customer,
			f.posProd.String(), time.Now().UTC().Format(time.RFC3339))
	}
	invoicesOn := func(customer uuid.UUID) (n int) {
		t.Helper()
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM invoices WHERE customer_id = $1`, customer).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	txnExists := func() bool {
		t.Helper()
		var ok bool
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT EXISTS(SELECT 1 FROM pos_transactions WHERE id = $1)`, client).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}

	before := invoicesOn(f.custB)
	rows := f.auditRows(t)
	status, buf := f.key(t, firstBound, "POST", "/api/v1/pos/sync", sync(f.regA, f.custB.String()), nil)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "a branch bound key may act only on its own branch") {
		t.Errorf("bound key pos sync naming the foreign customer: %d %s, want the pin's 403", status, buf)
	}
	if !strings.Contains(string(buf), "customer_id") {
		t.Errorf("the pos sync refusal does not name customer_id: %s", buf)
	}
	if after := f.auditRows(t); after != rows+1 {
		t.Errorf("pos sync refusal: key.branch_refused rows %d then %d, want one new row", rows, after)
	}
	if after := invoicesOn(f.custB); after != before {
		t.Errorf("the refused pos sync still wrote %d invoices naming the foreign customer", after-before)
	}
	if txnExists() {
		t.Errorf("the refused pos sync still wrote its replayed transaction")
	}

	// The A control: the same sale on the pin's own customer completes and its
	// ACCOUNT tender invoices that customer, and the user keeps the same reach
	// on the foreign customer's sale at the foreign register.
	ownBefore := invoicesOn(f.custID)
	status, buf = f.key(t, firstBound, "POST", "/api/v1/pos/sync", sync(f.regA, f.custID.String()), nil)
	if status != http.StatusOK || !strings.Contains(string(buf), `"synced_count":1`) {
		t.Errorf("bound key pos sync on the pin's own customer: %d %s, want the sale synced", status, buf)
	}
	if grown := invoicesOn(f.custID) - ownBefore; grown != 1 {
		t.Errorf("the control pos sync wrote %d invoices on the pin's customer, want its ACCOUNT tender's one", grown)
	}
	f.allow(t, "user", "POST", "/api/v1/pos/sync", sync(f.regB, f.custB.String()), nil, http.StatusOK)
}
