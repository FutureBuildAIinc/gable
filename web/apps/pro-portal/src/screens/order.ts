// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing, type TemplateResult } from 'lit'
import { customElement, property, state } from 'lit/decorators.js'
import { icon } from '@gable/design-system'
import { Check, ClipboardList, FileUp, Layers, PackagePlus, RefreshCw, Search, ShoppingCart, Sparkles, Trash2, X } from 'lucide'
import { can } from '../lib/capabilities.ts'
import { portalClient, portalRequest, readCached } from '../lib/api.ts'
import { formatCents, formatDollars, quantityWithUnit, shortDate } from '../lib/format.ts'
import { OFFLINE_WRITE_REASON } from '../lib/offline.ts'
import { navigate } from '../lib/router.ts'
import { statusChip } from '../lib/status.ts'
import type {
  PortalCart,
  PortalCatalogDetail,
  PortalCatalogProduct,
  PortalCategoryNode,
  PortalMe,
  PortalOrder,
  PortalProcurementProject,
  PortalQuote,
  PortalQuoteConvertResult,
  PortalQuoteIntakeItem,
  PortalQuoteIntakeResponse,
  PortalReorderPreview,
  Project,
  ProjectPage,
} from '../types.ts'

type OrderView = 'catalog' | 'reorder' | 'lists' | 'requests'

function initialOrderView(): OrderView {
  const params = new URLSearchParams(window.location.search)
  const view = params.get('view')
  if (view === 'reorder' || view === 'lists' || view === 'requests') return view
  return 'catalog'
}

function shortestLeadDays(product: PortalCatalogProduct): number | null {
  const yardLeads = (product.availability ?? [])
    .map((item) => item.lead_time_days)
    .filter((days): days is number => typeof days === 'number' && days >= 0)
  if (yardLeads.length > 0) return Math.min(...yardLeads)
  return product.lead_time_days
}

@customElement('gable-pro-order')
export class GableProOrder extends LitElement {
  createRenderRoot() { return this }

  @property({ attribute: false }) me!: PortalMe
  @property({ type: Boolean }) offline = false

  @state() private view: OrderView = 'catalog'
  @state() private loading = true
  @state() private busy = false
  @state() private error = ''
  @state() private message = ''

  @state() private query = ''
  @state() private categoryId = ''
  @state() private categories: PortalCategoryNode[] = []
  @state() private products: PortalCatalogProduct[] = []
  @state() private selectedProduct: PortalCatalogDetail | null = null
  @state() private productQty = 1

  @state() private cart: PortalCart | null = null
  @state() private projects: Project[] = []
  @state() private checkoutProjectId = ''
  @state() private checkoutDeliveryMethod: 'DELIVERY' | 'PICKUP' = 'DELIVERY'
  @state() private checkoutAddress = ''
  @state() private checkoutNotes = ''
  @state() private newProjectName = ''

  @state() private orders: PortalOrder[] = []
  @state() private reorderPreview: PortalReorderPreview | null = null
  @state() private reorderProjectId = ''

  @state() private lists: PortalProcurementProject[] = []
  @state() private newListName = ''
  @state() private activeListId = ''
  @state() private lineDescription = ''
  @state() private lineQty = '1'
  @state() private lineUom = 'EA'
  @state() private lineProductId = ''

  @state() private quotes: PortalQuote[] = []
  @state() private intakeText = ''
  @state() private intakeDelivery: 'DELIVERY' | 'PICKUP' = 'DELIVERY'
  @state() private intakeProjectId = ''
  @state() private intakeNotes = ''
  @state() private intakeParsed: PortalQuoteIntakeItem[] = []

  connectedCallback(): void {
    super.connectedCallback()
    this.view = initialOrderView()
    void this.loadAll()
  }

  private get canOrder(): boolean {
    return can(this.me?.capabilities ?? null, 'order')
  }

  private get canSeeMoney(): boolean {
    return can(this.me?.capabilities ?? null, 'see_money')
  }

  private get canRequestQuotes(): boolean {
    return can(this.me?.capabilities ?? null, 'request_quotes')
  }

  private get canApproveQuotes(): boolean {
    return can(this.me?.capabilities ?? null, 'approve_quotes')
  }

  private async loadAll(): Promise<void> {
    this.loading = true
    this.error = ''
    try {
      const [catRes, treeRes, cartRes, ordRes, quoteRes, projRes, listRes] = await Promise.all([
        readCached<PortalCatalogProduct[]>('catalog:all', '/api/portal/v1/catalog'),
        readCached<PortalCategoryNode[]>('catalog:categories', '/api/portal/v1/catalog/categories'),
        readCached<PortalCart>('cart', '/api/portal/v1/cart'),
        readCached<PortalOrder[]>('orders', '/api/portal/v1/orders', { feed: true }),
        readCached<PortalQuote[]>('dealer-quotes', '/api/portal/v1/quotes', { feed: true }),
        readCached<ProjectPage>('projects', '/api/portal/v1/projects').catch(() => ({ data: { items: [], next_cursor: null }, fromCache: false, offline: false })),
        readCached<PortalProcurementProject[]>('procurement-lists', '/api/portal/v1/procurement/projects').catch(() => ({ data: [], fromCache: false, offline: false })),
      ])
      this.products = catRes.data
      this.categories = treeRes.data
      this.cart = cartRes.data
      this.orders = ordRes.data
      this.quotes = quoteRes.data
      this.projects = projRes.data.items ?? []
      this.lists = listRes.data
      const firstList = this.lists[0]
      if (this.activeListId === '' && firstList !== undefined) {
        this.activeListId = firstList.id
      }
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Order desk data could not be loaded.'
    } finally {
      this.loading = false
    }
  }

  private async searchCatalog(): Promise<void> {
    this.error = ''
    try {
      const key = `catalog:${this.categoryId || 'all'}:${this.query.trim() || 'all'}`
      const res = await readCached<PortalCatalogProduct[]>(key, '/api/portal/v1/catalog', {
        query: {
          q: this.query.trim() || undefined,
          category_id: this.categoryId || undefined,
        },
      })
      this.products = res.data
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Catalog search failed.'
    }
  }

  private async openProduct(id: string): Promise<void> {
    this.error = ''
    try {
      const res = await readCached<PortalCatalogDetail>(`catalog-detail:${id}`, `/api/portal/v1/catalog/${id}`)
      this.selectedProduct = res.data
      this.productQty = 1
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Product details could not be loaded.'
    }
  }

  private async addToCart(productId: string, quantity = 1): Promise<void> {
    if (!this.canOrder || this.offline || this.busy) return
    this.busy = true
    this.error = ''
    this.message = ''
    try {
      const res = await portalClient.post(
        '/api/portal/v1/cart/items',
        { product_id: productId, quantity },
        { idempotencyKey: crypto.randomUUID() },
      )
      this.cart = res.body
      this.selectedProduct = null
      this.message = 'Added to your cart at account pricing.'
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Could not add item to cart.'
    } finally {
      this.busy = false
    }
  }

  private async updateCartItem(itemId: string, quantity: number): Promise<void> {
    if (!this.canOrder || this.offline || this.busy) return
    this.busy = true
    this.error = ''
    try {
      if (quantity <= 0) {
        const res = await portalClient.delete('/api/portal/v1/cart/items/{id}', { path: { id: itemId } })
        this.cart = res.body ?? this.cart
      } else {
        const res = await portalClient.put(
          '/api/portal/v1/cart/items/{id}',
          { quantity },
          { path: { id: itemId }, idempotencyKey: crypto.randomUUID() },
        )
        this.cart = res.body
      }
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Cart could not be updated.'
    } finally {
      this.busy = false
    }
  }

  private async createProject(): Promise<void> {
    const name = this.newProjectName.trim()
    if (name === '' || this.offline || this.busy) return
    this.busy = true
    this.error = ''
    try {
      const res = await portalClient.post(
        '/api/portal/v1/projects',
        { name },
        { idempotencyKey: crypto.randomUUID() },
      )
      this.projects = [res.body, ...this.projects]
      this.checkoutProjectId = res.body.id
      this.newProjectName = ''
      this.message = `Job "${res.body.name}" created.`
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Could not create job.'
    } finally {
      this.busy = false
    }
  }

  private async checkout(): Promise<void> {
    if (!this.canOrder || this.offline || this.busy || (this.cart?.items.length ?? 0) === 0) return
    this.busy = true
    this.error = ''
    this.message = ''
    try {
      const res = await portalClient.post(
        '/api/portal/v1/checkout',
        {
          delivery_method: this.checkoutDeliveryMethod,
          delivery_address: this.checkoutAddress.trim(),
          payment_method: 'ACCOUNT',
          notes: this.checkoutNotes.trim(),
          project_id: this.checkoutProjectId !== '' ? this.checkoutProjectId : null,
        },
        { idempotencyKey: crypto.randomUUID() },
      )
      this.message = `${res.body.message} Order ${res.body.order_id.slice(0, 8)} is on the board.`
      const [cartRes, ordRes] = await Promise.all([
        portalClient.get('/api/portal/v1/cart'),
        portalClient.get('/api/portal/v1/orders'),
      ])
      this.cart = cartRes.body
      this.orders = ordRes.body
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Checkout could not be completed.'
    } finally {
      this.busy = false
    }
  }

  private async previewReorder(order: PortalOrder): Promise<void> {
    this.error = ''
    this.message = ''
    try {
      const preview = await portalRequest<PortalReorderPreview>('GET', `/api/portal/v1/orders/${order.id}/reorder-preview`)
      this.reorderPreview = preview
      this.reorderProjectId = order.project_id ?? ''
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Reorder price check could not be loaded.'
    }
  }

  private async confirmReorder(orderId: string): Promise<void> {
    if (!this.canOrder || this.offline || this.busy) return
    this.busy = true
    this.error = ''
    this.message = ''
    try {
      const res = await portalRequest<{ order_id: string; new_order_id?: string; message: string }>(
        'POST',
        '/api/portal/v1/orders/reorder',
        {
          order_id: orderId,
          project_id: this.reorderProjectId !== '' ? this.reorderProjectId : undefined,
        },
        { idempotencyKey: crypto.randomUUID() },
      )
      this.reorderPreview = null
      this.message = res.message || 'Reorder placed at current account prices.'
      const ordRes = await portalClient.get('/api/portal/v1/orders')
      this.orders = ordRes.body
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Reorder could not be placed.'
    } finally {
      this.busy = false
    }
  }

  private async assignOrderProject(orderId: string, projectId: string): Promise<void> {
    if (!this.canOrder || this.offline || this.busy) return
    this.busy = true
    this.error = ''
    try {
      await portalClient.put(
        '/api/portal/v1/orders/{id}/project',
        { project_id: projectId === '' ? null : projectId },
        { path: { id: orderId }, idempotencyKey: crypto.randomUUID() },
      )
      const ordRes = await portalClient.get('/api/portal/v1/orders')
      this.orders = ordRes.body
      this.message = 'Order job tag updated.'
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Could not update order job tag.'
    } finally {
      this.busy = false
    }
  }

  private async createMaterialList(): Promise<void> {
    const name = this.newListName.trim()
    if (name === '' || !this.canOrder || this.offline || this.busy) return
    this.busy = true
    this.error = ''
    try {
      const created = await portalRequest<PortalProcurementProject>(
        'POST',
        '/api/portal/v1/procurement/projects',
        { name },
        { idempotencyKey: crypto.randomUUID() },
      )
      this.lists = [created, ...this.lists]
      this.activeListId = created.id
      this.newListName = ''
      this.message = `Material list "${created.name}" created.`
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Material list could not be created.'
    } finally {
      this.busy = false
    }
  }

  private async addListLine(): Promise<void> {
    const active = this.lists.find((item) => item.id === this.activeListId)
    if (active === undefined || !this.canOrder || this.offline || this.busy) return
    const qty = Number(this.lineQty)
    const product = this.products.find((item) => item.id === this.lineProductId)
    const description = this.lineDescription.trim() || product?.name || ''
    if (description === '' || !Number.isFinite(qty) || qty <= 0) return
    this.busy = true
    this.error = ''
    try {
      await portalRequest(
        'POST',
        `/api/portal/v1/procurement/projects/${active.id}/lines`,
        {
          product_id: product?.id ?? null,
          description,
          qty_entered: qty,
          uom: this.lineUom || product?.uom || 'EA',
          notes: '',
        },
        { idempotencyKey: crypto.randomUUID() },
      )
      const updated = await portalRequest<PortalProcurementProject>('GET', `/api/portal/v1/procurement/projects/${active.id}`)
      this.lists = this.lists.map((item) => (item.id === updated.id ? updated : item))
      this.lineDescription = ''
      this.lineQty = '1'
      this.lineProductId = ''
      this.message = 'Line added to material list.'
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Could not add line to material list.'
    } finally {
      this.busy = false
    }
  }

  private async removeListLine(projectId: string, lineId: string): Promise<void> {
    if (!this.canOrder || this.offline || this.busy) return
    this.busy = true
    this.error = ''
    try {
      await portalRequest('DELETE', `/api/portal/v1/procurement/projects/${projectId}/lines/${lineId}`)
      const updated = await portalRequest<PortalProcurementProject>('GET', `/api/portal/v1/procurement/projects/${projectId}`)
      this.lists = this.lists.map((item) => (item.id === updated.id ? updated : item))
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Line could not be removed.'
    } finally {
      this.busy = false
    }
  }

  private async submitListRfq(projectId: string): Promise<void> {
    if (!this.canRequestQuotes || this.offline || this.busy) return
    this.busy = true
    this.error = ''
    try {
      const updated = await portalRequest<PortalProcurementProject>(
        'POST',
        `/api/portal/v1/procurement/projects/${projectId}/submit-rfq`,
        {},
        { idempotencyKey: crypto.randomUUID() },
      )
      this.lists = this.lists.map((item) => (item.id === updated.id ? updated : item))
      const quotesRes = await portalClient.get('/api/portal/v1/quotes')
      this.quotes = quotesRes.body
      this.message = 'Material list sent to the dealer for pricing.'
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Could not submit list for pricing.'
    } finally {
      this.busy = false
    }
  }

  private async parseIntake(createQuote: boolean): Promise<void> {
    if (!this.canRequestQuotes || this.offline || this.busy || this.intakeText.trim() === '') return
    this.busy = true
    this.error = ''
    this.message = ''
    try {
      const res = await portalRequest<PortalQuoteIntakeResponse>(
        'POST',
        '/api/portal/v1/quotes/intake',
        {
          text: this.intakeText,
          project_id: this.intakeProjectId !== '' ? this.intakeProjectId : undefined,
          notes: this.intakeNotes.trim(),
          delivery_type: this.intakeDelivery,
          create_quote: createQuote,
        },
        { idempotencyKey: crypto.randomUUID() },
      )
      this.intakeParsed = res.items
      if (res.quote) {
        this.quotes = [res.quote, ...this.quotes.filter((item) => item.id !== res.quote?.id)]
        this.intakeText = ''
        this.message = 'Quote request sent to the dealer.'
      } else {
        this.message = `Parsed ${res.items.length} ${res.items.length === 1 ? 'line' : 'lines'} from your list.`
      }
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Could not parse quote list.'
    } finally {
      this.busy = false
    }
  }

  private async handleIntakeFile(event: Event): Promise<void> {
    const input = event.target as HTMLInputElement
    const file = input.files?.[0]
    if (!file || !this.canRequestQuotes || this.offline) return
    const reader = new FileReader()
    reader.onload = async () => {
      const dataUrl = typeof reader.result === 'string' ? reader.result : ''
      const base64 = dataUrl.includes(',') ? dataUrl.split(',')[1] ?? '' : dataUrl
      this.busy = true
      this.error = ''
      try {
        const res = await portalRequest<PortalQuoteIntakeResponse>(
          'POST',
          '/api/portal/v1/quotes/intake',
          {
            file_base64: base64,
            content_type: file.type || 'image/jpeg',
            delivery_type: this.intakeDelivery,
            allow_synthetic: true,
            create_quote: false,
          },
          { idempotencyKey: crypto.randomUUID() },
        )
        this.intakeParsed = res.items
        this.message = `Extracted ${res.items.length} ${res.items.length === 1 ? 'line' : 'lines'} from ${file.name}.`
      } catch (error) {
        this.error = error instanceof Error ? error.message : 'Photo intake could not be processed.'
      } finally {
        this.busy = false
      }
    }
    reader.readAsDataURL(file)
  }

  private async decideQuote(quoteId: string, action: 'accept' | 'decline'): Promise<void> {
    if (!this.canApproveQuotes || this.offline || this.busy) return
    this.busy = true
    this.error = ''
    try {
      const res = action === 'accept'
        ? await portalClient.post('/api/portal/v1/quotes/{id}/accept', undefined, { path: { id: quoteId }, idempotencyKey: crypto.randomUUID() })
        : await portalClient.post('/api/portal/v1/quotes/{id}/decline', undefined, { path: { id: quoteId }, idempotencyKey: crypto.randomUUID() })
      this.quotes = this.quotes.map((item) => (item.id === res.body.id ? res.body : item))
      this.message = action === 'accept' ? 'Dealer quote accepted.' : 'Dealer quote declined.'
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Quote decision could not be saved.'
    } finally {
      this.busy = false
    }
  }

  private async convertQuote(quoteId: string): Promise<void> {
    if (!this.canOrder || this.offline || this.busy) return
    this.busy = true
    this.error = ''
    try {
      const res = await portalRequest<PortalQuoteConvertResult>(
        'POST',
        `/api/portal/v1/quotes/${quoteId}/convert`,
        {},
        { idempotencyKey: crypto.randomUUID() },
      )
      this.message = `Converted to order ${res.order_number}.`
      const ordRes = await portalClient.get('/api/portal/v1/orders')
      this.orders = ordRes.body
    } catch (error) {
      this.error = error instanceof Error ? error.message : 'Quote could not be converted to an order.'
    } finally {
      this.busy = false
    }
  }

  private renderViewTabs(): TemplateResult {
    const tabs: Array<{ key: OrderView; label: string }> = [
      { key: 'catalog', label: 'Catalog and cart' },
      { key: 'reorder', label: `Reorder (${this.orders.length})` },
      { key: 'lists', label: `Material lists (${this.lists.length})` },
      { key: 'requests', label: `Dealer quotes (${this.quotes.length})` },
    ]
    return html`<div class="pro-tabs mt-4" role="tablist" aria-label="Order views">
      ${tabs.map((tab) => html`<button
        type="button"
        role="tab"
        aria-selected=${this.view === tab.key ? 'true' : 'false'}
        class="pro-tab ${this.view === tab.key ? 'pro-tab-active' : ''}"
        data-testid=${`order-tab-${tab.key}`}
        @click=${() => { this.view = tab.key }}
      >${tab.label}</button>`)}
    </div>`
  }

  private renderCatalogView(): TemplateResult {
    const cart = this.cart
    const showMoney = this.canSeeMoney
    return html`<div class="pro-two-col" data-testid="order-catalog-view">
      <div class="grid gap-3">
        <div class="pro-card">
          <div class="pro-form-grid">
            <label class="pro-field mb-0">
              <span class="pro-label">Search catalog</span>
              <div class="pro-row">
                <input
                  class="pro-input"
                  data-testid="catalog-search-input"
                  placeholder="SKU, dimension, species, grade..."
                  .value=${this.query}
                  @input=${(event: Event) => { this.query = (event.target as HTMLInputElement).value }}
                  @keydown=${(event: KeyboardEvent) => { if (event.key === 'Enter') void this.searchCatalog() }}
                />
                <button type="button" class="pro-icon-btn" aria-label="Search catalog" data-testid="catalog-search-btn" @click=${() => void this.searchCatalog()}>${icon(Search, 16)}</button>
              </div>
            </label>
            <label class="pro-field mb-0">
              <span class="pro-label">Category</span>
              <select
                class="pro-select"
                aria-label="Filter by category"
                data-testid="catalog-category-select"
                .value=${this.categoryId}
                @change=${(event: Event) => {
                  this.categoryId = (event.target as HTMLSelectElement).value
                  void this.searchCatalog()
                }}
              >
                <option value="">All categories</option>
                ${this.categories.map((cat) => html`<option value=${cat.id}>${cat.name} (${cat.product_count})</option>`)}
              </select>
            </label>
          </div>
        </div>

        <div class="pro-list" data-testid="catalog-product-list">
          ${this.products.length === 0
            ? html`<div class="pro-empty"><p class="pro-card-title">No catalog products match this filter.</p></div>`
            : this.products.map((product) => {
                const shortestLead = shortestLeadDays(product)
                const yards = product.availability ?? []
                return html`<article class="pro-card" data-testid=${`catalog-product-${product.id}`}>
                  <div class="pro-row">
                    <div class="pro-row-grow">
                      <p class="pro-page-kicker">${product.sku} · ${product.category}</p>
                      <h2 class="pro-card-title">${product.name}</h2>
                      <p class="pro-text-small">${[product.species, product.grade].filter(Boolean).join(' · ') || 'Standard yard spec'}</p>
                    </div>
                    <div class="text-right">
                      ${showMoney ? html`<div class="pro-money text-lg" data-testid=${`product-price-${product.id}`}>${formatDollars(product.customer_price)} <span class="pro-text-small">/${product.uom}</span></div>` : nothing}
                      <span class="pro-chip ${product.price_source === 'CONTRACT' ? 'pro-chip-green' : ''}">${product.price_source}</span>
                    </div>
                  </div>
                  <div class="mt-3 flex flex-wrap items-center gap-2">
                    <span class="pro-chip ${product.in_stock ? 'pro-chip-green' : 'pro-chip-amber'}">
                      ${product.in_stock ? `${quantityWithUnit(product.available, product.uom)} in stock` : 'Backorder'}
                    </span>
                    ${shortestLead !== null ? html`<span class="pro-chip pro-chip-blue" data-testid=${`product-lead-${product.id}`}>Shortest lead: ${shortestLead}d</span>` : nothing}
                    ${yards.map((yard) => html`<span class="pro-chip" data-testid=${`product-yard-${product.id}-${yard.yard_code}`}>
                      ${yard.yard_code}: ${yard.available} ${product.uom}${yard.lead_time_days !== null ? ` (${yard.lead_time_days}d)` : ''}
                    </span>`)}
                  </div>
                  <div class="pro-row mt-3">
                    <button type="button" class="pro-btn pro-btn-secondary pro-btn-small" data-testid=${`inspect-product-${product.id}`} @click=${() => void this.openProduct(product.id)}>
                      Volume breaks and specs
                    </button>
                    <span class="pro-row-grow"></span>
                    <button
                      type="button"
                      class="pro-btn pro-btn-primary pro-btn-small"
                      data-testid=${`cart-add-${product.id}`}
                      ?disabled=${!this.canOrder || this.offline || this.busy}
                      @click=${() => void this.addToCart(product.id, 1)}
                    >${icon(PackagePlus, 15)} Add to cart</button>
                  </div>
                </article>`
              })}
        </div>
      </div>

      <div class="grid gap-3">
        <section class="pro-card" data-testid="order-cart-card">
          <div class="pro-row">
            ${icon(ShoppingCart, 18, 'text-gable-green')}
            <div class="pro-row-grow">
              <h2 class="pro-card-title">Active cart (${cart?.item_count ?? 0})</h2>
              <p class="pro-text-small">${showMoney ? 'Priced on your contractor waterfall.' : 'Materials staged for jobsite fulfillment.'}</p>
            </div>
            ${showMoney ? html`<span class="pro-money text-lg" data-testid="cart-subtotal">${formatDollars(cart?.subtotal ?? 0)}</span>` : nothing}
          </div>

          ${(cart?.items.length ?? 0) === 0
            ? html`<p class="pro-text-small mt-3">Your cart is empty. Add items from the catalog or reorder a past job.</p>`
            : html`<div class="pro-line-list mt-3">
                ${(cart?.items ?? []).map((item) => html`<div class="pro-line" data-testid=${`cart-item-${item.id}`}>
                  <div>
                    <div class="pro-line-title">${item.product_name}</div>
                    <div class="pro-line-meta pro-data">${item.product_sku}${showMoney ? ` · ${formatDollars(item.unit_price)} each` : ` · Qty ${item.quantity}`}</div>
                    <div class="pro-row mt-2">
                      <label class="pro-sr-only" for=${`qty-${item.id}`}>Quantity</label>
                      <input
                        id=${`qty-${item.id}`}
                        class="pro-input pro-data w-24 min-h-9 py-1"
                        type="number"
                        min="1"
                        .value=${String(item.quantity)}
                        ?disabled=${!this.canOrder || this.offline || this.busy}
                        @change=${(event: Event) => void this.updateCartItem(item.id, Number((event.target as HTMLInputElement).value))}
                      />
                      <button
                        type="button"
                        class="pro-btn pro-btn-ghost pro-btn-small"
                        aria-label=${`Remove ${item.product_name}`}
                        ?disabled=${!this.canOrder || this.offline || this.busy}
                        @click=${() => void this.updateCartItem(item.id, 0)}
                      >${icon(Trash2, 14)} Remove</button>
                    </div>
                  </div>
                  ${showMoney ? html`<div class="pro-money">${formatDollars(item.line_total)}</div>` : nothing}
                </div>`)}
              </div>`}

          <fieldset class="mt-4 border-t border-zinc-800 pt-4" ?disabled=${!this.canOrder || this.offline || this.busy}>
            <label class="pro-field">
              <span class="pro-label">Job / project tag</span>
              <select
                class="pro-select"
                data-testid="checkout-project-select"
                .value=${this.checkoutProjectId}
                @change=${(event: Event) => { this.checkoutProjectId = (event.target as HTMLSelectElement).value }}
              >
                <option value="">No project tag</option>
                ${this.projects.map((proj) => html`<option value=${proj.id}>${proj.name}</option>`)}
              </select>
            </label>
            <div class="pro-row mb-3">
              <input
                class="pro-input"
                placeholder="New job name..."
                data-testid="new-project-input"
                .value=${this.newProjectName}
                @input=${(event: Event) => { this.newProjectName = (event.target as HTMLInputElement).value }}
              />
              <button type="button" class="pro-btn pro-btn-secondary pro-btn-small" data-testid="create-project-btn" @click=${() => void this.createProject()}>Add job</button>
            </div>
            <label class="pro-field">
              <span class="pro-label">Fulfillment</span>
              <select
                class="pro-select"
                data-testid="checkout-delivery-method"
                .value=${this.checkoutDeliveryMethod}
                @change=${(event: Event) => { this.checkoutDeliveryMethod = (event.target as HTMLSelectElement).value === 'PICKUP' ? 'PICKUP' : 'DELIVERY' }}
              >
                <option value="DELIVERY">Site delivery</option>
                <option value="PICKUP">Yard pickup</option>
              </select>
            </label>
            <label class="pro-field">
              <span class="pro-label">Site address or gate note</span>
              <input
                class="pro-input"
                placeholder="Jobsite address or instructions"
                .value=${this.checkoutAddress}
                @input=${(event: Event) => { this.checkoutAddress = (event.target as HTMLInputElement).value }}
              />
            </label>
            <button
              type="button"
              class="pro-btn pro-btn-primary pro-btn-wide mt-2"
              data-testid="checkout-submit-btn"
              ?disabled=${!this.canOrder || this.offline || this.busy || (cart?.items.length ?? 0) === 0}
              @click=${() => void this.checkout()}
            >Place order on account</button>
          </fieldset>
        </section>
      </div>
    </div>`
  }

  private renderReorderView(): TemplateResult {
    const preview = this.reorderPreview
    const showMoney = this.canSeeMoney
    return html`<div class="pro-two-col" data-testid="order-reorder-view">
      <div class="pro-list">
        ${this.orders.length === 0
          ? html`<div class="pro-empty"><p class="pro-card-title">No previous orders on this account yet.</p></div>`
          : this.orders.map((order) => html`<article class="pro-card" data-testid=${`order-row-${order.id}`}>
              <div class="pro-row">
                <div class="pro-row-grow">
                  <p class="pro-page-kicker pro-data">Order ${order.id.slice(0, 8)} · ${shortDate(order.created_at)}</p>
                  <h2 class="pro-card-title">${order.project_name || 'Unassigned job'}</h2>
                  <p class="pro-text-small">${order.lines.length} ${order.lines.length === 1 ? 'line' : 'lines'}</p>
                </div>
                <div class="text-right">
                  ${showMoney ? html`<div class="pro-money">${formatDollars(order.total_amount)}</div>` : nothing}
                  <div class="mt-1">${statusChip(order.status)}</div>
                </div>
              </div>
              <div class="pro-line-list mt-3">
                ${order.lines.slice(0, 3).map((line) => html`<div class="pro-line">
                  <span class="pro-text-small">${line.quantity} x ${line.product_name} (${line.product_sku})</span>
                  ${showMoney ? html`<span class="pro-money text-xs">${formatDollars(line.price_each)}</span>` : nothing}
                </div>`)}
              </div>
              <div class="pro-row mt-3 flex-wrap">
                <label class="pro-row-grow">
                  <span class="pro-sr-only">Assign project</span>
                  <select
                    class="pro-select min-h-9 py-1 text-xs"
                    aria-label=${`Job tag for order ${order.id.slice(0, 8)}`}
                    data-testid=${`order-project-select-${order.id}`}
                    .value=${order.project_id ?? ''}
                    ?disabled=${!this.canOrder || this.offline || this.busy}
                    @change=${(event: Event) => void this.assignOrderProject(order.id, (event.target as HTMLSelectElement).value)}
                  >
                    <option value="">No job tag</option>
                    ${this.projects.map((proj) => html`<option value=${proj.id} ?selected=${proj.id === order.project_id}>${proj.name}</option>`)}
                  </select>
                </label>
                <button
                  type="button"
                  class="pro-btn pro-btn-secondary pro-btn-small"
                  data-testid=${`reorder-preview-btn-${order.id}`}
                  @click=${() => void this.previewReorder(order)}
                >${icon(RefreshCw, 14)} Check reorder prices</button>
                <button
                  type="button"
                  class="pro-btn pro-btn-ghost pro-btn-small"
                  @click=${() => navigate(`/schedule/orders/${order.id}`)}
                >Track stop</button>
              </div>
            </article>`)}
      </div>

      <div>
        ${preview === null
          ? html`<div class="pro-card" data-testid="reorder-preview-empty">
              <h2 class="pro-card-title">Reorder with live price check</h2>
              <p class="pro-text-small mt-1">Select any past order to compare your original unit prices against today's contract and tier waterfall before placing the new order.</p>
            </div>`
          : html`<section class="pro-card" data-testid="reorder-preview-card">
              <div class="pro-row">
                <div class="pro-row-grow">
                  <p class="pro-page-kicker">Price difference preview</p>
                  <h2 class="pro-card-title">Order ${preview.source_order_id.slice(0, 8)}</h2>
                </div>
                <button type="button" class="pro-icon-btn" aria-label="Close reorder preview" @click=${() => { this.reorderPreview = null }}>${icon(X, 16)}</button>
              </div>
              ${showMoney ? html`<div class="pro-summary-grid">
                <div class="pro-stat">
                  <span class="pro-stat-value" data-testid="reorder-original-total">${formatCents(preview.original_total_cents)}</span>
                  <span class="pro-stat-label">Original order total</span>
                </div>
                <div class="pro-stat">
                  <span class="pro-stat-value" data-testid="reorder-current-total">${formatCents(preview.current_total_cents)}</span>
                  <span class="pro-stat-label">Current account total (${preview.total_diff_cents >= 0 ? `+${formatCents(preview.total_diff_cents)}` : formatCents(preview.total_diff_cents)})</span>
                </div>
              </div>` : nothing}
              <div class="pro-line-list mt-3">
                ${preview.lines.map((line) => html`<div class="pro-line" data-testid=${`reorder-line-${line.line_id}`}>
                  <div>
                    <div class="pro-line-title">${line.description}</div>
                    <div class="pro-line-meta pro-data">${line.sku} · ${quantityWithUnit(line.quantity, line.uom)}${showMoney ? ` · Was ${formatCents(line.original_unit_price_cents)} -> Now ${formatCents(line.current_unit_price_cents)}` : ''}</div>
                  </div>
                  ${showMoney ? html`<div class="text-right">
                    <span class="pro-money block">${formatCents(line.current_line_total_cents)}</span>
                    <span class="pro-chip ${line.line_diff_cents === 0 ? 'pro-chip-green' : 'pro-chip-amber'}">
                      ${line.line_diff_cents === 0 ? 'No change' : line.line_diff_cents > 0 ? `+${formatCents(line.line_diff_cents)}` : formatCents(line.line_diff_cents)}
                    </span>
                  </div>` : nothing}
                </div>`)}
              </div>
              <button
                type="button"
                class="pro-btn pro-btn-primary pro-btn-wide mt-4"
                data-testid="confirm-reorder-btn"
                ?disabled=${!this.canOrder || this.offline || this.busy}
                @click=${() => void this.confirmReorder(preview.source_order_id)}
              >${showMoney ? `Place reorder at ${formatCents(preview.current_total_cents)}` : 'Place reorder'}</button>
            </section>`}
      </div>
    </div>`
  }

  private renderListsView(): TemplateResult {
    const active = this.lists.find((item) => item.id === this.activeListId) ?? this.lists[0] ?? null
    const showMoney = this.canSeeMoney
    return html`<div class="pro-two-col" data-testid="order-lists-view">
      <div class="grid gap-3">
        <section class="pro-card">
          <h2 class="pro-card-title">Named material lists</h2>
          <p class="pro-text-small mt-1">Keep repeatable assemblies and job packages ready to price or send as an RFQ.</p>
          <div class="pro-row mt-3">
            <input
              class="pro-input"
              data-testid="new-list-name"
              placeholder="e.g. Standard Cedar Deck Package"
              .value=${this.newListName}
              ?disabled=${!this.canOrder || this.offline || this.busy}
              @input=${(event: Event) => { this.newListName = (event.target as HTMLInputElement).value }}
            />
            <button
              type="button"
              class="pro-btn pro-btn-primary pro-btn-small"
              data-testid="create-list-btn"
              ?disabled=${!this.canOrder || this.offline || this.busy || this.newListName.trim() === ''}
              @click=${() => void this.createMaterialList()}
            >${icon(Layers, 15)} Create list</button>
          </div>
          <div class="pro-line-list mt-3">
            ${this.lists.length === 0
              ? html`<p class="pro-text-small py-2">No saved material lists yet.</p>`
              : this.lists.map((list) => html`<button
                  type="button"
                  class="pro-line w-full text-left ${active?.id === list.id ? 'text-gable-green' : ''}"
                  data-testid=${`select-list-${list.id}`}
                  @click=${() => { this.activeListId = list.id }}
                >
                  <div>
                    <div class="pro-line-title">${list.name}</div>
                    <div class="pro-line-meta">${list.lines.length} ${list.lines.length === 1 ? 'line' : 'lines'} · Updated ${shortDate(list.updated_at)}</div>
                  </div>
                  ${statusChip(list.status)}
                </button>`)}
          </div>
        </section>
      </div>

      <div>
        ${active === null
          ? html`<div class="pro-empty"><p class="pro-card-title">Create or select a material list to manage its lines.</p></div>`
          : html`<section class="pro-card" data-testid="active-material-list">
              <div class="pro-row">
                <div class="pro-row-grow">
                  <p class="pro-page-kicker">Material list</p>
                  <h2 class="pro-card-title">${active.name}</h2>
                </div>
                ${statusChip(active.status)}
              </div>
              <div class="pro-line-list mt-3">
                ${active.lines.length === 0
                  ? html`<p class="pro-text-small py-2">No lines on this list yet.</p>`
                  : active.lines.map((line) => html`<div class="pro-line" data-testid=${`list-line-${line.id}`}>
                      <div>
                        <div class="pro-line-title">${line.description}</div>
                        <div class="pro-line-meta pro-data">${quantityWithUnit(line.qty_entered, line.uom)}${showMoney && line.unit_price_cents !== undefined ? ` · ${formatCents(line.unit_price_cents)}/${line.uom}` : ''}</div>
                      </div>
                      <div class="pro-row">
                        ${showMoney && line.line_total_cents !== undefined ? html`<span class="pro-money">${formatCents(line.line_total_cents)}</span>` : nothing}
                        <button
                          type="button"
                          class="pro-icon-btn size-9"
                          aria-label=${`Remove ${line.description}`}
                          ?disabled=${!this.canOrder || this.offline || this.busy}
                          @click=${() => void this.removeListLine(active.id, line.id)}
                        >${icon(Trash2, 14)}</button>
                      </div>
                    </div>`)}
              </div>
              <fieldset class="mt-4 border-t border-zinc-800 pt-4" ?disabled=${!this.canOrder || this.offline || this.busy}>
                <div class="pro-form-grid">
                  <label class="pro-field">
                    <span class="pro-label">Catalog product (optional)</span>
                    <select
                      class="pro-select"
                      data-testid="list-line-product"
                      .value=${this.lineProductId}
                      @change=${(event: Event) => {
                        this.lineProductId = (event.target as HTMLSelectElement).value
                        const found = this.products.find((item) => item.id === this.lineProductId)
                        if (found) {
                          this.lineDescription = found.name
                          this.lineUom = found.uom
                        }
                      }}
                    >
                      <option value="">Custom or special order line</option>
                      ${this.products.map((prod) => html`<option value=${prod.id}>${prod.sku} · ${prod.name}</option>`)}
                    </select>
                  </label>
                  <label class="pro-field">
                    <span class="pro-label">Description</span>
                    <input
                      class="pro-input"
                      data-testid="list-line-description"
                      placeholder="Material description"
                      .value=${this.lineDescription}
                      @input=${(event: Event) => { this.lineDescription = (event.target as HTMLInputElement).value }}
                    />
                  </label>
                  <label class="pro-field">
                    <span class="pro-label">Quantity</span>
                    <input
                      class="pro-input pro-data"
                      data-testid="list-line-qty"
                      type="number"
                      min="1"
                      .value=${this.lineQty}
                      @input=${(event: Event) => { this.lineQty = (event.target as HTMLInputElement).value }}
                    />
                  </label>
                  <label class="pro-field">
                    <span class="pro-label">Unit</span>
                    <input
                      class="pro-input pro-data"
                      data-testid="list-line-uom"
                      .value=${this.lineUom}
                      @input=${(event: Event) => { this.lineUom = (event.target as HTMLInputElement).value }}
                    />
                  </label>
                </div>
                <div class="pro-row flex-wrap">
                  <button type="button" class="pro-btn pro-btn-secondary pro-btn-small" data-testid="add-list-line-btn" @click=${() => void this.addListLine()}>Add line</button>
                  <span class="pro-row-grow"></span>
                  <button
                    type="button"
                    class="pro-btn pro-btn-primary pro-btn-small"
                    data-testid="submit-list-rfq-btn"
                    ?disabled=${!this.canRequestQuotes || active.lines.length === 0 || active.status === 'SUBMITTED'}
                    @click=${() => void this.submitListRfq(active.id)}
                  >${icon(ClipboardList, 14)} Send list for dealer quote</button>
                </div>
              </fieldset>
            </section>`}
      </div>
    </div>`
  }

  private renderRequestsView(): TemplateResult {
    const showMoney = this.canSeeMoney
    return html`<div class="pro-two-col" data-testid="order-requests-view">
      <div class="grid gap-3">
        <section class="pro-card" data-testid="quote-intake-card">
          <div class="pro-row">
            ${icon(Sparkles, 18, 'text-gable-green')}
            <div class="pro-row-grow">
              <h2 class="pro-card-title">Request a dealer quote from a list or photo</h2>
              <p class="pro-text-small">Paste your takeoff lines or upload a jobsite photo. We match catalog SKUs and flag special orders.</p>
            </div>
          </div>
          <fieldset class="mt-3" ?disabled=${!this.canRequestQuotes || this.offline || this.busy}>
            <label class="pro-field">
              <span class="pro-label">Paste material list (one item per line)</span>
              <textarea
                class="pro-textarea pro-data"
                data-testid="intake-textarea"
                placeholder="24 pcs 2x10x16 #2 Fir&#10;12 sheets 3/4 CDX Plywood&#10;6 bags rapid set concrete"
                .value=${this.intakeText}
                @input=${(event: Event) => { this.intakeText = (event.target as HTMLTextAreaElement).value }}
              ></textarea>
            </label>
            <div class="pro-form-grid">
              <label class="pro-field">
                <span class="pro-label">Job / project</span>
                <select
                  class="pro-select"
                  .value=${this.intakeProjectId}
                  @change=${(event: Event) => { this.intakeProjectId = (event.target as HTMLSelectElement).value }}
                >
                  <option value="">No project tag</option>
                  ${this.projects.map((proj) => html`<option value=${proj.id}>${proj.name}</option>`)}
                </select>
              </label>
              <label class="pro-field">
                <span class="pro-label">Fulfillment</span>
                <select
                  class="pro-select"
                  .value=${this.intakeDelivery}
                  @change=${(event: Event) => { this.intakeDelivery = (event.target as HTMLSelectElement).value === 'PICKUP' ? 'PICKUP' : 'DELIVERY' }}
                >
                  <option value="DELIVERY">Site delivery</option>
                  <option value="PICKUP">Yard pickup</option>
                </select>
              </label>
            </div>
            <div class="pro-row flex-wrap">
              <label class="pro-btn pro-btn-secondary pro-btn-small cursor-pointer">
                ${icon(FileUp, 14)} Upload list photo
                <input type="file" accept="image/*" class="pro-sr-only" data-testid="intake-file-input" @change=${(event: Event) => void this.handleIntakeFile(event)} />
              </label>
              <button
                type="button"
                class="pro-btn pro-btn-secondary pro-btn-small"
                data-testid="intake-parse-btn"
                ?disabled=${this.intakeText.trim() === ''}
                @click=${() => void this.parseIntake(false)}
              >Preview match</button>
              <span class="pro-row-grow"></span>
              <button
                type="button"
                class="pro-btn pro-btn-primary pro-btn-small"
                data-testid="intake-submit-btn"
                ?disabled=${this.intakeText.trim() === ''}
                @click=${() => void this.parseIntake(true)}
              >Send for dealer pricing</button>
            </div>
          </fieldset>

          ${this.intakeParsed.length > 0
            ? html`<div class="pro-line-list mt-4 border-t border-zinc-800 pt-3" data-testid="intake-parsed-list">
                ${this.intakeParsed.map((item) => html`<div class="pro-line">
                  <div>
                    <div class="pro-line-title">${item.matched_product_name || item.parsed_description}</div>
                    <div class="pro-line-meta pro-data">${quantityWithUnit(item.parsed_qty, item.parsed_unit)} · ${item.matched_sku ? `Matched ${item.matched_sku}` : 'Special order line'}</div>
                  </div>
                  <span class="pro-chip ${item.confidence === 'HIGH' ? 'pro-chip-green' : 'pro-chip-amber'}">${item.confidence}</span>
                </div>`)}
              </div>`
            : nothing}
        </section>
      </div>

      <div class="pro-list" data-testid="dealer-quotes-list">
        ${this.quotes.length === 0
          ? html`<div class="pro-empty"><p class="pro-card-title">No dealer quotes on your board yet.</p></div>`
          : this.quotes.map((quote) => html`<article class="pro-card" data-testid=${`dealer-quote-${quote.id}`}>
              <div class="pro-row">
                <div class="pro-row-grow">
                  <p class="pro-page-kicker pro-data">Quote ${quote.id.slice(0, 8)} · ${quote.delivery_type}</p>
                  <h2 class="pro-card-title">${quote.project_name || 'General account quote'}</h2>
                  <p class="pro-text-small">${quote.notes || 'Requested from dealer desk'}</p>
                </div>
                <div class="text-right">
                  ${showMoney ? html`<div class="pro-money">${quote.priced ? formatDollars(quote.total_amount) : 'Awaiting price'}</div>` : nothing}
                  <div class="mt-1">${statusChip(quote.status)}</div>
                </div>
              </div>
              <div class="pro-line-list mt-3">
                ${quote.lines.map((line) => html`<div class="pro-line">
                  <span class="pro-text-small">${quantityWithUnit(line.quantity, line.uom)} · ${line.description} (${line.product_sku})</span>
                  ${showMoney ? html`<span class="pro-money text-xs">${quote.priced ? formatDollars(line.line_total) : 'Pending'}</span>` : nothing}
                </div>`)}
              </div>
              <div class="pro-row mt-3 flex-wrap">
                ${quote.status === 'PRICED'
                  ? html`
                      <button
                        type="button"
                        class="pro-btn pro-btn-primary pro-btn-small"
                        data-testid=${`accept-dealer-quote-${quote.id}`}
                        ?disabled=${!this.canApproveQuotes || this.offline || this.busy}
                        @click=${() => void this.decideQuote(quote.id, 'accept')}
                      >${icon(Check, 14)} Accept quote</button>
                      <button
                        type="button"
                        class="pro-btn pro-btn-secondary pro-btn-small"
                        data-testid=${`decline-dealer-quote-${quote.id}`}
                        ?disabled=${!this.canApproveQuotes || this.offline || this.busy}
                        @click=${() => void this.decideQuote(quote.id, 'decline')}
                      >Decline</button>
                    `
                  : nothing}
                ${quote.status === 'ACCEPTED'
                  ? html`<button
                      type="button"
                      class="pro-btn pro-btn-primary pro-btn-small"
                      data-testid=${`convert-dealer-quote-${quote.id}`}
                      ?disabled=${!this.canOrder || this.offline || this.busy}
                      @click=${() => void this.convertQuote(quote.id)}
                    >Convert to order</button>`
                  : nothing}
              </div>
            </article>`)}
      </div>
    </div>`
  }

  private renderProductSheet(): TemplateResult | typeof nothing {
    const detail = this.selectedProduct
    if (detail === null) return nothing
    const showMoney = this.canSeeMoney
    return html`<div class="pro-sheet-backdrop" @click=${() => { this.selectedProduct = null }}></div>
      <div class="pro-sheet" role="dialog" aria-modal="true" aria-label=${detail.name} data-testid="product-detail-sheet">
        <div class="pro-row">
          <div class="pro-row-grow">
            <p class="pro-page-kicker">${detail.sku} · ${detail.category}</p>
            <h2 class="pro-card-title text-lg">${detail.name}</h2>
          </div>
          <button type="button" class="pro-icon-btn" aria-label="Close product sheet" @click=${() => { this.selectedProduct = null }}>${icon(X, 16)}</button>
        </div>
        <p class="pro-text-small mt-2">
          ${[detail.species, detail.grade, detail.vendor ? `Vendor: ${detail.vendor}` : '', detail.weight_lbs ? `${detail.weight_lbs} lbs` : ''].filter(Boolean).join(' · ')}
        </p>
        ${showMoney ? html`<div class="pro-summary-grid">
          <div class="pro-stat">
            <span class="pro-stat-value">${formatDollars(detail.customer_price)} /${detail.uom}</span>
            <span class="pro-stat-label">Your price (${detail.price_source}) · Base ${formatDollars(detail.base_price)}</span>
          </div>
        </div>` : nothing}
        ${showMoney && detail.volume_breaks.length > 0
          ? html`<div class="mt-4">
              <h3 class="pro-label">Volume breaks</h3>
              <div class="pro-line-list" data-testid="product-volume-breaks">
                ${detail.volume_breaks.map((rung) => html`<div class="pro-line">
                  <span class="pro-text-small">Buy ${rung.min_quantity}+ ${detail.uom} (${rung.details})</span>
                  <span class="pro-money">${formatDollars(rung.unit_price)} /${detail.uom} (save ${formatDollars(rung.saves_per_unit)})</span>
                </div>`)}
              </div>
            </div>`
          : nothing}
        <div class="pro-row mt-4">
          <label class="pro-field mb-0">
            <span class="pro-label">Quantity (${detail.uom})</span>
            <input
              class="pro-input pro-data w-28"
              type="number"
              min="1"
              .value=${String(this.productQty)}
              @input=${(event: Event) => { this.productQty = Math.max(1, Number((event.target as HTMLInputElement).value) || 1) }}
            />
          </label>
          <span class="pro-row-grow"></span>
          <button
            type="button"
            class="pro-btn pro-btn-primary"
            data-testid="sheet-add-to-cart"
            ?disabled=${!this.canOrder || this.offline || this.busy}
            @click=${() => void this.addToCart(detail.id, this.productQty)}
          >${icon(PackagePlus, 16)} Add ${this.productQty} to cart</button>
        </div>
      </div>`
  }

  render(): TemplateResult {
    return html`<section data-testid="order-screen">
      <p class="pro-page-kicker">Order · ${this.me.account.name}</p>
      <h1 class="pro-page-title">Catalog, reorder, and dealer quotes</h1>
      <p class="pro-page-subtitle">Your account waterfall prices, per-yard stock and shortest lead times, live reorder price checks, and pasted list intake.</p>
      ${this.offline ? html`<div class="pro-warning mt-4" data-testid="order-offline-banner">${OFFLINE_WRITE_REASON}</div>` : nothing}
      ${this.error !== '' ? html`<div class="pro-error mt-4" role="alert">${this.error}</div>` : nothing}
      ${this.message !== '' ? html`<div class="pro-success mt-4" role="status">${this.message}</div>` : nothing}
      ${this.renderViewTabs()}
      ${this.loading
        ? html`<div class="pro-empty mt-5" role="status"><p class="pro-card-title">Loading your catalog and order desk...</p></div>`
        : this.view === 'catalog'
          ? this.renderCatalogView()
          : this.view === 'reorder'
            ? this.renderReorderView()
            : this.view === 'lists'
              ? this.renderListsView()
              : this.renderRequestsView()}
      ${this.renderProductSheet()}
    </section>`
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-pro-order': GableProOrder
  }
}
