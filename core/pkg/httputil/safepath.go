// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httputil

import "strings"

// capabilityPrefixes are the routes whose next path segment is a capability
// token: whoever holds it is the customer of one quote. A token must never
// reach a log line or a metrics label, so every place that records a request
// path records SafePath of it.
var capabilityPrefixes = []string{
	"/api/public/v1/customer-quotes/",
	"/api/public/v1/dealer-quotes/",
}

// SafePath returns path with a capability token segment replaced by ":token".
// Any other path is returned unchanged.
func SafePath(path string) string {
	for _, p := range capabilityPrefixes {
		rest, ok := strings.CutPrefix(path, p)
		if !ok || rest == "" {
			continue
		}
		_, tail, hasTail := strings.Cut(rest, "/")
		if !hasTail {
			return p + ":token"
		}
		return p + ":token/" + tail
	}
	return path
}
