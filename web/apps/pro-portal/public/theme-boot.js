// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

try {
  if (localStorage.getItem('gable_theme') === 'light') {
    document.documentElement.dataset.theme = 'light'
  }
} catch {
  // The default theme applies when storage is unavailable.
}
