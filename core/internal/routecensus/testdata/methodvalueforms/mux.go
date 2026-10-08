// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package forms reproduces the review's N2 probes: the five ways a
// HandleFunc method value can travel from its receiver to the line that
// registers through it. A registration through the value is invisible to
// the census walk, so the binding itself must be reported as unresolved in
// every form: a local var, a package level var, a returned method value, a
// struct literal field, and a method value handed to a helper.
package forms

import "net/http"

// muxT is the receiver the forms bind the method value from.
type muxT struct{}

func (muxT) Handle(pattern string, h http.Handler) {}

func (muxT) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {}

var m muxT
