// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { GableDealerQuotePage } from './dealer-page.ts'
import { dealerTokenFromPath } from './dealer-api.ts'

const token = 'd'.repeat(43)

const open = {
  number: 'Q-000042', status: 'open', dealer_name: 'Gable Lumber', customer_name: 'Pat Contractor Co',
  expires_at: '2030-01-15T00:00:00Z', can_decide: true,
  lines: [{ description: '2x4 stud', quantity: '100', uom: 'EA', price_uom: 'EA', unit_price_ten_thousandths: 75000, line_total_cents: 75000 }],
  freight_cents: 2500, total_cents: 77500,
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

async function mountPage(get: Response, post?: Response): Promise<{ element: GableDealerQuotePage, fetchMock: ReturnType<typeof vi.fn> }> {
  const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => (init?.method === 'POST' && post !== undefined ? post : get.clone()))
  vi.stubGlobal('fetch', fetchMock)
  const element = new GableDealerQuotePage()
  document.body.append(element)
  await vi.waitFor(() => expect(element.querySelector('[data-testid="dealer-lines"], [data-testid="dealer-error"]')).not.toBeNull())
  return { element, fetchMock }
}

function type(element: Element, selector: string, value: string): void {
  const field = element.querySelector<HTMLInputElement>(selector)
  if (field === null) throw new Error(`Missing ${selector}`)
  field.value = value
  field.dispatchEvent(new Event('input', { bubbles: true }))
}

describe('dealer public quote page', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    window.history.replaceState({}, '', `/dq/${token}`)
    Object.defineProperty(navigator, 'onLine', { value: true, configurable: true })
    vi.restoreAllMocks()
  })

  it('reads the token only from a /dq/ path of the right shape', () => {
    expect(dealerTokenFromPath(`/dq/${token}`)).toBe(token)
    expect(dealerTokenFromPath(`/q/${token}`)).toBeNull()
    expect(dealerTokenFromPath('/dq/short')).toBeNull()
  })

  it('shows an open quote, sends no credentials or referrer, and accepts with a full name', async () => {
    const accepted = { ...open, status: 'accepted', can_decide: false }
    const { element, fetchMock } = await mountPage(json(open), json(accepted))
    expect(element.textContent).toContain('Q-000042')
    expect(element.textContent).toContain('$775.00')
    const accept = element.querySelector<HTMLButtonElement>('[data-testid="dealer-accept"]')
    expect(accept?.disabled).toBe(true)
    type(element, 'input[name="full_name"]', 'Pat Contractor')
    await element.updateComplete
    expect(accept?.disabled).toBe(false)
    accept?.click()
    await vi.waitFor(() => expect(element.querySelector('[data-testid="dealer-closed"]')?.textContent).toContain('Quote accepted'))
    const [url, init] = fetchMock.mock.calls.find(([, i]) => i?.method === 'POST') as [string, RequestInit]
    expect(url).toContain(`/api/public/v1/dealer-quotes/${token}/accept`)
    expect(JSON.parse(String(init.body))).toEqual({ full_name: 'Pat Contractor' })
    expect(init.credentials).toBe('omit')
    expect(init.referrerPolicy).toBe('no-referrer')
    expect(element.querySelector('[data-testid="dealer-decision"]')).toBeNull()
  })

  it('declines with an optional reason', async () => {
    const declined = { ...open, status: 'declined', can_decide: false }
    const { element, fetchMock } = await mountPage(json(open), json(declined))
    type(element, 'input[name="full_name"]', 'Pat Contractor')
    element.querySelector<HTMLButtonElement>('[data-testid="dealer-decline-open"]')?.click()
    await element.updateComplete
    type(element, 'textarea[name="reason"]', 'Found a better price')
    await element.updateComplete
    element.querySelector<HTMLButtonElement>('[data-testid="dealer-decline"]')?.click()
    await vi.waitFor(() => expect(element.querySelector('[data-testid="dealer-closed"]')?.textContent).toContain('Quote declined'))
    const [, init] = fetchMock.mock.calls.find(([, i]) => i?.method === 'POST') as [string, RequestInit]
    expect(JSON.parse(String(init.body))).toEqual({ full_name: 'Pat Contractor', reason: 'Found a better price' })
  })

  it.each([
    ['accepted', 'Quote accepted'],
    ['declined', 'Quote declined'],
    ['expired', 'Quote expired'],
  ])('shows a %s quote in plain words with no controls', async (status, words) => {
    const { element } = await mountPage(json({ ...open, status, can_decide: false }))
    expect(element.querySelector('[data-testid="dealer-closed"]')?.textContent).toContain(words)
    expect(element.querySelector('[data-testid="dealer-accept"]')).toBeNull()
  })

  it('says the link is no longer available on 410', async () => {
    const { element } = await mountPage(json({ error: { code: 'gone' } }, 410))
    expect(element.querySelector('[data-testid="dealer-error"]')?.textContent).toContain('no longer available')
  })

  it('says to wait on 429', async () => {
    const { element } = await mountPage(json({}, 429))
    expect(element.querySelector('[data-testid="dealer-error"]')?.textContent).toContain('Too many attempts')
  })

  it('shows what stands when an accept loses the race to an earlier decision', async () => {
    const { element } = await mountPage(json(open), json({ error: { code: 'invalid_state_transition' } }, 409))
    type(element, 'input[name="full_name"]', 'Pat Contractor')
    await element.updateComplete
    element.querySelector<HTMLButtonElement>('[data-testid="dealer-accept"]')?.click()
    await vi.waitFor(() => expect(element.querySelector('[data-testid="dealer-error"]')?.textContent).toContain('already been decided'))
  })
})
