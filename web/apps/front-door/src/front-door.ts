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
  Building2, ClipboardList, CreditCard, FileText, Hammer, ScrollText, LayoutDashboard,
  LayoutGrid, Package, Receipt, BookOpen, BarChart3, Settings, ShoppingBag,
  Store, Tags, Truck, Users, Globe, Monitor,
} from 'lucide';
import { icon, cn } from '@gable/design-system';
import { authCustody, authConfig, InvalidTokenError } from '@gable/auth';
import { loadAppsCatalog, tilesFor, type AppInfo, type DoorTile } from './apps-catalog.ts';
import { DEMO_PERSONAS, ROLE_WORDS, type DemoPersona } from './demo-personas.ts';

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

@customElement('gable-front-door')
export class GableFrontDoor extends LitElement {
  // Light DOM so the shared Tailwind tokens apply, exactly like the desk.
  createRenderRoot() { return this; }

  @state() private _state: DoorState = { kind: 'signed-out' };
  @state() private _userName: string | null = null;
  @state() private _roles: string[] = [];
  @state() private _devName = 'Local Developer';
  @state() private _token = '';
  @state() private _signInError: string | null = null;

  connectedCallback() {
    super.connectedCallback();
    // Whatever bundle landed here first may already hold a session (the
    // sessionStorage handoff): pick it up before choosing a state.
    const session = authCustody.session;
    if (session !== null) {
      this._userName = authCustody.displayName;
      this._roles = authCustody.roles;
      void this._loadCatalog();
    }
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
        this._signInError = 'Your session expired. Sign in again.';
        return;
      }
      this._state = { kind: 'error', message: err instanceof Error ? err.message : String(err) };
    }
  }

  private _signIn() {
    this._signInError = null;
    try {
      if (authConfig.devMode) {
        authCustody.signInDev(this._devName);
      } else {
        authCustody.signInWithToken(this._token);
      }
    } catch (err) {
      this._signInError = err instanceof Error ? err.message : String(err);
      if (err instanceof InvalidTokenError) {
        this._signInError = 'That token was refused: it is not a readable, unexpired JWT.';
      }
      return;
    }
    this._userName = authCustody.displayName;
    this._roles = authCustody.roles;
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
    try {
      authCustody.signInDev(persona.name, { role: persona.role, homeYardCode: persona.homeYardCode });
    } catch (err) {
      this._signInError = err instanceof Error ? err.message : String(err);
      return;
    }
    this._userName = authCustody.displayName;
    this._roles = authCustody.personaRole !== null ? [authCustody.personaRole] : [];
    void this._loadCatalog();
  }

  private _signOut() {
    authCustody.signOut();
    this._signInError = null;
    this._userName = null;
    this._roles = [];
    this._state = { kind: 'signed-out' };
  }

  /** A tile opens its app: a full navigation into the desk bundle. */
  private _open(tile: DoorTile) {
    window.location.assign(tile.path);
  }

  private get _headerWord(): string {
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
    const signedIn = this._state.kind !== 'signed-out';
    const personaRole = authCustody.personaRole;
    const headerRoleWords = personaRole !== null
      ? ROLE_WORDS[personaRole as keyof typeof ROLE_WORDS] ?? personaRole
      : null;
    return html`
      <header class="h-16 border-b border-white/5 bg-deep-space/80 backdrop-blur-xl px-4 md:px-6 grid grid-cols-[1fr_auto_1fr] items-center gap-4 sticky top-0 z-40">
        <div class="flex items-center gap-3 justify-self-start">
          <gable-brand-logo variant="mark" size="md" class-name="text-white drop-shadow-glow"></gable-brand-logo>
          <span class="hidden md:block"><gable-brand-logo variant="text" size="md"></gable-brand-logo></span>
        </div>
        <div class="text-center text-sm font-medium tracking-wide text-zinc-400 truncate">
          ${this._headerWord}
        </div>
        <div class="flex items-center gap-3 justify-self-end">
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
              @click=${() => this._signOut()}
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

  private _renderSignIn() {
    // The heading and the card width differ between dev (the wide card and
    // the tenant's name) and a production build (the base wrapper on
    // refactor/v1, the plain product name). Every dealer's real door carries
    // their own tenant name, so the production door must not render "Gable
    // Building Supply".
    const dev = authConfig.devMode;
    const headingText = dev ? 'Sign in to Gable Building Supply' : 'Sign in to Gable';
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
                : 'Paste the bearer token your identity provider issued. It is held for this tab only and cleared when you sign out.'}
            </p>
          </div>
          ${dev ? this._renderPersonaCards() : html`
            <label class="block space-y-1.5">
              <span class="text-xs uppercase tracking-wider text-muted-foreground">Bearer token</span>
              <textarea
                .value=${this._token}
                @input=${(e: Event) => { this._token = (e.target as HTMLTextAreaElement).value; }}
                rows="4"
                aria-label="Bearer token"
                placeholder="eyJhbGciOi…"
                class="w-full bg-slate-steel/50 border border-white/10 rounded-lg px-3 py-2 text-xs font-mono text-white focus:outline-none focus:ring-1 focus:ring-gable-green/50 focus:bg-slate-steel transition-all"
              ></textarea>
            </label>
            ${this._signInError ? html`
              <p role="alert" class="text-sm text-safety-red">${this._signInError}</p>
            ` : nothing}
            <button
              @click=${() => this._signIn()}
              class="w-full rounded-lg bg-gable-green/10 border border-gable-green/30 px-4 py-2.5 text-sm font-medium text-gable-green hover:bg-gable-green/20 hover:shadow-glow transition-all"
            >
              Sign in
            </button>
          `}
        </div>
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
            class="w-full bg-slate-steel/50 border border-white/10 rounded-lg px-3 py-2 text-sm text-white focus:outline-none focus:ring-1 focus:ring-gable-green/50 focus:bg-slate-steel transition-all"
          />
        </label>
        ${this._signInError ? html`
          <p role="alert" class="text-sm text-safety-red">${this._signInError}</p>
        ` : nothing}
        <button
          @click=${() => this._signIn()}
          class="w-full rounded-lg bg-gable-green/10 border border-gable-green/30 px-4 py-2.5 text-sm font-medium text-gable-green hover:bg-gable-green/20 hover:shadow-glow transition-all"
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
    if (session?.kind === 'token') {
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
