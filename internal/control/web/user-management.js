import {workspaceText,statusPill,field} from './ui-components.js';
import {settingsRevision,currentSettingsPath,userSettingsPath,settingsCreateButton,managementButton,showSettingsList,managementText,managedForm,loadSystemSettings} from './management.js';
import {request,enc,element,show,notice} from './api.js';
import {action,goToConsoleView} from './app.js';

const usersPath = '/api/v1/users';
let usersRequest = 0;

export async function listUsers(after = '', history = []) {
  const revision = settingsRevision;
  if (currentSettingsPath !== userSettingsPath) return;
  const load = ++usersRequest;
  const page = await request(usersPath + (after ? '?after=' + enc(after) : ''));
  if (revision !== settingsRevision || load !== usersRequest) return;
  const reopenList = () => listUsers(after,history);
  const create = settingsCreateButton('新建用户',() => editUserAccount(null,() => listUsers()));
  const rows = (page.items || []).map(user => {
    const self = user.username === element('workspace-username').textContent;
    const identity = workspaceText('div','','user-identity');
    identity.append(workspaceText('span',user.username,'settings-entry-name'));
    if (self) identity.append(statusPill('当前账号'));
    const role = workspaceText('span',user.platform_admin ? '平台管理员' : '普通用户','settings-role');
    const locked = user.locked_until && Date.parse(user.locked_until) > Date.now();
    const status = statusPill(locked ? '已锁定' : '正常',locked ? 'warn' : '');
    if (locked) status.title = '锁定至 ' + new Date(user.locked_until).toLocaleString();
    const action = self ? managementButton('账户设置',() => {
      goToConsoleView('console');
      show('password-form',true);
    }) : managementButton('重置密码',() => editUserAccount(user,reopenList));
    action.setAttribute('aria-label',(self ? '账户设置' : '重置密码') + ' · ' + user.username);
    return [identity,role,status,action];
  });
  showSettingsList('用户管理',create,['用户名','平台角色','登录状态','操作'],rows,'暂无用户。',
    history.length && managementButton('上一页',() => listUsers(history.at(-1),history.slice(0,-1))),
    page.next && managementButton('下一页',() => listUsers(page.next,[...history,after])));
}

function editUserAccount(user, reopenList) {
  usersRequest++;
  const revision = settingsRevision;
  const panel = element('settings-list');
  const title = workspaceText('h2',user ? '重置密码 · ' + user.username : '新建用户');
  const bar = workspaceText('header','','work-panel-header');
  bar.append(title,managementButton('返回用户列表',reopenList));
  const body = workspaceText('div','','user-settings-editor');
  const status = workspaceText('p','','user-settings-status');
  status.setAttribute('role','status');
  const newPassword = {type:'password',autocomplete:'new-password',required:true,minLength:12,maxLength:1024};
  const passwordName = user ? 'replacement' : 'password';
  const fields = [
    user ? field('current','管理员当前密码',{type:'password',autocomplete:'current-password',required:true,maxLength:1024}) :
      field('username','用户名',{autocomplete:'off',required:true,maxLength:64,pattern:'[A-Za-z0-9._\\-]+'}),
    field(passwordName,user ? '新密码' : '初始密码',newPassword),
    field('confirmation','确认密码',newPassword)
  ];
  const hint = managementText(user ? '至少 12 个字符。重置成功后，该账号的旧会话将注销，密码锁定将解除。' : '密码至少 12 个字符。新用户的项目访问权限可在对应项目中分配。');
  const form = managedForm(fields,user ? '重置密码' : '创建账号',async (data,form) => {
    if (data.get(passwordName) !== data.get('confirmation')) throw new Error('两次密码输入不一致。');
    const payload = Object.fromEntries(data);
    delete payload.confirmation;
    for (const input of form.querySelectorAll('input[type=password]')) input.value = '';
    try {
      await request(user ? usersPath + '/' + enc(user.id) + '/password' : usersPath,payload);
    } catch (error) {
      if (!user && error.status === 409) throw new Error('该用户名已存在。');
      throw error;
    }
    if (revision !== settingsRevision || !form.isConnected) return;
    notice(user ? '密码已重置。' : '账号已创建。');
    try { await reopenList(); }
    catch { if (revision === settingsRevision) await loadSystemSettings(); }
  },undefined,status);
  form.method = 'post';
  body.append(hint,form,status);
  panel.replaceChildren(bar,body);
  form.querySelector('input').focus();
}
