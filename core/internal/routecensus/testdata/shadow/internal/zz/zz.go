// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package zz reproduces the review probe for the shadowing finding: a
// parameter and a short variable named like a package constant. The census
// must not resolve the shadowing name to the constant.
package zz

import "net/http"

// routeZZ is the package constant the local names shadow.
const routeZZ = "GET /zz/shadowed"

// Register takes a parameter named like the constant.
func Register(mux *http.ServeMux, routeZZ string) {
	mux.HandleFunc(routeZZ, func(w http.ResponseWriter, r *http.Request) {})
}

// Register2 binds a short variable named like the constant.
func Register2(mux *http.ServeMux) {
	routeZZ := "GET /zz/local"
	mux.HandleFunc(routeZZ, func(w http.ResponseWriter, r *http.Request) {})
}
