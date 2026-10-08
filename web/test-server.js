// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Minimal static file server used during Playwright end-to-end testing.
 *
 * Serves two Vite dist bundles on one origin so sessionStorage auth handoff
 * works exactly as it does behind nginx in production:
 *
 *   http://localhost:5000/        → front-door bundle (apps/front-door/dist)
 *   http://localhost:5000/app/*   → desk bundle       (apps/desk/dist)
 *
 * API calls are forwarded to the Go backend so the browser can sign in
 * against the real auth endpoints during tests.
 */
import http from 'http';
import fs from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

const PORT = Number(process.env.TEST_PORT ?? 5000);
const API_TARGET = process.env.TEST_API_TARGET ?? 'http://localhost:8083';
const FD_DIST = path.join(__dirname, 'apps/front-door/dist');
const DESK_DIST = path.join(__dirname, 'apps/desk/dist');

function mimeType(file) {
  const ext = path.extname(file).toLowerCase();
  const map = {
    '.html': 'text/html;charset=utf-8',
    '.js': 'application/javascript',
    '.mjs': 'application/javascript',
    '.css': 'text/css',
    '.json': 'application/json',
    '.png': 'image/png',
    '.jpg': 'image/jpeg',
    '.svg': 'image/svg+xml',
    '.ico': 'image/x-icon',
    '.woff2': 'font/woff2',
    '.woff': 'font/woff',
    '.ttf': 'font/ttf',
  };
  return map[ext] ?? 'application/octet-stream';
}

function proxyRequest(req, res, target, targetPathname) {
  const url = new URL(req.url, `http://localhost:${PORT}`);
  const outPath = targetPathname ?? url.pathname;
  return new Promise((resolve) => {
    const proxyReq = http.request(
      `${target}${outPath}${url.search}`,
      { method: req.method, headers: { ...req.headers, host: new URL(target).host } },
      (proxyRes) => {
        res.writeHead(proxyRes.statusCode, proxyRes.headers);
        proxyRes.pipe(res, { end: true });
        res.on('finish', resolve);
      }
    );
    proxyReq.on('error', (e) => {
      console.error(`proxy error: ${e.message}`);
      res.writeHead(502);
      res.end();
      resolve();
    });
    req.pipe(proxyReq, { end: true });
  });
}

function serveFile(filePath, res) {
  fs.readFile(filePath, (err, data) => {
    if (err) { res.writeHead(404); res.end('Not found'); return; }
    res.writeHead(200, { 'Content-Type': mimeType(filePath) });
    res.end(data);
  });
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, `http://localhost:${PORT}`);

  // Proxy API to the Go backend
  if (url.pathname.startsWith('/api/')) {
    return proxyRequest(req, res, API_TARGET);
  }

  // Backend health check (Go uses /health, nginx uses /healthz — both)
  if (url.pathname === '/healthz' || url.pathname === '/health') {
    return proxyRequest(req, res, API_TARGET, '/health');
  }

  // Front door at /
  if (url.pathname === '/' || url.pathname === '/index.html') {
    return serveFile(path.join(FD_DIST, 'index.html'), res);
  }

  // Front-door hashed assets (/door/*.js, /door/*.css)
  if (url.pathname.startsWith('/door/')) {
    return serveFile(path.join(FD_DIST, url.pathname), res);
  }

  // Desk SPA: /app/* — try exact file first, fall back to index.html (SPA)
  if (url.pathname.startsWith('/app/')) {
    const deskPath = url.pathname.replace(/^\/app\//, '/');
    const filePath = path.join(DESK_DIST, deskPath);
    if (fs.existsSync(filePath) && fs.statSync(filePath).isFile()) {
      return serveFile(filePath, res);
    }
    return serveFile(path.join(DESK_DIST, 'index.html'), res);
  }

  // Desk hashed assets under /assets/ (no /app/ prefix — assets are served
  // from the desk dist root, e.g. /assets/vendor-lit-xxx.js)
  if (url.pathname.startsWith('/assets/')) {
    return serveFile(path.join(DESK_DIST, url.pathname), res);
  }

  res.writeHead(404);
  res.end();
});

server.listen(PORT, () => {
  console.log(`test server listening on http://localhost:${PORT}`);
  console.log(`  front-door: http://localhost:${PORT}/`);
  console.log(`  desk:        http://localhost:${PORT}/app/`);
  console.log(`  API proxy:   ${API_TARGET}`);
});
