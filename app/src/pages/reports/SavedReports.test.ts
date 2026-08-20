// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Saved Reports — the surface where an operator schedules a report for
 * recurring delivery.
 *
 * The thing worth testing here is not the form. It is the disclosure. Nothing
 * in this deployment executes a stored schedule: the backend has no working
 * scheduled-report runner and no email sender, and it says so in every schedule
 * response via `execution.enabled = false`. A UI that took the 201 at face
 * value and showed "Scheduled!" would leave a controller waiting for a report
 * that is never sent, and there is no error anywhere to discover it from.
 *
 * So: the notice must render, and the success message must not claim delivery.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './SavedReports'
import type { SavedReports } from './SavedReports'
import { mountAsync, update, flush, q, clickByText, jsonResponse } from '../../test/dom'
import { ToastService } from '../../lib/toast-service'

const REPORT_ID = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'

const SAVED_REPORTS = [
  {
    id: REPORT_ID,
    name: 'AR aging by customer',
    description: 'Open balances bucketed by age',
    entity_type: 'invoices',
    definition_json: { columns: [], filters: [], groupings: [] },
    created_at: '2026-01-01T00:00:00Z',
  },
]

const EXECUTION_DISABLED = {
  enabled: false,
  summary: 'Schedules are saved but never run: scheduled report delivery is not enabled in this deployment.',
  blockers: [
    'reporting.Scheduler is not wired into the server; nothing loads or fires schedules.',
    'No EmailSender implementation exists, so a generated report has nowhere to be delivered.',
  ],
  cron_dialect: 'Six fields, seconds first (e.g. "0 0 9 * * *").',
}

const EXECUTION_ENABLED = {
  enabled: true,
  summary: 'Schedules run on the server\'s cron engine and are emailed to their recipients.',
  cron_dialect: 'Six fields, seconds first (e.g. "0 0 9 * * *").',
}

interface Stub {
  posts: Record<string, unknown>[]
  deletes: string[]
}

function stubFetch(execution: unknown, schedules: unknown[] = []): Stub {
  const stub: Stub = { posts: [], deletes: [] }
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'

    if (url.includes('/reporting/schedules') && method === 'POST') {
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>
      stub.posts.push(body)
      return jsonResponse({ schedule: { id: 's-new', status: 'STORED', ...body }, execution }, 201)
    }
    if (url.includes('/reporting/schedules') && method === 'DELETE') {
      stub.deletes.push(url)
      return new Response(null, { status: 204 })
    }
    if (url.includes('/reporting/schedules')) {
      return jsonResponse({ schedules, execution })
    }
    if (url.includes('/reporting/saved') && url.endsWith('/run')) {
      return jsonResponse([{ customer: 'Kelbrook Homes', total: 1200 }])
    }
    if (url.includes('/reporting/saved')) {
      return jsonResponse(SAVED_REPORTS)
    }
    throw new Error(`unexpected fetch: ${method} ${url}`)
  }))
  return stub
}

async function openScheduleModal(execution: unknown, schedules: unknown[] = []) {
  const stub = stubFetch(execution, schedules)
  const el = await mountAsync<SavedReports>('gable-saved-reports')
  await clickByText(el, 'button', 'Schedule')
  return { el, stub }
}

async function fillAndSubmit(el: SavedReports, recipients = 'controller@example.com') {
  const input = q<HTMLInputElement>(el, 'input[aria-label="Recipients"]')
  input.value = recipients
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await update(el, {})

  q<HTMLFormElement>(el, 'form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await flush()
  await update(el, {})
}

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('SavedReports: schedules that do not run say so', () => {
  it('shows the execution notice and its blockers when delivery is disabled', async () => {
    const { el } = await openScheduleModal(EXECUTION_DISABLED)

    const notice = el.querySelector('[data-testid="schedule-execution-notice"]')
    expect(notice).not.toBeNull()
    expect(notice!.textContent).toContain('never run')
    // The reasons, not just a vague warning.
    expect(notice!.textContent).toContain('No EmailSender implementation exists')
  })

  it('does not claim the report will be delivered after saving a schedule', async () => {
    const toast = vi.spyOn(ToastService, 'show').mockImplementation(() => {})
    const { el, stub } = await openScheduleModal(EXECUTION_DISABLED)

    await fillAndSubmit(el)

    expect(stub.posts).toHaveLength(1)
    const message = String(toast.mock.calls.at(-1)?.[0] ?? '')
    expect(message).toContain('will not run')
    // The specific regression: a bare success message that implies delivery.
    expect(message).not.toMatch(/^Schedule created and registered/)
  })

  it('hides the notice and confirms registration when delivery IS enabled', async () => {
    const toast = vi.spyOn(ToastService, 'show').mockImplementation(() => {})
    const { el } = await openScheduleModal(EXECUTION_ENABLED)

    expect(el.querySelector('[data-testid="schedule-execution-notice"]')).toBeNull()

    await fillAndSubmit(el)
    expect(String(toast.mock.calls.at(-1)?.[0] ?? '')).toContain('registered')
  })
})

describe('SavedReports: schedule form', () => {
  it('sends the six-field preset expression for the default daily frequency', async () => {
    const { el, stub } = await openScheduleModal(EXECUTION_DISABLED)
    await fillAndSubmit(el)

    expect(stub.posts[0]).toMatchObject({
      report_id: REPORT_ID,
      cron_expression: '0 0 9 * * *',
      recipients: ['controller@example.com'],
      format: 'CSV',
    })
  })

  it('splits a comma-separated recipient list and drops the blanks', async () => {
    const { el, stub } = await openScheduleModal(EXECUTION_DISABLED)
    await fillAndSubmit(el, 'a@example.com , , b@example.com')

    expect(stub.posts[0].recipients).toEqual(['a@example.com', 'b@example.com'])
  })

  it('refuses a five-field crontab expression before it reaches the API', async () => {
    const toast = vi.spyOn(ToastService, 'show').mockImplementation(() => {})
    const { el, stub } = await openScheduleModal(EXECUTION_DISABLED)

    const freq = q<HTMLSelectElement>(el, 'select[aria-label="Frequency"]')
    freq.value = 'custom'
    freq.dispatchEvent(new Event('change', { bubbles: true }))
    await update(el, {})

    const cron = q<HTMLInputElement>(el, 'input[aria-label="Cron expression"]')
    cron.value = '0 9 * * *' // the form every crontab example uses — and the engine rejects
    cron.dispatchEvent(new Event('input', { bubbles: true }))
    await update(el, {})

    await fillAndSubmit(el)

    expect(stub.posts).toHaveLength(0)
    // The server's 4xx envelope would have said only "Bad Request", so the
    // explanation has to come from here.
    expect(String(toast.mock.calls.at(-1)?.[0] ?? '')).toContain('six fields')
  })

  it('rejects an empty recipient list without calling the API', async () => {
    const { el, stub } = await openScheduleModal(EXECUTION_DISABLED)
    await fillAndSubmit(el, '   ,  ')

    expect(stub.posts).toHaveLength(0)
  })

  it('lists only the schedules belonging to the report being edited', async () => {
    const { el } = await openScheduleModal(EXECUTION_DISABLED, [
      { id: 's1', report_id: REPORT_ID, cron_expression: '0 0 9 * * *', recipients: ['a@example.com'], status: 'STORED', format: 'CSV' },
      { id: 's2', report_id: 'some-other-report', cron_expression: '0 0 9 * * *', recipients: ['b@example.com'], status: 'STORED', format: 'PDF' },
    ])

    const list = q(el, '[data-testid="schedule-list"]')
    expect(list.querySelectorAll('li')).toHaveLength(1)
    expect(list.textContent).toContain('a@example.com')
    expect(list.textContent).not.toContain('b@example.com')
    // The stored status is shown, not hidden behind a friendly label.
    expect(list.textContent).toContain('STORED')
  })
})

describe('SavedReports: run', () => {
  it('actually runs a saved report and reports the row count', async () => {
    const toast = vi.spyOn(ToastService, 'show').mockImplementation(() => {})
    stubFetch(EXECUTION_DISABLED)
    const el = await mountAsync<SavedReports>('gable-saved-reports')

    await clickByText(el, 'button', 'Run')

    expect(String(toast.mock.calls.at(-1)?.[0] ?? '')).toContain('1 row')
  })
})
