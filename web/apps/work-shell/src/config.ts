// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The host's fixed configuration. Nothing here is a secret.

/**
 * The longest a sign out or role switch waits for the framed app to say its
 * own sign out has run. The desk's own sign out is in memory and storage, so
 * it settles fast; the bound exists for the frame that never answers. After
 * the wait the host clears its own custody and says so.
 */
export const SIGN_OUT_WAIT_MS = 4000;

/** The apps the shell can frame. Every one of them is a route tree of the
 * desk bundle today (one bundle serves /erp, /portal, /driver, /yard and
 * /pos), so a path is a root path on the shell's own origin. The frame's
 * address is never built from anything else: the framed protocol needs one
 * origin, and a remote frame would be refused by it anyway. */
export const SHELL_APPS = [
  {
    id: 'desk',
    name: 'Desk',
    path: '/home',
    icon: 'grid',
    roles: [],
    places: [
      { id: 'home', label: 'Home', path: '/home' },
      { id: 'orders', label: 'Orders', path: '/orders' },
      { id: 'quotes', label: 'Quotes', path: '/quotes' },
      { id: 'inventory', label: 'Inventory', path: '/inventory' },
    ],
  },
  {
    id: 'pos',
    name: 'Point of Sale',
    path: '/pos',
    icon: 'receipt',
    roles: ['owner', 'admin', 'sales', 'cashier'],
    places: [],
  },
  {
    id: 'yard',
    name: 'Yard',
    path: '/yard',
    icon: 'warehouse',
    roles: ['owner', 'admin', 'warehouse'],
    places: [
      { id: 'pick', label: 'Pick queue', path: '/yard' },
      { id: 'receiving', label: 'Receiving', path: '/yard/receiving' },
      { id: 'count', label: 'Cycle count', path: '/yard/count' },
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
  {
    id: 'portal',
    name: 'Portal',
    path: '/portal',
    icon: 'storefront',
    roles: ['owner', 'admin', 'sales'],
    places: [
      { id: 'orders', label: 'Orders', path: '/portal/orders' },
      { id: 'catalog', label: 'Catalog', path: '/portal/catalog' },
    ],
  },
] as const;
