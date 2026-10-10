// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest'
import { walkCursor, WalkTruncatedError, WALK_CURSOR_CAP, WALK_CURSOR_MAX_PAGES } from './cursorWalk'

describe('walkCursor', () => {
  it('reads every page in order and hands each cursor to the next read', async () => {
    const seen: (string | undefined)[] = []
    const pages: Record<string, { items: number[]; next_cursor: string | null }> = {
      first: { items: [1, 2], next_cursor: 'p2' },
      p2: { items: [3], next_cursor: 'p3' },
      p3: { items: [4], next_cursor: null },
    }
    const all = await walkCursor<number>(async (cursor) => { seen.push(cursor); return pages[cursor ?? 'first'] })
    expect(all).toEqual([1, 2, 3, 4])
    expect(seen).toEqual([undefined, 'p2', 'p3'])
  })

  it('stops on an empty page that carries a cursor', async () => {
    let reads = 0
    const all = await walkCursor<number>(async () => { reads++; return { items: [], next_cursor: 'same' } })
    expect(all).toEqual([])
    expect(reads).toBe(1)
  })

  it('stops on a cursor equal to the one it just sent', async () => {
    let reads = 0
    const all = await walkCursor<number>(async () => { reads++; return { items: [reads], next_cursor: 'same' } })
    expect(all).toEqual([1, 2])
    expect(reads).toBe(2)
  })

  it('throws, saying so, when a fresh cursor never ends, after the page cap', async () => {
    let reads = 0
    const walk = walkCursor<number>(async () => { reads++; return { items: [reads], next_cursor: `c${reads}` } })
    await expect(walk).rejects.toBeInstanceOf(WalkTruncatedError)
    expect(reads).toBe(WALK_CURSOR_MAX_PAGES)
  })

  it('throws at the row cap when pages are large', async () => {
    let reads = 0
    const walk = walkCursor<number>(async () => { reads++; return { items: Array.from({ length: 50 }, (_, i) => i), next_cursor: `c${reads}` } })
    await expect(walk).rejects.toThrow(/more than 2000 rows/)
    expect(reads).toBe(WALK_CURSOR_CAP / 50)
  })

  it('passes an error from a page read through, with no partial list', async () => {
    let reads = 0
    const walk = walkCursor<number>(async () => {
      reads++
      if (reads === 2) throw new Error('Failed to load')
      return { items: [1], next_cursor: 'p2' }
    })
    await expect(walk).rejects.toThrow('Failed to load')
    expect(reads).toBe(2)
  })
})
