// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Session custody for Gable's frontends.
 *
 * THE RULE (ADR 0012 section 3): a staff session is the HttpOnly cookie the
 * core sets at sign in; no script here ever holds, reads or writes a
 * credential for it. What this module keeps for a staff session is display
 * data only (the subject, the roles and the name the person typed), held in
 * memory and never written to any storage: each document boot asks the core
 * who the cookie belongs to (`restoreStaffSession`). A staff record found in
 * sessionStorage (an older build wrote one) is dropped on adoption. The
 * cookie decides every request, and the core answers 401 when the session is
 * gone.
 *
 * A bearer token (the out of band JWKS path, retired in ADR 0012 slice 8) is
 * held in memory only and is never written to any storage.
 *
 * The record is validated field by field on adoption and dropped outright
 * when it does not parse: a hostile or stale payload buys nothing.
 *
 * Dev mode (AUTH_MODE=dev on the core) has no credential to hold at all;
 * the door's dev sign-in stores a display name in the handoff record (the
 * only kind it carries), and only when the build was made with
 * VITE_AUTH_DEV_MODE.
 */

import { authConfig } from './config.ts';
import { BRANCH_STORAGE_KEY } from './branch.ts';
import { decodeTokenClaims, isExpired, type GableTokenClaims } from './jwt.ts';

/** The storage operations custody needs, so tests can inject doubles. */
export interface StorageLike {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

/** sessionStorage behind a throw probe: a hostile context degrades to memory. */
function probeSessionStorage(): StorageLike {
  const probeKey = 'gable.auth.storage-probe';
  try {
    sessionStorage.setItem(probeKey, '1');
    sessionStorage.removeItem(probeKey);
    return sessionStorage;
  } catch {
    const mem = new Map<string, string>();
    return {
      getItem: (k) => (mem.has(k) ? (mem.get(k) as string) : null),
      setItem: (k, v) => void mem.set(k, v),
      removeItem: (k) => void mem.delete(k),
    };
  }
}

/** The dev handoff key: the door writes it at a dev sign-in, every bundle adopts it at boot. */
export const SESSION_HANDOFF_KEY = 'gable.auth.session.v1';

/** A signed-in session, in the shape the frontends render. */
export type AuthSession =
  | { kind: 'token'; token: string; claims: GableTokenClaims }
  | { kind: 'staff'; sub: string; roles: string[]; name: string | null }
  | { kind: 'dev'; name: string; role?: string; homeYardCode?: string };

/** The handoff record's on-the-wire shape. The v tags the payload shape. */
interface HandoffRecord {
  v: 1;
  kind: 'dev';
  name?: string;
  role?: string;
  homeYardCode?: string;
}

/**
 * The persona a dev card sets: the role the door uses to filter tiles and
 * the home yard code the desk opens on. Both are optional, so the existing
 * dev sign-in (a free display name only) keeps working unchanged.
 */
export interface DevPersona {
  role?: string;
  homeYardCode?: string;
}

export class InvalidTokenError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'InvalidTokenError';
  }
}

export class DevModeDisabledError extends Error {
  constructor() {
    super('Dev sign-in is not available in this build');
    this.name = 'DevModeDisabledError';
  }
}

/** The counter's remembered-sale keys (the desk's POS terminal writes them). */
const COUNTER_SALE_KEY_PREFIX = 'gable.pos.sale.'

/** The desk's open apps, its tab strip (the desk's `lib/workspace.ts` writes it). */
const DESK_WORKSPACE_KEY = 'gable_workspace'

export class AuthCustody {
  private _session: AuthSession | null = null;
  private _adopted = false;
  private readonly _storage: StorageLike;

  constructor(storage: StorageLike = probeSessionStorage()) {
    this._storage = storage;
  }

  /** Adopt the handoff record once, on first use. Invalid records are dropped. */
  private _adoptHandoff(): void {
    if (this._adopted) return;
    this._adopted = true;

    const raw = this._storage.getItem(SESSION_HANDOFF_KEY);
    if (raw === null) return;

    const session = this._validateHandoff(raw);
    if (session === null) {
      this._storage.removeItem(SESSION_HANDOFF_KEY);
      return;
    }
    this._session = session;
  }

  private _validateHandoff(raw: string): AuthSession | null {
    let parsed: unknown;
    try {
      parsed = JSON.parse(raw);
    } catch {
      return null;
    }
    if (typeof parsed !== 'object' || parsed === null) return null;
    const rec = parsed as HandoffRecord;
    if (rec.v !== 1) return null;

    if (rec.kind === 'dev') {
      if (!authConfig.devMode) return null;
      if (typeof rec.name !== 'string' || rec.name.length === 0) return null;
      const role = typeof rec.role === 'string' && rec.role.length > 0 ? rec.role : undefined;
      const homeYardCode = typeof rec.homeYardCode === 'string' && rec.homeYardCode.length > 0
        ? rec.homeYardCode
        : undefined;
      return { kind: 'dev', name: rec.name, role, homeYardCode };
    }
    return null;
  }

  /** The held session, adopting the handoff on first use. Null when signed out. */
  get session(): AuthSession | null {
    this._adoptHandoff();
    return this._session;
  }

  /** The held bearer token, or null (dev sessions and signed-out states). */
  get token(): string | null {
    const s = this.session;
    return s !== null && s.kind === 'token' ? s.token : null;
  }

  /**
   * Who is signed in, as a stable key for per person settings (the theme
   * choice): the token's sub claim (else its email), or `dev:<name>` for a
   * dev session. Null when signed out.
   */
  get subject(): string | null {
    const s = this.session;
    if (s === null) return null;
    if (s.kind === 'dev') return `dev:${s.name}`;
    if (s.kind === 'staff') return s.sub;
    return s.claims.sub ?? s.claims.email ?? null;
  }

  /** The signed-in user's display name, from the token or the dev record. */
  get displayName(): string | null {
    const s = this.session;
    if (s === null) return null;
    if (s.kind === 'dev') return s.name;
    if (s.kind === 'staff') return s.name ?? s.sub;
    return s.claims.email ?? s.claims.name ?? s.claims.sub ?? null;
  }

  /**
   * The signed-in user's roles (empty for a dev session): a staff session's
   * role set as the core answered it, else a token's roles list plus the
   * single role claim, which core's RequireRole admits too
   * (core/pkg/middleware/auth.go).
   */
  get roles(): string[] {
    const s = this.session;
    if (s !== null && s.kind === 'staff') return [...s.roles];
    if (s === null || s.kind !== 'token') return [];
    const { roles, role } = s.claims;
    const merged = Array.isArray(roles) ? roles.filter((r) => typeof r === 'string') : [];
    if (typeof role === 'string' && role !== '' && !merged.includes(role)) merged.push(role);
    return merged;
  }

  /**
   * The persona's role when the session is a dev session with one (a
   * persona card set it), otherwise null. The door uses this to filter its
   * tiles in dev mode; a token session never sets it.
   */
  get personaRole(): string | null {
    const s = this.session;
    if (s === null || s.kind !== 'dev') return null;
    return s.role ?? null;
  }

  /**
   * The persona's home yard code when the session is a dev session with
   * one, otherwise null. The desk's BranchContext uses this to open the
   * right yard for a persona; a token session never sets it.
   */
  get personaHomeYardCode(): string | null {
    const s = this.session;
    if (s === null || s.kind !== 'dev') return null;
    return s.homeYardCode ?? null;
  }

  /**
   * Hold a staff session the core just opened. The cookie is already set by
   * the sign in answer; what is held here is display data only, in memory.
   * `name` is what the person typed at the door (a staff session carries no
   * email).
   */
  signInStaff(identity: { sub: string; roles: string[]; name?: string | null }): AuthSession {
    if (identity.sub.length === 0) throw new InvalidTokenError('Empty subject');
    const name = identity.name !== undefined && identity.name !== null && identity.name.trim() !== ''
      ? identity.name.trim()
      : null;
    const session: AuthSession = { kind: 'staff', sub: identity.sub, roles: [...identity.roles], name };
    this._session = session;
    this._adopted = true;
    return session;
  }

  /**
   * Hold a bearer token issued by the configured identity provider (the out
   * of band JWKS path, until ADR 0012 slice 8 retires it). The token is
   * decoded (never verified client-side) and refused as data when it is not
   * a JWT or its own exp claim is in the past. It lives in memory only: no
   * storage ever holds it, so a reload drops it.
   */
  signInWithToken(token: string): AuthSession {
    if (token.length === 0) throw new InvalidTokenError('Empty token');
    const claims = decodeTokenClaims(token);
    if (claims === null) throw new InvalidTokenError('Not a JWT');
    if (isExpired(claims)) throw new InvalidTokenError('Token is expired');

    const session: AuthSession = { kind: 'token', token, claims };
    this._session = session;
    this._adopted = true;
    return session;
  }

  /**
   * Sign in for local development (the core runs AUTH_MODE=dev): no
   * credential exists or is needed; the record carries a display name so
   * the door can render a user. Refused unless the build set
   * VITE_AUTH_DEV_MODE.
   *
   * `persona` is optional: a demo persona card passes the persona's role
   * and home yard code so the door can filter tiles with the role and the
   * desk can open the persona's home yard. A persona sign in also drops the
   * yard an earlier session stored, which the desk would otherwise restore
   * before it consults the persona. A free-form dev sign-in omits it, keeps
   * the stored yard, and the session carries only the display name.
   */
  signInDev(displayName: string, persona?: DevPersona): AuthSession {
    if (!authConfig.devMode) throw new DevModeDisabledError();
    const name = displayName.trim();
    if (name.length === 0) throw new InvalidTokenError('Empty display name');

    const role = persona?.role && persona.role.length > 0 ? persona.role : undefined;
    const homeYardCode = persona?.homeYardCode && persona.homeYardCode.length > 0
      ? persona.homeYardCode
      : undefined;
    if (persona) {
      try {
        globalThis.localStorage?.removeItem(BRANCH_STORAGE_KEY);
      } catch {
        // A hostile storage context: the desk falls back to its own yard rules.
      }
    }
    const session: AuthSession = { kind: 'dev', name, role, homeYardCode };
    this._session = session;
    this._adopted = true;
    this._writeHandoff({ v: 1, kind: 'dev', name, role, homeYardCode });
    return session;
  }

  /** Clear the session: memory and handoff both. */
  signOut(): void {
    this._session = null;
    this._adopted = true;
    // The counter remembers the empty sale a person last opened on each
    // register; signing out forgets them (a stale hint is harmless, a kept one
    // outlives the person). The desk's open apps are the person's too:
    // signing out closes them, so the next person starts on their own home.
    try {
      const ls = globalThis.localStorage;
      for (let i = ls.length - 1; i >= 0; i--) {
        const k = ls.key(i);
        if (k !== null && (k.startsWith(COUNTER_SALE_KEY_PREFIX) || k === DESK_WORKSPACE_KEY)) ls.removeItem(k);
      }
    } catch {
      // A hostile storage context: nothing was remembered that we can reach.
    }
    try {
      this._storage.removeItem(SESSION_HANDOFF_KEY);
    } catch {
      // A hostile storage context: memory is already clear, which is enough.
    }
  }

  /**
   * The 401 path: the server rejected the credential, so drop it. The
   * legacy localStorage cleanup stays in fetchWithAuth, which owns it.
   */
  onUnauthorized(): void {
    this.signOut();
  }

  private _writeHandoff(rec: HandoffRecord): void {
    try {
      this._storage.setItem(SESSION_HANDOFF_KEY, JSON.stringify(rec));
    } catch {
      // Degrade to memory-only custody: the session lives for this document.
    }
  }
}

/** The shared custody singleton every Gable frontend bundle uses. */
export const authCustody = new AuthCustody();
