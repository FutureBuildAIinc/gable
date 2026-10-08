# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# ADR 0004: web session custody

## Status

Accepted for the Gable v1 refactor (item R1-8). The interim design below is the
v1 design. The target design is built at the cutover (cycle 7), and this record
is replaced by one that supersedes it then.

## Context

The web workspace is two separate bundles on one origin: the front door at `/`
and the desk everywhere else. They are separate documents, so a full page
navigation from the door to the desk destroys the door's JavaScript memory. A
bearer token held only in a module variable would be gone on arrival, and the
user would be asked to sign in twice. The same is true of a reload of the desk.

The rule for every Gable frontend is that a token is held in private memory in
the `@gable/auth` package and is never written to `localStorage`.

## Decision

### Interim (v1): a tab scoped sessionStorage handoff

At sign in, `@gable/auth` keeps the session in memory and also writes one record
to `sessionStorage` under a single package private key. Each bundle adopts the
record once at boot, validating it field by field (shape, a readable JWT, an
unexpired `exp` claim) and dropping it when it does not parse.

Why `sessionStorage` and not `localStorage`:

- `sessionStorage` is scoped to one tab and is discarded when the tab closes. It
  is never shared with another tab and never written to the persistent profile.
  A token in `localStorage` outlives the tab, survives a browser restart and is
  readable from every tab of the origin.
- The session handoff only has to bridge a navigation inside one tab, which is
  exactly what `sessionStorage` is for.

The stored value is removed on sign out and on any 401 from the API. Nothing but
the custody module reads or writes the key; the package does not export it.

### The exposure, stated plainly

`sessionStorage` is readable by any script running on the origin. For the whole
life of the tab, a cross site scripting flaw in either bundle, or in any
dependency either bundle loads, can read the token and send it away. This is the
same exposure `localStorage` has for the tab's life; the difference is only that
it ends when the tab does. The content security policy in `web/nginx.conf`
(`script-src 'self'`, no inline scripts, no `eval`) is the control that narrows
the exposure, not a remedy for it. v1 accepts the exposure because the install
base is a staff workspace behind sign in, and because the target design below
removes it.

### Target (cutover, cycle 7): an HttpOnly cookie issued by the API

At the cutover the API issues the session as an `HttpOnly`, `Secure`,
`SameSite=Strict` cookie at sign in, through the FutureBuild bridge. Both
bundles then send it with `credentials: 'same-origin'`, and no script can read
it. Custody in JavaScript shrinks to the display data the bundles need (name,
roles), which is not a credential.

That change needs server work (the sign in endpoint sets the cookie, a CSRF
check on state changing requests, a sign out that clears it) and a client change
that deletes the handoff. It is deliberately not done in R1-8.

## Consequences

- The door can hand a session to the desk and the desk survives a reload, with
  no second sign in.
- Until the cutover, XSS in the tab can steal the token. Any change that adds a
  script source to the content security policy, or a new third party script,
  needs a review against this record.
- The handoff key is private to `@gable/auth`; a test asserts the package does
  not export it, and the 401 and sign out paths are tested to remove it.
