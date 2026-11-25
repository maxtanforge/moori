import {workspaceText,workspaceLink,field,optionField,managementActions,snapshotFacts,row,joined,dateTime} from './ui-components.js';
import {managementButton,managedForm,performOperation,nodeOperationPath,composeRemoveButton,containerOperationMessage,templatesPath,templateDeployment} from './management.js';
import {workspaceNodeName,maintenanceTable,nodePath,assertNodeManagementReady} from './node-workspace.js';
import {lockEditorForm,showEditorPreview,showEditorForm,editorCard} from './maintenance-page.js';
import {resourceChoiceOptions,editorRows} from './container-maintenance.js';
import {enc,request,element,notice} from './api.js';
import {projects,accountEpoch,submitDeployment,cursors,loadProjects,goToConsoleView,selected,selectProject,loadDeployments,openDeployment,detail,action,refreshAfterNodeMutation} from './app.js';

const maintainedFields = [
  ['image', '镜像（指定 SHA-256 摘要）', 'single', 'registry.example/app@sha256:…'],
  ['restart', '重启策略', 'restart'],
  ['ports', '端口映射（每行一条）', 'lines', '127.0.0.1:8080:80/tcp']
];

export function maintenanceDeploymentPath(project,id) {
  return '/api/v1/projects/' + enc(project) + '/deployments/' + enc(id);
}

export function editComposeDeployment(page,project,item,recreate,node) {
  const version = accountEpoch;
  const current = item.snapshot?.compose;
  if (!current?.services || item.state !== 'succeeded') throw new Error('没有可用的成功部署快照。');
  const draft = structuredClone(current);
  const target = item.snapshot.target;
  const path = maintenanceDeploymentPath(project,item.id);
  const preview = workspaceText('section','','compose-preview');
  let draftRevision = 0;
  let serviceGroups;
  let editDefinition = false;
  let restoreGroups = () => {};
  const saved = field('saved-compose','当前完整 Compose 配置',{
    value:JSON.stringify(current,null,2),readOnly:true,rows:8,spellcheck:false
  },'textarea');
  const toggle = managementButton('按完整定义编辑',() => {
    if (page.pending) return;
    editDefinition = !editDefinition;
    saved.lastElementChild.readOnly = !editDefinition;
    saved.firstChild.textContent = editDefinition ? '编辑完整 Compose 定义' : '当前完整 Compose 配置';
    toggle.textContent = editDefinition ? '返回分组编辑' : '按完整定义编辑';
    const groups = [...form.querySelectorAll('.editor-card')];
    for (const group of groups) group.hidden = editDefinition;
    if (editDefinition) {
      const controls = groups.flatMap(group => [...group.querySelectorAll('input,textarea,select,button')]).filter(control => !control.disabled);
      controls.forEach(control => { control.disabled = true; });
      restoreGroups = () => controls.forEach(control => { control.disabled = false; });
      saved.lastElementChild.focus();
    } else {
      restoreGroups();
    }
    page.navigation.hidden = editDefinition || groups.length < 3;
    invalidate();
  },false,page.status);
  const scope = workspaceText('p',target + ' · 当前版本 ' + item.release,'maintenance-scope');
  const form = managedForm(recreate ? [] : editorFields(), '预览' + (recreate ? '重新部署' : '变更'), async () => {
    if (!recreate && !editDefinition) {
      saveService();
      for (const kind of ['volumes','networks']) {
        const definitions = form.elements['declared-' + kind].value;
        try {
          const object = JSON.parse(definitions || '{}');
          if (!object || Array.isArray(object) || typeof object !== 'object') throw new Error();
          draft[kind] = object;
        } catch {
          throw new Error((kind === 'volumes' ? '卷' : '网络') + '声明应为 JSON 对象。');
        }
      }
    }
    const compose = editDefinition ? saved.lastElementChild.value : JSON.stringify(draft);
    if (editDefinition && !compose.trim()) throw new Error('请填写 Compose 定义。');
    const body = {compose:recreate ? '' : compose, force_recreate:recreate, ...(node && {node})};
    preview.replaceChildren();
    const revision = draftRevision;
    const previewed = await request(path + '/compose-preview',body);
    if (revision !== draftRevision || !page.visible()) return;
    const confirmer = managementButton('确认部署',async () => {
      if (page.pending || revision !== draftRevision || !page.visible()) return;
      page.pending = true;
      const unlock = lockEditorForm(form);
      saved.lastElementChild.readOnly = true;
      toggle.disabled = true;
      page.status.textContent = '正在提交部署…';
      try {
        const result = await submitDeployment(path + '/compose-apply',{...body,node:previewed.node,snapshot_digest:previewed.snapshot_digest});
        if (!page.visible()) return;
        const view = managementButton('查看部署记录',() => openMaintainedDeployment(project,result.id,version),false,page.status);
        form.replaceChildren();
        form.hidden = true;
        toggle.hidden = true;
        page.navigation.hidden = true;
        showEditorPreview(preview,'部署申请已受理',workspaceText('p','部署编号 ' + result.id),view);
        void updateMaintenanceDeploymentStatus(project,result.id,page);
      } finally {
        page.pending = false;
        if (page.visible() && !form.hidden) {
          saved.lastElementChild.readOnly = !editDefinition;
          toggle.disabled = false;
          unlock();
        }
      }
    },false,page.status);
    confirmer.classList.add('primary');
    const summary = workspaceText('p','执行节点 ' + workspaceNodeName(previewed.node.agent) + ' · ' +
      '提交后进入队列','maintenance-scope');
    const panels = workspaceText('div','','compose-preview-grid');
    if (!recreate) panels.append(composePreviewPanel('当前配置',current));
    panels.append(composePreviewPanel(recreate ? '将重新部署的配置' : '将应用的配置',JSON.parse(previewed.compose)));
    showEditorPreview(preview,'部署预览',summary,panels,confirmer);
  },() => !page.pending && page.visible(),page.status);
  form.classList.add('compose-maintenance-form');
  const invalidate = () => {
    draftRevision++;
    preview.replaceChildren();
  };
  form.addEventListener('input',invalidate);
  form.addEventListener('change',invalidate);
  saved.lastElementChild.addEventListener('input',invalidate);
  let activeService = Object.keys(draft.services)[0];
  if (!recreate) fillService();
  page.status.textContent = '';
  showEditorForm(page,editorCard('部署目标',scope,
    ...(!recreate ? [saved,toggle] : []),
    ...(recreate ? [workspaceText('p','在同一节点按当前 Compose 配置重建服务。重建期间服务可能短暂中断。','maintenance-impact')] : [])),
    form,preview);

  function editorFields() {
    const service = optionField('service','服务',Object.keys(draft.services).map(name => [name,name]));
    service.lastElementChild.addEventListener('change',event => {
      try {
        saveService();
        activeService = event.target.value;
        fillService();
        page.status.textContent = '';
      } catch (error) {
        event.target.value = activeService;
        page.status.textContent = error.message;
      }
    });
    const fields = workspaceText('div','','compose-maintenance-fields');
    for (const [key,label,kind,placeholder] of maintainedFields) {
      if (kind === 'restart') {
        const select = optionField(key,label,[['','默认'],['no','不自动重启'],['always','总是重启'],
          ['unless-stopped','除非手动停止'],['on-failure','仅失败时重启']]);
        select.lastElementChild.required = false;
        const retry = field('restart-retries','失败重试次数（可留空）',{type:'number',min:0,max:4294967295,step:1});
        select.lastElementChild.addEventListener('change',() => { retry.hidden = select.lastElementChild.value !== 'on-failure'; });
        fields.append(select,retry);
      } else {
        fields.append(field(key,label,{placeholder,spellcheck:false, ...(kind === 'single' ? {required:true,maxLength:512} : {rows:3})},
          kind === 'single' ? 'input' : 'textarea'));
      }
    }
    const definitions = workspaceText('details','','compose-definitions');
    const declarations = {};
    definitions.append(workspaceText('summary','项目级卷和网络声明'),
      workspaceText('p','服务只能引用已声明的卷和网络。空对象由 Compose 创建资源，已有资源可声明为 {"external":true,"name":"Docker 资源名"}。'));
    for (const kind of ['volumes','networks']) {
      const wrapper = field('declared-' + kind,kind === 'volumes' ? '卷声明（JSON）' : '网络声明（JSON）',
        {value:JSON.stringify(draft[kind] || {},null,2),rows:5,spellcheck:false},'textarea');
      declarations[kind] = wrapper.lastElementChild;
      definitions.append(wrapper);
    }
    const choices = kind => () => {
      try {
        return resourceChoiceOptions(Object.keys(JSON.parse(declarations[kind].value || '{}')));
      } catch {
        return resourceChoiceOptions(Object.keys(draft[kind] || {}));
      }
    };
    serviceGroups = {
      volumes:editorRows('volumes','服务卷',[],[
        ['source','Compose 卷',{options:choices('volumes')}],
        ['destination','容器路径',{required:true,placeholder:'/data'}],
        ['mode','权限',{options:[['rw','可读写'],['ro','只读']]}]
      ],{addLabel:'添加挂载'}),
      networks:editorRows('networks','服务网络',[],[
        ['network','Compose 网络',{options:choices('networks')}]
      ],{addLabel:'添加网络'}),
      environment:composeKeyValueEditor('environment','环境变量','添加变量'),
      labels:composeKeyValueEditor('labels','标签','添加标签')
    };
    for (const kind of ['volumes','networks']) {
      declarations[kind].addEventListener('change',() => serviceGroups[kind].setRows(serviceGroups[kind].rows()));
    }
    return [editorCard('服务配置',service,fields,definitions),...Object.values(serviceGroups).map(group => group.card)];

  }

  function fillService() {
    const values = draft.services[activeService];
    for (const [key,,kind] of maintainedFields) {
      const value = values[key];
      const control = form.elements[key];
      if (kind === 'restart') {
        const [policy,retries] = (value || '').split(':');
        control.value = policy;
        const retry = form.elements['restart-retries'];
        retry.value = retries ?? '';
        retry.parentElement.hidden = policy !== 'on-failure';
      } else if (kind === 'lines') control.value = (value || []).join('\n');
      else control.value = value || '';
    }
    serviceGroups.volumes.setRows((values.volumes || []).map(mount => {
      const [source,destination,mode = 'rw'] = mount.split(':');
      return {source,destination,mode};
    }));
    serviceGroups.networks.setRows((values.networks || []).map(network => ({network})));
    for (const key of ['environment','labels']) {
      serviceGroups[key].setRows(Object.entries(values[key] || {}).map(([key,value]) => ({key,value})));
    }
  }

  function saveService() {
    const update = {...draft.services[activeService]};
    for (const [key,label,kind] of maintainedFields) {
      const raw = form.elements[key].value;
      if (!raw.trim()) {
        delete update[key];
      } else if (kind === 'lines') {
        update[key] = raw.split(/\r?\n/).map(line => line.trim()).filter(Boolean);
      } else if (kind === 'restart') {
        const retry = form.elements['restart-retries'];
        if (raw === 'on-failure' && !retry.validity.valid) throw new Error('失败重试次数无效。');
        update[key] = raw === 'on-failure' && retry.value !== '' ? raw + ':' + retry.value : raw;
      } else {
        update[key] = raw.trim();
      }
    }
    const previousMounts = new Set(update.volumes || []);
    const mounts = serviceGroups.volumes.rows().map(({source,destination,mode}) => {
      if (!source || !destination.trim()) throw new Error('请选择卷及容器路径。');
      const mount = source + ':' + destination.trim();
      if (mode === 'ro') return mount + ':ro';
      return previousMounts.has(mount + ':rw') ? mount + ':rw' : mount;
    });
    const networks = serviceGroups.networks.rows().map(({network}) => {
      if (!network) throw new Error('请选择服务网络。');
      return network;
    });
    for (const [key,values] of [['volumes',mounts],['networks',networks]]) {
      if (values.length) update[key] = values;
      else delete update[key];
    }
    for (const key of ['environment','labels']) {
      const mapping = serviceGroups[key].read();
      if (Object.keys(mapping).length) update[key] = mapping;
      else delete update[key];
    }
    draft.services[activeService] = update;
  }
}

function composeKeyValueEditor(key,title,addLabel) {
  const group = editorRows(key,title,[],[
    ['key','',{ariaLabel:title + '名称',required:true,placeholder:key === 'environment' ? 'APP_MODE' : 'team',
      ...(key === 'environment' && {pattern:'[A-Za-z_][A-Za-z0-9_]*'})}],
    ['value','',{ariaLabel:title + '值',tag:'textarea',rows:1,spellcheck:false}]
  ],{addLabel});
  if (key === 'environment') group.card.append(workspaceText('p','密码和令牌通过部署目标的密钥绑定维护。','editor-hint'));
  return {
    ...group,
    read() {
      const values = new Map();
      for (const row of group.rows()) {
        const name = row.key.trim();
        if (!name || values.has(name)) throw new Error(title + '的名称不能为空或重复。');
        values.set(name,row.value);
      }
      return Object.fromEntries(values);
    }
  };
}

function composePreviewPanel(title,compose) {
  const panel = workspaceText('details','','compose-preview-document');
  panel.open = true;
  panel.append(workspaceText('summary',title),workspaceText('pre',JSON.stringify(compose,null,2)));
  return panel;
}

export async function openMaintainedDeployment(project,id,version) {
  while (accountEpoch === version && !projects.some(item => item.id === project) && cursors.projects) await loadProjects(true);
  if (accountEpoch !== version) return;
  if (!projects.some(item => item.id === project)) throw new Error('项目未出现在当前目录中，请刷新项目列表。');
  goToConsoleView('console');
  if (selected?.id !== project) {
    element('project').value = project;
    await selectProject();
  } else {
    await loadDeployments();
  }
  if (accountEpoch === version && selected?.id === project) await openDeployment(id);
}

function composeRuntimeRows(name,view) {
  return (view.containers || []).filter(container => container.compose_project === name).map(container => [
    container.compose_service, container.name || container.id, container.image, container.restart_policy,
    joined((container.ports || []).map(port => port.public ?
      (port.ip ? port.ip + ' · ' : '') + port.public + ' → ' + port.private + '/' + port.protocol :
      port.private + '/' + port.protocol)),
    joined((container.mounts || []).map(mount => (mount.name || mount.source || mount.type) +
      ' → ' + mount.destination + (mount.read_only ? '（只读）' : ''))),
    joined(container.networks)
  ]);
}

function composeRuntimeSummary(name,view) {
  const rows = composeRuntimeRows(name,view);
  const table = maintenanceTable('当前运行配置',['服务','容器','镜像','重启策略','端口','卷及挂载','网络'],rows,
    '当前节点快照没有该项目的容器记录。');
  const sampled = view.section_times?.containers;
  if (sampled && Date.now() - new Date(sampled).getTime() > 120000) {
    table.firstElementChild.after(workspaceText('p','上次采集 ' + dateTime(sampled),'work-note resource-detail-note'));
  }
  return table;
}

export function createComposeProject(page,node) {
  page.privateConfig = true;
  const name = field('name','项目名称',{required:true,maxLength:128,autocomplete:'off',placeholder:'my-web'});
  const source = field('compose','Compose YAML',{required:true,rows:18,spellcheck:false,
    placeholder:'services:\n  web:\n    image: nginx:alpine\n'},'textarea');
  const context = editorCard('部署到 ' + node.label,workspaceText('p',
    '粘贴 compose.yaml 的内容。节点将校验配置、保存源文件并启动服务。','maintenance-scope'));
  const form = managedForm([editorCard('项目配置',name,source)],'创建并部署',async data => {
    const target = data.get('name').trim();
    const content = data.get('compose');
    if (!/^[a-z0-9][a-z0-9_-]{0,127}$/.test(target)) throw new Error('项目名须以小写字母或数字开头，可包含小写字母、数字、下划线和连字符。');
    if (/^moori-[a-f0-9]{32}$/.test(target)) throw new Error('此名称为 Moori 部署保留，请选择其他项目名。');
    if (!content.trim()) throw new Error('请填写 Compose YAML。');
    if (new TextEncoder().encode(content).length > 65536) throw new Error('Compose YAML 超过 64 KiB。');
    page.pending = true;
    const unlock = lockEditorForm(form);
    page.results.replaceChildren();
    try {
      const state = await performOperation(node,{action:'compose.source-create',target,
        compose_source:{path:'compose.yaml',revision:'',content}},nodeOperationPath(node),page);
      if (!page.visible()) return;
      if (state === 'succeeded') {
        page.completed = true;
        form.hidden = true;
        page.status.textContent = 'Compose 项目 ' + target + ' 已创建并部署。';
        page.results.prepend(workspaceLink(nodePath(node.id,'compose',target,'edit'),'编辑 Compose 源文件','workspace-link'));
      }
    } finally {
      page.pending = false;
      if (!page.completed) unlock();
    }
  },() => page.visible() && !page.pending && !page.completed,page.status);
  showEditorForm(page,context,form,workspaceText('section','','compose-preview'));
}

async function composeSourceOperation(page,node,name,action,edit,force = false) {
  assertNodeManagementReady(node.id);
  const controller = new AbortController();
  page.privateConfig = true;
  page.cancelLoad = () => controller.abort();
  const signal = AbortSignal.any([controller.signal,AbortSignal.timeout(action === 'compose.source-deploy' ? 240000 : 45000)]);
  const path = nodeOperationPath(node);
  try {
    const body = {action,target:name,generation:node.generation,...(edit && {compose_source:edit}),...(force && {force_recreate:true})};
    let job = await request(path,body,undefined,signal);
    while (['queued','running'].includes(job.state)) {
      await new Promise(resolve => setTimeout(resolve,800));
      signal.throwIfAborted();
      job = await request(path + '/' + enc(job.id),undefined,undefined,signal);
    }
    if (job.state !== 'succeeded' || !job.result?.compose_source) {
      throw new Error(job.error || '无法确认 Compose 源文件操作结果。');
    }
    return job.result.compose_source;
  } finally {
    page.cancelLoad = undefined;
  }
}

export async function editExistingCompose(page,node,name,recreate,view) {
  page.status.textContent = '正在读取已授权的 Compose 源文件…';
  let sources;
  let readError;
  try {
    sources = await composeSourceOperation(page,node,name,'compose.source-read');
  } catch (error) {
    readError = error.message;
  }
  if (!page.visible()) return;
  page.status.textContent = '';
  if (sources?.files?.length) editRegisteredCompose(page,node,name,recreate,sources,view);
  else {
    const note = readError && readError !== '此 Compose 项目尚未注册源文件' ?
      '源文件暂不可用。' + readError : '此项目由外部工具维护。请在原部署工具中修改，或在节点授权后查看原始源文件。';
    page.body.replaceChildren(editorCard('受限管理',workspaceText('p',note,'maintenance-scope'),
      composeRuntimeSummary(name,view)));
  }
}

function editRegisteredCompose(page,node,name,recreate,sources,view) {
  const files = new Map(sources.files.map(file => [file.path,file.content]));
  const selection = optionField('source-file','源文件',sources.files.map(file => [file.path,file.path]));
  const select = selection.lastElementChild;
  const source = field('compose','Compose YAML',{required:true,rows:18,spellcheck:false,value:files.get(select.value)},'textarea');
  const input = source.lastElementChild;
  const preview = workspaceText('section','','compose-preview');
  let revision = 0;
  const invalidate = () => { revision++; preview.replaceChildren(); };
  const reset = managementButton('恢复文件内容',() => {
    input.value = files.get(select.value);
    input.dispatchEvent(new Event('input',{bubbles:true}));
    input.focus();
  },false,page.status);
  const reload = managementButton('重新读取源文件',async () => {
    if (page.pending || !page.visible()) return;
    if (input.value !== files.get(select.value) && !window.confirm('丢弃当前编辑并重新读取源文件？')) return;
    page.pending = true;
    invalidate();
    const unlock = lockEditorForm(form);
    try {
      const latest = await composeSourceOperation(page,node,name,'compose.source-read');
      if (page.visible()) editRegisteredCompose(page,node,name,recreate,latest,view);
    } finally {
      page.pending = false;
      unlock();
    }
  },false,page.status);
  select.addEventListener('change',() => {
    input.value = files.get(select.value);
    invalidate();
  });
  input.addEventListener('input',() => {
    select.disabled = input.value !== files.get(select.value);
    invalidate();
  });
  const context = editorCard(name,workspaceText('p','来源为 ' + node.label + ' 上已授权的当前文件。','maintenance-scope'),
    workspaceText('p','多个文件按部署顺序列出。请逐份预览和保存。','editor-hint'),
    managementActions(composeRemoveButton(node,name,view.containers || [],view),
      sources.files.length === 1 ?
        managementButton('评估纳管',() => {
          if (input.value !== files.get(select.value)) throw new Error('请先保存当前修改，再评估纳管。');
          return adoptExistingCompose(page,node,name,sources,view);
        },false,page.status) : null));
  const form = managedForm([editorCard('Compose 源文件',selection,source,managementActions(reset,reload))],'校验并预览',async () => {
    const version = ++revision;
    preview.replaceChildren();
    const edit = {revision:sources.revision,path:select.value,content:input.value};
    page.status.textContent = '正在校验源文件…';
    const plan = await composeSourceOperation(page,node,name,'compose.source-preview',edit);
    if (!page.visible() || version !== revision) return;
    page.status.textContent = '';
    const run = action => async () => {
      if (page.pending || version !== revision || !page.visible()) return;
      page.pending = true;
      const unlock = lockEditorForm(form);
      const buttons = [...preview.querySelectorAll('button')];
      buttons.forEach(button => { button.disabled = true; });
      page.status.textContent = action === 'compose.source-save' ? '正在保存源文件…' : '正在保存并部署…';
      try {
        const saved = await composeSourceOperation(page,node,name,action,edit,recreate && action === 'compose.source-deploy');
        if (!page.visible()) return;
        sources.revision = saved.revision;
        files.set(edit.path,edit.content);
        invalidate();
        page.status.textContent = action === 'compose.source-save' ? '源文件已保存。' : '源文件已保存，部署完成。';
        if (action === 'compose.source-deploy') void refreshAfterNodeMutation(node.id,['containers','images','networks','volumes']);
      } finally {
        page.pending = false;
        if (page.visible()) {
          unlock();
          select.disabled = input.value !== files.get(select.value);
          buttons.forEach(button => { button.disabled = false; });
        }
      }
    };
    const save = managementButton('保存到源文件',run('compose.source-save'),false,page.status);
    const deploy = managementButton('保存并部署',run('compose.source-deploy'),false,page.status);
    deploy.classList.add('primary');
    showEditorPreview(preview,'Compose 配置预览',snapshotFacts([
      ['源文件',edit.path],['当前服务',joined(plan.before)],['拟部署服务',joined(plan.after)]
    ]),managementActions(save,deploy));
  },() => !page.pending && page.visible(),page.status);
  showEditorForm(page,context,form,preview);
}

// adoptExistingCompose converts the external project's authorized source
// file into a managed template draft. Image digests come from the node
// snapshot, so no registry access is involved.
async function adoptExistingCompose(page,node,name,sources,view) {
  if (page.pending || !page.visible()) return;
  const file = sources.files[0];
  const digests = new Map((view.images || []).map(image => [image.full_id,image.digests || []]));
  const pins = {};
  for (const container of view.containers || []) {
    if (container.compose_project !== name || !container.compose_service || !container.image_id) continue;
    const found = digests.get(container.image_id) || [];
    if (found.length) pins[container.compose_service] = [...new Set([...(pins[container.compose_service] || []),...found])];
  }
  page.pending = true;
  page.status.textContent = '正在评估纳管…';
  let adopted;
  try {
    adopted = await request('/api/v1/compose-adopt',{project:name,compose:file.content,pins});
    if (!page.visible()) return;
  } catch (error) {
    if (page.visible()) page.status.textContent = containerOperationMessage(error.message) || '纳管评估失败。';
    return;
  } finally {
    page.pending = false;
  }
  if (page.visible()) page.status.textContent = '';
  renderAdoptResult(page,node,name,adopted,sources,view);
}

function renderAdoptResult(page,node,name,adopted,sources,view) {
  const back = managementButton('返回源文件',() => {
    if (!page.pending && page.visible()) editRegisteredCompose(page,node,name,false,sources,view);
  },false,page.status);
  const issues = adopted.issues || [];
  const issueList = () => {
    const list = workspaceText('ul','','adopt-issues');
    for (const issue of issues) {
      list.append(workspaceText('li',(issue.service ? issue.service + '：' : '') + issue.message));
    }
    return list;
  };
  if (!adopted.compose) {
    page.body.replaceChildren(editorCard('纳管评估 · ' + name,
      workspaceText('p','没有服务可以按受管模型纳管。请按下述说明调整后重新评估，或继续在原部署工具中维护。','maintenance-scope'),
      issueList(),managementActions(back)));
    page.status.textContent = '';
    return;
  }
  const compose = field('adopt-compose','转换后的 Compose 配置',{value:adopted.compose,rows:16,readOnly:true,spellcheck:false},'textarea');
  const resources = [...(adopted.volumes || []).map(name => ['卷',name,view.volumes]),
    ...(adopted.networks || []).map(name => ['网络',name,view.networks])];
  const ports = adopted.ports || [];
  const occupied = ports.flatMap(port => {
    const [mapping,protocol = 'tcp'] = port.value.split('/');
    const published = Number(mapping.split(':').at(-2));
    const owners = (view.containers || []).filter(container =>
      container.state === 'running' && container.ports?.some(entry => entry.public === published && entry.protocol === protocol));
    return owners.length ? [published + '/' + protocol + ' · ' + owners.map(owner => owner.name || owner.id || '容器').join('、')] : [];
  });
  const changes = field('adopt-review','我已核对转换差异、资源绑定和切换计划',{type:'checkbox',required:true});
  const saveName = field('adopt-name','模板名称',{required:true,value:'纳管-' + name,maxLength:128});
  const form = managedForm([
    editorCard('转换后的 Compose 配置',compose),
    editorCard('保存为模板',saveName, ...(issues.length || resources.length || ports.length ? [changes] : []),
      workspaceText('p','模板保存后，到 设置 → Compose 模板 部署此版本；部署时把已有卷和网络绑定回原名即可复用现有数据。','editor-hint'))
  ],'保存为 Compose 模板',async () => {
    const templateName = saveName.lastElementChild.value.trim();
    if (!templateName) throw new Error('请填写模板名称。');
    page.pending = true;
    page.status.textContent = '正在保存模板…';
    try {
      const adjustablePorts = ports.length <= 64;
      const template = await request(templatesPath,{name:templateName,description:'从 ' + node.label + ' 纳管的外部 Compose 项目 ' + name + '。',compose:adopted.compose,
        ...(adjustablePorts && ports.length ? {parameters:{ports:ports.map(({service,port}) => ({service,port}))}} : {})});
      if (!page.visible()) return;
      notice('模板「' + templateName + '」已保存。');
      const steps = workspaceText('ol','','adopt-steps');
      for (const step of ['部署刚保存的版本，部署目标选择当前节点。',
        resources.length ? '核对卷和网络绑定：' + resources.map(([kind,value]) => kind + ' ' + value).join('、') + '。共享数据卷的服务避免同时写入。' : '本模板没有外部卷或网络绑定。',
        ...(ports.length ? ['检查宿主机端口是否已被旧项目占用。' + (adjustablePorts ? '可以先用临时端口验证；' : '端口过多，未生成临时端口覆盖项。') +
          '正式切换时先停止旧项目并保留容器，再用原端口部署。若失败，先停止失败的新项目并释放端口，再启动旧项目。'] : []),
        '验证新服务和数据后，再清理旧项目；默认保留卷，网络单独处理。']) {
        steps.append(workspaceText('li',step));
      }
      page.body.replaceChildren(editorCard('纳管模板已保存 · ' + templateName,
        workspaceText('p','下一步：','maintenance-scope'),steps,managementActions(
          projects.length && template?.versions?.[0] && managementButton('部署刚保存的版本',() => templateDeployment(template,template.versions[0],{
            node,volumes:adopted.volume_bindings,networks:adopted.network_bindings
          }),false,page.status),
          !projects.length && workspaceLink('console','先创建项目和部署目标','workspace-link'),
          workspaceLink('settings/compose-templates','前往 Compose 模板','workspace-link'),back)));
      page.status.textContent = '';
    } finally {
      page.pending = false;
    }
  },() => !page.pending && page.visible(),page.status);
  const context = editorCard(name + ' · 纳管评估',
    workspaceText('p','已按受管模型转换 ' + adopted.services.length + ' 个服务：' + adopted.services.join('、') +
      '。转换结果已通过编译校验，确认无误后保存为模板。','maintenance-scope'),
    ...(issues.length ? [workspaceText('h3','需要核对的转换差异'),issueList()] : []),
    ...resources.map(([kind,value,rows]) => workspaceText('p',kind + ' ' + value + ' · ' +
      (rows?.some(item => item.name === value) ? '节点快照中存在' : '节点快照中未找到，部署前需核对'),'editor-hint')),
    ...(ports.length ? [workspaceText('p','固定宿主机端口：' + ports.map(port => port.service + ' ' + port.value).join('、') + '。同机部署前核对占用，计划切换和回退。','editor-hint')] : []),
    ...(occupied.length ? [workspaceText('p','节点快照发现同端口的运行中容器，部署前需确认是否冲突：' + occupied.join('；'),'editor-hint')] : []));
  showEditorForm(page,context,form,workspaceText('section','','compose-preview'));
  form.querySelector('.editor-submit-bar').append(back);
  page.status.textContent = '';
}
