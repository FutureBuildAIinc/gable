// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Lightweight SPA router — singleton service using popstate + pushState.
 * Components listen for 'route-changed' events to react to navigation.
 */

import { authCustody } from '@gable/auth';

export interface RouteConfig {
  path: string;
  /** Dynamic import returning the module that defines the custom element */
  load: () => Promise<unknown>;
  /** Which layout shell to render around this page */
  layout: 'erp' | 'portal' | 'driver' | 'yard' | 'none';
  /** If set, redirect to this path instead of rendering */
  redirect?: string;
  /**
   * The roles the page is for. A signed in person holding none of them gets
   * a plain refusal in place of the page. Unset: every role. UX only, the
   * server's role walls decide what a request may do.
   */
  roles?: string[];
}

/** The roles the server's own wall admits to the admin pages (core/internal/app/serve). */
export const ADMIN_OWNER = ['admin', 'owner'];
export const TAX_ZONE_ROLES = ['admin', 'owner', 'finance'];

/**
 * The roles the desk knows the person by: a staff session or token's roles,
 * else a dev persona's role (a dev stack puts no roles on the wire, so the
 * persona card is all the desk has). Empty when nothing names a role.
 */
export function heldRoles(): string[] {
  const held = [...(authCustody.roles ?? [])];
  const persona = authCustody.personaRole;
  if (persona !== null && !held.includes(persona)) held.push(persona);
  return held;
}

/**
 * Whether a person holding `held` may see a page for `required`. No required
 * roles is open to all. A person who names no role at all (local development
 * with no persona) is left to the server, which refuses what it must.
 */
export function rolesAllow(required: string[] | undefined, held: string[]): boolean {
  if (!required || required.length === 0) return true;
  if (held.length === 0) return true;
  return required.some((r) => held.includes(r));
}

/** True when the signed in person's roles do not reach the route. */
export function routeRefused(route: RouteConfig): boolean {
  return !rolesAllow(route.roles, heldRoles());
}

/** The words of the plain refusal: "This page is for admin and owner". */
export function refusalWords(roles: string[]): string {
  const list = roles.length <= 1
    ? roles.join('')
    : `${roles.slice(0, -1).join(', ')} and ${roles[roles.length - 1]}`;
  return `This page is for ${list}`;
}

export interface RouteMatch {
  route: RouteConfig;
  params: Record<string, string>;
}

class RouterService extends EventTarget {
  private static _instance: RouterService;
  private _routes: RouteConfig[] = [];
  private _currentMatch: RouteMatch | null = null;

  static get instance(): RouterService {
    if (!RouterService._instance) {
      RouterService._instance = new RouterService();
    }
    return RouterService._instance;
  }

  private constructor() {
    super();
    window.addEventListener('popstate', () => this._resolve());

    // Intercept all <a> clicks for SPA navigation
    document.addEventListener('click', (e) => {
      const anchor = (e.target as HTMLElement).closest('a');
      if (
        !anchor ||
        anchor.target === '_blank' ||
        anchor.hasAttribute('download') ||
        e.ctrlKey || e.metaKey || e.shiftKey || e.altKey
      ) return;

      const href = anchor.getAttribute('href');
      if (!href || href.startsWith('http') || href.startsWith('//') || href.startsWith('#') || href.startsWith('mailto:')) return;

      e.preventDefault();
      this.navigate(href);
    });
  }

  /** Register the full route table and perform initial resolve */
  init(routes: RouteConfig[]) {
    this._routes = routes;
    this._resolve();
  }

  get currentMatch(): RouteMatch | null {
    return this._currentMatch;
  }

  get currentPath(): string {
    return window.location.pathname;
  }

  navigate(path: string) {
    if (path === window.location.pathname) return;
    history.pushState(null, '', path);
    this._resolve();
  }

  replace(path: string) {
    history.replaceState(null, '', path);
    this._resolve();
  }

  back() {
    history.back();
  }

  private _resolve() {
    const path = window.location.pathname;

    for (const route of this._routes) {
      const params = this._match(route.path, path);
      if (params !== null) {
        // Handle redirects
        if (route.redirect) {
          this.replace(route.redirect);
          return;
        }
        this._currentMatch = { route, params };
        this.dispatchEvent(new CustomEvent('route-changed', { detail: this._currentMatch }));
        return;
      }
    }

    // No match — could render 404
    this._currentMatch = null;
    this.dispatchEvent(new CustomEvent('route-changed', { detail: null }));
  }

  /** Match a route pattern against a path, returning params or null */
  private _match(pattern: string, path: string): Record<string, string> | null {
    // Exact root match
    if (pattern === '/' && path === '/') return {};
    if (pattern === '/' && path !== '/') return null;

    const patternParts = pattern.split('/').filter(Boolean);
    const pathParts = path.split('/').filter(Boolean);

    if (patternParts.length !== pathParts.length) return null;

    const params: Record<string, string> = {};
    for (let i = 0; i < patternParts.length; i++) {
      if (patternParts[i].startsWith(':')) {
        params[patternParts[i].slice(1)] = decodeURIComponent(pathParts[i]);
      } else if (patternParts[i] !== pathParts[i]) {
        return null;
      }
    }
    return params;
  }
}

export const router = RouterService.instance;
