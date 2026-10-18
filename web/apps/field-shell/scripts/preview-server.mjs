// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The screenshots' static server: the field shell's dist under /field/ and the
// desk's dist at every other path, one origin, no API (the spec mocks it with
// page.route). Playwright's webServer starts and stops this inside its own
// foreground run; nothing detaches.
//
//   node scripts/preview-server.mjs   (PORT env, default 18395)

import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { extname, join, normalize } from 'node:path';
import { fileURLToPath } from 'node:url';

const PORT = Number(process.env.PORT ?? 18395);
const APP = fileURLToPath(new URL('..', import.meta.url));
const SHELL_DIST = join(APP, 'dist');
const WORK_SHELL_DIST = join(APP, '..', 'work-shell', 'dist');
const DOOR_DIST = join(APP, '..', 'front-door', 'dist');
const DESK_DIST = join(APP, '..', 'desk', 'dist');

const TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.json': 'application/json',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2',
  '.ico': 'image/x-icon',
  '.map': 'application/json',
  '.txt': 'text/plain; charset=utf-8',
};

async function send(res, file) {
  try {
    const body = await readFile(file);
    res.writeHead(200, { 'content-type': TYPES[extname(file)] ?? 'application/octet-stream' });
    res.end(body);
  } catch {
    res.writeHead(404, { 'content-type': 'text/plain; charset=utf-8' });
    res.end('not found');
  }
}

/** Resolve a request path under a dist root, refusing to climb out of it. */
function under(root, requestPath) {
  const clean = normalize(requestPath).replace(/^([/\\])+/, '');
  const file = join(root, clean);
  return file.startsWith(root) ? file : null;
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url ?? '/', 'http://localhost');
  const path = url.pathname;

  if (path === '/') {
    await send(res, join(DOOR_DIST, 'index.html'));
    return;
  }
  if (path.startsWith('/door/')) {
    const file = under(DOOR_DIST, path);
    if (file === null) {
      res.writeHead(403).end();
      return;
    }
    await send(res, file);
    return;
  }
  if (path === '/shell') {
    res.writeHead(308, { location: '/shell/' }).end();
    return;
  }
  if (path === '/shell/') {
    await send(res, join(WORK_SHELL_DIST, 'index.html'));
    return;
  }
  if (path.startsWith('/shell/')) {
    const file = under(WORK_SHELL_DIST, path.slice('/shell/'.length));
    if (file === null) {
      res.writeHead(403).end();
      return;
    }
    await send(res, file);
    return;
  }
  if (path === '/field') {
    res.writeHead(308, { location: '/field/' }).end();
    return;
  }
  if (path === '/field/') {
    await send(res, join(SHELL_DIST, 'index.html'));
    return;
  }
  if (path.startsWith('/field/')) {
    const file = under(SHELL_DIST, path.slice('/field/'.length));
    if (file === null) {
      res.writeHead(403).end();
      return;
    }
    await send(res, file);
    return;
  }
  // The desk: a real file when there is one, otherwise its index.html (the
  // framed flag rides the query string, which the file lookup never sees).
  const file = under(DESK_DIST, path);
  if (file === null) {
    res.writeHead(403).end();
    return;
  }
  try {
    await readFile(file);
    await send(res, file);
  } catch {
    await send(res, join(DESK_DIST, 'index.html'));
  }
});

server.listen(PORT, '127.0.0.1', () => {
  console.log(`screens preview on http://127.0.0.1:${PORT} (door: ${DOOR_DIST}, field-shell: ${SHELL_DIST}, desk: ${DESK_DIST})`);
});
