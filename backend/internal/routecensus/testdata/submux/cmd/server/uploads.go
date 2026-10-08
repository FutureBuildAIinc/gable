// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package main is the fixture's stand-in for cmd/server, where the allow
// listed uploads mount lives.
package main

import "net/http"

// mountUploads is the allow listed /uploads/ file server mount.
func mountUploads(mux *http.ServeMux, h http.Handler) {
	mux.Handle("/uploads/", http.StripPrefix("/uploads/", h))
}
