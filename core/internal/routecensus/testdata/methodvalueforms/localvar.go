// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package forms

import "net/http"

// LocalVar binds the method value to a local variable with a var
// declaration, then registers through the variable.
func LocalVar(h http.HandlerFunc) {
	var f = m.HandleFunc
	f("GET /localvar/x", h)
}
