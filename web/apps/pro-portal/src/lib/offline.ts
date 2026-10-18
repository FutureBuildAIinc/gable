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
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

export class OfflineReadStore {
  constructor(
    private readonly storage: StorageLike = localStorage,
    private readonly prefix = 'gable.pro.read.',
  ) {}

  read<T>(key: string): CacheEntry<T> | null {
    try {
      const raw = this.storage.getItem(this.prefix + key)
      if (raw === null) return null
      const parsed = JSON.parse(raw) as CacheEntry<T>
      return typeof parsed === 'object' && parsed !== null && 'value' in parsed ? parsed : null
    } catch {
      return null
    }
  }

  write<T>(key: string, entry: CacheEntry<T>): void {
    try {
      this.storage.setItem(this.prefix + key, JSON.stringify(entry))
    } catch {
      // A full or blocked store still leaves the live read usable.
    }
  }

  clear(): void {
    try {
      this.storage.removeItem(this.prefix)
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
