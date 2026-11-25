import {workspaceText,optionField,field,snapshotFacts,workspaceGlyph,option,actionIcon,row,formatBytes} from './ui-components.js';
import {managedForm,managementButton,nodeOperationPath} from './management.js';
import {discoveredRegistryHosts,imageRegistryChoices,imageRegistryParts,imageRegistryScopes,registryScopeOptions} from './image-management.js';
import {volumeMount} from './node-workspace.js';
import {editorCard,submitEditorOperation,showEditorPreview,showEditorForm} from './maintenance-page.js';
import {request,enc} from './api.js';
import {action,selected} from './app.js';

export function editStandaloneContainer(page,node,item,view,mode,configuration = {}) {
  const creating = mode === 'create';
  const recreate = mode === 'redeploy';
  const target = item?.full_id || item?.id;
  const name = item?.name || target;
  const preview = workspaceText('section','','compose-preview');
  const ports = editorRows('ports','端口映射',(item?.ports || []).filter(port => port.public).map(port => ({
    address:port.ip,host:port.public,container:port.private,protocol:port.protocol || 'tcp'
  })),[
    ['address','宿主机地址',{placeholder:'所有地址'}],
    ['host','宿主机端口',{type:'number',min:1,max:65535,placeholder:'自动'}],
    ['container','容器端口',{type:'number',min:1,max:65535,required:true}],
    ['protocol','协议',{options:[['tcp','TCP'],['udp','UDP'],['sctp','SCTP']]}]
  ],{addLabel:'添加端口'});
  const mounts = standaloneMountEditor(item?.mounts || [],view);
  const networkNames = names => names.filter(name => !['host','none'].includes(name));
  const attached = networkNames(item?.networks || []);
  const networks = editorRows('networks','网络',attached.map(network => ({network})),[
    ['network','节点网络',{options:resourceChoiceOptions([...networkNames((view.networks || []).map(network => network.name)),...attached])}]
  ],{addLabel:'添加网络'});
  const environment = editorMappingEditor('environment','环境变量',configuration.environment);
  const labels = editorMappingEditor('labels','标签',configuration.labels);
  const image = standaloneImageEditor(page,item,creating,view);
  // Prefill the current policy so the editor shows it. Only a changed choice
  // is sent: an untouched editor keeps the existing policy, matching how the
  // image field diffs against its initial value.
  const currentPolicy = String(item?.restart_policy || '');
  const [policyName,policyCount] = currentPolicy.split(':');
  const knownPolicy = ['no','always','unless-stopped','on-failure'].includes(policyName) &&
    (policyName !== 'on-failure' || /^\d+$/.test(policyCount || ''));
  const initialRestart = knownPolicy ? currentPolicy : '';
  const restart = optionField('restart','重启策略',[['',creating ? 'Docker 默认策略' : '沿用当前策略'],['no','不自动重启'],['always','总是重启'],
    ['unless-stopped','除非手动停止'],['on-failure','仅失败时重启']],knownPolicy ? policyName : '');
  const retries = field('restart-retries','失败重试次数',{type:'number',min:0,max:2147483647,step:1,placeholder:'不限',disabled:true});
  retries.hidden = true;
  restart.lastElementChild.required = false;
  restart.lastElementChild.addEventListener('change',event => {
    retries.hidden = retries.lastElementChild.disabled = event.target.value !== 'on-failure';
  });
  if (knownPolicy && policyName === 'on-failure') {
    retries.hidden = retries.lastElementChild.disabled = false;
    retries.lastElementChild.value = policyCount;
  }
  const runtimeFields = workspaceText('div','','editor-runtime-fields');
  if (creating) runtimeFields.append(field('name','容器名称',{pattern:'[A-Za-z0-9][A-Za-z0-9._\\-]{1,127}',
    maxLength:128,required:true,placeholder:'my-app'}));
  runtimeFields.append(restart,retries);
  const basics = editorCard('基础配置',runtimeFields,image.fields,image.hint);
  basics.dataset.editGroup = 'basics';
  const resources = containerResourceEditor(configuration.resources,item,view.host);
  const settings = [basics,resources.card,ports.card,mounts.card,networks.card,environment.card,labels.card];
  const collectLists = () => ({
    ports:ports.rows().map(formatEditorPort),
    ...mounts.read(),
    networks:networks.rows().map(({network}) => network.trim())
  });
  const readLists = recreate ? {} : collectLists();
  const form = managedForm(recreate ? [] : settings,creating ? '预览部署' : recreate ? '预览重建' : '预览变更',data => {
    preview.replaceChildren();
    const edit = {};
    if (!recreate) {
      const imageReference = image.read();
      if (creating || imageReference !== image.initial) {
        edit.image = imageReference;
        const scope = image.pullScope(imageReference);
        if (scope) edit.pull_scope = scope;
      }
      const policy = data.get('restart');
      const retryCount = data.get('restart-retries');
      const chosen = policy === 'on-failure' && retryCount ? policy + ':' + retryCount : policy;
      if (policy && chosen !== initialRestart) edit.restart = chosen;
      const lists = collectLists();
      for (const [key,values] of Object.entries(lists)) {
        if (JSON.stringify(values) !== JSON.stringify(readLists[key])) edit[key] = values;
      }
      for (const [key,group] of [['environment',environment],['labels',labels]]) {
        const mapping = group.changes();
        if (Object.keys(mapping).length) edit[key] = mapping;
      }
      const limits = resources.read(data);
      if (Object.keys(limits).length) edit.resources = limits;
      if (!Object.keys(edit).length) throw new Error('请至少修改一项配置，或使用重新部署。');
    }
    const limits = edit.resources;
    const live = !creating && !!limits && Object.keys(edit).length === 1 &&
      limits.nano_cpus !== 0 && limits.memory_bytes !== 0 &&
      !(configuration.legacy_cpu && Object.hasOwn(limits,'nano_cpus'));
    const command = creating ? {action:'container.create',name:data.get('name').trim(),edit} :
      live ? {action:'container.resources',target,resources:limits} : {action:'container.replace',target,edit};
    const confirmation = managementButton(creating ? '确认部署' : live ? '确认更新限制' : '确认替换',() =>
      submitEditorOperation(page,form,confirmation,node,command),!creating && !live,page.status);
    if (creating || live) confirmation.classList.add('primary');
    const summary = creating ? [['名称',command.name],['节点',node.label]] : [];
    if (edit.image) summary.push(['镜像',edit.image]);
    if (edit.pull_scope) summary.push(['镜像仓库',image.registryName()]);
    if (edit.restart) summary.push(['重启策略',edit.restart]);
    for (const [key,label] of [['ports','端口映射'],['volumes','命名卷'],['binds','Bind 挂载'],['networks','网络']]) {
      if (edit[key]) summary.push([label,edit[key].length ? edit[key].join(' · ') : '清空']);
    }
    if (edit.environment) summary.push(['环境变量',Object.keys(edit.environment).join('、')]);
    if (edit.labels) summary.push(['标签',Object.keys(edit.labels).join('、')]);
    if (limits) {
      if (Object.hasOwn(limits,'nano_cpus')) summary.push(['CPU 上限',limits.nano_cpus ? limits.nano_cpus / 1e9 + ' 核' : '不限']);
      if (Object.hasOwn(limits,'memory_bytes')) summary.push(['内存上限',limits.memory_bytes ? formatBytes(limits.memory_bytes) : '不限']);
      if (Object.hasOwn(limits,'pids')) summary.push(['PID 上限',limits.pids || '不限']);
    }
    if (!creating && limits) summary.push(['生效方式',live ? 'Docker 原地更新，保留容器和可写层' : '重建容器，可写层会被替换']);
    showEditorPreview(preview,creating ? '部署预览' : live ? '资源限制预览' : '替换预览',summary.length ? snapshotFacts(summary) :
      workspaceText('p','按当前配置和镜像重新创建。'),confirmation);
  },() => !page.pending && page.visible(),page.status);
  form.classList.add('compose-maintenance-form','editor-form','standalone-editor');
  form.autocomplete = 'off';
  const invalidate = () => {
    preview.replaceChildren();
    page.status.textContent = '';
  };
  form.addEventListener('input',invalidate);
  form.addEventListener('change',invalidate);
  const context = workspaceText('section','','editor-card container-editor-context');
  const identity = workspaceText('div','','container-editor-identity');
  identity.append(workspaceText('h2',creating ? node.label : name),
    workspaceText('p',creating ? '执行节点' : node.label + ' · 独立容器','maintenance-scope'));
  context.append(workspaceGlyph(creating ? 'server' : 'container'),identity);
  if (!creating) context.append(workspaceText('p',recreate ? '重建会替换容器可写层，运行中的服务可能短暂中断。' : '预览会显示原地更新或重建的生效方式。','maintenance-impact'));
  showEditorForm(page,context,form,preview);
  if (!recreate) void image.loadSources();
}

function containerResourceEditor(current,item,host) {
  const specs = [
    {key:'nano_cpus',label:'CPU 上限（核）',unit:1e9,initial:current?.nano_cpus ?? item?.nano_cpus ?? 0,min:10000000,max:1e15,step:'any',sliderMax:host?.cpu_count || 8,sliderStep:0.01},
    {key:'memory_bytes',label:'内存上限（MiB）',unit:2**20,initial:current?.memory_bytes ?? item?.memory_limit_bytes ?? 0,min:6*2**20,max:2**50,step:'any',sliderMax:Math.floor(host?.memory_total_bytes / 2**20) || 8192,sliderStep:1},
    {key:'pids',label:'PID 上限',unit:1,initial:current?.pids ?? item?.pids_limit ?? 0,min:1,max:2**31-1,step:1,sliderMax:1024,sliderStep:1}
  ];
  const inputs = workspaceText('div','','editor-runtime-fields');
  for (const {key,label,unit,initial,min,max,step,sliderMax,sliderStep} of specs) {
    const group = workspaceText('div','','resource-limit-control');
    const exactField = field(key,label,{type:'number',min:0,max:max/unit,step,value:initial ? initial/unit : '',placeholder:'不限'});
    const exact = exactField.lastElementChild;
    const slider = document.createElement('input');
    Object.assign(slider,{type:'range',min:0,step:sliderStep});
    slider.dataset.limit = key;
    slider.setAttribute('aria-label',label + '滑块');
    const updateSliderValue = () => {
      const value = exact.valueAsNumber || 0;
      slider.value = String(value);
      slider.setAttribute('aria-valuetext',value ? String(value) : '不限');
    };
    const syncExact = () => {
      slider.max = Math.min(max/unit,Math.max(sliderMax,min/unit,initial/unit,exact.valueAsNumber || 0));
      updateSliderValue();
    };
    exact.addEventListener('input',syncExact);
    slider.addEventListener('input',() => {
      let value = slider.valueAsNumber;
      if (value > 0 && value < min/unit) value = exact.valueAsNumber >= min/unit ? 0 : min/unit;
      exact.value = value ? String(value) : '';
      updateSliderValue();
    });
    syncExact();
    group.append(exactField,slider);
    inputs.append(group);
  }
  return {
    card:editorCard('资源限制',inputs,workspaceText('p','滑至最左端、留空或填写 0 表示不限。CPU 最小 0.01 核，内存最小 6 MiB。','editor-hint')),
    read(data) {
      const changed = {};
      for (const {key,label,unit,initial,min,max} of specs) {
        const value = Math.round(Number(data.get(key) || 0)*unit);
        if (value === initial) continue;
        if (!Number.isSafeInteger(value) || value < 0 || value > max || value > 0 && value < min) {
          throw new Error(label + '超出允许范围。');
        }
        changed[key] = value;
      }
      return changed;
    }
  };
}

function standaloneMountEditor(initial,view) {
  const volumes = (view.volumes || []).map(volume => volume.name);
  const rows = initial.filter(mount => ['volume','bind'].includes(mount.type)).flatMap(mount => {
    const row = {type:mount.type,destination:mount.destination,mode:mount.read_only ? 'ro' : 'rw'};
    if (mount.type === 'bind') return mount.source ? [{...row,bind:mount.source}] : [];
    const volume = mount.name || (view.volumes || []).find(volume => volumeMount(mount,volume))?.name;
    return volume ? [{...row,volume}] : [];
  });
  const choices = resourceChoiceOptions([...volumes,...rows.map(row => row.volume)]);
  const group = editorRows('volumes','卷挂载',rows,[
    ['type','挂载类型',{options:[['volume','命名卷'],['bind','Bind']]}],
    ['volume','命名卷',{options:choices,optionalOptions:true}],
    ['bind','宿主机路径',{required:true,placeholder:'/srv/data'}],
    ['destination','容器路径',{required:true,placeholder:'/data'}],
    ['mode','权限',{options:[['rw','可读写'],['ro','只读']]}]
  ],{addLabel:'添加挂载',configureRow:row => {
    const type = row.querySelector('[data-field=type]');
    const volume = row.querySelector('[data-field=volume]');
    if (!volume.value && choices.length === 1) type.value = 'bind';
    const bind = row.querySelector('[data-field=bind]');
    const toggle = () => {
      volume.parentElement.hidden = volume.disabled = type.value !== 'volume';
      bind.parentElement.hidden = bind.disabled = type.value !== 'bind';
    };
    type.addEventListener('change',toggle);
    toggle();
  }});
  group.card.append(workspaceText('p','Bind 仅用于有权管理该宿主机的管理员。请核对绝对路径和访问权限。','editor-hint'));
  return {card:group.card,read() {
    const result = {volumes:[],binds:[]};
    const destinations = new Set();
    for (const row of group.rows()) {
      const destination = row.destination.trim();
      if (destinations.has(destination)) throw new Error('每个挂载必须使用不同的容器路径。');
      destinations.add(destination);
      const bind = row.type === 'bind';
      const source = (bind ? row.bind : row.volume).trim();
      result[bind ? 'binds' : 'volumes'].push(source + ':' + destination + (row.mode === 'ro' ? ':ro' : ''));
    }
    return result;
  }};
}

export function splitStandaloneImage(value) {
  if (/^(?:sha256:)?[0-9a-f]{64}$/.test(value)) return ['@local',value];
  const slash = value.indexOf('/');
  const first = slash > 0 ? value.slice(0,slash) : '';
  if (['docker.io','index.docker.io','registry-1.docker.io'].includes(first.toLowerCase())) return ['docker.io',value.slice(slash+1)];
  if (first === 'localhost' || /[.:]/.test(first)) return [first,value.slice(slash+1)];
  return ['docker.io',value];
}

function standaloneImageEditor(page,item,creating,view) {
  const [oldHost,oldPath] = splitStandaloneImage(item?.image || item?.image_id || '');
  const options = [['docker.io','Docker Hub（docker.io）']];
  if (!['docker.io','@local'].includes(oldHost)) options.push([oldHost,oldHost + '（当前来源）']);
  for (const host of discoveredRegistryHosts(view)) {
    if (host !== oldHost.toLowerCase()) options.push([host,host + '（节点已使用）']);
  }
  options.push(['@custom','自定义仓库地址'],['@local','本地镜像 ID']);
  const source = optionField('image-source','镜像来源',options,oldHost);
  const sourceInput = source.lastElementChild;
  const custom = field('image-host','仓库地址',{required:true,maxLength:253,placeholder:'registry.example.com:5000'});
  const reference = field('image','镜像路径与标签或摘要',{value:oldPath,required:creating || !!oldPath,maxLength:512,placeholder:'namespace/image:tag'});
  const refInput = reference.lastElementChild;
  const update = () => {
    custom.hidden = custom.lastElementChild.disabled = sourceInput.value !== '@custom';
    refInput.placeholder = sourceInput.value === '@local' ? 'sha256:…' : 'namespace/image:tag';
  };
  sourceInput.addEventListener('change',update);
  update();
  const read = () => {
    const path = refInput.value.trim();
    if (!path && !creating) return '';
    if (sourceInput.value === '@local') {
      if (!/^(?:sha256:)?[0-9a-f]{64}$/.test(path)) throw new Error('请填写完整的本地镜像 ID。');
      return path;
    }
    const host = (sourceInput.value === '@custom' ? custom.lastElementChild.value : sourceInput.value).trim();
    if (!host || /[\s/@?#]/.test(host)) throw new Error('请填写仓库域名或带端口的地址，不含协议。');
    if (!path || /[\s\\]/.test(path) || path.startsWith('/') || path.includes('://')) throw new Error('请填写仓库下的镜像路径及标签或摘要。');
    return host + '/' + path;
  };
  const initial = oldPath ? read() : '';
  const scopeField = optionField('pull-scope','匹配的镜像仓库',[]);
  const select = scopeField.lastElementChild;
  select.required = false;
  scopeField.hidden = true;
  const fields = workspaceText('div','','editor-image-fields');
  custom.classList.add('editor-registry-address');
  reference.classList.add('editor-image-reference');
  fields.append(source,custom,reference,scopeField);
  const hint = workspaceText('p','','editor-hint');
  let registries;
  let scopes = [];
  function updateScopes(value) {
    const image = value && imageRegistryParts(value);
    scopes = image ? imageRegistryScopes(registries || [],image) : [];
    registryScopeOptions(select,scopes);
    scopeField.hidden = scopes.length < 2;
  }
  function refreshScopes() {
    let value = '';
    try { value = read(); } catch { /* A partially typed reference has no scope yet. */ }
    updateScopes(value);
  }
  for (const input of [refInput,custom.lastElementChild]) input.addEventListener('input',refreshScopes);
  sourceInput.addEventListener('change',refreshScopes);
  const reloadSources = async () => {
    hint.textContent = '正在读取共享镜像仓库…';
    try {
      registries = await imageRegistryChoices(() => page.visible());
      if (!page.visible()) return;
      for (const registry of registries) {
        if (!registry.enabled || !registry.host) continue;
        const existing = [...sourceInput.options].find(option => option.value === registry.host);
        if (existing) existing.textContent = registry.name + ' · ' + registry.host;
        else option(sourceInput,registry.host,registry.name + ' · ' + registry.host);
      }
      refreshScopes();
      hint.textContent = '节点缺少镜像时，使用系统设置中匹配的共享仓库拉取。';
    } catch {
      if (page.visible()) hint.replaceChildren('未能读取共享镜像仓库。',managementButton('重试',reloadSources));
    }
  };
  const pullScope = value => {
    if (splitStandaloneImage(value)[0] === '@local') return;
    // The server also resolves the registry when the catalog is still loading.
    updateScopes(value);
    if (!scopes.length) return;
    if (!select.value) throw new Error('有多个仓库与镜像路径匹配，请选择本次拉取使用的仓库。');
    return {registry_id:select.value};
  };
  return {fields,hint,read,initial,pullScope,registryName:() => select.selectedOptions[0]?.textContent,loadSources:reloadSources};
}

export async function readContainerConfiguration(node,target,cancelSignal) {
  if (!node.generation) throw new Error('无法获取节点代次，请先刷新节点列表。');
  const path = nodeOperationPath(node);
  const signal = AbortSignal.any([cancelSignal,AbortSignal.timeout(45000)]);
  let job = await request(path,{action:'container.edit-config',target,generation:node.generation},undefined,signal);
  const resultPath = path + '/' + enc(job.id);
  while (['queued','running'].includes(job.state)) {
    await new Promise(resolve => setTimeout(resolve,800));
    signal.throwIfAborted();
    job = await request(resultPath,undefined,undefined,signal);
  }
  if (job.state !== 'succeeded' || !job.result?.edit_config) {
    throw new Error(job.error || '读取当前容器环境变量和标签失败，请重试。');
  }
  return job.result.edit_config;
}

export function resourceChoiceOptions(names) {
  return [['','请选择'],...[...new Set(names.filter(Boolean))].sort((a,b) => a.localeCompare(b)).map(value => [value,value])];
}

export function editorRows(key,title,initial,columns,{addLabel,configureRow} = {}) {
  const list = workspaceText('div','','editor-rows');
  const card = editorCard(title);
  card.dataset.editGroup = key;
  const empty = workspaceText('p','','editor-hint');
  const add = actionIcon(managementButton(addLabel,() => {
    const row = appendRow({});
    signalsChange();
    row.querySelector('input,select,textarea')?.focus();
  }),'add',addLabel,false);
  add.classList.add('editor-add-row');
  const header = workspaceText('div','','editor-group-heading');
  header.append(card.firstElementChild,add);
  card.append(header,empty,list);
  setRows(initial);
  return {
    card, setRows,
    rows:() => [...list.children].map(row => Object.fromEntries(
      [...row.querySelectorAll('[data-field]')].map(control => [control.dataset.field,control.value])))
  };

  function updateState() {
    const unavailable = columns.some(([, , {options,optionalOptions}]) => options && !optionalOptions && !getOptions(options).some(([value]) => value));
    add.disabled = unavailable;
    empty.hidden = list.children.length !== 0;
    empty.textContent = unavailable ? '暂无可选资源。' : '当前没有配置项。';
  }

  function signalsChange() {
    updateState();
    card.dispatchEvent(new Event('change',{bubbles:true}));
  }

  function setRows(rows) {
    list.replaceChildren();
    rows.forEach(appendRow);
    updateState();
  }

  function getOptions(options) {
    return typeof options === 'function' ? options() : options;
  }

  function appendRow(values) {
    const row = workspaceText('div','','editor-row');
    for (const [column,label,{options,optionalOptions,tag = 'input',...settings}] of columns) {
      let wrapper;
      if (options) {
        const choices = getOptions(options);
        const selected = values[column] ?? choices[0][0];
        const retained = selected && !choices.some(([value]) => value === selected) ? [[selected,selected + '（当前值）'],...choices] : choices;
        wrapper = optionField('','',retained,selected);
      } else {
        wrapper = field('','',{...settings,value:values[column] ?? ''},tag);
      }
      const control = wrapper.lastElementChild;
      control.dataset.field = column;
      if (label) {
        control.setAttribute('aria-label',label);
        if (key !== 'networks') {
          const mountSource = key === 'volumes' && (column === 'volume' || column === 'bind');
          wrapper.prepend(workspaceText('span',mountSource ? '挂载来源' : label,'editor-field-caption'));
        }
      }
      row.append(wrapper);
    }
    const label = '移除一项' + title;
    const remove = actionIcon(managementButton(label,() => {
      row.remove();
      signalsChange();
      if (!add.disabled) add.focus();
      else (list.querySelector('input,select,textarea') || card.querySelector('input'))?.focus();
    }),'remove');
    remove.classList.add('editor-remove-row');
    row.append(remove);
    configureRow?.(row,values);
    list.append(row);
    return row;
  }
}

function editorMappingEditor(key,title,existing = {}) {
  const original = new Map(Object.entries(existing || {}));
  const isEnvironment = key === 'environment';
  const editable = name => isEnvironment ? /^[A-Za-z_][A-Za-z0-9_]*$/.test(name) :
    !!name && !/[ =\t\r\n\x00]/.test(name) && !name.toLowerCase().startsWith('com.docker.');
  const group = editorRows(key,title,[...original].map(([key,value]) => ({key,value})),[
    ['key','',{ariaLabel:title + '名称',required:true,placeholder:isEnvironment ? 'APP_MODE' : 'team'}],
    ['value','',{ariaLabel:title + '值',tag:'textarea',rows:1,spellcheck:false,autocomplete:'off'}]
  ],{addLabel:isEnvironment ? '添加变量' : '添加标签',configureRow:(row,values) => {
    const value = row.querySelector('[data-field=value]');
    if (values.value === null) {
      value.dataset.unset = 'true';
      value.placeholder = '未赋值';
      value.addEventListener('input',() => {
        delete value.dataset.unset;
        value.placeholder = '';
      });
    }
    if (values.key != null && !editable(values.key)) {
      row.querySelector('[data-field=key]').readOnly = true;
      value.readOnly = true;
      row.querySelector('.editor-remove-row').hidden = true;
    }
  }});
  const displayBaseline = new Map(group.rows().map(row => [row.key,row.value]));
  group.card.append(workspaceText('p',isEnvironment ?
    '当前值只在编辑中显示。应用密钥请通过部署目标维护。' :
    '更改名称等于移除原键并新增键。Docker 管理标签只能查看。','editor-hint'));
  return {
    card:group.card,
    changes() {
      const current = new Map();
      const inputs = [...group.card.querySelectorAll('[data-field=value]')];
      for (const [index,row] of group.rows().entries()) {
        if (original.has(row.key) && !editable(row.key)) {
          current.set(row.key,original.get(row.key));
          continue;
        }
        const name = row.key.trim();
        if (!name || current.has(name)) throw new Error(title + '的名称不能为空或重复。');
        const unchangedNull = original.get(name) !== null || inputs[index].dataset.unset;
        if (original.has(name) && unchangedNull && displayBaseline.get(name) === row.value) current.set(name,original.get(name));
        else current.set(name,row.value);
      }
      const changes = new Map();
      for (const name of original.keys()) if (!current.has(name)) changes.set(name,null);
      for (const [name,value] of current) if (original.get(name) !== value) changes.set(name,value);
      return Object.fromEntries(changes);
    }
  };
}

function formatEditorPort({address,host,container,protocol}) {
  address = address.trim();
  const binding = address.includes(':') && !address.startsWith('[') ? '[' + address + ']' : address;
  return (binding ? binding + ':' : '') + (binding || host ? host + ':' : '') + container + '/' + protocol;
}
