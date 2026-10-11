// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// Fix round 8 of the branch bound key pin: the r17 P3s, each through the real
// serve chain.

import (
	"net/http"
	"strings"
	"testing"
)

// Fix round 8 (r17 P3-1): the till report read mapped every service error to
// 500, so a session the caller's branch context hides answered 500 where the
// other by id reads (the Z report, the return) answer 404. The hidden session
// now reads as missing, the caller's own still reads.
func TestKeyBranchPin_HiddenTillReportAnswers404(t *testing.T) {
	f, _, _ := round5Fixture(t)
	hdr := map[string]string{"X-Branch-Id": f.branchA.String()}
	status, buf := f.userAs(t, "admin", "GET", "/api/v1/pos/till/"+f.tillB.String()+"/report", "", hdr)
	if status != http.StatusNotFound {
		t.Errorf("another branch's till report: %d %s, want the 404 the other by id reads answer", status, buf)
	}
	if strings.Contains(string(buf), f.tillB.String()) {
		t.Errorf("the hidden till report answer carries the session's id: %s", buf)
	}
	status, buf = f.userAs(t, "admin", "GET", "/api/v1/pos/till/"+f.tillA.String()+"/report", "", hdr)
	if status != http.StatusOK {
		t.Errorf("the caller's own till report: %d %s, want 200", status, buf)
	}
}
