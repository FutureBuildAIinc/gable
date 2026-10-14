// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest'
import { confirmDialog, promptDialog, type GableAppDialog } from './app-dialog.ts'
import { q, flush } from '../../test/dom.ts'

function dialogEl(): GableAppDialog {
  return q<GableAppDialog>(document.body, 'gable-app-dialog')
}

async function settleDialog(): Promise<GableAppDialog> {
  const el = dialogEl()
  await el.updateComplete
  await flush()
  return el
}

function pressKey(key: string, shiftKey = false) {
  const el = dialogEl()
  const panel = q<HTMLElement>(el, '[aria-modal="true"]')
  panel.dispatchEvent(new KeyboardEvent('keydown', { key, shiftKey, bubbles: true, cancelable: true }))
}

async function typeIntoPrompt(value: string) {
  const el = dialogEl()
  const input = q<HTMLInputElement>(el, 'input')
  input.value = value
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await el.updateComplete
}

function clickConfirm() {
  q<HTMLButtonElement>(dialogEl(), '[data-dialog-confirm]').click()
}

function clickCancel() {
  q<HTMLButtonElement>(dialogEl(), '[data-dialog-cancel]').click()
}

describe('gable-app-dialog confirmDialog', () => {
  it('resolves true when Confirm is clicked', async () => {
    const result = confirmDialog({ title: 'Confirm this order?' })
    await settleDialog()
    clickConfirm()
    await expect(result).resolves.toBe(true)
    expect(document.body.querySelector('gable-app-dialog')).toBeNull()
  })

  it('resolves false when Cancel is clicked', async () => {
    const result = confirmDialog({ title: 'Confirm this order?' })
    await settleDialog()
    clickCancel()
    await expect(result).resolves.toBe(false)
  })

  it('resolves false on Escape', async () => {
    const result = confirmDialog({ title: 'Confirm this order?' })
    await settleDialog()
    pressKey('Escape')
    await expect(result).resolves.toBe(false)
  })

  it('resolves false when the veil is clicked', async () => {
    const result = confirmDialog({ title: 'Confirm this order?' })
    const el = await settleDialog()
    q<HTMLElement>(el, '.backdrop-blur-sm').click()
    await expect(result).resolves.toBe(false)
  })

  it('is an alertdialog labelled by its title and described by its body', async () => {
    const result = confirmDialog({ title: 'Remove this line?', body: 'It cannot be undone.' })
    const el = await settleDialog()
    const panel = q<HTMLElement>(el, '[role="alertdialog"]')
    expect(panel.getAttribute('aria-modal')).toBe('true')
    const titleId = panel.getAttribute('aria-labelledby')!
    expect(el.querySelector(`#${titleId}`)!.textContent).toBe('Remove this line?')
    const bodyId = panel.getAttribute('aria-describedby')!
    expect(el.querySelector(`#${bodyId}`)!.textContent).toBe('It cannot be undone.')
    clickCancel()
    await result
  })

  it('uses the dialog role for a prompt', async () => {
    const result = promptDialog({ title: 'Cancel this order', label: 'Reason' })
    const el = await settleDialog()
    expect(el.querySelector('[role="dialog"]')).not.toBeNull()
    expect(el.querySelector('[role="alertdialog"]')).toBeNull()
    pressKey('Escape')
    await result
  })

  it('focuses Cancel for a danger tone and Confirm otherwise', async () => {
    const danger = confirmDialog({ title: 'Void this sale?', tone: 'danger' })
    await settleDialog()
    expect(document.activeElement).toBe(q(dialogEl(), '[data-dialog-cancel]'))
    clickCancel()
    await danger

    const plain = confirmDialog({ title: 'Confirm this order?' })
    await settleDialog()
    expect(document.activeElement).toBe(q(dialogEl(), '[data-dialog-confirm]'))
    clickConfirm()
    await plain
  })

  it('keeps Tab inside the dialog', async () => {
    const result = confirmDialog({ title: 'Confirm this order?' })
    await settleDialog()
    const confirmButton = q<HTMLElement>(dialogEl(), '[data-dialog-confirm]')
    const cancelButton = q<HTMLElement>(dialogEl(), '[data-dialog-cancel]')
    confirmButton.focus()
    pressKey('Tab')
    expect(document.activeElement).toBe(cancelButton)
    pressKey('Tab', true)
    expect(document.activeElement).toBe(confirmButton)
    clickCancel()
    await result
  })

  it('Enter confirms when the focus is not on Cancel, and cancels there', async () => {
    const confirmed = confirmDialog({ title: 'Confirm this order?' })
    await settleDialog()
    q<HTMLElement>(dialogEl(), '[data-dialog-confirm]').focus()
    pressKey('Enter')
    await expect(confirmed).resolves.toBe(true)

    const cancelled = confirmDialog({ title: 'Confirm this order?' })
    await settleDialog()
    q<HTMLElement>(dialogEl(), '[data-dialog-cancel]').focus()
    pressKey('Enter')
    await expect(cancelled).resolves.toBe(false)
  })

  it('returns the focus to the control that opened it', async () => {
    const opener = document.createElement('button')
    document.body.appendChild(opener)
    opener.focus()
    const result = confirmDialog({ title: 'Confirm this order?' })
    await settleDialog()
    expect(document.activeElement).not.toBe(opener)
    clickCancel()
    await result
    expect(document.activeElement).toBe(opener)
    opener.remove()
  })

  it('settles a dialog still open when a new one is asked for', async () => {
    const first = confirmDialog({ title: 'First?' })
    await settleDialog()
    const second = confirmDialog({ title: 'Second?' })
    await expect(first).resolves.toBe(false)
    await settleDialog()
    expect(document.body.querySelectorAll('gable-app-dialog').length).toBe(1)
    clickConfirm()
    await expect(second).resolves.toBe(true)
  })
})

describe('gable-app-dialog promptDialog', () => {
  it('resolves the typed text on Confirm', async () => {
    const result = promptDialog({ title: 'Cancel this order', label: 'Reason' })
    await settleDialog()
    await typeIntoPrompt('Customer changed their mind')
    clickConfirm()
    await expect(result).resolves.toBe('Customer changed their mind')
  })

  it('resolves null on Cancel and on Escape', async () => {
    const cancelled = promptDialog({ title: 'Cancel this order', label: 'Reason' })
    await settleDialog()
    clickCancel()
    await expect(cancelled).resolves.toBeNull()

    const escaped = promptDialog({ title: 'Cancel this order', label: 'Reason' })
    await settleDialog()
    pressKey('Escape')
    await expect(escaped).resolves.toBeNull()
  })

  it('focuses the field', async () => {
    const result = promptDialog({ title: 'Cancel this order', label: 'Reason' })
    await settleDialog()
    expect(document.activeElement).toBe(q(dialogEl(), 'input'))
    pressKey('Escape')
    await result
  })

  it('keeps Confirm off until text is typed when required', async () => {
    const result = promptDialog({ title: 'Void this sale', label: 'Reason', required: true })
    await settleDialog()
    expect(q<HTMLButtonElement>(dialogEl(), '[data-dialog-confirm]').disabled).toBe(true)
    pressKey('Enter')
    await flush()
    expect(document.body.querySelector('gable-app-dialog')).not.toBeNull()
    await typeIntoPrompt('   ')
    expect(q<HTMLButtonElement>(dialogEl(), '[data-dialog-confirm]').disabled).toBe(true)
    await typeIntoPrompt('Entered by mistake')
    expect(q<HTMLButtonElement>(dialogEl(), '[data-dialog-confirm]').disabled).toBe(false)
    pressKey('Enter')
    await expect(result).resolves.toBe('Entered by mistake')
  })
})
