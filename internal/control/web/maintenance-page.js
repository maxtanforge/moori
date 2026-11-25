import {workspaceText,routeHref,workspaceLink} from './ui-components.js';
import {nodeById,containerOperationMessage,isMissingContainer,managementButton,performOperation} from './management.js';
import {nodePath,nodeManagementIssue} from './node-workspace.js';
import {openMaintainedDeployment,maintenanceDeploymentPath,editComposeDeployment,editExistingCompose,createComposeProject} from './compose-maintenance.js';
import {editStandaloneContainer,readContainerConfiguration} from './container-maintenance.js';
import {deploymentRunsPanel} from './ci-management.js';
import {element,enc,notice,request} from './api.js';
import {renderConsoleView,currentRoute,accountEpoch,projects} from './app.js';

let maintenancePage;

window.addEventListener('pagehide',() => {
  if (maintenancePage?.privateConfig) renderMaintenancePage(null);
});
window.addEventListener('pageshow',event => {
  if (event.persisted && !maintenancePage && !element('maintenance-page').hidden) renderConsoleView(currentRoute());
});

export function deploymentEditorPath(project,id,mode) {
  return ['deployment',enc(project),enc(id),mode].join('/');
}

export function parseDeploymentEditor(value) {
  const [type,project,id,mode,extra] = value.split('/');
  if (type !== 'deployment' || extra !== undefined || !project || !id || !['edit','redeploy'].includes(mode)) return null;
  try {
    const decoded = [project,id].map(decodeURIComponent);
    if (decoded.some(value => !value || value.length > 128 || value.includes('/'))) return null;
    return {project:decoded[0],deployment:decoded[1],mode};
  } catch {
    return null;
  }
}

export function renderMaintenancePage(route,key) {
  if (!route && !maintenancePage || route && maintenancePage?.key === key) return;
  const panel = element('maintenance-panel');
  maintenancePage?.cancelLoad?.();
  panel.replaceChildren();
  maintenancePage = undefined;
  if (!route) return;

  const account = accountEpoch;
  const body = workspaceText('div','','editor-body');
  const status = workspaceText('p','','editor-status');
  status.id = 'maintenance-status';
  status.setAttribute('role','status');
  const output = workspaceText('pre','','editor-output');
  output.hidden = true;
  output.tabIndex = 0;
  const results = workspaceText('div','','editor-results');
  results.id = 'maintenance-results';
  const main = workspaceText('div','','editor-main');
  main.append(body,status,results,output);
  const navigation = workspaceText('nav','','editor-nav');
  navigation.setAttribute('aria-label','配置分组');
  navigation.hidden = true;
  const page = {key,body,status,output,results,navigation,visible:() => maintenancePage === page && accountEpoch === account};
  maintenancePage = page;
  panel.append(main,navigation);
  const composeCreate = route.category === 'compose' && route.mode === 'create';
  const title = composeCreate ? '创建 Compose 项目' : route.mode === 'deployments' ? '部署记录' : route.mode === 'create' ? '部署容器' : route.mode === 'redeploy' ? '重新部署' :
    route.category === 'containers' ? '编辑容器配置' : '修改 Compose 配置';
  element('maintenance-title').textContent = title;
  const back = element('maintenance-back');
  back.onclick = null;
  if (route.project) {
    back.href = routeHref('console');
    back.textContent = '返回部署详情';
    back.onclick = event => {
      if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      event.preventDefault();
      openMaintainedDeployment(route.project,route.deployment,account).catch(error => {
        if (page.visible()) page.status.textContent = error.message;
        else if (account === accountEpoch) notice(error.message,'error');
      });
    };
    void loadMaintenanceEditor(page,async () => {
      const item = await request(maintenanceDeploymentPath(route.project,route.deployment));
      if (page.visible()) editComposeDeployment(page,route.project,item,route.mode === 'redeploy');
    });
  } else {
    if (route.container) page.containerList = nodePath(route.id,'containers');
    back.href = routeHref(route.returnPath || nodePath(route.id,route.category,route.container || route.resource));
    back.textContent = composeCreate ? '返回 Compose 项目列表' : route.mode === 'create' ? '返回容器列表' : '返回详情';
    status.textContent = route.mode === 'deployments' ? '正在读取资源…' : '正在读取当前节点配置…';
  }
}

export function initializeNodeMaintenance(route,snapshot) {
  const page = maintenancePage;
  if (!page?.visible() || page.initialized || page.loading || page.loadFailed || !snapshot) return;
  if (route.mode === 'deployments') {
    void loadMaintenanceEditor(page,() => loadDeploymentHistory(page,route,snapshot),'正在读取部署对象…');
    return;
  }
  const issue = nodeManagementIssue(route.id,snapshot);
  if (issue) {
    page.status.textContent = [issue.title,issue.reason,issue.next].filter(Boolean).join('。');
    return;
  }
  const view = snapshot.overview;
  if (!view?.available) {
    page.status.textContent = snapshot.message || '该节点暂无可用的 Docker 配置。';
    return;
  }
  const node = nodeById(route.id);
  const recreate = route.mode === 'redeploy';
  if (route.category === 'containers') {
    if (route.mode === 'create') {
      page.initialized = true;
      page.status.textContent = '';
      editStandaloneContainer(page,node,null,view,route.mode);
      return;
    }
    const item = (view.containers || []).find(container => (container.full_id || container.id) === route.container);
    if (!item || item.compose_project || !['created','running','exited'].includes(item.state)) {
      page.status.textContent = '此容器当前不可编辑。请从资源详情核对状态和所属项目。';
      return;
    }
    if (recreate) {
      page.initialized = true;
      page.status.textContent = '';
      editStandaloneContainer(page,node,item,view,route.mode);
    } else {
      void loadMaintenanceEditor(page,async () => {
        const controller = new AbortController();
        page.privateConfig = true;
        page.cancelLoad = () => controller.abort();
        try {
          const config = await readContainerConfiguration(node,route.container,controller.signal);
          if (!page.visible()) return;
          if (config.labels?.['com.docker.compose.project'] || config.labels?.['com.docker.swarm.service.id']) {
            throw new Error('此容器由 Compose 项目或 Swarm 服务管理，请在其配置中修改。');
          }
          editStandaloneContainer(page,node,item,view,route.mode,config);
        } finally {
          page.cancelLoad = undefined;
        }
      },'正在读取当前容器配置…');
    }
    return;
  }
  if (route.mode === 'create') {
    page.initialized = true;
    page.status.textContent = '';
    createComposeProject(page,node);
    return;
  }
  const project = (view.compose_projects || []).find(item => item.name === route.resource);
  if (!project) {
    page.status.textContent = '此 Compose 项目未出现在当前节点快照中。';
    return;
  }
  page.status.textContent = '';
  if (/^moori-[a-f0-9]{32}$/.test(project.name)) {
    void loadMaintenanceEditor(page,async () => {
      const source = await request('/api/v1/nodes/' + enc(node.id) + '/compose-projects/' + enc(project.name));
      if (!page.visible()) return;
      const item = await request(maintenanceDeploymentPath(source.project,source.deployment));
      if (page.visible()) editComposeDeployment(page,source.project,item,recreate,source.node);
    });
  } else {
    page.initialized = true;
    editExistingCompose(page,node,project.name,recreate,view);
  }
}

async function loadDeploymentHistory(page,route,snapshot) {
  if (route.category === 'containers') {
    const item = snapshot.overview?.containers?.find(candidate => (candidate.full_id || candidate.id) === route.container);
    if (!item?.name || item.compose_project) throw new Error('当前快照中没有此独立容器。');
    const query = new URLSearchParams({generation:String(snapshot.generation),container_id:route.container});
    page.body.replaceChildren(workspaceText('h2',item.name),deploymentRunsPanel(
      '/api/v1/nodes/' + enc(route.id) + '/container-deployment-runs/' + enc(item.name) + '?' + query));
    return;
  }
  const account = accountEpoch;
  page.body.replaceChildren(deploymentRunsPanel(
    '/api/v1/nodes/' + enc(route.id) + '/compose-projects/' + enc(route.resource) + '/runs',
    (id, scope) => openMaintainedDeployment(scope.project,id,account)));
}

async function loadMaintenanceEditor(page,load,loadingLabel = '正在读取当前部署版本…') {
  if (!page.visible() || page.loading) return;
  page.loading = true;
  page.loadFailed = false;
  page.results.replaceChildren();
  page.status.textContent = loadingLabel;
  try {
    await load();
    if (page.visible()) {
      page.initialized = true;
      if (page.status.textContent === loadingLabel) page.status.textContent = '';
    }
  } catch (error) {
    if (page.visible()) {
      page.loadFailed = true;
      page.status.textContent = error.name === 'TimeoutError' ? '读取配置超时，请重试。' :
        containerOperationMessage(error.message) || '读取配置失败。';
      page.results.append(page.containerList && isMissingContainer(error.message) ?
        workspaceLink(page.containerList,'返回容器列表','workspace-link') :
        managementButton('重试读取配置',() => loadMaintenanceEditor(page,load,loadingLabel),false,page.status));
    }
  } finally {
    page.loading = false;
  }
}

export function editorCard(title,...content) {
  const section = workspaceText('section','','editor-card');
  section.append(workspaceText('h2',title),...content);
  return section;
}

export function showEditorForm(page,context,form,preview) {
  context.classList.add('editor-context');
  const submit = form.querySelector('button[type=submit]');
  const actions = workspaceText('div','','editor-submit-bar');
  actions.append(submit);
  form.append(actions);
  page.body.replaceChildren(context,form,preview);

  const groups = [...form.querySelectorAll('.editor-card')];
  page.navigation.hidden = groups.length < 3;
  if (page.navigation.hidden) return;
  const selectGroup = button => {
    page.navigation.querySelector('[aria-current]')?.removeAttribute('aria-current');
    button.setAttribute('aria-current','location');
  };
  const choices = groups.map(group => {
    const heading = group.querySelector('h2');
    heading.tabIndex = -1;
    const button = document.createElement('button');
    button.type = 'button';
    button.textContent = heading.textContent;
    button.addEventListener('click',() => {
      selectGroup(button);
      heading.focus({preventScroll:true});
      group.scrollIntoView({block:'start'});
    });
    return button;
  });
  page.navigation.replaceChildren(workspaceText('h2','配置分组'),...choices);
  if (form.classList.contains('standalone-editor')) {
    form.id = 'standalone-maintenance-form';
    const navSubmit = workspaceText('button',submit.textContent,'primary editor-nav-submit');
    navSubmit.type = 'submit';
    navSubmit.setAttribute('form',form.id);
    page.navigation.append(navSubmit);
  }
  selectGroup(choices[0]);
  form.addEventListener('focusin',event => {
    const choice = choices[groups.indexOf(event.target.closest('.editor-card'))];
    if (choice) selectGroup(choice);
  });
}

export function showEditorPreview(preview,...content) {
  const heading = workspaceText('h2',content.shift());
  heading.tabIndex = -1;
  preview.replaceChildren(heading,...content);
  heading.focus({preventScroll:true});
  heading.scrollIntoView({block:'nearest'});
}

export function lockEditorForm(form) {
  const controls = [...form.elements].filter(control => !control.disabled);
  controls.forEach(control => { control.disabled = true; });
  return () => controls.forEach(control => { control.disabled = false; });
}

export async function submitEditorOperation(page,form,confirmation,node,command,path) {
  if (page.pending || !page.visible()) return;
  page.pending = true;
  confirmation.hidden = true;
  const unlock = lockEditorForm(form);
  page.results.replaceChildren();
  page.output.hidden = true;
  try {
    page.completed = await performOperation(node,command,path,page) === 'succeeded';
  } catch (error) {
    confirmation.hidden = false;
    throw error;
  } finally {
    page.pending = false;
    if (!page.completed && page.visible()) unlock();
  }
}
