// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The field shell's fixed configuration. Nothing here is a secret.

/**
 * The longest a sign out or role switch waits for the framed app to say its
 * own sign out has run. The desk's own sign out is in memory and storage, so
 * it settles fast; the bound exists for the frame that never answers. After
 * the wait the host clears its own custody and says so.
 */
export const SIGN_OUT_WAIT_MS = 4000;

/**
 * The field apps the shell can frame, for the field roles (warehouse and
 * driver in Gable's own role words, as tauri/core/src/profiles.rs maps them,
 * plus owner and admin). Both are route trees of the desk bundle today (/yard
 * and /driver), framed on the shell's own origin with the single header
 * protocol.
 */
export const FIELD_APPS = [
  {
    id: 'yard',
    name: 'Yard',
    path: '/yard',
    icon: 'warehouse',
    roles: ['owner', 'admin', 'warehouse'],
    places: [
      { id: 'pick', label: 'Pick queue', path: '/yard' },
      { id: 'inventory', label: 'Inventory', path: '/yard/inventory' },
      { id: 'receiving', label: 'Receiving', path: '/yard/receiving' },
    ],
  },
  {
    id: 'driver',
    name: 'Driver',
    path: '/driver',
    icon: 'truck',
    roles: ['owner', 'admin', 'driver'],
    places: [],
  },
] as const;
