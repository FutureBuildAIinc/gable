// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Financial statements: what the balance sheet and P&L actually render.
 *
 * The backend sends int64 cents; these pages must show dollars, grouped, in
 * the accounting convention (negatives in parentheses). The fixtures below are
 * balanced ledgers with known totals, so a conversion slip shows up as a wrong
 * string rather than as a plausible-looking number.
 *
 * Both pages render money through `formatStatementCents()`, which delegates
 * every digit to the shared `formatCents()` from lib/utils.ts — see
 * erp-money-formatting.test.ts for the five private copies that predate this
 * and which these two pages deliberately do not join.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './BalanceSheet'
import './ProfitAndLoss'
import type { LitElement } from 'lit'
import type { BalanceSheetReport, ProfitAndLossReport, AccountLineItem } from '../../types/gl'
import { mountAsync, text, jsonResponse, q } from '../../test/dom'
import { formatStatementCents, statementAmountClasses } from './statement-format'

function serve(routes: Record<string, unknown>) {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      for (const [fragment, body] of Object.entries(routes)) {
        if (url.includes(fragment)) return Promise.resolve(jsonResponse(body))
      }
      return Promise.resolve(jsonResponse(null))
    }),
  )
}

function line(code: string, name: string, amount: number): AccountLineItem {
  return { account_id: `a-${code}`, account_code: code, account_name: name, amount }
}

/**
 * A balanced sheet: assets 7,300,000c = liabilities 2,000,000c + equity
 * 5,300,000c, where equity is 5,000,000c of capital plus 300,000c retained.
 */
function balanceSheet(overrides: Partial<BalanceSheetReport> = {}): BalanceSheetReport {
  return {
    as_of_date: '2026-08-31',
    assets: [
      line('1010', 'Cash', 4_800_000),
      line('1020', 'Accounts Receivable', 1_500_000),
      line('1300', 'Inventory', 1_000_000),
    ],
    liabilities: [line('2010', 'Accounts Payable', 2_000_000)],
    equity: [line('3010', 'Owner Capital', 5_000_000)],
    total_assets: 7_300_000,
    total_liabilities: 2_000_000,
    total_equity: 5_300_000,
    retained_earnings: 300_000,
    ...overrides,
  }
}

function profitAndLoss(overrides: Partial<ProfitAndLossReport> = {}): ProfitAndLossReport {
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

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('formatStatementCents', () => {
  it('converts cents to grouped dollars', () => {
    expect(formatStatementCents(1_234_567)).toBe('$12,345.67')
  })

  it('groups thousands on a large value', () => {
    // 987,654,321 cents = $9,876,543.21 — three separators.
    expect(formatStatementCents(987_654_321)).toBe('$9,876,543.21')
  })

  it('renders zero as $0.00, not as a dash or a blank', () => {
    expect(formatStatementCents(0)).toBe('$0.00')
  })

  it('parenthesises negatives instead of using a minus sign', () => {
    expect(formatStatementCents(-10_000)).toBe('($100.00)')
  })

  it('groups thousands inside the parentheses too', () => {
    expect(formatStatementCents(-1_234_567)).toBe('($12,345.67)')
  })

  it('keeps a one-cent value distinguishable from zero', () => {
    expect(formatStatementCents(1)).toBe('$0.01')
    expect(formatStatementCents(-1)).toBe('($0.01)')
  })

  it('is exactly formatCents for non-negative values', async () => {
    const { formatCents } = await import('../../lib/utils')
    for (const cents of [0, 1, 99, 100, 1_234_567, 987_654_321]) {
      expect(formatStatementCents(cents)).toBe(formatCents(cents))
    }
  })

  it('colours by sign', () => {
    expect(statementAmountClasses(-1)).toContain('rose')
    expect(statementAmountClasses(1)).toContain('emerald')
    expect(statementAmountClasses(0)).toContain('zinc')
  })
})

describe('balance sheet', () => {
  it('renders each section total in dollars', async () => {
    serve({ '/gl/balance-sheet': balanceSheet() })
    const el = await mountAsync<LitElement>('gable-balance-sheet')
    const body = text(el)

    expect(body).toContain('$73,000.00') // total assets
    expect(body).toContain('$20,000.00') // total liabilities
    expect(body).toContain('$53,000.00') // total equity
    // Not the raw cents figure.
    expect(body).not.toContain('$7,300,000.00')
  })

  it('renders retained earnings as its own line', async () => {
    serve({ '/gl/balance-sheet': balanceSheet() })
    const el = await mountAsync<LitElement>('gable-balance-sheet')
    const body = text(el)

    expect(body).toContain('Retained Earnings')
    expect(body).toContain('$3,000.00') // 300,000 cents
  })

  it('reports a balanced sheet as balanced', async () => {
    serve({ '/gl/balance-sheet': balanceSheet() })
    const el = await mountAsync<LitElement>('gable-balance-sheet')

    expect(text(el)).toContain('Balanced')
    expect(text(el)).not.toContain('Unbalanced')
    expect(text(el)).not.toContain('Diff:')
  })

  it('shows liabilities + equity equal to total assets', async () => {
    serve({ '/gl/balance-sheet': balanceSheet() })
    const el = await mountAsync<LitElement>('gable-balance-sheet')

    expect(text(el)).toContain('Total Liabilities + Equity')
    // 2,000,000 + 5,300,000 = 7,300,000 cents = $73,000.00, the same figure
    // reported for total assets.
    const rendered = text(el).match(/\$73,000\.00/g) ?? []
    expect(rendered.length).toBeGreaterThanOrEqual(2)
  })

  it('flags an unbalanced sheet and states the gap', async () => {
    // Assets are 500 cents more than liabilities + equity.
    serve({
      '/gl/balance-sheet': balanceSheet({ total_assets: 7_300_500 }),
    })
    const el = await mountAsync<LitElement>('gable-balance-sheet')

    expect(text(el)).toContain('Unbalanced')
    expect(text(el)).toContain('Diff: $5.00')
  })

  it('renders a contra-asset credit balance in parentheses', async () => {
    serve({
      '/gl/balance-sheet': balanceSheet({
        assets: [
          line('1010', 'Cash', 4_800_000),
          line('1020', 'Accounts Receivable', 1_500_000),
          line('1300', 'Inventory', 1_100_000),
          line('1590', 'Accumulated Depreciation', -100_000),
        ],
      }),
    })
    const el = await mountAsync<LitElement>('gable-balance-sheet')

    expect(text(el)).toContain('Accumulated Depreciation')
    expect(text(el)).toContain('($1,000.00)')
    expect(text(el)).not.toContain('$-1,000.00')
  })

  it('renders a negative retained earnings (accumulated deficit) in parentheses', async () => {
    serve({
      '/gl/balance-sheet': balanceSheet({
        retained_earnings: -250_000,
        total_equity: 4_750_000,
        total_assets: 6_750_000,
      }),
    })
    const el = await mountAsync<LitElement>('gable-balance-sheet')

    expect(text(el)).toContain('($2,500.00)')
  })

  it('renders a zero balance as $0.00', async () => {
    serve({
      '/gl/balance-sheet': balanceSheet({
        assets: [line('1010', 'Cash', 0)],
        liabilities: [],
        equity: [],
        total_assets: 0,
        total_liabilities: 0,
        total_equity: 0,
        retained_earnings: 0,
      }),
    })
    const el = await mountAsync<LitElement>('gable-balance-sheet')

    expect(text(el)).toContain('$0.00')
    expect(text(el)).toContain('Balanced')
  })

  it('groups thousands on a nine-figure balance sheet', async () => {
    serve({
      '/gl/balance-sheet': balanceSheet({
        assets: [line('1010', 'Cash', 987_654_321_00)],
        liabilities: [],
        equity: [line('3010', 'Owner Capital', 987_654_321_00)],
        total_assets: 987_654_321_00,
        total_liabilities: 0,
        total_equity: 987_654_321_00,
        retained_earnings: 0,
      }),
    })
    const el = await mountAsync<LitElement>('gable-balance-sheet')

    expect(text(el)).toContain('$987,654,321.00')
    expect(text(el)).toContain('Balanced')
  })

  it('says so when a section has no accounts, rather than rendering an empty table', async () => {
    serve({
      '/gl/balance-sheet': balanceSheet({
        liabilities: [],
        total_liabilities: 0,
        total_assets: 5_300_000,
      }),
    })
    const el = await mountAsync<LitElement>('gable-balance-sheet')

    expect(text(el)).toContain('No accounts')
  })

  it('requests the selected as-of date', async () => {
    const fetchMock = vi.fn((_url: string) => Promise.resolve(jsonResponse(balanceSheet())))
    vi.stubGlobal('fetch', fetchMock)
    await mountAsync<LitElement>('gable-balance-sheet')

    expect(fetchMock).toHaveBeenCalled()
    const url = fetchMock.mock.calls[0][0]
    expect(url).toContain('/api/v1/gl/balance-sheet')
    expect(url).toMatch(/as_of=\d{4}-\d{2}-\d{2}/)
  })
})

describe('profit and loss', () => {
  it('renders revenue, gross profit and net income in dollars', async () => {
    serve({ '/gl/profit-and-loss': profitAndLoss() })
    const el = await mountAsync<LitElement>('gable-profit-and-loss')
    const body = text(el)

    expect(body).toContain('$15,000.00') // revenue
    expect(body).toContain('$5,000.00') // gross profit
    expect(body).toContain('$3,000.00') // net income
    expect(body).not.toContain('$1,500,000.00')
  })

  it('renders COGS and operating expenses as separate sections', async () => {
    serve({ '/gl/profit-and-loss': profitAndLoss() })
    const el = await mountAsync<LitElement>('gable-profit-and-loss')
    const body = text(el)

    expect(body).toContain('Cost of Goods Sold')
    expect(body).toContain('$10,000.00') // COGS
    expect(body).toContain('Rent')
    expect(body).toContain('$2,000.00') // operating expenses
  })

  it('renders a net loss in parentheses', async () => {
    serve({
      '/gl/profit-and-loss': profitAndLoss({
        revenue: [line('4010', 'Sales Revenue', 100_000)],
        cogs: [line('5010', 'Cost of Goods Sold', 80_000)],
        expenses: [line('6010', 'Rent', 150_000)],
        total_revenue: 100_000,
        total_cogs: 80_000,
        gross_profit: 20_000,
        total_expenses: 150_000,
        net_income: -130_000,
      }),
    })
    const el = await mountAsync<LitElement>('gable-profit-and-loss')

    expect(text(el)).toContain('($1,300.00)')
    expect(text(el)).not.toContain('$-1,300.00')
  })

  it('renders negative revenue (returns exceeding sales) in parentheses', async () => {
    serve({
      '/gl/profit-and-loss': profitAndLoss({
        revenue: [line('4010', 'Sales Revenue', -300_000)],
        cogs: [],
        expenses: [],
        total_revenue: -300_000,
        total_cogs: 0,
        gross_profit: -300_000,
        total_expenses: 0,
        net_income: -300_000,
      }),
    })
    const el = await mountAsync<LitElement>('gable-profit-and-loss')

    expect(text(el)).toContain('($3,000.00)')
  })

  it('renders a zero-activity period as $0.00 throughout', async () => {
    serve({
      '/gl/profit-and-loss': profitAndLoss({
        revenue: [],
        cogs: [],
        expenses: [],
        total_revenue: 0,
        total_cogs: 0,
        gross_profit: 0,
        total_expenses: 0,
        net_income: 0,
      }),
    })
    const el = await mountAsync<LitElement>('gable-profit-and-loss')

    expect(text(el)).toContain('$0.00')
    expect(text(el)).not.toContain('NaN')
  })

  it('groups thousands on a nine-figure income statement', async () => {
    serve({
      '/gl/profit-and-loss': profitAndLoss({
        revenue: [line('4010', 'Sales Revenue', 123_456_789_01)],
        cogs: [],
        expenses: [],
        total_revenue: 123_456_789_01,
        total_cogs: 0,
        gross_profit: 123_456_789_01,
        total_expenses: 0,
        net_income: 123_456_789_01,
      }),
    })
    const el = await mountAsync<LitElement>('gable-profit-and-loss')

    expect(text(el)).toContain('$123,456,789.01')
  })

  it('requests the selected reporting window', async () => {
    const fetchMock = vi.fn((_url: string) => Promise.resolve(jsonResponse(profitAndLoss())))
    vi.stubGlobal('fetch', fetchMock)
    await mountAsync<LitElement>('gable-profit-and-loss')

    const url = fetchMock.mock.calls[0][0]
    expect(url).toContain('/api/v1/gl/profit-and-loss')
    expect(url).toMatch(/start=\d{4}-\d{2}-01/) // defaults to the first of the month
    expect(url).toMatch(/end=\d{4}-\d{2}-\d{2}/)
  })
})

describe('statement cross-check', () => {
  it('shows the same figure for P&L net income and balance sheet retained earnings', async () => {
    // Both statements are served from one balanced ledger: revenue 1,500,000
    // less COGS 1,000,000 less rent 200,000 = 300,000 cents, which is exactly
    // the retained earnings the balance sheet carries.
    serve({ '/gl/profit-and-loss': profitAndLoss() })
    const pl = await mountAsync<LitElement>('gable-profit-and-loss')
    const plText = text(pl)
    vi.unstubAllGlobals()

    serve({ '/gl/balance-sheet': balanceSheet() })
    const bs = await mountAsync<LitElement>('gable-balance-sheet')
    const bsText = text(bs)

    expect(plText).toContain('$3,000.00') // net income
    expect(bsText).toContain('$3,000.00') // retained earnings
  })
})

describe('statement error handling', () => {
  it('does not render a fabricated statement when the request fails', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve(jsonResponse({ error: 'boom' }, 500))),
    )
    const el = await mountAsync<LitElement>('gable-balance-sheet')

    // No totals, and specifically not a zeroed sheet that reads as "balanced".
    expect(text(el)).toContain('No Data Available')
    expect(text(el)).not.toContain('Balanced')
  })
})

describe('print affordance', () => {
  it('offers a print button on the balance sheet', async () => {
    serve({ '/gl/balance-sheet': balanceSheet() })
    const el = await mountAsync<LitElement>('gable-balance-sheet')

    const printBtn = q<HTMLButtonElement>(el, 'button[title="Print report"]')
    expect(printBtn).toBeTruthy()
  })
})
