// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { CustomerQuoteInput, CustomerQuoteView } from '../types.ts'

export type QuoteMarkup = NonNullable<CustomerQuoteInput['markup']>

export function emptyQuoteInput(): CustomerQuoteInput {
  return {
    title: '',
    project_id: null,
    source_quote_id: null,
    job_site_address: '',
    end_customer: { name: '', email: '', phone: '' },
    site_contact: { name: '', email: '', phone: '' },
    markup: { kind: 'markup', bps: 1800 },
    hide_line_prices: false,
    tax: { mode: 'site_rate', rate_bps: 0 },
    payment_terms: '',
    deposit_bps: 0,
    customer_note: '',
    require_code: false,
    valid_until: null,
    groups: [],
    lines: [],
    category_rules: [],
    labour: [],
    overhead: [],
  }
}

export function quoteToInput(quote: CustomerQuoteView): CustomerQuoteInput {
  return {
    title: quote.title,
    project_id: quote.project_id,
    source_quote_id: quote.source_quote_id,
    job_site_address: quote.job_site_address,
    end_customer: { ...quote.end_customer },
    site_contact: { ...quote.site_contact },
    markup: { ...quote.markup },
    hide_line_prices: quote.hide_line_prices,
    tax: { ...quote.tax },
    payment_terms: quote.payment_terms,
    deposit_bps: quote.deposit_bps,
    customer_note: quote.customer_note,
    require_code: quote.require_code,
    valid_until: quote.valid_until,
    groups: quote.groups.map((group) => ({ key: group.id, label: group.label })),
    lines: quote.lines.map((line) => ({
      id: line.id,
      product_id: line.product_id,
      quantity: line.quantity,
      group_key: line.group_id,
      markup: line.markup === null ? null : { ...line.markup },
      note: line.note,
    })),
    category_rules: quote.category_rules.map((rule) => ({ category_id: rule.category_id, markup: { ...rule.markup } })),
    labour: quote.labour.map((line) => ({
      description: line.description,
      rate_type: line.rate_type,
      rate_cents: line.rate_cents,
      hours: line.hours,
      contractor_only: line.contractor_only,
    })),
    overhead: quote.overhead.map((line) => ({
      label: line.label,
      amount_type: line.amount_type,
      value: line.value,
      contractor_only: line.contractor_only,
    })),
  }
}

export function percentToBps(value: string, fallback = 0): number {
  if (value.trim() === '') return fallback
  const parsed = Number(value)
  return Number.isFinite(parsed) ? Math.round(parsed * 100) : fallback
}

export function bpsToPercent(value: number | null | undefined): string {
  return value === null || value === undefined ? '' : String(value / 100)
}

export function revisionHeader(quote: CustomerQuoteView): string {
  return `"${quote.revision}"`
}
