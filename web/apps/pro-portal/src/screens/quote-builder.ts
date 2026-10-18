// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing, type TemplateResult } from 'lit'
import { customElement, property, state } from 'lit/decorators.js'
import { ApiError } from '@gable/api-client'
import { icon } from '@gable/design-system'
import { Download, Eye, FilePlus2, Link2, PackagePlus, RefreshCw, Send, Trash2, X } from 'lucide'
import { assertWritesOnline, portalClient, portalRequest } from '../lib/api.ts'
import { can } from '../lib/capabilities.ts'
import { formatBps, formatCents, formatDollars, statusLabel } from '../lib/format.ts'
import { OFFLINE_WRITE_REASON } from '../lib/offline.ts'
import { navigate } from '../lib/router.ts'
import { statusChip } from '../lib/status.ts'
import { bpsToPercent, emptyQuoteInput, percentToBps, quoteToInput, revisionHeader, type QuoteMarkup } from './quote-input.ts'
import type { CustomerQuoteConfirmPreview, CustomerQuoteInput, CustomerQuoteSummary, CustomerQuoteView, PortalCatalogProduct, PortalMe, Project } from '../types.ts'
import { sanitizePublicQuote, type PublicQuote } from '../public/public-view.ts'
import '../public/public-page.ts'

type LineInput = NonNullable<CustomerQuoteInput['lines']>[number]
type LabourInput = NonNullable<CustomerQuoteInput['labour']>[number]
type OverheadInput = NonNullable<CustomerQuoteInput['overhead']>[number]

@customElement('gable-pro-quote-builder')
export class GableProQuoteBuilder extends LitElement {
  createRenderRoot() { return this }

  @property({ attribute: false }) me!: PortalMe
  @property({ type: Boolean }) offline = false
  @property({ type: String }) quoteId: string | null = null
  @state() private summaries: CustomerQuoteSummary[] = []
  @state() private quote: CustomerQuoteView | null = null
  @state() private input: CustomerQuoteInput = emptyQuoteInput()
  @state() private catalog: PortalCatalogProduct[] = []
  @state() private projects: Project[] = []
  @state() private preview: PublicQuote | null = null
  @state() private confirmPreview: CustomerQuoteConfirmPreview | null = null
  @state() private sharedLink = ''
  @state() private catalogOpen = false
  @state() private catalogSearch = ''
  @state() private loading = true
  @state() private busy = false
  @state() private dirty = false
  @state() private error = ''
  @state() private message = ''

  connectedCallback(): void {
    super.connectedCallback()
    void this.load()
  }

  updated(changed: Map<PropertyKey, unknown>): void {
    if (changed.has('quoteId')) {
      const previous = changed.get('quoteId')
      if (this.quoteId !== null && previous !== this.quoteId) void this.loadQuote(this.quoteId)
      if (this.quoteId === null && previous) this.resetDraft()
    }
  }

  private get canEdit(): boolean {
    return can(this.me.capabilities, 'request_quotes') && (this.quote === null || this.quote.status === 'draft')
  }

  private async load(): Promise<void> {
    this.loading = true
    this.error = ''
    try {
      const [quotes, catalog, projects] = await Promise.all([
        portalClient.get('/api/portal/v1/customer-quotes', { query: { limit: 30 } }),
        portalClient.get('/api/portal/v1/catalog'),
        portalClient.get('/api/portal/v1/projects', { query: { limit: 50 } }),
      ])
      this.summaries = quotes.body.items
      this.catalog = catalog.body
      this.projects = projects.body.items
      if (this.quoteId !== null) await this.loadQuote(this.quoteId)
      else if (this.summaries[0] !== undefined) await this.loadQuote(this.summaries[0].id)
      else this.resetDraft()
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Customer quotes did not load.'
    } finally {
      this.loading = false
    }
  }

  private async loadQuote(id: string): Promise<void> {
    this.busy = true
    this.error = ''
    try {
      const response = await portalClient.get('/api/portal/v1/customer-quotes/{id}', { path: { id } })
      this.quote = response.body
      this.input = quoteToInput(response.body)
      this.dirty = false
      this.preview = null
      this.confirmPreview = null
      this.sharedLink = ''
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'The quote did not load.'
    } finally {
      this.busy = false
    }
  }

  private resetDraft(): void {
    this.quote = null
    this.input = emptyQuoteInput()
    this.dirty = true
    this.preview = null
    this.confirmPreview = null
    this.sharedLink = ''
    this.message = ''
  }

  private patchInput(patch: Partial<CustomerQuoteInput>): void {
    this.input = { ...this.input, ...patch }
    this.dirty = true
    this.message = ''
  }

  private patchLine(index: number, patch: Partial<LineInput>): void {
    const lines = [...(this.input.lines ?? [])]
    const current = lines[index]
    if (current === undefined) return
    lines[index] = { ...current, ...patch }
    this.patchInput({ lines })
  }

  private patchLabour(index: number, patch: Partial<LabourInput>): void {
    const labour = [...(this.input.labour ?? [])]
    const current = labour[index]
    if (current === undefined) return
    labour[index] = { ...current, ...patch }
    this.patchInput({ labour })
  }

  private patchOverhead(index: number, patch: Partial<OverheadInput>): void {
    const overhead = [...(this.input.overhead ?? [])]
    const current = overhead[index]
    if (current === undefined) return
    overhead[index] = { ...current, ...patch }
    this.patchInput({ overhead })
  }

  private async save(): Promise<void> {
    if (!this.canEdit || this.busy || this.offline || !this.dirty) return
    this.busy = true
    this.error = ''
    this.message = ''
    try {
      assertWritesOnline()
      if (this.quote === null) {
        const response = await portalClient.post('/api/portal/v1/customer-quotes', this.input, { idempotencyKey: crypto.randomUUID() })
        this.quote = response.body
      } else {
        const response = await portalClient.put('/api/portal/v1/customer-quotes/{id}', this.input, {
          path: { id: this.quote.id },
          headers: { 'If-Match': revisionHeader(this.quote) },
          idempotencyKey: crypto.randomUUID(),
        })
        this.quote = response.body
      }
      this.input = quoteToInput(this.quote)
      this.dirty = false
      this.message = 'Draft saved and priced by the dealer.'
      const list = await portalClient.get('/api/portal/v1/customer-quotes', { query: { limit: 30 } })
      this.summaries = list.body.items
      if (this.quoteId !== this.quote.id) navigate(`/quotes/${this.quote.id}`)
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'The draft could not be saved.'
    } finally {
      this.busy = false
    }
  }

  private async showPreview(): Promise<void> {
    if (this.quote === null || this.dirty || this.busy) return
    this.busy = true
    this.error = ''
    try {
      const response = await portalClient.get('/api/portal/v1/customer-quotes/{id}/preview', { path: { id: this.quote.id } })
      this.preview = sanitizePublicQuote(response.body)
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'The customer preview did not load.'
    } finally {
      this.busy = false
    }
  }

  private async send(): Promise<void> {
    if (this.quote === null || this.dirty || this.busy || this.offline || !can(this.me.capabilities, 'request_quotes')) return
    this.busy = true
    this.error = ''
    try {
      assertWritesOnline()
      const response = await portalClient.post('/api/portal/v1/customer-quotes/{id}/send', undefined, {
        path: { id: this.quote.id },
        headers: { 'If-Match': revisionHeader(this.quote) },
        idempotencyKey: crypto.randomUUID(),
      })
      this.quote = response.body.quote
      this.input = quoteToInput(this.quote)
      this.sharedLink = response.body.link.path
      this.message = response.body.link.delivered ? 'Quote sent and link issued.' : 'Link issued. Share it with your customer.'
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'The quote could not be sent.'
    } finally {
      this.busy = false
    }
  }

  private async rotate(): Promise<void> {
    if (this.quote === null || this.busy || this.offline) return
    this.busy = true
    this.error = ''
    try {
      assertWritesOnline()
      const response = await portalClient.post('/api/portal/v1/customer-quotes/{id}/link/rotate', undefined, {
        path: { id: this.quote.id },
        idempotencyKey: crypto.randomUUID(),
      })
      this.quote = response.body.quote
      this.sharedLink = response.body.link.path
      this.message = 'The old link is dead. A new link is ready.'
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'The link could not be rotated.'
    } finally {
      this.busy = false
    }
  }

  private async revoke(): Promise<void> {
    if (this.quote === null || this.busy || this.offline) return
    this.busy = true
    this.error = ''
    try {
      assertWritesOnline()
      await portalClient.post('/api/portal/v1/customer-quotes/{id}/link/revoke', undefined, {
        path: { id: this.quote.id },
        idempotencyKey: crypto.randomUUID(),
      })
      this.quote = { ...this.quote, link: null }
      this.sharedLink = ''
      this.message = 'The customer link is revoked.'
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'The link could not be revoked.'
    } finally {
      this.busy = false
    }
  }

  private async confirm(acceptPriceChanges = false): Promise<void> {
    if (this.quote === null || this.busy || this.offline || !can(this.me.capabilities, 'approve_quotes')) return
    this.busy = true
    this.error = ''
    try {
      assertWritesOnline()
      this.quote = await portalRequest<CustomerQuoteView>(
        'POST',
        `/api/portal/v1/customer-quotes/${this.quote.id}/confirm`,
        acceptPriceChanges ? { accept_price_changes: true } : undefined,
        { idempotencyKey: crypto.randomUUID() },
      )
      this.confirmPreview = null
      this.message = 'Customer approval confirmed into the dealer order.'
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) {
        try {
          const preview = await portalClient.get('/api/portal/v1/customer-quotes/{id}/confirm-preview', { path: { id: this.quote.id } })
          this.confirmPreview = preview.body
          this.error = 'Dealer material prices moved since this quote was priced. Review the current cost and confirm.'
          return
        } catch {
          // Fall back to standard error handling if preview cannot be loaded.
        }
      }
      this.error = error instanceof Error ? error.message : 'The quote could not be confirmed.'
    } finally {
      this.busy = false
    }
  }

  private async exportQuotes(format: 'json' | 'csv'): Promise<void> {
    this.error = ''
    try {
      const response = await portalClient.get('/api/portal/v1/customer-quotes/export', {
        query: format === 'csv' ? { format, kind: 'quotes' } : { format },
      })
      const text = format === 'json' ? JSON.stringify(response.body, null, 2) : response.body as unknown as string
      const blob = new Blob([text], { type: format === 'json' ? 'application/json' : 'text/csv' })
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = `customer-quotes.${format}`
      anchor.click()
      URL.revokeObjectURL(url)
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'The export did not load.'
    }
  }

  private addProduct(product: PortalCatalogProduct): void {
    if ((this.input.lines ?? []).some((line) => line.product_id === product.id)) {
      this.catalogOpen = false
      this.message = `${product.name} is already on this quote.`
      return
    }
    this.patchInput({ lines: [...(this.input.lines ?? []), { product_id: product.id, quantity: '1', group_key: null, markup: null, note: '' }] })
    this.catalogOpen = false
    this.message = `${product.name} added. Save to capture the account price.`
  }

  private markupEditor(value: QuoteMarkup | null | undefined, onChange: (markup: QuoteMarkup | null) => void, placeholder = 'Default'): TemplateResult {
    const kind = value?.kind ?? 'markup'
    return html`<div class="grid grid-cols-1 sm:grid-cols-2 gap-2">
      <select class="pro-select" aria-label="Markup kind" .value=${kind} @change=${(event: Event) => {
        const next = (event.target as HTMLSelectElement).value === 'margin' ? 'margin' as const : 'markup' as const
        onChange({ kind: next, bps: value?.bps ?? 0 })
      }}>
        <option value="markup">Markup on cost</option>
        <option value="margin">Target margin</option>
      </select>
      <input class="pro-input" aria-label="Markup percent" type="number" min="0" step="0.01" placeholder=${placeholder} .value=${bpsToPercent(value?.bps)} @input=${(event: Event) => {
        const raw = (event.target as HTMLInputElement).value
        onChange(raw === '' ? null : { kind, bps: percentToBps(raw) })
      }} />
    </div>`
  }

  private renderCustomer(): TemplateResult {
    return html`<section class="pro-card">
      <h2 class="pro-card-title">Project and customer</h2>
      <div class="pro-form-grid mt-3">
        <label class="pro-field sm:col-span-2"><span class="pro-label">Quote title</span><input class="pro-input" data-testid="quote-title" .value=${this.input.title} @input=${(event: Event) => this.patchInput({ title: (event.target as HTMLInputElement).value })} required /></label>
        <label class="pro-field"><span class="pro-label">Project</span><select class="pro-select" .value=${this.input.project_id ?? ''} @change=${(event: Event) => { const value = (event.target as HTMLSelectElement).value; this.patchInput({ project_id: value === '' ? null : value }) }}>
          <option value="">No project</option>
          ${this.projects.map((project) => html`<option value=${project.id} ?selected=${project.id === this.input.project_id}>${project.name}</option>`)}
          ${this.input.project_id !== null && !this.projects.some((project) => project.id === this.input.project_id) ? html`<option value=${this.input.project_id} selected>Current project</option>` : nothing}
        </select></label>
        <label class="pro-field"><span class="pro-label">Job site address</span><input class="pro-input" .value=${this.input.job_site_address ?? ''} @input=${(event: Event) => this.patchInput({ job_site_address: (event.target as HTMLInputElement).value })} /></label>
        <label class="pro-field"><span class="pro-label">Customer name</span><input class="pro-input" .value=${this.input.end_customer.name} @input=${(event: Event) => this.patchInput({ end_customer: { ...this.input.end_customer, name: (event.target as HTMLInputElement).value } })} required /></label>
        <label class="pro-field"><span class="pro-label">Customer email</span><input class="pro-input" type="email" .value=${this.input.end_customer.email ?? ''} @input=${(event: Event) => this.patchInput({ end_customer: { ...this.input.end_customer, email: (event.target as HTMLInputElement).value } })} /></label>
        <label class="pro-field"><span class="pro-label">Customer phone</span><input class="pro-input" type="tel" .value=${this.input.end_customer.phone ?? ''} @input=${(event: Event) => this.patchInput({ end_customer: { ...this.input.end_customer, phone: (event.target as HTMLInputElement).value } })} /></label>
        <label class="pro-field"><span class="pro-label">Site contact</span><input class="pro-input" .value=${this.input.site_contact?.name ?? ''} @input=${(event: Event) => this.patchInput({ site_contact: { name: (event.target as HTMLInputElement).value, email: '', phone: this.input.site_contact?.phone ?? '' } })} /></label>
        <label class="pro-field"><span class="pro-label">Site phone</span><input class="pro-input" type="tel" .value=${this.input.site_contact?.phone ?? ''} @input=${(event: Event) => this.patchInput({ site_contact: { name: this.input.site_contact?.name ?? '', email: '', phone: (event.target as HTMLInputElement).value } })} /></label>
        <label class="pro-field"><span class="pro-label">Valid until, UTC, 24 hour ISO</span><input class="pro-input" type="text" inputmode="numeric" placeholder="YYYY-MM-DDT07:00" .value=${this.input.valid_until?.slice(0, 16) ?? ''} @input=${(event: Event) => { const value = (event.target as HTMLInputElement).value; this.patchInput({ valid_until: value === '' ? null : /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value) ? `${value}:00Z` : value }) }} /></label>
      </div>
    </section>`
  }

  private renderLines(): TemplateResult {
    const lines = this.input.lines ?? []
    return html`<section class="pro-card" data-testid="quote-lines">
      <div class="pro-row"><div class="pro-row-grow"><h2 class="pro-card-title">Materials at your cost</h2><p class="pro-text-small">Line override wins, then category, then the quote default.</p></div>
        <button type="button" class="pro-btn pro-btn-secondary pro-btn-small" data-testid="add-product" ?disabled=${!this.canEdit || this.offline} @click=${() => { this.catalogOpen = true }}>${icon(PackagePlus, 14)} Add</button></div>
      <div class="mt-3 grid gap-3">
        ${lines.length === 0 ? html`<div class="pro-empty"><p class="pro-text-small">Add catalog products to start the quote.</p></div>` : lines.map((line, index) => {
          const stored = this.quote?.lines.find((item) => item.id === line.id)
          const product = this.catalog.find((item) => item.id === line.product_id)
          return html`<div class="rounded-2xl border border-zinc-800 bg-surface-2 p-3" data-testid=${`quote-line-${index}`}>
            <div class="pro-row"><div class="pro-row-grow"><div class="pro-card-title">${stored?.description ?? product?.name ?? 'Catalog product'}</div><div class="pro-text-small pro-data">${stored?.sku ?? product?.sku ?? ''}</div></div>
              <button type="button" class="pro-icon-btn" aria-label="Remove line" ?disabled=${!this.canEdit || this.offline} @click=${() => this.patchInput({ lines: lines.filter((_item, itemIndex) => itemIndex !== index) })}>${icon(Trash2, 15)}</button></div>
            <div class="pro-form-grid mt-3">
              <label class="pro-field"><span class="pro-label">Quantity</span><input class="pro-input pro-data" inputmode="decimal" .value=${line.quantity} @input=${(event: Event) => this.patchLine(index, { quantity: (event.target as HTMLInputElement).value })} /></label>
              <label class="pro-field"><span class="pro-label">Option group</span><select class="pro-select" .value=${line.group_key ?? ''} @change=${(event: Event) => { const value = (event.target as HTMLSelectElement).value; this.patchLine(index, { group_key: value === '' ? null : value }) }}>
                <option value="">Standalone line</option>
                ${(this.input.groups ?? []).map((group) => html`<option value=${group.key} ?selected=${group.key === line.group_key}>${group.label}</option>`)}
              </select></label>
            </div>
            <span class="pro-label">Line markup</span>
            ${this.markupEditor(line.markup, (markup) => this.patchLine(index, { markup }), 'Category or quote')}
            <div class="pro-text-small mt-2 flex flex-wrap gap-x-4 gap-y-1">
              <span>Cost <span class="pro-data">${stored === undefined ? 'Save to price' : formatCents(stored.cost_cents)}</span></span>
              <span>Sell <span class="pro-data">${stored === undefined ? 'Save to price' : formatCents(stored.sell_cents)}</span></span>
              <span>Tier <span class="pro-data">${stored?.applied.tier ?? 'not priced'}</span></span>
            </div>
          </div>`
        })}
      </div>
    </section>`
  }

  private renderGroups(): TemplateResult {
    const groups = this.input.groups ?? []
    return html`<section class="pro-card">
      <div class="pro-row"><div class="pro-row-grow"><h2 class="pro-card-title">Option groups</h2><p class="pro-text-small">The customer chooses one line in each group.</p></div>
        <button type="button" class="pro-btn pro-btn-ghost pro-btn-small" ?disabled=${!this.canEdit || this.offline} @click=${() => this.patchInput({ groups: [...groups, { key: crypto.randomUUID(), label: `Option group ${groups.length + 1}` }] })}>${icon(FilePlus2, 14)} Add group</button></div>
      ${groups.length === 0 ? html`<p class="pro-text-small mt-3">No option groups. Every line stands alone.</p>` : groups.map((group, index) => html`<div class="pro-row mt-3">
        <input class="pro-input" aria-label="Option group label" .value=${group.label} @input=${(event: Event) => {
          const next = [...groups]
          next[index] = { ...group, label: (event.target as HTMLInputElement).value }
          this.patchInput({ groups: next })
        }} />
        <button type="button" class="pro-icon-btn" aria-label="Remove option group" @click=${() => this.patchInput({ groups: groups.filter((_item, itemIndex) => itemIndex !== index), lines: (this.input.lines ?? []).map((line) => line.group_key === group.key ? { ...line, group_key: null } : line) })}>${icon(X, 15)}</button>
      </div>`)}
    </section>`
  }

  private categoryRows(): { id: string, name: string }[] {
    const rows = new Map<string, string>()
    for (const product of this.catalog) {
      if ((this.input.lines ?? []).some((line) => line.product_id === product.id) && product.category_id !== null) rows.set(product.category_id, product.category)
    }
    for (const line of this.quote?.lines ?? []) {
      if (line.category_id !== null && !rows.has(line.category_id)) rows.set(line.category_id, line.sku)
    }
    return [...rows].map(([id, name]) => ({ id, name }))
  }

  private renderCategoryRules(): TemplateResult {
    const rows = this.categoryRows()
    if (rows.length === 0) return html``
    return html`<section class="pro-card" data-testid="category-rules">
      <h2 class="pro-card-title">Category markup</h2>
      <p class="pro-text-small">Used where a line has no override.</p>
      ${rows.map((category) => {
        const rule = (this.input.category_rules ?? []).find((item) => item.category_id === category.id)
        return html`<div class="mt-3" data-testid=${`category-rule-${category.id}`}><span class="pro-label">${category.name}</span>
          ${this.markupEditor(rule?.markup, (markup) => {
            const rules = [...(this.input.category_rules ?? []).filter((item) => item.category_id !== category.id)]
            if (markup !== null) rules.push({ category_id: category.id, markup })
            this.patchInput({ category_rules: rules })
          }, 'Quote default')}</div>`
      })}
    </section>`
  }

  private renderLabourOverhead(): TemplateResult {
    return html`<section class="pro-card" data-testid="labour-overhead">
      <h2 class="pro-card-title">Labour and overhead</h2>
      ${(this.input.labour ?? []).map((line, index) => html`<div class="mt-3 rounded-xl border border-zinc-800 p-3" data-testid=${`labour-${index}`}>
        <div class="pro-form-grid">
          <label class="pro-field sm:col-span-2"><span class="pro-label">Labour description</span><input class="pro-input" .value=${line.description} @input=${(event: Event) => this.patchLabour(index, { description: (event.target as HTMLInputElement).value })} /></label>
          <label class="pro-field"><span class="pro-label">Rate type</span><select class="pro-select" .value=${line.rate_type} @change=${(event: Event) => this.patchLabour(index, { rate_type: (event.target as HTMLSelectElement).value === 'hourly' ? 'hourly' : 'flat' })}><option value="flat">Flat</option><option value="hourly">Hourly</option></select></label>
          <label class="pro-field"><span class="pro-label">Rate in cents</span><input class="pro-input pro-data" type="number" min="0" .value=${String(line.rate_cents)} @input=${(event: Event) => this.patchLabour(index, { rate_cents: Number((event.target as HTMLInputElement).value) })} /></label>
          ${line.rate_type === 'hourly' ? html`<label class="pro-field"><span class="pro-label">Hours</span><input class="pro-input pro-data" inputmode="decimal" .value=${line.hours ?? ''} @input=${(event: Event) => this.patchLabour(index, { hours: (event.target as HTMLInputElement).value })} /></label>` : nothing}
        </div>
        <label class="pro-text-small flex items-center gap-2"><input class="pro-check" type="checkbox" .checked=${line.contractor_only ?? false} @change=${(event: Event) => this.patchLabour(index, { contractor_only: (event.target as HTMLInputElement).checked })} /> Keep this line off the customer page</label>
        <button type="button" class="pro-btn pro-btn-ghost pro-btn-small" @click=${() => this.patchInput({ labour: (this.input.labour ?? []).filter((_item, itemIndex) => itemIndex !== index) })}>Remove labour</button>
      </div>`)}
      <button type="button" class="pro-btn pro-btn-secondary pro-btn-small mt-3" data-testid="add-labour" ?disabled=${!this.canEdit || this.offline} @click=${() => this.patchInput({ labour: [...(this.input.labour ?? []), { description: 'Labour', rate_type: 'flat', rate_cents: 0, hours: '', contractor_only: false }] })}>Add labour</button>
      <div class="my-4 border-t border-zinc-800"></div>
      ${(this.input.overhead ?? []).map((line, index) => html`<div class="mt-3 rounded-xl border border-zinc-800 p-3" data-testid=${`overhead-${index}`}>
        <div class="pro-form-grid">
          <label class="pro-field sm:col-span-2"><span class="pro-label">Overhead label</span><input class="pro-input" .value=${line.label} @input=${(event: Event) => this.patchOverhead(index, { label: (event.target as HTMLInputElement).value })} /></label>
          <label class="pro-field"><span class="pro-label">Amount type</span><select class="pro-select" .value=${line.amount_type} @change=${(event: Event) => this.patchOverhead(index, { amount_type: (event.target as HTMLSelectElement).value === 'percent' ? 'percent' : 'flat', value: 0 })}><option value="flat">Flat</option><option value="percent">Percent</option></select></label>
          <label class="pro-field"><span class="pro-label">${line.amount_type === 'percent' ? 'Percent' : 'Amount in cents'}</span><input class="pro-input pro-data" type="number" min="0" step="0.01" .value=${line.amount_type === 'percent' ? bpsToPercent(line.value) : String(line.value)} @input=${(event: Event) => { const raw = (event.target as HTMLInputElement).value; this.patchOverhead(index, { value: line.amount_type === 'percent' ? percentToBps(raw) : Number(raw) }) }} /></label>
        </div>
        <label class="pro-text-small flex items-center gap-2"><input class="pro-check" type="checkbox" .checked=${line.contractor_only ?? false} @change=${(event: Event) => this.patchOverhead(index, { contractor_only: (event.target as HTMLInputElement).checked })} /> Keep this line off the customer page</label>
        <button type="button" class="pro-btn pro-btn-ghost pro-btn-small" @click=${() => this.patchInput({ overhead: (this.input.overhead ?? []).filter((_item, itemIndex) => itemIndex !== index) })}>Remove overhead</button>
      </div>`)}
      <button type="button" class="pro-btn pro-btn-secondary pro-btn-small mt-3" data-testid="add-overhead" ?disabled=${!this.canEdit || this.offline} @click=${() => this.patchInput({ overhead: [...(this.input.overhead ?? []), { label: 'Overhead', amount_type: 'flat', value: 0, contractor_only: false }] })}>Add overhead</button>
    </section>`
  }

  private renderTerms(): TemplateResult {
    const tax = this.input.tax ?? { mode: 'site_rate' as const, rate_bps: 0 }
    return html`<section class="pro-card" data-testid="quote-terms">
      <h2 class="pro-card-title">Customer terms</h2>
      <label class="pro-text-small mt-3 flex items-center gap-2"><input class="pro-check" data-testid="hide-line-prices" type="checkbox" .checked=${this.input.hide_line_prices ?? false} @change=${(event: Event) => this.patchInput({ hide_line_prices: (event.target as HTMLInputElement).checked })} /> Hide line prices and show one lump sum</label>
      <div class="pro-form-grid mt-3">
        <label class="pro-field"><span class="pro-label">Tax mode</span><select class="pro-select" .value=${tax.mode} @change=${(event: Event) => { const mode = (event.target as HTMLSelectElement).value as typeof tax.mode; this.patchInput({ tax: { mode, rate_bps: mode === 'site_rate' ? tax.rate_bps : 0 } }) }}><option value="site_rate">Site rate</option><option value="included">Tax included</option><option value="at_checkout">Tax at checkout</option></select></label>
        ${tax.mode === 'site_rate' ? html`<label class="pro-field"><span class="pro-label">Site tax percent</span><input class="pro-input pro-data" type="number" min="0" step="0.01" .value=${bpsToPercent(tax.rate_bps)} @input=${(event: Event) => this.patchInput({ tax: { mode: 'site_rate', rate_bps: percentToBps((event.target as HTMLInputElement).value) } })} /></label>` : nothing}
        <label class="pro-field"><span class="pro-label">Deposit percent</span><input class="pro-input pro-data" type="number" min="0" step="0.01" .value=${bpsToPercent(this.input.deposit_bps)} @input=${(event: Event) => this.patchInput({ deposit_bps: percentToBps((event.target as HTMLInputElement).value) })} /></label>
        <label class="pro-field sm:col-span-2"><span class="pro-label">Payment terms</span><textarea class="pro-textarea" .value=${this.input.payment_terms ?? ''} @input=${(event: Event) => this.patchInput({ payment_terms: (event.target as HTMLTextAreaElement).value })}></textarea></label>
        <label class="pro-field sm:col-span-2"><span class="pro-label">Customer note</span><textarea class="pro-textarea" .value=${this.input.customer_note ?? ''} @input=${(event: Event) => this.patchInput({ customer_note: (event.target as HTMLTextAreaElement).value })}></textarea></label>
      </div>
      <label class="pro-text-small flex items-center gap-2"><input class="pro-check" type="checkbox" .checked=${this.input.require_code ?? false} @change=${(event: Event) => this.patchInput({ require_code: (event.target as HTMLInputElement).checked })} /> Require a six digit code on sign-off</label>
    </section>`
  }

  private renderSummary(): TemplateResult {
    const quote = this.quote
    if (quote === null) return html`<div class="pro-card"><h2 class="pro-card-title">New customer quote</h2><p class="pro-text-small">Save the draft to capture dealer costs and calculate customer prices.</p></div>`
    return html`<div class="grid gap-3">
      <div class="pro-card" data-testid="quote-summary">
        <div class="pro-row"><div class="pro-row-grow"><p class="pro-page-kicker">${quote.reference}</p><h2 class="pro-card-title">${statusLabel(quote.effective_status)}</h2></div>${statusChip(quote.effective_status)}</div>
        <div class="pro-summary-grid">
          <div class="pro-stat"><span class="pro-stat-value">${formatCents(quote.totals.material_cost_cents)}</span><span class="pro-stat-label">Your material cost</span></div>
          <div class="pro-stat"><span class="pro-stat-value">${formatCents(quote.totals.material_sell_cents)}</span><span class="pro-stat-label">Customer materials</span></div>
          <div class="pro-stat"><span class="pro-stat-value">${formatCents(quote.totals.margin_cents)}</span><span class="pro-stat-label">Margin · ${formatBps(quote.totals.margin_bps)}</span></div>
          <div class="pro-stat"><span class="pro-stat-value">${formatCents(quote.totals.total_cents)}</span><span class="pro-stat-label">Customer total</span></div>
        </div>
        <p class="pro-text-small mt-3">Revision ${quote.revision}. The dealer sees the project and dealer prices, never your markup or customer sell price.</p>
      </div>
      <div class="pro-card">
        <h2 class="pro-card-title">Customer loop</h2>
        <div class="mt-3 flex flex-wrap gap-2">
          <button type="button" class="pro-btn pro-btn-secondary pro-btn-small" data-testid="preview-quote" ?disabled=${this.dirty || this.busy} @click=${() => void this.showPreview()}>${icon(Eye, 14)} Preview</button>
          ${quote.status === 'draft' ? html`<button type="button" class="pro-btn pro-btn-primary pro-btn-small" data-testid="send-quote" ?disabled=${this.dirty || this.offline || this.busy || !can(this.me.capabilities, 'request_quotes')} @click=${() => void this.send()}>${icon(Send, 14)} Send</button>` : nothing}
          ${quote.link !== null ? html`<button type="button" class="pro-btn pro-btn-secondary pro-btn-small" data-testid="rotate-link" ?disabled=${this.offline || this.busy} @click=${() => void this.rotate()}>${icon(RefreshCw, 14)} Rotate link</button>
            <button type="button" class="pro-btn pro-btn-ghost pro-btn-small" data-testid="revoke-link" ?disabled=${this.offline || this.busy} @click=${() => void this.revoke()}>Revoke</button>` : nothing}
          ${quote.status === 'approved' && can(this.me.capabilities, 'approve_quotes') ? html`<button type="button" class="pro-btn pro-btn-primary pro-btn-small" data-testid="confirm-quote" ?disabled=${this.offline || this.busy} @click=${() => void this.confirm()}>Confirm to dealer order</button>` : nothing}
        </div>
        ${this.confirmPreview !== null && quote.status === 'approved' ? html`<div class="pro-warning mt-3" data-testid="confirm-preview">
          <div class="font-medium">Dealer material cost changed: ${formatCents(this.confirmPreview.captured_material_cost_cents)} to ${formatCents(this.confirmPreview.current_material_cost_cents)}</div>
          <div class="pro-text-small mt-1">Projected margin: ${formatCents(this.confirmPreview.projected_margin_cents)}</div>
          <button type="button" class="pro-btn pro-btn-primary pro-btn-small mt-2" data-testid="confirm-accept-price-changes" ?disabled=${this.offline || this.busy} @click=${() => void this.confirm(true)}>Accept current dealer prices and confirm</button>
        </div>` : nothing}
        ${this.sharedLink !== '' ? html`<div class="pro-success mt-3" data-testid="shared-link"><div class="pro-row">${icon(Link2, 16)}<a class="underline" href=${this.sharedLink} target="_blank" rel="noopener">Open the customer link</a></div></div>` : nothing}
        ${quote.order !== null ? html`<div class="pro-notice mt-3">Dealer order ${quote.order.number} · ${statusLabel(quote.order.fulfilment)}</div>` : nothing}
      </div>
    </div>`
  }

  private renderCatalogSheet(): TemplateResult | typeof nothing {
    if (!this.catalogOpen) return nothing
    const term = this.catalogSearch.toLowerCase()
    const products = this.catalog.filter((product) => `${product.name} ${product.sku}`.toLowerCase().includes(term))
    return html`<div class="pro-sheet-backdrop" @click=${() => { this.catalogOpen = false }}></div>
      <div class="pro-sheet" role="dialog" aria-modal="true" aria-label="Add catalog product" data-testid="catalog-sheet">
        <div class="pro-row mb-3"><div class="pro-row-grow"><h2 class="pro-card-title">Add from the catalog</h2><p class="pro-text-small">The server captures the account price when you save.</p></div><button class="pro-icon-btn" aria-label="Close" @click=${() => { this.catalogOpen = false }}>${icon(X, 16)}</button></div>
        <input class="pro-input" type="search" aria-label="Search products" placeholder="Search products or SKU" .value=${this.catalogSearch} @input=${(event: Event) => { this.catalogSearch = (event.target as HTMLInputElement).value }} />
        <div class="mt-3 grid gap-2">
          ${products.map((product) => html`<div class="pro-option">
            <span class="pro-swatch">${icon(PackagePlus, 19)}</span>
            <div class="pro-row-grow"><div class="pro-card-title">${product.name}</div><div class="pro-text-small"><span class="pro-data">${product.sku}</span> · ${product.available} available · ${product.lead_time_days === null ? 'Ask about lead time' : `${product.lead_time_days} day lead`}</div><div class="pro-money">${formatDollars(product.customer_price)} / ${product.uom}</div></div>
            <button type="button" class="pro-btn pro-btn-primary pro-btn-small" data-testid=${`add-product-${product.id}`} ?disabled=${(this.input.lines ?? []).some((line) => line.product_id === product.id)} @click=${() => this.addProduct(product)}>Add</button>
          </div>`)}
        </div>
      </div>`
  }

  private renderPreviewSheet(): TemplateResult | typeof nothing {
    if (this.preview === null) return nothing
    return html`<div class="pro-sheet-backdrop" @click=${() => { this.preview = null }}></div>
      <div class="pro-sheet lg:max-w-5xl" role="dialog" aria-modal="true" aria-label="Customer view preview" data-testid="preview-sheet">
        <button type="button" class="pro-btn pro-btn-ghost pro-btn-small mb-2" @click=${() => { this.preview = null }}>${icon(X, 14)} Close preview</button>
        <gable-customer-quote-page .quote=${this.preview} .hosted=${true}></gable-customer-quote-page>
      </div>`
  }

  render(): TemplateResult {
    return html`<section data-testid="quotes-screen">
      <p class="pro-page-kicker">Customer quotes</p>
      <h1 class="pro-page-title">Your price, your customer</h1>
      <p class="pro-page-subtitle">Build from dealer products at your cost. Your customer sees only the sell price you choose.</p>
      <div class="pro-row mt-4">
        <select class="pro-select max-w-md" aria-label="Open customer quote" .value=${this.quote?.id ?? ''} @change=${(event: Event) => { const id = (event.target as HTMLSelectElement).value; if (id !== '') navigate(`/quotes/${id}`) }}>
          ${this.quote === null ? html`<option value="" selected>New customer quote</option>` : nothing}
          ${this.summaries.map((summary) => html`<option value=${summary.id} ?selected=${summary.id === this.quote?.id}>${summary.reference} · ${summary.title}</option>`)}
        </select>
        <button type="button" class="pro-btn pro-btn-secondary pro-btn-small" data-testid="new-quote" ?disabled=${!can(this.me.capabilities, 'request_quotes')} @click=${() => navigate('/quotes')}>New</button>
        <span class="pro-row-grow"></span>
        <button type="button" class="pro-icon-btn" aria-label="Export JSON" data-testid="export-json" @click=${() => void this.exportQuotes('json')}>${icon(Download, 16)}<span class="pro-sr-only">JSON</span></button>
        <button type="button" class="pro-icon-btn" aria-label="Export CSV" data-testid="export-csv" @click=${() => void this.exportQuotes('csv')}>${icon(Download, 16)}<span class="pro-sr-only">CSV</span></button>
      </div>
      ${this.error !== '' ? html`<div class="pro-error mt-4" role="alert">${this.error}</div>` : nothing}
      ${this.message !== '' ? html`<div class="pro-success mt-4" role="status">${this.message}</div>` : nothing}
      ${this.offline ? html`<div class="pro-warning mt-4" data-testid="quote-offline">${OFFLINE_WRITE_REASON}</div>` : nothing}
      ${this.loading ? html`<div class="pro-empty mt-5" role="status"><p class="pro-card-title">Opening the quote desk...</p></div>` : html`<div class="pro-two-col mt-5">
        <div class="grid gap-3">
          <fieldset class="contents" ?disabled=${!this.canEdit || this.offline}>
            ${this.renderCustomer()}
            ${this.renderLines()}
            ${this.renderGroups()}
            ${this.renderCategoryRules()}
            ${this.renderLabourOverhead()}
            ${this.renderTerms()}
          </fieldset>
          <fieldset class="pro-card" ?disabled=${!this.canEdit || this.offline}>
            <div class="flex flex-col gap-3 sm:flex-row sm:items-center"><div class="pro-row-grow"><span class="pro-label">Whole quote markup</span>${this.markupEditor(this.input.markup, (markup) => this.patchInput({ markup }), 'Quote default')}</div>
              <button type="button" class="pro-btn pro-btn-primary w-full sm:w-auto" data-testid="save-quote" ?disabled=${!this.canEdit || !this.dirty || this.busy || this.offline} @click=${() => void this.save()}>${this.busy ? 'Saving...' : 'Save and price'}</button></div>
            ${this.dirty ? html`<p class="pro-text-small mt-2">Unsaved changes. Save to reprice and enable preview.</p>` : nothing}
          </fieldset>
        </div>
        <div>${this.renderSummary()}</div>
      </div>`}
      ${this.renderCatalogSheet()}
      ${this.renderPreviewSheet()}
    </section>`
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-pro-quote-builder': GableProQuoteBuilder
  }
}
