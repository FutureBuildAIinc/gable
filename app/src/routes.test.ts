// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Route-table integrity.
 *
 * routes.ts is first-match-wins and hand-ordered ("order matters: more-specific
 * paths must come before less-specific ones"). That invariant is invisible in
 * review — moving `/quotes/:id` above `/quotes/analytics` silently sends the
 * analytics page to the quote detail view. These tests resolve every literal
 * path in the real table through the real router so a bad reorder fails CI.
 */
import { describe, it, expect, beforeEach } from 'vitest'
import { routes } from './routes'
import { router } from './lib/router'
import { appRoutes, tagForPath, appKeyForPath } from './apps/registry'

const LAYOUTS = new Set(['erp', 'portal', 'driver', 'yard', 'none'])

/** Routes whose path contains no `:param` segment and that do not redirect. */
const literalRoutes = routes.filter((r) => !r.path.includes(':') && !r.redirect)

beforeEach(() => {
  history.replaceState(null, '', '/')
})

describe('route table shape', () => {
  it('is non-trivially populated', () => {
    expect(routes.length).toBeGreaterThan(40)
  })

  it('gives every route an absolute path', () => {
    const bad = routes.filter((r) => !r.path.startsWith('/'))
    expect(bad.map((r) => r.path)).toEqual([])
  })

  it('gives every route a known layout shell', () => {
    const bad = routes.filter((r) => !LAYOUTS.has(r.layout))
    expect(bad.map((r) => `${r.path} -> ${r.layout}`)).toEqual([])
  })

  it('gives every route a loader', () => {
    const bad = routes.filter((r) => typeof r.load !== 'function')
    expect(bad.map((r) => r.path)).toEqual([])
  })

  it('registers each path exactly once', () => {
    const seen = new Set<string>()
    const duplicates: string[] = []
    for (const r of routes) {
      if (seen.has(r.path)) duplicates.push(r.path)
      seen.add(r.path)
    }
    expect(duplicates).toEqual([])
  })

  it('names every dynamic segment', () => {
    const unnamed = routes.filter((r) =>
      r.path.split('/').some((seg) => seg === ':'),
    )
    expect(unnamed.map((r) => r.path)).toEqual([])
  })

  it('points every redirect at a path that exists in the table', () => {
    const paths = new Set(routes.map((r) => r.path))
    const dangling = routes
      .filter((r) => r.redirect)
      .filter((r) => !paths.has(r.redirect as string))
    expect(dangling.map((r) => `${r.path} -> ${r.redirect}`)).toEqual([])
  })
})

describe('route ordering (no route is shadowed by an earlier pattern)', () => {
  it('resolves every literal path to its own route', () => {
    router.init(routes)
    const shadowed: string[] = []

    for (const route of literalRoutes) {
      history.replaceState(null, '', route.path)
      router.init(routes)
      const matched = router.currentMatch?.route.path
      if (matched !== route.path) {
        shadowed.push(`${route.path} was matched by ${matched ?? '(no route)'}`)
      }
    }

    expect(shadowed).toEqual([])
  })

  it('checks a meaningful number of paths', () => {
    // Guards the loop above against silently degenerating into a no-op if the
    // literal/dynamic filter ever stops matching anything.
    expect(literalRoutes.length).toBeGreaterThan(30)
  })

  it('keeps /quotes/analytics ahead of /quotes/:id', () => {
    history.replaceState(null, '', '/quotes/analytics')
    router.init(routes)
    expect(router.currentMatch?.route.path).toBe('/quotes/analytics')
  })

  it('keeps /purchasing/new ahead of /purchasing/:id', () => {
    history.replaceState(null, '', '/purchasing/new')
    router.init(routes)
    expect(router.currentMatch?.route.path).toBe('/purchasing/new')
  })

  it('still resolves a real detail path through the dynamic pattern', () => {
    history.replaceState(null, '', '/purchasing/po-123')
    router.init(routes)
    expect(router.currentMatch?.route.path).toBe('/purchasing/:id')
    expect(router.currentMatch?.params).toEqual({ id: 'po-123' })
  })

  it('follows the /sales -> /quotes redirect declared in the table', () => {
    history.replaceState(null, '', '/sales')
    router.init(routes)
    expect(window.location.pathname).toBe('/quotes')
    expect(router.currentMatch?.route.path).toBe('/quotes')
  })
})

describe('converted-app registry', () => {
  it('contributes its manifest routes into the table', () => {
    const tablePaths = new Set(routes.map((r) => r.path))
    for (const r of appRoutes()) {
      expect(tablePaths.has(r.path)).toBe(true)
    }
    expect(appRoutes().length).toBeGreaterThan(0)
  })

  it('maps a manifest path to its custom-element tag', () => {
    expect(tagForPath('/millwork/configure')).toBe('gable-door-configurator')
    expect(appKeyForPath('/millwork/configure')).toBe('millwork')
  })

  it('returns null for a path no converted app owns', () => {
    expect(tagForPath('/orders')).toBeNull()
    expect(appKeyForPath('/orders')).toBeNull()
  })

  it('gives every manifest route a tag and a known layout', () => {
    for (const r of appRoutes()) {
      expect(tagForPath(r.path)).toMatch(/^gable-/)
      expect(LAYOUTS.has(r.layout)).toBe(true)
    }
  })
})

/**
 * Every declared route must resolve to a real page tag.
 *
 * `GableApp._pathToTag` falls back to `gable-not-found` for anything it does not
 * recognise, so adding a route to this table without adding it to that map ships
 * a nav item that lands on the 404 page. There is no type error, no lint error,
 * and no test failure — and unit tests that mount a page component directly keep
 * passing, so CI stays green while the page is unreachable.
 *
 * That is exactly how six shipped surfaces (at-risk quotes, the exposure report,
 * market indices, AP, balance sheet and P&L) became unreachable at once. This
 * test is the guard.
 */
describe('every route resolves to a page tag', () => {
  it('has no route that falls through to gable-not-found', async () => {
    const { GableApp } = await import('./app')
    const app = new GableApp()

    // _pathToTag is private to the component; reach it deliberately rather than
    // widening the public surface just to test this invariant.
    const pathToTag = (p: string): string =>
      (app as unknown as { _pathToTag(path: string): string })._pathToTag(p)

    const unreachable = routes
      // Parameterised paths are matched by the router before the tag lookup, and
      // redirects never render a page of their own.
      .filter((r) => !r.path.includes(':') && !r.redirect)
      .filter((r) => pathToTag(r.path) === 'gable-not-found')
      .map((r) => r.path)

    expect(unreachable).toEqual([])
  })
})
