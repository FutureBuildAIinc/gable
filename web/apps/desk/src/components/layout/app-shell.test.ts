// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The ERP shell — the chrome every desktop page lives inside.
 *
 * `<gable-app-shell>` replaced the old left sidebar with a tab strip: each
 * module the user opens becomes a tab that remembers where it was left, and
 * the active tab contributes the menu band under it. That state is persisted
 * to localStorage, so a bad key or a stale entry follows the user across
 * reloads. `lib/workspace.test.ts` covers the zone/menu pure functions; this
 * file covers the shell that renders them and the interactions that mutate
 * them.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { router } from '../../lib/router'
import { workspace } from '../../lib/workspace'
import type { GableAppShell } from './app-shell'
import './app-shell'
import { mount, update, text, q, flush } from '../../test/dom'
import { authConfig, authCustody } from '@gable/auth'

const TABLE = [
  { path: '/home', load: async () => {}, layout: 'erp' as const },
  { path: '/orders/:id', load: async () => {}, layout: 'erp' as const },
  { path: '/orders', load: async () => {}, layout: 'erp' as const },
  { path: '/invoices', load: async () => {}, layout: 'erp' as const },
  { path: '/accounting/journal-entries', load: async () => {}, layout: 'erp' as const },
  { path: '/accounting/trial-balance', load: async () => {}, layout: 'erp' as const },
]

/** Put the router on `path`, then mount the shell there. */
async function shellAt(path: string): Promise<GableAppShell> {
  history.replaceState(null, '', path)
  router.init(TABLE)
  return mount<GableAppShell>('gable-app-shell')
}

/** Labels of the open workspace tabs, in strip order. */
function tabLabels(el: GableAppShell): string[] {
  return Array.from(el.querySelectorAll('nav[aria-label="Open modules"] button[aria-label^="Switch to"]'))
    .map((b) => text(b))
}

beforeEach(() => {
  // The workspace service is a singleton shared by every test in this file.
  // Closing everything but the pinned Home tab is the only public reset.
  for (const tab of [...workspace.tabs]) workspace.close(tab.key)
  localStorage.clear()
  sessionStorage.clear()
  authCustody.signOut()
  authConfig.devMode = false
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(new Response('{}', { status: 200 }))))
})

afterEach(() => {
  vi.unstubAllGlobals()
  authCustody.signOut()
  authConfig.devMode = false
})

describe('gable-app-shell — chrome', () => {
  it('renders the header, the tab strip and the main region', async () => {
    const el = await shellAt('/orders')

    expect(el.querySelector('header')).not.toBeNull()
    expect(el.querySelector('nav[aria-label="Open modules"]')).not.toBeNull()
    expect(el.querySelector('main #main-content')).not.toBeNull()
  })

  it('renders the page content it was handed', async () => {
    const page = document.createElement('gable-order-list')
    const el = await mount<GableAppShell>('gable-app-shell', { pageContent: page })

    expect(q(el, 'main #main-content gable-order-list')).toBe(page)
  })

  it('offers a skip link to the main region for keyboard users', async () => {
    const el = await shellAt('/orders')
    const skip = q<HTMLAnchorElement>(el, 'a[href="#main-content"]')

    expect(text(skip)).toBe('Skip to main content')
    expect(el.querySelector('#main-content')).not.toBeNull()
  })

  it('labels the global search input', async () => {
    const el = await shellAt('/orders')
    expect(q<HTMLInputElement>(el, 'input[aria-label="Search everything"]')).toBeTruthy()
  })

  it('mounts the omnibar and the shortcuts modal', async () => {
    const el = await shellAt('/orders')
    expect(el.querySelector('gable-omnibar')).not.toBeNull()
    expect(el.querySelector('gable-shortcuts-modal')).not.toBeNull()
  })

  it('mounts the branch switcher in the header', async () => {
    const el = await shellAt('/orders')
    expect(q(el, 'header gable-branch-switcher')).toBeTruthy()
  })

  it('draws the tab strip and menu band scrollbars in the app\'s colours', async () => {
    // With many modules open the strips scroll; the bar must be the app's
    // thin themed one (the gable-scrollbar-x utility), in both modes, never
    // the browser's white system bar. no-scrollbar was never defined anywhere.
    const el = await shellAt('/orders')
    const strip = q(el, 'nav[aria-label="Open modules"]')
    expect(strip.className).toContain('gable-scrollbar-x')
    expect(strip.className).not.toContain('no-scrollbar')
    // /orders has a menu band; it scrolls the same way.
    const menuBand = Array.from(el.querySelectorAll('div')).find(
      (d) => d.className.includes('h-11') && d.className.includes('overflow-x-auto'),
    )
    if (!menuBand) throw new Error('no menu band on /orders')
    expect(menuBand.className).toContain('gable-scrollbar-x')
    expect(menuBand.className).not.toContain('no-scrollbar')
  })
})

describe('gable-app-shell — workspace tabs', () => {
  it('pins Home first and opens a tab for the route it mounted on', async () => {
    const el = await shellAt('/orders')
    expect(tabLabels(el)).toEqual(['Home', 'Orders'])
  })

  it('opens a second tab when the user navigates into another module', async () => {
    const el = await shellAt('/orders')

    router.navigate('/invoices')
    await flush()
    await update(el, {})

    expect(tabLabels(el)).toEqual(['Home', 'Orders', 'Invoicing'])
  })

  it('refocuses the existing tab instead of opening a duplicate', async () => {
    const el = await shellAt('/orders')
    router.navigate('/invoices')
    await flush()
    router.navigate('/orders')
    await flush()
    await update(el, {})

    expect(tabLabels(el)).toEqual(['Home', 'Orders', 'Invoicing'])
  })

  it('remembers where each module was left and returns there', async () => {
    const el = await shellAt('/orders/ord-42')
    router.navigate('/invoices')
    await flush()
    await update(el, {})

    const ordersTab = Array.from(
      el.querySelectorAll<HTMLButtonElement>('button[aria-label="Switch to Orders"]'),
    )[0]
    ordersTab.click()
    await flush()

    expect(window.location.pathname).toBe('/orders/ord-42')
  })

  it('marks the active tab for assistive tech', async () => {
    const el = await shellAt('/orders')
    const active = q(el, 'button[aria-current="page"]')
    expect(text(active)).toBe('Orders')
  })

  it('closes a tab and lands on its neighbour', async () => {
    const el = await shellAt('/orders')
    router.navigate('/invoices')
    await flush()
    await update(el, {})

    q<HTMLButtonElement>(el, 'button[aria-label="Close Invoicing"]').click()
    await flush()
    await update(el, {})

    expect(tabLabels(el)).toEqual(['Home', 'Orders'])
    expect(window.location.pathname).toBe('/orders')
  })

  it('gives the pinned Home tab no close button', async () => {
    const el = await shellAt('/orders')
    expect(el.querySelector('button[aria-label="Close Home"]')).toBeNull()
    expect(el.querySelector('button[aria-label="Close Orders"]')).not.toBeNull()
  })

  it('restores tabs from a previous session, with their paths', async () => {
    localStorage.setItem(
      'gable_workspace',
      JSON.stringify({ tabs: [{ key: 'invoice', path: '/invoices' }] }),
    )
    const el = await shellAt('/orders')

    expect(tabLabels(el)).toEqual(['Home', 'Invoicing', 'Orders'])
  })

  it('ignores a persisted tab whose zone no longer exists', async () => {
    // Only keys and paths are persisted; labels and icons re-derive from the
    // zone table, so a retired module cannot render a garbage tab.
    localStorage.setItem(
      'gable_workspace',
      JSON.stringify({ tabs: [{ key: 'retired-module', path: '/retired' }] }),
    )
    const el = await shellAt('/orders')

    expect(tabLabels(el)).toEqual(['Home', 'Orders'])
  })

  it('starts fresh from corrupt persisted state rather than throwing', async () => {
    localStorage.setItem('gable_workspace', 'not json')
    const el = await shellAt('/orders')

    expect(tabLabels(el)).toEqual(['Home', 'Orders'])
  })
})

describe('gable-app-shell — the active module menu band', () => {
  it('renders the active app menu with its own items', async () => {
    const el = await shellAt('/accounting/journal-entries')
    const links = Array.from(el.querySelectorAll('a[href^="/accounting"]')).map((a) => text(a))

    expect(links).toEqual([
      'Chart of Accounts',
      'Journal Entries',
      'Trial Balance',
      'Profit & Loss',
      'Balance Sheet',
      'Accounts Payable',
    ])
  })

  it('lights the menu item matching the current path', async () => {
    const el = await shellAt('/accounting/journal-entries')
    const active = Array.from(el.querySelectorAll('a[href^="/accounting"]')).find((a) =>
      a.className.includes('text-gable-green'),
    )

    expect(text(active ?? null)).toBe('Journal Entries')
  })

  it('moves the highlight when the user navigates inside the module', async () => {
    const el = await shellAt('/accounting/journal-entries')

    router.navigate('/accounting/trial-balance')
    await flush()
    await update(el, {})

    const active = Array.from(el.querySelectorAll('a[href^="/accounting"]')).find((a) =>
      a.className.includes('text-gable-green'),
    )
    expect(text(active ?? null)).toBe('Trial Balance')
  })

  it('keeps the parent item lit on a detail route', async () => {
    const el = await shellAt('/orders/ord-42')
    const active = Array.from(el.querySelectorAll('a[href^="/orders"]')).find((a) =>
      a.className.includes('text-gable-green'),
    )

    expect(text(active ?? null)).toBe('Orders')
  })

  it('renders no menu band on Home', async () => {
    const el = await shellAt('/home')
    expect(tabLabels(el)).toEqual(['Home'])
    expect(el.querySelector('a[href^="/accounting"]')).toBeNull()
  })
})

describe('gable-app-shell — connectivity and shortcuts', () => {
  it('shows the offline banner when the browser reports offline', async () => {
    const el = await shellAt('/orders')
    expect(text(el)).not.toContain('You are offline')

    vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(false)
    window.dispatchEvent(new Event('offline'))
    await update(el, {})

    expect(text(el)).toContain('You are offline. Some features may not be available.')
  })

  it('clears the offline banner when connectivity returns', async () => {
    vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(false)
    const el = await shellAt('/orders')
    expect(text(el)).toContain('You are offline')

    window.dispatchEvent(new Event('online'))
    await update(el, {})

    expect(text(el)).not.toContain('You are offline')
  })

  it('opens the shortcuts modal on "?"', async () => {
    const el = await shellAt('/orders')
    const modal = q(el, 'gable-shortcuts-modal') as HTMLElement & { open: boolean }
    expect(modal.open).toBe(false)

    window.dispatchEvent(new KeyboardEvent('keydown', { key: '?' }))
    await update(el, {})

    expect((q(el, 'gable-shortcuts-modal') as HTMLElement & { open: boolean }).open).toBe(true)
  })

  it('leaves "?" alone while the user is typing in a field', async () => {
    const el = await shellAt('/orders')
    const input = q<HTMLInputElement>(el, 'input[aria-label="Search everything"]')
    input.dispatchEvent(new KeyboardEvent('keydown', { key: '?', bubbles: true }))
    await update(el, {})

    expect((q(el, 'gable-shortcuts-modal') as HTMLElement & { open: boolean }).open).toBe(false)
  })

  it('stops listening for shortcuts once removed from the document', async () => {
    const el = await shellAt('/orders')
    el.remove()

    // No listener, no state change, and crucially no error from a detached
    // element trying to re-render.
    expect(() =>
      window.dispatchEvent(new KeyboardEvent('keydown', { key: '?' })),
    ).not.toThrow()
  })
})

/**
 * The header's `_renderUser` block: a dev persona card session carries the
 * persona's name and role (so the desk names the user the way the door did);
 * a token session carries the claim's name and roles. The old fixed `??`
 * avatar was a placeholder; the change has to keep its promise of persona-
 * shaped UX end to end, including the header.
 */
describe('gable-app-shell: the header user block', () => {
  it('a dev persona session renders the persona name and role in words', async () => {
    authConfig.devMode = true
    authCustody.signInDev('Jordan Pike', { role: 'cashier', homeYardCode: 'KEL-MAIN' })
    const el = await shellAt('/orders')
    await flush()
    await update(el, {})

    expect(text(el)).toContain('Jordan Pike')
    expect(text(el)).toContain('Cashier')
  })

  it('a token session renders the claim name and the claim roles joined', async () => {
    authConfig.devMode = false
    // A structurally valid, unexpired JWT whose claims are easy to assert on.
    function b64url(s: string): string {
      return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
    }
    const token = [
      b64url(JSON.stringify({ alg: 'RS256', typ: 'JWT' })),
      b64url(JSON.stringify({
        sub: 'u-42',
        name: 'Robin Diaz',
        roles: ['finance', 'sales'],
        exp: Math.floor(Date.now() / 1000) + 3600,
      })),
      'sig',
    ].join('.')
    authCustody.signInWithToken(token)
    const el = await shellAt('/orders')
    await flush()
    await update(el, {})

    expect(text(el)).toContain('Robin Diaz')
    expect(text(el)).toContain('Finance')
    expect(text(el)).toContain('Sales')
  })
})

/**
 * The header's person menu (walk note: there is no obvious way to sign
 * out). The name chip opens a menu with the person's name and role and
 * Sign out; signing out clears the session custody and leaves the desk
 * for the front door. The menu is a real menu: arrows move, Escape
 * closes and returns the focus, a click outside closes.
 */
describe('gable-app-shell: the person menu', () => {
  async function signedInShell(): Promise<GableAppShell> {
    authConfig.devMode = true
    authCustody.signInDev('Jordan Pike', { role: 'cashier', homeYardCode: 'KEL-MAIN' })
    const el = await shellAt('/orders')
    await flush()
    await update(el, {})
    return el
  }

  it('the name chip is a menu button holding the name, the role and Sign out', async () => {
    const el = await signedInShell()
    const chip = q<HTMLButtonElement>(el, 'button[aria-haspopup="menu"]')
    expect(chip.getAttribute('aria-expanded')).toBe('false')

    chip.click()
    await update(el, {})

    expect(chip.getAttribute('aria-expanded')).toBe('true')
    const menu = q(el, '[role="menu"]')
    const item = q<HTMLButtonElement>(menu, '[role="menuitem"]')
    expect(text(item)).toBe('Sign out')
    // The menu owns only menu items; the name and role sit beside it in the dropdown.
    expect(Array.from(menu.children).every((c) => c.getAttribute('role') === 'menuitem')).toBe(true)
    const dropdown = q(el, '[data-user-menu]')
    expect(text(dropdown)).toContain('Jordan Pike')
    expect(text(dropdown)).toContain('Cashier')
  })

  it('Sign out clears the session and leaves the desk for the door', async () => {
    const el = await signedInShell()
    const goToDoor = vi.spyOn(el as unknown as { _goToDoor: () => void }, '_goToDoor')
      .mockImplementation(() => {})

    q<HTMLButtonElement>(el, 'button[aria-haspopup="menu"]').click()
    await update(el, {})
    q<HTMLButtonElement>(el, '[role="menuitem"]').click()
    await flush()

    expect(authCustody.session).toBeNull()
    expect(goToDoor).toHaveBeenCalled()
  })

  it('Sign out of a staff session revokes it at the core, with the CSRF header, before the door opens', async () => {
    authConfig.devMode = false
    authCustody.signInStaff({ sub: 'sub-1', roles: ['cashier'], name: 'till@example.test' })
    const order: string[] = []
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      order.push(`${init?.method ?? 'GET'} ${url} csrf=${new Headers(init?.headers).get('X-Gable-CSRF')}`)
      return new Response(null, { status: 204 })
    })
    vi.stubGlobal('fetch', fetchMock)
    const el = await shellAt('/orders')
    await flush()
    await update(el, {})
    const goToDoor = vi.spyOn(el as unknown as { _goToDoor: () => void }, '_goToDoor')
      .mockImplementation(() => { order.push('door') })

    q<HTMLButtonElement>(el, 'button[aria-haspopup="menu"]').click()
    await update(el, {})
    q<HTMLButtonElement>(el, '[role="menuitem"]').click()
    await flush()

    const out = order.indexOf('POST /api/v1/auth/signout csrf=1')
    expect(out).toBeGreaterThanOrEqual(0)
    expect(order.indexOf('door')).toBeGreaterThan(out)
    expect(authCustody.session).toBeNull()
    expect(goToDoor).toHaveBeenCalled()
    vi.unstubAllGlobals()
  })

  it('Escape closes the menu and returns the focus to the chip', async () => {
    const el = await signedInShell()
    const chip = q<HTMLButtonElement>(el, 'button[aria-haspopup="menu"]')
    chip.click()
    await update(el, {})

    q(el, '[role="menu"]').dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await update(el, {})
    await flush()

    expect(el.querySelector('[role="menu"]')).toBeNull()
    expect(document.activeElement).toBe(chip)
  })

  it('a click outside closes the menu', async () => {
    const el = await signedInShell()
    q<HTMLButtonElement>(el, 'button[aria-haspopup="menu"]').click()
    await update(el, {})
    expect(el.querySelector('[role="menu"]')).not.toBeNull()

    document.body.click()
    await update(el, {})

    expect(el.querySelector('[role="menu"]')).toBeNull()
  })

  it('ArrowDown on the chip opens the menu and moves to Sign out', async () => {
    const el = await signedInShell()
    const chip = q<HTMLButtonElement>(el, 'button[aria-haspopup="menu"]')
    chip.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    await update(el, {})
    await flush()

    const item = q<HTMLButtonElement>(el, '[role="menuitem"]')
    expect(document.activeElement).toBe(item)
  })

  it('opening the menu by click (or Enter or Space, which fire click) moves the focus to Sign out', async () => {
    const el = await signedInShell()
    const chip = q<HTMLButtonElement>(el, 'button[aria-haspopup="menu"]')
    chip.click()
    await update(el, {})
    await flush()

    const item = q<HTMLButtonElement>(el, '[role="menuitem"]')
    expect(document.activeElement).toBe(item)
  })

  it('Escape on the chip closes the menu and keeps the focus on the chip', async () => {
    const el = await signedInShell()
    const chip = q<HTMLButtonElement>(el, 'button[aria-haspopup="menu"]')
    chip.click()
    await update(el, {})
    await flush()

    chip.focus()
    chip.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await update(el, {})

    expect(el.querySelector('[role="menu"]')).toBeNull()
    expect(document.activeElement).toBe(chip)
  })

  it('the menu closes when the focus leaves it', async () => {
    const el = await signedInShell()
    const chip = q<HTMLButtonElement>(el, 'button[aria-haspopup="menu"]')
    chip.click()
    await update(el, {})
    await flush()

    const item = q<HTMLButtonElement>(el, '[role="menuitem"]')
    item.dispatchEvent(new FocusEvent('focusout', { relatedTarget: document.body, bubbles: true }))
    await update(el, {})

    expect(el.querySelector('[role="menu"]')).toBeNull()
  })

  it('ArrowUp on the chip opens the menu on its last item', async () => {
    const el = await signedInShell()
    const chip = q<HTMLButtonElement>(el, 'button[aria-haspopup="menu"]')
    chip.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }))
    await update(el, {})
    await flush()

    const items = Array.from(el.querySelectorAll<HTMLButtonElement>('[role="menuitem"]'))
    expect(items.length).toBeGreaterThan(0)
    expect(document.activeElement).toBe(items[items.length - 1])
  })
})
