// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The CRM activity timeline on an account, and its "+ Log Activity" button.
 *
 * That button used to dispatch an `open-log-modal` event that nothing in the
 * app listened for. `LogActivityModal` — a complete, working form wired to
 * `POST /api/v1/customers/{id}/activities` — sat unimported in the tree, so
 * clicking the only way to record a call or a site visit did nothing at all and
 * threw no error. These tests hold the two halves together: the click must
 * open the modal, and submitting it must reach the endpoint and refresh the
 * feed.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './ActivityFeed'
import type { GableActivityFeed } from './ActivityFeed'
import type { Activity, Contact } from '../../types/crm'
import { mountAsync, update, flush, text, q, jsonResponse } from '../../test/dom'

const CUSTOMER = '33333333-3333-4333-8333-333333333333'

const CONTACT: Contact = {
  id: 'c-1',
  customer_id: CUSTOMER,
  first_name: 'Dana',
  last_name: 'Ramirez',
  role: 'Buyer',
  is_primary: true,
  is_active: true,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

interface Recorded {
  url: string
  method: string
  body: string | null
}

let calls: Recorded[]
let activities: Activity[]

function serve() {
  calls = []
  activities = []
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      const method = (init?.method ?? 'GET').toUpperCase()
      const body = typeof init?.body === 'string' ? init.body : null
      calls.push({ url, method, body })

      if (method === 'GET' && url.endsWith('/activities')) {
        return Promise.resolve(jsonResponse(activities))
      }
      if (method === 'GET' && url.endsWith('/contacts')) {
        return Promise.resolve(jsonResponse([CONTACT]))
      }
      if (method === 'POST' && url.endsWith('/activities')) {
        const sent = JSON.parse(body ?? '{}')
        const created: Activity = {
          id: 'a-1',
          customer_id: CUSTOMER,
          contact_id: sent.contact_id,
          activity_type: sent.activity_type,
          description: sent.description,
          activity_date: sent.activity_date,
          created_at: sent.activity_date,
          updated_at: sent.activity_date,
        }
        activities = [created]
        return Promise.resolve(jsonResponse(created, 201))
      }
      return Promise.resolve(jsonResponse([]))
    }),
  )
}

async function mountFeed(): Promise<GableActivityFeed> {
  serve()
  return mountAsync<GableActivityFeed>('gable-activity-feed', { customerId: CUSTOMER })
}

async function clickLogActivity(el: GableActivityFeed): Promise<void> {
  const btn = Array.from(el.querySelectorAll('button')).find((b) =>
    text(b).includes('Log Activity'),
  )
  if (!btn) throw new Error('no "Log Activity" button')
  btn.click()
  await flush()
  await update(el, {})
}

beforeEach(() => {
  localStorage.clear()
  document.body.innerHTML = ''
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('gable-activity-feed — Log Activity', () => {
  it('opens the log-activity modal when the button is clicked', async () => {
    const el = await mountFeed()
    expect(el.querySelector('gable-log-activity-modal')).toBeNull()

    await clickLogActivity(el)
    expect(el.querySelector('gable-log-activity-modal')).not.toBeNull()
  })

  it('hands the modal the customer and the loaded contacts', async () => {
    const el = await mountFeed()
    await clickLogActivity(el)

    const modal = q<HTMLElement>(el, 'gable-log-activity-modal')
    expect(modal.getAttribute('customer-id')).toBe(CUSTOMER)
    expect(text(modal)).toContain('Dana Ramirez')
  })

  it('posts the activity and refreshes the timeline on submit', async () => {
    const el = await mountFeed()
    await clickLogActivity(el)

    const modal = q<HTMLElement>(el, 'gable-log-activity-modal')
    const notes = q<HTMLTextAreaElement>(modal, 'textarea')
    notes.value = 'Called about the framing package'
    notes.dispatchEvent(new Event('input', { bubbles: true }))
    await flush()

    q<HTMLFormElement>(modal, 'form').dispatchEvent(
      new Event('submit', { bubbles: true, cancelable: true }),
    )
    await flush()
    await update(el, {})
    await flush()
    await update(el, {})

    const posted = calls.find((c) => c.method === 'POST' && c.url.endsWith('/activities'))
    expect(posted).toBeDefined()
    expect(JSON.parse(posted!.body ?? '{}')).toMatchObject({
      activity_type: 'CALL',
      description: 'Called about the framing package',
    })

    // The modal closes and the newly logged activity is in the timeline.
    expect(el.querySelector('gable-log-activity-modal')).toBeNull()
    expect(text(el)).toContain('Called about the framing package')
  })

  it('closes without posting when the modal is dismissed', async () => {
    const el = await mountFeed()
    await clickLogActivity(el)

    const modal = q<HTMLElement>(el, 'gable-log-activity-modal')
    const cancel = Array.from(modal.querySelectorAll('button')).find(
      (b) => text(b) === 'Cancel',
    )
    if (!cancel) throw new Error('no Cancel button')
    cancel.click()
    await flush()
    await update(el, {})

    expect(el.querySelector('gable-log-activity-modal')).toBeNull()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
  })
})
