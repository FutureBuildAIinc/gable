// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package appwrite

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrUnsupportedVersion wraps every CheckFloor refusal.
var ErrUnsupportedVersion = errors.New("appwrite: unsupported server version")

// CheckFloor refuses an Appwrite server below 2.0.0. It parses major.minor
// from the version string, stripping any pre-release or build suffix, and
// refuses 1.x and anything unparseable.
func CheckFloor(version string) error {
	v := version
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return fmt.Errorf("%w: %q is not a major.minor version", ErrUnsupportedVersion, version)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return fmt.Errorf("%w: %q has no numeric major", ErrUnsupportedVersion, version)
	}
	if _, err := strconv.Atoi(parts[1]); err != nil {
		return fmt.Errorf("%w: %q has no numeric minor", ErrUnsupportedVersion, version)
	}
	if major < 2 {
		return fmt.Errorf("%w: %q is below the 2.0.0 floor", ErrUnsupportedVersion, version)
	}
	return nil
}
