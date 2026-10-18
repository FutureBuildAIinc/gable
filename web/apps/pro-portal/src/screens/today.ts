// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing, type TemplateResult } from 'lit'
import { customElement, property, state } from 'lit/decorators.js'
import { icon } from '@gable/design-system'
import { CalendarClock, ChevronRight, FileCheck2, PackageOpen, ReceiptText, Truck, X } from 'lucide'
import { can } from '../lib/capabilities.ts'
import { readCached } from '../lib/api.ts'
import { formatCents, shortDate, timeOfDay } from '../lib/format.ts'
import { navigate } from '../lib/router.ts'
import { statusChip } from '../lib/status.ts'
import { yardTimeZone } from '../lib/yard.ts'
import type { PortalMe, PortalToday } from '../types.ts'

type SectionKey = 'quotes' | 'deliveries' | 'next' | 'invoices' | 'orders'

interface Tile {
  key: SectionKey
  label: string
  count: number
  icon: typeof FileCheck2
}

@customElement('gable-pro-today')
export class GableProToday extends LitElement {
  createRenderRoot() { return this }

  @property({ attribute: false }) me!: PortalMe
  @property({ type: Boolean }) offline = false
  @state() private today: PortalToday | null = null
  @state() private loading = true
  @state() private error = ''
  @state() private active: SectionKey | null = null

  connectedCallback(): void {
    super.connectedCallback()
    void this.load()
  }

  private async load(): Promise<void> {
    this.loading = true
    this.error = ''
    try {
      const result = await readCached<PortalToday>('today', '/api/portal/v1/today', { feed: true })
      this.today = result.data
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Today did not load.'
    } finally {
      this.loading = false
    }
  }

  private tiles(today: PortalToday): Tile[] {
    const tiles: Tile[] = []
    if (can(this.me.capabilities, 'approve_quotes')) tiles.push({ key: 'quotes', label: 'Quotes to decide', count: today.quotes_awaiting_pro.count, icon: FileCheck2 })
    tiles.push(
      { key: 'deliveries', label: 'Stops today', count: today.deliveries_today.count, icon: Truck },
      { key: 'next', label: 'Next stops', count: today.next_deliveries.count, icon: CalendarClock },
    )
    if (can(this.me.capabilities, 'see_money')) tiles.push({ key: 'invoices', label: 'Invoices due', count: today.invoices_due.count, icon: ReceiptText })
    tiles.push({ key: 'orders', label: 'Open orders', count: today.open_orders.count, icon: PackageOpen })
    return tiles
  }

  private openTile(key: SectionKey): void {
    this.active = this.active === key ? null : key
  }

  private tile(tile: Tile): TemplateResult {
    const selected = this.active === tile.key
    return html`<button
      type="button"
      class="pro-stat text-left ${selected ? 'border-gable-green' : ''}"
      data-testid=${`today-tile-${tile.key}`}
      aria-pressed=${selected ? 'true' : 'false'}
      @click=${() => this.openTile(tile.key)}
    >
      <span class="pro-row">
        <span class="pro-stat-value" data-testid=${`today-count-${tile.key}`}>${tile.count}</span>
        <span class="pro-row-grow"></span>
        ${icon(tile.icon, 18, 'text-gable-green')}
      </span>
      <span class="pro-stat-label">${tile.label}</span>
      <span class="pro-text-small mt-2 flex items-center gap-1">Open list ${icon(ChevronRight, 13)}</span>
    </button>`
  }

  private sectionHead(title: string, shown: number, total: number): TemplateResult {
    return html`<div class="pro-row mb-2">
      <div class="pro-row-grow">
        <h2 class="pro-card-title">${title}</h2>
        ${total > shown ? html`<p class="pro-text-small">Showing the first ${shown} of ${total}.</p>` : nothing}
      </div>
      <button type="button" class="pro-icon-btn" aria-label="Close list" @click=${() => { this.active = null }}>${icon(X, 16)}</button>
    </div>`
  }

  private emptyRow(words: string): TemplateResult {
    return html`<p class="pro-text-small py-3">${words}</p>`
  }

  private quoteSection(today: PortalToday): TemplateResult {
    const rows = today.quotes_awaiting_pro.first
    const showMoney = can(this.me.capabilities, 'see_money')
    return html`<div class="pro-card mt-4" data-testid="today-list-quotes">
      ${this.sectionHead('Quotes waiting for your decision', rows.length, today.quotes_awaiting_pro.count)}
      <div class="pro-line-list">
        ${rows.length === 0 ? this.emptyRow('No dealer quote needs a decision.') : rows.map((quote) => html`<button class="pro-line w-full text-left" @click=${() => navigate(`/order?view=requests&quote=${quote.id}`)}>
          <span><span class="pro-line-title">Dealer quote</span><span class="pro-line-meta block">Sent ${shortDate(quote.sent_at)}</span></span>
          ${showMoney ? html`<span class="pro-money">${formatCents(quote.total_cents)}</span>` : nothing}
        </button>`)}
      </div>
    </div>`
  }

  private deliverySection(today: PortalToday, next: boolean): TemplateResult {
    const section = next ? today.next_deliveries : today.deliveries_today
    const rows = section.first
    const key = next ? 'next' : 'deliveries'
    return html`<div class="pro-card mt-4" data-testid=${`today-list-${key}`}>
      ${this.sectionHead(next ? 'The next deliveries and pickups' : 'Deliveries and pickups today', rows.length, section.count)}
      <div class="pro-line-list">
        ${rows.length === 0 ? this.emptyRow(next ? 'No later stop is on the board.' : 'No stop is due today.') : rows.map((stop) => html`<button class="pro-line w-full text-left" @click=${() => navigate(`/schedule/orders/${stop.order_id}`)}>
          <span><span class="pro-line-title">${stop.is_pickup ? 'Pickup' : 'Delivery'} · ${stop.yard_code}</span><span class="pro-line-meta block">${shortDate(stop.scheduled_for, yardTimeZone(this.me, undefined, stop.yard_code))} ${timeOfDay(stop.scheduled_for, yardTimeZone(this.me, undefined, stop.yard_code))} · ${stop.status}</span></span>
          ${statusChip(stop.status)}
        </button>`)}
      </div>
    </div>`
  }

  private invoiceSection(today: PortalToday): TemplateResult {
    const rows = today.invoices_due.first
    return html`<div class="pro-card mt-4" data-testid="today-list-invoices">
      ${this.sectionHead('Invoices due or overdue', rows.length, today.invoices_due.count)}
      <div class="pro-line-list">
        ${rows.length === 0 ? this.emptyRow('Nothing is due or overdue.') : rows.map((invoice) => html`<button class="pro-line w-full text-left" @click=${() => navigate(`/pay?invoice=${invoice.id}`)}>
          <span><span class="pro-line-title pro-data">${invoice.number}</span><span class="pro-line-meta block">Due ${shortDate(invoice.due_date)}</span></span>
          <span class="text-right"><span class="pro-money block">${formatCents(invoice.total_cents)}</span>${invoice.is_overdue ? html`<span class="mt-1 inline-block">${statusChip('overdue')}</span>` : nothing}</span>
        </button>`)}
      </div>
    </div>`
  }

  private orderSection(today: PortalToday): TemplateResult {
    const rows = today.open_orders.first
    const showMoney = can(this.me.capabilities, 'see_money')
    return html`<div class="pro-card mt-4" data-testid="today-list-orders">
      ${this.sectionHead('Open orders', rows.length, today.open_orders.count)}
      <div class="pro-line-list">
        ${rows.length === 0 ? this.emptyRow('No open order is in motion.') : rows.map((order) => html`<button class="pro-line w-full text-left" @click=${() => navigate(`/schedule/orders/${order.id}`)}>
          <span><span class="pro-line-title pro-data">Order ${order.id.slice(0, 8)}</span><span class="pro-line-meta block">Updated ${shortDate(order.updated_at)}</span></span>
          <span class="text-right">${showMoney ? html`<span class="pro-money block">${formatCents(order.total_cents)}</span>` : nothing}<span class="${showMoney ? 'mt-1 ' : ''}inline-block">${statusChip(order.status)}</span></span>
        </button>`)}
      </div>
    </div>`
  }

  private activeSection(today: PortalToday): TemplateResult | typeof nothing {
    switch (this.active) {
      case 'quotes': return this.quoteSection(today)
      case 'deliveries': return this.deliverySection(today, false)
      case 'next': return this.deliverySection(today, true)
      case 'invoices': return this.invoiceSection(today)
      case 'orders': return this.orderSection(today)
      default: return nothing
    }
  }

  render(): TemplateResult {
    const today = this.today
    const attention = today === null ? 0 : today.quotes_awaiting_pro.count + today.deliveries_today.count + today.invoices_due.count
    return html`<section data-testid="today-screen">
      <p class="pro-page-kicker">Today · ${this.me.account.name}</p>
      <h1 class="pro-page-title">Good to see you, ${this.me.user.name}</h1>
      <p class="pro-page-subtitle">${attention === 0 ? 'The board is clear. Your jobs and account are right here.' : `${attention} ${attention === 1 ? 'thing needs' : 'things need'} your attention today.`}</p>
      ${this.error !== '' ? html`<div class="pro-error mt-4" role="alert">${this.error} <button class="underline" @click=${() => void this.load()}>Try again</button></div>` : nothing}
      ${this.loading && today === null ? html`<div class="pro-empty mt-5" role="status"><p class="pro-card-title">Opening your Today board...</p></div>` : nothing}
      ${today !== null ? html`
        <h2 class="pro-section-title">Your action strip</h2>
        <div class="pro-stat-grid lg:grid-cols-5" data-testid="today-actions">
          ${this.tiles(today).map((tile) => this.tile(tile))}
        </div>
        ${this.activeSection(today)}
      ` : nothing}
    </section>`
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-pro-today': GableProToday
  }
}
