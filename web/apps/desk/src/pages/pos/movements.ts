// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The drawer's cash movements as pure functions of the form: what the dialog
// sends and what it says when the form cannot be sent yet. The same rules the
// server holds (a reason from the closed list for paid in and paid out, a note
// for other, a second person for a pickup), so the cashier hears them before
// the call. Integer cents throughout.

import { dollarsToCents } from '../../services/POSService';
import type { MovementKind, MovementReason, MovementRequest } from '../../types/pos';

export const MOVEMENT_KINDS: { kind: MovementKind; label: string; hint: string }[] = [
    { kind: 'safe_drop', label: 'Safe drop', hint: 'Cash from the drawer to the safe' },
    { kind: 'pickup', label: 'Pickup', hint: 'Cash handed to a second person' },
    { kind: 'paid_in', label: 'Paid in', hint: 'Cash into the drawer outside a sale' },
    { kind: 'paid_out', label: 'Paid out', hint: 'Cash out of the drawer for an expense' },
];

/** The closed reason lists of the paid kinds, as the server holds them. */
export const MOVEMENT_REASONS: Record<'paid_in' | 'paid_out', { value: MovementReason; label: string }[]> = {
    paid_out: [
        { value: 'supplies', label: 'Supplies' },
        { value: 'freight', label: 'Freight' },
        { value: 'fuel', label: 'Fuel' },
        { value: 'postage', label: 'Postage' },
        { value: 'meals', label: 'Meals' },
        { value: 'repairs', label: 'Repairs' },
        { value: 'other', label: 'Other' },
    ],
    paid_in: [
        { value: 'fee_received', label: 'Fee received' },
        { value: 'vendor_refund', label: 'Vendor refund' },
        { value: 'other', label: 'Other' },
    ],
};

export interface MovementForm {
    kind: MovementKind;
    amount: string;
    reason: string;
    note: string;
    takenBy: string;
}

export type MovementPlan =
    | { ok: true; request: MovementRequest }
    | { ok: false; field: 'amount' | 'reason' | 'note' | 'taken_by'; message: string };

export const isPaidKind = (kind: MovementKind): kind is 'paid_in' | 'paid_out' => kind === 'paid_in' || kind === 'paid_out';

export function kindLabel(kind: MovementKind): string {
    return MOVEMENT_KINDS.find((k) => k.kind === kind)?.label ?? kind;
}

/** What Record sends, or the first thing still missing from the form. */
export function planMovement(form: MovementForm): MovementPlan {
    const cents = dollarsToCents(form.amount);
    if (isNaN(cents) || cents <= 0) {
        return { ok: false, field: 'amount', message: 'Enter the amount, more than zero.' };
    }
    const request: MovementRequest = { kind: form.kind, amount_cents: cents };
    if (isPaidKind(form.kind)) {
        const known = MOVEMENT_REASONS[form.kind].some((r) => r.value === form.reason);
        if (!known) return { ok: false, field: 'reason', message: 'Choose a reason.' };
        request.reason_code = form.reason as MovementReason;
        if (form.reason === 'other') {
            if (form.note.trim() === '') return { ok: false, field: 'note', message: 'Say what the other reason is.' };
            request.note = form.note.trim();
        } else if (form.note.trim() !== '') {
            request.note = form.note.trim();
        }
    }
    if (form.kind === 'pickup') {
        if (form.takenBy.trim() === '') {
            return { ok: false, field: 'taken_by', message: 'Name the second person taking the cash.' };
        }
        request.taken_by = form.takenBy.trim();
    }
    return { ok: true, request };
}
