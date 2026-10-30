// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The email outcome words (CP-14): each delivery status said plainly for a
// receipt and for an invoice, and the short words the sends list carries.
import { describe, it, expect } from 'vitest'
import { emailOutcomeWords, emailDeliveryWords } from './emailWords'
import type { EmailDelivery } from '../services/EmailDeliveryService'

function delivery(overrides: Partial<EmailDelivery> = {}): EmailDelivery {
  return {
    id: 'd-1',
    created_at: '2026-10-01T12:00:00Z',
    branch_id: null,
    recipient: 'billing@example.test',
    subject: 'Invoice IN-000042',
    document_type: 'invoice',
    document_id: 'i-1',
    document_number: 'IN-000042',
    status: 'sent',
    sender_answer: null,
    error: null,
    sent_by: null,
    ...overrides,
  }
}

describe('emailOutcomeWords', () => {
  it('says sent with the address it went to, for a receipt and for an invoice', () => {
    expect(emailOutcomeWords(delivery(), 'receipt')).toBe('Receipt sent to billing@example.test')
    expect(emailOutcomeWords(delivery(), 'invoice')).toBe('Invoice sent to billing@example.test')
  })

  it('says logged as email not being set up, never as success', () => {
    expect(emailOutcomeWords(delivery({ status: 'logged' }), 'receipt'))
      .toBe('Email is not set up on this server: the receipt was not sent')
    expect(emailOutcomeWords(delivery({ status: 'logged' }), 'invoice'))
      .toBe('Email is not set up on this server: the invoice was not sent')
  })

  it('says failed as the mail server refusing or being unreachable, with the retry', () => {
    expect(emailOutcomeWords(delivery({ status: 'failed' }), 'receipt'))
      .toBe('The receipt was not sent: the mail server refused or could not be reached. Try again, or print it.')
  })

  it('says still sending for a send not resolved and for a row that never appeared', () => {
    expect(emailOutcomeWords(delivery({ status: 'queued' }), 'receipt')).toBe('The receipt email is still being sent')
    expect(emailOutcomeWords(undefined, 'invoice')).toBe('The invoice email is still being sent')
  })
})

describe('emailDeliveryWords', () => {
  it('carries the list row words for every status', () => {
    expect(emailDeliveryWords('sent')).toBe('Sent')
    expect(emailDeliveryWords('logged')).toBe('Not sent: email not set up')
    expect(emailDeliveryWords('failed')).toBe('Not sent: the mail server refused or could not be reached')
    expect(emailDeliveryWords('queued')).toBe('Still sending')
  })
})
