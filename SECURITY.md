# Security Policy

Thanks for helping keep Gable and the people who run it safe. This document
explains how to report a vulnerability privately, which branches receive
security fixes, how quickly you can expect a response, and the one deployment
setting that most commonly turns a demo into an incident.

## Reporting a vulnerability

**Please do not open a public issue, pull request, or discussion for a
security problem.** Public reports expose every operator running the code
before a fix exists.

Use one of these private channels instead:

1. **GitHub Security Advisories (preferred).** From this repository, open the
   **Security** tab → **Report a vulnerability**, which starts a private
   advisory only you and the maintainers can see:
   <https://github.com/FutureBuildAIinc/gable/security/advisories/new>
   *(If this repository is published under a different path, use that repo's
   Security tab — the private-advisory flow is the same.)*
2. **Email.** Write to **security@futurebuild.ai** *(placeholder — replace with
   your project's real security inbox before publishing)*. Encrypt with our
   published PGP key if one is available; otherwise send a first contact and we
   will arrange an encrypted channel.

To help us triage quickly, please include:

- The affected component and path. Gable is licensed **per component** — naming
  the directory (e.g. `backend/internal/...`, `backend/pkg/apps/...`, `app/...`)
  from [`LICENSE-MAP.md`](./LICENSE-MAP.md) helps us route the report.
- The commit SHA or branch you tested against.
- A minimal reproduction, proof of concept, or the vulnerable code path.
- Impact: what an attacker can read, write, or bypass.
- Any suggested remediation, if you have one.

You may report anonymously. If you want credit, tell us how you would like to
be named in the advisory.

## Supported branches

Security fixes are developed against `main` and flow out through the normal
promotion path.

| Branch | Supported | Notes |
|---|---|---|
| `main` | ✅ Yes | Current trunk. Security fixes land here first. |
| `staging` | ⚠️ In transit | Receives fixes on their way to `main`; not a long-term support target. |
| Forks / vendored copies | ❌ No | Re-base onto a patched `main` and re-apply local changes. |
| Pre-public history / old tags | ❌ No | Not maintained. |

There is no separate long-term-support line yet. Run a recent `main` to stay
patched.

## Response window

These are targets, not contractual guarantees, for a volunteer-and-maintainer
project:

| Stage | Target |
|---|---|
| Acknowledge your report | within **3 business days** |
| Initial assessment (severity, affected versions) | within **7 days** |
| Fix or documented mitigation for confirmed high/critical issues | within **90 days**, coordinated with you |

We follow **coordinated disclosure**: we will agree a disclosure date with you,
credit you in the advisory (unless you prefer otherwise), and publish the fix
and advisory together. Please give us a reasonable window before any public
write-up.

## Scope

- **In scope:** code in this repository — the Go backend, the frontend surface,
  migrations, and the deployment examples under [`.do/`](./.do/).
- **Out of scope:** vulnerabilities in third-party dependencies (report those
  upstream; see [`THIRD-PARTY-NOTICES.md`](./THIRD-PARTY-NOTICES.md)), and the
  non-confidential demo/seed data itself.

## ⚠️ `AUTH_MODE=dev` must never reach production

Gable ships an authentication bypass for local development. When
`AUTH_MODE=dev` is set:

- The backend **skips JWT/JWKS verification entirely**. The auth middleware is
  never constructed (`backend/cmd/server/main.go:144-145`), so requests carry no
  claims at all, and `RequireRole` passes through whenever claims are nil
  (`backend/pkg/middleware/auth.go`). No user is impersonated — the request is
  simply unauthenticated and every role gate opens for it. The effect is full
  admin/owner reach for any anonymous caller.
- The B2B portal auth is likewise bypassed and injects demo customer claims.
- A dev-only default is used for the portal JWT secret.

This is safe **only** on a developer's laptop with non-confidential data. It is
**never** safe on any internet-reachable or production deployment — it hands
every visitor full administrative access.

**Rules:**

1. **Never set `AUTH_MODE=dev` on a public hostname or a production deploy.**
2. Production must run with `AUTH_MODE` unset (or any value other than `dev`).
   The backend is **fail-closed**: with `AUTH_MODE` ≠ `dev` it *requires*
   `JWKS_URL` and refuses to start without it, and it requires `CORS_ORIGINS`
   to be set.
3. Set a strong `PORTAL_JWT_SECRET` in production; the dev default is used only
   under `AUTH_MODE=dev`.

If you find any reachable environment running `AUTH_MODE=dev`, treat it as a
disclosable vulnerability and report it through the channels above.
