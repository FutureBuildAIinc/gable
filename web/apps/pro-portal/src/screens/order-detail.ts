// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing, type TemplateResult } from 'lit'
import { customElement, property, state } from 'lit/decorators.js'
import { icon } from '@gable/design-system'
import { ArrowLeft, Check, FileCheck2, MapPin, Pencil, Truck } from 'lucide'
import { assertWritesOnline, portalClient } from '../lib/api.ts'
import { can } from '../lib/capabilities.ts'
import { OFFLINE_WRITE_REASON } from '../lib/offline.ts'
import { formatDollars, shortDate, timeOfDay } from '../lib/format.ts'
import { holdWhen, yardTimeZone } from '../lib/yard.ts'
import { navigate } from '../lib/router.ts'
import { statusChip } from '../lib/status.ts'
import type { PortalMe, PortalOrder, PortalOrderTimeline } from '../types.ts'

@customElement('gable-pro-order-detail')
export class GableProOrderDetail extends LitElement {
  createRenderRoot() { return this }

  @property({ attribute: false }) me!: PortalMe
  @property({ type: Boolean }) offline = false
  @property({ type: String }) orderId = ''
  @state() private order: PortalOrder | null = null
  @state() private timeline: PortalOrderTimeline | null = null
  @state() private loading = true
  @state() private saving = false
  @state() private error = ''
  @state() private message = ''

  connectedCallback(): void {
    super.connectedCallback()
    void this.load()
  }

  updated(changed: Map<PropertyKey, unknown>): void {
    if (changed.has('orderId') && changed.get('orderId') !== undefined && changed.get('orderId') !== this.orderId) {
      void this.load()
    }
  }

  private async load(): Promise<void> {
    if (this.orderId === '') return
    this.loading = true
    this.error = ''
    try {
      const [order, timeline] = await Promise.all([
        portalClient.get('/api/portal/v1/orders/{id}', { path: { id: this.orderId } }),
        portalClient.get('/api/portal/v1/orders/{id}/timeline', { path: { id: this.orderId } }),
      ])
      this.order = order.body
      this.timeline = timeline.body as PortalOrderTimeline
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'The order did not load.'
    } finally {
      this.loading = false
    }
  }

  private async saveInstructions(event: SubmitEvent): Promise<void> {
    event.preventDefault()
    if (this.saving || this.offline || !can(this.me.capabilities, 'edit_delivery_instructions')) return
    const data = new FormData(event.currentTarget as HTMLFormElement)
    const instructions = String(data.get('instructions') ?? '').trim()
    this.saving = true
    this.error = ''
    this.message = ''
    try {
      assertWritesOnline()
      const response = await portalClient.put('/api/portal/v1/orders/{id}/delivery-instructions', { instructions }, {
        path: { id: this.orderId },
        idempotencyKey: crypto.randomUUID(),
      })
      this.order = response.body
      this.message = 'The driver site note is saved.'
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'The site note could not be saved.'
    } finally {
      this.saving = false
    }
  }

  private renderTimeline(timeline: PortalOrderTimeline): TemplateResult {
    return html`<div class="pro-card" data-testid="order-timeline">
      <div class="pro-row mb-4">
        ${icon(Truck, 20, 'text-gable-green')}
        <div class="pro-row-grow"><h2 class="pro-card-title">The five step journey</h2></div>
        ${statusChip(timeline.status)}
      </div>
      <ol class="pro-timeline">
        ${timeline.steps.map((step, index) => html`<li class="pro-timeline-step" data-step=${step.key} data-state=${step.state}>
          <span class="pro-timeline-dot ${step.state === 'done' ? 'pro-timeline-dot-done' : step.state === 'current' ? 'pro-timeline-dot-current' : ''}">
            ${step.state === 'done' ? icon(Check, 15) : index + 1}
          </span>
          <div>
            <div class="pro-timeline-label">${step.label}</div>
            <div class="pro-timeline-meta">${step.at === null ? step.state === 'current' ? 'Now' : 'Ahead' : `${shortDate(step.at, yardTimeZone(this.me))} ${timeOfDay(step.at, yardTimeZone(this.me))}`}</div>
          </div>
        </li>`)}
      </ol>
    </div>`
  }

  private renderPod(timeline: PortalOrderTimeline): TemplateResult | typeof nothing {
    const pod = timeline.pod
    if (pod === null) return nothing
    const photo = pod.photo_url !== null && /^https?:\/\//.test(pod.photo_url) ? pod.photo_url : null
    return html`<div class="pro-card mt-3" data-testid="order-pod">
      <div class="pro-row">
        ${icon(FileCheck2, 20, 'text-gable-green')}
        <div class="pro-row-grow"><h2 class="pro-card-title">Proof of delivery</h2></div>
      </div>
      <div class="pro-line-list mt-2">
        <div class="pro-total-row"><span>Signed by</span><span class="text-white">${pod.signed_by ?? 'Not recorded'}</span></div>
        <div class="pro-total-row"><span>Place</span><span class="text-white">${pod.place ?? 'Not recorded'}</span></div>
        <div class="pro-total-row"><span>Received</span><span class="pro-data">${shortDate(pod.timestamp, yardTimeZone(this.me))} ${timeOfDay(pod.timestamp, yardTimeZone(this.me))}</span></div>
      </div>
      ${photo !== null ? html`<a class="pro-btn pro-btn-secondary pro-btn-small mt-3" href=${photo} target="_blank" rel="noopener">View delivery photo</a>` : nothing}
    </div>`
  }

  private renderInstructions(order: PortalOrder, timeline: PortalOrderTimeline): TemplateResult {
    const editable = can(this.me.capabilities, 'edit_delivery_instructions') && !timeline.is_cancelled && !['FULFILLED', 'CANCELLED'].includes(order.status)
    return html`<div class="pro-card mt-3" data-testid="site-instructions">
      <div class="pro-row">
        ${icon(MapPin, 20, 'text-gable-green')}
        <div class="pro-row-grow"><h2 class="pro-card-title">Site instructions</h2></div>
      </div>
      ${editable ? html`<form class="mt-3" @submit=${(event: SubmitEvent) => void this.saveInstructions(event)}>
        <label class="pro-field">
          <span class="pro-label">What the driver needs to know</span>
          <textarea class="pro-textarea" name="instructions" maxlength="2000" .value=${order.site_delivery_instructions ?? ''}></textarea>
        </label>
        ${this.offline ? html`<p class="pro-warning" data-testid="instructions-offline">${OFFLINE_WRITE_REASON}</p>` : nothing}
        <button type="submit" class="pro-btn pro-btn-primary" data-testid="save-instructions" ?disabled=${this.offline || this.saving}>
          ${icon(Pencil, 15)} ${this.saving ? 'Saving...' : 'Save site note'}
        </button>
      </form>` : html`<p class="pro-text-small mt-3">${order.site_delivery_instructions ?? 'No site instruction is recorded.'}</p>`}
    </div>`
  }

  render(): TemplateResult {
    const order = this.order
    const timeline = this.timeline
    const showMoney = can(this.me.capabilities, 'see_money')
    return html`<section data-testid="order-detail-screen">
      <button type="button" class="pro-btn pro-btn-ghost pro-btn-small mb-3" @click=${() => navigate('/schedule')}>${icon(ArrowLeft, 15)} Schedule</button>
      ${this.loading ? html`<div class="pro-empty" role="status"><p class="pro-card-title">Loading the order journey...</p></div>` : nothing}
      ${this.error !== '' ? html`<div class="pro-error" role="alert">${this.error}</div>` : nothing}
      ${this.message !== '' ? html`<div class="pro-success" role="status">${this.message}</div>` : nothing}
      ${order !== null && timeline !== null ? html`
        <p class="pro-page-kicker">Order ${order.id.slice(0, 8)} · ${timeline.delivery_type === 'PICKUP' ? 'Pickup' : 'Delivery'}</p>
        <h1 class="pro-page-title">${order.project_name ?? 'Your order'}</h1>
        <p class="pro-page-subtitle">${order.lines.length} ${order.lines.length === 1 ? 'line' : 'lines'}${showMoney ? ` · ${formatDollars(order.total_amount)} at your account prices` : ` · ${order.status}`}</p>
        <div class="mt-5">${this.renderTimeline(timeline)}</div>
        ${order.schedule_hold !== undefined && order.schedule_hold.status === 'held' ? html`<div class="pro-card mt-3">
          <div class="pro-row"><div class="pro-row-grow"><h2 class="pro-card-title">Held ${order.schedule_hold.kind}</h2><p class="pro-text-small">${holdWhen(this.me, order.schedule_hold)}</p></div>
          <button class="pro-btn pro-btn-secondary pro-btn-small" @click=${() => navigate(`/schedule?order=${order.id}`)}>Change window</button></div>
        </div>` : nothing}
        ${this.renderPod(timeline)}
        ${this.renderInstructions(order, timeline)}
        <div class="pro-card mt-3">
          <h2 class="pro-card-title">Order lines</h2>
          <div class="pro-line-list mt-2">
            ${order.lines.map((line) => html`<div class="pro-line"><span><span class="pro-line-title">${line.product_name}</span><span class="pro-line-meta block pro-data">${line.product_sku} · ${showMoney ? `${line.quantity} at ${formatDollars(line.price_each)}` : `Qty ${line.quantity}`}</span></span>${showMoney ? html`<span class="pro-money">${formatDollars(line.quantity * line.price_each)}</span>` : html`<span class="pro-data text-xs text-zinc-400">Qty ${line.quantity}</span>`}</div>`)}
          </div>
        </div>
      ` : nothing}
    </section>`
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-pro-order-detail': GableProOrderDetail
  }
}
