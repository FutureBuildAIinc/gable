// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The switch: a person sets light or dark for themselves and keeps it. The
 * choice is stored per signed in subject (gable_theme:<subject>) with the last
 * used value under gable_theme for the front door before anyone signs in, and
 * applied as data-theme on the document element (absent = Industrial Dark).
 */
import { describe, it, expect, beforeEach } from 'vitest'
import {
  currentTheme,
  setTheme,
  toggleTheme,
  initTheme,
  useThemeSubject,
  onThemeChange,
  THEME_KEY,
  themeKeyFor,
} from './theme'

beforeEach(() => {
  localStorage.clear()
  delete document.documentElement.dataset.theme
  initTheme(null)
})

describe('theme switch', () => {
  it('starts as Industrial Dark with no data-theme', () => {
    expect(currentTheme()).toBe('dark')
    expect(document.documentElement.dataset.theme).toBeUndefined()
  })

  it('applies light as data-theme and stores it as the last used value', () => {
    setTheme('light')
    expect(currentTheme()).toBe('light')
    expect(document.documentElement.dataset.theme).toBe('light')
    expect(localStorage.getItem(THEME_KEY)).toBe('light')
  })

  it('removes data-theme when set back to dark', () => {
    setTheme('light')
    setTheme('dark')
    expect(document.documentElement.dataset.theme).toBeUndefined()
    expect(localStorage.getItem(THEME_KEY)).toBe('dark')
  })

  it('stores the choice under the signed in subject and survives a reload', () => {
    useThemeSubject('person-a')
    setTheme('light')
    expect(localStorage.getItem(themeKeyFor('person-a'))).toBe('light')

    // a reload: a fresh document applies the stored value at start
    delete document.documentElement.dataset.theme
    initTheme('person-a')
    expect(currentTheme()).toBe('light')
  })

  it('keeps the choice per person: another subject has its own', () => {
    useThemeSubject('person-a')
    setTheme('light')
    localStorage.setItem(themeKeyFor('person-b'), 'dark')
    useThemeSubject('person-b')
    expect(currentTheme()).toBe('dark')
    useThemeSubject('person-a')
    expect(currentTheme()).toBe('light')
  })

  it('shows a person with no choice of their own the last used value, without storing it as theirs', () => {
    setTheme('light') // the front door, nobody signed in
    useThemeSubject('person-c')
    expect(currentTheme()).toBe('light')
    expect(localStorage.getItem(themeKeyFor('person-c'))).toBeNull()
    // their own choice is written only when they make one
    setTheme('dark')
    expect(localStorage.getItem(themeKeyFor('person-c'))).toBe('dark')
  })

  it('follows a change made in another tab, for the key that applies here', () => {
    useThemeSubject('person-e')
    localStorage.setItem(themeKeyFor('person-e'), 'light')
    window.dispatchEvent(new StorageEvent('storage', { key: themeKeyFor('person-e'), newValue: 'light' }))
    expect(currentTheme()).toBe('light')
    // another person's key, or a value that is not a theme, changes nothing
    window.dispatchEvent(new StorageEvent('storage', { key: themeKeyFor('person-x'), newValue: 'dark' }))
    window.dispatchEvent(new StorageEvent('storage', { key: themeKeyFor('person-e'), newValue: 'purple' }))
    expect(currentTheme()).toBe('light')
    // signed out: the last used key applies
    useThemeSubject(null)
    window.dispatchEvent(new StorageEvent('storage', { key: THEME_KEY, newValue: 'dark' }))
    expect(currentTheme()).toBe('dark')
  })

  it('ignores a stored value that is not a theme', () => {
    localStorage.setItem(themeKeyFor('person-d'), 'purple')
    initTheme('person-d')
    expect(currentTheme()).toBe('dark')
  })

  it('toggles and tells listeners', () => {
    const seen: string[] = []
    const off = onThemeChange((t) => seen.push(t))
    expect(toggleTheme()).toBe('light')
    expect(toggleTheme()).toBe('dark')
    off()
    toggleTheme()
    expect(seen).toEqual(['light', 'dark'])
  })
})
