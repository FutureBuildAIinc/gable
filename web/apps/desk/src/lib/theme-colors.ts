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

/**
 * A tenant's brand colour, made legible on the card. A brand colour that
 * suits dark (a bright green) can be about 1.3:1 on Gable light's card; there
 * it gives way to the mode's own Gable green. Dark keeps the brand colour as
 * it is, and a brand colour that already reads at 3:1 (a control or large
 * text) is kept in light too.
 */
export function brandOnCard(brand: string): string {
  return brandAtLeast(brand, 3)
}

/**
 * A tenant's brand colour used as text (a nav label at about 14 pixels, medium
 * weight): it needs 4.5:1 on the card, not the 3:1 a bar or a fill needs.
 */
export function brandLabelOnCard(brand: string): string {
  return brandAtLeast(brand, 4.5)
}

function rgbOf(hex: string): [number, number, number] {
  const h = hex.replace('#', '')
  const full = h.length === 3 ? h.split('').map((c) => c + c).join('') : h.slice(0, 6)
  return [0, 2, 4].map((k) => parseInt(full.slice(k, k + 2), 16)) as [number, number, number]
}

function lumOf(rgb: [number, number, number]): number {
  const lin = rgb.map((v) => {
    const c = v / 255
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * lin[0] + 0.7152 * lin[1] + 0.0722 * lin[2]
}

function contrast(a: [number, number, number], b: [number, number, number]): number {
  const [hi, lo] = [lumOf(a), lumOf(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

const HEX = /^#[0-9A-Fa-f]{3}$|^#[0-9A-Fa-f]{6}$/

/**
 * The brand, or the mode's Gable green when the brand does not reach
 * `needed` against the ground it is drawn on: the card, or the card with the
 * brand itself tinted over it at `tint` alpha (an active row).
 */
function brandAtLeast(brand: string, needed: number, tint = 0, groundToken = 'slate-steel'): string {
  if (document.documentElement.dataset.theme !== 'light') return brand
  if (!HEX.test(brand)) return brand
  let card = themeHex(groundToken, '')
  if (!card.startsWith('#')) card = themeHex('slate-steel', '')
  if (!card.startsWith('#')) return brand
  const b = rgbOf(brand)
  const c = rgbOf(card)
  const ground = c.map((v, k) => v * (1 - tint) + b[k] * tint) as [number, number, number]
  return contrast(b, ground) >= needed ? brand : themeHex('gable-green')
}

/**
 * A brand label drawn on a row tinted with the brand at `tint` alpha (the
 * portal's active nav item): 4.5:1 against that row, measured on the surface
 * the row is drawn on (portal-panel), not the bare card.
 */
export function brandLabelOnTint(brand: string, tint: number): string {
  return brandAtLeast(brand, 4.5, tint, 'portal-panel')
}

/**
 * The label colour (as a CSS colour) on a tenant brand fill. Dark keeps the
 * black label it always had; light takes paper or ink, whichever reads better.
 */
export function inkOrPaperOn(fill: string): string {
  const paper = 'rgb(var(--gb-black))'
  if (document.documentElement.dataset.theme !== 'light' || !HEX.test(fill)) return paper
  const paperHex = themeHex('black', '')
  const inkHex = themeHex('white', '')
  if (!paperHex.startsWith('#') || !inkHex.startsWith('#')) return paper
  const f = rgbOf(fill)
  return contrast(rgbOf(inkHex), f) > contrast(rgbOf(paperHex), f) ? 'rgb(var(--gb-white))' : paper
}

function mixTo(from: [number, number, number], to: [number, number, number], t: number): string {
  return '#' + from.map((v, k) => Math.round(v * (1 - t) + to[k] * t).toString(16).padStart(2, '0')).join('')
}

/**
 * A tenant brand fill and the label to draw on it. Dark keeps the fill and
 * the black label it always had. In light, when neither paper nor ink reaches
 * 4.5:1 on a mid tone fill, the fill is stepped (toward paper or ink, as
 * brandOnCard steps a brand) until the better label does.
 */
export function brandFillWithLabel(fill: string): { fill: string; label: string } {
  const paper = 'rgb(var(--gb-black))'
  const ink = 'rgb(var(--gb-white))'
  if (document.documentElement.dataset.theme !== 'light' || !HEX.test(fill)) return { fill, label: paper }
  const paperHex = themeHex('black', '')
  const inkHex = themeHex('white', '')
  if (!paperHex.startsWith('#') || !inkHex.startsWith('#')) return { fill, label: paper }
  const P = rgbOf(paperHex)
  const I = rgbOf(inkHex)
  const best = (f: [number, number, number]) => {
    const cp = contrast(P, f)
    const ci = contrast(I, f)
    return ci > cp ? { c: ci, label: ink } : { c: cp, label: paper }
  }
  const start = rgbOf(fill)
  let pick = best(start)
  if (pick.c >= 4.5) return { fill, label: pick.label }
  // step toward the end that makes the better label stronger
  const toward = pick.label === ink ? P : I
  for (let t = 0.05; t <= 1.0001; t += 0.05) {
    const hex = mixTo(start, toward, t)
    pick = best(rgbOf(hex))
    if (pick.c >= 4.5) return { fill: hex, label: pick.label }
  }
  return { fill, label: pick.label }
}
