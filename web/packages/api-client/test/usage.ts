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

  // Invoices and credit memos (ADR 0005 6.2 and 6.3): the cursor list, the
  // detail with its lines, the void, and the credit memo from draft to posted.
  const invoices = await client.get("/api/v1/invoices", { query: { status: "unpaid", overdue: "true", limit: 10 } });
  const invoiceNumber: string | undefined = invoices.body.items[0]?.number;
  const invoice = await client.get("/api/v1/invoices/{id}", { path: { id: "8f14e45f-ceea-467f-a830-aacd11a4" } });
  const billed: number = invoice.body.lines[0]?.line_total_cents ?? 0;
  const open: number = invoice.body.open_cents;
  const voided = await client.post(
    "/api/v1/invoices/{id}/transitions",
    { to: "void", revision: 2, reason: "billed in error" },
    { path: { id: "8f14e45f-ceea-467f-a830-aacd11a4" } },
  );
  const voidedAt: string | null | undefined = voided.body?.voided_at;
  const memo = await client.post("/api/v1/credit-memos", {
    invoice_id: "8f14e45f-ceea-467f-a830-aacd11a4",
    reason_code: "return",
    reason: "two pieces came back",
    lines: [{ invoice_line_id: "8f14e45f-ceea-467f-a830-aacd11a4", quantity: "-2", restock: true }],
  });
  const memoNumber: string | null | undefined = memo.body?.number;
  const posted = await client.post(
    "/api/v1/credit-memos/{id}/transitions",
    { to: "open", revision: 1 },
    { path: { id: "8f14e45f-ceea-467f-a830-aacd11a4" } },
  );
  const memoTotal: number = posted.body?.total_cents ?? 0;

  return [invoiceNumber, billed, open, voidedAt, memoNumber, memoTotal, quoteTotal, nextCursor, firstStatus, revision, lineTotal, orderStatus, vehicleCount, cancelledStatus, limit, terms, shipToRevision, customerRevision];
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

  // @ts-expect-error overdue is a computed flag and a list filter: it is a true/false string
  await client.get("/api/v1/invoices", { query: { overdue: 7 } });

  // @ts-expect-error the only invoice transition a client sends is void; the body needs its target
  await client.post("/api/v1/invoices/{id}/transitions", { revision: 1 }, { path: { id: "x" } });

  // @ts-expect-error a credit memo names its reason by code and carries its lines
  await client.post("/api/v1/credit-memos", { customer_id: "x" });

  // @ts-expect-error path parameter must be a string
  await client.get("/api/v1/quotes/{id}", { path: { id: 123 } });

  // @ts-expect-error wrong body shape (cents field is a number, not a string)
  await client.post("/api/v1/payments", { invoice_id: "x", amount: "199", method: "CASH" });

  // @ts-expect-error extra path parameter the route does not declare
  await client.post("/api/v1/orders/{id}/cancel", undefined, { path: { id: "8f14e45f", junk: "y" } });
}
