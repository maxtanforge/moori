import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdtemp, readFile} from 'node:fs/promises';
import {createServer} from 'node:http';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {once} from 'node:events';
import {chromiumExit, removeChromiumProfile} from './chromium-cleanup.mjs';

const [browser] = process.argv.slice(2);
assert.ok(browser, 'Pass the path to the Chromium executable');
const profile = await mkdtemp(join(tmpdir(), 'moori-monitor-test-'));
const page = new URL('../', import.meta.url);
const fixture = new URL('./monitoring-browser-fixture.js', import.meta.url);
let finish;
const completion = new Promise(resolve => { finish = resolve; });
async function drag(from, to, y) {
  const [port] = (await readFile(join(profile,'DevToolsActivePort'),'utf8')).split(/\r?\n/);
  const tabs = await (await fetch('http://127.0.0.1:' + port + '/json/list')).json();
  const tab = tabs.find(tab => tab.type === 'page' && tab.url === 'http://127.0.0.1:' + server.address().port + '/');
  assert.ok(tab,'Monitoring tab missing from DevTools');
  const socket = new WebSocket(tab.webSocketDebuggerUrl);
  try {
    await once(socket,'open');
    let id = 0;
    for (const [type,x,button,buttons] of [
      ['mouseMoved',from,'none',0], ['mousePressed',from,'left',1],
      ['mouseMoved',to,'none',1], ['mouseReleased',to,'left',0],
    ]) {
      socket.send(JSON.stringify({id:++id,method:'Input.dispatchMouseEvent',params:{type,x,y,button,buttons,clickCount:1}}));
      const [event] = await once(socket,'message');
      const reply = JSON.parse(event.data);
      assert.equal(reply.id,id);
      assert.ifError(reply.error);
    }
  } finally {
    socket.close();
  }
}
const server = createServer(async (req, res) => {
  if (req.url === '/drag' && req.method === 'POST') {
    try {
      let body = '';
      for await (const chunk of req) body += chunk;
      const coords = JSON.parse(body);
      assert.ok(Array.isArray(coords) && coords.length === 3 && coords.every(Number.isFinite));
      await drag(...coords);
      res.writeHead(204).end();
    } catch (error) {
      res.writeHead(500).end(error.message);
    }
    return;
  }
  if (req.url === '/result' && req.method === 'POST') {
    let result = '';
    for await (const chunk of req) result += chunk;
    res.end();
    finish(result);
    return;
  }
  try {
    if (req.url === '/') {
      const html = await readFile(new URL('index.html', page), 'utf8');
      res.setHeader('Content-Type', 'text/html; charset=utf-8');
      res.end(html.replace('<script type="module" src="/app.js"></script>', '<script type="module" src="/fixture.js"></script>'));
      return;
    }
    if (!/^\/(?:vendor\/)?[a-z0-9.-]+\.(?:js|css)$/.test(req.url)) {
      res.writeHead(404).end();
      return;
    }
    if (req.url.endsWith('.css')) {
      res.setHeader('Content-Type', 'text/css');
    } else {
      res.setHeader('Content-Type', 'text/javascript');
    }
    res.end(await readFile(req.url === '/fixture.js' ? fixture : new URL(req.url.slice(1), page)));
  } catch {
    res.writeHead(404).end();
  }
});
server.listen(0, '127.0.0.1');
await once(server, 'listening');
const child = spawn(browser, ['--headless', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--remote-debugging-port=0', '--user-data-dir=' + profile, 'http://127.0.0.1:' + server.address().port], {windowsHide: true, stdio: 'ignore'});
const exited = chromiumExit(child);
let timer;
try {
  const result = await Promise.race([
    completion,
    exited.then(({code, signal, error}) => 'Chromium exited: ' + (error || signal || code)),
    new Promise(resolve => { timer = setTimeout(() => resolve('Browser test timed out'), 15000); })
  ]);
  assert.equal(result, 'PASS');
  console.log('PASS: node and container routes, sample times, metric gaps, timing controls, alerts and stale responses');
} finally {
  clearTimeout(timer);
  server.closeAllConnections();
  server.close();
  await removeChromiumProfile(child, exited, profile);
}
