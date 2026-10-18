// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import '../index.css'
import { initTheme } from '@gable/design-system'
import './public-page.ts'

export function initCustomerTheme(): 'light' | 'dark' {
  let saved: string | null = null
  try {
    saved = localStorage.getItem('gable.pro.theme') ?? localStorage.getItem('gable_theme')
  } catch {
    saved = null
  }
  const root = document.documentElement
  const explicitDark = saved === 'dark' || (saved !== 'light' && (root.classList.contains('dark') || root.dataset.theme === 'dark'))
  const theme: 'light' | 'dark' = explicitDark ? 'dark' : 'light'
  if (theme === 'light') {
    root.dataset.theme = 'light'
  } else {
    delete root.dataset.theme
  }
  try {
    localStorage.setItem('gable_theme', theme)
  } catch {
    // Storage writes are optional when running in restricted contexts.
  }
  initTheme(null)
  return theme
}

initCustomerTheme()

const root = document.getElementById('root')
if (root) root.innerHTML = '<gable-customer-quote-page></gable-customer-quote-page>'
