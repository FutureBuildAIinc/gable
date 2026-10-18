// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing, type TemplateResult } from 'lit'
import { customElement, property, state } from 'lit/decorators.js'
import { icon } from '@gable/design-system'
import { Check, CheckCircle2, FileText, PenLine, ShieldCheck, X } from 'lucide'
import { acceptPublicQuote, declinePublicQuote, getPublicQuote, putPublicChoices, requestPublicCode, tokenFromPath } from './api.ts'
import { sanitizePublicQuote, type PublicQuote } from './public-view.ts'
import { formatCents, shortDate, statusLabel } from '../lib/format.ts'

const OFFLINE_WORDS = 'Reconnect to approve or change selections. Nothing is queued while you are offline.'

type PublicLine = PublicQuote['lines'][number]

@customElement('gable-customer-quote-page')
export class GableCustomerQuotePage extends LitElement {
  createRenderRoot() { return this }

  @property({ attribute: false }) quote: PublicQuote | null = null
  @property({ type: Boolean }) hosted = false
  @state() private loading = true
  @state() private busy = false
  @state() private error = ''
  @state() private notice = ''
  @state() private stage: 'details' | 'code' | 'signed' = 'details'
  @state() private fullName = ''
  @state() private email = ''
  @state() private phone = ''
  @state() private channel: 'email' | 'sms' = 'email'
  @state() private code = ''
  @state() private signatureMode: 'typed' | 'drawn' = 'typed'
  @state() private signatureText = ''
  @state() private signatureDrawn = false
  @state() private declineOpen = false
  @state() private declineReason = ''
  @state() private offline = navigator.onLine === false

  private token: string | null = null
  private drawing = false

  connectedCallback(): void {
    super.connectedCallback()
    window.addEventListener('online', this.onOnline)
    window.addEventListener('offline', this.onOffline)
    if (!this.hosted) {
      this.token = tokenFromPath(window.location.pathname)
      void this.load()
    } else {
      this.loading = false
      if (this.quote !== null) this.quote = sanitizePublicQuote(this.quote)
    }
  }

  disconnectedCallback(): void {
    window.removeEventListener('online', this.onOnline)
    window.removeEventListener('offline', this.onOffline)
    super.disconnectedCallback()
  }

  private readonly onOnline = () => { this.offline = false }
  private readonly onOffline = () => { this.offline = true }

  private async load(): Promise<void> {
    this.loading = true
    this.error = ''
    if (this.token === null) {
      this.error = 'Use the complete quote link from your contractor.'
      this.loading = false
      return
    }
    try {
      this.quote = await getPublicQuote(this.token)
      if (this.quote.status === 'approved' || this.quote.status === 'declined' || this.quote.status === 'confirmed') this.stage = 'signed'
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'This quote did not load.'
    } finally {
      this.loading = false
    }
  }

  private get selectionsComplete(): boolean {
    const lines = this.quote?.lines ?? []
    return lines.length > 0 && lines.every((line) => line.selection !== 'pending') && lines.some((line) => line.selection === 'approved')
  }

  private get signatureReady(): boolean {
    return this.signatureMode === 'typed' ? (this.signatureText.trim() !== '' || this.fullName.trim() !== '') : this.signatureDrawn
  }

  private get contactReady(): boolean {
    return this.email.trim() !== '' || this.phone.trim() !== ''
  }

  private async choose(line: PublicLine, state: 'approved' | 'declined'): Promise<void> {
    if (this.quote === null || this.busy || this.offline || this.hosted || !this.quote.can_decide) return
    this.busy = true
    this.error = ''
    this.notice = ''
    try {
      if (this.token !== null) {
        this.quote = await putPublicChoices(this.token, { choices: [{ line_id: line.id, state }] })
        this.notice = state === 'approved' ? `${line.description} is selected.` : `${line.description} is declined.`
      }
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'The selection was not saved.'
    } finally {
      this.busy = false
    }
  }

  private async beginAccept(event: SubmitEvent): Promise<void> {
    event.preventDefault()
    if (!this.canAccept || this.busy || this.offline || this.hosted || this.token === null || this.quote === null) return
    if (this.quote.requires_code && this.stage === 'details') {
      this.busy = true
      this.error = ''
      try {
        await requestPublicCode(this.token, this.channel)
        this.stage = 'code'
        this.notice = this.channel === 'email' ? 'A six digit code was sent to the quote email.' : 'A six digit code was sent to the quote phone.'
      } catch (error) {
        this.error = error instanceof Error ? error.message : 'The code could not be sent.'
      } finally {
        this.busy = false
      }
      return
    }
    await this.accept()
  }

  private async accept(): Promise<void> {
    if (this.token === null || !this.canAccept) return
    this.busy = true
    this.error = ''
    try {
      const signature = this.signatureMode === 'typed'
        ? { kind: 'typed' as const, text: this.signatureText.trim() || this.fullName.trim() }
        : { kind: 'drawn' as const, png_base64: this.signaturePng() }
      this.quote = await acceptPublicQuote(this.token, {
        full_name: this.fullName.trim(),
        ...(this.email.trim() !== '' ? { email: this.email.trim() } : {}),
        ...(this.phone.trim() !== '' ? { phone: this.phone.trim() } : {}),
        signature,
        ...(this.quote?.requires_code ? { code: this.code.trim() } : {}),
        selections_version: this.quote?.selections_version ?? 1,
      })
      this.stage = 'signed'
      this.notice = 'Your approval is recorded.'
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Your approval was not recorded.'
    } finally {
      this.busy = false
    }
  }

  private async decline(): Promise<void> {
    if (this.token === null || this.fullName.trim().length < 2 || this.busy || this.offline || this.hosted) {
      if (this.fullName.trim().length < 2) this.error = 'Add your full name before declining.'
      return
    }
    this.busy = true
    this.error = ''
    try {
      this.quote = await declinePublicQuote(this.token, {
        full_name: this.fullName.trim(),
        reason: this.declineReason.trim(),
        selections_version: this.quote?.selections_version ?? 1,
      })
      this.stage = 'signed'
      this.notice = 'Your response is recorded.'
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Your response was not recorded.'
    } finally {
      this.busy = false
    }
  }

  private get canAccept(): boolean {
    return this.selectionsComplete && this.fullName.trim().length >= 2 && this.contactReady && this.signatureReady && (!this.quote?.requires_code || this.stage === 'details' || /^\d{6}$/.test(this.code))
  }

  private signaturePng(): string {
    const canvas = this.renderRoot.querySelector<HTMLCanvasElement>('[data-testid="signature-canvas"]')
    return canvas?.toDataURL('image/png').split(',')[1] ?? ''
  }

  private point(event: PointerEvent): { x: number, y: number } {
    const canvas = event.currentTarget as HTMLCanvasElement
    const rect = canvas.getBoundingClientRect()
    return {
      x: (event.clientX - rect.left) * canvas.width / rect.width,
      y: (event.clientY - rect.top) * canvas.height / rect.height,
    }
  }

  private startStroke(event: PointerEvent): void {
    event.preventDefault()
    const canvas = event.currentTarget as HTMLCanvasElement
    canvas.setPointerCapture(event.pointerId)
    const context = canvas.getContext('2d')
    if (context === null) return
    const point = this.point(event)
    context.strokeStyle = getComputedStyle(canvas).color
    context.lineWidth = 3
    context.lineCap = 'round'
    context.lineJoin = 'round'
    context.beginPath()
    context.moveTo(point.x, point.y)
    this.drawing = true
    this.signatureDrawn = true
  }

  private moveStroke(event: PointerEvent): void {
    if (!this.drawing) return
    event.preventDefault()
    const canvas = event.currentTarget as HTMLCanvasElement
    const context = canvas.getContext('2d')
    if (context === null) return
    const point = this.point(event)
    context.lineTo(point.x, point.y)
    context.stroke()
  }

  private endStroke(event: PointerEvent): void {
    if (!this.drawing) return
    event.preventDefault()
    this.drawing = false
    this.requestUpdate()
  }

  private clearSignature(): void {
    const canvas = this.renderRoot.querySelector<HTMLCanvasElement>('[data-testid="signature-canvas"]')
    const context = canvas?.getContext('2d')
    if (canvas && context) context.clearRect(0, 0, canvas.width, canvas.height)
    this.signatureDrawn = false
  }

  private renderLine(line: PublicLine, groupLabel: string | null): TemplateResult {
    const selected = line.selection === 'approved'
    const declined = line.selection === 'declined'
    return html`<div class="pro-option ${selected ? 'pro-option-selected' : ''}" data-testid=${`public-line-${line.id}`}>
      <span class="pro-swatch">${icon(selected ? CheckCircle2 : FileText, 21)}</span>
      <div class="pro-row-grow">
        ${groupLabel !== null ? html`<div class="pro-page-kicker">${groupLabel}</div>` : nothing}
        <div class="pro-card-title">${line.description}</div>
        <div class="pro-text-small pro-data">${line.quantity} ${line.uom}</div>
        ${line.note !== undefined && line.note !== '' ? html`<div class="pro-text-small">${line.note}</div>` : nothing}
        ${!this.hosted && this.quote?.can_decide ? html`<div class="mt-2 flex flex-wrap gap-2">
          <button type="button" class="pro-btn ${selected ? 'pro-btn-primary' : 'pro-btn-secondary'} pro-btn-small" data-testid=${`approve-${line.id}`} aria-pressed=${selected ? 'true' : 'false'} ?disabled=${this.busy || this.offline} @click=${() => void this.choose(line, 'approved')}>${icon(Check, 13)} Approve</button>
          <button type="button" class="pro-btn pro-btn-ghost pro-btn-small" data-testid=${`decline-${line.id}`} aria-pressed=${declined ? 'true' : 'false'} ?disabled=${this.busy || this.offline} @click=${() => void this.choose(line, 'declined')}>${icon(X, 13)} Decline</button>
        </div>` : selected || declined ? html`<div class="pro-text-small mt-2 ${selected ? 'text-gable-green' : ''}">${selected ? 'Approved' : 'Declined'}</div>` : nothing}
      </div>
      ${!this.quote?.hide_line_prices && line.price_cents !== undefined ? html`<span class="pro-money">${formatCents(line.price_cents)}</span>` : nothing}
    </div>`
  }

  private renderSelections(quote: PublicQuote): TemplateResult {
    const groups = quote.groups.map((group) => ({ id: group.id, label: group.label }))
    return html`<div>
      <h2 class="pro-section-title">Your selections</h2>
      <div class="grid gap-3" data-testid="public-selections">
        ${groups.map((group) => html`<section class="pro-card">
          <h3 class="pro-card-title">${group.label}</h3>
          <p class="pro-text-small">Choose one option.</p>
          <div class="mt-3 grid gap-2">${quote.lines.filter((line) => line.group_id === group.id).map((line) => this.renderLine(line, null))}</div>
        </section>`)}
        ${quote.lines.filter((line) => line.group_id === undefined).length > 0 ? html`<section class="pro-card">
          <h3 class="pro-card-title">Included in the quote</h3>
          <div class="mt-3 grid gap-2">${quote.lines.filter((line) => line.group_id === undefined).map((line) => this.renderLine(line, null))}</div>
        </section>` : nothing}
      </div>
    </div>`
  }

  private renderTotals(quote: PublicQuote): TemplateResult {
    return html`<div class="pro-card" data-testid="public-totals">
      <p class="pro-page-kicker">${quote.hide_line_prices ? 'Lump sum quote' : 'Quote total'}</p>
      <p class="pro-money-large mt-1">${formatCents(quote.totals.total_cents)}</p>
      <div class="mt-3">
        <div class="pro-total-row"><span>Subtotal</span><span class="pro-data">${formatCents(quote.totals.subtotal_cents)}</span></div>
        <div class="pro-total-row"><span>Tax</span><span class="pro-data">${formatCents(quote.totals.tax_cents)}</span></div>
        <div class="pro-total-row pro-total-grand"><span>Total</span><span class="pro-data">${formatCents(quote.totals.total_cents)}</span></div>
        ${quote.totals.deposit_cents > 0 ? html`<div class="pro-total-row"><span>Deposit</span><span class="pro-data">${formatCents(quote.totals.deposit_cents)}</span></div>` : nothing}
      </div>
      <p class="pro-text-small mt-3">${quote.totals.tax_note || statusLabel(quote.totals.tax_mode)}</p>
      <p class="pro-text-small">${quote.payment_terms}</p>
    </div>`
  }

  private renderSigned(quote: PublicQuote): TemplateResult {
    const approved = quote.status === 'approved' || quote.status === 'confirmed'
    return html`<div class="pro-card mt-3" data-testid="public-signed" role="status">
      <div class="pro-row">
        ${icon(approved ? CheckCircle2 : X, 24, approved ? 'text-gable-green' : 'text-rose-300')}
        <div class="pro-row-grow">
          <h2 class="pro-card-title">${approved ? 'Quote approved' : quote.status === 'declined' ? 'Quote declined' : `Quote ${statusLabel(quote.status).toLowerCase()}`}</h2>
          <p class="pro-text-small">${quote.approval !== undefined ? `${quote.approval.signed_by} · ${shortDate(quote.approval.at)}` : 'The status is recorded.'}</p>
        </div>
      </div>
      ${quote.fulfilment !== undefined ? html`<div class="pro-notice mt-3">Fulfilment: ${statusLabel(quote.fulfilment)}</div>` : nothing}
      <p class="pro-text-small mt-3">Keep this page for your records. Your contractor can see the same status.</p>
    </div>`
  }

  private renderSignoff(quote: PublicQuote): TemplateResult {
    if (this.hosted || this.stage === 'signed' || !quote.can_decide) return html``
    return html`<div class="pro-card mt-3" id="signoff" data-testid="public-signoff">
      <div class="pro-row">
        ${icon(PenLine, 20, 'text-gable-green')}
        <div class="pro-row-grow"><h2 class="pro-card-title">Sign off</h2></div>
      </div>
      <p class="pro-text-small">Your name, contact, signature, and the approved selections are recorded together.</p>
      <form class="mt-3" @submit=${(event: SubmitEvent) => void this.beginAccept(event)}>
        <label class="pro-field"><span class="pro-label">Full name</span><input class="pro-input" name="full_name" autocomplete="name" .value=${this.fullName} @input=${(event: Event) => { this.fullName = (event.target as HTMLInputElement).value }} required /></label>
        <div class="pro-form-grid">
          <label class="pro-field"><span class="pro-label">Email</span><input class="pro-input" type="email" autocomplete="email" .value=${this.email} @input=${(event: Event) => { this.email = (event.target as HTMLInputElement).value }} /></label>
          <label class="pro-field"><span class="pro-label">Phone</span><input class="pro-input" type="tel" autocomplete="tel" .value=${this.phone} @input=${(event: Event) => { this.phone = (event.target as HTMLInputElement).value }} /></label>
        </div>
        <div class="pro-tabs" role="group" aria-label="Signature style">
          ${(['typed', 'drawn'] as const).map((mode) => html`<button type="button" class="pro-tab ${this.signatureMode === mode ? 'pro-tab-active' : ''}" data-testid=${`signature-${mode}`} @click=${() => { this.signatureMode = mode }}>${mode === 'typed' ? 'Type signature' : 'Draw signature'}</button>`)}
        </div>
        ${this.signatureMode === 'typed' ? html`<label class="pro-field"><span class="pro-label">Signature</span><input class="pro-input" .value=${this.signatureText} @input=${(event: Event) => { this.signatureText = (event.target as HTMLInputElement).value }} placeholder="Your full name" /></label>` : html`<div>
          <span class="pro-label">Draw your signature</span>
          <canvas class="pro-signature" data-testid="signature-canvas" width="640" height="240" aria-label="Drawn signature" @pointerdown=${(event: PointerEvent) => this.startStroke(event)} @pointermove=${(event: PointerEvent) => this.moveStroke(event)} @pointerup=${(event: PointerEvent) => this.endStroke(event)} @pointercancel=${(event: PointerEvent) => this.endStroke(event)}></canvas>
          <button type="button" class="pro-btn pro-btn-ghost pro-btn-small mt-1" @click=${() => this.clearSignature()}>Clear signature</button>
        </div>`}
        ${quote.requires_code ? html`<div class="pro-notice">
          <div class="pro-row">${icon(ShieldCheck, 18)}<span>This contractor requires a six digit code before acceptance.</span></div>
          ${this.stage === 'details' ? html`<div class="pro-tabs mt-2" role="group" aria-label="Code channel">
            ${(['email', 'sms'] as const).map((channel) => html`<button type="button" class="pro-tab ${this.channel === channel ? 'pro-tab-active' : ''}" data-testid=${`code-channel-${channel}`} @click=${() => { this.channel = channel }}>${channel === 'email' ? 'Email code' : 'Text code'}</button>`)}
          </div>` : html`<label class="pro-field mt-3"><span class="pro-label">Six digit code</span><input class="pro-input pro-data" data-testid="verification-code" inputmode="numeric" maxlength="6" autocomplete="one-time-code" .value=${this.code} @input=${(event: Event) => { this.code = (event.target as HTMLInputElement).value.replace(/\D/g, '') }} /></label>`}
        </div>` : nothing}
        ${this.offline ? html`<p class="pro-warning" data-testid="public-offline">${OFFLINE_WORDS}</p>` : nothing}
        <button type="submit" class="pro-btn pro-btn-primary pro-btn-wide" data-testid="public-accept" ?disabled=${!this.canAccept || this.busy || this.offline}>
          ${this.busy ? 'Saving...' : quote.requires_code && this.stage === 'details' ? 'Send code and continue' : 'Approve quote and selections'}
        </button>
      </form>
      <button type="button" class="pro-btn pro-btn-ghost pro-btn-wide mt-2" data-testid="public-decline-open" @click=${() => { this.declineOpen = !this.declineOpen }}>Decline instead</button>
      ${this.declineOpen ? html`<div class="mt-2">
        <label class="pro-field"><span class="pro-label">Reason, optional</span><textarea class="pro-textarea" .value=${this.declineReason} @input=${(event: Event) => { this.declineReason = (event.target as HTMLTextAreaElement).value }}></textarea></label>
        <button type="button" class="pro-btn pro-btn-danger pro-btn-wide" data-testid="public-decline" ?disabled=${this.busy || this.offline} @click=${() => void this.decline()}>Decline this quote</button>
      </div>` : nothing}
    </div>`
  }

  render(): TemplateResult {
    const quote = this.quote
    return html`<main class="pro-main" data-testid="public-page">
      <div class="pro-public-wrap">
        <header class="pro-public-header">
          <div class="pro-row">
            <span class="pro-dealer-mark">${quote?.contractor.name.slice(0, 1) ?? 'Q'}</span>
            <div><div class="pro-dealer-name">${quote?.contractor.name ?? 'Customer quote'}</div><div class="pro-account-name">Quote and selections</div></div>
          </div>
          <gable-theme-toggle></gable-theme-toggle>
        </header>
        ${this.hosted ? html`<div class="pro-notice lg:col-span-2" data-testid="public-preview">Preview: this is the customer view. Nothing has been sent.</div>` : nothing}
        ${this.error !== '' ? html`<div class="pro-error lg:col-span-2" role="alert">${this.error}</div>` : nothing}
        ${this.notice !== '' ? html`<div class="pro-success lg:col-span-2" role="status">${this.notice}</div>` : nothing}
        ${this.loading ? html`<div class="pro-empty lg:col-span-2" role="status"><p class="pro-card-title">Loading your quote...</p></div>` : quote !== null ? html`
          <div>
            <p class="pro-page-kicker">${quote.reference} · no sign in needed</p>
            <h1 class="pro-page-title">${quote.title}</h1>
            <p class="pro-page-subtitle">For ${quote.customer_name} · ${quote.job_site_address} · valid until ${shortDate(quote.valid_until)}</p>
            ${quote.note !== '' ? html`<div class="pro-notice mt-4">${quote.note}</div>` : nothing}
            ${quote.notice !== undefined && quote.notice !== '' ? html`<div class="pro-warning mt-4" data-testid="public-server-notice">${quote.notice}</div>` : nothing}
            ${this.renderSelections(quote)}
          </div>
          <div class="pro-public-side">
            ${this.renderTotals(quote)}
            ${this.stage === 'signed' || !quote.can_decide ? this.renderSigned(quote) : this.renderSignoff(quote)}
          </div>
        ` : nothing}
      </div>
    </main>`
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-customer-quote-page': GableCustomerQuotePage
  }
}
