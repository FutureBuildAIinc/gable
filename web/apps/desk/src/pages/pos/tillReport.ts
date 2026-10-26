// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The till report panel's figures as pure functions of the report (CP-08):
// the X report read mid shift and the frozen Z read share them. Integer
// cents throughout; the report's method maps are in storage spelling
// (CASH, CHECK, CARD).

import type { TillReport } from '../../types/pos';

/** The panel's kind: the live X read of an open drawer, or a frozen Z. */
export type TillReportKind = 'x' | 'z';

/**
 * True while the drawer is open and the caller may not see the money
 * figures: the close is a blind count, so a cashier's report of an open
 * drawer carries no expected figures (a manager's always carries the CASH
 * figure, even on an empty drawer). The rule is the server's; the panel
 * follows what the report carries.
 */
export function isBlind(report: TillReport): boolean {
    return report.session.status === 'open' && !('CASH' in (report.expected_by_method ?? {}));
}

/** A method's label from its storage spelling, as the counter names it. */
export function reportMethodLabel(method: string): string {
    const m = method.toLowerCase();
    if (m === 'account') return 'Charge to account';
    if (m === 'store_credit') return 'Store credit';
    return m.charAt(0).toUpperCase() + m.slice(1);
}

/**
 * The methods the panel lists, ordered and deduped across the taken,
 * expected and refunds maps; an account charge is never money in the
 * drawer, so it stays off the drawer figures as it does at the close.
 */
export function reportMethods(report: TillReport): string[] {
    const keys = new Set<string>();
    for (const map of [report.tendered_by_method, report.expected_by_method, report.refunds_by_method]) {
        for (const k of Object.keys(map ?? {})) {
            if (k.toLowerCase() !== 'account') keys.add(k);
        }
    }
    return [...keys].sort();
}

/** The expected drawer cash, null while the caller may not see it. */
export function expectedDrawerCash(report: TillReport): number | null {
    if (isBlind(report)) return null;
    return report.expected_by_method?.CASH ?? null;
}
