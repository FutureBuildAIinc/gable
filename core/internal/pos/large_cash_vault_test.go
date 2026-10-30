// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/largecash"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// withLargeCashKey wires the counter's large cash rules with the given vault
// key in place of the fixture's test key: empty is a stack started without
// PAYMENT_VAULT_KEY, a malformed value one started with a bad key.
func withLargeCashKey(key string) func(*fixture) {
	return func(f *fixture) {
		v, _ := payment.NewVault(key)
		f.largeCash = largecash.NewService(f.db, v, key)
		f.service.WithLargeCash(f.largeCash)
	}
}

// TestCP17_NoVaultKeyRefusesTheCapture: on a stack with no vault key, or a
// malformed one, a United States crossing with the full payer capture is
// refused through the route with 503 large_cash_vault_unavailable, at the
// preview and at the completion, rather than store the taxpayer
// identification number and the ID number in clear. The refused completion
// leaves the sale open and writes no large cash row.
func TestCP17_NoVaultKeyRefusesTheCapture(t *testing.T) {
	for name, key := range map[string]string{"no key": "", "a malformed key": "not-a-key"} {
		t.Run(name, func(t *testing.T) {
			testutil.LockOutboxTables(t)
			f := newFixture(t, testutil.RequireDB(t), withLargeCashKey(key))
			lcSetCountry(t, f, "US")
			lcStock(t, f)
			sale := lcBigSale(f, t, &f.customerID, "2000")
			total := lcTotalCents(t, f, sale)
			refused := func(act string, r resp) {
				t.Helper()
				if r.status != http.StatusServiceUnavailable {
					t.Fatalf("%s without a vault key = %d: %s, want 503", act, r.status, r.raw)
				}
				if _, blockers, _ := errorOf(t, r); len(blockers) == 0 || blockers[0] != "large_cash_vault_unavailable" {
					t.Fatalf("%s's blockers = %v, want large_cash_vault_unavailable", act, blockers)
				}
			}
			refused("the preview", lcFloorPreview(t, f, sale, total, map[string]any{"payer": lcUSPayer()}))
			refused("the completion", lcComplete(f, t, sale, map[string]any{"payer": lcUSPayer()}, tender("cash", total)))
			if got := f.getSale(t, sale)["status"]; got != "open" {
				t.Errorf("the refused completion left the sale %v, want open", got)
			}
			for _, c := range lcOwnRows(t, f, sale) {
				if c.rows != 0 {
					t.Errorf("the refusal wrote %d rows to %s", c.rows, c.table)
				}
			}
		})
	}
}
