// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package zz reproduces the review probe for the sub mux finding: a sub mux
// mounted through http.StripPrefix. The census must refuse it outside the
// allow list instead of listing the mounted route under the wrong path.
package zz

import "net/http"

// Register mounts a sub mux under a prefix.
func Register(mux *http.ServeMux, h http.Handler) {
	sub := http.NewServeMux()
	sub.HandleFunc("GET /zz/inner", func(w http.ResponseWriter, r *http.Request) {})
	mux.Handle("/zz/sub/", http.StripPrefix("/zz/sub", sub))
}
