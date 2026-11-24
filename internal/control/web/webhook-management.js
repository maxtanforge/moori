import {managementActions,workspaceText,field,option,row} from './ui-components.js';
import {webhookSettingsPath,currentSettingsPath,settingsRevision,managementButton,settingsTableEntry,showSettingsList,settingsCreateButton,managementText,managedForm,registriesPath,loadSystemSettings} from './management.js';
import {workspaceNodeName} from './node-workspace.js';
import {splitStandaloneImage} from './container-maintenance.js';
import {element,request,enc} from './api.js';
import {platformAdmin,goToConsoleView,currentRoute,localExecution,agents} from './app.js';

const containerWebhooksPath = '/api/v1/container-webhooks';
let webhookListRequest = 0;
let webhookDraft;

export function openContainerWebhookSettings(node, container, imageReference) {
  if (!platformAdmin || element('console').hidden) return;
  webhookDraft = {node,container,repository:containerWebhookRepository(imageReference)};
  try {
    goToConsoleView(webhookSettingsPath);
    if (webhookDraft && currentSettingsPath === webhookSettingsPath) editContainerWebhook(webhookDraft);
  } finally {
    webhookDraft = undefined;
  }
}

export function containerWebhookRepository(imageReference) {
  if (!imageReference) return '';
  const [host,image] = splitStandaloneImage(imageReference);
  if (!/^(?:localhost|[a-z0-9]+(?:[.-][a-z0-9]+)+)(?::[0-9]+)?$/.test(host)) return '';
  const match = /^([a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:\/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*)(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(?:@sha256:[a-f0-9]{64})?$/.exec(image);
  if (!match) return '';
  const repository = host === 'docker.io' && !match[1].includes('/') ? 'library/' + match[1] : match[1];
  const value = host + '/' + repository;
  return value.length <= 310 ? value : '';
}

export async function listContainerWebhooks(after = '', history = []) {
  if (currentSettingsPath !== webhookSettingsPath) return;
  if (webhookDraft) {
    const draft = webhookDraft;
    webhookDraft = undefined;
    editContainerWebhook(draft);
    return;
  }
  const revision = settingsRevision;
  const load = ++webhookListRequest;
  const page = await request(containerWebhooksPath + (after ? '?after=' + enc(after) : ''));
  if (revision !== settingsRevision || load !== webhookListRequest) return;

  const reopenList = () => listContainerWebhooks(after,history);
  const rows = (page.items || []).map(hook => {
    const revoke = managementButton('撤销',() => confirmContainerWebhookRevocation(hook,revoke,reopenList),true);
    revoke.setAttribute('aria-label','撤销 · ' + hook.container + ' · ' + hook.id);
    return [
      settingsTableEntry(hook.container,hook.id),
      settingsTableEntry(workspaceNodeName(hook.node),hook.node + ' · 第 ' + hook.generation + ' 代'),
      settingsTableEntry(hook.repository,hook.registry_id ? (hook.registry_name || '已指定共享仓库凭证') : '自动匹配共享仓库凭证'),
      revoke
    ];
  });
  showSettingsList('Webhook 管理',settingsCreateButton('新建 Webhook',editContainerWebhook),
    ['容器','节点','镜像仓库','操作'],rows,'暂无容器发布 Webhook。',
    history.length && managementButton('上一页',() => listContainerWebhooks(history.at(-1),history.slice(0,-1))),
    page.next && managementButton('下一页',() => listContainerWebhooks(page.next,[...history,after])));
  if (!rows.length && history.length) {
    element('settings-list').append(managementActions(managementButton('上一页',
      () => listContainerWebhooks(history.at(-1),history.slice(0,-1)))));
  }
}

function confirmContainerWebhookRevocation(hook, trigger, reopenList) {
  const panel = element('settings-list');
  panel.querySelector('#webhook-revoke-confirmation')?.remove();
  const confirmation = workspaceText('section','','webhook-revoke-confirmation user-settings-editor');
  confirmation.id = 'webhook-revoke-confirmation';
  const title = workspaceText('h3','撤销 Webhook · ' + hook.container);
  title.id = 'webhook-revoke-title';
  confirmation.setAttribute('aria-labelledby',title.id);
  const status = workspaceText('p','','user-settings-status');
  status.setAttribute('role','status');
  const cancel = managementButton('取消',() => {
    confirmation.remove();
    trigger.focus();
  });
  const revoke = managementButton('确认撤销',async () => {
    status.textContent = '';
    cancel.disabled = true;
    try {
      await request(containerWebhooksPath + '/' + enc(hook.id),undefined,undefined,undefined,'DELETE');
      if (currentSettingsPath !== webhookSettingsPath || !trigger.isConnected) return;
      await reopenList();
    } finally {
      cancel.disabled = false;
    }
  },true,status);
  confirmation.append(title,managementText('撤销后，此 URL 和令牌将失效，尚在排队的发布将取消。已开始的发布可能继续运行。'),managementActions(cancel,revoke),status);
  const heading = panel.querySelector('.settings-table-heading');
  heading.after(confirmation);
  cancel.focus();
}

function editContainerWebhook(draft = {}) {
  const editor = ++webhookListRequest;
  const revision = settingsRevision;
  const panel = element('settings-list');
  const bar = workspaceText('header','','work-panel-header');
  bar.append(workspaceText('h2','新建容器发布 Webhook'),managementButton('返回 Webhook 列表',() => listContainerWebhooks()));
  const body = workspaceText('div','','user-settings-editor webhook-settings-editor');
  const status = workspaceText('p','','user-settings-status');
  status.setAttribute('role','status');
  const nodeField = field('node','节点',{required:true},'select');
  const node = nodeField.querySelector('select');
  option(node,'','请选择节点');
  const nameInput = {autocomplete:'off',maxLength:128,pattern:'[A-Za-z0-9][A-Za-z0-9_.\\-]{0,127}'};
  const repository = field('repository','镜像仓库',{required:true,autocomplete:'off',maxLength:310,placeholder:'docker.io/library/nginx',value:draft.repository || ''});
  repository.querySelector('input').setAttribute('aria-describedby','webhook-repository-hint');
  const repositoryHint = workspaceText('p','填写带注册表域名的完整仓库路径，无需添加标签或摘要。');
  repositoryHint.id = 'webhook-repository-hint';
  const privateScope = document.createElement('details');
  const registryField = field('registry_id','共享仓库凭证',{disabled:true},'select');
  const registryScope = registryField.querySelector('select');
  const registryStatus = workspaceText('p','','webhook-node-status');
  registryStatus.setAttribute('role','status');
  privateScope.append(workspaceText('summary','私有仓库凭证（可选）'),registryField,registryStatus);
  let registries;
  let readingRegistries = false;

  let working = false;
  const canSubmit = () => !working && Number(node.selectedOptions[0]?.dataset.generation) > 0;
  const form = managedForm([nodeField,field('container','容器名称',{...nameInput,required:true,value:draft.container || ''}),
    repository,repositoryHint,privateScope],'创建 Webhook',async (data,form) => {
    const binding = {
      node:data.get('node'),generation:Number(node.selectedOptions[0]?.dataset.generation),
      container:data.get('container').trim(),repository:data.get('repository').trim(),
      ...(data.get('registry_id') ? {registry_id:data.get('registry_id')} : {})
    };
    working = true;
    refresh.disabled = true;
    try {
      const credential = await request(containerWebhooksPath,binding);
      if (editor !== webhookListRequest || revision !== settingsRevision || !form.isConnected ||
          currentRoute() !== webhookSettingsPath) return;
      showContainerWebhookCredential(credential);
    } finally {
      working = false;
      refresh.disabled = false;
    }
  },canSubmit,status);
  form.id = 'webhook-create-form';
  form.method = 'post';
  const submit = form.querySelector('button[type=submit]');
  submit.disabled = true;
  node.addEventListener('change',() => { submit.disabled = !canSubmit(); });
  repository.querySelector('input').addEventListener('input',updateRegistryScope);
  privateScope.addEventListener('toggle',() => {
    if (privateScope.open && !registries && !readingRegistries) void loadRegistries();
  });
  const nodeStatus = workspaceText('p','正在读取节点…','webhook-node-status');
  nodeStatus.setAttribute('role','status');
  const refresh = managementButton('重新读取节点',loadNodes,false,nodeStatus);
  refresh.hidden = true;
  body.append(nodeStatus,refresh,form,status);
  panel.replaceChildren(bar,body);
  node.focus();
  void loadNodes();

  async function loadNodes() {
    const selection = node.options.length > 1 ? node.value : draft.node;
    const options = new Map();
    if (localExecution) options.set('@local',{generation:1,display_name:workspaceNodeName('@local')});
    let after = '';
    let failed = false;
    try {
      do {
        const page = await request('/api/v1/agents' + (after ? '?after=' + enc(after) : ''));
        if (revision !== settingsRevision || !form.isConnected) return;
        if (localExecution && page.local_display_name) options.get('@local').display_name = page.local_display_name;
        for (const agent of page.items || []) {
          if (agent.id !== '@local' && Number(agent.generation) > 0) options.set(agent.id,agent);
        }
        after = page.next || '';
      } while (after);
    } catch {
      failed = true;
      for (const item of element('resource-node').options) {
        if (item.value !== '@local' && Number(item.dataset.generation) > 0 && !options.has(item.value)) {
          options.set(item.value,{generation:item.dataset.generation,display_name:item.textContent});
        }
      }
    }
    if (revision !== settingsRevision || !form.isConnected) return;
    while (node.options.length > 1) node.remove(1);
    for (const [id,agent] of options) {
      option(node,id,agent.display_name && agent.display_name !== id && id !== '@local' ?
        agent.display_name + ' · ' + id : agent.display_name || id);
      node.lastElementChild.dataset.generation = agent.generation;
    }
    node.value = options.has(selection) ? selection : '';
    submit.disabled = !canSubmit();
    refresh.hidden = !failed && options.size > 0;
    nodeStatus.textContent = failed ? (options.size ? '节点列表更新失败，已显示可用的节点记录。' : '无法读取节点。') :
      (options.size ? '' : '暂无可绑定的节点。');
    nodeStatus.hidden = !nodeStatus.textContent;
  }

  async function loadRegistries() {
    readingRegistries = true;
    registryStatus.textContent = '正在读取共享仓库凭证…';
    try {
      const items = [];
      let after = '';
      do {
        const page = await request(registriesPath + (after ? '?after=' + enc(after) : ''));
        if (revision !== settingsRevision || !form.isConnected) return;
        items.push(...(page.items || []));
        after = page.next || '';
      } while (after);
      registries = items;
      updateRegistryScope();
    } catch {
      if (form.isConnected) registryStatus.textContent = '读取共享仓库凭证失败。重新展开可重试。';
    } finally {
      readingRegistries = false;
    }
  }

  function updateRegistryScope() {
    if (!registries) return;
    const previous = registryScope.value;
    const [host,...path] = repository.querySelector('input').value.trim().split('/');
    const name = path.join('/');
    registryScope.replaceChildren();
    option(registryScope,'','自动匹配共享仓库凭证');
    for (const registry of registries) {
      const prefix = registry.repository_prefix || '';
      if (!registry.enabled || host !== registry.host || !name ||
          prefix && name !== prefix && !name.startsWith(prefix + '/')) continue;
      option(registryScope,registry.id,registry.name);
    }
    registryScope.disabled = registryScope.options.length < 2;
    if ([...registryScope.options].some(item => item.value === previous)) registryScope.value = previous;
    registryStatus.textContent = registryScope.disabled ? '没有匹配的共享仓库凭证。' : '';
  }
}

function showContainerWebhookCredential(credential) {
  const panel = element('settings-list');
  const returnToList = () => {
    const token = element('webhook-token');
    if (token) token.value = '';
    return loadSystemSettings();
  };
  const title = workspaceText('h2','Webhook 已创建');
  title.tabIndex = -1;
  const bar = workspaceText('header','','work-panel-header');
  bar.append(title,managementButton('返回 Webhook 列表',returnToList));
  const body = workspaceText('div','','user-settings-editor webhook-settings-editor');
  body.id = 'webhook-credential';
  const status = workspaceText('p');
  status.setAttribute('role','status');
  const url = new URL('/api/v1/webhooks/containers/' + enc(credential.id),location.origin).href;
  body.append(
    managementText('令牌仅在此显示一次。请复制并妥善保管。'),
    containerWebhookCopyField('url','URL',url,status),
    containerWebhookCopyField('token','令牌',credential.token,status),
    status
  );
  panel.replaceChildren(bar,body);
  title.focus();
}

function containerWebhookCopyField(key, label, value, status) {
  const fieldLabel = field('','Webhook ' + label,{id:'webhook-' + key,readOnly:true,autocomplete:'off'});
  const input = fieldLabel.querySelector('input');
  input.value = value;
  const copy = managementButton('复制 ' + label,async () => {
    try {
      if (navigator.clipboard) {
        await navigator.clipboard.writeText(input.value);
        status.textContent = '已复制 ' + label + '。';
        return;
      }
    } catch {}
    if (!input.isConnected) return;
    input.focus();
    input.select();
    status.textContent = '已选中 ' + label + '，请使用复制快捷键。';
  },false,status);
  copy.id = 'copy-webhook-' + key;
  const row = workspaceText('div','','webhook-credential-row');
  row.append(fieldLabel,copy);
  return row;
}

window.addEventListener('pagehide',() => {
  webhookListRequest++;
  const token = element('webhook-token');
  if (!token) return;
  token.value = '';
  element('webhook-credential').replaceChildren(managementText('令牌仅在创建后显示一次。'));
});
