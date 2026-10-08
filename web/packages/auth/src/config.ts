// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Auth runtime config, read from the Vite build environment.
 *
 * The client never verifies a signature: the core verifies every token
 * against the JWKS at JWKS_URL and the configured issuer
 * (core/pkg/middleware/auth.go). These values tell the frontends WHICH
 * authority issued the tokens they hold, so a door built for one estate
 * does not hand a foreign token to the desk, and so dev mode can be told
 * apart from a production build at runtime.
 */

export interface AuthRuntimeConfig {
  /** Expected OIDC issuer of the tokens this build accepts. */
  issuer: string;
  /** JWKS URL the core verifies against (informational client-side). */
  jwksUrl: string;
  /**
   * True when the backend runs AUTH_MODE=dev: the auth middleware is never
   * constructed and anonymous callers have full reach. The front door offers
   * its dev sign-in (a display name, no credential) only when this is set.
   */
  devMode: boolean;
}

function readEnv(): Record<string, string | undefined> {
  // import.meta.env exists in every Vite build and in vitest; the cast keeps
  // this package typecheckable without vite/client types in every consumer.
  const env = (import.meta as ImportMeta & { env?: Record<string, string | undefined> }).env;
  return env ?? {};
}

export function authConfigFromEnv(env: Record<string, string | undefined> = readEnv()): AuthRuntimeConfig {
  return {
    issuer: env.VITE_AUTH_ISSUER ?? '',
    jwksUrl: env.VITE_JWKS_URL ?? '',
    devMode: env.VITE_AUTH_DEV_MODE === 'true',
  };
}

/** The build's auth config. Frozen at build time by Vite's env replacement. */
export const authConfig: AuthRuntimeConfig = authConfigFromEnv();
