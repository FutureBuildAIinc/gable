// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/salesdoc"
)

// RULE (ADR 0011 sections 5.1 and 7, CP-18): the tender half of the shared
// offline arithmetic fixture (salesdoc/testdata/offline-arithmetic.json, the
// file the Rust core reads too) settles through the live cash rounding, so
// an offline cash sale's change agrees with the online counter's to the cent
// on a branch that rounds cash.
func TestOfflineTenderFixtureSettlesUnderTheLiveCashRounding(t *testing.T) {
	raw, err := os.ReadFile("../salesdoc/testdata/offline-arithmetic.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fix struct {
		Tenders []struct {
			Name                  string                   `json:"name"`
			CashRoundingMode      string                   `json:"cash_rounding_mode"`
			TotalCents            int64                    `json:"total_cents"`
			Tenders               []salesdoc.OfflineTender `json:"tenders"`
			Refused               bool                     `json:"refused"`
			ExpectedTenderedCents int64                    `json:"expected_tendered_cents"`
			ExpectedCashCents     int64                    `json:"expected_cash_cents"`
			ExpectedAccountCents  int64                    `json:"expected_account_cents"`
			ExpectedChangeCents   int64                    `json:"expected_change_cents"`
			ExpectedRoundingCents int64                    `json:"expected_rounding_cents"`
			ExpectedCode          *string                  `json:"expected_code"`
		} `json:"tenders"`
	}
	if err := json.Unmarshal(raw, &fix); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(fix.Tenders) == 0 {
		t.Fatal("the fixture holds no tender case")
	}
	for _, tc := range fix.Tenders {
		t.Run(tc.Name, func(t *testing.T) {
			mode := pos.ParseRoundingMode(tc.CashRoundingMode)
			round := func(cents int64) (int64, int64) { return pos.RoundCashFor(mode, cents) }
			res, err := salesdoc.SettleOfflineTenders(httpx.Cents(tc.TotalCents), tc.Tenders, round)
			if tc.Refused {
				if err == nil {
					t.Fatalf("SettleOfflineTenders succeeded (%+v), want refusal", res)
				}
				if tc.ExpectedCode != nil && res.RefusalCode != *tc.ExpectedCode {
					t.Errorf("refusal code = %q, want %q", res.RefusalCode, *tc.ExpectedCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("SettleOfflineTenders: %v", err)
			}
			got := [5]int64{int64(res.TenderedCents), int64(res.CashCents), int64(res.AccountCents), int64(res.ChangeCents), int64(res.RoundingCents)}
			want := [5]int64{tc.ExpectedTenderedCents, tc.ExpectedCashCents, tc.ExpectedAccountCents, tc.ExpectedChangeCents, tc.ExpectedRoundingCents}
			if got != want {
				t.Errorf("tendered, cash, account, change, rounding = %v, want %v", got, want)
			}
		})
	}
}
