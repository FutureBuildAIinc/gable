// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest'
import { walkCursor, WALK_CURSOR_CAP } from './cursorWalk'

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

  it('stops at the cap when the server keeps offering a cursor', async () => {
    let reads = 0
    const all = await walkCursor<number>(async () => { reads++; return { items: Array.from({ length: 50 }, (_, i) => i), next_cursor: 'more' } })
    expect(all).toHaveLength(WALK_CURSOR_CAP)
    expect(reads).toBe(WALK_CURSOR_CAP / 50)
  })
})
