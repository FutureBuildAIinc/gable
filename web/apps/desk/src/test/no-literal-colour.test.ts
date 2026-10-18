// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * One guard over all of web/apps (the desk and the front door): no screen
 * draws a literal colour. No hex or rgb() value, no arbitrary hex class, no
 * literal white surface (a picture plate is the plate token, not a white
 * class) and no black veil (a modal or photo scrim is the backdrop token). A
 * colour is a token, so it follows Industrial Dark and Gable light; code that
 * cannot take a class reads the variables, as rgb(var(--gb-name)) or
 * themeColor(). A deliberate exception goes in ALLOWED as file plus literal
 * plus reason, and the rest of that file is still scanned.
 */
import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'

const APPS = resolve(process.cwd(), '..')

interface Allowed {
  /** relative to web/apps */
  file: string
  /** the exact literal as the check reports it */
  value: string
  reason: string
}

const ALLOWED: Allowed[] = [
  { file: 'desk/index.html', value: '#00FFA3', reason: "the theme-color meta value cannot read a variable" },
  { file: 'front-door/index.html', value: '#00FFA3', reason: "the theme-color meta value cannot read a variable" },
  { file: 'work-shell/index.html', value: '#00FFA3', reason: "the theme-color meta value cannot read a variable" },
  { file: 'field-shell/index.html', value: '#00FFA3', reason: "the theme-color meta value cannot read a variable" },
  { file: 'desk/src/lib/theme-colors.ts', value: '#00FFA3', reason: "the Industrial Dark value read when the stylesheet is not loaded yet" },
  { file: 'desk/src/pages/millwork/DoorConfigurator.ts', value: '#6D2E15', reason: "the mahogany preview border is the wood's own colour, the same in both modes" },
  { file: 'desk/src/pages/millwork/ProductConfigurator.ts', value: 'rgba(184, 134, 61, 0.2)', reason: "a wood species' own tint is data, the same in both modes" },
  { file: 'desk/src/pages/millwork/ProductConfigurator.ts', value: 'rgba(139, 90, 43, 0.2)', reason: "a wood species' own tint is data, the same in both modes" },
  { file: 'desk/src/pages/millwork/ProductConfigurator.ts', value: 'rgba(180, 83, 55, 0.2)', reason: "a wood species' own tint is data, the same in both modes" },
  { file: 'desk/src/pages/millwork/ProductConfigurator.ts', value: 'rgba(160, 120, 70, 0.2)', reason: "a wood species' own tint is data, the same in both modes" },
  { file: 'desk/src/pages/millwork/ProductConfigurator.ts', value: 'rgba(200, 180, 140, 0.2)', reason: "a wood species' own tint is data, the same in both modes" },
  { file: 'desk/src/pages/millwork/ProductConfigurator.ts', value: 'rgba(100, 100, 100, 0.1)', reason: "a wood species' own tint is data, the same in both modes" },
]

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

const NAMED = [
  'white', 'black', 'red', 'green', 'blue', 'yellow', 'orange', 'purple', 'pink', 'gray', 'grey', 'silver',
  'maroon', 'navy', 'teal', 'aqua', 'cyan', 'lime', 'olive', 'fuchsia', 'magenta', 'brown', 'gold', 'indigo',
  'violet', 'crimson', 'coral', 'salmon', 'tan', 'ivory', 'beige', 'khaki', 'lavender', 'orchid', 'plum',
  'tomato', 'turquoise', 'azure', 'snow', 'whitesmoke', 'gainsboro', 'lightgray', 'lightgrey', 'darkgray',
  'darkgrey', 'dimgray', 'dimgrey', 'slategray', 'slategrey', 'darkslategray', 'darkred', 'darkgreen',
  'darkblue', 'lightblue', 'lightgreen', 'skyblue', 'royalblue', 'steelblue', 'firebrick', 'seagreen',
  'forestgreen', 'limegreen', 'hotpink', 'deeppink', 'chocolate', 'sienna', 'peru', 'wheat', 'linen',
  'mintcream', 'ghostwhite', 'aliceblue', 'honeydew', 'seashell', 'oldlace', 'cornsilk', 'lemonchiffon',
].join('|')

function sources(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'dist' || name === 'coverage') continue
    const p = join(dir, name)
    if (statSync(p).isDirectory()) sources(p, out)
    else if (/\.(ts|tsx|css|html|js)$/.test(name) && !/\.test\.ts$/.test(name)) out.push(p)
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

const PROP = 'color|background(?:-color)?|border(?:-(?:top|right|bottom|left))?(?:-color)?|fill|stroke|outline(?:-color)?|caret-color|text-decoration-color'
const NAMED_IN_STYLE = new RegExp(`(?<![\\w-])(?:${PROP})\\s*:\\s*(?:[^;"'\`}]*\\s)?(?:${NAMED})(?![\\w-])`, 'gi')
const STYLE_ATTR = /\bstyle\s*=\s*(?:"([^"]*)"|'([^']*)'|`([^`]*)`)/g
const STYLE_BLOCK = /<style[^>]*>([\s\S]*?)<\/style>/gi

const SHAPES: Array<[string, RegExp, (m: string) => string]> = [
  ['arbitrary hex colour class', /[A-Za-z0-9_:&![\]>*-]*-\[#[0-9A-Fa-f]{3,8}\]/g,
    (m) => advice(m.slice(m.indexOf('[') + 1, -1))],
  ['hex colour', /(?<![&\w])#[0-9A-Fa-f]{3,8}\b/g, advice],
  ['rgb or hsl literal', /\b(?:rgba?|hsla?)\(\s*\d[^)]*\)/g,
    () => 'use rgb(var(--gb-name) / alpha) or a token class'],
  ['literal white surface', /\bbg-white(?![\w/-])/g,
    () => 'a picture plate or knob is bg-plate; a card is bg-slate-steel'],
  ['literal black surface or veil', /\bbg-black(?![\w-])(?!\/(?:5|10|15|20|25|30)\b)/g,
    () => 'a modal or photo scrim is bg-backdrop/NN'],
]

/** Every literal colour in a file's text, as [label, value, advice]. */
export function findLiterals(raw: string, isStyleFile: boolean): Array<[string, string, string]> {
  const text = stripComments(raw)
  const out: Array<[string, string, string]> = []
  for (const [label, re, hint] of SHAPES) {
    for (const m of text.matchAll(re)) {
      // An arbitrary class also contains a hex: report it once, as the class.
      if (label === 'hex colour' && /-\[$/.test(text.slice(Math.max(0, (m.index ?? 0) - 2), m.index))) continue
      out.push([label, m[0], hint(m[0])])
    }
  }
  const styleText: string[] = []
  for (const m of text.matchAll(STYLE_ATTR)) styleText.push(m[1] ?? m[2] ?? m[3] ?? '')
  for (const m of text.matchAll(STYLE_BLOCK)) styleText.push(m[1])
  if (isStyleFile) styleText.push(text)
  for (const chunk of styleText) {
    for (const m of chunk.matchAll(NAMED_IN_STYLE)) {
      const named = m[0].split(/[\s:]/).filter(Boolean).pop() ?? m[0]
      out.push(['named colour in a style', named, 'use rgb(var(--gb-name)), e.g. color: rgb(var(--gb-white))'])
    }
  }
  return out
}

describe('no literal colour anywhere in web/apps', () => {
  it('finds none that is not allowlisted by file and literal', () => {
    const found: string[] = []
    for (const file of sources(APPS)) {
      const rel = relative(APPS, file)
      const isStyleFile = /\.css$/.test(file)
      for (const [label, value, hint] of findLiterals(readFileSync(file, 'utf8'), isStyleFile)) {
        if (ALLOWED.some((a) => a.file === rel && a.value === value)) continue
        found.push(`${rel}: ${label} "${value}": ${hint}`)
      }
    }
    expect(found).toEqual([])
  })

  it('every allowlisted literal states a reason and is still present', () => {
    for (const a of ALLOWED) {
      expect(a.reason.trim().length, `${a.file} ${a.value}`).toBeGreaterThan(0)
      const text = readFileSync(join(APPS, a.file), 'utf8')
      const values = findLiterals(text, /\.css$/.test(a.file)).map(([, v]) => v)
      expect(values, `${a.file} no longer holds ${a.value}: drop the entry`).toContain(a.value)
    }
  })

  // Planted cases: each shape the guard must catch, and each exception it
  // must not widen into a whole file.
  const catches: Array<[string, string, string]> = [
    // built in two pieces so Tailwind's content scan does not see a class here
    ['an arbitrary hex class', `<div class="bg-[` + `#123456]"></div>`, 'bg-[' + '#123456]'],
    ['a three digit hex', `const c = '#abc'`, '#abc'],
    ['a four digit hex', `const c = '#abcd'`, '#abcd'],
    ['an eight digit hex', `const c = '#111320cc'`, '#111320cc'],
    ['an rgba literal', `const c = 'rgba(1, 2, 3, 0.5)'`, 'rgba(1, 2, 3, 0.5)'],
    ['a literal white surface', `<div class="p-2 bg-white">`, 'bg-white'],
    ['a named colour in a style attribute', `<p style="color: white">`, 'white'],
    ['a named colour after other declarations', `<p style="margin:0;background:red">`, 'red'],
    ['a named colour in a style block', `<style>.a { border: 1px solid black; }</style>`, 'black'],
  ]
  it.each(catches)('catches %s', (_name, text, value) => {
    expect(findLiterals(text, false).map(([, v]) => v)).toContain(value)
  })

  it('does not call a variable read or a token class a literal', () => {
    const ok = `<p class="bg-slate-steel text-white" style="color: rgb(var(--gb-white)); margin: 0">`
    expect(findLiterals(ok, false)).toEqual([])
  })

  it('an allowlisted literal does not exempt the rest of its file', () => {
    const file = 'desk/index.html'
    const allowed = ALLOWED.find((a) => a.file === file)
    const planted = `${allowed?.value ?? ''} <p style="background:#123456">`
    const left = findLiterals(planted, false).filter(
      ([, v]) => !ALLOWED.some((a) => a.file === file && a.value === v),
    )
    expect(left.map(([, v]) => v)).toEqual(['#123456'])
  })
})
