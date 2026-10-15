// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The tokens: Industrial Dark is unchanged (the dark variables equal
 * Tailwind's default palettes and today's brand hex), Gable light holds the
 * palette's values exactly, and every contrast pair the palette names holds
 * its WCAG threshold from the token values.
 */
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import colors from 'tailwindcss/colors'
import { LIGHT_PALETTE, LIGHT_FUCHSIA } from './light-palette.fixture'

const css = readFileSync(resolve(process.cwd(), 'src/tokens.css'), 'utf8')

/** The declarations of the first block that opens with `selector {`. */
function block(selector: string): Record<string, string> {
  const start = css.indexOf(`${selector} {`)
  expect(start, `selector ${selector}`).toBeGreaterThanOrEqual(0)
  const end = css.indexOf('\n  }', start)
  const out: Record<string, string> = {}
  for (const m of css.slice(start, end).matchAll(/(--[a-z0-9-]+):\s*([^;]+);/g)) out[m[1]] = m[2].trim()
  return out
}

/** The donut's second light series: the palette's blue mixed a tenth toward the card (`#f8f3ec`), so it sits between the deep green and the sage in lightness. */
const LIGHT_DONUT_BLUE = '#1c7798'

const dark = block(':root')
const light = block(':root[data-theme="light"]')

function channels(hex: string): string {
  const h = hex.replace('#', '')
  return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16)).join(' ')
}

function luminance(hex: string): number {
  const lin = channels(hex).split(' ').map((v) => {
    const c = Number(v) / 255
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * lin[0] + 0.7152 * lin[1] + 0.0722 * lin[2]
}

function ratio(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

/** A token's value as a hex colour, in the given mode. */
function hex(vars: Record<string, string>, name: string): string {
  const parts = vars[`--gb-${name}`].split(' ').map(Number)
  return '#' + parts.map((n) => n.toString(16).padStart(2, '0')).join('')
}

const STEPS = ['50', '100', '200', '300', '400', '500', '600', '700', '800', '900', '950'] as const
const NEUTRALS = ['zinc', 'slate', 'gray'] as const
const STATUS = Object.keys(LIGHT_PALETTE.status) as (keyof typeof LIGHT_PALETTE.status)[]

/** The shipped palettes whose dark values are Tailwind's own. */
const DEFAULT_PALETTES = [...NEUTRALS, ...STATUS, 'fuchsia'] as const

describe('Industrial Dark is unchanged', () => {
  it('holds Tailwind default values for white, black and every palette', () => {
    expect(dark['--gb-white']).toBe('255 255 255')
    expect(dark['--gb-black']).toBe('0 0 0')
    const dflt = colors as unknown as Record<string, Record<string, string>>
    for (const name of DEFAULT_PALETTES) {
      for (const step of STEPS) {
        expect(dark[`--gb-${name}-${step}`], `${name}-${step}`).toBe(channels(dflt[name][step]))
      }
    }
  })

  it("holds today's brand hex", () => {
    const brand: Record<string, string> = {
      'gable-green': '#00FFA3',
      'deep-space': '#0A0B10',
      'slate-steel': '#161821',
      'surface-2': '#1E2029',
      'surface-3': '#252836',
      'safety-red': '#F43F5E',
      'blueprint-blue': '#38BDF8',
      plate: '#FFFFFF',
      backdrop: '#000000',
    }
    for (const [name, value] of Object.entries(brand)) {
      expect(dark[`--gb-${name}`], name).toBe(channels(value))
    }
  })

  it('pins every colour that has its own token so dark draws as it did', () => {
    // The places that once collapsed onto a nearest token: each has a token
    // whose dark value is exactly the value it drew before.
    const exact: Record<string, string> = {
      // the till dialogs on the counter
      'till-surface': '#161b22',
      'till-edge': '#30363d',
      'till-text': '#e6edf3',
      'till-muted': '#8b949e',
      'till-soft': '#c9d1d9',
      'till-field': '#0d1117',
      'till-accent': '#8957e5',
      'till-go': '#238636',
      'till-ok': '#3fb950',
      'till-bad': '#f85149',
      'till-warn': '#d29922',
      'till-rule': '#21262d',
      // the configurator text, the scanner dialog, the matrix row hover
      pale: '#E0E0E0',
      well: '#12131A',
      'well-hover': '#1a1c26',
      // the second voice keeps the neutrals the chips drew
      voice: '#ffffff',
      'voice-soft': colors.zinc[300],
      'voice-text': colors.zinc[400],
      'voice-fill': colors.zinc[500],
    }
    for (const [name, value] of Object.entries(exact)) {
      expect(dark[`--gb-${name}`], name).toBe(channels(value))
    }
    // the glow shadows by size and strength
    expect(dark['--gb-glow-10-30']).toBe('0 0 10px rgba(0, 255, 163, 0.3)')
    expect(dark['--gb-glow-soft']).toBe('0 0 15px rgba(0, 255, 163, 0.1)')
    expect(dark['--gb-glow']).toBe('0 0 15px rgba(0, 255, 163, 0.3)')
    expect(dark['--gb-glow-20-10']).toBe('0 0 20px rgba(0, 255, 163, 0.1)')
    expect(dark['--gb-glow-20-30']).toBe('0 0 20px rgba(0, 255, 163, 0.3)')
    expect(dark['--gb-glow-30-15']).toBe('0 0 30px rgba(0, 255, 163, 0.15)')
    expect(dark['--gb-glow-40-30']).toBe('0 0 40px rgba(0, 255, 163, 0.3)')
  })

  it('pins every dark value: no variable is unlisted and none differs', () => {
    const dflt = colors as unknown as Record<string, Record<string, string>>
    const expected: Record<string, string> = {
      '--gb-white': '255 255 255',
      '--gb-black': '0 0 0',
    }
    for (const name of DEFAULT_PALETTES) {
      for (const step of STEPS) expected[`--gb-${name}-${step}`] = channels(dflt[name][step])
    }
    const hexes: Record<string, string> = {
      'gable-green': '#00FFA3',
      'gable-green-hover': '#00E593',
      'gable-green-tint': '#00331F',
      'deep-space': '#0A0B10',
      'slate-steel': '#161821',
      'surface-2': '#1E2029',
      'surface-3': '#252836',
      'safety-red': '#F43F5E',
      'blueprint-blue': '#38BDF8',
      plate: '#FFFFFF',
      backdrop: '#000000',
      sage: colors.zinc[400],
      'sage-fill': colors.zinc[600],
      'sage-tint': colors.zinc[800],
      voice: '#FFFFFF',
      'voice-soft': colors.zinc[300],
      'voice-text': colors.zinc[400],
      'voice-fill': colors.zinc[500],
      pale: '#E0E0E0',
      well: '#12131A',
      'well-hover': '#1a1c26',
      'till-surface': '#161b22',
      'till-edge': '#30363d',
      'till-text': '#e6edf3',
      'till-muted': '#8b949e',
      'till-soft': '#c9d1d9',
      'till-field': '#0d1117',
      'till-accent': '#8957e5',
      'till-go': '#238636',
      'till-ok': '#3fb950',
      'till-bad': '#f85149',
      'till-warn': '#d29922',
      'till-rule': '#21262d',
      'portal-ground': '#0C0E14',
      'portal-panel': '#111320',
      'chip-ground': '#000000',
      shade: '#000000',
      'photo-btn': '#FFFFFF',
      'map-note': '#666666',
      'chart-tick': colors.zinc[600],
      'chart-fulfilled': colors.indigo[400],
      'chart-series-1': '#00FFA3',
      'chart-series-2': '#38BDF8',
      'picture-plate': '#FFFFFF',
      tile: '#0A0B10',
    }
    for (const [name, value] of Object.entries(hexes)) expected[`--gb-${name}`] = channels(value)
    expected['--gb-hover-row'] = '255 255 255'
    expected['--gb-hover-row-a'] = '0.05'
    Object.assign(expected, {
      '--gb-tile-shadow': '0 0 #0000',
      '--gb-glow': '0 0 15px rgba(0, 255, 163, 0.3)',
      '--gb-glow-soft': '0 0 15px rgba(0, 255, 163, 0.1)',
      '--gb-glow-strong': '0 0 25px rgba(0, 255, 163, 0.5)',
      '--gb-glow-10-30': '0 0 10px rgba(0, 255, 163, 0.3)',
      '--gb-glow-20-10': '0 0 20px rgba(0, 255, 163, 0.1)',
      '--gb-glow-20-30': '0 0 20px rgba(0, 255, 163, 0.3)',
      '--gb-glow-30-15': '0 0 30px rgba(0, 255, 163, 0.15)',
      '--gb-glow-40-30': '0 0 40px rgba(0, 255, 163, 0.3)',
      '--gb-elevation-1': '0px 1px 2px rgba(0, 0, 0, 0.3), 0px 1px 3px 1px rgba(0, 0, 0, 0.15)',
      '--gb-elevation-2': '0px 1px 2px rgba(0, 0, 0, 0.3), 0px 2px 6px 2px rgba(0, 0, 0, 0.15)',
      '--gb-elevation-3': '0px 1px 3px rgba(0, 0, 0, 0.3), 0px 4px 8px 3px rgba(0, 0, 0, 0.15)',
      // the semantic HSL variables and the radius, as the base file had them
      '--background': '230 20% 5%',
      '--foreground': '210 40% 98%',
      '--primary': '158 100% 50%',
      '--primary-foreground': '230 20% 5%',
      '--primary-container': '158 100% 10%',
      '--on-primary-container': '158 100% 80%',
      '--secondary': '217 33% 17%',
      '--secondary-foreground': '210 40% 98%',
      '--secondary-container': '217 33% 25%',
      '--on-secondary-container': '210 40% 98%',
      '--tertiary': '199 95% 60%',
      '--tertiary-foreground': '230 20% 5%',
      '--destructive': '346 87% 60%',
      '--destructive-foreground': '210 40% 98%',
      '--card': '229 19% 11%',
      '--card-foreground': '210 40% 98%',
      '--popover': '229 19% 11%',
      '--popover-foreground': '210 40% 98%',
      '--muted': '217 33% 17%',
      '--muted-foreground': '215 20% 65%',
      '--accent': '158 100% 50%',
      '--accent-foreground': '230 20% 5%',
      '--border': '217 33% 20%',
      '--input': '217 33% 20%',
      '--ring': '158 100% 50%',
      '--radius': '0.75rem',
    })
    expect(Object.keys(dark).sort()).toEqual(Object.keys(expected).sort())
    for (const [key, value] of Object.entries(expected)) expect(dark[key], key).toBe(value)
  })

  it('draws no colour-scheme on the dark root (native controls stay as they were)', () => {
    expect(css.slice(0, css.indexOf(':root[data-theme="light"] {'))).not.toMatch(/color-scheme/)
  })

  it('keeps the semantic HSL variables as they were', () => {
    expect(dark['--background']).toBe('230 20% 5%')
    expect(dark['--primary']).toBe('158 100% 50%')
    expect(dark['--card']).toBe('229 19% 11%')
    expect(dark['--border']).toBe('217 33% 20%')
    expect(dark['--ring']).toBe('158 100% 50%')
  })
})

describe('Gable light holds the palette values', () => {
  it('the surfaces and voices', () => {
    const s = LIGHT_PALETTE.surfaces
    const map: Record<string, string> = {
      'deep-space': s.ground,
      'slate-steel': s.card,
      'surface-2': s.raised,
      'surface-3': s.modal,
      plate: s.modal,
      backdrop: s.ink,
      'gable-green': s.green,
      'gable-green-hover': s['green-hover'],
      'gable-green-tint': s['green-tint'],
      'blueprint-blue': s.blue,
      'safety-red': s.red,
      sage: s.sage,
      'sage-fill': s['sage-fill'],
      'sage-tint': s['sage-tint'],
    }
    for (const [name, value] of Object.entries(map)) {
      expect(light[`--gb-${name}`], name).toBe(channels(value))
    }
  })

  it('the neutral ramp on white, black, zinc, slate and gray', () => {
    const n = LIGHT_PALETTE.neutrals
    expect(light['--gb-white']).toBe(channels(n.white))
    expect(light['--gb-black']).toBe(channels(n.black))
    for (const name of NEUTRALS) {
      for (const step of STEPS) {
        expect(light[`--gb-${name}-${step}`], `${name}-${step}`).toBe(channels(n[step]))
      }
    }
  })

  it('every status palette step', () => {
    for (const name of STATUS) {
      for (const step of STEPS) {
        expect(light[`--gb-${name}-${step}`], `${name}-${step}`).toBe(
          channels(LIGHT_PALETTE.status[name][step]),
        )
      }
    }
  })

  it('sage as the second voice, and no glow of any size', () => {
    const s = LIGHT_PALETTE.surfaces
    for (const name of ['voice', 'voice-soft', 'voice-text']) {
      expect(light[`--gb-${name}`], name).toBe(channels(s.sage))
    }
    expect(light['--gb-voice-fill']).toBe(channels(s['sage-fill']))
    for (const key of Object.keys(light).filter((k) => k.startsWith('--gb-glow'))) {
      expect(light[key], key).toBe('0 0 #0000')
    }
  })

  it('every light value of the tokens that carry their own dark value', () => {
    const sv = LIGHT_PALETTE.surfaces
    const n = LIGHT_PALETTE.neutrals
    const byName: Record<string, string> = {
      'portal-ground': sv.ground,
      'portal-panel': sv.card,
      'chip-ground': sv.card,
      shade: sv.ground,
      'photo-btn': sv.paper,
      'map-note': n['500'],
      'chart-tick': n['500'],
      'chart-fulfilled': sv['sage-fill'],
      'chart-series-1': sv['green-hover'],
      'chart-series-2': LIGHT_DONUT_BLUE,
      'picture-plate': sv.ground,
      tile: sv.raised,
    }
    for (const [name, value] of Object.entries(byName)) {
      expect(light[`--gb-${name}`], name).toBe(channels(value))
    }
    expect(light['--gb-hover-row']).toBe(channels(sv.ground))
    expect(light['--gb-hover-row-a']).toBe('0.7')
    expect(light['--gb-tile-shadow']).toBe('0 1px 2px rgb(34 31 29 / 0.14)')
  })

  it('the derived fuchsia ramp', () => {
    for (const step of STEPS) {
      expect(light[`--gb-fuchsia-${step}`], `fuchsia-${step}`).toBe(channels(LIGHT_FUCHSIA[step]))
    }
  })

  it('soft ink tinted shadows and no glow', () => {
    expect(light['--gb-glow']).toBe('0 0 #0000')
    expect(light['--gb-glow-strong']).toBe('0 0 #0000')
    expect(light['--gb-elevation-1']).toBe('0 1px 2px rgb(34 31 29 / 0.14)')
    expect(light['--gb-elevation-2']).toBe('0 3px 10px rgb(34 31 29 / 0.16)')
    expect(light['--gb-elevation-3']).toBe('0 12px 32px rgb(34 31 29 / 0.22)')
  })

  it('defines every variable dark defines', () => {
    for (const key of Object.keys(dark).filter((k) => k.startsWith('--gb-'))) {
      expect(light, key).toHaveProperty([key])
    }
  })
})

describe('the order donut in light', () => {
  const series = ['chart-series-1', 'chart-series-2', 'chart-fulfilled']

  it('spreads its first three series apart in lightness, so it reads in greyscale', () => {
    const l = series.map((name) => luminance(hex(light, name)))
    expect(l[0]).toBeLessThan(l[1])
    expect(l[1]).toBeLessThan(l[2])
    expect(l[1] - l[0]).toBeGreaterThanOrEqual(0.05)
    expect(l[2] - l[1]).toBeGreaterThanOrEqual(0.05)
  })

  it.each(series)('%s reads against the card as a graphic (3:1)', (name) => {
    expect(ratio(hex(light, name), hex(light, 'slate-steel'))).toBeGreaterThanOrEqual(3)
  })
})

describe('Gable light contrast (WCAG 2), computed from the tokens', () => {
  // [pair, foreground token, background token, needed ratio]
  const PAIRS: [string, string, string, number][] = [
    ['ink on ground', 'white', 'deep-space', 4.5],
    ['ink on card', 'white', 'slate-steel', 4.5],
    ['ink on modal', 'white', 'surface-3', 4.5],
    ['muted (neutral 400) on card', 'zinc-400', 'slate-steel', 4.5],
    ['muted (neutral 400) on ground', 'zinc-400', 'deep-space', 4.5],
    ['hint (neutral 500) on card', 'zinc-500', 'slate-steel', 4.5],
    ['hint (neutral 500) on ground', 'zinc-500', 'deep-space', 4.5],
    ['control edge (neutral 600) on card', 'zinc-600', 'slate-steel', 3],
    ['control edge (neutral 600) on ground', 'zinc-600', 'deep-space', 3],
    ['green control on ground', 'gable-green', 'deep-space', 3],
    ['green text on card', 'gable-green', 'slate-steel', 4.5],
    ['green text on ground', 'gable-green', 'deep-space', 4.5],
    ['paper label on green', 'black', 'gable-green', 4.5],
    ['paper label on green hover', 'black', 'gable-green-hover', 4.5],
    ['blue text on card', 'blueprint-blue', 'slate-steel', 4.5],
    ['blue text on ground', 'blueprint-blue', 'deep-space', 4.5],
    ['red text on card', 'safety-red', 'slate-steel', 4.5],
    ['red text on ground', 'safety-red', 'deep-space', 4.5],
    ['paper label on red', 'black', 'safety-red', 4.5],
    ['sage text on card', 'sage', 'slate-steel', 4.5],
    ['sage text on ground', 'sage', 'deep-space', 4.5],
    ['ink label on sage fill', 'white', 'sage-fill', 4.5],
    ['green text on green tint', 'gable-green', 'gable-green-tint', 4.5],
    ['sage text on sage tint', 'sage', 'sage-tint', 4.5],
    ['fuchsia 300 (A2A chip) on card', 'fuchsia-300', 'slate-steel', 4.5],
    ['fuchsia 300 on ground', 'fuchsia-300', 'deep-space', 4.5],
    ['fuchsia 500 on card', 'fuchsia-500', 'slate-steel', 4.5],
    ['ink on fuchsia tint (950)', 'white', 'fuchsia-950', 4.5],
  ]

  it.each(PAIRS)('%s', (_name, fg, bg, needed) => {
    const r = ratio(hex(light, fg), hex(light, bg))
    console.log(`contrast ${_name}: ${hex(light, fg)} on ${hex(light, bg)} = ${r.toFixed(2)}:1 (needs ${needed}:1)`)
    expect(r).toBeGreaterThanOrEqual(needed)
  })
})
