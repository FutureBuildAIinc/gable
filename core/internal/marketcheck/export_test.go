// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package marketcheck

// RefusedDial and AllowPrivateFor expose the dial guard's decisions to the tests.
func RefusedDial(addr string, allowPrivate bool) error { return checkDialAddress(addr, allowPrivate) }
func AllowPrivateFor(host string) bool                 { return allowPrivateFor(host) }
