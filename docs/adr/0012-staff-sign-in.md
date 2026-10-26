# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# ADR 0012: staff sign in through an Appwrite deployment

## Status

Proposed for the Gable v1 refactor (item S9, the shell track's sign in row of
`docs/parity/LBM-PARITY-MATRIX.md`, Ruling 4). It stands on ADR 0002 (machine
keys), ADR 0004 (web session custody), ADR 0005 (the sales and money core),
ADR 0009 (finer admin scopes) and ADR 0010 (multi company). When its session
slice lands, it supersedes the interim design of ADR 0004 (the
`sessionStorage` handoff) by building that record's target (an `HttpOnly`
cookie issued by the API), and both records then say so in their Status.

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
  (step 4, lines 207 to 228) builds `middleware.NewAuthMiddleware` when
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
  claims too. Today that is safe only because step 4 either mounts the JWT
  middleware, which answers 401 first, or runs in dev mode.
- **Some handlers read `role` alone, or a UUID `sub`.** `callerRole` in
  `core/internal/order/handler.go` (line 100) and `caller` in
  `core/internal/ap/handler.go` (line 80) read only `claims.Role`;
  `userRole` in `core/internal/pricing/exposure_handler.go` (around line 444)
  reads `role`, else the first of `roles`. The hold release in
  `core/internal/order/service.go` (around line 1292) refuses only a non
  empty role outside the finance roles. Four readers parse `sub` as a UUID:
  `cashierOf` (`core/internal/pos/handler.go`, line 251), `userID`
  (`exposure_handler.go`, line 417), `core/internal/reporting/handler.go`
  (line 259) and `core/internal/bankrecon/service.go` (line 362); the counter
  override's "a manager other than the sale's cashier" check compares the
  sale's cashier UUID with the approver's `sub`.
- **The guards' roles.** The guards in `serve.go` and `wire_branch_wall.go`
  use `owner`, `admin`, `sales`, `finance`, `purchasing`, `warehouse`,
  `cashier`, `driver`, and `user` once (the `/uploads/` route, `serve.go`
  line 1183).
- **Branches come from Gable's own table.** `core/pkg/middleware/branch.go`
  checks `X-Branch-Id` against `user_locations` rows keyed by the token's
  `sub` (a `TEXT` column); `owner` and `admin` may omit the header and reach
  every branch; the list wall (`Wall`, ADR 0007 section 2.3) holds a list to
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
  https://appwrite.io/docs/advanced/platform/rate-limits
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
  whose user is blocked or deleted, or who holds no mapped role (Appwrite's
  `owner` alone, or only unknown roles).
- **A fresh session every time.** Every sign in issues a new random session
  id from a cryptographic source; any session id the client sent is ignored
  and never reused (no fixation).
- **The core's session.** A new table, `staff_sessions`, keyed by the hash of
  the session id (the id itself is never stored), holds the subject, the
  Appwrite user id, the client kind (`web` or `shell`), the roles read at the
  last membership read and when that read was made, the creation and last
  use, an idle limit, an absolute limit, and a revoked flag. Writes to it
  carry the concurrency test the repository requires (pool size 4, three
  contenders).
- **The request path, fail closed.** The auth layer dispatches in this order:
  a machine key by its `sk_live_` shape (ADR 0002 section 1, unchanged); a
  shell access token by its own fixed prefix (section 3); the session cookie;
  and, until slice 8 retires it, a JWT on the JWKS path. A valid session or
  shell token fills `middleware.UserClaims` with `sub`, `email` and `roles`;
  `role` is always empty. Outside the public paths, a request with none of
  these, or with an unknown, revoked, idle expired or absolutely expired
  session, is answered 401 before any handler runs; it never reaches a guard
  with no claims. Step 4 of `serve.go` mounts this middleware whenever the
  Appwrite settings are present, and the exit stays for a core with neither
  Appwrite settings nor the dev build (section 7).
- **Readers of `role` alone change in slice 2.** Because a session sets
  `roles` only, no handler may depend on `role`: `callerRole`
  (`order/handler.go`), `caller` (`ap/handler.go`) and `userRole`
  (`pricing/exposure_handler.go`) move to `middleware.ClaimsHaveAnyRole` over
  both fields, so a decision never depends on which field, or which order of
  `roles`, a person's roles arrive in; and the hold release in
  `order/service.go` refuses an empty role when a person's claims are
  present (ADR 0005 section 5.2 holds it to admin, owner and finance). Every
  `claims.Role` reader is found by a grep in that slice and covered by a
  test.
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
  key calls, so the core limits its own routes: the staff sign in per client
  address and per email, and the shell's code redemption and refresh per
  client address, refusing past the limit with the same fixed answer plus
  `Retry-After`. The portal login keeps its own limiter.
- **Audit.** Sign in, every refusal (with the attempted email and a fixed
  reason, never the password), sign out, a revocation at refresh (removal,
  block or deletion), a shell refresh token rotation and a reuse that
  revokes a family each write one row through the existing audit logger
  (`serve.go`, step 3c), with the actor's `sub` where there is one and the
  client kind.

### 3. Custody, web and shells

- **Web.** The sign in answer sets the session id as an `HttpOnly`, `Secure`,
  `SameSite=Strict` cookie on the API's origin with `Path=/` and no `Domain`
  attribute: ADR 0004's target. The path is `/` and not `/api` because the
  guarded `/uploads/` route sits outside `/api`. Both bundles send it with
  `credentials: 'same-origin'`; a state changing request must also carry a
  custom header the core checks (the CSRF control ADR 0004 names), which a
  cross site form cannot set. `@gable/auth` keeps only display data (name,
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
  other URL. The shell redeems the code once, before it expires, with its
  PKCE verifier, for an access token and a refresh token, which
  `tauri/core/src/tokens.rs` keeps in the OS keychain as it does today. The
  access token is an opaque random value with a fixed prefix of its own
  (distinct from `sk_live_`), resolved to the shell's row in
  `staff_sessions`. The refresh token rotates on use; presenting a rotated
  refresh token again revokes the whole family.
- **Machine keys are unchanged** (ADR 0002): still `sk_live_` bearers, still
  scoped, still refused on the key management routes.

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
| `email_password` | browser or shell to the core: `POST /api/v1/auth/sign-in` | `account.createEmailPasswordSession` |
| `google` | browser to the core's start route; the core redirects it to Appwrite's `createOAuth2Token` URL for the provider (navigation only); Appwrite runs the provider redirect and sends the browser to Gable's callback with the user id and one time secret | `account.createSession` with that user id and secret |
| `microsoft` | as `google` | as `google` |
| `oidc` | as `google`, with Appwrite's generic OIDC provider pointed at the dealer's issuer | as `google` |
| `magic_link` | the person asks at the door; the core asks Appwrite to email a link whose target is Gable's callback; the link lands there with the user id and secret | `account.createMagicURLToken` with Gable's callback as its URL; then `account.createSession` |
| `passkey` | deferred: not built by this record | none |

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
  cross site top level navigation, on which a Strict cookie is not sent),
  single use, short lived, and cleared at the callback, and puts the same
  value in the query of the `success` and `failure` URLs (or the magic link's
  URL) it gives Appwrite. Gable's callback refuses a missing, mismatched or
  reused `state` before it exchanges anything. A one time secret Appwrite has
  already exchanged is refused by Appwrite, and the callback answers the
  fixed refusal. A magic link opened on another device or browser fails the
  check by design: that is the protection against a link someone else sent
  signing a person into the sender's account.
- **After the exchange.** A user id and secret for a person who is not a
  confirmed Team member with a mapped role is refused like any sign in
  (section 2), and the Appwrite session the exchange opened is ended first.
- **The first page after a callback.** The callback sets the Strict session
  cookie and redirects to the desk. That navigation ends a cross site chain,
  so the Strict cookie may not ride the document request; the desk is a
  static bundle that needs none, and its first API fetch is same site and
  carries the cookie. A shell's sign in ends at the same callback, which then
  redirects to the shell's return target with its one time code (section 3).

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
  with other applications cannot grant a Gable role by accident. The role
  list is a code constant tested against the guards, so a role a guard names
  and the mapping misses fails a test. The one guard naming `user` (the
  `/uploads/` route) is widened in slice 2 to every mapped staff role, and
  `user` leaves the vocabulary.
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
  `admin:staff` scope covers.

### 6. Inviting, changing and removing staff

- **Invite.** From Gable's admin, an `owner` or `admin` invites by email with
  roles and branches. The core finds the Appwrite user by email or creates it
  with no password (`users.create`, `users.write`), adds the Team membership
  with the mapped roles (`teams.createMembership` with the API key,
  `teams.write`, which Appwrite adds at once, confirmed, with no email), and
  writes the branch grants in the same act. For a dealer that allows
  `email_password`, the core then starts Appwrite's account recovery for the
  invitee (`account.createRecovery`, `sessions.write`) with its URL on a
  Gable page; the person sets a password there, the page posts it to the
  core, and the core completes the recovery (`account.updateRecovery`). A
  dealer that allows only redirect methods skips the recovery: the person
  signs in through the provider, and Appwrite attaches the provider identity
  to the invited user by its email (slice 4 proves this on the local Appwrite
  before a redirect only invite is offered). Appwrite sends
  the recovery email, so the dealer's Appwrite needs its mail settings. A
  dealer may also add members in its Appwrite console; Gable then sees the
  person at first sign in, with roles and no branches until one is granted.
- **Change roles.** Gable's admin updates the membership's roles
  (`teams.updateMembership`, `teams.write`) and re-reads the person's open
  sessions at once.
- **Remove.** Gable's admin removes the membership
  (`teams.deleteMembership`, `teams.write`) and revokes the person's core
  sessions in the same act. A removal or block made in the Appwrite console
  takes effect at the person's next refresh (section 2).
- Each act writes audit rows and outbox events as the users routes do.

### 7. Dev mode, the PIN step up and the JWKS path

- **`AUTH_MODE=dev` exists only in a dev build.** The dev bypass is compiled
  only under a build tag (`devauth`) that the local compose build and the CI
  test commands set, and that the release image's build in `core/Dockerfile`
  never sets. A binary built without the tag exits at start when
  `AUTH_MODE=dev` is set; a dev build exits when both dev mode and the
  Appwrite settings are set. A test builds without the tag and asserts the
  exit. The demo people of S13 become seeded users of the local Appwrite, so
  the walks sign in for real.
- **The counter PIN step up stays as ruled beside this.** A manager's PIN at
  the terminal (hashed, per staff member, set on the staff screen) authorizes
  one act under the cashier's session; it is not a sign in and opens no
  session. Under an Appwrite session it keys on the manager's staff row and
  that row's linked `sub` (slice 2), and the core reads the manager's
  membership live with its API key at the step up: a money act, so any
  answer that is not definite refuses, and a removed or blocked manager's PIN
  is refused at once.
- **The JWKS path retires.** It stays only until slice 1 proves Appwrite end
  to end (a test issuer for the counter's second person proofs uses it until
  then); slice 8 then removes it, so Gable has one staff sign in path.

### 8. Configuration

Gable needs only four settings: `APPWRITE_ENDPOINT`, `APPWRITE_PROJECT_ID`,
`APPWRITE_TEAM_ID` and `APPWRITE_API_KEY`, plus its own public origin, from
which it builds its callback URL (`/api/v1/auth/callback`). The key holds
`sessions.write` (sessions, the OAuth2 and magic URL tokens, recovery),
`users.read` (status), `users.write` (the invitee's user), `teams.read` and
`teams.write`; it is injected by the operator, never committed or logged.
Gable holds no provider client id or secret.

In the Appwrite project the operator:

- adds Gable's public hostname as a **Web platform** (the project's platform
  list). Appwrite accepts the `success` and `failure` URLs of
  `createOAuth2Token`, and the URLs of the magic link and recovery, only on
  a hostname in that list, so Gable's callback must be on it;
- enables, under Auth, each method the dealer allows, and for each OAuth2
  provider enters the provider's client id and secret in its OAuth2
  settings, registering at the provider the redirect URI Appwrite shows
  there (Appwrite's own callback, not Gable's);
- sets Appwrite's mail settings, for the magic link and the invitee's
  recovery email.

`GET /api/v1/auth/config` keeps answering public facts only.

### 9. Tests

- **A local Appwrite in a container**, started by a compose file for CI and
  for the lead's walks, with a setup script that creates the project, the
  Team, the platform and one seeded user per role through the API. CI runs
  it in its own job on the paths it guards, because the stack is heavy.
- **Fail closed:** no cookie, an unknown, a revoked, an idle expired and an
  absolutely expired session each answer 401 on a guarded route and never
  pass through; the core with Appwrite settings and no JWKS starts with the
  session middleware mounted; dev mode with Appwrite settings exits; a build
  without `devauth` exits on `AUTH_MODE=dev`.
- **Sign in refusals,** each with the one fixed answer: an unconfirmed
  membership, a blocked user, a deleted user, a member with only Appwrite's
  `owner` or only unknown roles, a non member.
- **Roles and subjects:** every `gable-` role to its Gable role; every role a
  guard names covered by the mapping; a `sales` session cannot release a
  hold; a `["sales","finance"]` session's exposure view does not depend on
  the order of `roles`; no handler reads `role` alone; the `sub` is the same
  UUID for the same user; `cashierOf` and the other UUID readers resolve it;
  a cashier who is also an admin cannot approve the override on their own
  sale; branch grants hold under the new `sub`.
- **Sessions:** a new session id on every sign in and a presented id
  ignored; the idle, absolute and snapshot age limits; a definite removal or
  block revoking at refresh; the CSRF header missing refused, and a cross
  site request carrying it refused by the cookie; the concurrency test on
  the session table.
- **Redirect methods:** a callback with a missing, wrong or reused `state`
  refused before any exchange, and the `state` cookie sent as Lax; a replayed
  user id and secret (already exchanged) refused; a valid user id and secret
  for a user not in the Team, or unconfirmed, or holding no mapped role,
  refused with the fixed answer and its Appwrite session ended; a start for a
  method the allowlist does not hold refused; the `success` and `failure`
  URLs given to Appwrite are Gable's callback and nothing else.
- **Shells:** a wrong PKCE verifier, a reused one time code, an expired code,
  a return target other than the registered scheme or loopback, and a
  rotated refresh token reused revoking the family.
- **Limits and audit:** the sign in limit per address and per email, the
  redemption and refresh limits; one audit row per event of section 2.
- **Invite and PIN:** an invited person with no prior Appwrite account
  reaching a first sign in through the recovery page; a removed manager's
  PIN refused at once.
- **Playwright** against the local Appwrite: sign in, sign out, and an
  expired session showing the panel; one walk per redirect method as it
  lands.

### 10. Build plan

| Slice | What it proves or builds | Dev hours |
|---|---|---|
| 1 | The local Appwrite proof: the compose file and setup script, and a Go integration test that signs a seeded user in by email and password server side, reads the account and the membership, maps the roles, ends the session as the user, and is refused for a non member; it also checks that Appwrite accepts Gable's callback as a `success` URL once the hostname is a Web platform. Nothing else starts until it is green. | 6 to 10 |
| 2 | The core: the Appwrite client, `staff_sessions` with its migration, the sign in, refresh and sign out routes, the fail closed session middleware, the UUID `sub`, the `role` readers changed, the `user` guard widened, the roster link, the limits, the audit rows, the configuration checks, and their tests. | 14 to 20 |
| 3 | The web: the door's form and config against the core, cookie custody, the handoff deleted, the CSRF header, the session expired panel, and the Playwright sign in, sign out and expiry specs. | 6 to 10 |
| 4 | The redirect methods through Appwrite: the start route, Gable's callback with its `state` check and the secret exchange, `google` and `microsoft` (a provider configured in the local Appwrite against a test provider), then `magic_link` and `oidc`, and the `oidc` row's issuer fields retired. | 5 to 8 |
| 5 | The shells: the system browser, the deep link or loopback return, PKCE redemption, refresh rotation, keychain custody. | 6 to 10 |
| 6 | Invite with the recovery page, role change and remove from Gable's admin, with branch grants in the same act. | 6 to 10 |
| 7 | The demo people as seeded Appwrite users, and the `devauth` build tag. | 4 to 6 |
| 8 | The JWKS path retired (`JWKS_URL`, `AUTH_ISSUER`, its middleware branch and tests), once slice 1 is green. | 2 to 4 |
| | **Total** | **49 to 78** |

Order: slices 1, 2, 3 and 7 first (staff sign in on the web, the demo
people), then 8, 4, 5 and 6. Slice 4 builds `google` and `microsoft` before
`magic_link` and `oidc`. Passkeys are deferred and not counted.

## Consequences

- A dealer hosting Gable runs Appwrite beside it and nothing more; when it
  allows an OAuth2 method, Appwrite's endpoint faces staff browsers for the
  provider redirect. FutureBuild points the same settings at its own Appwrite
  deployment.
- Gable depends on Appwrite at sign in, at refresh and at the PIN step up,
  not on every request. An Appwrite outage stops new sign ins and step ups
  and lets running sessions live on their snapshot until its maximum age.
- A removed person keeps access until the next refresh when removed outside
  Gable, and loses it at once when removed through Gable.
- The core gains a session table, an Appwrite client and one callback for
  every redirect method. `RequireRole`, the
  branch walls and ADR 0010 are untouched; the handlers that read `role`
  alone change in slice 2.
- Providers live only in Appwrite: a dealer configures Google, Microsoft or
  its own issuer once, in Appwrite, and Gable holds no provider secret.
- ADR 0004's interim exposure (a token readable by script) ends for staff on
  the web when slice 3 lands.
- The total exceeds the matrix's re-sized S9 estimate (16 to 24).

## Open questions

None in this record. Passkeys are deferred to a later record; the hosted
deployment's Appwrite project and key are an operator's setup, not a design
choice.
