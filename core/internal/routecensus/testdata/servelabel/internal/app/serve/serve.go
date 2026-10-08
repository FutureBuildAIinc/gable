// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package serve is the fixture's stand-in for the real internal/app/serve:
// the moved body of cmd/server, which the census reports under its
// historical cmd/server label.
package serve

import "net/http"

// NewRouter assembles the router, as the real package does.
func NewRouter() *http.ServeMux {
	return http.NewServeMux()
}

// RegisterRoutes carries the shape of the real package's own registrations:
// a plain route and the allow listed /uploads/ mount.
func RegisterRoutes(mux *http.ServeMux, h http.Handler) {
	mux.HandleFunc("GET /xx/serve", func(w http.ResponseWriter, r *http.Request) {})
	mux.Handle("/uploads/", http.StripPrefix("/uploads/", h))
}
