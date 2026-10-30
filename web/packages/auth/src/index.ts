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
  type AuthSession,
  type DevPersona,
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
export { BRANCH_STORAGE_KEY } from './branch.ts';
export {
  authMethodsConfig,
  degradedAuthMethodsConfig,
  parseAuthMethodsConfig,
  resetAuthMethodsCache,
  type AuthMethodsRuntimeConfig,
  type SignInMethodCode,
  type SignInMethodConfig,
  type SignInMethodKind,
} from './runtime-config.ts';
export {
  recordIdentity,
  readIdentity,
  forgetIdentity,
  IDENTITY_TAP_KEY,
  type TappedIdentity,
} from './identity-tap.ts';
export {
  CSRF_HEADER,
  restoreStaffSession,
  signInStaff,
  signOutStaff,
  type StaffSignInResult,
} from './staffAuth.ts';
