// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package apps is a fixture standing in for pkg/apps: the one directory
// whose gatedRouter forwarder calls the census may skip, because they pass
// a caller-supplied pattern through to the real router.
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
	g.next.HandleFunc(pattern, handler)
}
