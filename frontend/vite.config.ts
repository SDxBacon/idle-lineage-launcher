import { createReadStream, promises as fs } from 'node:fs';
import { homedir } from 'node:os';
import * as path from 'node:path';
import type { IncomingMessage, ServerResponse } from 'node:http';
import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';
import wails from '@wailsio/runtime/plugins/vite';

const gameDataDirectory = 'IdleLineageLauncher';

export default defineConfig({
  server: {
    host: '127.0.0.1',
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  plugins: [gameAssetsDevPlugin(), react(), wails('./bindings')],
});

/**
 * In production, /game/* is served by the Go asset middleware. Wails dev mode
 * points the webview directly at Vite, so the equivalent read-only route is
 * needed here for `task dev`. It serves only the installed Git working tree
 * and never copies game content into the frontend.
 */
function gameAssetsDevPlugin(): Plugin {
  return {
    name: 'idle-lineage-game-assets',
    apply: 'serve',
    configureServer(server) {
      server.middlewares.use(async (request, response, next) => {
        let pathname: string;
        try {
          pathname = decodeURIComponent(new URL(request.url ?? '/', 'http://localhost').pathname);
        } catch {
          sendText(response, 400, 'invalid game path');
          return;
        }
        if (pathname !== '/game' && !pathname.startsWith('/game/')) {
          next();
          return;
        }
        await serveDevGameAsset(request, response, pathname);
      });
    },
  };
}

async function serveDevGameAsset(request: IncomingMessage, response: ServerResponse, pathname: string) {
  setNoStoreHeaders(response);
  if (request.method !== 'GET' && request.method !== 'HEAD') {
    response.setHeader('Allow', 'GET, HEAD');
    sendText(response, 405, 'method not allowed');
    return;
  }

  try {
    const dataRoot = launcherDataRoot();
    const root = path.join(dataRoot, 'game', 'src');
    await fs.readFile(path.join(root, '.git', 'HEAD'), 'utf8');
    let relative = pathname.slice('/game'.length).replace(/^\/+/, '');
    if (relative === '') relative = 'index.html';
    const parts = relative.split('/');
    if (parts.some(part => part === '' || part === '.' || part === '..' || part.toLowerCase() === '.git' || part.includes('\\') || part.includes('\0'))) {
      sendText(response, 400, 'invalid game path');
      return;
    }

    const realRoot = await fs.realpath(root);
    let target = path.resolve(root, ...parts);
    if (!isInside(realRoot, target)) {
      sendText(response, 400, 'invalid game path');
      return;
    }
    target = await fs.realpath(target);
    if (!isInside(realRoot, target)) {
      sendText(response, 404, 'game asset not found');
      return;
    }

    let info = await fs.stat(target);
    if (info.isDirectory()) {
      target = await fs.realpath(path.join(target, 'index.html'));
      if (!isInside(realRoot, target)) {
        sendText(response, 404, 'game asset not found');
        return;
      }
      info = await fs.stat(target);
    }
    if (!info.isFile()) {
      sendText(response, 404, 'game asset not found');
      return;
    }
    serveFile(request, response, target, info.size);
  } catch (error) {
    const code = typeof error === 'object' && error !== null && 'code' in error ? String(error.code) : '';
    sendText(response, code === 'ENOENT' ? 404 : 503, code === 'ENOENT' ? 'game asset not found' : 'game is not installed');
  }
}

function serveFile(request: IncomingMessage, response: ServerResponse, filename: string, size: number) {
  response.setHeader('Accept-Ranges', 'bytes');
  response.setHeader('Content-Type', contentType(filename));

  if (size === 0) {
    response.statusCode = 200;
    response.setHeader('Content-Length', '0');
    response.end();
    return;
  }

  const range = request.headers.range?.match(/^bytes=(\d*)-(\d*)$/);
  let start = 0;
  let end = size - 1;
  if (range) {
    if (range[1] === '' && range[2] !== '') {
      const suffixLength = Number(range[2]);
      start = Math.max(0, size - suffixLength);
    } else {
      start = range[1] === '' ? 0 : Number(range[1]);
      end = range[2] === '' ? end : Number(range[2]);
    }
    if (!Number.isSafeInteger(start) || !Number.isSafeInteger(end) || start < 0 || end < start || start >= size) {
      response.statusCode = 416;
      response.setHeader('Content-Range', `bytes */${size}`);
      response.end();
      return;
    }
    end = Math.min(end, size - 1);
    response.statusCode = 206;
    response.setHeader('Content-Range', `bytes ${start}-${end}/${size}`);
  } else {
    response.statusCode = 200;
  }
  response.setHeader('Content-Length', String(end - start + 1));
  if (request.method === 'HEAD') {
    response.end();
    return;
  }
  const stream = createReadStream(filename, { start, end });
  stream.on('error', () => {
    if (!response.headersSent) sendText(response, 500, 'unable to read game asset');
    else response.destroy();
  });
  stream.pipe(response);
}

function launcherDataRoot() {
  if (process.platform === 'darwin') {
    return path.join(homedir(), 'Library', 'Application Support', gameDataDirectory);
  }
  if (process.platform === 'win32') {
    return path.join(process.env.LOCALAPPDATA ?? path.join(homedir(), 'AppData', 'Local'), gameDataDirectory);
  }
  return path.join(process.env.XDG_CONFIG_HOME ?? path.join(homedir(), '.config'), gameDataDirectory);
}

function isInside(root: string, target: string) {
  const relative = path.relative(root, target);
  return relative !== '..' && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative);
}

function setNoStoreHeaders(response: ServerResponse) {
  response.setHeader('Cache-Control', 'no-store, max-age=0');
  response.setHeader('Pragma', 'no-cache');
  response.setHeader('Expires', '0');
  response.setHeader('X-Content-Type-Options', 'nosniff');
}

function sendText(response: ServerResponse, status: number, message: string) {
  response.statusCode = status;
  response.setHeader('Content-Type', 'text/plain; charset=utf-8');
  response.end(`${message}\n`);
}

function contentType(filename: string) {
  const types: Record<string, string> = {
    '.html': 'text/html; charset=utf-8',
    '.css': 'text/css; charset=utf-8',
    '.js': 'text/javascript; charset=utf-8',
    '.mjs': 'text/javascript; charset=utf-8',
    '.json': 'application/json; charset=utf-8',
    '.png': 'image/png',
    '.jpg': 'image/jpeg',
    '.jpeg': 'image/jpeg',
    '.gif': 'image/gif',
    '.webp': 'image/webp',
    '.svg': 'image/svg+xml',
    '.ico': 'image/x-icon',
    '.mp3': 'audio/mpeg',
    '.wav': 'audio/wav',
    '.ogg': 'audio/ogg',
    '.mp4': 'video/mp4',
    '.webm': 'video/webm',
    '.ttf': 'font/ttf',
    '.otf': 'font/otf',
    '.woff': 'font/woff',
    '.woff2': 'font/woff2',
    '.wasm': 'application/wasm',
  };
  return types[path.extname(filename).toLowerCase()] ?? 'application/octet-stream';
}
