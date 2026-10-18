// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

export const OFFLINE_WRITE_REASON = 'Reconnect to make changes. Nothing is queued while you are offline.'
export const OFFLINE_READ_NOTE = 'Offline, showing what you had. Writes are off until you reconnect.'

export interface CacheEntry<T> {
  value: T
  etag: string | null
  latestChange: string | null
}

interface StorageLike {
  readonly length?: number
  key?(index: number): string | null
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

export interface CacheScope {
  accountId?: string | null
  userId?: string | null
}

export function cacheScopeKey(accountId?: string | null, userId?: string | null): string {
  const account = accountId?.trim() ?? ''
  const user = userId?.trim() ?? ''
  if (account === '' && user === '') return ''
  return `${account}:${user}`
}

export class OfflineReadStore {
  private readonly memory = new Set<string>()
  private scope: string

  constructor(
    private readonly storage: StorageLike = localStorage,
    private readonly prefix = 'gable.pro.read.',
    scope: string | CacheScope = '',
  ) {
    this.scope = typeof scope === 'string' ? scope.trim() : cacheScopeKey(scope.accountId, scope.userId)
  }

  setScope(accountId?: string | null, userId?: string | null): void {
    if (userId === undefined && typeof accountId === 'string' && accountId.includes(':')) {
      this.scope = accountId.trim()
      return
    }
    this.scope = cacheScopeKey(accountId, userId)
  }

  private storageKey(key: string): string {
    return this.scope === '' ? `${this.prefix}${key}` : `${this.prefix}${this.scope}:${key}`
  }

  read<T>(key: string): CacheEntry<T> | null {
    try {
      const raw = this.storage.getItem(this.storageKey(key))
      if (raw === null) return null
      const parsed = JSON.parse(raw) as CacheEntry<T>
      return typeof parsed === 'object' && parsed !== null && 'value' in parsed ? parsed : null
    } catch {
      return null
    }
  }

  write<T>(key: string, entry: CacheEntry<T>): void {
    try {
      const fullKey = this.storageKey(key)
      this.memory.add(fullKey)
      this.storage.setItem(fullKey, JSON.stringify(entry))
    } catch {
      // A full or blocked store still leaves the live read usable.
    }
  }

  clear(): void {
    try {
      const keysToRemove = new Set<string>(this.memory)
      if (typeof this.storage.length === 'number' && typeof this.storage.key === 'function') {
        for (let i = 0; i < this.storage.length; i += 1) {
          const k = this.storage.key(i)
          if (k !== null && k.startsWith(this.prefix)) keysToRemove.add(k)
        }
      }
      for (const k of Object.keys(this.storage)) {
        if (k.startsWith(this.prefix)) keysToRemove.add(k)
      }
      for (const k of keysToRemove) {
        this.storage.removeItem(k)
      }
      this.memory.clear()
    } catch {
      // The in-memory session is already separate from the cache.
    }
  }
}

export function writesDisabled(offline: boolean): boolean {
  return offline
}

export function browserOffline(): boolean {
  return typeof navigator !== 'undefined' && navigator.onLine === false
}
