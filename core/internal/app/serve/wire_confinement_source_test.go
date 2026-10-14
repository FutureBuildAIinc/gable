// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"os"
	"strings"
	"testing"
)

// TestServeWiringMountsTheConfinements is the source level half of the round
// 3 P2-2 fix: the behaviour tests drive the fixture's own mounts, so a
// confinement dropped from Run() itself would leave them green while
// production lost its wall. The deny by default gate already refuses an
// unclassified route whatever the mount does, but a CONFINED route that lost
// its wall would pass the gate and reach the handler unconfined, so Run()'s
// confinement call sites are pinned here: this test fails when serve.go or
// chain.go stops calling one of them.
func TestServeWiringMountsTheConfinements(t *testing.T) {
	serveSrc, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatalf("read serve.go: %v", err)
	}
	chainSrc, err := os.ReadFile("chain.go")
	if err != nil {
		t.Fatalf("read chain.go: %v", err)
	}
	exposureSrc, err := os.ReadFile("wire_exposure.go")
	if err != nil {
		t.Fatalf("read wire_exposure.go: %v", err)
	}
	for _, pin := range []struct{ file, call, what string }{
		{string(serveSrc), "wall.locations(mux, locationHandler)", "the location routes' key branch wall (WithKeyBranchWall)"},
		{string(serveSrc), "wireExposure(exposureDeps{", "the exposure wiring"},
		{string(exposureSrc), "registerExposureRoutes(deps.Mux", "the exposure by id quote confinement"},
		{string(serveSrc), "taxCustomerWall(wall.keyWall", "the tax exemption and preview confinement"},
		{string(serveSrc), "pricingRulesCustomerWall(wall.keyWall", "the pricing rule create and calculate confinement"},
		{string(serveSrc), "categoryRulesCustomerWall(wall.keyWall", "the category rule confinement"},
		{string(chainSrc), "middleware.KeyBranchRouteGate(d.Mux", "the route allowlist gate in the chain"},
		{string(serveSrc), "\t\tmiddleware.RequireRole(\"admin\", \"owner\"),\n\t).WithAuditLog(auditLog)", "the location handler's audit log (the PD-5 grant, revoke and home branch rows)"},
		{string(serveSrc), "parsing.NewHandler(parsingSvc).WithAuditLog(auditLog)", "the parsing handler's audit log (the PD-6 upload row)"},
		{string(serveSrc), "vision.NewHandler(visionSvc).WithAuditLog(auditLog)", "the vision handler's audit log (the PD-6 scan row)"},
	} {
		if !strings.Contains(pin.file, pin.call) {
			t.Errorf("serve's wiring no longer calls %s (%s); the confinement it mounts is gone from production", pin.call, pin.what)
		}
	}
}
