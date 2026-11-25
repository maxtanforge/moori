'use strict';

import {element} from '/api.js';
const expectedTimestamp = time => new Intl.DateTimeFormat(undefined,{
  year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit',hourCycle:'h23'
}).format(new Date(time));
const points = [
  {at: '2026-01-01T00:15:00Z', cpu_percent: 12, memory_bytes: 512, rx_bytes_sec: 0.0085},
  {at: '2026-01-01T00:16:00Z', cpu_percent: 13, memory_bytes: 768},
  {at: '2026-01-01T00:45:00Z', cpu_percent: 61, memory_bytes: 1024, disk_used_bytes: 900 * 2**30, read_bytes_sec: 982 * 2**20, rx_bytes_sec: 0.125, tx_bytes_sec: 0}
];
const range = {from: '2026-01-01T00:00:00Z', until: '2026-01-01T01:00:00Z', step_seconds: 60};
const containerID = 'a'.repeat(64);
const requestPaths = [];
let finishOldHistory;
let oldHistorySignal;
let monitoringEnabled = true;
let refreshTick;
const nativeTimeout = window.setTimeout.bind(window);
const nativeClear = window.clearTimeout.bind(window);
window.setTimeout = (callback,delay,...args) => {
  if (delay === 30000) {
    refreshTick = callback;
    return -1;
  }
  return nativeTimeout(callback,delay,...args);
};
window.clearTimeout = id => {
  if (id === -1) refreshTick = undefined;
  else nativeClear(id);
};

async function monitorResponse(path, signal) {
  requestPaths.push(path);
  const {pathname, searchParams} = new URL(path, location.origin);
  if (pathname.endsWith('/alerts')) return {generation: 1, items: [
    {id: 1, code: 'agent_offline', state: 'fired', at: points[0].at},
    {id: 2, code: 'agent_offline', state: 'recovered', at: points[2].at}
  ]};
  if (pathname.endsWith('/healthy/history')) {
    const container = searchParams.get('container');
    const history = {...range, generation: 1, points: container ? [{...points[2], cpu_percent: 150,
      rx_bytes_sec: 1259, tx_bytes_sec: 1048576}] : points,
      containers: [{id: containerID, name: '<b>demo</b>'}],
      events: [{at: points[1].at, kind: 'deployment_succeeded', label: 'version <script>'}]};
    if (searchParams.get('range') === '7d') {
      oldHistorySignal = signal;
      return new Promise(resolve => { finishOldHistory = () => resolve(history); });
    }
    return history;
  }
  return {...range, generation:1, points:[], events:[], containers:[]};
}
const nativeFetch = window.fetch.bind(window);
let browserError;
window.addEventListener('unhandledrejection',event => { browserError = event.reason?.stack || String(event.reason); });
window.fetch = async (path, options) => {
  if (path === '/api/v1/auth/setup') return Response.json({required:true});
  if (typeof path !== 'string' || !path.startsWith('/api/v1/monitor/')) return nativeFetch(path,options);
  if (!monitoringEnabled && !path.endsWith('/alerts')) return new Response('Monitoring unavailable',{status:503});
  return Response.json(await monitorResponse(path, options?.signal));
};

function check(condition, message) {
  if (!condition) throw new Error(message);
}
async function until(condition, message) {
  for (let i = 0; i < 100; i++) {
    if (condition()) return;
    await new Promise(resolve => setTimeout(resolve, 30));
  }
  throw new Error(message);
}
async function run() {
  await import('/monitoring.js');
  await until(() => !element('setup-panel').hidden, 'Console boot did not finish');
  check(typeof renderMonitorRoute === 'function', 'Route-scoped monitoring entry point is missing');
  element('console').hidden = false;
  element('monitoring').parentElement.hidden = false;
  check(requestPaths.length === 0 && !refreshTick, 'Inactive monitoring issued a request');
  let timeAxisLabels;
  const charts = [];
  const Chart = window.uPlot;
  window.uPlot = new Proxy(Chart, {
    construct(target, args) {
      timeAxisLabels = args[0].axes[0].values;
      const plot = Reflect.construct(target, args);
      charts.push(plot);
      return plot;
    }
  });
  renderMonitorRoute({id:'healthy',category:'overview'});
  await until(() => document.querySelector('#monitor-charts canvas') && refreshTick, 'CPU chart and 30-second refresh did not initialize')
    .catch(error => { throw new Error(error.message + ': ' + (browserError || element('monitor-history-status').textContent)); });
  check(requestPaths.some(path => path.endsWith('/healthy/history?range=1h')), 'Node monitoring did not directly query its history');
  window.uPlot = Chart;
  for (const metric of ['网络速率','磁盘','磁盘 I/O']) {
    const plot = charts.find(item => item.root.parentElement.dataset.metric === metric);
    const {width,height} = plot;
    const ctx = plot.ctx;
    const drawText = ctx.fillText;
    let ticks;
    ctx.fillText = function(label,x,...rest) {
      if (this.textAlign === 'right') {
        ticks.push({label, left:x - this.measureText(label).actualBoundingBoxLeft});
      }
      drawText.call(this,label,x,...rest);
    };
    try {
      for (const chartWidth of [640,280]) {
        ticks = [];
        plot.setSize({width:chartWidth,height});
        plot.redraw();
        await new Promise(requestAnimationFrame);
        check(ticks.length > 0, metric + ' did not draw Y-axis ticks');
        for (const {label,left} of ticks) {
          check(left >= 0, metric + ' clipped "' + label + '" at x=' + left + ' on a ' + chartWidth + 'px chart');
        }
      }
    } finally {
      ctx.fillText = drawText;
      plot.setSize({width,height});
    }
  }
  const midnight = new Date(2026, 0, 2).getTime() / 1000;
  for (const step of [60, 1]) {
    const ticks = [midnight - step, midnight];
    const clock = new Intl.DateTimeFormat(undefined, {
      hour:'2-digit', minute:'2-digit', ...(step < 60 ? {second:'2-digit'} : {}), hourCycle:'h23'
    });
    const labels = timeAxisLabels(null, ticks);
    check(ticks.every((tick, i) => labels[i] === clock.format(tick * 1000)),
      'Time axis should show a single 24-hour clock label across midnight');
  }
  const cpu = element('monitor-charts').querySelector('figure');
  const networkChart = () => [...element('monitor-charts').querySelectorAll('figure')]
    .find(chart => chart.querySelector('h4').textContent === '网络速率');
  const network = networkChart();
  check(network.querySelector('.monitor-readout').textContent.includes('接收 0.13 B/s · 发送 0 B/s'), 'Fractional and zero network rates were not distinguished');
  network.querySelector('.monitor-plot').dispatchEvent(new KeyboardEvent('keydown', {key:'Home'}));
  check(network.querySelector('.monitor-readout').textContent.includes('接收 <0.01 B/s · 发送 —'), 'Very small traffic or missing network samples lost their meaning');
  check(cpu.querySelector('.monitor-plot').dataset.segments === '2', 'Chart connected separate telemetry intervals');
  check(cpu.querySelector('.monitor-plot').dataset.eventMarkers === '1', 'Deployment marker is missing');
  check(element('monitor-events').textContent.includes('version <script>'), 'Timeline lost the literal event label');
  check(element('monitor-alerts').textContent.includes('已触发') && element('monitor-alerts').textContent.includes('已恢复'), 'Alert history is incomplete');
  check(element('monitor-last-sample').textContent.includes(expectedTimestamp(points[2].at)), 'Last sample used a refresh time rather than the original observation');
  const holder = cpu.querySelector('.monitor-plot');
  holder.dispatchEvent(new KeyboardEvent('keydown', {key: 'Home'}));
  check(cpu.textContent.includes(expectedTimestamp(points[0].at)), 'Chart keyboard exploration has no timestamp');
  const bounds = holder.getBoundingClientRect();
  cpu.querySelector('.u-over').dispatchEvent(new MouseEvent('mousemove', {clientX: bounds.left + bounds.width / 2}));
  check(cpu.textContent.includes('无采样'), 'Pointer exploration reported telemetry inside a gap');

  const over = cpu.querySelector('.u-over');
  over.scrollIntoView({block:'center',inline:'center'});
  await new Promise(resolve => requestAnimationFrame(resolve));
  const plotBounds = over.getBoundingClientRect();
  const midpoint = plotBounds.top + plotBounds.height / 2;
  const target = document.elementFromPoint(plotBounds.left + plotBounds.width * 0.25,midpoint);
  check(target === over || over.contains(target), 'Drag target was ' + target?.className + ' at ' + JSON.stringify(plotBounds));
  const mouseEvents = [];
  for (const name of ['mousedown','mousemove','mouseup']) over.addEventListener(name,event => mouseEvents.push([name,event.clientX,event.movementX,event.buttons]));
  const drag = await nativeFetch('/drag',{method:'POST',body:JSON.stringify([
    plotBounds.left + plotBounds.width * 0.25, plotBounds.left + plotBounds.width * 0.65, midpoint,
  ])});
  check(drag.ok, 'Browser drag failed: ' + (await drag.text()));
  await until(() => holder.dataset.zoomed === '1', 'Drag selection did not zoom the chart: ' + JSON.stringify(mouseEvents) + ' · ' + browserError);
  element('monitor-refresh').click();
  await until(() => !holder.isConnected, 'Refresh did not replace the chart');
  const refreshedHolder = element('monitor-charts').querySelector('.monitor-plot');
  await until(() => refreshedHolder.dataset.zoomed === '1', 'Refresh lost the selected zoom');
  refreshedHolder.dispatchEvent(new MouseEvent('dblclick', {bubbles: true}));
  await until(() => refreshedHolder.dataset.zoomed === '0', 'Double-click did not reset the zoom');

  const rangeControl = element('monitor-range');
  rangeControl.value = '6h';
  rangeControl.dispatchEvent(new Event('change'));
  await until(() => requestPaths.some(path => path.endsWith('range=6h')) && refreshTick, 'Range did not update history');
  const auto = element('monitor-auto');
  check(auto.checked, 'Auto refresh is disabled by default');
  auto.click();
  check(!auto.checked && !refreshTick, 'Pause left a scheduled refresh');
  const count = requestPaths.filter(path => path.includes('/history?')).length;
  element('monitor-refresh').click();
  await until(() => requestPaths.filter(path => path.includes('/history?')).length === count + 1 && !element('monitor-refresh').disabled,
    'Manual refresh failed while paused');
  check(!refreshTick && rangeControl.value === '6h', 'Manual refresh resumed polling or changed the range');

  renderMonitorRoute({id:'healthy',category:'containers',container:containerID});
  await until(() => element('monitor-charts').textContent.includes('150.0%'), 'Container CPU history did not load');
  check(networkChart().querySelector('.monitor-readout').textContent.includes('接收 1.23 KiB/s · 发送 1 MiB/s'), 'Container network rates lost precision or units');
  check(requestPaths.some(path => path.includes('range=6h&container=' + containerID)), 'Container request lost the range or full identity');
  check(element('monitor-object').textContent.includes('<b>demo</b>') && !element('monitor-object').firstElementChild,
    'Container name was interpreted as markup');
  check(element('monitor-back').pathname === '/node/healthy/containers/' + containerID, 'Container monitor cannot return to its details');
  auto.click();
  check(auto.checked && refreshTick, 'Resuming monitoring did not schedule refresh');
  refreshTick();
  await until(() => requestPaths.filter(path => path.includes('container=' + containerID)).length === 2 && refreshTick,
    'Automatic refresh lost the current container');

  rangeControl.value = '7d';
  rangeControl.dispatchEvent(new Event('change'));
  await until(() => finishOldHistory, 'Earlier request was never started');
  renderMonitorRoute({id:'stale',category:'overview'});
  await until(() => element('monitor-history-status').textContent.includes('暂无采样'), 'Empty node history did not render');
  check(oldHistorySignal?.aborted, 'Changing monitored objects did not abort the earlier request');
  finishOldHistory();
  await new Promise(resolve => setTimeout(resolve, 50));
  check(!element('monitor-charts').querySelector('canvas'), 'Late history overwrote the selected node');
  renderMonitorRoute(null);
  check(element('monitoring').hidden && !refreshTick, 'Leaving the page did not stop polling');
  monitorSignedOut();
  check(rangeControl.value === '1h' && auto.checked, 'Sign-out retained monitoring preferences');

  monitoringEnabled = false;
  renderMonitorRoute({id:'healthy',category:'overview'});
  await until(() => element('monitor-history-status').textContent.includes('不可用'), 'Optional monitoring did not report disabled storage');
  check(!element('monitor-charts').children.length, 'Disabled monitoring left earlier metrics visible');
  monitorSignedOut();
}
run().then(() => 'PASS', error => error.stack).then(body => nativeFetch('/result', {method:'POST',body}));
