// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httputil

import (
	stdpath "path"
	"strings"
)

// capabilityPrefixes are the routes whose next path segment is a capability
// token: whoever holds it is the customer of one quote. A token must never
// reach a log line or a metrics label, so every place that records a request
// path records SafePath of it.
var capabilityPrefixes = []string{
	"/api/public/v1/customer-quotes/",
	"/api/public/v1/dealer-quotes/",
}

func isCapabilitySegment(s string) bool {
	if len(s) != 43 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// SafePath returns path with a capability token segment replaced by ":token".
// Any other path is returned unchanged.
func SafePath(path string) string {
	if path == "" {
		return ""
	}
	for _, p := range capabilityPrefixes {
		if path == p {
			return p
		}
	}
	cleaned := stdpath.Clean(path)
	for _, p := range capabilityPrefixes {
		if cleaned+"/" == p && strings.HasSuffix(path, "/") {
			return p
		}
		rest, ok := strings.CutPrefix(cleaned, p)
		if !ok || rest == "" {
			continue
		}
		_, tail, hasTail := strings.Cut(rest, "/")
		if !hasTail {
			return p + ":token"
		}
		return p + ":token/" + tail
	}
	if strings.HasPrefix(cleaned, "/api/public/") {
		segs := strings.Split(cleaned, "/")
		changed := false
		for i, seg := range segs {
			if isCapabilitySegment(seg) {
				segs[i] = ":token"
				changed = true
			}
		}
		if changed {
			return strings.Join(segs, "/")
		}
	}
	return path
}
