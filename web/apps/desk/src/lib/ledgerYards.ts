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

/** The calm note for a customer whose ledger lives at yards other than the current one. */
export function ledgerYardNote(branchIds: readonly string[]): string {
    const names = branchContext.branches.filter(b => branchIds.includes(b.id)).map(b => b.name);
    const where = names.length > 0 ? names.join(' and ') : 'another yard';
    return `This account's ledger is kept at ${where}. Switch the yard to see it.`;
}
