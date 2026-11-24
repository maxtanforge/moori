// Shared console services that depend on no view state: DOM primitives, the
// fetch wrapper with machine-readable error parsing, and the top progress bar.
// The app entry registers the signed-out handler via setUnauthorizedHandler so
// this module stays a dependency-free leaf of the module graph.

export const element = id => document.getElementById(id);
export const enc = encodeURIComponent;

export function show(id, visible) { element(id).hidden = !visible; }

export function notice(message = '',tone = 'info') {
  const banner = element('notice');
  banner.dataset.tone = tone;
  // Failures must interrupt: role="alert" implies assertive live announcement,
  // while informational updates stay polite.
  const urgent = tone === 'error';
  banner.role = urgent ? 'alert' : 'status';
  banner.setAttribute('aria-live',urgent ? 'assertive' : 'polite');
  banner.textContent = message;
}

let unauthorizedHandler = () => {};
export function setUnauthorizedHandler(handler) { unauthorizedHandler = handler; }

export async function request(path, body, key, signal, method) {
  const options = {method, mode: 'same-origin', credentials: 'same-origin', redirect: 'error', cache: 'no-store', headers: {}, signal};
  if (body !== undefined) {
    options.method = method || 'POST';
    options.headers['Content-Type'] = 'application/json';
    options.body = JSON.stringify(body);
  }
  if (key) options.headers['Idempotency-Key'] = key;
  const response = await fetch(path, options);
  if (response.status === 401) unauthorizedHandler();
  if (!response.ok) {
    const raw = (await response.text()).trim();
    const error = new Error(raw || `HTTP ${response.status}`);
    error.status = response.status;
    if (raw.startsWith('{')) {
      try {
        const parsed = JSON.parse(raw);
        if (typeof parsed.code === 'string') error.code = parsed.code;
        if (typeof parsed.message === 'string') error.message = parsed.message;
      } catch {}
    }
    throw error;
  }
  if (response.status === 204) return null;
  return response.json();
}

const pageReads = new Set();
let pageProgressTimer;
let pageProgressShown = 0;

export async function trackPageRead(task,button) {
  const read = {};
  const bar = element('page-progress');
  pageReads.add(read);
  button?.setAttribute('aria-busy','true');
  if (pageReads.size === 1) {
    clearTimeout(pageProgressTimer);
    if (bar.hidden) pageProgressTimer = setTimeout(() => {
      bar.hidden = false;
      pageProgressShown = performance.now();
    },150);
  }
  try {
    return await task();
  } finally {
    button?.removeAttribute('aria-busy');
    if (pageReads.delete(read) && pageReads.size === 0) {
      clearTimeout(pageProgressTimer);
      if (!bar.hidden) pageProgressTimer = setTimeout(() => { bar.hidden = true; },
        Math.max(0,250 - (performance.now() - pageProgressShown)));
    }
  }
}

export function resetPageProgress() {
  pageReads.clear();
  clearTimeout(pageProgressTimer);
  element('page-progress').hidden = true;
}
