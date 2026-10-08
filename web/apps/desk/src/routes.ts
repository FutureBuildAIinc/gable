// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { RouteConfig } from './lib/router.ts';
import { appRoutes } from './apps/registry.ts';

/**
 * Route table — order matters: more-specific paths must come before less-specific ones.
 *
 * All paths are prefixed /app/ because the desk bundle is mounted at /app/ in
 * the combined nginx layout: the front door owns / and the desk owns /app/,
 * so the two bundles' SPA fallbacks never collide. When Vite's base is /app/,
 * the browser URL includes /app/ but Vite strips it before the router matches,
 * so these paths are exactly what the router sees.
 *
 * Converted apps (docs/modularization-blueprint.md) declare their routes in
 * web/apps/desk/src/apps/<key>.ts instead of here — they're spread in via appRoutes().
 */
export const routes: RouteConfig[] = [
  // ── POS (no layout) ─────────────────────────────────────────────
  { path: '/app/pos', load: () => import('./pages/pos/POSTerminal.ts'), layout: 'none' },

  // ── Portal login (no layout) ────────────────────────────────────
  { path: '/app/portal/login', load: () => import('./pages/portal/PortalLogin.ts'), layout: 'none' },

  // ── Portal (portal layout) ─────────────────────────────────────
  { path: '/app/portal/orders', load: () => import('./pages/portal/PortalOrders.ts'), layout: 'portal' },
  { path: '/app/portal/invoices', load: () => import('./pages/portal/PortalInvoices.ts'), layout: 'portal' },
  { path: '/app/portal/deliveries', load: () => import('./pages/portal/PortalDeliveries.ts'), layout: 'portal' },
  { path: '/app/portal/catalog/:id', load: () => import('./pages/portal/PortalProductDetail.ts'), layout: 'portal' },
  { path: '/app/portal/catalog', load: () => import('./pages/portal/PortalCatalog.ts'), layout: 'portal' },
  { path: '/app/portal/cart', load: () => import('./pages/portal/PortalCart.ts'), layout: 'portal' },
  { path: '/app/portal/checkout', load: () => import('./pages/portal/PortalCheckout.ts'), layout: 'portal' },
  { path: '/app/portal/account', load: () => import('./pages/portal/PortalMyAccount.ts'), layout: 'portal' },
  { path: '/app/portal/team/invite', load: () => import('./pages/portal/PortalInvite.ts'), layout: 'portal' },
  { path: '/app/portal/team', load: () => import('./pages/portal/PortalTeam.ts'), layout: 'portal' },
  { path: '/app/portal/projects/:id', load: () => import('./pages/projects/ProjectDashboard.ts'), layout: 'portal' },
  { path: '/app/portal/projects', load: () => import('./pages/projects/ProjectList.ts'), layout: 'portal' },
  { path: '/app/portal', load: () => import('./pages/portal/PortalDashboard.ts'), layout: 'portal' },

  // ── Driver (driver layout) ─────────────────────────────────────
  { path: '/app/driver/routes/:id', load: () => import('./pages/driver/StopList.ts'), layout: 'driver' },
  { path: '/app/driver/deliveries/:id', load: () => import('./pages/driver/DeliveryDetail.ts'), layout: 'driver' },
  { path: '/app/driver', load: () => import('./pages/driver/RouteList.ts'), layout: 'driver' },

  // ── Yard (yard layout) ─────────────────────────────────────────
  { path: '/app/yard/pick/:id', load: () => import('./pages/yard/PickDetail.ts'), layout: 'yard' },
  { path: '/app/yard/inventory', load: () => import('./pages/yard/InventoryLookup.ts'), layout: 'yard' },
  { path: '/app/yard/count', load: () => import('./pages/yard/CycleCount.ts'), layout: 'yard' },
  { path: '/app/yard/receiving', load: () => import('./pages/yard/ReceivePO.ts'), layout: 'yard' },
  { path: '/app/yard', load: () => import('./pages/yard/PickQueue.ts'), layout: 'yard' },

  // ── ERP (erp/AppShell layout) ──────────────────────────────────
  { path: '/app/inventory/:id', load: () => import('./pages/inventory/ProductDetail.ts'), layout: 'erp' },
  { path: '/app/inventory', load: () => import('./pages/Inventory.ts'), layout: 'erp' },
  { path: '/app/quotes/new', load: () => import('./pages/QuoteBuilder.ts'), layout: 'erp' },
  { path: '/app/quotes/:id/edit', load: () => import('./pages/QuoteBuilder.ts'), layout: 'erp' },
  { path: '/app/quotes/analytics', load: () => import('./pages/quotes/QuoteAnalytics.ts'), layout: 'erp' },
  { path: '/app/quotes/exposure', load: () => import('./pages/quotes/Exposure.ts'), layout: 'erp' },
  { path: '/app/quotes/:id', load: () => import('./pages/quotes/QuoteDetail.ts'), layout: 'erp' },
  { path: '/app/quotes', load: () => import('./pages/quotes/QuoteList.ts'), layout: 'erp' },
  { path: '/app/orders/:id', load: () => import('./pages/orders/OrderDetail.ts'), layout: 'erp' },
  { path: '/app/orders', load: () => import('./pages/orders/OrderList.ts'), layout: 'erp' },
  { path: '/app/invoices/:id', load: () => import('./pages/invoices/InvoiceDetail.ts'), layout: 'erp' },
  { path: '/app/invoices', load: () => import('./pages/invoices/InvoiceList.ts'), layout: 'erp' },
  { path: '/app/reports/daily-till', load: () => import('./pages/DailyTill.ts'), layout: 'erp' },
  { path: '/app/reports/ar-aging', load: () => import('./pages/reports/ARAgingReport.ts'), layout: 'erp' },
  { path: '/app/reports/exposure', load: () => import('./pages/reports/ExposureReport.ts'), layout: 'erp' },
  { path: '/app/reports/customer-statement', load: () => import('./pages/reports/CustomerStatementPage.ts'), layout: 'erp' },
  { path: '/app/reports/saved', load: () => import('./pages/reports/SavedReports.ts'), layout: 'erp' },
  { path: '/app/reports/builder', load: () => import('./pages/reports/ReportBuilder.ts'), layout: 'erp' },
  { path: '/app/dispatch', load: () => import('./pages/DispatchBoard.ts'), layout: 'erp' },
  { path: '/app/fleet', load: () => import('./pages/logistics/FleetManagement.ts'), layout: 'erp' },
  // Converted apps (millwork, governance, …) — routes come from their manifests.
  ...appRoutes(),
  { path: '/app/purchasing/vendors/:id', load: () => import('./pages/purchasing/VendorDetail.ts'), layout: 'erp' },
  { path: '/app/purchasing/vendors', load: () => import('./pages/purchasing/VendorList.ts'), layout: 'erp' },
  { path: '/app/purchasing/new', load: () => import('./pages/purchasing/NewPurchaseOrder.ts'), layout: 'erp' },
  { path: '/app/purchasing/recommendations', load: () => import('./pages/purchasing/PurchasingRecommendations.ts'), layout: 'erp' },
  { path: '/app/purchasing/:id', load: () => import('./pages/purchasing/PurchaseOrderDetail.ts'), layout: 'erp' },
  { path: '/app/purchasing', load: () => import('./pages/purchasing/PurchaseOrderList.ts'), layout: 'erp' },
  { path: '/app/sales', load: async () => {}, layout: 'erp', redirect: '/app/quotes' },
  { path: '/app/admin/apps', load: () => import('./pages/admin/AppsPage.ts'), layout: 'erp' },
  { path: '/app/admin/branches/:id/users', load: () => import('./pages/admin/branches/BranchUsers.ts'), layout: 'erp' },
  { path: '/app/admin/branches', load: () => import('./pages/admin/branches/Branches.ts'), layout: 'erp' },
  { path: '/app/admin/market-indices', load: () => import('./pages/admin/MarketIndices.ts'), layout: 'erp' },
  { path: '/app/admin', load: () => import('./pages/admin/tech_admin/TechAdminPage.ts'), layout: 'erp' },
  { path: '/app/pricing', load: () => import('./pages/admin/pricing/PricingMatrix.ts'), layout: 'erp' },
  { path: '/app/accounts/:id', load: () => import('./pages/accounts/AccountDetailPage.ts'), layout: 'erp' },
  { path: '/app/accounts', load: () => import('./pages/accounts/AccountsPage.ts'), layout: 'erp' },
  { path: '/app/accounting/chart-of-accounts', load: () => import('./pages/accounting/ChartOfAccounts.ts'), layout: 'erp' },
  { path: '/app/accounting/journal-entries', load: () => import('./pages/accounting/JournalEntries.ts'), layout: 'erp' },
  { path: '/app/accounting/trial-balance', load: () => import('./pages/accounting/TrialBalance.ts'), layout: 'erp' },
  { path: '/app/accounting/profit-and-loss', load: () => import('./pages/accounting/ProfitAndLoss.ts'), layout: 'erp' },
  { path: '/app/accounting/balance-sheet', load: () => import('./pages/accounting/BalanceSheet.ts'), layout: 'erp' },
  { path: '/app/accounting/accounts-payable', load: () => import('./pages/accounting/AccountsPayable.ts'), layout: 'erp' },
  // ERP home: Apps launcher + Dashboard as internal tabs.
  { path: '/app/home', load: () => import('./pages/Home.ts'), layout: 'erp' },
  // Direct dashboard deep-link (also lives in Home's Dashboard tab).
  { path: '/app/dashboard', load: () => import('./pages/Dashboard.ts'), layout: 'erp' },
  // Surface picker — mounted at `/app/` for:
  //   - local dev (`vite dev`)                          → import.meta.env.DEV
  //   - the public demo build (the public demo build)   → VITE_DEMO_MODE=true
  // The front door owns `/`; the desk's surface picker is at /app/local-test.
  ...(import.meta.env.DEV || import.meta.env.VITE_DEMO_MODE === 'true'
    ? [
        { path: '/app/local-test', load: () => import('./pages/LocalTestHub.ts'), layout: 'none' as const },
        { path: '/app/', load: () => import('./pages/LocalTestHub.ts'), layout: 'none' as const },
      ]
    : [
        { path: '/app/', load: () => import('./pages/Home.ts'), layout: 'erp' as const },
      ]),
];
