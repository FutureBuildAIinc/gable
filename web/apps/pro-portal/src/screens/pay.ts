// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing, type TemplateResult } from 'lit'
import { customElement, property, state } from 'lit/decorators.js'
import { icon } from '@gable/design-system'
import { Check, CreditCard, Download, Landmark, ReceiptText, ShieldCheck, Star, Trash2 } from 'lucide'
import { can } from '../lib/capabilities.ts'
import { portalDownload, portalRequest, readCached } from '../lib/api.ts'
import { calendarDay, formatCents, formatDollars, shortDate } from '../lib/format.ts'
import { OFFLINE_WRITE_REASON } from '../lib/offline.ts'
import { statusChip } from '../lib/status.ts'
import type {
  PortalBillingSummary,
  PortalInvoice,
  PortalMe,
  PortalPayment,
  PortalPaymentMethod,
  PortalStatement,
  PortalStatementRunItem,
} from '../types.ts'
import '../components/pro-select.ts'

type PayView = 'invoices' | 'statement' | 'jobs' | 'methods'

function invoiceOpenCents(invoice: PortalInvoice, summaryOpenMap: Map<string, number>): number {
  const fromStatement = summaryOpenMap.get(invoice.id)
  if (typeof fromStatement === 'number') return fromStatement
  return Math.round(invoice.total_amount * 100)
}

function isInvoicePayable(invoice: PortalInvoice): boolean {
  const upper = invoice.status.toUpperCase()
  return upper === 'UNPAID' || upper === 'PARTIAL' || upper === 'OPEN'
}

@customElement('gable-pro-pay')
export class GableProPay extends LitElement {
  createRenderRoot() { return this }

  @property({ attribute: false }) me!: PortalMe
  @property({ type: Boolean }) offline = false

  @state() private view: PayView = 'invoices'
  @state() private loading = true
  @state() private busy = false
  @state() private error = ''
  @state() private message = ''

  @state() private summary: PortalBillingSummary | null = null
  @state() private invoices: PortalInvoice[] = []
  @state() private selectedInvoiceIds: string[] = []
  @state() private statement: PortalStatement | null = null
  @state() private statementRuns: PortalStatementRunItem[] = []
  @state() private statementFrom = ''
  @state() private statementTo = ''

  @state() private methods: PortalPaymentMethod[] = []
  @state() private payments: PortalPayment[] = []
  @state() private selectedMethodId = ''
  @state() private useHostedToken = false
  @state() private hostedPreset: 'visa' | 'ach' = 'visa'

  @state() private newMethodType: 'CARD' | 'BANK' = 'CARD'
  @state() private newMethodBrand = 'Visa'
  @state() private newMethodLast4 = '4242'
  @state() private newMethodDefault = true

  connectedCallback(): void {
    super.connectedCallback()
    void this.loadAll()
  }

  private get canSeeMoney(): boolean {
    return can(this.me?.capabilities ?? null, 'see_money')
  }

  private get canPay(): boolean {
    return can(this.me?.capabilities ?? null, 'pay')
  }

  private async loadAll(): Promise<void> {
    if (!this.canSeeMoney) {
      this.loading = false
      return
    }
    this.loading = true
    this.error = ''
    try {
      const [sumRes, invRes, stmtRes, runsRes, methRes, payRes] = await Promise.all([
        readCached<PortalBillingSummary>('billing:summary', '/api/portal/v1/billing/summary'),
        readCached<PortalInvoice[]>('invoices', '/api/portal/v1/invoices', { feed: true }),
        readCached<PortalStatement>('billing:statement', '/api/portal/v1/statement'),
        readCached<PortalStatementRunItem[]>('billing:statement-runs', '/api/portal/v1/statements').catch(() => ({ data: [], fromCache: false, offline: false })),
        readCached<PortalPaymentMethod[]>('payment-methods', '/api/portal/v1/payment-methods').catch(() => ({ data: [], fromCache: false, offline: false })),
        readCached<PortalPayment[]>('payments', '/api/portal/v1/payments').catch(() => ({ data: [], fromCache: false, offline: false })),
      ])
      this.summary = sumRes.data
      this.invoices = invRes.data
      this.statement = stmtRes.data
      this.statementFrom = stmtRes.data.from
      this.statementTo = stmtRes.data.to
      this.statementRuns = runsRes.data
      this.methods = methRes.data
      this.payments = payRes.data

      const defaultMethod = this.methods.find((item) => item.is_default) ?? this.methods[0]
      if (defaultMethod && this.selectedMethodId === '') {
        this.selectedMethodId = defaultMethod.id
      }

      const params = new URLSearchParams(window.location.search)
      const askedInvoice = params.get('invoice')
      if (askedInvoice && this.invoices.some((inv) => inv.id === askedInvoice && isInvoicePayable(inv))) {
        this.selectedInvoiceIds = [askedInvoice]
      } else if (this.selectedInvoiceIds.length === 0) {
        const firstOpen = this.invoices.find((inv) => isInvoicePayable(inv))
        if (firstOpen) this.selectedInvoiceIds = [firstOpen.id]
      }
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Billing and payment data could not be loaded.'
    } finally {
      this.loading = false
    }
  }

  private openBalanceMap(): Map<string, number> {
    const map = new Map<string, number>()
    for (const row of this.statement?.open_invoices ?? []) {
      map.set(row.id, row.open_cents)
    }
    return map
  }

  private toggleInvoice(invoiceId: string, checked: boolean): void {
    if (checked) {
      if (!this.selectedInvoiceIds.includes(invoiceId)) {
        this.selectedInvoiceIds = [...this.selectedInvoiceIds, invoiceId]
      }
    } else {
      this.selectedInvoiceIds = this.selectedInvoiceIds.filter((id) => id !== invoiceId)
    }
  }

  private selectAllOpenInvoices(): void {
    this.selectedInvoiceIds = this.invoices.filter((inv) => isInvoicePayable(inv)).map((inv) => inv.id)
  }

  private selectedTotalCents(): number {
    const openMap = this.openBalanceMap()
    return this.invoices
      .filter((inv) => this.selectedInvoiceIds.includes(inv.id) && isInvoicePayable(inv))
      .reduce((sum, inv) => sum + invoiceOpenCents(inv, openMap), 0)
  }

  private async submitPayment(): Promise<void> {
    if (!this.canPay || this.offline || this.busy || this.selectedInvoiceIds.length === 0) return
    this.busy = true
    this.error = ''
    this.message = ''
    try {
      const idempotencyKey = crypto.randomUUID()
      const payload = this.useHostedToken || this.selectedMethodId === ''
        ? {
            invoice_ids: this.selectedInvoiceIds,
            token: this.hostedPreset === 'ach' ? 'tok_ach_teststub' : 'tok_visa_teststub',
            idempotency_key: idempotencyKey,
          }
        : {
            invoice_ids: this.selectedInvoiceIds,
            payment_method_id: this.selectedMethodId,
            idempotency_key: idempotencyKey,
          }
      const payment = await portalRequest<PortalPayment>('POST', '/api/portal/v1/payments', payload, { idempotencyKey })
      this.payments = [payment, ...this.payments.filter((item) => item.id !== payment.id)]
      this.message = `Payment ${payment.number} for ${formatCents(payment.amount_cents)} applied across ${payment.allocations.length} ${payment.allocations.length === 1 ? 'invoice' : 'invoices'}.`
      await this.loadAll()
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Payment could not be processed.'
    } finally {
      this.busy = false
    }
  }

  private async refreshStatement(): Promise<void> {
    this.error = ''
    try {
      const res = await readCached<PortalStatement>(
        `billing:statement:${this.statementFrom}:${this.statementTo}`,
        '/api/portal/v1/statement',
        { query: { from: this.statementFrom || undefined, to: this.statementTo || undefined } },
      )
      this.statement = res.data
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Statement could not be loaded.'
    }
  }

  private async downloadPdf(path: string, filename: string): Promise<void> {
    this.error = ''
    try {
      await portalDownload(path, filename)
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'PDF download failed.'
    }
  }

  private async addPaymentMethod(): Promise<void> {
    if (!this.canPay || this.offline || this.busy) return
    const last4 = this.newMethodLast4.replace(/\D/g, '').slice(-4)
    if (last4.length !== 4) {
      this.error = 'Hosted token verification requires the 4-digit account suffix.'
      return
    }
    this.busy = true
    this.error = ''
    this.message = ''
    try {
      const tokenStub = this.newMethodType === 'BANK' ? `tok_ach_${last4}_teststub` : `tok_card_${last4}_teststub`
      const created = await portalRequest<PortalPaymentMethod>(
        'POST',
        '/api/portal/v1/payment-methods',
        {
          gateway_token: tokenStub,
          method_type: this.newMethodType,
          brand: this.newMethodBrand.trim() || (this.newMethodType === 'BANK' ? 'ACH' : 'Card'),
          last4,
          exp_month: this.newMethodType === 'CARD' ? 12 : undefined,
          exp_year: this.newMethodType === 'CARD' ? 2029 : undefined,
          is_default: this.newMethodDefault,
        },
        { idempotencyKey: crypto.randomUUID() },
      )
      const list = await portalRequest<PortalPaymentMethod[]>('GET', '/api/portal/v1/payment-methods')
      this.methods = list
      this.selectedMethodId = created.id
      this.message = `Saved ${created.brand || created.method_type} ending in ${created.last4}.`
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Payment method could not be saved.'
    } finally {
      this.busy = false
    }
  }

  private async setDefaultMethod(methodId: string): Promise<void> {
    if (!this.canPay || this.offline || this.busy) return
    this.busy = true
    this.error = ''
    try {
      await portalRequest('PUT', `/api/portal/v1/payment-methods/${methodId}/default`, {}, { idempotencyKey: crypto.randomUUID() })
      this.methods = await portalRequest<PortalPaymentMethod[]>('GET', '/api/portal/v1/payment-methods')
      this.selectedMethodId = methodId
      this.message = 'Default payment method updated.'
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Could not update default payment method.'
    } finally {
      this.busy = false
    }
  }

  private async deleteMethod(methodId: string): Promise<void> {
    if (!this.canPay || this.offline || this.busy) return
    this.busy = true
    this.error = ''
    try {
      await portalRequest('DELETE', `/api/portal/v1/payment-methods/${methodId}`)
      this.methods = this.methods.filter((item) => item.id !== methodId)
      if (this.selectedMethodId === methodId) {
        this.selectedMethodId = this.methods[0]?.id ?? ''
      }
      this.message = 'Saved payment method removed.'
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Could not remove payment method.'
    } finally {
      this.busy = false
    }
  }

  private renderBalanceAndAging(): TemplateResult | typeof nothing {
    const summary = this.summary
    if (summary === null) return nothing
    const tiles = summary.balance_tiles
    const aging = summary.aging
    return html`
      <div class="pro-stat-grid mt-4" data-testid="pay-balance-tiles">
        <div class="pro-stat">
          <span class="pro-stat-value" data-testid="tile-current-balance">${formatCents(tiles.current_balance_cents)}</span>
          <span class="pro-stat-label">Current balance (${tiles.open_invoice_count} open)</span>
        </div>
        <div class="pro-stat">
          <span class="pro-stat-value" data-testid="tile-past-due">${formatCents(tiles.past_due_cents)}</span>
          <span class="pro-stat-label">Past due (${tiles.overdue_invoice_count} overdue)</span>
        </div>
        <div class="pro-stat">
          <span class="pro-stat-value" data-testid="tile-available-credit">${formatCents(tiles.available_credit_cents)}</span>
          <span class="pro-stat-label">Available credit (limit ${formatCents(tiles.credit_limit_cents)})</span>
        </div>
        <div class="pro-stat">
          <span class="pro-stat-value" data-testid="tile-net-balance">${formatCents(tiles.net_balance_cents)}</span>
          <span class="pro-stat-label">Net balance (${formatCents(tiles.unapplied_cash_cents)} unapplied)</span>
        </div>
      </div>

      <section class="pro-card mt-3" data-testid="pay-aging-strip">
        <div class="pro-row">
          <div class="pro-row-grow">
            <h2 class="pro-card-title">Account aging buckets</h2>
            <p class="pro-text-small">As of ${calendarDay(aging.as_of)} · All figures in integer cents</p>
          </div>
          <span class="pro-money">${formatCents(aging.total_open_cents)} open</span>
        </div>
        <div class="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-5">
          <div class="pro-stat"><span class="pro-stat-value text-sm" data-testid="aging-current">${formatCents(aging.current_cents)}</span><span class="pro-stat-label">Current</span></div>
          <div class="pro-stat"><span class="pro-stat-value text-sm" data-testid="aging-1-30">${formatCents(aging.days_1_30_cents)}</span><span class="pro-stat-label">1 to 30 days</span></div>
          <div class="pro-stat"><span class="pro-stat-value text-sm" data-testid="aging-31-60">${formatCents(aging.days_31_60_cents)}</span><span class="pro-stat-label">31 to 60 days</span></div>
          <div class="pro-stat"><span class="pro-stat-value text-sm" data-testid="aging-61-90">${formatCents(aging.days_61_90_cents)}</span><span class="pro-stat-label">61 to 90 days</span></div>
          <div class="pro-stat"><span class="pro-stat-value text-sm" data-testid="aging-over-90">${formatCents(aging.days_over_90_cents)}</span><span class="pro-stat-label">Over 90 days</span></div>
        </div>
      </section>
    `
  }

  private renderViewTabs(): TemplateResult {
    const tabs: Array<{ key: PayView; label: string }> = [
      { key: 'invoices', label: `Invoices and pay (${this.invoices.length})` },
      { key: 'statement', label: 'Account statement' },
      { key: 'jobs', label: `Job balances (${this.summary?.job_summaries.length ?? 0})` },
      { key: 'methods', label: `Payment methods (${this.methods.length})` },
    ]
    return html`<div class="pro-tabs mt-4" role="tablist" aria-label="Pay views">
      ${tabs.map((tab) => html`<button
        type="button"
        role="tab"
        aria-selected=${this.view === tab.key ? 'true' : 'false'}
        class="pro-tab ${this.view === tab.key ? 'pro-tab-active' : ''}"
        data-testid=${`pay-tab-${tab.key}`}
        @click=${() => { this.view = tab.key }}
      >${tab.label}</button>`)}
    </div>`
  }

  private renderInvoicesView(): TemplateResult {
    const openMap = this.openBalanceMap()
    const selectedCents = this.selectedTotalCents()
    return html`<div class="pro-two-col" data-testid="pay-invoices-view">
      <div class="grid gap-3">
        <div class="pro-row">
          <h2 class="pro-card-title">Invoices on your account</h2>
          <span class="pro-row-grow"></span>
          <button type="button" class="pro-btn pro-btn-secondary pro-btn-small" data-testid="select-all-open-btn" @click=${() => this.selectAllOpenInvoices()}>
            Select all open
          </button>
        </div>
        <div class="pro-list" data-testid="pay-invoice-list">
          ${this.invoices.length === 0
            ? html`<div class="pro-empty"><p class="pro-card-title">No invoices on your account.</p></div>`
            : this.invoices.map((invoice) => {
                const payable = isInvoicePayable(invoice)
                const checked = this.selectedInvoiceIds.includes(invoice.id)
                const openCents = invoiceOpenCents(invoice, openMap)
                return html`<article class="pro-card" data-testid=${`invoice-row-${invoice.id}`}>
                  <div class="pro-row">
                    ${payable
                      ? html`<input
                          type="checkbox"
                          class="pro-check"
                          aria-label=${`Select invoice ${invoice.number}`}
                          data-testid=${`select-invoice-${invoice.id}`}
                          .checked=${checked}
                          ?disabled=${!this.canPay || this.offline || this.busy}
                          @change=${(event: Event) => this.toggleInvoice(invoice.id, (event.target as HTMLInputElement).checked)}
                        />`
                      : nothing}
                    <div class="pro-row-grow">
                      <p class="pro-page-kicker pro-data">${invoice.number} · ${invoice.payment_terms}</p>
                      <h3 class="pro-card-title">Due ${shortDate(invoice.due_date)}</h3>
                      <p class="pro-text-small">Total ${formatDollars(invoice.total_amount)} (Tax ${formatDollars(invoice.tax_amount)})</p>
                    </div>
                    <div class="text-right">
                      <div class="pro-money text-lg" data-testid=${`invoice-open-${invoice.id}`}>${payable ? formatCents(openCents) : formatDollars(invoice.total_amount)}</div>
                      <div class="mt-1 flex justify-end gap-1">
                        ${invoice.is_overdue ? statusChip('OVERDUE') : nothing}
                        ${statusChip(invoice.status)}
                      </div>
                    </div>
                  </div>
                  <div class="pro-row mt-3">
                    <span class="pro-text-small">${invoice.lines.length} ${invoice.lines.length === 1 ? 'line' : 'lines'}</span>
                    <span class="pro-row-grow"></span>
                    <button
                      type="button"
                      class="pro-btn pro-btn-ghost pro-btn-small"
                      data-testid=${`download-invoice-pdf-${invoice.id}`}
                      @click=${() => void this.downloadPdf(`/api/portal/v1/invoices/${invoice.id}/pdf`, `${invoice.number}.pdf`)}
                    >${icon(Download, 14)} Invoice PDF</button>
                  </div>
                </article>`
              })}
        </div>
      </div>

      <div class="grid gap-3">
        <section class="pro-card" data-testid="pay-checkout-card">
          <div class="pro-row">
            ${icon(ShieldCheck, 18, 'text-gable-green')}
            <div class="pro-row-grow">
              <h2 class="pro-card-title">Single-flight bill pay</h2>
              <p class="pro-text-small">Pay one or several open invoices at once. Tokenized via hosted gateway; raw card or bank numbers never touch this page.</p>
            </div>
          </div>

          <div class="pro-summary-grid">
            <div class="pro-stat">
              <span class="pro-stat-value" data-testid="pay-selected-total">${formatCents(selectedCents)}</span>
              <span class="pro-stat-label">${this.selectedInvoiceIds.length} ${this.selectedInvoiceIds.length === 1 ? 'invoice' : 'invoices'} selected</span>
            </div>
          </div>

          ${!this.canPay
            ? html`<div class="pro-notice mt-3" data-testid="pay-capability-notice">Your role can review balances, statements, and invoice PDFs. Ask an owner or accounts payable user to submit payments.</div>`
            : nothing}

          <fieldset class="mt-4" ?disabled=${!this.canPay || this.offline || this.busy}>
            <label class="pro-field">
              <span class="pro-label">Payment source</span>
              <pro-select
                data-testid="pay-method-select"
                aria-label="Payment source"
                .value=${this.useHostedToken || this.methods.length === 0 ? '__hosted__' : this.selectedMethodId}
                .options=${[
                  ...this.methods.map((method) => ({
                    value: method.id,
                    label: `${method.brand || method.method_type} ending in ${method.last4}${method.is_default ? ' (Default)' : ''}`,
                  })),
                  { value: '__hosted__', label: 'One-time hosted gateway token' },
                ]}
                ?disabled=${!this.canPay || this.offline || this.busy}
                @change=${(event: Event) => {
                  const val = (event.target as HTMLElement & { value: string }).value
                  if (val === '__hosted__') {
                    this.useHostedToken = true
                  } else {
                    this.useHostedToken = false
                    this.selectedMethodId = val
                  }
                }}
              ></pro-select>
            </label>

            ${this.useHostedToken || this.methods.length === 0
              ? html`<label class="pro-field">
                  <span class="pro-label">Hosted gateway token profile</span>
                  <pro-select
                    data-testid="pay-hosted-preset"
                    aria-label="Hosted gateway token profile"
                    .value=${this.hostedPreset}
                    .options=${[
                      { value: 'visa', label: 'Hosted Card Token (Visa **** 4242)' },
                      { value: 'ach', label: 'Hosted Bank ACH Token (Checking **** 6789)' },
                    ]}
                    ?disabled=${!this.canPay || this.offline || this.busy}
                    @change=${(event: Event) => { this.hostedPreset = (event.target as HTMLElement & { value: string }).value === 'ach' ? 'ach' : 'visa' }}
                  ></pro-select>
                </label>`
              : nothing}

            <button
              type="button"
              class="pro-btn pro-btn-primary pro-btn-wide mt-2"
              data-testid="submit-payment-btn"
              ?disabled=${!this.canPay || this.offline || this.busy || this.selectedInvoiceIds.length === 0 || selectedCents <= 0}
              @click=${() => void this.submitPayment()}
            >${icon(Check, 16)} Pay ${formatCents(selectedCents)} now</button>
          </fieldset>
        </section>

        <section class="pro-card" data-testid="pay-recent-payments">
          <h2 class="pro-card-title">Recent payments and allocations</h2>
          <div class="pro-line-list mt-3">
            ${this.payments.length === 0
              ? html`<p class="pro-text-small py-2">No payments recorded yet.</p>`
              : this.payments.map((payment) => html`<div class="pro-line" data-testid=${`payment-row-${payment.id}`}>
                  <div>
                    <div class="pro-line-title pro-data">${payment.number} · ${payment.card_brand || payment.method}${payment.card_last4 ? ` **** ${payment.card_last4}` : ''}</div>
                    <div class="pro-line-meta">
                      ${calendarDay(payment.received_on)} · Applied to ${payment.allocations.map((alloc) => `${alloc.invoice_number} (${formatCents(alloc.amount_cents)})`).join(', ') || 'account'}
                    </div>
                  </div>
                  <div class="text-right">
                    <span class="pro-money block">${formatCents(payment.amount_cents)}</span>
                    ${statusChip(payment.status)}
                  </div>
                </div>`)}
          </div>
        </section>
      </div>
    </div>`
  }

  private renderStatementView(): TemplateResult {
    const stmt = this.statement
    return html`<div class="pro-two-col" data-testid="pay-statement-view">
      <div class="grid gap-3">
        <section class="pro-card">
          <div class="pro-row flex-wrap">
            <div class="pro-row-grow">
              <p class="pro-page-kicker">Live statement</p>
              <h2 class="pro-card-title">${stmt?.customer_name || this.me.account.name}</h2>
            </div>
            <button
              type="button"
              class="pro-btn pro-btn-secondary pro-btn-small"
              data-testid="download-statement-pdf"
              @click=${() => void this.downloadPdf(`/api/portal/v1/statement/pdf?from=${encodeURIComponent(this.statementFrom)}&to=${encodeURIComponent(this.statementTo)}`, 'statement.pdf')}
            >${icon(Download, 14)} Download statement PDF</button>
          </div>

          <div class="pro-form-grid mt-3">
            <label class="pro-field">
              <span class="pro-label">From date</span>
              <input
                class="pro-input pro-data"
                type="text"
                placeholder="YYYY-MM-DD"
                data-testid="statement-from"
                .value=${this.statementFrom}
                @change=${(event: Event) => {
                  this.statementFrom = (event.target as HTMLInputElement).value
                  void this.refreshStatement()
                }}
              />
            </label>
            <label class="pro-field">
              <span class="pro-label">To date</span>
              <input
                class="pro-input pro-data"
                type="text"
                placeholder="YYYY-MM-DD"
                data-testid="statement-to"
                .value=${this.statementTo}
                @change=${(event: Event) => {
                  this.statementTo = (event.target as HTMLInputElement).value
                  void this.refreshStatement()
                }}
              />
            </label>
          </div>

          ${stmt !== null
            ? html`
                <div class="mt-2 grid grid-cols-3 gap-2">
                  <div class="pro-stat"><span class="pro-stat-value text-sm">${formatCents(stmt.opening_balance_cents)}</span><span class="pro-stat-label">Opening</span></div>
                  <div class="pro-stat"><span class="pro-stat-value text-sm">${formatCents(stmt.closing_balance_cents)}</span><span class="pro-stat-label">Closing</span></div>
                  <div class="pro-stat"><span class="pro-stat-value text-sm" data-testid="statement-net">${formatCents(stmt.net_balance_cents)}</span><span class="pro-stat-label">Net balance</span></div>
                </div>
                <div class="pro-line-list mt-4" data-testid="statement-lines">
                  ${stmt.lines.map((line) => html`<div class="pro-line">
                    <div>
                      <div class="pro-line-title">${line.description}</div>
                      <div class="pro-line-meta pro-data">${calendarDay(line.date)} · ${line.type}</div>
                    </div>
                    <div class="text-right">
                      <span class="pro-money block">${formatCents(line.amount_cents)}</span>
                      <span class="pro-text-small pro-data">Bal ${formatCents(line.balance_after_cents)}</span>
                    </div>
                  </div>`)}
                </div>
              `
            : nothing}
        </section>
      </div>

      <div>
        <section class="pro-card" data-testid="statement-runs-card">
          <h2 class="pro-card-title">Published statement runs</h2>
          <div class="pro-line-list mt-3">
            ${this.statementRuns.length === 0
              ? html`<p class="pro-text-small py-2">No monthly statement runs archived yet.</p>`
              : this.statementRuns.map((run) => html`<div class="pro-line" data-testid=${`statement-run-${run.id}`}>
                  <div>
                    <div class="pro-line-title pro-data">${calendarDay(run.period_start)} to ${calendarDay(run.period_end)}</div>
                    <div class="pro-line-meta">Closing ${formatCents(run.closing_balance_cents)}</div>
                  </div>
                  <button
                    type="button"
                    class="pro-btn pro-btn-ghost pro-btn-small"
                    @click=${() => void this.downloadPdf(`/api/portal/v1/statements/${run.id}/pdf`, `statement-${run.period_end}.pdf`)}
                  >${icon(Download, 14)} PDF</button>
                </div>`)}
          </div>
        </section>
      </div>
    </div>`
  }

  private renderJobsView(): TemplateResult {
    const jobs = this.summary?.job_summaries ?? []
    return html`<div class="pro-list" data-testid="pay-jobs-view">
      ${jobs.length === 0
        ? html`<div class="pro-empty"><p class="pro-card-title">No job financial summaries on this account yet.</p></div>`
        : jobs.map((job) => html`<article class="pro-card" data-testid=${`job-summary-${job.project_id}`}>
            <div class="pro-row">
              <div class="pro-row-grow">
                <p class="pro-page-kicker">${job.order_count} orders · ${job.invoice_count} invoices · ${job.delivered_count}/${job.delivery_count} delivered</p>
                <h2 class="pro-card-title">${job.project_name}</h2>
              </div>
              <div class="text-right">
                <span class="pro-money text-lg">${formatCents(job.open_balance_cents)} open</span>
                <div class="mt-1">${statusChip(job.status)}</div>
              </div>
            </div>
            <div class="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-4">
              <div class="pro-stat"><span class="pro-stat-value text-sm">${formatCents(job.quoted_cents)}</span><span class="pro-stat-label">Quoted</span></div>
              <div class="pro-stat"><span class="pro-stat-value text-sm">${formatCents(job.ordered_cents)}</span><span class="pro-stat-label">Ordered</span></div>
              <div class="pro-stat"><span class="pro-stat-value text-sm">${formatCents(job.invoiced_cents)}</span><span class="pro-stat-label">Invoiced</span></div>
              <div class="pro-stat"><span class="pro-stat-value text-sm">${formatCents(job.paid_cents)}</span><span class="pro-stat-label">Paid</span></div>
            </div>
          </article>`)}
    </div>`
  }

  private renderMethodsView(): TemplateResult {
    return html`<div class="pro-two-col" data-testid="pay-methods-view">
      <div class="pro-list" data-testid="saved-methods-list">
        ${this.methods.length === 0
          ? html`<div class="pro-empty"><p class="pro-card-title">No saved payment methods on file.</p></div>`
          : this.methods.map((method) => html`<article class="pro-card" data-testid=${`payment-method-${method.id}`}>
              <div class="pro-row">
                ${icon(method.method_type === 'BANK' ? Landmark : CreditCard, 18, 'text-gable-green')}
                <div class="pro-row-grow">
                  <div class="pro-card-title">${method.brand || method.method_type} ending in ${method.last4}</div>
                  <div class="pro-text-small">
                    ${method.method_type}${method.exp_month && method.exp_year ? ` · Exp ${method.exp_month}/${method.exp_year}` : ''} · Tokenized via ${method.gateway}
                  </div>
                </div>
                ${method.is_default ? html`<span class="pro-chip pro-chip-green">${icon(Star, 12)} Default</span>` : nothing}
              </div>
              <div class="pro-row mt-3">
                ${!method.is_default
                  ? html`<button
                      type="button"
                      class="pro-btn pro-btn-secondary pro-btn-small"
                      data-testid=${`default-method-${method.id}`}
                      ?disabled=${!this.canPay || this.offline || this.busy}
                      @click=${() => void this.setDefaultMethod(method.id)}
                    >Make default</button>`
                  : nothing}
                <span class="pro-row-grow"></span>
                <button
                  type="button"
                  class="pro-btn pro-btn-ghost pro-btn-small"
                  data-testid=${`delete-method-${method.id}`}
                  ?disabled=${!this.canPay || this.offline || this.busy}
                  @click=${() => void this.deleteMethod(method.id)}
                >${icon(Trash2, 14)} Remove</button>
              </div>
            </article>`)}
      </div>

      <div>
        <section class="pro-card" data-testid="add-payment-method-card">
          <div class="pro-row">
            ${icon(ShieldCheck, 18, 'text-gable-green')}
            <div class="pro-row-grow">
              <h2 class="pro-card-title">Link tokenized payment method</h2>
              <p class="pro-text-small">Hosted gateway tokenization keeps raw card and bank numbers off your portal session.</p>
            </div>
          </div>
          <fieldset class="mt-3" ?disabled=${!this.canPay || this.offline || this.busy}>
            <div class="pro-form-grid">
              <label class="pro-field">
                <span class="pro-label">Method type</span>
                <pro-select
                  data-testid="new-method-type"
                  aria-label="Method type"
                  .value=${this.newMethodType}
                  .options=${[
                    { value: 'CARD', label: 'Commercial card' },
                    { value: 'BANK', label: 'Bank ACH' },
                  ]}
                  ?disabled=${!this.canPay || this.offline || this.busy}
                  @change=${(event: Event) => {
                    this.newMethodType = (event.target as HTMLElement & { value: string }).value === 'BANK' ? 'BANK' : 'CARD'
                    this.newMethodBrand = this.newMethodType === 'BANK' ? 'Operating ACH' : 'Visa'
                  }}
                ></pro-select>
              </label>
              <label class="pro-field">
                <span class="pro-label">Label or brand</span>
                <input
                  class="pro-input"
                  data-testid="new-method-brand"
                  .value=${this.newMethodBrand}
                  @input=${(event: Event) => { this.newMethodBrand = (event.target as HTMLInputElement).value }}
                />
              </label>
              <label class="pro-field">
                <span class="pro-label">Last 4 digits (from hosted field)</span>
                <input
                  class="pro-input pro-data"
                  data-testid="new-method-last4"
                  maxlength="4"
                  .value=${this.newMethodLast4}
                  @input=${(event: Event) => { this.newMethodLast4 = (event.target as HTMLInputElement).value }}
                />
              </label>
            </div>
            <label class="pro-text-small flex items-center gap-2">
              <input
                type="checkbox"
                class="pro-check"
                .checked=${this.newMethodDefault}
                @change=${(event: Event) => { this.newMethodDefault = (event.target as HTMLInputElement).checked }}
              />
              Set as default payment method for this account
            </label>
            <button
              type="button"
              class="pro-btn pro-btn-primary pro-btn-wide mt-3"
              data-testid="save-payment-method-btn"
              ?disabled=${!this.canPay || this.offline || this.busy}
              @click=${() => void this.addPaymentMethod()}
            >${icon(ReceiptText, 15)} Save tokenized method</button>
          </fieldset>
        </section>
      </div>
    </div>`
  }

  render(): TemplateResult {
    if (!this.canSeeMoney) {
      return html`<section data-testid="pay-screen">
        <p class="pro-page-kicker">Pay · ${this.me.account.name}</p>
        <h1 class="pro-page-title">Account billing and payments</h1>
        <div class="pro-empty mt-5" data-testid="pay-restricted">
          <p class="pro-card-title">Your role does not include account billing visibility.</p>
        </div>
      </section>`
    }

    return html`<section data-testid="pay-screen">
      <p class="pro-page-kicker">Pay · ${this.me.account.name}</p>
      <h1 class="pro-page-title">Invoices, statements, and bill pay</h1>
      <p class="pro-page-subtitle">Live balance and aging buckets in integer cents, job account summaries, downloadable PDFs, and single-flight tokenized payments.</p>
      ${this.offline ? html`<div class="pro-warning mt-4" data-testid="pay-offline-banner">${OFFLINE_WRITE_REASON}</div>` : nothing}
      ${this.error !== '' ? html`<div class="pro-error mt-4" role="alert">${this.error}</div>` : nothing}
      ${this.message !== '' ? html`<div class="pro-success mt-4" role="status">${this.message}</div>` : nothing}
      ${this.loading
        ? html`<div class="pro-empty mt-5" role="status"><p class="pro-card-title">Opening your billing and payment desk...</p></div>`
        : html`
            ${this.renderBalanceAndAging()}
            ${this.renderViewTabs()}
            ${this.view === 'invoices'
              ? this.renderInvoicesView()
              : this.view === 'statement'
                ? this.renderStatementView()
                : this.view === 'jobs'
                  ? this.renderJobsView()
                  : this.renderMethodsView()}
          `}
    </section>`
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-pro-pay': GableProPay
  }
}
