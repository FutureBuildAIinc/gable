// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package routecensus

import (
	"fmt"
	"strings"
)

// FileHeader is the SPDX header every committed census file carries. It is
// part of Render's output so the generated file is REUSE-complete by itself.
const FileHeader = `# SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
#
# Route census: every route the Go sources of this module register, one
# route per line, tab separated: method, pattern, module, handler. The
# module column is the registering package's directory relative to the Go
# module root. An asterisk in the method column means the pattern has no
# method prefix and answers every method. Conditional registrations (routes
# the server mounts only under some configuration) are listed like any
# other; where the sources register one route twice, identically, through
# mutually exclusive branches, the route appears once. See
# docs/refactor/ROUTE-CENSUS.md.
#
# Generated file. Do not edit by hand; regenerate from the Go module root:
#
#	go run ./cmd/census -write
#
# The census test (internal/routecensus) fails when this file and the
# sources disagree.
`

// ColumnHeader is the first body line of a rendered census.
const ColumnHeader = "method\tpattern\tmodule\thandler"

// Render renders the routes as the committed census file's content:
// header, column header, then one route per line in SortRoutes order.
func Render(routes []Route) string {
	var b strings.Builder
	b.WriteString(FileHeader)
	b.WriteString(ColumnHeader)
	b.WriteString("\n")
	for _, r := range routes {
		method := r.Method
		if method == "" {
			method = "*"
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", method, r.Pattern, r.Module, r.Handler)
	}
	return b.String()
}
