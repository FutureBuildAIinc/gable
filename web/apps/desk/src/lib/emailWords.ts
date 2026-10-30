// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Plain words for what an emailed document's delivery row says (CP-14): sent
// means the mail server took it, logged means email is not set up on this
// server and nothing left the process, failed means the mail server refused
// or could not be reached. A send still queued when the desk stops waiting is
// said as still sending, never as success.

import type { EmailDelivery, EmailDeliveryStatus } from '../services/EmailDeliveryService.ts';

type DocumentNoun = 'receipt' | 'invoice';

/** The sentence the counter and the invoice page say once a send resolves. */
export function emailOutcomeWords(delivery: EmailDelivery | undefined, noun: DocumentNoun): string {
    if (!delivery || delivery.status === 'queued') {
        return `The ${noun} email is still being sent`;
    }
    if (delivery.status === 'sent') {
        const to = delivery.recipient ? ` to ${delivery.recipient}` : '';
        return `${noun === 'receipt' ? 'Receipt' : 'Invoice'} sent${to}`;
    }
    if (delivery.status === 'logged') {
        return `Email is not set up on this server: the ${noun} was not sent`;
    }
    return `The ${noun} was not sent: the mail server refused or could not be reached. Try again, or print it.`;
}

/** The short words the email sends list carries beside each row. */
export function emailDeliveryWords(status: EmailDeliveryStatus): string {
    switch (status) {
        case 'sent':
            return 'Sent';
        case 'logged':
            return 'Not sent: email not set up';
        case 'failed':
            return 'Not sent: the mail server refused or could not be reached';
        case 'queued':
            return 'Still sending';
    }
}
