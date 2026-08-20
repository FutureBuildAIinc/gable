// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The installable-apps enablement gate.
 *
 * `appsService` decides whether a route renders its page or the "app disabled"
 * panel, and it is a module singleton consulted on every re-render of
 * `<gable-app>`. Three of its behaviours are easy to break and expensive when
 * broken:
 *
 *  - **Fails open.** Its own docblock: "unknown keys and unloaded state count
 *    as enabled — the backend route gate is the enforcement layer." A gate that
 *    failed *closed* would black out the whole ERP for anyone whose catalog
 *    request has not landed yet, or who is offline.
 *  - **Only announces real changes.** Listeners re-render, and pages refire
 *    their fetches on mount, so an unconditional `apps-changed` is a render loop.
 *  - **Surfaces dependency blockers.** Disabling an app other apps depend on
 *    must tell the admin which ones, not just fail.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { appsService, AppToggleError } from './AppsService'
import type { AppInfo } from '../apps/types.ts'
import { jsonResponse } from '../test/dom'

function app(key: string, enabled = true, core = false): AppInfo {
  return {
    key,
    name: key[0].toUpperCase() + key.slice(1),
    summary: `${key} module`,
    category: 'operations',
    core,
    enabled,
    depends_on: null,
  }
}

let fetchMock: ReturnType<typeof vi.fn>

/** Serve `GET /api/v1/apps` with `apps` and record every request. */
function serve(apps: AppInfo[]) {
  fetchMock = vi.fn(() => Promise.resolve(jsonResponse({ apps })))
  vi.stubGlobal('fetch', fetchMock)
}

beforeEach(() => {
  localStorage.clear()
  serve([])
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('AppsService — loading', () => {
  it('decodes the catalog from the envelope the API returns', async () => {
    serve([app('millwork'), app('governance', false)])
    const apps = await appsService.load(true)

    expect(apps.map((a) => a.key)).toEqual(['millwork', 'governance'])
    expect(appsService.loaded).toBe(true)
  })

  it('caches, so a second load makes no second request', async () => {
    serve([app('millwork')])
    await appsService.load(true)
    const after = fetchMock.mock.calls.length

    await appsService.load()
    expect(fetchMock.mock.calls.length).toBe(after)
  })

  it('shares one in-flight request between concurrent callers', async () => {
    serve([app('millwork')])
    await appsService.load(true) // settle any earlier state first
    const after = fetchMock.mock.calls.length

    await Promise.all([appsService.load(true), appsService.load(), appsService.load()])

    // One forced reload, and the two concurrent calls join it.
    expect(fetchMock.mock.calls.length).toBe(after + 1)
  })

  it('treats a missing apps key as an empty catalog rather than throwing', async () => {
    fetchMock = vi.fn(() => Promise.resolve(jsonResponse({})))
    vi.stubGlobal('fetch', fetchMock)

    await expect(appsService.load(true)).resolves.toEqual([])
  })

  it('throws on a non-2xx so the caller can decide, and stays unloaded', async () => {
    // gable-app deliberately swallows this — "offline/pre-auth: backend gate
    // still enforces" — which only works if the rejection actually surfaces.
    fetchMock = vi.fn(() => Promise.resolve(jsonResponse({}, 503)))
    vi.stubGlobal('fetch', fetchMock)

    await expect(appsService.load(true)).rejects.toThrow('Failed to load apps (503)')
  })

  it('clears its in-flight slot after a failure so a retry can proceed', async () => {
    fetchMock = vi.fn(() => Promise.resolve(jsonResponse({}, 503)))
    vi.stubGlobal('fetch', fetchMock)
    await expect(appsService.load(true)).rejects.toThrow()

    serve([app('millwork')])
    await expect(appsService.load(true)).resolves.toHaveLength(1)
  })
})

describe('AppsService — the gate fails open', () => {
  it('reports an app the catalog says is enabled', async () => {
    serve([app('millwork', true)])
    await appsService.load(true)

    expect(appsService.isEnabled('millwork')).toBe(true)
  })

  it('reports an app the catalog says is disabled', async () => {
    serve([app('millwork', false)])
    await appsService.load(true)

    expect(appsService.isEnabled('millwork')).toBe(false)
  })

  it('treats an unknown key as enabled', async () => {
    serve([app('millwork', false)])
    await appsService.load(true)

    expect(appsService.isEnabled('some-app-added-next-week')).toBe(true)
  })

  it('treats every key as enabled while the catalog is empty', async () => {
    serve([])
    await appsService.load(true)

    expect(appsService.isEnabled('millwork')).toBe(true)
    expect(appsService.apps).toEqual([])
  })
})

describe('AppsService — change announcements', () => {
  /** Collect `apps-changed` events fired while `run` executes. */
  async function changesDuring(run: () => Promise<unknown>): Promise<number> {
    let count = 0
    const onChange = () => { count += 1 }
    appsService.addEventListener('apps-changed', onChange)
    try {
      await run()
    } finally {
      appsService.removeEventListener('apps-changed', onChange)
    }
    return count
  }

  it('announces a catalog that actually changed', async () => {
    serve([app('millwork', true)])
    await appsService.load(true)

    serve([app('millwork', false)])
    expect(await changesDuring(() => appsService.load(true))).toBe(1)
  })

  it('stays silent when a reload returns identical state', async () => {
    // Listeners re-render and pages refire fetches on mount; announcing
    // unchanged state is a render loop, not a no-op.
    serve([app('millwork', true)])
    await appsService.load(true)

    expect(await changesDuring(() => appsService.load(true))).toBe(0)
  })

  it('announces a toggle', async () => {
    serve([app('millwork', true)])
    await appsService.load(true)

    fetchMock = vi.fn(() => Promise.resolve(jsonResponse({ apps: [app('millwork', false)] })))
    vi.stubGlobal('fetch', fetchMock)

    expect(await changesDuring(() => appsService.toggle('millwork', false))).toBe(1)
    expect(appsService.isEnabled('millwork')).toBe(false)
  })
})

describe('AppsService — toggling', () => {
  beforeEach(async () => {
    serve([app('millwork', true)])
    await appsService.load(true)
  })

  it('hits the enable and disable sub-resources', async () => {
    fetchMock = vi.fn(() => Promise.resolve(jsonResponse({ apps: [app('millwork', false)] })))
    vi.stubGlobal('fetch', fetchMock)

    const lastUrl = () =>
      (fetchMock.mock.calls.at(-1) as unknown as [string, RequestInit])[0]
    const lastInit = () =>
      (fetchMock.mock.calls.at(-1) as unknown as [string, RequestInit])[1]

    await appsService.toggle('millwork', false)
    expect(lastUrl()).toBe('/api/v1/apps/millwork/disable')
    expect(lastInit().method).toBe('POST')

    await appsService.toggle('millwork', true)
    expect(lastUrl()).toBe('/api/v1/apps/millwork/enable')
  })

  it('percent-encodes an app key with a path-unsafe character', async () => {
    fetchMock = vi.fn(() => Promise.resolve(jsonResponse({ apps: [] })))
    vi.stubGlobal('fetch', fetchMock)

    await appsService.toggle('a/b', false)
    const [url] = fetchMock.mock.calls.at(-1) as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/apps/a%2Fb/disable')
  })

  it('reports the blockers when a dependency prevents a disable', async () => {
    // "Failed to disable millwork" alone leaves the admin with no next step.
    fetchMock = vi.fn(() =>
      Promise.resolve(
        jsonResponse(
          {
            error: {
              message: 'cannot disable millwork: 2 app(s) depend on it',
              blockers: ['quote', 'order'],
            },
          },
          409,
        ),
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    await expect(appsService.toggle('millwork', false)).rejects.toThrow(
      'cannot disable millwork: 2 app(s) depend on it',
    )

    const err = await appsService.toggle('millwork', false).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(AppToggleError)
    expect((err as AppToggleError).blockers).toEqual(['quote', 'order'])
  })

  it('falls back to a generic message when the error body is not JSON', async () => {
    fetchMock = vi.fn(() => Promise.resolve(new Response('<html>502</html>', { status: 502 })))
    vi.stubGlobal('fetch', fetchMock)

    const err = await appsService.toggle('millwork', true).catch((e: unknown) => e)
    expect((err as Error).message).toBe('Failed to enable millwork')
    expect((err as AppToggleError).blockers).toEqual([])
  })

  it('leaves the cached catalog untouched when a toggle is rejected', async () => {
    fetchMock = vi.fn(() => Promise.resolve(jsonResponse({ error: { message: 'nope' } }, 409)))
    vi.stubGlobal('fetch', fetchMock)

    await expect(appsService.toggle('millwork', false)).rejects.toThrow()
    expect(appsService.isEnabled('millwork')).toBe(true)
  })
})
