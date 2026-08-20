// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * ERP money rendering outside `formatCents()`.
 *
 * CLAUDE.md is unambiguous: "When rendering money on **ERP pages**, use
 * `formatCents()` from `app/src/lib/utils.ts`". Five pages used not to — each
 * carried its own private `_formatCents`, and no two of the five agreed:
 *
 *   accounting/TrialBalance.ts          `--` at zero, then toLocaleString('en-US', {min:2})
 *   accounting/JournalEntries.ts        toLocaleString('en-US', {min:2})
 *   accounting/BankReconciliation.ts    abs()/100 .toFixed(2) + " DR" when negative
 *   accounting/POMatching.ts            (cents/100).toFixed(2)
 *   purchasing/PurchaseOrderDetail.ts   (cents/100).toFixed(2)
 *
 * The `.toFixed(2)` three dropped thousands separators entirely, so a six-figure
 * freight allocation rendered `$123456.78`. All five now delegate to the shared
 * helper; the only page-specific behaviour left is expressed at its call site
 * (BankReconciliation's " DR" suffix, TrialBalance's `--` for an empty side of
 * the ledger). These tests are the record of that consolidation.
 *
 * Every page here loads through `fetchWithAuth`, so stubbing `fetch` drives all
 * four from one place.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import '../accounting/TrialBalance'
import '../accounting/JournalEntries'
import '../accounting/BankReconciliation'
import '../accounting/POMatching'
import type { LitElement } from 'lit'
import type { TrialBalanceRow, JournalEntry } from '../../types/gl'
import type { ReconciliationSession } from '../../types/bankrecon'
import type { MatchConfig } from '../../types/matching'
import { mountAsync, text, jsonResponse } from '../../test/dom'

/** Serve each URL fragment with its payload; anything else gets `[]`. */
function serve(routes: Record<string, unknown>) {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      for (const [fragment, body] of Object.entries(routes)) {
        if (url.includes(fragment)) return Promise.resolve(jsonResponse(body))
      }
      return Promise.resolve(jsonResponse([]))
    }),
  )
}

/** Money-looking substrings in the rendered page, in document order. */
function amounts(el: LitElement): string[] {
  return text(el).match(/\$[\d,]+\.\d{2}(?: DR)?/g) ?? []
}

function tbRow(code: string, name: string, debit: number, credit: number): TrialBalanceRow {
  return { account_id: `a-${code}`, account_code: code, account_name: name, account_type: 'ASSET', debit, credit }
}

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('trial balance', () => {
  const rows = [
    tbRow('1000', 'Cash', 1_234_567, 0), // $12,345.67
    tbRow('1200', 'Accounts Receivable', 738_807, 0), // $7,388.07
  ]

  it('converts cents to dollars rather than printing them as dollars', async () => {
    serve({ '/gl/trial-balance': rows })
    const el = await mountAsync<LitElement>('gable-trial-balance')

    expect(amounts(el)).toContain('$12,345.67')
    expect(amounts(el)).not.toContain('$1,234,567.00')
  })

  it('groups thousands, matching formatCents', async () => {
    serve({ '/gl/trial-balance': rows })
    const el = await mountAsync<LitElement>('gable-trial-balance')

    expect(text(el)).toContain('$7,388.07')
  })

  it('renders a dash for an empty side of the ledger', async () => {
    serve({ '/gl/trial-balance': rows })
    const el = await mountAsync<LitElement>('gable-trial-balance')

    // Every row here is debit-only, so each credit cell reads "--".
    expect(text(el)).toContain('--')
  })

  it('flags an out-of-balance ledger and states the gap', async () => {
    serve({
      '/gl/trial-balance': [tbRow('1000', 'Cash', 1_000_000, 0), tbRow('4000', 'Sales', 0, 999_500)],
    })
    const el = await mountAsync<LitElement>('gable-trial-balance')

    expect(text(el)).toContain('Out of Balance')
    expect(text(el)).toContain('Diff: $5.00') // 1,000,000 - 999,500 = 500 cents
  })

  it('reports a balanced ledger', async () => {
    serve({
      '/gl/trial-balance': [
        tbRow('1000', 'Cash', 1_000_000, 0),
        tbRow('4000', 'Sales', 0, 1_000_000),
      ],
    })
    const el = await mountAsync<LitElement>('gable-trial-balance')

    expect(text(el)).toContain('Balanced')
    expect(text(el)).not.toContain('Diff:')
  })

  // Regression (app/src/pages/accounting/TrialBalance.ts): the private formatter
  // short-circuited `cents === 0` to "--" *inside* the helper, not at the call
  // site. The table cells already guard with `row.debit > 0 ? … : '--'`, so the
  // only place that branch actually fired was the three summary cards at the top
  // of the page: a ledger with no postings showed "Total Debits --" instead of
  // "Total Debits $0.00". A dash there reads as "not loaded", which is a
  // different (and more alarming) statement than "zero". The page now uses the
  // shared `formatCents()`; the call sites that want a dash still ask for one.
  it('renders a zero total as $0.00, not as a dash', async () => {
    serve({ '/gl/trial-balance': [] as TrialBalanceRow[] })
    const el = await mountAsync<LitElement>('gable-trial-balance')

    const totals = Array.from(el.querySelectorAll('p.text-2xl')).map((p) => text(p))
    expect(totals).toContain('$0.00')
  })
})

describe('journal entries', () => {
  function entry(total: number): JournalEntry {
    return {
      id: 'je-1',
      entry_number: 1042,
      entry_date: '2026-08-11',
      memo: 'Invoice INV-77 posted to AR',
      source: 'INVOICE',
      status: 'POSTED',
      posted_by: 'system',
      total_debit: total,
      total_credit: total,
      created_at: '2026-08-11T14:00:00Z',
      updated_at: '2026-08-11T14:00:00Z',
    }
  }

  it('converts the entry total from cents to dollars', async () => {
    serve({ '/gl/journal-entries': [entry(738_807)], '/gl/accounts': [] })
    const el = await mountAsync<LitElement>('gable-journal-entries')

    expect(text(el)).toContain('$7,388.07')
    expect(text(el)).not.toContain('$738,807.00')
  })

  it('renders a zero-value entry as $0.00', async () => {
    // Unlike TrialBalance, this copy has no dash case — same money, two
    // renderings, one page apart.
    serve({ '/gl/journal-entries': [entry(0)], '/gl/accounts': [] })
    const el = await mountAsync<LitElement>('gable-journal-entries')

    expect(text(el)).toContain('$0.00')
  })

  /** Open the create drawer and type `debit` on line 1, `credit` on line 2. */
  async function draft(el: LitElement, debit: string, credit: string) {
    const newEntry = Array.from(el.querySelectorAll('button')).find((b) =>
      text(b).includes('New Entry'),
    ) as HTMLButtonElement
    newEntry.click()
    await el.updateComplete

    const numbers = Array.from(el.querySelectorAll<HTMLInputElement>('input[type="number"]'))
    // Line 1 debit, line 2 credit.
    for (const [input, value] of [
      [numbers[0], debit],
      [numbers[3], credit],
    ] as [HTMLInputElement, string][]) {
      input.value = value
      input.dispatchEvent(new Event('input', { bubbles: true }))
      await el.updateComplete
    }
  }

  it('takes the draft entry in dollars, matching what it posts', async () => {
    // The read side of this page is cents (`total_debit` above) but the write
    // side is dollars: `gl/handler.go:218` declares `Debit float64` and
    // `:253` does `int64(math.Round(lr.Debit * 100))`. The running total is
    // therefore correctly *not* divided by 100 — it is already dollars. Pinning
    // this so nobody "fixes" the asymmetry by adding a /100 that would post a
    // journal entry 100x too small.
    serve({ '/gl/journal-entries': [], '/gl/accounts': [] })
    const el = await mountAsync<LitElement>('gable-journal-entries')
    await draft(el, '73.88', '73.88')

    expect(text(el)).toContain('DR: $73.88')
    expect(text(el)).toContain('CR: $73.88')
    expect(text(el)).toContain('[Balanced]')
  })

  it('refuses to post an unbalanced entry', async () => {
    serve({ '/gl/journal-entries': [], '/gl/accounts': [] })
    const el = await mountAsync<LitElement>('gable-journal-entries')
    await draft(el, '73.88', '73.87')

    expect(text(el)).toContain('[Unbalanced]')
    const create = Array.from(el.querySelectorAll('button')).find((b) =>
      text(b).includes('Create Entry'),
    ) as HTMLButtonElement
    expect(create.disabled).toBe(true)
  })

  it('refuses to post an entry that balances at zero', async () => {
    // 0 = 0 is arithmetically balanced and financially meaningless; the
    // backend rejects it too (gl/service.go:120).
    serve({ '/gl/journal-entries': [], '/gl/accounts': [] })
    const el = await mountAsync<LitElement>('gable-journal-entries')
    await draft(el, '0', '0')

    expect(text(el)).toContain('[Unbalanced]')
  })
})

describe('bank reconciliation', () => {
  function session(statement: number, difference: number): ReconciliationSession {
    return {
      id: 'sess-1',
      bank_account_id: 'ba-1',
      bank_account_name: 'Operating — 1234',
      period_start: '2026-07-01',
      period_end: '2026-07-31',
      statement_balance: statement,
      gl_balance: statement - difference,
      cleared_count: 41,
      cleared_total: 900_000,
      outstanding_count: 3,
      outstanding_total: 50_000,
      difference,
      status: 'IN_PROGRESS',
      created_at: '2026-08-01T00:00:00Z',
    }
  }

  it('converts a statement balance from cents to dollars', async () => {
    serve({ '/bankrecon/sessions': [session(1_234_567, 0)], '/bankrecon/accounts': [] })
    const el = await mountAsync<LitElement>('gable-bank-reconciliation')

    expect(text(el)).toContain('$12,345.67')
    expect(text(el)).not.toContain('$1,234,567.00')
  })

  it('marks a negative difference "DR" and drops the minus sign', async () => {
    // This page uses debit/credit notation instead of a sign, so -$500.00 reads
    // "$500.00 DR" here and "-$500.00" everywhere else in the ERP. The notation
    // lives at the call site (`_formatBalance`); the digits come from the shared
    // `formatCents()`.
    serve({ '/bankrecon/sessions': [session(1_234_567, -50_000)], '/bankrecon/accounts': [] })
    const el = await mountAsync<LitElement>('gable-bank-reconciliation')

    expect(text(el)).toContain('500.00 DR')
    expect(text(el)).not.toContain('-500.00')
  })

  it('leaves a positive difference unannotated', async () => {
    serve({ '/bankrecon/sessions': [session(1_234_567, 50_000)], '/bankrecon/accounts': [] })
    const el = await mountAsync<LitElement>('gable-bank-reconciliation')

    const diff = amounts(el).filter((a) => a.includes('500.00'))
    expect(diff.every((a) => !a.endsWith('DR'))).toBe(true)
  })

  // Regression (app/src/pages/accounting/BankReconciliation.ts): the private
  // helper used `.toFixed(2)` with no locale formatting, so a bank balance
  // rendered "$12345.67" with no thousands separator — on the one screen whose
  // entire job is comparing two large numbers digit by digit. CLAUDE.md says ERP
  // pages must use `formatCents()`, which groups.
  it('groups thousands on a bank balance', async () => {
    serve({ '/bankrecon/sessions': [session(1_234_567, 0)], '/bankrecon/accounts': [] })
    const el = await mountAsync<LitElement>('gable-bank-reconciliation')

    expect(text(el)).toContain('$12,345.67')
    expect(text(el)).not.toContain('$12345.67')
  })
})

describe('PO matching', () => {
  const config: MatchConfig = {
    id: 'cfg-1',
    qty_tolerance_pct: 2,
    price_tolerance_pct: 1.5,
    dollar_tolerance: 1_234_567, // $12,345.67 in cents
    auto_approve_on_match: false,
    updated_at: '2026-08-11T14:00:00Z',
  }

  it('shows the dollar tolerance in dollars, not in cents', async () => {
    // Round-tripped: the field divides by 100 on read and multiplies on change,
    // so a broken conversion here silently rewrites the tolerance on next save.
    serve({ '/matching/config': config, '/matching/exceptions': [] })
    const el = await mountAsync<LitElement>('gable-po-matching')

    const inputs = Array.from(el.querySelectorAll<HTMLInputElement>('input[type="number"]'))
    const tolerance = inputs.at(-1)!
    expect(tolerance.value).toBe('12345.67')
  })

  /** Type `value` into the dollar-tolerance field and return what was PUT. */
  async function saveTolerance(value: string): Promise<Record<string, unknown>[]> {
    const calls: Record<string, unknown>[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string, init: RequestInit = {}) => {
        if (init.body) calls.push(JSON.parse(init.body as string) as Record<string, unknown>)
        if (url.includes('/matching/config')) return Promise.resolve(jsonResponse(config))
        return Promise.resolve(jsonResponse([]))
      }),
    )
    const el = await mountAsync<LitElement>('gable-po-matching')

    const tolerance = Array.from(
      el.querySelectorAll<HTMLInputElement>('input[type="number"]'),
    ).at(-1)!
    tolerance.value = value
    tolerance.dispatchEvent(new Event('change', { bubbles: true }))
    await el.updateComplete
    return calls
  }

  // Regression (app/src/pages/accounting/POMatching.ts): the change handler used
  // to send `Math.round(dollars * 100)` — cents — but the endpoint takes DOLLARS.
  // `matching/model.go` declares `DollarTolerance *float64 // dollars, converted
  // to cents` on `UpdateMatchConfigRequest`, and `matching/service.go` applies it
  // with `money.DollarsToCents(*req.DollarTolerance)`. The value was therefore
  // scaled by 100 twice: a tolerance typed as $73.88 was stored as 7,388 dollars
  // = 738,800 cents.
  //
  // This is not cosmetic. `dollar_tolerance` is the absolute-dollar floor beside
  // the percentage check in the three-way match (`matching/service.go`,
  // `if !priceOK && abs64(lineAmountDiffCents(detail)) <= cfg.DollarTolerance`):
  // a tolerance 100x too large makes PO matching wave through the vendor
  // overbilling it exists to catch, and (with auto-approve on) post it. The round
  // trip was visibly wrong too — after saving, the field re-read as "7388.00".
  //
  // The read side keeps its `/100`, which is correct: the GET returns int64 cents
  // (`MatchConfig.DollarTolerance int64 // cents`).
  it('sends the tolerance in the dollars the API documents', async () => {
    expect(await saveTolerance('73.88')).toContainEqual({ dollar_tolerance: 73.88 })
  })

  it('round-trips the tolerance the backend would store', async () => {
    // What the backend does with what we send: money.DollarsToCents(73.88) =
    // 7388 cents, which the read side renders back as "73.88". Sending cents
    // instead re-read as "7388.00" — the visible symptom of the double scale.
    const [sent] = await saveTolerance('73.88')
    const storedCents = Math.round((sent.dollar_tolerance as number) * 100)
    expect(storedCents).toBe(7388)
    expect((storedCents / 100).toFixed(2)).toBe('73.88')
  })
})
