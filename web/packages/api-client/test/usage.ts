// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Type-level usage check for the client, run by npm run typecheck. The
// happy paths must compile against the generated contract types, and the
// wrong paths sit under @ts-expect-error: if a wrong call ever stops
// being a compile error, the unused expectation itself fails the check,
// so the typing cannot silently rot when the contract changes.

import { createClient } from "../src/index.js";

const client = createClient({ baseUrl: "https://erp.example.test" });

// Happy paths -------------------------------------------------------------

async function _happy() {
  const page = await client.get("/api/v1/quotes", { query: { limit: 50, status: "sent", include: "total" } });
  const quoteTotal: number = page.body.total ?? 0;
  const nextCursor: string | null = page.body.next_cursor;
  const firstStatus: "draft" | "sent" | "accepted" | "rejected" | "expired" | undefined =
    page.body.items[0]?.status;

  const sent = await client.post(
    "/api/v1/quotes/{id}/transitions",
    { to: "sent", revision: 1 },
    { path: { id: "8f14e45f-ceea-467f-a830-aacd11a4" }, headers: { "If-Match": '"1"' } },
  );
  const revision: number = sent.body.revision;
  const lineTotal: number = sent.body.lines[0]?.line_total_cents ?? 0;

  const one = await client.get("/api/v1/quotes/{id}", { path: { id: "8f14e45f-ceea-467f-a830-aacd11a4" } });
  const customerId: string = one.body.customer_id;

  const created = await client.post(
    "/api/v1/orders",
    { customer_id: customerId, delivery_type: "pickup", lines: [{ product_id: customerId, quantity: "2" }] },
    { idempotencyKey: "replay-once" },
  );
  const orderStatus: string | undefined = created.body?.status;

  const integration = createClient({
    baseUrl: "https://erp.example.test",
    headers: { "X-Integration-Key": "configured-at-runtime" },
  });
  const vehicles = await integration.get("/api/integration/vehicles");
  const vehicleCount: number = vehicles.body.length;

  // The cancel is the transition now (ADR 0005 5.2): it answers with the
  // cancelled order, not a bare 204.
  const cancelled = await client.post(
    "/api/v1/orders/{id}/transitions",
    { to: "cancelled", revision: 1, reason: "customer moved" },
    { path: { id: "8f14e45f" } },
  );
  const cancelledStatus: string | undefined = cancelled.body?.status;

  const customers = await client.get("/api/v1/customers", { query: { q: "acme", tier: "gold", is_active: true, limit: 20 } });
  const limit: number | null | undefined = customers.body.items[0]?.credit_limit_cents;
  const terms: string | undefined = customers.body.items[0]?.payment_terms.code;
  const shipTo = await client.post(
    "/api/v1/customers/{id}/ship-tos",
    { code: "YARD", name: "Lake job", line1: "12 Lake Rd", tax_rate_percent: "8.875" },
    { path: { id: "8f14e45f-ceea-467f-a830-aacd11a4" } },
  );
  const shipToRevision: number = shipTo.body.revision;
  const edited = await client.put(
    "/api/v1/customers/{id}",
    { account_number: "A-1", name: "Acme", credit_limit_cents: null, po_required: true, payment_terms_id: "8f14e45f-ceea-467f-a830-aacd11a4" },
    { path: { id: "8f14e45f-ceea-467f-a830-aacd11a4" }, headers: { "If-Match": '"3"' } },
  );
  const customerRevision: number = edited.body.revision;

  // Products, pricing and locations (C3-1): the stocking unit, the scaled base
  // price, the stock totals as decimal strings, the price read's pair and
  // lowercase basis, and a location edit at its revision.
  const products = await client.get("/api/v1/products", { query: { limit: 20, include: "total" } });
  const kit = await client.get("/api/v1/products/{id}/kit-components", { path: { id: products.body.items[0]?.id ?? "x" } });
  const kitRevision: number = kit.body.revision;
  const replacedKit = await client.put(
    "/api/v1/products/{id}/kit-components",
    { revision: kitRevision, components: [{ component_product_id: kit.body.kit_product_id, quantity: "2" }] },
    { path: { id: kit.body.kit_product_id } },
  );
  const replacedRevision: number = replacedKit.body.revision;
  const stockUom: string | undefined = products.body.items[0]?.stock_uom;
  const basePrice: number | undefined = products.body.items[0]?.base_price_ten_thousandths;
  const available: string | undefined = products.body.items[0]?.available;
  const priced = await client.get("/api/v1/pricing/calculate", {
    query: { customer_id: "8f14e45f", product_id: "8f14e45f", quantity: "120" },
  });
  const unitPrice: number = priced.body.unit_price_ten_thousandths;
  const basis: "contract" | "tier" | "retail" | "quantity_break" | "job_override" | "promotional" | "category_tier" | "category_account" =
    priced.body.price_basis;
  const total: number = priced.body.line_total_cents;
  const moved = await client.put(
    "/api/v1/locations/{id}",
    { code: "YARD-1", name: "Main yard", active: true },
    { path: { id: "8f14e45f" }, headers: { "If-Match": '"2"' } },
  );
  const locationType: string = moved.body.type;

  return [quoteTotal, nextCursor, firstStatus, revision, lineTotal, orderStatus, vehicleCount, cancelledStatus, limit, terms, shipToRevision, customerRevision, stockUom, basePrice, available, unitPrice, basis, total, locationType];
}

// Wrong paths: each line must be a compile error --------------------------

async function _wrong() {
  // @ts-expect-error unknown path
  await client.get("/api/v1/widgets");

  // @ts-expect-error DELETE on a path that carries only GET and POST
  await client.delete("/api/v1/quotes");

  // @ts-expect-error unknown query parameter (the quote list is cursor paged: no offset)
  await client.get("/api/v1/quotes", { query: { offset: 10 } });

  // @ts-expect-error the wire status is lowercase
  await client.post("/api/v1/quotes/{id}/transitions", { to: "SENT" }, { path: { id: "8f14e45f" } });

  // @ts-expect-error a unit price is an integer in ten thousandths, not a decimal string
  await client.post("/api/v1/quotes", { customer_id: "x", lines: [{ quantity: "1", uom: "PCS", unit_price_ten_thousandths: "5.5" }] });

  // @ts-expect-error the customer list is cursor paged and the tier is lowercase
  await client.get("/api/v1/customers", { query: { offset: 0, tier: "GOLD" } });

  // @ts-expect-error a customer PUT must carry the controls it must not reset: payment_terms_id and po_required
  await client.put("/api/v1/customers/{id}", { account_number: "A", name: "n" }, { path: { id: "x" } });

  // @ts-expect-error a contact PUT must carry can_place_orders and the order limit (null for none)
  await client.put("/api/v1/contacts/{id}", { first_name: "A", last_name: "B" }, { path: { id: "x" } });

  // @ts-expect-error a credit limit is integer cents, never a float dollar amount string
  await client.put("/api/v1/customers/{id}", { account_number: "A", name: "n", credit_limit_cents: "100.50" }, { path: { id: "x" } });

  // @ts-expect-error a ship-to needs its code, name and line1
  await client.post("/api/v1/customers/{id}/ship-tos", { name: "only a name" }, { path: { id: "x" } });

  // @ts-expect-error the product list is cursor paged: no offset
  await client.get("/api/v1/products", { query: { offset: 10 } });

  // @ts-expect-error a component quantity is a decimal string, never a number
  await client.put("/api/v1/products/{id}/kit-components", { components: [{ component_product_id: "x", quantity: 2 }] }, { path: { id: "x" } });

  // @ts-expect-error a product is created in a stocking unit (stock_uom), not uom_primary
  await client.post("/api/v1/products", { sku: "A", description: "d", uom_primary: "PCS" });

  // @ts-expect-error a base price is an integer in ten thousandths, not a decimal string
  await client.post("/api/v1/products", { sku: "A", description: "d", stock_uom: "PCS", base_price_ten_thousandths: "4.25" });

  // @ts-expect-error a pricing rule's discount is a decimal string
  await client.post("/api/v1/pricing/rules", { name: "n", rule_type: "promotional", discount_pct: 5 });

  // @ts-expect-error a location type is lowercase on the wire
  await client.post("/api/v1/locations", { code: "C", type: "YARD" });

  // @ts-expect-error path parameter must be a string
  await client.get("/api/v1/quotes/{id}", { path: { id: 123 } });

  // @ts-expect-error wrong body shape (cents field is a number, not a string)
  await client.post("/api/v1/payments", { invoice_id: "x", amount: "199", method: "CASH" });

  // @ts-expect-error extra path parameter the route does not declare
  await client.post("/api/v1/orders/{id}/cancel", undefined, { path: { id: "8f14e45f", junk: "y" } });
}
