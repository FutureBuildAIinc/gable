// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Colours for code that cannot use a Tailwind class: a value that is built
 * into a string with an alpha suffix, or handed to a canvas. Every colour
 * reads the --gb-* channel variables, so it follows Industrial Dark and
 * Gable light.
 */

/** A channel triple such as "4 108 67" as #rrggbb. */
export function channelsToHex(raw: string): string | null {
  const parts = raw.trim().split(/\s+/).map(Number)
  if (parts.length !== 3 || parts.some((n) => !Number.isFinite(n))) return null
  return '#' + parts.map((n) => Math.max(0, Math.min(255, Math.round(n))).toString(16).padStart(2, '0')).join('')
}

/** Industrial Dark values for a colour read before the stylesheet is loaded. */
const DARK_FALLBACK: Record<string, string> = { 'gable-green': '#00FFA3' }

/** A theme colour as #rrggbb (for a suffix such as `${hex}15`), read at call time. */
export function themeHex(name: string, fallback = DARK_FALLBACK[name] ?? 'transparent'): string {
  const raw = getComputedStyle(document.documentElement).getPropertyValue(`--gb-${name}`)
  return channelsToHex(raw) ?? fallback
}

/** A CSS colour that follows the mode with no read: for a style attribute. */
export function cssVarColor(name: string, alpha?: number): string {
  return alpha === undefined ? `rgb(var(--gb-${name}))` : `rgb(var(--gb-${name}) / ${alpha})`
}
