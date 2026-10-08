// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { router, type RouteMatch } from './lib/router.ts';
import { appKeyForPath, appManifests, tagForPath as appTagForPath } from './apps/registry.ts';
import { appsService } from './services/AppsService.ts';
import { SESSION_EXPIRED_EVENT } from './services/fetchClient.ts';

// Import layout shells (eagerly — they're small and always needed)
import './components/layout/app-shell.ts';
import './components/layout/portal-layout.ts';
import './components/layout/driver-layout.ts';
import './components/layout/yard-layout.ts';

// Import toast container (global)
import './components/ui/toast-container.ts';

// Import not-found page (fallback for unknown routes)
import './components/ui/not-found.ts';

// Disabled-app panel (rendered when a route's owning app is toggled off)
import './components/ui/app-disabled.ts';

// Session-expired panel (rendered when an ERP request comes back 401)
import './components/ui/session-expired.ts';

@customElement('gable-app')
export class GableApp extends LitElement {
  // Light DOM so Tailwind works
  createRenderRoot() { return this; }

  @state() private _match: RouteMatch | null = null;
  @state() private _loading = true;
  @state() private _sessionExpired = false;

  private _onAppsChanged = () => this.requestUpdate();
  private _onSessionExpired = () => { this._sessionExpired = true; };

  connectedCallback() {
    super.connectedCallback();
    router.addEventListener('route-changed', this._onRouteChanged);
    window.addEventListener(SESSION_EXPIRED_EVENT, this._onSessionExpired);
    // App enablement — fire-and-forget; gating fails open until it loads.
    appsService.addEventListener('apps-changed', this._onAppsChanged);
    void appsService.load().catch(() => {
      /* offline/pre-auth: backend gate still enforces */
    });
    // Initial route
    if (router.currentMatch) {
      this._onRouteChanged(
        new CustomEvent('route-changed', { detail: router.currentMatch })
      );
    }
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    router.removeEventListener('route-changed', this._onRouteChanged);
    window.removeEventListener(SESSION_EXPIRED_EVENT, this._onSessionExpired);
    appsService.removeEventListener('apps-changed', this._onAppsChanged);
  }

  private _onRouteChanged = async (e: Event) => {
    const match = (e as CustomEvent<RouteMatch | null>).detail;
    this._match = match;

    if (!match) {
      this._loading = false;
      return;
    }

    this._loading = true;

    try {
      // Lazy-load the page module (which self-registers as a custom element)
      await match.route.load();
      this._loading = false;
    } catch (err) {
      console.error('Failed to load route:', err);
      this._loading = false;
    }
  };

  /** Derive the custom element tag from the route's import path */
  private _getPageTag(): string {
    if (!this._match) return '';
    // Extract filename from the load function's toString or use a convention
    // The route's path determines the page tag
    const path = this._match.route.path;

    // Map routes to custom element tags via a naming convention
    // e.g. '/' → 'gable-dashboard', '/orders' → 'gable-order-list'
    return this._pathToTag(path);
  }

  private _pathToTag(path: string): string {
    // Converted apps declare path→tag in their manifests (web/apps/desk/src/apps/) —
    // one source of truth. The map below shrinks as modules convert.
    const appTag = appTagForPath(path);
    if (appTag) return appTag;

    const tagMap: Record<string, string> = {
      // The desk bundle is mounted at /app/: all paths here are what the
      // router sees (browser URL with /app/ prefix).
      '/app/': (import.meta.env.DEV || import.meta.env.VITE_DEMO_MODE === 'true') ? 'gable-local-test-hub' : 'gable-home',
      '/app/local-test': 'gable-local-test-hub',
      '/app/home': 'gable-home',
      '/app/dashboard': 'gable-dashboard',
      '/app/pos': 'gable-pos-terminal',
      '/app/inventory': 'gable-inventory',
      '/app/inventory/:id': 'gable-product-detail',
      '/app/quotes': 'gable-quote-list',
      '/app/quotes/new': 'gable-quote-builder',
      '/app/quotes/:id/edit': 'gable-quote-builder',
      '/app/quotes/analytics': 'gable-quote-analytics',
      '/app/quotes/:id': 'gable-quote-detail',
      '/app/orders': 'gable-order-list',
      '/app/orders/:id': 'gable-order-detail',
      '/app/invoices': 'gable-invoice-list',
      '/app/invoices/:id': 'gable-invoice-detail',
      '/app/reports/daily-till': 'gable-daily-till',
      '/app/reports/ar-aging': 'gable-ar-aging-report',
      '/app/reports/customer-statement': 'gable-customer-statement',
      '/app/reports/saved': 'gable-saved-reports',
      '/app/reports/builder': 'gable-report-builder',
      '/app/dispatch': 'gable-dispatch-board',
      '/app/fleet': 'gable-fleet-management',
      '/app/purchasing/vendors/:id': 'gable-vendor-detail',
      '/app/purchasing/vendors': 'gable-vendor-list',
      '/app/purchasing/new': 'gable-new-purchase-order',
      '/app/purchasing/recommendations': 'gable-purchasing-recommendations',
      '/app/purchasing/:id': 'gable-purchase-order-detail',
      '/app/purchasing': 'gable-purchase-order-list',
      '/app/admin': 'gable-tech-admin',
      '/app/admin/apps': 'gable-apps-page',
      '/app/admin/branches': 'gable-admin-branches',
      '/app/admin/branches/:id/users': 'gable-admin-branch-users',
      '/app/pricing': 'gable-pricing-matrix',
      '/app/accounts': 'gable-accounts-page',
      '/app/accounts/:id': 'gable-account-detail',
      '/app/accounting/chart-of-accounts': 'gable-chart-of-accounts',
      '/app/accounting/journal-entries': 'gable-journal-entries',
      '/app/accounting/trial-balance': 'gable-trial-balance',
      '/app/portal/login': 'gable-portal-login',
      '/app/portal': 'gable-portal-dashboard',
      '/app/portal/orders': 'gable-portal-orders',
      '/app/portal/invoices': 'gable-portal-invoices',
      '/app/portal/deliveries': 'gable-portal-deliveries',
      '/app/portal/catalog': 'gable-portal-catalog',
      '/app/portal/catalog/:id': 'gable-portal-product-detail',
      '/app/portal/cart': 'gable-portal-cart',
      '/app/portal/checkout': 'gable-portal-checkout',
      '/app/portal/account': 'gable-portal-my-account',
      '/app/portal/team': 'gable-portal-team',
      '/app/portal/team/invite': 'gable-portal-invite',
      '/app/portal/projects': 'gable-project-list',
      '/app/portal/projects/:id': 'gable-project-dashboard',
      '/app/driver': 'gable-driver-route-list',
      '/app/driver/routes/:id': 'gable-stop-list',
      '/app/driver/deliveries/:id': 'gable-delivery-detail',
      '/app/yard': 'gable-pick-queue',
      '/app/yard/pick/:id': 'gable-pick-detail',
      '/app/yard/inventory': 'gable-yard-inventory-lookup',
      '/app/yard/count': 'gable-cycle-count',
      '/app/yard/receiving': 'gable-receive-po',
      // Price exposure (lumber index protection).
      '/app/quotes/exposure': 'gable-quote-exposure',
      '/app/reports/exposure': 'gable-exposure-report',
      '/app/admin/market-indices': 'gable-market-indices',
      // Accounting surfaces.
      '/app/accounting/accounts-payable': 'gable-accounts-payable',
      '/app/accounting/balance-sheet': 'gable-balance-sheet',
      '/app/accounting/profit-and-loss': 'gable-profit-and-loss',
    };

    return tagMap[path] || 'gable-not-found';
  }

  render() {
    // The session-expired panel is an overlay over whatever surface is up, so
    // it wraps the shell selection rather than replacing it.
    return html`
      ${this._renderSurface()}
      ${this._sessionExpired
        ? html`<gable-session-expired></gable-session-expired>`
        : nothing}
    `;
  }

  private _renderSurface() {
    if (this._loading) {
      return html`
        <div class="flex h-screen w-full items-center justify-center bg-deep-space">
          <div class="flex flex-col items-center gap-3">
            <div class="h-8 w-8 animate-spin rounded-full border-2 border-gable-green border-t-transparent"></div>
            <span class="text-sm text-zinc-500 font-medium tracking-wide">Loading...</span>
          </div>
        </div>
      `;
    }

    if (!this._match) {
      return html`
        <div class="flex h-screen w-full items-center justify-center bg-deep-space text-white">
          <div class="text-center">
            <h1 class="text-4xl font-bold font-mono mb-2">404</h1>
            <p class="text-zinc-400">Page not found</p>
            <a href="/app/home" class="text-gable-green hover:underline mt-4 inline-block">Go to Dashboard</a>
          </div>
        </div>
      `;
    }

    const tag = this._getPageTag();
    const layout = this._match.route.layout;

    // App gate (UX only — the backend 404s a disabled app's API regardless):
    // routes owned by a disabled app render the disabled panel inside the
    // normal layout so the user can navigate to /admin/apps.
    const appKey = appKeyForPath(this._match.route.path);
    const disabledApp =
      appKey && !appsService.isEnabled(appKey)
        ? appManifests.find((a) => a.key === appKey)
        : undefined;

    // Create the page element dynamically with params as attributes
    const pageHtml = disabledApp
      ? html`<gable-app-disabled app-name=${disabledApp.name}></gable-app-disabled>`
      : this._renderPageTag(tag);

    switch (layout) {
      case 'erp':
        return html`<gable-app-shell .pageContent=${pageHtml}></gable-app-shell><gable-toast-container></gable-toast-container>`;
      case 'portal':
        return html`<gable-portal-layout .pageContent=${pageHtml}></gable-portal-layout><gable-toast-container></gable-toast-container>`;
      case 'driver':
        return html`<gable-driver-layout .pageContent=${pageHtml}></gable-driver-layout><gable-toast-container></gable-toast-container>`;
      case 'yard':
        return html`<gable-yard-layout .pageContent=${pageHtml}></gable-yard-layout><gable-toast-container></gable-toast-container>`;
      case 'none':
      default:
        return html`${pageHtml}<gable-toast-container></gable-toast-container>`;
    }
  }

  // Memoized page element: re-renders of gable-app (e.g. from 'apps-changed')
  // must not tear down and remount the current page — remounting re-runs
  // connectedCallback, which for data pages refires fetches.
  private _pageEl: HTMLElement | null = null;
  private _pageElKey = '';

  /** Render a custom element tag with route params as attributes */
  private _renderPageTag(tag: string) {
    const params = this._match?.params || {};
    const key = tag + JSON.stringify(params);
    if (key !== this._pageElKey || !this._pageEl) {
      const el = document.createElement(tag);
      // Pass route params as attributes
      for (const [k, value] of Object.entries(params)) {
        el.setAttribute(`route-${k}`, value);
      }
      this._pageEl = el;
      this._pageElKey = key;
    }
    return html`${this._pageEl}`;
  }
}
