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

async function happy() {
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
    { customer_id: customerId, lines: [{ product_id: customerId, quantity: 2, price_each: 199 }] },
    { idempotencyKey: "replay-once" },
  );
  const orderStatus: string | undefined = created.body?.status;

  const integration = createClient({
    baseUrl: "https://erp.example.test",
    headers: { "X-Integration-Key": "configured-at-runtime" },
  });
  const vehicles = await integration.get("/api/integration/vehicles");
  const vehicleCount: number = vehicles.body.length;

  const cleared = await client.post("/api/v1/orders/{id}/cancel", undefined, { path: { id: "8f14e45f" } });
  const noBody: undefined = cleared.body;

  return [quoteTotal, nextCursor, firstStatus, revision, lineTotal, orderStatus, vehicleCount, noBody];
}

// Wrong paths: each line must be a compile error --------------------------

async function wrong() {
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

  // @ts-expect-error path parameter must be a string
  await client.get("/api/v1/quotes/{id}", { path: { id: 123 } });

  // @ts-expect-error wrong body shape (cents field is a number, not a string)
  await client.post("/api/v1/payments", { invoice_id: "x", amount: "199", method: "CASH" });

  // @ts-expect-error extra path parameter the route does not declare
  await client.post("/api/v1/orders/{id}/cancel", undefined, { path: { id: "8f14e45f", junk: "y" } });
}
