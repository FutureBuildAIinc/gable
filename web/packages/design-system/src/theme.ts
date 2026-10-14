// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The theme switch. Industrial Dark is the default (no data-theme on the
 * document element); Gable light is the alternative a person sets for
 * themselves and keeps. The choice is stored per signed in subject under
 * gable_theme:<subject>, and the last used value under gable_theme so the
 * front door can paint before anyone signs in.
 *
 * Call initTheme(subject) at the app's entry, before the first render, and
 * useThemeSubject(subject) again on sign in.
 */

export type Theme = 'dark' | 'light'

/** The last used value, read by the front door before anyone signs in. */
export const THEME_KEY = 'gable_theme'

/** The key a signed in subject's choice is stored under. */
export function themeKeyFor(subject: string): string {
  return `${THEME_KEY}:${subject}`
}

/** Fired on window whenever the applied theme changes. */
export const THEME_EVENT = 'gable-theme-changed'

let activeSubject: string | null = null

function parse(raw: string | null): Theme | null {
  return raw === 'light' || raw === 'dark' ? raw : null
}

function read(key: string): Theme | null {
  try {
    return parse(globalThis.localStorage?.getItem(key) ?? null)
  } catch {
    return null
  }
}

function write(key: string, theme: Theme): void {
  try {
    globalThis.localStorage?.setItem(key, theme)
  } catch {
    // A hostile storage context: the choice lasts for this document only.
  }
}

/** The applied theme: light only when data-theme says so. */
export function currentTheme(): Theme {
  return document.documentElement.dataset.theme === 'light' ? 'light' : 'dark'
}

/** Apply a theme to the document and tell listeners when it changed. */
function apply(theme: Theme): void {
  const before = currentTheme()
  if (theme === 'light') document.documentElement.dataset.theme = 'light'
  else delete document.documentElement.dataset.theme
  if (before !== theme) {
    window.dispatchEvent(new CustomEvent<Theme>(THEME_EVENT, { detail: theme }))
  }
}

/**
 * Pick the subject whose choice applies (null = nobody signed in) and apply
 * it. A subject with no stored choice keeps the last used value, which is
 * then stored for them.
 */
export function useThemeSubject(subject: string | null): Theme {
  activeSubject = subject !== null && subject !== '' ? subject : null
  const lastUsed = read(THEME_KEY)
  let theme: Theme
  if (activeSubject === null) {
    theme = lastUsed ?? 'dark'
  } else {
    const own = read(themeKeyFor(activeSubject))
    theme = own ?? lastUsed ?? 'dark'
    if (own === null) write(themeKeyFor(activeSubject), theme)
  }
  apply(theme)
  write(THEME_KEY, theme)
  return theme
}

/** At start, before the first render the app controls. */
export function initTheme(subject: string | null): Theme {
  return useThemeSubject(subject)
}

/** A person's choice: applied, stored for them and as the last used value. */
export function setTheme(theme: Theme): void {
  apply(theme)
  write(THEME_KEY, theme)
  if (activeSubject !== null) write(themeKeyFor(activeSubject), theme)
}

export function toggleTheme(): Theme {
  const next: Theme = currentTheme() === 'light' ? 'dark' : 'light'
  setTheme(next)
  return next
}

/** Subscribe to theme changes; returns the unsubscribe. */
export function onThemeChange(listener: (theme: Theme) => void): () => void {
  const handler = (e: Event) => listener((e as CustomEvent<Theme>).detail)
  window.addEventListener(THEME_EVENT, handler)
  return () => window.removeEventListener(THEME_EVENT, handler)
}

/** The value of a colour variable as a CSS colour, for canvas and chart code. */
export function themeColor(name: string, alpha = 1): string {
  const raw = getComputedStyle(document.documentElement).getPropertyValue(`--gb-${name}`).trim()
  if (raw === '') return `rgb(0 0 0 / ${alpha})`
  return `rgb(${raw} / ${alpha})`
}
