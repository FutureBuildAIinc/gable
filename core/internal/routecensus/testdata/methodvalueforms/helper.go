// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package forms

import "net/http"

// helper registers through the method value it is handed.
func helper(f func(string, func(http.ResponseWriter, *http.Request)), h http.HandlerFunc) {
	f("GET /helper/x", h)
}

// PassToHelper hands the method value to a helper that registers through it.
func PassToHelper(h http.HandlerFunc) {
	helper(m.HandleFunc, h)
}
