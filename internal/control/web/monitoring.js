import {routeHref,percent,formatBytes,dateTime} from './ui-components.js';
import {workspaceNodeName,nodePath} from './node-workspace.js';
import {element,enc,request} from './api.js';

(() => {
  const api = '/api/v1/monitor/nodes';
  const chartHeight = 185;
  const timeFields = {hour:'2-digit',minute:'2-digit',hourCycle:'h23'};
  const calendarFields = {year:'numeric',month:'2-digit',day:'2-digit'};
  const chartTime = new Intl.DateTimeFormat(undefined,{...timeFields,second:'2-digit'});
  const chartMinute = new Intl.DateTimeFormat(undefined,timeFields);
  const chartTimestamp = new Intl.DateTimeFormat(undefined,{...calendarFields,...timeFields,second:'2-digit'});
  let plots = [];
  const zoomState = new Map();
  const readingPositions = new Map();
  const alertLabels = {agent_offline: '节点离线', cpu_high: 'CPU 使用率过高', memory_high: '内存使用率过高', disk_low: '磁盘空间不足'};
  const eventLabels = {
    deployment_started: '部署开始', deployment_succeeded: '部署成功', deployment_failed: '部署失败',
    rollback_started: '回滚开始', rollback_succeeded: '回滚成功', rollback_failed: '回滚失败',
    container_restart: '重启容器'
  };
  const cpu = {title: 'CPU 使用率', series: [['cpu_percent', 'CPU']], format: value => percent(value), percent: true, floor: 5};
  const memory = {title: '内存', series: [['memory_bytes', '使用'], ['memory_limit_bytes', '上限']], format: value => formatBytes(value)};
  const disk = {title: '磁盘', series: [['disk_used_bytes', '使用'], ['disk_total_bytes', '总容量']], format: value => formatBytes(value)};
  const network = {title: '网络速率', series: [['rx_bytes_sec', '接收'], ['tx_bytes_sec', '发送']], format: formatNetworkRate};
  const io = {title: '磁盘 I/O', series: [['read_bytes_sec', '读'], ['write_bytes_sec', '写']], format: formatRate};

  let selection;
  let containerName = '';
  let alertsSignature = '';
  let pending;
  let timer;

  function make(tag, content = '', className = '') {
    const node = document.createElement(tag);
    node.textContent = content;
    if (className) node.className = className;
    return node;
  }

  function formatRate(value) { return formatBytes(value) + '/s'; }
  function formatNetworkRate(value) {
    const units = ['B/s','KiB/s','MiB/s','GiB/s','TiB/s','PiB/s'];
    if (value > 0 && value < 0.01) return '<0.01 B/s';
    let unit = 0;
    while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++; }
    return Number(value.toFixed(2)) + ' ' + units[unit];
  }
  function alertName(code) { return Object.hasOwn(alertLabels, code) ? alertLabels[code] : code; }
  function eventName(kind) { return Object.hasOwn(eventLabels, kind) ? eventLabels[kind] : '节点操作'; }

  function stateLabel(label,tone) { return make('span',label,'monitor-state ' + tone); }

  function listEmpty(list, message) {
    list.replaceChildren(make('li', message));
  }

  function renderEvents(events) {
    const list = element('monitor-events');
    const rows = events.map(event => {
      const row = make('li', eventName(event.kind) + (event.label ? ' · ' + event.label : ''));
      const time = make('time', dateTime(event.at));
      time.dateTime = event.at;
      row.append(time);
      return row;
    });
    if (rows.length) list.replaceChildren(...rows);
    else listEmpty(list, '此时间范围暂无事件。');
  }

  function renderAlerts(response) {
    const items = (response.items || []).slice().sort((a, b) => Date.parse(b.at) - Date.parse(a.at) || b.id - a.id);
    const signature = JSON.stringify(items);
    if (signature === alertsSignature) return;
    alertsSignature = signature;
    const list = element('monitor-alerts');
    if (!items.length) {
      listEmpty(list, '暂无告警通知。');
      return;
    }
    list.replaceChildren(...items.map(alert => {
      const row = make('li');
      const recovered = alert.state === 'recovered';
      const time = make('time', dateTime(alert.at));
      time.dateTime = alert.at;
      row.append(alertName(alert.code), ' ', stateLabel(recovered ? '已恢复' : '已触发', recovered ? 'good' : 'bad'), time);
      return row;
    }));
  }

  function formatChartTime(value) {
    return Number.isFinite(value) ? chartTimestamp.format(value) : '—';
  }

  function chartTicks(_plot,ticks) {
    const clock = ticks.length > 1 && ticks[1] - ticks[0] < 60 ? chartTime : chartMinute;
    return ticks.map(value => clock.format(value * 1000));
  }

  function renderHistory(data) {
    const events = data.events || [];
    renderEvents(events);
    const from = Date.parse(data.from);
    const until = Date.parse(data.until);
    const points = (data.points || []).map(point => ({...point, time: Date.parse(point.at)}))
      .filter(point => point.time >= from && point.time <= until).sort((a, b) => a.time - b.time);
    const gap = Math.max(45000, Number(data.step_seconds || 30) * 1500);
    const metrics = selection.container ? [cpu, memory, network, io] : [cpu, memory, disk, network, io];
    destroyPlots();
    const focusedMetric = document.activeElement?.dataset?.metric;
    const charts = metrics.filter(metric => metric.series.some(([key]) => points.some(point => Number.isFinite(point[key]))))
      .map(metric => makeChart(metric, points, from, until, gap, events)).filter(Boolean);
    element('monitor-charts').replaceChildren(...charts);
    fitPlots();
    // The 30-second rebuild must not yank keyboard focus off a chart the
    // operator is stepping through.
    if (focusedMetric) {
      element('monitor-charts').querySelector(`[data-metric="${CSS.escape(focusedMetric)}"]`)?.focus({preventScroll: true});
    }
    const latest = points.at(-1);
    element('monitor-last-sample').textContent = '最近采样：' + formatChartTime(latest?.time);
    const status = element('monitor-history-status');
    const label = charts.length ? '拖拽缩放曲线，双击复位。移到曲线或用方向键查看采样，断开处表示采样中断。' : '此时间范围暂无采样。';
    if (status.textContent !== label) status.textContent = label;
  }

  // Categorical series pair, validated per surface mode with the design-system
  // palette checker (light on #ffffff, dark on #172728: lightness band, chroma
  // floor, CVD separation, contrast). Dark steps are selected for a dark
  // surface, not flipped light steps. All other chrome wears design tokens.
  const chartPalettes = {
    light: {series: ['#00855f', '#7958ae']},
    dark: {series: ['#0fae8c', '#8464d0']},
  };
  function chartPalette() {
    const style = getComputedStyle(document.documentElement);
    const pick = (name, fallback) => style.getPropertyValue(name).trim() || fallback;
    const hex = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(pick('--surface', '#ffffff'));
    const digits = hex[1].length === 3 ? hex[1].split('').map(ch => ch + ch).join('') : hex[1];
    const channel = index => parseInt(digits.slice(index, index + 2), 16) / 255;
    const luminance = digits ? 0.2126 * channel(0) + 0.7152 * channel(2) + 0.0722 * channel(4) : 1;
    const dark = luminance < 0.4;
    return {...chartPalettes[dark ? 'dark' : 'light'],
      grid: pick('--chart-grid', dark ? '#213738' : '#e7efec'),
      line: pick('--line', '#dbe6e3'),
      text: pick('--muted', '#496265'),
      marker: pick('--muted', '#496265'),
      restart: pick('--warn', '#8a5308'),
    };
  }

  function eventMarkerPlugin(events, fromSec, untilSec, palette, holder) {
    return {hooks: {draw(u) {
      const ctx = u.ctx;
      ctx.save();
      ctx.lineWidth = 1;
      ctx.setLineDash([4, 3]);
      let drawn = 0;
      for (const event of events) {
        const at = Date.parse(event.at) / 1000;
        if (!(at >= fromSec && at <= untilSec)) continue;
        const x = u.valToPos(at, 'x', true);
        if (x < u.bbox.left || x > u.bbox.left + u.bbox.width) continue;
        ctx.strokeStyle = event.kind === 'container_restart' ? palette.restart : palette.marker;
        ctx.beginPath();
        ctx.moveTo(x, u.bbox.top);
        ctx.lineTo(x, u.bbox.top + u.bbox.height);
        ctx.stroke();
        drawn++;
      }
      ctx.restore();
      holder.dataset.eventMarkers = String(drawn);
    }}};
  }

  const niceSteps = [1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10];
  function niceCeil(value) {
    const power = 10 ** Math.floor(Math.log10(value));
    const step = niceSteps.find(step => step * power >= value);
    return (step ?? 10) * power;
  }
  // Bottom-anchored y range with ~8% headroom above the data peak, so the 2px
  // stroke and hover dots never clip against the plot frame. The previous
  // range pinned the axis top to the exact data maximum, which cut every
  // peak off at the top edge. Percent metrics keep a 100 cap.
  function yRange(metric, _u, _dataMin, dataMax) {
    const peak = Math.max(metric.floor ?? 0, dataMax || 0);
    if (peak <= 0) return [0, 1];
    let top = niceCeil(peak * 1.08);
    // Percent metrics cap at 100 while the cap still covers the peak; container
    // CPU legitimately exceeds 100 on multi-core hosts (aggregated per core).
    if (metric.percent && top > 100 && peak < 100) top = 100;
    if (top <= peak) top = Math.ceil(peak) + 1;
    return [0, top];
  }

  function makeChart(metric, points, from, until, gap, events) {
    const series = metric.series.filter(([key]) => points.some(point => Number.isFinite(point[key])));
    const available = points.filter(point => series.some(([key]) => Number.isFinite(point[key])));
    if (!series.length || !available.length) return null;
    const chartPoints = [];
    for (const point of points) {
      const previous = chartPoints.at(-1);
      if (previous && point.time - previous.time > gap) chartPoints.push({time:(previous.time + point.time) / 2});
      chartPoints.push(point);
    }
    const palette = chartPalette();
    const chart = make('figure', '', 'monitor-chart');
    chart.append(make('h4', metric.title));
    const holder = make('div', '', 'monitor-plot');
    holder.dataset.zoomed = '0';
    holder.tabIndex = 0;
    holder.setAttribute('role', 'group');
    holder.setAttribute('aria-label', metric.title + '时间序列。使用左右方向键查看采样。');
    holder.dataset.metric = metric.title;
    // The readout doubles as the series legend: names in muted ink, a colored
    // dot carrying identity (single-series charts skip the dot — the title
    // already names the series). uPlot's built-in legend stays hidden so
    // value text never wears a series color.
    const output = make('p', '', 'monitor-readout');
    output.setAttribute('aria-live', 'polite');
    const stamp = make('span', '', 'monitor-readout-time');
    const missing = make('span', '无采样', 'monitor-readout-missing');
    const readings = series.map(([key, name], index) => {
      const item = make('span', '', 'monitor-readout-item');
      if (series.length > 1) {
        const dot = make('i', '', 'monitor-readout-dot');
        dot.style.background = palette.series[index] ?? palette.series.at(-1);
        item.append(dot);
      }
      const value = make('span', '—');
      item.append(name + ' ', value);
      return {key, value, item};
    });
    // Text keeps the original "时间 · 系列 值" phrasing (screen readers and the
    // browser fixture read textContent); the separators are visually hidden so
    // the flex row shows clean spacing instead of stacked dots.
    const parts = [stamp, make('span', ' · ', 'monitor-readout-sep'), missing];
    for (const reading of readings) parts.push(make('span', ' · ', 'monitor-readout-sep'), reading.item);
    output.append(...parts);
    chart.append(holder, output);
    // Split a series wherever a sample is missing or the distance to the
    // previous one exceeds the continuity threshold, so sampling
    // interruptions stay visible as line breaks and lone points.
    const values = series.map(([key]) => chartPoints.map(point => Number.isFinite(point[key]) ? point[key] : null));
    // Carry the keyboard reading position across the 30-second rebuild so an
    // operator stepping through samples does not jump back to the latest one.
    let position = Math.min(readingPositions.get(metric.title) ?? available.length - 1, available.length - 1);
    let segments = 0;
    for (let i = 0, drawing = false; i < values[0].length; i++) {
      if (values[0][i] === null) drawing = false;
      else if (!drawing) { drawing = true; segments++; }
    }
    holder.dataset.segments = String(segments);
    const zoom = zoomState.get(metric.title);
    const plot = new uPlot({
      width: holder.clientWidth || 640,
      height: chartHeight,
      cursor: {drag: {x: true, y: false}},
      scales: {
        x: {time: true, range: (_plot,min,max) => [min ?? from / 1000,max ?? until / 1000]},
        y: {range: (u, dataMin, dataMax) => yRange(metric, u, dataMin, dataMax)},
      },
      series: [
        {value: (_plot,value) => formatChartTime(value == null ? undefined : value * 1000)},
        ...series.map(([key, name], index) => ({
          label: name,
          stroke: palette.series[index] ?? palette.series.at(-1),
          width: 2,
          spanGaps: false,
          // Larger auto points keep lone samples at sampling gaps visible.
          points: {size: 7},
        })),
      ],
      axes: [
        {stroke: palette.text, ticks: {stroke: palette.line, size: 4}, grid: {stroke: palette.grid, width: 1},
          border: {show: true, stroke: palette.line}, values: chartTicks, size: 30},
        {stroke: palette.text, ticks: {stroke: palette.line, size: 4}, grid: {stroke: palette.grid, width: 1},
          border: {show: false}, values: (u, ticks) => ticks.map(value => metric.format(value))},
      ],
      legend: {show: false},
      plugins: [eventMarkerPlugin(events, from / 1000, until / 1000, palette, holder),
        {hooks: {setCursor(u) {
          if (u.cursor.idx == null) {
            position = available.length - 1;
            read(available[position]);
            return;
          }
          const point = chartPoints[u.cursor.idx];
          if (!point || u.cursor.left == null) return;
          const hover = u.posToVal(u.cursor.left, 'x') * 1000;
          if (Math.abs(point.time - hover) > gap / 2) {
            read(undefined, hover);
            return;
          }
          const index = available.indexOf(point);
          if (index >= 0) {
            position = index;
            read(available[index], point.time);
          } else {
            read(undefined, point.time);
          }
        }}},{hooks: {setScale(u, key) {
          if (key !== 'x') return;
          const {min, max} = u.scales.x;
          const full = Math.abs(min - from / 1000) < 1 && Math.abs(max - until / 1000) < 1;
          if (full) zoomState.delete(metric.title);
          else zoomState.set(metric.title, {min, max});
          holder.dataset.zoomed = full ? '0' : '1';
        }}}],
      }, [chartPoints.map(point => point.time / 1000), ...values], holder);

    function read(point, time) {
      stamp.textContent = formatChartTime(point?.time ?? time);
      missing.hidden = Boolean(point);
      for (const {key, value} of readings) {
        const current = point?.[key];
        value.textContent = Number.isFinite(current) ? metric.format(current) : '—';
      }
    }
    holder.addEventListener('keydown', event => {
      if (!['Home', 'End', 'ArrowLeft', 'ArrowRight'].includes(event.key)) return;
      event.preventDefault();
      // Keyboard exploration stays inside the zoomed window: stepping onto a
      // sample outside it would extrapolate the cursor past the plot frame and
      // desync the readout from the internal position.
      const visible = [];
      for (let index = 0; index < available.length; index++) {
        const at = available[index].time / 1000;
        if (at >= plot.scales.x.min && at <= plot.scales.x.max) visible.push(index);
      }
      if (!visible.length) return;
      let cursor = visible.indexOf(position);
      if (cursor < 0) cursor = event.key === 'ArrowLeft' ? visible.length : -1;
      let next;
      if (event.key === 'Home') next = 0;
      else if (event.key === 'End') next = visible.length - 1;
      else next = Math.max(0, Math.min(visible.length - 1, cursor + (event.key === 'ArrowLeft' ? -1 : 1)));
      position = visible[next];
      readingPositions.set(metric.title, position);
      const point = available[position];
      plot.setCursor({left: plot.valToPos(point.time / 1000, 'x', false),
        top: (plot.bbox.top + plot.bbox.height / 2) / (window.devicePixelRatio || 1)});
      read(point, point.time);
    });
    read(available[position]);
    // Re-apply a kept zoom after the 30-second rebuild so polling does not
    // reset the user's window; switching object or range clears it instead.
    plot.setScale('x', zoom || {min: from / 1000, max: until / 1000});
    holder.addEventListener('dblclick', () => {
      plot.setScale('x', {min: from / 1000, max: until / 1000});
    });
    plots.push(plot);
    return chart;
  }

  function destroyPlots() {
    for (const plot of plots) plot.destroy();
    plots = [];
  }

  function fitPlots() {
    for (const plot of plots) {
      const width = plot.root.parentElement.clientWidth;
      if (width > 0) plot.setSize({width,height:chartHeight});
    }
  }

  function clearHistory() {
    destroyPlots();
    zoomState.clear();
    readingPositions.clear();
    for (const id of ['monitor-charts','monitor-events']) element(id).replaceChildren();
    element('monitor-last-sample').textContent = '最近采样：—';
  }

  window.updateMonitorIdentity = (id,overview) => {
    if (selection?.id !== id) return;
    const container = selection.container;
    const item = container && (overview?.containers || []).find(item => item.full_id === container);
    if (item?.name) containerName = item.name;
    element('monitor-object').textContent = container ? containerName || container.slice(0,12) : workspaceNodeName(id);
    element('monitor-object-context').textContent = container ? workspaceNodeName(id) + ' · ' + container.slice(0,12) : '';
  };

  function scheduleRefresh() {
    clearTimeout(timer);
    if (selection && !pending && element('monitor-auto').checked && !document.hidden) {
      timer = setTimeout(() => { void loadHistory(); },30000);
    }
  }

  async function loadHistory(reset = false) {
    if (!selection) return;
    clearTimeout(timer);
    pending?.abort();
    const controller = new AbortController();
    pending = controller;
    const signal = AbortSignal.any([controller.signal,AbortSignal.timeout(15000)]);
    const status = element('monitor-history-status');
    const refresh = element('monitor-refresh');
    refresh.disabled = true;
    if (reset) {
      clearHistory();
      status.textContent = '正在加载历史数据…';
    }
    const {id,container} = selection;
    const path = api + '/' + enc(id);
    const params = new URLSearchParams({range:element('monitor-range').value});
    if (container) params.set('container',container);
    try {
      const [history,alerts] = await Promise.allSettled([
        request(path + '/history?' + params,undefined,undefined,signal),
        request(path + '/alerts',undefined,undefined,signal)
      ]);
      if (pending !== controller) return;
      if (history.status === 'rejected') {
        status.textContent = history.reason?.status === 503 ? '节点监控未启用或不可用。' : '无法读取监控历史，请重试。';
        return;
      }
      const data = history.value;
      const item = container && (data.containers || []).find(item => item.id === container);
      if (item?.name) containerName = item.name;
      updateMonitorIdentity(id);
      renderHistory(data);
      if (alerts.status === 'fulfilled' && alerts.value.generation === data.generation) {
        renderAlerts(alerts.value);
      } else {
        alertsSignature = '';
        listEmpty(element('monitor-alerts'),'无法读取节点告警，请重试。');
      }
    } finally {
      if (pending === controller) {
        pending = undefined;
        refresh.disabled = false;
        scheduleRefresh();
      }
    }
  }

  window.renderMonitorRoute = (route,overview) => {
    if (selection?.id === route?.id && selection?.container === route?.container) {
      if (route) updateMonitorIdentity(route.id,overview);
      return;
    }
    clearTimeout(timer);
    pending?.abort();
    pending = undefined;
    selection = route;
    containerName = '';
    alertsSignature = '';
    clearHistory();
    element('monitor-alerts').replaceChildren();
    element('monitor-history-status').textContent = '';
    element('monitoring').hidden = !route;
    if (!route) {
      element('monitor-object').textContent = '';
      element('monitor-object-context').textContent = '';
      return;
    }
    const back = element('monitor-back');
    const {id,container} = route;
    element('monitor-title').textContent = container ? '容器监控' : '节点监控';
    const destination = route.returnPath || (container ? nodePath(id,'containers',container) : nodePath(id));
    back.textContent = container ? (destination === nodePath(id,'containers') ? '返回容器列表' : '返回容器详情') : '返回节点概览';
    back.href = routeHref(destination);
    updateMonitorIdentity(id,overview);
    void loadHistory(true);
  };

  window.monitorSignedOut = () => {
    renderMonitorRoute(null);
    element('monitor-range').value = '1h';
    element('monitor-auto').checked = true;
  };

  document.getElementById('monitor-refresh').addEventListener('click',() => void loadHistory());
  document.getElementById('monitor-range').addEventListener('change',() => void loadHistory(true));
  document.getElementById('monitor-auto').addEventListener('change',scheduleRefresh);
  document.addEventListener('visibilitychange',scheduleRefresh);
  let resizeTimer;
  window.addEventListener('resize',() => {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(fitPlots,150);
  });
})();
