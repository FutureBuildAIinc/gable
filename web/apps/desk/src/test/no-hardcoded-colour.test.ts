// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The sweep stays swept: no arbitrary hex colour class (bg-[#161821],
 * text-[#00FFA3], border-[#0A0B10] and the like) in web/apps/**. A colour
 * the desk draws is a token, so it follows Industrial Dark and Gable light.
 * A deliberate exception goes in ALLOWED with its reason.
 */
import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'

/** file (relative to web/apps) -> reason the arbitrary colour stays. */
const ALLOWED: Record<string, string> = {}

/**
 * Files whose inline style= and Lit css colours have been moved onto the
 * variables: no raw hex or rgb() literal may return there. Each surface adds
 * its files as it is swept.
 */
const INLINE_CLEAN = [
  'desk/src/pages/pos/POSTerminal.ts',
  'desk/src/components/layout/app-shell.ts',
]

const APPS = resolve(process.cwd(), '..')
const CLASS_HEX = /[A-Za-z0-9_:&![\]>*-]*-\[#[0-9A-Fa-f]{3,8}\]/g

function sources(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'dist' || name === 'coverage') continue
    const p = join(dir, name)
    if (statSync(p).isDirectory()) sources(p, out)
    else if (/\.(ts|tsx|css|html)$/.test(name) && !/\.test\.ts$/.test(name)) out.push(p)
  }
  return out
}

describe('no arbitrary hex colour class in web/apps', () => {
  it('finds none outside the allowlist', () => {
    const found: string[] = []
    for (const file of sources(APPS)) {
      const rel = relative(APPS, file)
      if (rel in ALLOWED) continue
      const text = readFileSync(file, 'utf8')
      for (const m of text.matchAll(CLASS_HEX)) found.push(`${rel}: ${m[0]}`)
    }
    expect(found).toEqual([])
  })

  it('keeps the swept files free of raw colour literals', () => {
    const raw = /#[0-9A-Fa-f]{6}\b|#[0-9A-Fa-f]{3}\b|rgba?\((?!var\()/g
    const found: string[] = []
    for (const rel of INLINE_CLEAN) {
      const text = readFileSync(join(APPS, rel), 'utf8')
      for (const m of text.matchAll(raw)) found.push(`${rel}: ${m[0]}`)
    }
    expect(found).toEqual([])
  })

  it('every allowlisted file states a reason', () => {
    for (const [file, reason] of Object.entries(ALLOWED)) {
      expect(reason.trim().length, file).toBeGreaterThan(0)
    }
  })
})
