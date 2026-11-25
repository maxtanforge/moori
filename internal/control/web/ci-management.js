import {managementActions,resourceTable,workspaceText,field,option,optionField,snapshotFacts,joined,dateTime,row,deploymentStates,workspaceLink} from './ui-components.js';
import {ciSettingsPath,currentSettingsPath,settingsRevision,managementButton,showSettingsList,settingsCreateButton,managementText,managedForm,registriesPath,nodeById,settingsTableEntry} from './management.js';
import {containerWebhookRepository} from './webhook-management.js';
import {workspaceNodeName} from './node-workspace.js';
import {element,request,enc,show} from './api.js';
import {goToConsoleView,currentRoute,selected,projects,localExecution,agents,base} from './app.js';

const ciTargetsPath = '/api/v1/ci/targets';
let ciDraft;
let ciListRequest = 0;
let ciCredentialEpoch = 0;

export function clearCICredential() {
  ciCredentialEpoch++;
  const token = element('ci-token');
  if (token) token.value = '';
  const credential = element('ci-credential');
  if (credential) {
    delete credential.dataset.id;
    credential.replaceChildren();
  }
}

window.addEventListener('pagehide',clearCICredential);

export function openContainerCISettings(node, container, image) {
  ciDraft = {node,container,repository:containerWebhookRepository(image)};
  try {
    goToConsoleView(ciSettingsPath);
    if (ciDraft && currentSettingsPath === ciSettingsPath) void openOrCreateCITarget(ciDraft);
  } finally {
    ciDraft = undefined;
  }
}

export async function listCITargets() {
  clearCICredential();
  if (ciDraft) {
    const draft = ciDraft;
    ciDraft = undefined;
    await openOrCreateCITarget(draft);
    return;
  }
  const revision = settingsRevision;
  const load = ++ciListRequest;
  const applications = new Map();
  let cursor = '';
  do {
    const page = await request(ciTargetsPath + (cursor ? '?after=' + enc(cursor) : ''));
    if (revision !== settingsRevision || load !== ciListRequest) return;
    for (const item of page.items || []) {
      if (!applications.has(item.project)) applications.set(item.project,[]);
      applications.get(item.project).push(item);
    }
    cursor = page.next || '';
  } while (cursor);
  const rows = [...applications].sort(([left],[right]) => left.localeCompare(right)).map(([project,targets]) => [
    project,targets.length + ' 个环境',joined([...new Set(targets.map(item => item.kind === 'compose' ? 'Compose' : '容器'))]),
    managementButton('查看部署环境',() => showCIApplication(project,targets))
  ]);
  showSettingsList('应用与流水线',managementActions(
    workspaceLink('console','部署记录','editor-link'),settingsCreateButton('接入应用',() => editCITarget())),
    ['应用','部署环境','部署方式','操作'],rows,'暂无应用。接入应用后，选择部署环境并生成流水线配置。');
}

function showCIApplication(project,targets) {
  const rows = targets.map(item => [item.target,item.kind === 'compose' ? 'Compose' : '容器',
    item.node ? workspaceNodeName(item.node) : '未分配节点',item.container || 'Compose 项目',
    managementButton('配置接入',() => showCITarget(item,() => showCIApplication(project,targets)))]);
  ciSettingsPage('应用 · ' + project,listCITargets,
    managementText('先选择部署环境，再配置接入凭证、复制部署步骤并查看发布记录。'),
    managementActions(settingsCreateButton('添加部署环境',() => editCITarget({project}))),
    resourceTable(['部署环境','部署方式','执行节点','部署对象','操作'],rows,true));
}

async function openOrCreateCITarget(draft) {
  if (!draft.node.generation) {
    editCITarget(draft);
    return;
  }
  const revision = settingsRevision;
  const status = workspaceText('p','正在读取容器流水线…','work-empty');
  status.setAttribute('role','status');
  ciSettingsPage('容器流水线',listCITargets,status);
  const load = ciListRequest;
  const isCurrent = () => revision === settingsRevision && load === ciListRequest && currentRoute() === ciSettingsPath;
  const params = new URLSearchParams({node:draft.node.id,generation:draft.node.generation,container:draft.container});
  try {
    const target = await request('/api/v1/ci/container-target?' + params);
    if (isCurrent()) showCITarget(target,listCITargets);
  } catch (error) {
    if (!isCurrent()) return;
    if (error.status === 404) {
      editCITarget(draft);
      return;
    }
    status.textContent = '读取容器流水线失败：' + error.message;
    status.after(managementActions(managementButton('重试',() => openOrCreateCITarget(draft))));
  }
}

function ciSettingsPage(title, back, ...content) {
  ciListRequest++;
  clearCICredential();
  const header = workspaceText('header','','work-panel-header');
  const heading = workspaceText('h2',title);
  heading.tabIndex = -1;
  header.append(heading,managementButton('返回',back));
  const panel = element('settings-list');
  panel.replaceChildren(header,...content);
  heading.focus();
}

function editCITarget(draft = {}) {
  const revision = settingsRevision;
  const editor = workspaceText('div','','user-settings-editor');
  const status = workspaceText('p','','user-settings-status');
  status.setAttribute('role','status');
  const nodeField = field('node','执行节点',{required:true},'select');
  const node = nodeField.querySelector('select');
  option(node,'','选择节点');
  const generation = field('generation','节点代次',{type:'hidden'});
  generation.hidden = true;
  const generationInput = generation.querySelector('input');
  const repository = field('repository','镜像仓库',{required:true,maxLength:310,placeholder:'docker.io/library/nginx',value:draft.repository || ''});
  const repositoryInput = repository.lastElementChild;
  const registryField = field('registry_id','共享仓库凭证（可选）',{disabled:true},'select');
  const registry = registryField.lastElementChild;
  const registryStatus = workspaceText('p','正在读取共享仓库凭证…','user-settings-status');
  registryStatus.setAttribute('role','status');
  const registries = [];
  option(registry,'','自动匹配共享仓库凭证');
  const key = {required:true,maxLength:128,pattern:'[A-Za-z0-9_.:\\-]+'};
  let saving = false;
  const kind = optionField('kind','部署方式',[['container','独立容器'],['compose','Compose']],draft.kind || 'container');
  const project = field('project','应用标识',{...key,value:draft.project || selected?.id || '',placeholder:'选择已有应用或输入新应用'});
  project.lastElementChild.setAttribute('list','ci-app-options');
  const applicationOptions = workspaceText('datalist');
  applicationOptions.id = 'ci-app-options';
  for (const item of projects) option(applicationOptions,item.id,item.id);
  const container = field('container','容器名称',{required:true,value:draft.container || ''});
  const composeHint = managementText('Compose 定义随应用代码保存在 Git 中。流水线提交 YAML 和镜像摘要，由 Moori 创建受管项目；不会接管或覆盖节点上已有的外部 Compose 项目。');
  const form = managedForm([
    project,field('target','部署环境标识',{...key,placeholder:'例如 my-app-production'}),
    applicationOptions,kind,nodeField,generation,container,repository,registryField,registryStatus,composeHint
  ],'保存部署环境',async data => {
    const body = Object.fromEntries([...data].map(([name,value]) => [name,value.trim()]));
    body.generation = Number(body.generation);
    if (!body.registry_id) delete body.registry_id;
    saving = true;
    try {
      const target = await request(ciTargetsPath,body);
      if (revision === settingsRevision && editorRevision === ciListRequest && form.isConnected) showCITarget(target,listCITargets);
    } finally {
      saving = false;
    }
  },() => !!generationInput.value,status);
  form.id = 'ci-target-form';
  const submit = form.querySelector('button[type="submit"]');
  const setGeneration = () => {
    generationInput.value = node.selectedOptions[0]?.dataset.generation || '';
    submit.disabled = saving || !generationInput.value;
  };
  const addNode = item => {
    option(node,item.id,item.label || item.id);
    node.lastElementChild.dataset.generation = item.generation;
  };
  if (draft.node?.generation) {
    addNode(draft.node);
    node.value = draft.node.id;
  }
  node.addEventListener('change',setGeneration);
  kind.lastElementChild.addEventListener('change',setKind);
  repositoryInput.addEventListener('input',setRegistryOptions);
  function setKind() {
    const compose = kind.lastElementChild.value === 'compose';
    for (const wrapper of [container,repository,registryField,registryStatus]) {
      wrapper.hidden = compose || wrapper === registryStatus && !registryStatus.textContent;
      wrapper.querySelectorAll('input,select').forEach(input => { input.disabled = compose; });
    }
    composeHint.hidden = !compose;
    if (!compose) setRegistryOptions();
  }
  setKind();
  setGeneration();
  const reload = managementButton('重新读取节点',loadNodes,false,status);
  reload.hidden = true;
  editor.append(form,status,reload);
  ciSettingsPage('接入应用 · 部署环境',listCITargets,editor);
  const editorRevision = ciListRequest;
  void loadNodes();
  void loadRegistryOptions();

  async function loadRegistryOptions() {
    try {
      let after = '';
      do {
        const page = await request(registriesPath + (after ? '?after=' + enc(after) : ''));
        if (revision !== settingsRevision || !form.isConnected) return;
        registries.push(...(page.items || []).filter(item => item.enabled));
        after = page.next || '';
      } while (after);
      registryStatus.textContent = '';
      registryStatus.hidden = true;
      setRegistryOptions();
    } catch (error) {
      if (revision !== settingsRevision || !form.isConnected) return;
      registryStatus.replaceChildren(workspaceText('span','读取共享仓库凭证失败：' + error.message),
        managementButton('重试',() => void loadRegistryOptions()));
      registryStatus.hidden = false;
    }
  }

  function setRegistryOptions() {
    const selectedID = registry.value;
    const [host,...segments] = repositoryInput.value.trim().split('/');
    const name = segments.join('/');
    registry.options.length = 1;
    for (const item of registries) {
      const prefix = item.repository_prefix;
      if (host !== item.host || !name || prefix && name !== prefix && !name.startsWith(prefix + '/')) continue;
      option(registry,item.id,item.name);
    }
    if ([...registry.options].some(item => item.value === selectedID)) registry.value = selectedID;
    registry.disabled = registry.options.length === 1 || kind.lastElementChild.value === 'compose';
  }

  async function loadNodes() {
    const nodes = new Map();
    if (localExecution) nodes.set('@local',nodeById('@local'));
    try {
      let after = '';
      do {
        const page = await request('/api/v1/agents' + (after ? '?after=' + enc(after) : ''));
        if (revision !== settingsRevision || !form.isConnected) return;
        for (const agent of page.items || []) {
          if (agent.id !== '@local' && agent.generation > 0) {
            nodes.set(agent.id,{id:agent.id,label:agent.display_name,generation:agent.generation});
          }
        }
        after = page.next || '';
      } while (after);
      const selection = node.value || draft.node?.id || '';
      node.options.length = 1;
      for (const item of nodes.values()) addNode(item);
      node.value = nodes.has(selection) ? selection : '';
      setGeneration();
      status.textContent = nodes.size ? '' : '暂无可绑定的节点。';
      reload.hidden = nodes.size > 0;
    } catch (error) {
      if (revision !== settingsRevision || !form.isConnected) return;
      status.textContent = '读取节点失败：' + error.message;
      reload.hidden = false;
    }
  }
}

function showCITarget(target, back) {
  const path = ciTargetsPath + '/' + enc(target.target);
  const body = workspaceText('div','','user-settings-editor');
  const status = workspaceText('p','','user-settings-status');
  status.setAttribute('role','status');
  const credentials = workspaceText('div');
  const tokens = workspaceText('div');
  const runs = deploymentRunsPanel(path + '/runs');
  credentials.id = 'ci-credential';
  runs.id = 'ci-runs';
  const compose = target.kind === 'compose';
  const additionalTargets = ciTokenGrantPicker(target);
  const tokenFields = [
    field('id','令牌 ID',{required:true,maxLength:128,autocomplete:'off'}),
    field('ttl_hours','有效小时数',{required:true,type:'number',min:1,max:8760,value:24})
  ];
  if (compose) {
    const publish = field('allow_publish','允许流水线提交此环境的 Compose 版本',{type:'checkbox',checked:true});
    publish.classList.add('check');
    tokenFields.push(publish);
  }
  tokenFields.push(additionalTargets.details);
  const tokenForm = managedForm(tokenFields,'签发令牌',async data => {
    clearCICredential();
    const version = ciCredentialEpoch;
    const input = {
      id:data.get('id').trim(),ttl_hours:Number(data.get('ttl_hours')),allow_publish:data.has('allow_publish')
    };
    const grants = additionalTargets.selectedGrants();
    if (grants.length) input.grants = grants;
    const result = await request(path + '/tokens',input);
    if (!body.isConnected || version !== ciCredentialEpoch || currentRoute() !== ciSettingsPath) return;
    credentials.dataset.id = result.id;
    credentials.append(managementText('令牌只在签发后显示一次。请保存到 CI 平台的 Secret 中。'),
      ciCopyField('ci-token','MOORI_TOKEN',result.token,status));
    element('ci-token').focus();
    await loadTokens();
  },undefined,status);
  tokenForm.id = 'ci-token-form';
  body.append(workspaceText('h3','1. 应用与部署环境'),snapshotFacts([
    ['应用',target.project],['部署环境',target.target],['部署方式',compose ? 'Compose' : '独立容器'],
    ['执行节点',target.node ? workspaceNodeName(target.node) : '未分配节点'],
    ...(!compose ? [['容器',target.container],['镜像仓库',target.repository]] : [])
  ]),workspaceText('h3','2. 接入凭证'),tokenForm,credentials,tokens,
    workspaceText('h3','3. 流水线部署步骤'),ciIntegrationPanel(target,status),
    workspaceText('h3','4. 部署记录'),runs,status);
  ciSettingsPage(target.project + ' / ' + target.target,back,body);
  void loadTokens();

  async function loadTokens() {
    try {
      const page = await request(path + '/tokens');
      if (!body.isConnected) return;
      const rows = (page.items || []).map(item => {
        const grants = item.grants?.length ? item.grants : [{...target,allow_publish:item.can_publish}];
        const scope = grants.map(grant => `${grant.project} / ${grant.target}（${grant.allow_publish ? '发布与部署' : '部署'}）`).join('、');
        const state = item.revoked ? '已撤销' : Date.parse(item.expires_at) <= Date.now() ? '已过期' : '有效';
        return [item.id,dateTime(item.expires_at),scope,state,
          item.revoked ? '—' : managementButton('撤销',() => confirmRevocation(item),true,status)];
      });
      tokens.replaceChildren(rows.length ? resourceTable(['令牌 ID','过期时间','授权范围','状态','操作'],rows,true) : managementText('暂无流水线令牌。'));
    } catch (error) {
      if (body.isConnected) tokens.replaceChildren(managementText('读取令牌失败：' + error.message),managementButton('重试',loadTokens));
    }
  }

  function confirmRevocation(item) {
    element('ci-revoke-confirmation')?.remove();
    const confirmation = workspaceText('div');
    confirmation.id = 'ci-revoke-confirmation';
    const confirm = managementButton('确认撤销',async () => {
      await request(path + '/tokens/' + enc(item.id),undefined,undefined,undefined,'DELETE');
      if (!body.isConnected) return;
      if (credentials.dataset.id === item.id) clearCICredential();
      await loadTokens();
    },true,status);
    confirmation.append(managementText(`撤销流水线令牌 ${item.id}？此令牌将在所有授权目标上失效。`),
      managementActions(managementButton('取消',() => {
        confirmation.remove();
        tokens.querySelector('button')?.focus();
      }),confirm));
    tokens.append(confirmation);
    confirm.focus();
  }

}

export function deploymentRunsPanel(endpoint, openDetails) {
  const panel = workspaceText('section','','resource-section deployment-runs');
  const status = workspaceText('p','','work-note');
  status.setAttribute('role','status');
  const scope = workspaceText('h2');
  scope.hidden = true;
  const rows = workspaceText('div');
  let index = 0;
  let cursors = [''];
  let nextCursor = '';
  let pending = false;
  const sources = {manual:'手动',ci:'流水线',webhook:'Webhook',rollback:'回滚'};
  const pageNumber = workspaceText('span','第 1 页','work-note');
  function pageButton(label, pageIndex) {
    const button = workspaceText('button',label);
    button.type = 'button';
    button.addEventListener('click',() => void load(pageIndex()));
    return button;
  }
  const refresh = pageButton('刷新记录',() => 0);
  const previous = pageButton('上一页',() => index-1);
  const next = pageButton('下一页',() => index+1);
  panel.append(scope,rows,status,managementActions(refresh,previous,pageNumber,next));

  function updateButtons() {
    refresh.disabled = pending;
    previous.disabled = pending || index === 0;
    next.disabled = pending || !nextCursor;
    pageNumber.textContent = '第 ' + (index+1) + ' 页';
  }

  async function load(pageIndex) {
    if (pending || pageIndex < 0 || pageIndex > index && !nextCursor) return;
    pending = true;
    updateButtons();
    status.textContent = '正在读取部署记录…';
    try {
      const cursor = pageIndex > index ? nextCursor : cursors[pageIndex];
      const query = new URLSearchParams({limit:'20',...(cursor && {after:cursor})});
      const page = await request(endpoint + (endpoint.includes('?') ? '&' : '?') + query,undefined,undefined,AbortSignal.timeout(10000));
      if (!panel.isConnected) return;
      cursors = pageIndex === 0 ? [''] : cursors.slice(0,pageIndex+1);
      cursors[pageIndex] = cursor;
      index = pageIndex;
      nextCursor = page.next || '';
      scope.hidden = !page.project || !page.target;
      if (!scope.hidden) scope.textContent = page.project + ' / ' + page.target;
      const data = (page.items || []).map(item => [
        openDetails ? managementButton(item.id,() => openDetails(item.id,page),false,status) : item.id,
        settingsTableEntry(item.release || item.image || '—',item.digest || ''),
        sources[item.source] || '—',
        settingsTableEntry(deploymentStates[item.state] || item.state,item.error),
        item.created_at?.startsWith('0001-') ? '—' : dateTime(item.created_at)
      ]);
      rows.replaceChildren(data.length ? resourceTable(['记录 ID','版本 / 镜像','来源','结果','创建时间'],data,true) : managementText('暂无部署记录。'));
      status.textContent = '';
    } catch (error) {
      if (panel.isConnected) status.textContent = '读取部署记录失败：' + error.message;
    } finally {
      pending = false;
      updateButtons();
    }
  }

  updateButtons();
  queueMicrotask(() => { if (panel.isConnected) void load(0); });
  return panel;
}

function ciIntegrationPanel(target,status) {
  const panel = workspaceText('section');
  const provider = optionField('ci_provider','流水线平台',[['gitea','Gitea Actions'],['github','GitHub Actions'],['shell','其他 CI（Shell）']]);
  const compose = target.kind === 'compose';
  const hint = managementText(compose ?
    '将 Compose 定义保存在应用仓库中，默认读取 compose.yaml。构建并推送后设置 MOORI_IMAGE 和 MOORI_SERVICE（默认 web）；多服务使用 MOORI_IMAGES JSON 映射。其余服务的镜像也必须固定到摘要。Moori 校验配置、记录版本并执行部署；回滚可重新提交原版本内容与摘要。' :
    `外部 CI 构建并推送镜像后，将完整摘要引用 ${target.repository}@sha256:<摘要> 写入 MOORI_IMAGE。Moori 沿用容器配置执行更新；回滚可重新提交旧摘要。`);
  const instructions = managementText(`在 CI 中配置 MOORI_URL（可访问的 Moori 地址，例如 ${location.origin}）和 MOORI_TOKEN 两个 Secret。将生成的步骤放在镜像推送之后。Runner 需要 curl 和 jq；Moori 节点需具备镜像拉取权限。`);
  const snippet = ciCopyField('ci-gitea-setup','部署步骤',giteaDeployStep(target),status,'textarea');
  const input = snippet.querySelector('textarea');
  provider.lastElementChild.addEventListener('change',() => {
    const value = giteaDeployStep(target);
    input.value = provider.lastElementChild.value === 'shell' ?
      '# 设置 MOORI_URL、MOORI_TOKEN、MOORI_DELIVERY_KEY；构建后设置 MOORI_IMAGE 或 MOORI_IMAGES。\n' +
      value.split('  run: |\n')[1].split('\n').map(line => line.slice(4)).join('\n') : value;
  });
  panel.append(provider,hint,instructions,
    managementText('Actions 构建步骤通过 GITHUB_ENV 传递变量；其他 CI 使用环境变量，并为每次发布设置稳定的 MOORI_DELIVERY_KEY。重复请求保持原键，新版本或回滚使用新键。'),snippet);
  return panel;
}

function ciTokenGrantPicker(current) {
  const details = workspaceText('details');
  details.id = 'ci-token-grants';
  const list = workspaceText('div');
  const status = workspaceText('p');
  status.setAttribute('role','status');
  const more = managementButton('加载更多目标',loadPage,false,status);
  more.hidden = true;
  details.append(workspaceText('summary','授权其他部署目标'),
    managementText('当前目标已包含在授权范围内。可继续选择其他项目或节点的目标。'),list,status,more);
  const choices = [];
  let started = false;
  let after = '';
  details.addEventListener('toggle',() => {
    if (!details.open || started) return;
    started = true;
    void loadPage();
  });
  return {details,selectedGrants:() => choices.filter(choice => choice.include.checked).map(choice => ({
    project:choice.target.project,target:choice.target.target,allow_publish:!!choice.publish?.checked
  }))};

  async function loadPage() {
    more.hidden = true;
    status.textContent = '正在读取部署目标…';
    try {
      const page = await request(ciTargetsPath + (after ? '?after=' + enc(after) : ''));
      if (!details.isConnected) return;
      for (const item of page.items || []) {
        if (item.target === current.target) continue;
        const type = item.kind === 'compose' ? 'Compose' : '容器';
        const node = item.node ? workspaceNodeName(item.node) : '未分配节点';
        const name = `${item.project} / ${item.target}`;
        const label = field('',`${name} · ${type} · ${node}`,{type:'checkbox'});
        label.classList.add('check');
        const include = label.lastElementChild;
        include.dataset.ciGrantTarget = item.target;
        list.append(label);
        let publish;
        if (item.kind === 'compose') {
          const permission = field('','允许发布镜像版本',{type:'checkbox',disabled:true});
          permission.classList.add('check');
          publish = permission.lastElementChild;
          publish.dataset.ciPublishTarget = item.target;
          publish.setAttribute('aria-label',`${name}，允许发布镜像版本`);
          include.addEventListener('change',() => {
            publish.disabled = !include.checked;
            if (publish.disabled) publish.checked = false;
          });
          list.append(permission);
        }
        choices.push({target:item,include,publish});
      }
      after = page.next || '';
      more.textContent = '加载更多目标';
      more.hidden = !after;
      status.textContent = choices.length || after ? '' : '暂无其他部署目标。';
    } catch (error) {
      if (!details.isConnected) return;
      status.textContent = '读取部署目标失败：' + error.message;
      more.textContent = '重试';
      more.hidden = false;
    }
  }
}

function ciCopyField(id,label,value,status,tag = 'input') {
  const fieldLabel = field('',label,{id,readOnly:true,autocomplete:'off'},tag);
  const input = fieldLabel.lastElementChild;
  input.value = value;
  if (tag === 'textarea') input.rows = 10;
  const copy = managementButton('复制 ' + label,async () => {
    try {
      await navigator.clipboard.writeText(input.value);
      status.textContent = '已复制 ' + label + '。';
      return;
    } catch {}
    if (!input.isConnected) return;
    input.focus();
    input.select();
    status.textContent = '已选中 ' + label + '，请使用复制快捷键。';
  },false,status);
  copy.id = 'copy-' + id;
  const row = workspaceText('div','','webhook-credential-row');
  row.append(fieldLabel,copy);
  return row;
}

export function giteaDeployStep(target) {
  const compose = target.kind === 'compose';
  const publication = compose ? `
    file="\${MOORI_COMPOSE_FILE:-compose.yaml}"
    images="\${MOORI_IMAGES:-}"
    if [ -z "$images" ]; then
      : "\${MOORI_IMAGE:?Set the pushed image digest}"
      images=$(jq -nc --arg service "\${MOORI_SERVICE:-web}" --arg image "$MOORI_IMAGE" '{($service):$image}')
    fi
    release="\${MOORI_RELEASE:-$key}"
    publication=$(jq -nc --arg project "$project" --arg target "$target" --arg id "$release" --rawfile compose "$file" --argjson images "$images" '{project:$project,target:$target,id:$id,compose:$compose,images:$images}')
    api -H 'Content-Type: application/json' --data "$publication" "$base/api/v1/releases" >/dev/null
    payload=$(jq -nc --arg project "$project" --arg target "$target" --arg release "$release" '{project:$project,target:$target,release:$release}')` : `
    : "\${MOORI_IMAGE:?Set the pushed image digest}"
    payload=$(jq -nc --arg project "$project" --arg target "$target" --arg image "$MOORI_IMAGE" '{project:$project,target:$target,image:$image}')`;
  return `- name: Deploy with Moori
  env:
    MOORI_URL: \${{ secrets.MOORI_URL }}
    MOORI_TOKEN: \${{ secrets.MOORI_TOKEN }}
  run: |
    set -eu
    : "\${MOORI_URL:?Set the MOORI_URL secret}" "\${MOORI_TOKEN:?Set the MOORI_TOKEN secret}"
    base="\${MOORI_URL%/}"
    url="$base/api/v1/${compose ? 'deployments' : 'container-deployments'}"
    api() {
      curl --fail-with-body --silent --show-error --max-time 30 -H "Authorization: Bearer $MOORI_TOKEN" "$@"
    }
    project="\${MOORI_PROJECT:-${target.project}}"
    target="\${MOORI_TARGET:-${target.target}}"
    key="\${MOORI_DELIVERY_KEY:-}"
    if [ -z "$key" ]; then
      : "\${GITHUB_RUN_ID:?Set MOORI_DELIVERY_KEY for this CI}"
      key="ci-\${GITHUB_RUN_ID}-\${GITHUB_RUN_ATTEMPT:-1}-\${GITHUB_JOB:-deploy}"
    fi${publication}
    result=$(api -H 'Content-Type: application/json' -H "Idempotency-Key: $key" --data "$payload" "$url")
    id=$(printf '%s' "$result" | jq -er .id)
    deadline=$(($(date +%s) + 600))
    while [ "$(date +%s)" -lt "$deadline" ]; do
      result=$(api "$url/$id")
      state=$(printf '%s' "$result" | jq -er .state)
      printf '%s: %s\\n' "$id" "$state"
      case "$state" in
        succeeded) exit 0 ;;
        failed|uncertain|canceled|abandoned|needs_attention) printf '%s\\n' "$result" | jq -r '.error // .state' >&2; exit 1 ;;
      esac
      sleep 5
    done
    printf 'Timed out waiting for %s\\n' "$id" >&2
    exit 1`;
}
