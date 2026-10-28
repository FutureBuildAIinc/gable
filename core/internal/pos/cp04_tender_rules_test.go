// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The counter's tender rules a dealer sets (CP-04): the check ID rule
// (pos.check_id_required_over_cents, a system_settings row the dealer writes
// on the Counter rules screen), and the settings routes that carry it with
// the database wide check limit.

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// setIDRule writes the database wide check ID rule for one test: absent
// (never), "0" (every check) or a figure in cents (checks over it).
func setIDRule(t *testing.T, f *fixture, cents string) {
	t.Helper()
	if cents == "" {
		mustExec(t, f.db, `DELETE FROM system_settings WHERE key = 'pos.check_id_required_over_cents'`)
		return
	}
	mustExec(t, f.db, `INSERT INTO system_settings (key, value) VALUES ('pos.check_id_required_over_cents', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, cents)
	t.Cleanup(func() {
		mustExec(t, f.db, `DELETE FROM system_settings WHERE key = 'pos.check_id_required_over_cents'`)
	})
}

// idTender is a check tender with the ID recorded: checked, and its type.
func idTender(cents int64, idType string) map[string]any {
	return map[string]any{"method": "check", "amount_cents": cents, "id_checked": true, "id_type": idType}
}

// RULE: a check the dealer's ID rule covers is refused at the completion
// until the tender records that the customer's ID was checked (409
// id_required); with the ID checked it completes.
func TestCP04_CheckOverIDThresholdNeedsID(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	setIDRule(t, f, "1000")

	under, underTotal := f.cartOf("1", nil) // 599
	if r := f.completeSale(under, tender("check", underTotal)); r.status != http.StatusOK {
		t.Fatalf("a check under the ID rule's figure = %d: %s", r.status, r.raw)
	}

	over, overTotal := f.cartOf("2", nil) // 1198
	stock := f.stock()
	invoices := f.counterInvoices(t)
	r := f.completeSale(over, tender("check", overTotal))
	blockerOf(t, r, "id_required")
	if want := "Checks over 10.00 USD in total"; !strings.Contains(string(r.raw), want) {
		t.Errorf("the refusal %s does not say what the rule asks", r.raw)
	}
	if f.stock() != stock {
		t.Errorf("a refused check moved stock")
	}
	// nothing was written: the sale stays open with no invoice behind it
	if got := str(t, f.getSale(t, over), "status"); got != "open" {
		t.Errorf("the refused sale status = %q, want open", got)
	}
	if got := f.counterInvoices(t); got != invoices {
		t.Errorf("%d invoices after the refusal, want %d", got, invoices)
	}
	// with the ID checked and its type the same check completes
	if r := f.completeSale(over, idTender(overTotal, "drivers_license")); r.status != http.StatusOK {
		t.Errorf("a check with the ID checked = %d: %s", r.status, r.raw)
	}
}

// RULE: a rule of 0 asks for the ID on every check, even a small one.
func TestCP04_IDRuleEveryCheck(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	setIDRule(t, f, "0")

	sale, total := f.cartOf("1", nil)
	blockerOf(t, f.completeSale(sale, tender("check", total)), "id_required")
	if r := f.completeSale(sale, idTender(total, "state_id")); r.status != http.StatusOK {
		t.Errorf("a small check with the ID checked = %d: %s", r.status, r.raw)
	}
}

// RULE: with no rule set (the default) no check asks for the ID.
func TestCP04_IDRuleNeverByDefault(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	setIDRule(t, f, "")

	sale, total := f.cartOf("2", nil)
	if r := f.completeSale(sale, tender("check", total)); r.status != http.StatusOK {
		t.Errorf("a check with no ID rule anywhere = %d: %s", r.status, r.raw)
	}
}

// RULE: the ID rule is about checks; cash is never held for an ID.
func TestCP04_CashUnaffectedByIDRule(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	setIDRule(t, f, "0")

	sale, total := f.cartOf("2", nil)
	if r := f.completeSale(sale, tender("cash", total)); r.status != http.StatusOK {
		t.Errorf("cash under an every check ID rule = %d: %s", r.status, r.raw)
	}
}

// RULE: the ID rule reads the sale's checks in total. Every check tender of
// a sale whose checks together pass the figure must carry the ID, so
// splitting the money into checks under the figure cannot avoid it; a small
// checked check frees nothing.
func TestCP04_SplitCheckEachNeedsID(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	setIDRule(t, f, "1000")

	// the covered check carries no ID; a small checked check does not free it
	sale, _ := f.cartOf("2", nil) // 1198
	blockerOf(t, f.completeSale(sale, idTender(100, "state_id"), tender("check", 1098)), "id_required")
	// the same two checks, each with the ID, complete
	if r := f.completeSale(sale, idTender(100, "state_id"), idTender(1098, "drivers_license")); r.status != http.StatusOK {
		t.Errorf("two checks each with the ID = %d: %s", r.status, r.raw)
	}
	// two checks under the figure, together over it, are refused without the
	// ID: splitting a check cannot avoid the rule
	sale2, _ := f.cartOf("2", nil) // 1198
	blockerOf(t, f.completeSale(sale2, tender("check", 599), tender("check", 599)), "id_required")
	// the same split, each check with the ID, completes
	if r := f.completeSale(sale2, idTender(599, "state_id"), idTender(599, "drivers_license")); r.status != http.StatusOK {
		t.Errorf("two checks under the figure, each with the ID = %d: %s", r.status, r.raw)
	}
	// a single check under the figure asks for no ID
	sale3, total := f.cartOf("1", nil) // 599
	if r := f.completeSale(sale3, tender("check", total)); r.status != http.StatusOK {
		t.Errorf("one check under the figure = %d: %s", r.status, r.raw)
	}
}

// RULE: the settings round trip carries both rules; only an owner or an
// admin may write them, a stale revision is refused, and the write is
// audited.
func TestCP04_SettingsRoundTripAndGuard(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	t.Cleanup(func() {
		mustExec(t, f.db, `DELETE FROM system_settings WHERE key IN ('pos.check_limit_cents', 'pos.check_id_required_over_cents')`)
		mustExec(t, f.db, `DELETE FROM admin_revisions WHERE resource = 'pos.settings'`)
	})

	get := f.do("GET", "/api/v1/pos/settings", nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if get.status != http.StatusOK {
		t.Fatalf("the counter reads the settings = %d: %s", get.status, get.raw)
	}
	rulesOf := func(m map[string]any) map[string]any { return m["rules"].(map[string]any) }
	if rulesOf(get.body)["check_limit_cents"] != nil {
		t.Errorf("check_limit_cents = %v, want null before anything is set", rulesOf(get.body)["check_limit_cents"])
	}
	idRule := rulesOf(get.body)["check_id_required"].(map[string]any)
	if idRule["mode"] != "never" {
		t.Errorf("the default ID rule mode = %v, want never", idRule["mode"])
	}
	rev0 := rev(t, get)

	body := func(limit any, mode string, over any, revision int64) map[string]any {
		return map[string]any{"revision": revision,
			"rules": map[string]any{"check_limit_cents": limit,
				"check_id_required": map[string]any{"mode": mode, "over_cents": over}}}
	}

	// a cashier may read but not write
	if r := f.do("PUT", "/api/v1/pos/settings", body(10000, "over_amount", 5000, rev0),
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusForbidden {
		t.Errorf("a cashier writing the settings = %d, want 403: %s", r.status, r.raw)
	}
	// a revision the settings never held is refused stale
	r := f.do("PUT", "/api/v1/pos/settings", body(10000, "over_amount", 5000, rev0+99),
		"X-Test-Role", "admin", "X-Test-Sub", mustUUID(t))
	if code, blockers, _ := errorOf(t, r); r.status != http.StatusConflict || code != "stale_revision" || len(blockers) != 0 {
		t.Errorf("a write on a revision the settings never held = %d %s, want 409 stale_revision: %s", r.status, code, r.raw)
	}

	var written []int64
	var actors []string
	for _, role := range []string{"admin", "owner"} {
		sub := mustUUID(t)
		actors = append(actors, sub)
		get = f.do("GET", "/api/v1/pos/settings", nil, "X-Test-Role", role, "X-Test-Sub", sub)
		if get.status != http.StatusOK {
			t.Fatalf("GET as %s = %d: %s", role, get.status, get.raw)
		}
		cur := rev(t, get)
		wantLimit := int64(10000 + cur)
		wantOver := int64(5000 + cur)
		put := f.do("PUT", "/api/v1/pos/settings", body(wantLimit, "over_amount", wantOver, cur),
			"X-Test-Role", role, "X-Test-Sub", sub, "If-Match", fmt.Sprintf(`"%d"`, cur))
		if put.status != http.StatusOK {
			t.Fatalf("PUT as %s = %d: %s", role, put.status, put.raw)
		}
		if rev(t, put) != cur+1 {
			t.Errorf("revision after the write = %d, want %d", rev(t, put), cur+1)
		}
		written = append(written, rev(t, put))
		rules := rulesOf(put.body)
		if num(t, rules, "check_limit_cents") != wantLimit {
			t.Errorf("check_limit_cents = %v, want %d", rules["check_limit_cents"], wantLimit)
		}
		back := rules["check_id_required"].(map[string]any)
		if back["mode"] != "over_amount" || num(t, back, "over_cents") != wantOver {
			t.Errorf("the ID rule = %v, want over_amount over %d", back, wantOver)
		}
	}

	// the rows behind the answer
	if v := f.scalar(`SELECT value FROM system_settings WHERE key = 'pos.check_limit_cents'`); v == nil {
		t.Errorf("the check limit setting row was not written")
	}
	// the audit rows: one per write, each carrying the revision it made
	// (scoped to this test's actors; the table keeps earlier runs' rows)
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.settings.saved' AND user_id::text = ANY($1)`, actors); n != int64(len(written)) {
		t.Errorf("the settings writes wrote %d audit rows, want %d", n, len(written))
	}
	for i, r := range written {
		if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.settings.saved' AND user_id::text = $1 AND (changes->>'revision')::bigint = $2`, actors[i], r); n != 1 {
			t.Errorf("revision %d carries %d audit rows, want 1", r, n)
		}
	}
}

// RULE: three owners write the settings at once from one revision (pool size
// 4): exactly one wins, the others are refused the stale revision, and the
// answer never shows a lost write.
func TestCP04_SettingsConcurrentWrites(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newFixture(t, db)
	t.Cleanup(func() {
		mustExec(t, f.db, `DELETE FROM system_settings WHERE key IN ('pos.check_limit_cents', 'pos.check_id_required_over_cents')`)
		mustExec(t, f.db, `DELETE FROM admin_revisions WHERE resource = 'pos.settings'`)
	})
	get := f.do("GET", "/api/v1/pos/settings", nil, "X-Test-Role", "admin", "X-Test-Sub", mustUUID(t))
	cur := rev(t, get)

	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, stale := 0, 0
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := map[string]any{"revision": cur,
				"rules": map[string]any{"check_limit_cents": int64(1000 * (i + 1)),
					"check_id_required": map[string]any{"mode": "every_check"}}}
			r := f.do("PUT", "/api/v1/pos/settings", body,
				"X-Test-Role", "owner", "X-Test-Sub", mustUUID(t), "If-Match", fmt.Sprintf(`"%d"`, cur))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case r.status == http.StatusOK:
				ok++
			case r.status == http.StatusConflict && staleCode(r):
				stale++
			default:
				t.Errorf("a contender = %d: %s", r.status, r.raw)
			}
		}(i)
	}
	wg.Wait()
	if ok != 1 || stale != 2 {
		t.Errorf("winners %d, stale %d, want 1 and 2", ok, stale)
	}
}

// staleCode reports a contender's refusal as the stale revision one.
func staleCode(r resp) bool {
	e, _ := r.body["error"].(map[string]any)
	return e != nil && e["code"] == "stale_revision"
}

// RULE: a customer's own check limit beats the database wide one, whichever
// is tighter or looser, at the completion.
func TestCP04_CustomerCheckLimitBeatsDatabaseLimit(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.setCheckDefault("50000")

	// the customer's own higher limit lets a check the default refuses
	mustExec(t, f.db, `UPDATE customers SET check_limit = 900.00 WHERE id = $1`, f.customerID)
	sale, total := f.cartOf("100", &f.customerID)
	if r := f.completeSale(sale, tender("check", total)); r.status != http.StatusOK {
		t.Errorf("a check under the customer's own higher limit = %d: %s", r.status, r.raw)
	}
	// and its own lower limit refuses a check the default would take
	mustExec(t, f.db, `UPDATE customers SET check_limit = 5.00 WHERE id = $1`, f.customerID)
	small, smallTotal := f.cartOf("1", &f.customerID)
	blockerOf(t, f.completeSale(small, tender("check", smallTotal)), "check_over_limit")
}
