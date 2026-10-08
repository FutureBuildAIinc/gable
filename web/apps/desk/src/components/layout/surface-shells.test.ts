// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The three non-ERP shells: portal, driver and yard.
 *
 * These are separate products sharing a bundle. The portal shell is the only
 * one that carries an auth guard and dealer branding — a contractor who is not
 * signed in must never see another dealer's logo, support address, or a page
 * of their own data behind it. The driver and yard shells are phone chrome:
 * their job is to stay out of the way and keep the bottom nav pointing at the
 * right screen.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { router } from '../../lib/router'
import type { GablePortalLayout } from './portal-layout'
import type { GableDriverLayout } from './driver-layout'
import type { GableYardLayout } from './yard-layout'
import './portal-layout'
import './driver-layout'
import './yard-layout'
import { mount, update, text, q, flush } from '../../test/dom'

const TABLE = [
  { path: '/portal/orders', load: async () => {}, layout: 'portal' as const },
  { path: '/portal/invoices', load: async () => {}, layout: 'portal' as const },
  { path: '/portal/login', load: async () => {}, layout: 'none' as const },
  { path: '/portal', load: async () => {}, layout: 'portal' as const },
  { path: '/driver', load: async () => {}, layout: 'driver' as const },
  { path: '/yard/inventory', load: async () => {}, layout: 'yard' as const },
  { path: '/yard/pick/:id', load: async () => {}, layout: 'yard' as const },
  { path: '/yard', load: async () => {}, layout: 'yard' as const },
  { path: '/', load: async () => {}, layout: 'erp' as const },
]

function at(path: string) {
  history.replaceState(null, '', path)
  router.init(TABLE)
}

/** A signed-in contractor, as PortalLogin stores them. */
function signIn(config: Record<string, unknown> | null = null) {
  localStorage.setItem(
    'portal_user',
    JSON.stringify({ id: 'u-1', name: 'Ada Rowe', email: 'ada@ridgeview.test' }),
  )
  if (config) localStorage.setItem('portal_config', JSON.stringify(config))
}

beforeEach(() => {
  localStorage.clear()
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(new Response('{}', { status: 200 }))))
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('gable-portal-layout — auth guard', () => {
  it('bounces an unauthenticated visitor to the portal login', async () => {
    at('/portal/orders')
    await mount<GablePortalLayout>('gable-portal-layout')

    expect(window.location.pathname).toBe('/portal/login')
  })

  it('leaves a signed-in contractor where they are', async () => {
    signIn()
    at('/portal/orders')
    await mount<GablePortalLayout>('gable-portal-layout')

    expect(window.location.pathname).toBe('/portal/orders')
  })

  it('treats a corrupt portal_user as signed out rather than crashing', async () => {
    localStorage.setItem('portal_user', '{not json')
    at('/portal/orders')
    await mount<GablePortalLayout>('gable-portal-layout')

    expect(window.location.pathname).toBe('/portal/login')
  })
})

describe('gable-portal-layout — chrome and branding', () => {
  it('renders the page content inside a labelled navigation shell', async () => {
    signIn()
    at('/portal/orders')
    const page = document.createElement('gable-portal-orders')
    const el = await mount<GablePortalLayout>('gable-portal-layout', { pageContent: page })

    expect(el.querySelector('nav[aria-label="Portal navigation"]')).not.toBeNull()
    expect(q(el, 'main gable-portal-orders')).toBe(page)
  })

  it('lists the contractor-facing sections', async () => {
    signIn()
    at('/portal')
    const el = await mount<GablePortalLayout>('gable-portal-layout')

    const links = Array.from(
      el.querySelectorAll<HTMLAnchorElement>('nav[aria-label="Portal navigation"] a'),
    ).map((a) => a.getAttribute('href'))

    expect(links).toEqual([
      '/portal',
      '/portal/projects',
      '/portal/orders',
      '/portal/invoices',
      '/portal/deliveries',
      '/portal/team',
    ])
  })

  it('titles the header with the section being viewed', async () => {
    signIn()
    at('/portal/invoices')
    const el = await mount<GablePortalLayout>('gable-portal-layout')

    expect(text(q(el, 'header h2'))).toBe('Invoices')
  })

  it('falls back to a generic title on a section with no nav entry', async () => {
    signIn()
    at('/portal/account')
    const el = await mount<GablePortalLayout>('gable-portal-layout')

    expect(text(q(el, 'header h2'))).toBe('Portal')
  })

  it('applies the dealer accent colour and logo from the stored config', async () => {
    signIn({
      dealer_name: 'Ridgeview Supply',
      logo_url: 'https://cdn.example.test/ridgeview.svg',
      primary_color: '#FF6600',
      support_email: 'help@ridgeview.test',
    })
    at('/portal')
    const el = await mount<GablePortalLayout>('gable-portal-layout')

    expect(q(el, 'div[style*="--portal-primary"]').getAttribute('style'))
      .toContain('--portal-primary: #FF6600')
    expect(q<HTMLImageElement>(el, 'aside img').getAttribute('src'))
      .toBe('https://cdn.example.test/ridgeview.svg')
    expect(text(el)).toContain('Support: help@ridgeview.test')
  })

  it('falls back to Gable branding when no dealer config is stored', async () => {
    signIn()
    at('/portal')
    const el = await mount<GablePortalLayout>('gable-portal-layout')

    expect(q(el, 'div[style*="--portal-primary"]').getAttribute('style'))
      .toContain('--portal-primary: #00FFA3')
    expect(el.querySelector('aside img')).toBeNull()
    expect(el.querySelector('aside gable-brand-logo')).not.toBeNull()
  })

  it('shows the signed-in contractor with initials, name and email', async () => {
    signIn()
    at('/portal')
    const el = await mount<GablePortalLayout>('gable-portal-layout')

    const body = text(el)
    expect(body).toContain('AR') // Ada Rowe
    expect(body).toContain('Ada Rowe')
    expect(body).toContain('ada@ridgeview.test')
  })

  it('marks the current section as active in the sidebar', async () => {
    signIn()
    at('/portal/invoices')
    const el = await mount<GablePortalLayout>('gable-portal-layout')

    const active = Array.from(
      el.querySelectorAll<HTMLAnchorElement>('nav[aria-label="Portal navigation"] a'),
    ).filter((a) => (a.getAttribute('style') ?? '').includes('background-color'))

    expect(active.map((a) => a.getAttribute('href'))).toEqual(['/portal/invoices'])
  })

  it('collapses and re-expands the sidebar', async () => {
    signIn()
    at('/portal')
    const el = await mount<GablePortalLayout>('gable-portal-layout')

    q<HTMLButtonElement>(el, 'button[aria-label="Collapse sidebar"]').click()
    await update(el, {})
    expect(el.querySelector('button[aria-label="Expand sidebar"]')).not.toBeNull()

    q<HTMLButtonElement>(el, 'button[aria-label="Expand sidebar"]').click()
    await update(el, {})
    expect(el.querySelector('button[aria-label="Collapse sidebar"]')).not.toBeNull()
  })
})

describe('gable-portal-layout — sign out', () => {
  it('clears every trace of the session and leaves the portal', async () => {
    signIn({ dealer_name: 'Ridgeview Supply' })
    localStorage.setItem('portal_token', 'legacy-jwt')
    at('/portal')
    const el = await mount<GablePortalLayout>('gable-portal-layout')

    q<HTMLButtonElement>(el, 'button[aria-label="Sign out"]').click()
    await flush()

    expect(localStorage.getItem('portal_user')).toBeNull()
    expect(localStorage.getItem('portal_config')).toBeNull()
    expect(localStorage.getItem('portal_token')).toBeNull()
    expect(window.location.pathname).toBe('/')
  })

  it('calls the logout endpoint so the httpOnly cookie is revoked server-side', async () => {
    // Clearing localStorage alone leaves a valid session cookie in the browser.
    const fetchMock = vi.fn(() => Promise.resolve(new Response('{}', { status: 200 })))
    vi.stubGlobal('fetch', fetchMock)
    signIn()
    at('/portal')
    const el = await mount<GablePortalLayout>('gable-portal-layout')

    q<HTMLButtonElement>(el, 'button[aria-label="Sign out"]').click()
    await flush()

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toContain('/api/portal/v1/logout')
    expect(init.method).toBe('POST')
    expect(init.credentials).toBe('include')
  })

  it('still clears the local session when the logout call fails', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))))
    signIn()
    at('/portal')
    const el = await mount<GablePortalLayout>('gable-portal-layout')

    q<HTMLButtonElement>(el, 'button[aria-label="Sign out"]').click()
    await flush()

    expect(localStorage.getItem('portal_user')).toBeNull()
  })
})

describe('gable-driver-layout', () => {
  it('renders the driver chrome around its page', async () => {
    at('/driver')
    const page = document.createElement('gable-driver-route-list')
    const el = await mount<GableDriverLayout>('gable-driver-layout', { pageContent: page })

    expect(text(q(el, 'header'))).toContain('GABLEDRIVER')
    expect(q(el, 'main gable-driver-route-list')).toBe(page)
  })

  it('needs no authentication guard of its own', async () => {
    // The driver app is reached through the ERP session; the shell must not
    // redirect, or a driver mid-route loses their stop list on a re-render.
    at('/driver')
    await mount<GableDriverLayout>('gable-driver-layout')

    expect(window.location.pathname).toBe('/driver')
  })
})

describe('gable-yard-layout', () => {
  it('renders the yard chrome around its page with a bottom tab bar', async () => {
    at('/yard')
    const page = document.createElement('gable-pick-queue')
    const el = await mount<GableYardLayout>('gable-yard-layout', { pageContent: page })

    expect(text(q(el, 'header'))).toContain('GABLEYARD')
    expect(q(el, 'main gable-pick-queue')).toBe(page)
    expect(
      Array.from(el.querySelectorAll<HTMLAnchorElement>('nav a')).map((a) =>
        a.getAttribute('href'),
      ),
    ).toEqual(['/yard', '/yard/inventory', '/yard/receiving'])
  })

  /** hrefs of the bottom-nav entries currently highlighted. */
  function activeTabs(el: GableYardLayout): (string | null)[] {
    return Array.from(el.querySelectorAll<HTMLAnchorElement>('nav a'))
      .filter((a) => a.className.includes('text-amber-400'))
      .map((a) => a.getAttribute('href'))
  }

  it('highlights Pick only on the exact /yard path', async () => {
    at('/yard')
    const el = await mount<GableYardLayout>('gable-yard-layout')
    expect(activeTabs(el)).toEqual(['/yard'])
  })

  it('does not light Pick on a nested yard screen', async () => {
    // '/yard' prefixes every other yard route, so this tab needs an exact
    // match or all three tabs light at once.
    at('/yard/inventory')
    const el = await mount<GableYardLayout>('gable-yard-layout')
    expect(activeTabs(el)).toEqual(['/yard/inventory'])
  })

  it('lights no tab on a pick detail screen', async () => {
    at('/yard/pick/pick-9')
    const el = await mount<GableYardLayout>('gable-yard-layout')
    expect(activeTabs(el)).toEqual([])
  })

  it('follows navigation between yard screens', async () => {
    at('/yard')
    const el = await mount<GableYardLayout>('gable-yard-layout')

    router.navigate('/yard/inventory')
    await flush()
    await update(el, {})

    expect(activeTabs(el)).toEqual(['/yard/inventory'])
  })
})
