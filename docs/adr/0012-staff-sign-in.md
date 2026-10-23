# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# ADR 0012: staff sign in through an Appwrite deployment

## Status

Proposed for the Gable v1 refactor (item S9, the shell track's sign in row of
`docs/parity/LBM-PARITY-MATRIX.md`, Ruling 4). It stands on ADR 0002 (machine
keys), ADR 0004 (web session custody), ADR 0009 (finer admin scopes) and
ADR 0010 (multi company). When its session slice lands, it supersedes the
interim design of ADR 0004 (the `sessionStorage` handoff) by building that
record's target (an `HttpOnly` cookie issued by the API), and both records
then say so in their Status.

## Context

### The ruling

Gable signs staff in with the Auth and Teams services of an Appwrite
deployment. That holds in two places: FutureBuild's hosted production, where
the Appwrite deployment is the one behind a platform such as FB Console, and
the community source version a dealer hosts, where the dealer runs its own
Appwrite (self hosted, or Appwrite's own cloud). Nothing in this record may
depend on a component a self hosting dealer cannot obtain.

### What the code does today

- **The core verifies outside bearer tokens.** `core/internal/app/serve/serve.go`
  (step 4, around lines 194 to 230) builds `middleware.NewAuthMiddleware` when
  `JWKS_URL` is set. `core/pkg/middleware/auth.go` parses the bearer with the
  JWKS key set, checks the issuer only when `AUTH_ISSUER` is set, checks no
  audience, refuses a role bearing token with no subject, and puts the
  `UserClaims` (`sub`, `email`, `roles`, `role`, `org_id`) in the context.
- **Roles come from the token.** `RequireRole` admits a request when the
  claims' `role` or any of `roles` is in the guard's list. The guards in
  `serve.go` and `wire_branch_wall.go` use `owner`, `admin`, `sales`,
  `finance`, `purchasing`, `warehouse`, `cashier`, `driver` and `user`.
  With no claims and no machine key (dev mode), every guard passes.
- **Branches come from Gable's own table.** `core/pkg/middleware/branch.go`
  checks `X-Branch-Id` against `user_locations` rows keyed by the token's
  `sub`; `owner` and `admin` may omit the header and reach every branch; the
  list wall (`Wall`, ADR 0007 section 2.3) holds a list to the caller's
  grants. ADR 0010 derives a user's companies from the same grants. The
  grants are written by the audited users routes ADR 0009 scopes as
  `users:grants`.
- **The staff roster is a separate thing.** `core/internal/staff` owns the
  `staff` and `module_grants` tables (an email, a free text role label, an
  active flag, module grants for an integration). Its own package doc says a
  staff row is distinct from a JWT `sub` user.
- **The web has no sign in flow.** Nothing under `web/` holds an
  `authorization_endpoint`, a `code_verifier` or a `redirect_uri`.
  `web/packages/auth` holds session custody (ADR 0004: a token in memory plus
  a tab scoped `sessionStorage` record), the dev persona session, the 401
  `gable:session-expired` event, and the runtime config of sign in methods
  read from the public `GET /api/v1/auth/config`.
- **The sign in methods exist only as a list.** `core/internal/signinmethods`
  holds the dealer's allowlist of six codes (`email_password`, `passkey`,
  `magic_link`, `google`, `microsoft`, `oidc`). Its `Start` (`service.go`,
  lines 227 to 238) answers `StartNoRoute` for email and password and
  `StartNotImplemented` for the other five.
- **The one server sign in is the contractor portal's**
  (`POST /api/portal/v1/login`, with its own middleware in
  `core/pkg/middleware/portal_auth.go`). Staff have none.
- **The persona cards run on `AUTH_MODE=dev`,** which the local
  `docker-compose.yml` sets and which disables staff authentication.
- **The Tauri shells already hold tokens safely.** `tauri/core/src/tokens.rs`
  keeps an access and a refresh token in the OS keychain, exposes only the
  access token over IPC, and clears both at sign out. The desk shell
  registers the `gable` deep link scheme (`tauri-plugin-deep-link`).

### What Appwrite offers

- **Auth.** Email and password, magic URL, email one time password, OAuth2
  providers (Google, Microsoft and a generic `oidc` provider among them),
  passkeys in recent releases, custom tokens, and multi factor
  authentication: https://appwrite.io/docs/products/auth
- **Server side sessions.** A server holding an API key with
  `sessions.write` creates the session itself (email and password, or the
  OAuth2 token flow with a `state` check) and keeps the session secret on
  its own side: https://appwrite.io/docs/products/auth/server-side-rendering
- **JWTs.** `account.createJWT` returns a short lived token that a server
  passes back to Appwrite with `setJWT` to act as the user:
  https://appwrite.io/docs/products/auth/jwt . Appwrite publishes no JWKS for
  it: the token is signed HS256 with the Appwrite server's own master
  secret, so only Appwrite can verify it.
- **Teams.** A team has memberships; each membership carries a list of role
  strings; an `owner` role is reserved by Appwrite for those who may invite
  and remove members; an invitation is a membership the invitee confirms:
  https://appwrite.io/docs/products/auth/teams
- **Custom tokens.** A server mints a one time secret for a user and the
  client exchanges it for a session:
  https://appwrite.io/docs/products/auth/custom-token
- **Self hosting.** A Docker host (Docker Compose) or the Helm chart; the
  documented minimum is two CPU cores and 4 GB of memory:
  https://appwrite.io/docs/advanced/self-hosting and
  https://appwrite.io/docs/advanced/self-hosting/installation

### What FutureBuild's OIDC bridge is

FutureBuild runs an OIDC bridge in front of its own Appwrite deployment so
that tools with native OIDC can sign in through it. As built, it:

- serves its own sign in form, email and password only, and checks the
  password by opening an Appwrite session server to server; an account with
  Appwrite's multi factor setting on cannot sign in through it;
- gates every client on one list of required Teams, and puts the matched
  Team ids (not the membership roles) in a `groups` claim; the subject is
  the Appwrite project, user id and registration;
- registers confidential clients only (a client secret, PKCE required) with
  exact `https` redirect URIs: no public client, no loopback address and no
  custom scheme;
- signs ID tokens ES256 and publishes a JWKS, but issues opaque access
  tokens, so only its ID token is a JWT the core's JWKS check could read;
- is FutureBuild's own component and is not published with Gable.

No sign in through that bridge has yet completed in FutureBuild's
production. Whichever path this record takes, the first build slice proves it
end to end on a local Appwrite before anything relies on it.

## Decision

### 1. The options weighed

**(a) Through an OIDC bridge.** An OIDC issuer backed by the Appwrite
deployment signs staff in; the web runs an authorization code flow with PKCE;
the core verifies the issuer's tokens through its existing JWKS check.

- A self hosting dealer would have to run an OIDC issuer in front of its
  Appwrite. Appwrite is not itself an OIDC provider, and FutureBuild's bridge
  is not published, so the dealer has nothing to run. Gable would have to
  publish and maintain a bridge of its own: a second identity service with
  its own database, signing keys and rotation, which is more than this item
  can carry and more than a dealer should host.
- The JWKS check "fits" only the signature. The bridge's access tokens are
  opaque; using its ID token as the API bearer is the wrong token for the
  job. Its tokens carry no roles, so `RequireRole` would refuse every staff
  request until the core looked roles up itself, and the core checks no
  audience today.
- The bridge as built cannot serve the shells (no loopback, no custom
  scheme, no public client), nor four of the six ruled methods (Google,
  Microsoft, magic link and passkey; its form is email and password only).
- Teams map only to "may sign in": the `groups` claim has Team ids and no
  membership roles.

**(b) Appwrite directly.** Gable talks to Appwrite's own Auth and Teams APIs.

- A self hosting dealer deploys Appwrite (one documented install) and Gable,
  and nothing else. FutureBuild's hosted production points the same settings
  at its own Appwrite deployment.
- Every ruled method is an Appwrite method (section 4), so the six codes of
  `signinmethods` map onto Appwrite without a second identity service.
- Teams give roles, not just membership (section 5).
- The core cannot verify Appwrite's JWT locally: it is HS256 under the
  Appwrite server's master secret, which no relying service may hold.
  Calling Appwrite to validate on every API request would put Appwrite on
  the hot path of every staff request and on the counter's. So the core
  talks to Appwrite at sign in and at refresh only, and holds its own
  session in between (section 2).

**Recommendation: (b), with Appwrite called by the core.** It is the only
option a self hosting dealer can run from what Gable publishes, it covers the
ruled methods, and it lets Teams carry roles.

### 2. The core holds the session

The core is Gable's one relying party on Appwrite. The browser and the shells
never hold an Appwrite session or an Appwrite JWT.

- **Sign in.** The core opens an Appwrite session through Appwrite's server
  side pattern (an API key holding `sessions.write`), reads the account, reads
  the user's membership in the dealer's Team (section 5), and then ends the
  Appwrite session at once. Nothing Appwrite issued is stored. A person who
  is not a confirmed member with at least one mapped role is refused with the
  one fixed answer every refusal gives.
- **The core's session.** A new table, `staff_sessions`, keyed by a random
  session id that is stored only as a hash, holds the subject, the client
  kind (`web` or `shell`), the roles read at sign in, the creation and last
  use, an idle limit and an absolute limit (both settings), and a revoked
  flag. Writes to it carry the concurrency test the brief requires.
- **The request path.** A session middleware resolves the session to the
  same `middleware.UserClaims` the JWT path fills (`sub`, `email`, `roles`),
  so `RequireRole`, the branch middleware, the list wall and ADR 0010's
  company derivation run unchanged. Machine keys dispatch by shape before
  anything else, exactly as ADR 0002 section 1 says.
- **Refresh.** When the session's role snapshot is older than a setting, the
  core re-reads the membership and the user's status with its API key. A
  definite answer that the person is no longer a member, holds no mapped
  role, or is blocked revokes the session; an Appwrite answer that is not
  definite (unreachable, an error) keeps the session on its old snapshot
  until the absolute limit, so a brief Appwrite outage does not close the
  counter. Role changes therefore take effect at the next refresh, not on
  the next request.
- **Sign out** revokes the core's session and clears the client's copy.
  There is no Appwrite session to end, because none outlived the sign in.
- **The subject.** `sub` is `appwrite:<projectId>:<userId>`, so a subject from
  this path can never collide with one from an outside OIDC issuer, and
  `user_locations` and the audit log key to it as they key to `sub` today.

### 3. Custody, web and shells

- **Web.** The sign in answer sets the session id as an `HttpOnly`, `Secure`,
  `SameSite=Strict` cookie on the API's origin: ADR 0004's target. Both
  bundles send it with `credentials: 'same-origin'`; a state changing request
  must also carry a custom header the core checks (the CSRF control ADR 0004
  names). `@gable/auth` keeps only display data (name, roles, home yard);
  the `sessionStorage` handoff and the token in memory are deleted. A 401
  still fires `gable:session-expired`, and the panel offers sign in again.
  The content security policy keeps `connect-src 'self'`: the browser never
  talks to Appwrite.
- **Shells (Tauri).** A shell is a public client of the core, not of
  Appwrite. Email and password is posted from the shell's own door to the
  core. Every redirect method opens the system browser on the core's start
  route with a PKCE challenge; the core runs the Appwrite flow and finishes
  by redirecting to the shell with a one time code, through the `gable`
  deep link where the profile registers it, or a loopback address the shell
  listens on for that one sign in where it does not. The shell redeems the
  code with its PKCE verifier for an opaque access token and a refresh
  token, which `tauri/core/src/tokens.rs` keeps in the OS keychain as it
  does today. The shell's access token is resolved by the same session
  middleware; its refresh token rotates on use.
- **Machine keys are unchanged** (ADR 0002): still `sk_live_` bearers, still
  scoped, still refused on the key management routes.

### 4. The sign in methods

The six codes of `signinmethods` stay the dealer's allowlist and the door's
runtime config; each maps to an Appwrite method the dealer also enables in
its Appwrite project:

| Gable code | Appwrite method | Core route |
|---|---|---|
| `email_password` | email and password session | `POST /api/v1/auth/sign-in` |
| `magic_link` | magic URL token, then session | start route, then the core's callback |
| `google` | OAuth2 token flow, provider `google` | start route, then the core's callback |
| `microsoft` | OAuth2 token flow, provider `microsoft` | start route, then the core's callback |
| `oidc` | OAuth2 token flow, provider `oidc` (the dealer's own issuer, configured in Appwrite) | start route, then the core's callback |
| `passkey` | Appwrite passkeys, where the dealer's release ships them | open (question 3) |

The start route (`signinmethods.Start`) stops answering `not_implemented` for
a method as its slice lands. Every callback checks a `state` bound to a cookie
on the core's origin before it exchanges anything, as Appwrite's own guide
requires. The `oidc` code covers a dealer that wants its own identity
provider: it is configured in Appwrite, not in Gable.

### 5. Teams, roles and branches

- **One Team per dealer deployment.** The core is configured with one Team id;
  membership in it is what makes a person staff. Branches are not Teams.
- **Membership roles map to Gable roles by a fixed prefix:** `gable-owner`,
  `gable-admin`, `gable-sales`, `gable-finance`, `gable-purchasing`,
  `gable-warehouse`, `gable-cashier`, `gable-driver`. The core strips the
  prefix and ignores every other role string. Appwrite's own `owner` role
  (who may administer the Team) maps to nothing in Gable, so administering
  the Team and owning the dealership stay separate facts, and a Team shared
  with other applications cannot grant a Gable role by accident. The role
  list is a code constant tested against the guards, so a role a guard names
  and the mapping misses fails a test.
- **Branches stay in Gable.** Branch grants and the home branch stay rows of
  `user_locations`, written through the audited users routes, keyed by the
  new `sub`. Per branch Teams were weighed and refused: they would copy an
  audited Gable table into a second store, lose the home branch, and change
  ADR 0010's derivation of companies. `owner` and `admin` keep their every
  branch reach without a header.
- **The staff roster is linked, not merged.** A `staff` row may carry the
  `sub` of the person it describes (a nullable column), so the integration
  module grants and the sign in identity name the same person; the roster
  stays the integration surface ADR 0009's `admin:staff` scopes.

### 6. Inviting and removing staff

- **Invite.** From Gable's admin, an `owner` or `admin` invites by email with
  roles and branches. The core creates the Team membership with its API key
  (`teams.write`) and Appwrite's invitation email whose link lands on a
  Gable page that confirms the membership; the branch grants are written in
  the same act. A dealer may also invite in its Appwrite console; Gable then
  sees the person at first sign in, with roles and no branches until one is
  granted.
- **Remove.** Gable's admin removes the membership through the API and
  revokes the person's core sessions in the same act. A removal made in the
  Appwrite console takes effect at the person's next refresh (section 2).
  Blocking the Appwrite user does the same.
- Both acts write audit rows and outbox events as the users routes do.

### 7. Dev mode, the PIN step up and the JWKS path

- **`AUTH_MODE=dev` stays for the quick local stack only.** The core refuses
  to start with both dev mode and Appwrite settings, and refuses dev mode in
  any build that is not the local compose stack, as it refuses a missing
  `JWKS_URL` today. The demo people of S13 become seeded users of the local
  Appwrite, so the walks sign in for real.
- **The counter PIN step up is a separate ruled slice and stays beside this.**
  A manager's PIN at the terminal authorizes one act under the cashier's
  session; it is not a sign in and opens no session. The core checks, at the
  step up, that the manager's own `sub` holds a role the act requires from
  its latest membership read.
- **The JWKS path stays for now.** A deployment that sets `JWKS_URL` keeps
  today's behaviour. Its retirement, and the audience check it lacks, are
  question 4.

### 8. Configuration

The core reads `APPWRITE_ENDPOINT`, `APPWRITE_PROJECT_ID`, `APPWRITE_TEAM_ID`
and `APPWRITE_API_KEY` (scopes `sessions.write`, `users.read`, `teams.read`,
`teams.write`, injected by the operator, never committed or logged). The
dealer registers Gable's origin, and each shell's deep link or loopback, as
platforms in its Appwrite project, enables the methods it allows, and adds
the OAuth2 provider credentials in Appwrite. `GET /api/v1/auth/config` keeps
answering public facts only.

### 9. Tests

- **A local Appwrite in a container**, started by a compose file for CI and
  for the lead's walks, with a setup script that creates the project, the
  Team, the platforms and one seeded user per role through the API. CI runs
  it in its own job on the paths it guards, because the stack is heavy.
- **Core unit tests** against a stub Appwrite (no network): sign in, refusal
  shapes, membership and status at refresh, the definite and the indefinite
  answers, revocation on removal, the subject shape.
- **Role and branch mapping tests:** every `gable-` role to its Gable role,
  Appwrite's `owner` and unknown roles to nothing, every role a guard names
  covered by the mapping, branch grants unchanged under the new `sub`.
- **Session tests:** idle and absolute limits, rotation of shell refresh
  tokens, the CSRF header, the concurrency test at pool size 4 with three
  contenders on the session table.
- **Token verification tests** for the paths that remain: the JWKS path's
  existing tests, machine keys by shape, and a session id that is not a JWT
  never reaching the JWT parser.
- **Playwright** against the local Appwrite: sign in, sign out, and an
  expired session showing the panel; one walk per redirect method as it
  lands.

### 10. Build plan

| Slice | What it proves or builds | Dev hours |
|---|---|---|
| 1 | The local Appwrite proof: the compose file and setup script, and a Go integration test that signs a seeded user in by email and password server side, reads the account and the membership, maps the roles, ends the session, and is refused for a non member. Nothing else starts until it is green. | 6 to 10 |
| 2 | The core: the Appwrite client, `staff_sessions` with its migration, the sign in, refresh and sign out routes, the session middleware filling `UserClaims`, the configuration checks, and the unit and concurrency tests. | 10 to 14 |
| 3 | The web: the door's form against the core, cookie custody, the handoff deleted, the CSRF header, the session expired panel, and the Playwright sign in, sign out and expiry specs. | 6 to 10 |
| 4 | The redirect methods: magic link, Google, Microsoft and `oidc` through Appwrite, the start route and the callback with its `state` check. | 6 to 10 |
| 5 | The shells: the system browser, the deep link or loopback return, PKCE redemption, refresh rotation, keychain custody. | 6 to 10 |
| 6 | Invite and remove from Gable's admin, with branch grants in the same act, and the staff roster link. | 6 to 8 |
| 7 | The demo people as seeded Appwrite users, and dev mode narrowed. | 3 to 5 |
| | **Total** | **43 to 67** |

Passkeys wait on question 3 and are not counted.

## Consequences

- A dealer hosting Gable runs Appwrite beside it and nothing more; FutureBuild
  points the same four settings at its own Appwrite deployment.
- Gable depends on Appwrite at sign in and at refresh, not on every request.
  An Appwrite outage stops new sign ins and lets running sessions live on
  their last snapshot until their absolute limit.
- A removed person keeps access until the next refresh when removed outside
  Gable, and loses it at once when removed through Gable.
- The core gains a session table and an Appwrite client; `RequireRole`, the
  branch walls and ADR 0010 are untouched because the session fills the same
  claims.
- ADR 0004's interim exposure (a token readable by script) ends for staff on
  the web when slice 3 lands.
- The total exceeds the matrix's re-sized S9 estimate (16 to 24); question 1.

## Questions left for the principal

1. The plan is 43 to 67 dev hours against the S9 row's 16 to 24. Which slices
   make the demo cut (slices 1 to 3 and 7 are the smallest set that signs
   staff in for real on the web)?
2. FutureBuild's hosted production: does Gable use the Appwrite project and a
   Team per dealer on the platform's deployment, and who issues Gable's API
   key with the four scopes there?
3. Passkeys: Appwrite ships them only in recent releases, and a passkey is
   bound to the origin that registers it. Is `passkey` deferred, or built
   with the browser talking to Appwrite for that one method?
4. The JWKS path: retire it once this lands, or keep it (and add the audience
   check) for a deployment that brings its own issuer instead of Appwrite's
   `oidc` provider?
5. Does FutureBuild's bridge stay out of Gable's path entirely, as this
   record assumes, or must Gable also accept its tokens for some tool?
