// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { components } from '@gable/api-client'

export type PortalMe = components['schemas']['PortalMe']
export type PortalCapabilities = components['schemas']['PortalCapabilities']
export type PortalConfig = components['schemas']['PortalConfig']
export type PortalToday = components['schemas']['PortalToday']
export type PortalOrder = components['schemas']['PortalOrder']
export type PortalInvoice = components['schemas']['PortalInvoice']
export type PortalDelivery = components['schemas']['PortalDelivery']
export type PortalQuote = components['schemas']['PortalQuote']
export type PortalCatalogProduct = components['schemas']['PortalCatalogProduct']
export type PortalCatalogDetail = components['schemas']['PortalCatalogDetail']
export type PortalCart = components['schemas']['PortalCart']
export type PortalOrderTimeline = Omit<components['schemas']['PortalOrderTimeline'], 'pod'> & {
  pod: components['schemas']['PortalTimelinePOD'] | null
}
export type PortalScheduleSlot = components['schemas']['PortalScheduleSlot']
export type PortalScheduleHold = components['schemas']['PortalScheduleHold']
export type CustomerQuoteView = components['schemas']['CustomerQuoteView']
export type CustomerQuoteSummary = components['schemas']['CustomerQuoteSummary']
export type CustomerQuoteInput = components['schemas']['CustomerQuoteInput']
export type CustomerQuotePublicView = components['schemas']['CustomerQuotePublicView']
export type CustomerQuoteConfirmPreview = components['schemas']['CustomerQuoteConfirmPreview']
export type ProjectPage = components['schemas']['ProjectPage']
export type Project = components['schemas']['Project']
