// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The coverage check sorts last by file name, so it runs after the tests whose
// legs it counts (Go runs a package's tests in file order).

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/gablelbm/gable/pkg/middleware"
)

// TestKeyBranchPin_ZWalledRoutesAllHaveALeg fails for every WALLED entry of the
// allowlist that no driving test sent a bound key call to.
func TestKeyBranchPin_ZWalledRoutesAllHaveALeg(t *testing.T) {
	walledLegs.Lock()
	defer walledLegs.Unlock()
	for _, d := range walledLegDrivers {
		if !walledLegs.drivers[d] {
			t.Skipf("driver %q did not run to its end in this invocation (a -run filter or an earlier failure)", d)
		}
	}
	noop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	mux := http.NewServeMux()
	var walled []string
	for _, route := range middleware.KeyBranchAllowlistRoutes() {
		method, pattern, _ := strings.Cut(route, " ")
		if class, ok := middleware.KeyBranchRouteClassFor(method, pattern); ok && class == middleware.KeyBranchClassWalled {
			mux.Handle(route, noop)
			walled = append(walled, route)
		}
	}
	covered := map[string]bool{}
	for call := range walledLegs.seen {
		method, path, _ := strings.Cut(call, " ")
		_, pattern := mux.Handler(httptest.NewRequest(method, path, nil))
		if pattern != "" {
			covered[pattern] = true
		}
	}
	var missing []string
	for _, route := range walled {
		if !covered[route] {
			missing = append(missing, route)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d of %d WALLED routes have no bound key twin leg:\n%s", len(missing), len(walled), strings.Join(missing, "\n"))
	}
}
