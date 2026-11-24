import {routeHref,option,field,row,joined,dateTime,percent,bytePair,formatBytes,download,deploymentState} from './ui-components.js';
import {element,enc,show,notice,request,trackPageRead,resetPageProgress,setUnauthorizedHandler} from './api.js';
import {resolveSystemSettingsPath,renderSystemSettings,currentSettingsPath,registrySettingsPath,refreshRegistryDiscovery,templateEditor,createNodeResource,pullNodeImage,nodeById,activeNode} from './management.js';
import './terminal.js';
import './user-management.js';
import './webhook-management.js';
import './alert-settings.js';
import './ci-management.js';
import './platform-audit.js';
import './image-management.js';
import './monitoring.js';
import {parseNodeRoute,nodeRoute,workspaceNodeName,nodePath,renderNodeNavigation,snapshotNode,nodeSnapshot,resetNodeWorkspace,loadDirectoryHealth,loadOverviewTotals,nodeNameRevision,rememberNodeDisplayName,setNodeSnapshot,selectWorkspaceNode,renderNodeHeading,directoryHealth,renderNodeDirectory,nodeDisplayNames,renderSidebarNode,applyNodeResourceChanges,renderWorkspaceBody,renderNodeRecovery,showNodeDisplayNameEditor,saveNodeDisplayName} from './node-workspace.js';
import {parseDeploymentEditor,renderMaintenancePage,deploymentEditorPath} from './maintenance-page.js';
import './compose-maintenance.js';
import './container-maintenance.js';

const roles = {platform_admin: '平台管理员', project_admin: '项目管理员', operator: '运维成员', viewer: '只读成员', none: '无项目角色'};
export let projects = [];
export let selected = null;
export let detail = null;
let epoch = 0;
export let accountEpoch = 0;
export let platformAdmin = false;
export let localExecution = false;
let containerListReturn;
let containerDrillOrigin;
setUnauthorizedHandler(signedOut);
export let agents = [];
let issuedAgentID = '';
let agentIssueAttempt;
export const cursors = {};
const operationKeys = new Map();

export function base() { return '/api/v1/projects/' + enc(selected.id); }
function canOperate() { return selected && ['platform_admin', 'project_admin', 'operator'].includes(selected.role); }
function canAdminister() { return selected && ['platform_admin', 'project_admin'].includes(selected.role); }
function updateDeploymentButton() {
  element('create-target').disabled = !platformAdmin;
  element('deploy-form').querySelector('button[type="submit"]').disabled = !canOperate() || !element('target').value || !element('release').value;
}

const consoleLinks = [...element('console-nav').querySelectorAll('[data-view]')];
const consoleViews = [...document.querySelectorAll('[data-console-view]')];

function setSidebarOpen(open) {
  element('sidebar-toggle').setAttribute('aria-expanded', String(open));
  element('console-sidebar').dataset.open = String(open);
}

function permittedConsoleView(view) {
  if (view === 'monitoring' && platformAdmin) return 'environments';
  return consoleLinks.some(link => link.dataset.view === view && (view === 'console' || platformAdmin)) ?
    view : 'console';
}

export function currentRoute() {
  return location.pathname.slice(1);
}

let renderedRoute = null;

export function renderConsoleView(requested, moveFocus = false) {
  if (element('console').hidden) return;
  if (moveFocus) {
    notice();
    resetPageProgress();
  }
  const route = platformAdmin ? parseNodeRoute(requested) : null;
  const returnToList = trackContainerListNavigation(nodeRoute,route,moveFocus);
  const returnToDetail = trackContainerDrilldown(nodeRoute,route,requested,moveFocus);
  const editor = route?.mode ? route : platformAdmin ? parseDeploymentEditor(requested) : null;
  const settingsPath = resolveSystemSettingsPath(requested);
  const view = editor ? 'maintenance' : route?.monitor ? 'monitoring' : route ? 'environments' :
    settingsPath ? 'settings' : permittedConsoleView(requested);
  const canonical = settingsPath || view;
  let settled = requested;
  if (!editor && !route && requested && canonical !== requested && currentRoute() === requested) {
    history.replaceState(null, '', routeHref(canonical));
    settled = canonical;
  }
  const navigated = moveFocus && settled !== renderedRoute;
  renderedRoute = settled;
  if (navigated) window.dispatchEvent(new CustomEvent('console-navigated', {detail: settled}));
  for (const panel of consoleViews) panel.hidden = panel.dataset.consoleView !== view;
  const topLevel = route ? 'environments' : editor ? 'console' : view;
  for (const link of consoleLinks) {
    if (link.dataset.view === topLevel) link.setAttribute('aria-current', 'page');
    else link.removeAttribute('aria-current');
  }
  const currentSection = consoleLinks.find(link => link.dataset.view === topLevel);
  const section = element('workspace-section');
  section.textContent = currentSection?.textContent || '';
  if (currentSection && (route || editor)) section.href = currentSection.getAttribute('href');
  else section.removeAttribute('href');
  const node = element('workspace-node');
  const locationName = route ? workspaceNodeName(route.id) : editor?.project || '';
  node.textContent = locationName;
  if (route && (route.category !== 'overview' || route.monitor)) node.href = routeHref(nodePath(route.id));
  else node.removeAttribute('href');
  show('workspace-path-divider',!!locationName);
  show('workspace-node',!!locationName);
  renderMaintenancePage(editor,requested);
  renderNodeNavigation(view === 'environments' || !!route, route, moveFocus);
  renderMonitorRoute(route?.monitor ? route : null,route?.id === snapshotNode ? nodeSnapshot?.overview : undefined);
  renderSystemSettings(settingsPath);
  setSidebarOpen(false);
  if (!moveFocus) return;
  if (returnToList) {
    const table = element('resource-view').querySelector('.container-list');
    if (table) table.scrollLeft = returnToList.left;
    const rowLink = table?.querySelector('[data-resource-focus="' + returnToList.target + ':details"]');
    (rowLink || element('container-search')).focus({preventScroll:true});
    window.scrollTo(0,returnToList.top);
    return;
  }
  const panel = consoleViews.find(item => item.dataset.consoleView === view);
  if (returnToDetail) {
    const tab = panel?.querySelector('.container-tab[aria-selected="true"]');
    tab?.focus({preventScroll:true});
    window.scrollTo(0,returnToDetail.top);
    return;
  }
  const heading = route?.logs ? element('container-logs-title') : route?.term ? element('container-terminal-title') : panel?.querySelector('h1, h2');
  heading?.focus({preventScroll:true});
  window.scrollTo(0, 0);
}

function trackContainerDrilldown(previous,route,path,moving) {
  const drilled = item => item?.container && (item.logs || item.term || item.monitor || item.mode);
  if (moving && drilled(route) && previous?.id === route.id && previous.category === 'containers' &&
      !drilled(previous) && (!previous.container || previous.container === route.container)) {
    containerDrillOrigin = {
      node:route.id,target:route.container,top:window.scrollY,
      path:nodePath(route.id,'containers',previous.container,previous.detailTab)
    };
  }
  const origin = containerDrillOrigin;
  if (drilled(route) && origin?.node === route.id && origin.target === route.container) {
    route.returnPath = origin.path;
    return;
  }
  if (moving && origin) {
    containerDrillOrigin = undefined;
    if (drilled(previous) && origin.path === path && route?.container === origin.target) return origin;
  }
}

function trackContainerListNavigation(previous,route,moveFocus) {
  if (!moveFocus) return;
  const inContainers = route?.category === 'containers' && previous?.id === route.id;
  if (!inContainers) {
    containerListReturn = undefined;
    return;
  }
  if (previous.category === 'containers' && !previous.container && !previous.mode && route.container) {
    containerListReturn = {top:window.scrollY,left:element('resource-view').querySelector('.container-list')?.scrollLeft || 0};
  }
  if (previous.container && !route.container && !route.mode) {
    const saved = containerListReturn;
    containerListReturn = undefined;
    return {top:saved?.top || 0,left:saved?.left || 0,target:previous.container};
  }
}

export function goToConsoleView(requested) {
  if (element('console').hidden) return;
  const view = platformAdmin && (parseNodeRoute(requested) || parseDeploymentEditor(requested)) ? requested :
    resolveSystemSettingsPath(requested) || permittedConsoleView(requested);
  if (currentRoute() !== view) history.pushState(null, '', routeHref(view));
  renderConsoleView(view, true);
}

element('sidebar-toggle').addEventListener('click', () => {
  setSidebarOpen(element('console-sidebar').dataset.open !== 'true');
});
document.addEventListener('click', event => {
  const link = event.target.closest('a[href^="/"]');
  if (!link || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  if (link.target && link.target !== '_self') return;
  if (element('console').hidden) return;
  event.preventDefault();
  goToConsoleView(link.getAttribute('href').slice(1));
});
document.addEventListener('keydown', event => {
  if (event.key !== 'Escape' || event.defaultPrevented || element('management-dialog').open ||
      element('console-sidebar').dataset.open !== 'true' || !element('sidebar-toggle').getClientRects().length) return;
  setSidebarOpen(false);
  element('sidebar-toggle').focus();
});
document.querySelector('.skip-link').addEventListener('click', event => {
  event.preventDefault();
  element('main').focus();
});
window.addEventListener('popstate', () => renderConsoleView(currentRoute(), true));

export function signedOut() {
  accountEpoch++;
  resetPageProgress();
  platformAdmin = false;
  localExecution = false;
  containerListReturn = undefined;
  containerDrillOrigin = undefined;
  agents = [];
  element('target-form').reset();
  element('password-form').reset();
  showAccountSettings(false);
  showTargetForm(false);
  resetAgentTokenPanel();
  show('environments', false);
  show('settings', false);
  renderSystemSettings('');
  if (element('management-dialog').open) element('management-dialog').close();
  element('management-content').replaceChildren();
  clearInterval(nodeTimer);
  monitorSignedOut();
  resetNodeWorkspace();
  renderMaintenancePage(null);
  element('resource-view').replaceChildren();
  element('resource-status').textContent = '';
  show('setup-panel', false);
  epoch++;
  selected = null;
  detail = null;
  projects = [];
  operationKeys.clear();
  show('console', false);
  show('console-sidebar', false);
  show('sidebar-toggle', false);
  show('workspace-header',false);
  document.body.classList.remove('workspace-active');
  element('workspace-username').textContent = '';
  element('workspace-avatar').textContent = '';
  show('operations-nav', false);
  setSidebarOpen(false);
  element('workspace').classList.remove('workspace-signed-in');
  show('logout', false);
  show('login-panel', true);
  element('login-form').elements.password.value = '';
}

// Preserve a mutation key if the response is lost so a retry returns its
// original deployment. A successful request allows a later new operation.
export async function submitDeployment(path, body) {
  const operation = path + JSON.stringify(body);
  if (!operationKeys.has(operation)) operationKeys.set(operation, Array.from(crypto.getRandomValues(new Uint8Array(32)), byte => byte.toString(16).padStart(2, "0")).join(""));
  const result = await request(path, body, operationKeys.get(operation));
  operationKeys.delete(operation);
  return result;
}

export function action(id, handler, type = 'click') {
  element(id).addEventListener(type, async event => {
    event.preventDefault();
    const buttons = element(id).tagName === 'FORM' ? [...element(id).querySelectorAll('button')] : [element(id)];
    if (buttons.some(button => button.disabled)) return;
    buttons.forEach(button => {
      button.disabled = true;
      // Icon buttons (data-action-icon) get busy feedback, e.g. the spinning
      // refresh glyph, matching what trackPageRead does for page reads.
      if (button.dataset.actionIcon) button.setAttribute('aria-busy','true');
    });
    notice();
    try { await handler(event); } catch (error) { notice(error.message || '请求失败，请重试。','error'); }
    finally {
      buttons.forEach(button => { button.disabled = false; button.removeAttribute('aria-busy'); });
      updateDeploymentButton();
    }
  });
}

export async function loadProjects(more = false) {
  const version = epoch;
  const page = await request('/api/v1/projects?after=' + enc(more ? cursors.projects || '' : ''));
  if (version !== epoch) return;
  if (!more) { projects = []; element('project').replaceChildren(); }
  for (const project of page.items) {
    projects.push(project);
    option(element('project'), project.id, project.id);
  }
  cursors.projects = page.next;
  show('more-projects', !!page.next);
  show('empty-projects', projects.length === 0);
  element('empty-projects').textContent = platformAdmin ? '创建第一个部署目标即可添加应用项目。' : '当前没有可访问的项目，请联系管理员授予项目权限。';
  if (!more) await selectProject();
}

export async function selectProject() {
  epoch++;
  selected = projects.find(project => project.id === element('project').value) || null;
  detail = null;
  for (const id of ['deployments', 'target', 'release', 'members', 'audit', 'snapshot', 'diagnostics']) element(id).replaceChildren();
  for (const name of ['deployments', 'targets', 'releases', 'members', 'audit']) {
    delete cursors[name];
    show('more-' + name, false);
  }
  element('member-form').reset();
  element('action-form').reset();
  element('role').textContent = '';
  show('empty-deployments', false);
  updateDeploymentButton();
  show('details', false);
  show('project-content', !!selected);
  if (!selected) return;
  element('role').textContent = roles[selected.role];
  show('deploy-form', canOperate());
  show('administration', canAdminister());
  const jobs = [loadDeployments()];
  if (canOperate()) jobs.push(loadTargets());
  if (canAdminister()) jobs.push(loadMembers(), loadAudit());
  await Promise.all(jobs);
}

export async function loadDeployments(more = false) {
  const version = epoch;
  const page = await request(base() + '/deployments?after=' + enc(more ? cursors.deployments || '' : ''));
  if (version !== epoch) return;
  if (!more) element('deployments').replaceChildren();
  for (const item of page.items) {
    const button = document.createElement('button');
    button.textContent = item.id;
    button.addEventListener('click', () => openDeployment(item.id).catch(error => notice(error.message,'error')));
    row('deployments', [button, item.target, item.release, deploymentStateCell(item)]);
  }
  cursors.deployments = page.next;
  show('more-deployments', !!page.next);
  show('empty-deployments', element('deployments').children.length === 0);
}

// Failed and needs-attention deployments usually carry execution output; link
// straight to the logs so the diagnosis does not require finding the detail
// panel button first.
const deploymentLogStates = ['failed', 'needs_attention'];
function deploymentStateCell(item) {
  if (!deploymentLogStates.includes(item.state)) return deploymentState(item.state);
  const cell = document.createElement('span');
  cell.textContent = deploymentState(item.state) + ' · ';
  const logs = document.createElement('button');
  logs.textContent = '查看日志';
  logs.addEventListener('click', () => openDeploymentLogs(item.id).catch(error => notice(error.message,'error')));
  cell.append(logs);
  return cell;
}

async function openDeploymentLogs(id) {
  await openDeployment(id);
  await diagnostics('logs');
}

async function loadTargets(more = false) {
  const version = epoch;
  const page = await request(base() + '/targets?after=' + enc(more ? cursors.targets || '' : ''));
  if (version !== epoch) return;
  if (!more) element('target').replaceChildren();
  for (const item of page.items) if (item.state !== 'container') option(element('target'), item.id, item.id);
  cursors.targets = page.next;
  show('more-targets', !!page.next);
  if (!more) await loadReleases();
}

async function loadReleases(more = false) {
  const version = epoch;
  const target = element('target').value;
  if (!more) element('release').replaceChildren();
  updateDeploymentButton();
  show('more-releases', false);
  show('release-empty', !!target && !element('release').children.length);
  if (!target) return;
  const page = await request(base() + '/targets/' + enc(target) + '/releases?after=' + enc(more ? cursors.releases || '' : ''));
  if (version !== epoch || target !== element('target').value) return;
  for (const item of page.items) {
    if (!item.id.startsWith('console-template-')) option(element('release'), item.id, item.id);
  }
  updateDeploymentButton();
  show('release-empty', !element('release').children.length);
  cursors.releases = page.next;
  show('more-releases', !!page.next);
}

let detailRequest = 0;
export async function openDeployment(id) {
  const version = epoch;
  const sequence = ++detailRequest;
  const item = await request(base() + '/deployments/' + enc(id));
  if (version !== epoch || sequence !== detailRequest) return;
  detail = item;
  show('save-as-template', platformAdmin && !!item.snapshot);
  const maintainable = platformAdmin && item.state === 'succeeded' && !!item.snapshot?.compose?.services;
  show('edit-deployment', maintainable);
  show('redeploy', maintainable);
  element('detail-title').textContent = item.id;
  element('detail-status').textContent = `${deploymentState(item.state)} · ${item.release}`;
  element('detail-digest').textContent = item.digest;
  element('snapshot').textContent = JSON.stringify(item.snapshot, null, 2);
  element('diagnostics').textContent = '';
  show('diagnostics', false);
  const cancelable = ['queued', 'dispatching', 'executing', 'verifying', 'reconciling', 'needs_attention'].includes(item.state);
  show('action-form', canOperate() && (cancelable || item.state === 'succeeded'));
  show('cancel-button', cancelable);
  show('rollback-button', item.state === 'succeeded');
  show('details', true);
}

async function loadMembers(more = false) {
  const version = epoch;
  const page = await request(base() + '/members?after=' + enc(more ? cursors.members || '' : ''));
  if (version !== epoch) return;
  if (!more) element('members').replaceChildren();
  for (const member of page.items) row('members', [member.username, roles[member.role]]);
  show('empty-members', element('members').children.length === 0);
  cursors.members = page.next;
  show('more-members', !!page.next);
}

async function loadAudit(more = false) {
  const version = epoch;
  const page = await request(base() + '/audit?after=' + (more ? cursors.audit || 0 : 0));
  if (version !== epoch) return;
  if (!more) element('audit').replaceChildren();
  for (const event of page.items) row('audit', [dateTime(event.created_at), event.action, event.actor, `${event.subject} ${event.detail}`]);
  show('empty-audit', element('audit').children.length === 0);
  cursors.audit = page.next;
  show('more-audit', !!page.next);
}

async function boot() {
  const setup = await request('/api/v1/auth/setup');
  if (setup.required) {
    signedOut();
    show('login-panel', false);
    show('setup-panel', true);
    return;
  }
  show('setup-panel', false);
  const account = await request('/api/v1/auth/me');
  platformAdmin = !!account.platform_admin;
  localExecution = !!account.local_execution;
  show('environments', platformAdmin);
  show('settings', platformAdmin);
  show('project-deployments-nav', !platformAdmin);
  show('deployment-back', platformAdmin);

  showTargetForm(false);
  show('create-target', platformAdmin);
  element('local-status').textContent = localExecution ? '直接使用主服务所在机器的 Docker，无需 Agent。' : (account.local_message || '本机执行未启用，可先接入远程 Agent。');
  element('execution-mode').querySelector('[value=local]').disabled = !localExecution;
  element('execution-mode').value = localExecution ? 'local' : 'existing';
  element('agent-endpoint').value = (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/agent/v1/connect';
  updateExecutionMode();
  element('password-form').elements.username.value = account.username;
  show('login-panel', false);
  show('logout', true);
  show('console', true);
  show('console-sidebar', true);
  show('sidebar-toggle', true);
  element('workspace-username').textContent = account.username;
  element('workspace-avatar').textContent = [...account.username].slice(0,2).join('').toUpperCase();
  show('workspace-header',true);
  document.body.classList.add('workspace-active');
  show('operations-nav', platformAdmin);
  element('workspace').classList.add('workspace-signed-in');
  renderConsoleView(currentRoute() || (platformAdmin ? 'environments' : 'console'));
  if (platformAdmin) {
    loadNodeList();
    clearInterval(nodeTimer);
    nodeTimer = setInterval(refreshVisibleNodeViews,15000);
  }
  await loadProjects();
}

function refreshVisibleNodeViews() {
  if (document.hidden) return;
  if (nodeRoute) void loadNodeResources();
  else if (currentSettingsPath === registrySettingsPath) void refreshRegistryDiscovery?.();
  else if (currentRoute() === 'environments' || !currentRoute()) {
    for (const after of directoryPages) void loadDirectoryHealth(after, accountEpoch);
    void loadOverviewTotals();
  }
}

let nodePage = '';
const directoryPages = new Set();
let nodeRequest = 0;
let pendingNodeRead;
let nodeTimer;

async function loadNodeList(more = false) {
  if (!platformAdmin) return;
  const version = accountEpoch;
  const select = element('resource-node');
  const previous = select.value;
  const nameRevision = nodeNameRevision;
  try {
    const after = more ? nodePage : '';
    const page = await request('/api/v1/agents?after=' + enc(after));
    if (version !== accountEpoch) return;
    if (nameRevision === nodeNameRevision) {
      rememberNodeDisplayName('@local',page.local_display_name);
      for (const agent of page.items || []) rememberNodeDisplayName(agent.id,agent.display_name);
    }
    if (!more) {
      select.replaceChildren();
      option(select, '@local', workspaceNodeName('@local'));
    }
    for (const agent of page.items || []) {
      [...select.querySelectorAll('option[data-direct]')].find(item => item.value === agent.id)?.remove();
      option(select, agent.id, workspaceNodeName(agent.id));
      select.lastElementChild.dataset.generation = agent.generation;
    }
    if (nodeRoute) {
      selectWorkspaceNode(nodeRoute.id);
      renderNodeHeading();
    } else if ([...select.options].some(item => item.value === previous)) select.value = previous;
    element('node-directory-status').textContent = '';
    void refreshRegistryDiscovery?.();
    nodePage = page.next || '';
    show('more-resource-nodes', !!nodePage);
    if (!more) {
      directoryPages.clear();
      directoryHealth.clear();
    }
    directoryPages.add(after);
    renderNodeDirectory();
    void loadDirectoryHealth(after, version);
    if (!more) void loadOverviewTotals();
    if (nodeRoute && !nodeSnapshot) await loadNodeResources();
  } catch (error) {
    if (version === accountEpoch) element('node-directory-status').textContent = '读取节点列表失败：' + error.message;
  }
}

let mutationRefresh;
const mutationRefreshDelays = [0,750,1500,3000,5000];

export async function refreshAfterNodeMutation(id,...sections) {
  if (!platformAdmin || nodeRoute?.id !== id) return;
  const account = accountEpoch;
  const active = mutationRefresh?.id === id && mutationRefresh.account === account;
  const refresh = active ? mutationRefresh : {id,account,pending:new Map()};
  for (const section of sections.length ? sections : ['']) {
    refresh.pending.set(section,{
      baseline: nodeSnapshot?.overview?.section_times?.[section],
      updated: nodeSnapshot?.updated_at,
      generated: nodeSnapshot?.overview?.generated_at,
      attempt:0,due:Date.now()
    });
  }
  if (active) return;
  mutationRefresh = refresh;
  const current = () => mutationRefresh === refresh && account === accountEpoch && nodeRoute?.id === id;
  try {
    while (current() && refresh.pending.size) {
      const now = Date.now();
      const due = [...refresh.pending].filter(([,entry]) => entry.due <= now);
      if (!due.length) {
        const next = Math.min(...[...refresh.pending.values()].map(entry => entry.due));
        await new Promise(resolve => setTimeout(resolve,Math.min(750,next - now)));
        continue;
      }
      await loadNodeResources(true);
      if (!current()) return;
      for (const [section,entry] of due) {
        if (refresh.pending.get(section) !== entry) continue;
        const snapshot = nodeSnapshot;
        const times = snapshot?.overview?.section_times;
        const fresh = snapshot?.overview?.available && !snapshot.read_failed &&
          (section && times ? !!times[section] && times[section] !== entry.baseline :
            snapshot.updated_at !== entry.updated || snapshot.overview.generated_at !== entry.generated);
        entry.attempt++;
        if (fresh || entry.attempt >= mutationRefreshDelays.length) refresh.pending.delete(section);
        else entry.due = Date.now() + mutationRefreshDelays[entry.attempt];
      }
    }
  } finally {
    if (mutationRefresh === refresh) mutationRefresh = undefined;
  }
}

export async function loadNodeResources(refreshAfterPending = false) {
  if (!platformAdmin || !nodeRoute) return;
  const version = accountEpoch;
  const id = nodeRoute.id;
  if (pendingNodeRead?.id === id && pendingNodeRead.version === version) {
    if (refreshAfterPending) pendingNodeRead.refresh = true;
    return pendingNodeRead.finished;
  }
  const requestId = ++nodeRequest;
  const nameRevision = nodeNameRevision;
  let finish;
  const finished = new Promise(resolve => { finish = resolve; });
  const pending = {id, version, finished};
  pendingNodeRead = pending;
  const status = element('resource-status');
  if (!nodeSnapshot) status.textContent = '正在读取节点资源…';
  try {
    const data = await request('/api/v1/nodes/' + enc(id) + '/resources',undefined,undefined,AbortSignal.timeout(12000));
    if (version !== accountEpoch || requestId !== nodeRequest || nodeRoute?.id !== id) return;
    const view = data.overview;
    if (nameRevision === nodeNameRevision && (nodeDisplayNames.get(id) || '') !== (data.display_name || '')) {
      rememberNodeDisplayName(id,data.display_name);
      renderNodeHeading();
      renderSidebarNode();
      renderNodeDirectory();
    }
    const node = element('resource-node').selectedOptions[0];
    if (node && data.generation) node.dataset.generation = data.generation;
    status.textContent = view ? [
      data.stale && '上次快照已过期', data.updated_at && '采集于 ' + dateTime(data.updated_at),
      view.truncated && '清单已截断', view.warning, view.message, view.host?.warning
    ].filter(Boolean).join(' · ') : (data.message || '等待节点上报资源。');
    element('node-workspace').classList.toggle('node-stale', !!data.stale);
    const contactChanged = nodeSnapshot?.contact !== data.contact;
    const recoveryChanged = contactChanged || nodeSnapshot?.read_failed ||
      nodeSnapshot?.management_available !== data.management_available || nodeSnapshot?.message !== data.message;
    const snapshotChanged = !nodeSnapshot || recoveryChanged || !data.updated_at ||
      nodeSnapshot.updated_at !== data.updated_at || nodeSnapshot.generation !== data.generation ||
      nodeSnapshot.overview?.generated_at !== view?.generated_at;
    const overrideDropped = applyNodeResourceChanges(data,id);
    setNodeSnapshot(data);
    if (contactChanged) renderSidebarNode();
    if (snapshotChanged || overrideDropped) renderWorkspaceBody();
    else renderNodeRecovery(id,data);
  } catch (error) {
    if (version === accountEpoch && requestId === nodeRequest && nodeRoute?.id === id) {
      status.textContent = (nodeSnapshot?.overview ? '读取失败，保留上次快照：' : '读取节点资源失败：') + error.message;
      element('node-workspace').classList.add('node-stale');
      if (!nodeSnapshot?.read_failed) {
        setNodeSnapshot({...nodeSnapshot,read_failed:true});
        renderWorkspaceBody();
      }
    }
  } finally {
    try {
      if (pendingNodeRead === pending) {
        pendingNodeRead = undefined;
        if (pending.refresh && accountEpoch === version && nodeRoute?.id === id) await loadNodeResources();
      }
    } finally {
      finish();
    }
  }
}

export function resourceGroups(view) {
  const groups = [];
  const host = view.host;
  if (host) groups.push(['系统与硬件', ['项目', '值'], [
    ['主机名', host.hostname], ['系统', host.os], ['内核', host.kernel],
    ['架构', host.architecture], ['CPU 型号', host.cpu_model], ['逻辑核数', host.cpu_count],
    ['硬件', [host.vendor, host.product].filter(Boolean).join(' · ')],
    ['CPU 使用率', percent(host.cpu_percent)], ['内存总量', formatBytes(host.memory_total_bytes)], ['可用内存', formatBytes(host.memory_available_bytes)],
    ['运行时间', host.uptime_seconds == null ? '—' : Math.floor(host.uptime_seconds / 60) + ' 分钟'],
    ['采集范围', {host:'宿主机', container:'容器可见范围', process:'进程可见范围'}[host.scope] || host.scope]
  ], 'host']);
  if (view.filesystems?.length) groups.push(['文件系统', ['挂载点', '设备', '类型', '总量', '已用', '可用'], view.filesystems.map(item =>
    [item.mountpoint, item.device, item.filesystem, formatBytes(item.total_bytes || null), formatBytes(item.total_bytes ? item.used_bytes : null), formatBytes(item.total_bytes ? item.avail_bytes : null)]), 'filesystems']);
  if (host?.interfaces?.length) groups.push(['主机网络接口', ['接口', '累计接收', '累计发送'], host.interfaces.map(item =>
    [item.name, formatBytes(item.rx_bytes), formatBytes(item.tx_bytes)]), 'host']);
  const engine = view.engine;
  if (engine) groups.push(['Docker 配置', ['项目', '值'], [
    ['名称', engine.name], ['版本', engine.version], ['API 版本', engine.api_version], ['数据目录', engine.root_dir],
    ['存储驱动', engine.storage_driver], ['日志驱动', engine.logging_driver],
    ['Cgroup', [engine.cgroup_driver, engine.cgroup_version].filter(Boolean).join(' · ')], ['默认运行时', engine.default_runtime]
  ], 'engine']);
  const usage = view.disk_usage;
  if (usage) groups.push(['Docker 磁盘占用', ['共享镜像层', '容器可写层', '卷', '构建缓存'], [
    [formatBytes(usage.layers), formatBytes(usage.containers), formatBytes(usage.volumes), formatBytes(usage.build_cache)]
  ], 'disk_usage']);
  return groups;
}

export function containerRow(item) {
  const stats = item.stats || {};
  const ports = (item.ports || []).map(port => port.public ? `${port.ip ? port.ip + ' · ' : ''}${port.public} → ${port.private}/${port.protocol}` : `${port.private}/${port.protocol}`);
  const mounts = (item.mounts || []).map(mount => `${mount.source || mount.type} → ${mount.destination}${mount.read_only ? '（只读）' : ''}`);
  const state = {running:'运行中', exited:'已停止', created:'已创建', restarting:'重启中', paused:'已暂停'}[item.state] || item.state;
  return [
    [item.name, item.id].filter(Boolean).join(' · '), item.image, [state, item.state === 'running' && item.health, item.status].filter(Boolean).join(' · '),
    [item.compose_project, item.compose_service].filter(Boolean).join(' / ') || '—', joined(ports),
    joined([...mounts, ...(item.networks || [])]),
    joined([item.restart_policy, item.nano_cpus ? `${item.nano_cpus / 1e9} 核` : '', item.memory_limit_bytes ? formatBytes(item.memory_limit_bytes) : '', item.pids_limit ? item.pids_limit + ' PID' : ''].filter(Boolean)), percent(stats.cpu_percent),
    bytePair(stats.memory_bytes, stats.memory_limit_bytes), stats.pids ?? '—', bytePair(stats.rx_bytes, stats.tx_bytes),
    bytePair(stats.block_read_bytes, stats.block_write_bytes), formatBytes(item.writable_bytes), dateTime(stats.sampled_at)
  ];
}

function updateExecutionMode() {
  const fields = element('target-form').elements;
  const mode = fields.mode.value;
  show('existing-agent-fields', mode === 'existing');
  show('local-status', mode === 'local');
  fields.existing.required = mode === 'existing';
  fields.existing.disabled = mode !== 'existing';
}

async function loadAgents(more = false) {
  const version = accountEpoch;
  const page = await request('/api/v1/agents?after=' + enc(more ? cursors.agents || '' : ''));
  if (version !== accountEpoch) return;
  if (!more) { agents = []; element('existing-agent').replaceChildren(); }
  for (const agent of page.items) {
    agents.push(agent);
    option(element('existing-agent'), agent.id, agent.id);
  }
  cursors.agents = page.next;
  show('more-agents', !!page.next);
}

function showTargetForm(open) {
  show('onboarding', open);
  element('create-target').setAttribute('aria-expanded', String(open));
}

action('create-target', async () => {
  const open = element('onboarding').hidden;
  showTargetForm(open);
  if (!open) return;
  const fields = element('target-form').elements;
  fields.mode.value = localExecution ? 'local' : 'existing';
  if (!fields.project.value) fields.project.value = selected?.id || '';
  updateExecutionMode();
  fields.project.focus();
  if (fields.mode.value === 'existing') await loadAgents();
});
action('cancel-target', () => {
  element('target-form').reset();
  showTargetForm(false);
  element('create-target').focus();
});
action('execution-mode', async () => {
  updateExecutionMode();
  if (element('execution-mode').value === 'existing') await loadAgents();
}, 'change');
action('refresh-agents', () => loadAgents());
action('more-agents', () => loadAgents(true));
action('target-form', async () => {
  const version = accountEpoch;
  const fields = element('target-form').elements;
  const mode = fields.mode.value;
  const body = {project: fields.project.value, target: fields.target.value};
  if (mode === 'local') body.local = true;
  else {
    const agent = agents.find(agent => agent.id === fields.existing.value);
    if (!agent) throw new Error('请选择一个已有 Agent。');
    body.agent = agent.id; body.generation = agent.generation;
  }
  await request('/api/v1/targets', body);
  if (version !== accountEpoch) return;
  element('target-form').reset();
  showTargetForm(false);
  await loadProjects();
  while (version === accountEpoch && !projects.some(project => project.id === body.project) && cursors.projects) await loadProjects(true);
  if (version !== accountEpoch) return;
  element('project').value = body.project;
  await selectProject();
  notice(mode === 'local' ? '本机部署目标已创建，无需接入 Agent。' : '部署目标已创建。');
}, 'submit');

function resetAgentTokenPanel() {
  issuedAgentID = '';
  agentIssueAttempt = undefined;
  element('agent-token-value').value = '';
  element('agent-token-form').reset();
  show('agent-token-result', false);
  show('agent-token-form', true);
  show('agent-token-panel', false);
  element('add-remote').setAttribute('aria-expanded', 'false');
}

function closeAgentTokenPanel() {
  resetAgentTokenPanel();
  element('add-remote').focus();
}

action('add-remote', () => {
  show('agent-token-panel', true);
  element('add-remote').setAttribute('aria-expanded', 'true');
  if (issuedAgentID) element('agent-token-value').focus();
  else element('agent-token-form').elements.id.focus();
});
action('cancel-agent-token', closeAgentTokenPanel);
action('dismiss-agent-token', closeAgentTokenPanel);
action('agent-token-form', async () => {
  const version = accountEpoch;
  const id = element('agent-token-form').elements.id.value;
  if (agentIssueAttempt?.id !== id) {
    const key = Array.from(crypto.getRandomValues(new Uint8Array(32)), byte => byte.toString(16).padStart(2,'0')).join('');
    agentIssueAttempt = {id,key};
  }
  const issued = await request('/api/v1/agents', {id}, agentIssueAttempt.key);
  if (version !== accountEpoch) return;
  agentIssueAttempt = undefined;
  issuedAgentID = issued.id;
  element('agent-token-value').value = issued.token;
  show('agent-token-form', false);
  show('agent-token-result', true);
  element('agent-token-value').focus();
  void loadNodeList();
}, 'submit');
action('copy-agent-token', async () => {
  if (!issuedAgentID) return;
  const field = element('agent-token-value');
  try {
    if (navigator.clipboard) {
      await navigator.clipboard.writeText(field.value);
      notice('已复制 Token。');
      return;
    }
  } catch {}
  field.focus();
  field.select();
  notice('已选中 Token，请使用复制快捷键。');
});
action('download-credential', () => {
  if (issuedAgentID) download(issuedAgentID + '.credential', element('agent-token-value').value + '\n');
});
action('download-compose', () => {
  if (!issuedAgentID) return;
  const compose = `services:\n  moori-agent:\n    image: \${MOORI_AGENT_IMAGE:-ghcr.io/maxtanforge/moori-agent:0.1.0}\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n      - moori-agent-data:/data\n      - /proc:/host/proc:ro\n      - /sys:/host/sys:ro\n      - /etc/os-release:/host/etc/os-release:ro\n      - /etc/hostname:/host/etc/hostname:ro\n    environment:\n      MOORI_ENDPOINT: "\${MOORI_ENDPOINT:?set Agent WebSocket endpoint}"\n      MOORI_AGENT_TOKEN: "\${MOORI_AGENT_TOKEN:?set Agent token}"\n      MOORI_HOST_ROOT: /host\n    restart: unless-stopped\nvolumes:\n  moori-agent-data:\n`;
  download('compose.' + issuedAgentID + '.yml', compose);
});
window.addEventListener('beforeunload', event => {
  if (issuedAgentID) { event.preventDefault(); event.returnValue = ''; }
});

action('setup-form', async () => {
  const fields = element('setup-form').elements;
  if (fields.password.value !== fields.confirmation.value) throw new Error('两次密码输入不一致。');
  try {
    await request('/api/v1/auth/setup', {username: fields.username.value, password: fields.password.value});
  } catch (error) {
    const status = await request('/api/v1/auth/setup');
    if (!status.required) { signedOut(); notice('初始化已完成，请使用管理员账号登录。'); return; }
    throw error;
  }
  element('setup-form').reset();
  await boot();
}, 'submit');

action('login-form', async () => {
  const fields = element('login-form').elements;
  await request('/api/v1/auth/login', {username: fields.username.value, password: fields.password.value});
  fields.password.value = '';
  await boot();
}, 'submit');
action('logout', async () => { await request('/api/v1/auth/logout', {}); signedOut(); });
function showAccountSettings(open) {
  show('password-form',open);
  element('account-toggle').setAttribute('aria-expanded',String(open));
  if (open) element('password-form').elements.username.focus();
}

action('account-toggle', () => showAccountSettings(element('password-form').hidden));
action('password-form', async () => {
  const fields = element('password-form').elements;
  if (fields.replacement.value !== fields.confirmation.value) throw new Error('两次新密码输入不一致。');
  const username = fields.username.value;
  await request('/api/v1/auth/account', {username, current: fields.current.value, replacement: fields.replacement.value});
  element('password-form').reset();
  showAccountSettings(false);
  signedOut();
  element('login-form').elements.username.value = username;
  notice('账户已更新，请重新登录。');
}, 'submit');
action('project', selectProject, 'change');
action('target', () => loadReleases(), 'change');
action('reload', () => loadProjects());
action('reload-container-detail', () => trackPageRead(() => loadNodeResources(true),element('reload-container-detail')));
action('reload-resources', () => trackPageRead(() => loadNodeResources(true),element('reload-resources')));
action('edit-node-display-name', () => showNodeDisplayNameEditor(element('node-display-name-form').hidden));
action('cancel-node-display-name', () => {
  showNodeDisplayNameEditor(false,true);
  element('node-display-name-status').textContent = '';
});
action('node-display-name-form', saveNodeDisplayName, 'submit');
action('more-resource-nodes', () => loadNodeList(true));
action('reload-node-directory', () => trackPageRead(() => loadNodeList(),element('reload-node-directory')));

action('save-as-template', () => selected && detail && templateEditor(null,{project:selected.id,deployment:detail.id}));
action('create-volume', () => createNodeResource('volume'));
action('pull-image', () => pullNodeImage(nodeById(activeNode().id)));
action('create-network', () => createNodeResource('network'));
element('resource-node').addEventListener('change', () => {
  if (nodeRoute) goToConsoleView(nodePath(element('resource-node').value, nodeRoute.category));
});
element('node-search').addEventListener('input', renderNodeDirectory);
element('node-contact-filter').addEventListener('change', renderNodeDirectory);
element('container-search').addEventListener('input', renderWorkspaceBody);
element('container-state-filter').addEventListener('change', renderWorkspaceBody);
action('more-projects', () => loadProjects(true));
action('more-targets', () => loadTargets(true));
action('more-releases', () => loadReleases(true));
action('more-deployments', () => loadDeployments(true));
action('more-members', () => loadMembers(true));
action('more-audit', () => loadAudit(true));
action('refresh-detail', () => detail && openDeployment(detail.id));
action('edit-deployment', () => detail && goToConsoleView(deploymentEditorPath(selected.id,detail.id,'edit')));
action('redeploy', () => detail && goToConsoleView(deploymentEditorPath(selected.id,detail.id,'redeploy')));
action('deploy-form', async () => {
  const version = epoch;
  const result = await submitDeployment(base() + '/deployments', {target: element('target').value, release: element('release').value});
  if (version !== epoch) return;
  await loadDeployments();
  await openDeployment(result.id);
  notice('部署申请已受理。');
}, 'submit');

action('action-form', async event => {
  if (!detail) return;
  const version = epoch;
  const id = detail.id;
  const name = event.submitter.value;
  const path = base() + '/deployments/' + enc(id) + '/' + name;
  const body = {note: element('action-form').elements.note.value};
  const result = name === 'rollback' ? await submitDeployment(path, body) : await request(path, body);
  if (version !== epoch) return;
  await openDeployment(result.id);
  await loadDeployments();
  notice(name === 'rollback' ? '已创建回滚部署。' : '取消请求已处理，执行结果请查看当前状态。');
}, 'submit');
action('member-form', async () => {
  const fields = element('member-form').elements;
  const version = epoch;
  await request(base() + '/members/' + enc(fields.username.value), {role: fields.role.value});
  if (version !== epoch) return;
  await loadProjects();
  notice('成员权限已更新。');
}, 'submit');
async function diagnostics(kind) {
  if (!detail) return;
  const version = epoch;
  const id = detail.id;
  const path = kind === 'logs' ? '/deployments/' + enc(id) + '/logs' : '/targets/' + enc(detail.snapshot.target) + '/health';
  const data = await request(base() + path);
  if (version !== epoch || detail?.id !== id) return;
  element('diagnostics').textContent = kind === 'logs' ? (data.text || '') + (data.truncated ? '\n（日志已截断）' : '') : JSON.stringify(data, null, 2);
  show('diagnostics', true);
}
action('logs-button', () => diagnostics('logs'));
action('health-button', () => diagnostics('health'));
function bootFailed(message) {
  element('boot-failure-text').textContent = message;
  show('boot-failure', true);
}
action('boot-retry', async () => {
  show('boot-failure', false);
  await boot();
});
// Before the panels are decided the page would otherwise stay blank; a failed
// boot needs its own retry entry rather than a transient notice alone.
boot().catch(error => {
  const message = error.message || '无法连接服务，请稍后重试。';
  if (element('login-panel').hidden && element('setup-panel').hidden) bootFailed(message);
  else notice(message,'error');
});
