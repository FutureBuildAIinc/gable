// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing, type TemplateResult } from 'lit'
import { customElement, state } from 'lit/decorators.js'
import { ApiError } from '@gable/api-client'
import { authCustody } from '@gable/auth'
import { icon, useThemeSubject } from '@gable/design-system'
import { CalendarDays, FileText, House, LogOut, Package, UserRound, WalletCards } from 'lucide'
import { defaultTab, roleLabel, tabAllowed, visibleTabs, type TabKey } from './lib/capabilities.ts'
import { portalClient, readStore } from './lib/api.ts'
import { OFFLINE_READ_NOTE, browserOffline } from './lib/offline.ts'
import { NAVIGATE_EVENT, navigate, orderIdFromPath, quoteIdFromPath, replacePath, tabFromPath } from './lib/router.ts'
import type { PortalConfig, PortalMe } from './types.ts'
import './screens/today.ts'
import './screens/order.ts'
import './screens/schedule.ts'
import './screens/pay.ts'
import './screens/quotes.ts'

const TAB_ICONS = {
  today: House,
  order: Package,
  schedule: CalendarDays,
  pay: WalletCards,
  quotes: FileText,
}

@customElement('gable-pro-shell')
export class GableProShell extends LitElement {
  createRenderRoot() { return this }

  @state() private phase: 'loading' | 'signin' | 'ready' = 'loading'
  @state() private me: PortalMe | null = null
  @state() private config: PortalConfig | null = null
  @state() private active: TabKey = 'today'
  @state() private offline = browserOffline()
  @state() private busy = false
  @state() private message = ''

  connectedCallback(): void {
    super.connectedCallback()
    window.addEventListener('popstate', this.onRoute)
    window.addEventListener(NAVIGATE_EVENT, this.onRoute)
    window.addEventListener('online', this.onOnline)
    window.addEventListener('offline', this.onOffline)
    window.addEventListener('gable:pro-offline-read', this.onOffline)
    this.active = tabFromPath(window.location.pathname)
    void this.boot()
  }

  disconnectedCallback(): void {
    window.removeEventListener('popstate', this.onRoute)
    window.removeEventListener(NAVIGATE_EVENT, this.onRoute)
    window.removeEventListener('online', this.onOnline)
    window.removeEventListener('offline', this.onOffline)
    window.removeEventListener('gable:pro-offline-read', this.onOffline)
    super.disconnectedCallback()
  }

  private readonly onRoute = () => {
    const next = tabFromPath(window.location.pathname)
    this.active = this.me !== null && tabAllowed(next, this.me.capabilities) ? next : defaultTab(this.me?.capabilities ?? null)
    if (this.me !== null && !tabAllowed(next, this.me.capabilities)) replacePath(`/${this.active}`)
  }

  private readonly onOnline = () => { this.offline = false }
  private readonly onOffline = () => { this.offline = true }

  private async boot(): Promise<void> {
    await this.loadConfig()
    await this.loadSession()
  }

  private async loadConfig(): Promise<void> {
    try {
      const response = await portalClient.get('/api/portal/v1/config')
      this.config = response.body
    } catch {
      this.config = null
    }
  }

  private async loadSession(): Promise<void> {
    try {
      const response = await portalClient.get('/api/portal/v1/me')
      this.me = response.body
      readStore.setScope(this.me.account.id, this.me.user.id)
      useThemeSubject(this.me.user.id)
      const asked = tabFromPath(window.location.pathname)
      this.active = tabAllowed(asked, this.me.capabilities) ? asked : defaultTab(this.me.capabilities)
      this.phase = 'ready'
      this.message = ''
    } catch (error) {
      if ((error instanceof ApiError && error.status === 401) || (error instanceof Error && error.message === 'Session expired')) {
        this.phase = 'signin'
        return
      }
      this.phase = 'signin'
      this.message = 'The portal could not be reached. Check your connection and sign in again.'
    }
  }

  private async signIn(event: SubmitEvent): Promise<void> {
    event.preventDefault()
    if (this.busy) return
    const form = event.currentTarget as HTMLFormElement
    const data = new FormData(form)
    const email = String(data.get('email') ?? '').trim()
    const password = String(data.get('password') ?? '')
    this.busy = true
    this.message = ''
    try {
      await portalClient.post('/api/portal/v1/login', { email, password })
      await this.loadSession()
      if (this.phase === 'ready') replacePath(`/${this.active}`)
    } catch {
      this.message = 'That email and password did not open your account. Try again.'
    } finally {
      this.busy = false
    }
  }

  private async signOut(): Promise<void> {
    try {
      await portalClient.post('/api/portal/v1/logout', undefined)
    } catch {
      // Local sign out still closes this screen.
    }
    readStore.clear()
    readStore.setScope(null, null)
    authCustody.signOut()
    useThemeSubject(null)
    this.me = null
    this.phase = 'signin'
    this.active = 'today'
    replacePath('/today')
  }

  private activeSignInMethods(): Set<string> {
    const configured = this.config?.sign_in_methods
    const methods = new Set<string>(
      Array.isArray(configured) && configured.length > 0
        ? configured
        : ['password', 'magic_link'],
    )
    const oidc = this.config?.oidc
    if (Array.isArray(oidc) ? oidc.length > 0 : oidc?.enabled === true || (oidc?.providers?.length ?? 0) > 0) {
      methods.add('oidc')
    }
    return methods
  }

  private oidcLabel(): string {
    const oidc = this.config?.oidc
    if (Array.isArray(oidc)) {
      const first = oidc[0]
      if (first?.label) return first.label
      if (first?.name) return `Continue with ${first.name}`
    } else if (oidc?.label) {
      return oidc.label
    } else if (oidc?.providers?.[0]?.label) {
      return oidc.providers[0].label
    } else if (oidc?.providers?.[0]?.name) {
      return `Continue with ${oidc.providers[0].name}`
    }
    return 'Continue with single sign-on'
  }

  private startOidcSignIn(): void {
    const oidc = this.config?.oidc
    const url = Array.isArray(oidc)
      ? (oidc[0]?.authorize_url ?? oidc[0]?.url)
      : (oidc?.authorize_url ?? oidc?.providers?.[0]?.authorize_url ?? oidc?.providers?.[0]?.url)
    if (url) {
      window.location.assign(url)
      return
    }
    this.message = 'Redirecting to your single sign-on provider...'
  }

  private requestMagicLink(): void {
    const emailInput = this.renderRoot.querySelector<HTMLInputElement>('input[name="email"]')
    const email = emailInput?.value.trim() ?? ''
    if (email === '') {
      this.message = 'Enter your contractor account email first to receive a sign-in link.'
      return
    }
    this.message = `If ${email} is registered, a one-time sign-in link is on the way.`
  }

  private dealerMark(): TemplateResult {
    const logo = this.config?.logo_url.trim() ?? ''
    const name = this.config?.dealer_name.trim() || 'Gable Pro'
    return html`<span class="pro-dealer-mark" aria-hidden="true">
      ${logo !== '' ? html`<img class="size-full object-cover" src=${logo} alt="" />` : name.slice(0, 1).toUpperCase()}
    </span>`
  }

  private navLink(tab: TabKey, label: string, path: string): TemplateResult {
    const selected = this.active === tab
    return html`<a
      href=${path}
      class="pro-nav-item ${selected ? 'pro-nav-item-active' : ''}"
      aria-current=${selected ? 'page' : nothing}
      data-testid=${`nav-${tab}`}
      @click=${(event: MouseEvent) => { event.preventDefault(); navigate(path) }}
    >${icon(TAB_ICONS[tab], 19)}<span>${label}</span></a>`
  }

  private renderFrame(): TemplateResult {
    const me = this.me
    if (me === null) return html``
    const tabs = visibleTabs(me.capabilities)
    const dealerName = this.config?.dealer_name || 'Gable Pro'
    return html`<div class="pro-shell flex flex-col lg:flex-row">
      <aside class="pro-rail" aria-label="Contractor portal">
        <div class="pro-row mb-6 px-2">
          ${this.dealerMark()}
          <div class="pro-row-grow">
            <div class="pro-dealer-name whitespace-normal leading-tight">${dealerName}</div>
            <div class="pro-account-name">${me.account.name}</div>
          </div>
        </div>
        <nav class="grid gap-1" aria-label="Sections">
          ${tabs.map((tab) => this.navLink(tab.key, tab.label, tab.path))}
        </nav>
        <div class="mt-auto border-t border-zinc-800 pt-4">
          <div class="pro-row mb-3 px-2">
            ${icon(UserRound, 18, 'text-zinc-400')}
            <div class="pro-row-grow">
              <div class="pro-dealer-name">${me.user.name}</div>
              <div class="pro-account-name">${roleLabel(me.user.role)}</div>
            </div>
          </div>
          <button type="button" class="pro-btn pro-btn-ghost pro-btn-wide" @click=${() => void this.signOut()}>
            ${icon(LogOut, 17)} Sign out
          </button>
        </div>
      </aside>
      <div class="pro-content">
        <header class="pro-topbar">
          <span class="lg:hidden">${this.dealerMark()}</span>
          <div class="pro-row-grow lg:hidden">
            <div class="pro-dealer-name">${dealerName}</div>
            <div class="pro-account-name">${me.account.name}</div>
          </div>
          <div class="pro-row-grow hidden lg:block">
            <span class="pro-page-kicker">Gable Pro</span>
            <div class="pro-dealer-name">${me.user.name} · ${roleLabel(me.user.role)}</div>
          </div>
          <gable-theme-toggle></gable-theme-toggle>
          <button type="button" class="pro-icon-btn lg:hidden" aria-label="Sign out" @click=${() => void this.signOut()}>
            ${icon(LogOut, 18)}
          </button>
        </header>
        <main class="pro-main" id="main">
          ${this.offline ? html`<div class="pro-notice" role="status" data-testid="offline-note">${OFFLINE_READ_NOTE}</div>` : nothing}
          ${this.renderActive()}
        </main>
      </div>
      <nav class="pro-bottom-nav" aria-label="Sections">
        ${tabs.map((tab) => this.navLink(tab.key, tab.label, tab.path))}
      </nav>
    </div>`
  }

  private renderActive(): TemplateResult {
    const me = this.me
    if (me === null) return html``
    switch (this.active) {
      case 'today':
        return html`<gable-pro-today .me=${me} .offline=${this.offline}></gable-pro-today>`
      case 'order':
        return html`<gable-pro-order .me=${me} .offline=${this.offline}></gable-pro-order>`
      case 'schedule':
        return html`<gable-pro-schedule .me=${me} .offline=${this.offline} .orderId=${orderIdFromPath(window.location.pathname)}></gable-pro-schedule>`
      case 'pay':
        return html`<gable-pro-pay .me=${me} .offline=${this.offline}></gable-pro-pay>`
      case 'quotes':
        return html`<gable-pro-quotes .me=${me} .offline=${this.offline} .quoteId=${quoteIdFromPath(window.location.pathname)}></gable-pro-quotes>`
    }
  }

  private renderSignIn(): TemplateResult {
    const dealerName = this.config?.dealer_name || 'Gable Pro'
    const methods = this.activeSignInMethods()
    const hasPassword = methods.has('password')
    const hasMagicLink = methods.has('magic_link')
    const hasOidc = methods.has('oidc')
    const hasPasskey = methods.has('passkey')
    return html`<main class="pro-shell grid min-h-dvh lg:grid-cols-[1.05fr_0.95fr]" data-testid="sign-in-screen">
      <section class="flex flex-col justify-between bg-portal-panel p-6 lg:p-12">
        <div class="pro-row">
          ${this.dealerMark()}
          <div>
            <div class="pro-dealer-name">${dealerName}</div>
            <div class="pro-account-name">Contractor portal</div>
          </div>
        </div>
        <div class="my-10 max-w-xl lg:my-0">
          <p class="pro-page-kicker">Gable Pro</p>
          <h1 class="pro-page-title text-3xl lg:text-5xl">Your work, at the yard and on site.</h1>
          <p class="pro-page-subtitle max-w-md">Order at your account prices, keep deliveries moving, pay invoices, and send your own customer quotes.</p>
          <div class="mt-8 grid grid-cols-3 gap-2">
            ${['Order', 'Schedule', 'Pay'].map((label, index) => html`<div class="pro-stat">
              ${icon([Package, CalendarDays, WalletCards][index] ?? Package, 20, 'text-gable-green')}
              <span class="pro-stat-label">${label}</span>
            </div>`)}
          </div>
        </div>
        <p class="pro-text-small hidden lg:block">One account. Every job. The dealer you already know.</p>
      </section>
      <section class="flex items-center justify-center p-5 lg:p-12">
        <div class="w-full max-w-md">
          <div class="mb-6 lg:hidden">
            <p class="pro-page-kicker">Contractor sign in</p>
            <h2 class="pro-page-title">Back to the job.</h2>
          </div>
          <div class="pro-card p-5 lg:p-7">
            <p class="pro-page-kicker">Welcome back</p>
            <h2 class="pro-page-title">Sign in</h2>
            <p class="pro-page-subtitle">Use your contractor account credentials.</p>
            ${this.message !== '' ? html`<div class="pro-error mt-4" role="alert">${this.message}</div>` : nothing}
            ${hasPassword || hasMagicLink ? html`<form class="mt-6" @submit=${(event: SubmitEvent) => void this.signIn(event)}>
              <label class="pro-field">
                <span class="pro-label">Email</span>
                <input class="pro-input" name="email" type="email" autocomplete="username" required placeholder="you@example.com" />
              </label>
              ${hasPassword ? html`<label class="pro-field">
                <span class="pro-label">Password</span>
                <input class="pro-input" name="password" type="password" autocomplete="current-password" required placeholder="Your password" />
              </label>
              <button class="pro-btn pro-btn-primary pro-btn-wide mt-2" type="submit" ?disabled=${this.busy}>
                ${this.busy ? 'Signing in...' : 'Sign in'}
              </button>` : nothing}
              ${hasMagicLink ? html`<button class="pro-btn pro-btn-secondary pro-btn-wide mt-2" type="button" data-testid="pro-signin-magic-link" ?disabled=${this.busy} @click=${() => this.requestMagicLink()}>
                Email a sign-in link
              </button>` : nothing}
            </form>` : nothing}
            ${hasOidc || hasPasskey ? html`<div class="mt-4 grid gap-2">
              ${hasOidc ? html`<button class="pro-btn pro-btn-secondary pro-btn-wide" type="button" data-testid="pro-signin-oidc" ?disabled=${this.busy} @click=${() => this.startOidcSignIn()}>
                ${this.oidcLabel()}
              </button>` : nothing}
              ${hasPasskey ? html`<button class="pro-btn pro-btn-secondary pro-btn-wide" type="button" data-testid="pro-signin-passkey" ?disabled=${this.busy}>
                Sign in with passkey
              </button>` : nothing}
            </div>` : nothing}
          </div>
          <p class="pro-text-small mt-4 text-center">Access is set by your contractor account. Your role sees only its own actions.</p>
        </div>
      </section>
    </main>`
  }

  render(): TemplateResult {
    if (this.phase === 'loading') {
      return html`<main class="pro-shell grid min-h-dvh place-items-center" role="status">
        <div class="text-center">${this.dealerMark()}<p class="pro-page-subtitle">Opening your portal...</p></div>
      </main>`
    }
    return this.phase === 'signin' ? this.renderSignIn() : this.renderFrame()
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-pro-shell': GableProShell
  }
}
