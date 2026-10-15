// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * No end to end spec sends a request to a third party address (item 130,
 * round 1): a base URL or link a spec types must be loopback or a reserved
 * example domain, so CI never reaches a provider on the network.
 */
import { describe, it, expect } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

// The desk workspace is the working directory of its tests: the specs are two folders up.
const E2E = resolve(process.cwd(), '../../e2e')
const ALLOWED = /^(127\.0\.0\.1|localhost|host\.docker\.internal|www\.w3\.org|([a-z0-9-]+\.)*example(\.com|\.org|\.net)?|[a-z0-9.-]*\.example|[a-z0-9.-]*\.invalid)$/i

describe('end to end specs', () => {
  it('name no third party host', () => {
    const offenders: string[] = []
    for (const name of readdirSync(E2E).filter((f) => f.endsWith('.ts'))) {
      const body = readFileSync(join(E2E, name), 'utf8')
      for (const m of body.matchAll(/https?:\/\/([a-z0-9.-]+)/gi)) {
        if (!ALLOWED.test(m[1])) offenders.push(`${name}: ${m[1]}`)
      }
    }
    expect(offenders).toEqual([])
  })
})
