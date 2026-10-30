// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { ExceptionResolution } from '../../services/OfflineAdminService.ts';

export interface ResolutionChoice {
    value: ExceptionResolution;
    label: string;
    /** The server asks for a reason on every act, a retry included (O4 parse). */
    needsReason: boolean;
    help: string;
}

const CHOICES: Record<ExceptionResolution, ResolutionChoice> = {
    retry: { value: 'retry', label: 'Retry', needsReason: true, help: 'Post it again once the cause is fixed.' },
    accept: { value: 'accept', label: 'Accept', needsReason: true, help: 'Post it at the figures the register recorded, after you checked them.' },
    restate: { value: 'restate', label: 'Restate stock', needsReason: true, help: 'Convert the stock quantity to the current stocking unit, then post.' },
    late_post: { value: 'late_post', label: 'Late post', needsReason: true, help: 'Post it on the earliest open day; it joins no drawer.' },
    write_off: { value: 'write_off', label: 'Write off', needsReason: true, help: 'Nothing posts to sales. Any cash counted stays where the count put it.' },
    post_separate: { value: 'post_separate', label: 'Post as a separate sale', needsReason: true, help: 'A genuine sale for another customer: clear its link to the earlier cart and post it.' },
    review: { value: 'review', label: 'Mark reviewed', needsReason: true, help: 'You looked at the warning; nothing posts or changes.' },
};

/** The ways a manager may settle an exception: exactly the actions the server lists for its reason (ADR 0011 section 3.4). */
export function resolutionsFor(actions: readonly ExceptionResolution[]): ResolutionChoice[] {
    return actions.map((a) => CHOICES[a]).filter((c): c is ResolutionChoice => c !== undefined);
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
        unknown_tail: 'The register lost sales when it was re-enrolled',
        sequence_gap: 'A number is missing from the register\'s run',
        sold_past_warning: 'Sold past a warning',
        one_account_leg: 'Two account payments in one sale',
        account_unverified: 'The account was on hold or missed a rule it had',
        clock_unverified: 'The register clock looked wrong',
        id_unverified: 'The ID a payment needs was not recorded',
        queue_mark_unverified: 'The register\'s count of unsent sales did not add up',
        snapshot_unknown: 'The register copy was not one it was given',
        stock_unverified: 'The stock figures did not match the register copy',
        tax_unverified: 'The tax did not match the register copy',
        tender_mismatch: 'The payments did not add up to the sale',
        walk_in_account: 'Walk-in customer put on account',
        payer_details_required: 'Payer details were missing',
        tender_not_offline: 'That payment is not taken offline',
    };
    return known[code] ?? `Other reason (${code})`;
}
