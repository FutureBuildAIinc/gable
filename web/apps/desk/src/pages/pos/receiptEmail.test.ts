// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's receipt email answer (CP-14): the Sale complete card's note
// follows the delivery row, and only a failed send reads as an error.
import { describe, it, expect } from 'vitest'
import { receiptEmailAnswer } from './receiptEmail'
import type { EmailDelivery } from '../../services/EmailDeliveryService'
import { OUTCOME_UNKNOWN } from '../../lib/emailOutcome'

function delivery(status: EmailDelivery['status'], overrides: Partial<EmailDelivery> = {}): EmailDelivery {
  return {
    id: 'd-1',
    created_at: '2026-10-01T12:00:00Z',
    branch_id: null,
    recipient: 'billing@example.test',
    subject: 'Invoice IN-000051',
    document_type: 'invoice',
    document_id: 'i-1',
    document_number: 'IN-000051',
    status,
    sender_answer: null,
    error: null,
    sent_by: null,
    ...overrides,
  }
}

describe('receiptEmailAnswer', () => {
  it('does not call a send with an unreadable record a failure', () => {
    const answer = receiptEmailAnswer(OUTCOME_UNKNOWN)
    expect(answer.note).toContain('sent on')
    expect(answer.refused).toBe(false)
  })

  it('says sent with the address, plainly', () => {
    const answer = receiptEmailAnswer(delivery('sent'))
    expect(answer.note).toBe('Receipt sent to billing@example.test')
    expect(answer.refused).toBe(false)
  })

  it('says a logged send as email not being set up, not as an error', () => {
    const answer = receiptEmailAnswer(delivery('logged'))
    expect(answer.note).toBe('Email is not set up on this server: the receipt was not sent')
    expect(answer.refused).toBe(false)
  })

  it('says a failed send as the error it is', () => {
    const answer = receiptEmailAnswer(delivery('failed'))
    expect(answer.note).toBe('The receipt was not sent: the mail server refused or could not be reached. Try again, or print it.')
    expect(answer.refused).toBe(true)
  })

  it('says still sending when the send has not resolved', () => {
    const answer = receiptEmailAnswer(delivery('queued'))
    expect(answer.note).toBe('The receipt email is still being sent')
    expect(answer.refused).toBe(false)
  })
})
