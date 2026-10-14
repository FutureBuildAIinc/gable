// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Guard: the web apps never use the browser's own chrome. No `confirm(`,
 * `alert(` or `prompt(` call (with or without `window.`) and no native
 * select element belongs in application source; the app's own dialog and
 * select components take their place. A method a file declares itself (for
 * example `private async confirm(...)`) is not the browser's and is allowed.
 * Every violation is named by file and line.
 */
import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const APPS_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..', '..')

const SKIP_DIRS = new Set(['node_modules', 'dist', 'coverage', '.vite'])

function isTestFile(rel: string): boolean {
  return /\.(test|spec)\.ts$/.test(rel) || rel.includes(`${path.sep}src${path.sep}test${path.sep}`)
}

function collectSources(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) {
      if (!SKIP_DIRS.has(entry.name)) collectSources(full, out)
    } else if (entry.name.endsWith('.ts') && !isTestFile(full)) {
      out.push(full)
    }
  }
  return out
}

// A browser dialog call: not preceded by `.` or an identifier character (so a
// method call or declaration of the file's own does not match), optionally on
// `window.`.
const DIALOG_CALL = /(?<![\w$.])(?:window\.)?(confirm|alert|prompt)\s*\(/
// A method declaration of the file's own, for example `private async confirm(`.
const METHOD_DECL = /(^|\s)(public|private|protected|static|override|abstract|async)\s+$/
const NATIVE_SELECT = new RegExp('<' + 'select')

function violations(): string[] {
  const found: string[] = []
  for (const file of collectSources(APPS_ROOT)) {
    const rel = path.relative(APPS_ROOT, file)
    const lines = readFileSync(file, 'utf8').split('\n')
    lines.forEach((line, index) => {
      const where = `${rel}:${index + 1}`
      const call = DIALOG_CALL.exec(line)
      if (call && !METHOD_DECL.test(line.slice(0, call.index))) {
        found.push(`${where}: browser dialog call \`${call[0]}\``)
      }
      if (NATIVE_SELECT.test(line)) {
        found.push(`${where}: native <` + `select element`)
      }
    })
  }
  return found
}

describe('browser chrome guard', () => {
  it('no browser dialog call (confirm, alert, prompt) under web/apps', () => {
    const found = violations().filter((v) => v.includes('dialog call'))
    expect(found, found.join('\n')).toEqual([])
  })

  it('no native select element under web/apps', () => {
    const found = violations().filter((v) => !v.includes('dialog call'))
    expect(found, found.join('\n')).toEqual([])
  })
})
