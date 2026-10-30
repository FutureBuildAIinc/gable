// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The Field shell host: a phone first frame for the field roles (warehouse
// and driver in Gable's own role words, as tauri/core/src/profiles.rs maps
// them) using @gable/shell-chrome in its phone mode.
//
//   door    no session: the shell's own sign in (the dev card when the build
//           allows it, the front door's address otherwise).
//   field   a session: the compact phone top bar (the dealer's lockup, the
//           yard picker, the theme toggle, the account chip), the honest
//           offline banner (money never queues), the selected field app in a
//           same origin frame, and the bottom tab bar for the role's field
//           apps (Yard and Driver). Every interactive control has a touch
//           target of at least 44 px.
//
// Sign out and Switch role first ASK the frame to run its own sign out
// (src/sign-out.ts), the same nonce verified handshake with a bounded wait as
// the work shell. A wrong or late nonce never signs anything out silently:
// the host then clears its own custody and says so.

import { LitElement, html, nothing, type TemplateResult } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { keyed } from 'lit/directives/keyed.js';
import {
  acceptChromeControls,
  acceptChromeTitle,
  acceptSwitchRole,
  announceChromeAction,
  announceFrameAction,
  announceYardChanged,
  currentTheme,
  framedAddress,
  onThemeChange,
  toggleTheme,
  type ChromeAction,
  type ChromeTitleFeed,
  type Theme,
} from '@gable/design-system';
import { authConfig, authCustody, signOutStaff } from '@gable/auth';
import type { ShellApp, ShellYard } from '@gable/shell-chrome';
import '@gable/shell-chrome';
import { FIELD_APPS } from './config.ts';
import { loadDealerBrand, type DealerBrand } from './dealer.ts';
import { initialState, openApp, signedIn, switchRole, type HostState } from './host-state.ts';
import { sessionFromCustody } from './session.ts';
import { requestFrameSignOut, type FrameSignOutOutcome } from './sign-out.ts';
import { currentYardId, loadYards, storeYard } from './yard.ts';

/** The roles a dev sign in can pick from in the field shell, the seed's own vocabulary. */
const DEV_ROLES: readonly { id: string; label: string }[] = [
  { id: 'warehouse', label: 'Warehouse' },
  { id: 'driver', label: 'Driver' },
  { id: 'cashier', label: 'Cashier' },
  { id: 'owner', label: 'Owner' },
  { id: 'admin', label: 'Admin' },
];

@customElement('gable-field-shell')
export class GableFieldShell extends LitElement {
  createRenderRoot() {
    return this;
  }

  @state() private _state: HostState = initialState();
  @state() private _dealer: DealerBrand = { name: '', markUrl: '' };
  @state() private _yards: readonly ShellYard[] = [];
  @state() private _currentYardId: string | null = null;
  @state() private _online = typeof navigator === 'undefined' ? true : navigator.onLine;
  @state() private _theme: Theme = currentTheme();
  @state() private _frameControls: readonly ChromeAction[] = [];
  @state() private _frameFeed: ChromeTitleFeed | null = null;
  @state() private _notice = '';
  /** How the last sign out or switch role ended, for the page's test hook. */
  @state() private _leaveOutcome: FrameSignOutOutcome | 'ended-by-app' | '' = '';

  private _leaving = false;
  @state() private _devName = '';
  @state() private _devRole = 'warehouse';
  private _offTheme: (() => void) | null = null;

  private _onConnection = () => {
    this._online = navigator.onLine;
  };

  connectedCallback() {
    super.connectedCallback();
    this.addEventListener('gable-shell-open-app', this._onOpenApp as EventListener);
    this.addEventListener('gable-shell-theme', this._onTheme);
    this.addEventListener('gable-shell-feedback', this._onFeedback);
    this.addEventListener('gable-shell-yard', this._onYard as EventListener);
    this.addEventListener('gable-shell-frame-action', this._onFrameAction as EventListener);
    this.addEventListener('gable-shell-switch-role', this._onSwitchRole);
    this.addEventListener('gable-shell-sign-out', this._onSignOut);
    window.addEventListener('message', this._onMessage);
    window.addEventListener('online', this._onConnection);
    window.addEventListener('offline', this._onConnection);
    this._offTheme = onThemeChange((t) => {
      this._theme = t;
    });
    this._currentYardId = currentYardId();
    void this._boot();
  }

  disconnectedCallback() {
    this.removeEventListener('gable-shell-open-app', this._onOpenApp as EventListener);
    this.removeEventListener('gable-shell-theme', this._onTheme);
    this.removeEventListener('gable-shell-feedback', this._onFeedback);
    this.removeEventListener('gable-shell-yard', this._onYard as EventListener);
    this.removeEventListener('gable-shell-frame-action', this._onFrameAction as EventListener);
    this.removeEventListener('gable-shell-switch-role', this._onSwitchRole);
    this.removeEventListener('gable-shell-sign-out', this._onSignOut);
    window.removeEventListener('message', this._onMessage);
    window.removeEventListener('online', this._onConnection);
    window.removeEventListener('offline', this._onConnection);
    this._offTheme?.();
    super.disconnectedCallback();
  }

  // -- boot --------------------------------------------------------------------

  private async _boot(): Promise<void> {
    const session = sessionFromCustody();
    if (session === null) return;
    this._state = signedIn(session, FIELD_APPS);
    const [dealer, yards] = await Promise.all([loadDealerBrand(), loadYards()]);
    this._dealer = dealer;
    this._yards = yards;
    // The yard the frame opens on: the stored pick while it is still granted,
    // otherwise the home yard (the desk's BranchContext answers the same way).
    const stored =
      this._currentYardId !== null && yards.some((y) => y.id === this._currentYardId) ? this._currentYardId : null;
    const next = stored ?? (yards.find((y) => y.is_home) ?? yards[0])?.id ?? null;
    if (next !== this._currentYardId) {
      this._currentYardId = next;
      storeYard(next);
    }
  }

  // -- the door ------------------------------------------------------------------

  private _signInDev() {
    const name = this._devName.trim();
    if (name === '') return;
    authCustody.signInDev(name, { role: this._devRole });
    void this._boot();
  }

  // -- events from the chrome -----------------------------------------------------

  private _onOpenApp = (e: Event) => {
    if (this._leaving) return;
    const { appId, path } = (e as CustomEvent<{ appId: string; path: string }>).detail;
    const next = openApp(this._state, appId, path);
    if (next === this._state) return;
    this._state = next;
    this._frameControls = [];
    this._frameFeed = null;
  };

  private _onTheme = () => {
    // The framed app announced it can act on Theme: it presses its own
    // control, and the shared theme store repaints the shell. Nothing framed,
    // or a frame that cannot act: the shell toggles its own.
    const frame = this._frameWindow();
    if (frame !== null && this._frameControls.includes('theme')) {
      announceChromeAction(frame, window.location.origin, 'theme');
      return;
    }
    toggleTheme();
  };

  private _onFeedback = () => {
    const frame = this._frameWindow();
    if (frame !== null && this._frameControls.includes('feedback')) {
      announceChromeAction(frame, window.location.origin, 'feedback');
    }
  };

  private _onYard = (e: Event) => {
    const { branchId } = (e as CustomEvent<{ branchId: string | null }>).detail;
    // null is "all yards"; any other id must be one the person was granted.
    if (branchId !== null && !this._yards.some((y) => y.id === branchId)) return;
    this._currentYardId = branchId;
    storeYard(branchId);
    announceYardChanged(this._frameWindow(), window.location.origin, branchId);
  };

  private _onFrameAction = (e: Event) => {
    const { id } = (e as CustomEvent<{ id: string }>).detail;
    announceFrameAction(this._frameWindow(), window.location.origin, id);
  };

  private _onSwitchRole = () => {
    void this._leave({ ask: true });
  };

  private _onSignOut = () => {
    void this._leave({ ask: true });
  };

  // -- messages from the frame ------------------------------------------------------

  private _onMessage = (event: MessageEvent) => {
    const frame = this._frameWindow();
    if (frame === null) return;
    const host = { origin: window.location.origin, contentWindow: frame };
    // The framed app ended its own walk; it already signed itself out.
    if (acceptSwitchRole(event, host)) {
      void this._leave({ ask: false });
      return;
    }
    const controls = acceptChromeControls(event, host);
    if (controls !== null) this._frameControls = controls;
    const feed = acceptChromeTitle(event, host);
    if (feed !== null) this._frameFeed = feed;
  };

  // -- sign out and switch role --------------------------------------------------------

  /**
   * The shared leave flow: the frame is asked to run its own sign out (unless
   * it already ended itself), then the host clears its own custody and the
   * door comes back. The frame's per person stores (theme, yard) are the
   * person's, not the session's, and stay.
   */
  private async _leave(options: { ask: boolean }): Promise<void> {
    if (this._leaving) return;
    this._leaving = true;
    let outcome: FrameSignOutOutcome | 'ended-by-app' = 'ended-by-app';
    if (options.ask) {
      this._notice = 'Signing the app out...';
      this.requestUpdate();
      await this.updateComplete;
      outcome = await requestFrameSignOut({
        frame: this._frameElement(),
        origin: window.location.origin,
        win: window,
      });
    }
    this._leaveOutcome = outcome;
    await signOutStaff();
    this._state = switchRole(this._state);
    this._frameControls = [];
    this._frameFeed = null;
    this._notice =
      outcome === 'timed-out' ? 'The app did not answer in time, so the shell signed you out anyway.' : '';
    this._leaving = false;
    this.requestUpdate();
  }

  // -- helpers -----------------------------------------------------------------------------------

  private _frameElement(): HTMLIFrameElement | null {
    return this.querySelector<HTMLIFrameElement>('iframe[data-testid="app-frame"]');
  }

  private _frameWindow(): Window | null {
    return this._frameElement()?.contentWindow ?? null;
  }

  // -- render ---------------------------------------------------------------------------------------

  render() {
    return html`
      <div
        class="h-screen overflow-hidden bg-deep-space text-foreground flex flex-col font-sans selection:bg-gable-green/30"
        data-testid="field-shell"
        data-phase=${this._state.phase}
        data-leave=${this._leaveOutcome}
      >
        ${this._state.phase === 'door' ? this._renderDoor() : this._renderField(this._state)}
      </div>
    `;
  }

  private _renderDoor(): TemplateResult {
    return html`
      <main class="flex-1 overflow-y-auto flex items-center justify-center p-6">
        <div class="w-full max-w-sm rounded-2xl border border-white/10 bg-slate-steel p-6 sm:p-8 shadow-elevation-2">
          <div class="flex items-baseline justify-center leading-none text-2xl mb-1">
            <span class="font-bold tracking-tight text-white">Gable</span>
            <span class="font-light tracking-widest text-gable-green ml-0.5">LBM</span>
          </div>
          <p class="text-center text-sm text-zinc-400 mb-6">The field shell</p>
          ${this._notice !== ''
            ? html`<p
                role="status"
                class="mb-4 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-400"
              >
                ${this._notice}
              </p>`
            : nothing}
          ${authConfig.devMode
            ? html`
                <label class="block text-xs font-medium uppercase tracking-wider text-zinc-500 mb-1.5" for="door-name"
                  >Your name</label
                >
                <input
                  id="door-name"
                  type="text"
                  autocomplete="off"
                  placeholder="A name for this device"
                  .value=${this._devName}
                  @input=${(e: Event) => {
                    this._devName = (e.target as HTMLInputElement).value;
                  }}
                  class="w-full min-h-[44px] mb-4 rounded-lg border border-white/10 bg-deep-space px-3 py-2.5 text-sm text-white placeholder:text-zinc-600 focus:outline-none focus:ring-1 focus:ring-gable-green/50"
                />
                <span
                  class="block text-xs font-medium uppercase tracking-wider text-zinc-500 mb-1.5"
                  id="door-role-label"
                >
                  Role
                </span>
                <div role="radiogroup" aria-labelledby="door-role-label" class="grid grid-cols-2 gap-2 mb-6">
                  ${DEV_ROLES.map((r) => {
                    const picked = r.id === this._devRole;
                    return html`
                      <button
                        type="button"
                        role="radio"
                        aria-checked=${picked ? 'true' : 'false'}
                        data-role=${r.id}
                        @click=${() => {
                          this._devRole = r.id;
                        }}
                        class=${`min-h-[44px] rounded-lg border px-3 py-2.5 text-sm transition-colors ${
                          picked
                            ? 'border-gable-green/50 bg-gable-green/10 text-gable-green font-semibold'
                            : 'border-white/10 bg-deep-space text-zinc-300 hover:border-white/20'
                        }`}
                      >
                        ${r.label}
                      </button>
                    `;
                  })}
                </div>
                <button
                  type="button"
                  data-testid="door-sign-in"
                  @click=${() => this._signInDev()}
                  class="w-full min-h-[44px] rounded-lg bg-gable-green px-4 py-2.5 text-sm font-semibold text-deep-space hover:bg-gable-green-hover transition-colors"
                >
                  Sign in
                </button>
              `
            : html`
                <p class="text-sm text-zinc-400 mb-6">Sign in at the front door, then open the field shell again.</p>
                <a
                  href="/"
                  class="flex items-center justify-center w-full min-h-[44px] rounded-lg bg-gable-green px-4 py-2.5 text-sm font-semibold text-deep-space hover:bg-gable-green-hover transition-colors"
                  >To the front door</a
                >
              `}
        </div>
      </main>
    `;
  }

  private _renderField(state: Extract<HostState, { phase: 'field' }>): TemplateResult {
    const current = state.apps.find((a) => a.id === state.currentAppId);
    return html`
      <gable-shell-header
        phone
        .dealerName=${this._dealer.name}
        .dealerMarkUrl=${this._dealer.markUrl}
        .apps=${state.apps as ShellApp[]}
        .currentAppId=${state.currentAppId ?? ''}
        .frameTitle=${this._frameFeed?.title ?? ''}
        .frameActions=${this._frameFeed?.actions ?? []}
        .feedbackOn=${this._frameControls.includes('feedback')}
        .theme=${this._theme}
        .yards=${this._yards as ShellYard[]}
        .currentYardId=${this._currentYardId}
        .userName=${state.session.name}
        .userRoleLabel=${state.session.roleLabel}
      ></gable-shell-header>
      <gable-shell-banner mode=${this._online ? '' : 'offline'}></gable-shell-banner>
      ${this._notice !== ''
        ? html`<p role="status" class="px-4 py-1.5 text-sm text-zinc-400 border-b border-white/5">${this._notice}</p>`
        : nothing}
      <main class="relative flex-1 min-h-0">
        ${current === undefined
          ? html`
              <div class="h-full flex items-center justify-center p-6">
                <div class="text-center max-w-xs">
                  <h2 class="text-xl font-semibold text-white mb-2">No field apps for this role</h2>
                  <p class="text-sm text-zinc-400 mb-4">
                    ${state.session.roleLabel} holds no apps in the field shell. Switch role to pick another.
                  </p>
                  <button
                    type="button"
                    data-action="switch-role-empty"
                    @click=${() => this._onSwitchRole()}
                    class="min-h-[44px] px-4 py-2.5 rounded-lg bg-gable-green text-sm font-semibold text-deep-space hover:bg-gable-green-hover transition-colors"
                  >
                    Switch role
                  </button>
                </div>
              </div>
            `
          : keyed(
              `${current.id}:${state.currentPath}`,
              html`<iframe
                data-testid="app-frame"
                title=${current.name}
                allow="camera"
                src=${framedAddress(state.currentPath)}
                class="block w-full h-full border-0 bg-deep-space"
              ></iframe>`,
            )}
      </main>
      <gable-shell-tabs
        .apps=${state.apps as ShellApp[]}
        .currentAppId=${state.currentAppId ?? ''}
      ></gable-shell-tabs>
    `;
  }
}
