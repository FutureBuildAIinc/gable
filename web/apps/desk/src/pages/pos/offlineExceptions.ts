// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { ExceptionResolution } from '../../services/OfflineAdminService.ts';

export interface ResolutionChoice {
    value: ExceptionResolution;
    label: string;
    /** A reason is typed for every act that changes what posts; a retry needs none. */
    needsReason: boolean;
    help: string;
}

const CHOICES: Record<ExceptionResolution, ResolutionChoice> = {
    retry: { value: 'retry', label: 'Retry', needsReason: false, help: 'Post it again once the cause is fixed.' },
    accept: { value: 'accept', label: 'Accept', needsReason: true, help: 'Post it at the figures the register recorded, after you checked them.' },
    restate_stock: { value: 'restate_stock', label: 'Restate stock', needsReason: true, help: 'Convert the stock quantity to the current stocking unit, then post.' },
    late_post: { value: 'late_post', label: 'Late post', needsReason: true, help: 'Post it on the earliest open day; it joins no drawer.' },
    write_off: { value: 'write_off', label: 'Write off', needsReason: true, help: 'Nothing posts to sales. Any cash counted stays where the count put it.' },
    separate_sale: { value: 'separate_sale', label: 'Post as a separate sale', needsReason: true, help: 'A genuine sale for another customer: clear its link to the earlier cart and post it.' },
    settled_replaced: { value: 'settled_replaced', label: 'Same as the live sale', needsReason: true, help: 'The live sale completed: write this one off with no posting.' },
};

const BY_REASON: Record<string, ExceptionResolution[]> = {
    insufficient_stock: ['retry', 'write_off'],
    stock_unit_changed: ['restate_stock', 'write_off'],
    session_closed: ['late_post', 'retry', 'write_off'],
    day_closed: ['late_post', 'retry', 'write_off'],
    period_closed: ['late_post', 'write_off'],
    price_unverified: ['accept', 'write_off'],
    tax_flag_unverified: ['accept', 'write_off'],
    cap_exceeded: ['accept', 'write_off'],
    snapshot_stale: ['accept', 'write_off'],
    broken_chain: ['accept', 'write_off'],
    late_after_gap: ['retry', 'late_post', 'write_off'],
    replaced_sale_mismatch: ['separate_sale', 'write_off'],
    replaced_sale_settled: ['settled_replaced', 'retry', 'write_off'],
    bad_signature: ['write_off'],
    replay_mismatch: ['write_off'],
    device_revoked: ['write_off'],
    sequence_reused: ['write_off'],
};

/** The ways a manager may settle an exception of this reason (ADR 0011 section 3.4). */
export function resolutionsFor(reasonCode: string): ResolutionChoice[] {
    return (BY_REASON[reasonCode] ?? ['retry', 'write_off']).map((r) => CHOICES[r]);
}

/** The reason code in a clerk's words. */
export function reasonWords(code: string): string {
    const known: Record<string, string> = {
        insufficient_stock: 'Not enough stock on hand',
        stock_unit_changed: 'The stocking unit changed',
        session_closed: 'The drawer was already closed',
        day_closed: 'The branch day was already closed',
        period_closed: 'The accounting period was closed',
        price_unverified: 'A price did not match the register copy',
        tax_flag_unverified: 'A tax figure did not match the register copy',
        cap_exceeded: 'Over an offline limit',
        snapshot_stale: 'The register copy was older than the drawer',
        broken_chain: 'A sale before it is missing',
        late_after_gap: 'Arrived after a gap was accepted',
        replaced_sale_mismatch: 'Replaces a cart of another register or drawer',
        replaced_sale_settled: 'Replaces a cart that already settled',
        bad_signature: 'The register signature failed',
        replay_mismatch: 'Sent again with different figures',
        device_revoked: 'The register was revoked',
        sequence_reused: 'The number was used twice',
        out_of_wall: 'Outside what the user may reach',
        cashier_mismatch: 'Not the drawer cashier',
        one_account_leg: 'Two account payments in one sale',
    };
    return known[code] ?? code.replace(/_/g, ' ');
}
