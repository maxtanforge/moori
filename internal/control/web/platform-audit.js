import {workspaceText,dateTime} from './ui-components.js';
import {currentSettingsPath,auditSettingsPath,settingsRevision,showSettingsList,managementButton} from './management.js';
import {request,enc} from './api.js';
import {action,detail} from './app.js';

const platformAuditActions = new Map([
  ['account.changed','修改账户密码'],
  ['account.created','创建用户'],
  ['account.password_locked','锁定账户'],
  ['account.password_reset','重置密码'],
  ['agent.token_created','创建节点注册令牌'],
  ['alert.settings_cleared','恢复默认告警阈值'],
  ['alert.settings_updated','更新告警阈值'],
  ['alert.target_created','添加告警通知目标'],
  ['alert.target_deleted','删除告警通知目标'],
  ['compose_template.version_created','创建 Compose 模板版本'],
  ['container.terminal','进入容器终端'],
  ['container.terminal.closed','退出容器终端'],
  ['node.display_name_changed','修改节点显示名称'],
  ['platform.initialized','初始化平台'],
  ['platform_admin.changed','调整平台管理员权限'],
  ['registry.created','添加镜像仓库'],
  ['registry.credential_rotated','更新镜像仓库凭据'],
  ['registry.updated','编辑镜像仓库'],
  ['registry.disabled','停用镜像仓库'],
  ['registry.enabled','启用镜像仓库'],
  ['webhook.created','创建容器更新 Webhook'],
  ['webhook.revoked','吊销容器更新 Webhook']
]);
const auditActorNames = new Map([['local','本地命令行'],['system','系统']]);
let platformAuditRequest = 0;

export async function listPlatformAudit(before = 0, history = []) {
  if (currentSettingsPath !== auditSettingsPath) return;
  const revision = settingsRevision;
  const load = ++platformAuditRequest;
  const page = await request('/api/v1/audit?before=' + enc(before));
  if (revision !== settingsRevision || load !== platformAuditRequest) return;
  const rows = (page.items || []).map(event => {
    const created = event.created_at;
    // SQLite's CURRENT_TIMESTAMP is UTC, with a space in place of the ISO separator.
    const iso = /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/.test(created) ? created.replace(' ','T') + 'Z' : created;
    const time = workspaceText('time',dateTime(iso));
    if (iso && time.textContent !== '—') time.dateTime = iso;
    const action = platformAuditActions.get(event.action) || event.action;
    const actor = event.actor_name || auditActorNames.get(event.actor) || event.actor;
    let detail = event.detail;
    if (event.action === 'platform_admin.changed') {
      if (detail === 'granted=true') detail = '授予权限';
      if (detail === 'granted=false') detail = '移除权限';
    }
    return [time,action,actor || '—',event.subject_name || event.subject || '—',detail || '—'];
  });
  showSettingsList('平台审计',null,['时间','操作','操作者','对象','详情'],rows,'暂无平台审计记录。',
    history.length && managementButton('上一页',() => listPlatformAudit(history.at(-1),history.slice(0,-1))),
    page.next && managementButton('下一页',() => listPlatformAudit(page.next,[...history,before])));
}
