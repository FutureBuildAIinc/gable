// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package main is the fixture root for the build exclusion finding: only
// files the go tool builds are walked.
package main

import "net/http"

// Register registers the one route the fixture keeps.
func Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /zz/kept", func(w http.ResponseWriter, r *http.Request) {})
}
