// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { html } from 'lit';

/** The status chip colours, the same set the order pages use. */
export type ChipColor = 'default' | 'info' | 'success' | 'warning' | 'error';

export function chipClass(color: ChipColor): string {
    const base = 'inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium border';
    switch (color) {
        case 'info': return `${base} bg-blue-500/20 text-blue-400 border-blue-500/50`;
        case 'success': return `${base} bg-gable-green/20 text-gable-green border-gable-green/50`;
        case 'warning': return `${base} bg-amber-500/20 text-amber-400 border-amber-500/50`;
        case 'error': return `${base} bg-red-500/20 text-red-400 border-red-500/50`;
        default: return `${base} bg-white/10 text-white border-transparent`;
    }
}

/** The Overdue badge an open invoice past its due date carries beside its status. */
export function overdueBadge(isOverdue: boolean) {
    return isOverdue
        ? html`<span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-semibold border bg-red-500/20 text-red-400 border-red-500/50" data-testid="overdue-badge">Overdue</span>`
        : '';
}
