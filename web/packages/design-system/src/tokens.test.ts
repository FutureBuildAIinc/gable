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
import { LIGHT_PALETTE } from './light-palette.fixture'

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

describe('Industrial Dark is unchanged', () => {
  it('holds Tailwind default values for white, black and every palette', () => {
    expect(dark['--gb-white']).toBe('255 255 255')
    expect(dark['--gb-black']).toBe('0 0 0')
    const dflt = colors as unknown as Record<string, Record<string, string>>
    for (const name of [...NEUTRALS, ...STATUS]) {
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
  ]

  it.each(PAIRS)('%s', (_name, fg, bg, needed) => {
    const r = ratio(hex(light, fg), hex(light, bg))
    console.log(`contrast ${_name}: ${hex(light, fg)} on ${hex(light, bg)} = ${r.toFixed(2)}:1 (needs ${needed}:1)`)
    expect(r).toBeGreaterThanOrEqual(needed)
  })
})
