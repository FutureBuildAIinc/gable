// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package skipdir lives under a directory the go tool ignores.
package skipdir

import "net/http"

// Register registers a route the census must not see.
func Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /zz/skipdir", func(w http.ResponseWriter, r *http.Request) {})
}
