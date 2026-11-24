import {workspaceText,field,resourceTable,managementActions,dateTime} from './ui-components.js';
import {currentSettingsPath,alertSettingsPath,settingsRevision,settingsCreateButton,managedForm,managementButton,settingsTableEntry,managementDialog,managementText} from './management.js';
import {request,element,enc} from './api.js';

const alertSettingsAPI = '/api/v1/alert-settings';
const alertTargetsAPI = '/api/v1/alert-targets';
const alertTargetLimit = 10;
let alertSettingsRequest = 0;
let alertSettingsFeedback = '';

export async function listAlertSettings() {
  if (currentSettingsPath !== alertSettingsPath) return;
  const revision = settingsRevision;
  const load = ++alertSettingsRequest;
  const [settings, targets] = await Promise.all([request(alertSettingsAPI), request(alertTargetsAPI)]);
  if (revision !== settingsRevision || load !== alertSettingsRequest) return;
  renderAlertSettings(settings, targets.items || []);
}

function renderAlertSettings(settings, targets) {
  const panel = element('settings-list');
  const restoreFocus = panel.contains(document.activeElement);
  const heading = workspaceText('h2','告警通知');
  heading.id = 'alert-settings-title';
  heading.tabIndex = -1;
  const add = settingsCreateButton('添加通知目标',editAlertTarget);
  if (targets.length >= alertTargetLimit) {
    add.disabled = true;
    add.title = '通知目标已达上限（' + alertTargetLimit + ' 个），请先删除不再使用的目标。';
  }
  const bar = workspaceText('header','','work-panel-header settings-table-heading');
  bar.append(heading,add);

  const body = workspaceText('div','','user-settings-editor alert-settings-editor');
  const status = workspaceText('p','','user-settings-status');
  status.setAttribute('role','status');
  const feedback = workspaceText('p','','alert-feedback');
  feedback.setAttribute('role','status');
  feedback.textContent = alertSettingsFeedback;
  alertSettingsFeedback = '';

  const note = workspaceText('p','','alert-settings-note');
  note.textContent = settings.enabled ?
    (settings.custom ? '自定义阈值 · ' + [settings.updated_by,dateTime(settings.updated_at)].filter(Boolean).join(' · ') : '当前使用默认阈值。') :
    '节点监控未启用，告警评估与通知不会运行。';

  const policyTitle = workspaceText('h3','告警阈值');
  policyTitle.id = 'alert-policy-title';
  const form = managedForm([
    field('cpu_percent','CPU 阈值（%）',{type:'number',min:1,max:100,step:1,required:true,value:settings.cpu_percent}),
    field('memory_percent','内存阈值（%）',{type:'number',min:1,max:100,step:1,required:true,value:settings.memory_percent}),
    field('sustained_seconds','持续超过（秒）',{type:'number',min:1,max:3600,step:1,required:true,value:settings.sustained_seconds})
  ],'保存阈值',async data => {
    await saveAlertSettings({cpu_percent:Number(data.get('cpu_percent')),memory_percent:Number(data.get('memory_percent')),sustained_seconds:Number(data.get('sustained_seconds'))});
    alertSettingsFeedback = '已保存告警阈值，下一个评估周期生效。';
    await listAlertSettings();
  },() => true,status);
  form.id = 'alert-policy-form';
  form.method = 'post';
  form.setAttribute('aria-labelledby',policyTitle.id);
  const reset = managementButton('恢复默认阈值',async () => {
    if (!window.confirm('恢复默认告警阈值？当前自定义阈值将被清除。')) return;
    await request(alertSettingsAPI,undefined,undefined,undefined,'DELETE');
    alertSettingsFeedback = '已恢复默认阈值。';
    await listAlertSettings();
  },true,status);
  form.append(reset);

  const targetsTitle = workspaceText('h3','通知目标（' + targets.length + ' / ' + alertTargetLimit + '）');
  targetsTitle.id = 'alert-targets-title';
  let targetsArea;
  if (!targets.length) {
    targetsArea = workspaceText('p','尚未配置通知目标。配置后，告警触发和恢复事件会以 JSON POST 送达。','work-empty');
  } else {
    const rows = targets.map(target => {
      const remove = managementButton('删除',() => confirmAlertTargetDeletion(target),true,status);
      remove.setAttribute('aria-label','删除通知目标 ' + alertTargetOrigin(target.url));
      return [
        settingsTableEntry(target.url,''),
        settingsTableEntry(target.created_by === 'environment' ? '环境迁移' : target.created_by || '—',dateTime(target.created_at)),
        remove
      ];
    });
    targetsArea = resourceTable(['通知目标','添加者','操作'],rows,true);
    targetsArea.classList.add('settings-table');
    targetsArea.firstElementChild.setAttribute('aria-labelledby',targetsTitle.id);
    const footer = workspaceText('footer','','settings-table-footer');
    footer.append(workspaceText('span','已显示 ' + rows.length + ' 条'));
    targetsArea = [targetsArea,footer];
  }

  body.append(note,policyTitle,form,status,feedback,targetsTitle,...[].concat(targetsArea));
  panel.replaceChildren(bar,body);
  if (restoreFocus) heading.focus({preventScroll:true});

  function editAlertTarget() {
    panel.querySelector('#alert-target-editor')?.remove();
    const editor = workspaceText('section','','alert-target-editor');
    editor.id = 'alert-target-editor';
    const editorStatus = workspaceText('p','','user-settings-status');
    editorStatus.setAttribute('role','status');
    const cancel = managementButton('取消',() => editor.remove());
    const targetForm = managedForm([field('url','Webhook 地址',{type:'url',required:true,autocomplete:'off',maxLength:512,placeholder:'https://hooks.example.com/alert'})],
      '添加',async data => {
        try {
          await request(alertTargetsAPI,{url:data.get('url').trim()});
        } catch (error) {
          if (error.status === 400) throw new Error('地址无效：需要不含凭据和片段的绝对 HTTP(S) 地址。');
          if (error.status === 409) throw new Error('通知目标已达上限（' + alertTargetLimit + ' 个），请先删除不再使用的目标。');
          throw error;
        }
        alertSettingsFeedback = '已添加通知目标。';
        await listAlertSettings();
      },() => true,editorStatus);
    targetForm.method = 'post';
    targetForm.append(managementActions(cancel));
    const editorTitle = workspaceText('h3','添加通知目标');
    editor.append(editorTitle,targetForm,editorStatus);
    bar.after(editor);
    targetForm.querySelector('input').focus();
  }

  function confirmAlertTargetDeletion(target) {
    const cancel = managementButton('取消',() => element('management-dialog').close());
    const confirmButton = managementButton('确认删除',async () => {
      await request(alertTargetsAPI + '/' + enc(target.id),undefined,undefined,undefined,'DELETE');
      element('management-dialog').close();
      alertSettingsFeedback = '已删除通知目标。';
      await listAlertSettings();
    },true);
    managementDialog('删除通知目标',
      managementText('删除后，' + target.url + ' 不再接收告警事件。历史告警不受影响。'),
      managementActions(cancel,confirmButton));
  }
}

async function saveAlertSettings(payload) {
  await request(alertSettingsAPI,payload,undefined,undefined,'PUT');
}

function alertTargetOrigin(url) {
  try { return new URL(url).host || url; } catch { return url; }
}
