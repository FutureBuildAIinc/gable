// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing, type TemplateResult } from 'lit'
import { customElement, property, state } from 'lit/decorators.js'
import { icon } from '@gable/design-system'
import { Check, CheckCircle2, X } from 'lucide'
import { acceptDealerQuote, declineDealerQuote, dealerTokenFromPath, getDealerQuote, type DealerQuote } from './dealer-api.ts'
import { formatCents, shortDate } from '../lib/format.ts'

const OFFLINE_WORDS = 'Reconnect to accept or decline. Nothing is queued while you are offline.'

const CLOSED_WORDS: Record<Exclude<DealerQuote['status'], 'open'>, [string, string]> = {
  accepted: ['Quote accepted', 'This quote was accepted. Your dealer can see the same status.'],
  declined: ['Quote declined', 'This quote was declined. Ask your dealer if you need a new one.'],
  expired: ['Quote expired', 'This quote has passed its valid until date. Ask your dealer for a new one.'],
  withdrawn: ['Quote closed', 'This quote is no longer open. Ask your dealer for a current one.'],
}

@customElement('gable-dealer-quote-page')
export class GableDealerQuotePage extends LitElement {
  createRenderRoot() { return this }

  @property({ attribute: false }) quote: DealerQuote | null = null
  @state() private loading = true
  @state() private busy = false
  @state() private error = ''
  @state() private notice = ''
  @state() private fullName = ''
  @state() private declineOpen = false
  @state() private declineReason = ''
  @state() private offline = navigator.onLine === false

  private token: string | null = null

  connectedCallback(): void {
    super.connectedCallback()
    window.addEventListener('online', this.onOnline)
    window.addEventListener('offline', this.onOffline)
    this.token = dealerTokenFromPath(window.location.pathname)
    void this.load()
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
      this.error = 'Use the complete quote link from your dealer.'
      this.loading = false
      return
    }
    try {
      this.quote = await getDealerQuote(this.token)
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'This quote did not load.'
    } finally {
      this.loading = false
    }
  }

  private get nameReady(): boolean {
    return this.fullName.trim().length >= 2
  }

  private async decide(accept: boolean): Promise<void> {
    if (this.token === null || this.busy || this.offline) return
    if (!this.nameReady) {
      this.error = accept ? 'Type your full name to accept.' : 'Type your full name to decline.'
      return
    }
    this.busy = true
    this.error = ''
    this.notice = ''
    try {
      this.quote = accept
        ? await acceptDealerQuote(this.token, this.fullName.trim())
        : await declineDealerQuote(this.token, this.fullName.trim(), this.declineReason.trim())
      this.notice = accept ? 'Your acceptance is recorded.' : 'Your response is recorded.'
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Your response was not recorded.'
      // A refusal means the quote moved on: show where it stands now.
      await this.refresh()
    } finally {
      this.busy = false
    }
  }

  private async refresh(): Promise<void> {
    if (this.token === null) return
    try {
      this.quote = await getDealerQuote(this.token)
    } catch {
      // The error above stays; the page keeps what it had.
    }
  }

  private renderLines(quote: DealerQuote): TemplateResult {
    return html`<section class="pro-card" data-testid="dealer-lines">
      <h2 class="pro-card-title">Lines</h2>
      <div class="mt-3 grid gap-2">
        ${quote.lines.map((line) => html`<div class="pro-option">
          <div class="pro-row-grow">
            <div class="pro-card-title">${line.description}</div>
            <div class="pro-text-small pro-data">${line.quantity} ${line.uom} at ${formatCents(Math.round(line.unit_price_ten_thousandths / 100))} per ${line.price_uom}</div>
          </div>
          <span class="pro-money">${formatCents(line.line_total_cents)}</span>
        </div>`)}
      </div>
      <div class="mt-3">
        ${quote.freight_cents > 0 ? html`<div class="pro-total-row"><span>Freight</span><span class="pro-data">${formatCents(quote.freight_cents)}</span></div>` : nothing}
        <div class="pro-total-row pro-total-grand"><span>Total</span><span class="pro-data" data-testid="dealer-total">${formatCents(quote.total_cents)}</span></div>
      </div>
    </section>`
  }

  private renderClosed(quote: DealerQuote): TemplateResult {
    const [title, words] = CLOSED_WORDS[quote.status === 'open' ? 'withdrawn' : quote.status]
    const accepted = quote.status === 'accepted'
    return html`<div class="pro-card mt-3" data-testid="dealer-closed" role="status">
      <div class="pro-row">
        ${icon(accepted ? CheckCircle2 : X, 24, accepted ? 'text-gable-green' : 'text-rose-300')}
        <div class="pro-row-grow">
          <h2 class="pro-card-title">${title}</h2>
          <p class="pro-text-small">${words}</p>
        </div>
      </div>
    </div>`
  }

  private renderDecision(): TemplateResult {
    return html`<div class="pro-card mt-3" data-testid="dealer-decision">
      <h2 class="pro-card-title">Your answer</h2>
      <p class="pro-text-small">Your full name is recorded with your answer.</p>
      <label class="pro-field mt-3"><span class="pro-label">Full name</span><input class="pro-input" name="full_name" autocomplete="name" .value=${this.fullName} @input=${(event: Event) => { this.fullName = (event.target as HTMLInputElement).value }} /></label>
      ${this.offline ? html`<p class="pro-warning" data-testid="dealer-offline">${OFFLINE_WORDS}</p>` : nothing}
      <button type="button" class="pro-btn pro-btn-primary pro-btn-wide" data-testid="dealer-accept" ?disabled=${!this.nameReady || this.busy || this.offline} @click=${() => void this.decide(true)}>${icon(Check, 14)} ${this.busy ? 'Saving...' : 'Accept quote'}</button>
      <button type="button" class="pro-btn pro-btn-ghost pro-btn-wide mt-2" data-testid="dealer-decline-open" @click=${() => { this.declineOpen = !this.declineOpen }}>Decline instead</button>
      ${this.declineOpen ? html`<div class="mt-2">
        <label class="pro-field"><span class="pro-label">Reason, optional</span><textarea class="pro-textarea" name="reason" .value=${this.declineReason} @input=${(event: Event) => { this.declineReason = (event.target as HTMLTextAreaElement).value }}></textarea></label>
        <button type="button" class="pro-btn pro-btn-danger pro-btn-wide" data-testid="dealer-decline" ?disabled=${!this.nameReady || this.busy || this.offline} @click=${() => void this.decide(false)}>Decline this quote</button>
      </div>` : nothing}
    </div>`
  }

  render(): TemplateResult {
    const quote = this.quote
    return html`<main class="pro-main" data-testid="dealer-page">
      <div class="pro-public-wrap">
        <header class="pro-public-header">
          <div class="pro-row">
            <span class="pro-dealer-mark">${quote?.dealer_name.slice(0, 1) || 'Q'}</span>
            <div><div class="pro-dealer-name">${quote?.dealer_name || 'Dealer quote'}</div><div class="pro-account-name">Quote</div></div>
          </div>
          <gable-theme-toggle></gable-theme-toggle>
        </header>
        ${this.error !== '' ? html`<div class="pro-error lg:col-span-2" role="alert" data-testid="dealer-error">${this.error}</div>` : nothing}
        ${this.notice !== '' ? html`<div class="pro-success lg:col-span-2" role="status">${this.notice}</div>` : nothing}
        ${this.loading ? html`<div class="pro-empty lg:col-span-2" role="status"><p class="pro-card-title">Loading your quote...</p></div>` : quote !== null ? html`
          <div>
            <p class="pro-page-kicker">${quote.number} · no sign in needed</p>
            <h1 class="pro-page-title">Quote ${quote.number}</h1>
            <p class="pro-page-subtitle">For ${quote.customer_name}${quote.expires_at !== null ? html` · valid until ${shortDate(quote.expires_at)}` : nothing}</p>
            ${this.renderLines(quote)}
          </div>
          <div class="pro-public-side">${quote.status === 'open' && quote.can_decide ? this.renderDecision() : this.renderClosed(quote)}</div>
        ` : nothing}
      </div>
    </main>`
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-dealer-quote-page': GableDealerQuotePage
  }
}
