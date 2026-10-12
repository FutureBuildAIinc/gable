// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package techadmin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/pkg/middleware"
)

// A mint that names customers:restricted and carries no claims at all is
// refused, fail closed: with no caller there is no owner to grant it. A mint
// naming other scopes needs no claims here (the route's role guard speaks).
func TestOwnerGrantsRestrictedFailsClosedWithoutClaims(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/keys", nil)
	if err := ownerGrantsRestricted(r, []string{"customers:read", middleware.CustomerRestrictedScope}); err == nil {
		t.Error("a claims-less mint of customers:restricted was allowed, want the refusal")
	}
	if err := ownerGrantsRestricted(r, []string{"customers:read"}); err != nil {
		t.Errorf("a claims-less mint of another scope: %v, want no restriction from this check", err)
	}
}
