// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package forms

import "net/http"

// S carries the method value in a field.
type S struct {
	F func(string, func(http.ResponseWriter, *http.Request))
}

// StructField registers through a method value stored in a field.
func StructField(h http.HandlerFunc) {
	s := S{F: m.HandleFunc}
	s.F("GET /structfield/x", h)
}
