// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Branch scoping — which yard's data the user is looking at.
 *
 * `BranchContext` picks the branch at boot and persists it; `fetchClient`
 * stamps `X-Branch-Id` onto every request from the same localStorage key
 * (deliberately, to dodge a circular import). Those two halves have to agree,
 * because the failure mode is silent and expensive: a user believing they are
 * in Kelowna while every write lands in Vernon's inventory.
 *
 * `main.ts` also depends on `init()` resolving *before* `router.init()`, so the
 * first page fetch already carries the header. These tests hold both ends of
 * that contract, including the end-to-end pass through the real fetch client.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { branchContext, type BranchChangedDetail } from './BranchContext'
import { fetchWithAuth } from './fetchClient'
import { jsonResponse } from '../test/dom'
import type { BranchSummary } from '../types/location'

const STORAGE_KEY = 'gable_current_branch_id'

function branch(id: string, code: string, isHome = false): BranchSummary {
  return { id, code, name: `${code} Yard`, active: true, is_home: isHome }
}

const KELOWNA = branch('b-kel', 'KEL')
const VERNON = branch('b-ver', 'VER', true)
const PENTICTON = branch('b-pen', 'PEN')

let fetchMock: ReturnType<typeof vi.fn>

/** Serve `/me/branches` with `list`, and 200 `{}` for anything else. */
function serve(list: BranchSummary[]) {
  fetchMock = vi.fn((url: string) =>
    Promise.resolve(
      url.includes('/me/branches') ? jsonResponse(list) : jsonResponse({}),
    ),
  )
  vi.stubGlobal('fetch', fetchMock)
}

/**
 * Re-run init from scratch. `branchContext` is a module singleton that
 * memoizes its in-flight promise, so `refresh()` is the public way to make it
 * forget and reload — which is exactly what an admin-side grant change does.
 */
function reinit(): Promise<void> {
  return branchContext.refresh()
}

beforeEach(() => {
  localStorage.clear()
  serve([])
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('BranchContext — initial selection', () => {
  it('prefers the user’s home branch over list order', async () => {
    serve([KELOWNA, VERNON, PENTICTON])
    await reinit()

    expect(branchContext.currentId).toBe('b-ver')
    expect(branchContext.current?.code).toBe('VER')
  })

  it('falls back to the first branch when none is marked home', async () => {
    serve([KELOWNA, PENTICTON])
    await reinit()

    expect(branchContext.currentId).toBe('b-kel')
  })

  it('persists the chosen branch so the next request carries it', async () => {
    serve([KELOWNA, VERNON])
    await reinit()

    expect(localStorage.getItem(STORAGE_KEY)).toBe('b-ver')
  })

  it('restores a previously selected branch that is still granted', async () => {
    localStorage.setItem(STORAGE_KEY, 'b-pen')
    serve([KELOWNA, VERNON, PENTICTON])
    await reinit()

    // The stored choice wins over the home branch — the user picked it.
    expect(branchContext.currentId).toBe('b-pen')
  })

  it('drops a stored branch the user no longer has access to', async () => {
    // Access revoked between sessions: silently keeping the id would stamp
    // X-Branch-Id with a branch the server will reject on every call.
    localStorage.setItem(STORAGE_KEY, 'b-pen')
    serve([KELOWNA, VERNON])
    await reinit()

    expect(branchContext.currentId).toBe('b-ver')
    expect(localStorage.getItem(STORAGE_KEY)).toBe('b-ver')
  })

  it('selects nothing, and stores nothing, for a user with no branches', async () => {
    localStorage.setItem(STORAGE_KEY, 'b-stale')
    serve([])
    await reinit()

    expect(branchContext.branches).toEqual([])
    expect(branchContext.currentId).toBeNull()
    expect(branchContext.current).toBeNull()
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull()
  })

  it('degrades to no branches when the grants call fails', async () => {
    // Pre-auth or a backend blip must not stop the app booting; pages surface
    // their own errors.
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse({}, 500))))
    await expect(reinit()).resolves.toBeUndefined()

    expect(branchContext.branches).toEqual([])
    expect(branchContext.currentId).toBeNull()
    expect(branchContext.initialized).toBe(true)
  })

  it('reports itself initialized so callers can stop waiting', async () => {
    serve([KELOWNA])
    await reinit()
    expect(branchContext.initialized).toBe(true)
  })

  it('shares one in-flight request between concurrent init() callers', async () => {
    serve([KELOWNA, VERNON])
    await reinit()
    const before = fetchMock.mock.calls.length

    await Promise.all([branchContext.init(), branchContext.init(), branchContext.init()])

    expect(fetchMock.mock.calls.length).toBe(before)
  })
})

describe('BranchContext — switching', () => {
  beforeEach(async () => {
    serve([KELOWNA, VERNON, PENTICTON])
    await reinit()
  })

  it('persists the new selection', () => {
    branchContext.setCurrent('b-kel')

    expect(branchContext.currentId).toBe('b-kel')
    expect(branchContext.current?.code).toBe('KEL')
    expect(localStorage.getItem(STORAGE_KEY)).toBe('b-kel')
  })

  it('announces the switch on itself, with the resolved branch', () => {
    const seen: BranchChangedDetail[] = []
    const onChange = (e: Event) => seen.push((e as CustomEvent<BranchChangedDetail>).detail)
    branchContext.addEventListener('branch-changed', onChange)

    branchContext.setCurrent('b-kel')
    branchContext.removeEventListener('branch-changed', onChange)

    expect(seen).toHaveLength(1)
    expect(seen[0].branchId).toBe('b-kel')
    expect(seen[0].branch?.code).toBe('KEL')
  })

  it('announces the switch on window so pages can re-fetch', () => {
    // `lib/branch-listener.ts` subscribes to this global event; without it a
    // page keeps showing the previous branch's rows under the new header.
    const seen: BranchChangedDetail[] = []
    const onChange = (e: Event) => seen.push((e as CustomEvent<BranchChangedDetail>).detail)
    window.addEventListener('gable:branch-changed', onChange)

    branchContext.setCurrent('b-pen')
    window.removeEventListener('gable:branch-changed', onChange)

    expect(seen.map((d) => d.branchId)).toEqual(['b-pen'])
  })

  it('clears the stored branch when switching to "all branches"', () => {
    branchContext.setCurrent(null)

    expect(branchContext.currentId).toBeNull()
    expect(branchContext.current).toBeNull()
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull()
  })

  it('still announces a re-selection of the same branch', () => {
    // Listeners choose how to react; suppressing the event here would break a
    // deliberate "reload this branch" action.
    const seen: string[] = []
    const onChange = (e: Event) =>
      seen.push(String((e as CustomEvent<BranchChangedDetail>).detail.branchId))
    branchContext.addEventListener('branch-changed', onChange)

    branchContext.setCurrent('b-ver')
    branchContext.setCurrent('b-ver')
    branchContext.removeEventListener('branch-changed', onChange)

    expect(seen).toEqual(['b-ver', 'b-ver'])
  })

  it('resolves current to null for an id that is not in the granted list', () => {
    branchContext.setCurrent('b-nope')
    expect(branchContext.currentId).toBe('b-nope')
    expect(branchContext.current).toBeNull()
  })
})

describe('BranchContext — refresh after a grant change', () => {
  it('picks up newly granted branches', async () => {
    serve([KELOWNA])
    await reinit()
    expect(branchContext.branches).toHaveLength(1)

    serve([KELOWNA, VERNON, PENTICTON])
    await branchContext.refresh()

    expect(branchContext.branches.map((b) => b.code)).toEqual(['KEL', 'VER', 'PEN'])
  })

  it('moves the user off a branch that was revoked under them', async () => {
    serve([KELOWNA, VERNON, PENTICTON])
    await reinit()
    branchContext.setCurrent('b-pen')

    serve([KELOWNA, VERNON])
    await branchContext.refresh()

    expect(branchContext.currentId).toBe('b-ver')
    expect(localStorage.getItem(STORAGE_KEY)).toBe('b-ver')
  })

  it('leaves a still-valid selection alone', async () => {
    serve([KELOWNA, VERNON, PENTICTON])
    await reinit()
    branchContext.setCurrent('b-kel')

    serve([KELOWNA, VERNON])
    await branchContext.refresh()

    expect(branchContext.currentId).toBe('b-kel')
  })
})

describe('BranchContext — the X-Branch-Id contract with fetchClient', () => {
  it('stamps the selected branch onto a subsequent request', async () => {
    // End-to-end through the real client: BranchContext writes localStorage,
    // fetchClient reads it. The two never call each other, so only a test
    // that crosses the seam can catch a key rename.
    serve([KELOWNA, VERNON])
    await reinit()
    branchContext.setCurrent('b-kel')

    await fetchWithAuth('/api/v1/inventory')

    const init = fetchMock.mock.calls.at(-1)![1] as RequestInit
    expect((init.headers as Headers).get('X-Branch-Id')).toBe('b-kel')
  })

  it('sends no branch header once the user selects all branches', async () => {
    serve([KELOWNA, VERNON])
    await reinit()
    branchContext.setCurrent(null)

    await fetchWithAuth('/api/v1/inventory')

    const init = fetchMock.mock.calls.at(-1)![1] as RequestInit
    expect((init.headers as Headers).has('X-Branch-Id')).toBe(false)
  })

  it('sends the branch header on the very first page fetch after init', async () => {
    // main.ts sequences `branchContext.init().finally(() => router.init(routes))`
    // for exactly this reason.
    serve([KELOWNA, VERNON])
    await reinit()

    await fetchWithAuth('/api/v1/orders')

    const init = fetchMock.mock.calls.at(-1)![1] as RequestInit
    expect((init.headers as Headers).get('X-Branch-Id')).toBe('b-ver')
  })
})
