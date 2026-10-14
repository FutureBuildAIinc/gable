<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Market check: the pricing flow's request and answer

The Market check screen (Pricing, Market check) asks an FB Connect pricing flow what a competitor's store charges
for products the dealer sells. This page is the contract between Gable and that flow. It holds no address and no
webhook id: both are dealer settings in Tech Admin, kept sealed at rest, and anyone holding the id can run the flow.

## The call

Gable's server posts, once per batch of up to three items (at most four batches at once), to

    POST {connect address}/api/v1/webhooks/{webhook id}/sync
    Content-Type: application/json

Each batch has its own timeout. Redirects are not followed; a 3xx is a failed batch. The answer is read up to 1 MB.

## The request

```json
{
  "source": "gable",
  "yard": {"name": "Kelowna Main Yard", "town": "Kelowna, BC"},
  "store": {"id": "<store id or empty>", "zip": "<store ZIP or empty>", "label": "<store label>"},
  "bandPercent": 5,
  "items": [
    {"sku": "DL_SPF_2X4_L08_S", "name": "2x4x8' SPF #2&BTR KD", "category": "Lumber", "unit": "PCS", "price": 5.42}
  ]
}
```

`price` is the yard's walk in price in dollars, a JSON number with two decimals. Cost is never sent.

## The answer

```json
{
  "store": {"id": "1234", "name": "Spokane Store"},
  "rows": [
    {
      "sku": "DL_SPF_2X4_L08_S",
      "status": "matched",
      "match": {"title": "...", "theirPrice": 4.00, "listedPrice": 4.00, "link": "https://www.homedepot.com/...", "confidence": 90, "normNote": "Sold as a pack of 10; price divided by 10"},
      "note": ""
    }
  ]
}
```

`store` may be null. `status` is `matched`, `unmatched` or `error`; `match` is absent unless matched. Other fields
are ignored. `theirPrice` is the per unit price after the flow's own unit normalisation; `listedPrice` is the price
as listed.

## What Gable does with the rows (they are untrusted)

- Rows are matched to the items sent by `sku`. A SKU Gable did not send is ignored. An item with no row is an error
  row ("The flow did not return this item").
- Title and note are cut to 200 and 300 characters with control characters removed.
- A price must be finite, above zero and below 100000 dollars, or the row is an error row.
- Confidence is clamped to 0 to 100.
- A link is kept only when it starts with `https://www.homedepot.com/`; any other is dropped.
- The difference and the position are recomputed from Gable's own price and the competitor's `theirPrice`; the flow's
  own difference, if it sends one, is ignored.
- The suggested price keeps the dealer's margin floor and is rounded in whole cents: the floor rounds up, a band
  target rounds down.
- A failed batch (network, timeout, a non 200 answer, bad JSON, too large) marks only its own items as errors.
