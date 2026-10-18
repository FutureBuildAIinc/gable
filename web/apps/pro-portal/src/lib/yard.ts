// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { shortDate, timeOfDay } from './format.ts'
import type { PortalMe, PortalScheduleHold } from '../types.ts'

export function yardTimeZone(me: PortalMe, yardId?: string, yardCode?: string): string | undefined {
  const yard = me.yards.find((item) => yardId !== undefined && item.id === yardId)
    ?? me.yards.find((item) => yardCode !== undefined && item.code === yardCode)
    ?? me.yards.find((item) => item.is_primary)
  return yard?.timezone
}

export function holdWhen(me: PortalMe, hold: PortalScheduleHold): string {
  const zone = yardTimeZone(me, hold.yard_id)
  return `${shortDate(hold.start_utc, zone)} ${timeOfDay(hold.start_utc, zone)} to ${timeOfDay(hold.end_utc, zone)}`
}
