// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The portal's half of the service layer.
 *
 * 37 of the 39 service modules go through `fetchWithAuth` and are shaped the
 * same way; `PortalService` is the interesting one. It is the only module in
 * the app that still calls bare `fetch()` (in `clearToken`, to hit the logout
 * endpoint that revokes the httpOnly cookie), and it wraps every other call in
 * a private `portalFetch` that turns a non-2xx into a thrown `Error` before any
 * page sees it.
 *
 * Both of those are load-bearing. Portal auth is a cookie the browser sends
 * automatically, so a logout that only clears localStorage leaves a working
 * session behind; and a `portalFetch` that swallowed a 403 would render an
 * empty catalog to a contractor who is simply over their credit limit.
 *
 * `fetchClient.test.ts` covers the transport (headers, retries, the 401
 * interceptor); this covers the wrapper and the endpoint surface.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { PortalService, clearToken, isAuthenticated } from './PortalService'
import { jsonResponse } from '../test/dom'

let fetchMock: ReturnType<typeof vi.fn>

function lastCall() {
  const [url, init] = fetchMock.mock.calls.at(-1) as unknown as [string, RequestInit]
  return {
    url,
    method: init?.method ?? 'GET',
    body: init?.body ? JSON.parse(init.body as string) : undefined,
  }
}

beforeEach(() => {
  localStorage.clear()
  fetchMock = vi.fn(() => Promise.resolve(jsonResponse({})))
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('portalFetch — error handling', () => {
  it('returns the decoded body on success', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(jsonResponse([{ id: 'o-1', status: 'FULFILLED', total_amount: 777.6, created_at: 'x', lines: [] }])),
    )

    await expect(PortalService.getOrders()).resolves.toHaveLength(1)
  })

  it('throws on a non-2xx instead of returning a partial result', async () => {
    // Returning `[]` here would render "no orders yet" to a contractor whose
    // session merely expired.
    fetchMock.mockImplementation(() => Promise.resolve(new Response('forbidden', { status: 403 })))

    await expect(PortalService.getOrders()).rejects.toThrow('API error: 403')
  })

  it('carries the server\'s message into the error, not just the status', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(new Response('over credit limit', { status: 409 })),
    )

    await expect(PortalService.checkout({
      delivery_method: 'DELIVERY',
      delivery_address: '2200 Sawmill Loop',
      payment_method: 'ACCOUNT',
      notes: '',
    })).rejects.toThrow(/over credit limit/)
  })

  it('still reports a failure when the error body cannot be read', async () => {
    const unreadable = new Response('', { status: 500 })
    Object.defineProperty(unreadable, 'text', {
      value: () => Promise.reject(new Error('stream closed')),
    })
    fetchMock.mockImplementation(() => Promise.resolve(unreadable))

    await expect(PortalService.getOrders()).rejects.toThrow('API error: 500')
  })

  it('lets a 401 through as the transport\'s "Session expired"', async () => {
    // The 401 interceptor lives in fetchWithAuth, not here — portalFetch must
    // not shadow it with a generic "API error: 401".
    //
    // This test takes ~2s of wall clock, which is not incidental: it is the
    // retry-delay cost of the "should not retry a 401" bug pinned in
    // fetchClient.test.ts. Every expired portal session pays it for real.
    history.replaceState(null, '', '/portal/orders')
    fetchMock.mockImplementation(() => Promise.resolve(new Response('', { status: 401 })))

    await expect(PortalService.getOrders()).rejects.toThrow('Session expired')
  })
})

describe('PortalService — endpoint surface', () => {
  it('posts credentials to the portal login', async () => {
    await PortalService.login('ada@ridgeview.test', 'hunter2')

    expect(lastCall()).toEqual({
      url: '/api/portal/v1/login',
      method: 'POST',
      body: { email: 'ada@ridgeview.test', password: 'hunter2' },
    })
  })

  it('reads dashboard, orders, invoices and deliveries from their own paths', async () => {
    await PortalService.getDashboard()
    expect(lastCall().url).toBe('/api/portal/v1/dashboard')

    await PortalService.getOrders()
    expect(lastCall().url).toBe('/api/portal/v1/orders')

    await PortalService.getInvoices()
    expect(lastCall().url).toBe('/api/portal/v1/invoices')

    await PortalService.getDeliveries()
    expect(lastCall().url).toBe('/api/portal/v1/deliveries')
  })

  it('sends a reorder as a body, not as a path segment', async () => {
    await PortalService.reorder('ord-42')

    expect(lastCall()).toEqual({
      url: '/api/portal/v1/orders/reorder',
      method: 'POST',
      body: { order_id: 'ord-42' },
    })
  })
})

describe('PortalService — catalog query string', () => {
  it('omits the query string entirely when nothing is filtered', async () => {
    await PortalService.getCatalog()
    expect(lastCall().url).toBe('/api/portal/v1/catalog')

    await PortalService.getCatalog({})
    expect(lastCall().url).toBe('/api/portal/v1/catalog')
  })

  it('sends only the filters that were supplied', async () => {
    await PortalService.getCatalog({ species: 'SPF' })
    expect(lastCall().url).toBe('/api/portal/v1/catalog?species=SPF')
  })

  it('combines several filters', async () => {
    await PortalService.getCatalog({ q: '2x6', category: 'Dimensional', grade: '2 & Btr' })
    expect(lastCall().url).toBe(
      '/api/portal/v1/catalog?q=2x6&category=Dimensional&grade=2+%26+Btr',
    )
  })

  it('drops an empty search term rather than sending q=', async () => {
    // `?q=` is a search for the empty string on some backends; absence is not.
    await PortalService.getCatalog({ q: '', species: 'SPF' })
    expect(lastCall().url).toBe('/api/portal/v1/catalog?species=SPF')
  })
})

describe('PortalService — cart', () => {
  it('adds an item with its quantity', async () => {
    await PortalService.addToCart('p-1', 24)
    expect(lastCall()).toEqual({
      url: '/api/portal/v1/cart/items',
      method: 'POST',
      body: { product_id: 'p-1', quantity: 24 },
    })
  })

  it('updates a line by item id, not product id', async () => {
    await PortalService.updateCartItem('ci-9', 12)
    expect(lastCall()).toEqual({
      url: '/api/portal/v1/cart/items/ci-9',
      method: 'PUT',
      body: { quantity: 12 },
    })
  })

  it('removes a line with DELETE and no body', async () => {
    await PortalService.removeCartItem('ci-9')
    expect(lastCall()).toEqual({
      url: '/api/portal/v1/cart/items/ci-9',
      method: 'DELETE',
      body: undefined,
    })
  })

  it('posts the whole checkout request', async () => {
    const req = {
      delivery_method: 'PICKUP' as const,
      delivery_address: '',
      payment_method: 'ACCOUNT' as const,
      notes: 'Call on arrival',
    }
    await PortalService.checkout(req)

    expect(lastCall()).toEqual({
      url: '/api/portal/v1/checkout',
      method: 'POST',
      body: req,
    })
  })
})

describe('PortalService — session', () => {
  it('reports authentication from the stored user, not from a token', async () => {
    // The JWT lives in an httpOnly cookie JS cannot read, so `portal_user` is
    // the only signal available.
    expect(isAuthenticated()).toBe(false)

    localStorage.setItem('portal_user', JSON.stringify({ id: 'u-1' }))
    expect(isAuthenticated()).toBe(true)
  })

  it('revokes the server-side cookie on logout, with credentials', async () => {
    await clearToken()

    const [url, init] = fetchMock.mock.calls.at(-1) as unknown as [string, RequestInit]
    expect(url).toBe('/api/portal/v1/logout')
    expect(init.method).toBe('POST')
    expect(init.credentials).toBe('include')
  })

  it('bypasses the auth wrapper for logout — a dead session must still log out', async () => {
    // `clearToken` deliberately calls bare `fetch`. Routing it through
    // fetchWithAuth would let the 401 interceptor hijack the logout of an
    // already-expired session and redirect mid-cleanup.
    fetchMock.mockImplementation(() => Promise.resolve(new Response('', { status: 401 })))
    localStorage.setItem('portal_token', 'legacy-jwt')

    await expect(clearToken()).resolves.toBeUndefined()
    expect(localStorage.getItem('portal_token')).toBeNull()
  })

  it('clears local state even when the logout request fails outright', async () => {
    fetchMock.mockImplementation(() => Promise.reject(new TypeError('Failed to fetch')))
    localStorage.setItem('portal_token', 'legacy-jwt')

    await expect(clearToken()).resolves.toBeUndefined()
    expect(localStorage.getItem('portal_token')).toBeNull()
  })

  it('sends exactly one logout request, with no retry', async () => {
    await clearToken()
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
