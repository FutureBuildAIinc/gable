// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { GLRequestError } from '../../services/GLService';

/**
 * The words for a statement that would not load. A refusal (403) on these
 * pages means the caller picked a view wider than their reach, the
 * consolidated one, and is told to pick a branch instead of "failed".
 */
export function loadFailure(err: unknown, what: string): string {
    if (err instanceof GLRequestError && err.status === 403) {
        return 'You do not have access to this view. Pick a branch you may read.';
    }
    return `Failed to load ${what}`;
}
