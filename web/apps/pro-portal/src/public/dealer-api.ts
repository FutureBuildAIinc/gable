// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

const API_URL = import.meta.env.VITE_API_URL || ''
const TOKEN_SHAPE = /^[A-Za-z0-9_-]{43}$/

export type DealerQuoteStatus = 'open' | 'accepted' | 'declined' | 'expired' | 'withdrawn'

export interface DealerQuoteLine {
  description: string
  quantity: string
  uom: string
  price_uom: string
  unit_price_ten_thousandths: number
  line_total_cents: number
}

export interface DealerQuote {
  number: string
  status: DealerQuoteStatus
  dealer_name: string
  customer_name: string
  expires_at: string | null
  can_decide: boolean
  lines: DealerQuoteLine[]
  freight_cents: number
  total_cents: number
}

export class DealerApiError extends Error {
  constructor(readonly status: number) {
    super(
      status === 410 ? 'This link is no longer available. Ask your dealer for a new one.'
        : status === 429 ? 'Too many attempts. Wait and try again.'
          : status === 409 ? 'This quote has already been decided.'
            : status === 404 ? 'This quote link is not valid. Use the complete link from your dealer.'
              : 'The quote request did not go through.',
    )
    this.name = 'DealerApiError'
  }
}

export function dealerTokenFromPath(pathname: string): string | null {
  const match = /^\/dq\/([^/]+)$/.exec(pathname)
  if (match === null) return null
  try {
    const token = decodeURIComponent(match[1] ?? '')
    return TOKEN_SHAPE.test(token) ? token : null
  } catch {
    return null
  }
}

const STATUSES: DealerQuoteStatus[] = ['open', 'accepted', 'declined', 'expired', 'withdrawn']

function num(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.trunc(value) : 0
}

function str(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

/** Reads only the fields the page shows; anything else the server sent is dropped. */
export function sanitizeDealerQuote(value: unknown): DealerQuote {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) throw new DealerApiError(500)
  const root = value as Record<string, unknown>
  const status = STATUSES.find((s) => s === root['status']) ?? 'withdrawn'
  const lines = (Array.isArray(root['lines']) ? root['lines'] : []).map((item): DealerQuoteLine => {
    const line = (typeof item === 'object' && item !== null ? item : {}) as Record<string, unknown>
    return {
      description: str(line['description']),
      quantity: str(line['quantity']),
      uom: str(line['uom']),
      price_uom: str(line['price_uom']),
      unit_price_ten_thousandths: num(line['unit_price_ten_thousandths']),
      line_total_cents: num(line['line_total_cents']),
    }
  })
  return {
    number: str(root['number']),
    status,
    dealer_name: str(root['dealer_name']),
    customer_name: str(root['customer_name']),
    expires_at: typeof root['expires_at'] === 'string' ? root['expires_at'] : null,
    can_decide: root['can_decide'] === true && status === 'open',
    lines,
    freight_cents: num(root['freight_cents']),
    total_cents: num(root['total_cents']),
  }
}

async function request(token: string, suffix: string, init: RequestInit): Promise<DealerQuote> {
  if (!TOKEN_SHAPE.test(token)) throw new DealerApiError(404)
  const response = await fetch(`${API_URL}/api/public/v1/dealer-quotes/${encodeURIComponent(token)}${suffix}`, {
    ...init,
    credentials: 'omit',
    cache: 'no-store',
    referrerPolicy: 'no-referrer',
    headers: { 'Content-Type': 'application/json' },
  })
  if (!response.ok) throw new DealerApiError(response.status)
  return sanitizeDealerQuote(await response.json())
}

export function getDealerQuote(token: string): Promise<DealerQuote> {
  return request(token, '', { method: 'GET' })
}

export function acceptDealerQuote(token: string, fullName: string): Promise<DealerQuote> {
  return request(token, '/accept', { method: 'POST', body: JSON.stringify({ full_name: fullName }) })
}

export function declineDealerQuote(token: string, fullName: string, reason: string): Promise<DealerQuote> {
  return request(token, '/decline', { method: 'POST', body: JSON.stringify({ full_name: fullName, reason }) })
}
