// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package forms

import "net/http"

// get returns the method value to its caller.
func get(mm muxT) func(string, func(http.ResponseWriter, *http.Request)) {
	return mm.HandleFunc
}

// Returned registers through the returned method value.
func Returned(h http.HandlerFunc) {
	get(m)("GET /returned/x", h)
}
