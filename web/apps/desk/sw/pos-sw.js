// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Service worker for the work shell's `/pos` route (ADR 0011 section 5.2).
// Precaches and serves the `/pos` HTML shell and static desk bundle assets on
// the Gable's origin so a reload of `/pos` while offline succeeds. Never
// caches `/api/*` responses or non-POS routes.
//
// This is a template: the desk's build (`posServiceWorker` in vite.config.ts)
// fills in the build's hashed asset list and a version taken from it and
// emits the result as `/pos-sw.js`. The install precaches the whole bundle,
// so an offline reload never meets an asset the worker has not seen, and
// each deploy changes the worker, installs it under a new cache and drops
// the old one.

const PRECACHE_VERSION = '__POS_PRECACHE_VERSION__';
const PRECACHE_ASSETS = __POS_PRECACHE_ASSETS__;
const CACHE_PREFIX = 'gable-pos-shell-';
const CACHE_NAME = CACHE_PREFIX + PRECACHE_VERSION;
const PRECACHE_URLS = ['/pos', '/theme-boot.js', '/favicon.svg', ...PRECACHE_ASSETS];
// A server's Vary (Origin, Accept-Encoding) differs between the install's
// fetch and the page's own request for a module script, so a strict match
// misses offline. Every precached asset is content hashed, so its URL alone
// names it.
const MATCH = { ignoreVary: true };

function isEligiblePath(pathname) {
  if (pathname.startsWith('/api/') || pathname === '/api') {
    return false;
  }
  if (pathname === '/pos' || pathname === '/pos/') {
    return true;
  }
  if (pathname.startsWith('/assets/')) {
    return true;
  }
  return pathname === '/theme-boot.js' || pathname === '/favicon.svg';
}

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches
      .open(CACHE_NAME)
      .then((cache) => cache.addAll(PRECACHE_URLS))
      .then(() => self.skipWaiting())
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(
          keys
            .filter((key) => key.startsWith(CACHE_PREFIX) && key !== CACHE_NAME)
            .map((key) => caches.delete(key))
        )
      )
      .then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', (event) => {
  const request = event.request;
  if (request.method !== 'GET') {
    return;
  }
  const url = new URL(request.url);
  if (url.origin !== self.location.origin) {
    return;
  }
  if (!isEligiblePath(url.pathname)) {
    return;
  }

  if (request.mode === 'navigate' || url.pathname === '/pos' || url.pathname === '/pos/') {
    event.respondWith(
      fetch(request)
        .then((response) => {
          if (response && response.ok) {
            const copy = response.clone();
            caches.open(CACHE_NAME).then((cache) => cache.put('/pos', copy));
          }
          return response;
        })
        .catch(() =>
          caches.open(CACHE_NAME).then((cache) => cache.match('/pos', MATCH))
        )
    );
    return;
  }

  event.respondWith(
    caches.open(CACHE_NAME).then((cache) =>
      cache.match(request, MATCH).then((cached) => {
        if (cached) {
          return cached;
        }
        return fetch(request).then((response) => {
          if (response && response.ok) {
            cache.put(request, response.clone());
          }
          return response;
        });
      })
    )
  );
});
