// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The dashboard, portal, yard, driver and remaining desk pages draw no
 * literal colour: no hex or rgb() value, no literal white surface
 * (bg-white means a picture plate, so it is bg-plate) and no black veil
 * (a modal or photo scrim is bg-backdrop). A colour is a token, so it follows
 * Industrial Dark and Gable light. The counter, the quote builder, Market
 * check and Tech Admin are checked by their own surface work.
 */
import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'

const SRC = resolve(process.cwd(), 'src')

/** Paths (relative to src) owned by the counter, quote builder, Market check and Tech Admin work. */
const OTHER_SURFACES = [
  /^pages\/pos\//,
  /^pages\/QuoteBuilder/,
  /^pages\/quotes\//,
  /^components\/quotes\//,
  /^pages\/admin\/pricing\/MarketCheck/,
  /^pages\/admin\/tech_admin\//,
]

/** file (relative to src) -> reason a literal colour stays. */
const ALLOWED: Record<string, string> = {
  'lib/theme-colors.ts': 'the Industrial Dark value read when the stylesheet is not loaded yet',
  'pages/millwork/ProductConfigurator.ts':
    'wood species swatches: a species\' own tint is data, the same in both modes',
  'pages/millwork/DoorConfigurator.ts':
    'the mahogany preview border is the wood\'s own colour, the same in both modes',
}

function sources(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'dist') continue
    const p = join(dir, name)
    if (statSync(p).isDirectory()) sources(p, out)
    else if (/\.ts$/.test(name) && !/\.test\.ts$/.test(name)) out.push(p)
  }
  return out
}

function stripComments(text: string): string {
  return text.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:'"`])\/\/.*$/gm, '$1')
}

const SHAPES: Array<[string, RegExp]> = [
  ['hex colour', /#[0-9A-Fa-f]{6}\b|#[0-9A-Fa-f]{3}\b/g],
  ['rgb or hsl literal', /\b(?:rgba?|hsla?)\(\s*\d/g],
  ['literal white surface', /\bbg-white(?![\w/-])/g],
  ['literal black surface or veil', /\bbg-black(?![\w-])(?!\/(?:5|10|15|20|25|30)\b)/g],
]

describe('no literal colour outside the counter, quote builder, Market check and Tech Admin', () => {
  it('finds none outside the allowlist', () => {
    const found: string[] = []
    for (const file of sources(SRC)) {
      const rel = relative(SRC, file)
      if (rel in ALLOWED || OTHER_SURFACES.some((re) => re.test(rel))) continue
      const text = stripComments(readFileSync(file, 'utf8'))
      for (const [label, re] of SHAPES) {
        for (const m of text.matchAll(re)) found.push(`${rel}: ${label}: ${m[0]}`)
      }
    }
    expect(found).toEqual([])
  })

  it('every allowlisted file states a reason', () => {
    for (const [file, reason] of Object.entries(ALLOWED)) {
      expect(reason.trim().length, file).toBeGreaterThan(0)
    }
  })
})
