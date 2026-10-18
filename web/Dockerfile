# SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
#
# One web image for the whole workspace: the front door, the desk and the
# work shell, built from web/ with its root lockfile and served by
# web/nginx.conf.
#
#   docker build -f web/Dockerfile .      (context: the repository root)

# Build stage
FROM node:20-alpine AS builder

WORKDIR /app/web

ARG VITE_API_URL
ENV VITE_API_URL=$VITE_API_URL
ARG VITE_DEMO_MODE
ENV VITE_DEMO_MODE=$VITE_DEMO_MODE
# Read by packages/auth: set to "true" only for the local stack (AUTH_MODE=dev).
ARG VITE_AUTH_DEV_MODE
ENV VITE_AUTH_DEV_MODE=$VITE_AUTH_DEV_MODE

# Manifests first so the install layer caches until a dependency changes.
COPY web/package.json web/package-lock.json ./
COPY web/apps/desk/package.json apps/desk/
COPY web/apps/front-door/package.json apps/front-door/
COPY web/apps/work-shell/package.json apps/work-shell/
COPY web/apps/field-shell/package.json apps/field-shell/
COPY web/packages/api-client/package.json packages/api-client/
COPY web/packages/auth/package.json packages/auth/
COPY web/packages/design-system/package.json packages/design-system/
COPY web/packages/shell-chrome/package.json packages/shell-chrome/
RUN npm ci

COPY web/ .

# Every bundle: desk, front-door, work-shell and field-shell.
RUN npm run build

# Run stage
FROM nginx:1.27-alpine

RUN apk --no-cache add curl

# Remove default nginx config
RUN rm /etc/nginx/conf.d/default.conf

COPY web/nginx.conf /etc/nginx/conf.d/default.conf
COPY web/nginx-headers.conf /etc/nginx/gable-headers.conf
COPY web/nginx-headers-frameable.conf /etc/nginx/gable-headers-frameable.conf

COPY --from=builder /app/web/apps/front-door/dist /usr/share/nginx/html/front-door
COPY --from=builder /app/web/apps/desk/dist /usr/share/nginx/html/desk
COPY --from=builder /app/web/apps/work-shell/dist /usr/share/nginx/html/work-shell
COPY --from=builder /app/web/apps/field-shell/dist /usr/share/nginx/html/field-shell

# nginx's official image runs the master as root and the workers as `nginx`;
# that is kept because a custom non-root user broke the pid and cache paths on
# DigitalOcean App Platform.

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/ || exit 1

CMD ["nginx", "-g", "daemon off;"]
