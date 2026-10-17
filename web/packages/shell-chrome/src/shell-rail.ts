// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The apps rail: the apps the person's roles hold and their few top level
// places, never a navigation tree. Hidden by default; the host reveals it on
// hover or on a tap of the header's menu control (a peek), and the person can
// pin it as icons or wide. The phone shows no rail at all; the narrow screen
// caps it at icons. The mode itself is resolved by the pure railModeFor in
// shell-state.ts; this element renders what the host hands it and emits
//
//   gable-shell-rail-open    { appId, path }   an app or a place was picked
//   gable-shell-rail-choice  { choice }        pin as shown, toggle icons and
//                                              wide, or unpin back to hidden
//
// Rows are one keyboard: ArrowUp and ArrowDown move, Home and End jump.

import { LitElement, html, nothing, type TemplateResult } from 'lit';
import { customElement, property } from 'lit/decorators.js';
import { icon } from '@gable/design-system';
import { Pin, PinOff, PanelLeftClose, PanelLeftOpen } from 'lucide';
import { initialsOf, type RailChoice, type RailMode, type ShellApp, type ShellPlace } from './shell-state.ts';

@customElement('gable-shell-rail')
export class GableShellRail extends LitElement {
  createRenderRoot() {
    return this;
  }

  /** The apps the person's roles hold, in catalog order. */
  @property({ attribute: false }) apps: ShellApp[] = [];
  /** The framed app's id, or '' when none. */
  @property({ attribute: 'current-app-id' }) currentAppId = '';
  /** The rail's display mode; 'none' renders nothing. */
  @property({ attribute: 'mode' }) mode: RailMode = 'none';
  /** True when the rail is pinned (the person's choice is not 'hidden'). */
  @property({ type: Boolean, attribute: 'pinned' }) pinned = false;

  private _emit(type: string, detail?: unknown) {
    this.dispatchEvent(new CustomEvent(type, { detail, bubbles: true, composed: true }));
  }

  private _open(appId: string, path: string) {
    this._emit('gable-shell-rail-open', { appId, path });
  }

  private _choose(choice: RailChoice) {
    this._emit('gable-shell-rail-choice', { choice });
  }

  private _rows(): HTMLButtonElement[] {
    return [...this.querySelectorAll<HTMLButtonElement>('button[data-row]')];
  }

  private _onKeydown(e: KeyboardEvent) {
    const rows = this._rows();
    if (rows.length === 0) return;
    const here = rows.indexOf(e.target as HTMLButtonElement);
    if (here < 0) return;
    let next: number;
    switch (e.key) {
      case 'ArrowDown':
        next = Math.min(rows.length - 1, here + 1);
        break;
      case 'ArrowUp':
        next = Math.max(0, here - 1);
        break;
      case 'Home':
        next = 0;
        break;
      case 'End':
        next = rows.length - 1;
        break;
      default:
        return;
    }
    e.preventDefault();
    rows[next]?.focus();
  }

  render() {
    if (this.mode === 'none') return html``;
    const wide = this.mode === 'wide';
    return html`
      <nav
        aria-label="Apps"
        data-mode=${this.mode}
        class=${`fixed left-0 top-14 bottom-0 z-30 flex flex-col border-r border-white/5 bg-deep-space/95 backdrop-blur-xl ${
          wide ? 'w-60' : 'w-14'
        }`}
      >
        <div class=${`flex items-center gap-1 p-2 border-b border-white/5 ${wide ? 'justify-between' : 'flex-col'}`}>
          ${wide
            ? html`<span class="px-1 text-[10px] font-bold uppercase tracking-[0.13em] text-zinc-500">Apps</span>`
            : nothing}
          <span class="flex items-center gap-1 ${wide ? '' : 'flex-col'}">
            ${this.pinned
              ? html`
                  <button
                    type="button"
                    data-action="unpin"
                    aria-label="Hide the rail"
                    title="Hide the rail"
                    @click=${() => this._choose('hidden')}
                    class="h-8 w-8 rounded-md flex items-center justify-center text-zinc-400 hover:text-white hover:bg-white/5 transition-colors"
                  >
                    ${icon(PinOff, 14)}
                  </button>
                  <button
                    type="button"
                    data-action="toggle-width"
                    aria-label=${wide ? 'Pin as icons' : 'Pin wide'}
                    title=${wide ? 'Pin as icons' : 'Pin wide'}
                    @click=${() => this._choose(wide ? 'icons' : 'wide')}
                    class="h-8 w-8 rounded-md flex items-center justify-center text-zinc-400 hover:text-white hover:bg-white/5 transition-colors"
                  >
                    ${wide ? icon(PanelLeftClose, 14) : icon(PanelLeftOpen, 14)}
                  </button>
                `
              : html`
                  <button
                    type="button"
                    data-action="pin"
                    aria-label="Keep the rail open"
                    title="Keep the rail open"
                    @click=${() => this._choose(this.mode === 'wide' ? 'wide' : 'icons')}
                    class="h-8 w-8 rounded-md flex items-center justify-center text-zinc-400 hover:text-white hover:bg-white/5 transition-colors"
                  >
                    ${icon(Pin, 14)}
                  </button>
                `}
          </span>
        </div>
        <div class="flex-1 overflow-y-auto py-2 ${wide ? 'px-2' : 'px-1.5'}" @keydown=${this._onKeydown}>
          ${this.apps.map((app) => (wide ? this._renderAppWide(app) : this._renderAppIcon(app)))}
        </div>
      </nav>
    `;
  }

  private _renderAppIcon(app: ShellApp): TemplateResult {
    const current = app.id === this.currentAppId;
    return html`
      <button
        type="button"
        data-row
        data-app=${app.id}
        aria-label=${app.name}
        aria-current=${current ? 'true' : 'false'}
        @click=${() => this._open(app.id, app.path)}
        class=${`group relative flex h-11 w-11 mx-auto items-center justify-center rounded-lg text-[11px] font-mono font-bold transition-colors ${
          current
            ? 'bg-gable-green/10 text-gable-green shadow-[inset_2px_0_0_0_rgb(var(--gb-gable-green))]'
            : 'text-zinc-400 hover:text-white hover:bg-white/5'
        }`}
      >
        ${initialsOf(app.name)}
        <span
          aria-hidden="true"
          class="pointer-events-none absolute left-[calc(100%+8px)] top-1/2 -translate-y-1/2 whitespace-nowrap rounded-lg border border-white/10 bg-slate-steel px-2 py-1 text-xs font-sans font-medium text-zinc-200 opacity-0 shadow-elevation-2 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100"
          >${app.name}</span
        >
      </button>
    `;
  }

  private _renderAppWide(app: ShellApp): TemplateResult {
    const current = app.id === this.currentAppId;
    return html`
      <div class="mb-1">
        <button
          type="button"
          data-row
          data-app=${app.id}
          aria-current=${current ? 'true' : 'false'}
          @click=${() => this._open(app.id, app.path)}
          class=${`w-full flex items-center gap-2.5 min-h-[44px] px-3 rounded-lg text-sm text-left transition-colors ${
            current
              ? 'bg-gable-green/10 text-gable-green font-semibold shadow-[inset_3px_0_0_0_rgb(var(--gb-gable-green))]'
              : 'text-zinc-200 hover:bg-white/5'
          }`}
        >
          <span
            aria-hidden="true"
            class=${`h-6 w-6 shrink-0 rounded-md flex items-center justify-center text-[9px] font-mono font-bold ${
              current ? 'bg-gable-green/20 text-gable-green' : 'bg-white/5 text-zinc-400'
            }`}
            >${initialsOf(app.name)}</span
          >
          <span class="flex-1 truncate">${app.name}</span>
        </button>
        ${app.places.map((place) => this._renderPlace(app, place))}
      </div>
    `;
  }

  private _renderPlace(app: ShellApp, place: ShellPlace): TemplateResult {
    return html`
      <button
        type="button"
        data-row
        data-app=${app.id}
        data-place=${place.id}
        @click=${() => this._open(app.id, place.path)}
        class="w-full flex items-center min-h-[36px] pl-11 pr-3 rounded-lg text-[13px] text-left text-zinc-400 hover:text-white hover:bg-white/5 transition-colors"
      >
        <span class="truncate">${place.label}</span>
      </button>
    `;
  }
}
