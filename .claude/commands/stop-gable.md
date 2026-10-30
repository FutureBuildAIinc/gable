---
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
description: Stop the local Gable stack and remove its containers and volume (the run-gable-locally skill).
---

Use the **run-gable-locally** skill, its stop section.

Run `make down` (or `make down` with the same `STACK_PROJECT` or `COMPOSE_PROJECT_NAME` the stack was started with), then show the user that nothing is left:

```bash
docker ps -a --filter label=com.docker.compose.project=gable-stack
docker volume ls --filter label=com.docker.compose.project=gable-stack
```

Both must be empty. Only the stack's own project is removed; a Postgres started with `make db` is another project and is never touched. If a container outlives `make down`, name it to the user with `make ps`; never remove a container the stack did not start.
