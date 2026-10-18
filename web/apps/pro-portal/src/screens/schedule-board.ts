// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing, type TemplateResult } from 'lit'
import { customElement, property, state } from 'lit/decorators.js'
import { icon } from '@gable/design-system'
import { CalendarDays, Check, Clock3, Package, Trash2, Truck } from 'lucide'
import { assertWritesOnline, portalClient, readCached } from '../lib/api.ts'
import { can } from '../lib/capabilities.ts'
import { OFFLINE_WRITE_REASON } from '../lib/offline.ts'
import { calendarDay } from '../lib/format.ts'
import { holdWhen } from '../lib/yard.ts'
import { navigate } from '../lib/router.ts'
import { statusChip } from '../lib/status.ts'
import type { PortalMe, PortalOrder, PortalScheduleSlot } from '../types.ts'
import '../components/pro-select.ts'

function isoDay(offset = 0): string {
  const value = new Date()
  value.setUTCDate(value.getUTCDate() + offset)
  return value.toISOString().slice(0, 10)
}

@customElement('gable-pro-schedule-board')
export class GableProScheduleBoard extends LitElement {
  createRenderRoot() { return this }

  @property({ attribute: false }) me!: PortalMe
  @property({ type: Boolean }) offline = false
  @state() private orders: PortalOrder[] = []
  @state() private slots: PortalScheduleSlot[] = []
  @state() private yardId = ''
  @state() private kind: 'delivery' | 'pickup' = 'delivery'
  @state() private selectedOrderId = ''
  @state() private selectedWindowId = ''
  @state() private loading = true
  @state() private busy = false
  @state() private message = ''
  @state() private error = ''

  connectedCallback(): void {
    super.connectedCallback()
    this.yardId = this.me.account.primary_branch_id || this.me.yards[0]?.id || ''
    this.selectedOrderId = new URLSearchParams(window.location.search).get('order') ?? ''
    void this.load()
  }

  private get selectedOrder(): PortalOrder | undefined {
    return this.orders.find((order) => order.id === this.selectedOrderId)
  }

  private get selectedSlot(): PortalScheduleSlot | undefined {
    return this.slots.find((slot) => slot.window_id === this.selectedWindowId)
  }

  private async load(): Promise<void> {
    this.loading = true
    this.error = ''
    try {
      const result = await readCached<PortalOrder[]>('orders', '/api/portal/v1/orders', { feed: true })
      this.orders = result.data.filter((order) => !['FULFILLED', 'CANCELLED'].includes(order.status))
      if (this.selectedOrderId === '' && this.orders[0] !== undefined) this.selectedOrderId = this.orders[0].id
      await this.loadWindows()
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'The schedule did not load.'
    } finally {
      this.loading = false
    }
  }

  private async loadWindows(): Promise<void> {
    if (this.yardId === '') {
      this.slots = []
      return
    }
    this.selectedWindowId = ''
    try {
      const response = await portalClient.get('/api/portal/v1/schedule/windows', {
        query: { yard_id: this.yardId, kind: this.kind, from: isoDay(), to: isoDay(14) },
      })
      this.slots = response.body
      this.error = ''
    } catch (error) {
      this.slots = []
      this.error = error instanceof Error ? error.message : 'Open windows did not load.'
    }
  }

  private async holdSelected(): Promise<void> {
    const slot = this.selectedSlot
    if (slot === undefined || this.busy || this.offline || !can(this.me.capabilities, 'schedule')) return
    this.busy = true
    this.message = ''
    this.error = ''
    try {
      assertWritesOnline()
      const orderId = this.selectedOrder?.id
      const body = { window_id: slot.window_id, date: slot.date, order_id: orderId ?? null }
      const current = this.selectedOrder?.schedule_hold
      if (current !== undefined && current.status === 'held') {
        await portalClient.put('/api/portal/v1/schedule/holds/{id}', body, {
          path: { id: current.id },
          idempotencyKey: crypto.randomUUID(),
        })
        this.message = 'The window was changed.'
      } else {
        await portalClient.post('/api/portal/v1/schedule/holds', body, { idempotencyKey: crypto.randomUUID() })
        this.message = orderId === undefined ? 'The window is held for checkout.' : 'The window is held on the order.'
      }
      await this.load()
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'The window could not be held.'
    } finally {
      this.busy = false
    }
  }

  private async releaseHold(): Promise<void> {
    const current = this.selectedOrder?.schedule_hold
    if (current === undefined || this.busy || this.offline || !can(this.me.capabilities, 'schedule')) return
    this.busy = true
    this.error = ''
    this.message = ''
    try {
      assertWritesOnline()
      await portalClient.delete('/api/portal/v1/schedule/holds/{id}', { path: { id: current.id } })
      this.message = 'The held window was released.'
      await this.load()
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'The hold could not be released.'
    } finally {
      this.busy = false
    }
  }

  private orderCard(order: PortalOrder): TemplateResult {
    const hold = order.schedule_hold
    return html`<div class="pro-card" data-testid=${`schedule-order-${order.id}`}>
      <div class="pro-row">
        <span class="pro-swatch">${icon(order.status === 'READY' ? Package : Truck, 21)}</span>
        <div class="pro-row-grow">
          <div class="pro-card-title">${order.project_name ?? `Order ${order.id.slice(0, 8)}`}</div>
          <div class="pro-text-small">Order ${order.id.slice(0, 8)} · ${order.lines.length} ${order.lines.length === 1 ? 'line' : 'lines'}</div>
        </div>
        ${statusChip(order.status)}
      </div>
      <div class="pro-text-small mt-3">
        ${hold !== undefined && hold.status === 'held'
          ? html`${icon(CalendarDays, 14, 'inline text-gable-green')} ${hold.kind === 'pickup' ? 'Pickup' : 'Delivery'} ${holdWhen(this.me, hold)}`
          : 'No window is held yet.'}
      </div>
      <div class="mt-3 flex flex-wrap gap-2">
        <button type="button" class="pro-btn pro-btn-secondary pro-btn-small" @click=${() => navigate(`/schedule/orders/${order.id}`)}>Order timeline</button>
        <button type="button" class="pro-btn pro-btn-ghost pro-btn-small" @click=${() => { this.selectedOrderId = order.id }}>Book here</button>
      </div>
    </div>`
  }

  private slotCard(slot: PortalScheduleSlot): TemplateResult {
    const selected = this.selectedWindowId === slot.window_id
    return html`<button
      type="button"
      class="pro-schedule-slot ${selected ? 'pro-schedule-slot-selected' : ''}"
      data-testid=${`schedule-slot-${slot.window_id}`}
      aria-pressed=${selected ? 'true' : 'false'}
      @click=${() => { this.selectedWindowId = slot.window_id }}
    >
      <span class="pro-row">
        <span class="pro-card-title pro-data">${calendarDay(slot.date)}</span>
        <span class="pro-row-grow"></span>
        <span class="pro-chip ${slot.remaining > 0 ? 'pro-chip-green' : 'pro-chip-red'}">${slot.remaining} left</span>
      </span>
      <span class="pro-money mt-2 block">${slot.start_local} to ${slot.end_local}</span>
      <span class="pro-text-small mt-1 block">${slot.yard_code} · ${slot.kind === 'pickup' ? 'Pickup slot' : 'Delivery window'}</span>
      ${selected ? html`<span class="pro-text-small mt-2 flex items-center gap-1 text-gable-green">${icon(Check, 14)} Selected</span>` : nothing}
    </button>`
  }

  private bookingPanel(): TemplateResult {
    const order = this.selectedOrder
    const hold = order?.schedule_hold
    const maySchedule = can(this.me.capabilities, 'schedule')
    return html`<section class="pro-card" data-testid="schedule-booking">
      <div class="pro-row">
        ${icon(Clock3, 20, 'text-gable-green')}
        <div class="pro-row-grow"><h2 class="pro-card-title">Book a window</h2></div>
      </div>
      <div class="mt-4 grid gap-3">
        <label class="pro-field">
          <span class="pro-label">Order</span>
          <pro-select
            data-testid="schedule-order-select"
            aria-label="Order"
            .value=${this.selectedOrderId}
            .options=${[
              { value: '', label: 'For a later checkout' },
              ...this.orders.map((item) => ({ value: item.id, label: item.project_name ?? item.id.slice(0, 8) })),
            ]}
            @change=${(event: Event) => { this.selectedOrderId = (event.target as HTMLElement & { value: string }).value }}
          ></pro-select>
        </label>
        <label class="pro-field">
          <span class="pro-label">Yard</span>
          <pro-select
            data-testid="schedule-yard-select"
            aria-label="Yard"
            .value=${this.yardId}
            .options=${this.me.yards.map((yard) => ({ value: yard.id, label: yard.name }))}
            @change=${(event: Event) => { this.yardId = (event.target as HTMLElement & { value: string }).value; void this.loadWindows() }}
          ></pro-select>
        </label>
      </div>
      <div class="pro-tabs" role="group" aria-label="Window kind">
        ${(['delivery', 'pickup'] as const).map((kind) => html`<button type="button" class="pro-tab ${this.kind === kind ? 'pro-tab-active' : ''}" data-testid=${`schedule-kind-${kind}`} @click=${() => { this.kind = kind; void this.loadWindows() }}>${kind === 'delivery' ? 'Delivery' : 'Pickup'}</button>`)}
      </div>
      ${hold !== undefined && hold.status === 'held' ? html`<div class="pro-notice">
        Current hold: ${hold.kind} ${holdWhen(this.me, hold)}.
        ${maySchedule ? html`<button class="pro-btn pro-btn-ghost pro-btn-small ml-2" data-testid="schedule-release" ?disabled=${this.offline || this.busy} @click=${() => void this.releaseHold()}>${icon(Trash2, 14)} Release</button>` : nothing}
      </div>` : nothing}
      ${this.offline ? html`<p class="pro-warning" data-testid="schedule-offline">${OFFLINE_WRITE_REASON}</p>` : nothing}
      ${!maySchedule ? html`<p class="pro-text-small">Your role can read this schedule. Booking is another role's action.</p>` : nothing}
      <div class="grid gap-3" data-testid="schedule-slots">
        ${this.slots.length === 0 ? html`<div class="pro-empty"><p class="pro-text-small">No open ${this.kind} window is available at this yard.</p></div>` : this.slots.map((slot) => this.slotCard(slot))}
      </div>
      ${maySchedule ? html`<button
        type="button"
        class="pro-btn pro-btn-primary pro-btn-wide mt-4"
        data-testid="schedule-hold"
        ?disabled=${this.selectedSlot === undefined || this.offline || this.busy || (this.selectedSlot?.remaining ?? 0) < 1}
        @click=${() => void this.holdSelected()}
      >${this.busy ? 'Saving...' : hold !== undefined && hold.status === 'held' ? 'Change to this window' : 'Hold this window'}</button>` : nothing}
    </section>`
  }

  render(): TemplateResult {
    return html`<section data-testid="schedule-screen">
      <p class="pro-page-kicker">Deliveries and pickups</p>
      <h1 class="pro-page-title">Keep the site moving</h1>
      <p class="pro-page-subtitle">Pick a yard window, follow every order, and give the driver the right site note.</p>
      ${this.error !== '' ? html`<div class="pro-error mt-4" role="alert">${this.error}</div>` : nothing}
      ${this.message !== '' ? html`<div class="pro-success mt-4" role="status">${this.message}</div>` : nothing}
      ${this.loading && this.orders.length === 0 ? html`<div class="pro-empty mt-5" role="status"><p class="pro-card-title">Loading the yard board...</p></div>` : html`
        <div class="pro-two-col mt-5">
          <div>
            <h2 class="pro-section-title">Open orders</h2>
            ${this.orders.length === 0 ? html`<div class="pro-empty"><p class="pro-text-small">No unfulfilled order needs a window.</p></div>` : this.orders.map((order) => this.orderCard(order))}
          </div>
          <div>
            <h2 class="pro-section-title">Yard windows</h2>
            ${this.bookingPanel()}
          </div>
        </div>
      `}
    </section>`
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-pro-schedule-board': GableProScheduleBoard
  }
}
