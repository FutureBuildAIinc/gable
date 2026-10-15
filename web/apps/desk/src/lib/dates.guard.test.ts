// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The date guard (G1 walk note N15).
 *
 * The app has one date module, `web/apps/desk/src/lib/dates.ts`, and one date
 * field, `<gable-date-field>`. This guard walks every `.ts` source under
 * `web/apps/` (tests excluded) and fails on:
 *
 *   - a native `date`, `datetime-local`, `month` or `time` input (the
 *     browser's white system calendar and its "mm/dd/yyyy" placeholder),
 *     quoted, unquoted or bound,
 *   - a `toLocaleDateString(`, `toLocaleTimeString(` or
 *     `Intl.DateTimeFormat(` call outside `lib/dates.ts`, so no page formats
 *     a date on its own in the browser's locale instead of the dealer's,
 *   - a `toLocaleString(` on a date (built from a Date, or called with date
 *     options), a `toDateString(` or `toUTCString(`, or a `date-fns` import,
 *     the other places a date hides in the browser's own format.
 *
 * Every finding names its file and line.
 */
import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { join, relative, sep } from 'node:path'

const APPS_ROOT = join(__dirname, '..', '..', '..')

// The one module allowed to call the date formatters.
const EXEMPT = join('desk', 'src', 'lib', 'dates.ts')

function* sources(dir: string): Generator<string> {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name === 'node_modules' || entry.name === 'dist') continue
      yield* sources(path)
    } else if (entry.name.endsWith('.ts') && !entry.name.endsWith('.test.ts')) {
      yield path
    }
  }
}

const RULES: Array<{ kind: string; pattern: RegExp }> = [
  {
    kind: 'native date/time input',
    pattern: /(?:^|\s|\.)type\s*=\s*(?:\$\{\s*)?['"]?(?:date|datetime-local|month|time)['"]?(?=[\s}>/]|$)/,
  },
  { kind: 'toLocaleDateString(', pattern: /\.toLocaleDateString\s*\(/ },
  { kind: 'toLocaleTimeString(', pattern: /\.toLocaleTimeString\s*\(/ },
  { kind: 'Intl.DateTimeFormat(', pattern: /Intl\.DateTimeFormat\s*\(/ },
  // toLocaleString on a date: built from a Date, or called with date options
  // (numbers never take those). A plain toLocaleString() on a number stays.
  { kind: 'toLocaleString( on a date', pattern: /new Date\([^)]*\)\.toLocaleString\s*\(/ },
  {
    kind: 'toLocaleString( with date options',
    pattern: /\.toLocaleString\s*\([^)]*\b(?:dateStyle|timeStyle|month|weekday|day|year|hour)\b/,
  },
  { kind: 'toDateString(', pattern: /\.toDateString\s*\(/ },
  { kind: 'toUTCString(', pattern: /\.toUTCString\s*\(/ },
  { kind: 'date-fns import', pattern: /from\s+['"]date-fns['"]/ },
]

describe('date guard', () => {
  it('no native date inputs and no own date formatting outside lib/dates.ts', () => {
    const findings: string[] = []
    for (const file of sources(APPS_ROOT)) {
      const rel = relative(APPS_ROOT, file).split(sep).join('/')
      if (rel === EXEMPT.split(sep).join('/')) continue
      const lines = readFileSync(file, 'utf8').split('\n')
      lines.forEach((line, i) => {
        for (const rule of RULES) {
          if (rule.pattern.test(line)) {
            findings.push(`${rel}:${i + 1}: ${rule.kind}: ${line.trim()}`)
          }
        }
      })
    }
    expect(findings).toEqual([])
  })

  // The native input rule against the forms this codebase writes: the line's
  // own end after the value (the usual multi-line form), inline, unquoted and
  // bound. Planted, so a rule that misses one fails here and not in a page.
  it('catches a native date input at a line\'s end, inline, unquoted and bound', () => {
    const rule = RULES.find((r) => r.kind === 'native date/time input')
    expect(rule).toBeTruthy()
    const hits = [
      'type="date"',
      '<input type="date" .value=${this.asOf} />',
      'type="datetime-local"',
      'type="month"',
      'type="time" />',
      'type=date>',
      ".type=${'date'}",
    ]
    for (const h of hits) expect(rule!.pattern.test(h), h).toBe(true)
    for (const miss of ['type="text"', 'type="number"', 'type="daterange"', 'prototype="date"']) {
      expect(rule!.pattern.test(miss), miss).toBe(false)
    }
  })
})
