# The deploy recipe

One Compose file that runs Gable on a Coolify based platform such as FB
Deploy. The platform checks out this repository at a commit, builds the
images from the Dockerfiles in that commit, and runs the services the file
describes behind the platform's own reverse proxy.

There are two variants and they are the same file with a few environment
values changed:

- the **demo variant** loads the Gable Building Supply sample data on the
  first start and keeps it through every later redeploy;
- the **customer variant** never seeds anything.

Both sign staff in through Appwrite (ADR 0012, item 345). The recipe has no
dev sign in.

Both hold nothing but what the demo seed writes in the first case, and what
the dealer enters in the second. The demo tenant holds sample data only:
invented customers, invented products and invented money. No dealer, no
customer and no person's data is in it.

## What the file runs

| Service | What it is |
|---|---|
| `postgres` | PostgreSQL 16 on a named volume. No host port: only the other services reach it. |
| `migrate` | One shot. It applies the SQL migrations, and it loads the demo seed on the first start only. |
| `core` | The HTTP API. |
| `worker` | The background jobs: the events outbox drain and the retention purges. |
| `web` | nginx. It serves the built web apps and proxies `/api` to `core`. |

`core`, `worker` and `web` start only after `migrate` has finished and
`postgres` is healthy, so the database is migrated before anything talks to
it. Every long running service has a health check.

No service publishes a host port. The `web` service exposes 8080 and the
platform's proxy is told, in the platform's own domain setting, to send the
domain to that service. The proxy puts TLS in front of it.

## The demo variant, step by step

### 1. Create the application

Create the application on the platform from this repository
(`FutureBuildAIinc/gable`), the branch that holds the demo, and the commit
to deploy. The platform builds `core/Dockerfile` and `web/Dockerfile` from
that commit, so a deploy is a commit: nothing is built here and uploaded.

### 2. Point it at the compose file

The compose file path is `deploy/compose.yml`.

Docker Compose resolves relative paths (the build context and the mounted API
snippet) against the project directory, which is `deploy/` unless the platform
passes `--project-directory` at the repository root. The default
`GABLE_REPO_ROOT` (`..`) is what `docker compose -f deploy/compose.yml` needs;
set `GABLE_REPO_ROOT` to `.` only when the platform passes
`--project-directory` at the repository root.

### 3. Set the environment values

Ten values have no default. The recipe refuses to start without them, and
says which one is missing.

| Value | What it is | How to make it | A secret |
|---|---|---|---|
| `POSTGRES_PASSWORD` | The database password. It goes in the connection string, so keep it to letters and digits. | `openssl rand -hex 16` | yes |
| `PAYMENT_VAULT_KEY` | Seals the payment and AI keys an administrator enters in Tech Admin. The same value every run, or the stored keys cannot be opened again. | `openssl rand -hex 32` (64 hex characters) | yes |
| `PORTAL_JWT_SECRET` | Signs the portal's session tokens. | `openssl rand -hex 32` | yes |
| `GABLE_PUBLIC_URL` | The deployment's public address. The core writes it into the links it hands out. | `https://demo.gablelbm.com` | no |
| `CORS_ORIGINS` | The origins allowed to call the API. The same address on a single domain deployment. | `https://demo.gablelbm.com` | no |
| `APPWRITE_ENDPOINT` | The Appwrite API address the core signs staff in through (ADR 0012). | `https://<the Appwrite host>/v1` | no |
| `APPWRITE_PROJECT_ID` | The Appwrite project id. | from the Appwrite console | no |
| `APPWRITE_TEAM_ID` | The Appwrite staff team id. | from the Appwrite console | no |
| `APPWRITE_API_KEY` | The Appwrite server key. It goes in the platform's environment from the vault, never into a file. | made in the Appwrite console | yes |
| `TRUSTED_PROXIES` | The platform's proxy network (and the compose network where `web` fronts `core`), as CIDRs or addresses separated by commas, so each visitor gets their own rate limit budget instead of sharing one budget on the proxy's address. | For example `10.0.0.0/8,172.16.0.0/12` (confirm from the `core` boot log on the first deploy) | no |

The demo adds two more, and both are data, not authentication:

| Value | What it is |
|---|---|
| `DEMO_SEED` | `1`. The `migrate` service loads the demo data. |
| `DEMO_SEED_IMPORT` | `1`. The same, with the larger invented catalogue and its history. |

The demo signs in the way a customer does, through Appwrite. Seed the demo
people once into the Appwrite project the demo points at, with
`scripts/appwrite/seed-demo-people.sh --config <file>` and the password in
`DEMO_PEOPLE_PASSWORD` from the vault.

`AUTH_MODE=dev` and the persona sign in (`VITE_AUTH_DEV_MODE`) are not part of
this recipe. They belong to the local root compose stack alone, where the core
is built with the `devauth` tag; the images this recipe builds and runs carry
neither, and a core given `AUTH_MODE=dev` refuses to start.

The customer variant leaves `DEMO_SEED` and `DEMO_SEED_IMPORT` unset; see
[Sign in](#sign-in) below.

### 4. The domain

Give the deployment its domain in the platform's own domain setting and
point it at the `web` service on port 8080. The platform's proxy puts TLS in
front of it and terminates the certificate.

### 5. The access gate

Turn the platform's access gate on in front of the domain. It is an HTTP
password prompt with one shared password, and the demo's owner gives that
password to the people the demo is shown to. It keeps the public
out of a deployment that holds sample data and nothing else.

### 6. The first deploy

The platform builds the images, runs `migrate` once, and then starts
`core`, `worker` and `web`. The `migrate` service's log says that the
migrations were applied and that the demo data was seeded. Check the `core`
service's boot log to confirm `TRUSTED_PROXIES` covers the proxy peer: the
core logs the setting's state at startup and warns if `X-Forwarded-For`
arrives from an address outside `TRUSTED_PROXIES`. The domain answers with
the front door, and the desk signs in with a persona.

### 7. A redeploy that keeps the data

Redeploying the same commit does not touch the demo. The seed writes a
marker when it finishes and a later run finds the marker and skips, so the
data on the volume stays as it was. The migrations still run on every
deploy, which is what a new commit needs.

The seed truncates the transactional tables every time it runs. That is why
the marker exists, and why the demo tenant keeps its data across redeploys
instead of being repaved every time.

### 8. Resetting the demo on purpose

Two ways, and both are deliberate acts:

- set `DEMO_SEED_FORCE` to `1` and redeploy. The seed runs again and the
  demo is back at a known state;
- remove the named volume and redeploy. Everything is gone, the demo and
  every setting with it.

## The customer variant

The same file, with the demo turned off. A customer's deployment starts
empty and holds only what the dealer enters.

### The seed stays off

Leave `DEMO_SEED` and `DEMO_SEED_IMPORT` unset. The `migrate` service still
applies the migrations, and the seed says that it was not asked for and does
nothing. A customer's database never holds sample data.

### Sign in

Leave `AUTH_MODE` unset. With `AUTH_MODE` unset the core is fail-closed: it
signs staff in through Appwrite (ADR 0012) and refuses to start without its
sign in settings.

| Value | Required in customer variant | What it is |
|---|---|---|
| `APPWRITE_ENDPOINT`, `APPWRITE_PROJECT_ID`, `APPWRITE_TEAM_ID`, `APPWRITE_API_KEY` | yes | The Appwrite project and its staff team (see the table above). The recipe refuses to start without them. |
| `STAFF_SIGNIN_FLOOR_MS` | no | The least time every password sign in answer takes, in milliseconds (default 1000). It must sit above the Appwrite password hash cost or the two password refusals can be split by timing; a value under 250 or over 10000, or not a whole number, refuses the boot. |
| `JWKS_URL` | no | Optional: an identity provider's JWKS endpoint URL, used by the core to verify bearer tokens. |
| `AUTH_ISSUER` | no | When set, the core also checks that each token's `iss` claim matches this issuer string. |

The web image is the customer image, built without the persona sign in. The
persona sign in is a display name and no credential, and it must not be in a
customer's deployment.

The other demo settings go with it: `DEMO_SEED`, `DEMO_SEED_IMPORT` and
`DEMO_SEED_FORCE` stay unset, and `GABLE_PUBLIC_URL` and `CORS_ORIGINS`
become the customer's own address.

### Backups of the named volume

The data lives on one named volume, `gable_postgres_data` unless
`GABLE_PG_VOLUME` says otherwise. Back it up with the platform's own backup
feature if it has one, or from a shell on the host:

```bash
docker run --rm -v gable_postgres_data:/data -v "$PWD":/backup \
  alpine tar czf /backup/gable-postgres.tgz -C /data .
```

Restoring is the same command the other way round, into an empty volume,
before the deployment starts again. A backup that has never been restored
has never been taken: restore one into a scratch database and open it.

### Upgrading by commit

A deploy is a commit, so an upgrade is a commit. Move the branch the
platform follows, or point it at a tag, and deploy again. The platform
builds that commit's images, the `migrate` service applies the new
migrations before `core` and `worker` start, and the volume keeps the
data. A migration that cannot be applied leaves the old deployment running:
the `migrate` service has to finish before the others start.

### What is not in the package yet

- The link from an Appwrite user to its Gable staff row and yard grants
  (ADR 0012 slice 2).
- An automated backup. Back the volume up yourself, on a schedule, and
  restore one to be sure.
- Mail. Without the SMTP values the sender logs instead of delivering, and
  the screen says so.
- AI. Without a key, every AI feature answers that AI is off.
- Online payment. Without the payment processor's keys the portal's online
  payment is off and the portal says so.
- The access gate. It is a platform feature, not a Gable feature, and a
  customer deployment does not need one in front of it once the sign in is
  real.
- A lawyer's review of the large cash aggregation across branches and of
  the consumer durable mapping. That review is done in the first US customer
  implementation, and the worklist and the export say on screen that the
  dealer confirms each filing with its own adviser.

## Running from prebuilt images

The recipe above builds the images on the host. When the host must not build
(a shared server whose capacity belongs to other work, a throttled
workstation), run the same recipe from images that were built elsewhere: the
repository's `publish-images` workflow builds them on GitHub and pushes them
to GHCR under the organization, and `deploy/compose.images.yml` runs them. It
is the same recipe: the same services, the same environment values (every one
of the ten above, with the same refusals), the same health checks, the same
seed once guard. Only the source of the images changes.

### 1. Build the images

Dispatch the workflow by hand with the commit to build:

```bash
gh workflow run publish-images.yml -f ref=<commit>
```

The workflow is dispatch only: it never runs on a push, a schedule or a pull
request. It checks out that commit, resolves its full sha, and pushes three
images, all tagged with that sha:

- `ghcr.io/futurebuildaiinc/gable-core:<sha>`: the one core binary, used by
  the `migrate`, `core` and `worker` services;
- `ghcr.io/futurebuildaiinc/gable-web:<sha>`: the web image built WITHOUT the
  persona sign in. It is the web image for the demo and for a customer alike.

The workflow also pushes a `demo-<sha>` web image built with the persona sign
in. The recipe does not run it: the demo signs in through Appwrite.

The run's summary lists the image refs with their digests. No secret is
printed or stored.

### 2. Point the platform at the images file

Set the compose file path to `deploy/compose.images.yml` and add one value to
the environment:

| Value | What it is |
|---|---|
| `GABLE_IMAGE_TAG` | Required. The commit sha the images were built for (the `<sha>` above). The file refuses to start without it. |

Everything else is the recipe as written above: the demo variant sets the same
two demo values, the customer variant leaves them unset. A deployment upgrade
is: dispatch the workflow for the new commit, change `GABLE_IMAGE_TAG`, deploy
again. The `migrate` service applies the new migrations before the others
start, and the volume keeps the data.

The `web` service still mounts the repository's `web/nginx-local-api.conf`,
so the platform keeps its checkout of this repository at the same commit; only
the building moved to the workflow.

### 3. The pull credential

If the GHCR packages are private, the platform cannot pull the images until
the repository owner (who owns the packages) sets up a read only pull
credential for the platform's host and adds it where the platform keeps its
registry credentials. That credential belongs to the platform's own settings,
set by the repository owner: **never put a credential in this repository.**
With the packages public, no credential is needed.

## The check

`deploy/check.sh` is the recipe's static check, for `deploy/compose.yml` and
`deploy/compose.images.yml` both. It starts nothing, builds
nothing and reaches nothing:

```bash
bash deploy/check.sh
```

It proves that each compose file parses, that it refuses to start when one of
the ten required values is missing (the images file also refuses without
`GABLE_IMAGE_TAG`), that no service publishes a host port,
that every long running service has a health check, that `deploy/compose.yml`
builds from this repository's own Dockerfiles while `deploy/compose.images.yml`
runs prebuilt image refs pinned to the required tag, and that the customer
variant passes `JWKS_URL` and `AUTH_ISSUER` to `core`.
