// Console behavioural smoke without a browser: bundles the ES-module graph
// with esbuild, boots the real index.html in jsdom with a stubbed fetch, and
// drives history routing end-to-end. Run with `npm test` in this directory
// (requires network for the initial `npm install`).
import jsdom from 'jsdom';
const {JSDOM, ResourceLoader, VirtualConsole} = jsdom;
import {readFileSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {build} from 'esbuild';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const BUNDLE = new URL('.bundle/console-bundle.js', import.meta.url);

await build({
  entryPoints: [ROOT + 'app.js'],
  bundle: true,
  format: 'iife',
  platform: 'browser',
  legalComments: 'none',
  outfile: fileURLToPath(BUNDLE),
  logLevel: 'silent',
});

const bundle = readFileSync(BUNDLE);
const scriptFiles = ['vendor/xterm.js', 'vendor/addon-fit.js', 'vendor/uplot.js'];
const files = new Map();
for (const name of scriptFiles) files.set('/' + name, readFileSync(ROOT + name));
files.set('/app.js', bundle);

class Loader extends ResourceLoader {
  fetch(url, options) {
    const path = new URL(url).pathname;
    if (files.has(path)) return Promise.resolve(files.get(path));
    return Promise.resolve(Buffer.from(''));
  }
}

const failures = [];
const noise = /Not implemented|Could not parse CSS/i;

const adminMe = {username: 'root', platform_admin: true, local_execution: false};

function boot(initialURL, me = adminMe) {
  const vc = new VirtualConsole();
  const errors = [];
  vc.on('jsdomError', e => { if (!noise.test(String(e.message))) errors.push('jsdomError: ' + e.message + ' :: ' + (e.detail && e.detail.stack || '')); });
  vc.on('error', (...a) => { if (!noise.test(a.join(' '))) errors.push('console.error: ' + a.join(' ')); });
  const stub = async (input, init = {}) => {
    const url = String(input && input.url ? input.url : input);
    const path = new URL(url, 'https://control.example').pathname;
    const json = body => new Response(JSON.stringify(body), {status: 200, headers: {'Content-Type': 'application/json'}});
    if (path === '/api/v1/auth/setup') return json({required: false});
    if (path === '/api/v1/auth/me') return json(me);
    if (path === '/api/v1/agents') return json({items: [{id: '@local', contact: 'online', display_name: '本机 Docker'}]});
    if (path === '/api/v1/monitor/nodes') return json({items: []});
    if (path === '/api/v1/monitor/nodes/%40local/history') return json({
      from: '2026-11-21T10:00:00Z', until: '2026-11-21T11:00:00Z', step_seconds: 30, generation: 1,
      points: [
        {at: '2026-11-21T10:00:00Z', cpu_percent: 12.5, memory_bytes: 104857600},
        {at: '2026-11-21T10:00:30Z', cpu_percent: 9.1, memory_bytes: 104857600},
        {at: '2026-11-21T10:01:00Z', cpu_percent: 44.2, memory_bytes: 209715200},
        {at: '2026-11-21T10:01:30Z', cpu_percent: 8.0, memory_bytes: 209715200},
      ],
      events: [{kind: 'deployment_succeeded', at: '2026-11-21T10:00:45Z'}],
    });
    if (path === '/api/v1/projects') return json({items: [{id: 'demo', role: 'platform_admin'}], next: null});
    if (path === '/api/v1/projects/demo/deployments') return json({items: [
      {id: 'dep-1', target: 'production', release: 'v1', state: 'needs_attention'}], next: null});
    if (path === '/api/v1/projects/demo/audit') return json({items: [
      {created_at: '2026-11-21T08:12:33Z', action: 'release.created', actor: 'max', subject: 'v1', detail: 'published'}], next: null});
    if (path.startsWith('/api/v1/projects/demo/')) return json({items: [], next: null});
    if (path === '/api/v1/overview') return json({nodes:{online:0,offline:0,total:0},deployments:{succeeded_window:0,failed_window:0},alerts:{enabled:false,active:0,window:0}});
    return json({items: [], next: null});
  };
  const page = readFileSync(ROOT + 'index.html', 'utf8').replace('<script type="module" src="/app.js"></script>', '<script src="/app.js" defer></script>');
  const dom = new JSDOM(page, {
    url: initialURL, runScripts: 'dangerously', resources: new Loader(),
    pretendToBeVisual: true, virtualConsole: vc,
    beforeParse(parsed) {
      parsed.fetch = stub;
      if (parsed.devicePixelRatio === undefined) parsed.devicePixelRatio = 1;
      // jsdom lacks window.matchMedia, which the vendored uPlot calls at load
      // time to watch device-pixel-ratio changes; real browsers always have it.
      if (!parsed.matchMedia) {
        parsed.matchMedia = () => ({matches: false, media: '', onchange: null,
          addEventListener() {}, removeEventListener() {}, dispatchEvent() { return false; }});
      }
      // jsdom has no canvas implementation; uPlot charts need a 2D context
      // that tolerates every call and property write, so chart construction
      // (layout, legend DOM, hooks) stays exercised without pixel output.
      const stubContext = () => new Proxy(function() {}, {
        get(target, prop) {
          if (prop === 'measureText') return () => ({width: 0});
          if (typeof prop === 'symbol') return undefined;
          return target;
        },
        set() { return true; },
        apply() { return stubContext(); },
      });
      parsed.HTMLCanvasElement.prototype.getContext = function() { return stubContext(); };
      // jsdom has AbortSignal but not AbortSignal.any (a real browser API the
      // console uses); polyfill it so app promises do not reject for a jsdom-only
      // reason and genuine rejections stay meaningful.
      if (parsed.AbortSignal && !parsed.AbortSignal.any) {
        parsed.AbortSignal.any = signals => {
          const controller = new parsed.AbortController();
          for (const signal of signals) signal?.addEventListener('abort', () => controller.abort(signal.reason), {once: true});
          return controller.signal;
        };
      }
    },
  });
  const {window} = dom;
  return {window, errors};
}

let rejections = 0;
process.on('unhandledRejection', reason => {
  rejections++;
  console.log('UNHANDLED-REJECTION', String(reason && reason.message || reason).split('\n')[0]);
});
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
function check(name, condition, detail = '') {
  if (condition) console.log('PASS ' + name);
  else { failures.push(name + ' ' + detail); console.log('FAIL ' + name + ' ' + detail); }
}

// --- Scenario 1: boot at root as admin, then in-app navigation ---
{
  const {window, errors} = boot('https://control.example/');
  await sleep(250);
  const doc = window.document;
  check('boot: no script errors', errors.length === 0, errors.join('\n'));
  check('boot: console visible', !doc.getElementById('console').hidden);
  check('boot: admin sees environments', !doc.getElementById('environments').hidden);
  check('boot: root URL kept', window.location.pathname === '/');

  doc.querySelector('#console-nav a[data-view=console]').click();
  await sleep(50);
  check('nav click: URL /console', window.location.pathname === '/console', window.location.pathname);
  check('nav click: console panel visible', !doc.querySelector('[data-console-view="console"]').hidden);
  await sleep(200);
  check('console: deployment state localized', doc.getElementById('deployments').textContent.includes('需要处理'),
    doc.getElementById('deployments').textContent);
  check('console: audit rows use locale time', doc.getElementById('audit').textContent.includes('2026') &&
      !doc.getElementById('audit').textContent.includes('2026-11-21T'),
    doc.getElementById('audit').textContent);
  check('console: members empty state', !doc.getElementById('empty-members').hidden);
  check('console: audit rows suppress empty state', doc.getElementById('empty-audit').hidden);

  doc.querySelector('#console-nav a[data-view=environments]').click();
  await sleep(50);
  check('nav click: URL /environments', window.location.pathname === '/environments', window.location.pathname);

  // in-app navigation into the settings area, then between its tabs
  doc.querySelector('#console-nav a[data-view=settings]').click();
  await sleep(50);
  check('nav click: URL /settings/registries', window.location.pathname === '/settings/registries', window.location.pathname);
  check('nav click: settings panel visible', !doc.querySelector('[data-console-view="settings"]').hidden);
  doc.querySelector('#settings-tabs a[href="/settings/users"]').click();
  await sleep(50);
  check('tab click: URL /settings/users', window.location.pathname === '/settings/users', window.location.pathname);
  check('tab click: users tab marked', doc.querySelector('#settings-tabs a[href="/settings/users"]')?.getAttribute('aria-current') === 'page');

  // deep route via pushState + popstate (back/forward path)
  window.history.pushState(null, '', '/node/%40local/monitor');
  window.dispatchEvent(new window.PopStateEvent('popstate'));
  await sleep(120);
  check('popstate: URL kept', window.location.pathname === '/node/%40local/monitor');
  check('popstate: monitoring panel visible', !doc.querySelector('[data-console-view="monitoring"]').hidden);
  check('popstate: two metric charts render', doc.querySelectorAll('#monitor-charts .monitor-chart').length === 2,
    doc.querySelectorAll('#monitor-charts .monitor-chart').length + ' charts');
  check('popstate: uPlot canvas attached', doc.querySelectorAll('#monitor-charts canvas').length === 2,
    doc.querySelectorAll('#monitor-charts canvas').length + ' canvases');
  check('popstate: readout shows latest sample', (doc.querySelector('#monitor-charts .monitor-readout') || {}).textContent?.includes('CPU'));

  window.history.back();
  await sleep(120);
  check('history.back(): returns to /settings/users', window.location.pathname === '/settings/users', window.location.pathname);
  check('history.back(): settings visible again', !doc.querySelector('[data-console-view="settings"]').hidden);
  window.close();
}

// --- Scenario 2: deep link boots straight into the route ---
{
  const {window, errors} = boot('https://control.example/node/%40local/containers');
  await sleep(250);
  const doc = window.document;
  check('deep link: no script errors', errors.length === 0, errors.join('\n'));
  check('deep link: URL preserved', window.location.pathname === '/node/%40local/containers', window.location.pathname);
  check('deep link: environments panel visible', !doc.getElementById('environments').hidden);
  window.close();
}

// --- Scenario 3: operator (non-admin) at unknown path falls back to console ---
{
  const {window, errors} = boot('https://control.example/garbage/path', {username: 'op', platform_admin: false, local_execution: true});
  await sleep(250);
  const doc = window.document;
  check('unknown route: no fatal errors', errors.length === 0, errors.join('\n'));
  check('unknown route: URL self-heals to /console', window.location.pathname === '/console', window.location.pathname);
  check('unknown route: console panel visible', !doc.querySelector('[data-console-view="console"]').hidden);
  window.close();
}

if (rejections) failures.push(rejections + ' unhandled promise rejection(s)');
console.log(failures.length ? '\n' + failures.length + ' FAILURES' : '\nALL CHECKS PASSED');
process.exit(failures.length ? 1 : 0);
