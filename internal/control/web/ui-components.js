import {element} from './api.js';

const actionIconPaths = {
  menu:'M4 6h16M4 12h16M4 18h16',
  refresh:'M20 7v5h-5M20 12a8 8 0 0 0-14-5M4 17v-5h5M4 12a8 8 0 0 0 14 5',
  edit:'m13.5 6.5 4 4M4 20l4.5-1 11-11a2.8 2.8 0 0 0-4-4l-11 11L4 20z',
  close:'m6 6 12 12M18 6 6 18',
  logs:'M6 3h8l4 4v14H6zM14 3v5h4M9 12h6M9 16h6',
  terminal:'M5 7l5 5-5 5M12 17h7',
  remove:'M4 7h16M9 7V4h6v3M6 7l1 13h10l1-13M10 10v6M14 10v6',
  add:'M12 5v14M5 12h14',
  upload:'M12 16V3m-5 5 5-5 5 5M4 16v5h16v-5',
  logout:'M10 17l5-5-5-5M15 12H3M14 4h5a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-5',
  download:'M12 4v11m-5-4 5 5 5-5M5 20h14',
  deploy:'M6 5h4l2 2h6a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2zM8 12h8M8 16h5',
  server:'M4 4h16v7H4zM4 13h16v7H4zM8 8h.1M8 16h.1M13 8h4M13 16h4',
  remote:'M5 6h14v10H5zM3 19h18M9 10h6M12 7v6',
  pulse:'M3 12h4l3-6 4 12 3-6h4',
  settings:'M4 6h4m4 0h8M8 4v4M4 12h9m4 0h3m-7-2v4M4 18h4m4 0h8m-12-2v4',
  home:'M4 12V4h7v8zM13 4h7v5h-7zM4 14h7v6H4zM13 11h7v9h-7z',
  container:'M12 3 4 7.5v9L12 21l8-4.5v-9L12 3zm0 9v9M4 7.5l8 4.5 8-4.5',
  compose:'M12 3 3 8l9 5 9-5-9-5zm-9 9 9 5 9-5m-18 4 9 5 9-5',
  image:'M4 5h16v14H4zM4 15l5-5 5 5 2-2 4 4M15 8h.1',
  network:'M9 3h6v5H9zM3 16h6v5H3zM15 16h6v5h-6zM12 8v4M6 16v-4h12v4',
  volume:'M4 6c0-2 4-3 8-3s8 1 8 3-4 3-8 3-8-1-8-3zm0 0v12c0 2 4 3 8 3s8-1 8-3V6M4 12c0 2 4 3 8 3s8-1 8-3',
  arrow:'M5 12h13m-5-5 5 5-5 5',
  northeast:'M6 18 18 6M9 6h9v9',
  online:'M4 12l5 5L20 6',
  offline:'M6 12h12',
  play:'M8 5l12 7-12 7z',
  stop:'M6 6h12v12H6z',
  pause:'M9 5v14M15 5v14'
};

export function uiIcon(name) {
  const ns = 'http://www.w3.org/2000/svg';
  const icon = document.createElementNS(ns,'svg');
  icon.setAttribute('viewBox','0 0 24 24');
  icon.setAttribute('aria-hidden','true');
  icon.setAttribute('focusable','false');
  icon.classList.add('action-icon');
  const path = document.createElementNS(ns,'path');
  path.setAttribute('d',actionIconPaths[name]);
  icon.append(path);
  return icon;
}

export function actionIcon(button,name,label = button.textContent.trim(),iconOnly = true) {
  const icon = uiIcon(name);
  button.classList.add(iconOnly ? 'icon-button' : 'with-action-icon');
  if (iconOnly) {
    button.setAttribute('aria-label',label);
    button.title = label;
    button.replaceChildren(icon);
  } else {
    button.prepend(icon);
  }
  return button;
}

for (const button of document.querySelectorAll('button[data-action-icon]')) {
  actionIcon(button,button.dataset.actionIcon,button.getAttribute('aria-label') ?? undefined);
}
for (const action of document.querySelectorAll('[data-action-label-icon]')) {
  actionIcon(action,action.dataset.actionLabelIcon,undefined,false);
}
for (const glyph of document.querySelectorAll('[data-heading-icon]')) {
  glyph.append(uiIcon(glyph.dataset.headingIcon));
}
for (const link of document.querySelectorAll('[data-nav-icon]')) {
  link.prepend(uiIcon(link.dataset.navIcon));
}

export function workspaceText(tag, value, className) {
  const result = document.createElement(tag);
  if (className) result.className = className;
  result.textContent = value;
  return result;
}

export function routeHref(path) {
  return '/' + path;
}

export function linkRoute(link) {
  const href = link.getAttribute('href') || '';
  return href.startsWith('/') || href.startsWith('#') ? href.slice(1) : '';
}

export function workspaceLink(path, label, className) {
  const link = workspaceText('a', label, className);
  link.href = routeHref(path);
  return link;
}

export function workspaceGlyph(name) {
  const glyph = workspaceText('span','','workspace-glyph');
  glyph.append(uiIcon(name));
  return glyph;
}

export function statusPill(text, state = '') {
  return workspaceText('span',text,'status-pill' + (state ? ' ' + state : ''));
}

export function managementActions(...buttons) {
  const wrap = document.createElement('div');
  wrap.className = 'resource-actions';
  wrap.append(...buttons.filter(Boolean));
  return wrap;
}

export function field(name, label, settings = {}, tag = 'input') {
  const wrapper = document.createElement('label');
  wrapper.textContent = label;
  const input = document.createElement(tag);
  Object.assign(input, {name}, settings);
  wrapper.append(input);
  return wrapper;
}

export function option(select, value, label) {
  const node = document.createElement('option');
  node.value = value;
  node.textContent = label;
  select.append(node);
}

export function optionField(name, label, options, start = '') {
  const wrapper = field(name, label, {required:true}, 'select');
  const select = wrapper.lastElementChild;
  for (const [value, text] of options) option(select, value, text);
  if (options.some(([value]) => value === start)) select.value = start;
  return wrapper;
}

function setDisplayValue(target, value) {
  if (value instanceof Node) target.append(value);
  else target.textContent = value === '' || value == null ? '—' : value;
}

function tableRow(values, rowHeader) {
  const tr = document.createElement('tr');
  for (const value of values) {
    const first = rowHeader && !tr.firstElementChild;
    const cell = document.createElement(first ? 'th' : 'td');
    if (first) cell.scope = 'row';
    setDisplayValue(cell, value);
    tr.append(cell);
  }
  return tr;
}

export function resourceTable(headers, rows, rowHeaders = false) {
  const wrap = document.createElement('div');
  wrap.className = 'table-wrap';
  const table = document.createElement('table');
  const thead = document.createElement('thead');
  const headRow = document.createElement('tr');
  for (const label of headers) {
    const th = document.createElement('th');
    th.scope = 'col';
    th.textContent = label;
    headRow.append(th);
  }
  const tbody = document.createElement('tbody');
  for (const values of rows) tbody.append(tableRow(values,rowHeaders));
  thead.append(headRow);
  table.append(thead, tbody);
  wrap.append(table);
  return wrap;
}

export function snapshotFacts(values) {
  const list = workspaceText('dl','','snapshot-facts');
  for (const [label,value] of values) {
    const item = document.createElement('div');
    const detail = workspaceText('dd','');
    setDisplayValue(detail, value);
    item.append(workspaceText('dt',label),detail);
    list.append(item);
  }
  return list;
}

export function row(table, values) {
  element(table).append(tableRow(values, false));
}

export function formatContainerPort(port) {
  const target = `${port.private}/${port.protocol}`;
  if (!port.public) return target;
  const ip = port.ip || '';
  const host = ip.includes(':') && !ip.startsWith('[') ? `[${ip}]` : ip;
  return `${host ? host + ':' : ''}${port.public} → ${target}`;
}

export function joined(items) { return (items || []).join(' · ') || '—'; }
export function dateTime(value) {
  if (!value || value === '0001-01-01T00:00:00Z') return '—';
  const date = new Date(value);
  return Number.isFinite(date.getTime()) ? date.toLocaleString() : '—';
}
// Deployment lifecycle states, shared verbatim by the console deployment
// tables and the CI run records so both surfaces speak the same vocabulary.
export const deploymentStates = {queued:'排队中',running:'运行中',succeeded:'成功',failed:'失败',uncertain:'结果待确认',
  dispatching:'派发中',executing:'部署中',verifying:'验证中',reconciling:'核查中',
  canceled:'已取消',abandoned:'已终止',needs_attention:'需要处理'};
export function deploymentState(state) { return deploymentStates[state] || state; }
export function percent(value) { return Number.isFinite(value) ? value.toFixed(1) + '%' : '—'; }
export function bytePair(first, second) { return first == null && second == null ? '—' : formatBytes(first) + ' / ' + formatBytes(second); }

export function formatBytes(value) {
  if (value == null || !Number.isFinite(Number(value)) || Number(value) < 0) return '—';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let size = Number(value), unit = 0;
  while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit++; }
  return `${unit ? size.toFixed(1) : size} ${units[unit]}`;
}

export function download(name, content) {
  const url = URL.createObjectURL(new Blob([content], {type: 'application/octet-stream'}));
  const link = document.createElement('a');
  link.href = url; link.download = name;
  document.body.append(link); link.click(); link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
