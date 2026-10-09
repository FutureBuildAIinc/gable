// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The payment modal is the one place a user types money into the ERP, so it
 * straddles the cents/dollars boundary in both directions: it receives
 * `amount-due` as int64 cents, shows dollars, and must post integer cents
 * back, applied to the invoice in the same act. A missed conversion in either
 * direction posts a payment 100x wrong.
 */
import { describe, it, expect, vi } from 'vitest'
import './PaymentModal'
import type { GablePaymentModal } from './PaymentModal'
import { mount, update, q } from '../../test/dom'
import type { CreatePaymentRequest } from '../../types/payment'

function open(amountDueCents: number) {
  return mount<GablePaymentModal>('gable-payment-modal', {
    isOpen: true,
    invoiceId: 'inv-1',
    customerId: 'cust-1',
    amountDue: amountDueCents,
  })
}

function amountInput(el: GablePaymentModal): HTMLInputElement {
  return q<HTMLInputElement>(el, 'input[type="number"]')
}

/** Type into the amount field the way a user would. */
async function typeAmount(el: GablePaymentModal, dollars: string) {
  const input = amountInput(el)
  input.value = dollars
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await update(el, {})
}

/** Submit the form and return the `save` payload it dispatched. */
function submit(el: GablePaymentModal): CreatePaymentRequest {
  const saved = vi.fn()
  el.addEventListener('save', (e) => saved((e as CustomEvent<CreatePaymentRequest>).detail))
  q<HTMLFormElement>(el, 'form').dispatchEvent(
    new Event('submit', { bubbles: true, cancelable: true }),
  )
  expect(saved).toHaveBeenCalledTimes(1)
  return saved.mock.calls[0][0] as CreatePaymentRequest
}

describe('gable-payment-modal — cents in', () => {
  it('renders 7388 cents as 73.88 dollars in the amount field', async () => {
    const el = await open(7388)
    expect(amountInput(el).value).toBe('73.88')
  })

  it('renders the docs\' other value as 7388.07, not 738807', async () => {
    const el = await open(738_807)
    expect(amountInput(el).value).toBe('7388.07')
  })

  it('renders a zero balance as 0', async () => {
    const el = await open(0)
    expect(amountInput(el).value).toBe('0')
  })

  it('re-derives the dollar amount when amount-due changes', async () => {
    const el = await open(7388)
    await update(el, { amountDue: 12_345 })
    expect(amountInput(el).value).toBe('123.45')
  })
})

describe('gable-payment-modal — cents out', () => {
  it('posts the prefilled balance back as the same integer cents it received', async () => {
    const el = await open(7388)
    expect(submit(el).amount_cents).toBe(7388)
  })

  it('converts a typed dollar amount to cents', async () => {
    const el = await open(7388)
    await typeAmount(el, '50.25')
    expect(submit(el).amount_cents).toBe(5025)
  })

  it('rounds float multiplication error rather than truncating', async () => {
    // 19.99 * 100 === 1998.9999999999998
    const el = await open(0)
    await typeAmount(el, '19.99')
    expect(submit(el).amount_cents).toBe(1999)
  })

  it('never posts a fractional cent', async () => {
    const el = await open(0)
    await typeAmount(el, '10.999')
    const { amount_cents } = submit(el)
    expect(Number.isInteger(amount_cents)).toBe(true)
    expect(amount_cents).toBe(1100)
  })

  it('applies the payment to the invoice in the same act, capped at what it owes', async () => {
    const el = await open(2500)
    const payload = submit(el)
    expect(payload.customer_id).toBe('cust-1')
    expect(payload.applications).toEqual([{ invoice_id: 'inv-1', amount_cents: 2500 }])
  })

  it('holds an overpayment as unapplied cash: the application is capped at the open amount', async () => {
    const el = await open(2500)
    await typeAmount(el, '40.00')
    const payload = submit(el)
    expect(payload.amount_cents).toBe(4000)
    expect(payload.applications).toEqual([{ invoice_id: 'inv-1', amount_cents: 2500 }])
  })

  it('carries the method and reference through, lowercase as the route takes them', async () => {
    const el = await open(2500)
    const select = q<HTMLSelectElement>(el, 'select')
    select.value = 'check'
    select.dispatchEvent(new Event('change', { bubbles: true }))
    await update(el, {})

    const payload = submit(el)
    expect(payload.method).toBe('check')
    expect(payload.amount_cents).toBe(2500)
  })

  it('emits close alongside save so the caller can dismiss it', async () => {
    const el = await open(2500)
    const closed = vi.fn()
    el.addEventListener('close', closed)
    q<HTMLFormElement>(el, 'form').dispatchEvent(
      new Event('submit', { bubbles: true, cancelable: true }),
    )
    expect(closed).toHaveBeenCalledTimes(1)
  })
})

describe('gable-payment-modal — visibility', () => {
  it('renders nothing while closed', async () => {
    const el = await mount<GablePaymentModal>('gable-payment-modal', { isOpen: false })
    expect(el.querySelector('form')).toBeNull()
    expect(el.querySelector('[role="dialog"]')).toBeNull()
  })

  it('renders an accessible dialog when opened', async () => {
    const el = await open(1000)
    const dialog = q(el, '[role="dialog"]')
    expect(dialog.getAttribute('aria-modal')).toBe('true')
    expect(dialog.getAttribute('aria-labelledby')).toBe('payment-modal-title')
  })
})
