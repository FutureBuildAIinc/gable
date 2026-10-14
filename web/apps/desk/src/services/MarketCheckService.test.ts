// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** The run outlives the default 10 second fetch timeout (a cold FB Connect run takes longer), and an abort reads in words. */
import { describe, it, expect, beforeEach, vi } from 'vitest'

const fetchWithAuth = vi.fn()
vi.mock('./fetchClient', () => ({ fetchWithAuth: (...a: unknown[]) => fetchWithAuth(...a) }))

import { marketCheckService, RUN_TIMEOUT_MS, TOO_SLOW_WORDS } from './MarketCheckService'

beforeEach(() => { fetchWithAuth.mockClear() })

async function failure(): Promise<string> {
  try {
    await marketCheckService.run(['p-1'])
  } catch (e) {
    return (e as Error).message
  }
  return ''
}

describe('marketCheckService.run', () => {
  it('passes a timeout above the server worst case (batches of up to 30 seconds)', async () => {
    fetchWithAuth.mockResolvedValue(new Response(JSON.stringify({ rows: [] }), { status: 200 }))
    await marketCheckService.run(['p-1'])
    const init = fetchWithAuth.mock.calls[0][1] as { timeout?: number }
    expect(init.timeout).toBe(RUN_TIMEOUT_MS)
    expect(RUN_TIMEOUT_MS).toBeGreaterThanOrEqual(90_000)
  })

  it.each([
    ['AbortError', 'signal is aborted without reason'],
    ['TimeoutError', 'The operation timed out'],
  ])('reads a %s in words', async (name, message) => {
    const err = new Error(message)
    err.name = name
    fetchWithAuth.mockImplementation(() => { throw err })
    expect(await failure()).toBe(TOO_SLOW_WORDS)
    expect(TOO_SLOW_WORDS).toBe('FB Connect took too long to answer; try again')
  })

  it('leaves another failure as it is', async () => {
    fetchWithAuth.mockImplementation(() => { throw new TypeError("Failed to fetch") })
    expect(await failure()).toBe('Failed to fetch')
  })
})
