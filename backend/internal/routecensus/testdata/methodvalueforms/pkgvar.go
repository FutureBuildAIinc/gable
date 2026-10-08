// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package forms

import "net/http"

// reg binds the method value at package level.
var reg = m.HandleFunc

// PkgVar registers through the package level binding.
func PkgVar(h http.HandlerFunc) {
	reg("GET /pkgvar/x", h)
}
