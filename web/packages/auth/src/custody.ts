// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * JWT custody for Gable's frontends.
 *
 * THE RULE: a bearer token lives in private memory in this module and is
 * never written to localStorage. The only place a session may outlive a
 * document is the handoff key in sessionStorage, and only so the front
 * door can hand a signed-in session to the desk across a full page
 * navigation (the door at / and the desk at /home are separate bundles;
 * an in-memory token alone would die at the navigation). sessionStorage is
 * per tab and dies with it; a credential never lands in persistent storage.
 *
 * The record is validated field by field on adoption and dropped outright
 * when it does not parse: a hostile or stale payload buys nothing.
 *
 * Dev mode (AUTH_MODE=dev on the core) has no credential to hold at all;
 * the door's dev sign-in stores a display name under the same handoff
 * discipline, and only when the build was made with VITE_AUTH_DEV_MODE.
 */

import { authConfig } from './config.ts';
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

/** The handoff key: the door writes it at sign-in, every bundle adopts it at boot. */
export const SESSION_HANDOFF_KEY = 'gable.auth.session.v1';

/** A signed-in session, in the shape the frontends render. */
export type AuthSession =
  | { kind: 'token'; token: string; claims: GableTokenClaims }
  | { kind: 'dev'; name: string };

/** The handoff record's on-the-wire shape. The v tags the payload shape. */
interface HandoffRecord {
  v: 1;
  kind: 'token' | 'dev';
  token?: string;
  name?: string;
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

    if (rec.kind === 'token') {
      if (typeof rec.token !== 'string') return null;
      const claims = decodeTokenClaims(rec.token);
      if (claims === null || isExpired(claims)) return null;
      return { kind: 'token', token: rec.token, claims };
    }
    if (rec.kind === 'dev') {
      if (!authConfig.devMode) return null;
      if (typeof rec.name !== 'string' || rec.name.length === 0) return null;
      return { kind: 'dev', name: rec.name };
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

  /** The signed-in user's display name, from the token or the dev record. */
  get displayName(): string | null {
    const s = this.session;
    if (s === null) return null;
    if (s.kind === 'dev') return s.name;
    return s.claims.email ?? s.claims.name ?? s.claims.sub ?? null;
  }

  /** The signed-in user's roles from the token claims (empty otherwise). */
  get roles(): string[] {
    const s = this.session;
    return s !== null && s.kind === 'token' ? (s.claims.roles ?? []) : [];
  }

  /**
   * Sign in with a bearer token issued by the configured identity provider.
   * The token is decoded (never verified client-side) and refused as data
   * when it is not a JWT or its own exp claim is in the past. On success it
   * is held in memory and written to the sessionStorage handoff.
   */
  signInWithToken(token: string): AuthSession {
    if (token.length === 0) throw new InvalidTokenError('Empty token');
    const claims = decodeTokenClaims(token);
    if (claims === null) throw new InvalidTokenError('Not a JWT');
    if (isExpired(claims)) throw new InvalidTokenError('Token is expired');

    const session: AuthSession = { kind: 'token', token, claims };
    this._session = session;
    this._adopted = true;
    this._writeHandoff({ v: 1, kind: 'token', token });
    return session;
  }

  /**
   * Sign in for local development (the core runs AUTH_MODE=dev): no
   * credential exists or is needed; the record carries a display name so
   * the door can render a user. Refused unless the build set
   * VITE_AUTH_DEV_MODE.
   */
  signInDev(displayName: string): AuthSession {
    if (!authConfig.devMode) throw new DevModeDisabledError();
    const name = displayName.trim();
    if (name.length === 0) throw new InvalidTokenError('Empty display name');

    const session: AuthSession = { kind: 'dev', name };
    this._session = session;
    this._adopted = true;
    this._writeHandoff({ v: 1, kind: 'dev', name });
    return session;
  }

  /** Clear the session: memory and handoff both. */
  signOut(): void {
    this._session = null;
    this._adopted = true;
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
