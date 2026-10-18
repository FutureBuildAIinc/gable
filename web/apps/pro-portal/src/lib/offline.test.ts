// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest'
import { OFFLINE_WRITE_REASON, OfflineReadStore, writesDisabled } from './offline.ts'

class MemoryStorage {
  private readonly values = new Map<string, string>()
  getItem(key: string) { return this.values.get(key) ?? null }
  setItem(key: string, value: string) { this.values.set(key, value) }
  removeItem(key: string) { this.values.delete(key) }
}

describe('offline reads and writes', () => {
  it('round trips a read with its change feed validators', () => {
    const store = new OfflineReadStore(new MemoryStorage())
    store.write('today', { value: { count: 3 }, etag: 'validator', latestChange: 'cursor' })
    expect(store.read<{ count: number }>('today')).toEqual({
      value: { count: 3 },
      etag: 'validator',
      latestChange: 'cursor',
    })
  })

  it('drops malformed cached data instead of inventing a read', () => {
    const storage = new MemoryStorage()
    storage.setItem('gable.pro.read.today', '{broken')
    expect(new OfflineReadStore(storage).read('today')).toBeNull()
  })

  it('disables writes offline and never describes a queue', () => {
    expect(writesDisabled(true)).toBe(true)
    expect(writesDisabled(false)).toBe(false)
    expect(OFFLINE_WRITE_REASON).toContain('Nothing is queued')
  })
})
