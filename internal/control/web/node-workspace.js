import {workspaceLink,workspaceText,uiIcon,option,workspaceGlyph,statusPill,routeHref,resourceTable,snapshotFacts,actionIcon,managementActions,dateTime,row,formatBytes,percent,bytePair,joined,publishedPortLabels} from './ui-components.js';
import {stopContainerLogs,renderContainerLogs,nodeById,renderNodeOperationHistory,managementButton,containerLogsLink,containerTerminalLink,containerRemoveButton,composeRemoveButton,maintenanceOperationButton,performOperation,dialogOperationSurface,nodeOperationPath,operationStates,containerOperationMessage,operationPending,managementDialog,isMissingContainer,networkMaintenance,guardedResourceRemoval} from './management.js';
import {stopContainerTerminal,renderContainerTerminal} from './terminal.js';
import {openContainerWebhookSettings} from './webhook-management.js';
import {openContainerCISettings} from './ci-management.js';
import {clearImageDetailEditor,imageUsage,imageManagementActions,buildImageTagPanels} from './image-management.js';
import {initializeNodeMaintenance} from './maintenance-page.js';
import {readContainerConfiguration} from './container-maintenance.js';
import {enc,element,show,notice,trackPageRead,request} from './api.js';
import {localExecution,action,detail,loadNodeResources,accountEpoch,platformAdmin,resourceGroups,selected,refreshAfterNodeMutation,containerRow,refreshAfterNodeDeletion,goToConsoleView} from './app.js';

export const nodeCategories = [
  ['overview','节点概览'], ['containers','容器'], ['compose','Compose 项目'], ['images','镜像'],
  ['networks','网络'], ['volumes','卷'], ['host','主机与 Docker'], ['operations','操作记录']
];
const nodeCategoryIcons = {
  overview:'home',containers:'container',compose:'compose',images:'image',networks:'network',volumes:'volume',host:'server',operations:'logs'
};
const resourceCatalog = {
  compose:{list:'compose_projects', title:'Compose 项目', key:item => item.name, name:item => item.name},
  images:{list:'images', title:'镜像', key:item => item.full_id || item.id, name:item => item.tags?.[0] || item.id},
  networks:{list:'networks', title:'网络', key:item => item.id || item.name, name:item => item.name},
  volumes:{list:'volumes', title:'卷', key:item => item.name, name:item => item.name}
};
export const directoryHealth = new Map();
let overviewTotals;
export let nodeDirectoryRevision = 0;
const pendingNodeDeletions = new Set();
export const nodeDisplayNames = new Map();
export let nodeNameRevision = 0;
export let nodeRoute;
export let nodeSnapshot;
export let snapshotNode;
let containerConfiguration;
let containerSelection = new Set();
let containerSelectionNode = '';
let containerInlineOperation;
const nodeResourceChanges = new Map();

window.addEventListener('pagehide',clearContainerConfiguration);
window.addEventListener('pageshow',event => {
  if (event.persisted && nodeRoute?.container && !nodeRoute.mode) renderWorkspaceBody();
});

export function nodePath(id, category = 'overview', resource = '', mode = '') {
  return ['node', enc(id), category, resource && enc(resource), mode].filter(Boolean).join('/');
}

function nodeMonitorLink(id,target = '',label = '查看监控') {
  const path = target ? nodePath(id,'containers',target,'monitor') : nodePath(id,'monitor');
  return workspaceLink(path,label,'editor-link');
}

export function parseNodeRoute(value) {
  const [type, node, category, target, mode, extra] = value.split('/');
  if (type !== 'node' || extra !== undefined) return null;
  try {
    const id = decodeURIComponent(node);
    if (id !== '@local' && !/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(id)) return null;
    if (category === 'monitor' && target === undefined) return {id,category:'overview',monitor:true};
    if (!nodeCategories.some(([key]) => key === category)) return null;
    if (target === undefined) return {id,category};
    if (category === 'containers' && target === 'new' && mode === undefined) return {id,category,mode:'create'};
    if (category === 'compose' && target === 'new' && mode === 'create') return {id,category,mode:'create'};
    const logs = category === 'containers' && mode === 'logs';
    const term = category === 'containers' && mode === 'term';
    const monitor = category === 'containers' && mode === 'monitor';
    const detailTab = category === 'containers' && ['specs','config'].includes(mode);
    if (!logs && !term && !monitor && !detailTab && mode !== undefined && (!['edit','redeploy','deployments'].includes(mode) || !['containers','compose'].includes(category))) return null;
    const key = decodeURIComponent(target);
    if (category === 'operations') return mode === undefined && /^[a-f0-9]{32}$/.test(key) ? {id,category,operation:key} : null;
    if (category === 'containers') {
      if (!/^[0-9a-f]{64}$/.test(key)) return null;
      if (logs || term || monitor) return {id,category,container:key, ...(logs ? {logs:true} : term ? {term:true} : {monitor:true})};
      if (detailTab) return {id,category,container:key,detailTab:mode};
      return {id,category,container:key,mode};
    }
    if (!Object.hasOwn(resourceCatalog,category) || !key || key.length > 512) return null;
    return {id,category,resource:key,mode};
  } catch {
    return null;
  }
}

export function workspaceNodeName(id) {
  return nodeDisplayNames.get(id) || (id === '@local' ? '本机 Docker' : id);
}

// An optimistic edit mutates the displayed inventory. Re-read the server
// representation until the edit is confirmed or its local override expires.
export function nodeSnapshotETag() {
  return !nodeSnapshot?.read_failed && !nodeResourceChanges.size && nodeSnapshot?.etag;
}

export function setNodeSnapshot(snapshot) {
  nodeSnapshot = snapshot;
}

export function rememberNodeDisplayName(id, name) {
  if (name) nodeDisplayNames.set(id,name);
  else nodeDisplayNames.delete(id);
}

export function renderNodeHeading() {
  if (!nodeRoute) return;
  const {id} = nodeRoute;
  const name = workspaceNodeName(id);
  const kind = id === '@local' ? '本机 Docker' : '远程 Agent';
  element('workspace-node').textContent = name;
  if (nodeRoute.monitor) updateMonitorIdentity(id,snapshotNode === id ? nodeSnapshot?.overview : undefined);
  element('node-heading').textContent = name;
  element('node-kind').textContent = kind;
  show('node-kind', name !== kind);
}

function nodeContact(id) {
  if (id === '@local' && !localExecution) return 'disabled';
  return directoryHealth.get(id)?.contact;
}

const nodeContactLabels = {online:'在线',offline:'离线',never_seen:'尚未连接',disabled:'不可用'};

export function nodeManagementIssue(id,snapshot = snapshotNode === id ? nodeSnapshot : undefined) {
  const local = id === '@local';
  if (local && !localExecution) return {
    title:'本机 Docker 不可用', reason:snapshot?.message || element('local-status').textContent,
    next:'本机 Docker 在主服务启动时检测。修正配置并重启后，回到此处确认；修改挂载或运行用户后需重新创建主服务容器。远程节点可单独接入。'
  };
  if (snapshot?.contact === 'disabled') return {
    title:'节点连接已停用', reason:snapshot.message,
    next:'在环境目录核对该节点是否已登记、凭证是否仍有效。只有确认凭证失效时才需要重新签发。'
  };
  if (snapshot?.contact === 'offline') return {
    title:local ? '本机执行器离线' : '远程 Agent 已离线',
    next:local ? '检查主服务日志和 Docker 是否运行；恢复后在此刷新状态。' : '在远程主机检查 Agent 是否运行、日志中的连接错误，以及主服务地址是否可达。连接恢复后，此页会自动更新。'
  };
  if (snapshot?.contact === 'never_seen') return {
    title:local ? '等待本机执行器首次连接' : '等待远程 Agent 首次连接',
    next:local ? '稍候刷新；若一直未连接，请查看主服务启动日志。' : '确认已在远程主机启动对应的 Agent，并核对主服务地址和首次接入凭证。'
  };
  if (snapshot?.read_failed) return {
    title:'暂时无法确认节点状态', next:'请刷新快照，待主服务重新取得节点状态后再继续操作。'
  };
  if (snapshot?.overview?.available === false) return {
    title:'节点在线，但 Docker 不可用', reason:snapshot.overview.message,
    next:'检查该节点的 Docker 服务、socket 挂载和进程权限。远程节点还可查看 Agent 日志。'
  };
  if (snapshot?.contact === 'online' && !snapshot.overview) return {
    title:'节点已连接，等待资源清单',
    next:'首次采集完成后会自动显示资源。持续没有清单时，请检查节点的 Docker 和采集日志。'
  };
  if (snapshot?.contact === 'online' && snapshot.management_available === false) return {
    title:'节点在线，Docker 管理尚未就绪',
    next:local ? '稍候刷新；若持续不可用，请检查主服务日志。' : '稍候刷新。若持续不可用，请检查 Agent 日志，并确认 Agent 版本支持 Docker 管理。'
  };
  return null;
}

export function renderNodeRecovery(id,snapshot) {
  const panel = element('node-recovery');
  const issue = nodeManagementIssue(id,snapshot);
  const lastContact = issue && snapshot?.contact === 'offline' && snapshot.last_contact;
  const signature = JSON.stringify([id,issue,lastContact,!!snapshot?.overview]);
  if (panel.dataset.state === signature) return;
  panel.dataset.state = signature;
  panel.hidden = !issue;
  panel.replaceChildren();
  if (!issue) return;
  panel.append(workspaceText('h3',issue.title));
  if (issue.reason) panel.append(workspaceText('p',issue.reason));
  if (lastContact) panel.append(workspaceText('p','上次连接：' + dateTime(lastContact),'work-note'));
  if (snapshot?.overview?.available) panel.append(workspaceText('p','下方资源来自最近一次采集，目前仅供查看。','work-note'));
  panel.append(workspaceText('p',issue.next,'work-note'));
  if (id === '@local' || snapshot?.contact === 'disabled' || snapshot?.contact === 'never_seen') {
    panel.append(workspaceLink('environments',id === '@local' ? '查看环境目录，接入远程节点' : '返回环境目录','workspace-link'));
  }
}

export function assertNodeManagementReady(id) {
  const issue = nodeManagementIssue(id);
  if (issue) throw new Error([issue.title,issue.reason,issue.next].filter(Boolean).join('。'));
}

export function renderSidebarNode() {
  if (!nodeRoute) return;
  const select = element('resource-node');
  const names = new Map();
  for (const item of select.options) {
    const name = workspaceNodeName(item.value);
    names.set(name,(names.get(name) || 0) + 1);
  }
  for (const item of select.options) {
    const name = workspaceNodeName(item.value);
    const identity = item.value === '@local' ? '本机' : item.value;
    item.textContent = names.get(name) > 1 && name !== identity ? name + ' · ' + identity : name;
  }
  const id = nodeRoute.id;
  const name = workspaceNodeName(id);
  const icon = element('sidebar-node-icon');
  if (icon.dataset.node !== id) {
    icon.dataset.node = id;
    icon.replaceChildren(uiIcon(id === '@local' ? 'server' : 'remote'));
  }
  const reportedContact = snapshotNode === id ? nodeSnapshot?.contact : undefined;
  const contact = Object.hasOwn(nodeContactLabels,reportedContact) ? reportedContact : nodeContact(id);
  const canSwitch = select.options.length > 1;
  element('sidebar-node-label').textContent = name;
  show('sidebar-node-label', !canSwitch);
  show('resource-node', canSwitch);
  const status = element('sidebar-node-status');
  status.dataset.contact = contact || '';
  status.textContent = nodeContactLabels[contact] || '未采集状态';
  const pill = element('node-contact');
  pill.textContent = nodeContactLabels[contact] || '';
  pill.classList.toggle('good', contact === 'online');
  pill.classList.toggle('bad', contact === 'offline');
  show('node-contact', nodeRoute.category === 'overview' && !!contact);
}

export function resetNodeWorkspace() {
  nodeDirectoryRevision++;
  pendingNodeDeletions.clear();
  overviewTotals = undefined;
  containerInlineOperation = undefined;
  nodeResourceChanges.clear();
  containerSelection.clear();
  containerSelectionNode = '';
  clearContainerConfiguration();
  clearImageDetailEditor();
  stopContainerLogs();
  nodeRoute = snapshotNode = undefined;
  setNodeSnapshot(undefined);
  directoryHealth.clear();
  nodeDisplayNames.clear();
  nodeNameRevision++;
  const select = element('resource-node');
  select.replaceChildren();
  option(select,'@local',workspaceNodeName('@local'));
  element('node-search').value = '';
  element('node-contact-filter').value = '';
  element('node-list').replaceChildren();
  show('node-resource-context', false);
}

export function selectWorkspaceNode(id) {
  const select = element('resource-node');
  if (![...select.options].some(option => option.value === id)) {
    option(select, id, workspaceNodeName(id));
    select.lastElementChild.dataset.direct = 'true';
  }
  select.value = id;
  renderSidebarNode();
}

export function renderNodeNavigation(active, route, interactive = false) {
  if (!active || route?.category !== 'images' || nodeRoute?.id !== route.id || nodeRoute?.resource !== route.resource) clearImageDetailEditor();
  nodeRoute = active && route ? route : undefined;
  if (!isContainerList(nodeRoute,containerInlineOperation?.node)) {
    containerInlineOperation = undefined;
    show('container-inline-operation',false);
  }
  element('resource-action-feedback').hidden = true;
  if (containerConfiguration && (nodeRoute?.mode || nodeRoute?.logs || nodeRoute?.monitor || nodeRoute?.id !== containerConfiguration.node.id ||
      nodeRoute?.container !== containerConfiguration.target)) clearContainerConfiguration();
  const logs = !!nodeRoute?.logs;
  if (!logs) stopContainerLogs();
  const term = !!nodeRoute?.term;
  if (!term) stopContainerTerminal();
  const containerDetail = !!nodeRoute?.container && !logs && !term && !nodeRoute.monitor && !nodeRoute.mode;
  show('node-context', !containerDetail);
  show('reload-container-detail', containerDetail);
  show('node-resources', !logs && !term);
  show('container-logs-page', logs);
  show('container-terminal-page', term);
  showNodeDisplayNameEditor(false);
  element('node-display-name-status').textContent = '';
  show('node-resource-context', !!nodeRoute);
  if (!active) return;
  show('node-directory', !nodeRoute);
  show('environment-actions', !nodeRoute);
  show('node-workspace', !!nodeRoute);
  const label = nodeCategories.find(([key]) => key === nodeRoute?.category)?.[1];
  const detail = nodeRoute?.container || nodeRoute?.resource || nodeRoute?.operation;
  renderNodeHeading();
  element('environments-title-icon').replaceChildren(uiIcon(term ? 'terminal' : logs ? 'logs' : nodeCategoryIcons[nodeRoute?.category] || 'server'));
  element('environments-title').textContent = term ? '容器终端' : logs ? '容器日志' : detail ? label + '详情' : label || '环境目录';
  if (!nodeRoute) {
    renderNodeDirectory();
    return;
  }

  const links = nodeCategories.map(([key,label]) => {
    const link = workspaceLink(nodePath(nodeRoute.id,key), label);
    link.prepend(uiIcon(nodeCategoryIcons[key]));
    link.dataset.category = key;
    if (nodeRoute.category === key) link.setAttribute('aria-current', 'page');
    return link;
  });
  element('node-resource-nav').replaceChildren(...links);
  selectWorkspaceNode(nodeRoute.id);
  if (snapshotNode !== nodeRoute.id) {
    nodeResourceChanges.clear();
    snapshotNode = nodeRoute.id;
    setNodeSnapshot(undefined);
    element('container-search').value = '';
    element('container-state-filter').value = '';
    element('node-workspace').classList.remove('node-stale');
    element('resource-status').textContent = '';
  }
  renderWorkspaceBody();
  if (interactive && !nodeSnapshot) void trackPageRead(() => loadNodeResources());
  else void loadNodeResources();
}

function resourceSampleTime(view,section) {
  if (section === 'compose_projects') {
    // Docker's container inventory produces the Compose project list.
    return Date.parse(view?.section_times ? view.section_times.containers : view?.generated_at);
  }
  return Date.parse(view?.section_times?.[section]);
}

export function applyNodeResourceChanges(data,nodeID) {
  let dropped = false;
  for (const [key,change] of nodeResourceChanges) {
    // Both timestamps come from the node; control-plane receipt time may
    // advance when an earlier sample finally arrives.
    const sample = resourceSampleTime(data.overview,change.section);
    const imageRemovalConfirmed = change.awaitingImageInventory && data.overview?.available && !data.stale &&
      !data.overview.truncated && (data.overview.image_count === 0 || Array.isArray(data.overview.images)) &&
      !data.overview.images?.some(image => (image.full_id || image.id) === change.id);
    if (change.node !== nodeID || change.generation !== data.generation ||
        sample > change.observedAt || imageRemovalConfirmed || Date.now() >= change.until) {
      nodeResourceChanges.delete(key);
      dropped = true;
      continue;
    }
    applyNodeResourceChange(data.overview,change.section,change.id,change.update,change.insert);
  }
  return dropped;
}

export function rememberNodeResourceChange(node,section,id,update,observedAt,insert = false) {
  if (nodeRoute?.id !== node.id || nodeSnapshot?.generation !== node.generation || !nodeSnapshot?.overview) return;
  const sample = resourceSampleTime(nodeSnapshot.overview,section);
  const projectRemoval = section === 'compose_projects' && update === null;
  let observed = Date.parse(observedAt);
  // Older agents omit the completion timestamp. Keep their confirmed removal
  // over the current snapshot until the node supplies a newer container list.
  if (projectRemoval && !Number.isFinite(observed)) observed = Number.isFinite(sample) ? sample : 0;
  // Success without a node timestamp can still remove the row. Wait for
  // inventory confirmation without comparing clocks across machines.
  const awaitingImageInventory = section === 'images' && update === null && !Number.isFinite(observed);
  if (!awaitingImageInventory &&
      (!Number.isFinite(observed) || sample > observed)) return;
  const key = section + '/' + id;
  const previous = nodeResourceChanges.get(key);
  if (previous?.node === node.id && previous.generation === node.generation && previous.observedAt > observed) return;
  nodeResourceChanges.set(key,{node:node.id,generation:node.generation,section,id,update,insert,
    observedAt:observed,awaitingImageInventory,until:projectRemoval ? Infinity : Date.now()+60000});
  applyNodeResourceChange(nodeSnapshot.overview,section,id,update,insert);
  return true;
}

export function rememberContainerLifecycle(node,target,job) {
  const observed = job.result?.container;
  if (job.state !== 'succeeded' || observed?.full_id !== target || !observed.state) return;
  const {state,status,health,state_observed_at} = observed;
  return rememberNodeResourceChange(node,'containers',target,{state,status,health},state_observed_at);
}

const resourceCounts = {containers:'container_count',images:'image_count',networks:'network_count',volumes:'volume_count'};

function applyNodeResourceChange(view,section,id,update,insert) {
  if (!view) return;
  const items = view[section] || [];
  const index = items.findIndex(item => (section === 'volumes' || section === 'compose_projects' ? item.name : item.full_id || item.id) === id);
  const count = resourceCounts[section];
  if (index < 0) {
    if (insert) {
      view[section] = [update,...items];
      // An incomplete inventory may have counted the resource already.
      if (view[count] === items.length) {
        view[count]++;
        if (section === 'containers') {
          const counter = update.state === 'running' ? 'running_count' : 'stopped_count';
          if (Number.isFinite(view[counter])) view[counter]++;
        }
      }
    }
    return;
  }
  const previous = items[index];
  if (update === null) {
    view[section] = items.filter((_,position) => position !== index);
    if (Number.isFinite(view[count])) view[count] = Math.max(0,view[count] - 1);
  } else if (!insert) {
    view[section] = items.map((item,position) => position === index ? {...item,...update} : item);
  }
  if (section === 'containers' && (update === null || update.state) && Number.isFinite(view.running_count)) {
    const delta = Number(update?.state === 'running') - Number(previous.state === 'running');
    view.running_count = Math.max(0,view.running_count + delta);
    if (Number.isFinite(view.stopped_count)) view.stopped_count = Math.max(0,view.stopped_count + (update === null ? -1 : 0) - delta);
  }
}

export async function loadDirectoryHealth(after, version) {
  const revision = nodeDirectoryRevision;
  try {
    const result = await request('/api/v1/monitor/nodes' + (after ? '?after=' + enc(after) : ''));
    if (version !== accountEpoch || revision !== nodeDirectoryRevision) return;
    for (const item of result.items || []) directoryHealth.set(item.agent,item);
    renderNodeDirectory();
    renderSidebarNode();
  } catch {
    // The directory still lists registered nodes if telemetry is unavailable.
  }
}

// The overview endpoint aggregates fleet-wide numbers the directory cannot
// derive from its paginated client-side view.
export async function loadOverviewTotals() {
  if (!platformAdmin) return;
  const version = accountEpoch;
  const revision = nodeDirectoryRevision;
  let result;
  try {
    result = await request('/api/v1/overview');
  } catch {
    // Fall back to counts from the directory.
  }
  if (version !== accountEpoch || revision !== nodeDirectoryRevision) return;
  overviewTotals = result;
  renderNodeDirectory();
}

function overviewStatCells(view) {
  const nodes = view.nodes;
  const nodeNote = !(view.node_state_available ?? view.enabled) ? '节点状态不可用' :
    ['在线 ' + nodes.online,nodes.offline ? '离线 ' + nodes.offline : '',nodes.never_seen ? '未连接 ' + nodes.never_seen : '',nodes.disabled ? '不可用 ' + nodes.disabled : '']
      .filter(Boolean).join(' · ') || '暂无节点';
  const deployments = view.deployments;
  const deploymentNote = ['24 小时成功 ' + deployments.succeeded_window,'失败 ' + deployments.failed_window].join(' · ');
  const alerts = view.alerts;
  const alertNote = view.enabled ? '24 小时触发 ' + alerts.fired_window : '指标存储不可用';
  return [
    ['节点',nodes.total,'server',nodes.offline ? 'offline' : nodes.online ? 'online' : '',nodeNote],
    ['部署',deployments.active,'deploy',deployments.failed_window ? 'offline' : '',deploymentNote],
    ['告警',view.enabled ? alerts.active : '—','pulse',view.enabled && alerts.active ? 'offline' : '',alertNote]
  ];
}

// Resource IDs are unwieldy full hex strings; pair the code with a copy
// button so docker commands do not require manual selection.
function copyableID(value) {
  const cell = workspaceText('span','','id-copy');
  cell.append(workspaceText('code',value));
  cell.append(managementButton('复制',async () => {
    try {
      if (navigator.clipboard) {
        await navigator.clipboard.writeText(value);
        notice('已复制 ID。');
        return;
      }
    } catch {}
    notice('无法访问剪贴板，请手动复制。','error');
  }));
  return cell;
}

export function renderNodeDirectory() {
  if (!platformAdmin) return;
  const list = element('node-list');
  const query = element('node-search').value.trim().toLowerCase();
  const nodes = [...element('resource-node').options].filter(option => !option.dataset.direct);
  const filter = element('node-contact-filter').value;
  const showStart = !localExecution && !query && !filter && nodes.length === 1 && nodes[0].value === '@local';
  show('directory-start',showStart);
  if (showStart) {
    element('directory-start-title').textContent = '本机 Docker 当前不可用';
    element('directory-start-hint').textContent = '打开本机节点查看检测结果，或创建 Agent Token 接入其他主机。';
  }
  const totals = element('directory-totals');
  // With the overview endpoint the strip becomes the fleet dashboard; the
  // paginated client-side counts remain as the fallback while it loads.
  const stats = overviewTotals ? overviewStatCells(overviewTotals) : [
    ['已列出节点',nodes.length,'server','total',''],
    ['在线',nodes.filter(option => directoryHealth.get(option.value)?.contact === 'online').length || (directoryHealth.size ? 0 : '—'),'online','online',''],
    ['离线',nodes.filter(option => directoryHealth.get(option.value)?.contact === 'offline').length || (directoryHealth.size ? 0 : '—'),'offline','offline','']];
  totals.replaceChildren(...stats.map(([label,count,icon,state,note]) => {
    const cell = workspaceText('div','','directory-stat');
    cell.dataset.signal = state;
    const text = workspaceText('div','','directory-stat-info');
    text.append(workspaceText('span',label),workspaceText('strong',String(count)));
    if (note) text.append(workspaceText('small',note));
    cell.append(workspaceGlyph(icon),text);
    return cell;
  }));
  const matches = nodes.filter(option => {
    const contact = nodeContact(option.value);
    const matchesContact = !filter || (filter === 'unavailable' ? !['online','offline'].includes(contact) : contact === filter);
    return matchesContact && (workspaceNodeName(option.value).toLowerCase().includes(query) || option.value.toLowerCase().includes(query));
  });
  list.replaceChildren();
  if (!matches.length) {
    const empty = workspaceText('div','','work-empty');
    empty.append(workspaceText('p',query || filter ? '没有匹配的节点。' : '当前没有可用的节点。'));
    if (query || filter) empty.append(managementButton('清除筛选',() => {
      element('node-search').value = '';
      element('node-contact-filter').value = '';
      renderNodeDirectory();
      element('node-search').focus();
    }));
    list.append(empty);
    return;
  }
  const entries = workspaceText('ul','','environment-list');
  for (const option of matches) {
    const id = option.value;
    const health = directoryHealth.get(id);
    const local = id === '@local';
    const contact = nodeContact(id);
    const label = nodeContactLabels[contact] || '未采集状态';
    const row = workspaceText('li','','environment-row');
    row.dataset.contact = contact || '';
    const identity = workspaceText('div','','environment-identity');
    const name = workspaceText('div','','environment-name');
    const labelName = workspaceNodeName(id);
    const kind = local ? '本机 Docker' : labelName === id ? '远程 Agent' : '远程 Agent · ' + id;
    name.append(workspaceLink(nodePath(id),labelName,'workspace-link environment-link'));
    if (labelName !== kind) name.append(workspaceText('small',kind));
    identity.append(workspaceGlyph(local ? 'server' : 'remote'),name);
    if (local && !localExecution) identity.title = element('local-status').textContent;
    const status = workspaceText('div','','environment-state');
    status.append(statusPill(label,contact === 'online' ? 'good' : contact === 'offline' ? 'bad' : ''));
    const enter = workspaceText('span','','directory-enter');
    enter.setAttribute('aria-hidden','true');
    enter.append(uiIcon('arrow'));
    row.append(identity,status,
      directoryMetric('最近联系',health?.last_contact ? dateTime(health.last_contact) : '—'),
      directoryMemory(health),
      local ? enter : nodeDeleteButton(id,Number(option.dataset.generation)));
    entries.append(row);
  }
  list.append(entries);
}

function nodeDeleteButton(id,generation) {
  const name = workspaceNodeName(id);
  const identity = name === id ? id : name + '（' + id + '）';
  const button = managementButton('删除',() => confirmNodeDeletion(id,generation,identity),true);
  button.dataset.deleteNode = id;
  button.disabled = !generation || pendingNodeDeletions.has(id);
  return actionIcon(button,'remove','删除节点 ' + identity);
}

function confirmNodeDeletion(id,generation,identity) {
  if (!platformAdmin || id === '@local' || !generation || pendingNodeDeletions.has(id)) return;
  const dialog = element('management-dialog');
  const version = accountEpoch;
  const cancel = managementButton('取消',() => dialog.close());
  const confirm = managementButton('确认删除',async () => {
    if (pendingNodeDeletions.has(id) || version !== accountEpoch) return;
    pendingNodeDeletions.add(id);
    renderNodeDirectory();
    cancel.disabled = true;
    const current = () => dialog.open && confirm.isConnected;
    element('management-status').textContent = '正在删除节点…';
    try {
      await request('/api/v1/agents/' + enc(id),{generation},undefined,undefined,'DELETE');
    } catch (error) {
      if (version === accountEpoch) {
        const guidance = error.code === 'target_busy' ? '请等待正在执行或排队的任务结束后重试。' :
          error.code === 'conflict' ? '请关闭此窗口并刷新节点列表，核对当前注册信息。' : '';
        const message = '删除节点失败：' + error.message + (guidance ? ' ' + guidance : '');
        if (current()) element('management-status').textContent = message;
        else notice(message,'error');
      }
      return;
    } finally {
      if (version === accountEpoch) {
        pendingNodeDeletions.delete(id);
        cancel.disabled = false;
        renderNodeDirectory();
      }
    }
    if (version !== accountEpoch) return;
    if (current()) dialog.close();
    nodeDirectoryRevision++;
    nodeNameRevision++;
    nodeDisplayNames.delete(id);
    directoryHealth.delete(id);
    overviewTotals = undefined;
    if (snapshotNode === id) {
      snapshotNode = nodeSnapshot = undefined;
      nodeResourceChanges.clear();
      containerSelection.clear();
    }
    if (nodeRoute?.id === id) goToConsoleView('environments');
    refreshAfterNodeDeletion(id);
    notice('已删除节点 ' + identity + '。');
  },true);
  managementDialog('删除远程节点',
    workspaceText('p','删除 ' + identity + ' 后，Agent Token 将失效，部署目标将解除绑定，节点的 Webhook 和镜像仓库授权将撤销。'),
    managementActions(cancel,confirm));
}

function directoryMemory(health) {
  const total = health?.sample?.memory_bytes;
  const available = health?.sample?.memory_available_bytes;
  if (health?.metrics !== 'current' || !Number.isFinite(total) || total <= 0 ||
      !Number.isFinite(available) || available < 0 || available > total) return directoryMetric('已用内存','—');
  const used = total - available;
  const metric = directoryMetric('已用内存',formatBytes(used) + ' / ' + formatBytes(total));
  const usage = document.createElement('progress');
  usage.max = total;
  usage.value = used;
  usage.setAttribute('aria-label','内存使用率');
  metric.append(usage);
  return metric;
}

function directoryMetric(label,value) {
  const metric = workspaceText('div','','environment-metric');
  metric.append(workspaceText('span',label),workspaceText('strong',value));
  return metric;
}

export function renderWorkspaceBody() {
  if (!nodeRoute) return;
  if (nodeRoute.monitor) {
    renderNodeHeading();
    return;
  }
  if (nodeRoute.mode) {
    initializeNodeMaintenance(nodeRoute,nodeSnapshot);
    return;
  }
  const view = nodeSnapshot?.overview;
  const {id,category,container,resource} = nodeRoute;
  if (containerSelectionNode !== id) {
    containerSelection.clear();
    containerSelectionNode = id;
  }
  const home = category === 'overview';
  const manageable = !!view?.available && !nodeManagementIssue(id);
  renderNodeRecovery(id,nodeSnapshot);
  element('node-workspace').classList.toggle('node-workspace-compact', !home);
  renderNodeHeading();
  show('edit-node-display-name', home);
  element('node-meta').textContent = [
    view?.host?.hostname && '主机名 ' + view.host.hostname, view?.host?.os, view?.engine?.version && 'Docker ' + view.engine.version,
    view?.host?.cpu_count && view.host.cpu_count + ' 核', view?.host?.memory_total_bytes && formatBytes(view.host.memory_total_bytes)
  ].filter(Boolean).join(' · ');
  if (nodeRoute.logs) {
    const item = view?.containers?.find(entry => (entry.full_id || entry.id) === container);
    renderContainerLogs(nodeById(id),container,item?.name || container.slice(0,12));
    return;
  }
  if (nodeRoute.term) {
    const item = view?.containers?.find(entry => (entry.full_id || entry.id) === container);
    renderContainerTerminal(nodeById(id),container,item?.name || container.slice(0,12));
    return;
  }
  show('resource-panel-toolbar', !home && !container && !resource && category !== 'operations');
  show('container-filters', category === 'containers' && !container);
  show('container-list-count', category === 'containers' && !container);
  show('create-volume', manageable && category === 'volumes');
  show('create-network', manageable && category === 'networks');
  show('create-container', manageable && category === 'containers' && !container);
  if (category === 'containers' && !container) element('create-container').href = routeHref(nodePath(id,'containers','new'));
  show('create-compose', manageable && category === 'compose' && !resource);
  if (category === 'compose' && !resource) element('create-compose').href = routeHref(nodePath(id,'compose','new','create'));
  show('pull-image', manageable && category === 'images');
  show('import-image', manageable && category === 'images');
  element('resource-title').textContent = category === 'containers' ? '容器列表' : nodeCategories.find(([key]) => key === category)[1];
  element('resource-title-icon').replaceChildren(uiIcon(nodeCategoryIcons[category]));
  const body = element('resource-view');
  if (category === 'operations') {
    renderNodeOperationHistory(nodeRoute,body);
    return;
  }
  const focused = body.contains(document.activeElement) ? document.activeElement.dataset.resourceFocus : '';
  const previousActions = body.querySelector('details.container-extra-actions[open]');
  const previousTable = body.querySelector('.container-list');
  const tableScroll = previousTable?.dataset.node === id ? previousTable.scrollLeft : 0;
  body.replaceChildren();
  if (!view) {
    body.append(workspaceText('p',nodeSnapshot ? '此节点暂无资源快照。' : '正在读取节点资源…','work-empty'));
  } else if (home) {
    body.append(buildNodeOverview(id,view));
  } else if (category === 'containers') {
    body.append(container ? buildContainerDetails(id,container,view) : buildContainerList(id,view));
    const table = body.querySelector('.container-list');
    if (table) table.scrollLeft = tableScroll;
    if (previousActions && previousActions.dataset.container === container && previousActions.dataset.node === id) {
      const extraActions = body.querySelector('details.container-extra-actions');
      if (extraActions) extraActions.open = true;
    }
  } else if (Object.hasOwn(resourceCatalog,category)) {
    body.append(resource ? buildResourceDetail(id,category,resource,view) : buildResourceList(id,category,view));
  } else {
    const groups = resourceGroups(view);
    for (const [title,headers,rows,key] of groups) {
      const panel = workspaceText('section','','resource-section');
      const sampled = view.section_times?.[key];
      const heading = workspaceText('h3',title);
      panel.append(heading);
      if (sampled && Date.now() - new Date(sampled).getTime() > 120000) {
        panel.append(workspaceText('p','上次采集 ' + dateTime(sampled),'work-note resource-detail-note'));
      }
      panel.append(rows.length ? resourceTable(headers,rows) : workspaceText('p','暂无记录。','work-empty'));
      body.append(panel);
    }
    if (!groups.length) body.append(workspaceText('p','暂无此类资源快照。','work-empty'));
  }
  if (focused) [...body.querySelectorAll('[data-resource-focus]')].find(item => item.dataset.resourceFocus === focused)?.focus({preventScroll:true});
}

export function showNodeDisplayNameEditor(open, restoreFocus = false) {
  const form = element('node-display-name-form');
  const toggle = element('edit-node-display-name');
  form.hidden = !open;
  toggle.setAttribute('aria-expanded', String(open));
  if (open) {
    form.elements.display_name.value = nodeDisplayNames.get(nodeRoute.id) || '';
    element('node-display-name-status').textContent = '';
    form.elements.display_name.focus();
  } else if (restoreFocus) {
    toggle.focus();
  }
}

export async function saveNodeDisplayName() {
  const route = nodeRoute;
  if (!route || route.category !== 'overview') return;
  const version = accountEpoch;
  const input = element('node-display-name-form').elements.display_name;
  const name = input.value.trim();
  if ([...name].length > 64) {
    element('node-display-name-status').textContent = '显示名最多 64 个字符。';
    input.focus();
    return;
  }
  const status = element('node-display-name-status');
  status.textContent = '正在保存…';
  try {
    await request('/api/v1/nodes/' + enc(route.id) + '/display-name', {display_name:name});
  } catch (error) {
    if (nodeRoute === route && version === accountEpoch) status.textContent = error.message;
    return;
  }
  if (version !== accountEpoch) return;
  nodeNameRevision++;
  rememberNodeDisplayName(route.id,name);
  renderNodeHeading();
  renderSidebarNode();
  renderNodeDirectory();
  if (nodeRoute !== route) return;
  showNodeDisplayNameEditor(false,true);
  status.textContent = '显示名已保存。';
}

function buildNodeOverview(id,view) {
  const layout = workspaceText('div','','node-overview');
  const inventory = workspaceText('section','','node-inventory');
  const tiles = workspaceText('div','','inventory-tiles');
  inventory.append(workspaceText('h2','资源概览'),tiles);
  const sections = [
    ['containers',view.container_count, view.available ? (view.running_count ?? 0) + ' 运行中' : ''],
    ['compose',view.available ? view.compose_projects?.length ?? 0 : undefined],
    ['images',view.image_count], ['networks',view.network_count], ['volumes',view.volume_count]
  ];
  for (const [key,count,note] of sections) {
    const tile = workspaceLink(nodePath(id,key),'','inventory-tile');
    tile.dataset.resource = key;
    const arrow = uiIcon('northeast');
    arrow.classList.add('inventory-arrow');
    const figures = workspaceText('div','','inventory-figures');
    figures.append(workspaceText('strong',view.available && count != null ? String(count) : '—'),
      workspaceText('span',nodeCategories.find(([name]) => name === key)[1],'inventory-label'));
    if (note) figures.append(workspaceText('small',note));
    tile.append(workspaceGlyph(nodeCategoryIcons[key]),figures,arrow);
    tiles.append(tile);
  }
  const host = view.host;
  const snapshot = workspaceText('section','','node-snapshot-summary');
  const header = workspaceText('div','','host-indicator-heading');
  header.append(workspaceGlyph('pulse'),workspaceText('h2','主机指标'));
  const memoryUsed = host?.memory_total_bytes > 0 && host.memory_available_bytes != null ?
    Math.max(0,host.memory_total_bytes - host.memory_available_bytes) : null;
  const memoryPercent = memoryUsed == null ? undefined : memoryUsed / host.memory_total_bytes * 100;
  const hostLink = workspaceLink(nodePath(id,'host'),'主机与 Docker','workspace-link host-overview-link');
  hostLink.append(uiIcon('arrow'));
  const monitor = nodeMonitorLink(id,'','查看监控');
  monitor.className = 'workspace-link host-overview-link';
  monitor.append(uiIcon('arrow'));
  const links = workspaceText('div','','host-overview-links');
  links.append(monitor,hostLink);
  snapshot.append(header,
    hostIndicator('CPU 使用率',host?.cpu_percent,percent(host?.cpu_percent)),
    hostIndicator('内存使用率',memoryPercent,memoryUsed == null ? '—' : bytePair(memoryUsed,host.memory_total_bytes)),
    snapshotFacts([['操作系统',host?.os], ['Docker 版本',view.engine?.version]]),links);
  layout.append(inventory,snapshot);
  return layout;
}

function hostIndicator(label,value,description) {
  const section = workspaceText('div','','host-indicator');
  const caption = workspaceText('div','','host-indicator-label');
  caption.append(workspaceText('span',label),workspaceText('strong',description));
  section.append(caption);
  if (Number.isFinite(value)) {
    const meter = document.createElement('progress');
    meter.max = 100;
    meter.value = Math.max(0,Math.min(100,value));
    meter.setAttribute('aria-label',label);
    section.append(meter);
  }
  return section;
}

function containerState(item) {
  const labels = {running:'运行中',exited:'已停止',created:'已创建',restarting:'重启中',paused:'已暂停',dead:'异常退出'};
  const unhealthy = item.state === 'running' && item.health === 'unhealthy';
  const label = unhealthy ? '不健康' : labels[item.state] || item.state || '未知';
  return statusPill(label,unhealthy || item.state === 'dead' ? 'bad' : item.state === 'running' ? 'good' : item.state === 'restarting' ? 'warn' : '');
}

function buildContainerList(id,view) {
  const manageable = !nodeManagementIssue(id);
  const query = element('container-search').value.trim().toLowerCase();
  const state = element('container-state-filter').value;
  const items = (view.containers || []).filter(item =>
    (!state || item.state === state) && [item.name,item.id,item.image,item.compose_project,item.compose_service]
      .filter(Boolean).join(' ').toLowerCase().includes(query));
  const total = (view.containers || []).length;
  element('container-list-count').textContent = query || state ? `${items.length} / ${total}` : `${total} 个`;
  if (!items.length) {
    if (query || state) {
      const empty = workspaceText('div','','work-empty container-empty');
      empty.append(workspaceText('p','没有匹配的容器。'),
        managementButton('清除筛选',() => {
          element('container-search').value = '';
          element('container-state-filter').value = '';
          renderWorkspaceBody();
          element('container-search').focus();
        }));
      return empty;
    }
    return workspaceText('p',view.available ? '此节点暂无容器。' : 'Docker 当前不可用。','work-empty');
  }
  const rows = items.map(item => {
    const target = item.full_id || item.id;
    const valid = /^[0-9a-f]{64}$/.test(target);
    const name = item.name || item.id || '容器';
    const nameCell = valid ? workspaceLink(nodePath(id,'containers',target),name,'workspace-link') : workspaceText('span',name);
    nameCell.classList.add('container-name');
    nameCell.title = name;
    if (valid) nameCell.dataset.resourceFocus = target + ':details';
    const nameGroup = workspaceText('div','');
    nameGroup.append(nameCell);
    const publishedPorts = publishedPortLabels(item.ports || []);
    const ports = workspaceText('div',publishedPorts.length ? '' : '—','container-ports');
    for (const label of publishedPorts) ports.append(workspaceText('span',label));
    if (publishedPorts.length) {
      const mobilePorts = ports.cloneNode(true);
      mobilePorts.className = 'container-ports-mobile';
      nameGroup.append(mobilePorts);
    }
    const image = containerImage(item,view);
    const imageLabel = image ? resourceLink(id,'images',image,item.image || undefined) :
      workspaceText('span',item.image || '—');
    imageLabel.classList.add('container-image');
    imageLabel.title = item.image || '';
    const project = (view.compose_projects || []).find(project => project.name === item.compose_project);
    const projectCell = project ? resourceLink(id,'compose',project) : item.compose_project;
    const log = valid && manageable ? containerLogsLink(id,target,true) : null;
    if (log) log.dataset.resourceFocus = target + ':logs';
    const term = valid && manageable && item.state === 'running' ? containerTerminalLink(id,target,true) : null;
    if (term) term.dataset.resourceFocus = target + ':term';
    const monitor = valid && actionIcon(nodeMonitorLink(id,target),'pulse');
    if (monitor) monitor.dataset.resourceFocus = target + ':monitor';
    const lifecycle = valid && manageable ? containerListLifecycle(nodeById(id),item,target,name) : null;
    const remove = valid && manageable && containerRemoveButton(nodeById(id),item,view);
    if (remove) {
      const hint = remove.title;
      actionIcon(remove,'remove','删除容器');
      remove.title = hint;
      remove.dataset.resourceFocus = target + ':remove';
      remove.classList.add('container-list-remove');
    }
    return [valid && manageable ? bulkSelectBox(item) : '',
      nameGroup,containerState(item),imageLabel,projectCell,ports,percent(item.stats?.cpu_percent),formatBytes(item.stats?.memory_bytes),
      monitor ? managementActions(lifecycle,log,term,monitor,remove) : '—'];
  });
  const table = resourceTable(['选择','名称','状态','镜像','Compose 项目','端口映射','CPU','内存','操作'],rows);
  table.classList.add('container-list');
  table.dataset.node = id;
  const selectable = manageable ? items.filter(item => /^[0-9a-f]{64}$/.test(item.full_id || item.id)) : [];
  const page = document.createDocumentFragment();
  const bar = selectable.length ? buildContainerBulkBar(id,selectable) : null;
  if (bar) page.append(bar);
  page.append(table);
  if (bar) syncBulkBar(bar);
  return page;
}

function isContainerList(route,id) {
  return route?.id === id && route?.category === 'containers' && !route.container && !route.mode;
}

function containerListLifecycle(node,item,target,name) {
  const stopping = ['running','restarting','paused'].includes(item.state);
  if (!stopping && !['created','exited','dead'].includes(item.state)) return null;
  const verb = stopping ? '停止' : '启动';
  const command = {action:stopping ? 'container.stop' : 'container.start',target};
  const button = stopping ? maintenanceOperationButton(verb,node,name,command,'停止可能暂时中断容器提供的服务。',true) :
    managementButton(verb,() => startContainerFromList(node,name,command),false,element('container-inline-status'));
  if (!stopping) button.disabled = !!containerInlineOperation?.running;
  button.dataset.resourceFocus = target + ':lifecycle';
  return actionIcon(button,stopping ? 'stop' : 'play',verb + ' ' + name);
}

async function startContainerFromList(node,name,command) {
  if (containerInlineOperation?.running) return;
  const operation = {node:node.id,running:true};
  containerInlineOperation = operation;
  const status = element('container-inline-status');
  const results = element('container-inline-links');
  const visible = () => containerInlineOperation === operation && isContainerList(nodeRoute,node.id);
  const surface = {status,results,visible,inline:true};
  results.replaceChildren();
  element('resource-action-feedback').hidden = true;
  show('container-inline-operation',true);
  renderWorkspaceBody();
  try {
    const state = await performOperation(node,command,undefined,surface);
    if (visible()) status.textContent = name + ' · ' + (state === 'succeeded' ? '已启动' : status.textContent);
  } catch (error) {
    if (visible()) status.textContent = name + ' · ' + (error.message || '启动失败。');
  } finally {
    operation.running = false;
    if (visible()) {
      renderWorkspaceBody();
      const focus = element('resource-view').querySelector('[data-resource-focus="' + command.target + ':lifecycle"]');
      (focus || element('container-search')).focus({preventScroll:true});
    }
  }
}

function bulkSelectBox(item) {
  const target = item.full_id || item.id;
  const box = document.createElement('input');
  box.type = 'checkbox';
  box.dataset.containerSelect = target;
  box.dataset.resourceFocus = target + ':select';
  box.checked = containerSelection.has(target);
  box.addEventListener('change',() => {
    if (box.checked) containerSelection.add(target); else containerSelection.delete(target);
    syncBulkBar();
  });
  box.setAttribute('aria-label','选择容器 ' + (item.name || target.slice(0,12)));
  return box;
}

function buildContainerBulkBar(id,selectable) {
  const bar = workspaceText('div','','container-bulk-bar');
  const toggle = document.createElement('input');
  toggle.type = 'checkbox';
  toggle.setAttribute('aria-label','全选当前列表中的容器');
  toggle.addEventListener('change',() => {
    for (const item of selectable) {
      const target = item.full_id || item.id;
      if (toggle.checked) containerSelection.add(target); else containerSelection.delete(target);
    }
    for (const box of element('resource-view').querySelectorAll('[data-container-select]')) box.checked = toggle.checked;
    syncBulkBar();
  });
  const count = workspaceText('span','','container-bulk-count');
  const barStatus = workspaceText('span','','work-note container-bulk-status');
  barStatus.setAttribute('role','status');
  bar.append(toggle,count,
    managementButton('批量启动所选',() => confirmBulkContainerLifecycle(id,'start',selectable,barStatus)),
    managementButton('批量停止所选',() => confirmBulkContainerLifecycle(id,'stop',selectable,barStatus),true),
    barStatus);
  return bar;
}

function syncBulkBar(bar = element('resource-view').querySelector('.container-bulk-bar')) {
  // The bar may still live in a detached fragment during initial render; its
  // parent (fragment or container) always also holds the container table.
  if (!bar || !bar.parentNode) return;
  const boxes = [...bar.parentNode.querySelectorAll('[data-container-select]')];
  const picked = boxes.filter(box => box.checked).length;
  bar.querySelector('.container-bulk-count').textContent = `已选 ${picked} / ${boxes.length}`;
  const toggle = bar.querySelector('input[type=checkbox]');
  toggle.checked = boxes.length > 0 && picked === boxes.length;
  toggle.indeterminate = picked > 0 && picked < boxes.length;
  for (const button of bar.querySelectorAll('button')) button.disabled = picked === 0;
}

function confirmBulkContainerLifecycle(id,verb,selectable,barStatus) {
  const node = nodeById(id);
  const label = verb === 'start' ? '启动' : '停止';
  const applicable = verb === 'stop' ? ['running','paused','restarting'] : ['created','exited','dead'];
  const selected = selectable.filter(item => containerSelection.has(item.full_id || item.id));
  const targets = selected.filter(item => applicable.includes(item.state));
  if (!targets.length) {
    barStatus.textContent = '所选容器当前状态不适合批量' + label + '，请调整选择。';
    return;
  }
  if (!node.generation) {
    barStatus.textContent = '无法获取节点代次，请先刷新节点列表。';
    return;
  }
  const list = workspaceText('ul','','batch-operation-list');
  const rows = targets.map(item => {
    const target = item.full_id || item.id;
    const row = workspaceText('li','');
    const name = workspaceText('span',item.name || target.slice(0,12),'batch-operation-name');
    const outcome = workspaceText('span','待提交','batch-operation-outcome');
    row.append(name,outcome);
    list.append(row);
    return {target,name:name.textContent,outcome,state:'waiting'};
  });
  const confirmButton = managementButton('确认批量' + label,async () => {
    confirmButton.hidden = true;
    const surface = dialogOperationSurface();
    const account = accountEpoch;
    const path = nodeOperationPath(node);
    const history = workspaceLink(nodePath(id,'operations'),'查看操作记录','workspace-link');
    history.addEventListener('click',surface.leave);
    surface.results.append(history);
    const update = (row,state,message) => {
      row.state = state;
      row.outcome.dataset.state = state;
      row.outcome.textContent = message || operationStates[state];
    };
    const updateJob = (row,job) => {
      update(row,job.state,[operationStates[job.state],containerOperationMessage(job.error)].filter(Boolean).join(' · '));
      if (account === accountEpoch && rememberContainerLifecycle(node,row.target,job)) renderWorkspaceBody();
    };
    surface.status.textContent = '正在提交批量操作…';
    for (const row of rows) {
      if (account !== accountEpoch) return;
      try {
        const job = await request(path,{action:'container.' + verb,target:row.target,generation:node.generation},
          undefined,AbortSignal.timeout(30000));
        if (account !== accountEpoch) return;
        row.job = job.id;
        updateJob(row,job);
        if (containerSelectionNode === id) {
          containerSelection.delete(row.target);
          for (const box of element('resource-view').querySelectorAll('[data-container-select]')) {
            if (box.dataset.containerSelect === row.target) box.checked = false;
          }
          syncBulkBar();
        }
        if (surface.visible() && /^[a-f0-9]{32}$/.test(job.id)) {
          const record = workspaceLink(nodePath(id,'operations',job.id),'记录','workspace-link');
          record.setAttribute('aria-label','查看 ' + row.name + ' 的操作记录');
          record.addEventListener('click',surface.leave);
          row.outcome.after(record);
        }
      } catch (error) {
        update(row,Number.isInteger(error.status) ? 'failed' : 'uncertain',
          Number.isInteger(error.status) ? '提交失败 · ' + error.message : '提交未确认，请查看操作记录');
      }
    }
    const pending = () => rows.filter(row => row.job && operationPending(row));
    const summary = () => {
      const count = state => rows.filter(row => row.state === state).length;
      return `成功 ${count('succeeded')} · 失败 ${count('failed')} · 待确认 ${count('uncertain')} · 进行中 ${pending().length}`;
    };
    const deadline = Date.now() + 30000;
    const canPoll = () => account === accountEpoch && Date.now() < deadline;
    while (canPoll() && pending().length) {
      if (surface.visible()) surface.status.textContent = summary();
      await new Promise(resolve => setTimeout(resolve,1000));
      const waiting = pending();
      for (let offset = 0; offset < waiting.length && canPoll(); offset += 6) {
        await Promise.allSettled(waiting.slice(offset,offset + 6).map(async row => {
          const job = await request(path + '/' + enc(row.job),undefined,undefined,AbortSignal.timeout(4000));
          if (account === accountEpoch) updateJob(row,job);
        }));
      }
    }
    if (surface.visible()) {
      surface.status.textContent = summary() + (pending().length ? '。其余结果可在操作记录查看。' : '');
    }
    if (account === accountEpoch && nodeRoute?.id === id) {
      if (rows.some(row => row.job)) void refreshAfterNodeMutation(id,['containers']);
    }
  },verb === 'stop');
  managementDialog('批量' + label + ' ' + targets.length + ' 个容器',
    workspaceText('p',verb === 'stop' ? '将向所选运行中的容器提交停止操作，可能暂时中断它们提供的服务。' : '将向所选容器提交启动操作。'),
    workspaceText('p',(selected.length - targets.length ? '另有 ' + (selected.length - targets.length) + ' 个所选容器状态不符将跳过。' : '') +
      '提交后由节点逐个执行，单个失败不影响其余。','work-note'),
    list,managementActions(confirmButton));
}

function buildContainerDetails(id,target,view) {
  const item = (view.containers || []).find(container => (container.full_id || container.id) === target);
  const page = workspaceText('section','','container-workspace');
  const back = workspaceLink(nodePath(id,'containers'),'返回容器列表','workspace-link resource-back');
  if (!item) {
    clearContainerConfiguration();
    page.append(back,workspaceText('p','此容器未出现在当前节点快照中。','work-empty'));
    return page;
  }
  const name = item.name || item.id;
  const title = workspaceText('div','','container-work-heading');
  const heading = workspaceText('h2',name);
  heading.id = 'container-heading';
  const deploymentHistoryLink = !item.compose_project ? workspaceLink(nodePath(id,'containers',target,'deployments'),'部署记录','editor-link') :
    /^moori-[a-f0-9]{32}$/.test(item.compose_project) ?
      workspaceLink(nodePath(id,'compose',item.compose_project,'deployments'),'项目部署记录','editor-link') : null;
  title.append(workspaceGlyph('container'),heading,containerState(item),back);
  if (deploymentHistoryLink) title.insertBefore(deploymentHistoryLink,back);
  const node = nodeById(id);
  const manageable = !nodeManagementIssue(id);
  const lifecycle = {
    running:[['停止','stop'],['重启','restart'],['暂停','pause']],
    paused:[['恢复','resume']],
    created:[['启动','start']],
    exited:[['启动','start']]
  };
  const project = (view.compose_projects || []).find(project => project.name === item.compose_project);
  const editable = !item.compose_project && ['created','running','exited'].includes(item.state);
  const lifecycleActions = managementActions(
    ...(Object.hasOwn(lifecycle,item.state) ? lifecycle[item.state] : [])
      .map(([label,verb]) => {
        const button = maintenanceOperationButton(label,node,name,{action:'container.' + verb,target},
          ['stop','restart','pause'].includes(verb) ? '此操作可能暂时中断容器提供的服务。' : '',verb === 'stop');
        if (verb === 'start' || verb === 'resume') button.classList.add('primary');
        return actionIcon(button,{stop:'stop',restart:'refresh',pause:'pause',start:'play',resume:'play'}[verb],undefined,false);
      })
  );
  const configuration = project ? workspaceLink(nodePath(id,'compose',project.name,'edit'),
    /^moori-[a-f0-9]{32}$/.test(project.name) ? '修改 Compose 配置' : '查看 Compose 源文件','editor-link') :
    item.compose_project ? workspaceLink(nodePath(id,'compose'),'查看 Compose 项目','editor-link') :
    editable ? workspaceLink(nodePath(id,'containers',target,'edit'),'编辑配置','editor-link') : null;
  if (configuration) {
    actionIcon(configuration,'edit',undefined,false);
    if (editable) configuration.classList.add('primary');
  }

  const actionPanel = workspaceText('section','','container-action-panel');
  actionPanel.append(workspaceText('h3','容器操作','container-action-heading'));
  const actionGroups = workspaceText('div','','container-action-groups');
  const addGroup = (panel,label,actions) => {
    if (!actions?.childElementCount) return;
    const group = workspaceText('div','','container-action-group');
    group.setAttribute('role','group');
    group.setAttribute('aria-label',label);
    group.append(workspaceText('span',label,'container-action-label'),actions);
    panel.append(group);
  };
  if (manageable) {
    const redeploy = editable ? actionIcon(workspaceLink(nodePath(id,'containers',target,'redeploy'),'重新部署','editor-link'),'refresh',undefined,false) : null;
    const configureCI = !item.compose_project && managementButton('配置流水线',() => openContainerCISettings(node,name,item.image));
    if (configureCI) configureCI.id = 'configure-container-ci';
    const remove = containerRemoveButton(node,item,view);
    const removal = managementActions(remove);
    if (remove.disabled && remove.title) removal.append(workspaceText('small',remove.title,'container-action-hint'));
    addGroup(actionGroups,'运行',lifecycleActions);
    addGroup(actionGroups,'配置与部署',managementActions(configuration,redeploy));
    actionPanel.append(actionGroups);
    const extra = workspaceText('details','','container-extra-actions');
    extra.dataset.node = id;
    extra.dataset.container = target;
    const trigger = workspaceText('summary','更多操作');
    trigger.dataset.resourceFocus = target + ':extra-actions';
    const extras = workspaceText('div','','container-extra-content');
    addGroup(extras,'自动化',managementActions(configureCI,
      editable && managementButton('配置 Webhook',() => openContainerWebhookSettings(id,name,item.image))));
    addGroup(extras,'删除',removal);
    extra.append(trigger,extras);
    actionPanel.append(extra);
  }
  for (const [index,button] of [...actionPanel.querySelectorAll('a,button')].entries()) button.dataset.resourceFocus = target + ':' + index;
  const [,image,state,compose,ports,,limits,cpu,memory,pids,network,io,disk,sampled] = containerRow(item);
  const linkedImage = containerImage(item,view);
  const summary = workspaceText('div','','container-overview container-metadata');
  summary.append(snapshotFacts([
    ['镜像',linkedImage ? resourceLink(id,'images',linkedImage,image) : image],
    ['Compose 服务',project ? resourceLink(id,'compose',project,compose) : compose],
    ['状态',state],['容器 ID',copyableID(target)]
  ]));
  const overviewPanel = workspaceText('div','','container-tab-panel');
  const samples = containerFactPanel('资源采样',[
    ['CPU',cpu], ['内存',memory], ['进程',pids], ['网络收 / 发',network], ['磁盘读 / 写',io], ['可写层',disk], ['采集时间',sampled]
  ]);
  samples.classList.add('container-samples');
  overviewPanel.append(summary,samples);
  const specPanel = workspaceText('div','','container-tab-panel');
  const config = buildContainerConnections(id,item,view);
  config.prepend(containerFactPanel('运行配置',[['端口',ports],['重启策略与限额',limits]]));
  specPanel.append(config);
  const environmentPanel = workspaceText('div','','container-tab-panel');
  if (!manageable) clearContainerConfiguration();
  let selectedTab = nodeRoute.detailTab || 'summary';
  const bar = workspaceText('div','','container-tabs');
  bar.setAttribute('role','tablist');
  bar.setAttribute('aria-label','容器详情分区');
  const tabEntries = [['summary','概要',overviewPanel],['specs','运行配置',specPanel]]
    .concat(manageable ? [['config','环境与标签',environmentPanel]] : [])
    .map(([key,label,panel]) => {
      const tab = workspaceText('button',label,'container-tab');
      tab.type = 'button';
      tab.id = 'container-tab-' + key;
      tab.dataset.resourceFocus = target + ':tab:' + key;
      tab.setAttribute('role','tab');
      tab.setAttribute('aria-controls','container-panel-' + key);
      panel.tabIndex = 0;
      panel.id = 'container-panel-' + key;
      panel.setAttribute('role','tabpanel');
      panel.setAttribute('aria-labelledby',tab.id);
      tab.addEventListener('click',() => {
        if (selectedTab === key) return;
        selectedTab = key;
        nodeRoute.detailTab = key === 'summary' ? undefined : key;
        history.replaceState(history.state,'',routeHref(nodePath(id,'containers',target,nodeRoute.detailTab)));
        applyTab();
      });
      return {key,tab,panel};
    });
  function applyTab() {
    if (selectedTab !== 'config') clearContainerConfiguration();
    for (const {key,tab,panel} of tabEntries) {
      const on = key === selectedTab;
      tab.setAttribute('aria-selected',String(on));
      tab.tabIndex = on ? 0 : -1;
      panel.hidden = !on;
      if (key === 'config' && on) panel.append(buildContainerConfiguration(node,target));
      if (on) revealTab(tab);
    }
  }
  if (!tabEntries.some(({key}) => key === selectedTab)) selectedTab = 'summary';
  function revealTab(tab) {
    const bounds = bar.getBoundingClientRect();
    const item = tab.getBoundingClientRect();
    if (item.left < bounds.left) bar.scrollLeft -= bounds.left - item.left;
    else if (item.right > bounds.right) bar.scrollLeft += item.right - bounds.right;
  }
  for (const entry of tabEntries) bar.append(entry.tab);
  bar.addEventListener('keydown',event => {
    if (!['ArrowLeft','ArrowRight','Home','End'].includes(event.key)) return;
    const index = tabEntries.findIndex(({tab}) => tab === document.activeElement);
    if (index < 0) return;
    event.preventDefault();
    const position = event.key === 'Home' ? 0 : event.key === 'End' ? tabEntries.length - 1 :
      (index + (event.key === 'ArrowRight' ? 1 : tabEntries.length - 1)) % tabEntries.length;
    nextFocus(tabEntries[position].tab);
  });
  function nextFocus(tab) {
    for (const entry of tabEntries) entry.tab.tabIndex = entry.tab === tab ? 0 : -1;
    tab.focus({preventScroll:true});
    revealTab(tab);
  }
  const logs = manageable ? containerLogsLink(id,target) : null;
  if (logs) logs.dataset.resourceFocus = target + ':detail-logs';
  const termLink = manageable && item.state === 'running' ? containerTerminalLink(id,target) : null;
  if (termLink) termLink.dataset.resourceFocus = target + ':detail-term';
  const monitor = nodeMonitorLink(id,target,'监控');
  monitor.dataset.resourceFocus = target + ':detail-monitor';
  title.insertBefore(managementActions(logs,termLink,monitor),back);
  page.append(title);
  if (manageable) page.append(actionPanel);
  page.append(bar,...tabEntries.map(({panel}) => panel));
  applyTab();
  if (bar.clientWidth === 0) requestAnimationFrame(() => {
    if (bar.isConnected) revealTab(tabEntries.find(entry => entry.key === selectedTab).tab);
  });
  return page;
}

function clearContainerConfiguration() {
  containerConfiguration?.dispose();
  containerConfiguration = undefined;
}

function buildContainerConfiguration(node,target) {
  if (containerConfiguration?.node.id === node.id && containerConfiguration.node.generation === node.generation &&
      containerConfiguration.target === target) return containerConfiguration.panel;
  clearContainerConfiguration();
  const panel = workspaceText('details','','container-configuration');
  panel.open = true;
  const summary = workspaceText('summary','环境变量与标签');
  summary.dataset.resourceFocus = target + ':configuration';
  const content = workspaceText('div','','container-detail-grid container-configuration-values');
  panel.append(summary,content);
  let pending;
  function clear() {
    pending?.abort();
    pending = undefined;
    content.replaceChildren();
  }
  const current = {node,target,panel,dispose:() => { clear(); panel.remove(); }};
  containerConfiguration = current;
  async function load() {
    clear();
    const controller = new AbortController();
    pending = controller;
    const status = workspaceText('p','正在读取环境变量与标签…');
    status.setAttribute('role','status');
    content.append(status);
    const visible = () => containerConfiguration === current && panel.open && pending === controller;
    try {
      const config = await readContainerConfiguration(node,target,controller.signal);
      if (!visible()) return;
      content.replaceChildren(configurationValues('环境变量',config.environment),configurationValues('标签',config.labels));
    } catch (error) {
      if (!visible()) return;
      status.textContent = error.name === 'TimeoutError' ? '读取配置超时，请重试。' : containerOperationMessage(error.message);
      status.append(' ',isMissingContainer(error.message) ? workspaceLink(nodePath(node.id,'containers'),'返回容器列表','workspace-link') :
        managementButton('重试',load,false,status));
    }
  }
  panel.addEventListener('toggle',() => {
    if (!panel.open) clear();
    else if (containerConfiguration === current && !pending) void load();
  });
  void load();
  return panel;
}

function configurationValues(title,values) {
  const pairs = Object.entries(values || {}).map(([key,value]) => [
    key,value == null || value === '' ?
      workspaceText('span',value == null ? '未赋值' : '空值','work-note') : value
  ]);
  if (pairs.length) return containerFactPanel(title,pairs);
  const panel = containerFactPanel(title,[]);
  panel.lastElementChild.replaceWith(workspaceText('p','暂无配置项。','work-note'));
  return panel;
}

function containerFactPanel(label,facts) {
  const panel = workspaceText('section','','container-detail-section');
  panel.append(workspaceText('h3',label),snapshotFacts(facts));
  return panel;
}

export function resourceLink(id,category,item,label) {
  const catalog = resourceCatalog[category];
  const key = catalog.key(item);
  if (!key) return workspaceText('span',label || catalog.name(item) || '—');
  const link = workspaceLink(nodePath(id,category,key),label || catalog.name(item),'workspace-link');
  link.dataset.resourceFocus = category + ':' + key;
  return link;
}

function containerImage(container,view) {
  const images = view.images || [];
  if (container.image_id) return images.find(image => image.full_id === container.image_id || image.id === container.image_id);
  return container.image ? images.find(image => image.tags?.includes(container.image) || image.digests?.includes(container.image)) : undefined;
}

export function volumeMount(mount,volume) {
  return mount.type === 'volume' && (mount.name ? mount.name === volume.name : !!volume.mountpoint && mount.source === volume.mountpoint);
}

function buildContainerConnections(id,item,view) {
  const section = workspaceText('div','','container-connections');
  const mounts = (item.mounts || []).map(mount => {
    const volume = (view.volumes || []).find(volume => volumeMount(mount,volume));
    return [volume ? resourceLink(id,'volumes',volume) : mount.name || mount.source, mount.destination, mount.read_only ? '只读' : '可读写'];
  });
  const networks = (item.networks || []).map(name => {
    const network = (view.networks || []).find(item => item.name === name);
    return [network ? resourceLink(id,'networks',network) : name];
  });
  section.append(maintenanceTable('存储挂载',['来源','容器路径','权限'],mounts,'暂无挂载。'),
    maintenanceTable('已接入网络',['网络'],networks,'暂无网络记录。'));
  return section;
}

export function maintenanceTable(title,headers,rows,empty) {
  const panel = workspaceText('section','','maintenance-related');
  panel.append(workspaceText('h3',title),rows.length ? resourceTable(headers,rows) : workspaceText('p',empty,'work-empty'));
  return panel;
}

function buildResourceList(id,category,view) {
  const catalog = resourceCatalog[category];
  if (!view.available) return workspaceText('p','Docker 当前不可用。','work-empty');
  const link = item => resourceLink(id,category,item);
  const node = nodeById(id);
  const manageable = !nodeManagementIssue(id);
  const listings = {
    compose:[['项目','管理方式','服务'], item => [link(item),composeManagementPill(item.name),joined(item.services)]],
    images:[['镜像','ID','摘要','大小','使用状态','操作'], item => [link(item),item.id,joined(item.digests),formatBytes(item.size),imageUsage(item),
      manageable && imageManagementActions(node,item) || '—']],
    networks:[['名称','驱动','范围','子网','网关','内部'], item => [link(item),item.driver,item.scope,joined(item.subnets),joined(item.gateways),item.internal ? '是' : '否']],
    volumes:[['名称','类型 / 驱动','范围','挂载点','已用空间','容量上限','引用数'], item => [link(item),item.tmpfs ? 'tmpfs / ' + item.driver : item.driver,item.scope,item.mountpoint,
      item.known ? formatBytes(item.size) : '—',item.capacity_bytes ? formatBytes(item.capacity_bytes) : '—',item.ref_count]]
  };
  const [headers,row] = listings[category];
  const rows = (view[catalog.list] || []).map(row);
  const section = workspaceText('section','','resource-section');
  const sampled = view.section_times?.[category === 'compose' ? 'containers' : category];
  if (sampled && Date.now() - new Date(sampled).getTime() > 120000) {
    section.append(workspaceText('p','上次采集 ' + dateTime(sampled),'work-note resource-detail-note'));
  }
  section.append(rows.length ? resourceTable(headers,rows) : workspaceText('p','暂无记录。','work-empty'));
  return section;
}

function composeManagementPill(name) {
  return /^moori-[a-f0-9]{32}$/.test(name) ? statusPill('Moori 管理','good') : statusPill('源文件项目');
}

function buildResourceDetail(id,category,key,view) {
  const catalog = resourceCatalog[category];
  const item = (view[catalog.list] || []).find(item => catalog.key(item) === key);
  const page = workspaceText('section','','container-workspace');
  const back = workspaceLink(nodePath(id,category),'返回' + catalog.title + '列表','workspace-link resource-back');
  if (!item) {
    page.append(back,workspaceText('p','此资源未出现在当前节点快照中。','work-empty'));
    return page;
  }
  const heading = workspaceText('div','','maintenance-heading container-work-heading');
  const name = catalog.name(item);
  heading.append(workspaceGlyph(nodeCategoryIcons[category]),workspaceText('h2',name),category === 'compose' ? composeManagementPill(name) : statusPill(catalog.title),back);
  if (category === 'compose' && /^moori-[a-f0-9]{32}$/.test(name)) {
    heading.insertBefore(workspaceLink(nodePath(id,'compose',name,'deployments'),'部署记录','editor-link'),back);
  }
  page.append(heading);
  const node = nodeById(id);
  const manageable = !nodeManagementIssue(id);
  const containers = view.containers || [];
  let related, facts, actions;
  if (category === 'compose') {
    related = containers.filter(container => container.compose_project === item.name);
    facts = [['服务',joined(item.services)]];
    const managed = /^moori-[a-f0-9]{32}$/.test(item.name);
    facts.push(['配置来源',managed ? 'Moori 部署版本' : 'Compose 源文件']);
    if (!managed) facts.push(['维护方式','可在这里查看和维护节点已授权的 Compose 源文件。']);
    actions = managementActions(
      workspaceLink(nodePath(id,'compose',key,'edit'),managed ? '修改配置' : '查看 Compose 源文件','editor-link'),
      managed && workspaceLink(nodePath(id,'compose',key,'redeploy'),'重新部署','editor-link'),
      !managed && composeRemoveButton(node,name,related,view,false)
    );
  } else if (category === 'images') {
    related = containers.filter(container => containerImage(container,view) === item);
    facts = [['镜像 ID',copyableID(key)],['镜像摘要',joined(item.digests)],['使用状态',imageUsage(item)],
      ['大小',formatBytes(item.size)],['创建时间',item.created ? dateTime(item.created * 1000) : '—']];
    page.append(...buildImageTagPanels(node,item,key,manageable));
    actions = imageManagementActions(node,item,true);
  } else if (category === 'networks') {
    related = containers.filter(container => container.networks?.includes(item.name));
    facts = [['网络 ID',copyableID(item.id)],['驱动',item.driver],['范围',item.scope],['子网',joined(item.subnets)],['网关',joined(item.gateways)],['内部网络',item.internal ? '是' : '否']];
    if (item.compose_project) facts.push(['Compose 项目',item.compose_project]);
    if (/^[0-9a-f]{64}$/.test(item.id)) actions = networkMaintenance(node,item,containers,related);
  } else {
    related = containers.filter(container => container.mounts?.some(mount => volumeMount(mount,item)));
    facts = [['驱动',item.driver],['范围',item.scope],['挂载点',item.mountpoint],
      ['类型',item.tmpfs ? 'tmpfs 临时内存卷' : '持久卷'],['已用空间',item.known ? formatBytes(item.size) : '—'],
      ['容量上限',item.capacity_bytes ? formatBytes(item.capacity_bytes) : '—'],['挂载容器数',item.ref_count ?? '未知']];
    const command = action => ({action:'volume.' + action,target:item.name});
    const references = Math.max(related.length,item.ref_count || 0);
    const blocked = item.scope && item.scope !== 'local' ? '非本机卷，不能在此删除。' : references ?
      `仍有 ${references} 个容器引用。可通过「检查卷」核对当前挂载，已停止的容器也会保留引用。` : '';
    actions = managementActions(
      maintenanceOperationButton('检查卷',node,name,command('inspect')),
      guardedResourceRemoval('删除卷',node,name,command('delete'),'这会永久删除卷中的数据。Docker 会重新检查容器引用。',blocked)
    );
  }
  if (manageable && actions) {
    const panel = workspaceText('section','','maintenance-actions');
    for (const button of actions.querySelectorAll('button,a')) button.dataset.resourceFocus = category + ':' + key + ':' + button.textContent;
    panel.append(workspaceText('h3',catalog.title + '操作'),actions);
    page.append(panel);
  }
  const summary = workspaceText('div','','container-overview');
  summary.append(snapshotFacts(facts));
  const rows = related.map(container => {
    const target = container.full_id || container.id;
    const label = container.name || container.id;
    const link = /^[0-9a-f]{64}$/.test(target) ? workspaceLink(nodePath(id,'containers',target),label,'workspace-link') : label;
    if (link instanceof Node) link.dataset.resourceFocus = target + ':related';
    return [link,containerState(container),container.compose_service,container.image];
  });
  const hiddenReferences = category === 'volumes' && item.ref_count > rows.length;
  const referenceNote = hiddenReferences
    ? `快照记录了 ${item.ref_count} 个挂载容器，当前列出 ${rows.length} 个。可通过「检查卷」核对实时挂载。`
    : category === 'volumes' && item.ref_count == null ? '挂载状态未知，可通过「检查卷」核对。' : '';
  const relationships = maintenanceTable('关联容器',['名称','状态','Compose 服务','镜像'],rows,referenceNote || '当前快照中无关联容器。');
  page.append(summary,relationships);
  if (hiddenReferences && rows.length) page.append(workspaceText('p',referenceNote,'work-note'));
  return page;
}
