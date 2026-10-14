// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package pictures holds the one spelling of the catalog.product_images
// setting and the SQL that applies it, so every read that answers a product's
// picture path (the counter's tiles, the product wire, the inventory levels)
// reads the setting the same way: the path while the setting is "on", null
// when it is off, absent, or the product has no picture.
package pictures

import "fmt"

// Setting is the system_settings key that turns product pictures on.
const Setting = "catalog.product_images"

// FlagSQL is a scalar SQL expression that is TRUE only while the setting is
// "on". A statement holding it reads the setting row once.
const FlagSQL = `COALESCE((SELECT value = 'on' FROM system_settings WHERE key = '` + Setting + `'), FALSE)`

// ImageSQL is the SQL expression for the picture path of the products row
// aliased alias: its image_url while the setting is on, NULL otherwise and
// for a product whose path is empty.
func ImageSQL(alias string) string {
	return fmt.Sprintf(`CASE WHEN %s THEN NULLIF(%s.image_url, '') END`, FlagSQL, alias)
}
