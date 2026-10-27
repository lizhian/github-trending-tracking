// Throwaway UI preview. No dependency installation or external requests required.
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { dirname, resolve, extname } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(fileURLToPath(import.meta.url));
const port = Number(process.env.PROTOTYPE_PORT || 4173);
const types = { '.html': 'text/html; charset=utf-8', '.css': 'text/css', '.js': 'text/javascript' };
createServer(async (request, response) => {
  const url = new URL(request.url, `http://127.0.0.1:${port}`);
  if (url.pathname === '/') {
    response.writeHead(302, { Location: '/prototype/trends/' }).end();
    return;
  }
  const resource = url.pathname.replace(/^\/prototype\/trends\/?/, '') || 'index.html';
  const path = resolve(root, 'trends', resource);
  if (!path.startsWith(resolve(root, 'trends') + '/')) {
    response.writeHead(404).end();
    return;
  }
  try {
    let content = await readFile(path);
    if (extname(path) === '.html' && process.env.NODE_ENV === 'production') {
      content = Buffer.from(content.toString().replace('data-prototype="true"', 'data-prototype="false"'));
    }
    response.writeHead(200, { 'Content-Type': types[extname(path)] || 'application/octet-stream', 'Cache-Control': 'no-store' }).end(content);
  } catch {
    response.writeHead(404).end('Not found');
  }
}).listen(port, '127.0.0.1', () => console.log(`Prototype: http://127.0.0.1:${port}/prototype/trends/`));
