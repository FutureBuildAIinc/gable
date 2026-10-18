// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

// PerLinkKeys counts the keys the per link limiter holds, for the tests of the
// external test package.
func (s *Service) PerLinkKeys() int {
	s.perLink.mu.Lock()
	defer s.perLink.mu.Unlock()
	return len(s.perLink.hits)
}
