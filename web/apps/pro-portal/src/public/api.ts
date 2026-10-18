// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { sanitizePublicQuote, type PublicQuote, type PublicQuoteAccept, type PublicQuoteChoices, type PublicQuoteDecline } from './public-view.ts'

const API_URL = import.meta.env.VITE_API_URL || ''
const TOKEN_SHAPE = /^[A-Za-z0-9_-]{43}$/

export class PublicApiError extends Error {
  constructor(readonly status: number) {
    super(status === 410 ? 'This link is no longer available. Ask your contractor for a new one.' : status === 429 ? 'Too many attempts. Wait and try again.' : 'The quote request did not go through.')
    this.name = 'PublicApiError'
  }
}

export function tokenFromPath(pathname: string): string | null {
  const match = /^\/q\/([^/]+)$/.exec(pathname)
  if (match === null) return null
  try {
    const token = decodeURIComponent(match[1] ?? '')
    return TOKEN_SHAPE.test(token) ? token : null
  } catch {
    return null
  }
}

function path(token: string, suffix = ''): string {
  if (!TOKEN_SHAPE.test(token)) throw new PublicApiError(404)
  return `${API_URL}/api/public/v1/customer-quotes/${encodeURIComponent(token)}${suffix}`
}

async function request<T>(url: string, init: RequestInit, allowlist: (value: unknown) => T): Promise<T> {
  const response = await fetch(url, {
    ...init,
    credentials: 'omit',
    cache: 'no-store',
    referrerPolicy: 'no-referrer',
    headers: { 'Content-Type': 'application/json', ...init.headers },
  })
  if (!response.ok) throw new PublicApiError(response.status)
  if (response.status === 202 || response.status === 204) return undefined as T
  return allowlist(await response.json())
}

export function getPublicQuote(token: string): Promise<PublicQuote> {
  return request(path(token), { method: 'GET' }, sanitizePublicQuote)
}

export function putPublicChoices(token: string, choices: PublicQuoteChoices): Promise<PublicQuote> {
  return request(path(token, '/selections'), { method: 'PUT', body: JSON.stringify(choices) }, sanitizePublicQuote)
}

export function requestPublicCode(token: string, channel: 'email' | 'sms'): Promise<void> {
  return request(path(token, '/code'), { method: 'POST', body: JSON.stringify({ channel }) }, () => undefined)
}

export function acceptPublicQuote(token: string, acceptance: PublicQuoteAccept): Promise<PublicQuote> {
  return request(path(token, '/accept'), { method: 'POST', body: JSON.stringify(acceptance) }, sanitizePublicQuote)
}

export function declinePublicQuote(token: string, decline: PublicQuoteDecline): Promise<PublicQuote> {
  return request(path(token, '/decline'), { method: 'POST', body: JSON.stringify(decline) }, sanitizePublicQuote)
}
