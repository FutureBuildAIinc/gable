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

// activitiesOn counts the activities a customer's record carries.
func (f *keyPinFixture) activitiesOn(t *testing.T, customer uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM crm_activities WHERE customer_id = $1`, customer).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestKeyBranchPin_QuoteFollowUpCustomerWall is QE-04-a: a key pinned to A
// wrote a follow up on a quote at A whose customer was held only by B, and
// the call it logged landed on the other customer's record. The follow up
// write and the quote activity routes act on the quote the path names, so
// they are held through the quote's own customer, as the convert is: the
// quote's branch is not enough, the customer the key is bound to is the wall.
//
// The A control: the same two calls on the pin's own customer's quote are
// answered, and the follow up lands on the quote.
func TestKeyBranchPin_QuoteFollowUpCustomerWall(t *testing.T) {
	f, _, _ := round5Fixture(t)
	ctx := context.Background()

	// A quote at the pin's branch whose customer is held only by the other
	// branch: the branch check passes, the customer wall is what refuses.
	foreignQuote := func() uuid.UUID {
		t.Helper()
		body := fmt.Sprintf(`{"customer_id":%q,"branch_id":%q,"lines":[%s]}`,
			f.custB.String(), f.branchA.String(), f.quoteLine())
		id := f.made(t, "user", "quotes", "POST", "/api/v1/quotes", body, http.StatusCreated)
		unlink(t, f, f.custB, f.branchA)
		return id
	}

	refuse := func(label, method, path, body string) {
		t.Helper()
		rows := f.auditRows(t)
		status, buf := f.key(t, f.bound, method, path, body, nil)
		if status != http.StatusForbidden || !strings.Contains(string(buf), "a branch bound key may act only on its own branch") {
			t.Errorf("%s: bound key on the foreign customer's quote: %d %s, want the pin's 403", label, status, buf)
		}
		if after := f.auditRows(t); after != rows+1 {
			t.Errorf("%s: the refusal wrote %d audit rows then %d, want one new row", label, rows, after)
		}
	}

	// The follow up write: refused on the foreign customer's quote.
	id := foreignQuote()
	rev := f.readJSON(t, "/api/v1/quotes/"+id.String())["revision"]
	body := fmt.Sprintf(`{"follow_up_at":%q,"revision":%v}`, "2030-01-02T00:00:00Z", rev)
	refuse("the follow up write", "PUT", "/api/v1/quotes/"+id.String()+"/follow-up", body)
	var stored *string
	if err := f.db.Pool.QueryRow(ctx, `SELECT follow_up_at::text FROM quotes WHERE id = $1`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != nil {
		t.Errorf("the refused follow up still wrote %q on the quote", *stored)
	}

	// The activity write: refused on the foreign customer's quote, and no
	// activity landed on the other customer's record.
	act := foreignQuote()
	actsBefore := f.activitiesOn(t, f.custB)
	refuse("the quote activity", "POST", "/api/v1/quotes/"+act.String()+"/activities",
		`{"activity_type":"call","description":"pin call on the quote"}`)
	if after := f.activitiesOn(t, f.custB); after != actsBefore {
		t.Errorf("the refused activity still wrote %d rows on the other customer", after-actsBefore)
	}

	// The A control: the pin's own customer's quote takes both calls.
	ownBody := fmt.Sprintf(`{"customer_id":%q,"branch_id":%q,"lines":[%s]}`,
		f.custID.String(), f.branchA.String(), f.quoteLine())
	own := f.made(t, "user", "quotes", "POST", "/api/v1/quotes", ownBody, http.StatusCreated)
	rev = f.readJSON(t, "/api/v1/quotes/"+own.String())["revision"]
	setBody := fmt.Sprintf(`{"follow_up_at":%q,"revision":%v}`, "2030-01-02T00:00:00Z", rev)
	status, buf := f.key(t, f.bound, "PUT", "/api/v1/quotes/"+own.String()+"/follow-up", setBody, nil)
	if status != http.StatusOK {
		t.Fatalf("the pin's own customer's follow up write: %d %s, want 200", status, buf)
	}
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT follow_up_at::text FROM quotes WHERE id = $1`, own).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == nil {
		t.Error("the pin's own follow up did not land on the quote")
	}
	status, buf = f.key(t, f.bound, "POST", "/api/v1/quotes/"+own.String()+"/activities",
		`{"activity_type":"call","description":"pin call on the quote"}`, nil)
	if status != http.StatusCreated {
		t.Fatalf("the pin's own customer's activity: %d %s, want 201", status, buf)
	}
}
