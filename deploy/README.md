# The deploy recipe

One Compose file that runs Gable on a Coolify based platform such as FB
Deploy. The platform checks out this repository at a commit, builds the
images from the Dockerfiles in that commit, and runs the services the file
describes behind the platform's own reverse proxy.

There are two variants and they are the same file with a few environment
values changed:

- the **demo variant** loads the Gable Building Supply sample data on the
  first start and keeps it through every later redeploy;
- the **customer variant** never seeds anything and signs people in for real.

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

The paths inside that file (the build context and the mounted API snippet)
are relative to the compose file's own directory, so the repository root is
one level up from it. If the platform resolves paths against the repository
root instead, set `GABLE_REPO_ROOT` to `.` and the paths land in the same
places.

### 3. Set the environment values

Five values have no default. The recipe refuses to start without them, and
says which one is missing.

| Value | What it is | How to make it | A secret |
|---|---|---|---|
| `POSTGRES_PASSWORD` | The database password. It goes in the connection string, so keep it to letters and digits. | `openssl rand -hex 16` | yes |
| `PAYMENT_VAULT_KEY` | Seals the payment and AI keys an administrator enters in Tech Admin. The same value every run, or the stored keys cannot be opened again. | `openssl rand -hex 32` (64 hex characters) | yes |
| `PORTAL_JWT_SECRET` | Signs the portal's session tokens. | `openssl rand -hex 32` | yes |
| `GABLE_PUBLIC_URL` | The deployment's public address. The core writes it into the links it hands out. | `https://demo.gablelbm.com` | no |
| `CORS_ORIGINS` | The origins allowed to call the API. The same address on a single domain deployment. | `https://demo.gablelbm.com` | no |

The demo adds four more:

| Value | What it is |
|---|---|
| `AUTH_MODE` | `dev`. The core stops checking who the caller is. |
| `DEMO_SEED` | `1`. The `migrate` service loads the demo data. |
| `DEMO_SEED_IMPORT` | `1`. The same, with the larger invented catalogue and its history. |
| `VITE_AUTH_DEV_MODE` | `true`, and it is a build argument: the web image is built with the persona sign in. |

`AUTH_MODE=dev` gives every caller full reach. No token is verified and
every role check passes, so anyone who reaches the port has the whole ERP,
the general ledger, the accounts and the payments. The demo is safe only
because the access gate in front of it keeps the public out and because it
holds sample data and nothing else. **Never run a customer this way.**

`VITE_AUTH_DEV_MODE` is a build argument, so changing it needs a rebuild.
A platform that passes the application's environment values to the build
picks it up as it is; one that does not needs the value added to the build
arguments.

Optional values, all with a default:

| Value | Default | What it is |
|---|---|---|
| `GABLE_REPO_ROOT` | `..` | Where the repository root is, relative to the compose file. |
| `GABLE_PG_VOLUME` | `gable_postgres_data` | The name of the data volume. Give a second deployment on the same host its own name. |
| `TRUSTED_PROXIES` | empty | The platform's proxy network, as CIDRs or addresses, separated by commas. Set it so every visitor gets their own rate limit budget instead of sharing one with every other visitor. |
| `RATE_LIMIT_PER_MINUTE` | `120` | The API's request limit per caller address. |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_FROM`, `SMTP_TLS` | empty | Mail. Left empty, the report runs and its result is logged instead of being delivered, and the report's screen says that the delivery was logged and not sent. |
| `DEMO_SEED_ONCE` | `1` | The seed's memory. With it on, the seed runs on the first start only. |
| `DEMO_SEED_FORCE` | empty | `1` asks for a reseed on purpose. |
| `POSTGRES_USER`, `POSTGRES_DB` | `gable_user`, `gable_db` | The database's user and name. |

No payment processor key and no AI key are in the recipe. Without a payment
key the portal's online payment is off. Without an AI key every AI feature
answers that AI is off, and nothing pretends to have run.

### 4. The domain

Give the deployment its domain in the platform's own domain setting and
point it at the `web` service on port 8080. The platform's proxy puts TLS in
front of it and terminates the certificate.

### 5. The access gate

Turn the platform's access gate on in front of the domain. It is an HTTP
password prompt with one shared password, and the demo's owner gives that
password to the people the demo is shown to. It is the only thing between
the internet and a deployment running with `AUTH_MODE=dev`.

### 6. The first deploy

The platform builds the images, runs `migrate` once, and then starts
`core`, `worker` and `web`. The `migrate` service's log says that the
migrations were applied and that the demo data was seeded. The domain
answers with the front door, and the desk signs in with a persona.

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

Leave `AUTH_MODE` unset. The core is then fail-closed: it needs `JWKS_URL`
and refuses to start without it. Today that is what the staff sign in uses;
once the staff sign in lands it is the same setting with the sign in behind
it.

Build the web image without `VITE_AUTH_DEV_MODE`, so the persona sign in is
not offered. The demo's sign in is a display name and no credential, and it
must not be in a customer's deployment.

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

- The staff sign in. Until it lands, a customer's deployment needs
  `JWKS_URL` from its own identity provider.
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

## The check

`deploy/check.sh` is the recipe's static check. It starts nothing, builds
nothing and reaches nothing:

```bash
bash deploy/check.sh
```

It proves that the compose file parses, that it refuses to start when one of
the five required values is missing, that no service publishes a host port,
that every long running service has a health check, and that the images are
built from this repository's own Dockerfiles.
