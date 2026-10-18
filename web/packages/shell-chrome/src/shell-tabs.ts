// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The phone shell's bottom tab bar: the field apps the person's roles hold
// (Yard and Driver), with a touch target of at least 44 px on every tab.
// Purely presentational: the host hands the bar the role's catalog slice and
// the current app id, and the bar emits
//
//   gable-shell-open-app   { appId, path }   a tab was picked
//
// Tabs share one keyboard: ArrowLeft and ArrowUp move left, ArrowRight and
// ArrowDown move right, Home and End jump.

import { LitElement, html } from 'lit';
import { customElement, property } from 'lit/decorators.js';
import { appIcon, icon } from '@gable/design-system';
import type { ShellApp } from './shell-state.ts';

@customElement('gable-shell-tabs')
export class GableShellTabs extends LitElement {
  createRenderRoot() {
    return this;
  }

  /** The apps the person's roles hold, in catalog order. */
  @property({ attribute: false }) apps: ShellApp[] = [];
  /** The framed app's id, or '' when none. */
  @property({ attribute: 'current-app-id' }) currentAppId = '';

  private _emit(type: string, detail?: unknown) {
    this.dispatchEvent(new CustomEvent(type, { detail, bubbles: true, composed: true }));
  }

  private _open(app: ShellApp) {
    this._emit('gable-shell-open-app', { appId: app.id, path: app.path });
  }

  private _tabs(): HTMLButtonElement[] {
    return [...this.querySelectorAll<HTMLButtonElement>('button[data-tab]')];
  }

  private _onKeydown(e: KeyboardEvent) {
    const tabs = this._tabs();
    if (tabs.length === 0) return;
    const here = tabs.indexOf(e.target as HTMLButtonElement);
    if (here < 0) return;
    let next: number;
    switch (e.key) {
      case 'ArrowRight':
      case 'ArrowDown':
        next = Math.min(tabs.length - 1, here + 1);
        break;
      case 'ArrowLeft':
      case 'ArrowUp':
        next = Math.max(0, here - 1);
        break;
      case 'Home':
        next = 0;
        break;
      case 'End':
        next = tabs.length - 1;
        break;
      default:
        return;
    }
    e.preventDefault();
    tabs[next]?.focus();
  }

  render() {
    if (this.apps.length === 0) return html``;
    return html`
      <nav
        aria-label="Field apps"
        class="shrink-0 border-t border-white/5 bg-deep-space/95 backdrop-blur-xl px-2 py-1.5 z-40"
      >
        <div class="mx-auto flex max-w-md items-stretch justify-around gap-2" @keydown=${this._onKeydown}>
          ${this.apps.map((app) => {
            const current = app.id === this.currentAppId;
            return html`
              <button
                type="button"
                data-tab=${app.id}
                aria-current=${current ? 'page' : 'false'}
                @click=${() => this._open(app)}
                class=${`flex-1 min-h-[48px] min-w-[44px] px-3 py-1.5 rounded-xl flex items-center justify-center gap-2 text-sm font-medium transition-colors ${
                  current
                    ? 'bg-gable-green/10 text-gable-green font-semibold border border-gable-green/30'
                    : 'text-zinc-400 hover:text-white hover:bg-white/5 border border-transparent'
                }`}
              >
                <span aria-hidden="true" class="shrink-0">${icon(appIcon(app.icon), 18)}</span>
                <span class="truncate">${app.name}</span>
              </button>
            `;
          })}
        </div>
      </nav>
    `;
  }
}
