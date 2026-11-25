import {workspaceText,managementActions,workspaceLink,statusPill,option,optionField,snapshotFacts,actionIcon,field,row,download,formatBytes} from './ui-components.js';
import {managementDialog,dialogOperationSurface,maintenanceOperationButton,managementButton,nodeOperationPath,registriesPath,managedForm,performOperation,reportOperation,nodeById,activeNode} from './management.js';
import {nodePath,renderWorkspaceBody,assertNodeManagementReady} from './node-workspace.js';
import {lockEditorForm} from './maintenance-page.js';
import {splitStandaloneImage} from './container-maintenance.js';
import {enc,request,element,notice} from './api.js';
import {detail,action,selected,accountEpoch,refreshAfterNodeMutation} from './app.js';

const maxImageArchiveBytes = 8 * 1024 ** 3;
let imageDetailEditor;

export function discoveredRegistryHosts(view) {
  const refs = [
    ...(view?.containers || []).map(item => item.image),
    ...(view?.images || []).flatMap(item => [...(item.tags || []),...(item.digests || [])])
  ];
  const hosts = new Set();
  for (const ref of refs) {
    if (!ref) continue;
    const [host] = splitStandaloneImage(ref);
    if (host !== 'docker.io' && host !== '@local' && /^[a-z0-9.-]+(?::[0-9]+)?$/i.test(host)) {
      hosts.add(host.toLowerCase());
    }
  }
  return [...hosts].sort();
}

function imageActionSurface(node,title,...content) {
  const status = workspaceText('p','','image-operation-status');
  status.setAttribute('role','status');
  managementDialog(title + ' · ' + node.label,...content,status);
  return {...dialogOperationSurface(),status};
}

export function imageManagementActions(node,item,detail = false) {
  const imageID = item.full_id || item.id;
  if (!/^sha256:[0-9a-f]{64}$/.test(imageID)) return null;
  const remove = maintenanceOperationButton('删除',node,item.tags?.[0] || imageID,
    {action:'image.delete',image_id:imageID},'确认删除此镜像？删除后需要重新拉取或导入才能使用。执行前会重新检查容器引用。',true);
  remove.disabled = item.container_count !== 0;
  remove.title = item.container_count > 0 ? '镜像被容器使用，无法删除（包括已停止的容器）' :
    item.container_count === 0 ? '删除未使用的镜像' : '使用状态未知，请刷新或更新节点后重试';
  return managementActions(
    managementButton('导出',() => openImageExport(node,imageID)),
    !detail && workspaceLink(nodePath(node.id,'images',imageID),'管理标签','editor-link'),remove
  );
}

export function imageUsage(item) {
  const status = workspaceText('span','','image-usage');
  if (item.container_count > 0) {
    status.append(statusPill('使用中（' + item.container_count + ' 个容器）','good'));
  } else if (item.container_count === 0) {
    status.append(statusPill('未使用'));
  } else {
    status.append(statusPill('使用状态未知','warn'),workspaceText('small','刷新快照后重试'));
  }
  return status;
}

function imageTagParts(text) {
  if (text.includes('@')) return null;
  const [host,path] = splitStandaloneImage(text);
  const colon = path.lastIndexOf(':');
  if (host === '@local' || colon <= path.lastIndexOf('/') || colon <= 0 || colon === path.length - 1) return null;
  return imageRegistryParts(text);
}

async function imageOperationResult(node,initial,surface) {
  const deadline = Date.now() + 45000;
  const url = nodeOperationPath(node) + '/' + enc(initial.id);
  let job = initial;
  while (surface.visible() && ['queued','running'].includes(job.state) && Date.now() < deadline) {
    await new Promise(resolve => setTimeout(resolve,1000));
    if (surface.visible()) job = await request(url);
  }
  if (!surface.visible()) return;
  if (job.state !== 'succeeded') throw new Error(job.error || '未能确认节点操作结果，请重试。');
  return job.result;
}

export async function imageRegistryChoices(isCurrent) {
  const choices = [];
  let after = '';
  do {
    const response = await request(registriesPath + (after ? '?after=' + enc(after) : ''));
    if (!isCurrent()) return [];
    choices.push(...(response.items || []));
    if (!response.next || response.next === after) break;
    after = response.next;
  } while (after);
  return choices;
}

export function imageRegistryParts(reference) {
  const [host,path] = splitStandaloneImage(reference.trim());
  const name = path.split('@')[0].replace(/:[^/]*$/, '');
  if (!name || host === '@local') return null;
  return {host:host.toLowerCase(),repository:host === 'docker.io' && !name.includes('/') ? 'library/' + name : name};
}

export function imageRegistryScopes(registries,image) {
  if (!image) return [];
  const {host,repository} = image;
  return registries.filter(item => item.enabled && item.host === host &&
    (!item.repository_prefix || repository === item.repository_prefix || repository.startsWith(item.repository_prefix + '/')))
    .sort((a,b) => (b.repository_prefix || '').length - (a.repository_prefix || '').length);
}

export function registryScopeOptions(select,registries,emptyLabel = '请选择仓库') {
  const previous = select.value;
  select.replaceChildren();
  option(select,'',emptyLabel);
  const labels = registries.map(item => item.name + ' · ' + item.host + (item.repository_prefix ? '/' + item.repository_prefix : ''));
  const counts = new Map();
  for (const label of labels) counts.set(label,(counts.get(label) || 0) + 1);
  for (const [index,item] of registries.entries()) {
    const label = labels[index];
    option(select,item.id,counts.get(label) === 1 ? label : label + ' · ' + item.id);
  }
  const [first,second] = registries;
  const tied = second && (first.repository_prefix || '') === (second.repository_prefix || '');
  const automatic = first && !tied ? first.id : '';
  const keepChoice = previous !== select.dataset.automatic && registries.some(item => item.id === previous);
  select.value = keepChoice ? previous : automatic;
  select.dataset.automatic = automatic;
}

function openImagePush(node,imageID,reference) {
  const authorization = optionField('scope','镜像仓库',[['','请选择仓库']]);
  const available = workspaceText('p','正在读取共享仓库…','work-note');
  const status = workspaceText('p','','image-operation-status');
  status.setAttribute('role','status');
  let registries;
  let scopes = [];
  let surface;
  function updateScopes() {
    if (!registries) return;
    scopes = imageRegistryScopes(registries,imageTagParts(reference));
    registryScopeOptions(authorization.lastElementChild,scopes);
    available.textContent = scopes.length ? '将所选本地标签推送到此镜像仓库。' : '没有匹配此标签的已启用仓库。';
  }
  const form = managedForm([snapshotFacts([['本地标签',reference]]),authorization,available],'确认推送',async data => {
    const registry = scopes.find(item => item.id === data.get('scope'));
    if (!registry) throw new Error('请选择镜像仓库。');
    const unlock = lockEditorForm(form);
    try {
      await performOperation(node,{action:'image.push',image_id:imageID,image:reference,registry_id:registry.id},nodeOperationPath(node),surface);
    } finally {
      if (surface.visible()) unlock();
    }
  },() => scopes.length > 0,status);
  surface = imageActionSurface(node,'推送镜像',form,status);
  const loadScopes = () => {
    imageRegistryChoices(surface.visible).then(result => {
      if (!surface.visible()) return;
      registries = result;
      updateScopes();
    }).catch(error => {
      if (!surface.visible()) return;
      available.replaceChildren(workspaceText('span','读取共享仓库失败：' + error.message),
        managementButton('重试',loadScopes));
    });
  };
  loadScopes();
}

export function clearImageDetailEditor() { imageDetailEditor = undefined; }

export function buildImageTagPanels(node,item,imageID,manageable = true) {
  if (!/^sha256:[0-9a-f]{64}$/.test(imageID)) return [];
  const key = JSON.stringify([accountEpoch,node.id,node.generation,imageID]);
  if (imageDetailEditor?.key !== key) clearImageDetailEditor();
  if (manageable && !imageDetailEditor) {
    imageDetailEditor = createImageDetailEditor(node,imageID);
  }
  const {panel,added = new Map()} = imageDetailEditor || {};
  const tags = new Map();
  const tagIdentity = tag => {
    const parts = imageTagParts(tag);
    return parts ? `${parts.host}/${parts.repository}:${tag.split(':').at(-1)}` : tag;
  };
  for (const tag of item.tags || []) {
    const key = tagIdentity(tag);
    added.delete(key);
    tags.set(key,tag);
  }
  const list = workspaceText('ul','','image-tag-list');
  for (const [key,tag] of [...tags,...added]) {
    const row = workspaceText('li');
    row.append(workspaceText('code',tag));
    if (manageable && imageTagParts(tag)) {
      const button = actionIcon(managementButton('推送',() => openImagePush(node,imageID,tag)),'upload','推送 ' + tag);
      button.dataset.resourceFocus = 'image-tag:' + key;
      row.append(button);
    }
    list.append(row);
  }
  const tagPanel = workspaceText('section','','image-tags-panel');
  const heading = workspaceText('h3','镜像标签');
  tagPanel.append(heading,list.childElementCount ? list : workspaceText('p','此镜像暂无标签。','work-note'));
  return manageable ? [tagPanel,panel] : [tagPanel];
}

function createImageDetailEditor(node,imageID) {
  const panel = workspaceText('section','','image-tag-editor');
  const form = workspaceText('form');
  const hostField = optionField('host','镜像源',[['docker.io','Docker Hub（docker.io）']]);
  const referenceField = field('image','镜像路径与标签',{required:true,maxLength:384,placeholder:'namespace/image:tag'});
  const status = workspaceText('p','','image-operation-status');
  status.setAttribute('role','status');
  const source = hostField.lastElementChild;
  const reference = referenceField.lastElementChild;
  const address = workspaceText('span','docker.io','image-tag-host');
  address.setAttribute('aria-hidden','true');
  const path = workspaceText('div','','image-tag-reference');
  path.append(address,reference);
  referenceField.append(path);
  const add = workspaceText('button','添加标签');
  add.type = 'submit';
  add.disabled = !node.generation;
  const hint = workspaceText('p','原有标签会保留。添加后可从标签旁的图标发起推送。','work-note');
  form.append(hostField,referenceField,managementActions(add),hint,status);
  panel.append(workspaceText('h3','添加镜像标签'),form);
  const added = new Map();
  const editor = {key:JSON.stringify([accountEpoch,node.id,node.generation,imageID]),panel,added};
  const surface = {visible:() => imageDetailEditor === editor && panel.isConnected};
  let busy = false;
  for (const [index,control] of [...form.elements].entries()) control.dataset.resourceFocus = 'image-label-editor:' + index;
  const destination = () => source.value + '/' + reference.value.trim();
  source.addEventListener('change',() => { address.textContent = source.value; });
  imageRegistryChoices(() => imageDetailEditor === editor).then(items => {
    if (imageDetailEditor !== editor) return;
    const hosts = new Set(['docker.io']);
    for (const {host,name,enabled} of items) if (enabled && !hosts.has(host)) {
      option(source,host,`${name}（${host}）`);
      hosts.add(host);
    }
  }).catch(error => {
    if (surface.visible()) hint.textContent = '未能读取镜像源，仅可添加 Docker Hub 标签：' + error.message;
  });

  form.addEventListener('submit',async event => {
    event.preventDefault();
    if (busy || !node.generation || !surface.visible()) return;
    const image = destination();
    const parts = imageTagParts(image);
    if (!parts) { status.textContent = '请输入镜像路径和明确的标签，例如 namespace/image:tag。'; return; }
    busy = true;
    const unlock = lockEditorForm(form);
    const operation = async command => imageOperationResult(node,
      await request(nodeOperationPath(node),{...command,generation:node.generation}),surface);
    let tagReady = false;
    try {
      status.textContent = '正在检查目标标签…';
      const preview = await operation({action:'image.tag-preview',image_id:imageID,image});
      if (!preview || !surface.visible()) return;
      if (preview.current_image_id && preview.current_image_id !== imageID) throw new Error('该标签已指向其他镜像，请换一个标签。');
      if (!preview.current_image_id) {
        status.textContent = '正在添加标签…';
        if (!await operation({action:'image.tag',image_id:imageID,image:preview.image,expected_image_id:''})) return;
      }
      if (!surface.visible()) return;
      tagReady = true;
      added.set(`${parts.host}/${parts.repository}:${image.split(':').at(-1)}`,preview.image);
      renderWorkspaceBody();
      if (surface.visible()) status.textContent = '标签已添加。';
    } catch (error) {
      if (surface.visible()) status.textContent = error.message;
    } finally {
      busy = false;
      unlock();
      if (tagReady && surface.visible()) void refreshAfterNodeMutation(node.id,['images']);
    }
  });
  return editor;
}

function imageTransferPath(node,job) {
  return '/api/v1/nodes/' + enc(node.id) + '/image-transfers/' + enc(job.id);
}

function requireImageTransfer(node) {
  if (!node.generation) throw new Error('无法获取节点代次，请先刷新节点列表。');
  if (node.id !== '@local' && location.protocol !== 'https:') throw new Error('远程镜像档案需要通过加密网关访问。');
}

function openImageExport(node,imageID) {
  requireImageTransfer(node);
  let surface;
  const button = managementButton('下载 .tar',async () => {
    assertNodeManagementReady(node.id);
    surface.status.textContent = '正在准备下载…';
    let job;
    try {
      job = await request(nodeOperationPath(node),{action:'image.export',image_id:imageID,generation:node.generation});
    } catch (error) {
      if (surface.visible()) surface.status.textContent = error.message;
      return;
    }
    if (!surface.visible()) return;
    const download = document.createElement('a');
    download.href = imageTransferPath(node,job);
    download.download = 'docker-image.tar';
    document.body.append(download);
    download.click();
    download.remove();
    await reportOperation(node,job,surface);
  });
  button.classList.add('primary');
  surface = imageActionSurface(node,'导出镜像',workspaceText('p','浏览器会将镜像下载为 Docker 镜像档案。','work-note'),button);
}

function imageArchiveUpload(path,file,signal,progress) {
  return new Promise((resolve,reject) => {
    if (signal.aborted) {
      reject(signal.reason);
      return;
    }
    const upload = new XMLHttpRequest();
    upload.open('POST',path);
    upload.setRequestHeader('Content-Type','application/x-tar');
    upload.timeout = 15 * 60 * 1000;
    const stop = () => upload.abort();
    signal.addEventListener('abort',stop,{once:true});
    upload.addEventListener('loadend',() => signal.removeEventListener('abort',stop),{once:true});
    upload.upload.addEventListener('progress',event => progress(event.loaded));
    upload.addEventListener('load',() => {
      if (upload.status === 204) resolve();
      else {
        const errors = {
          403:'当前会话无法访问此镜像传输。',
          409:'镜像传输已失效或节点未连接。',
          413:'档案超过当前入口允许的大小。',
          415:'请选择 Docker 镜像 tar 档案。',
          504:'等待节点接收档案已超时。'
        };
        reject(new Error(errors[upload.status] || '上传失败（HTTP ' + upload.status + '）。'));
      }
    });
    upload.addEventListener('abort',() => reject(signal.reason || new Error('已取消上传。')));
    upload.addEventListener('error',() => reject(new Error('上传中断，请核对网络和节点连接。')));
    upload.addEventListener('timeout',() => reject(new Error('上传已超时。')));
    upload.send(file);
  });
}

function openImageImport(node) {
  requireImageTransfer(node);
  const archive = field('archive','Docker 镜像档案（.tar）',{type:'file',required:true,accept:'.tar,application/x-tar'});
  const accept = field('confirmed','我确认在所选节点应用档案内的标签，同名标签可能被替换。',{type:'checkbox',required:true});
  accept.classList.add('check');
  const progress = workspaceText('progress');
  progress.setAttribute('aria-label','镜像档案上传进度');
  progress.max = 100;
  progress.value = 0;
  const transferStatus = workspaceText('p','','image-operation-status');
  transferStatus.setAttribute('role','status');
  const formStatus = workspaceText('p','','image-operation-status');
  formStatus.setAttribute('role','status');
  let controller;
  let surface;
  const cancel = managementButton('取消上传',() => controller?.abort());
  const progressArea = workspaceText('div','','image-transfer-progress');
  progressArea.append(progress,cancel,transferStatus);
  progressArea.hidden = true;
  archive.lastElementChild.addEventListener('change',() => {
    accept.lastElementChild.checked = false;
    formStatus.textContent = '';
    surface.status.textContent = '';
    progressArea.hidden = true;
  });
  const form = managedForm([archive,accept],'开始导入',async data => {
    const file = data.get('archive');
    if (!file?.size || file.size > maxImageArchiveBytes) throw new Error('请选择非空且不超过 8 GiB 的 Docker 镜像档案。');
    const unlock = lockEditorForm(form);
    const lifecycle = new AbortController();
    controller = lifecycle;
    const {signal} = lifecycle;
    const dialog = element('management-dialog');
    const leave = () => lifecycle.abort();
    dialog.addEventListener('close',() => {
      if (!surface.visible()) leave();
    },{signal});
    window.addEventListener('console-navigated',() => {
      leave();
      if (surface.visible()) dialog.close();
    },{once:true,signal});
    window.addEventListener('pagehide',leave,{once:true,signal});
    progressArea.hidden = false;
    cancel.hidden = false;
    progress.value = 0;
    transferStatus.textContent = '等待节点接受档案…';
    surface.status.textContent = '';
    try {
      assertNodeManagementReady(node.id);
      const job = await request(nodeOperationPath(node),{action:'image.import',generation:node.generation},undefined,signal);
      if (!surface.visible() || signal.aborted) return;
      await imageArchiveUpload(imageTransferPath(node,job),file,signal,bytes => {
        progress.value = Math.min(100,bytes / file.size * 100);
        transferStatus.textContent = '已上传 ' + formatBytes(bytes) + ' / ' + formatBytes(file.size);
      });
      progress.value = 100;
      cancel.hidden = true;
      transferStatus.textContent = '档案已上传，等待 Docker 加载。';
      if (surface.visible()) await reportOperation(node,job,surface);
    } catch (error) {
      if (surface.visible()) {
        transferStatus.textContent = signal.aborted ? '已取消上传。若节点已开始加载，请核对镜像与标签。' : error.message;
      }
    } finally {
      cancel.hidden = true;
      lifecycle.abort();
      controller = undefined;
      if (surface.visible()) unlock();
    }
  },() => !controller,formStatus);
  surface = imageActionSurface(node,'导入 Docker 镜像',form,formStatus,progressArea);
}

document.getElementById('import-image').addEventListener('click',() => {
  try { openImageImport(nodeById(activeNode().id)); }
  catch (error) { notice(error.message,'error'); }
});
