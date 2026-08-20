// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The header control that decides which branch the user is operating in.
 *
 * Everything downstream — inventory counts, pick queues, the till — is scoped
 * by the `X-Branch-Id` this dropdown sets. Its label is the only place the UI
 * states which yard you are in, so a stale or wrong label is a receiving error
 * waiting to happen. `BranchContext.test.ts` covers the state; this covers the
 * control.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { branchContext } from '../../services/BranchContext'
import type { GableBranchSwitcher } from './branch-switcher'
import './branch-switcher'
import { mount, update, text, q, jsonResponse } from '../../test/dom'
import type { BranchSummary } from '../../types/location'

function branch(id: string, code: string, isHome = false): BranchSummary {
  return { id, code, name: `${code} Yard`, active: true, is_home: isHome }
}

const KELOWNA = branch('b-kel', 'KEL')
const VERNON = branch('b-ver', 'VER', true)

/** Load `list` into the branch singleton, then mount the switcher. */
async function switcherWith(list: BranchSummary[]): Promise<GableBranchSwitcher> {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) =>
      Promise.resolve(url.includes('/me/branches') ? jsonResponse(list) : jsonResponse({})),
    ),
  )
  await branchContext.refresh()
  return mount<GableBranchSwitcher>('gable-branch-switcher')
}

/** Open the dropdown and return the option buttons in render order. */
async function openMenu(el: GableBranchSwitcher): Promise<HTMLButtonElement[]> {
  q<HTMLButtonElement>(el, 'button[aria-haspopup="listbox"]').click()
  await update(el, {})
  return Array.from(el.querySelectorAll<HTMLButtonElement>('[role="listbox"] button[role="option"]'))
}

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('gable-branch-switcher — visibility', () => {
  it('renders nothing for a user with no branch grants', async () => {
    // Single-branch installs and pre-auth boots both land here; showing an
    // empty dropdown would imply the user can switch somewhere.
    const el = await switcherWith([])
    // Lit leaves its own marker comment behind; what matters is that no
    // control is rendered.
    expect(el.querySelector('button')).toBeNull()
    expect(text(el)).toBe('')
  })

  it('renders the trigger once the user has branches', async () => {
    const el = await switcherWith([KELOWNA, VERNON])
    expect(el.querySelector('button[aria-haspopup="listbox"]')).not.toBeNull()
  })
})

describe('gable-branch-switcher — label', () => {
  it('names the branch the user is currently scoped to', async () => {
    const el = await switcherWith([KELOWNA, VERNON])
    // VERNON is the home branch, so BranchContext selected it.
    expect(text(q(el, 'button[aria-haspopup="listbox"]'))).toContain('VER Yard')
  })

  it('says "All Branches" when the scope is cleared', async () => {
    const el = await switcherWith([KELOWNA, VERNON])
    branchContext.setCurrent(null)
    await update(el, {})

    expect(text(q(el, 'button[aria-haspopup="listbox"]'))).toContain('All Branches')
  })

  it('falls back to the code when a branch has no name', async () => {
    const unnamed: BranchSummary = { ...KELOWNA, name: '', is_home: true }
    const el = await switcherWith([unnamed])

    expect(text(q(el, 'button[aria-haspopup="listbox"]'))).toContain('KEL')
  })
})

describe('gable-branch-switcher — the menu', () => {
  it('lists every granted branch with its code', async () => {
    const el = await switcherWith([KELOWNA, VERNON])
    const options = await openMenu(el)

    expect(options.map((o) => text(o))).toEqual(['KEL KEL Yard', 'VER VER Yard home'])
  })

  it('marks the home branch', async () => {
    const el = await switcherWith([KELOWNA, VERNON])
    const options = await openMenu(el)

    expect(text(options[1])).toContain('home')
    expect(text(options[0])).not.toContain('home')
  })

  it('marks the current branch as selected for assistive tech', async () => {
    const el = await switcherWith([KELOWNA, VERNON])
    const options = await openMenu(el)

    expect(options.map((o) => o.getAttribute('aria-selected'))).toEqual(['false', 'true'])
  })

  it('reports its expanded state on the trigger', async () => {
    const el = await switcherWith([KELOWNA, VERNON])
    const trigger = q<HTMLButtonElement>(el, 'button[aria-haspopup="listbox"]')
    expect(trigger.getAttribute('aria-expanded')).toBe('false')

    await openMenu(el)
    expect(
      q<HTMLButtonElement>(el, 'button[aria-haspopup="listbox"]').getAttribute('aria-expanded'),
    ).toBe('true')
  })

  it('closes on a click elsewhere in the page', async () => {
    const el = await switcherWith([KELOWNA, VERNON])
    await openMenu(el)

    document.body.click()
    await update(el, {})

    expect(el.querySelector('[role="listbox"]')).toBeNull()
  })

  it('stays open when the menu itself is clicked', async () => {
    const el = await switcherWith([KELOWNA, VERNON])
    await openMenu(el)

    q<HTMLElement>(el, '[role="listbox"]').click()
    await update(el, {})

    expect(el.querySelector('[role="listbox"]')).not.toBeNull()
  })
})

describe('gable-branch-switcher — switching', () => {
  it('scopes the session to the branch that was picked', async () => {
    const el = await switcherWith([KELOWNA, VERNON])
    const options = await openMenu(el)

    options[0].click()
    await update(el, {})

    expect(branchContext.currentId).toBe('b-kel')
    expect(localStorage.getItem('gable_current_branch_id')).toBe('b-kel')
  })

  it('closes the menu and relabels the trigger after a pick', async () => {
    const el = await switcherWith([KELOWNA, VERNON])
    const options = await openMenu(el)

    options[0].click()
    await update(el, {})

    expect(el.querySelector('[role="listbox"]')).toBeNull()
    expect(text(q(el, 'button[aria-haspopup="listbox"]'))).toContain('KEL Yard')
  })

  it('tells the rest of the app to re-fetch', async () => {
    const el = await switcherWith([KELOWNA, VERNON])
    const seen: string[] = []
    const onChange = (e: Event) =>
      seen.push(String((e as CustomEvent<{ branchId: string | null }>).detail.branchId))
    window.addEventListener('gable:branch-changed', onChange)

    const options = await openMenu(el)
    options[0].click()
    await update(el, {})
    window.removeEventListener('gable:branch-changed', onChange)

    expect(seen).toEqual(['b-kel'])
  })

  it('follows a branch change made somewhere else in the app', async () => {
    const el = await switcherWith([KELOWNA, VERNON])

    branchContext.setCurrent('b-kel')
    await update(el, {})

    expect(text(q(el, 'button[aria-haspopup="listbox"]'))).toContain('KEL Yard')
  })

  it('stops following once removed from the document', async () => {
    const el = await switcherWith([KELOWNA, VERNON])
    el.remove()

    expect(() => branchContext.setCurrent('b-kel')).not.toThrow()
    expect(() => document.body.click()).not.toThrow()
  })
})
