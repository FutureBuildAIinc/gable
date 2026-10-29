// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Service worker for the work shell's `/pos` route (ADR 0011 section 5.2).
// Precaches and serves the `/pos` HTML shell and static desk bundle assets on
// the Gable's origin so a reload of `/pos` while offline succeeds. Never
// caches `/api/*` responses or non-POS routes.

const CACHE_NAME = 'gable-pos-shell-v1';
const PRECACHE_URLS = ['/pos', '/theme-boot.js', '/favicon.svg'];

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
            .filter((key) => key !== CACHE_NAME)
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
          caches.open(CACHE_NAME).then((cache) => cache.match('/pos'))
        )
    );
    return;
  }

  event.respondWith(
    caches.open(CACHE_NAME).then((cache) =>
      cache.match(request).then((cached) => {
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
