// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// CP-08 keeps an open drawer's float and expected figures blind to a
// cashier. A machine key carries no claims, and no claims used to read as
// "nothing to hide from", so a key with pos read scope saw them. A key is
// refused the figures as a cashier is; a request with no key and no claims
// (the dev mode harness) still reads them.
func TestAMachineKeyReadsTheOpenDrawerBlind(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(10000)
	path := "/api/v1/pos/till/current?register_id=" + f.register

	read := func(url string, headers ...string) map[string]any {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", url, res.StatusCode)
		}
		var out map[string]any
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	keyed := keyedServer(f, uuid.NewString())
	got := read(keyed.URL + path)
	if float, _ := got["opening_float_cents"].(float64); float != 0 {
		t.Errorf("a machine key read the opening float %v of an open drawer, want it blind (0)", got["opening_float_cents"])
	}
	if exp, _ := got["expected_by_method"].(map[string]any); len(exp) != 0 {
		t.Errorf("a machine key read the expected figures %v of an open drawer, want them blind", exp)
	}

	// no key and no claims: the dev harness still reads the float
	dev := read(f.srv.URL + path)
	if float, _ := dev["opening_float_cents"].(float64); float != 10000 {
		t.Errorf("the keyless dev read of the opening float = %v, want 10000", dev["opening_float_cents"])
	}
}
