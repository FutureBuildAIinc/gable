// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package apicontract

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

// CensusRoute is one route of the committed census file.
type CensusRoute struct {
	Method  string // upper case method, or * for a pattern that answers every method
	Pattern string // the ServeMux pattern exactly as registered
	Module  string
	Handler string
}

// Key is the coverage identity of a route: method and pattern, the same
// pair the contract addresses.
func (r CensusRoute) Key() string {
	return r.Method + " " + r.Pattern
}

// ParseCensus reads core/api/ROUTES.txt: comment lines, blank lines and
// the column header are skipped, and every data line must carry four tab
// separated columns.
func ParseCensus(path string) ([]CensusRoute, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()

	var routes []CensusRoute
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if text == "" || strings.HasPrefix(text, "#") || strings.HasPrefix(text, "method\t") {
			continue
		}
		parts := strings.Split(text, "\t")
		if len(parts) != 4 {
			return nil, fmt.Errorf("%s:%d: expected 4 tab separated columns, found %d", path, line, len(parts))
		}
		routes = append(routes, CensusRoute{
			Method:  parts[0],
			Pattern: parts[1],
			Module:  parts[2],
			Handler: parts[3],
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("%s carries no routes", path)
	}
	return routes, nil
}

// ParsePending reads core/api/contract-pending.txt and returns the sorted
// method+pattern keys it lists. A pending entry names a route that exists
// in the census but has no contract operation yet.
func ParsePending(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()

	var keys []string
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		parts := strings.Split(text, "\t")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("%s:%d: expected method and pattern tab separated, found %q", path, line, text)
		}
		keys = append(keys, parts[0]+" "+parts[1])
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	sort.Strings(keys)
	return keys, nil
}
