// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The shell's ONE header band. White labelled: the dealer's mark and name
// lead (from the dealer profile the core serves; no Gable mark competes with
// it), the app selector sits centred, and the right side holds the framed
// app's actions, the yard, theme, feedback, and the account chip with its
// menu (Switch role, Sign out).
//
// Purely presentational: the host hands the band the catalog slice, the
// dealer profile, the yards, the signed in person and the framed app's feed,
// and the band emits
//
//   gable-shell-open-app     { appId, path }   a row of the selector was picked
//   gable-shell-menu                             the rail's menu control was pressed
//   gable-shell-theme                            the theme control was pressed
//   gable-shell-feedback                         the feedback control was pressed
//   gable-shell-yard         { branchId }        a yard was picked
//   gable-shell-frame-action { id }              a framed app's action was pressed
//   gable-shell-switch-role                      Switch role was picked
//   gable-shell-sign-out                         Sign out was picked
//
// The selector's keyboard is the house menu keyboard: ArrowUp and ArrowDown
// move, Home and End jump, Escape closes and hands focus back to the control,
// Tab dismisses.

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '@gable/design-system';
import type { FrameAction } from '@gable/design-system';
import {
  Building2,
  Check,
  ChevronDown,
  LogOut,
  MessageSquarePlus,
  Moon,
  PanelLeft,
  Sun,
  UserRoundPlus,
} from 'lucide';
import { appById, initialsOf, type ShellApp } from './shell-state.ts';

/** One yard the person can stand in, the slice of /api/v1/me/branches the header needs. */
export interface ShellYard {
  id: string;
  code: string;
  name: string;
  is_home?: boolean;
}

@customElement('gable-shell-header')
export class GableShellHeader extends LitElement {
  createRenderRoot() {
    return this;
  }

  /** The dealer's display name, from the dealer profile. Empty renders no word. */
  @property({ attribute: 'dealer-name' }) dealerName = '';
  /** The dealer's mark URL (the profile's logo). Empty renders the initials tile. */
  @property({ attribute: 'dealer-mark-url' }) dealerMarkUrl = '';
  /** The apps the person's roles hold, in catalog order. */
  @property({ attribute: false }) apps: ShellApp[] = [];
  /** The framed app's id, or '' when none. */
  @property({ attribute: 'current-app-id' }) currentAppId = '';
  /** The framed app's own title, posted up through the chrome feed. */
  @property({ attribute: 'frame-title' }) frameTitle = '';
  /** The framed app's page actions, posted up through the chrome feed. */
  @property({ attribute: false }) frameActions: readonly FrameAction[] = [];
  /** True when the framed app announced it can act on Feedback. */
  @property({ type: Boolean, attribute: 'feedback-on' }) feedbackOn = false;
  /** The applied theme, for the toggle's icon. */
  @property({ attribute: 'theme' }) theme: 'dark' | 'light' = 'dark';
  /** The yards the person can stand in. */
  @property({ attribute: false }) yards: readonly ShellYard[] = [];
  /** The current yard's id; null means all yards. */
  @property({ attribute: 'current-yard-id' }) currentYardId: string | null = null;
  /** The signed in person's display name. */
  @property({ attribute: 'user-name' }) userName = '';
  /** The signed in person's role in words. */
  @property({ attribute: 'user-role-label' }) userRoleLabel = '';

  @state() private _selectorOpen = false;
  @state() private _accountOpen = false;
  @state() private _yardsOpen = false;

  private _onDocPointerDown = (e: Event) => {
    if (e.composedPath().includes(this)) return;
    this._selectorOpen = false;
    this._accountOpen = false;
    this._yardsOpen = false;
  };

  connectedCallback() {
    super.connectedCallback();
    document.addEventListener('pointerdown', this._onDocPointerDown, true);
  }

  disconnectedCallback() {
    document.removeEventListener('pointerdown', this._onDocPointerDown, true);
    super.disconnectedCallback();
  }

  private _emit(type: string, detail?: unknown) {
    this.dispatchEvent(new CustomEvent(type, { detail, bubbles: true, composed: true }));
  }

  // -- the selector -----------------------------------------------------------

  private _menuItems(): HTMLButtonElement[] {
    return [...this.querySelectorAll<HTMLButtonElement>('[data-selector-panel] button[role="menuitem"]')];
  }

  private _openSelector() {
    this._selectorOpen = true;
    void this.updateComplete.then(() => this._menuItems()[0]?.focus());
  }

  private _closeSelector(refocus = false) {
    if (!this._selectorOpen) return;
    this._selectorOpen = false;
    if (refocus) {
      void this.updateComplete.then(() =>
        this.querySelector<HTMLButtonElement>('[data-action="selector"]')?.focus(),
      );
    }
  }

  private _onSelectorKeydown(e: KeyboardEvent) {
    if (e.key === 'ArrowDown' && !this._selectorOpen) {
      e.preventDefault();
      this._openSelector();
      return;
    }
    if (!this._selectorOpen) return;
    const items = this._menuItems();
    if (items.length === 0) return;
    const here = items.indexOf(e.target as HTMLButtonElement);
    const move = (to: number) => {
      e.preventDefault();
      items[Math.max(0, Math.min(items.length - 1, to))]?.focus();
    };
    switch (e.key) {
      case 'ArrowDown':
        return move(here + 1);
      case 'ArrowUp':
        return move(here - 1);
      case 'Home':
        return move(0);
      case 'End':
        return move(items.length - 1);
      case 'Escape':
        e.preventDefault();
        return this._closeSelector(true);
      case 'Tab':
        // Tabbing away is a dismissal, not a trap.
        return this._closeSelector();
      default:
    }
  }

  private _pickApp(app: ShellApp) {
    this._selectorOpen = false;
    this._emit('gable-shell-open-app', { appId: app.id, path: app.path });
  }

  // -- the account menu ---------------------------------------------------------

  private _openAccount(focus: 'first' | 'last') {
    this._accountOpen = true;
    void this.updateComplete.then(() => {
      const items = [...this.querySelectorAll<HTMLElement>('[data-account-menu] [role="menuitem"]')];
      if (items.length === 0) return;
      (focus === 'last' ? items[items.length - 1] : items[0]).focus();
    });
  }

  private _closeAccount(refocus: boolean) {
    this._accountOpen = false;
    if (refocus) {
      void this.updateComplete.then(() =>
        this.querySelector<HTMLButtonElement>('[data-action="account"]')?.focus(),
      );
    }
  }

  private _onAccountChipKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      if (!this._accountOpen) return;
      e.preventDefault();
      this._closeAccount(false);
      return;
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      this._openAccount('first');
      return;
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault();
      this._openAccount('last');
    }
  }

  private _onAccountMenuKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      this._closeAccount(true);
      return;
    }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    const items = [...this.querySelectorAll<HTMLElement>('[data-account-menu] [role="menuitem"]')];
    if (items.length === 0) return;
    const idx = items.indexOf(document.activeElement as HTMLElement);
    const next = e.key === 'ArrowDown' ? (idx + 1) % items.length : (idx - 1 + items.length) % items.length;
    items[next]?.focus();
  }

  // -- the yard picker ------------------------------------------------------------

  private _yardLabel(): string {
    const current = this.yards.find((y) => y.id === this.currentYardId);
    if (current) return current.name || current.code;
    if (this.currentYardId === null && this.yards.length > 0) return 'All yards';
    return 'No yard';
  }

  private _pickYard(branchId: string | null) {
    this._yardsOpen = false;
    this._emit('gable-shell-yard', { branchId });
  }

  // -- render -----------------------------------------------------------------------

  render() {
    const app = appById(this.apps, this.currentAppId);
    const initials = initialsOf(this.userName);
    return html`
      <header
        role="banner"
        class="relative h-14 border-b border-white/5 bg-deep-space/85 backdrop-blur-xl px-3 md:px-4 flex items-center gap-2 z-40"
      >
        <button
          type="button"
          data-action="menu"
          aria-label="Apps rail"
          title="Apps rail"
          @click=${() => this._emit('gable-shell-menu')}
          class="hidden sm:flex h-9 w-9 shrink-0 rounded-lg items-center justify-center text-zinc-400 hover:text-white hover:bg-white/5 border border-white/5 transition-colors"
        >
          ${icon(PanelLeft, 16)}
        </button>

        <div class="flex items-center gap-2.5 min-w-0 shrink-0">
          ${this.dealerMarkUrl !== ''
            ? html`<img
                src=${this.dealerMarkUrl}
                alt=""
                class="h-8 w-8 rounded-lg object-contain bg-white/5 border border-white/5"
              />`
            : this.dealerName !== ''
              ? html`<span
                  aria-hidden="true"
                  class="h-8 w-8 rounded-lg bg-gable-green/10 border border-gable-green/30 flex items-center justify-center text-[11px] font-mono font-bold text-gable-green"
                  >${initialsOf(this.dealerName)}</span
                >`
              : nothing}
          ${this.dealerName !== ''
            ? html`<span class="hidden md:block text-sm font-semibold text-white max-w-[220px] truncate"
                >${this.dealerName}</span
              >`
            : nothing}
        </div>

        <div class="flex-1 min-w-0 flex justify-center px-1 sm:flex-none sm:absolute sm:left-1/2 sm:top-1/2 sm:-translate-x-1/2 sm:-translate-y-1/2 sm:max-w-[46vw] sm:px-0">
          <button
            type="button"
            data-action="selector"
            aria-haspopup="menu"
            aria-expanded=${this._selectorOpen ? 'true' : 'false'}
            @click=${() => (this._selectorOpen ? this._closeSelector(true) : this._openSelector())}
            @keydown=${this._onSelectorKeydown}
            class="flex items-center gap-2 h-9 max-w-full px-3 rounded-lg border border-white/5 bg-slate-steel/50 text-sm font-medium text-zinc-200 hover:border-gable-green/30 hover:bg-slate-steel transition-colors"
          >
            <span class="truncate">
            ${app === undefined ? 'Apps' : app.name}${''}
            <span class="hidden sm:inline">${this.frameTitle !== '' ? ` · ${this.frameTitle}` : ''}</span>
          </span>
            <span class="text-zinc-500 shrink-0">${icon(ChevronDown, 14)}</span>
          </button>
        </div>

        <div class="ml-auto flex items-center gap-2 shrink-0">
          ${this.frameActions.map(
            (action) => html`
              <button
                type="button"
                data-action="frame-action"
                @click=${() => this._emit('gable-shell-frame-action', { id: action.id })}
                class="hidden lg:block h-9 px-3 rounded-lg border border-gable-green/30 bg-gable-green/10 text-sm font-medium text-gable-green hover:bg-gable-green/20 transition-colors"
              >
                ${action.label}
              </button>
            `,
          )}
          ${this._renderYards()}
          <button
            type="button"
            data-action="theme"
            aria-label=${this.theme === 'dark' ? 'Light mode' : 'Dark mode'}
            title=${this.theme === 'dark' ? 'Light mode' : 'Dark mode'}
            @click=${() => this._emit('gable-shell-theme')}
            class="h-9 w-9 rounded-lg flex items-center justify-center text-zinc-400 hover:text-white hover:bg-white/5 border border-white/5 transition-colors"
          >
            ${this.theme === 'dark' ? icon(Sun, 16) : icon(Moon, 16)}
          </button>
          <button
            type="button"
            data-action="feedback"
            aria-label="Feedback"
            title=${this.feedbackOn ? 'Feedback' : 'The framed app has no feedback channel'}
            ?disabled=${!this.feedbackOn}
            @click=${() => this._emit('gable-shell-feedback')}
            class=${this.feedbackOn
              ? 'h-9 w-9 rounded-lg flex items-center justify-center text-zinc-400 hover:text-white hover:bg-white/5 border border-white/5 transition-colors'
              : 'h-9 w-9 rounded-lg flex items-center justify-center text-zinc-600 border border-white/5 cursor-not-allowed'}
          >
            ${icon(MessageSquarePlus, 16)}
          </button>
          ${this._renderAccount(initials)}
        </div>
      </header>
      ${this._selectorOpen ? this._renderSelectorPanel() : nothing}
    `;
  }

  private _renderSelectorPanel() {
    return html`
      <div
        data-selector-panel
        role="menu"
        aria-label="Apps"
        class="absolute left-1/2 -translate-x-1/2 top-14 z-50 min-w-[240px] max-w-[min(320px,calc(100vw-24px))] max-h-[70vh] overflow-y-auto rounded-xl border border-white/10 bg-slate-steel shadow-elevation-2 p-1.5"
      >
        ${this.apps.map((app) => {
          const current = app.id === this.currentAppId;
          return html`
            <button
              type="button"
              role="menuitem"
              tabindex="-1"
              aria-current=${current ? 'true' : 'false'}
              @click=${() => this._pickApp(app)}
              @keydown=${this._onSelectorKeydown}
              class=${`w-full flex items-center gap-2.5 min-h-[44px] px-3 rounded-lg text-sm text-left transition-colors ${
                current ? 'bg-gable-green/10 text-gable-green font-semibold' : 'text-zinc-200 hover:bg-white/5'
              }`}
            >
              <span class="flex-1 truncate">${app.name}</span>
              ${current ? html`<span class="text-[10px] uppercase tracking-wider font-bold">here</span>` : nothing}
            </button>
          `;
        })}
      </div>
    `;
  }

  private _renderYards() {
    if (this.yards.length === 0) return nothing;
    return html`
      <div class="relative">
        <button
          type="button"
          data-action="yards"
          aria-haspopup="listbox"
          aria-expanded=${this._yardsOpen ? 'true' : 'false'}
          @click=${() => {
            this._yardsOpen = !this._yardsOpen;
          }}
          class="flex items-center gap-2 px-3 h-9 rounded-md border border-white/10 bg-slate-steel/50 hover:bg-slate-steel hover:border-gable-green/30 transition-colors text-sm text-zinc-200"
        >
          <span class="text-gable-green">${icon(Building2, 14)}</span>
          <span class="hidden md:block font-medium max-w-[140px] truncate">${this._yardLabel()}</span>
          <span class="text-zinc-500">${icon(ChevronDown, 14)}</span>
        </button>
        ${this._yardsOpen
          ? html`
              <div
                role="listbox"
                aria-label="Yards"
                class="absolute right-0 mt-2 w-64 bg-slate-steel border border-white/10 rounded-lg shadow-elevation-2 overflow-hidden z-50"
              >
                <div class="py-1 max-h-80 overflow-y-auto">
                  ${this.yards.map((yard) => {
                    const active = yard.id === this.currentYardId;
                    return html`
                      <button
                        type="button"
                        role="option"
                        aria-selected=${active ? 'true' : 'false'}
                        @click=${() => this._pickYard(yard.id)}
                        class="w-full text-left px-3 py-2 flex items-center gap-2 text-sm hover:bg-white/5 ${active
                          ? 'text-gable-green'
                          : 'text-zinc-200'}"
                      >
                        <span class="w-4 shrink-0">${active ? icon(Check, 14) : nothing}</span>
                        <span class="font-mono text-xs text-zinc-500 w-16 shrink-0">${yard.code}</span>
                        <span class="flex-1 truncate">${yard.name || yard.code}</span>
                        ${yard.is_home
                          ? html`<span class="text-[10px] uppercase tracking-wider text-gable-green/70">home</span>`
                          : nothing}
                      </button>
                    `;
                  })}
                </div>
              </div>
            `
          : nothing}
      </div>
    `;
  }

  private _renderAccount(initials: string) {
    const label = this.userName === '' ? 'Account' : this.userName;
    return html`
      <div class="relative">
        <button
          type="button"
          data-action="account"
          aria-haspopup="menu"
          aria-expanded=${this._accountOpen ? 'true' : 'false'}
          aria-label="${label} account"
          @click=${() => (this._accountOpen ? this._closeAccount(false) : this._openAccount('first'))}
          @keydown=${this._onAccountChipKeydown}
          class="flex items-center gap-2 rounded-full border border-white/10 pl-1 pr-3 py-1 hover:border-gable-green/30 transition-colors"
        >
          <span
            aria-hidden="true"
            class="h-7 w-7 rounded-full bg-gradient-to-br from-gable-green/20 to-emerald-500/20 border border-gable-green/30 flex items-center justify-center text-[10px] font-mono font-bold text-gable-green"
            >${initials === '' ? '?' : initials}</span
          >
          <span class="hidden lg:block text-xs font-semibold text-white max-w-[140px] truncate">${label}</span>
        </button>
        ${this._accountOpen
          ? html`
              <div
                data-account-menu
                class="absolute right-0 top-full mt-2 w-64 rounded-xl border border-white/10 bg-slate-steel shadow-elevation-2 z-50 overflow-hidden"
              >
                <div class="px-4 py-3 border-b border-white/5">
                  ${this.userName !== ''
                    ? html`<div class="text-sm font-medium text-white">${this.userName}</div>`
                    : nothing}
                  ${this.userRoleLabel !== ''
                    ? html`<div class="text-[10px] uppercase tracking-wider text-muted-foreground font-mono">
                        ${this.userRoleLabel}
                      </div>`
                    : nothing}
                </div>
                <div role="menu" aria-label="Account" @keydown=${this._onAccountMenuKeydown}>
                  <button
                    type="button"
                    role="menuitem"
                    @click=${() => {
                      this._closeAccount(false);
                      this._emit('gable-shell-switch-role');
                    }}
                    class="w-full flex items-center gap-2 px-4 py-2.5 text-sm text-zinc-300 hover:bg-white/5 hover:text-white transition-colors"
                  >
                    ${icon(UserRoundPlus, 16, 'w-4 h-4')}
                    Switch role
                  </button>
                  <button
                    type="button"
                    role="menuitem"
                    @click=${() => {
                      this._closeAccount(false);
                      this._emit('gable-shell-sign-out');
                    }}
                    class="w-full flex items-center gap-2 px-4 py-2.5 text-sm text-zinc-300 hover:bg-white/5 hover:text-white transition-colors"
                  >
                    ${icon(LogOut, 16, 'w-4 h-4')}
                    Sign out
                  </button>
                </div>
              </div>
            `
          : nothing}
      </div>
    `;
  }
}
