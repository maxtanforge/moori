import {linkRoute,workspaceText,managementActions,actionIcon,resourceTable,option,workspaceLink,statusPill,routeHref,optionField,field,dateTime,download,row} from './ui-components.js';
import {listUsers} from './user-management.js';
import {listContainerWebhooks} from './webhook-management.js';
import {listAlertSettings} from './alert-settings.js';
import {clearCICredential,listCITargets} from './ci-management.js';
import {listPlatformAudit} from './platform-audit.js';
import {discoveredRegistryHosts,imageRegistryChoices,imageRegistryParts,imageRegistryScopes,registryScopeOptions} from './image-management.js';
import {nodePath,nodeCategories,nodeRoute,assertNodeManagementReady,rememberNodeResourceChange,rememberContainerLifecycle,renderWorkspaceBody,resourceLink,workspaceNodeName} from './node-workspace.js';
import {resourceChoiceOptions} from './container-maintenance.js';
import {element,notice,show,enc,request} from './api.js';
import {platformAdmin,detail,action,accountEpoch,goToConsoleView,refreshAfterNodeMutation,signedOut,selected,projects,submitDeployment,loadDeployments,openDeployment} from './app.js';

let managementVersion = 0;
let activeOperationDialog;
let containerLogs;
export const registrySettingsPath = 'settings/registries';
const templateSettingsPath = 'settings/compose-templates';
export const userSettingsPath = 'settings/users';
export const ciSettingsPath = 'settings/ci';
export const webhookSettingsPath = 'settings/webhooks';
export const alertSettingsPath = 'settings/alerts';
export const auditSettingsPath = 'settings/audit';
export let currentSettingsPath = '';
export let settingsRevision = 0;
export let refreshRegistryDiscovery;
let cancelRegistryDiscovery;

export function managementButton(label, onClick, danger = false, statusElement) {
  const button = document.createElement('button');
  button.type = 'button';
  button.textContent = label;
  button.classList.toggle('danger', danger);
  button.addEventListener('click', async () => {
    button.disabled = true;
    button.setAttribute('aria-busy','true');
    try {
      await onClick();
    } catch (error) {
      const status = statusElement || (element('management-dialog').open ? element('management-status') : element('notice'));
      status.textContent = error.message || '操作失败。';
    } finally {
      button.disabled = false;
      button.removeAttribute('aria-busy');
    }
  });
  return button;
}

export function managementDialog(title, ...content) {
  managementVersion++;
  const dialog = element('management-dialog');
  element('management-title').textContent = title;
  element('management-content').replaceChildren(...content);
  element('management-status').textContent = '';
  element('management-output').textContent = '';
  show('management-output', false);
  if (!dialog.open) {
    dialog.showModal();
    // showModal would otherwise land focus on the header close button; start
    // from the first field instead, falling back to the dialog itself.
    const focusable = element('management-content').querySelector('input, select, textarea, button');
    (focusable || dialog).focus();
  }
}

function managementResult(value, message = '') {
  const output = element('management-output');
  output.textContent = typeof value === 'string' ? value : JSON.stringify(value, null, 2);
  show('management-output', !!output.textContent);
  element('management-status').textContent = message;
}

export function managedForm(fields, label, submit, canSubmit = () => true, status = element('management-status')) {
  const form = document.createElement('form');
  const button = document.createElement('button');
  button.type = 'submit';
  button.textContent = label;
  form.append(...fields, button);
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (button.disabled || !canSubmit()) return;
    button.disabled = true;
    status.textContent = '';
    try {
      await submit(new FormData(form), form);
    } catch (error) {
      status.textContent = error.message || '操作失败。';
    } finally {
      button.disabled = !canSubmit();
    }
  });
  return form;
}

export function managementText(text) {
  const p = document.createElement('p');
  p.textContent = text;
  return p;
}

export function resolveSystemSettingsPath(path) {
  if (!platformAdmin) return '';
  if (path === 'settings' || path === 'management') return registrySettingsPath;
  return [registrySettingsPath,templateSettingsPath,userSettingsPath,ciSettingsPath,webhookSettingsPath,alertSettingsPath,auditSettingsPath].includes(path) ? path : '';
}

export function renderSystemSettings(path) {
  if (currentSettingsPath === path) return;
  if (currentSettingsPath === ciSettingsPath) clearCICredential();
  cancelRegistryDiscovery?.();
  currentSettingsPath = path;
  settingsRevision++;
  for (const link of element('settings-tabs').querySelectorAll('a')) {
    if (path && linkRoute(link) === path) link.setAttribute('aria-current','page');
    else link.removeAttribute('aria-current');
  }
  if (path) void loadSystemSettings();
  else element('settings-list').replaceChildren();
}

export async function loadSystemSettings() {
  const version = settingsRevision;
  const panel = element('settings-list');
  const status = workspaceText('p','正在读取设置…','work-empty');
  status.setAttribute('role','status');
  panel.replaceChildren(status);
  try {
    if (currentSettingsPath === registrySettingsPath) await listRegistries();
    else if (currentSettingsPath === templateSettingsPath) await listTemplates();
    else if (currentSettingsPath === userSettingsPath) await listUsers();
    else if (currentSettingsPath === ciSettingsPath) await listCITargets();
    else if (currentSettingsPath === webhookSettingsPath) await listContainerWebhooks();
    else if (currentSettingsPath === alertSettingsPath) await listAlertSettings();
    else if (currentSettingsPath === auditSettingsPath) await listPlatformAudit();
  } catch (error) {
    if (version !== settingsRevision) return;
    status.textContent = '读取失败：' + error.message;
    panel.append(managementActions(managementButton('重试',loadSystemSettings)));
  }
}

export function settingsCreateButton(label, onClick) {
  const button = actionIcon(managementButton(label,onClick),'add',label,false);
  button.classList.add('primary');
  return button;
}

export function settingsTableEntry(name, detail, detailClass = '') {
  const entry = workspaceText('div','','settings-entry');
  entry.append(workspaceText('span',name,'settings-entry-name'));
  if (detail) entry.append(workspaceText('span',detail,'settings-entry-detail ' + detailClass));
  return entry;
}

export function showSettingsList(title, create, headers, rows, empty, ...pagination) {
  const bar = workspaceText('header','','work-panel-header settings-table-heading');
  const heading = workspaceText('h2',title);
  heading.id = 'settings-table-title';
  heading.tabIndex = -1;
  bar.append(heading);
  if (create) bar.append(create);
  const panel = element('settings-list');
  const restoreFocus = panel.contains(document.activeElement);
  if (!rows.length) {
    panel.replaceChildren(bar,workspaceText('p',empty,'work-empty'));
    if (restoreFocus) heading.focus({preventScroll:true});
    return;
  }
  const wrap = resourceTable(headers,rows,true);
  wrap.classList.add('settings-table');
  wrap.classList.toggle('settings-table-readonly',!create);
  wrap.firstElementChild.setAttribute('aria-labelledby',heading.id);
  const footer = workspaceText('footer','','settings-table-footer');
  footer.append(workspaceText('span',`已显示 ${rows.length} 条`));
  if (pagination.some(Boolean)) footer.append(managementActions(...pagination));
  panel.replaceChildren(bar,wrap,footer);
  if (restoreFocus) heading.focus({preventScroll:true});
}

export function activeNode() {
  const selector = element('resource-node');
  return {id: selector.value, label: selector.selectedOptions[0]?.textContent || selector.value};
}

export function nodeById(id) {
  const option = [...element('resource-node').options].find(item => item.value === id);
  return {id, label:option?.textContent || id, generation:Number(option?.dataset.generation || (id === '@local' ? 1 : 0))};
}

export function nodeOperationPath(node) { return '/api/v1/nodes/' + enc(node.id) + '/operations'; }

const operationLabels = {
  'container.create':'创建容器', 'container.start':'启动容器', 'container.stop':'停止容器',
  'container.restart':'重启容器', 'container.pause':'暂停容器', 'container.resume':'恢复容器',
  'container.resources':'更新资源限制', 'container.remove':'删除容器', 'container.replace':'重建容器', 'container.release':'发布容器',
  'compose.remove':'删除 Compose 项目', 'compose.source-create':'创建 Compose 项目',
  'compose.source-save':'保存 Compose 源文件', 'compose.source-deploy':'部署 Compose 源文件',
  'image.pull':'拉取镜像', 'image.tag':'标记镜像', 'image.delete':'删除镜像',
  'image.push':'推送镜像', 'image.import':'导入镜像',
  'network.create':'创建网络', 'network.delete':'删除网络', 'network.connect':'连接网络',
  'network.disconnect':'断开网络', 'volume.create':'创建卷', 'volume.delete':'删除卷'
};
export const operationStates = {queued:'等待节点领取',running:'节点正在执行',succeeded:'成功',failed:'失败',uncertain:'结果未确认'};
const operationCategories = {container:'containers',compose:'compose',image:'images',network:'networks',volume:'volumes'};
const composeInventory = ['containers','images','networks','volumes'];
const operationInventorySections = {
  'container.create':composeInventory, 'container.replace':composeInventory, 'container.release':composeInventory,
  'container.resources':['containers'],
  'container.remove':['containers','networks','volumes'],
  'container.start':['containers'], 'container.stop':['containers'], 'container.restart':['containers'],
  'container.pause':['containers'], 'container.resume':['containers'],
  'compose.remove':composeInventory, 'compose.source-create':composeInventory,
  'compose.source-deploy':composeInventory,
  'image.pull':['images'], 'image.tag':['images'], 'image.delete':['images'], 'image.import':['images'],
  'network.create':['networks'], 'network.delete':['networks'],
  'network.connect':['networks','containers'], 'network.disconnect':['networks','containers'],
  'volume.create':['volumes'], 'volume.delete':['volumes']
};
const lifecycleLabels = {
  'container.start':'启动','container.stop':'停止','container.restart':'重启','container.pause':'暂停','container.resume':'恢复'
};

function applyConfirmedResourceOperation(node,command,job) {
  if (job.state !== 'succeeded' || !command) return;
  const result = job.result || {};
  const observed = result.resource_observed_at;
  switch (command.action) {
    case 'container.create':
    case 'container.replace': {
      const item = result.container;
      if (observed && /^[a-f0-9]{64}$/.test(item?.full_id) && item.state) {
        if (command.action === 'container.replace' && command.target !== item.full_id) {
          rememberNodeResourceChange(node,'containers',command.target,null,observed);
        }
        return rememberNodeResourceChange(node,'containers',item.full_id,item,observed,true);
      }
      break;
    }
    case 'container.resources': {
      const item = result.container;
      if (observed && item?.full_id === command.target) {
        return rememberNodeResourceChange(node,'containers',item.full_id,{
          nano_cpus:item.nano_cpus || 0,memory_limit_bytes:item.memory_limit_bytes || 0,pids_limit:item.pids_limit || 0
        },observed);
      }
      break;
    }
    case 'volume.create': {
      const volume = result.volume;
      if (volume?.name === command.name) return rememberNodeResourceChange(node,'volumes',volume.name,volume,
        observed || new Date().toISOString(),true);
      break;
    }
    case 'network.create': {
      const network = result.network;
      if (network?.name === command.name && /^[a-f0-9]{64}$/.test(network.id)) {
        return rememberNodeResourceChange(node,'networks',network.id,network,observed || new Date().toISOString(),true);
      }
      break;
    }
    case 'compose.source-create': {
      const services = result.compose_source?.after;
      if (observed && Array.isArray(services)) {
        return rememberNodeResourceChange(node,'compose_projects',command.target,{name:command.target,services},observed,true);
      }
      break;
    }
    case 'compose.remove':
      return rememberNodeResourceChange(node,'compose_projects',command.target,null,observed);
    case 'volume.delete':
    case 'network.delete':
    case 'container.remove':
      return rememberNodeResourceChange(node,operationCategories[command.action.split('.')[0]],command.target,null,observed);
    case 'image.delete':
      return rememberNodeResourceChange(node,'images',command.image_id,null,result.image_removed_at);
    default:
      if (lifecycleLabels[command.action]) rememberContainerLifecycle(node,command.target,job);
  }
}

export const operationPending = job => ['queued','running'].includes(job.state);

function operationResourceLink(id, action) {
  const category = operationCategories[action?.split('.')[0]];
  return category ? workspaceLink(nodePath(id,category),'查看' + nodeCategories.find(([key]) => key === category)[1] + '列表','workspace-link') : null;
}

function operationFollowUpLinks(id, job) {
  const links = [operationResourceLink(id,job.action)].filter(Boolean);
  if (!operationPending(job) && job.state !== 'succeeded') {
    if (job.action === 'compose.remove' || (job.action === 'container.remove' && job.error?.includes('挂载卷未能删除'))) {
      links.push(workspaceLink(nodePath(id,'volumes'),'查看卷列表','workspace-link'));
    }
  }
  if (job.action === 'compose.remove' && !operationPending(job)) links.push(workspaceLink(nodePath(id,'networks'),'查看网络列表','workspace-link'));
  return links;
}

function operationStatusPill(job) {
  return statusPill(operationStates[job.state] || job.state,job.state === 'succeeded' ? 'good' : ['failed','uncertain'].includes(job.state) ? 'bad' : '');
}

let operationHistoryView;
let operationHistoryPaging;
export function renderNodeOperationHistory(route, body) {
  if (operationHistoryView?.route === route && body.contains(operationHistoryView.panel)) return;
  const panel = workspaceText('section','','resource-section operation-history');
  const status = workspaceText('p','','work-note operation-history-status');
  status.setAttribute('role','status');
  const content = document.createElement('div');
  if (route.operation) content.className = 'operation-history-detail';
  if (operationHistoryPaging?.node !== route.id || operationHistoryPaging.account !== accountEpoch) {
    operationHistoryPaging = {node:route.id,account:accountEpoch,index:0,cursors:['']};
  }
  const paging = operationHistoryPaging;
  const footer = workspaceText('div','','operation-history-footer');
  const pageLabel = workspaceText('span','','work-note');
  const previousPage = workspaceText('button','上一页');
  const nextPage = workspaceText('button','下一页');
  previousPage.type = nextPage.type = 'button';
  previousPage.addEventListener('click',() => void reload(paging.index - 1));
  nextPage.addEventListener('click',() => void reload(paging.index + 1));
  footer.append(pageLabel,managementActions(previousPage,nextPage));
  footer.hidden = true;
  let nextCursor = '';
  const context = {route,panel};
  operationHistoryView = context;
  const visible = () => nodeRoute === route && operationHistoryView === context && panel.isConnected;
  let pending = false, timer, previous;
  const reload = async (index = paging.index) => {
    if (pending || !visible() || index < 0 || index > paging.index + 1 || index > paging.index && !nextCursor) return;
    const cursor = index > paging.index ? nextCursor : paging.cursors[index];
    clearTimeout(timer);
    pending = true;
    previousPage.disabled = nextPage.disabled = true;
    footer.setAttribute('aria-busy','true');
    if (!previous) status.textContent = '正在读取操作记录…';
    try {
      const reply = await request(nodeOperationPath(route) + (route.operation ? '?id=' + enc(route.operation) :
        '?limit=20' + (cursor ? '&after=' + enc(cursor) : '')));
      if (!visible()) return;
      const jobs = route.operation ? reply : reply.items;
      if (!route.operation) {
        paging.index = index;
        paging.cursors[index] = cursor;
        nextCursor = reply.next || '';
        pageLabel.textContent = '第 ' + (index + 1) + ' 页 · ' + jobs.length + ' 条';
        footer.hidden = index === 0 && !nextCursor;
      }
      const signature = JSON.stringify(jobs);
      status.textContent = '';
      if (signature !== previous) {
        previous = signature;
        if (!jobs.length) {
          content.replaceChildren(workspaceText('p',route.operation ? '未找到这条操作记录，记录可能已过期。' : '暂无变更操作记录。','work-empty'));
        } else if (route.operation) {
          const [job] = jobs;
          content.replaceChildren(workspaceText('h3',operationLabels[job.action] || job.action),operationStatusPill(job),
            workspaceText('p',[dateTime(job.created_at),job.target,job.id].filter(Boolean).join(' · '),'work-note'));
          const outcome = operationOutcomeMessage(job);
          if (outcome) content.append(workspaceText('p',outcome,'work-note'));
          const links = workspaceText('div','','resource-actions');
          links.append(...operationFollowUpLinks(route.id,job));
          if (job.action === 'image.pull' && job.target && ['failed','uncertain'].includes(job.state)) {
            links.append(managementButton('重新拉取',() => pullNodeImage(nodeById(route.id),job.target)));
          }
          content.append(links);
        } else {
          content.replaceChildren(resourceTable(['提交时间','操作','目标','状态'],jobs.map(job => [dateTime(job.created_at),
            workspaceLink(nodePath(route.id,'operations',job.id),operationLabels[job.action] || job.action,'workspace-link'),job.target,operationStatusPill(job)])));
        }
      }
      if (jobs.some(operationPending)) timer = setTimeout(reload,2500);
    } catch (error) {
      if (visible()) status.textContent = '读取操作记录失败：' + error.message;
    } finally {
      pending = false;
      footer.removeAttribute('aria-busy');
      previousPage.disabled = paging.index === 0;
      nextPage.disabled = !nextCursor;
    }
  };
  const toolbar = workspaceText('header','','operation-history-toolbar');
  toolbar.append(route.operation ? workspaceLink(nodePath(route.id,'operations'),'返回操作记录','workspace-link') :
    workspaceText('p','当前账号的变更记录 · 保留 30 天','work-note'),
    actionIcon(managementButton('刷新操作记录',() => reload(),false,status),'refresh'));
  panel.append(toolbar,status,content,footer);
  body.replaceChildren(panel);
  void reload();
}

function operationOutcomeMessage(job) {
  const reason = containerOperationMessage(job.error);
  const guidance = job.state === 'uncertain' ? '节点未确认最终结果。请核对资源当前状态，再决定是否重试。' : '';
  return [reason,guidance].filter(Boolean).join(' ');
}

export function dialogOperationSurface() {
  const account = accountEpoch;
  const version = managementVersion;
  const dialog = element('management-dialog');
  return {
    token:version, status:element('management-status'), results:element('management-content'),
    output:element('management-output'), visible:() => account === accountEpoch && version === managementVersion && dialog.open,
    leave:() => dialog.close()
  };
}

export async function performOperation(node, command, submitPath = nodeOperationPath(node), surface = dialogOperationSurface()) {
  assertNodeManagementReady(node.id);
  if (!node.generation) throw new Error('无法获取节点代次，请先刷新节点列表。');
  const token = surface.token ?? surface;
  if (activeOperationDialog === token) throw new Error('请等待当前节点操作完成。');
  activeOperationDialog = token;
  try {
    surface.command = command;
    surface.status.textContent = '正在提交操作…';
    let job;
    try {
      job = await request(submitPath, {...command, generation:node.generation});
    } catch (error) {
      if (Number.isInteger(error.status) || !Object.hasOwn(operationLabels,command.action)) throw error;
      if (surface.visible()) {
        surface.status.textContent = '未收到提交确认。操作可能已被节点接收，请先查看操作记录和资源状态。';
        const link = workspaceLink(nodePath(node.id,'operations'),'查看操作记录','workspace-link');
        if (surface.leave) link.addEventListener('click',surface.leave);
        surface.results.append(link);
      }
      return;
    }
    return await reportOperation(node,job,surface);
  } finally {
    if (activeOperationDialog === token) activeOperationDialog = undefined;
  }
}

export async function reportOperation(node, initial, surface) {
  const started = Date.now();
  const action = surface.command?.action || initial.action;
  const pulling = action === 'image.pull';
  const longRunning = pulling || action === 'compose.source-create';
  let readFailures = 0;
  const path = nodeOperationPath(node) + '/' + enc(initial.id);
  let job = initial;
  const recorded = Object.hasOwn(operationLabels,initial.action);
  const {command} = surface;
  const account = accountEpoch;
  const historyPath = nodePath(node.id,'operations',initial.id);
  if (recorded && /^[a-f0-9]{32}$/.test(initial.id) && surface.visible() && ![...surface.results.querySelectorAll('a')].some(link => linkRoute(link) === historyPath)) {
    const link = workspaceLink(historyPath,'查看操作记录','workspace-link');
    if (surface.leave) link.addEventListener('click',surface.leave);
    surface.results.append(link);
  }
  while (account === accountEpoch && (recorded || surface.visible()) && operationPending(job)) {
    const elapsed = Math.max(0,Math.floor((Date.now() - started) / 1000));
    if (surface.visible()) {
      if (pulling) {
        const stage = readFailures ? '暂时无法读取拉取状态，正在重试' :
          job.state === 'queued' ? '等待节点领取拉取任务' : 'Docker 正在拉取镜像';
        surface.status.textContent = stage + ' · 已等待 ' + elapsed + ' 秒。可关闭窗口，在操作记录中查看结果。';
      } else surface.status.textContent = job.state === 'queued' ? '等待节点领取操作…' : '节点正在执行…';
    }
    if (!longRunning && elapsed >= 45) {
      if (!surface.visible()) return;
      const recheck = managementButton('继续查看结果',async () => {
        const latest = await request(path);
        if (!surface.visible()) return;
        recheck.remove();
        await reportOperation(node,latest,surface);
      },false,surface.status);
      surface.results.append(recheck);
      surface.status.textContent = '操作仍在执行，编号 ' + initial.id;
      return;
    }
    await new Promise(resolve => setTimeout(resolve,longRunning && (readFailures || elapsed >= 45) ? 2500 : 1000));
    if (account === accountEpoch && (recorded || surface.visible())) {
      try {
        job = await request(path,undefined,undefined,AbortSignal.timeout(5000));
        readFailures = 0;
      } catch (error) {
        if (longRunning && (!error.status || error.status >= 500) && ++readFailures <= 3) continue;
        if (!recorded) throw error;
        if (surface.visible()) surface.status.textContent = '操作已提交，暂时无法读取结果。请到操作记录核查后再重试。';
        return;
      }
    }
  }
  if (account !== accountEpoch || operationPending(job)) return;
  const inventory = operationInventorySections[command?.action || job.action];
  const applied = applyConfirmedResourceOperation(node,command,job);
  if (inventory?.length) void refreshAfterNodeMutation(node.id,inventory,job.result?.resource_observed_at);
  const lifecycleLabel = lifecycleLabels[command?.action];
  if (!surface.visible()) {
    if (nodeRoute?.id === node.id && (applied || lifecycleLabel && job.state === 'succeeded')) renderWorkspaceBody();
    return job.state;
  }
  if (applied && ['container.create','container.replace'].includes(command?.action) && !nodeRoute?.mode) renderWorkspaceBody();
  if (applied && ['volume.create','volume.delete','network.create','network.delete','container.remove'].includes(command?.action)) {
    const category = inventory[0];
    surface.leave?.();
    const removed = command.action.endsWith('.delete') || command.action === 'container.remove';
    if (removed && nodeRoute?.id === node.id && nodeRoute.category === category &&
        (nodeRoute.resource || nodeRoute.container) === command.target) goToConsoleView(nodePath(node.id,category));
    else renderWorkspaceBody();
    return job.state;
  }
  const labels = {succeeded:'操作成功', failed:'操作失败', uncertain:'执行结果待核查'};
  const result = longRunning ? '' : job.result?.log ?? job.result?.container ?? job.result?.volume ?? job.result?.network ?? job.result ?? '';
  if (surface.output) {
    surface.output.textContent = typeof result === 'string' ? result : JSON.stringify(result,null,2);
    surface.output.hidden = !surface.output.textContent;
  }
  surface.status.textContent = [labels[job.state] || job.state,operationOutcomeMessage(job),job.result?.truncated && '输出已截断'].filter(Boolean).join(' · ');
  if (pulling && job.state === 'succeeded') {
    surface.status.textContent = '镜像拉取成功：' + (job.result?.image || command?.image || '已保存至节点');
    const link = operationResourceLink(node.id,'image.pull');
    if (surface.leave) link.addEventListener('click',surface.leave);
    surface.results.append(link);
  }
  if (recorded && ['failed','uncertain'].includes(job.state)) {
    for (const link of operationFollowUpLinks(node.id,job)) {
      if (surface.leave) link.addEventListener('click',surface.leave);
      surface.results.append(link);
    }
  }
  if (job.action === 'compose.remove' && job.state === 'succeeded') {
    if (nodeRoute?.id === node.id && nodeRoute.category === 'compose' && nodeRoute.resource === command?.target) {
      surface.leave?.();
      goToConsoleView(nodePath(node.id,'compose'));
      return job.state;
    }
    const link = workspaceLink(nodePath(node.id,'networks'),'前往网络列表','workspace-link');
    if (surface.leave) link.addEventListener('click',surface.leave);
    surface.results.append(link);
  }
  if (job.state === 'succeeded' && ['container.create','container.replace'].includes(job.action) && /^[0-9a-f]{64}$/.test(job.result?.container?.full_id)) {
    const link = workspaceLink(nodePath(node.id,'containers',job.result.container.full_id),'查看新容器','workspace-link');
    if (surface.leave) link.addEventListener('click',surface.leave);
    surface.results.prepend(link);
    if (job.action === 'container.replace' && surface.containerList) {
      const back = element('maintenance-back');
      back.href = routeHref(surface.containerList);
      back.textContent = '返回容器列表';
    }
  }
  if (job.state === 'succeeded' && command?.action === 'image.delete' &&
      nodeRoute?.id === node.id && nodeRoute.category === 'images') {
    if (nodeRoute.resource === command.image_id) {
      surface.leave();
      goToConsoleView(nodePath(node.id,'images'));
      const feedback = element('resource-action-feedback');
      feedback.textContent = '镜像已删除。';
      feedback.hidden = false;
    } else {
      renderWorkspaceBody();
    }
  }
  if (job.state === 'succeeded' && nodeRoute?.id === node.id && nodeRoute.category === 'containers' && lifecycleLabel) {
    surface.leave?.();
    renderWorkspaceBody();
    if (!surface.inline) {
      const feedback = element('resource-action-feedback');
      feedback.textContent = '容器' + lifecycleLabel + '操作已完成。';
      feedback.hidden = false;
    }
  }
  return job.state;
}

export function isMissingContainer(message) { return message === 'Docker container no longer exists on this node'; }

export function containerOperationMessage(message) {
  if (message === 'Docker stopped reporting image pull progress') return 'Docker 已连续 15 分钟未返回拉取进度，等待已结束。';
  if (message === 'The node restarted before confirming the Docker operation. Refresh resources before retrying.') return '节点在确认操作结果前已重启。';
  if (message === 'Docker did not confirm the result. Refresh resources before retrying.') return 'Docker 执行中断，节点未收到最终确认。';
  if (message === 'Docker image is used by a container; remove the container first') return '镜像被容器使用，无法删除。请先删除引用它的容器（包括已停止的容器）。';
  if (message === 'Docker volume is referenced by a container') return '卷仍被容器引用，无法删除。请检查关联容器，包括已经停止的容器。';
  if (message === 'Docker network is referenced by a container') return '网络仍连着容器，无法删除。请先断开连接，或通过 Compose 项目调整。';
  if (message === 'only user-defined local bridge networks can be changed') return '只能修改用户自建的本机桥接网络。';
  if (isMissingContainer(message)) return '该容器 ID 已不在目标节点上。若容器已重建，请从列表重新选择。';
  if (message === 'Docker could not find the replacement image or an attached resource') return 'Docker 找不到替换所需的镜像或关联资源，请检查目标节点上的镜像、网络和挂载。';
  if (message === 'image is unavailable locally; pull it on this node first') return '目标节点没有这个镜像，请先在镜像列表拉取。';
  if (message === 'multiple enabled registries match this image at the same repository prefix') return '存在多个同域名、同前缀的共享仓库，请选择本次使用的仓库。';
  if (['Docker could not pull the container image','Docker could not pull the registry image'].includes(message)) return '目标节点拉取镜像失败。请核对镜像路径、版本、节点网络和仓库读取权限。';
  if (message === 'registry operation credentials are unavailable') return '节点未能取得本次拉取的仓库凭证，请核对仓库配置和连接状态。';
  if (message === 'private registry credentials require verified HTTPS') return '传递私有仓库凭证需要加密连接，请核对 Agent 的连接地址。';
  if (message === 'anonymous image pull failed; pull private images through an authorized registry first') return '匿名拉取镜像失败。如果是私有镜像，请先通过已授权的仓库拉取。';
  return message;
}

export function containerLogsLink(id,target,iconOnly = false) {
  const link = workspaceLink(nodePath(id,'containers',target,'logs'),'查看日志','editor-link');
  return iconOnly ? actionIcon(link,'logs') : link;
}

export function containerTerminalLink(id,target,iconOnly = false) {
  const link = workspaceLink(nodePath(id,'containers',target,'term'),'进入终端','editor-link');
  return iconOnly ? actionIcon(link,'terminal') : link;
}

export function stopContainerLogs() {
  if (!containerLogs) return;
  containerLogs.stop();
  containerLogs = undefined;
  element('container-logs-controls').replaceChildren();
  element('container-logs-output').textContent = '';
  element('container-logs-status').textContent = '';
  show('container-logs-output', false);
}

export function renderContainerLogs(node,target,name) {
  element('container-logs-list').href = routeHref(nodePath(node.id,'containers'));
  const back = element('container-logs-back');
  const destination = nodeRoute?.returnPath || nodePath(node.id,'containers',target);
  back.href = routeHref(destination);
  const fromList = destination === nodePath(node.id,'containers');
  back.textContent = fromList ? '返回容器列表' : '返回容器详情';
  element('container-logs-list').hidden = fromList;
  back.previousElementSibling.hidden = fromList;
  element('container-logs-title').textContent = name;
  const key = [node.id,target,node.generation].join('/');
  if (containerLogs?.key === key) return;
  stopContainerLogs();

  const status = element('container-logs-status');
  if (!node.generation) {
    status.textContent = '正在读取节点信息…';
    return;
  }
  const output = element('container-logs-output');
  const rows = optionField('tail','尾部行数',['100','200','500','1000'].map(value => [value,value + ' 行']),'200');
  const tail = rows.lastElementChild;
  const scope = optionField('log-run-scope','日志范围',[['current','本次运行'],['all','全部运行']],'current');
  const runScope = scope.lastElementChild;
  const auto = field('auto-refresh','实时',{type:'checkbox',checked:true});
  const toggle = auto.lastElementChild;
  toggle.setAttribute('role','switch');
  auto.prepend(toggle);
  auto.className = 'check log-auto';
  const refreshButton = actionIcon(workspaceText('button','刷新日志'),'refresh');
  refreshButton.type = 'button';
  refreshButton.classList.add('log-refresh');
  const stamps = field('log-timestamps','时间戳',{type:'checkbox',checked:false});
  const stampsToggle = stamps.lastElementChild;
  stampsToggle.setAttribute('role','switch');
  stamps.prepend(stampsToggle);
  stamps.className = 'check log-auto';
  const search = field('log-filter','过滤日志',{type:'search',placeholder:'包含文本',autocomplete:'off',maxlength:200});
  const searchInput = search.lastElementChild;
  const filterCount = workspaceText('span','','log-filter-count');
  const downloadButton = actionIcon(workspaceText('button','下载日志'),'download');
  downloadButton.type = 'button';
  downloadButton.classList.add('log-download');
  downloadButton.addEventListener('click',() => {
    if (!rawLog) return;
    const blob = new Blob([rawLog],{type:'text/plain;charset=utf-8'});
    const link = document.createElement('a');
    link.href = URL.createObjectURL(blob);
    link.download = (name || 'container').replace(/[^\w.-]+/g,'_') + '.log';
    link.click();
    URL.revokeObjectURL(link.href);
  });
  element('container-logs-controls').replaceChildren(search,scope,rows,auto,stamps,refreshButton,downloadButton,filterCount);
  searchInput.addEventListener('input',() => {
    logQuery = searchInput.value.trim().toLowerCase();
    renderLog();
  });

  const version = accountEpoch;
  const lifetime = new AbortController();
  const encoder = new TextEncoder();
  const activePage = () => !lifetime.signal.aborted && version === accountEpoch && !document.hidden;
  let active;
  let timer;
  let retryAfter = 0;
  let rawLog = '';
  let logQuery = '';
  const fallback = '实时流不可用，每 3 秒读取日志快照。';
  const paused = '已暂停实时日志。';

  // renderLog applies the active filter to the raw snapshot; streaming and
  // snapshot reads both publish through it so the view stays consistent.
  function renderLog() {
    // The placeholder is display-only: rawLog stays empty so downloading
    // (guarded by rawLog) never exports placeholder text as a log file.
    let text = rawLog || '暂无容器日志。';
    let total = 0;
    let shown = 0;
    if (logQuery && rawLog) {
      const lines = rawLog.split('\n');
      total = lines.length;
      const kept = lines.filter(line => line.toLowerCase().includes(logQuery));
      shown = kept.length;
      text = kept.length ? kept.join('\n') : '没有匹配的日志行。';
    }
    filterCount.textContent = logQuery && total ? `匹配 ${shown} / ${total} 行` : '';
    if (output.textContent === text && !output.hidden) return;
    const scroll = output.scrollTop;
    const followTail = output.hidden || output.scrollHeight - output.clientHeight - scroll < 16;
    output.textContent = text;
    show('container-logs-output',true);
    output.scrollTop = followTail ? output.scrollHeight : scroll;
  }

  function cancelRead() {
    clearTimeout(timer);
    const previous = active;
    active = undefined;
    previous?.abort();
    refreshButton.disabled = false;
  }

  function publish(log) {
    if (log === rawLog && !output.hidden) return;
    rawLog = log;
    renderLog();
  }

  function boundedLog(log,lines) {
    let end = log.length - (log.endsWith('\n') ? 2 : 1);
    for (let i = 0; end >= 0 && i < lines; i++) {
      end = log.lastIndexOf('\n',end);
      if (end < 0) break;
      if (i === lines - 1) log = log.slice(end + 1);
      end--;
    }
    const bytes = encoder.encode(log);
    if (bytes.length <= 64 * 1024) return {log,truncated:false};
    let offset = bytes.length - 64 * 1024;
    while ((bytes[offset] & 0xc0) === 0x80) offset++;
    return {log:new TextDecoder('utf-8',{ignoreBOM:true}).decode(bytes.subarray(offset)),truncated:true};
  }

  async function readSnapshot(run,lines,current) {
    const signal = AbortSignal.any([run.signal,AbortSignal.timeout(45000)]);
    const path = nodeOperationPath(node);
    let job = await request(path,{action:'container.logs',target,tail:lines,timestamps:stampsToggle.checked,generation:node.generation,
      ...(runScope.value === 'all' ? {all_log_runs:true} : {})},undefined,signal);
    const resultPath = path + '/' + enc(job.id);
    let pollDelay = 150;
    while (current() && ['queued','running'].includes(job.state)) {
      await new Promise(resolve => setTimeout(resolve,pollDelay));
      pollDelay = Math.min(pollDelay + 100,1000);
      if (current()) job = await request(resultPath,undefined,undefined,signal);
    }
    if (!current()) return;
    if (job.state !== 'succeeded') throw new Error(job.error || '节点未能返回日志。');
    const bounded = boundedLog(job.result?.log || '',lines);
    publish(bounded.log);
    return !!job.result?.truncated || bounded.truncated;
  }

  async function readStream(run,lines,current) {
    const params = new URLSearchParams({tail:lines,generation:node.generation,timestamps:stampsToggle.checked});
    if (runScope.value === 'all') params.set('all_log_runs','true');
    const path = '/api/v1/nodes/' + enc(node.id) + '/containers/' + enc(target) + '/log-stream?' + params;
    const connecting = new AbortController();
    const connectTimeout = setTimeout(() => connecting.abort(),12000);
    const signal = AbortSignal.any([run.signal,connecting.signal]);
    let response;
    try {
      response = await fetch(path,{mode:'same-origin',credentials:'same-origin',redirect:'error',
        cache:'no-store',headers:{Accept:'text/plain'},signal});
    } finally {
      clearTimeout(connectTimeout);
    }
    if (!current()) {
      if (response.body) void response.body.cancel().catch(() => {});
      return;
    }
    if (response.status === 401) {
      signedOut();
      return;
    }
    if (!response.ok || !response.body || response.headers.get('Content-Type')?.split(';')[0] !== 'text/plain') {
      if (response.body) void response.body.cancel().catch(() => {});
      throw new Error('节点未能建立实时日志流。');
    }
    run.connected = true;
    // A stream opens against the node's latest start time. Do not display
    // lines retained from an older connection while it is still empty.
    publish('');
    const reader = response.body.getReader();
    const decoder = new TextDecoder('utf-8',{ignoreBOM:true});
    const stop = () => { void reader.cancel().catch(() => {}); };
    run.signal.addEventListener('abort',stop,{once:true});
    let frame;
    let log = '';
    let truncated = false;
    status.textContent = '实时日志已连接，等待输出。';
    function flush() {
      cancelAnimationFrame(frame);
      frame = undefined;
      if (!current() || !log) return;
      publish(log);
      const label = truncated ? '实时日志已连接。显示最近内容，单页上限 64 KiB。' : '实时日志已连接。';
      if (status.textContent !== label) status.textContent = label;
    }
    function append(chunk) {
      if (!chunk) return;
      const bounded = boundedLog(log + chunk,lines);
      log = bounded.log;
      truncated ||= bounded.truncated;
      frame ??= requestAnimationFrame(flush);
    }
    try {
      while (current()) {
        const chunk = await reader.read();
        if (!current()) return;
        if (chunk.done) {
          append(decoder.decode());
          break;
        }
        append(decoder.decode(chunk.value,{stream:true}));
      }
    } finally {
      flush();
      run.signal.removeEventListener('abort',stop);
      reader.releaseLock();
    }
  }

  function scheduleFallback() {
    timer = setTimeout(() => {
      if (activePage() && toggle.checked) void read(Date.now() >= retryAfter ? 'stream' : 'fallback');
    },3000);
  }

  async function read(mode,haveSnapshot = false) {
    cancelRead();
    if (!activePage()) return;
    const run = new AbortController();
    active = run;
    const current = () => active === run && activePage() && !run.signal.aborted;
    const lines = Number(tail.value);
    refreshButton.disabled = mode !== 'stream';
    try {
      if (mode === 'stream') {
        status.textContent = '正在连接实时日志…';
        await readStream(run,lines,current);
      } else {
        if (mode === 'fallback') status.textContent = fallback;
        const truncated = await readSnapshot(run,lines,current);
        if (current()) status.textContent =
          [mode === 'fallback' ? fallback : toggle.checked ? '' : paused, truncated && '日志输出已截断（64 KiB）。'].filter(Boolean).join(' ');
      }
    } catch (error) {
      if (!current()) return;
      if (mode !== 'stream') status.textContent =
        (mode === 'fallback' ? fallback : paused) + ' 读取日志失败：' + (error.message || '请重试。');
    } finally {
      if (current()) {
        active = undefined;
        refreshButton.disabled = false;
        if (toggle.checked && mode === 'stream') {
          status.textContent = fallback;
          retryAfter = Date.now() + 30000;
          if (run.connected || haveSnapshot) scheduleFallback();
          else void read('fallback');
        } else if (toggle.checked && mode === 'fallback') {
          scheduleFallback();
        } else if (toggle.checked && mode === 'manual') {
          void read('stream',true);
        }
      }
    }
  }

  containerLogs = {key,stop() {
    lifetime.abort();
    cancelRead();
  }};
  const {signal} = lifetime;
  document.addEventListener('visibilitychange',() => {
    if (document.hidden) cancelRead();
    else if (toggle.checked) void read('stream');
  },{signal});
  tail.addEventListener('change',() => {
    if (toggle.checked) void read('stream');
    else status.textContent = paused + ' 行数变更将在下次手动刷新时应用。';
  },{signal});
  runScope.addEventListener('change',() => {
    cancelRead();
    publish('');
    if (toggle.checked) void read('stream');
    else void read('manual');
  },{signal});
  stampsToggle.addEventListener('change',() => {
    if (toggle.checked) void read('stream');
    else status.textContent = paused + ' 时间戳变更将在下次读取时应用。';
  },{signal});
  toggle.addEventListener('change',() => {
    if (toggle.checked) void read('stream');
    else {
      cancelRead();
      status.textContent = paused;
    }
  },{signal});
  refreshButton.addEventListener('click',() => void read('manual'),{signal});
  void read('stream');
}

export function maintenanceOperationButton(label,node,name,command,impact = '',danger = false) {
  return managementButton(label,() => {
    const title = label + ' · ' + name;
    const scope = managementText('节点 ' + node.label);
    scope.className = 'maintenance-scope';
    if (!impact) {
      managementDialog(title,scope);
      return performOperation(node,command);
    }
    const message = managementText(impact);
    message.className = danger ? 'maintenance-impact destructive' : 'maintenance-impact';
    const confirm = managementButton('确认' + label,async () => {
      confirm.hidden = true;
      try {
        await performOperation(node,command);
      } catch (error) {
        confirm.hidden = false;
        throw error;
      }
    },danger);
    managementDialog(title,scope,message,managementActions(confirm));
  },danger);
}

function removalResourceList(title,items) {
  if (!items.length) return null;
  const list = workspaceText('ul','','removal-items');
  for (const [name,note] of items) {
    const row = workspaceText('li');
    row.append(name instanceof Node ? name : workspaceText('strong',name));
    if (note) row.append(workspaceText('span',' — ' + note));
    list.append(row);
  }
  const section = document.createElement('section');
  section.append(workspaceText('h3',title),list);
  return section;
}

function resourceRemoveButton(label,node,name,command,selected,view = {},allowVolumeCleanup = true) {
  return managementButton(label,() => {
    const compose = command.action === 'compose.remove';
    const mounts = selected.flatMap(item => item.mounts || []);
    const names = [...new Set(mounts.filter(mount => mount.type === 'volume').map(mount => mount.name).filter(Boolean))];
    const binds = [...new Set(mounts.filter(mount => mount.type === 'bind').map(mount => mount.source).filter(Boolean))];
    const selectedIDs = new Set(selected.map(container => container.full_id || container.id).filter(Boolean));
    const other = (view.containers || []).filter(container => compose ? container.compose_project !== command.target : !selectedIDs.has(container.full_id || container.id));
    let removable = 0;
    const volumes = names.map(volumeName => {
      const item = view.volumes?.find(volume => volume.name === volumeName);
      const attached = container => container.mounts?.some(mount => mount.type === 'volume' && mount.name === volumeName);
      const shared = other.some(attached) || item?.ref_count > selected.filter(attached).length;
      const ownershipKnown = typeof item?.compose_project === 'string';
      const outside = ownershipKnown && (compose ? item.compose_project !== command.target : !!item.compose_project);
      const protectedVolume = item?.scope && item.scope !== 'local';
      if (!shared && !outside && !protectedVolume) removable++;
      const note = !allowVolumeCleanup ? '保留' : shared ? '仍有其他容器引用，保留' : protectedVolume ? '非本机卷，保留' :
        outside ? '不属于此次清理范围，保留' : !ownershipKnown ? '归属将在执行时核对' : '默认保留，可选择一并清理';
      return [item ? resourceLink(node.id,'volumes',item) : volumeName,note];
    });
    const impact = managementText(compose ?
      '停止并删除项目容器。镜像、网络及 Compose 源文件保留；需要清理网络时，请单独到网络页面操作。' :
      '删除容器及其可写层。镜像和网络保留。');
    impact.className = 'maintenance-impact destructive';
    const volumeOption = allowVolumeCleanup && removable ? workspaceText('label','','check removal-volume-option') : null;
    const volumeCheckbox = volumeOption && document.createElement('input');
    if (volumeOption) {
      volumeCheckbox.name = 'remove_volumes';
      volumeCheckbox.type = 'checkbox';
      volumeOption.append(volumeCheckbox,workspaceText('span','同时清理符合条件的挂载卷（卷中数据将丢失）'));
    }
    const hint = managementText((names.length ? '默认保留卷。' : '当前快照未列出挂载卷，本次将保留卷。') +
      '宿主机绑定目录保留。Docker 执行时会核对当前资源与引用；清单可能与快照不同。');
    const dependencies = [
      compose && removalResourceList('项目容器（快照）',selected.map(item => [item.name || item.id || item.compose_service || '项目容器',item.state])),
      removalResourceList('挂载卷（快照）',volumes),
      removalResourceList('宿主机绑定目录',binds.map(path => [path,'保留']))
    ].filter(Boolean);
    for (const group of dependencies) for (const link of group.querySelectorAll('a')) {
      link.addEventListener('click',() => element('management-dialog').close());
    }
    const confirm = managementButton('确认' + label,async () => {
      const removeVolumes = !!volumeCheckbox?.checked;
      if (volumeCheckbox) volumeCheckbox.disabled = true;
      confirm.hidden = true;
      try {
        await performOperation(node,{...command,remove_volumes:removeVolumes});
      } catch (error) {
        if (volumeCheckbox) volumeCheckbox.disabled = false;
        confirm.hidden = false;
        throw error;
      }
    },true);
    managementDialog(label + ' · ' + name,managementText('节点 ' + node.label),impact,...dependencies,hint,
      ...[volumeOption].filter(Boolean),managementActions(confirm));
  },true);
}

export function containerRemoveButton(node,item,view) {
  const button = resourceRemoveButton('删除容器',node,item.name || item.id,
    {action:'container.remove',target:item.full_id || item.id},[item],view);
  button.disabled = !!item.compose_project || !['created','exited','dead'].includes(item.state);
  button.title = item.compose_project ? '此容器属于 Compose 项目，请从项目中删除' :
    button.disabled ? '请先停止容器，再执行删除' : '删除容器';
  return button;
}

export function composeRemoveButton(node,name,containers,view,allowVolumeCleanup = true) {
  const button = resourceRemoveButton('删除项目',node,name,{action:'compose.remove',target:name},
    containers.filter(item => item.compose_project === name),view,allowVolumeCleanup);
  button.disabled = /^moori-[a-f0-9]{32}$/.test(name);
  if (button.disabled) button.title = '此项目由 Moori 部署管理，请通过对应部署处理';
  return button;
}

export function networkMaintenance(node,item,containers,related) {
  const target = item.id;
  const command = action => ({action:'network.' + action,target});
  const protectedNetwork = item.protected || ['bridge','default','host','none','nat','ingress','docker_gwbridge'].includes(item.name) ||
    (item.scope && item.scope !== 'local') || (item.driver && item.driver !== 'bridge');
  const blocked = protectedNetwork ? '这是受保护的网络，不能在这里修改。' : related.length ? '网络仍有关联容器。请先断开连接；Compose 容器需通过项目配置调整。' : '';
  const actions = managementActions(maintenanceOperationButton('检查网络',node,item.name,command('inspect')),
    guardedResourceRemoval('删除网络',node,item.name,command('delete'),'删除网络及其配置。执行时 Docker 会重新检查连接。',blocked));
  if (protectedNetwork) return actions;
  const standalone = containers.filter(container => !container.compose_project && /^[0-9a-f]{64}$/.test(container.full_id || ''));
  const attached = container => container.networks?.includes(item.name);
  for (const connect of [true,false]) {
    const options = standalone.filter(container => !!attached(container) !== connect);
    if (!options.length) continue;
    const verb = connect ? '接入容器' : '断开容器';
    actions.append(managementButton(verb,() => {
      const selection = optionField('container','独立容器',options.map(container => [container.full_id,container.name]));
      const form = managedForm([selection],connect ? '确认接入' : '确认断开',data =>
        performOperation(node,{...command(connect ? 'connect' : 'disconnect'),container:data.get('container')}));
      managementDialog(verb + ' · ' + item.name,
        managementText('节点 ' + node.label),
        managementText(connect ? '选择要接入此网络的独立容器。' : '断开连接可能中断此容器的网络通信。'),
        form);
    }));
  }
  return actions;
}

export function guardedResourceRemoval(label,node,name,command,impact,blocked) {
  const button = maintenanceOperationButton(label,node,name,command,impact,true);
  if (!blocked) return button;
  button.disabled = true;
  const allowance = workspaceText('span','','removal-blocked');
  allowance.append(button,workspaceText('span',blocked,'work-note'));
  return allowance;
}

export function pullNodeImage(node,reference = '') {
  const registry = workspaceLink(registrySettingsPath,'打开镜像仓库设置','workspace-link');
  const imageField = field('image','镜像名称、标签或摘要',{required:true,maxLength:512,placeholder:'docker.io/library/nginx:stable',value:reference});
  const image = imageField.lastElementChild;
  const registryField = optionField('registry_id','镜像仓库',[['','正在读取…']]);
  const select = registryField.lastElementChild;
  const status = managementText('');
  let registries;
  const update = () => {
    const matches = imageRegistryScopes(registries,imageRegistryParts(image.value));
    registryScopeOptions(select,matches,matches.length ? '请选择仓库' : '自动匹配');
    select.required = matches.length > 0;
  };
  const form = managedForm([imageField,registryField,status],'拉取镜像',data => {
    const id = data.get('registry_id');
    return performOperation(node,{action:'image.pull',image:data.get('image').trim(),...(id && {registry_id:id})});
  });
  const submit = form.querySelector('button[type=submit]');
  submit.disabled = true;
  image.addEventListener('input',() => { if (registries) update(); });
  registry.addEventListener('click',() => element('management-dialog').close());
  managementDialog('拉取镜像 · ' + node.label,form,registry);
  const surface = dialogOperationSurface();
  imageRegistryChoices(surface.visible).then(items => {
    if (!surface.visible()) return;
    registries = items;
    update();
    submit.disabled = false;
  }).catch(error => {
    if (!surface.visible()) return;
    registries = [];
    update();
    status.textContent = '未能读取仓库列表，提交后由主服务匹配共享仓库：' + error.message;
    submit.disabled = false;
  });
}

export function createNodeResource(kind) {
  const node = nodeById(activeNode().id);
  const label = kind === 'volume' ? '卷' : 'Bridge 网络';
  const fields = [field('name','名称',{required:true,maxLength:128,pattern:'[a-zA-Z0-9][a-zA-Z0-9_.-]*'})];
  if (kind === 'volume') {
    const type = optionField('type','卷类型',[['local','本地持久卷'],['tmpfs','tmpfs 临时内存卷']],'local');
    const size = field('size','容量上限（MiB）',{type:'number',min:1,max:1048576,step:1,disabled:true});
    const hint = workspaceText('p','tmpfs 数据存放于内存，卸载后清空。','work-note');
    size.hidden = hint.hidden = true;
    type.lastElementChild.addEventListener('change',() => {
      const enabled = type.lastElementChild.value === 'tmpfs';
      size.hidden = hint.hidden = size.lastElementChild.disabled = !enabled;
      size.lastElementChild.required = enabled;
    });
    fields.push(type,size,hint);
  }
  managementDialog('创建' + label + ' · ' + node.label,managedForm(fields,'创建',data => {
    const command = {action:kind + '.create',name:data.get('name')};
    if (data.get('type') === 'tmpfs') command.tmpfs_size_mib = Number(data.get('size'));
    return performOperation(node,command);
  }));
}

export const registriesPath = '/api/v1/registries';

async function listRegistries(previous = [], after = '') {
  if (currentSettingsPath !== registrySettingsPath) return;
  const version = settingsRevision;
  const page = await request(registriesPath + (after ? '?after=' + enc(after) : ''));
  if (version !== settingsRevision) return;
  const items = [...previous, ...(page.items || [])];
  cancelRegistryDiscovery?.();
  const rows = items.map(item => [
    settingsTableEntry(item.name,item.host,'settings-code'),
    settingsTableEntry(item.repository_prefix || '全部镜像',item.has_credential ? '凭证已配置' : '匿名访问'),
    statusPill(item.enabled ? '已启用' : '已停用',item.enabled ? 'good' : ''),
    managementButton('管理', () => registryDetails(item,page.credentials_available))
  ]);
  showSettingsList('镜像仓库',settingsCreateButton('添加仓库',() => registryEditor(undefined,page.credentials_available)),
    ['仓库','镜像范围','状态','操作'],rows,'尚未添加镜像仓库。',
    page.next && managementButton('加载更多',() => listRegistries(items,page.next)));
  const known = new Set(items.map(item => item.host.toLowerCase()));
  const node = optionField('registry-discovery-node','发现来源',[]);
  const select = node.lastElementChild;
  const panel = workspaceText('section','','resource-section registry-discovery');
  panel.setAttribute('aria-label','发现的镜像仓库');
  const results = workspaceText('div','','resource-detail-note');
  const summary = workspaceText('p','','work-note');
  summary.setAttribute('role','status');
  let optionsKey, source, pending;
  let visibleHosts = [];

  refreshRegistryDiscovery = async () => {
    if (!panel.isConnected) return;
    const nodes = [...element('resource-node').options].map(item => [item.value,item.textContent]);
    if (!nodes.length) nodes.push(['@local',workspaceNodeName('@local')]);
    const keys = JSON.stringify(nodes);
    if (keys !== optionsKey) {
      optionsKey = keys;
      const previous = select.value;
      select.replaceChildren();
      for (const [value,label] of nodes) option(select,value,label);
      if (nodes.some(([value]) => value === previous)) select.value = previous;
    }
    const selected = select.value;
    if (source !== selected) {
      pending?.abort();
      pending = undefined;
      source = selected;
      visibleHosts = [];
      results.replaceChildren();
      summary.textContent = '正在读取节点镜像清单…';
    }
    if (pending) return;
    const read = new AbortController();
    pending = read;
    try {
      const snapshot = await request('/api/v1/nodes/' + enc(selected) + '/resources',undefined,undefined,
        AbortSignal.any([read.signal,AbortSignal.timeout(12000)]));
      if (pending !== read || !panel.isConnected) return;
      if (!snapshot.overview?.available) {
        summary.textContent = visibleHosts.length ? '当前清单不可用，保留上次发现结果。' : '等待节点上报镜像清单…';
        return;
      }
      const hosts = discoveredRegistryHosts(snapshot.overview).filter(host => !known.has(host));
      if (hosts.join(',') !== visibleHosts.join(',')) {
        visibleHosts = hosts;
        results.replaceChildren(...hosts.map(host => managementActions(workspaceText('code',host),
          managementButton('添加仓库',() => registryEditor(undefined,page.credentials_available,host)))));
      }
      const message = snapshot.stale ? '正在显示节点的上次快照。' :
        hosts.length ? '随节点清单自动更新。添加私有仓库时请填写凭证。' : '当前清单没有发现待添加的仓库地址。';
      if (summary.textContent !== message) summary.textContent = message;
    } catch (error) {
      if (pending === read && panel.isConnected) summary.textContent = visibleHosts.length ?
        '刷新失败，保留上次发现结果。' : '暂时无法读取节点镜像清单。';
    } finally {
      if (pending === read) pending = undefined;
    }
  };
  cancelRegistryDiscovery = () => {
    pending?.abort();
    pending = undefined;
    refreshRegistryDiscovery = undefined;
    cancelRegistryDiscovery = undefined;
  };
  const discover = actionIcon(managementButton('刷新发现结果',() => refreshRegistryDiscovery?.()),'refresh');
  select.addEventListener('change',() => void refreshRegistryDiscovery?.());
  panel.append(workspaceText('h3','发现的镜像仓库'),managementActions(node,discover),summary,results);
  element('settings-list').append(panel);
  void refreshRegistryDiscovery();
}

function registryEditor(item,credentialsAvailable,discoveredHost = '') {
  const inputs = item ? [] : [
    field('name', '名称', {required:true,maxLength:128,value:discoveredHost}),
    field('host', 'Registry 地址', {required:true,maxLength:255,value:discoveredHost,placeholder:'registry.example.com:5000'}),
    field('repository_prefix', '仓库前缀（可选）', {maxLength:255,placeholder:'team/app'})
  ];
  inputs.push(field('username', '用户名', {maxLength:256,autocomplete:'off'}));
  inputs.push(field('password', '访问令牌或密码', {type:'password',maxLength:4096,autocomplete:'new-password'}));
  const url = item ? registriesPath + '/' + enc(item.id) + '/credential' : registriesPath;
  const help = credentialsAvailable === false ? managementText('主服务未加载加密密钥。请恢复原密钥并重启主服务。' +
    (item ? '' : ' 公开仓库仍可留空用户名和令牌，匿名添加。')) : null;
  managementDialog(item ? '轮换凭证 · ' + item.name : '添加 Registry', ...(help ? [help] : []), managedForm(inputs, '保存', async (data, form) => {
    const body = Object.fromEntries(data);
    form.elements.password.value = '';
    await request(url,body);
    element('management-dialog').close();
    await listRegistries();
    notice(item ? '仓库凭证已更新。' : '已添加共享仓库。');
  }));
}

function registryInfoEditor(item) {
  const inputs = [
    field('name', '名称', {required:true,maxLength:128,value:item.name}),
    field('host', 'Registry 地址', {required:true,maxLength:255,value:item.host,placeholder:'registry.example.com:5000',readOnly:item.has_credential}),
    field('repository_prefix', '仓库前缀（可选）', {maxLength:255,value:item.repository_prefix || '',placeholder:'team/app'})
  ];
  const help = item.has_credential ? managementText('已保存的访问凭证与当前地址加密绑定，地址不可修改；更换地址需新建仓库并保存凭证。') : null;
  managementDialog('编辑信息 · ' + item.name, ...(help ? [help] : []), managedForm(inputs, '保存', async data => {
    await request(registriesPath + '/' + enc(item.id) + '/details',Object.fromEntries(data));
    element('management-dialog').close();
    await listRegistries();
    notice('仓库信息已更新。');
  }));
}

function registryDetails(item,credentialsAvailable) {
  const endpoint = registriesPath + '/' + enc(item.id);
  const buttons = [managementButton('编辑信息', () => registryInfoEditor(item)),
    managementButton('轮换凭证', () => registryEditor(item,credentialsAvailable)),
    managementButton(item.enabled ? '停用' : '启用', async () => {
      if (item.enabled && !window.confirm('停用仓库「' + item.name + '」？本机和所有 Agent 将无法使用此仓库凭证。')) return;
      await request(endpoint + '/enabled',{enabled:!item.enabled});
      element('management-dialog').close();
      await listRegistries();
    })];
  if (item.enabled) buttons.push(managementButton('验证或拉取',() => registryProbe(item)));
  managementDialog('镜像仓库 · ' + item.name,managementText('本机和所有 Agent 共用此仓库配置。'),
    managementText('镜像范围：' + item.host + (item.repository_prefix ? '/' + item.repository_prefix : '')),managementActions(...buttons));
}

function registryProbe(item) {
  const nodes = [...element('resource-node').options].map(option => [option.value,option.textContent]);
  const agent = optionField('agent','执行节点',nodes,activeNode().id);
  managementDialog('验证或拉取 · ' + item.name, managedForm([
    agent,
    field('image','镜像引用',{required:true,maxLength:512,placeholder:item.host + '/' + (item.repository_prefix || 'team/app') + ':tag'}),
    optionField('action','操作',[['registry.verify','验证镜像访问'],['image.pull','拉取镜像']])
  ], '执行', data => {
    const image = data.get('image').trim();
    if (!imageRegistryScopes([item],imageRegistryParts(image)).length) throw new Error('请输入此仓库范围内的镜像引用。');
    return performOperation(nodeById(data.get('agent')),{action:data.get('action'),registry_id:item.id,image});
  }));
}

export const templatesPath = '/api/v1/compose-templates';

async function listTemplates() {
  if (currentSettingsPath !== templateSettingsPath) return;
  const version = settingsRevision;
  const page = await request(templatesPath);
  if (version !== settingsRevision) return;
  const rows = (page.items || []).map(item => {
    const versionCount = item.versions?.length || 0;
    const open = managementButton('选择版本', () => templateDetails(item));
    return [settingsTableEntry(item.name,item.description),workspaceText('span',String(versionCount),'settings-number'),open];
  });
  showSettingsList('Compose 模板',
    settingsCreateButton('创建模板',() => templateEditor()),
    ['模板','版本数','操作'],rows,'尚未创建 Compose 模板。');
}

function templateDetails(item) {
  const rows = (item.versions || []).map(version => [
    version.version, version.digest, dateTime(version.created_at || version.createdAt),
    managementButton('部署此版本', () => templateDeployment(item,version))
  ]);
  managementDialog('Compose 模板 · ' + item.name,
    managementActions(managementButton('上传新版本', () => templateEditor(item))),
    resourceTable(['版本','摘要','创建时间','操作'],rows));
}

export function templateEditor(item, source) {
  const fields = [];
  if (!item) fields.push(
    field('name','模板名称',{required:true,maxLength:128}),
    field('description','说明',{maxLength:512})
  );
  if (source) {
    fields.push(managementText('从部署记录「' + source.deployment + '」读取 Compose。'));
  } else {
    const compose = field('compose','Compose YAML（镜像需使用摘要）',{required:true,maxLength:262144},'textarea');
    const upload = field('upload','或上传 Compose 文件',{type:'file',accept:'.yml,.yaml,.json'});
    upload.lastElementChild.addEventListener('change', async () => {
      const file = upload.lastElementChild.files[0];
      if (!file) return;
      if (file.size > 262144) {
        element('management-status').textContent = 'Compose 文件超过 256 KiB。';
        upload.lastElementChild.value = '';
        return;
      }
      compose.lastElementChild.value = await file.text();
    });
    fields.push(upload,compose);
  }
  const parameters = document.createElement('details');
  const summary = document.createElement('summary');
  summary.textContent = '声明可在部署时填写的参数';
  parameters.append(summary,
    field('ports','端口项，每行填写 服务:索引（从 0 开始）',{placeholder:'web:0',rows:2},'textarea'),
    field('environment','环境变量，每行填写 服务:变量名',{placeholder:'web:APP_URL',rows:2},'textarea'),
    field('volumes','可绑定的卷，每行填写一个 Compose 卷键',{placeholder:'data',rows:2},'textarea'),
    field('networks','可绑定的网络，每行填写一个 Compose 网络键',{placeholder:'frontend',rows:2},'textarea'));
  fields.push(parameters);
  managementDialog(item ? '上传新版本 · ' + item.name : '创建 Compose 模板', managedForm(fields,'保存',async data => {
    const lines = key => String(data.get(key) || '').split(/\r?\n/).map(text => text.trim()).filter(Boolean);
    const pairs = key => lines(key).map(text => {
      const pair = text.split(':').map(part => part.trim());
      if (pair.length !== 2 || !pair[0] || !pair[1]) throw new Error('参数格式不正确：' + text);
      return pair;
    });
    const body = {
      parameters:{
        ports:pairs('ports').map(([service,index]) => {
          if (!/^\d+$/.test(index)) throw new Error('端口索引必须是非负整数。');
          return {service,port:Number(index)};
        }),
        environment:pairs('environment').map(([service,key]) => ({service,key})),
        volumes:lines('volumes').map(key => ({key})),
        networks:lines('networks').map(key => ({key}))
      }
    };
    if (!item) {
      body.name = data.get('name');
      body.description = data.get('description');
    }
    if (source) body.source = source;
    else body.compose = data.get('compose');
    await request(item ? templatesPath + '/' + enc(item.id) + '/versions' : templatesPath,body);
    element('management-dialog').close();
    await listTemplates();
    notice('模板版本已保存。');
  }));
}

export function templateDeployment(item, version, adoption) {
  const adoptionNode = adoption?.node;
  if (!projects.length) throw new Error('请先创建项目和部署目标。');
  const projectField = optionField('project','项目',projects.map(project => [project.id,project.id]),selected?.id);
  const targetField = optionField('target','部署目标与节点',[]);
  const targetSelect = targetField.lastElementChild;
  const projectSelect = projectField.lastElementChild;
  const fields = [projectField,targetField];
  const params = version.parameters || {};
  const ports = params.ports || [];
  const environment = params.environment || [];
  const volumes = params.volumes || [];
  const networks = params.networks || [];
  for (const [index,item] of ports.entries()) fields.push(field('port-' + index,`${item.service} 的端口映射`,{value:item.value || '',maxLength:128}));
  for (const [index,item] of environment.entries()) fields.push(field('environment-' + index,`${item.service} · ${item.key}`,{maxLength:4096,autocomplete:'off'}));
  const externalInputs = [];
  for (const [kind,resources,label] of [['volumes',volumes,'已有卷'],['networks',networks,'已有网络']]) {
    for (const [index,resource] of resources.entries()) {
      const key = kind === 'volumes' ? 'volume' : 'network';
      const wrapper = optionField(key + '-' + index,label + ' · ' + resource.key,[]);
      const select = wrapper.lastElementChild;
      select.required = !!resource.required;
      select.dataset.resourceKind = kind;
      const expected = adoption?.[kind]?.[resource.key];
      if (expected) select.dataset.adoptedResource = expected;
      externalInputs.push(select);
      fields.push(wrapper);
    }
  }
  const defaultHint = '环境变量请填写非敏感配置，应用密钥通过部署目标绑定。';
  const hint = managementText(defaultHint);
  const deploymentEndpoint = templatesPath + '/' + enc(item.id) + '/versions/' + enc(version.version);
  let binding = null;
  function payload(data) {
    const changed = values => values.filter(value => value.value !== '' && value.name !== '' && value.value !== null && value.name !== null);
    const body = {
      project:data.get('project'), target:data.get('target'),
      ports:changed(ports.map((p,i) => ({service:p.service,port:p.port,value:data.get('port-' + i)}))),
      environment:changed(environment.map((p,i) => ({service:p.service,key:p.key,value:data.get('environment-' + i)}))),
      volumes:changed(volumes.map((p,i) => ({key:p.key,name:data.get('volume-' + i)}))),
      networks:changed(networks.map((p,i) => ({key:p.key,name:data.get('network-' + i)})))
    };
    if (binding && (body.volumes.length || body.networks.length)) body.node = binding;
    return body;
  }
  let validated = null;
  let revision = 0;
  const form = managedForm([...fields,hint],'申请部署',async data => {
    const body = payload(data);
    if (validated?.body !== JSON.stringify(body)) throw new Error('请先校验当前配置。');
    let response;
    try {
      response = await submitDeployment(deploymentEndpoint + '/deploy',{...body,snapshot_digest:validated.digest});
    } catch (error) {
      refreshOnConflict(error);
      throw error;
    }
    element('management-dialog').close();
    if (selected?.id === body.project) {
      await loadDeployments();
      if (response?.id) {
        await openDeployment(response.id);
        if (currentSettingsPath === templateSettingsPath) goToConsoleView('console');
      }
    }
    notice(response?.id ? '部署申请已受理：' + response.id : '部署申请已受理。');
  },() => !!validated);
  const submit = form.querySelector('button[type=submit]');
  const isCurrent = () => form.isConnected && element('management-dialog').open;
  function invalidate() {
    revision++;
    submit.disabled = true;
    validated = null;
    if (isCurrent()) managementResult('');
  }
  function refreshOnConflict(error) {
    if (error.status !== 409 || !isCurrent()) return;
    void updateBindings(true);
    error.message = '目标节点或配置已变化，请重新校验并预览。';
  }
  const preview = managementButton('校验并预览',async () => {
    if (!targetSelect.value) throw new Error('请先选择部署目标。');
    if (adoptionNode && !binding) throw new Error('请先核对所选部署目标绑定到原节点。');
    if (externalInputs.some(input => input.required && input.disabled)) throw new Error('请先读取部署节点的卷和网络。');
    if (!form.reportValidity()) return;
    const body = payload(new FormData(form));
    invalidate();
    const currentRevision = revision;
    let draft;
    try {
      draft = await request(deploymentEndpoint + '/preview',body);
    } catch (error) {
      if (!isCurrent() || currentRevision !== revision) return;
      refreshOnConflict(error);
      throw error;
    }
    if (!isCurrent() || currentRevision !== revision || JSON.stringify(payload(new FormData(form))) !== JSON.stringify(body)) return;
    if (!draft.snapshot_digest) throw new Error('预览缺少部署摘要，请重新校验。');
    validated = {body:JSON.stringify(body),digest:draft.snapshot_digest};
    let compose = draft.compose;
    if (typeof compose === 'string') {
      try { compose = JSON.parse(compose); } catch { /* YAML is shown as text. */ }
    }
    managementResult(compose, '校验通过 · ' + draft.snapshot_digest);
    submit.disabled = false;
  });
  preview.id = 'template-preview';
  form.insertBefore(preview,submit);
  form.addEventListener('input',invalidate);
  managementDialog('部署 ' + item.name + ' / ' + version.version, form);
  invalidate();
  let choiceVersion = 0;
  function clearBindings() {
    binding = null;
    hint.textContent = defaultHint;
    for (const input of externalInputs) {
      input.replaceChildren();
      option(input,'','等待节点资源');
      input.disabled = true;
    }
    invalidate();
  }

  async function updateBindings(keepInputs = false) {
    const current = ++choiceVersion;
    const previous = binding;
    const choices = new Map(externalInputs.map(input => [input,input.value]));
    clearBindings();
    if (!targetSelect.value) return;
    let assigned;
    try {
      assigned = await request('/api/v1/projects/' + enc(projectSelect.value) + '/targets/' + enc(targetSelect.value) + '/node');
    } catch (error) {
      if (isCurrent() && current === choiceVersion) hint.textContent = '无法核对目标节点 · ' + error.message;
      return;
    }
    if (!isCurrent() || current !== choiceVersion) return;
    binding = {agent:assigned.agent,generation:assigned.generation};
    if (adoptionNode && binding.agent !== adoptionNode.id) {
      hint.textContent = '请选用绑定到 ' + adoptionNode.label + ' 的部署目标，以沿用已核对的资源。';
      binding = null;
      return;
    }
    hint.textContent = '目标节点：' + nodeById(binding.agent).label + '。镜像凭证来自匹配的共享仓库，应用密钥通过部署目标绑定。';
    if (!externalInputs.length) return;
    try {
      const report = await request('/api/v1/nodes/' + enc(assigned.agent) + '/resources');
      if (!isCurrent() || current !== choiceVersion) return;
      if (report.generation !== assigned.generation || !report.overview?.available) {
        hint.textContent += ' 暂无此节点的可用卷和网络快照。';
        return;
      }
      const preserve = keepInputs && previous?.agent === assigned.agent && previous?.generation === assigned.generation;
      for (const select of externalInputs) {
        const kind = select.dataset.resourceKind;
        const names = (report.overview[kind] || []).filter(resource => {
          if (resource.scope && resource.scope !== 'local') return false;
          if (kind === 'volumes') return resource.driver === 'local';
          return resource.driver === 'bridge' && !['bridge','host','none'].includes(resource.name);
        }).map(resource => resource.name);
        select.replaceChildren();
        option(select,'',select.required ? '选择节点资源' : '使用 Compose 声明');
        for (const [name,label] of resourceChoiceOptions(names).slice(1)) option(select,name,label);
        select.disabled = false;
        if (preserve && names.includes(choices.get(select))) select.value = choices.get(select);
        else if (!preserve && names.includes(select.dataset.adoptedResource)) select.value = select.dataset.adoptedResource;
      }
    } catch (error) {
      if (isCurrent() && current === choiceVersion) hint.textContent += ' 无法读取卷和网络 · ' + error.message;
    }
  }
  async function updateTargets() {
    const current = ++choiceVersion;
    const targetProject = projectSelect.value;
    targetSelect.replaceChildren();
    targetSelect.disabled = true;
    clearBindings();
    try {
      let after = '';
      do {
        const page = await request('/api/v1/projects/' + enc(targetProject) + '/targets?after=' + enc(after));
        if (!isCurrent() || current !== choiceVersion) return;
        for (const target of page.items || []) option(targetSelect,target.id,target.id);
        after = page.next;
      } while (after && targetSelect.length < 200);
      if (targetProject === selected?.id && [...targetSelect.options].some(option => option.value === element('target').value)) {
        targetSelect.value = element('target').value;
      }
      if (targetSelect.length) {
        targetSelect.disabled = false;
        await updateBindings();
      } else hint.textContent = '项目没有可用的部署目标。';
    } catch (error) {
      if (isCurrent() && current === choiceVersion) hint.textContent = '读取部署目标失败：' + error.message;
    }
  }
  projectSelect.addEventListener('change', updateTargets);
  targetSelect.addEventListener('change', () => updateBindings());
  void updateTargets();
}
