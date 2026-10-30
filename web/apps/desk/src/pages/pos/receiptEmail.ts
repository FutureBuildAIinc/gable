// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's receipt email answer (CP-14): the Sale complete card's note
// says what the delivery row says, never success on the 202 alone. A send the
// log sender took (email not set up on this server) and a send still in
// flight are states of the server, said plainly, not errors; a failed send is
// an error the clerk can act on by emailing again or printing.

import type { EmailDelivery } from '../../services/EmailDeliveryService.ts';
import { OUTCOME_UNKNOWN } from '../../lib/emailOutcome.ts';
import { emailOutcomeWords } from '../../lib/emailWords.ts';

export interface ReceiptEmailAnswer {
    note: string;
    refused: boolean;
}

export function receiptEmailAnswer(delivery: EmailDelivery | typeof OUTCOME_UNKNOWN | undefined): ReceiptEmailAnswer {
    return {
        note: emailOutcomeWords(delivery, 'receipt'),
        refused: delivery !== OUTCOME_UNKNOWN && delivery?.status === 'failed',
    };
}
