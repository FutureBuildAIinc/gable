// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Client-side JWT payload decode.
 *
 * NO signature verification happens here and none should: the core verifies
 * every request's bearer token against its JWKS (core/pkg/middleware/auth.go).
 * The client decodes only to read what the door displays (who is signed in,
 * which roles) and to refuse to adopt a token whose own exp claim says it is
 * already expired. A token that is structurally garbage is refused as data,
 * never rendered.
 */

/** The claim set the core's UserClaims carries that the frontends use. */
export interface GableTokenClaims {
  sub?: string;
  email?: string;
  name?: string;
  roles?: string[];
  /** Expiry, seconds since the Unix epoch, as carried in the exp claim. */
  exp?: number;
}

/** Decode a JWT's payload segment. Null for anything that is not a JWS/JWT. */
export function decodeTokenClaims(token: string): GableTokenClaims | null {
  const parts = token.split('.');
  if (parts.length !== 3 || parts.some((p) => p.length === 0)) return null;

  try {
    // base64url → base64, padding restored; atob wants base64.
    const b64 = parts[1].replace(/-/g, '+').replace(/_/g, '/');
    const padded = b64 + '='.repeat((4 - (b64.length % 4)) % 4);
    const json = atob(padded);
    const claims = JSON.parse(json);
    if (typeof claims !== 'object' || claims === null || Array.isArray(claims)) return null;
    return claims as GableTokenClaims;
  } catch {
    return null;
  }
}

/** Milliseconds since epoch at which the token expires, or null if unknown. */
export function tokenExpiryMs(claims: GableTokenClaims): number | null {
  return typeof claims.exp === 'number' ? claims.exp * 1000 : null;
}

/** True when the token's own exp claim says it is in the past. */
export function isExpired(claims: GableTokenClaims, nowMs: number = Date.now()): boolean {
  const expiry = tokenExpiryMs(claims);
  return expiry !== null && expiry <= nowMs;
}
