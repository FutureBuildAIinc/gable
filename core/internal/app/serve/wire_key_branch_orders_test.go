// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestKeyBranchPin_OrderOwnsItsShipToAndContact is round 9 P1-2: an order
// refuses a ship-to or a contact its customer does not own, for every caller,
// as the credit memo create does, naming the field; a matching ship-to and
// contact still work. Branch A's order naming branch B's ship-to would
// otherwise snapshot another customer's address.
func TestKeyBranchPin_OrderOwnsItsShipToAndContact(t *testing.T) {
	f, w := round4Fixture(t)
	A := f.custID.String()
	line := f.productLine()
	// The unbound key and the user name the branch; the bound key takes its pin.
	branch := fmt.Sprintf(`"branch_id":%q,`, f.branchA.String())
	body := func(extra string) string {
		return fmt.Sprintf(`{"customer_id":%q,"delivery_type":"pickup",%s%s"lines":[%s]}`, A, branch, extra, line)
	}
	ordersOfA := func() int {
		var n int
		if err := f.db.Pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM orders WHERE customer_id = $1`, f.custID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, who := range []string{"bound", "unbound", "user"} {
		before := ordersOfA()
		for _, c := range []struct{ field, extra string }{
			{"ship_to_id", fmt.Sprintf(`"ship_to_id":%q,`, w.shipB.String())},
			{"ordered_by_contact_id", fmt.Sprintf(`"ordered_by_contact_id":%q,`, w.conB.String())},
		} {
			status, buf := f.send(t, who, "POST", "/api/v1/orders", body(c.extra), nil)
			if status != http.StatusBadRequest || !strings.Contains(string(buf), c.field) {
				t.Errorf("%s POST /orders naming the foreign %s: %d %s, want a 400 naming it", who, c.field, status, buf)
			}
		}
		if n := ordersOfA(); n != before {
			t.Errorf("%s: a refused order was written: %d then %d orders of the customer", who, before, n)
		}
		own := f.made(t, who, "orders", "POST", "/api/v1/orders",
			body(fmt.Sprintf(`"ship_to_id":%q,"ordered_by_contact_id":%q,`, w.shipA.String(), w.conA.String())), http.StatusCreated)
		if own == uuid.Nil {
			continue
		}
		// The update reaches the same build.
		put := func(extra string) string {
			return fmt.Sprintf(`{"customer_id":%q,"delivery_type":"pickup",%s"revision":1,"lines":[%s]}`, A, extra, line)
		}
		status, buf := f.send(t, who, "PUT", "/api/v1/orders/"+own.String(), put(fmt.Sprintf(`"ship_to_id":%q,`, w.shipB.String())), nil)
		if status != http.StatusBadRequest || !strings.Contains(string(buf), "ship_to_id") {
			t.Errorf("%s PUT /orders naming the foreign ship-to: %d %s, want a 400 naming it", who, status, buf)
		}
		f.allow(t, who, "PUT", "/api/v1/orders/"+own.String(), put(fmt.Sprintf(`"ship_to_id":%q,`, w.shipA.String())), nil, http.StatusOK)
	}
}
