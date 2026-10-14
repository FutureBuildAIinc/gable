// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Driving the app's own controls (components/ui/app-dialog.ts and
 * gable-select.ts) from e2e specs: the browser's dialog handlers and
 * selectOption do not reach them.
 */
import type { Locator, Page } from '@playwright/test';

/** The gable-select host for a locator pointing at it or at its button. */
function selectHost(control: Locator): Locator {
    return control.locator('xpath=ancestor-or-self::gable-select').first();
}

interface GableSelectLike {
    options: { value: string; label: string }[];
}

/** Pick an option of the app's own select by its visible label. */
export async function pickOption(control: Locator, option: string | RegExp) {
    const host = selectHost(control);
    await host.getByRole('button').first().click();
    await host.getByRole('option', { name: option, exact: true }).click();
}

/** Pick an option of the app's own select by its value, reading the labels from the component. */
export async function pickOptionByValue(control: Locator, value: string) {
    const host = selectHost(control);
    const label = await host.evaluate((el, v) => {
        const options = (el as unknown as GableSelectLike).options;
        return options.find((o) => o.value === v)?.label ?? null;
    }, value);
    if (label === null) throw new Error(`gable-select has no option with value ${value}`);
    await pickOption(host, label);
}

/** Confirm the app's own dialog over the page (its confirm button's label, or Cancel). */
export async function answerAppDialog(page: Page, confirmLabel: string | RegExp = 'Confirm') {
    const dialog = page.locator('gable-app-dialog [aria-modal="true"]');
    await dialog.getByRole('button', { name: confirmLabel, exact: true }).click();
}

/** Fill the field of the app's own prompt dialog over the page. */
export async function fillAppDialogPrompt(page: Page, value: string) {
    await page.locator('gable-app-dialog [aria-modal="true"] input').fill(value);
}
