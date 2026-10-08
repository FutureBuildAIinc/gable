// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * @gable/auth — JWT custody and the shared authenticated fetch client for
 * Gable's frontend bundles (the front door and the desk).
 */

export {
  authConfig,
  authConfigFromEnv,
  type AuthRuntimeConfig,
} from './config.ts';
export {
  AuthCustody,
  authCustody,
  DevModeDisabledError,
  InvalidTokenError,
  SESSION_HANDOFF_KEY,
  type AuthSession,
  type StorageLike,
} from './custody.ts';
export {
  decodeTokenClaims,
  isExpired,
  tokenExpiryMs,
  type GableTokenClaims,
} from './jwt.ts';
export {
  fetchWithAuth,
  SESSION_EXPIRED_EVENT,
  type FetchWithAuthOptions,
} from './fetchClient.ts';
