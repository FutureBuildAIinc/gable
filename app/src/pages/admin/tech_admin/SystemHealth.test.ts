// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The System Health tab of Tech Admin.
 *
 * This panel used to be static markup: the words "System Health ok" and "All
 * services are running normally" were hardcoded, so it read as a green light
 * while the database was on fire. It now renders whatever
 * `GET /healthz/ready` reports. These tests hold it to that, because the
 * failure mode is silent — a panel that always says "ok" passes every smoke
 * test a human would run against a healthy box.
 *
 * Two properties matter:
 *
 *  - **A degraded backend must read as degraded.** `/healthz/ready` answers 503
 *    with `status: "degraded"` when the pool ping fails. That body is a health
 *    *report*, not a transport error, so it has to be parsed and shown rather
 *    than discarded as a failed request.
 *  - **An unreachable probe must say so.** Rendering nothing, or falling back to
 *    "ok", would be the original bug with extra steps.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './TechAdminPage'
import type { TechAdminPage } from './TechAdminPage'
import { mountAsync, update, flush, text, jsonResponse } from '../../../test/dom'

const READY_OK = {
  status: 'ok',
  uptime: '3h12m8s',
  checks: {
    database: {
      status: 'connected',
      pool_total: 4,
      pool_idle: 3,
      pool_in_use: 1,
      pool_max: 10,
    },
  },
}

const READY_DEGRADED = {
  status: 'degraded',
  uptime: '12s',
  checks: { database: { status: 'disconnected' } },
}

/** Serve /healthz/ready with `readiness`; everything else the page loads is empty. */
function serve(readiness: () => Promise<Response>) {
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/healthz/ready')) return readiness()
      return Promise.resolve(jsonResponse([]))
    }),
  )
}

/** Mount the page and switch to the System Health tab. */
async function openHealthTab(): Promise<TechAdminPage> {
  const el = await mountAsync<TechAdminPage>('gable-tech-admin')
  const tab = Array.from(el.querySelectorAll('button')).find((b) => text(b) === 'System Health')
  if (!tab) throw new Error('no System Health tab button')
  tab.click()
  await flush()
  await update(el, {})
  return el
}

beforeEach(() => {
  localStorage.clear()
  document.body.innerHTML = ''
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('Tech Admin — System Health', () => {
  it('renders the status, uptime and pool stats the probe reported', async () => {
    serve(() => Promise.resolve(jsonResponse(READY_OK)))
    const el = await openHealthTab()
    const body = text(el)

    expect(body).toContain('ok')
    expect(body).toContain('3h12m8s')
    expect(body).toContain('database')
    expect(body).toContain('connected')
    // Pool numbers come straight from the probe, not from a template.
    expect(body).toContain('10')
  })

  it('reports a degraded backend as degraded, not as ok', async () => {
    // 503 is how /healthz/ready says "the database ping failed". The old panel
    // would have shown "System Health ok" here.
    serve(() => Promise.resolve(jsonResponse(READY_DEGRADED, 503)))
    const el = await openHealthTab()
    const body = text(el)

    expect(body).toContain('degraded')
    expect(body).toContain('disconnected')
    expect(body).not.toContain('All services are running normally')
  })

  it('says the probe is unreachable rather than claiming health', async () => {
    serve(() => Promise.reject(new TypeError('Failed to fetch')))
    const el = await openHealthTab()
    const body = text(el)

    expect(body).toContain('unreachable')
    expect(body).not.toContain('All services are running normally')
  })

  it('treats a non-JSON answer as unreachable (SPA fallback served index.html)', async () => {
    // Without a proxy rule for /healthz the dev server answers the probe with
    // the SPA shell. That is not a health report and must not read as one.
    serve(() =>
      Promise.resolve(new Response('<!doctype html><html></html>', {
        status: 200,
        headers: { 'Content-Type': 'text/html' },
      })),
    )
    const el = await openHealthTab()
    expect(text(el)).toContain('unreachable')
  })

  it('re-checks on demand', async () => {
    const responses = [READY_DEGRADED, READY_OK]
    serve(() => Promise.resolve(jsonResponse(responses.shift() ?? READY_OK)))

    const el = await openHealthTab()
    expect(text(el)).toContain('degraded')

    const recheck = Array.from(el.querySelectorAll('button')).find((b) =>
      text(b).includes('Re-check'),
    )
    if (!recheck) throw new Error('no Re-check button')
    recheck.click()
    await flush()
    await update(el, {})

    expect(text(el)).toContain('ok')
  })
})

describe('Tech Admin — Integrations', () => {
  it('no longer advertises integrations that do not exist', async () => {
    // The tab used to lead with a hardcoded QuickBooks / Avalara / Zapier grid
    // whose Connect button had no handler. Only the EDI partner table, which is
    // backed by /api/v1/edi/partners, should remain.
    serve(() => Promise.resolve(jsonResponse(READY_OK)))
    const el = await mountAsync<TechAdminPage>('gable-tech-admin')
    const tab = Array.from(el.querySelectorAll('button')).find((b) => text(b) === 'Integrations')
    if (!tab) throw new Error('no Integrations tab button')
    tab.click()
    await flush()
    await update(el, {})

    const body = text(el)
    expect(body).toContain('EDI Trading Partners')
    expect(body).not.toContain('QuickBooks')
    expect(body).not.toContain('Avalara')
    expect(body).not.toContain('Zapier')
  })
})
