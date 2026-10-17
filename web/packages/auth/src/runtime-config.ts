// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The runtime auth methods config (S8): the front door and the shells
 * discover the dealer's allowed sign in methods at boot from
 * GET /api/v1/auth/config, instead of hardcoding them per bundle.
 *
 * The loader fetches once and caches for the page's life. It fails closed:
 * when the call fails, or the answer does not parse, the config is email
 * and password only, marked degraded, so the door always renders a working
 * method with a visible notice instead of an empty card.
 *
 * The response holds public facts only (the contract's strict allowlist
 * type); fields this module does not know are dropped rather than carried.
 */

/** The six method codes the server's vocabulary holds. */
export type SignInMethodCode =
  | 'email_password'
  | 'passkey'
  | 'magic_link'
  | 'google'
  | 'microsoft'
  | 'oidc';

/** How the door renders one method. */
export type SignInMethodKind = 'form' | 'passkey' | 'link' | 'redirect';

/** One allowed sign in method, exactly the fields the server's type has. */
export interface SignInMethodConfig {
  readonly method: SignInMethodCode;
  readonly kind: SignInMethodKind;
  readonly label: string;
  readonly start_url: string | null;
  readonly issuer_url: string | null;
  readonly client_id: string | null;
}

/** The runtime config the door renders from. */
export interface AuthMethodsRuntimeConfig {
  readonly authMode: 'dev' | 'production';
  readonly methods: readonly SignInMethodConfig[];
  /** True when the server's answer was not had and the config failed closed. */
  readonly degraded: boolean;
  /** The notice the door shows when degraded; null otherwise. */
  readonly notice: string | null;
}

const METHOD_CODES: readonly SignInMethodCode[] = [
  'email_password',
  'passkey',
  'magic_link',
  'google',
  'microsoft',
  'oidc',
];
const KINDS: readonly SignInMethodKind[] = ['form', 'passkey', 'link', 'redirect'];

const DEGRADED_NOTICE =
  'The sign in methods could not be read from the server; email and password is offered alone.';

/** The fail closed config: email and password only, with its notice. */
export function degradedAuthMethodsConfig(): AuthMethodsRuntimeConfig {
  return {
    authMode: 'production',
    methods: [
      {
        method: 'email_password',
        kind: 'form',
        label: 'Email and password',
        start_url: '/api/portal/v1/login',
        issuer_url: null,
        client_id: null,
      },
    ],
    degraded: true,
    notice: DEGRADED_NOTICE,
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function stringOrNull(value: unknown): string | null {
  return typeof value === 'string' && value !== '' ? value : null;
}

/** Read the parsed JSON field by field; never cast. */
export function parseAuthMethodsConfig(raw: unknown): AuthMethodsRuntimeConfig {
  if (!isRecord(raw)) return degradedAuthMethodsConfig();
  const authMode = raw['auth_mode'] === 'dev' ? 'dev' : 'production';
  const list = raw['methods'];
  if (!Array.isArray(list)) return degradedAuthMethodsConfig();
  const methods: SignInMethodConfig[] = [];
  for (const item of list) {
    if (!isRecord(item)) continue;
    const method = item['method'];
    const kind = item['kind'];
    if (typeof method !== 'string' || !METHOD_CODES.includes(method as SignInMethodCode)) continue;
    if (typeof kind !== 'string' || !KINDS.includes(kind as SignInMethodKind)) continue;
    methods.push({
      method: method as SignInMethodCode,
      kind: kind as SignInMethodKind,
      label: typeof item['label'] === 'string' && item['label'] !== '' ? item['label'] : method,
      start_url: stringOrNull(item['start_url']),
      issuer_url: stringOrNull(item['issuer_url']),
      client_id: stringOrNull(item['client_id']),
    });
  }
  if (methods.length === 0) return degradedAuthMethodsConfig();
  return { authMode, methods, degraded: false, notice: null };
}

/** The page life cache: the first call's promise is every call's answer. */
let cached: Promise<AuthMethodsRuntimeConfig> | null = null;

/** Forget the cache (tests and an explicit retry call this). */
export function resetAuthMethodsCache(): void {
  cached = null;
}

/**
 * Fetch the dealer's allowed sign in methods once and cache the promise for
 * the page's life. `baseUrl` is the API base (empty means the page's own
 * origin). A failure of any kind fails closed to email and password with the
 * visible notice.
 */
export function authMethodsConfig(
  baseUrl = '',
  fetchFn: typeof fetch = globalThis.fetch.bind(globalThis),
): Promise<AuthMethodsRuntimeConfig> {
  if (cached !== null) return cached;
  cached = (async () => {
    let response: Response;
    try {
      response = await fetchFn(`${baseUrl}/api/v1/auth/config`, {
        cache: 'no-store',
        headers: { Accept: 'application/json' },
      });
    } catch {
      return degradedAuthMethodsConfig();
    }
    if (response.status !== 200) return degradedAuthMethodsConfig();
    let body: unknown;
    try {
      body = await response.json();
    } catch {
      return degradedAuthMethodsConfig();
    }
    return parseAuthMethodsConfig(body);
  })();
  return cached;
}
