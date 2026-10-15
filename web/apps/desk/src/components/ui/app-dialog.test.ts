// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest'
import { confirmDialog, promptDialog, type GableAppDialog } from './app-dialog.ts'
import { mount, update, q, flush } from '../../test/dom.ts'
import type { GableSelect } from './gable-select.ts'
import './gable-select.ts'

function dialogEl(): GableAppDialog {
  return q<GableAppDialog>(document.body, 'gable-app-dialog')
}

function dialogElSafe(): GableAppDialog | null {
  return document.body.querySelector<GableAppDialog>('gable-app-dialog')
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

describe('a select pick that opens the dialog', () => {
  async function pickerThatAsks(): Promise<GableSelect> {
    const el = await mount<GableSelect>('gable-select', {
      options: [
        { value: 'a', label: 'Apple' },
        { value: 'b', label: 'Berry' },
      ],
    })
    el.addEventListener('change', () => { void confirmDialog({ title: 'Leave the open sale?' }) })
    return el
  }

  function focusInsideDialog(): boolean {
    const dlg = document.body.querySelector('gable-app-dialog')
    return dlg !== null && dlg.contains(document.activeElement)
  }

  it('takes the focus when the pick was a click', async () => {
    const el = await pickerThatAsks()
    q<HTMLButtonElement>(el, 'button').click()
    await update(el, {})
    q<HTMLElement>(el, '[role="option"]').click()
    await flush()
    await settleDialog()
    expect(focusInsideDialog(), 'focus must be inside the dialog, not back on the select behind the veil').toBe(true)
    clickConfirm()
    await flush()
  })

  it('takes the focus when the pick was Enter', async () => {
    const el = await pickerThatAsks()
    const button = q<HTMLButtonElement>(el, 'button')
    button.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }))
    await update(el, {})
    button.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    await flush()
    await settleDialog()
    expect(focusInsideDialog(), 'focus must be inside the dialog, not back on the select behind the veil').toBe(true)
    clickConfirm()
    await flush()
  })
})

describe('gable-app-dialog over another dialog', () => {
  it('dims the modal behind it with a scrim at the modal\'s own radius', async () => {
    // The page's modal (say the fleet's Edit Vehicle) is a card on its own
    // veil; it carries data-modal-panel so a question asked over it can find it.
    const veil = document.createElement('div')
    veil.className = 'fixed inset-0 z-[60]'
    const panel = document.createElement('div')
    panel.setAttribute('data-modal-panel', '')
    panel.style.borderRadius = '16px'
    veil.appendChild(panel)
    document.body.appendChild(veil)
    const result = confirmDialog({ title: 'Delete this vehicle?', tone: 'danger' })
    const el = await settleDialog()
    const scrim = q<HTMLElement>(el, '[data-dialog-scrim]')
    expect(scrim.style.position).toBe('fixed')
    expect(scrim.style.borderRadius).toBe('16px')
    expect(scrim.className).toContain('bg-backdrop')
    expect(scrim.getAttribute('aria-hidden')).toBe('true')
    clickCancel()
    await expect(result).resolves.toBe(false)
    expect(dialogElSafe()?.querySelector('[data-dialog-scrim]') ?? null).toBeNull()
    veil.remove()
  })

  it('draws no scrim when no modal is open behind it', async () => {
    const result = confirmDialog({ title: 'Confirm this order?' })
    const el = await settleDialog()
    expect(el.querySelector('[data-dialog-scrim]')).toBeNull()
    clickCancel()
    await result
  })
})

describe('gable-app-dialog in the top layer', () => {
  it('renders as a native dialog element, open while the question is up', async () => {
    const result = confirmDialog({ title: 'Confirm this order?' })
    await settleDialog()
    const dlg = dialogEl().querySelector('dialog')
    expect(dlg, 'the app dialog renders a native <dialog> so the browser puts it in the top layer').not.toBeNull()
    expect(dlg!.hasAttribute('open') || (dlg as HTMLDialogElement).open).toBe(true)
    clickCancel()
    await expect(result).resolves.toBe(false)
    expect(dialogElSafe()?.querySelector('dialog') ?? null).toBeNull()
  })

  it('stays answerable when asked from inside a modal layer', async () => {
    const modal = document.createElement('div')
    modal.className = 'fixed inset-0 z-[60]'
    document.body.appendChild(modal)
    const result = confirmDialog({ title: 'Delete this vehicle?', tone: 'danger' })
    await settleDialog()
    const dlg = dialogEl().querySelector('dialog')
    expect(dlg).not.toBeNull()
    clickConfirm()
    await expect(result).resolves.toBe(true)
    modal.remove()
  })
})

describe('gable-app-dialog panel clicks', () => {
  it('keeps a typed prompt open when the click lands on the panel itself', async () => {
    const result = promptDialog({ title: 'Void this sale', label: 'Reason', required: true })
    await settleDialog()
    await typeIntoPrompt('entered by mistake')
    const el = dialogEl()
    const dlg = q<HTMLElement>(el, 'dialog')
    // The padding lives on the panel, the dialog's own box carries none, so a
    // click on the panel's edge is a content click, never a backdrop click.
    expect(dlg.className).toContain('p-0')
    q<HTMLElement>(dlg, 'div').dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    await flush()
    expect(dialogElSafe()).not.toBeNull()
    expect(q<HTMLInputElement>(el, 'input').value).toBe('entered by mistake')
    clickCancel()
    await expect(result).resolves.toBeNull()
  })

  it('a click on the dialog element itself is the backdrop and cancels', async () => {
    const result = confirmDialog({ title: 'Confirm this order?' })
    await settleDialog()
    q<HTMLElement>(dialogEl(), 'dialog').dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    await expect(result).resolves.toBe(false)
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
