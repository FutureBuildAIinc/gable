// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package apps is a fixture standing in for pkg/apps: the one directory
// whose gatedRouter forwarder calls the census may skip when the pattern is
// the caller supplied one. A call inside a forwarder whose pattern is a
// literal resolves on its own and is a real registration the census must
// count.
package apps

import "net/http"

// Router is the mux interface the forwarder delegates to.
type Router interface {
	Handle(pattern string, handler http.Handler)
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

// gatedRouter forwards a caller-supplied pattern to the real router.
type gatedRouter struct {
	next Router
}

func (g gatedRouter) Handle(pattern string, handler http.Handler) {
	g.next.Handle(pattern, handler)
}

func (g gatedRouter) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	// A literal pattern inside the forwarder is not forwarded traffic: it
	// registers a route of its own.
	g.next.HandleFunc("GET /apps/hidden", func(w http.ResponseWriter, r *http.Request) {})
	g.next.HandleFunc(pattern, handler)
}
