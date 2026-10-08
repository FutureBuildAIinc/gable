// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package zz reproduces the review probe for the double registration
// finding: an exact double registration must fail the census, while the
// same route registered through two mutually exclusive branches still
// collapses to one.
package zz

import "net/http"

// Register registers the same route twice unconditionally.
func Register(mux *http.ServeMux, h http.HandlerFunc) {
	mux.HandleFunc("GET /zz/dup", h)
	mux.HandleFunc("GET /zz/dup", h)
}

// RegisterConditional registers the same route through two exclusive
// branches, which is one logical route.
func RegisterConditional(mux *http.ServeMux, strict bool, h http.HandlerFunc) {
	if strict {
		mux.HandleFunc("GET /zz/cond", h)
	} else {
		mux.HandleFunc("GET /zz/cond", h)
	}
}
