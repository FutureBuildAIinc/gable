// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { ApiError, createClient } from '@gable/api-client'
import { fetchWithAuth } from '@gable/auth'
import { browserOffline, OfflineReadStore } from './offline.ts'

const API_URL = import.meta.env.VITE_API_URL || ''

function authFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = typeof input === 'string' ? input : input instanceof Request ? input.url : input.toString()
  return fetchWithAuth(url, { retries: 0, ...init })
}

export const portalClient = createClient({ baseUrl: API_URL, fetch: authFetch })
export const readStore = new OfflineReadStore()

export interface CachedRead<T> {
  data: T
  fromCache: boolean
  offline: boolean
}

interface CachedReadOptions {
  feed?: boolean
  query?: Record<string, string | undefined>
}

export class OfflineUnavailableError extends Error {
  constructor() {
    super('This read is not on your device yet. Reconnect to load it.')
    this.name = 'OfflineUnavailableError'
  }
}

function pathWithQuery(path: string, query: Record<string, string | undefined>): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== '') params.set(key, value)
  }
  const suffix = params.toString()
  return suffix === '' ? path : `${path}?${suffix}`
}

export async function readCached<T>(key: string, path: string, options: CachedReadOptions = {}): Promise<CachedRead<T>> {
  const cached = readStore.read<T>(key)
  const query = { ...options.query }
  if (options.feed === true && cached?.latestChange) query['since'] = cached.latestChange
  const headers = new Headers()
  if (cached?.etag) headers.set('If-None-Match', cached.etag)

  try {
    const response = await authFetch(`${API_URL}${pathWithQuery(path, query)}`, { headers })
    if (response.status === 304 && cached !== null) {
      return { data: cached.value, fromCache: true, offline: false }
    }
    if (!response.ok) {
      const body = await response.text()
      throw new ApiError(response.status, body, response.headers.get('X-Request-ID'))
    }
    const data = await response.json() as T
    readStore.write(key, {
      value: data,
      etag: response.headers.get('ETag'),
      latestChange: response.headers.get('X-Portal-Latest-Change'),
    })
    return { data, fromCache: false, offline: false }
  } catch (error) {
    if (cached !== null && !(error instanceof ApiError) && !(error instanceof Error && error.message === 'Session expired')) {
      window.dispatchEvent(new CustomEvent('gable:pro-offline-read'))
      return { data: cached.value, fromCache: true, offline: true }
    }
    if (cached === null && browserOffline()) throw new OfflineUnavailableError()
    throw error
  }
}

export function assertWritesOnline(): void {
  if (browserOffline()) throw new OfflineUnavailableError()
}
