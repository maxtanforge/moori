import {routeHref,workspaceText} from './ui-components.js';
import {nodePath,nodeRoute} from './node-workspace.js';
import {element,enc} from './api.js';
import {accountEpoch} from './app.js';

// Container terminal page: an xterm.js terminal bridged to an interactive
// docker exec session on the node. The control plane pairs the browser
// socket with the agent's exec relay: binary frames carry terminal bytes,
// text frames carry the handshake and resize / eof control messages.

let containerTerminal;

export function stopContainerTerminal() {
  if (!containerTerminal) return;
  containerTerminal.stop();
  containerTerminal = undefined;
  element('container-terminal-reconnect')?.remove();
  const status = element('container-terminal-status');
  status.textContent = '';
  delete status.dataset.state;
  const output = element('container-terminal-output');
  output.replaceChildren();
  output.hidden = true;
}

export function renderContainerTerminal(node,target,name) {
  element('container-terminal-list').href = routeHref(nodePath(node.id,'containers'));
  const back = element('container-terminal-back');
  const destination = nodeRoute?.returnPath || nodePath(node.id,'containers',target);
  back.href = routeHref(destination);
  const fromList = destination === nodePath(node.id,'containers');
  back.textContent = fromList ? '返回容器列表' : '返回容器详情';
  element('container-terminal-list').hidden = fromList;
  back.previousElementSibling.hidden = fromList;
  element('container-terminal-title').textContent = name;
  const key = [node.id,target,node.generation].join('/');
  if (containerTerminal?.key === key) return;
  stopContainerTerminal();

  const status = element('container-terminal-status');
  status.dataset.state = 'waiting';
  if (!node.generation) {
    status.textContent = '正在读取节点信息…';
    return;
  }
  const output = element('container-terminal-output');
  output.hidden = false;

  const term = new Terminal({
    cursorBlink:true, scrollback:4000, fontSize:13,
    fontFamily:'ui-monospace,SFMono-Regular,Menlo,Consolas,monospace',
    theme:{background:'#10151c',foreground:'#dce3ea',cursor:'#6fb28f',selectionBackground:'#2c4a63'}
  });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(output);
  try { fit.fit(); } catch { /* the panel may still be laid out */ }

  const version = accountEpoch;
  let socket;
  let observer;
  let ended = false;
  let stopped = false;
  // Ready-frame receipt distinguishes "never connected" from "dropped":
  // a failed handshake closes the socket before any frame arrives.
  let connected = false;
  const encoder = new TextEncoder();
  const decoder = new TextDecoder();
  const reconnect = workspaceText('button','重新连接');
  reconnect.type = 'button';
  reconnect.id = 'container-terminal-reconnect';
  reconnect.hidden = true;
  reconnect.addEventListener('click',() => {
    stopContainerTerminal();
    renderContainerTerminal(node,target,name);
  });
  element('container-terminal-exit').before(reconnect);

  function finish(message,state) {
    if (stopped || ended) return;
    ended = true;
    status.dataset.state = state;
    status.textContent = message;
    reconnect.hidden = false;
    reconnect.focus({preventScroll:true});
  }

  function stop() {
    if (stopped) return;
    stopped = true;
    observer?.disconnect();
    socket?.close();
    term.dispose();
  }

  // Sizes follow the panel; the daemon resizes the remote TTY so full
  // screen editors behave. A failed resize never interrupts the session.
  function sendResize() {
    if (socket?.readyState !== 1 || ended) return;
    try { fit.fit(); } catch { return; }
    if (term.cols >= 2 && term.rows >= 2) {
      socket.send(JSON.stringify({resize:{cols:term.cols,rows:term.rows}}));
    }
  }

  const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const cols = Math.min(Math.max(term.cols,2),1024);
  const rows = Math.min(Math.max(term.rows,2),512);
  const params = new URLSearchParams({generation:node.generation,cols:String(cols),rows:String(rows)});
  const path = protocol + '//' + location.host + '/api/v1/nodes/' + enc(node.id) +
    '/containers/' + enc(target) + '/terminal?' + params;
  status.textContent = '正在连接容器终端…';
  socket = new WebSocket(path);
  socket.binaryType = 'arraybuffer';

  socket.onmessage = event => {
    if (stopped || ended) return;
    if (event.data instanceof ArrayBuffer) {
      term.write(decoder.decode(event.data,{stream:true}));
      return;
    }
    let frame;
    try { frame = JSON.parse(event.data); } catch { return; }
    if (frame.ready) {
      connected = true;
      status.dataset.state = 'live';
      status.textContent = '已连接';
      sendResize();
      term.focus();
    } else if (frame.error) {
      output.hidden = true;
      finish(frame.error,'failed');
    } else if (frame.eof) {
      const exitCode = frame.exit_code ?? 0;
      const code = Number.isInteger(exitCode) && exitCode >= 0 ? ' · 退出码 ' + exitCode : '';
      finish('会话已结束' + code,'ended');
    }
  };

  socket.onclose = () => {
    if (stopped) return;
    if (ended) return;
    output.hidden = !connected;
    finish(connected ? '终端连接已断开。' : '无法连接容器终端，请检查节点与容器状态。','failed');
  };

  term.onData(data => {
    if (socket?.readyState === 1 && !ended) socket.send(encoder.encode(data));
  });

  observer = new ResizeObserver(sendResize);
  observer.observe(output);

  containerTerminal = {key,stop};
}

element('container-terminal-exit').addEventListener('click', () => {
  stopContainerTerminal();
  element('container-terminal-back').click();
});
window.addEventListener('pagehide',stopContainerTerminal);
