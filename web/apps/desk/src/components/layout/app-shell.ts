// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * ERP shell — tabbed workspace (no sidebar).
 *
 * Modules launched from Home (or navigated to directly) open as tabs in
 * the strip under the header; Home is pinned first. Tab state lives in
 * lib/workspace.ts. The old left sidebar is gone — app discovery happens
 * on the Home launcher, power users use the omnibar (⌘K).
 */
import { LitElement, html, nothing } from 'lit';
import { customElement, state, property } from 'lit/decorators.js';
import { cn } from '../../lib/utils.ts';
import { router } from '../../lib/router.ts';
import { workspace, menuForKey, activeMenuPath, type WorkspaceTab } from '../../lib/workspace.ts';
import '@gable/design-system';
import { authCustody } from '@gable/auth';
import '../ui/omnibar.ts';
import '../ui/shortcuts-modal.ts';
import './branch-switcher.ts';
import { icon } from '../../lib/icons.ts';
import { Search, X, Plus, LogOut } from 'lucide';

/** Initials for a display name (the same rule the door uses). */
function initialsFor(name: string): string {
  const parts = name.trim().split(/[\s@.]+/).filter(Boolean);
  if (parts.length === 0) return '??';
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return (parts[0][0] + parts[1][0]).toUpperCase();
}

/** Capitalise a role slug (cashier -> Cashier). Same map the door uses. */
function roleInWords(role: string): string {
  if (role.length === 0) return '';
  return role[0].toUpperCase() + role.slice(1);
}

@customElement('gable-app-shell')
export class GableAppShell extends LitElement {
  createRenderRoot() { return this; }

  @property({ attribute: false }) pageContent: unknown = nothing;

  @state() private _shortcutsOpen = false;
  @state() private _isOffline = !navigator.onLine;
  @state() private _userMenuOpen = false;

  private _boundKeyDown = this._handleKeyDown.bind(this);
  private _boundOnline = () => { this._isOffline = false; };
  private _boundOffline = () => { this._isOffline = true; };
  private _boundDocClick = (e: Event) => {
    if (!this._userMenuOpen) return;
    if ((e.target as HTMLElement).closest?.('[data-user-menu]')) return;
    this._userMenuOpen = false;
  };
  private _boundRouteChanged = () => {
    workspace.syncToPath(router.currentPath);
    this.requestUpdate();
  };
  private _boundWorkspaceChanged = () => { this.requestUpdate(); };

  connectedCallback() {
    super.connectedCallback();
    window.addEventListener('keydown', this._boundKeyDown);
    window.addEventListener('online', this._boundOnline);
    window.addEventListener('offline', this._boundOffline);
    document.addEventListener('click', this._boundDocClick);
    router.addEventListener('route-changed', this._boundRouteChanged);
    workspace.addEventListener('workspace-changed', this._boundWorkspaceChanged);
    workspace.restore();
    workspace.syncToPath(router.currentPath);
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    window.removeEventListener('keydown', this._boundKeyDown);
    window.removeEventListener('online', this._boundOnline);
    window.removeEventListener('offline', this._boundOffline);
    document.removeEventListener('click', this._boundDocClick);
    router.removeEventListener('route-changed', this._boundRouteChanged);
    workspace.removeEventListener('workspace-changed', this._boundWorkspaceChanged);
  }

  private _handleKeyDown(e: KeyboardEvent) {
    if (e.key === '?' && !e.metaKey && !e.ctrlKey && !['INPUT', 'TEXTAREA'].includes((e.target as HTMLElement).tagName)) {
      e.preventDefault();
      this._shortcutsOpen = true;
    }
  }

  /**
   * The signed in user's name chip and its menu. The chip opens a small
   * menu with the person's name and role and Sign out; signing out clears
   * the shared auth custody and leaves the desk for the front door. When
   * nothing is signed in this renders the empty state (there is no signed
   * in user to show).
   */
  private _renderUser() {
    const session = authCustody.session;
    if (session === null) return nothing;
    const name = authCustody.displayName ?? '';
    const personaRole = authCustody.personaRole;
    const tokenRoles = authCustody.roles;
    const roleWords = personaRole !== null
      ? roleInWords(personaRole)
      : tokenRoles.length > 0
        ? tokenRoles.map(roleInWords).join(' · ')
        : 'local development';
    const initials = name === '' ? '??' : initialsFor(name);
    return html`
      <div class="relative" data-user-menu @focusout=${this._handleUserMenuFocusout}>
        <button
          @click=${() => this._toggleUserMenu()}
          @keydown=${this._handleUserChipKeydown}
          aria-haspopup="menu"
          aria-expanded=${this._userMenuOpen ? 'true' : 'false'}
          aria-label="Account menu"
          class="flex items-center gap-2 rounded-lg focus:outline-none focus-visible:ring-2 focus-visible:ring-gable-green focus-visible:ring-offset-2 focus-visible:ring-offset-deep-space"
        >
          <div class="hidden lg:flex items-center gap-2">
            <div class="h-9 w-9 rounded-full bg-gradient-to-br from-gable-green/20 to-emerald-500/20 border border-gable-green/30 flex items-center justify-center text-xs font-mono font-bold text-gable-green shadow-glow">
              ${initials}
            </div>
            <div class="leading-tight text-left">
              ${name !== '' ? html`<div class="text-sm font-medium text-white">${name}</div>` : nothing}
              <div class="text-[10px] uppercase tracking-wider text-muted-foreground font-mono">${roleWords}</div>
            </div>
          </div>
          <div class="lg:hidden h-9 w-9 rounded-full bg-gradient-to-br from-gable-green/20 to-emerald-500/20 border border-gable-green/30 flex items-center justify-center text-xs font-mono font-bold text-gable-green shadow-glow">
            ${initials}
          </div>
        </button>
        ${this._userMenuOpen ? html`
          <div
            class="absolute right-0 top-full mt-2 w-64 rounded-xl border border-white/10 bg-slate-steel shadow-xl z-50 overflow-hidden"
          >
            <div class="px-4 py-3 border-b border-white/5">
              ${name !== '' ? html`<div class="text-sm font-medium text-white">${name}</div>` : nothing}
              <div class="text-[10px] uppercase tracking-wider text-muted-foreground font-mono">${roleWords}</div>
            </div>
            <div
              role="menu"
              aria-label="Account"
              @keydown=${this._handleUserMenuKeydown}
            >
              <button
                role="menuitem"
                @click=${() => this._handleSignOut()}
                class="w-full flex items-center gap-2 px-4 py-2.5 text-sm text-zinc-300 hover:bg-white/5 hover:text-white transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-gable-green"
              >
                ${icon(LogOut, 16, 'w-4 h-4')}
                Sign out
              </button>
            </div>
          </div>
        ` : nothing}
      </div>
    `;
  }

  /** Open the menu and move the focus to its first (or last) item. */
  private _openUserMenu(focus: 'first' | 'last') {
    this._userMenuOpen = true;
    void this.updateComplete.then(() => {
      const items = Array.from(this.querySelectorAll<HTMLElement>('[role="menuitem"]'));
      if (items.length === 0) return;
      (focus === 'last' ? items[items.length - 1] : items[0]).focus();
    });
  }

  /** Click, Enter or Space on the chip (all fire click): toggle; an open
   *  moves the focus into the menu, the ARIA menu button pattern. */
  private _toggleUserMenu() {
    if (this._userMenuOpen) {
      this._closeUserMenu(false);
    } else {
      this._openUserMenu('first');
    }
  }

  /** On the chip: ArrowDown opens on the first item, ArrowUp on the last,
   *  Escape closes an open menu (the focus is already on the chip). */
  private _handleUserChipKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      if (!this._userMenuOpen) return;
      e.preventDefault();
      this._closeUserMenu(false);
      return;
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      this._openUserMenu('first');
      return;
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault();
      this._openUserMenu('last');
    }
  }

  /** The menu closes when the focus leaves the chip and menu for good
   *  (a Tab past the menu, a click elsewhere that moves the focus). */
  private _handleUserMenuFocusout(e: FocusEvent) {
    if (!this._userMenuOpen) return;
    const next = e.relatedTarget as Node | null;
    const wrap = this.querySelector('[data-user-menu]');
    if (next && wrap?.contains(next)) return;
    this._userMenuOpen = false;
  }

  /** In the menu: the arrows move between items, Escape closes and
   *  returns the focus to the chip. */
  private _handleUserMenuKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      this._closeUserMenu(true);
      return;
    }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    const items = Array.from(this.querySelectorAll<HTMLElement>('[role="menuitem"]'));
    if (items.length === 0) return;
    const idx = items.indexOf(document.activeElement as HTMLElement);
    const next = e.key === 'ArrowDown'
      ? (idx + 1) % items.length
      : (idx - 1 + items.length) % items.length;
    items[next].focus();
  }

  private _closeUserMenu(returnFocus: boolean) {
    this._userMenuOpen = false;
    if (returnFocus) {
      void this.updateComplete.then(() => {
        this.querySelector<HTMLButtonElement>('button[aria-haspopup="menu"]')?.focus();
      });
    }
  }

  private _handleSignOut() {
    this._userMenuOpen = false;
    authCustody.signOut();
    this._goToDoor();
  }

  /** The front door is a separate document served at /, so leaving the
   *  desk for it is a full navigation, the same way the door's tiles open
   *  the desk. */
  private _goToDoor() {
    window.location.assign('/');
  }

  private _tab(tab: WorkspaceTab) {
    const active = workspace.activeKey === tab.key;
    return html`
      <div
        class="${cn(
          'group relative flex items-center shrink-0 rounded-t-lg border-b-2 transition-all',
          active
            ? 'border-gable-green bg-gable-green/5 text-gable-green'
            : 'border-transparent text-zinc-400 hover:text-white hover:bg-white/5',
        )}"
      >
        <button
          @click=${() => workspace.activate(tab.key)}
          aria-label="Switch to ${tab.label}"
          aria-current=${active ? 'page' : 'false'}
          class="flex items-center gap-2 pl-3.5 py-2 text-sm font-medium ${tab.pinned ? 'pr-3.5' : 'pr-1'}"
        >
          <span class="${active ? 'text-gable-green' : ''}">${icon(tab.icon, 16)}</span>
          <span class="whitespace-nowrap">${tab.label}</span>
        </button>
        ${tab.pinned
          ? nothing
          : html`
              <button
                @click=${(e: Event) => { e.stopPropagation(); workspace.close(tab.key); }}
                aria-label="Close ${tab.label}"
                class="mr-1.5 rounded p-0.5 text-zinc-600 hover:text-white hover:bg-white/10 transition-colors ${active ? 'text-zinc-400' : ''}"
              >
                ${icon(X, 13)}
              </button>
            `}
      </div>
    `;
  }

  /** Menu band for the active tab: app identity + its own menu items. */
  private _appMenu() {
    const key = workspace.activeKey;
    if (key === 'home') return nothing;
    const menu = menuForKey(key);
    if (menu.length === 0) return nothing;
    const tab = workspace.tabs.find((t) => t.key === key);
    const active = activeMenuPath(menu, router.currentPath);
    return html`
      <div class="flex items-center gap-1 px-4 md:px-6 h-11 bg-deep-space/60 border-b border-white/5 overflow-x-auto no-scrollbar">
        ${tab
          ? html`<span class="flex items-center gap-2 pr-3 mr-2 border-r border-white/10 text-[11px] font-semibold uppercase tracking-wider text-zinc-500 shrink-0">
              ${icon(tab.icon, 13)} ${tab.label}
            </span>`
          : nothing}
        ${menu.map(
          (item) => html`
            <a
              href="${item.path}"
              class="${cn(
                'shrink-0 px-3 py-1.5 rounded-md text-sm font-medium transition-colors',
                active === item.path
                  ? 'text-gable-green bg-gable-green/10'
                  : 'text-zinc-400 hover:text-white hover:bg-white/5',
              )}"
              >${item.label}</a
            >
          `,
        )}
      </div>
    `;
  }

  render() {
    return html`
      <div class="min-h-screen bg-deep-space text-foreground flex flex-col font-sans selection:bg-gable-green/30">
        <!-- Skip Navigation -->
        <a href="#main-content" class="sr-only focus:not-sr-only focus:absolute focus:z-50 focus:top-4 focus:left-4 focus:px-4 focus:py-2 focus:bg-[#00FFA3] focus:text-[#0A0B10] focus:rounded">
          Skip to main content
        </a>

        <!-- Header -->
        <header class="h-16 border-b border-white/5 bg-deep-space/80 backdrop-blur-xl px-4 md:px-6 flex items-center gap-4 sticky top-0 z-40 shadow-sm">
          <a href="/home" aria-label="Go to Home" class="flex items-center gap-3 shrink-0">
            <gable-brand-logo variant="mark" size="md" class-name="text-white drop-shadow-glow"></gable-brand-logo>
            <span class="hidden md:block"><gable-brand-logo variant="text" size="md"></gable-brand-logo></span>
          </a>

          <div class="flex-1 max-w-xl mx-auto">
            <div class="relative group">
              ${icon(Search, 16, 'absolute left-3 top-1/2 -translate-y-1/2 text-zinc-500 group-focus-within:text-gable-green transition-colors')}
              <input
                type="text"
                placeholder="Search everything... (Cmd+K)"
                aria-label="Search everything"
                class="w-full bg-slate-steel/50 border border-white/5 rounded-full py-2 pl-10 pr-4 text-sm text-white focus:outline-none focus:ring-1 focus:ring-gable-green/50 focus:bg-slate-steel transition-all"
              />
            </div>
          </div>

          <div class="flex items-center gap-4 shrink-0">
            <gable-branch-switcher></gable-branch-switcher>
            <div class="text-xs text-zinc-500 font-medium hidden lg:block bg-white/5 px-2 py-1 rounded border border-white/5">
              ⌘K
            </div>
            ${this._renderUser()}
          </div>
        </header>

        <!-- Workspace tab strip -->
        <nav
          aria-label="Open modules"
          class="sticky top-16 z-30 flex items-end gap-1 px-3 md:px-5 pt-1.5 bg-slate-steel/60 backdrop-blur-xl border-b border-white/5 overflow-x-auto no-scrollbar"
        >
          ${workspace.tabs.map((t) => this._tab(t))}
          <button
            @click=${() => router.navigate('/home')}
            aria-label="Open an app"
            title="Open an app"
            class="ml-1 mb-1.5 shrink-0 rounded-lg p-1.5 text-zinc-500 hover:text-gable-green hover:bg-white/5 transition-colors"
          >
            ${icon(Plus, 16)}
          </button>
        </nav>

        <!-- Per-app menu band (the active tab's own navigation) -->
        ${this._appMenu()}

        <!-- Offline Banner -->
        ${this._isOffline ? html`
          <div class="bg-amber-500/10 border border-amber-500/30 text-amber-400 text-sm px-4 py-2 text-center">
            You are offline. Some features may not be available.
          </div>
        ` : nothing}

        <!-- Page Content -->
        <main class="flex-1 w-full">
          <div id="main-content" class="p-6 md:p-8 max-w-[1600px] w-full mx-auto animate-fade-in">
            ${this.pageContent}
          </div>
        </main>

        <gable-omnibar></gable-omnibar>
        <gable-shortcuts-modal .open=${this._shortcutsOpen} @close=${() => { this._shortcutsOpen = false; }}></gable-shortcuts-modal>
      </div>
    `;
  }
}
