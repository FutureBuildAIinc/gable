// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { GableProScheduleBoard } from './schedule-board.ts'
import { GableProOrderDetail } from './order-detail.ts'
import { deliveredTimelineFixture, meFixture, orderFixture, slotsFixture, testIds, timelineFixture } from '../test/fixtures.ts'
import type { PortalOrder, PortalOrderTimeline } from '../types.ts'

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ETag: 'schedule-v1', 'X-Portal-Latest-Change': new Date().toISOString() },
  })
}

function mockPortal(order: PortalOrder = orderFixture, timeline: PortalOrderTimeline = timelineFixture) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === 'string' ? input : input instanceof Request ? input.url : input.toString(), 'http://localhost')
    const method = init?.method ?? 'GET'
    if (url.pathname === '/api/portal/v1/orders') return json([order])
    if (url.pathname === `/api/portal/v1/orders/${testIds.order}/timeline`) return json(timeline)
    if (url.pathname === `/api/portal/v1/orders/${testIds.order}`) return json(order)
    if (url.pathname === `/api/portal/v1/orders/${testIds.order}/delivery-instructions`) return json(order)
    if (url.pathname === '/api/portal/v1/schedule/windows') {
      return json(slotsFixture.filter((slot) => slot.yard_id === url.searchParams.get('yard_id') && slot.kind === url.searchParams.get('kind')))
    }
    if (url.pathname === '/api/portal/v1/schedule/holds' && method === 'POST') {
      return json({ ...order.schedule_hold, order_id: testIds.order, status: 'held' }, 201)
    }
    if (url.pathname === `/api/portal/v1/schedule/holds/${testIds.hold}`) {
      return method === 'DELETE' ? new Response(null, { status: 204 }) : json({ ...order.schedule_hold, status: 'held' })
    }
    return json({ error: 'unexpected' }, 404)
  })
}

async function mountBoard(me = meFixture): Promise<GableProScheduleBoard> {
  const element = new GableProScheduleBoard()
  element.me = me
  document.body.append(element)
  await vi.waitFor(() => expect(element.querySelector('[data-testid="schedule-slots"] .pro-schedule-slot')).not.toBeNull())
  return element
}

async function mountDetail(timeline = timelineFixture): Promise<{ element: GableProOrderDetail, fetchMock: ReturnType<typeof mockPortal> }> {
  const fetchMock = mockPortal(orderFixture, timeline)
  vi.stubGlobal('fetch', fetchMock)
  const element = new GableProOrderDetail()
  element.me = meFixture
  element.orderId = testIds.order
  document.body.append(element)
  await vi.waitFor(() => expect(element.querySelector('[data-testid="order-timeline"]')).not.toBeNull())
  return { element, fetchMock }
}

describe('Schedule screen', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    localStorage.clear()
    sessionStorage.clear()
    window.history.replaceState({}, '', '/schedule')
    Object.defineProperty(navigator, 'onLine', { value: true, configurable: true })
    vi.restoreAllMocks()
    vi.stubGlobal('fetch', mockPortal({ ...orderFixture, schedule_hold: undefined }))
  })

  it('holds a delivery window on the selected order', async () => {
    const fetchMock = mockPortal({ ...orderFixture, schedule_hold: undefined })
    vi.stubGlobal('fetch', fetchMock)
    const element = await mountBoard()
    element.querySelector<HTMLButtonElement>(`[data-testid="schedule-slot-${testIds.windowA}"]`)?.click()
    await element.updateComplete
    element.querySelector<HTMLButtonElement>('[data-testid="schedule-hold"]')?.click()
    await vi.waitFor(() => expect(element.querySelector('.pro-success')?.textContent).toContain('held on the order'))
    const call = fetchMock.mock.calls.find(([input, init]) => String(input).includes('/schedule/holds') && (init?.method ?? 'GET') === 'POST')
    expect(call).toBeDefined()
    expect(JSON.parse(String(call?.[1]?.body)).order_id).toBe(testIds.order)
  })

  it('changes and releases an existing hold through the contracted routes', async () => {
    const fetchMock = mockPortal()
    vi.stubGlobal('fetch', fetchMock)
    const element = await mountBoard()
    element.querySelector<HTMLButtonElement>(`[data-testid="schedule-slot-${testIds.windowA}"]`)?.click()
    await element.updateComplete
    element.querySelector<HTMLButtonElement>('[data-testid="schedule-hold"]')?.click()
    await vi.waitFor(() => expect(element.querySelector('.pro-success')?.textContent).toContain('changed'))
    expect(fetchMock.mock.calls.some(([input, init]) => String(input).includes(testIds.hold) && (init?.method ?? 'GET') === 'PUT')).toBe(true)
    element.querySelector<HTMLButtonElement>('[data-testid="schedule-release"]')?.click()
    await vi.waitFor(() => expect(element.querySelector('.pro-success')?.textContent).toContain('released'))
    expect(fetchMock.mock.calls.some(([input, init]) => String(input).includes(testIds.hold) && (init?.method ?? 'GET') === 'DELETE')).toBe(true)
  })

  it('disables schedule writes offline with no queue', async () => {
    const element = await mountBoard()
    element.querySelector<HTMLButtonElement>(`[data-testid="schedule-slot-${testIds.windowA}"]`)?.click()
    element.offline = true
    await element.updateComplete
    expect(element.querySelector<HTMLButtonElement>('[data-testid="schedule-hold"]')?.disabled).toBe(true)
    expect(element.querySelector('[data-testid="schedule-offline"]')?.textContent).toContain('Nothing is queued')
  })

  it('renders the five step timeline and saves site instructions', async () => {
    const { element, fetchMock } = await mountDetail()
    expect(element.querySelectorAll('[data-testid="order-timeline"] li')).toHaveLength(5)
    const form = element.querySelector('form')
    form?.dispatchEvent(new SubmitEvent('submit', { bubbles: true, cancelable: true }))
    await vi.waitFor(() => expect(element.querySelector('.pro-success')?.textContent).toContain('site note is saved'))
    expect(fetchMock.mock.calls.some(([input, init]) => String(input).includes('delivery-instructions') && (init?.method ?? 'GET') === 'PUT')).toBe(true)
  })

  it('shows proof of delivery when the timeline carries it', async () => {
    const { element } = await mountDetail(deliveredTimelineFixture)
    expect(element.querySelector('[data-testid="order-pod"]')?.textContent).toContain('Site Receiver')
    expect(element.querySelector('[data-testid="order-pod"]')?.textContent).toContain('North gate, lot 4')
  })
})
