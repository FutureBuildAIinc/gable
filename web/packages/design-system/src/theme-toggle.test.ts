// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** The sun or moon control: names itself, toggles, and is a keyboard reachable button. */
import { describe, it, expect, beforeEach } from 'vitest'
import './theme-toggle'
import { currentTheme, initTheme, THEME_KEY } from './theme'

async function mount(): Promise<HTMLElement> {
  const el = document.createElement('gable-theme-toggle')
  document.body.append(el)
  await (el as unknown as { updateComplete: Promise<boolean> }).updateComplete
  return el
}

beforeEach(() => {
  document.body.innerHTML = ''
  localStorage.clear()
  delete document.documentElement.dataset.theme
  initTheme(null)
})

describe('gable-theme-toggle', () => {
  it('is a native button named for the mode it switches to', async () => {
    const el = await mount()
    const button = el.querySelector('button') as HTMLButtonElement
    expect(button.type).toBe('button')
    expect(button.getAttribute('aria-label')).toBe('Light mode')
  })

  it('toggles the theme, stores it and renames itself', async () => {
    const el = await mount()
    const button = el.querySelector('button') as HTMLButtonElement
    button.click()
    await (el as unknown as { updateComplete: Promise<boolean> }).updateComplete
    expect(currentTheme()).toBe('light')
    expect(localStorage.getItem(THEME_KEY)).toBe('light')
    expect(el.querySelector('button')!.getAttribute('aria-label')).toBe('Dark mode')
    el.querySelector('button')!.click()
    expect(currentTheme()).toBe('dark')
  })
})
