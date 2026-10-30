// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// RULE: an exchange whose cash difference crosses the branch's large cash
// threshold is answered the way the completion is: the preview says what the
// rule needs of this exchange, and the payer's details ride the exchange's
// own body, so the crossing is recorded and the exchange is rung. Without the
// payer the exchange is refused and nothing is written.
func TestCP17_ExchangeCarriesThePayerAcrossTheThreshold(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	lcSetCountry(t, f, "US")
	lcStock(t, f)
	f.openTill(0)
	original, originalTotal := f.cartOf("1", &f.customerID)
	if r := f.completeSale(original, tender("cash", originalTotal)); r.status != http.StatusOK {
		t.Fatalf("the original sale = %d: %s", r.status, r.raw)
	}
	line := str(t, f.getSale(t, original)["lines"].([]any)[0].(map[string]any), "id")

	// the preview states the figures of the exchange and what the branch's
	// large cash rule needs of it
	preview := f.exchange("/preview", map[string]any{
		"original_sale_id": original, "reason": "wrong size",
		"returned_lines": []map[string]any{lineOf(line, "1", "wrong_item")},
		"sale_lines":     f.replaceWith("2000"),
		"tender_method":  "cash",
	})
	if preview.status != http.StatusOK {
		t.Fatalf("the exchange preview = %d: %s", preview.status, preview.raw)
	}
	difference := num(t, preview.body, "difference_cents")
	if difference <= 1_000_000 {
		t.Fatalf("the preview's difference = %d, want over the United States threshold", difference)
	}
	ask, ok := preview.body["large_cash"].(map[string]any)
	if !ok {
		t.Fatalf("the preview answered no large cash question = %v, want it to say what the rule needs", preview.body)
	}
	if ask["asks_details"] != true {
		t.Fatalf("the preview's large cash answer = %v, want it to ask for the payer's details", ask)
	}
	if fields, _ := ask["fields"].([]any); len(fields) == 0 {
		t.Fatalf("the preview named no field of the payer: %v", ask)
	}
	if got := num(t, ask, "aggregate_cents"); got != difference+originalTotal {
		t.Errorf("the preview's aggregate = %d, want the difference and the earlier cash %d", got, difference+originalTotal)
	}

	exchange := func(extra map[string]any) resp {
		body := map[string]any{
			"original_sale_id": original, "reason": "wrong size",
			"returned_lines": []map[string]any{lineOf(line, "1", "wrong_item")},
			"sale_lines":     f.replaceWith("2000"),
			"tenders":        []map[string]any{tender("cash", difference)},
		}
		for k, v := range extra {
			body[k] = v
		}
		return f.exchange("", body)
	}

	// without the payer the exchange is refused, and nothing is written
	refused := exchange(nil)
	if refused.status != http.StatusConflict || !strings.Contains(string(refused.raw), "payer_details_required") {
		t.Fatalf("the exchange without the payer = %d: %s, want 409 payer_details_required", refused.status, refused.raw)
	}
	if n := f.scalar(`SELECT count(*) FROM large_cash_events`); n.(int64) != 0 {
		t.Errorf("the refused exchange recorded %v crossings, want none", n)
	}
	if n := f.scalar(`SELECT count(*) FROM pos_returns`); n.(int64) != 0 {
		t.Errorf("the refused exchange wrote %v returns, want none", n)
	}

	// with the payer on its own body the exchange is rung and the crossing is
	// recorded for the cash it took
	ring := exchange(map[string]any{"payer": lcUSPayer()})
	if ring.status != http.StatusCreated {
		t.Fatalf("the exchange with the payer = %d: %s", ring.status, ring.raw)
	}
	var total int64
	var state, rule string
	if err := f.db.Pool.QueryRow(ctxBG(), `SELECT total_cents, state, rule_family FROM large_cash_events`).Scan(&total, &state, &rule); err != nil {
		t.Fatalf("no crossing was recorded: %v", err)
	}
	if rule != "US_8300" || state != "reportable" {
		t.Errorf("the crossing's rule %s state %s, want US_8300 reportable", rule, state)
	}
	if total != difference+originalTotal {
		t.Errorf("the crossing's total = %d, want the difference and the earlier cash %d", total, difference+originalTotal)
	}
}

// RULE: an exchange under the threshold asks for nothing: the preview says
// no and the exchange is rung without a payer.
func TestCP17_ExchangeUnderTheThresholdAsksNothing(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	lcSetCountry(t, f, "US")
	lcStock(t, f)
	f.openTill(0)
	original, originalTotal := f.cartOf("1", &f.customerID)
	if r := f.completeSale(original, tender("cash", originalTotal)); r.status != http.StatusOK {
		t.Fatalf("the original sale = %d: %s", r.status, r.raw)
	}
	line := str(t, f.getSale(t, original)["lines"].([]any)[0].(map[string]any), "id")
	preview := f.exchange("/preview", map[string]any{
		"original_sale_id": original, "reason": "wrong size",
		"returned_lines": []map[string]any{lineOf(line, "1", "wrong_item")},
		"sale_lines":     f.replaceWith("2"),
		"tender_method":  "cash",
	})
	if preview.status != http.StatusOK {
		t.Fatalf("the exchange preview = %d: %s", preview.status, preview.raw)
	}
	difference := num(t, preview.body, "difference_cents")
	ask, ok := preview.body["large_cash"].(map[string]any)
	if !ok {
		t.Fatalf("the preview answered no large cash question = %v", preview.body)
	}
	if ask["asks_details"] != false {
		t.Errorf("the preview's large cash answer = %v, want it to ask for nothing", ask)
	}
	r := f.exchange("", map[string]any{
		"original_sale_id": original, "reason": "wrong size",
		"returned_lines": []map[string]any{lineOf(line, "1", "wrong_item")},
		"sale_lines":     f.replaceWith("2"),
		"tenders":        []map[string]any{tender("cash", difference)},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("the exchange under the threshold = %d: %s", r.status, r.raw)
	}
	if n := f.scalar(`SELECT count(*) FROM large_cash_events`); n.(int64) != 0 {
		t.Errorf("the exchange under the threshold recorded %v crossings, want none", n)
	}
}
