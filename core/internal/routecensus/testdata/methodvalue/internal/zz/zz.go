// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package zz reproduces the review probe for the method value finding: a
// HandleFunc method value bound to a variable. The census must report the
// binding as unresolved instead of silently missing the route.
package zz

import "net/http"

// Register registers through a method value.
func Register(mux *http.ServeMux, h http.HandlerFunc) {
	f := mux.HandleFunc
	f("GET /zz/method-value", h)
}
