// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Surface selection — the seam between the router and the four layout shells.
 *
 * Gable is four products in one bundle (CLAUDE.md "App route trees"): the ERP
 * desktop, the B2B portal, the driver phone app and the yard scanner, plus a
 * chromeless POS terminal. `<gable-app>` is the only thing that decides which
 * chrome wraps a page, using `route.layout` and a hardcoded path→tag map. Get
 * that wrong and a portal customer sees the ERP's branch switcher and workspace
 * tabs, or a driver's phone gets the desktop shell.
 *
 * `routes.test.ts` proves the route *table* is ordered correctly; this file
 * proves the *rendering* of a match. The router is initialised with real paths
 * but no-op loaders so the assertions are about shell selection, not about
 * dynamically importing 25 page modules into jsdom.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import type { RouteConfig } from './lib/router'
import { router } from './lib/router'
import { appsService } from './services/AppsService'
import { SESSION_EXPIRED_EVENT } from './services/fetchClient'
import type { GableApp } from './app'
import './app'
import { mountAsync, update, q, text, flush, jsonResponse } from './test/dom'

/** Route table with the real paths the tag map keys off, and inert loaders. */
const TABLE: RouteConfig[] = [
  { path: '/orders/:id', load: async () => {}, layout: 'erp' },
  { path: '/orders', load: async () => {}, layout: 'erp' },
  { path: '/portal/orders', load: async () => {}, layout: 'portal' },
  { path: '/portal/login', load: async () => {}, layout: 'none' },
  { path: '/driver', load: async () => {}, layout: 'driver' },
  { path: '/yard/pick/:id', load: async () => {}, layout: 'yard' },
  { path: '/pos', load: async () => {}, layout: 'none' },
  { path: '/millwork/blueprint', load: async () => {}, layout: 'erp' },
  { path: '/sales', load: async () => {}, layout: 'erp', redirect: '/orders' },
]

/**
 * Mount `<gable-app>` at `path` the way `main.ts` boots it: the element goes
 * into the document first and `router.init()` runs afterwards, so the app's
 * first render comes from a `route-changed` event rather than from the
 * router's replayed `currentMatch`.
 */
async function mountAt(path: string, table: RouteConfig[] = TABLE): Promise<GableApp> {
  history.replaceState(null, '', path)
  // `router` is a module singleton, so a match left over from the previous test
  // would be replayed by connectedCallback. Resolve the new path first.
  router.init(table)
  // Settle the enablement catalog against the fetch stub this test installed,
  // so the gate below is decided rather than racing the first render.
  await appsService.load(true).catch(() => {})
  const el = await mountAsync<GableApp>('gable-app')
  // Re-resolve so the freshly mounted element gets its own `route-changed`.
  router.init(table)
  await flush()
  await update(el, {})
  return el
}

/** The apps catalog the enablement gate reads at boot. */
function appsPayload(overrides: Record<string, boolean> = {}) {
  return {
    apps: [
      { key: 'millwork', name: 'Millwork', enabled: overrides.millwork ?? true },
      { key: 'governance', name: 'Governance (RFCs)', enabled: overrides.governance ?? true },
    ],
  }
}

/** Install a fetch stub that answers the apps catalog with `overrides` applied. */
function stubApps(overrides: Record<string, boolean> = {}) {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse(appsPayload(overrides)))))
}

beforeEach(() => {
  stubApps()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('gable-app — layout shell selection', () => {
  it('wraps an ERP route in the tabbed workspace shell', async () => {
    const el = await mountAt('/orders')
    expect(el.querySelector('gable-app-shell')).not.toBeNull()
    expect(el.querySelector('gable-portal-layout')).toBeNull()
  })

  it('wraps a portal route in the portal shell, not the ERP shell', async () => {
    localStorage.setItem('portal_user', JSON.stringify({ id: 'u-1', name: 'Ada Rowe' }))
    const el = await mountAt('/portal/orders')
    expect(el.querySelector('gable-portal-layout')).not.toBeNull()
    expect(el.querySelector('gable-app-shell')).toBeNull()
  })

  it('wraps a driver route in the mobile driver shell', async () => {
    const el = await mountAt('/driver')
    expect(el.querySelector('gable-driver-layout')).not.toBeNull()
    expect(el.querySelector('gable-app-shell')).toBeNull()
  })

  it('wraps a yard route in the yard shell', async () => {
    const el = await mountAt('/yard/pick/pick-9')
    expect(el.querySelector('gable-yard-layout')).not.toBeNull()
    expect(el.querySelector('gable-app-shell')).toBeNull()
  })

  it('renders a layout:none route bare — the POS terminal owns the whole screen', async () => {
    const el = await mountAt('/pos')
    expect(el.querySelector('gable-app-shell')).toBeNull()
    expect(el.querySelector('gable-portal-layout')).toBeNull()
    expect(el.querySelector('gable-driver-layout')).toBeNull()
    expect(el.querySelector('gable-yard-layout')).toBeNull()
    expect(el.querySelector('gable-pos-terminal')).not.toBeNull()
  })

  it('keeps the portal login page outside the portal shell', async () => {
    // Wrapping it would bounce an unauthenticated visitor back to the login
    // page the shell is trying to render.
    const el = await mountAt('/portal/login')
    expect(el.querySelector('gable-portal-layout')).toBeNull()
    expect(el.querySelector('gable-portal-login')).not.toBeNull()
  })

  it('mounts the toast container on every surface', async () => {
    for (const path of ['/orders', '/driver', '/pos']) {
      const el = await mountAt(path)
      expect(el.querySelector('gable-toast-container')).not.toBeNull()
      document.body.innerHTML = ''
    }
  })
})

describe('gable-app — page element resolution', () => {
  it('maps a static ERP path to its custom element', async () => {
    const el = await mountAt('/orders')
    expect(q(el, 'gable-app-shell gable-order-list')).toBeTruthy()
  })

  it('maps a dynamic path to its detail element', async () => {
    const el = await mountAt('/orders/ord-42')
    expect(q(el, 'gable-order-detail')).toBeTruthy()
  })

  it('passes route params down as route-* attributes', async () => {
    // CLAUDE.md: "route params come in via @property({ attribute: 'route-id' })".
    const el = await mountAt('/orders/ord-42')
    expect(q(el, 'gable-order-detail').getAttribute('route-id')).toBe('ord-42')
  })

  it('percent-decodes a param before handing it to the page', async () => {
    const el = await mountAt('/orders/' + encodeURIComponent('PO 42/A'))
    expect(q(el, 'gable-order-detail').getAttribute('route-id')).toBe('PO 42/A')
  })

  it('resolves a converted app path through its manifest, not the legacy map', async () => {
    const el = await mountAt('/millwork/blueprint')
    expect(q(el, 'gable-blueprint-verifier')).toBeTruthy()
  })

  it('falls back to the not-found element for a routed path with no tag', async () => {
    const el = await mountAt('/unmapped', [
      { path: '/unmapped', load: async () => {}, layout: 'erp' },
    ])
    expect(el.querySelector('gable-not-found')).not.toBeNull()
  })

  it('renders a 404 with a way home for a path in no route at all', async () => {
    const el = await mountAt('/nope/not/here')
    expect(text(el)).toContain('404')
    expect(text(el)).toContain('Page not found')
    expect(q<HTMLAnchorElement>(el, 'a').getAttribute('href')).toBe('/')
  })

  it('depends on being mounted before router.init() to reach that 404', async () => {
    // Characterization of a real ordering constraint. `connectedCallback` only
    // replays `router.currentMatch` when it is truthy, and an unrouted path
    // leaves it null — so an app mounted *after* init never receives the
    // `route-changed(null)` that clears `_loading`, and shows the boot spinner
    // forever. `main.ts` is safe today because it inserts `<gable-app>` before
    // `branchContext.init().finally(() => router.init(routes))` resolves; this
    // test exists so anyone who reorders that boot sequence finds out here.
    history.replaceState(null, '', '/nope/not/here')
    router.init(TABLE)
    const el = await mountAsync<GableApp>('gable-app')

    expect(text(el)).toBe('Loading...')
    expect(text(el)).not.toContain('404')
  })
})

describe('gable-app — navigation', () => {
  it('swaps both the shell and the page when the surface changes', async () => {
    localStorage.setItem('portal_user', JSON.stringify({ id: 'u-1', name: 'Ada Rowe' }))
    const el = await mountAt('/orders')
    expect(el.querySelector('gable-app-shell')).not.toBeNull()

    router.navigate('/portal/orders')
    await flush()
    await el.updateComplete

    expect(el.querySelector('gable-app-shell')).toBeNull()
    expect(el.querySelector('gable-portal-layout')).not.toBeNull()
    expect(el.querySelector('gable-portal-orders')).not.toBeNull()
  })

  it('follows a redirect route to the target surface', async () => {
    const el = await mountAt('/sales')
    expect(window.location.pathname).toBe('/orders')
    expect(el.querySelector('gable-order-list')).not.toBeNull()
  })

  it('keeps the same page element instance across an unrelated re-render', async () => {
    // The page element is memoized on purpose: remounting re-runs
    // connectedCallback, which refires every data fetch on the page. An
    // 'apps-changed' event must not cost the user their in-flight page.
    const el = await mountAt('/orders/ord-42')
    const before = q(el, 'gable-order-detail')

    appsService.dispatchEvent(new CustomEvent('apps-changed'))
    await flush()
    await el.updateComplete

    expect(q(el, 'gable-order-detail')).toBe(before)
  })

  it('builds a new page element when the route params change', async () => {
    const el = await mountAt('/orders/ord-42')
    const before = q(el, 'gable-order-detail')

    router.navigate('/orders/ord-43')
    await flush()
    await el.updateComplete

    const after = q(el, 'gable-order-detail')
    expect(after).not.toBe(before)
    expect(after.getAttribute('route-id')).toBe('ord-43')
  })
})

describe('gable-app — installable-app gate', () => {
  it('renders the page normally while the catalog says the app is on', async () => {
    const el = await mountAt('/millwork/blueprint')
    expect(el.querySelector('gable-blueprint-verifier')).not.toBeNull()
    expect(el.querySelector('gable-app-disabled')).toBeNull()
  })

  it('replaces a disabled app page with the disabled panel, inside its layout', async () => {
    stubApps({ millwork: false })
    const el = await mountAt('/millwork/blueprint')

    expect(el.querySelector('gable-blueprint-verifier')).toBeNull()
    const panel = q(el, 'gable-app-shell gable-app-disabled')
    // The panel names the app so the user knows what to turn back on.
    expect(panel.getAttribute('app-name')).toBe('Millwork')
  })

  it('leaves routes owned by other apps alone when one app is off', async () => {
    stubApps({ millwork: false })
    const el = await mountAt('/orders')

    expect(el.querySelector('gable-order-list')).not.toBeNull()
    expect(el.querySelector('gable-app-disabled')).toBeNull()
  })

  it('fails open for an app the catalog never told it about', async () => {
    // Pre-auth or offline the catalog never arrives, so no key is known. The
    // backend still 404s a disabled app's API, so the UI must not black out
    // every converted app on the strength of a GET it could not make.
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse({ apps: [] }))))
    const el = await mountAt('/millwork/blueprint')

    expect(el.querySelector('gable-blueprint-verifier')).not.toBeNull()
    expect(el.querySelector('gable-app-disabled')).toBeNull()
  })
})

/**
 * The receiving half of fetchClient's 401 interceptor.
 *
 * The ERP has no `/login` route — authentication is an external identity
 * provider — so the interceptor used to hard-navigate to a path that resolves
 * to `gable-not-found`, i.e. an expired session looked like a broken link.
 * fetchClient now fires `gable:session-expired` and the shell renders a panel
 * over whatever surface is up. If this listener is ever dropped the event goes
 * nowhere and the failure is silent again, so it is asserted here.
 */
describe('gable-app — session-expired panel', () => {
  it('renders nothing until a 401 is announced', async () => {
    const el = await mountAt('/orders')
    expect(el.querySelector('gable-session-expired')).toBeNull()
  })

  it('overlays the panel on the ERP surface without unmounting the page', async () => {
    const el = await mountAt('/orders')

    window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT))
    await flush()
    await update(el, {})

    expect(el.querySelector('gable-session-expired')).not.toBeNull()
    // The shell and its page stay mounted underneath — the panel is an overlay,
    // not a replacement, so a reload returns the user where they were.
    expect(el.querySelector('gable-app-shell')).not.toBeNull()
  })

  it('stops listening once detached', async () => {
    const el = await mountAt('/orders')
    el.remove()
    await flush()

    window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT))
    await flush()

    expect(el.querySelector('gable-session-expired')).toBeNull()
  })
})
