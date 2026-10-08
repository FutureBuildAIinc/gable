// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//go:build ignore

// Package main is excluded from every build by its ignore constraint.
package main

import "net/http"

// Register registers a route the census must not see.
func Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /zz/ignored", func(w http.ResponseWriter, r *http.Request) {})
}
