// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property } from 'lit/decorators.js';
import { framedByShell } from '@gable/design-system';
import { cn } from '../../lib/utils.ts';
import { router } from '../../lib/router.ts';
import { icon } from '../../lib/icons.ts';
import { ClipboardList, Package, ScanBarcode } from 'lucide';

const navItems = [
  { iconData: ClipboardList, label: 'Pick', path: '/yard' },
  { iconData: Package, label: 'Inventory', path: '/yard/inventory' },
  { iconData: ScanBarcode, label: 'Receiving', path: '/yard/receiving' },
];

@customElement('gable-yard-layout')
export class GableYardLayout extends LitElement {
  createRenderRoot() { return this; }

  @property({ attribute: false }) pageContent: unknown = nothing;

  private _boundRouteChanged = () => { this.requestUpdate(); };

  connectedCallback() {
    super.connectedCallback();
    router.addEventListener('route-changed', this._boundRouteChanged);
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    router.removeEventListener('route-changed', this._boundRouteChanged);
  }

  render() {
    const path = router.currentPath;
    const framed = framedByShell();
    const nav = html`
      <nav class="${framed
        ? 'sticky top-0 md:max-w-md md:mx-auto h-12 bg-slate-steel/80 backdrop-blur-md border-b border-white/10 flex items-center justify-around z-40'
        : 'fixed bottom-0 left-0 right-0 md:max-w-md md:mx-auto h-16 bg-slate-steel/95 backdrop-blur-md border-t border-white/10 flex items-center justify-around z-50'}">
        ${navItems.map(item => {
          const isActive = item.path === '/yard' ? path === '/yard' : path.startsWith(item.path);
          return html`
            <a href="${item.path}" class="${cn(
              framed
                ? 'flex items-center gap-1.5 px-3 min-h-[40px] rounded-lg transition-colors'
                : 'flex flex-col items-center gap-1 px-4 py-2 rounded-lg transition-colors',
              isActive ? 'text-amber-400' : 'text-zinc-500 hover:text-zinc-300'
            )}">
              ${icon(item.iconData, framed ? 16 : 20)}
              <span class="text-[10px] font-mono uppercase tracking-wider">${item.label}</span>
            </a>
          `;
        })}
      </nav>
    `;

    return html`
      <div class="min-h-screen bg-deep-space text-white font-sans md:max-w-md md:mx-auto md:border-x md:border-white/10 relative shadow-2xl flex flex-col">
        ${framed
          ? nav
          : html`
        <header class="h-16 flex items-center justify-between px-4 border-b border-white/10 bg-slate-steel/80 backdrop-blur-md sticky top-0 z-50">
          <div class="font-bold text-lg tracking-wider font-mono">
            GABLE<span class="text-amber-400">YARD</span>
          </div>
          <div class="h-8 w-8 rounded-full bg-amber-400/10 border border-amber-400/20 flex items-center justify-center text-xs font-mono text-amber-400">
            Y1
          </div>
        </header>
        `}

        <main class="flex-1 ${framed ? 'pb-6' : 'pb-20'} overflow-y-auto">
          ${this.pageContent}
        </main>

        ${framed ? nothing : nav}
      </div>
    `;
  }
}
