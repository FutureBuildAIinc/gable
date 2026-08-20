# Digital Ocean App Platform — Example Deploy Specs

This directory holds **example** App Platform specs for self-hosting Gable on
Digital Ocean. They are references you copy and adapt — every hostname,
repository, and database name below is a placeholder. Nothing here points at a
live environment.

| Spec | Tracks branch | Example domain | Logical DB |
|---|---|---|---|
| `app-demo.yaml` | `main` | `demo.example.com` | `gable_demo_db` |
| `app-staging.yaml` | `staging` | `staging.example.com` | `gable_staging_db` |

Both examples reference a single DO Managed Postgres cluster
(`your-db-cluster`, PG 16) with isolated logical databases, so multiple
environments can share one cluster without stepping on each other.

> **Security — auth is not optional on reachable hosts.** The example specs run
> with `AUTH_MODE=production` and a placeholder `JWKS_URL`. The backend is
> **fail-closed**: with `AUTH_MODE` ≠ `dev` it requires a valid `JWKS_URL` and
> `CORS_ORIGINS` or it refuses to start. **Never** set `AUTH_MODE=dev` on any
> reachable deployment — it disables authentication entirely and is for local
> development only. See [`../SECURITY.md`](../SECURITY.md).

## Architecture

```
git push -> DO App Platform pulls branch
            |
            |- builds backend/Dockerfile  -> main + migrate + seed binaries
            |                                 (alpine, port 8080)
            |
            |- builds app/Dockerfile      -> nginx + Vite SPA bundle
            |                                 (VITE_API_URL baked at build time)
            |
            |- deploys backend + frontend services
            |
            `- runs POST_DEPLOY job: ./migrate && ./seed
                                      (against the env's logical DB)
```

The same Docker image used for the backend service is reused for the post-deploy
migrate-and-seed job — that's why `backend/Dockerfile` builds three binaries
(`main`, `migrate`, `seed`) into the runtime image. The job entrypoint is
overridden via `run_command`.

Frontend routing: App Platform splits traffic by path. The backend service owns
`/api`, `/health`, `/healthz`, `/metrics`. The frontend owns `/`. The SPA calls
`/api/v1/*` on the same hostname, which App Platform routes back to the backend.

## First-time setup

1. Create the Managed Postgres cluster once (shared between environments):
   ```bash
   doctl databases create your-db-cluster \
       --engine pg --version 16 --region nyc1 \
       --size db-s-1vcpu-1gb --num-nodes 1
   ```
   Then create the logical databases. **Note (PG 16 ownership gotcha):**
   databases created via API/doctl are owned by `doadmin`, and on PG 16 a
   non-admin role then has no `CREATE` on schema `public`, so migrations fail.
   Either create the database with `OWNER gable_user` via psql, or run a
   one-off `ALTER DATABASE <db> OWNER TO gable_user;` as `doadmin` before the
   first deploy.
   ```bash
   doctl databases db create <cluster-id> gable_demo_db
   doctl databases db create <cluster-id> gable_staging_db
   ```

2. Create the apps:
   ```bash
   doctl apps create --spec .do/app-demo.yaml
   doctl apps create --spec .do/app-staging.yaml
   ```

3. After the first deploy, DO assigns each app a hostname like
   `<your-app>.ondigitalocean.app`. In your DNS provider's zone for
   `example.com`:
   - `CNAME demo -> <demo target>` (if your provider proxies, turn the proxy
     **off** — App Platform handles TLS)
   - `CNAME staging -> <staging target>`
   - Add the verification TXT records App Platform requests during the
     domain-attach flow.

4. Once DNS verifies, DO issues Let's Encrypt certificates automatically and
   your custom domains go live.

## Subsequent deploys

Push to the matching branch — DO auto-deploys because every service has
`deploy_on_push: true`:

```bash
git push origin main      # -> the "demo" example app
git push origin staging     # -> the "staging" example app
```

To force a redeploy without a code change (e.g. to re-run the seed job):

```bash
doctl apps create-deployment <app-id> --force-rebuild
```

To update the spec itself (env vars, instance sizes, routes):

```bash
doctl apps update <app-id> --spec .do/app-demo.yaml
```

## Post-deploy job behavior

Every deploy runs `./migrate && ./seed` against the env's database.

- **Migrations** are idempotent — they apply only the SQL files not yet recorded
  in `schema_migrations`.
- **Seed** uses `ON CONFLICT DO NOTHING` / upsert patterns on natural keys
  (account_number, sku, code, email, license, plate), so re-runs do not
  duplicate data, though they will overwrite drift on rows whose natural keys
  match.

If you want a fresh dataset (wipe + reseed), drop the logical DB and let the
next deploy recreate it:

```bash
# Connect to the cluster's `defaultdb` first, then:
psql> DROP DATABASE gable_demo_db;
psql> CREATE DATABASE gable_demo_db OWNER gable_user;
# Then force a redeploy as shown above.
```

Only do this against an environment you're comfortable resetting — dropping a
database interrupts anything running against it.

## Secrets

`DATABASE_URL` is resolved via DO's component binding syntax
(`${gable-db.DATABASE_URL}`). App Platform substitutes the real connection
string (with credentials and `sslmode=require`) at runtime; the literal value is
never committed.

Any other secret a reachable deployment needs — e.g. `PORTAL_JWT_SECRET`, and
whatever your identity provider requires alongside `JWKS_URL` — should be set as
**encrypted env vars** via `doctl apps update` or the dashboard. **Do not** add
secret values inline in these YAML files. `JWKS_URL` itself is typically a public
URL and can stay in the spec, but the placeholder must be replaced with your
provider's real endpoint.

## Local-only files (ignored by git)

The repo's `.gitignore` excludes `.do/*.local.*` and `.do/.env*`. Use these
patterns for scratch specs and local credentials you don't want to publish:

```
.do/app-demo.local.yaml   # personal overrides
.do/.env.demo             # doctl context env
```

## Rollback

App Platform retains the last N deployments per app. To roll back:

```bash
doctl apps list-deployments <app-id>
doctl apps create-deployment <app-id> --restore-deployment <deployment-id>
```

The post-deploy migrate/seed job runs again against the existing DB, which is
safe (idempotent). If a migration caused the breakage, author a corrective
forward migration — DO's rollback does not undo schema changes.
