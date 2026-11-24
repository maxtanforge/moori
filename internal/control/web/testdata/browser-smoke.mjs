import {spawn} from 'node:child_process';
import {once} from 'node:events';
import assert from 'node:assert/strict';
import {mkdir, writeFile} from 'node:fs/promises';
import {join} from 'node:path';
import {chromiumExit, removeChromiumProfile} from './chromium-cleanup.mjs';

const [url, binary, profile, mode] = process.argv.slice(2);
const pageURL = new URL(url);
const sessionTest = mode === 'direct-http' || mode === 'proxy-https';
const hostArgs = sessionTest ?
  ['--no-proxy-server', '--host-resolver-rules=MAP moori-direct.test ' + pageURL.hostname] : [];
if (hostArgs.length) pageURL.hostname = 'moori-direct.test';
const child = spawn(binary, ['--headless', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--disable-background-networking', '--disable-features=OptimizationGuideModelDownloading,OptimizationHints,OptimizationGuideOnDeviceModel',
  '--ignore-certificate-errors', '--remote-debugging-port=0', '--user-data-dir=' + profile, ...hostArgs, 'about:blank'],
  {windowsHide: true, stdio: ['ignore', 'ignore', 'pipe']});
const exited = chromiumExit(child);
let socket;
let serial = 0;
const pending = new Map();
function send(method, params = {}, sessionId) {
  const id = ++serial;
  return new Promise((resolve, reject) => {
    pending.set(id, {resolve, reject});
    socket.send(JSON.stringify({id, method, params, sessionId}));
  });
}
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
try {
  const endpoint = await new Promise((resolve, reject) => {
    let output = '';
    const timer = setTimeout(() => reject(new Error('browser startup timeout: ' + output.slice(-2000))), 25000);
    child.on('error', reject);
    child.stderr.on('data', chunk => {
      output += chunk;
      const match = output.match(/DevTools listening on (ws:\/\/\S+)/);
      if (match) { clearTimeout(timer); resolve(match[1]); }
    });
    child.once('exit', code => { clearTimeout(timer); reject(new Error(`browser exited ${code}: ${output}`)); });
  });
  socket = new WebSocket(endpoint);
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data);
    const call = pending.get(message.id);
    if (!call) return;
    pending.delete(message.id);
    if (message.error) call.reject(new Error(JSON.stringify(message.error)));
    else call.resolve(message.result);
  });
  await once(socket, 'open');
  const {targetId} = await send('Target.createTarget', {url:pageURL.href});
  const {sessionId} = await send('Target.attachToTarget', {targetId, flatten: true});
  const evaluate = async expression => {
    const response = await send('Runtime.evaluate', {expression, returnByValue: true, awaitPromise: true}, sessionId);
    if (response.exceptionDetails) throw new Error(JSON.stringify(response.exceptionDetails));
    return response.result.value;
  };
  const screenshot = async name => {
    const directory = process.env.MOORI_SCREENSHOTS;
    if (!directory) return;
    await mkdir(directory,{recursive:true});
    await evaluate('document.fonts.ready');
    const {data} = await send('Page.captureScreenshot',{format:'png'},sessionId);
    await writeFile(join(directory,name + '.png'),Buffer.from(data,'base64'));
  };
  const until = async (expression, attempts = 100) => {
    for (let i = 0; i < attempts; i++) {
      if (await evaluate(expression)) return;
      await delay(50);
    }
    throw new Error('timed out: ' + expression);
  };
  const openView = view => evaluate("document.querySelector('#console-nav a[href=\"/" + view + "\"]').click()");
  if (sessionTest) {
    await until("location.hostname === 'moori-direct.test' && document.readyState === 'complete'");
    assert.equal(await evaluate('window.isSecureContext'), mode === 'proxy-https');
    await until("!document.getElementById('setup-panel').hidden");
    await evaluate(`var f=document.getElementById('setup-form'); f.elements.username.value='admin'; f.elements.password.value='initial password'; f.elements.confirmation.value='initial password'; f.requestSubmit()`);
    await until("!document.getElementById('console').hidden && document.getElementById('workspace-username').textContent === 'admin'");
    assert.equal(await evaluate("document.getElementById('agent-endpoint').value === (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/agent/v1/connect'"), true);
    await evaluate("document.getElementById('logout').click()");
    await until("!document.getElementById('login-panel').hidden");
    await evaluate(`var f=document.getElementById('login-form'); f.elements.username.value='admin'; f.elements.password.value='initial password'; f.requestSubmit()`);
    await until("!document.getElementById('console').hidden && document.getElementById('workspace-username').textContent === 'admin'");
    const {cookies} = await send('Network.getCookies', {urls:[pageURL.href]}, sessionId);
    const expectedCookie = mode === 'proxy-https' ? '__Host-moori-session' : 'moori-session';
    const sessionCookie = cookies.find(cookie => cookie.name === expectedCookie);
    assert.ok(sessionCookie?.httpOnly);
    assert.equal(sessionCookie.secure, mode === 'proxy-https');
    assert.deepEqual(await evaluate(`(async () => {
      const path = '/api/v1/container-webhooks';
      const created = await fetch(path,{method:'POST',headers:{'Content-Type':'application/json'},
        body:JSON.stringify({node:'@local',generation:1,container:'web',repository:'docker.io/library/nginx'})});
      const {id} = await created.json();
      const revoked = await fetch(path + '/' + encodeURIComponent(id),{method:'DELETE'});
      return [created.status,revoked.status];
    })()`),[201,204],'webhook revocation preserves the console session');
    console.log('PASS: setup, session, logout, login and Agent endpoint through ' + mode);
  } else if (mode === 'setup') {
    await until(`document.getElementById('setup-panel') && !document.getElementById('setup-panel').hidden`);
    await send('Emulation.setDeviceMetricsOverride', {width:1440,height:900,deviceScaleFactor:1,mobile:false}, sessionId);
    await screenshot('setup');
    await evaluate(`var f=document.getElementById('setup-form'); f.elements.username.value='admin'; f.elements.password.value='initial password'; f.elements.confirmation.value='different password'; f.requestSubmit()`);
    await until(`document.getElementById('notice').textContent.includes('两次密码')`);
    assert.equal(await evaluate("document.getElementById('notice').dataset.tone"),'error');
    await evaluate(`document.getElementById('setup-form').elements.confirmation.value='initial password'; document.getElementById('setup-form').requestSubmit()`);
    await until(`!document.getElementById('console').hidden && !document.getElementById('empty-projects').hidden`);
    assert.equal(await evaluate(`document.querySelector('#console-nav [aria-current=page]').pathname`), '/environments');
    assert.equal(await evaluate(`document.getElementById('project').getClientRects().length`), 0);
    await send('Emulation.setDeviceMetricsOverride', {width:1440,height:900,deviceScaleFactor:1,mobile:false}, sessionId);
    await openView('environments');
    await until(`location.pathname === '/environments' && document.getElementById('environments').getClientRects().length > 0`);
    assert.equal(await evaluate(`document.getElementById('project').getClientRects().length`), 0);
    assert.equal(await evaluate(`document.getElementById('console-sidebar').getBoundingClientRect().right <= document.getElementById('main').getBoundingClientRect().left`), true);
    assert.equal(await evaluate("document.querySelector('#console-nav a[href=\"/monitoring\"]')"), null);
    await evaluate("history.pushState(null, '', '/node/%40local/monitor'); dispatchEvent(new PopStateEvent('popstate'))");
    await until("document.querySelector('#monitor-charts canvas') && document.getElementById('monitor-charts').textContent.includes('24')");
    await evaluate('location.reload()');
    await until("location.pathname === '/node/%40local/monitor' && document.querySelector('#console-nav [data-view=environments][aria-current=page]') && document.querySelector('#monitor-charts canvas')");
    await evaluate('history.back()');
    await until("location.pathname === '/environments' && document.getElementById('monitoring').hidden");
    await send('Emulation.setDeviceMetricsOverride', {width:390,height:844,deviceScaleFactor:1,mobile:true}, sessionId);
    await until(`document.getElementById('sidebar-toggle').getClientRects().length > 0`);
    assert.ok(await evaluate("document.getElementById('sidebar-toggle').getBoundingClientRect().height >= 44"), 'mobile navigation touch target');
    assert.equal(await evaluate(`document.getElementById('console-sidebar').getClientRects().length`), 0);
    await evaluate(`document.getElementById('sidebar-toggle').click()`);
    assert.equal(await evaluate(`document.getElementById('sidebar-toggle').getAttribute('aria-expanded')`), 'true');
    await openView('environments');
    assert.equal(await evaluate(`document.getElementById('sidebar-toggle').getAttribute('aria-expanded')`), 'false');
    assert.equal(await evaluate(`document.documentElement.scrollWidth <= window.innerWidth`), true);
    await send('Emulation.setDeviceMetricsOverride', {width:1440,height:900,deviceScaleFactor:1,mobile:false}, sessionId);

    await until("document.querySelector('#node-directory a[href*=\"%40local\"]')");
    assert.equal(await evaluate("document.getElementById('workspace-username')?.textContent"), 'admin');
    assert.equal(await evaluate("document.getElementById('project-deployments-nav').hidden"),true);
    assert.equal(await evaluate("document.querySelector('#node-list .environment-row a')?.textContent"), '本机 Docker');
    assert.equal(await evaluate("document.getElementById('directory-start').hidden"),true);
    assert.equal(await evaluate("document.querySelector('.environment-row').querySelectorAll('a').length"),1);
    assert.ok(await evaluate(`(() => {
      const row = document.querySelector('.environment-row');
      const box = row.querySelector('.environment-state').getBoundingClientRect();
      return document.elementFromPoint(box.x + box.width / 2,box.y + box.height / 2) === row.querySelector('a');
    })()`),'clicking node metadata activates the node link');
    assert.ok(await evaluate("document.getElementById('directory-totals').getBoundingClientRect().bottom <= document.querySelector('.directory-layout .work-panel').getBoundingClientRect().top"));
    assert.equal(await evaluate("document.getElementById('console-sidebar').getBoundingClientRect().width"), 216);
    assert.ok(await evaluate("parseFloat(getComputedStyle(document.getElementById('main')).paddingLeft) >= 20"), 'desktop content has breathing room');
    assert.ok(await evaluate("document.querySelector('.site-header').getBoundingClientRect().height <= 58"), 'compact desktop header');
    assert.ok(await evaluate("document.querySelector('.environment-row').getBoundingClientRect().height <= 64"), 'compact node rows');
    assert.equal(await evaluate("document.querySelector('#console-nav [data-view=environments] svg')?.getAttribute('aria-hidden')"), 'true');
    await screenshot('environment-directory');
    await evaluate(`const contactFilter = document.getElementById('node-contact-filter');
      contactFilter.value = 'offline'; contactFilter.dispatchEvent(new Event('change'));`);
    assert.deepEqual(await evaluate("Array.from(document.querySelectorAll('.environment-link'), link => link.textContent)"),['test-worker']);
    await evaluate("contactFilter.value = ''; contactFilter.dispatchEvent(new Event('change'))");
    await send('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:true},sessionId);
    assert.equal(await evaluate("document.documentElement.scrollWidth <= innerWidth"), true);
    assert.deepEqual(await evaluate("Array.from(document.querySelectorAll('#node-list .environment-row:first-child .environment-metric')).map(metric => !!metric.getClientRects().length)"), [true,true]);
    await screenshot('environment-directory-mobile');
    await send('Emulation.setDeviceMetricsOverride',{width:1440,height:900,deviceScaleFactor:1,mobile:false},sessionId);
    assert.equal(await evaluate("document.getElementById('node-workspace').hidden"), true);
    assert.deepEqual(await evaluate("['reload-node-directory','reload-resources'].map(id => { const b = document.getElementById(id); return [b.getAttribute('aria-label'), b.title, b.querySelector('svg')?.getAttribute('aria-hidden')]; })"), [
      ['刷新节点列表','刷新节点列表','true'], ['刷新快照','刷新快照','true']
    ]);
    await evaluate("document.querySelector('#node-directory a[href*=\"%40local\"]').click()");
    await until("document.getElementById('node-meta').textContent.includes('主机名 local-machine')");
    await evaluate("document.querySelector('#resource-view a[href=\"/node/%40local/monitor\"]').click()");
    await until("!document.getElementById('monitoring').hidden && document.querySelector('#monitor-charts canvas')");
    assert.match(await evaluate("document.getElementById('monitor-last-sample').textContent"), /最近采样：.+/);
    await screenshot('node-monitoring');
    await evaluate("document.getElementById('monitor-back').click()");
    await until("location.pathname === '/node/%40local/overview' && document.getElementById('monitoring').hidden");
    await until("document.querySelector('.node-snapshot-summary progress')");
    await screenshot('node-overview');
    assert.equal(await evaluate("document.getElementById('node-heading').textContent"), '本机 Docker');
    assert.ok(await evaluate("document.getElementById('console-sidebar').contains(document.getElementById('resource-node'))"));
    await evaluate("document.getElementById('edit-node-display-name').click()");
    await evaluate(`var nameForm=document.getElementById('node-display-name-form'); nameForm.elements.display_name.value='机房主控'; nameForm.requestSubmit()`);
    await until("document.getElementById('workspace-node').textContent === '机房主控'");
    assert.equal(await evaluate("document.getElementById('node-heading').textContent"), '机房主控');
    assert.equal(await evaluate("document.getElementById('resource-node').selectedOptions[0].textContent"), '机房主控');
    assert.match(await evaluate("document.getElementById('node-meta').textContent"), /local-machine/);
    await evaluate("location.reload()");
    await until("document.getElementById('node-heading')?.textContent === '机房主控' && document.querySelector('#resource-view progress')");
    await evaluate("document.getElementById('edit-node-display-name').click()");
    await evaluate("var nameForm=document.getElementById('node-display-name-form'); nameForm.elements.display_name.value='test-worker'; nameForm.requestSubmit()");
    await until("document.getElementById('resource-node').selectedOptions[0].textContent === 'test-worker · 本机'");
    assert.ok(await evaluate("Array.from(document.getElementById('resource-node').options).some(option => option.value === 'test-worker' && option.textContent === 'test-worker')"));
    await evaluate("document.getElementById('edit-node-display-name').click()");
    await evaluate(`var nameForm=document.getElementById('node-display-name-form'); nameForm.elements.display_name.value=''; nameForm.requestSubmit()`);
    await until("document.getElementById('workspace-node').textContent === '本机 Docker'");
    assert.deepEqual(await evaluate("Array.from(document.querySelectorAll('#resource-view progress')).map(meter => meter.value)"), [24,75]);
    assert.equal(await evaluate("document.querySelector('.inventory-tile').getAttribute('href')"), '/node/%40local/containers');
    assert.ok(await evaluate("document.querySelector('.inventory-tile').getBoundingClientRect().height <= 82"), 'compact resource cards');
    await evaluate(`
      (() => {
        const node = nodeById('@local');
        const saved = nodeSnapshot;
        const target = 'a'.repeat(64);
        const snapshot = (time,state) => ({generation:node.generation,updated_at:time,overview:{
          section_times:{containers:time},containers:[{full_id:target,state}],running_count:state==='running' ? 1 : 0
        }});
        const before = '2026-01-01T10:00:00.000Z';
        const inFlight = '2026-01-01T10:00:01.000Z';
        const observedAt = '2026-01-01T10:00:02.000Z';
        const after = '2026-01-01T10:00:03.000Z';
        try {
          nodeSnapshot = snapshot(before,'running');
          rememberNodeResourceChange(node,'containers',target,{state:'exited'},observedAt);
          if (nodeSnapshot.overview.containers[0].state !== 'exited' || nodeSnapshot.overview.running_count !== 0) throw new Error('confirmed state did not update list and overview immediately');
          rememberNodeResourceChange(node,'containers',target,{state:'running'},inFlight);
          if (nodeSnapshot.overview.containers[0].state !== 'exited' || nodeSnapshot.overview.running_count !== 0) throw new Error('older operation response rolled back the confirmed state');
          const earlier = snapshot(inFlight,'running');
          applyNodeResourceChanges(earlier,node.id,true);
          if (earlier.overview.containers[0].state !== 'exited' || earlier.overview.running_count !== 0) throw new Error('snapshot collected before the operation rolled back its result');
          const imageID = 'sha256:' + 'e'.repeat(64);
          const imageSnapshot = {generation:node.generation,overview:{section_times:{images:before},images:[{full_id:imageID}],image_count:3}};
          nodeSnapshot = structuredClone(imageSnapshot);
          rememberNodeResourceChange(node,'images',imageID,null,observedAt);
          if (nodeSnapshot.overview.image_count !== 2) throw new Error('confirmed image removal did not update overview');
          applyNodeResourceChanges(imageSnapshot,node.id);
          applyNodeResourceChanges(imageSnapshot,node.id);
          if (imageSnapshot.overview.image_count !== 2) throw new Error('stale snapshot rolled back or double-counted the image removal');
          nodeSnapshot = snapshot(before,'exited');
          const later = snapshot(after,'running');
          applyNodeResourceChanges(later,node.id,true);
          if (later.overview.containers[0].state !== 'running') throw new Error('post-operation inventory did not take priority');
          rememberNodeResourceChange(node,'containers',target,{state:'exited'},observedAt);
          nodeResourceChanges.get('containers/' + target).until = Date.now() - 1;
          rememberNodeResourceChange(node,'containers','b'.repeat(64),{state:'running'},observedAt);
          if (!applyNodeResourceChanges(snapshot(before,'running'),node.id,true)) throw new Error('expiry did not request a redraw for the same snapshot');
        } finally {
          nodeSnapshot = saved;
          nodeResourceChanges.clear();
        }
      })();
    `);
    await evaluate("document.querySelector('#node-resource-nav a[data-category=containers]').click()");
    await until("document.querySelector('#resource-view table')");
    await evaluate(`
      window.deploymentHistoryFetch = window.fetch;
      window.deploymentHistoryCalls = [];
      window.fetch = (path,options) => {
        if (!path.startsWith('/api/v1/nodes/%40local/container-deployment-runs/edge-proxy?')) return deploymentHistoryFetch(path,options);
        const query = new URL(path,location.origin).searchParams;
        deploymentHistoryCalls.push(Object.fromEntries(query));
        return Promise.resolve(Response.json(query.has('after') ?
          {items:[{id:'old-run',image:'nginx:old',source:'manual',state:'succeeded',created_at:'2026-01-01T10:00:00Z'}]} :
          {items:[{id:'latest-run',image:'nginx:new',source:'webhook',state:'succeeded',created_at:'2026-01-01T11:00:00Z'}],next:'cursor-2'}));
      };
      [...document.querySelectorAll('#resource-view .container-name-link, #resource-view tbody a')].find(link=>link.textContent.trim()==='edge-proxy').click();
    `);
    await until("document.querySelector('#resource-view a[href$=\"/deployments\"]')");
    await evaluate("document.querySelector('#resource-view a[href$=\"/deployments\"]').click()");
    await until("document.querySelector('.deployment-runs')?.textContent.includes('latest-run')");
    await screenshot('container-deployment-history');
    assert.match(await evaluate("document.querySelector('.deployment-runs').textContent"),/Webhook/);
    assert.equal(await evaluate("[...document.querySelectorAll('.deployment-runs button')].find(b=>b.textContent==='上一页').disabled"),true);
    await evaluate("[...document.querySelectorAll('.deployment-runs button')].find(b=>b.textContent==='下一页').click()");
    await until("document.querySelector('.deployment-runs').textContent.includes('old-run')");
    assert.equal(await evaluate("deploymentHistoryCalls.at(-1).after"),'cursor-2');
    assert.equal(await evaluate("deploymentHistoryCalls[0].limit"),'20');
    assert.equal(await evaluate("[...document.querySelectorAll('.deployment-runs button')].find(b=>b.textContent==='下一页').disabled"),true);
    await evaluate("[...document.querySelectorAll('.deployment-runs button')].find(b=>b.textContent==='上一页').click()");
    await until("document.querySelector('.deployment-runs').textContent.includes('latest-run')");
    await evaluate("document.getElementById('maintenance-back').click()");
    await until("!document.getElementById('maintenance-page').getClientRects().length");
    await evaluate("window.fetch = deploymentHistoryFetch; document.querySelector('#node-resource-nav a[data-category=containers]').click()");
    await until("document.querySelector('#resource-view table')");
    const rowHeight = await evaluate("document.querySelector('#resource-view tbody tr').getBoundingClientRect().height");
    const rowCells = rowHeight > 44 && await evaluate(`[...document.querySelector('#resource-view tbody tr').cells].map(cell => {
      const box = cell.getBoundingClientRect();
      return [cell.textContent,box.width,box.height,[...cell.children].map(node=>[node.tagName,node.getBoundingClientRect().width,node.getBoundingClientRect().height])];
    })`);
    assert.ok(rowHeight <= 44, 'compact container rows: ' + rowHeight + ' px ' + JSON.stringify(rowCells));
    assert.ok(await evaluate("document.querySelector('.container-list button[aria-label=\"删除容器\"]')?.disabled"),'running and Compose containers explain why deletion is disabled');
    await evaluate(`
      window.preBatchSnapshot = structuredClone(nodeSnapshot);
      window.observedBatchAt = new Date(Date.now()+1000).toISOString();
      const selected = [...document.querySelectorAll('#resource-view [data-container-select]')];
      window.batchFetch = window.fetch;
      window.batchRequests = [];
      window.fetch = (path,options) => {
        const endpoint = '/api/v1/nodes/%40local/operations';
        if (path === endpoint && options?.method === 'POST') {
          const command = JSON.parse(options.body);
          batchRequests.push(command);
          const id = (command.target[0] === 'a' ? 'e' : 'f').repeat(32);
          return Promise.resolve(Response.json(command.target[0] === 'a' ? {
            id,state:'succeeded',action:command.action,result:{container:{full_id:command.target,state:'exited',state_observed_at:observedBatchAt}}
          } : {id,state:'queued',action:command.action}));
        }
        if (path === endpoint + '/' + 'f'.repeat(32)) return new Promise(resolve => {
          window.finishBatch = () => resolve(Response.json({id:'f'.repeat(32),state:'succeeded',action:'container.stop',
            result:{container:{full_id:'c'.repeat(64),state:'exited',state_observed_at:observedBatchAt}}}));
        });
        return batchFetch(path,options);
      };
      for (const item of selected) item.click();
      [...document.querySelectorAll('.container-bulk-bar button')].find(button => button.textContent === '批量停止所选').click();
    `);
    assert.equal(await evaluate("document.getElementById('management-title').textContent"),'批量停止 2 个容器');
    assert.deepEqual(await evaluate("[...document.querySelectorAll('.batch-operation-name')].map(node=>node.textContent).sort()"),['edge-proxy','test-web']);
    await screenshot('container-batch');
    await evaluate("[...document.querySelectorAll('#management-content button')].find(button => button.textContent === '确认批量停止').click()");
    await until("typeof window.finishBatch === 'function'");
    assert.equal(await evaluate("[...document.querySelectorAll('.container-list tbody tr')].filter(row=>row.textContent.includes('已停止')).length"),1,
      'the first confirmed row updates while the second job is still running');
    assert.equal(await evaluate("[...document.querySelectorAll('.batch-operation-list a')].filter(link=>link.getAttribute('href').includes('/operations/')).length"),2,
      'each submitted container links to its own operation record');
    assert.deepEqual(await evaluate("batchRequests.map(command=>command.target).sort()"),['a'.repeat(64),'c'.repeat(64)]);
    assert.deepEqual(await evaluate(`(() => {
      const close = document.querySelector('.dialog-close');
      return [close.textContent.trim(), close.getAttribute('aria-label'), close.querySelector('svg')?.getAttribute('aria-hidden')];
    })()`),['','关闭','true']);
    await evaluate("document.querySelector('.dialog-close').click(); finishBatch(); true");
    await until("[...document.querySelectorAll('.container-list tbody tr')].filter(row=>row.textContent.includes('已停止')).length === 2");
    assert.equal(await evaluate('nodeSnapshot.overview.running_count'),0,
      'the closed dialog applies the last confirmed result to the overview before inventory refresh');
    await evaluate("window.fetch = batchFetch; nodeResourceChanges.clear(); nodeSnapshot = preBatchSnapshot; renderWorkspaceBody()");
    await until("!document.getElementById('management-dialog').open");
    await evaluate(`
      window.removalCalls = [];
      window.removalFetch = window.fetch;
      window.fetch = (path,options) => {
        if (path === '/api/v1/nodes/%40local/operations' && options?.method === 'POST') {
          const body = JSON.parse(options.body);
          if (['container.remove','compose.remove'].includes(body.action)) {
            removalCalls.push(body);
            return Promise.resolve(Response.json({id:'remove-test',action:body.action,state:'succeeded'}));
          }
        }
        return removalFetch(path,options);
      };
      var stopped = {full_id:'d'.repeat(64),name:'stopped-test',state:'exited',mounts:[{type:'volume',name:'test-data'},{type:'bind',source:'/srv/host-data'}]};
      containerRemoveButton(nodeById('@local'),stopped).click();
    `);
    assert.equal(await evaluate("document.querySelector('#management-content input[name=remove_volumes]').checked"),false);
    assert.ok(await evaluate(`(() => {
      const option = document.querySelector('.removal-volume-option');
      const box = option.querySelector('input').getBoundingClientRect();
      const description = option.querySelector('span').getBoundingClientRect();
      return box.right < description.left && box.top < description.bottom && box.bottom > description.top;
    })()`), 'volume checkbox and its label share a row');
    assert.match(await evaluate("document.getElementById('management-content').textContent"),/test-data/);
    assert.match(await evaluate("document.getElementById('management-content').textContent"),/宿主机绑定目录.*\/srv\/host-data.*保留/);
    assert.equal(await evaluate('removalCalls.length'),0,'container removal requires confirmation');
    await evaluate("[...document.querySelectorAll('#management-content button')].find(button=>button.textContent==='确认删除容器').click()");
    await until("document.getElementById('management-status').textContent.includes('操作成功')");
    assert.deepEqual(await evaluate('removalCalls[0]'),{action:'container.remove',target:'d'.repeat(64),remove_volumes:false,generation:1});
    await evaluate(`
      document.getElementById('management-dialog').close();
      composeRemoveButton(nodeById('@local'),'cleanup-stack',[{compose_project:'cleanup-stack',mounts:[{type:'volume',name:'test-data'}]}]).click();
    `);
    assert.equal(await evaluate("document.querySelector('#management-content input[name=remove_volumes]').checked"),false);
    assert.equal(await evaluate('removalCalls.length'),1,'Compose removal requires confirmation');
    assert.match(await evaluate("document.getElementById('management-content').textContent"), /网络及 Compose 源文件保留/);
    await evaluate(`
      document.querySelector('#management-content input[name=remove_volumes]').checked = true;
      [...document.querySelectorAll('#management-content button')].find(button=>button.textContent==='确认删除项目').click();
    `);
    await until("document.getElementById('management-status').textContent.includes('操作成功')");
    assert.deepEqual(await evaluate('removalCalls[1]'),{action:'compose.remove',target:'cleanup-stack',remove_volumes:true,generation:1});
    assert.equal(await evaluate("document.querySelector('#management-content a[href=\"/node/%40local/networks\"]')?.textContent"), '前往网络列表');
    await evaluate("document.getElementById('management-dialog').close();window.fetch=removalFetch");
    await screenshot('containers');
    assert.equal(await evaluate("Array.from(document.querySelectorAll('.workspace-location, #environment-path')).filter(path => path.getClientRects().length).length"), 1, 'one visible resource breadcrumb');
    assert.equal(await evaluate("document.getElementById('workspace-section').pathname"), '/environments');
    await send('Emulation.setDeviceMetricsOverride', {width:390,height:844,deviceScaleFactor:1,mobile:true}, sessionId);
    assert.ok(await evaluate("document.querySelector('.workspace-location').getClientRects().length"), 'mobile breadcrumb remains visible');
    assert.equal(await evaluate("document.documentElement.scrollWidth <= innerWidth"), true);
    assert.ok(await evaluate("document.querySelector('.container-list').scrollWidth <= document.querySelector('.container-list').clientWidth + 1"), 'compact container list fits the viewport');
    assert.equal(await evaluate("getComputedStyle(document.querySelector('.container-list th:nth-child(4)')).display"),'none');
    assert.ok(await evaluate("document.querySelector('.container-list a[aria-label=\"查看日志\"]').getBoundingClientRect().width >= 44"), 'mobile log shortcut stays visible and tappable');
    assert.ok(await evaluate("document.querySelector('.container-list a[aria-label=\"进入终端\"]').getBoundingClientRect().width >= 44"), 'mobile terminal shortcut stays visible and tappable');
    await screenshot('containers-mobile');
    await evaluate("document.getElementById('sidebar-toggle').click()");
    assert.ok(await evaluate("document.getElementById('resource-node').getBoundingClientRect().height >= 44"), 'mobile node selector touch target');
    assert.equal(await evaluate("document.documentElement.scrollWidth <= innerWidth"), true);
    await evaluate("document.getElementById('sidebar-toggle').click()");
    await evaluate("document.getElementById('workspace-node').click()");
    await until("location.pathname === '/node/%40local/overview' && document.querySelector('.inventory-tile')");
    await evaluate("document.querySelector('.inventory-tile').click()");
    await until("document.querySelector('#resource-view table')");
    await send('Emulation.setDeviceMetricsOverride', {width:1440,height:900,deviceScaleFactor:1,mobile:false}, sessionId);
    assert.equal(await evaluate("document.querySelector('#node-resources').getBoundingClientRect().top - document.querySelector('.node-context').getBoundingClientRect().bottom"),12);
    await evaluate("var searchContainers=document.getElementById('container-search'); searchContainers.value='no-match'; searchContainers.dispatchEvent(new Event('input'))");
    assert.equal(await evaluate("document.querySelectorAll('#resource-view tbody tr').length"), 0);
    await evaluate("document.querySelector('.container-empty button').click()");
    assert.equal(await evaluate("searchContainers.value"),'');
    await evaluate("searchContainers.value='test-web'; searchContainers.dispatchEvent(new Event('input'))");
    assert.equal(await evaluate("document.querySelectorAll('#resource-view tbody tr').length"), 1);
    assert.match(await evaluate("document.getElementById('container-list-count').textContent"), /^1 \/ [1-9]\d*$/);
    await evaluate("document.querySelector('#resource-view tbody tr a[href$=\"/monitor\"]').click()");
    await until("!document.getElementById('monitoring').hidden && document.getElementById('monitor-title').textContent === '容器监控'");
    assert.equal(await evaluate("document.querySelector('#node-resource-nav [aria-current=page]').dataset.category"), 'containers');
    assert.equal(await evaluate("document.getElementById('monitor-back').pathname"),'/node/%40local/containers');
    await evaluate("document.getElementById('monitor-back').click()");
    await until("location.pathname === '/node/%40local/containers' && document.querySelector('.container-list')");
    await evaluate("document.querySelector('#resource-view .container-name').click()");
    await until("document.getElementById('container-heading')?.textContent === 'test-web'");
    assert.equal(await evaluate("document.querySelector('.container-action-panel h3')?.textContent"), '容器操作');
    assert.equal(await evaluate("document.getElementById('node-context')?.hidden"),true);
    assert.equal(await evaluate("document.getElementById('reload-container-detail')?.getAttribute('aria-label')"),'刷新容器详情');
    assert.deepEqual(await evaluate("Array.from(document.querySelectorAll('.container-action-groups [role=group]'), group => group.getAttribute('aria-label'))"), ['运行','配置与部署']);
    assert.equal(await evaluate("getComputedStyle(document.querySelector('.container-action-groups')).display"),'flex');
    assert.match(await evaluate("document.querySelector('.container-action-hint').textContent"),/属于 Compose 项目/);
    assert.equal(await evaluate("getComputedStyle(document.querySelector('.container-samples .snapshot-facts')).display"),'grid');
    await screenshot('container-detail');
    assert.equal(await evaluate("document.querySelector('.container-extra-actions button.danger').getClientRects().length"),0);
    await evaluate("document.querySelector('.container-extra-actions summary').click(); renderWorkspaceBody()");
    assert.equal(await evaluate("document.querySelector('.container-extra-actions').open"),true);
    assert.ok(await evaluate("document.querySelector('.container-extra-actions button.danger').getClientRects().length"));
    await screenshot('container-extra-actions');
    await evaluate("document.querySelector('.container-extra-actions summary').click()");
    const containerHash = await evaluate('location.pathname');
    assert.equal(await evaluate("document.querySelector('.container-work-heading a[href$=\"/term\"]')?.pathname"), containerHash + '/term');
    await evaluate(`
      window.nativeTerminalSocket = window.WebSocket;
      window.terminalCloseCount = 0;
      window.WebSocket = class {
        readyState = 0;
        constructor(url) { window.terminalSocketURL = url; window.terminalSocket = this; }
        close() { window.terminalCloseCount++; }
      };
      document.querySelector('.container-work-heading a[href$="/term"]').click();
    `);
    await until("!document.getElementById('container-terminal-page').hidden && window.terminalSocketURL?.includes('/terminal?')");
    assert.equal(await evaluate("document.getElementById('container-terminal-exit')?.textContent"),'退出终端');
    await evaluate(`
      terminalSocket.onmessage({data:'{"ready":true}'});
      terminalSocket.onmessage({data:new TextEncoder().encode('最后一行').buffer});
      terminalSocket.onmessage({data:'{"eof":true}'});
      terminalSocket.onclose();
    `);
    assert.match(await evaluate("document.getElementById('container-terminal-status').textContent"),/退出码 0/);
    assert.equal(await evaluate("document.getElementById('container-terminal-output').hidden"),false);
    await screenshot('container-terminal');
    await evaluate("document.getElementById('container-terminal-exit').click(); window.WebSocket = window.nativeTerminalSocket");
    await until("document.getElementById('container-terminal-page').hidden && document.getElementById('container-heading')?.textContent === 'test-web'");
    assert.equal(await evaluate('location.pathname'),containerHash);
    assert.equal(await evaluate('terminalCloseCount'),1);
    await evaluate("document.querySelector('.container-work-heading .resource-back').click()");
    await until("location.pathname === '/node/%40local/containers' && document.querySelector('.container-list')");
    assert.equal(await evaluate("document.getElementById('container-search').value"),'test-web');
    assert.equal(await evaluate("document.activeElement.dataset.resourceFocus"),'a'.repeat(64) + ':details');
    await evaluate("document.activeElement.click()");
    await until("document.getElementById('container-heading')?.textContent === 'test-web'");
    assert.equal(await evaluate("document.querySelector('.container-work-heading a[href$=\"/monitor\"]')?.pathname"), containerHash + '/monitor');
    assert.equal(await evaluate("document.querySelector('.container-action-panel a[href$=\"/compose/test-stack/edit\"]')?.textContent"), '查看 Compose 源文件');
    await evaluate(`
      window.detailConfigurationReads = 0;
      window.holdDetailConfiguration = true;
      window.containerDetailFetch = window.fetch;
      window.fetch = (path, options) => {
        if (path === '/api/v1/nodes/%40local/operations' && options?.method === 'POST' &&
            JSON.parse(options.body).action === 'container.edit-config') {
          detailConfigurationReads++;
          if (holdDetailConfiguration) {
            window.heldDetailSignal = options.signal;
            return new Promise((_resolve,reject) => options.signal.addEventListener('abort',()=>reject(options.signal.reason),{once:true}));
          }
          return Promise.resolve(Response.json({id:'detail-config',state:'succeeded',result:{edit_config:{
            environment:{APP_TEXT:'line one\\nline two',EMPTY:'',UNSET:null,VERY_LONG_ENVIRONMENT_VARIABLE_NAME_WITH_NO_BREAKS:'x'.repeat(200)},
            labels:{'app.owner':'platform','org.opencontainers.image.very-long-metadata-key-for-layout':'y'.repeat(200)}
          }}}));
        }
        return containerDetailFetch(path,options);
      };
      clearContainerConfiguration();
      renderWorkspaceBody();
    `);
    assert.equal(await evaluate('detailConfigurationReads'),0);
    await evaluate(`
      const tab = document.getElementById('container-tab-summary');
      tab.focus();
      tab.dispatchEvent(new KeyboardEvent('keydown',{key:'End',bubbles:true}));
    `);
    assert.equal(await evaluate("document.activeElement.id"),'container-tab-config');
    assert.equal(await evaluate("document.getElementById('container-tab-summary').getAttribute('aria-selected')"),'true');
    assert.equal(await evaluate('detailConfigurationReads'),0);
    await evaluate("document.getElementById('container-tab-config').click()");
    assert.equal(await evaluate('location.pathname'),containerHash + '/config');
    await until("window.heldDetailSignal && document.querySelector('#container-panel-config [role=status]')");
    await evaluate("document.getElementById('container-tab-summary').click()");
    await until("heldDetailSignal.aborted && !document.querySelector('.container-configuration')");
    await evaluate("window.holdDetailConfiguration = false; window.detailConfigurationReads = 0; document.getElementById('container-tab-config').click()");
    await until("document.querySelector('.container-configuration dd')?.textContent.includes('line one')");
    assert.equal(await evaluate("document.querySelector('.container-configuration dd').textContent"), 'line one\nline two');
    assert.match(await evaluate("document.querySelector('.container-configuration').textContent"), /app\.ownerplatform/);
    const configurationRowsFit = stacked => evaluate(`([...document.querySelectorAll('.container-configuration .snapshot-facts > div')].every(row => {
      const [key,value] = row.children;
      const text = document.createRange();
      text.selectNodeContents(key);
      const keyText = text.getBoundingClientRect(), valueBox = value.getBoundingClientRect();
      return key.scrollWidth <= key.clientWidth + 1 && value.scrollWidth <= value.clientWidth + 1 &&
        (${stacked} ? keyText.bottom <= valueBox.top : keyText.right <= valueBox.left);
    }))`);
    assert.equal(await configurationRowsFit(false), true, 'long keys and values stay in their desktop columns');
    await send('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:true},sessionId);
    assert.equal(await configurationRowsFit(true), true, 'narrow layouts stack environment and label keys above their values');
    assert.equal(await evaluate('document.documentElement.scrollWidth <= innerWidth'), true);
    await send('Emulation.setDeviceMetricsOverride',{width:1440,height:900,deviceScaleFactor:1,mobile:false},sessionId);
    await evaluate("renderWorkspaceBody()");
    assert.equal(await evaluate("document.querySelector('.container-configuration').open && detailConfigurationReads === 1"), true);
    await screenshot('container-config');
    await evaluate("document.querySelector('.container-configuration summary').click()");
    await until("!document.querySelector('.container-configuration').textContent.includes('line one')");
    await evaluate("window.fetch = containerDetailFetch");
    await evaluate(`
      window.fetch = (path, options) => {
        if (path === '/api/v1/nodes/%40local/operations' && options?.method === 'POST' &&
            JSON.parse(options.body).action === 'container.restart') {
          return Promise.resolve(Response.json({id:'c'.repeat(32),action:'container.restart',state:'failed',error:'Docker container no longer exists on this node'}));
        }
        return containerDetailFetch(path,options);
      };
      [...document.querySelectorAll('.container-action-panel button')].find(button => button.textContent === '重启').click();
      [...document.querySelectorAll('#management-content button')].find(button => button.textContent === '确认重启').click();
    `);
    await until("document.getElementById('management-status').textContent.includes('容器 ID 已不在目标节点')");
    assert.ok(await evaluate("[...document.querySelectorAll('#management-content a')].some(link=>link.pathname === '/node/%40local/containers')"));
    assert.ok(await evaluate("[...document.querySelectorAll('#management-content a')].some(link=>link.pathname === '/node/%40local/operations/' + 'c'.repeat(32))"));
    await evaluate("document.getElementById('management-dialog').close(); window.fetch = containerDetailFetch");
    await evaluate(`
      window.fetch = (path, options) => {
        if (path === '/api/v1/nodes/%40local/operations' && options?.method === 'POST' &&
            JSON.parse(options.body).action === 'container.restart') return Promise.reject(new TypeError('connection lost'));
        return containerDetailFetch(path,options);
      };
      [...document.querySelectorAll('.container-action-panel button')].find(button => button.textContent === '重启').click();
      [...document.querySelectorAll('#management-content button')].find(button => button.textContent === '确认重启').click();
    `);
    await until("document.getElementById('management-status').textContent.includes('未收到提交确认')");
    assert.equal(await evaluate("document.querySelector('#management-content a').pathname"), '/node/%40local/operations');
    await evaluate("document.getElementById('management-dialog').close(); window.fetch = containerDetailFetch");
    await evaluate(`
      window.fetch = (path,options) => {
        if (path === '/api/v1/nodes/%40local/operations' && options?.method === 'POST') {
          const {action,target} = JSON.parse(options.body);
          if (['container.start','container.stop'].includes(action)) {
            const state = action === 'container.start' ? 'running' : 'exited';
            return Promise.resolve(Response.json({id:'lifecycle',action,state:'succeeded',result:{container:{full_id:target,state,
              health:state === 'exited' ? 'unhealthy' : '',state_observed_at:new Date().toISOString()}}}));
          }
        }
        return containerDetailFetch(path,options);
      };
      [...document.querySelectorAll('.container-action-panel button')].find(button => button.textContent === '停止').click();
      [...document.querySelectorAll('#management-content button')].find(button => button.textContent === '确认停止').click();
    `);
    await until("document.querySelector('.container-work-heading .status-pill')?.textContent === '已停止'");
    assert.ok((await evaluate('location.pathname')).startsWith(containerHash));
    assert.equal(await evaluate("document.getElementById('management-dialog').open"),false);
    assert.equal(await evaluate("document.querySelector('#resource-action-feedback:not([hidden])')?.textContent"),'容器停止操作已完成。');
    await evaluate("document.querySelector('.container-work-heading .resource-back').click()");
    await until("location.pathname === '/node/%40local/containers' && [...document.querySelectorAll('#resource-view tbody tr')].some(row => row.textContent.includes('test-web') && row.textContent.includes('已停止'))");
    await evaluate("document.querySelector('.container-list button[aria-label=\"启动 test-web\"]').click()");
    await until("document.getElementById('container-inline-status').textContent.includes('test-web · 已启动')");
    assert.equal(await evaluate("document.getElementById('management-dialog').open"),false);
    await until("location.pathname === '/node/%40local/containers' && [...document.querySelectorAll('#resource-view tbody tr')].some(row => row.textContent.includes('test-web') && row.textContent.includes('运行中'))");
    await evaluate("window.fetch = containerDetailFetch; history.pushState(null, '', " + JSON.stringify(containerHash) + "); dispatchEvent(new PopStateEvent('popstate'))");
    await until("document.getElementById('container-heading')?.textContent === 'test-web'");
    await evaluate("document.getElementById('container-tab-specs').click(); document.querySelector('.container-work-heading a[href$=\"/monitor\"]').click()");
    await until("!document.getElementById('monitoring').hidden");
    assert.equal(await evaluate("document.getElementById('monitor-back').pathname"),containerHash + '/specs');
    await screenshot('container-monitoring');
    await evaluate("document.getElementById('monitor-back').click()");
    await until("document.getElementById('container-tab-specs')?.getAttribute('aria-selected') === 'true'");
    await evaluate("document.getElementById('container-tab-specs').click(); document.querySelector('#resource-view a[href*=\"/volumes/test-data\"]').click()");
    await until("document.querySelector('.maintenance-heading h2')?.textContent === 'test-data'");
    await screenshot('volume-detail');
    assert.match(await evaluate("document.querySelector('.maintenance-related').textContent"), /test-web/);
    assert.equal(await evaluate("document.querySelector('#resource-view button.danger').disabled"),true);
    assert.match(await evaluate("document.querySelector('#resource-view .removal-blocked').textContent"), /仍有 1 个容器引用/);
    await evaluate(`
      window.volumeCreateFetch = window.fetch;
      window.volumePending = new Map();
      window.fetch = (path,options) => {
        const endpoint = '/api/v1/nodes/%40local/operations';
        if (path === endpoint && options?.method === 'POST') {
          const command = JSON.parse(options.body);
          if (command.action === 'volume.create') {
            volumePending.set(command.name,{ready:command.name === 'new-volume',size:command.tmpfs_size_mib});
            return Promise.resolve(Response.json({id:command.name,state:'queued',action:command.action}));
          }
        }
        if (path.startsWith(endpoint + '/') && volumePending.has(path.split('/').at(-1))) {
          const name = path.split('/').at(-1);
          return Promise.resolve(Response.json(volumePending.get(name).ready ? {
            id:name,action:'volume.create',state:'succeeded',result:{
              resource_observed_at:new Date().toISOString(),
              volume:{name,driver:'local',scope:'local',mountpoint:'/docker/volumes/' + name,ref_count:0,
                ...(volumePending.get(name).size && {tmpfs:true,capacity_bytes:volumePending.get(name).size*2**20})}
            }
          } : {id:name,action:'volume.create',state:'running'}));
        }
        return volumeCreateFetch(path,options);
      };
      document.querySelector('#node-resource-nav a[data-category=volumes]').click();
    `);
    await until("!document.getElementById('create-volume').hidden");
    await screenshot('volumes');
    await evaluate(`document.getElementById('create-volume').click();
      var volumeForm = document.querySelector('#management-content form');
      volumeForm.elements.name.value='new-volume';
      volumeForm.elements.type.value='tmpfs';
      volumeForm.elements.type.dispatchEvent(new Event('change'));
      volumeForm.elements.size.value='128';
      volumeForm.requestSubmit();`);
    await until("!document.getElementById('management-dialog').open && document.querySelector('#resource-view').textContent.includes('new-volume')");
    assert.equal(await evaluate("nodeSnapshot.overview.volume_count"),2);
    assert.equal(await evaluate("volumePending.get('new-volume').size"),128);
    assert.match(await evaluate("document.querySelector('#resource-view').textContent"),/128(?:\.0)? MiB/);
    await evaluate(`document.getElementById('create-volume').click();
      volumeForm = document.querySelector('#management-content form');
      volumeForm.elements.name.value='background-volume';
      volumeForm.requestSubmit();`);
    await until("volumePending.has('background-volume') && document.getElementById('management-status').textContent.includes('等待节点')");
    await evaluate("document.querySelector('.dialog-close').click(); volumePending.get('background-volume').ready=true");
    await until("document.querySelector('#resource-view').textContent.includes('background-volume')");
    assert.equal(await evaluate("nodeSnapshot.overview.volume_count"),3);
    await evaluate("window.fetch = volumeCreateFetch; nodeResourceChanges.delete('volumes/new-volume'); nodeResourceChanges.delete('volumes/background-volume'); void loadNodeResources(true)");
    await evaluate("history.pushState(null, '', " + JSON.stringify(containerHash) + "); dispatchEvent(new PopStateEvent('popstate'))");
    await until("document.getElementById('container-heading')?.textContent === 'test-web'");
    await evaluate("document.getElementById('container-tab-specs').click(); document.querySelector('#resource-view a[href*=\"/networks/\"]').click()");
    await until("document.querySelector('.maintenance-heading h2')?.textContent === 'test-net'");
    await screenshot('network-detail');
    assert.match(await evaluate("document.querySelector('.maintenance-related').textContent"), /test-web/);
    assert.equal(await evaluate("document.querySelector('#resource-view button.danger').disabled"),true);
    assert.match(await evaluate("document.querySelector('#resource-view .removal-blocked').textContent"), /网络仍有关联容器/);
    await evaluate("document.querySelector('#node-resource-nav a[data-category=images]').click()");
    await until("!document.getElementById('pull-image').hidden");
    assert.ok(await evaluate("document.getElementById('import-image')?.getClientRects().length"),'image list exposes archive import');
    await screenshot('images');
    await evaluate("document.getElementById('pull-image').click()");
    assert.equal(await evaluate("document.querySelector('#management-content input[name=image]').required"), true);
    await evaluate("document.getElementById('management-dialog').close()");
    await evaluate(`
      window.imageFetch = window.fetch;
      window.imageCalls = [];
      window.imageRefreshReads = 0;
      const imageRefreshAt = new Date().toISOString();
      const operations = '/api/v1/nodes/%40local/operations';
      window.fetch = (path,options) => {
        if (path === '/api/v1/nodes/%40local/resources') return imageFetch(path,options).then(async response => {
          const snapshot = await response.json();
          const before = !imageCalls.some(item=>item.action==='image.delete') || ++imageRefreshReads === 1;
          if (before) {
            snapshot.overview.images.push({full_id:'sha256:'+'b'.repeat(64),tags:['unused:v1'],container_count:0});
            snapshot.overview.image_count++;
          }
          snapshot.updated_at = before ? new Date(Date.parse(imageRefreshAt)-1).toISOString() : imageRefreshAt;
          // Agent clock is ahead of the browser and the delete reply has no observation timestamp.
          snapshot.overview.section_times = {...snapshot.overview.section_times,
            images:new Date(Date.parse(snapshot.updated_at)+60000).toISOString()};
          return Response.json(snapshot);
        });
        if (path === '/api/v1/registries') return Promise.resolve(Response.json({items:[{
          id:'test-registry',name:'测试仓库',host:'registry.example.test',enabled:true,repository_prefix:'team',
          bindings:[]
        }]}));
        if (path === operations && options?.method === 'POST') {
          const body = JSON.parse(options.body);
          imageCalls.push(body);
          if (body.action === 'image.tag-preview') return Promise.resolve(Response.json({
            id:'tag-preview',state:'succeeded',result:{image:body.image,image_id:body.image_id,
              current_image_id:body.image.endsWith(':occupied') ? 'sha256:'+'2'.repeat(64) : ''}
          }));
          return Promise.resolve(Response.json({id:body.action,state:'queued',action:body.action}));
        }
        if (path.startsWith(operations + '/image.')) {
          const action = path.split('/').at(-1);
          return Promise.resolve(Response.json({id:action,state:'succeeded',action,result:{images:['registry.example.test/team/app:stable']}}));
        }
        return imageFetch(path,options);
      };
    `);
    await evaluate('loadNodeResources(true)');
    await until("document.getElementById('resource-view').textContent.includes('unused:v1')");
    await evaluate(`
      window.deleteTestList = buildResourceList('@local','images',{available:true,images:[
        {full_id:'sha256:'+'a'.repeat(64),id:'used',tags:['used:v1'],container_count:2},
        {full_id:'sha256:'+'b'.repeat(64),id:'unused',tags:['unused:v1'],container_count:0},
        {full_id:'sha256:'+'c'.repeat(64),id:'unknown',tags:['unknown:v1']}
      ]});
      deleteTestList.id = 'image-delete-test';
      document.body.append(deleteTestList);
    `);
    assert.match(await evaluate("deleteTestList.textContent"), /使用中（2 个容器）/);
    assert.match(await evaluate("deleteTestList.textContent"), /未使用/);
    assert.match(await evaluate("deleteTestList.textContent"), /使用状态未知/);
    assert.deepEqual(await evaluate("[...deleteTestList.querySelectorAll('button.danger')].map(button=>button.disabled)"),[true,false,true]);
    await evaluate("deleteTestList.querySelectorAll('button.danger')[1].click()");
    assert.equal(await evaluate("imageCalls.some(item=>item.action==='image.delete')"),false,'opening confirmation must not delete');
    assert.match(await evaluate("document.getElementById('management-title').textContent"),/删除.*unused:v1/);
    await evaluate("[...document.querySelectorAll('#management-content button')].find(button=>button.textContent==='确认删除').click()");
    await until("document.getElementById('management-status').textContent.includes('操作成功')");
    assert.deepEqual(await evaluate("imageCalls.find(item=>item.action==='image.delete')"),{
      action:'image.delete',image_id:'sha256:'+'b'.repeat(64),generation:1
    });
    assert.equal(await evaluate("document.getElementById('resource-view').textContent.includes('unused:v1')"),false);
    assert.equal(await evaluate('nodeSnapshot.overview.image_count'),1);
    await until("imageRefreshReads >= 2 && !document.getElementById('resource-view').textContent.includes('unused:v1')");
    await evaluate("document.getElementById('management-dialog').close();deleteTestList.remove();document.getElementById('pull-image').click()");
    await evaluate(`
      var sharedPull = document.querySelector('#management-content form');
      sharedPull.elements.image.value = 'registry.example.test/team-extra/app:v1';
      sharedPull.elements.image.dispatchEvent(new Event('input'));
    `);
    await until("!sharedPull.querySelector('button[type=submit]').disabled");
    assert.equal(await evaluate('sharedPull.elements.registry_id.options.length'),1,'a sibling repository prefix does not match');
    await evaluate(`
      sharedPull.elements.image.value = 'registry.example.test/team/app:v1';
      sharedPull.elements.image.dispatchEvent(new Event('input'));
    `);
    assert.equal(await evaluate('sharedPull.elements.registry_id.value'),'test-registry');
    await evaluate('sharedPull.requestSubmit()');
    await until("document.getElementById('management-status').textContent.includes('操作成功')");
    assert.deepEqual(await evaluate("imageCalls.find(item=>item.action === 'image.pull')"),{
      action:'image.pull',image:'registry.example.test/team/app:v1',registry_id:'test-registry',generation:1
    });
    await evaluate("document.getElementById('management-dialog').close()");
    await evaluate("document.querySelector('#resource-view tbody a').click()");
    await until("document.querySelector('.maintenance-heading h2')?.textContent === 'test-app:v1'");
    assert.equal(await evaluate("document.getElementById('pull-image').getClientRects().length"),0,'image detail has no pull action');
    await until("document.querySelector('.image-tag-editor select[name=host] option[value=\"registry.example.test\"]')");
    await evaluate(`
      var imageTagForm = document.querySelector('.image-tag-editor form');
      imageTagForm.elements.host.value = 'registry.example.test';
      imageTagForm.elements.host.dispatchEvent(new Event('change'));
      imageTagForm.elements.image.value = 'team/app:occupied';
      imageTagForm.elements.image.dispatchEvent(new Event('input'));
      imageTagForm.requestSubmit();
    `);
    await until("document.querySelector('.image-tag-editor [role=status]').textContent.includes('已指向其他镜像')");
    assert.equal(await evaluate("imageCalls.some(item=>item.action==='image.tag')"),false);
    await evaluate(`
      imageTagForm.elements.image.value = 'team/app:next';
      imageTagForm.elements.image.dispatchEvent(new Event('input'));
      renderWorkspaceBody();
      if (document.querySelector('.image-tag-editor form') !== imageTagForm) throw new Error('snapshot refresh discarded the image tag form');
      imageTagForm.requestSubmit();
    `);
    await until("document.querySelector('.image-tag-editor [role=status]').textContent.includes('标签已添加')");
    assert.deepEqual(await evaluate("imageCalls.filter(item=>item.image?.endsWith(':next')).map(item=>item.action)"), ['image.tag-preview','image.tag']);
    assert.equal(await evaluate("imageCalls.find(item=>item.action==='image.tag').expected_image_id"),'');
    assert.equal(await evaluate("imageCalls.find(item=>item.action==='image.tag').overwrite"),undefined);
    assert.match(await evaluate("document.querySelector('.image-tags-panel').textContent"), /registry\.example\.test\/team\/app:next/);
    await screenshot('image-tags');
    await send('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:true},sessionId);
    assert.equal(await evaluate('document.documentElement.scrollWidth <= innerWidth'),true,'image label cards fit a narrow viewport');
    await send('Emulation.setDeviceMetricsOverride',{width:1440,height:900,deviceScaleFactor:1,mobile:false},sessionId);
    await evaluate(`
      document.querySelector('.image-tags-panel button[aria-label="推送 registry.example.test/team/app:next"]').click();
    `);
    await until("document.querySelector('#management-content select[name=scope]')?.options.length === 2");
    await evaluate(`
      var imagePushForm = document.querySelector('#management-content form');
      imagePushForm.elements.scope.selectedIndex=1;
      imagePushForm.requestSubmit();
    `);
    await until("document.getElementById('management-content').textContent.includes('操作成功')");
    assert.deepEqual(await evaluate("imageCalls.find(item=>item.action==='image.push')"),{
      action:'image.push',image_id:'sha256:'+'1'.repeat(64),image:'registry.example.test/team/app:next',
      registry_id:'test-registry',generation:1
    });
    await evaluate(`
      document.getElementById('management-dialog').close();
      window.nativeImageDownload = HTMLAnchorElement.prototype.click;
      HTMLAnchorElement.prototype.click = function() {
        if (this.download === 'docker-image.tar') window.imageDownloadPath = new URL(this.href).pathname;
        else return nativeImageDownload.call(this);
      };
      [...document.querySelectorAll('.maintenance-actions button')].find(button=>button.textContent==='导出').click();
      document.querySelector('#management-content button.primary').click();
    `);
    await until("window.imageDownloadPath && document.getElementById('management-content').textContent.includes('操作成功')");
    assert.equal(await evaluate('imageDownloadPath'),'/api/v1/nodes/%40local/image-transfers/image.export');
    await evaluate(`
      HTMLAnchorElement.prototype.click = nativeImageDownload;
      document.getElementById('management-dialog').close();
      document.querySelector('#node-resource-nav a[data-category=images]').click();
    `);
    await until("!document.getElementById('import-image').hidden");
    await evaluate(`
      window.nativeImageXHR = window.XMLHttpRequest;
      window.XMLHttpRequest = class extends EventTarget {
        upload = new EventTarget();
        open(method,path) { window.imageUploadPath = [method,path]; }
        setRequestHeader(name,value) { window.imageUploadHeader = [name,value]; }
        send(file) {
          window.imageUploadFile = file instanceof File && file.name === 'sample.tar';
          this.upload.dispatchEvent(new ProgressEvent('progress',{loaded:file.size}));
          if (window.holdImageUpload) return;
          this.status = 204;
          queueMicrotask(() => {
            this.dispatchEvent(new Event('load'));
            this.dispatchEvent(new Event('loadend'));
          });
        }
        abort() {
          window.imageUploadAborted = true;
          this.dispatchEvent(new Event('abort'));
          this.dispatchEvent(new Event('loadend'));
        }
      };
      window.submitImageArchive = () => {
        document.getElementById('import-image').click();
        const form = document.querySelector('#management-content form');
        const files = new DataTransfer();
        files.items.add(new File(['image'],'sample.tar'));
        form.elements.archive.files=files.files;
        form.requestSubmit();
        form.elements.confirmed.checked=true;
        form.requestSubmit();
      };
      submitImageArchive();
    `);
    await until("document.getElementById('management-content').textContent.includes('操作成功')");
    assert.equal(await evaluate('imageUploadFile'),true);
    assert.deepEqual(await evaluate('imageUploadPath'),['POST','/api/v1/nodes/%40local/image-transfers/image.import']);
    assert.deepEqual(await evaluate('imageUploadHeader'),['Content-Type','application/x-tar']);
    assert.equal(await evaluate("imageCalls.filter(item=>item.action==='image.import').length"),1);
    assert.equal(await evaluate("document.querySelector('.image-transfer-progress progress').value"),100);
    await evaluate(`
      document.getElementById('management-dialog').close();
      window.holdImageUpload = true;
      window.imageUploadPath = null;
      submitImageArchive();
    `);
    await until("imageUploadPath");
    await evaluate("history.pushState(null, '', '/node/%40local/overview'); dispatchEvent(new PopStateEvent('popstate'))");
    await until("imageUploadAborted && !document.getElementById('management-dialog').open");
    await evaluate("window.XMLHttpRequest = nativeImageXHR; window.fetch = imageFetch");
    await evaluate("history.pushState(null, '', " + JSON.stringify(containerHash) + "); dispatchEvent(new PopStateEvent('popstate'))");
    await until("document.getElementById('container-heading')?.textContent === 'test-web'");
    await evaluate("document.querySelector('.container-action-panel a[href$=\"/compose/test-stack/edit\"]').click()");
    await until("document.querySelector('#maintenance-page').textContent.includes('受限管理')");
    assert.equal(await evaluate("document.querySelector('#maintenance-page textarea')"),null);
    assert.match(await evaluate("document.querySelector('#maintenance-page').textContent"),/当前运行配置.*test-app:v1/s);
    await evaluate("document.getElementById('maintenance-back').click()");
    await until("document.querySelector('.maintenance-heading h2')?.textContent === 'test-stack'");
    assert.match(await evaluate("document.querySelector('.maintenance-related').textContent"),/test-web/);
    assert.match(await evaluate("document.querySelector('.maintenance-heading').textContent"),/受限管理/);
    await evaluate("[...document.querySelectorAll('.maintenance-actions button')].find(b=>b.textContent==='删除项目').click()");
    assert.match(await evaluate("document.getElementById('management-content').textContent"),/test-web/);
    assert.equal(await evaluate("document.querySelector('#management-content [name=remove_volumes]')"),null);
    await evaluate(`
      window.beforeProjectRemove = window.fetch;
      window.fetch = (path, options) => {
        if (path.endsWith('/operations') && options?.method === 'POST') {
          window.projectRemoval = JSON.parse(options.body);
          return Promise.resolve(Response.json({action:'compose.remove',state:'succeeded'}));
        }
        return beforeProjectRemove(path,options);
      };
      [...document.querySelectorAll('#management-content button')].find(b=>b.textContent==='确认删除项目').click();
    `);
    await until("location.pathname === '/node/%40local/compose' && !document.getElementById('management-dialog').open");
    assert.deepEqual(await evaluate('projectRemoval'),{action:'compose.remove',target:'test-stack',remove_volumes:false,generation:1});
    const projectRow = "document.querySelector('#resource-view a[href$=test-stack]')";
    assert.equal(await evaluate(projectRow),null,'confirmed removal immediately hides the project');
    await evaluate("loadNodeResources(true)");
    assert.equal(await evaluate(projectRow),null,'an older container inventory must not restore the project');
    await evaluate(`
      window.fetch = async (path, options) => {
        const response = await beforeProjectRemove(path, options);
        if (!path.endsWith('/resources')) return response;
        const data = await response.json();
        data.overview.section_times = {containers:new Date(Date.now() + 60000).toISOString()};
        return Response.json(data);
      };
      loadNodeResources(true).finally(() => { window.fetch = beforeProjectRemove; });
    `);
    await until(projectRow);
    await evaluate(projectRow + ".click()");
    await until("document.querySelector('.maintenance-heading h2')?.textContent === 'test-stack'");
    assert.equal(await evaluate("document.querySelector('.maintenance-actions a[href$=\"/redeploy\"]')"),null);
    await evaluate(`
      window.composeSourceFetch = window.fetch;
      window.fetch = (path,options) => {
        const body = options?.method === 'POST' && path.endsWith('/operations') ? JSON.parse(options.body) : {};
        if (body.action === 'compose.source-read') return Promise.resolve(Response.json({
          id:'source-read',state:'succeeded',result:{compose_source:{revision:'a'.repeat(64),files:[
            {path:'compose.yaml',content:'# original comment\\nservices: {web: {image: nginx}}'},
            {path:'compose.override.yaml',content:'services: {web: {restart: always}}'}
          ]}}
        }));
        if (body.action === 'compose.source-preview' || body.action === 'compose.source-save') {
          window.sourceCommand = body;
          return Promise.resolve(Response.json({id:'source-edit',state:'succeeded',
            result:{compose_source:{revision:'b'.repeat(64),before:['web'],after:['web','worker']}}}));
        }
        return composeSourceFetch(path,options);
      };
      document.querySelector('.maintenance-actions a[href$="/edit"]').click();
    `);
    await until("document.querySelector('select[name=source-file]')");
    assert.deepEqual(await evaluate("Array.from(document.querySelector('select[name=source-file]').options).map(o=>o.value)"), ['compose.yaml','compose.override.yaml']);
    assert.match(await evaluate("document.querySelector('textarea[name=compose]').value"), /^# original comment/);
    await screenshot('compose-source');
    await evaluate(`
      var sourceForm = document.querySelector('#maintenance-page form');
      sourceForm.elements['source-file'].value = 'compose.override.yaml';
      sourceForm.elements['source-file'].dispatchEvent(new Event('change'));
      sourceForm.elements.compose.value += '\\n# edited';
      sourceForm.elements.compose.dispatchEvent(new Event('input'));
      sourceForm.requestSubmit();
    `);
    await until("document.querySelector('.compose-preview').textContent.includes('worker')");
    assert.equal(await evaluate("sourceCommand.compose_source.revision"), 'a'.repeat(64));
    await evaluate("[...document.querySelectorAll('.compose-preview button')].find(button=>button.textContent==='保存到源文件').click()");
    await until("document.getElementById('maintenance-status').textContent.includes('源文件已保存')");
    assert.equal(await evaluate("sourceCommand.compose_source.path === 'compose.override.yaml' && !sourceForm.elements['source-file'].disabled"), true);
    await evaluate(`
      window.singleCompose = 'services: {web: {image: "busybox:latest", ports: ["8080:80"], volumes: ["data:/data"]}}\\nvolumes: {data: {external: true}}';
      window.fetch = (path,options) => {
        const body = options?.method === 'POST' ? JSON.parse(options.body || '{}') : {};
        if (path === '/api/v1/compose-adopt' && !window.testMissingAdoptionPin) {
          // The source fixture uses an image not present in the static inventory.
          return composeSourceFetch(path,{...options,body:JSON.stringify({...body,
            pins:{web:['busybox@sha256:' + 'a'.repeat(64)]}})});
        }
        if (path.endsWith('/operations') && body.action === 'compose.source-read') return Promise.resolve(Response.json({
          id:'adopt-source',state:'succeeded',result:{compose_source:{revision:'a'.repeat(64),
            files:[{path:'compose.yaml',content:window.singleCompose}]}}
        }));
        if (path === '/api/v1/compose-templates' && options?.method === 'POST') {
          window.adoptionTemplate = body;
          return Promise.resolve(Response.json({id:'new-template',name:body.name,
            versions:[{version:1,parameters:{ports:[{service:'web',port:0,value:'8080:80'}]}}]}, {status:201}));
        }
        return composeSourceFetch(path,options);
      };
      [...document.querySelectorAll('#maintenance-page button')].find(b=>b.textContent==='重新读取源文件').click();
    `);
    const adoptButton = "[...document.querySelectorAll('#maintenance-page button')].find(b=>b.textContent==='评估纳管')";
    const backToSource = "[...document.querySelectorAll('#maintenance-page button')].find(b=>b.textContent==='返回源文件')";
    await evaluate('window.testMissingAdoptionPin = true');
    await until(adoptButton);
    await evaluate(adoptButton + '.click()');
    await until(backToSource);
    assert.match(await evaluate("document.getElementById('maintenance-page').textContent"), /没有服务可以按受管模型纳管/);
    await evaluate(backToSource + '.click(); window.testMissingAdoptionPin = false');
    await until(adoptButton);
    await evaluate(adoptButton + '.click()');
    await until("document.querySelector('[name=adopt-review]') || document.getElementById('maintenance-status').textContent !== '正在评估纳管…'");
    assert.ok(await evaluate("!!document.querySelector('[name=adopt-review]')"),await evaluate("document.getElementById('maintenance-page').textContent"));
    assert.match(await evaluate("document.getElementById('maintenance-page').textContent"), /固定宿主机端口.*8080:80/);
    await evaluate(backToSource + '.click()');
    await until(adoptButton);
    await evaluate(adoptButton + '.click()');
    await until("document.querySelector('[name=adopt-review]')");
    assert.equal(await evaluate("document.querySelector('#maintenance-page form').checkValidity()"),false);
    await evaluate("document.querySelector('[name=adopt-review]').click(); document.querySelector('#maintenance-page form').requestSubmit()");
    await until("document.querySelector('.adopt-steps')");
    assert.deepEqual(await evaluate("adoptionTemplate.parameters.ports"),[{service:'web',port:0}]);
    assert.match(await evaluate("document.querySelector('.adopt-steps').textContent"), /临时端口.*再启动旧项目/);
    assert.equal(await evaluate("document.querySelector('.adopt-steps + .management-actions a[href=\"/console\"]')?.textContent || [...document.querySelectorAll('#maintenance-page a')].find(a=>a.pathname==='/console')?.textContent"), '先创建项目和部署目标');
    await evaluate("window.fetch = composeSourceFetch");
    await evaluate("history.pushState(null, '', '/node/%40local/containers'); dispatchEvent(new PopStateEvent('popstate'))");
    await until("document.querySelector('#create-container')?.getClientRects().length");
    assert.equal(await evaluate(`document.getElementById('container-filters')?.contains(document.getElementById('container-search'))`), true);
    if (await evaluate("CSS.supports('appearance', 'base-select') && CSS.supports('selector(::picker(select))')")) {
      assert.equal(await evaluate("document.querySelector('#container-state-filter selectedcontent')?.textContent.trim()"), '全部状态');
      assert.equal(await evaluate(`(() => {
        const picker = document.getElementById('container-state-filter');
        picker.value = 'running';
        const marker = picker.querySelector('selectedcontent [data-status=running]');
        const background = marker && getComputedStyle(marker).backgroundColor;
        picker.value = '';
        return Boolean(background && background !== 'rgba(0, 0, 0, 0)');
      })()`), true, 'the collapsed filter shows the current status marker');
    }
    await send('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:true},sessionId);
    assert.equal(await evaluate(`document.getElementById('container-filters').getBoundingClientRect().top >=
      document.getElementById('create-container').getBoundingClientRect().bottom`), true, 'mobile filters sit below the list action');
    assert.equal(await evaluate("document.documentElement.scrollWidth <= innerWidth"), true);
    await send('Emulation.setDeviceMetricsOverride',{width:1440,height:900,deviceScaleFactor:1,mobile:false},sessionId);
    await evaluate(`
      window.maintenanceFetch = window.fetch;
      window.fetch = (path, options) => {
        if (path === '/api/v1/registries') return Promise.resolve(Response.json({items:[
          {id:'test-creds',name:'Hub',host:'docker.io',enabled:true,has_credential:true,repository_prefix:'library/test-app'}
        ]}));

        if (path === '/api/v1/nodes/%40local/operations' && options?.method === 'POST') {
          const body = JSON.parse(options.body);
          if (body.action === 'container.edit-config') return Promise.resolve(Response.json({
            id:'config',state:'succeeded',result:{edit_config:{environment:{APP_MODE:'old-mode',KEEP:'unchanged',EMPTY:''},labels:{team:'platform'},
              resources:{nano_cpus:500000000,memory_bytes:134217728,pids:64}}}
          }));
          if (body.action === 'container.create') {
            window.createBody = body;
            return Promise.resolve(Response.json({id:'create',action:'container.create',state:'succeeded',
              result:{resource_observed_at:new Date().toISOString(),container:{full_id:'d'.repeat(64),name:body.name,image:body.edit.image,state:'running'}}}));
          }
          if (body.action === 'container.replace') {
            window.replacementBody = body;
            return Promise.resolve(Response.json({id:'replace',action:'container.replace',state:'succeeded',
              result:{container:{full_id:'e'.repeat(64)}}}));
          }
        }
        return maintenanceFetch(path,options);
      };
      document.querySelector('#create-container').click();
    `);
    await until("document.querySelector('#maintenance-page form')");
    assert.equal(await evaluate(`['name','image-source','image','restart'].every(name =>
      document.querySelector('[data-edit-group=basics]')?.querySelector('[name=' + name + ']'))`), true, 'core container fields share one section');
    await evaluate('window.scrollTo(0,0)');
    await screenshot('container-create');
    assert.equal(await evaluate("document.querySelector('.editor-nav [aria-current=location]')?.textContent"), '基础配置');
    assert.equal(await evaluate(`document.querySelector('[data-edit-group=ports] .editor-add-row').closest('.editor-group-heading') !== null`), true);
    assert.equal(await evaluate(`(() => {
      const button = document.querySelector('.editor-nav-submit');
      const bar = document.querySelector('.editor-submit-bar');
      return button.getBoundingClientRect().bottom <= innerHeight && button.getClientRects().length === 1 &&
        button.form === document.querySelector('.standalone-editor') && bar.getClientRects().length === 0;
    })()`), true, 'desktop preview sits in the navigation without covering form controls');
    assert.equal(await evaluate("document.querySelectorAll('.standalone-editor input[type=range]').length"),3);
    await evaluate("document.querySelector('[data-limit=nano_cpus]').focus()");
    await send('Input.dispatchKeyEvent',{type:'keyDown',key:'ArrowRight',code:'ArrowRight',windowsVirtualKeyCode:39},sessionId);
    await send('Input.dispatchKeyEvent',{type:'keyUp',key:'ArrowRight',code:'ArrowRight',windowsVirtualKeyCode:39},sessionId);
    assert.ok(await evaluate("Number(document.querySelector('[name=nano_cpus]').value) > 0"));
    await evaluate(`
      var createContainerForm = document.querySelector('#maintenance-page form');
      createContainerForm.elements.name.value = 'new-app';
      createContainerForm.elements.nano_cpus.value = '0.75';
      createContainerForm.elements.memory_bytes.value = '256';
      createContainerForm.elements.memory_bytes.dispatchEvent(new Event('input',{bubbles:true}));
      if (document.querySelector('[data-limit=memory_bytes]').value !== '256') throw new Error('memory slider out of sync');
      const pidsSlider = document.querySelector('[data-limit=pids]');
      pidsSlider.value = '96';
      pidsSlider.dispatchEvent(new Event('input',{bubbles:true}));
      createContainerForm.elements['image-source'].value = '@custom';
      createContainerForm.elements['image-source'].dispatchEvent(new Event('change',{bubbles:true}));
      createContainerForm.elements['image-host'].value = 'registry.example.test:5000';
      createContainerForm.elements.image.value = 'team/alpine:3.20';
      document.querySelector('.editor-nav-submit').click();
    `);
    assert.match(await evaluate("document.querySelector('.compose-preview').textContent"), /registry\.example\.test:5000\/team\/alpine:3\.20/);
    assert.deepEqual(await evaluate("splitStandaloneImage('sha256:' + 'a'.repeat(64))"), ['@local','sha256:' + 'a'.repeat(64)]);
    await evaluate(`
      createContainerForm.elements['image-source'].value = 'docker.io';
      createContainerForm.elements['image-source'].dispatchEvent(new Event('change',{bubbles:true}));
      createContainerForm.elements.image.value = 'alpine:3.20';
      document.querySelector('[data-edit-group=volumes] .editor-add-row').click();
      const bindKind = document.querySelector('[data-edit-group=volumes] [data-field=type]');
      bindKind.value = 'bind';
      bindKind.dispatchEvent(new Event('change',{bubbles:true}));
      document.querySelector('[data-edit-group=volumes] [data-field=bind]').value = '/srv/new-site';
      document.querySelector('[data-edit-group=volumes] [data-field=destination]').value = '/site';
      document.querySelector('[data-edit-group=volumes] [data-field=mode]').value = 'ro';
      createContainerForm.requestSubmit();
      document.querySelector('.compose-preview button').click();
    `);
    await until("document.getElementById('maintenance-status').textContent.includes('操作成功')");
    assert.equal(await evaluate('createBody.name'), 'new-app');
    assert.deepEqual(await evaluate('createBody.edit.resources'),{nano_cpus:750000000,memory_bytes:268435456,pids:96});
    assert.equal(await evaluate("nodeSnapshot.overview.containers.find(container => container.name === 'new-app')?.state"),'running');
    await evaluate("nodeResourceChanges.delete('containers/'+'d'.repeat(64)); void loadNodeResources(true)");
    assert.equal(await evaluate('createBody.edit.image'), 'docker.io/alpine:3.20');
    assert.deepEqual(await evaluate('createBody.edit.binds'), ['/srv/new-site:/site:ro']);
    assert.match(await evaluate("document.querySelector('#maintenance-results a').pathname"), /dddd$/);
    await evaluate(`history.pushState(null, '', '/node/%40local/containers/' + 'c'.repeat(64) + '/edit'); dispatchEvent(new PopStateEvent('popstate'))`);
    await until("document.querySelector('#maintenance-page form')");
    assert.equal(await evaluate("document.getElementById('maintenance-status').textContent"),'');
    assert.ok(await evaluate("document.querySelector('#maintenance-page select[name=image-source] option[value=\"registry.example.test\"]')"));
    await evaluate(`
      var replacementForm = document.querySelector('#maintenance-page form');
      if (replacementForm.elements.nano_cpus.value !== '0.5' ||
          replacementForm.elements.memory_bytes.value !== '128' || replacementForm.elements.pids.value !== '64')
        throw new Error('resource limits were not restored');
      const cpuSlider = document.querySelector('[data-limit=nano_cpus]');
      if (cpuSlider.value !== '0.5') throw new Error('CPU slider was not restored');
      cpuSlider.value = '0';
      cpuSlider.dispatchEvent(new Event('input',{bubbles:true}));
      replacementForm.requestSubmit();
      if (!document.querySelector('.compose-preview').textContent.includes('重建容器') ||
          !document.querySelector('.compose-preview').textContent.includes('不限')) throw new Error('clearing CPU limit did not preview replacement');
    `);
    await evaluate(`
      replacementForm.elements.nano_cpus.value = '0.75';
      replacementForm.requestSubmit();
      if (!document.querySelector('.compose-preview').textContent.includes('Docker 原地更新'))
        throw new Error('resource-only edit did not preview in-place update');
      replacementForm.elements.nano_cpus.value = '0.5';
      replacementForm.elements.image.value = 'test-app:v1';
      replacementForm.elements.restart.value = 'unless-stopped';
      document.querySelector('[data-edit-group=ports] .editor-add-row').click();
      document.querySelector('[data-edit-group=ports] [data-field=host]').value = '8080';
      document.querySelector('[data-edit-group=ports] [data-field=container]').value = '80';
      if (document.querySelector('[data-edit-group=environment] [data-field=value]').value !== 'old-mode' ||
          document.querySelector('[data-edit-group=labels] [data-field=value]').value !== 'platform')
        throw new Error('existing environment or label value was not restored');
      document.querySelector('[data-edit-group=environment] [data-field=value]').value = 'production';
      document.querySelector('[data-edit-group=volumes] .editor-add-row').click();
      document.querySelector('[data-edit-group=volumes] .editor-row:last-child [data-field=volume]').value = 'test-data';
      document.querySelector('[data-edit-group=volumes] .editor-row:last-child [data-field=destination]').value = '/data';
      document.querySelector('[data-edit-group=networks] .editor-add-row').click();
      document.querySelector('[data-edit-group=networks] [data-field=network]').value = 'test-net';
      renderWorkspaceBody();
    `);
    assert.equal(await evaluate("document.querySelector('#maintenance-page form') === replacementForm"), true, 'refresh keeps the draft form');
    await screenshot('container-edit');
    const secondMountCaption = "document.querySelector('[data-edit-group=volumes] .editor-row + .editor-row label:has([data-field=destination]) .editor-field-caption')";
    assert.equal(await evaluate(secondMountCaption + ".getClientRects().length"),0);
    assert.equal(await evaluate("document.querySelector('[data-edit-group=networks] select[data-field=network]').getAttribute('aria-label')"),'节点网络');
    assert.equal(await evaluate("document.querySelector('.editor-remove-row').getAttribute('aria-label')"), '移除一项端口映射');
    assert.equal(await evaluate("document.querySelector('.editor-remove-row svg')?.getAttribute('aria-hidden')"), 'true');
    assert.deepEqual(await evaluate("Array.from(document.querySelectorAll('[data-edit-group=volumes] .editor-row:last-child select[data-field=volume] option')).map(option => option.value)"), ['', 'test-data']);
    await evaluate("Array.from(document.querySelectorAll('.editor-nav button')).find(button => button.textContent === '卷挂载').click()");
    assert.equal(await evaluate("document.activeElement.textContent"), '卷挂载');
    assert.equal(await evaluate("document.querySelector('.editor-nav [aria-current=location]').textContent"), '卷挂载');
    assert.deepEqual(await evaluate("Array.from(document.querySelectorAll('[data-edit-group=networks] select[data-field=network] option')).map(option => option.value)"), ['', 'test-net']);
    assert.equal(await evaluate("document.querySelector('[data-edit-group=volumes] [data-field=bind]').value"), '/srv/site');

    await send('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:true},sessionId);
    assert.equal(await evaluate("document.documentElement.scrollWidth <= innerWidth"), true);
    assert.ok(await evaluate(secondMountCaption + ".getClientRects().length"));
    assert.equal(await evaluate("getComputedStyle(document.querySelector('.editor-nav')).position"), 'sticky');
    assert.equal(await evaluate("document.querySelector('.editor-submit-bar button').getClientRects().length > 0 && document.querySelector('.editor-nav-submit').getClientRects().length === 0"), true, 'mobile keeps its own visible submit control');
    await evaluate("Array.from(document.querySelectorAll('.editor-nav button')).find(button => button.textContent === '环境变量').click()");
    assert.equal(await evaluate(`(() => {
      const nav = document.querySelector('.editor-nav').getBoundingClientRect();
      return document.activeElement.textContent === '环境变量' && nav.top >= 0 &&
        nav.bottom <= innerHeight && document.activeElement.getBoundingClientRect().top >= nav.bottom;
    })()`), true, 'sticky navigation leaves the destination visible');
    assert.equal(await evaluate(`(() => {
      const row = document.querySelector('[data-edit-group=environment] .editor-row');
      return row.querySelector('.editor-remove-row').getBoundingClientRect().left >= row.querySelector('label').getBoundingClientRect().right;
    })()`), true, 'a narrow key-value row uses one line for its name and remove action');
    await send('Emulation.setDeviceMetricsOverride',{width:1440,height:900,deviceScaleFactor:1,mobile:false},sessionId);
    await evaluate('replacementForm.requestSubmit()');
    assert.match(await evaluate(`document.querySelector('.compose-preview').textContent`), /端口映射.*环境变量/);
    assert.equal(await evaluate(`document.querySelector('.compose-preview button').textContent`), '确认替换');
    assert.equal(await evaluate("document.querySelector('.compose-preview').textContent.includes('production')"), false);
    await evaluate("document.querySelector('.compose-preview button').click()");
    await until("document.getElementById('maintenance-status').textContent.includes('操作成功')");
    assert.deepEqual(await evaluate("replacementBody.edit.ports"), ['8080:80/tcp']);
    assert.deepEqual(await evaluate("replacementBody.edit.volumes"), ['test-data:/data']);
    assert.deepEqual(await evaluate("replacementBody.edit.networks"), ['test-net']);
    assert.equal(await evaluate("replacementBody.edit.image"), 'docker.io/test-app:v1');
    assert.deepEqual(await evaluate("replacementBody.edit.pull_scope"), {registry_id:'test-creds'});
    assert.equal(await evaluate("'pull_scope' in createBody.edit"), false, 'a different repository must not receive the authorization');

    assert.equal(await evaluate("replacementBody.edit.environment.APP_MODE"), 'production');
    assert.deepEqual(await evaluate("Object.keys(replacementBody.edit.environment)"), ['APP_MODE']);
    assert.equal(await evaluate("'binds' in replacementBody.edit || 'labels' in replacementBody.edit"), false);
    assert.equal(await evaluate("document.getElementById('management-dialog').open"), false);
    assert.match(await evaluate("document.querySelector('#maintenance-results a').pathname"), /eeee$/);
    assert.equal(await evaluate("document.getElementById('maintenance-back').pathname"), '/node/%40local/containers');
    await until('!pendingNodeRead');
    await evaluate(`
      window.managedStack = 'moori-' + 'f'.repeat(32);
      window.managedLoads = 0;
      window.managedApplies = 0;
      window.fetch = (path, options) => {
        if (path.endsWith('/compose-projects/' + managedStack)) {
          return Promise.resolve(++managedLoads === 1 ? new Response('temporary source error', {status:503}) :
            Response.json({project:'demo',deployment:'source',node:{agent:'@local',generation:1}}));
        }
        if (path.endsWith('/compose-apply')) {
          managedApplies++;
          return new Promise(resolve => { window.finishManagedApply = resolve; });
        }
        if (path.endsWith('/compose-preview')) {
          window.maintainedBody = JSON.parse(options.body);
          return Promise.resolve(Response.json({
            compose: maintainedBody.compose, snapshot_digest:'preview', node:{agent:'@local',generation:1}, }));
        }
        if (path.endsWith('/deployments/source')) return Promise.resolve(Response.json({
          id:'source',state:'succeeded',release:'v1',snapshot:{
            target:'production',compose:{services:{web:{image:'nginx@sha256:'+'a'.repeat(64), ports:['8080:80','8443:443'], command:['nginx','-g','daemon off;']},
              db:{image:'postgres@sha256:'+'b'.repeat(64), environment:{KEEP:'current',REMOVE:'retire'}}},
              volumes:{appdata:{}},networks:{backend:{}}}
          }
        }));
        return maintenanceFetch(path,options);
      };
      history.pushState(null, '', '/deployment/demo/source/edit'); dispatchEvent(new PopStateEvent('popstate'));
    `);
    await until("document.querySelector('#maintenance-page form')");
    const savedCompose = await evaluate("JSON.parse(document.querySelector('textarea[name=saved-compose]').value)");
    assert.deepEqual(savedCompose.services.web.command, ['nginx','-g','daemon off;']);
    assert.deepEqual(savedCompose.services.db.environment, {KEEP:'current',REMOVE:'retire'});
    assert.equal(await evaluate("document.querySelector('textarea[name=saved-compose]').readOnly"), true);
    await evaluate(`
      var editor = document.querySelector('#maintenance-page form');
      editor.elements.restart.value = 'always';
      editor.elements.service.value = 'db';
      editor.elements.service.dispatchEvent(new Event('change'));
      document.querySelector('[data-edit-group=labels] .editor-add-row').click();
      document.querySelector('[data-edit-group=labels] [data-field=key]').value = 'team';
      document.querySelector('[data-edit-group=labels] [data-field=value]').value = 'ops';
      document.querySelectorAll('[data-edit-group=environment] .editor-remove-row')[1].click();
      document.querySelector('[data-edit-group=environment] .editor-add-row').click();
      document.querySelectorAll('[data-edit-group=environment] [data-field=key]')[1].value = 'MODE';
      document.querySelectorAll('[data-edit-group=environment] [data-field=value]')[1].value = 'prod';
      document.querySelector('[data-edit-group=volumes] .editor-add-row').click();
      document.querySelector('[data-edit-group=volumes] [data-field=source]').value = 'appdata';
      document.querySelector('[data-edit-group=volumes] [data-field=destination]').value = '/var/lib/app';
      document.querySelector('[data-edit-group=networks] .editor-add-row').click();
      document.querySelector('[data-edit-group=networks] [data-field=network]').value = 'backend';
    `);
    assert.deepEqual(await evaluate(`Array.from(editor.elements).filter(control => control.willValidate && !control.validity.valid)
      .map(control => [control.name,control.value,control.validationMessage])`), []);
    await evaluate('editor.requestSubmit()');
    await until("document.querySelector('.compose-preview button') || document.getElementById('maintenance-status').textContent");
    assert.equal(await evaluate("document.getElementById('maintenance-status').textContent"), '');
    assert.equal(await evaluate("document.querySelector('.compose-preview button')?.textContent"), '确认部署');
    assert.deepEqual(await evaluate(`JSON.parse(maintainedBody.compose).services.web.ports`), ['8080:80','8443:443']);
    assert.equal(await evaluate(`JSON.parse(maintainedBody.compose).services.web.restart`), 'always');
    assert.equal(await evaluate(`JSON.parse(maintainedBody.compose).services.db.labels.team`), 'ops');
    assert.deepEqual(await evaluate("JSON.parse(maintainedBody.compose).services.db.environment"), {KEEP:'current',MODE:'prod'});
    assert.deepEqual(await evaluate("JSON.parse(maintainedBody.compose).services.db.volumes"), ['appdata:/var/lib/app']);
    assert.deepEqual(await evaluate("JSON.parse(maintainedBody.compose).services.db.networks"), ['backend']);
    await evaluate("document.querySelector('.compose-preview button').click()");
    await until('managedApplies === 1');
    assert.equal(await evaluate("editor.elements.service.disabled && editor.querySelector('button[type=submit]').disabled"), true);
    await evaluate(`finishManagedApply(new Response('temporary apply error',{status:503}))`);
    await until("!editor.elements.service.disabled && document.getElementById('maintenance-status').textContent.includes('temporary apply error')");
    await evaluate("document.querySelector('.compose-preview button').click()");
    await until('managedApplies === 2');
    await evaluate(`
      var managedFetch = window.fetch;
      window.fetch = async (path, options) => {
        const response = await managedFetch(path, options);
        if (path !== '/api/v1/nodes/%40local/resources') return response;
        const result = await response.json();
        result.updated_at = new Date(Date.now()+1000).toISOString();
        result.overview.compose_projects.push({name:managedStack,services:['web']});
        return Response.json(result);
      };
      history.pushState(null, '', '/node/%40local/compose/' + managedStack + '/edit'); dispatchEvent(new PopStateEvent('popstate'));
    `);
    await until("document.getElementById('maintenance-status').textContent.includes('temporary source error')");
    await evaluate("finishManagedApply(Response.json({id:'old-deployment'}))");
    assert.equal(await evaluate("document.querySelector('#maintenance-page .compose-preview')"), null);
    assert.equal(await evaluate("document.querySelector('#maintenance-results button')?.textContent"), '重试读取配置');
    await evaluate("document.querySelector('#maintenance-results button').click()");
    await until("managedLoads === 2 && document.querySelector('#maintenance-page form')");
    await evaluate(`window.fetch = maintenanceFetch; history.pushState(null, '', '/node/%40local/containers'); dispatchEvent(new PopStateEvent('popstate'))`);
    await evaluate("document.querySelector('#node-resource-nav a[data-category=containers]').click()");
    await until("document.querySelector('#resource-view tbody tr')");
    assert.equal(await evaluate("document.getElementById('container-search').value"), 'test-web');
    assert.match(await evaluate(`document.getElementById('resource-view').textContent`), /test-web.*15.5%.*1.0 MiB/);
    await until('!pendingNodeRead');
    await evaluate(`
      window.snapshotFetch = window.fetch;
      window.resourceReads = 0;
      window.fetch = async (path, options) => {
        if (path !== '/api/v1/nodes/%40local/resources') return snapshotFetch(path, options);
        const read = ++resourceReads;
        if (read === 1) await new Promise(resolve => { window.releaseResourceRead = resolve; });
        if (read >= 4) return new Response('node unavailable', {status:503});
        const response = await snapshotFetch(path, options);
        const result = await response.json();
        if (read <= 2) window.fixedSnapshotTimestamp = new Date(Date.now()+1000+read).toISOString();
        result.updated_at = fixedSnapshotTimestamp;
        result.contact = read === 3 ? 'offline' : 'online';
        if (read === 3) result.last_contact = '2026-01-01T00:00:00Z';
        if (read >= 2) result.overview.containers[0].stats.cpu_percent = 24.5;
        return Response.json(result);
      };
      void loadNodeResources();
      document.getElementById('reload-resources').click();
    `);
    assert.equal(await evaluate('resourceReads'), 1, 'refresh should queue behind an in-flight read');
    await until("!document.getElementById('page-progress').hidden && document.getElementById('reload-resources').getAttribute('aria-busy') === 'true'");
    await evaluate('releaseResourceRead()');
    await until("document.getElementById('resource-view').textContent.includes('24.5%')");
    await until("document.getElementById('page-progress').hidden && !document.getElementById('reload-resources').hasAttribute('aria-busy')");
    assert.equal(await evaluate("document.getElementById('container-search').value"), 'test-web');
    await evaluate("document.getElementById('reload-resources').click()");
    await until("document.getElementById('sidebar-node-status').dataset.contact === 'offline'");
    assert.match(await evaluate("document.getElementById('node-recovery').textContent"), /本机执行器离线.*上次连接.*仅供查看/);
    assert.equal(await evaluate("document.getElementById('create-container').hidden"), true);
    assert.equal(await evaluate("document.querySelector('#resource-view button.danger')"), null);
    assert.equal(await evaluate("document.getElementById('container-search').value"), 'test-web');
    await evaluate("document.getElementById('reload-resources').click()");
    await until("document.getElementById('resource-status').textContent.includes('保留上次快照')");
    assert.match(await evaluate("document.getElementById('resource-view').textContent"), /24.5%/);
    await evaluate(`
      window.fetch = async (path,options) => {
        const response = await snapshotFetch(path,options);
        if (path !== '/api/v1/nodes/%40local/resources') return response;
        return Response.json({...await response.json(),updated_at:fixedSnapshotTimestamp,contact:'online',management_available:false});
      };
      document.getElementById('reload-resources').click();
    `);
    await until("document.getElementById('node-recovery').textContent.includes('Docker 管理尚未就绪')");
    assert.equal(await evaluate("document.getElementById('create-container').hidden"), true);
    await evaluate(`window.fetch = snapshotFetch; document.getElementById('reload-resources').click()`);
    await until("document.getElementById('node-recovery').hidden && !document.getElementById('create-container').hidden");
    await evaluate(`
      window.logFetch = window.fetch;
      window.logCalls = [];
      window.logRequests = [];
      window.liveLogReads = [];
      window.enableLiveLogs = false;
      window.fetch = (path, options) => {
        const base = '/api/v1/nodes/%40local/operations';
        if (path.includes('/log-stream?')) {
          if (!enableLiveLogs) return Promise.resolve(new Response('live logs unavailable',{status:503}));
          const call = {path,signal:options.signal};
          const stream = new ReadableStream({
            start(controller) { call.append = value => controller.enqueue(typeof value === 'string' ? new TextEncoder().encode(value) : value); },
            cancel() { call.canceled = true; }
          });
          liveLogReads.push(call);
          return Promise.resolve(new Response(stream,{headers:{'Content-Type':'text/plain; charset=utf-8'}}));
        }
        if (path === base && options?.method === 'POST') {
          const command = JSON.parse(options.body);
          if (command.action === 'container.logs') {
            logCalls.push(command);
            logRequests.push({submitted:performance.now()});
            window.logSignal = options.signal;
            return Promise.resolve(Response.json({state:'queued', id:String(logCalls.length)}));
          }
        }
        if (path.startsWith(base + '/')) {
          const number = Number(path.substring(base.length + 1));
          const call = logCalls[number - 1];
          logRequests[number - 1].firstRead ??= performance.now();
          if (number === 5) {
            window.pendingLogRead = true;
            return new Promise((_resolve,reject) => {
              options.signal.addEventListener('abort', () => reject(options.signal.reason), {once:true});
            });
          }
          const log = Array.from({length:120 + number},(_,i) => 'application log line ' + i)
            .concat('test logs ' + number + ' / ' + call.tail).join(String.fromCharCode(10));
          return Promise.resolve(Response.json({state:'succeeded', result:{log, truncated:call.tail === 1000}}));
        }
        return logFetch(path, options);
      };
      const logContainerRow = [...document.querySelectorAll('#resource-view tr')].find(row => row.textContent.includes('test-web'));
      window.firstLogLink = logContainerRow.querySelector('a[aria-label="查看日志"]');
      firstLogLink?.click();
    `);
    const containerPath = '/node/%40local/containers/' + 'a'.repeat(64);
    assert.equal(await evaluate('firstLogLink?.pathname'), containerPath + '/logs');
    await until(`location.pathname === '${containerPath}/logs' && !document.getElementById('container-logs-page').hidden`);
    assert.equal(await evaluate("document.querySelector('#container-logs-page').getBoundingClientRect().top - document.querySelector('.node-context').getBoundingClientRect().bottom"),12);
    assert.equal(await evaluate("document.getElementById('management-dialog').open"), false);
    assert.equal(await evaluate("document.getElementById('node-resources').hidden"), true);
    assert.equal(await evaluate("document.activeElement.id"), 'container-logs-title');
    assert.equal(await evaluate("document.getElementById('container-logs-title').textContent"),'test-web');
    assert.equal(await evaluate("document.getElementById('container-logs-back').pathname"), '/node/%40local/containers');
    assert.equal(await evaluate(`document.querySelector('#container-logs-controls select[name=tail]')?.value`), '200');
    assert.deepEqual(await evaluate(`[...document.querySelectorAll('#container-logs-controls select[name=tail] option')].map(option => option.value)`), ['100', '200', '500', '1000']);
    assert.equal(await evaluate(`document.querySelector('#container-logs-controls input[name=auto-refresh]').checked`), true);
    assert.match(await evaluate("document.querySelector('#container-logs-controls .log-auto').textContent"), /实时/);
    await until(`document.getElementById('container-logs-output').textContent.includes('test logs 1 / 200')`);
    await screenshot('container-logs');
    assert.match(await evaluate("document.getElementById('container-logs-status').textContent"), /每 3 秒.*快照/);
    const logPosition = '(() => { const log = document.getElementById("container-logs-output"); return [log.scrollTop, log.scrollHeight - log.clientHeight]; })()';
    let [top, bottom] = await evaluate(logPosition);
    assert.ok(bottom > 0 && Math.abs(top - bottom) <= 2, 'first log result starts at the bottom');
    const firstPoll = await evaluate('logRequests[0].firstRead - logRequests[0].submitted');
    assert.ok(firstPoll < 850, 'first log result lookup took ' + firstPoll + ' ms');
    await until(`document.getElementById('container-logs-output').textContent.includes('test logs 2 / 200')`, 180);
    const setLogFilter = async value => evaluate(`(() => {
      const field = document.querySelector('#container-logs-controls input[name=log-filter]');
      field.value = ${JSON.stringify(value)};
      field.dispatchEvent(new Event('input'));
      return document.querySelector('.log-filter-count').textContent;
    })()`);
    assert.equal(await setLogFilter('log'),'匹配 123 / 123 行');
    assert.equal(await setLogFilter(''),'');
    assert.ok(await evaluate('logRequests[1].submitted - logRequests[0].firstRead >= 2900'), 'default refresh waits three seconds after reading');
    [top, bottom] = await evaluate(logPosition);
    assert.ok(Math.abs(top - bottom) <= 2, 'automatic refresh follows the tail from the bottom');
    const readingPosition = await evaluate("document.getElementById('container-logs-output').scrollTop = 96");

    const autoRefresh = `document.querySelector('#container-logs-controls input[name=auto-refresh]')`;
    await evaluate(autoRefresh + '.click()');
    assert.equal(await evaluate(autoRefresh + '.checked'), false);
    assert.equal(await evaluate(autoRefresh + '.getAttribute("role")'), 'switch');
    const logTail = `document.querySelector('#container-logs-controls select[name=tail]')`;
    if (await evaluate("CSS.supports('appearance', 'base-select') && CSS.supports('selector(::picker(select))')")) {
      const press = async (key, code, windowsVirtualKeyCode) => {
        const params = {key, code, windowsVirtualKeyCode};
        await send('Input.dispatchKeyEvent', {type:'keyDown', text:key === ' ' ? key : undefined, ...params}, sessionId);
        await send('Input.dispatchKeyEvent', {type:'keyUp', ...params}, sessionId);
      };
      await evaluate(logTail + '.focus()');
      await press(' ', 'Space', 32);
      await until(logTail + '.matches(":open")');
      const elevation = await evaluate(`(() => {
        const trigger = getComputedStyle(${logTail});
        const popup = getComputedStyle(${logTail}, '::picker(select)');
        return [trigger.borderTopWidth, popup.borderTopWidth, trigger.boxShadow, popup.boxShadow, popup.colorScheme];
      })()`);
      assert.deepEqual(elevation.slice(0, 2), ['0px', '0px']);
      assert.notEqual(elevation[2], 'none');
      assert.notEqual(elevation[3], 'none');
      assert.equal(elevation[4], 'dark');
      await press('Escape', 'Escape', 27);
      await until('!' + logTail + '.matches(":open")');
      assert.equal(await evaluate(logTail + '.value'), '200');
      await press(' ', 'Space', 32);
      await until(logTail + '.matches(":open")');
      await press('ArrowDown', 'ArrowDown', 40);
      await press('ArrowDown', 'ArrowDown', 40);
      await press('Enter', 'Enter', 13);
      await until(logTail + '.value === "1000"');
    } else {
      await evaluate(`${logTail}.value = '1000'; ${logTail}.dispatchEvent(new Event('change'));`);
    }
    assert.equal(await evaluate('logCalls.length'),2,'changing line count while paused keeps the current output frozen');
    await evaluate("document.querySelector('#container-logs-controls .log-refresh').click()");
    await until(`document.getElementById('container-logs-output').textContent.includes('test logs 3 / 1000')`);
    assert.equal(await evaluate(`document.getElementById('container-logs-status').textContent.includes('截断')`), true);
    await delay(3300);
    assert.equal(await evaluate('logCalls.length'), 3, 'pausing must stop automatic log refresh');

    await evaluate(autoRefresh + '.click()');
    assert.equal(await evaluate(autoRefresh + '.checked'), true);
    await until(`document.getElementById('container-logs-output').textContent.includes('test logs 4 / 1000')`, 180);
    [top, bottom] = await evaluate(logPosition);
    assert.ok(top < bottom && Math.abs(top - readingPosition) <= 2, 'automatic refresh keeps the reading position when scrolled up');
    assert.deepEqual(await evaluate(`logCalls.map(call => [call.tail, call.generation, call.target])`), [
      [200, 1, 'a'.repeat(64)], [200, 1, 'a'.repeat(64)], [1000, 1, 'a'.repeat(64)], [1000, 1, 'a'.repeat(64)],
    ]);
    await evaluate(`document.querySelector('#container-logs-controls .log-refresh').click()`);
    await until('logCalls.length === 5 && window.pendingLogRead');
    await evaluate("document.getElementById('container-logs-back').click()");
    await until("location.pathname === '/node/%40local/containers' && logSignal.aborted");
    await delay(3300);
    assert.equal(await evaluate('logCalls.length'), 5, 'leaving the log page must stop automatic refresh');
    assert.equal(await evaluate("document.getElementById('container-logs-output').textContent"), '');

    await evaluate(`document.querySelector('#resource-view a[href="${containerPath}"]').click()`);
    await until("document.getElementById('container-heading')?.textContent === 'test-web'");
    await evaluate(`document.querySelector('#resource-view a[href="${containerPath}/logs"]').click()`);
    await until(`location.pathname === '${containerPath}/logs' && document.getElementById('container-logs-output').textContent.includes('test logs 6 / 200')`);
    await evaluate("document.getElementById('container-logs-back').click()");
    await until(`location.pathname === '${containerPath}' && !document.getElementById('container-logs-output').textContent`);
    await evaluate(`
      window.enableLiveLogs = true;
      document.querySelector('#resource-view a[href="${containerPath}/logs"]').click();
    `);
    await until('liveLogReads.length === 1');
    const firstStream = new URL(await evaluate('liveLogReads[0].path'),pageURL);
    assert.match(firstStream.pathname,/\/containers\/a{64}\/log-stream$/);
    assert.deepEqual(Object.fromEntries(firstStream.searchParams),{tail:'200',generation:'1',timestamps:'false'});
    await evaluate(`liveLogReads[0].append(Array.from({length:128},(_,i)=>'live initial line '+i).join('\\n')+'\\n')`);
    await until("document.getElementById('container-logs-output').textContent.includes('live initial line 127')");
    assert.match(await evaluate("document.getElementById('container-logs-status').textContent"), /实时日志已连接/);
    [top,bottom] = await evaluate(logPosition);
    assert.ok(Math.abs(top-bottom) <= 2,'first live result follows the bottom');
    await evaluate(`
      document.getElementById('container-logs-output').scrollTop = 96;
      const bytes = new TextEncoder().encode('实时增量欧元 €\\n');
      liveLogReads[0].append(bytes.subarray(0,2));
      liveLogReads[0].append(bytes.subarray(2));
    `);
    await until("document.getElementById('container-logs-output').textContent.includes('实时增量欧元 €')");
    assert.equal((await evaluate(logPosition))[0],96,'stream updates keep the reading position');
    assert.match(await evaluate("document.getElementById('container-logs-output').textContent"), /live initial line 0/);
    await evaluate(autoRefresh + '.click()');
    await until('liveLogReads[0].signal.aborted && liveLogReads[0].canceled');
    assert.match(await evaluate("document.getElementById('container-logs-output').textContent"), /实时增量欧元/);
    assert.equal(await evaluate('logCalls.length'),6,'starting and pausing live logs does not read snapshots');
    await evaluate("document.querySelector('#container-logs-controls input[name=log-timestamps]').click()");
    await evaluate("document.querySelector('#container-logs-controls .log-refresh').click()");
    await until("document.getElementById('container-logs-output').textContent.includes('test logs 7 / 200')");
    assert.equal(await evaluate('logCalls[6].timestamps'),true);
    assert.equal(await evaluate('liveLogReads.length'),1,'manual refresh while paused uses one snapshot and does not resume streaming');
    await evaluate(`
      document.querySelector('#container-logs-controls select[name=tail]').value = '100';
      document.querySelector('#container-logs-controls select[name=tail]').dispatchEvent(new Event('change'));
      document.querySelector('#container-logs-controls input[name=auto-refresh]').click();
    `);
    await until('liveLogReads.length === 2');
    const secondStream = new URL(await evaluate('liveLogReads[1].path'),pageURL);
    assert.deepEqual(Object.fromEntries(secondStream.searchParams),{tail:'100',generation:'1',timestamps:'true'});
    await evaluate(`liveLogReads[1].append(Array.from({length:130},(_,i)=>'reconnected '+i).join('\\n')+'\\n')`);
    await until("document.getElementById('container-logs-output').textContent.includes('reconnected 129')");
    assert.match(await evaluate("document.getElementById('container-logs-output').textContent"), /^reconnected 30\n/);
    await evaluate("liveLogReads[1].append('界'.repeat(25000)+' recent')");
    await until("document.getElementById('container-logs-output').textContent.endsWith(' recent')");
    assert.ok(await evaluate("new TextEncoder().encode(document.getElementById('container-logs-output').textContent).length <= 65536"),'live output is byte-bounded');
    await evaluate("Object.defineProperty(document,'hidden',{configurable:true,value:true}); document.dispatchEvent(new Event('visibilitychange'))");
    await until('liveLogReads[1].signal.aborted && liveLogReads[1].canceled');
    await evaluate("delete document.hidden; document.dispatchEvent(new Event('visibilitychange'))");
    await until('liveLogReads.length === 3');
    await until("document.getElementById('container-logs-output').textContent === '暂无容器日志。'");
    assert.equal(await evaluate("document.querySelector('[name=log-run-scope]').value"),'current');
    await evaluate("const scope = document.querySelector('[name=log-run-scope]'); scope.value = 'all'; scope.dispatchEvent(new Event('change'))");
    await until('liveLogReads.length === 4 && liveLogReads[2].signal.aborted');
    assert.equal(new URL(await evaluate('liveLogReads[3].path'),pageURL).searchParams.get('all_log_runs'),'true');
    assert.equal(await evaluate("document.getElementById('container-logs-output').textContent"),'暂无容器日志。');
    await evaluate("document.getElementById('container-logs-back').click()");
    await until(`location.pathname === '${containerPath}' && liveLogReads[3].signal.aborted`);
    await evaluate('window.fetch = logFetch');
    await evaluate(`document.getElementById('resource-node').value='test-worker'; document.getElementById('resource-node').dispatchEvent(new Event('change'))`);
    await until("document.getElementById('node-meta').textContent.includes('主机名 remote-machine')");
    assert.equal(await evaluate("document.getElementById('node-heading').textContent"), 'test-worker');
    await evaluate("document.querySelector('#node-resource-nav a[data-category=images]').click()");
    await until("document.getElementById('resource-view').textContent.includes('test-app:v1')");
    assert.match(await evaluate(`document.getElementById('resource-view').textContent`), /test-app:v1/);
    assert.match(await evaluate(`document.getElementById('resource-status').textContent`), /已过期/);
    assert.match(await evaluate("document.getElementById('node-recovery').textContent"), /远程 Agent 已离线.*上次连接.*仅供查看/);
    assert.equal(await evaluate("document.getElementById('pull-image').hidden"), true);
    await evaluate(`
      window.historyFetch = window.fetch;
      window.historyReads = 0;
      window.fetch = (path,options) => {
        const endpoint = '/api/v1/nodes/test-worker/operations';
        if (path.startsWith(endpoint + '?limit=')) {
          const after = new URL(path, location.origin).searchParams.get('after');
          return Promise.resolve(Response.json({items:[{
            id:(after ? 'b' : 'a').repeat(32),action:'compose.remove',target:after ? 'older-stack' : 'test-stack',
            created_at:'2026-01-01T00:00:00Z',state:'failed'
          }],next:after ? '' : '123.1'}));
        }
        if (path.startsWith(endpoint + '?id=')) return Promise.resolve(Response.json([{
          id:'a'.repeat(32),action:'compose.remove',target:'test-stack',created_at:'2026-01-01T00:00:00Z',
          ...(historyReads++ === 0 ? {state:'running'} : {state:'failed',error:'Compose 容器已删除，但部分挂载卷未能删除'})
        }]));
        return historyFetch(path,options);
      };
      document.querySelector('#node-resource-nav a[data-category=operations]').click();
    `);
    await until("document.getElementById('resource-view').textContent.includes('test-stack')");
    await screenshot('operations');
    await evaluate("document.querySelector('.operation-history-footer button:last-child').click()");
    await until("document.getElementById('resource-view').textContent.includes('older-stack')");
    assert.match(await evaluate("document.querySelector('.operation-history-footer').textContent"),/第 2 页/);
    assert.equal(await evaluate("document.querySelector('.operation-history-footer button:last-child').disabled"),true);
    await evaluate("document.querySelector('.operation-history-footer button:first-child').click()");
    await until("document.getElementById('resource-view').textContent.includes('test-stack')");
    await evaluate("document.querySelector('#resource-view td a').click()");
    await until("document.getElementById('resource-view').textContent.includes('节点正在执行')");
    await until("document.getElementById('resource-view').textContent.includes('部分挂载卷未能删除')");
    for (const width of [390,1440]) {
      await send('Emulation.setDeviceMetricsOverride',{width,height:900,deviceScaleFactor:1,mobile:width === 390},sessionId);
      assert.ok(await evaluate(`(() => {
        const detail = document.querySelector('.operation-history-detail');
        const box = detail.getBoundingClientRect();
        return [...detail.children].every(child => {
          const item = child.getBoundingClientRect();
          return item.left >= box.left + 14 && item.right <= box.right - 14;
        });
      })()`),'operation detail content has side gutters at width ' + width);
    }
    assert.ok(await evaluate("[...document.querySelectorAll('#resource-view a')].some(link => link.pathname === '/node/test-worker/volumes')"));
    await evaluate("document.querySelector('#resource-view a[href$=volumes]').click()");
    await until("location.pathname === '/node/test-worker/volumes'");
    await evaluate('window.fetch = historyFetch');
    assert.equal(await evaluate(`document.getElementById('create-target').disabled`), false);
    assert.equal(await evaluate(`document.getElementById('onboarding').hidden`), true, 'local is available before creating any application target');
    await openView('environments');
    await evaluate("document.getElementById('account-toggle').click()");
    await screenshot('account-settings');
    await evaluate(`var f=document.getElementById('password-form'); f.elements.username.value='renamed'; f.elements.current.value='initial password'; f.elements.replacement.value='changed password'; f.elements.confirmation.value='changed password'; f.requestSubmit()`);
    await until(`!document.getElementById('login-panel').hidden && document.getElementById('notice').textContent.includes('账户已更新')`);
    await evaluate(`location.reload()`);
    await until(`document.getElementById('login-panel') && !document.getElementById('login-panel').hidden`);
    await screenshot('login');
    assert.equal(await evaluate(`document.getElementById('setup-panel').hidden`), true);
    await evaluate(`var f=document.getElementById('login-form'); f.elements.username.value='renamed'; f.elements.password.value='changed password'; f.requestSubmit()`);
    await until(`!document.getElementById('console').hidden`);
    assert.equal(await evaluate(`document.getElementById('password-form').elements.username.value`), 'renamed');
    await evaluate("document.querySelector('#console-nav [data-view=settings]').click(); document.querySelector('#settings-tabs a[href=\"/settings/ci\"]').click()");
    await until("document.querySelector('#settings-list a[href=\"/console\"]')");
    await evaluate("document.querySelector('#settings-list a[href=\"/console\"]').click()");
    await until("location.pathname === '/console'");
    assert.equal(await evaluate("document.getElementById('deployment-back').pathname"),'/settings/ci');
    await evaluate(`document.getElementById('create-target').click()`);
    await until(`!document.getElementById('onboarding').hidden && !document.getElementById('target-form').querySelector('button[type=submit]').disabled`);
    assert.equal(await evaluate(`document.getElementById('onboarding').previousElementSibling.contains(document.getElementById('create-target'))`), true);
    assert.equal(await evaluate(`document.getElementById('execution-mode').value`), 'local');
    await screenshot('deployment-target');
    await evaluate(`var f=document.getElementById('target-form'); f.elements.project.value='my-app'; f.elements.target.value='my-production'; f.requestSubmit()`);
    await until(`document.getElementById('target').value === 'my-production' && document.getElementById('notice').textContent.includes('本机部署目标已创建')`);
    assert.equal(await evaluate(`document.getElementById('empty-projects').hidden`), true);
    assert.equal(await evaluate(`document.getElementById('role').textContent`), '平台管理员');
    await evaluate("history.pushState(null, '', '/management'); dispatchEvent(new PopStateEvent('popstate'))");
    await until("document.querySelector('#console-nav [aria-current=page]')?.textContent.trim() === '系统设置'");
    assert.equal(await evaluate('location.pathname'), '/settings/registries');
    assert.equal(await evaluate("document.querySelector('#settings-tabs [aria-current=page]')?.textContent"), '镜像仓库');
    await until(`document.getElementById('settings-list').textContent.includes('添加仓库')`);
    await until("document.querySelector('.registry-discovery code')?.textContent === 'registry.example.test'");
    await evaluate(`window.registryDiscoveryFetch = window.fetch;
      window.fetch = async (path,options) => {
        if (path === '/api/v1/nodes/%40local/resources') {
          return Response.json({overview:{available:true,images:[{tags:['new.example.test/tools/demo:latest']}]}});
        }
        return registryDiscoveryFetch(path,options);
      };`);
    await evaluate('refreshVisibleNodeViews()');
    await until("document.querySelector('.registry-discovery code')?.textContent === 'new.example.test'");
    assert.equal(await evaluate("document.querySelectorAll('.registry-discovery code').length"),1);
    await evaluate('window.fetch = registryDiscoveryFetch; refreshVisibleNodeViews()');
    await until("document.querySelector('.registry-discovery code')?.textContent === 'registry.example.test'");
    await evaluate("document.querySelector('.registry-discovery .resource-detail-note button').click()");
    assert.equal(await evaluate("document.querySelector('#management-content input[name=host]').value"),'registry.example.test');
    await evaluate(`var r=document.querySelector('#management-content form'); r.elements.name.value='测试仓库'; r.elements.username.value='test'; r.elements.password.value='example'; r.requestSubmit()`);
    assert.match(await evaluate("document.getElementById('management-content').textContent"), /主服务未加载加密密钥/);
    await until(`document.getElementById('management-status').textContent.includes('镜像仓库凭证无法保存')`);
    await evaluate(`r.elements.username.value=''; r.requestSubmit()`);
    await until(`document.getElementById('settings-list').textContent.includes('测试仓库') && !document.getElementById('management-dialog').open`);
    assert.equal(await evaluate("document.querySelector('#settings-list tbody th[scope=row] .settings-entry-detail').textContent"), 'registry.example.test');
    assert.equal(await evaluate("document.querySelector('#settings-list tbody .status-pill').textContent"), '已启用');
    assert.equal(await evaluate("document.querySelector('#settings-list .settings-table-footer').textContent"), '已显示 1 条');
    await screenshot('settings-registries');
    await evaluate(`document.querySelector('#settings-list tbody button').click();
      [...document.querySelectorAll('#management-content button')].find(b=>b.textContent==='验证或拉取').click()`);
    assert.equal(await evaluate("!!document.querySelector('#management-content [name=project]')"),false);
    assert.deepEqual(await evaluate("[...document.querySelector('#management-content [name=agent]').options].map(o=>o.value).filter(id=>['@local','test-worker'].includes(id)).sort()"),['@local','test-worker']);
    await evaluate(`
      window.beforeRegistryProbe = window.fetch;
      window.fetch = (path,options) => {
        if (options?.method === 'POST' && path.endsWith('/operations')) {
          window.probeCall = {path,body:JSON.parse(options.body)};
          return Promise.resolve(Response.json({id:'probe',action:'registry.verify',state:'succeeded'}));
        }
        return beforeRegistryProbe(path,options);
      };
      var r=document.querySelector('#management-content form');
      r.elements.agent.value='@local';
      r.elements.image.value='registry.example.test/team/app:v1';
      r.requestSubmit();
    `);
    await until("window.probeCall");
    assert.deepEqual(await evaluate("probeCall.path"),'/api/v1/nodes/%40local/operations');
    assert.deepEqual(await evaluate("Object.keys(probeCall.body).sort()"),['action','generation','image','registry_id']);
    assert.equal(await evaluate("probeCall.body.action"),'registry.verify');
    await evaluate("window.fetch = beforeRegistryProbe; document.getElementById('management-dialog').close()");
    await evaluate("document.querySelector('#settings-tabs a[href=\"/settings/compose-templates\"]').click()");
    await until("location.pathname === '/settings/compose-templates' && document.querySelector('#settings-tabs [aria-current=page]')?.textContent === 'Compose 模板'");
    assert.equal(await evaluate("document.getElementById('workspace-section').textContent"), '系统设置');
    await until(`document.getElementById('settings-list').textContent.includes('创建模板')`);
    await evaluate(`document.getElementById('settings-list').querySelector('button').click(); var t=document.querySelector('#management-content form'); t.elements.name.value='基础模板'; t.elements.compose.value='services: {web: {image: "busybox@sha256:'+ 'a'.repeat(64) + '"}}'; t.requestSubmit()`);
    await until(`!document.getElementById('management-dialog').open && document.getElementById('settings-list').textContent.includes('基础模板')`);
    assert.equal(await evaluate("document.querySelector('#settings-list .settings-number').textContent"), '1');
    await screenshot('settings-templates');
    await evaluate(`document.querySelector('#settings-list tbody button').click(); [...document.querySelectorAll('#management-content button')].find(b=>b.textContent==='部署此版本').click()`);
    await until(`document.querySelector('#management-content form [name=target]').value==='my-production' && document.getElementById('management-content').textContent.includes('目标节点：本机 Docker')`);
    await evaluate(`document.getElementById('template-preview').click()`);
    await until(`document.getElementById('management-status').textContent.includes('校验通过') && document.getElementById('management-output').textContent.includes('busybox')`);
    await evaluate(`document.querySelector('#management-content form').requestSubmit()`);
    await until(`!document.getElementById('management-dialog').open && document.getElementById('notice').textContent.includes('部署申请已受理')`);
    await evaluate("document.querySelector('#settings-tabs a[href=\"/settings/users\"]').click()");
    await until("location.pathname === '/settings/users' && document.querySelector('#settings-list tbody')?.textContent.includes('renamed')");
    assert.equal(await evaluate("document.querySelector('#settings-tabs [aria-current=page]').textContent"), '用户管理');
    assert.equal(await evaluate("document.getElementById('notice').textContent"),'');
    await screenshot('settings-users');
    await evaluate("[...document.querySelectorAll('#settings-list button')].find(b=>b.textContent==='新建用户').click()");
    await evaluate(`var u=document.querySelector('#settings-list form'); u.elements.username.value='test-operator'; u.elements.password.value='initial operator password'; u.elements.confirmation.value='initial operator password'; u.requestSubmit()`);
    await until("document.querySelector('#settings-list tbody')?.textContent.includes('test-operator')");
    await evaluate("[...document.querySelectorAll('#settings-list tbody tr')].find(row=>row.textContent.includes('test-operator')).querySelector('button').click()");
    assert.equal(await evaluate("document.getElementById('management-dialog').open"), false);
    await evaluate(`var u=document.querySelector('#settings-list form'); u.elements.current.value='changed password'; u.elements.replacement.value='replacement operator password'; u.elements.confirmation.value='incorrect confirmation'; u.requestSubmit()`);
    await until("document.querySelector('#settings-list [role=status]')?.textContent.includes('两次密码')");
    await evaluate(`u.elements.confirmation.value='replacement operator password'; u.requestSubmit()`);
    await until("document.getElementById('notice').textContent.includes('密码已重置') && document.querySelector('#settings-list tbody')?.textContent.includes('test-operator')");
    await send('Emulation.setDeviceMetricsOverride', {width:390,height:844,deviceScaleFactor:1,mobile:true}, sessionId);
    assert.ok(await evaluate(`(() => {
      const panel = document.querySelector('#settings-list .settings-table');
      const action = panel.querySelector('tbody td:last-child button').getBoundingClientRect();
      return panel.scrollWidth <= panel.clientWidth && action.right <= panel.getBoundingClientRect().right && action.height >= 44;
    })()`), 'user list fits a mobile viewport with touch-sized row actions');
    await send('Emulation.setDeviceMetricsOverride', {width:1440,height:900,deviceScaleFactor:1,mobile:false}, sessionId);
    await evaluate(`history.pushState(null, '', '/node/%40local/containers/' + 'c'.repeat(64)); dispatchEvent(new PopStateEvent('popstate'))`);
    await until(`[...document.querySelectorAll('#resource-view button')].some(b=>b.textContent==='配置 Webhook')`);
    await evaluate(`[...document.querySelectorAll('#resource-view button')].find(b=>b.textContent==='配置 Webhook').click()`);
    await until(`location.pathname === '/settings/webhooks' && document.querySelector('#webhook-create-form [name=node]')?.value === '@local'`);
    assert.equal(await evaluate("document.getElementById('webhook-create-form').elements.container.value"), 'edge-proxy');
    assert.equal(await evaluate("document.getElementById('webhook-create-form').elements.repository.value"), 'docker.io/library/nginx');
    await evaluate(`var w=document.getElementById('webhook-create-form'); w.elements.repository.value='registry.example.test/team/app'; w.querySelector('details').open=true;`);
    await until("document.querySelector('#webhook-create-form [name=registry_id] option:nth-child(2)')?.textContent.includes('测试仓库')");
    await evaluate(`w.elements.registry_id.selectedIndex=1; w.requestSubmit();`);
    await until("document.getElementById('webhook-credential')");
    const hookURL = await evaluate("document.getElementById('webhook-url').value");
    const hookToken = await evaluate("document.getElementById('webhook-token').value");
    assert.match(hookURL, /\/api\/v1\/webhooks\/containers\/hook-[A-Z2-7]+$/);
    assert.match(hookToken, /^[A-Z2-7]+$/);
    await evaluate(`[...document.querySelectorAll('#settings-list button')].find(b=>b.textContent==='返回 Webhook 列表').click()`);
    await until("document.querySelector('#settings-list tbody')?.textContent.includes('edge-proxy')");
    assert.equal(await evaluate("document.getElementById('webhook-token')"), null);
    await screenshot('settings-webhooks');
    assert.match(await evaluate("document.querySelector('#settings-list tbody').textContent"), /测试仓库/);
    await evaluate(`document.querySelector('#settings-list tbody button').click(); [...document.querySelectorAll('#webhook-revoke-confirmation button')].find(b=>b.textContent==='确认撤销').click()`);
    await until("document.getElementById('settings-list').textContent.includes('暂无容器发布 Webhook')");
    await evaluate(`document.querySelector('#settings-tabs a[href="/settings/audit"]').click()`);
    await until("document.querySelector('#settings-list tbody')?.textContent.includes('吊销容器更新 Webhook')");
    assert.match(await evaluate("document.querySelector('#settings-list tbody tr').textContent"), /吊销容器更新 Webhook.*renamed/);
    await screenshot('settings-audit');
    assert.equal((await evaluate("document.querySelector('#settings-list tbody').textContent")).includes(hookToken), false);
    await evaluate(`history.pushState(null, '', '/node/%40local/containers/' + 'c'.repeat(64)); dispatchEvent(new PopStateEvent('popstate'))`);
    await until("document.querySelector('#configure-container-ci')");
    await evaluate("document.querySelector('#configure-container-ci').click()");
    await until("location.pathname === '/settings/ci' && document.querySelector('#ci-target-form [name=node]')?.value === '@local'");
    assert.equal(await evaluate("document.getElementById('ci-target-form').elements.container.value"),'edge-proxy');
    await evaluate(`var ciTarget=document.getElementById('ci-target-form'); ciTarget.elements.project.value='my-app'; ciTarget.elements.target.value='edge-ci'; ciTarget.requestSubmit()`);
    await until("document.getElementById('ci-token-form')");
    assert.equal(await evaluate(`fetch('/api/v1/ci/targets',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({project:'second-app',target:'remote-ci',node:'test-worker',generation:1,container:'remote-app',repository:'docker.io/library/nginx'})}).then(r=>r.status)`),201);
    await evaluate("document.querySelector('#ci-token-grants').open=true");
    await until("document.querySelector('[data-ci-grant-target=remote-ci]')");
    await evaluate("document.querySelector('[data-ci-grant-target=remote-ci]').click()");
    await evaluate(`var ciTokenForm=document.getElementById('ci-token-form');ciTokenForm.elements.id.value='gitea-edge';ciTokenForm.requestSubmit()`);
    await until("document.querySelector('#ci-token')?.value.startsWith('moori_ci_')");
    assert.match(await evaluate("document.getElementById('ci-gitea-setup').value"), /api\/v1\/container-deployments/);
    assert.equal(await evaluate("document.getElementById('ci-gitea-setup').value.includes(document.getElementById('ci-token').value)"),false);
    const ciFlow = await evaluate(`(async () => {
      const ready=await fetch('/_test/enable-ci-node',{method:'POST'});
      if (!ready.ok) throw new Error('CI fixture could not activate the node');
      const path='/api/v1/container-deployments';
      const token=document.getElementById('ci-token').value;
      const Authorization='Bearer '+token;
      const entries=[];
      for (const [project,target,remote] of [['my-app','edge-ci',false],['second-app','remote-ci',true]]) {
        const submit=await fetch(path,{method:'POST',headers:{Authorization,'Idempotency-Key':'browser-gitea','Content-Type':'application/json'},
          body:JSON.stringify({project,target,image:'docker.io/library/nginx@sha256:'+'a'.repeat(64)})});
        const operation=await submit.json();
        const result=await fetch('/_test/complete-ci/'+operation.id+(remote?'?remote=1':''),{method:'POST'});
        const status=await fetch(path+'/'+operation.id,{headers:{Authorization}}).then(r=>r.json());
        entries.push({id:operation.id,submit:submit.status,result:result.status,state:status.state,agent:status.agent});
      }
      return entries;
    })()`);
    assert.deepEqual(ciFlow.map(x=>[x.submit,x.result,x.state,x.agent]),[[202,204,'succeeded','@local'],[202,204,'succeeded','test-worker']]);
    assert.notEqual(ciFlow[0].id,ciFlow[1].id);
    await evaluate("[...document.querySelectorAll('#settings-list button')].find(b=>b.textContent==='刷新记录').click()");
    await until(`document.getElementById('ci-runs')?.textContent.includes('${ciFlow[0].id}') && document.getElementById('ci-runs')?.textContent.includes('成功')`);
    await evaluate(`document.querySelector('#settings-tabs a[href="/settings/audit"]').click()`);
    await until("!document.getElementById('ci-token')");
    await evaluate(`history.pushState(null, '', '/node/%40local/containers/' + 'c'.repeat(64)); dispatchEvent(new PopStateEvent('popstate'))`);
    await until("document.querySelector('#configure-container-ci')");
    await evaluate("document.querySelector('#configure-container-ci').click()");
    await until("document.getElementById('ci-token-form') && document.getElementById('ci-runs')?.textContent.includes('成功')");
    assert.equal(await evaluate("document.querySelector('#ci-target-form,#ci-token')"),null,'opening the bound container shows its existing target without revealing the token');
    await evaluate("[...document.querySelectorAll('#settings-list button')].find(b=>b.textContent==='撤销').click()");
    await until("document.getElementById('ci-revoke-confirmation')");
    await evaluate("[...document.querySelectorAll('#ci-revoke-confirmation button')].find(b=>b.textContent==='确认撤销').click()");
    await until("document.getElementById('settings-list').textContent.includes('已撤销')");
    await evaluate("listCITargets()");
    await until("document.querySelector('#settings-list tbody')?.textContent.includes('my-app')");
    assert.equal(await evaluate("[...document.querySelectorAll('#settings-list tbody tr')].filter(row=>row.firstElementChild.textContent==='my-app').length"),1,'application groups all its environments');
    await evaluate("[...document.querySelectorAll('#settings-list tbody tr')].find(row=>row.firstElementChild.textContent==='my-app').querySelector('button').click()");
    assert.match(await evaluate("document.getElementById('settings-list').textContent"),/my-production/);
    assert.match(await evaluate("document.getElementById('settings-list').textContent"),/edge-ci/);
    await evaluate("[...document.querySelectorAll('#settings-list button')].find(button=>button.textContent.includes('添加部署环境')).click()");
    await until("document.querySelector('#ci-target-form [name=node] option[value=\"@local\"]')");
    assert.equal(await evaluate("document.getElementById('ci-target-form').elements.project.value"),'my-app');
    await evaluate(`
      var composeCI=document.getElementById('ci-target-form');
      composeCI.elements.kind.value='compose';
      composeCI.elements.kind.dispatchEvent(new Event('change'));
      composeCI.elements.target.value='compose-ci';
      composeCI.elements.node.value='@local';
      composeCI.elements.node.dispatchEvent(new Event('change'));
    `);
    assert.equal(await evaluate("composeCI.elements.container.disabled && composeCI.elements.repository.disabled"),true,'Compose onboarding does not ask for a container or single image repository');
    assert.equal(await evaluate("composeCI.elements.generation.getClientRects().length"),0,'node generation is resolved internally');
    await evaluate("composeCI.requestSubmit()");
    await until("document.getElementById('ci-token-form')");
    assert.equal(await evaluate("document.getElementById('ci-token-form').elements.allow_publish.checked"),true);
    assert.match(await evaluate("document.getElementById('ci-gitea-setup').value"),/api\/v1\/releases/);
    assert.match(await evaluate("document.getElementById('ci-gitea-setup').value"),/--rawfile compose/);
    await evaluate(`
      document.querySelector('select[name=ci_provider]').value='shell';
      document.querySelector('select[name=ci_provider]').dispatchEvent(new Event('change'));
    `);
    assert.match(await evaluate("document.getElementById('ci-gitea-setup').value"),/^# 设置 MOORI_URL/);
    assert.equal(await evaluate("document.getElementById('ci-gitea-setup').value.includes('secrets.MOORI_TOKEN')"),false);
    await evaluate(`var composeToken=document.getElementById('ci-token-form');composeToken.elements.id.value='compose-pipeline';composeToken.requestSubmit()`);
    await until("document.querySelector('#ci-token')?.value.startsWith('moori_ci_')");
    const composePublication = await evaluate(`(async()=>{
      const Authorization='Bearer '+document.getElementById('ci-token').value;
      const headers={Authorization,'Content-Type':'application/json'};
      const body={project:'my-app',target:'compose-ci',id:'browser-compose-v1',
        compose:'services: {web: {image: nginx:build}}',images:{web:'docker.io/library/nginx@sha256:'+'a'.repeat(64)}};
      const publish=await fetch('/api/v1/releases',{method:'POST',headers,body:JSON.stringify(body)});
      const release=await publish.json();
      const deploy=await fetch('/api/v1/deployments',{method:'POST',headers:{...headers,'Idempotency-Key':'browser-compose'},
        body:JSON.stringify({project:body.project,target:body.target,release:body.id})});
      return [publish.status,deploy.status,release.digest];
    })()`);
    assert.deepEqual(composePublication.slice(0,2),[200,202]);
    assert.match(composePublication[2],/^sha256:[a-f0-9]{64}$/);
    await evaluate("[...document.querySelectorAll('#settings-list button')].find(button=>button.textContent==='刷新记录').click()");
    await until("document.getElementById('ci-runs')?.textContent.includes('browser-compose-v1')");
    await evaluate("document.getElementById('ci-gitea-setup').scrollIntoView({block:'center'})");
    await screenshot('ci-compose-onboarding');
    await send('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:true},sessionId);
    assert.equal(await evaluate('document.documentElement.scrollWidth <= innerWidth'),true,'CI onboarding and release records fit a narrow viewport');
    await send('Emulation.setDeviceMetricsOverride',{width:1440,height:900,deviceScaleFactor:1,mobile:false},sessionId);
    await evaluate("listCITargets()");
    await until("document.querySelector('#settings-list tbody')?.textContent.includes('my-app') && !document.getElementById('ci-token')");
    // The exact same request must survive a lost response without duplicating it.
    assert.equal(await evaluate(`fetch('/api/v1/targets',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({project:'my-app',target:'my-production',local:true})}).then(r=>r.status)`), 204);
    await openView('environments');
    await evaluate(`
      var agentTokenFetch = window.fetch;
      var droppedAgentTokenResponse = false;
      window.fetch = async (path, options) => {
        const response = await agentTokenFetch(path, options);
        if (path === '/api/v1/agents' && options?.method === 'POST' && response.ok && !droppedAgentTokenResponse) {
          droppedAgentTokenResponse = true;
          throw new Error('模拟 Agent Token 响应丢失');
        }
        return response;
      };
      document.getElementById('add-remote').click();
      var f = document.getElementById('agent-token-form');
      f.elements.id.value = 'server-2';
      f.requestSubmit();
    `);
    await until(`document.getElementById('notice').textContent.includes('模拟 Agent Token 响应丢失')`);
    await evaluate(`f.requestSubmit()`);
    await until(`!document.getElementById('agent-token-result').hidden`);
    await evaluate(`window.fetch = agentTokenFetch`);
    assert.equal(await evaluate(`document.getElementById('onboarding').hidden`), true);
    const issuedToken = await evaluate(`document.getElementById('agent-token-value').value`);
    assert.match(issuedToken, /^[A-Za-z0-9_-]{43}$/);
    assert.deepEqual(await evaluate(`fetch('/api/v1/projects/my-app/targets').then(r=>r.json()).then(page=>page.items.map(item=>[item.id,item.state || 'compose']))`),[['compose-ci','compose'],['edge-ci','container'],['my-production','compose']]);
    // Inspect generated download data without persisting a test credential.
    await evaluate(`var blobs=[]; URL.createObjectURL=(blob)=>{blobs.push(blob);return 'blob:test'}; HTMLAnchorElement.prototype.click=function(){}; document.getElementById('download-credential').click(); document.getElementById('download-compose').click()`);
    const downloads = await evaluate(`Promise.all(blobs.map(blob=>blob.text()))`);
    assert.match(downloads[0], /^[A-Za-z0-9_-]{43}\n$/);
    assert.equal(downloads[0].trim(), issuedToken);
    assert.ok(downloads[1].includes('moori-agent-data:/data\n') && downloads[1].includes('\nvolumes:\n  moori-agent-data:'));
    assert.ok(downloads[1].includes('MOORI_AGENT_TOKEN:') && downloads[1].includes('MOORI_ENDPOINT:') && !downloads[1].includes(issuedToken));
    await evaluate(`document.getElementById('dismiss-agent-token').click()`);
    assert.equal(await evaluate(`document.getElementById('agent-token-value').value`), '');
    await openView('console');
    await evaluate(`document.getElementById('create-target').click(); var f=document.getElementById('target-form'); f.elements.mode.value='existing'; f.elements.mode.dispatchEvent(new Event('change'))`);
    await until(`document.getElementById('existing-agent').value === 'server-2'`);
    await evaluate(`var f=document.getElementById('target-form'); f.elements.target.value='remote-staging'; f.requestSubmit()`);
    await until(`Array.from(document.getElementById('target').options).some(o=>o.value==='remote-staging')`);
    // Options and preview responses belong to the selected target and generation.
    await evaluate(`
      var regularFetch = window.fetch;
      var finishOldInventory, finishOldPreview, lastTemplateRequest, rejectedTemplateRequest, localInventoryReads = 0;
      window.fetch = (path, options) => {
        if (path === '/api/v1/nodes/%40local/resources') {
          const response = () => Response.json({generation:1,overview:{available:true,volumes:[{name:'local-data',driver:'local'}]}});
          if (++localInventoryReads === 1) return new Promise(resolve => {
            finishOldInventory = () => resolve(response());
          });
          return Promise.resolve(response());
        }
        if (path === '/api/v1/nodes/server-2/resources') {
          return Promise.resolve(Response.json({generation:1,overview:{available:true,
            volumes:[{name:'remote-data',driver:'local'}], networks:[{name:'bridge',driver:'bridge'},{name:'remote-net',driver:'bridge'}]}}));
        }
        if (path === '/api/v1/compose-templates/external-test/versions/1/preview') {
          lastTemplateRequest = JSON.parse(options.body);
          const response = () => Response.json({compose:'{"services":{}}',digest:'compose-digest',snapshot_digest:'snapshot-digest'});
          if (lastTemplateRequest.target === 'my-production') {
            return new Promise(resolve => { finishOldPreview = () => resolve(response()); });
          }
          return Promise.resolve(response());
        }
        if (path === '/api/v1/compose-templates/external-test/versions/1/deploy') {
          rejectedTemplateRequest = JSON.parse(options.body);
          return Promise.resolve(new Response('deployment state or digest conflict',{status:409}));
        }
        return regularFetch(path, options);
      };
      document.getElementById('target').value = 'my-production';
      templateDeployment({id:'external-test',name:'External test'},{version:1,parameters:{volumes:[{key:'data',required:true}],networks:[{key:'edge'}]}});
    `);
    await until("typeof finishOldInventory === 'function'");
    await evaluate(`var tf = document.querySelector('#management-content form');
      tf.elements.target.value='remote-staging'; tf.elements.target.dispatchEvent(new Event('change')); finishOldInventory()`);
    await until(`tf.elements['volume-0'].querySelector('[value=remote-data]')`);
    assert.equal(await evaluate(`tf.elements['volume-0'].tagName`), 'SELECT');
    assert.deepEqual(await evaluate(`Array.from(tf.elements['volume-0'].options).map(option=>option.value)`), ['', 'remote-data']);
    assert.deepEqual(await evaluate(`Array.from(tf.elements['network-0'].options).map(option=>option.value)`), ['', 'remote-net']);
    await evaluate(`tf.elements.target.value='my-production'; tf.elements.target.dispatchEvent(new Event('change'))`);
    await until(`tf.elements['volume-0'].querySelector('[value=local-data]')`);
    await evaluate(`tf.elements['volume-0'].value='local-data'; document.getElementById('template-preview').click()`);
    await until("typeof finishOldPreview === 'function'");
    await evaluate(`tf.elements.target.value='remote-staging'; tf.elements.target.dispatchEvent(new Event('change')); finishOldPreview()`);
    await until(`tf.elements['volume-0'].querySelector('[value=remote-data]')`);
    assert.equal(await evaluate(`tf.elements['volume-0'].value`), '', 'target switch clears the previous selected volume');
    assert.equal(await evaluate(`tf.querySelector('button[type=submit]').disabled`), true, 'late preview must not authorize submission');
    await evaluate(`tf.elements['volume-0'].value='remote-data'; tf.elements['network-0'].value='remote-net'; document.getElementById('template-preview').click()`);
    await until(`!tf.querySelector('button[type=submit]').disabled`);
    assert.deepEqual(await evaluate(`lastTemplateRequest.node`), {agent:'server-2',generation:1});
    assert.deepEqual(await evaluate(`lastTemplateRequest.networks`), [{key:'edge',name:'remote-net'}]);
    await evaluate(`tf.requestSubmit()`);
    await until(`document.getElementById('management-status').textContent.includes('重新校验并预览') && !tf.elements['volume-0'].disabled`);
    assert.equal(await evaluate(`rejectedTemplateRequest.snapshot_digest`), 'snapshot-digest');
    assert.equal(await evaluate(`tf.querySelector('button[type=submit]').disabled`), true, 'changed configuration must require a fresh preview');
    assert.equal(await evaluate(`tf.elements['volume-0'].value`), 'remote-data', 'a same-node refresh retains an available selection');
    await evaluate(`window.fetch = regularFetch; document.getElementById('management-dialog').close()`);
    if (process.env.MOORI_SCREENSHOTS) {
      for (const [path, marker, label] of [
        ['/node/%40local/compose', '#resource-view .resource-section, #resource-view table', 'compose'],
        ['/node/%40local/networks', '#resource-view .resource-section, #resource-view table', 'networks'],
        ['/node/%40local/host', '#resource-view .resource-section', 'host'],
        ['/settings/alerts', '#settings-list h2, #settings-list h3', 'settings-alerts'],
      ]) {
        await evaluate(`history.pushState(null, '', ${JSON.stringify(path)}); dispatchEvent(new PopStateEvent('popstate'))`);
        await until(`location.pathname === ${JSON.stringify(path)} && document.querySelector(${JSON.stringify(marker)})?.getClientRects().length`);
        await screenshot(label);
      }
    }
    await evaluate(`document.getElementById('logout').click()`);
    await until(`!document.getElementById('login-panel').hidden`);
    assert.equal(await evaluate(`document.getElementById('onboarding').hidden && document.getElementById('agent-token-panel').hidden && !document.getElementById('agent-token-value').value`), true);
    await evaluate(`var login=document.getElementById('login-form'); login.elements.username.value='test-operator'; login.elements.password.value='replacement operator password'; login.requestSubmit()`);
    await until("!document.getElementById('console').hidden && document.getElementById('workspace-username').textContent === 'test-operator'");
    assert.equal(await evaluate("document.getElementById('operations-nav').hidden"), true);
    assert.equal(await evaluate("document.getElementById('project-deployments-nav').hidden"),false);
    assert.equal(await evaluate("fetch('/api/v1/users').then(response=>response.status)"), 403);
    console.log('PASS: setup, local and remote resources, monitoring history, dropdowns, user management, webhook creation and revocation, platform audit, safe retry, Agent onboarding, logout cleanup');
  } else {
  await until('document.getElementById("login-panel") && !document.getElementById("login-panel").hidden');
  await evaluate(`document.querySelector('#login-form [name=username]').value='operator'; document.querySelector('#login-form [name=password]').value='initial test password'; document.getElementById('login-form').requestSubmit()`);
  await until(`!document.getElementById('console').hidden && document.getElementById('release').value === 'v1'`);
  assert.equal(await evaluate(`document.getElementById('operations-nav').hidden`), true);
  await evaluate("history.pushState(null, '', '/monitoring'); dispatchEvent(new PopStateEvent('popstate'))");
  await until(`document.querySelector('#console-nav [aria-current=page]').pathname === '/console' && document.getElementById('monitoring').getClientRects().length === 0`);
  await evaluate(`document.querySelector('#deployments button').click()`);
  await until(`!document.getElementById('details').hidden`);
  assert.equal(await evaluate(`!document.getElementById('action-form').hidden && !document.getElementById('cancel-button').hidden`), true, 'needs_attention must offer cancellation');
  await evaluate(`document.querySelector('#action-form [name=note]').value='operator reconciliation'; document.getElementById('cancel-button').click()`);
  await until(`document.getElementById('notice').textContent.includes('取消请求已处理')`);
  await evaluate(`document.getElementById('project').value='broken'; document.getElementById('project').dispatchEvent(new Event('change'))`);
  await until(`document.getElementById('notice').textContent.includes('catalog unavailable')`);
  assert.equal(await evaluate(`document.getElementById('deployments').children.length`), 0, 'failed project switch must clear old rows');
  assert.equal(await evaluate(`document.getElementById('target').options.length + document.getElementById('release').options.length`), 0, 'failed project switch must clear old selections');
  assert.equal(await evaluate(`document.querySelector('#deploy-form button[type=submit]').disabled`), true, 'unloaded project must disable deployment submission');
  await evaluate(`document.getElementById('logout').click()`);
  await until(`!document.getElementById('login-panel').hidden && document.getElementById('console').hidden`);
  console.log('PASS: login 204, project catalog, needs_attention cancellation, failed project isolation, logout 204');
  }
} finally {
  const graceful = socket?.readyState === WebSocket.OPEN;
  if (graceful) {
    await Promise.race([send('Browser.close').catch(() => {}), delay(1000)]);
    socket.close();
  }
  try {
    await removeChromiumProfile(child, exited, profile, graceful);
  } finally {
    child.stderr.destroy();
    // Bound the DevTools socket's final close handshake after profile cleanup.
    setTimeout(() => process.exit(process.exitCode ?? 0), 1200).unref();
  }
}
