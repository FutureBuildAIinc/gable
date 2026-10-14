// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The Log Activity modal's payload leg (PR 156 review P2-4): an activity is
 * an event with a time. The form keeps the day's field and adds the app's own
 * time entry (hours and minutes, the current time by default, refused in words
 * when it is not a time); the wire's activity_date is the picked day at the
 * picked time, in the person's own zone.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import type { GableLogActivityModal } from './LogActivityModal'
import './LogActivityModal'
import { mount, update, q, flush } from '../../test/dom'
import { jsonResponse } from '../../test/dom'

function serveSpy() {
  const calls: Array<{ url: string; body: string }> = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url: String(url), body: String(init?.body ?? '') })
      return jsonResponse({ id: 'a-1' })
    }),
  )
  return calls
}

async function fillAndSubmit(el: GableLogActivityModal, day: string, time: string) {
  const field = q<HTMLElement & { value: string }>(el, 'gable-date-field')
  field.value = day
  await update(el, {})
  const timeBox = q<HTMLInputElement>(el, 'input[aria-label="Activity time"]')
  timeBox.value = time
  timeBox.dispatchEvent(new Event('input', { bubbles: true }))
  const desc = q<HTMLTextAreaElement>(el, 'textarea')
  desc.value = 'Called about the order'
  desc.dispatchEvent(new Event('input', { bubbles: true }))
  await update(el, {})
  q<HTMLButtonElement>(el, 'button[type="submit"]').click()
  await flush()
}

beforeEach(() => {
  localStorage.clear()
})
afterEach(() => {
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

describe('Log Activity keeps a time', () => {
  it('posts the picked day at the picked time', async () => {
    const calls = serveSpy()
    const el = await mount<GableLogActivityModal>('gable-log-activity-modal', { customerId: 'c-1' })
    await fillAndSubmit(el, '2026-10-14', '09:30')
    const post = calls.find((c) => c.url.includes('/activities'))
    expect(post).toBeTruthy()
    const body = JSON.parse(post!.body)
    expect(body.activity_date).toBe(new Date(2026, 9, 14, 9, 30).toISOString())
    expect(body.activity_date).not.toContain('T12:00')
  })

  it('defaults the time to now and refuses a non time in words', async () => {
    const el = await mount<GableLogActivityModal>('gable-log-activity-modal', { customerId: 'c-1' })
    const timeBox = q<HTMLInputElement>(el, 'input[aria-label="Activity time"]')
    expect(timeBox.value).toMatch(/^\d{2}:\d{2}$/)
    const calls = serveSpy()
    await fillAndSubmit(el, '2026-10-14', 'whenever')
    expect(calls.find((c) => c.url.includes('/activities'))).toBeUndefined()
  })
})
