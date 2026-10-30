// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { Quote, QuoteRevision, QuoteStatus } from '../../types/quote';

/**
 * The quote's version words (QE-01-b): a revised quote keeps its number and
 * becomes the next version, so the number a dealer reads carries its version
 * beside it the moment it moves past the first.
 */
export function versionLabel(number: string, versionNumber: number | null | undefined): string {
    const v = versionNumber ?? 1;
    return v > 1 ? `${number} v${v}` : number;
}

/** A dealer revises a quote that was sent, declined or let expire; a draft is already editable and an accepted quote stands. */
export function canRevise(status: QuoteStatus): boolean {
    return status === 'sent' || status === 'rejected' || status === 'expired';
}

/** One line of the version history: what changed against the version before. */
export interface RevisionLineChange {
    sku: string;
    description: string;
    change: 'added' | 'removed' | 'quantity' | 'price' | 'quantity and price';
    fromQuantity?: string;
    toQuantity?: string;
    fromPrice?: number;
    toPrice?: number;
}

const revLines = (snapshot: QuoteRevision['snapshot'] | undefined): Quote['lines'] =>
    snapshot?.lines ? [...snapshot.lines] : [];

/**
 * The lines that changed from one sent version to the next: matched by SKU
 * (a revised quote replaces its lines, so ids do not survive), each change
 * named in a dealer's words. The oldest version has nothing before it.
 */
export function changedLines(prev: QuoteRevision | undefined, next: QuoteRevision): RevisionLineChange[] {
    if (!prev) return [];
    const before = new Map(revLines(prev.snapshot).map(l => [l.sku, l]));
    const after = revLines(next.snapshot);
    const out: RevisionLineChange[] = [];
    const seen = new Set<string>();
    for (const line of after) {
        seen.add(line.sku);
        const was = before.get(line.sku);
        if (!was) {
            out.push({ sku: line.sku, description: line.description, change: 'added', toQuantity: line.quantity, toPrice: line.unit_price_ten_thousandths });
            continue;
        }
        const quantityMoved = was.quantity !== line.quantity;
        const priceMoved = was.unit_price_ten_thousandths !== line.unit_price_ten_thousandths;
        if (quantityMoved || priceMoved) {
            out.push({
                sku: line.sku,
                description: line.description,
                change: quantityMoved && priceMoved ? 'quantity and price' : quantityMoved ? 'quantity' : 'price',
                fromQuantity: was.quantity, toQuantity: line.quantity,
                fromPrice: was.unit_price_ten_thousandths, toPrice: line.unit_price_ten_thousandths,
            });
        }
    }
    for (const [sku, was] of before) {
        if (!seen.has(sku)) {
            out.push({ sku: was.sku, description: was.description, change: 'removed', fromQuantity: was.quantity, fromPrice: was.unit_price_ten_thousandths });
        }
    }
    return out;
}
