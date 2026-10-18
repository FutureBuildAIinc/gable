// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Guard: the framing headers stay narrow. The default header file
 * (web/nginx-headers.conf) refuses framing outright (DENY, frame-ancestors
 * 'none'); only the desk's catch all location in web/nginx.conf includes the
 * frameable file (SAMEORIGIN, frame-ancestors 'self'), because the work shell
 * frames desk pages. The front door, the door's assets, the work shell host
 * itself and every portal page (the contractor sign in and checkout live
 * there) stay unframeable.
 */
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const WEB = resolve(process.cwd(), '..', '..')

function read(rel: string): string {
  return readFileSync(resolve(WEB, rel), 'utf8')
}

/** Split an nginx server block into its location blocks: [location line, body]. */
function locationBlocks(conf: string): Array<{ header: string; body: string }> {
  const out: Array<{ header: string; body: string }> = []
  const lines = conf.split('\n')
  let i = 0
  while (i < lines.length) {
    const line = lines[i] ?? ''
    const m = line.match(/^\s*location\s+(.+?)\s*\{\s*$/)
    if (!m) {
      i += 1
      continue
    }
    let depth = 1
    let body = ''
    i += 1
    while (i < lines.length && depth > 0) {
      const l = lines[i] ?? ''
      depth += (l.match(/\{/g) ?? []).length
      depth -= (l.match(/\}/g) ?? []).length
      if (depth > 0) body += l + '\n'
      i += 1
    }
    out.push({ header: m[1] ?? '', body })
  }
  return out
}

describe('the framing headers guard', () => {
  it('the default headers refuse framing; the frameable headers allow this origin only', () => {
    const deny = read('nginx-headers.conf')
    expect(deny).toContain('X-Frame-Options "DENY"')
    expect(deny).toContain("frame-ancestors 'none'")
    expect(deny).not.toContain('SAMEORIGIN')

    const frameable = read('nginx-headers-frameable.conf')
    expect(frameable).toContain('X-Frame-Options "SAMEORIGIN"')
    expect(frameable).toContain("frame-ancestors 'self'")
    expect(frameable).not.toContain('DENY')
  })

  it('only the desk catch all location is frameable; the door, the shell and the portal are not', () => {
    const conf = read('nginx.conf')
    const blocks = locationBlocks(conf)
    const at = (path: string) => blocks.find((b) => b.header === path)

    const frameable = blocks.filter((b) => b.body.includes('gable-headers-frameable.conf'))
    expect(frameable.map((b) => b.header)).toEqual(['/'])

    for (const path of ['= /', '/door/', '/shell/', '/field/', '^~ /portal/', '= /portal']) {
      const block = at(path)
      expect(block, `a location block for ${path}`).toBeDefined()
      expect(block?.body, `${path} includes the deny headers`).toContain('gable-headers.conf')
      expect(block?.body, `${path} is not frameable`).not.toContain('gable-headers-frameable.conf')
    }

    // The portal blocks serve the desk's index.html IN PLACE: a bare
    // `/index.html` last argument to try_files is an internal redirect, which
    // re-matches the desk's catch all and would hand portal pages the
    // frameable headers. The `=404` tail keeps the answer inside the block.
    const portalPrefix = at('^~ /portal/')
    expect(portalPrefix?.body).toContain('try_files $uri /index.html =404;')
    const portalExact = at('= /portal')
    expect(portalExact?.body).toContain('try_files /index.html =404;')

    // The desk's hashed assets ride the default deny headers too (framing a
    // script is meaningless, and the default must never silently widen).
    expect(at('/assets/')?.body).not.toContain('gable-headers-frameable.conf')
  })
})
