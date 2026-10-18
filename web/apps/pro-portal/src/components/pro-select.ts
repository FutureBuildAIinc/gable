// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing, type TemplateResult } from 'lit'
import { customElement, property, state } from 'lit/decorators.js'
import { icon } from '@gable/design-system'
import { ChevronDown } from 'lucide'

export interface ProSelectOption {
  value: string
  label: string
  disabled?: boolean
}

let selectSeq = 0

@customElement('pro-select')
export class ProSelect extends LitElement {
  createRenderRoot() { return this }

  @property({ type: Array }) options: ProSelectOption[] = []
  @property({ type: String }) value = ''
  @property({ type: String }) placeholder = ''
  @property({ type: String }) label = ''
  @property({ type: String, attribute: 'aria-label' }) selectAriaLabel = ''
  @property({ type: String }) buttonClass = ''
  @property({ type: Boolean }) disabled = false

  @state() private _open = false
  @state() private _activeIndex = -1
  @state() private _openUpward = false
  @state() private _listStyle = ''

  private _ids = `pro-select-${++selectSeq}`
  private _typeahead = ''
  private _typeaheadAt = 0
  private _onDocClick = (e: Event) => {
    if (!this.contains(e.target as Node)) this._close()
  }

  disconnectedCallback(): void {
    super.disconnectedCallback()
    document.removeEventListener('click', this._onDocClick, true)
  }

  override click(): void {
    const btn = this.querySelector<HTMLButtonElement>('button[role="combobox"]')
    if (btn) btn.click()
    else super.click()
  }

  private get effectiveDisabled(): boolean {
    return this.disabled || this.closest('fieldset:disabled') !== null
  }

  private get effectiveLabel(): string {
    return this.label || this.selectAriaLabel
  }

  get selectedOption(): ProSelectOption | undefined {
    return this.options.find((o) => o.value === this.value) ?? this.options[0]
  }

  private _enabledIndexes(): number[] {
    return this.options.map((o, i) => (o.disabled ? -1 : i)).filter((i) => i >= 0)
  }

  private _openList(): void {
    if (this.effectiveDisabled || this._open) return
    const rect = this.querySelector('button')?.getBoundingClientRect() ?? this.getBoundingClientRect()
    const below = window.innerHeight - rect.bottom
    const above = rect.top
    this._openUpward = below < 240 && above > below
    this._listStyle =
      `position:fixed;margin:0;left:${rect.left}px;width:${rect.width}px;` +
      (this._openUpward ? `top:auto;bottom:${window.innerHeight - rect.top}px;` : `bottom:auto;top:${rect.bottom}px;`)
    const selected = this.options.findIndex((o) => o.value === this.value && !o.disabled)
    this._activeIndex = selected >= 0 ? selected : (this._enabledIndexes()[0] ?? -1)
    this._open = true
    document.addEventListener('click', this._onDocClick, true)
    void this.updateComplete.then(() => {
      const list = this.querySelector<HTMLElement>('[role="listbox"]')
      if (list && typeof list.showPopover === 'function') {
        try {
          list.showPopover()
        } catch {
          // already shown
        }
      }
      this._scrollActiveIntoView()
    })
  }

  private _close(restoreFocus = false): void {
    if (!this._open) return
    this._open = false
    const list = this.querySelector<HTMLElement>('[role="listbox"]')
    if (list && typeof list.hidePopover === 'function') {
      try {
        list.hidePopover()
      } catch {
        // not shown
      }
    }
    document.removeEventListener('click', this._onDocClick, true)
    if (restoreFocus) {
      this.querySelector<HTMLElement>('button')?.focus()
    }
  }

  private _toggle(): void {
    if (this._open) {
      this._close(true)
    } else {
      this._openList()
    }
  }

  private _moveActive(step: number): void {
    const enabled = this._enabledIndexes()
    if (enabled.length === 0) return
    const at = enabled.indexOf(this._activeIndex)
    const next = at < 0 ? (step > 0 ? 0 : enabled.length - 1) : (at + step + enabled.length) % enabled.length
    this._activeIndex = enabled[next] ?? -1
    this._scrollActiveIntoView()
  }

  private _moveActiveToEnd(last: boolean): void {
    const enabled = this._enabledIndexes()
    if (enabled.length === 0) return
    this._activeIndex = (last ? enabled[enabled.length - 1] : enabled[0]) ?? -1
    this._scrollActiveIntoView()
  }

  private _typeaheadTo(char: string): void {
    const now = Date.now()
    this._typeahead = now - this._typeaheadAt > 600 ? char : this._typeahead + char
    this._typeaheadAt = now
    const needle = this._typeahead.toLowerCase()
    const enabled = this._enabledIndexes()
    const start = enabled.indexOf(this._activeIndex)
    for (let step = 1; step <= enabled.length; step++) {
      const index = enabled[(start + step + enabled.length) % enabled.length]
      if (index !== undefined && this.options[index]?.label.toLowerCase().startsWith(needle)) {
        if (this._open) {
          this._activeIndex = index
          this._scrollActiveIntoView()
        } else {
          this._choose(index)
        }
        return
      }
    }
  }

  private _choose(index: number): void {
    const option = this.options[index]
    if (!option || option.disabled) return
    const changed = option.value !== this.value
    this.value = option.value
    this._close(true)
    if (changed) {
      this.dispatchEvent(new Event('change', { bubbles: true, composed: true }))
    }
  }

  private _scrollActiveIntoView(): void {
    const el = this.querySelector<HTMLElement>(`#${this._ids}-opt-${this._activeIndex}`)
    if (el && typeof el.scrollIntoView === 'function') {
      try {
        el.scrollIntoView({ block: 'nearest' })
      } catch {
        // best effort in test environments
      }
    }
  }

  private _onButtonKeydown(e: KeyboardEvent): void {
    if (this.effectiveDisabled) return
    if (!this._open) {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Enter' || e.key === ' ') {
        e.preventDefault()
        this._openList()
      } else if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
        this._typeaheadTo(e.key)
      }
      return
    }
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        this._moveActive(1)
        break
      case 'ArrowUp':
        e.preventDefault()
        this._moveActive(-1)
        break
      case 'Home':
        e.preventDefault()
        this._moveActiveToEnd(false)
        break
      case 'End':
        e.preventDefault()
        this._moveActiveToEnd(true)
        break
      case 'Enter':
      case ' ':
        e.preventDefault()
        if (this._activeIndex >= 0) this._choose(this._activeIndex)
        break
      case 'Escape':
        e.preventDefault()
        e.stopPropagation()
        this._close(true)
        break
      case 'Tab':
        this._close()
        break
      default:
        if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
          e.preventDefault()
          this._typeaheadTo(e.key)
        }
    }
  }

  private _renderOption(option: ProSelectOption, index: number): TemplateResult {
    const active = index === this._activeIndex
    const selected = option.value === this.value
    return html`
      <div
        id="${this._ids}-opt-${index}"
        role="option"
        aria-selected=${selected ? 'true' : 'false'}
        aria-disabled=${option.disabled ? 'true' : 'false'}
        class="cursor-pointer px-3 py-2 text-sm ${option.disabled
          ? 'cursor-not-allowed text-zinc-400 opacity-50'
          : active
            ? 'bg-gable-green-tint font-medium text-gable-green'
            : selected
              ? 'font-medium text-gable-green'
              : 'text-white'}"
        @click=${(e: Event) => {
          e.preventDefault()
          e.stopPropagation()
          this._choose(index)
        }}
        @mouseenter=${() => { if (!option.disabled) this._activeIndex = index }}
      >${option.label}</div>
    `
  }

  private _renderList(): TemplateResult {
    const label = this.effectiveLabel
    return html`
      <div
        id="${this._ids}-list"
        role="listbox"
        aria-label=${label || nothing}
        @click=${(e: Event) => e.preventDefault()}
        popover="manual"
        style=${this._listStyle}
        class="z-50 max-h-60 overflow-y-auto rounded-sm border border-zinc-400 bg-surface-2 py-1 shadow-elevation-3 ${this._openUpward ? 'mb-1' : 'mt-1'}"
      >
        ${this.options.length > 0
          ? this.options.map((option, index) => this._renderOption(option, index))
          : html`<div role="presentation" class="px-3 py-2 text-sm text-zinc-400">No options</div>`}
      </div>
    `
  }

  render(): TemplateResult {
    const chosen = this.selectedOption
    const label = this.effectiveLabel
    const activeDesc = this._open && this._activeIndex >= 0 ? `${this._ids}-opt-${this._activeIndex}` : nothing
    return html`
      <div class="relative">
        <button
          type="button"
          role="combobox"
          aria-haspopup="listbox"
          aria-expanded=${this._open ? 'true' : 'false'}
          aria-controls="${this._ids}-list"
          aria-activedescendant=${activeDesc}
          aria-label=${label || nothing}
          ?disabled=${this.disabled}
          @click=${this._toggle}
          @keydown=${this._onButtonKeydown}
          class="pro-select flex items-center justify-between gap-2 text-left ${this.buttonClass}"
        >
          <span id="${this._ids}-value" class="truncate">${chosen ? chosen.label : this.placeholder}</span>
          ${icon(ChevronDown, 16, 'shrink-0 text-zinc-400')}
        </button>
        ${this._open ? this._renderList() : nothing}
      </div>
    `
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'pro-select': ProSelect
  }
}
