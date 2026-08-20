<!--
Thanks for contributing to Gable!

Target branch: open this PR against `staging` (NOT `main`). Maintainers
fast-forward `staging → main` after review. See CONTRIBUTING.md.
-->

## Summary

What does this PR do, and why? Link any related issue (e.g. `Closes #123`).

## Component(s) touched

Which parts of the tree does this change? (e.g. `backend/internal/order`,
`backend/pkg/apps/`, `app/`, `docs/`). Note that Gable is licensed
**per component** — see `LICENSE-MAP.md`.

## Type of change

- [ ] Bug fix
- [ ] New feature
- [ ] Refactor / cleanup
- [ ] Documentation
- [ ] Database migration
- [ ] Other (describe):

## Pre-flight checklist

Run the gates locally before pushing — CI runs them too, but failing locally is
faster.

**Backend** (`cd backend`):

- [ ] `go build ./...`
- [ ] `go vet ./...`
- [ ] `go test ./...`

**Frontend** (`cd app`):

- [ ] `npx tsc --noEmit`
- [ ] `npm run lint`
- [ ] `npm run build`

**General:**

- [ ] This PR targets **`staging`**, not `main`.
- [ ] Commits are focused (one logical change each) with clear messages.
- [ ] New DB columns follow the conventions (UUID PKs, `DECIMAL(19,4)` for
      quantities, money-as-cents in app code, every quantity paired with a UOM).
- [ ] New endpoints are under the correct prefix and wired into
      `RegisterRoutes` in `backend/cmd/server/main.go`.
- [ ] I have read `CONTRIBUTING.md` and agree to license my contribution under
      the OpenLBM Standard license governing the file(s) I touched (via the CLA).
- [ ] No secrets, credentials, or live hostnames are committed.
- [ ] `AUTH_MODE=dev` is not introduced into any reachable/production config.

## Screenshots / notes for reviewers

(Optional) UI screenshots, migration notes, or anything reviewers should know.
