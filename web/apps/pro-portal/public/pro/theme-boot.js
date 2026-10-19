// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

try {
  const saved = localStorage.getItem('gable.pro.theme') || localStorage.getItem('gable_theme')
  const fallback = document.currentScript && document.currentScript.getAttribute('data-default-theme')
  const root = document.documentElement
  const explicitDark = saved === 'dark' || (saved !== 'light' && (root.classList.contains('dark') || root.dataset.theme === 'dark'))
  const theme = saved === 'light' || saved === 'dark' ? saved : explicitDark ? 'dark' : fallback === 'light' ? 'light' : 'dark'
  if (theme === 'light') {
    root.dataset.theme = 'light'
  } else {
    delete root.dataset.theme
  }
} catch {
  // The default theme applies when storage is unavailable.
}
