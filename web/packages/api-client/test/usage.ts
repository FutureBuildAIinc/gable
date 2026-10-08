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
  const page = await client.get("/api/v1/quotes", { query: { limit: 50, offset: 0 } });
  const quoteTotal: number = page.body.total;
  const firstState: "DRAFT" | "SENT" | "ACCEPTED" | "REJECTED" | "EXPIRED" | undefined =
    page.body.data[0]?.state;

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

  return [quoteTotal, firstState, orderStatus, vehicleCount, noBody];
}

// Wrong paths: each line must be a compile error --------------------------

async function _wrong() {
  // @ts-expect-error unknown path
  await client.get("/api/v1/widgets");

  // @ts-expect-error DELETE on a path that carries only GET and POST
  await client.delete("/api/v1/quotes");

  // @ts-expect-error unknown query parameter (the contract declares only limit and offset)
  await client.get("/api/v1/quotes", { query: { status: "sent" } });

  // @ts-expect-error path parameter must be a string
  await client.get("/api/v1/quotes/{id}", { path: { id: 123 } });

  // @ts-expect-error wrong body shape (cents field is a number, not a string)
  await client.post("/api/v1/payments", { invoice_id: "x", amount: "199", method: "CASH" });

  // @ts-expect-error extra path parameter the route does not declare
  await client.post("/api/v1/orders/{id}/cancel", undefined, { path: { id: "8f14e45f", junk: "y" } });
}
