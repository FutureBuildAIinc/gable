// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * One guard over all of web/apps (the desk and the front door): no screen
 * draws a literal colour. No hex or rgb() value, no arbitrary hex class
 * (bg-[#161821]), no literal white surface (bg-white means a picture plate,
 * so it is bg-plate) and no black veil (a modal or photo scrim is
 * bg-backdrop). A colour is a token, so it follows Industrial Dark and Gable
 * light; colours that code cannot take as a class read the variables
 * (rgb(var(--gb-...)) or themeColor()). A deliberate exception goes in
 * ALLOWED with its reason.
 */
import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'

const APPS = resolve(process.cwd(), '..')

/** file (relative to web/apps) -> reason a literal colour stays. */
const ALLOWED: Record<string, string> = {
  'desk/src/lib/theme-colors.ts': 'the Industrial Dark value read when the stylesheet is not loaded yet',
  'desk/src/pages/millwork/ProductConfigurator.ts':
    "wood species swatches: a species' own tint is data, the same in both modes",
  'desk/src/pages/millwork/DoorConfigurator.ts':
    "the mahogany preview border is the wood's own colour, the same in both modes",
  'desk/index.html': 'the theme-color meta value cannot read a variable',
  'front-door/index.html': 'the theme-color meta value cannot read a variable',
}

/** What to use instead, by the literal's value (dark hex, lower case). */
const TOKEN_FOR: Record<string, string> = {
  '#00ffa3': 'gable-green',
  '#0a0b10': 'deep-space',
  '#161821': 'slate-steel',
  '#1e2029': 'surface-2',
  '#252836': 'surface-3',
  '#f43f5e': 'safety-red',
  '#38bdf8': 'blueprint-blue',
  '#ffffff': 'plate (a picture plate) or white (text)',
  '#fff': 'plate (a picture plate) or white (text)',
  '#000000': 'backdrop (a veil) or black (a label on green)',
  '#000': 'backdrop (a veil) or black (a label on green)',
}

function sources(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'dist' || name === 'coverage') continue
    const p = join(dir, name)
    if (statSync(p).isDirectory()) sources(p, out)
    else if (/\.(ts|tsx|css|html)$/.test(name) && !/\.test\.ts$/.test(name)) out.push(p)
  }
  return out
}

function stripComments(text: string): string {
  return text
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/(^|[^:'"`])\/\/.*$/gm, '$1')
}

function advice(value: string): string {
  const token = TOKEN_FOR[value.toLowerCase()]
  return token !== undefined
    ? `use the token ${token}`
    : 'use a token class (text-white, bg-slate-steel, border-zinc-800, ...) or rgb(var(--gb-name))'
}

const SHAPES: Array<[string, RegExp, (m: string) => string]> = [
  ['arbitrary hex colour class', /[A-Za-z0-9_:&![\]>*-]*-\[#[0-9A-Fa-f]{3,8}\]/g,
    (m) => advice(m.slice(m.indexOf('[') + 1, -1))],
  ['hex colour', /#[0-9A-Fa-f]{6}\b|#[0-9A-Fa-f]{3}\b/g, advice],
  ['rgb or hsl literal', /\b(?:rgba?|hsla?)\(\s*\d/g,
    () => 'use rgb(var(--gb-name) / alpha) or a token class'],
  ['literal white surface', /\bbg-white(?![\w/-])/g,
    () => 'a picture plate or knob is bg-plate; a card is bg-slate-steel'],
  ['literal black surface or veil', /\bbg-black(?![\w-])(?!\/(?:5|10|15|20|25|30)\b)/g,
    () => 'a modal or photo scrim is bg-backdrop/NN'],
]

describe('no literal colour anywhere in web/apps', () => {
  it('finds none outside the allowlist', () => {
    const found: string[] = []
    for (const file of sources(APPS)) {
      const rel = relative(APPS, file)
      if (rel in ALLOWED) continue
      const text = stripComments(readFileSync(file, 'utf8'))
      for (const [label, re, hint] of SHAPES) {
        for (const m of text.matchAll(re)) {
          // The arbitrary class also contains a hex: report it once, as the class.
          if (label === 'hex colour' && /-\[$/.test(text.slice(Math.max(0, m.index - 2), m.index))) continue
          found.push(`${rel}: ${label} "${m[0]}": ${hint(m[0])}`)
        }
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
