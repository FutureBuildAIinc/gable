// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The boot script both pages load before the first paint: it applies the
 * stored light choice and leaves Industrial Dark alone otherwise. It is a
 * same origin file because the page's CSP (script-src 'self') refuses an
 * inline script; both boot screens must load it.
 */
import { describe, it, expect, beforeEach } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const script = readFileSync(resolve(process.cwd(), 'public/theme-boot.js'), 'utf8')

function boot(): void {
  new Function(script)()
}

beforeEach(() => {
  localStorage.clear()
  delete document.documentElement.dataset.theme
})

describe('theme boot script', () => {
  it('applies a stored light choice', () => {
    localStorage.setItem('gable_theme', 'light')
    boot()
    expect(document.documentElement.dataset.theme).toBe('light')
  })

  it('leaves Industrial Dark when nothing or dark is stored', () => {
    boot()
    expect(document.documentElement.dataset.theme).toBeUndefined()
    localStorage.setItem('gable_theme', 'dark')
    boot()
    expect(document.documentElement.dataset.theme).toBeUndefined()
  })

  it('is loaded by both boot screens', () => {
    for (const page of ['index.html', '../front-door/index.html']) {
      const html = readFileSync(resolve(process.cwd(), page), 'utf8')
      expect(html, page).toContain('<script src="/theme-boot.js"></script>')
    }
  })
})
