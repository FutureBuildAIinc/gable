<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Products

A product is the master record for an item the yard sells. It carries
the SKU, the description, the stocking unit, the base price, the
average unit cost, the target margin, the commission rate, the
vendor, the parametric geometry, the lead time and the reorder point
and quantity. The product is the source of truth for inventory levels,
pricing and PIM (product information management) content, and the
first place a kit's component list is set.

The Go code is in `core/internal/product/`. The PIM detail is in
`core/internal/pim/`. The migration that brought the table onto the
contract is `core/migrations/093_catalog_pricing_wire_contract.sql`.

## What it does in a yard

The yard's catalog is the list of SKUs it stocks and prices. A
product carries the unit it is stocked in, the price a customer at
list pays per stocking unit, the cost on the average, the dimensions
(for freight and shelf planning), the lead time (for special orders),
and the reorder point and quantity (for purchasing). A kit is a
product whose `is_kit` is true and whose components live in
`product_kit_components`; a sale of a kit explodes into one kit line
and one component line per component, with the kit priced as a whole
and the components priced at zero.

The PIM (product information management) is the long form: a marketing
description, SEO text, media (photos, videos), and collateral
(sell sheets and social posts). The PIM content is what the
customer's portal renders on the product page; the marketing
generation routes call the AI to draft new collateral, descriptions
and images, then store the result on the product.

## Routes

Every route is in `core/api/fragments/product.yaml` and the registered
handles are in `core/internal/product/handler.go` and
`core/internal/pim/handler.go`. The route census (`core/api/ROUTES.txt`)
lists each one under the `products` module column (PIM routes included).

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/products` | Cursor list of products, newest first. |
| POST | `/api/v1/products` | Create a product. |
| GET | `/api/v1/products/reorder-alerts` | List products below their reorder point. |
| GET | `/api/v1/products/{id}` | One product with its base fields and stock totals. |
| GET | `/api/v1/products/{id}/detail` | The product with the embedded PIM content and media. |
| PATCH | `/api/v1/products/{id}/dimensions` | Write the parametric geometry. |
| PATCH | `/api/v1/products/{id}/lead-time` | Publish or clear the lead time. |
| PATCH | `/api/v1/products/{id}/margins` | Update the margin rules. |
| GET | `/api/v1/products/{id}/kit-components` | A kit's component list. |
| PUT | `/api/v1/products/{id}/kit-components` | Replace a kit's component list. |
| GET | `/api/v1/products/{id}/pim/content` | Get PIM content (long form description, SEO). |
| PUT | `/api/v1/products/{id}/pim/content` | Update PIM content. |
| GET | `/api/v1/products/{id}/pim/media` | List PIM media for a product. |
| DELETE | `/api/v1/products/{id}/pim/media/{mediaId}` | Delete a media item. |
| PATCH | `/api/v1/products/{id}/pim/media/{mediaId}/primary` | Set the primary media. |
| GET | `/api/v1/products/{id}/pim/collateral` | List PIM collateral. |
| DELETE | `/api/v1/products/{id}/pim/collateral/{collateralId}` | Delete a collateral item. |
| POST | `/api/v1/products/{id}/pim/generate/collateral` | Generate PIM collateral through the AI. |
| POST | `/api/v1/products/{id}/pim/generate/descriptions` | Generate PIM descriptions. |
| POST | `/api/v1/products/{id}/pim/generate/image` | Generate a PIM image. |
| POST | `/api/v1/products/{id}/pim/generate/seo` | Generate PIM SEO content. |

## The main resource

`ProductView` (see `core/api/fragments/product.yaml`
`components.schemas.ProductView`).

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The product id. |
| `sku` | text | The unique stock keeping unit. |
| `description` | text | The short description. |
| `stock_uom` | text | The stocking unit (`PCS`, `EA`, `LF`, `BF`, `MBF`, etc.). |
| `base_price_ten_thousandths` | integer | The list price per stocking unit at scale 4. |
| `average_unit_cost_ten_thousandths` | integer | The cost per stocking unit at scale 4, average across receipts. |
| `target_margin` | number | The target margin the pricing engine tries to hit. |
| `commission_rate` | number | The salesperson commission rate. |
| `vendor`, `vendor_id` | text, UUID, nullable | The default vendor. |
| `upc` | text, nullable | The Universal Product Code, when set. |
| `weight_lbs` | number | The shipping weight. |
| `length_in`, `width_in`, `height_in` | number, nullable | The dimensions, inches. |
| `stackable` | boolean, nullable | Whether the product stacks in storage. |
| `geometry_source` | text, nullable | The source of the geometry, when not manual. |
| `lead_time_days` | integer, nullable | The published lead time. |
| `reorder_point`, `reorder_qty` | decimal string | The reorder point and quantity, in the stocking unit, scale 4. |
| `on_hand`, `allocated`, `available` | decimal string | The stock totals in the stocking unit; `available` is `on_hand - allocated`. |
| `revision` | integer | Starts at 1; returned as ETag. |
| `created_at`, `updated_at` | timestamp | RFC 3339 UTC. |

The PIM content (`PimContent`) carries `short_description`,
`long_description`, `marketing_copy`, `attributes` (an object or null),
`seo_title`, `seo_description`, `seo_keywords` (array of strings or
null), `seo_slug`, plus the generation tracking fields
`last_gen_model`, `last_gen_prompt`, `last_gen_at`.

The PIM media (`PimMedia`) carries `id`, `product_id`, `media_type`,
`url`, `alt_text`, `sort_order`, `is_primary`, `status`, plus the
generation tracking fields `gen_model`, `gen_prompt`, `gen_style`,
`generated_at`.

The PIM collateral (`PimCollateral`) carries `id`, `product_id`,
`collateral_type` (`sell_sheet`, `facebook`, `instagram`,
`linkedin`, `email_blast`), `title`, `content`, `tone`, `audience`,
plus the generation tracking fields `gen_model`, `gen_prompt`,
`generated_at`.

### Money and quantity conventions

`base_price_ten_thousandths` and `average_unit_cost_ten_thousandths`
are integer prices at scale 4. The stock totals
(`on_hand`, `allocated`, `available`, `reorder_point`, `reorder_qty`)
are decimal strings with at most four fraction digits. The wire form
`UOM` is the standard codes (`PCS`, `EA`, `LF`, `BF`, `MBF`); the
product is uppercase on the wire (ADR 0001 section 6).

## Lifecycle and transitions

A product has no lifecycle of its own. The `revision` field tracks
every write; the product's PIM content, media and collateral are
versioned through the PIM service. A product that is a kit has a
component list; a kit cannot contain a kit (one level), and a write
that would create a cycle is a `400 validation_failed`. The kit
explosion is done once, at the line's create or edit on a sales
document, so a later change of the kit definition does not touch
existing documents.

The PIM generation routes are read by the desk; the AI generation is
done by the worker role. The base URL and per-task model slugs are
admin-overridable (see `core/internal/app/serve/serve.go` near the
`pimHandler.RegisterRoutes` call).

## Events the module writes

Product writes today do not write outbox events; the module's events
are PIM-side and ride the CRM and portal feeds. The PIM generation
routes do not write `audit_log` rows.

## Scopes, roles and keys

A machine key reaching the product routes needs `products:read` for
`GET` and `HEAD`, and `products:write` for every other method (ADR
0002; the segment is the first path segment under `/api/v1/`; the
PIM routes share the same segment). The user guard at the serve
layer is `admin`, `owner`, `sales`, `warehouse` for the catalog and
`admin`, `owner` for the PIM routes; the exact guards are composed in
`core/internal/app/serve/wire_branch_wall.go` at `wall.products` and
in `core/internal/app/serve/serve.go` at the
`pimHandler.RegisterRoutes` line. A key without the scope is `403
forbidden`; the audit row carries the refused scope.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 6, 7, 7a, 8, 9, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5.
- [`docs/adr/0005-sales-and-money-core.md`](../adr/0005-sales-and-money-core.md) section 2.6: the kit definition; section 2.1: the line types.
- [`docs/adr/0006-units-and-pricing.md`](../adr/0006-units-and-pricing.md) sections 6, 7.1: the product on the wire, the stock totals.

## How to try it locally

The repository's own seed and the local make targets are the only way
to exercise the module end to end. From the repository root:

```
make stack-up
make migrate
make seed
make serve
```

Then, with an inventory role bearer:

```
curl -X GET 'http://localhost:8080/api/v1/products?include=total' \
  -H 'Authorization: Bearer <token>'
```

The wire tests in `core/internal/product/wire_test.go` pin the
platform and HTTP behaviour; the goldens under
`core/internal/characterization/testdata` pin the wire shapes for the
product's scenarios.