// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { components } from '@gable/api-client'

export type PortalMe = components['schemas']['PortalMe']
export type PortalCapabilities = components['schemas']['PortalCapabilities']
export type PortalSignInMethod = 'password' | 'magic_link' | 'oidc' | 'passkey' | 'google' | 'microsoft'

export interface PortalOidcProvider {
  id?: string
  name?: string
  label?: string
  authorize_url?: string
  url?: string
}

export interface PortalOidcConfig {
  enabled?: boolean
  label?: string
  authorize_url?: string
  providers?: PortalOidcProvider[]
}

export type PortalConfig = components['schemas']['PortalConfig'] & {
  sign_in_methods?: PortalSignInMethod[]
  oidc?: PortalOidcConfig | PortalOidcProvider[] | null
}
export type PortalToday = components['schemas']['PortalToday']
export type PortalOrder = components['schemas']['PortalOrder']
export type PortalInvoice = components['schemas']['PortalInvoice']
export type PortalDelivery = components['schemas']['PortalDelivery']
export type PortalQuote = components['schemas']['PortalQuote']
export type PortalCatalogProduct = components['schemas']['PortalCatalogProduct'] & {
  availability?: PortalYardAvailability[]
}
export type PortalCatalogDetail = components['schemas']['PortalCatalogDetail'] & {
  availability?: PortalYardAvailability[]
}
export type PortalCategoryNode = components['schemas']['PortalCategoryNode']
export type PortalVolumeBreak = components['schemas']['PortalVolumeBreak']
export type PortalCart = components['schemas']['PortalCart']
export type PortalOrderTimeline = Omit<components['schemas']['PortalOrderTimeline'], 'pod'> & {
  pod: components['schemas']['PortalTimelinePOD'] | null
}
export type PortalScheduleSlot = components['schemas']['PortalScheduleSlot']
export type PortalScheduleHold = components['schemas']['PortalScheduleHold']
export type CustomerQuoteView = components['schemas']['CustomerQuoteView']
export type CustomerQuoteSummary = components['schemas']['CustomerQuoteSummary']
export type CustomerQuoteInput = components['schemas']['CustomerQuoteInput']
export type CustomerQuotePublicView = components['schemas']['CustomerQuotePublicView']
export type CustomerQuoteConfirmPreview = components['schemas']['CustomerQuoteConfirmPreview']
export type ProjectPage = components['schemas']['ProjectPage']
export type Project = components['schemas']['Project']

export interface PortalYardAvailability {
  yard_id: string
  yard_code: string
  yard_name: string
  on_hand: number
  available: number
  in_stock: boolean
  lead_time_days: number | null
}

export interface PortalReorderLineDiff {
  line_id: string
  product_id?: string | null
  sku: string
  description: string
  quantity: number
  uom: string
  original_unit_price_cents: number
  current_unit_price_cents: number
  price_diff_cents: number
  original_line_total_cents: number
  current_line_total_cents: number
  line_diff_cents: number
  price_source: string
  available_qty: number
  in_stock: boolean
}

export interface PortalReorderPreview {
  source_order_id: string
  project_id?: string | null
  lines: PortalReorderLineDiff[]
  original_total_cents: number
  current_total_cents: number
  total_diff_cents: number
}

export interface PortalProcurementLine {
  id: string
  project_id: string
  product_id?: string | null
  product_sku?: string
  product_name?: string
  description: string
  qty_entered: number
  uom: string
  notes: string
  unit_price_cents?: number
  line_total_cents?: number
  available_qty?: number
  in_stock?: boolean
  created_at: string
}

export interface PortalProcurementProject {
  id: string
  name: string
  status: string
  created_by?: string | null
  quote_id?: string | null
  created_at: string
  updated_at: string
  lines: PortalProcurementLine[]
}

export interface PortalQuoteIntakeItem {
  raw_text: string
  parsed_qty: number
  parsed_unit: string
  parsed_description: string
  matched_product_id?: string | null
  matched_product_name?: string
  matched_sku?: string
  confidence: string
}

export interface PortalQuoteIntakeResponse {
  source: string
  synthetic: boolean
  items: PortalQuoteIntakeItem[]
  quote?: PortalQuote | null
}

export interface PortalQuoteConvertResult {
  quote_id: string
  order_id: string
  order_number: string
  status: string
}

export interface PortalBalanceTiles {
  currency: string
  current_balance_cents: number
  credit_limit_cents: number
  available_credit_cents: number
  past_due_cents: number
  unapplied_cash_cents: number
  net_balance_cents: number
  open_invoice_count: number
  overdue_invoice_count: number
}

export interface PortalAgingBuckets {
  as_of: string
  currency: string
  current_cents: number
  days_1_30_cents: number
  days_31_60_cents: number
  days_61_90_cents: number
  days_over_90_cents: number
  unapplied_cents: number
  total_open_cents: number
  net_cents: number
}

export interface PortalJobAccountSummary {
  project_id: string
  project_name: string
  status: string
  budget_cents: number
  quoted_cents: number
  ordered_cents: number
  invoiced_cents: number
  paid_cents: number
  open_balance_cents: number
  quote_variance_cents: number
  order_count: number
  invoice_count: number
  delivery_count: number
  delivered_count: number
}

export interface PortalBillingSummary {
  balance_tiles: PortalBalanceTiles
  aging: PortalAgingBuckets
  job_summaries: PortalJobAccountSummary[]
}

export interface PortalStatementLine {
  id: string
  date: string
  type: string
  description: string
  amount_cents: number
  balance_after_cents: number
  reference_id?: string | null
  job_id?: string | null
}

export interface PortalStatementOpenInvoice {
  kind: string
  id: string
  number: string
  date: string
  due_date?: string | null
  job_id?: string | null
  total_cents: number
  open_cents: number
}

export interface PortalStatement {
  customer_id: string
  customer_name: string
  from: string
  to: string
  currency: string
  opening_balance_cents: number
  closing_balance_cents: number
  unapplied_cents: number
  net_balance_cents: number
  aging: PortalAgingBuckets
  lines: PortalStatementLine[]
  open_invoices: PortalStatementOpenInvoice[]
}

export interface PortalStatementRunItem {
  id: string
  statement_run_id: string
  customer_id: string
  period_start: string
  period_end: string
  opening_balance_cents: number
  closing_balance_cents: number
  total_debits_cents: number
  total_credits_cents: number
  current_cents: number
  days_1_30_cents: number
  days_31_60_cents: number
  days_61_90_cents: number
  days_over_90_cents: number
  created_at: string
}

export interface PortalPaymentMethod {
  id: string
  customer_id: string
  gateway: string
  method_type: 'CARD' | 'BANK' | string
  brand?: string
  last4: string
  exp_month?: number | null
  exp_year?: number | null
  is_default: boolean
  status: string
  created_at: string
}

export interface PortalPaymentAllocation {
  id: string
  invoice_id: string
  invoice_number: string
  amount_cents: number
  applied_on: string
  created_at: string
}

export interface PortalPayment {
  id: string
  number: string
  customer_id: string
  currency: string
  method: string
  status: string
  amount_cents: number
  unapplied_cents: number
  reference?: string
  card_brand?: string
  card_last4?: string
  received_on: string
  created_at: string
  allocations: PortalPaymentAllocation[]
}

