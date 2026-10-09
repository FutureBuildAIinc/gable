<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Governance (RFCs)

An RFC is a structured document the platform team writes to propose a
change before it lands. It carries a title, a problem statement, a
proposed solution and an expanded body the provider generated from
those three inputs. The RFC moves through a short lifecycle (draft,
review, approved, rejected) and is the trail of who proposed what,
when, and where the proposal ended up. The governance app is the
operator-installed app that exposes this surface; disabling the app
turns the routes off through the apps registry's gating router.

The Go code is in `core/internal/governance/`. The migration that
brought the table onto the contract is
`core/migrations/095_admin_wire_contract.sql` (the timestamps, the
revision, the document number, the status enum backfill).

## What it does in a yard

The platform team uses RFCs to gather comment before the work begins.
A contributor opens an RFC, fills the three required fields, and the
template provider renders a Markdown document the contributor edits
through the wire. Reviewers read, comment and move the RFC through
review to approved or rejected. A rejected RFC may be reopened to
draft for another pass. Approved is terminal: the proposal is done.

## Routes

Every route is in `core/api/fragments/governance.yaml` and the
registered handles are in `core/internal/governance/handler.go`. The
route census (`core/api/ROUTES.txt`) lists each one under the
`internal/governance` package (the route census has a `package` column,
not a module column). The five routes sit behind the apps registry's
gated router; when the governance app is disabled they answer
`app_disabled` and the SPA hides them.

| Method | Path | One line |
|---|---|---|
| GET | `/api/v1/governance/rfcs` | Cursor list of RFCs, newest first; status filter. |
| POST | `/api/v1/governance/rfcs` | Draft an RFC; the content is generated from the triple by the provider. |
| GET | `/api/v1/governance/rfcs/{id}` | Read one RFC with its body. |
| PUT | `/api/v1/governance/rfcs/{id}` | Update an RFC's editable fields on the client's revision. |
| POST | `/api/v1/governance/rfcs/{id}/transitions` | Move an RFC along its lifecycle on the client's revision. |

The exposure scan route at `POST /api/v1/admin/exposure-scan` is not
the governance module's; it lives under the admin segment and belongs
to the pricing module (see [tech-admin.md](tech-admin.md)).

## The main resource

`RFC` (see `core/api/fragments/governance.yaml`
`components.schemas.RFC`) embeds `RFCSummary`, the list item, and adds
the three body fields. The list never carries the body: `RFCSummary`
omits `problem_statement`, `proposed_solution` and `content`.

| Field | Wire form | Note |
|---|---|---|
| `id` | UUID | The RFC id. |
| `number` | text | The human readable document number (`RFC-000001`), minted from the sequence. |
| `title` | text | The RFC title; required on create. |
| `status` | lowercase enum | `draft`, `review`, `approved`, `rejected`. |
| `author_id` | UUID, nullable | The author as an identity provider subject; a soft reference (no table constrains it). |
| `revision` | integer | Starts at 1; returned as ETag. |
| `created_at`, `updated_at` | timestamp | RFC 3339 UTC. |
| `problem_statement` | text | The problem statement; required on create. |
| `proposed_solution` | text | The proposed solution; required on create. |
| `content` | text, nullable | The provider-generated body; null only on rows the legacy seed wrote without one. |

The `RFCStatus` enum is the four values above; the database keeps the
same lowercase vocabulary (ADR 0001 section 6), pinned by a CHECK
constraint added in `core/migrations/095_admin_wire_contract.sql`
(the legacy values `published` and anything else are backfilled to
`approved` before the CHECK is added).

### Money and quantity conventions

Governance carries no money and no quantity. The list cursor is the
keyset cursor of ADR 0001 section 2, ordered `created_at` then `id`
descending (`rfcs.created_at_id_desc` in
`core/internal/governance/handler.go`). The list envelope is the
`{items, next_cursor, limit}` shape with `total` only under
`include=total`. The `status` filter accepts a comma separated list of
the lowercase vocabulary; an unknown value (uppercase included) is a
400 naming the parameter.

## Lifecycle and transitions

The wire vocabulary is lowercase. The transitions are below; the
guard codes the body can return are listed in the prose after the
table:

| From | To | Effect | Event |
|---|---|---|---|
| `draft` | `review` | Move the RFC into review. | `rfc.review` |
| `review` | `approved` | Approve the proposal. | `rfc.approved` |
| `review` | `rejected` | Reject the proposal. | `rfc.rejected` |
| `rejected` | `draft` | Reopen the rejected RFC for another pass. | `rfc.reopened` |

A forbidden edge is `409 invalid_state_transition` with the from and
to spelled out. `approved` is terminal and has no outgoing edges.
Edits: an `approved` or `rejected` RFC may not be edited until it is
reopened (`core/internal/governance/service.go`, the update guard);
the transitions route is the only path that moves `status`. `PUT
/api/v1/governance/rfcs/{id}` carrying a `status` field is a 400
naming the field and pointing at the transitions route. `revision`
on the create is refused on the same grounds (`content` and `status`
are too). Every update and transition takes `If-Match` or the body
`revision` (`428 precondition_required` without, `409 stale_revision`).

The events are the constants in `core/internal/governance/service.go`
(`EventCreated` `rfc.created`, `EventUpdated` `rfc.updated`,
`EventReview` `rfc.review`, `EventApproved` `rfc.approved`,
`EventRejected` `rfc.rejected`, `EventReopened` `rfc.reopened`). The
transition events ride the transition's row through
`transitionEvents[to]` (a per target map keyed by status). The
`rfc.transitioned` audit row is written alongside the event with the
RFC's number, the from status and the to status.

## Content generation

The content is generated by the `AIProvider` seam (`core/internal/governance/ai.go`).
The only implementation shipped in this repository is
`TemplateAIProvider`, which is a stub that performs no network call
and reads no API key: it substitutes the submitted title, problem and
solution into a fixed Markdown skeleton and returns it. The provider
runs once at create, before the row is committed, so a create that
passes the parse always carries a body (a legacy seeded row may not;
its `content` reads back as null). Swapping in a real provider is the
way to get generated prose; the seam is the only seam the repository
defines for it.

## Events the module writes

Every mutation writes its event as the last statement of its
transaction ([ADR 0003](../adr/0003-events-outbox.md)). The create,
update and transitions write `rfc.created`, `rfc.updated` and the
transition event (`rfc.review`, `rfc.approved`, `rfc.rejected` or
`rfc.reopened`) respectively. Each write carries an `audit_log` row:
the create's audit row names `rfc.created`; the update's names
`rfc.updated`; the transition's names `rfc.transitioned` and carries
the number, the from status and the to status.

## Scopes, roles and keys

A machine key reaching the governance routes needs `governance:read`
for `GET` and `HEAD`, and `governance:write` for every other method
(ADR 0002; the segment is the first path segment under `/api/v1/`;
`governance` is in `machineKeyModules`). The user guard at the serve
layer is `admin`, `owner` (the exact guard is composed in
`core/internal/app/serve/serve.go` at the `governanceHandler.RegisterRoutes`
line). The apps registry's gated router wraps the guard, so a request
that reaches a disabled governance app answers `app_disabled` and is
never seen by the guard. A key without the scope is `403 forbidden`;
the audit row carries the refused scope.

## ADRs that govern this module

- [`docs/adr/0001-wire-contract.md`](../adr/0001-wire-contract.md) sections 1, 2, 3, 6, 9, 11, 12.
- [`docs/adr/0002-machine-keys.md`](../adr/0002-machine-keys.md) section 2.
- [`docs/adr/0003-events-outbox.md`](../adr/0003-events-outbox.md) sections 1, 2, 3, 5.

## How to try it locally

The repository's own seed and the local make targets are the only way
to exercise the module end to end. `make up` builds and starts the
local stack (Postgres, migrate and seed, `core serve`, `core worker`,
the web front door) on http://127.0.0.1:8080 with `AUTH_MODE=dev`;
`make down` removes it. To run the core from source instead:
`make db`, `make migrate`, `DEMO_SEED=1 make seed`, then
`cd core && AUTH_MODE=dev go run ./cmd/server`. The `make up` and
`make db` workflows use different compose projects and volumes, so
the `make db` data is never truncated or removed by `make up` or
`make down` (`AUTH_MODE=dev` needs no `Authorization` header; the
examples below show the production header shape).

Then, with an admin or owner role bearer and the seeded branch:

```
curl -X POST http://localhost:8080/api/v1/governance/rfcs \
  -H 'Authorization: Bearer <token>' \
  -H 'X-Branch-Id: <seeded branch uuid>' \
  -H 'Content-Type: application/json' \
  -d '{
        "title": "RFC: example",
        "problem_statement": "What is the problem?",
        "proposed_solution": "What is the proposal?"
      }'
```

The wire tests in `core/internal/governance/wire_test.go` pin the
platform and HTTP behaviour; the transaction proofs in
`core/internal/governance/tx_test.go` pin the lifecycle and the
revision guard.