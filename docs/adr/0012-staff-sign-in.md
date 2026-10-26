# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# ADR 0012: staff sign in through an Appwrite deployment

## Status

Accepted for the Gable v1 refactor (item S9, the shell track's sign in row of
`docs/parity/LBM-PARITY-MATRIX.md`, Ruling 4), merged after its independent
reviews. It stands on ADR 0002 (machine
keys), ADR 0004 (web session custody), ADR 0005 (the sales and money core),
ADR 0009 (finer admin scopes) and ADR 0010 (multi company). When its session
slice lands, it supersedes the interim design of ADR 0004 (the
`sessionStorage` handoff) by building that record's target (an `HttpOnly`
cookie issued by the API). ADR 0004's target names a bridge as the way the
cookie is issued; that is not this design. So when this record is accepted,
ADR 0004 is amended in the same merge: its Status says this record
supersedes it, and its target's text says the API issues the cookie after an
Appwrite sign in made by the core, with no bridge. The amendment waits for
this record's acceptance, and the supersession of ADR 0004's interim design
waits for its session slice.

## Context

### The ruling

Gable signs staff in with the Auth and Teams services of an Appwrite
deployment. That holds in two places: FutureBuild's hosted production, which
uses FutureBuild's own Appwrite deployment, and the community source version
a dealer hosts, where the dealer runs its own Appwrite (self hosted, or
Appwrite's own cloud). Nothing in this record may depend on a component a
self hosting dealer cannot obtain.

### What the code does today

- **The core verifies outside bearer tokens.** `core/internal/app/serve/serve.go`
  (step 4, lines 209 to 230) builds `middleware.NewAuthMiddleware` when
  `JWKS_URL` is set, runs with no staff authentication when `AUTH_MODE=dev`,
  and exits otherwise. `core/pkg/middleware/auth.go` parses the bearer with
  the JWKS key set, checks the issuer only when `AUTH_ISSUER` is set, checks
  no audience, refuses a role bearing token with no subject, and puts the
  `UserClaims` (`sub`, `email`, `roles`, `role`, `org_id`) in the context; a
  request with no bearer is answered 401.
- **The guards fail open on no claims.** `RequireRole` admits a request when
  the claims' `role` or any of `roles` is in the guard's list, and passes a
  request with no claims and no machine key ("dev mode"). The branch
  middleware (`branch.go`, around line 204) and the counter override
  (`core/internal/pos/override.go`, around line 212) are permissive on no
  claims too. `canViewOpenTillExpectation`
  (`core/internal/pos/handler.go`, around line 727) shows the money figures an
  open drawer keeps blind to a request that carries no claims and to one that
  carries no role at all. Today that is safe only because step 4 either mounts the JWT
  middleware, which answers 401 first, or runs in dev mode.
- **Some handlers read `role` alone, or a UUID `sub`.** `callerRole` in
  `core/internal/order/handler.go` (line 100) and `caller` in
  `core/internal/ap/handler.go` (line 80) read the role from `claims.Role`
  alone (`caller` reads the subject as its actor as well);
  `userRole` in `core/internal/pricing/exposure_handler.go` (around line 444)
  reads `role`, else the first of `roles`. The hold release in
  `core/internal/order/service.go` (around line 1292) refuses only a non
  empty role outside the finance roles. Four readers parse `sub` as a UUID:
  `cashierOf` (`core/internal/pos/handler.go`, line 251), `userID`
  (`exposure_handler.go`, line 417), `core/internal/reporting/handler.go`
  (line 259) and `core/internal/bankrecon/service.go` (line 362); the counter
  override's "a manager other than the sale's cashier" check compares the
  sale's cashier UUID with the approver's `sub`.
- **The partner routes read the email from the token.**
  `core/pkg/middleware/partner_auth.go`
  maps any claims that carry an email to the active customer with that email,
  and refuses the request when there is none. It guards
  `/api/partner/v1/dashboard`, `/api/partner/v1/quotes` and
  `/api/partner/v1/quotes/{id}` (`core/internal/partner/handler.go`, mounted at
  `serve.go` around line 963) with no role guard and no branch wall, so those
  routes answer a staff token that carries an email. Two Gable handlers
  attribute by email as well: `requesterSub`
  (`core/internal/staff/handler.go`, around line 378) falls back to
  `claims.Email`, and `getPerformedBy`
  (`core/internal/pricing/category_service.go`, around line 610) prefers it to
  the subject.
- **The guards' roles.** The guards in `core/internal/app/serve` use `owner`,
  `admin`, `sales`, `finance`, `purchasing`, `warehouse`, `cashier`, `driver`,
  and `user` once (the `/uploads/` route, `serve.go`
  line 1183).
- **Branches come from Gable's own table.** `core/pkg/middleware/branch.go`
  checks `X-Branch-Id` against `user_locations` rows keyed by the token's
  `sub` (a `TEXT` column); `owner` and `admin` may omit the header and reach
  every branch; the list wall (`Wall`, ADR 0007 section 5.7) holds a list to
  the caller's grants. ADR 0010 derives a user's companies from the same
  grants, written by the audited users routes ADR 0009 scopes as
  `users:grants`.
- **The staff roster is a separate thing.** `core/internal/staff` owns the
  `staff` and `module_grants` tables (an email, a free text role label, an
  active flag, module grants for an integration). The `Staff` type comment
  (`core/internal/staff/model.go`, line 34) says a staff row is distinct from
  a JWT `sub` user.
- **The web has no staff sign in flow.** Nothing under `web/` holds an
  `authorization_endpoint`, a `code_verifier` or a `redirect_uri`.
  `web/packages/auth` holds session custody (ADR 0004: a token in memory plus
  a tab scoped `sessionStorage` record), the dev persona session, the 401
  `gable:session-expired` event, and the runtime config of sign in methods
  read from the public `GET /api/v1/auth/config`.
- **The sign in methods exist only as a list.** `core/internal/signinmethods`
  holds the dealer's allowlist of six codes (`email_password`, `passkey`,
  `magic_link`, `google`, `microsoft`, `oidc`), with an issuer URL and client
  id stored on the `oidc` row. Its `Start` (`service.go`, lines 227 to 238)
  answers `StartNoRoute` for email and password and `StartNotImplemented` for
  the other five. The config's `start_url` for `email_password` is the portal
  login (`formStartAction` in `model.go`, line 84).
- **The one server sign in is the contractor portal's**
  (`POST /api/portal/v1/login`, with its own middleware in
  `core/pkg/middleware/portal_auth.go` and its own per address limit,
  `StrictRateLimit`, `serve.go` line 1082). The front door's email and
  password form posts there today. Staff have none.
- **The persona cards run on `AUTH_MODE=dev`,** which the local
  `docker-compose.yml` and the CI workflow set. Nothing in the binary knows
  whether it runs in the local stack: the release image (`core/Dockerfile`)
  and the local image are the same build.
- **The Tauri shells already hold tokens safely.** `tauri/core/src/tokens.rs`
  keeps an access and a refresh token in the OS keychain, exposes only the
  access token over IPC, and clears both at sign out. The desk shell
  registers the `gable` deep link scheme (`tauri-plugin-deep-link`).

### What Appwrite offers

- **Auth.** Email and password, magic URL, email one time password, OAuth2
  providers, passkeys in recent releases, custom tokens, and multi factor
  authentication: https://appwrite.io/docs/products/auth
- **Server side sessions.** A server holding an API key with
  `sessions.write` creates the session itself and keeps the session secret on
  its own side: https://appwrite.io/docs/products/auth/server-side-rendering .
  For OAuth2 the server's sign in endpoint sends the browser to Appwrite's
  `createOAuth2Token` URL; Appwrite runs the provider redirect and then sends
  the browser to the server's `success` URL with the user id and a one time
  secret, which the server exchanges for a session (`account.createSession`).
  The providers (Google, Microsoft, a generic OIDC provider and others) are
  configured in Appwrite: https://appwrite.io/docs/products/auth/oauth2 .
  Appwrite accepts a `success` or `failure` URL only on a hostname in the
  project's platform list (the parameter's own description in
  `app/controllers/api/account.php`).
- **Magic URL.** Appwrite emails a link to the URL the caller names, with the
  user id and secret appended, and that pair is exchanged for a session the
  same way: https://appwrite.io/docs/products/auth/magic-url
- **What Appwrite does with a provider's address.** When the OAuth2 callback
  meets a provider address it does not know, it looks for an existing user
  with that address and adopts it, unless the provider says the address is
  not verified: the gate that refuses an unverified provider email before
  that adoption arrives in the 1.9.0 release ("OAuth email verification:
  enforce email verification when linking OAuth2 providers",
  https://github.com/appwrite/appwrite/releases/tag/1.9.0) and is absent
  before it. Each
  provider adapter decides what
  "verified" means. The Microsoft adapter reports no address as verified
  from the 2.0.0 release on ("Microsoft explicitly does not verify emails
  in Graph /me, so treat as unverified until one is confirmed",
  `src/Appwrite/Auth/OAuth2/Microsoft.php` at
  https://github.com/appwrite/appwrite/blob/2.0.0/src/Appwrite/Auth/OAuth2/Microsoft.php),
  and reported every address it
  returned as verified before that, so 2.0.0 is the first release whose
  Microsoft provider address cannot take over an existing account. The 2.0.0 release
  notes do not list that change, so the tagged source is the record of it. The
  Google adapter
  takes the provider's
  own `email_verified` claim (`src/Appwrite/Auth/OAuth2/Google.php`), and
  the generic OIDC adapter takes the issuer's (`src/Appwrite/Auth/OAuth2/Oidc.php`).
  The Microsoft provider's tenant is required on every release Gable
  supports: the adapter's `verifyCredentials` throws "Microsoft tenant is
  required." when the tenant is empty. The `common` fallback that once
  accepted every Entra tenant is gone (it is in the 1.8.0 and 1.9.0
  adapters, and the 1.9.x patch line already requires a tenant). The adapter
  takes any tenant that is not empty, so `common` typed in still works and the
  operator rule of section 8 is the only guard against it.
- **The version.** `GET /v1/health/version` answers the Appwrite version. The
  route is public and needs no key (on the 2.x line it is
  `src/Appwrite/Platform/Modules/Health/Http/Health/Version/Get.php`, which
  sets the path, labels it public and answers the version; in the 1.5.x to
  1.8.x releases it is `app/controllers/api/health.php`). Appwrite's own
  compose file healthchecks that path from the 2.0.0 release on.
- **The line Gable targets.** The Appwrite 2.x line on PostgreSQL, and no
  other line: a 1.x Appwrite is refused at start, and the floor of section 8
  sits on the 2.x line. Every slice is proven on a local Appwrite pinned in
  the local compose to 2.1.0, the release the hosted deployment runs
  (section 9).
- **JWTs.** `account.createJWT` returns a short lived token that a server
  passes back to Appwrite with `setJWT` to act as the user:
  https://appwrite.io/docs/products/auth/jwt . Appwrite publishes no JWKS for
  it: Appwrite's source signs it HS256 with a server secret
  (`app/controllers/api/account.php` in the `appwrite/appwrite` repository),
  so only Appwrite can verify it.
- **Teams.** A team has memberships; each membership carries a list of role
  strings; an `owner` role is reserved by Appwrite for those who may invite
  and remove members: https://appwrite.io/docs/products/auth/teams . A
  membership created by a client is an invitation by email that the invitee
  confirms; one created by a server with an API key is added at once, with
  no email.
- **Rate limits** do not apply to calls made with an API key:
  https://appwrite.io/docs/advanced/security/rate-limits
- **Self hosting.** A Docker host (Docker Compose) or the Helm chart; the
  documented minimum is two CPU cores and 4 GB of memory:
  https://appwrite.io/docs/advanced/self-hosting and
  https://appwrite.io/docs/advanced/self-hosting/installation

### An OIDC bridge in front of Appwrite

Appwrite is not an OIDC provider. Signing staff in through OIDC would mean
running a separate issuer in front of Appwrite, with its own sign in form,
database, signing keys and rotation. Gable publishes none, so a self hosting
dealer would have none to run.

Whichever path this record takes, the first build slice proves it end to end
on a local Appwrite before anything relies on it.

## Decision

### 1. The options weighed

**(a) Through an OIDC bridge.** An OIDC issuer backed by the Appwrite
deployment signs staff in; the web runs an authorization code flow with PKCE;
the core verifies the issuer's tokens through its existing JWKS check.

- A self hosting dealer would have to run an OIDC issuer in front of its
  Appwrite, and Gable publishes none. Gable would have to publish and
  maintain one: a second identity service with its own database, signing
  keys and rotation, which is more than this item can carry and more than a
  dealer should host.
- The JWKS check fits only the signature: the core checks no audience, and
  the roles and branches Gable needs would still have to be looked up by the
  core.
- The shells (public clients returning to a loopback address or a custom
  scheme) and every method beyond the issuer's own form would each need more
  issuer work.

**(b) Appwrite directly.** Gable talks to Appwrite's own Auth and Teams APIs.

- A self hosting dealer deploys Appwrite (one documented install) and Gable,
  and nothing else. FutureBuild's hosted production points the same settings
  at its own Appwrite deployment.
- Appwrite holds the accounts, the sessions and the Teams for every method
  (section 4), so no second identity service exists.
- Teams give roles, not just membership (section 5).
- The core cannot verify Appwrite's JWT locally (section "What Appwrite
  offers"). Calling Appwrite on every API request would put Appwrite on the
  hot path of every staff request and on the counter's. So the core talks to
  Appwrite at sign in and at refresh only, and holds its own session in
  between (section 2).

**Decision: (b), with the core as Gable's only Appwrite client.** It is the
only option a self hosting dealer can run from what Gable publishes, it
covers the ruled methods, and it lets Teams carry roles. The bridge is out of
Gable's path entirely.

### 2. The core holds the session

The core is Gable's one relying party on Appwrite and the only caller of its
API. No browser script and no shell calls Appwrite's API, and neither ever
holds an Appwrite session, an Appwrite JWT or an Appwrite key. The redirect
methods pass the browser through Appwrite's provider redirect by navigation
only (section 4); the one time secret it brings back is spent by the core.

- **Sign in.** The core opens an Appwrite session server side (section 4
  gives the call per method), reads the account and the user's membership in
  the dealer's Team (section 5) with its API key, and then ends the Appwrite
  session at once, acting as the user on that session's own secret. Nothing
  Appwrite issued is stored. The core refuses, with the one fixed answer
  every refusal gives, a person whose membership is unconfirmed or absent,
  whose user is blocked or deleted, whose Appwrite email is not verified
  (section 6 says how an invitee proves it), or who holds no mapped role
  (only unknown roles), and a person whose membership holds Appwrite's
  `owner` at all (section 5 says why that one cannot be carried by a check
  for a mapped role). Multi factor
  authentication in Appwrite is out of scope for this record, deferred like
  passkeys: the core refuses a person whose account answers `mfa: true`,
  at sign in and at refresh, with a reason an admin can act on (turn the
  setting off for that user, or wait for the later record). The refusal is
  the core's own, not Appwrite's: Appwrite's multi factor gate runs on a
  session authenticated call, and the core's account read is a key call,
  which answers `mfa: true` and no more.
- **A fresh session every time.** Every sign in issues a new random session
  id from a cryptographic source; any session id the client sent is ignored
  and never reused (no fixation).
- **The core's session.** A new table, `staff_sessions`, keyed by the hash of
  the session id (the id itself is never stored), holds the subject, the
  Appwrite user id, the client kind (`web` or `shell`), the roles read at the
  last membership read and when that read was made, the creation and last
  use, an idle limit, an absolute limit, a revoked flag, and for a shell the
  hash of its current refresh token and its token family. Writes to it
  carry the concurrency test the repository requires (pool size 4, three
  contenders).
- **A pending sign in row.** The `state` is not only in the cookie, because a
  cookie cannot make a value single use on its own: the same value rides in
  the query of the URL Appwrite redirects to, and a browser can replay that
  URL. So the core keeps a second table, `pending_sign_ins`, keyed by the
  hash of the state, holding the method, the client kind (`web` or `shell`),
  the expiry and a used flag. For a shell the same row also holds what the
  shell's leg needs across the provider redirect: the PKCE challenge (S256),
  the validated return target, and the hash of the one time code the shell
  redeems, with that code's own expiry and its own flag, `code_used`,
  because the row is claimed twice: once for the state and once for the code.
  The row also names the person: once the callback knows them it holds the
  subject and the Appwrite user id, so the redemption knows whose
  `staff_sessions` row it signs in. The cookie carries the raw state and
  the row holds only its hash, so a read of the table yields nothing a replay
  could use; the one time code is stored hashed too, so the row never holds a
  credential that can be spent. The callback claims the row with one
  conditional update that flips the used flag and checks that exactly one row
  changed, commits it, and only then calls Appwrite: spending Appwrite's
  secret is an HTTP call and cannot join a database transaction, so the claim
  is what makes the state single use. A state that comes back twice finds the
  row used and is refused before anything is exchanged. The shell's one time
  code is redeemed the same way, on its own flag: the row is claimed with a
  conditional update that flips `code_used` and checks that exactly one row
  changed, and only such a claim yields the session, so a replayed code is
  refused by the row and never by the shell. A row past its expiry is
  refused, and expired rows are swept.
- **The request path, fail closed.** The auth layer dispatches in this order:
  a machine key by its `sk_live_` shape (ADR 0002 section 1, unchanged); a
  shell access token by its own fixed prefix (section 3); the session cookie;
  and, until slice 8 retires it, a JWT on the JWKS path. A valid session or
  shell token fills `middleware.UserClaims` with `sub` and `roles`, and with
  no email;
  `role` is always empty. A staff session's email is in neither the token nor
  the session row, and that is the decision: of the email readers of today
  (`PartnerAuth`, `requesterSub`, `getPerformedBy`, all named in Context),
  `requesterSub` and `getPerformedBy` fall back to the subject, and
  `PartnerAuth` refuses a claim with no email, so a staff token can never be
  taken for a partner's. Outside the
  public paths, a request with none of
  these, or with an unknown, revoked, idle expired or absolutely expired
  session, is answered 401 before any handler runs; it never reaches a guard
  with no claims. A credential that is presented and fails is never passed
  over for another: a request carrying an invalid session cookie and a valid
  bearer, or an invalid bearer and a valid cookie, is answered 401. Step 4 of
  `serve.go` mounts this middleware whenever the
  Appwrite settings are present, and the exit stays for a core with neither
  Appwrite settings nor the dev build (section 7).
- **Readers of `role` alone change in slice 2.** Because a session sets
  `roles` only, no handler may depend on `role`. These three readers become
  membership checks over the session's role set, not a role string:
  `callerRole` (`order/handler.go`), `caller` (`ap/handler.go`) and
  `userRole` (`pricing/exposure_handler.go`) ask `middleware.ClaimsHaveAnyRole`
  over both fields, so a decision never depends on which field, or which order
  of `roles`, a person's roles arrive in. The hold release in
  `order/service.go` takes the role set instead of `body.Role` and asks
  whether the session holds admin, owner or finance (ADR 0005 section 5.2),
  and an empty set is refused when a person's claims are present. The exposure
  scoping asks whether the session holds sales, and separately whether it
  holds owner or admin. **A session holding more than one role** is decided by
  that membership and never by an order: a session holding sales and finance
  is scoped like a sales session unless it also holds owner or admin, and
  owner or admin wins over sales. The audit actor records the whole role set,
  and the role string it carries for the audit row comes from a new helper,
  `middleware.ClaimsPrimaryRole(claims)`, which returns the first of the
  mapped roles in the fixed order owner, admin, sales, finance, purchasing,
  warehouse, cashier, driver, so that row does not depend on the order of
  `roles` either. Every call site that records a role string uses that
  helper: the AP caller for the audit subject, the order override's event
  actor, and the exposure acknowledge, request and override events, whose
  `actor_role` column (`core/internal/pricing/exposure_model.go`) the
  exposure handler fills today with `userRole(r)`; every decision uses
  `ClaimsHaveAnyRole`. Every `claims.Role` reader is
  found by a grep in that slice and covered by a test.
- **The subject is a UUID.** `sub` is the name based UUID (version 5) over a
  fixed Gable namespace and `<projectId>:<userId>`. Every reader that parses
  `sub` as a UUID (`cashierOf`, `userID`, the reporting and bankrecon
  authors) keeps working, the counter override's "other than the sale's
  cashier" comparison holds, `user_locations` and the audit log key to it as
  they key to `sub` today, and the session row keeps the Appwrite user id for
  refresh. The same Appwrite user always yields the same `sub`.
- **Refresh.** When the session's last membership read is older than a
  setting, the core re-reads the membership and the user's status with its
  API key. A definite answer that the person is no longer a member, holds no
  mapped role, or is blocked or deleted revokes the session; changed roles
  replace the snapshot. An answer that is not definite (Appwrite unreachable,
  an error) keeps the session on its snapshot, so a brief Appwrite outage
  does not close the counter, but only until the snapshot reaches a maximum
  age (its own setting, shorter than the absolute limit); past it the
  session is refused until a definite read succeeds.
- **Sign out** revokes the core's session and clears the client's copy.
  There is no Appwrite session to end, because none outlived the sign in.
- **Brute force.** Appwrite's own rate limits do not apply to the core's API
  key calls, so the core limits its own routes: the staff sign in and every
  redirect method's start route (the magic link's included) per client
  address and per email, and the shell's code redemption and refresh per
  client address, refusing past the limit with the same fixed answer plus
  `Retry-After`. The portal login keeps its own limiter.
- **Audit.** Sign in, every refusal (with the attempted email and a fixed
  reason, never the password), sign out, a revocation at refresh (removal,
  block or deletion), a shell refresh token rotation and a reuse that
  revokes a family each write one row through the existing audit logger
  (`serve.go`, step 3c), with the actor's `sub` where there is one and the
  client kind. Every call the core makes under `users.write` (section 8)
  writes its own audit row too, naming the Gable admin who caused it.

### 3. Custody, web and shells

- **Web.** The sign in answer sets the session id as an `HttpOnly`, `Secure`,
  `SameSite=Strict` cookie on the API's origin with `Path=/api` and no
  `Domain` attribute: ADR 0004's target. The web serves only `/api/` (and
  `/healthz/`) from the core, so the cookie needs no wider path; if staff
  ever fetch uploads in the browser, the file server moves under `/api` in a
  slice of its own. Both bundles send it with
  `credentials: 'same-origin'`; a state changing request the core
  authenticates by the session cookie must also carry a custom header the core
  checks (the CSRF check ADR 0004 asks for), which a cross site form cannot
  set. The check runs in the session middleware, after the cookie is accepted
  and before the handler, so it covers the cookie path alone: a machine key
  request is not a cookie request and is not caught by it, and the portal's
  own chain (`/api/portal/v1/login`, a plain form post that cannot set the
  header) is exempt. The header is added to the allowed request
  headers in `core/pkg/middleware/cors.go` for configured origins only, so a
  disallowed origin's preflight is still refused. The sign in route itself is
  not a cookie request, so it carries its own check against the same attack: it
  takes the custom header or an `Origin` that is Gable's own origin, so a
  cross site form cannot sign a browser into another account.
  `@gable/auth` keeps only display data (name,
  roles, home yard); the `sessionStorage` handoff and the token in memory are
  deleted. A 401 still fires `gable:session-expired`, and the panel offers
  sign in again.
- **Shells (Tauri).** A shell is a public client of the core, not of
  Appwrite. Email and password is posted from the shell's own door to the
  core. Every redirect method opens the system browser on the core's start
  route with a PKCE challenge (S256) and the shell's return target; the core
  runs the sign in and finishes by redirecting to the shell with a one time
  code. The core accepts as the return target only the registered `gable`
  scheme with its fixed callback path, or `http://127.0.0.1:<port>` or
  `http://[::1]:<port>` with the fixed callback path and any port, never any
  other URL. The challenge, the return target and the one time code all live in
  the shell's `pending_sign_ins` row (section 2): the row holds the challenge
  and the validated return target across the provider redirect, and then the
  hash of the one time code, which the shell redeems once, before it expires,
  with its PKCE verifier, for an access token and a refresh token, which
  `tauri/core/src/tokens.rs` keeps in the OS keychain as it does today. The
  access token is an opaque random value with a fixed prefix of its own
  (distinct from `sk_live_`), resolved to the shell's row in
  `staff_sessions`. The refresh token rotates on use; presenting a rotated
  refresh token again revokes the whole family.
- **The shells' own return.** Only the desk registers the `gable` scheme
  (`tauri/gable-desk/tauri.conf.json`), and its link handler takes plain path
  segments only, so a link with a query is dropped. So slice 5 gives each
  shell its own callback handler, and the work, field and pro shells use the
  loopback return target, not the scheme.
- **Machine keys are unchanged** (ADR 0002): still `sk_live_` bearers, still
  scoped, still refused on the key management routes.
- **The partner routes are not staff routes.** `PartnerAuth` and the three
  partner routes it guards are a partner's own reads of a customer's quotes,
  and they are outside this record: a staff session never passes `PartnerAuth`,
  because a staff claim carries no email, and when the JWKS path retires in
  slice 8 the partner routes lose their only source of claims, so they move
  under the portal's own auth or retire with that slice. Which one is the
  partner integration's decision, not this record's.

### 4. The sign in methods

The six codes of `signinmethods` stay the dealer's allowlist and the door's
runtime config. Each is an Appwrite method the dealer enables, and every
provider (Google, Microsoft, a dealer's own OIDC issuer) is configured in
Appwrite, never in Gable. Every method ends the same way: the core holds a
pair Appwrite issued for one sign in (an email and password, or a user id
and a one time secret), exchanges it for an Appwrite session server side,
reads the account and the membership, ends the Appwrite session, and issues
Gable's own session (section 2).

| Gable code | The person's path | The core's Appwrite calls |
|---|---|---|
| `email_password` | browser or shell to the core: `POST /api/v1/auth/sign-in`, which carries the CSRF header or passes the same origin `Origin` check (section 3) | `account.createEmailPasswordSession` |
| `google` | browser to the core's start route; the core redirects it to Appwrite's `createOAuth2Token` URL for the provider (navigation only); Appwrite runs the provider redirect and sends the browser to Gable's callback with the user id and one time secret | `account.createSession` with that user id and secret |
| `microsoft` | as `google`, with the console step below | as `google` |
| `oidc` | as `google`, with Appwrite's generic OIDC provider pointed at the dealer's issuer | as `google` |
| `magic_link` | the person asks at the door; the core looks the email up and, only for an existing member, asks Appwrite to email a link whose target is Gable's callback; the link lands there with the user id and secret | `users.list`; then `account.createMagicURLToken` with that user's id and Gable's callback as its URL; then `account.createSession` |
| `passkey` | deferred: not built by this record | none |

- **Microsoft needs one step in the Appwrite console.** Appwrite's Microsoft
  adapter reports no address as verified (Context, "What Appwrite offers"),
  so on every release Gable supports Appwrite refuses a Microsoft sign in
  that would adopt an existing user, and a user Appwrite makes for itself
  carries an unverified address that the core refuses (section 2). Microsoft
  therefore works in three acts and not in one: the person signs in with
  Microsoft, which creates the Appwrite user and its Microsoft identity, and
  the core refuses because the address is not verified; an administrator in
  the Appwrite console marks that user's address verified and adds the Team
  membership with the mapped roles; the person signs in with Microsoft
  again, and Appwrite now returns that member's user id and secret. The
  administrator checks two things before marking the address verified: that
  the user carries the Microsoft identity and no password, and that the
  identity is the person's own, because a user that signed up for itself
  under that address would otherwise take the member's seat.
- **Google needs no administrator step, on the floor or on the release the
  local proof runs; the invitee proves the address himself.** Its adapter
  honours the provider's `email_verified` claim, so
  Appwrite adopts the invited user, but on the 2.0.0 and 2.1.0 releases the
  adoption leaves the address as it was, so an invitee who has not proved the
  email yet is refused as unverified, with Google or with any other method,
  and has to prove it first. Appwrite marks the address verified on that
  adoption from the 2.5.0 release on, and there only when the provider's
  address is on the same domain as the member's; from the 2.6.2 release on it
  also revokes that user's other credentials, so from there an invitee who
  signs in with Google halfway through the recovery proof loses the recovery
  link. On the floor and on the pin the invitee keeps that link: it is bound
  to the account it was issued for and expires as Appwrite sets it. A generic
  `oidc` provider behaves like Google when its issuer claims the address
  verified and like Microsoft when it does not, so the dealer must point it
  at an issuer that claims verified only for addresses it verified.

- **Order:** `email_password` first; then `google` and `microsoft`; then
  `magic_link` and `oidc` on the same start and callback route. The start
  route (`signinmethods.Start`) stops answering `not_implemented` for a
  method as its slice lands.
- **What the browser touches.** For the OAuth2 methods the browser passes
  through Appwrite's provider redirect by top level navigation, carrying no
  Gable credential; it never holds an Appwrite session or key, and no Gable
  script calls Appwrite (the content security policy keeps
  `connect-src 'self'`). Appwrite's endpoint must therefore be reachable from
  staff browsers when an OAuth2 method is allowed; for email and password and
  the magic link it need only be reachable from the core.
- **The door's config changes.** In slice 3 the staff config's `start_url`
  for `email_password` becomes `/api/v1/auth/sign-in`, with a config test. The
  portal's own login page posts to `/api/portal/v1/login` directly, as it
  does today, and does not read this config. The `oidc` row's stored issuer
  URL and client id are retired in slice 4: the issuer is configured in
  Appwrite, and the row keeps only its label.
- **The `state` check.** Every redirect method's start sets a random `state`
  in a cookie that is `HttpOnly`, `Secure`, `SameSite=Lax` (the return is a
  cross site top level navigation, on which a Strict cookie is not sent), on
  `Path=/api` (the session cookie's path, so the cookie rides to the
  callback; a cookie left on the start route's own path would never be sent
  there), single use, short lived, and cleared at the callback, and puts the
  same value in the query of the `success` and `failure` URLs (or the magic
  link's URL) it gives Appwrite; Appwrite appends the user id and secret to
  that URL and keeps the query. That query is the one place the state
  travels: Appwrite's own `state` parameter is not used, so the floor of
  section 8 does not move for it. Before the 2.4.0 release that parameter
  existed only on Appwrite's internal OAuth2 endpoints, carrying Appwrite's
  own login state (the success URL, the failure URL and the token slot); from
  2.4.0 `createOAuth2Token` takes a caller's `state` and returns it unchanged
  on the success and failure URLs. Gable uses neither, and needs no such echo.
  For the magic link the
  `state` binds the link to the browser that asked for it, and the one time
  secret is what authenticates it: the browser carries that secret in the
  link to Gable's callback, as it does Appwrite's secret on the OAuth2
  return, and only the core spends it. Gable's callback refuses a missing,
  mismatched or reused `state` before it exchanges anything: the single use
  is the `pending_sign_ins` row of section 2, not the cookie, which is only
  what carries the value to the callback. A one time secret Appwrite has
  already exchanged is refused by Appwrite, and the callback answers the
  fixed refusal. A magic link opened on another device or browser fails the
  check by design: that is the protection against a link someone else sent
  signing a person into the sender's account.
- **The magic link start creates nothing and reveals nothing.** Appwrite's
  magic URL call creates a user when the email has never been used (the id it
  is given is used only then), and the
  core's calls are not rate limited by Appwrite. So the start looks the
  email up (`users.list`) and calls `account.createMagicURLToken` only with
  the id of an existing user who is a confirmed Team member with a mapped
  role and a verified email; it never passes a new id. It answers the same
  fixed reply whether or not it sent a link, and it carries the start
  route limits of section 2 (per address and per email).
- **After the exchange.** A user id and secret for a person who is not a
  confirmed Team member with a mapped role and a verified email is refused
  like any sign in (section 2), and the Appwrite session the exchange opened
  is ended first. What Appwrite records for a provider sign in is known at
  source (Context): a Google sign in is recorded against the invited user, a
  Microsoft sign in that meets no known user makes one of its own, and a
  provider sign in whose address the provider does not report as verified,
  and that would adopt an existing user, is refused. A user Appwrite records
  that is no member is
  refused, so the invite (section 6) still decides who may sign in.
- **The first page after a callback.** The callback answers with
  `Referrer-Policy: no-referrer` and redirects at once, so the user id and
  secret it carries reach no referrer header and no script on the landing
  page. It then sets the Strict session cookie and redirects to the desk.
  That navigation ends a cross site chain,
  so the Strict cookie may not ride the document request; the desk is a
  static bundle that needs none, and its first API fetch is same site and
  carries the cookie. A shell's sign in ends at the same callback, which then
  redirects to the shell's return target with its one time code (section 3).
  Appwrite only ever redirects to Gable's callback, so the shells' `gable`
  scheme and loopback targets are registered with the core (section 3), never
  as Appwrite platforms.

### 5. Teams, roles and branches

- **One Team per dealer deployment.** The core is configured with one Team id;
  confirmed membership in it is what makes a person staff. Branches are not
  Teams.
- **Membership roles map to Gable roles by a fixed prefix:** `gable-owner`,
  `gable-admin`, `gable-sales`, `gable-finance`, `gable-purchasing`,
  `gable-warehouse`, `gable-cashier`, `gable-driver`. The core strips the
  prefix and ignores every other role string. Appwrite's own `owner` role
  (who may administer the Team) maps to nothing in Gable, so administering
  the Team and owning the dealership stay separate facts, and a Team shared
  with other applications cannot grant a Gable role by accident. That last
  claim holds only while no member holds it: Appwrite lets a Team `owner`
  rewrite any member's roles and invite any address with any roles, so a
  Team `owner` can mint a `gable-owner` member outside Gable, with no Gable
  audit row. **A Team `owner` is therefore as strong as every Gable role
  together**, and the operator rule of section 8 says no member holds it,
  because Gable's own key administers the Team. The core cannot make that
  rule hold on its own: Appwrite lets a Team `owner` write the roles of its
  own membership, so an owner added outside Gable could add `gable-owner` to
  itself and pass a check that looks for a mapped role. **The core therefore
  refuses sign in for any membership that holds Appwrite's `owner` at all**,
  whatever else it holds, and the staff screen flags every Team owner the
  core sees in a membership list. The role
  list is a code constant tested against the guards, so a role a guard names
  and the mapping misses fails a test. The one guard naming `user` (the
  `/uploads/` route) is widened in slice 2 to every mapped staff role, and
  `user` leaves the vocabulary. That route's published contract
  (`core/api/fragments/uploads.yaml`) names the old guard, so slice 2 changes
  that text, lists the change in `docs/refactor/CONTRACT-CHANGES.md` and
  regenerates the client schema from it.
- **Branches stay in Gable.** Branch grants and the home branch stay rows of
  `user_locations`, written through the audited users routes, keyed by the
  new `sub`. Per branch Teams were weighed and refused: they would copy an
  audited Gable table into a second store, lose the home branch, and change
  ADR 0010's derivation of companies. `owner` and `admin` keep their every
  branch reach without a header.
- **The staff roster is linked, not merged, from slice 2.** A `staff` row
  carries the `sub` of the person it describes (a nullable column, unique
  when set), so the integration module grants, the counter PIN and the sign
  in identity name the same person; the roster stays the surface ADR 0009's
  `admin:staff` scope covers. Until the invite (slice 6) sets the link
  itself, an `owner` or `admin` sets it on the staff screen by picking, for
  a staff row, a person who has signed in (the core lists the subjects its
  sessions have seen and reads each person's email from Appwrite by the stored
  Appwrite user id, so the session row holds no email); setting and clearing
  the link are audited.
  No staff sign in is in use before slice 2, so no `user_locations` row keyed
  to an older subject needs carrying over.

### 6. Inviting, changing and removing staff

- **Invite.** From Gable's admin, an `owner` or `admin` invites by email with
  roles and branches. The core looks the email up (`users.list`,
  `users.read`):
  - **No Appwrite user:** the core creates one with no password and its email
    unverified (`users.create`, `users.write`).
  - **A user who is already a confirmed member of the Team:** the invite is
    refused as a duplicate; the admin changes roles instead.
  - **A user who exists but is not a member:** the invite is refused, with a
    reason the admin sees ("an account with this email already exists
    outside the Team"). Gable never adopts an account someone else may have
    registered under the invitee's email, and never deletes one: resolving
    it is the Appwrite administrator's act.

  Gable's invite and the `microsoft` method are two different onboards, and
  one person can take only one of them. The invite creates the Appwrite
  account, and on the 2.x line Appwrite then refuses a Microsoft sign in for
  that address: the callback finds the invited user by the provider's email
  and, because the Microsoft adapter reports no address as verified, takes
  the refusal branch before any provider identity is created, so the next
  sign in takes it as well. A dealer that allows `microsoft` therefore
  onboards through the Appwrite console as section 4 describes, and Gable's
  invite serves the dealers that sign in with email and password or the
  magic link.

  For a user it created, the core adds the Team membership with the mapped
  roles (`teams.createMembership` with the API key, `teams.write`, which
  Appwrite adds at once, confirmed, with no email) and writes the branch
  grants in the same act. The invitee then **proves the email** before any
  sign in can succeed: the core sends, through Appwrite, a link to a Gable
  page (account recovery, `account.createRecovery`, `sessions.write`, for a
  dealer that allows `email_password`, where the page also sets the
  password and the core completes it with `account.updateRecovery`; a magic
  URL token to Gable's own proof route otherwise). The magic URL proof has
  its own route, `/api/v1/auth/verify`, and not the redirect callback: the
  proof was started by the admin's invite and not by a browser that asked,
  so the browser that opens the email holds no `state` cookie and the
  callback's `state` check would refuse the proof every time. The route
  takes the one time secret alone, exchanges it for an Appwrite session,
  ends that session, issues no Gable session and answers that the person
  should now sign in. The proof route and the recovery page both answer with
  `Referrer-Policy: no-referrer`, because the user id and the secret ride in
  their URLs and the site's own default would send the full URL to the page's
  own requests. The exchange proves the email: Appwrite marks the
  address verified when it creates the session from a magic URL token, and the
  recovery completion marks it verified with the new password
  (`account.updateRecovery`), so both proofs mark the address verified in
  Appwrite and the core never writes the verification itself. The members only
  rule of the magic link start
  governs the door's start route and not this proof call: the invitee is not
  a verified member yet, and that is what the proof is for. Until the email
  is proved every sign in for that user is refused (section 2), so a person
  who never received the email cannot sign in as the invitee by any method.
  The link expires as Appwrite sets it; an expired link fails with a plain
  message, and the admin can resend the invite, which sends a fresh link.
  Appwrite sends these emails, so the dealer's Appwrite needs its mail
  settings.

  A dealer may also add members in its Appwrite console; Gable then sees the
  person at first sign in, with roles and no branches until one is granted,
  and only if the Appwrite user's email is verified.
- **Change roles.** Gable's admin updates the membership's roles
  (`teams.updateMembership`, `teams.write`) and re-reads the person's open
  sessions at once.
- **Remove.** Gable's admin removes the membership
  (`teams.deleteMembership`, `teams.write`) and revokes the person's core
  sessions in the same act. A removal or block made in the Appwrite console
  takes effect at the person's next refresh (section 2).
- Each act writes an audit row, as the users grant routes do, and an
  outbox event (ADR 0003); the users grant routes today write the audit row
  only.

### 7. Dev mode, the PIN step up and the JWKS path

- **`AUTH_MODE=dev` exists only in a dev build.** Slice 7 builds that, and
  today it is not there: `core/Dockerfile` builds one image with no build
  argument and `docker-compose.yml` passes no such argument to the core
  image. Slice 7 gives `core/Dockerfile` a build argument that is empty by
  default and, when set, adds the tag `devauth` to its `go build`;
  `docker-compose.yml` passes it for the local stack, and nothing that
  builds an image to publish does. In `.github/workflows/ci.yml`, which sets
  `AUTH_MODE: dev` for the whole workflow, slice 7 gives the Backend job's
  test commands and the Local stack job's core build the tag, and the Docker
  Build job builds the image without the argument and
  asserts that its binary exits at start with `AUTH_MODE=dev`. The Playwright
  job does not build through compose, so the tag goes there too:
  `web/scripts/e2e-stack.sh` builds the core with `go build` and starts it
  with `AUTH_MODE=dev`. A binary
  built without the tag exits at start when `AUTH_MODE=dev` is set; a dev
  build exits when both dev mode and the Appwrite settings are set. The
  demo people of S13 become seeded users of the local Appwrite, so the walks
  sign in for real. The local path for a developer is that compose stack: a
  core run from source with `AUTH_MODE=dev` is refused by the rule above,
  which is what the tag is for. The repository's own `docker-compose.yml` keeps
  `AUTH_MODE=dev` for the persona cards it seeds, and it is not the stack the
  walks run on: the walks run on the separate Appwrite compose of slice 1,
  with a core built without the tag.
- **The counter PIN step up stays as ruled beside this.** A manager's PIN at
  the terminal (hashed, per staff member, set on the staff screen) authorizes
  one act under the cashier's session; it is not a sign in and opens no
  session. Under an Appwrite session it keys on the manager's staff row and
  that row's linked `sub` (slice 2), and the core reads the manager's
  membership live with its API key at the step up: a money act, so any
  answer that is not definite refuses, and a removed or blocked manager's PIN
  is refused at once. How a presented PIN finds its staff row is the PIN
  slice's business; this record's roster link and that lookup agree on the
  same staff row, so the two cannot drift apart.
- **The JWKS path retires.** The counter's second person proofs run on a
  test issuer through the JWKS path until staff sign in on Appwrite exists.
  Slice 8 removes the path, so Gable has one staff sign in path, and it
  lands only after slice 1 is green and the counter's close walk has run on
  Appwrite sign in on the web (slices 1, 2, 3 and 7). The counter's walk
  moves to Appwrite as soon as those slices land; it does not wait for the
  shells.

### 8. Configuration

Gable needs only four settings: `APPWRITE_ENDPOINT`, `APPWRITE_PROJECT_ID`,
`APPWRITE_TEAM_ID` and `APPWRITE_API_KEY`, plus its own public origin, from
which it builds its callback URL (`/api/v1/auth/callback`) and its invite
proof URL (`/api/v1/auth/verify`). The key holds exactly these scopes, the
narrowest set that covers the record:

| Scope | Calls |
|---|---|
| `sessions.write` | the email and password session, the OAuth2 token URL the core asks Appwrite for (the browser then navigates to that URL), the magic URL token, the secret exchange, recovery |
| `users.read` | the user's status, email verification and multi factor flag at sign in and refresh; the email lookup at invite and at the magic link start |
| `users.write` | one call only, caused by a Gable admin's invite: `users.create` |
| `teams.read` | the membership read |
| `teams.write` | add, change and remove a membership |

Ending the Appwrite session the core opened is not in that table: the core
ends it on that session's own secret, and Appwrite's own scope for that call
is the session's, so no key scope covers it.

**The minimum Appwrite release is 2.0.0.** Gable targets the Appwrite 2.x
line on PostgreSQL, and refuses to start against any 1.x Appwrite. Within
that line the floor is 2.0.0, the first release whose provider address
cannot take over an existing account: the gate that refuses an unverified
provider email before Appwrite adopts the user with that address arrives in
1.9.0, but on 1.9.0 the Microsoft adapter still reports every address it
returns as verified, so the gate never refuses a Microsoft sign in there;
from 2.0.0 the adapter reports no address as verified and the gate refuses.
The core reads Appwrite's version at start (`GET /v1/health/version`) and
refuses to start when the version it reads is below 2.0.0. When Appwrite
cannot be reached at start, the core starts and keeps every provider redirect
method closed at the door until it has read a version at or above the floor;
email and password sign in, the invite proof and the PIN step up do not
depend on the floor. Any version read later that is below the floor closes
every Appwrite method, email and password included, and logs it, so a
deployment that is downgraded under the core is refused rather than served.

The key lives only in the core's environment, injected by the operator,
never committed, logged, sent to a client or written to a table, and every
`users.write` call writes an audit row (section 2). Gable holds no provider
client id or secret.

`users.write` reaches every user of the Appwrite project: a holder of the
key could change or remove any of them, even though the core makes one call
under it, the invite's `users.create`. So the project Gable is configured
with must hold only the people Gable serves (its own project, its users not
shared with another application). An operator who points Gable at a project
shared with other applications accepts that Gable's key reaches all of
their users.

In the Appwrite project the operator:

- adds Gable's public hostname as a **Web platform** (the project's platform
  list). Appwrite accepts the `success` and `failure` URLs of
  `createOAuth2Token`, and the URLs of the magic link and recovery, only on
  a hostname in that list, so Gable's callback must be on it;
- runs the Appwrite 2.x line on PostgreSQL, 2.0.0 or later, the floor of this
  section;
- switches off, under Auth, the client sign in methods Gable does not use
  (email and password, magic URL, email OTP, phone, anonymous, invites and
  JWT), and enables only the OAuth2 providers the dealer allows, entering each
  provider's client id and secret in its OAuth2
  settings, registering at the provider the redirect URI Appwrite shows
  there (Appwrite's own callback, not Gable's). Those switches are the
  project's own `auths` settings, and Appwrite's server reads them on every
  request to a route in its `auth` group
  (`app/controllers/shared/api/auth.php`), so a client call to the sign up
  (`POST /v1/account`), to the client magic URL token or to any other client
  method is refused while that method's switch is off; a key call skips that
  check, so the core's own email and
  password sign in still works with email and password off, which slice 1
  proves, while the client sign up and the client magic
  URL token stay closed, so no stranger can sign up under an invitee's
  address and block the invite;
- creates the Team with the key, through the API, which is how a Team is
  made with no owner membership at all: a Team created in the Appwrite
  console gives its creator the owner role, and that person could never sign
  in to Gable (section 5). So if the Team was created in the console, the
  operator removes the console creator's owner membership with the key
  before adding staff;
- gives no member Appwrite's Team `owner` role, because Gable's key
  administers the Team and a Team `owner` can rewrite any member's roles or
  invite any address with any roles, which is as strong as every Gable role
  together (section 5);
- for `microsoft`, enters the dealer's own Entra tenant id in Appwrite's
  Microsoft provider settings. Appwrite requires a tenant on every release
  Gable supports, and a tenant that accepts every Entra tenant is not an
  acceptable configuration for Gable: the floor stops the takeover of an
  existing account, and nothing else stops a person outside the dealer's
  tenant from reaching the sign in at all, which would leave Appwrite making
  a user for them;
- sets Appwrite's mail settings, for the magic link and the invitee's
  recovery email.

One more thing is the operator's: Gable is served over https, because the
session cookie is `Secure` and a browser accepts a `Secure` cookie only on a
secure origin, so a dealer testing on a plain http host over the LAN cannot
sign in. The local stack serves `http://127.0.0.1`, which browsers treat as a
trustworthy origin, so the local walk works.

`GET /api/v1/auth/config` keeps answering public facts only.

### 9. Tests

- **A local Appwrite in a container**, started by a compose file for CI and
  for the lead's walks, with a setup script that creates the project, the
  Team, the platform and one seeded user per role through the API. CI runs
  it in its own job on the paths it guards, because the stack is heavy. The
  compose runs Appwrite on PostgreSQL, like the deployment it stands in for,
  and pins the image at 2.1.0, the release the hosted deployment runs, so
  every slice is proven against the release it ships with.
- **The floor:** the core reads the local Appwrite's version and starts; a
  version below 2.0.0 refuses the core's start; a version it cannot read
  keeps every provider redirect method closed at the door while email and
  password sign in still answers; a core that read a 2.x version at start
  and later reads a 1.x version closes every Appwrite method, email and
  password included, and logs it.
- **Fail closed:** no cookie, an unknown, a revoked, an idle expired and an
  absolutely expired session each answer 401 on a guarded route and never
  pass through; the core with Appwrite settings and no JWKS starts with the
  session middleware mounted; dev mode with Appwrite settings exits; a build
  without `devauth` exits on `AUTH_MODE=dev`.
- **The session at rest and after sign out:** the session table holds no plain
  session id (the key is its hash), and after sign out that id no longer
  works on any route.
- **An unreachable Appwrite:** the session keeps working on its snapshot until
  the snapshot reaches its maximum age, and the session is refused once that
  age passes.
- **Machine keys:** a machine key still reaches its scoped routes, is still
  refused on the key management routes, and is not asked for the CSRF header
  (the header check is on the cookie path).
- **Sign in refusals,** each with the one fixed answer: an unconfirmed
  membership, a blocked user, a deleted user, an unverified email, a member
  with only unknown roles, a member whose membership holds Appwrite's
  `owner`, a non member, and an
  account that answers `mfa: true` (with its admin facing reason).
- **The session cookie's own attributes:** `HttpOnly`, `Secure`,
  `SameSite=Strict`, `Path=/api` and no `Domain`.
- **A Team `owner`:** a member whose membership holds Appwrite's `owner` is
  refused at sign in whatever else it holds, including `gable-owner` that the
  member wrote for itself, and the staff screen flags every Team owner the
  core sees in a membership list.
- **The provider's address never takes an account over:** on the local
  Appwrite a provider sign in whose address is not verified and is the
  address of an existing member (Appwrite's own `mock-unverified` provider)
  is refused, never yields that member's user id, and leaves that member's
  session alive; a provider sign in whose address is not verified and is no
  member's makes no session for anyone.
- **Microsoft's console step:** a provider sign in that meets no known user
  is refused with the fixed answer, and after an administrator marks that
  user verified and adds the membership, the same sign in returns that
  member's user id and secret and the core issues that member's session.
- **A verified provider address does not prove the email:** an invited,
  unproved user who signs in with a provider that reports the address
  verified is refused as unverified on the floor and on the pin, and is
  admitted after the email proof.
- **Credential precedence:** an invalid session cookie with a valid bearer,
  and an invalid bearer with a valid cookie, each answer 401.
- **CORS:** the CSRF header allowed for a configured origin; a disallowed
  origin's preflight refused.
- **Roles and subjects:** every `gable-` role to its Gable role; every role a
  guard names covered by the mapping, and the set of roles the widened
  `/uploads/` guard admits pinned by a test; a `sales` session cannot release
  a hold; a `["sales","finance"]` session's exposure view does not depend on
  the order of `roles`; no handler reads `role` alone; the `sub` is the same
  UUID for the same user; `cashierOf` and the other UUID readers resolve it;
  a cashier who is also an admin cannot approve the override on their own
  sale; branch grants hold under the new `sub`; no session and no shell token
  ever carries an empty role set.
- **Sessions:** a new session id on every sign in and a presented id
  ignored; the idle, absolute and snapshot age limits; a definite removal or
  block revoking at refresh; the CSRF header missing refused, and a cross
  site request carrying it refused by the cookie; a cross site form post to
  the sign in route (no custom header, a foreign `Origin`) refused, and the
  same post with the header or Gable's own `Origin` admitted; the
  concurrency test on the session table.
- **Redirect methods:** a callback with a missing, wrong or reused `state`
  refused before any exchange, and the `state` cookie sent as Lax on
  `Path=/api`, so that it reaches the callback at all; a `state` whose
  `pending_sign_ins` row is past its expiry refused; a replayed user id and
  secret (already exchanged) refused; a valid user id and secret for a user
  not in the Team, or unconfirmed, or holding no mapped role, refused with
  the fixed answer and its Appwrite session ended; a start for a method the
  allowlist does not hold refused; the `success` and `failure` URLs given to
  Appwrite are Gable's callback and nothing else, and the callback answers
  with `Referrer-Policy: no-referrer`; a magic link start for an unknown
  email or a non member creates no user, sends no mail, sets the same cookie
  and answers exactly
  as for a member.
- **Three callbacks on one `state`:** exactly one exchange happens and the
  others get the fixed refusal, and three refreshes on one shell refresh
  token yield one rotation or a revoked family, never two live tokens (the
  repository's concurrency rule: pool size 4, three contenders).
- **Removal and role change through Gable:** a removal revokes the person's
  web session and their shell family on the next request, and a role change
  replaces the snapshot at once.
- **A staff session never passes `PartnerAuth`**, and no partner route
  answers a staff token.
- **Shells:** a wrong PKCE verifier, a reused one time code, an expired code,
  a return target other than the registered scheme or loopback, and a
  rotated refresh token reused revoking the family.
- **Limits and audit:** the sign in and start route limits per address and
  per email, the redemption and refresh limits; one audit row per event of
  section 2, and one per `users.write` call.
- **Invite and PIN:** an invited person with no prior Appwrite account
  reaching a first sign in through the recovery page, and through the magic
  URL proof; an invitee refused at sign in until the email is proved; an
  invitee proving by magic URL in a browser that never asked for the link,
  and so holds no `state` cookie, reaching the proof route, with the address
  proved, no Gable session issued and the answer to sign in; a pre registered
  account with the invitee's email, not a member, not adopted (the invite
  refused with the admin facing reason, no membership added); a second invite
  for a confirmed member refused as a duplicate, with the membership's roles
  left as they were; an expired
  invite link failing cleanly and a resent invite working; a removed
  manager's PIN refused at once.
- **Playwright, in two jobs.** The Playwright job keeps the persona flow and
  the session expired panel on its own dev mode stack (`web/scripts/e2e-stack.sh`
  builds that core with `AUTH_MODE=dev`, and a dev build exits when the
  Appwrite settings are set, so that stack cannot sign in against Appwrite).
  The Appwrite backed specs, the real sign in, the sign out and one walk per
  redirect method as it lands, run in the local Appwrite job above, against a
  core built without the tag.

### 10. Build plan

| Slice | What it proves or builds | Dev hours |
|---|---|---|
| 1 | The local Appwrite proof: the compose file and setup script, with Appwrite on PostgreSQL and the image pinned at 2.1.0, the release the hosted deployment runs, and a Go integration test that signs a seeded user in by email and password server side, reads the account and the membership, maps the roles, ends the session as the user, and is refused for a non member; it also checks that Appwrite accepts Gable's callback as a `success` URL once the hostname is a Web platform, that the core reads the deployment's version and refuses to start below the floor of section 8, and that the email and password sign in still works through the key with the project's email and password switch off (section 8). Nothing else starts until it is green. | 6 to 10 |
| 2 | The core: the Appwrite client, `staff_sessions` with its migration, the sign in, refresh and sign out routes (the new ones joined to `publicAPIPaths`, so they are not refused by the auth middleware before they can answer), the fail closed session middleware, the UUID `sub`, the `role` readers changed (the exposure event's `actor_role` column among them), the `user` guard widened with the contract text that names it changed, listed in `docs/refactor/CONTRACT-CHANGES.md` and regenerated into the client schema, the roster link set from the staff screen, the limits, the audit rows, the configuration checks, and their tests. | 14 to 20 |
| 3 | The web: the door's form and config against the core, cookie custody, the handoff deleted, the CSRF header, the session expired panel, and the Playwright sign in, sign out and expiry specs. | 6 to 10 |
| 4 | The redirect methods through Appwrite: the start route, Gable's callback with its `state` check and the secret exchange, the `pending_sign_ins` table with its migration (the hash of the state, the method, the client kind, the expiry, the used flag, the subject and the Appwrite user id) and the callback that claims it with one conditional update before it calls Appwrite, `google` and `microsoft` (a provider configured in the local Appwrite against a test provider), the unverified provider address that would take an account over refused, `magic_link` (members only, no user creation) and `oidc`, and the `oidc` row's issuer fields retired. The new routes join `publicAPIPaths`. | 6 to 9 |
| 5 | The shells: the system browser, the deep link or loopback return, PKCE redemption, refresh rotation, keychain custody, each shell's own callback handler (only the desk registers the `gable` scheme, so the work, field and pro shells use the loopback return), and the shell's fields in the `pending_sign_ins` row (the PKCE challenge, the validated return target, the hash of the one time code and its own `code_used` flag). The redeem and refresh routes join `publicAPIPaths` so the shell's own token path is not refused by the auth middleware. | 6 to 10 |
| 6 | Invite with its three cases, the email proof (recovery page, or the magic URL proof on its own route with no `state`) and the verified mark, resend, role change and remove from Gable's admin, with branch grants in the same act. | 8 to 12 |
| 7 | The demo people as seeded Appwrite users, the `devauth` build tag, and the documentation that carries it (`README.md`, `AGENTS.md` and the `AUTH_MODE=dev go run` lines in `docs/modules/`), each of which becomes `go run -tags devauth`. | 4 to 6 |
| 8 | The JWKS path retired (`JWKS_URL`, `AUTH_ISSUER`, its middleware branch and tests), once the counter's close walk has run on Appwrite sign in (section 7), and the `JWKS_URL` lines in `.do/app-demo.yaml`, `.do/app-staging.yaml` and `.do/README.md` changed with it. The partner routes are moved under the portal's own auth or retired with that slice, whichever the partner integration picks (an open question below); a slice that retired the JWKS path and left three live routes answering 401 for every caller is not done. | 2 to 4 |
| | **Total** | **52 to 81** |

Order: slices 1, 2, 3 and 7 first (staff sign in on the web, the demo
people), then 8, 4, 5 and 6. Slice 8 is gated on the counter's close walk
having run on Appwrite sign in (section 7). Slice 4 builds `google` and
`microsoft` before `magic_link` and `oidc`. Slice 1 pins the Appwrite image
at the release the hosted deployment runs, on PostgreSQL. Passkeys are
deferred and not counted.

## Consequences

- A dealer hosting Gable runs Appwrite beside it and nothing more; when it
  allows an OAuth2 method, Appwrite's endpoint faces staff browsers for the
  provider redirect. FutureBuild points the same settings at its own Appwrite
  deployment.
- The dealer runs the Appwrite 2.x line on PostgreSQL, 2.0.0 or later. A 1.x
  deployment is refused at the core's start when its version is readable, and
  a deployment whose version cannot be read keeps the provider redirect
  methods closed at the door. On an older release a provider address could
  take over a member's account, which is what the floor exists for.
- Microsoft needs an administrator's act in the Appwrite console before it
  admits anyone (section 4), and a dealer's own Entra tenant must be pinned
  in Appwrite's Microsoft provider settings.
- The magic link is a same browser method: the link is bound to the browser
  that asked for it and fails in any other, so a dealer that enables
  `magic_link` tells staff to open the link in the browser they asked from.
- Gable depends on Appwrite at sign in, at refresh and at the PIN step up,
  not on every request. An Appwrite outage stops new sign ins and step ups
  and lets running sessions live on their snapshot until its maximum age.
- A removed person keeps access until the next refresh when removed outside
  Gable, and loses it at once when removed through Gable.
- The core gains a session table, an Appwrite client and one callback for
  every redirect method. `RequireRole`, the
  branch walls and ADR 0010 are untouched; the handlers that read `role`
  alone change in slice 2.
- Widening the `/uploads/` guard to every mapped staff role opens the
  dealer's uploaded files to every staff member, where only `admin`, `owner`
  and a holder of `user` reached them. The published contract for that
  route names the old guard, and slice 2 changes it (section 5).
- Providers live only in Appwrite: a dealer configures Google, Microsoft or
  its own issuer once, in Appwrite, and Gable holds no provider secret.
- Gable's Appwrite key holds `users.write`, which reaches every user of its
  project. Gable needs a project of its own; a project shared with other
  applications puts their users within reach of Gable's key (section 8).
- An invitee whose email already has an Appwrite account outside the Team
  cannot be invited until the Appwrite administrator resolves that account.
- ADR 0004's interim exposure (a token readable by script) ends for staff on
  the web when slice 3 lands.
- The total exceeds the matrix's re-sized S9 estimate (16 to 24).

## Open questions

- **Microsoft.** It is a ruled method and this record keeps it in the path,
  but Appwrite reports no Microsoft address as verified, so Microsoft admits
  nobody until an administrator marks the user verified and adds the
  membership in the Appwrite console (section 4). The alternatives, deferring
  Microsoft until Appwrite reports verification for it, or telling a dealer
  that needs Microsoft today to use email and password or the magic link
  instead, are a decision on record for the maintainers.
- **Which onboarding a dealer that allows Microsoft uses.** Gable's invite
  creates the Appwrite account, and Appwrite then refuses a Microsoft sign in
  for that address for good (section 6), so the two paths cannot serve the
  same person. This record takes the console path for such a dealer, and
  Gable's invite for the dealers that sign in with email and password or the
  magic link; a dealer that wants both paths for one Team would need an
  Appwrite side for the Microsoft members, which is a decision on record for
  the maintainers.
- **Where the partner routes go when the JWKS path retires.** The three
  partner routes answer from the JWT path's claims alone, so slice 8 leaves
  them without any. They move under the portal's own auth, which already
  holds a customer context, or they retire with that slice; which one is the
  partner integration's pick, and slice 8 carries whichever is picked (a
  decision on record for the maintainers).

Passkeys are deferred to a later record; the hosted deployment's Appwrite
project and key are an operator's setup, not a design choice.
