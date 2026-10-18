// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The statements cut by branch: the branch selector, the income statement's
 * comparison column, the trial balance summed across the branches, and the
 * drill down from a statement row to its ledger lines.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './BalanceSheet'
import './ProfitAndLoss'
import './TrialBalance'
import './branch-scope'
import type { LitElement } from 'lit'
import type { AccountLineItem, ProfitAndLossReport, TrialBalanceRow, GLDetailRow } from '../../types/gl'
import { mountAsync, text, jsonResponse, q, flush, update } from '../../test/dom'
import { branchContext } from '../../services/BranchContext'
import { previousPeriod } from './statement-format'

const KELOWNA = '11111111-1111-4111-8111-111111111111'
const VERNON = '22222222-2222-4222-8222-222222222222'

function seedBranches() {
  ;(branchContext as unknown as { _branches: unknown[]; _currentId: string | null })._branches = [
    { id: KELOWNA, code: 'KEL', name: 'Kelowna Main', is_home: true },
    { id: VERNON, code: 'VER', name: 'Vernon Yard', is_home: false },
  ]
  ;(branchContext as unknown as { _currentId: string | null })._currentId = null
}

function line(code: string, name: string, amount: number): AccountLineItem {
  return { account_id: `a-${code}`, account_code: code, account_name: name, amount }
}

function pnl(overrides: Partial<ProfitAndLossReport> = {}): ProfitAndLossReport {
  return {
    start_date: '2026-08-01',
    end_date: '2026-08-31',
    revenue: [line('4010', 'Sales Revenue', 1_500_000)],
    cogs: [line('5010', 'Cost of Goods Sold', 1_000_000)],
    expenses: [line('6010', 'Rent', 200_000)],
    total_revenue: 1_500_000,
    total_cogs: 1_000_000,
    gross_profit: 500_000,
    total_expenses: 200_000,
    net_income: 300_000,
    ...overrides,
  }
}

function detailRow(overrides: Partial<GLDetailRow> = {}): GLDetailRow {
  return {
    entry_id: 'e-1', entry_number: 17, entry_date: '2026-08-12', memo: 'Invoice 12', source: 'INVOICE',
    line_id: 'l-1', account_id: 'a-4010', account_code: '4010', account_name: 'Sales Revenue',
    description: 'Sales Revenue', branch_id: VERNON, debit_cents: 0, credit_cents: 123_456, ...overrides,
  }
}

function serve(routes: Record<string, unknown>) {
  const spy = vi.fn((url: string, _init?: RequestInit) => {
    for (const [fragment, body] of Object.entries(routes)) {
      if (url.includes(fragment)) return Promise.resolve(jsonResponse(body))
    }
    return Promise.resolve(jsonResponse(null))
  })
  vi.stubGlobal('fetch', spy)
  return spy
}

function headerOf(init: RequestInit | undefined, name: string): string | null {
  return new Headers(init?.headers).get(name)
}

/** Choose a branch the way the selector does: the select fires `change`. */
async function choose(host: LitElement, value: string) {
  const select = q<HTMLElement & { value: string }>(host, 'gable-branch-scope gable-select')
  select.value = value
  select.dispatchEvent(new Event('change', { bubbles: true }))
  await flush()
  await update(host, {})
}

beforeEach(() => {
  localStorage.clear()
  seedBranches()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('previousPeriod', () => {
  it('ends the day before the window starts and has the same length', () => {
    expect(previousPeriod('2026-08-01', '2026-08-31')).toEqual({ start: '2026-07-01', end: '2026-07-31' })
    expect(previousPeriod('2026-03-01', '2026-03-07')).toEqual({ start: '2026-02-22', end: '2026-02-28' })
  })

  it('counts a one day window as one day', () => {
    expect(previousPeriod('2026-08-10', '2026-08-10')).toEqual({ start: '2026-08-09', end: '2026-08-09' })
  })
})

describe('branch scope', () => {
  it('offers the consolidated view first, then each branch by name', async () => {
    const el = await mountAsync<LitElement>('gable-branch-scope')
    const select = q<HTMLElement & { options: { value: string; label: string }[] }>(el, 'gable-select')

    expect(select.options.map(o => o.label)).toEqual(['Consolidated', 'Kelowna Main', 'Vernon Yard'])
    expect(select.options[0].value).toBe('')
  })

  it('announces the chosen branch id, empty for the consolidated view', async () => {
    const el = await mountAsync<LitElement>('gable-branch-scope')
    const seen: string[] = []
    el.addEventListener('branch-scope-change', (e) => seen.push((e as CustomEvent).detail.value))
    const select = q<HTMLElement & { value: string }>(el, 'gable-select')

    select.value = VERNON
    select.dispatchEvent(new Event('change', { bubbles: true }))
    select.value = ''
    select.dispatchEvent(new Event('change', { bubbles: true }))

    expect(seen).toEqual([VERNON, ''])
  })
})

describe('income statement by branch', () => {
  it('asks for the consolidated view with no branch and an empty branch header', async () => {
    const fetchMock = serve({ '/gl/profit-and-loss': pnl() })
    await mountAsync<LitElement>('gable-profit-and-loss')

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).not.toContain('branch_id=')
    expect(headerOf(init, 'X-Branch-Id')).toBe('')
  })

  it('asks for the chosen branch by name and by header', async () => {
    const fetchMock = serve({ '/gl/profit-and-loss': pnl({ branch_id: VERNON }) })
    const el = await mountAsync<LitElement>('gable-profit-and-loss')
    await choose(el, VERNON)

    const [url, init] = fetchMock.mock.calls.at(-1)!
    expect(url).toContain(`branch_id=${VERNON}`)
    expect(headerOf(init, 'X-Branch-Id')).toBe(VERNON)
  })

  it('opens on the branch the operator is working in', async () => {
    ;(branchContext as unknown as { _currentId: string | null })._currentId = KELOWNA
    const fetchMock = serve({ '/gl/profit-and-loss': pnl({ branch_id: KELOWNA }) })
    await mountAsync<LitElement>('gable-profit-and-loss')

    expect(fetchMock.mock.calls[0][0]).toContain(`branch_id=${KELOWNA}`)
  })

  it('shows the previous period beside the current one when compared', async () => {
    const fetchMock = serve({
      '/gl/profit-and-loss': pnl({
        comparison: {
          start_date: '2026-07-01', end_date: '2026-07-31', total_revenue: 1_200_000, total_cogs: 900_000,
          gross_profit: 300_000, total_expenses: 150_000, net_income: 150_000,
        },
      }),
    })
    const el = await mountAsync<LitElement>('gable-profit-and-loss')
    const toggle = Array.from(el.querySelectorAll('button')).find(b => text(b).includes('Compare to previous period'))!
    toggle.click()
    await flush()
    await update(el, {})

    const [url] = fetchMock.mock.calls.at(-1)!
    expect(url).toContain('compare_start=')
    expect(url).toContain('compare_end=')
    expect(text(el)).toContain('Previous period')
    expect(text(el)).toContain('$12,000.00') // previous revenue
    expect(text(el)).toContain('$1,500.00') // previous net income
  })

  it('drills a statement row down to its ledger lines in the window and branch', async () => {
    const fetchMock = serve({ '/gl/profit-and-loss': pnl(), '/gl/detail': [detailRow()] })
    const el = await mountAsync<LitElement>('gable-profit-and-loss')
    await choose(el, VERNON)
    const row = Array.from(el.querySelectorAll('tr')).find(r => text(r).includes('Sales Revenue'))!
    row.click()
    await flush()
    await update(el, {})
    await flush()
    await update(el, {})

    const call = fetchMock.mock.calls.find(([u]) => u.includes('/gl/detail'))!
    expect(call[0]).toContain('account_id=a-4010')
    expect(call[0]).toContain('from=2026-08-01')
    expect(call[0]).toContain('to=2026-08-31')
    expect(call[0]).toContain(`branch_id=${VERNON}`)
    expect(text(el)).toContain('#17')
    expect(text(el)).toContain('Vernon Yard')
    expect(text(el)).toContain('$1,234.56')
  })

  it('says a refused view is outside the caller\'s reach, not that it failed', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse({ error: { code: 'forbidden' } }, 403))))
    const { ToastService } = await import('../../lib/toast-service.ts')
    const toast = vi.spyOn(ToastService, 'show').mockImplementation(() => undefined as never)
    await mountAsync<LitElement>('gable-profit-and-loss')

    expect(toast).toHaveBeenCalledWith('You do not have access to this view. Pick a branch you may read.', 'error')
    toast.mockRestore()
  })
})

describe('balance sheet by branch', () => {
  it('puts the date and the chosen branch on the query string', async () => {
    const sheet = {
      as_of_date: '2026-08-31', assets: [], liabilities: [], equity: [],
      total_assets: 0, total_liabilities: 0, total_equity: 0, retained_earnings: 0,
    }
    const fetchMock = serve({ '/gl/balance-sheet': sheet })
    const el = await mountAsync<LitElement>('gable-balance-sheet')
    await choose(el, KELOWNA)

    const [url, init] = fetchMock.mock.calls.at(-1)!
    expect(url).toMatch(/\/api\/v1\/gl\/balance-sheet\?as_of=\d{4}-\d{2}-\d{2}&branch_id=/)
    expect(url).toContain(`branch_id=${KELOWNA}`)
    expect(headerOf(init, 'X-Branch-Id')).toBe(KELOWNA)
  })
})

describe('trial balance by branch', () => {
  function row(code: string, branch: string, debit: number, credit: number): TrialBalanceRow {
    return { account_id: `a-${code}`, account_code: code, account_name: code === '1010' ? 'Cash' : 'Sales', account_type: code === '1010' ? 'ASSET' : 'REVENUE', currency: 'USD', branch_id: branch, debit, credit }
  }

  it('shows one row per account, the sum of the branches', async () => {
    serve({
      '/gl/trial-balance': [
        row('1010', KELOWNA, 700_000, 0), row('1010', VERNON, 300_000, 0),
        row('4010', KELOWNA, 0, 700_000), row('4010', VERNON, 0, 300_000),
      ],
    })
    const el = await mountAsync<LitElement>('gable-trial-balance')

    const cash = Array.from(el.querySelectorAll('tr')).filter(r => text(r).startsWith('1010 Cash'))
    expect(cash).toHaveLength(1)
    expect(text(cash[0])).toContain('$10,000.00')
    expect(text(el)).toContain('Balanced')
  })

  it('asks for the chosen branch and drills a row down to its lines', async () => {
    const fetchMock = serve({
      '/gl/trial-balance': [row('1010', VERNON, 300_000, 0), row('4010', VERNON, 0, 300_000)],
      '/gl/detail': [detailRow({ account_id: 'a-1010', account_code: '1010', debit_cents: 300_000, credit_cents: 0 })],
    })
    const el = await mountAsync<LitElement>('gable-trial-balance')
    await choose(el, VERNON)
    expect(fetchMock.mock.calls.at(-1)![0]).toContain(`branch_id=${VERNON}`)

    const cash = Array.from(el.querySelectorAll('tr')).find(r => text(r).startsWith('1010 Cash'))!
    cash.click()
    await flush()
    await update(el, {})
    await flush()
    await update(el, {})

    const detail = fetchMock.mock.calls.find(([u]) => u.includes('/gl/detail'))!
    expect(detail[0]).toContain('account_id=a-1010')
    expect(detail[0]).toContain(`branch_id=${VERNON}`)
    expect(text(el)).toContain('$3,000.00')
  })
})
