// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Router matching. The router is a first-match-wins table walker, so pattern
 * matching, param extraction and unknown-route behavior decide what every page
 * in the app renders.
 */
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { authCustody, authConfig } from '@gable/auth'
import { router, rolesAllow, heldRoles, routeRefused, refusalWords, ADMIN_OWNER, ADMIN_OWNER_FINANCE, type RouteConfig, type RouteMatch } from './router'

const noop = async () => {}

const table: RouteConfig[] = [
  { path: '/', load: noop, layout: 'erp' },
  { path: '/orders/:id', load: noop, layout: 'erp' },
  { path: '/orders', load: noop, layout: 'erp' },
  { path: '/quotes/analytics', load: noop, layout: 'erp' },
  { path: '/quotes/:id/edit', load: noop, layout: 'erp' },
  { path: '/quotes/:id', load: noop, layout: 'erp' },
  { path: '/portal/catalog/:id', load: noop, layout: 'portal' },
  { path: '/sales', load: noop, layout: 'erp', redirect: '/quotes' },
  { path: '/quotes', load: noop, layout: 'erp' },
]

/** Put the browser at `path`, then resolve `table` against it. */
function resolveAt(path: string): RouteMatch | null {
  history.replaceState(null, '', path)
  router.init(table)
  return router.currentMatch
}

beforeEach(() => {
  history.replaceState(null, '', '/')
})

describe('router — path matching', () => {
  it('matches the root route exactly', () => {
    expect(resolveAt('/')?.route.path).toBe('/')
  })

  it('does not let the root pattern swallow every other path', () => {
    expect(resolveAt('/orders')?.route.path).toBe('/orders')
  })

  it('matches a static route and reports its layout', () => {
    const match = resolveAt('/quotes')
    expect(match?.route.path).toBe('/quotes')
    expect(match?.route.layout).toBe('erp')
    expect(match?.params).toEqual({})
  })

  it('carries the portal layout for portal routes', () => {
    expect(resolveAt('/portal/catalog/abc')?.route.layout).toBe('portal')
  })

  it('rejects a path with more segments than the pattern', () => {
    expect(resolveAt('/orders/123/lines')).toBeNull()
  })

  it('rejects a path with fewer segments than the pattern', () => {
    expect(resolveAt('/portal/catalog')).toBeNull()
  })

  it('ignores trailing slashes rather than 404-ing', () => {
    // Empty segments are filtered out, so /orders/ is still /orders.
    expect(resolveAt('/orders/')?.route.path).toBe('/orders')
  })

  it('returns null for an unknown route', () => {
    expect(resolveAt('/does-not-exist')).toBeNull()
  })

  it('prefers an earlier static pattern over a later dynamic one', () => {
    // /quotes/analytics is listed before /quotes/:id, so it must win.
    expect(resolveAt('/quotes/analytics')?.route.path).toBe('/quotes/analytics')
  })
})

describe('router — params', () => {
  it('extracts a single param', () => {
    expect(resolveAt('/orders/ord-42')?.params).toEqual({ id: 'ord-42' })
  })

  it('extracts a param from a multi-segment pattern', () => {
    const match = resolveAt('/quotes/q-7/edit')
    expect(match?.route.path).toBe('/quotes/:id/edit')
    expect(match?.params).toEqual({ id: 'q-7' })
  })

  it('percent-decodes param values', () => {
    // Job names and SKUs routinely contain spaces and slashes.
    expect(resolveAt('/orders/' + encodeURIComponent('2x4 SPF/#2'))?.params.id)
      .toBe('2x4 SPF/#2')
  })

  it('accepts a UUID param', () => {
    const uuid = '3f2504e0-4f89-11d3-9a0c-0305e82c3301'
    expect(resolveAt(`/orders/${uuid}`)?.params).toEqual({ id: uuid })
  })
})

describe('router — redirects', () => {
  it('follows a redirect instead of matching the redirecting route', () => {
    const match = resolveAt('/sales')
    expect(match?.route.path).toBe('/quotes')
    expect(window.location.pathname).toBe('/quotes')
  })
})

describe('router — navigation', () => {
  it('pushes history and re-resolves on navigate()', () => {
    resolveAt('/orders')
    router.navigate('/quotes')
    expect(window.location.pathname).toBe('/quotes')
    expect(router.currentMatch?.route.path).toBe('/quotes')
  })

  it('is a no-op when navigating to the current path', () => {
    resolveAt('/orders')
    const listener = vi.fn()
    router.addEventListener('route-changed', listener)
    router.navigate('/orders')
    router.removeEventListener('route-changed', listener)
    expect(listener).not.toHaveBeenCalled()
  })

  it('emits route-changed with the match detail', () => {
    resolveAt('/orders')
    const detail = vi.fn()
    const listener = (e: Event) => detail((e as CustomEvent<RouteMatch | null>).detail)
    router.addEventListener('route-changed', listener)
    router.navigate('/orders/ord-9')
    router.removeEventListener('route-changed', listener)

    expect(detail).toHaveBeenCalledTimes(1)
    expect(detail.mock.calls[0][0]).toMatchObject({
      route: { path: '/orders/:id' },
      params: { id: 'ord-9' },
    })
  })

  it('emits route-changed with a null detail for an unknown path', () => {
    resolveAt('/orders')
    const detail = vi.fn()
    const listener = (e: Event) => detail((e as CustomEvent<RouteMatch | null>).detail)
    router.addEventListener('route-changed', listener)
    router.navigate('/nope')
    router.removeEventListener('route-changed', listener)

    expect(detail).toHaveBeenCalledWith(null)
    expect(router.currentMatch).toBeNull()
  })

  it('replace() swaps the current entry and re-resolves', () => {
    resolveAt('/orders')
    router.replace('/quotes')
    expect(router.currentPath).toBe('/quotes')
    expect(router.currentMatch?.route.path).toBe('/quotes')
  })
})

describe('router: role gate', () => {
  it('lets every role into a route that names none', () => {
    expect(rolesAllow(undefined, ['cashier'])).toBe(true)
    expect(rolesAllow([], ['cashier'])).toBe(true)
  })

  it('refuses a person whose roles are not on the route', () => {
    expect(rolesAllow(ADMIN_OWNER, ['cashier'])).toBe(false)
    expect(rolesAllow(ADMIN_OWNER, ['finance', 'sales'])).toBe(false)
  })

  it('admits a person holding any one of the roles', () => {
    expect(rolesAllow(ADMIN_OWNER, ['owner'])).toBe(true)
    expect(rolesAllow(ADMIN_OWNER, ['cashier', 'admin'])).toBe(true)
  })

  it('leaves a person who names no role to the server', () => {
    expect(rolesAllow(ADMIN_OWNER, [])).toBe(true)
  })

  it('reads the roles of a staff session and a dev persona', () => {
    authCustody.signInStaff({ sub: 'u1', roles: ['cashier'] })
    expect(heldRoles()).toEqual(['cashier'])
    expect(routeRefused({ path: '/x', load: noop, layout: 'erp', roles: ADMIN_OWNER })).toBe(true)
    authConfig.devMode = true
    authCustody.signInDev('Jordan Pike', { role: 'cashier' })
    expect(heldRoles()).toEqual(['cashier'])
    expect(routeRefused({ path: '/x', load: noop, layout: 'erp', roles: ADMIN_OWNER })).toBe(true)
    authCustody.signInDev('Owner', { role: 'owner' })
    expect(routeRefused({ path: '/x', load: noop, layout: 'erp', roles: ADMIN_OWNER })).toBe(false)
    authCustody.signOut()
    authConfig.devMode = false
  })

  it('words the refusal from the roles', () => {
    expect(refusalWords(ADMIN_OWNER)).toBe('This page is for admin and owner')
    expect(refusalWords(['admin', 'owner', 'finance'])).toBe('This page is for admin, owner and finance')
  })

  it('gates the counter rules page to admin and owner', async () => {
    const { routes } = await import('../routes')
    expect(routes.find((r) => r.path === '/admin/counter-rules')?.roles).toEqual(ADMIN_OWNER)
  })

  it('lets finance read the offline selling page and refuses a cashier', async () => {
    const { routes } = await import('../routes')
    const roles = routes.find((r) => r.path === '/admin/offline-counter')?.roles
    expect(roles).toEqual(ADMIN_OWNER_FINANCE)
    expect(rolesAllow(roles, ['finance'])).toBe(true)
    expect(rolesAllow(roles, ['cashier'])).toBe(false)
    expect(refusalWords(roles ?? [])).toBe('This page is for admin, owner and finance')
  })
})
