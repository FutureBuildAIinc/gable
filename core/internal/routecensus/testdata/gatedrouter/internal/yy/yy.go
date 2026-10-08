// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package yy reproduces the review probe for the gatedRouter finding: a
// type named gatedRouter outside pkg/apps, whose registration the census
// must count instead of skipping.
package yy

import "net/http"

type gatedRouter struct {
	mux *http.ServeMux
}

// Register registers through the locally defined gatedRouter.
func (g gatedRouter) Register() {
	g.mux.HandleFunc("GET /yy/hidden", func(w http.ResponseWriter, r *http.Request) {})
}
