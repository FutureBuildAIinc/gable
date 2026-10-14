// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * A customer is dealer wide (ADR 0007 section 5.8) but the subledger and the
 * statement come from finance documents, which keep their yard's wall: for a
 * customer outside the yard the person is on, both answer 404. That is not a
 * failure, so the services throw this class and the pages say where the ledger
 * is kept instead of raising an error toast.
 */
import { branchContext } from '../services/BranchContext.ts';

export class LedgerOutsideYardError extends Error {
    constructor() {
        super('The ledger is kept at another yard');
        this.name = 'LedgerOutsideYardError';
    }
}

/**
 * The calm note for a customer whose ledger (or activity) lives at yards other
 * than the current one. It advises a switch only to a yard the person has in
 * the switcher; with none, it says where the ledger is not.
 */
export function ledgerYardNote(branchIds: readonly string[], subject: 'ledger' | 'activity' = 'ledger'): string {
    const names = branchContext.branches.filter(b => branchIds.includes(b.id)).map(b => b.name);
    if (names.length === 0) return `This account's ${subject} is kept at another yard.`;
    return `This account's ${subject} is kept at ${names.join(' and ')}. Switch the yard to see it.`;
}
