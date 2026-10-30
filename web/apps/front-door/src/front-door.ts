// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The front door: the first screen of the Gable stack, served at /.
 *
 * One element, three states, one header — the micro-app selector pattern:
 *
 *   signed-out  the sign-in card. In a dev build (the core runs
 *               AUTH_MODE=dev) it asks for a display name and holds no
 *               credential; in a production build it accepts the bearer
 *               token the identity provider issued (the out-of-band path
 *               the desk has always used), held in @gable/auth custody.
 *   loading     the catalog is being read.
 *   ready       the user is signed in and the catalog has answered: a home
 *               of tiles (the desk pinned first, then every enabled app
 *               from GET /api/v1/apps), each opening that app inside the
 *               desk bundle.
 *
 * The header carries every state: the brand lockup left, the word of the
 * moment centred, the user and Sign out right. Sign out clears custody and
 * returns to the sign-in card. Light DOM with Tailwind classes on the
 * desk's own tokens (Industrial Dark), so the door and the desk are one
 * product; the tiles use the desk home's own idiom.
 *
 * The door never fences anything: enablement and roles are the backend's
 * to enforce on every route; the tiles are the catalog's honest answer.
 */
import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import {
  Building2, ClipboardList, CreditCard, FileText, Fingerprint, Hammer, Mail, ScrollText, LayoutDashboard,
  LayoutGrid, Package, Receipt, BookOpen, BarChart3, Settings, ShoppingBag,
  Store, Tags, TriangleAlert, Truck, Users, Globe, Monitor,
} from 'lucide';
import { icon, cn, useThemeSubject, googleGlyph, microsoftGlyph } from '@gable/design-system';
import {
  authCustody, authConfig, InvalidTokenError, signInStaff, signOutStaff, restoreStaffSession,
  authMethodsConfig, resetAuthMethodsCache, recordIdentity, readIdentity, forgetIdentity,
  type AuthMethodsRuntimeConfig, type SignInMethodCode, type SignInMethodConfig, type TappedIdentity,
} from '@gable/auth';
import { loadAppsCatalog, tilesFor, type AppInfo, type DoorTile } from './apps-catalog.ts';
import { DEMO_PERSONAS, ROLE_WORDS, type DemoPersona } from './demo-personas.ts';
import {
  ROLE_GROUPS,
  SHELL_PROFILES,
  isConsoleLauncher,
  roleEntryFor,
  shellForRole,
  type LauncherRole,
  type RoleGroup,
} from './roles.ts';

type DoorState =
  | { kind: 'signed-out' }
  | { kind: 'loading' }
  | { kind: 'ready'; apps: AppInfo[] }
  | { kind: 'error'; message: string };

/** The door's icon per catalog key (the desk launcher's own set). */
export const TILE_ICONS: Record<string, Parameters<typeof icon>[0]> = {
  desk: Monitor,
  inventory: Package,
  location: Building2,
  quote: FileText,
  order: ClipboardList,
  pricing: Tags,
  invoice: Receipt,
  gl: BookOpen,
  purchase_order: ShoppingBag,
  vendor: Store,
  delivery: Truck,
  pos: CreditCard,
  dashboard: LayoutDashboard,
  reporting: BarChart3,
  customer: Users,
  portal: Globe,
  millwork: Hammer,
  governance: ScrollText,
  techadmin: Settings,
};

function initialsFor(name: string): string {
  const parts = name.trim().split(/[\s@.]+/).filter(Boolean);
  if (parts.length === 0) return '?';
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return (parts[0][0] + parts[1][0]).toUpperCase();
}

/** The error envelope's message off a refusal, when the body carries one. */
async function serverMessage(resp: Response): Promise<string | null> {
  const body = await resp.json().catch(() => null);
  if (body !== null && typeof body === 'object' && 'error' in body) {
    const err = (body as Record<string, unknown>)['error'];
    if (err !== null && typeof err === 'object' && 'message' in err) {
      const message = (err as Record<string, unknown>)['message'];
      if (typeof message === 'string' && message !== '') return message;
    }
  }
  return null;
}

@customElement('gable-front-door')
export class GableFrontDoor extends LitElement {
  // Light DOM so the shared Tailwind tokens apply, exactly like the desk.
  createRenderRoot() { return this; }

  /** Navigation seam so tests can observe shell routing without replacing window.location. */
  navigateTo: (path: string) => void = (path: string) => {
    window.location.assign(path);
  };

  @state() private _state: DoorState = { kind: 'signed-out' };
  @state() private _userName: string | null = null;
  @state() private _roles: string[] = [];
  @state() private _devName = 'Local Developer';
  @state() private _signInError: string | null = null;
  @state() private _errorSource: SignInMethodCode | 'continue-as' | 'dev' | 'session' | null = null;
  // The runtime auth methods config (S8): the dealer's allowed sign in
  // methods, discovered from the server, in the server's order. Null until
  // the answer arrives (or fails closed) in a production build.
  @state() private _authCfg: AuthMethodsRuntimeConfig | null = null;
  @state() private _email = '';
  @state() private _password = '';

  private _isConsoleMode(): boolean {
    return typeof window !== 'undefined' && isConsoleLauncher(window.location.search);
  }

  connectedCallback() {
    super.connectedCallback();
    // In ?console=1 launcher mode, the five-group role picker and Continue as
    // from identity-tap render directly and never fetch or hold a token.
    if (this._isConsoleMode()) {
      return;
    }
    // Whatever bundle landed here first may already hold a session (the
    // sessionStorage handoff): pick it up before choosing a state.
    const session = authCustody.session;
    if (session !== null) {
      this._userName = authCustody.displayName;
      this._roles = authCustody.roles;
      void this._loadCatalog();
    }
    // A production build discovers the dealer's allowed sign in methods at
    // boot; a dev build keeps its persona cards and never asks.
    if (!authConfig.devMode) {
      void this._loadAuthConfig();
      // A fresh tab holds no display data but may hold a live session
      // cookie: ask the core who it is.
      if (session === null) void this._restoreSession();
    }
  }

  private async _restoreSession() {
    if (!(await restoreStaffSession())) return;
    this._adoptStaffSession();
    void this._loadCatalog();
  }

  /** Pick up the display data custody now holds (name, roles, theme). */
  private _adoptStaffSession() {
    useThemeSubject(authCustody.subject);
    this._userName = authCustody.displayName;
    this._roles = authCustody.roles;
  }

  private async _loadAuthConfig() {
    this._authCfg = await authMethodsConfig(import.meta.env.VITE_API_URL || '');
  }

  private async _retryAuthConfig() {
    resetAuthMethodsCache();
    await this._loadAuthConfig();
  }

  /** One method's start attempt: the server's plain refusal shows inline. */
  private async _startMethod(
    method: SignInMethodConfig,
    source: SignInMethodCode | 'continue-as' = method.method,
  ) {
    this._signInError = null;
    this._errorSource = source;
    if (method.start_url === null) {
      this._signInError = 'This method cannot start here.';
      return;
    }
    try {
      const resp = await fetch(method.start_url, {
        method: 'POST',
        headers: { Accept: 'application/json' },
      });
      if (resp.ok) {
        // No method answers a redirect yet; when one does, this is where
        // the navigation happens.
        return;
      }
      this._signInError = await serverMessage(resp) ?? 'Sign in did not start.';
    } catch {
      this._signInError = 'The server could not be reached.';
    }
  }

  /**
   * The email and password form: the core signs the person in through
   * Appwrite and answers the session cookie; custody keeps display data.
   */
  private async _signInEmailPassword() {
    this._signInError = null;
    this._errorSource = 'email_password';
    if (this._email === '' || this._password === '') {
      this._signInError = 'Enter your email and password.';
      return;
    }
    const result = await signInStaff(this._email, this._password);
    this._password = '';
    if (!result.ok) {
      this._signInError = result.message;
      return;
    }
    this._adoptStaffSession();
    this._tapIdentity(null, 'email_password');
    void this._loadCatalog();
  }

  private async _loadCatalog() {
    this._state = { kind: 'loading' };
    try {
      const apps = await loadAppsCatalog(import.meta.env.VITE_API_URL || '');
      this._state = { kind: 'ready', apps };
    } catch (err) {
      // A 401 makes the fetch client drop custody's session; with none left a
      // retry could only 401 again, so go back to the sign in card.
      if (authCustody.session === null) {
        this._signOut();
        this._errorSource = 'session';
        this._signInError = 'Your session expired. Sign in again.';
        return;
      }
      this._state = { kind: 'error', message: err instanceof Error ? err.message : String(err) };
    }
  }

  /** The dev build's free name sign in (the core runs AUTH_MODE=dev). */
  private _signIn() {
    this._signInError = null;
    this._errorSource = 'dev';
    try {
      authCustody.signInDev(this._devName);
    } catch (err) {
      this._signInError = err instanceof Error ? err.message : String(err);
      if (err instanceof InvalidTokenError) {
        this._signInError = 'Enter a name to sign in.';
      }
      return;
    }
    useThemeSubject(authCustody.subject);
    this._userName = authCustody.displayName;
    this._roles = authCustody.roles;
    this._tapIdentity();
    void this._loadCatalog();
  }

  /**
   * Sign in via a persona card: pass the persona's role and home yard code
   * into custody, so the door filters tiles with the role and the desk opens
   * on the persona's yard.
   *
   * `signInDev` with a persona drops the previously stored yard: an earlier
   * persona's home would otherwise win on the next desk boot, because every
   * dev persona sees every yard in its grants list and a stored id is
   * restored before the persona is consulted. A reload within the same
   * persona keeps the stored choice: the desk re-inits on its own.
   */
  private _signInAsPersona(persona: DemoPersona) {
    this._signInError = null;
    this._errorSource = null;
    try {
      authCustody.signInDev(persona.name, { role: persona.role, homeYardCode: persona.homeYardCode });
    } catch (err) {
      this._signInError = err instanceof Error ? err.message : String(err);
      return;
    }
    useThemeSubject(authCustody.subject);
    this._userName = authCustody.displayName;
    this._roles = authCustody.personaRole !== null ? [authCustody.personaRole] : [];
    this._tapIdentity(persona.homeYardCode);
    void this._loadCatalog();
  }

  /** Remember the last signed in user's display block for the next visit. */
  private _tapIdentity(yard: string | null = null, method: SignInMethodCode | null = null) {
    const name = authCustody.displayName;
    if (name === null) return;
    const role = authCustody.personaRole ?? this._roles[0] ?? null;
    recordIdentity(localStorage, { displayName: name, role, yard: yard ?? null, method });
  }

  private _methodLabelFor(code: SignInMethodCode): string {
    const configured = this._authCfg?.methods.find((m) => m.method === code);
    if (configured !== undefined) return configured.label;
    switch (code) {
      case 'email_password': return 'Email and password';
      case 'passkey': return 'Passkey';
      case 'magic_link': return 'Email a sign in link';
      case 'google': return 'Google';
      case 'microsoft': return 'Microsoft';
      case 'oidc': return 'Single sign on';
    }
  }

  private _focusFirstAllowedMethod() {
    const first = this._authCfg?.methods[0];
    if (first === undefined) return;
    if (first.kind === 'form') {
      this.querySelector<HTMLInputElement>('input[aria-label="Email"]')?.focus();
      return;
    }
    this.querySelector<HTMLButtonElement>(`button[aria-label="Sign in with ${first.label}"]`)?.focus();
  }

  /**
   * Clicking Continue as <name> starts the remembered method when it is still
   * in the dealer's allowlist, or focuses the first allowed method when it is
   * no longer allowed.
   */
  private _continueAs(last: TappedIdentity) {
    const methods = this._authCfg?.methods ?? [];
    const allowed = last.method !== undefined && last.method !== null
      ? methods.find((m) => m.method === last.method)
      : undefined;
    if (allowed !== undefined) {
      if (allowed.kind === 'form') {
        if (this._email !== '' && this._password !== '') {
          void this._signInEmailPassword();
        } else {
          this.querySelector<HTMLInputElement>('input[aria-label="Email"]')?.focus();
        }
        return;
      }
      void this._startMethod(allowed, 'continue-as');
      return;
    }
    this._focusFirstAllowedMethod();
  }

  /** The Sign out button: a staff session is revoked at the core first. */
  private async _signOutClicked() {
    if (authCustody.session?.kind === 'staff') await signOutStaff();
    this._signOut();
  }

  private _signOut() {
    authCustody.signOut();
    forgetIdentity(localStorage);
    useThemeSubject(null);
    this._signInError = null;
    this._errorSource = null;
    this._userName = null;
    this._roles = [];
    this._state = { kind: 'signed-out' };
  }

  /** A tile opens its app: a full navigation into the desk bundle. */
  private _open(tile: DoorTile) {
    this.navigateTo(tile.path);
  }

  /**
   * Launch a role from the ?console=1 five-group role picker: record the
   * display block in identity-tap (never a token) and open the role's target
   * shell (/shell/, /field/, or /portal).
   */
  private _launchConsoleRole(entry: LauncherRole) {
    recordIdentity(localStorage, {
      displayName: entry.displayName,
      role: entry.role,
      yard: entry.homeYardCode,
      method: null,
    });
    useThemeSubject(entry.subject);
    this.navigateTo(entry.shell.path);
  }

  /**
   * Continue as the remembered identity from identity-tap in ?console=1 mode:
   * open the target shell for the remembered role without storing or showing a token.
   */
  private _continueInConsole(last: TappedIdentity) {
    const targetShell = (last.role !== null ? shellForRole(last.role) : null) ?? SHELL_PROFILES.work;
    recordIdentity(localStorage, {
      displayName: last.displayName,
      role: last.role,
      yard: last.yard,
      method: last.method ?? null,
    });
    this.navigateTo(targetShell.path);
  }

  private get _headerWord(): string {
    if (this._isConsoleMode()) return 'Pick a role';
    if (this._state.kind === 'signed-out') return '';
    if (this._state.kind === 'loading') return 'Opening the door…';
    if (this._state.kind === 'error') return 'The catalog did not answer';
    return 'Pick an app';
  }

  render() {
    return html`
      <div class="min-h-screen bg-deep-space text-white flex flex-col">
        ${this._renderHeader()}
        <main class="flex-1 w-full max-w-[1600px] mx-auto px-6 md:px-8 py-8">
          ${this._renderBody()}
        </main>
        <footer class="px-6 md:px-8 py-4 text-center text-xs text-muted-foreground border-t border-white/5">
          Powered by Gable
        </footer>
      </div>
    `;
  }

  private _renderHeader() {
    const signedIn = !this._isConsoleMode() && this._state.kind !== 'signed-out';
    const personaRole = authCustody.personaRole;
    const headerRoleWords = personaRole !== null
      ? ROLE_WORDS[personaRole as keyof typeof ROLE_WORDS] ?? personaRole
      : null;
    const dealerName = !authConfig.devMode ? (this._authCfg?.dealerName ?? null) : null;
    const dealerMarkUrl = !authConfig.devMode ? (this._authCfg?.dealerMarkUrl ?? null) : null;
    const gridCols = signedIn
      ? 'grid-cols-[minmax(0,1fr)_auto_auto] sm:grid-cols-[1fr_auto_1fr]'
      : 'grid-cols-[1fr_auto_1fr]';
    return html`
      <header class="h-16 border-b border-white/5 bg-deep-space/80 backdrop-blur-xl px-4 md:px-6 grid ${gridCols} items-center gap-4 sticky top-0 z-40">
        ${dealerName !== null ? html`
          <div class="flex items-center gap-2.5 min-w-0 shrink-0 justify-self-start">
            ${dealerMarkUrl !== null
              ? html`<img
                  src=${dealerMarkUrl}
                  alt=""
                  class="h-8 w-8 rounded-lg object-contain bg-white/5 border border-white/5 shrink-0"
                />`
              : html`<span
                  aria-hidden="true"
                  class="h-8 w-8 rounded-lg bg-gable-green/10 border border-gable-green/30 flex items-center justify-center text-[11px] font-mono font-bold text-gable-green shrink-0"
                >${initialsFor(dealerName)}</span>`}
            <span class="text-sm font-semibold text-white max-w-[200px] sm:max-w-[240px] truncate">${dealerName}</span>
          </div>
        ` : html`
          <div class="flex items-center gap-3 justify-self-start">
            <gable-brand-logo variant="mark" size="md" class-name="text-white drop-shadow-glow"></gable-brand-logo>
            <span class="hidden md:block"><gable-brand-logo variant="text" size="md"></gable-brand-logo></span>
          </div>
        `}
        <div class="text-center text-sm font-medium tracking-wide text-zinc-400 truncate">
          ${this._headerWord}
        </div>
        <div class="flex items-center gap-3 justify-self-end">
          <gable-theme-toggle></gable-theme-toggle>
          ${signedIn ? html`
            ${this._userName && this._userName !== '' ? html`
              <div class="hidden sm:flex items-center gap-2">
                <div class="h-9 w-9 rounded-full bg-gradient-to-br from-gable-green/20 to-emerald-500/20 border border-gable-green/30 flex items-center justify-center text-xs font-mono font-bold text-gable-green shadow-glow">
                  ${initialsFor(this._userName)}
                </div>
                <div class="hidden lg:block leading-tight">
                  <div class="text-sm font-medium text-white">${this._userName}</div>
                  ${headerRoleWords !== null
                    ? html`<div class="text-[10px] uppercase tracking-wider text-muted-foreground font-mono">${headerRoleWords}</div>`
                    : this._roles.length > 0
                      ? html`<div class="text-[10px] uppercase tracking-wider text-muted-foreground font-mono">${this._roles.join(' · ')}</div>`
                      : this._state.kind === 'ready' || this._state.kind === 'loading' || this._state.kind === 'error'
                        ? html`<div class="text-[10px] uppercase tracking-wider text-muted-foreground font-mono">local development</div>`
                        : nothing}
                </div>
              </div>
            ` : nothing}
            <button
              @click=${() => void this._signOutClicked()}
              class="rounded-lg border border-white/10 bg-white/5 px-3 py-1.5 text-sm text-zinc-300 hover:text-white hover:border-gable-green/40 transition-colors"
            >
              Sign out
            </button>
          ` : nothing}
        </div>
      </header>
    `;
  }

  private _renderBody() {
    if (this._isConsoleMode()) {
      return this._renderConsoleLauncher();
    }
    switch (this._state.kind) {
      case 'signed-out':
        return this._renderSignIn();
      case 'loading':
        return html`
          <div class="flex items-center justify-center py-32">
            <div class="flex flex-col items-center gap-3">
              <div class="h-8 w-8 animate-spin rounded-full border-2 border-gable-green border-t-transparent"></div>
              <span class="text-sm text-muted-foreground font-medium tracking-wide">Loading apps…</span>
            </div>
          </div>
        `;
      case 'error':
        return html`
          <div class="flex items-center justify-center py-32">
            <div class="glass-card rounded-xl p-8 max-w-md text-center space-y-3">
              <h2 class="text-lg font-semibold text-white">The apps catalog did not answer</h2>
              <p class="text-sm text-zinc-400 font-mono">${this._state.message}</p>
              <button
                @click=${() => void this._loadCatalog()}
                class="rounded-lg bg-gable-green/10 border border-gable-green/30 px-4 py-2 text-sm text-gable-green hover:bg-gable-green/20 transition-colors"
              >
                Try again
              </button>
            </div>
          </div>
        `;
      case 'ready':
        return this._renderTiles(this._state.apps);
    }
  }

  /**
   * The ?console=1 front door launcher: "Continue as" from identity-tap and
   * the five-group role picker, never storing or showing a bearer token.
   */
  private _renderConsoleLauncher() {
    const last = readIdentity(localStorage);
    const lastEntry = last?.role !== undefined && last.role !== null ? roleEntryFor(last.role) : null;
    const lastShell = (last?.role !== undefined && last.role !== null ? shellForRole(last.role) : null) ?? SHELL_PROFILES.work;
    const lastRoleWords = lastEntry?.label ?? (last?.role ? (ROLE_WORDS[last.role as keyof typeof ROLE_WORDS] ?? last.role) : 'Staff');
    return html`
      <div class="max-w-6xl mx-auto space-y-6 animate-fade-in">
        <div class="glass-card rounded-2xl p-6 sm:p-8 space-y-5">
          <div class="space-y-1.5">
            <div class="text-xs font-mono uppercase tracking-wider text-gable-green font-semibold">
              Gable Shell Launcher
            </div>
            <h1 class="text-2xl font-semibold text-white">Pick a role to open its shell</h1>
            <p class="text-sm text-zinc-300 max-w-3xl">
              Each role opens the workspace frame built for its work: Gable Work for the office and counter, Gable Field for the yard and delivery runs, and Gable Pro for contractor accounts.
            </p>
          </div>
          ${last ? html`
            <div class="pt-1">
              <button
                type="button"
                data-testid="console-continue-as"
                @click=${() => this._continueInConsole(last)}
                aria-label="Continue as ${last.displayName}, ${lastRoleWords}, open ${lastShell.productName}"
                class="w-full min-h-[44px] rounded-xl border border-zinc-500 bg-surface-2 px-4 py-3.5 flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3 text-left hover:border-gable-green hover:bg-surface-3 focus:outline-none focus-visible:ring-2 focus-visible:ring-gable-green transition-all"
              >
                <div class="flex items-center gap-3 min-w-0">
                  <div class="h-10 w-10 rounded-full bg-gable-green/15 border border-gable-green flex items-center justify-center text-xs font-mono font-bold text-gable-green shrink-0">
                    ${initialsFor(last.displayName)}
                  </div>
                  <div class="min-w-0 leading-snug">
                    <div class="text-sm font-semibold text-white truncate">Continue as ${last.displayName}</div>
                    <div class="text-xs text-zinc-300 flex flex-wrap items-center gap-x-2 gap-y-0.5">
                      <span>${lastRoleWords}</span>
                      ${last.yard ? html`<span class="font-mono text-zinc-300">${last.yard}</span>` : nothing}
                    </div>
                  </div>
                </div>
                <div class="flex items-center justify-between sm:justify-end gap-2.5 shrink-0">
                  <span class="rounded-md border border-gable-green bg-gable-green/10 px-2.5 py-1 text-xs font-semibold text-gable-green">
                    ${lastShell.productName}
                  </span>
                  <span class="text-xs font-mono text-zinc-300">${lastShell.path}</span>
                </div>
              </button>
            </div>
          ` : nothing}
        </div>
        <div class="space-y-6">
          ${ROLE_GROUPS.map((group) => this._renderConsoleRoleGroup(group))}
        </div>
      </div>
    `;
  }

  private _renderConsoleRoleGroup(group: RoleGroup) {
    return html`
      <section
        data-testid="role-group-${group.id}"
        aria-labelledby="group-heading-${group.id}"
        class="glass-card rounded-2xl p-6 space-y-4"
      >
        <div class="space-y-1">
          <h2 id="group-heading-${group.id}" class="text-base font-semibold text-white">${group.title}</h2>
          <p class="text-xs sm:text-sm text-zinc-300">${group.summary}</p>
        </div>
        <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
          ${group.roles.map((entry) => this._renderConsoleRoleCard(entry))}
        </div>
      </section>
    `;
  }

  private _renderConsoleRoleCard(entry: LauncherRole) {
    return html`
      <button
        type="button"
        data-testid="role-card-${entry.role}"
        data-role=${entry.role}
        data-shell=${entry.shell.startApp}
        data-shell-path=${entry.shell.path}
        @click=${() => this._launchConsoleRole(entry)}
        aria-label="Open ${entry.shell.productName} as ${entry.label} (${entry.displayName})"
        class="group w-full min-h-[44px] text-left rounded-xl bg-surface-2 border border-zinc-500 p-4 flex flex-col justify-between gap-3.5 transition-all hover:border-gable-green hover:bg-surface-3 focus:outline-none focus-visible:ring-2 focus-visible:ring-gable-green"
      >
        <div class="space-y-2.5">
          <div class="flex items-start justify-between gap-3">
            <div class="flex items-center gap-3 min-w-0">
              <div class="h-10 w-10 rounded-lg bg-gable-green/15 border border-gable-green flex items-center justify-center text-gable-green shrink-0">
                ${icon(entry.icon, 20)}
              </div>
              <div class="min-w-0">
                <div class="text-base font-semibold text-white leading-snug">${entry.label}</div>
                <div class="text-xs text-zinc-300 leading-snug">${entry.displayName}</div>
              </div>
            </div>
            <span class="rounded-md border border-gable-green bg-gable-green/10 px-2.5 py-1 text-xs font-semibold text-gable-green shrink-0">
              ${entry.shell.productName}
            </span>
          </div>
          <p class="text-xs sm:text-sm text-zinc-300 leading-relaxed">${entry.line}</p>
          <div class="flex flex-wrap gap-1.5" aria-hidden="true">
            ${entry.surfaces.map(
              (surface) => html`
                <span class="rounded border border-zinc-500 bg-surface-3 px-2 py-0.5 text-[11px] font-medium text-zinc-200">
                  ${surface}
                </span>
              `,
            )}
          </div>
        </div>
        <div class="pt-2.5 border-t border-zinc-500/50 flex items-center justify-between gap-3 text-xs">
          <span class="font-medium text-gable-green">Open ${entry.shell.productName}</span>
          <span class="font-mono text-zinc-300">${entry.homeYardCode} · ${entry.shell.path}</span>
        </div>
      </button>
    `;
  }

  private _renderSignIn() {
    // The heading and the card width differ between dev (the wide card and
    // the demo tenant's name) and a production build, which leads with the
    // dealer's own name from /api/v1/auth/config when one is configured and
    // falls back to Gable's own lockup when none is set.
    const dev = authConfig.devMode;
    const dealerName = !dev ? (this._authCfg?.dealerName ?? null) : null;
    const headingText = dev
      ? 'Sign in to Gable Building Supply'
      : dealerName !== null
        ? `Sign in to ${dealerName}`
        : 'Sign in to Gable';
    const outerClasses = dev
      ? 'flex items-start justify-center py-12'
      : 'flex items-center justify-center py-16';
    const innerClasses = dev
      ? 'glass-card rounded-2xl p-8 w-full max-w-4xl space-y-6 animate-fade-in'
      : 'glass-card rounded-2xl p-8 w-full max-w-md space-y-5 animate-fade-in';
    return html`
      <div class="${outerClasses}">
        <div class="${innerClasses}">
          <div class="space-y-1">
            <h1 class="text-xl font-semibold text-white">${headingText}</h1>
            <p class="text-sm text-zinc-400">
              ${dev
                ? 'This is the local demo. Each card signs in as that person, shaping what the screens offer.'
                : this._signInSubtitle()}
            </p>
          </div>
          ${dev ? this._renderPersonaCards() : this._renderMethodsSignIn()}
        </div>
      </div>
    `;
  }

  /**
   * The production sign in's word: the form leads ("your work account");
   * with only redirect style methods on the card, the choice leads.
   */
  private _signInSubtitle(): string {
    const cfg = this._authCfg;
    if (cfg !== null && !cfg.degraded && !cfg.methods.some((m) => m.kind === 'form')) {
      return 'Choose how you sign in.';
    }
    return 'Sign in with your work account.';
  }

  /**
   * The production sign in: the dealer's allowed methods, in the server's
   * order, discovered at runtime. The bearer token paste stays as the out of
   * band path it always was, under its own disclosure.
   */
  private _renderMethodsSignIn() {
    if (this._authCfg === null) {
      return html`
        <div class="flex items-center justify-center gap-3 py-8" role="status">
          <div class="h-6 w-6 animate-spin rounded-full border-2 border-gable-green border-t-transparent"></div>
          <span class="text-sm text-muted-foreground font-medium tracking-wide">Reading the sign in methods…</span>
        </div>
      `;
    }
    const last = readIdentity(localStorage);
    const lastMethodLabel = last?.method !== undefined && last.method !== null
      ? this._methodLabelFor(last.method)
      : null;
    return html`
      ${this._authCfg.degraded && this._authCfg.notice ? html`
        <div
          role="alert"
          class="rounded-lg border border-amber-300/40 bg-amber-300/10 px-3 py-2.5 flex items-start justify-between gap-2.5 text-sm text-amber-300"
        >
          <div class="flex items-start gap-2 min-w-0">
            <span class="shrink-0 mt-0.5">${icon(TriangleAlert, 16)}</span>
            <span>${this._authCfg.notice}</span>
          </div>
          <button
            type="button"
            @click=${() => void this._retryAuthConfig()}
            class="shrink-0 rounded border border-amber-300/50 px-2.5 py-1 text-xs font-medium text-amber-300 hover:bg-amber-300/15 focus:outline-none focus-visible:ring-2 focus-visible:ring-amber-300 transition-colors"
          >
            Retry
          </button>
        </div>
      ` : nothing}
      ${this._signInError && this._errorSource === 'session' ? html`
        <p role="alert" class="text-sm text-safety-red">${this._signInError}</p>
      ` : nothing}
      ${last ? html`
        <div class="space-y-1.5">
          <button
            type="button"
            @click=${() => this._continueAs(last)}
            aria-label="Continue as ${last.displayName}${lastMethodLabel ? ` with ${lastMethodLabel}` : ''}"
            class="w-full rounded-lg border border-zinc-500 bg-surface-2 px-4 py-3 sm:py-2.5 min-h-[44px] sm:min-h-0 flex items-center gap-3 text-left hover:border-gable-green hover:bg-surface-3 focus:outline-none focus-visible:ring-2 focus-visible:ring-gable-green transition-all"
          >
            <div class="h-8 w-8 rounded-full bg-gradient-to-br from-gable-green/20 to-emerald-500/20 border border-gable-green flex items-center justify-center text-[11px] font-mono font-bold text-gable-green shrink-0">
              ${initialsFor(last.displayName)}
            </div>
            <div class="min-w-0 leading-tight">
              <div class="text-sm font-medium text-white truncate">Continue as ${last.displayName}</div>
              ${lastMethodLabel ? html`
                <div class="text-xs text-muted-foreground truncate">with ${lastMethodLabel}</div>
              ` : nothing}
            </div>
          </button>
          ${this._signInError && this._errorSource === 'continue-as' ? html`
            <p role="alert" class="text-sm text-safety-red">${this._signInError}</p>
          ` : nothing}
        </div>
      ` : nothing}
      <div class="space-y-2.5">
        ${this._renderMethodList(this._authCfg.methods)}
      </div>
    `;
  }

  /**
   * The methods in the server's order, with a quiet "or" divider between the
   * form and the other kinds; no divider when only one kind shows.
   */
  private _renderMethodList(methods: readonly SignInMethodConfig[]) {
    const out: unknown[] = [];
    for (let i = 0; i < methods.length; i++) {
      if (i > 0 && (methods[i - 1].kind === 'form') !== (methods[i].kind === 'form')) {
        out.push(html`
          <div class="flex items-center gap-3 py-1" role="presentation">
            <span class="h-px flex-1 bg-white/15"></span>
            <span class="text-xs uppercase tracking-wider text-muted-foreground">or</span>
            <span class="h-px flex-1 bg-white/15"></span>
          </div>
        `);
      }
      out.push(this._renderMethod(methods[i]));
    }
    return out;
  }

  /** One allowed method's leading mark, in the icon style the door already uses. */
  private _methodIcon(m: SignInMethodConfig) {
    switch (m.method) {
      case 'passkey': return icon(Fingerprint, 18);
      case 'magic_link': return icon(Mail, 18);
      case 'google': return googleGlyph(18);
      case 'microsoft': return microsoftGlyph(18);
      case 'oidc': return icon(Building2, 18);
      default: return nothing;
    }
  }

  /** One allowed method: the form, or the button that starts it. */
  private _renderMethod(m: SignInMethodConfig) {
    if (m.kind === 'form') {
      return html`
        <div class="space-y-2.5">
          <label class="block space-y-1.5">
            <span class="text-xs uppercase tracking-wider text-muted-foreground">Email</span>
            <input
              type="email"
              .value=${this._email}
              @input=${(e: Event) => { this._email = (e.target as HTMLInputElement).value; }}
              @keydown=${(e: KeyboardEvent) => { if (e.key === 'Enter') void this._signInEmailPassword(); }}
              aria-label="Email"
              autocomplete="username"
              class="w-full min-h-[44px] sm:min-h-0 bg-surface-2 border border-zinc-500 rounded-lg px-3 py-2.5 sm:py-2 text-base sm:text-sm text-white focus:outline-none focus:ring-1 focus:ring-gable-green focus:border-gable-green transition-all"
            />
          </label>
          <label class="block space-y-1.5">
            <span class="text-xs uppercase tracking-wider text-muted-foreground">Password</span>
            <input
              type="password"
              .value=${this._password}
              @input=${(e: Event) => { this._password = (e.target as HTMLInputElement).value; }}
              @keydown=${(e: KeyboardEvent) => { if (e.key === 'Enter') void this._signInEmailPassword(); }}
              aria-label="Password"
              autocomplete="current-password"
              class="w-full min-h-[44px] sm:min-h-0 bg-surface-2 border border-zinc-500 rounded-lg px-3 py-2.5 sm:py-2 text-base sm:text-sm text-white focus:outline-none focus:ring-1 focus:ring-gable-green focus:border-gable-green transition-all"
            />
          </label>
          <button
            type="button"
            @click=${() => void this._signInEmailPassword()}
            class="w-full min-h-[44px] sm:min-h-0 rounded-lg bg-gable-green/10 border border-gable-green px-4 py-3 sm:py-2.5 text-sm font-medium text-gable-green hover:bg-gable-green/20 hover:shadow-glow [[data-theme=light]_&]:bg-gable-green [[data-theme=light]_&]:text-black [[data-theme=light]_&]:hover:bg-gable-green-hover focus:outline-none focus-visible:ring-2 focus-visible:ring-gable-green transition-all"
          >
            Sign in
          </button>
          ${this._signInError && this._errorSource === 'email_password' ? html`
            <p role="alert" class="text-sm text-safety-red">${this._signInError}</p>
          ` : nothing}
        </div>
      `;
    }
    return html`
      <div class="space-y-1.5">
        <button
          type="button"
          @click=${() => void this._startMethod(m)}
          aria-label="Sign in with ${m.label}"
          class="w-full min-h-[44px] sm:min-h-0 flex items-center justify-center rounded-lg border border-zinc-500 bg-surface-2 px-4 py-3 sm:py-2.5 text-sm font-medium text-white hover:border-gable-green hover:bg-surface-3 focus:outline-none focus-visible:ring-2 focus-visible:ring-gable-green transition-all"
        >
          <span class="inline-flex items-center gap-2.5 max-w-full">
            <span class="shrink-0 text-zinc-300">${this._methodIcon(m)}</span>
            <span class="min-w-0 text-center leading-snug">${m.label}</span>
          </span>
        </button>
        ${this._signInError && this._errorSource === m.method ? html`
          <p role="alert" class="text-sm text-safety-red">${this._signInError}</p>
        ` : nothing}
      </div>
    `;
  }

  /** The persona card grid for the dev sign in (one card per persona). */
  private _renderPersonaCards() {
    return html`
      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3" role="list">
        ${DEMO_PERSONAS.map((persona) => this._renderPersonaCard(persona))}
      </div>
      <div class="border-t border-white/10 pt-5 space-y-3">
        <label class="block space-y-1.5">
          <span class="text-xs uppercase tracking-wider text-muted-foreground">Sign in with another name</span>
          <input
            .value=${this._devName}
            @input=${(e: Event) => { this._devName = (e.target as HTMLInputElement).value; }}
            @keydown=${(e: KeyboardEvent) => { if (e.key === 'Enter') this._signIn(); }}
            aria-label="Display name"
            class="w-full min-h-[44px] sm:min-h-0 bg-slate-steel/50 border border-white/10 rounded-lg px-3 py-2.5 sm:py-2 text-base sm:text-sm text-white focus:outline-none focus:ring-1 focus:ring-gable-green/50 focus:bg-slate-steel transition-all"
          />
        </label>
        ${this._signInError ? html`
          <p role="alert" class="text-sm text-safety-red">${this._signInError}</p>
        ` : nothing}
        <button
          @click=${() => this._signIn()}
          class="w-full min-h-[44px] sm:min-h-0 rounded-lg bg-gable-green/10 border border-gable-green px-4 py-3 sm:py-2.5 text-sm font-medium text-gable-green hover:bg-gable-green/20 hover:shadow-glow [[data-theme=light]_&]:bg-gable-green [[data-theme=light]_&]:text-black [[data-theme=light]_&]:hover:bg-gable-green-hover focus:outline-none focus-visible:ring-2 focus-visible:ring-gable-green transition-all"
        >
          Sign in
        </button>
      </div>
    `;
  }

  /** One persona card: the name, the role in words, the home yard name; the yard code in mono. */
  private _renderPersonaCard(persona: DemoPersona) {
    const roleWords = ROLE_WORDS[persona.role];
    return html`
      <div role="listitem">
        <button
          type="button"
          @click=${() => this._signInAsPersona(persona)}
          aria-label="Sign in as ${persona.name}, ${roleWords}, at ${persona.homeYardName}"
          class="group w-full text-left rounded-xl bg-slate-steel border border-white/5 p-4 transition-all hover:border-gable-green/40 hover:shadow-glow hover:-translate-y-0.5 focus:outline-none focus-visible:ring-2 focus-visible:ring-gable-green focus-visible:ring-offset-2 focus-visible:ring-offset-deep-space"
        >
          <div class="flex items-center gap-3">
            <div class="h-10 w-10 rounded-full bg-gradient-to-br from-gable-green/20 to-emerald-500/20 border border-gable-green/30 flex items-center justify-center text-xs font-mono font-bold text-gable-green shadow-glow shrink-0">
              ${initialsFor(persona.name)}
            </div>
            <div class="min-w-0">
              <div class="text-sm font-medium text-white truncate">${persona.name}</div>
              <div class="text-[11px] uppercase tracking-wider text-muted-foreground">${roleWords}</div>
            </div>
          </div>
          <div class="mt-3 pt-3 border-t border-white/5 flex items-center justify-between gap-3">
            <span class="text-xs text-zinc-400 truncate">${persona.homeYardName}</span>
            <span class="text-[10px] font-mono text-muted-foreground tracking-wider shrink-0">${persona.homeYardCode}</span>
          </div>
        </button>
      </div>
    `;
  }

  private _renderTiles(apps: AppInfo[]) {
    // Tile filter: token session -> its roles; dev session with a role -> the
    // persona's role (the cashier sees the counter, not purchasing); dev
    // session without a persona -> null, i.e. every enabled tile (the core
    // passes dev callers through every guard).
    const session = authCustody.session;
    let rolesForTiles: string[] | null;
    if (session?.kind === 'token' || session?.kind === 'staff') {
      rolesForTiles = this._roles;
    } else if (session?.kind === 'dev' && authCustody.personaRole !== null) {
      rolesForTiles = [authCustody.personaRole];
    } else {
      rolesForTiles = null;
    }
    const tiles = tilesFor(apps, rolesForTiles);
    return html`
      <div class="space-y-4 mb-8">
        <h1 class="text-2xl font-semibold text-white">Good to see you${this._userName ? html`, <span class="text-gable-green">${this._userName}</span>` : nothing}</h1>
        <p class="text-sm text-zinc-400">The enabled apps your roles admit. A tile opens the app inside the desk.</p>
      </div>
      <div class="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 xl:grid-cols-6 gap-4">
        ${tiles.map((tile) => this._renderTile(tile))}
      </div>
    `;
  }

  private _renderTile(tile: DoorTile) {
    const iconData = TILE_ICONS[tile.key] ?? LayoutGrid;
    return html`
      <button
        @click=${() => this._open(tile)}
        aria-label="Open ${tile.name}"
        class="group flex flex-col items-center gap-3 rounded-xl bg-slate-steel border border-white/5 p-6 transition-all hover:border-gable-green/40 hover:shadow-glow hover:-translate-y-0.5"
      >
        <div class="h-14 w-14 rounded-2xl bg-gable-green/10 text-gable-green flex items-center justify-center group-hover:bg-gable-green/20 transition-colors">
          ${icon(iconData, 28)}
        </div>
        <div class="text-sm font-medium text-white text-center leading-tight">${tile.name}</div>
        <div class="${cn('text-[10px] uppercase tracking-wider text-muted-foreground text-center')}">${tile.category}</div>
      </button>
    `;
  }
}
