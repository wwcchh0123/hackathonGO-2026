// Development-only observer and tool-call simulator. No hardware stack is chosen.
const $ = (id) => document.getElementById(id);
const statuses = {
  ready: '准备就绪',
  creating: '准备工作空间',
  sending: '正在提交需求',
  running: '正在创造',
  idle: '等待指令',
  terminated: '会话终止',
  error: '需要处理',
};
const views = {
  timeline: '执行时间线',
  code: '代码与工具',
  terminal: '终端输出',
  preview: '作品预览',
};
const names = {
  'session.created': '工作空间已创建',
  'user.message': '收到你的需求',
  'session.status_running': '开始执行',
  'agent.message': '编程搭档',
  'agent.tool_use': '工具调用',
  'agent.tool_result': '执行结果',
  'user.interrupt': '收到暂停指令',
  'session.status_idle': '等待下一步',
  'session.error': '执行错误',
};
let snapshot = null;
let source = null;
let events = [];
let seen = new Set();
let previewURL = '';
let currentView = 'timeline';
let socket = null;
let reconnect = null;
let stopped = false;
let connected = false;
let streamConnected = false;
let nextPage = '';
const tasks = new Map();
async function loadTasks(more = false) {
  const page = await request(`/api/tasks?limit=50${more && nextPage ? '&page=' + encodeURIComponent(nextPage) : ''}`);
  if (!more) tasks.clear();
  for (const task of page.data) tasks.set(task.task_ref, task);
  while (tasks.size > 256) tasks.delete(tasks.keys().next().value);
  for (const id of ['view-task', 'command-task']) {
    const select = $(id), selected = id === 'view-task' ? (snapshot?.task_ref || select.value) : select.value;
    select.replaceChildren(new Option('请选择任务', ''));
    for (const task of tasks.values()) select.add(new Option(`${task.title || '未命名任务'} · ${task.runtime || ''} ${task.model?.id || ''} · ${task.status}`, task.task_ref));
    select.value = selected;
  }
  nextPage = page.next_page || '';
  $('more-tasks').hidden = !page.has_more;
}
$('refresh-tasks').onclick = () => loadTasks().catch((e) => { $('receipt').textContent = e.message; });
$('more-tasks').onclick = () => loadTasks(true).catch((e) => { $('receipt').textContent = e.message; });
$('view-task').onchange = async () => {
  try { snapshot = await request('/api/device/view', { task_ref: $('view-task').value }); subscribe(snapshot.task_ref); render(); }
  catch (error) { $('receipt').textContent = error.message; }
};

async function request(path, body) {
  const response = await fetch(
    path,
    body === undefined
      ? { cache: 'no-store' }
      : {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        },
  );
  const data = await response.json();
  if (!response.ok) throw new Error(data.message || data.error || `HTTP ${response.status}`);
  return data;
}
function renderEvents() {
  const view = currentView;
  const visible = events.filter((e) =>
    view === 'code'
      ? e.type.includes('tool')
      : view === 'terminal'
        ? e.type.includes('tool') || e.type === 'session.error'
        : true,
  );
  const container = $('events');
  container.className = `events ${view}`;
  container.replaceChildren();
  if (!visible.length) {
    const empty = document.createElement('div');
    empty.className = 'empty';
    const icon = document.createElement('span');
    icon.className = 'orbit';
    icon.textContent = '✳';
    const title = document.createElement('h2');
    title.textContent = events.length ? '等待新的执行记录' : '从一句话开始创造';
    const text = document.createElement('p');
    text.textContent = '提交需求后，执行过程会实时出现在这里。';
    empty.append(icon, title, text);
    container.append(empty);
  }
  for (const event of visible) {
    const article = document.createElement('article');
    article.className = `event ${event.type === 'agent.message' ? 'message' : ''}`;
    const head = document.createElement('div');
    head.className = 'event-head';
    const dot = document.createElement('span');
    dot.className = 'event-dot';
    const title = document.createElement('strong');
    title.textContent =
      (names[event.type] || event.type) +
      (event.name ? ` · ${event.name}` : '');
    const time = document.createElement('time');
    time.textContent = event.created_at
      ? new Date(event.created_at).toLocaleTimeString('zh-CN', {
          hour12: false,
        })
      : '';
    head.append(dot, title, time);
    const body = document.createElement('pre');
    body.textContent =
      (Array.isArray(event.content) &&
        event.content
          .filter((c) => c.type === 'text')
          .map((c) => c.text)
          .join('\n')) ||
      JSON.stringify(event.input || event, null, 2);
    article.append(head, body);
    container.append(article);
  }
  $('count').textContent = snapshot?.session_id
    ? `${events.length} 条事件 · ${streamConnected ? '实时同步' : '正在恢复事件连接'}`
    : '等待任务';
}
function render() {
  if (!snapshot) return;
  $('prompt').textContent = snapshot.prompt || '你的下一件作品，从这里开始。';
  $('summary').textContent = snapshot.summary;
  $('status').className = `status ${snapshot.status}`;
  $('status-text').textContent = statuses[snapshot.status] || snapshot.status;
  $('error').hidden = !snapshot.error;
  $('error').textContent = snapshot.error || '';
  $('view-name').textContent = views[currentView];
  $('session').textContent =
    `${snapshot.device_id}${snapshot.session_id ? ' / ' + snapshot.session_id : ''}`;
  $('connection').textContent = connected ? '设备通道已连接' : '正在恢复连接';
  $('connection-dot').className = connected ? 'online' : '';
  const preview = currentView === 'preview' && snapshot.result_url;
  $('events').hidden = Boolean(preview);
  $('preview').hidden = !preview;
  if (preview && previewURL !== snapshot.result_url) {
    previewURL = snapshot.result_url;
    $('result-frame').src = previewURL;
    $('result-link').href = previewURL;
  }
  if (!snapshot.result_url && previewURL) {
    previewURL = '';
    $('result-frame').removeAttribute('src');
    $('result-link').removeAttribute('href');
  }
  $('show-preview').disabled = !snapshot.task_ref || (snapshot.status !== 'idle' && !snapshot.result_url);
  renderEvents();
}
function subscribe(taskRef) {
  source?.close();
  source = null;
  events = [];
  seen = new Set();
  streamConnected = false;
  if (!taskRef) return;
  source = new EventSource(
    `/api/tasks/${encodeURIComponent(taskRef)}/events`,
  );
  source.onopen = () => {
    streamConnected = true;
    renderEvents();
  };
  source.onerror = () => {
    streamConnected = false;
    renderEvents();
    if (source?.readyState === EventSource.CLOSED) setTimeout(() => { if (!stopped && snapshot?.task_ref === taskRef) subscribe(taskRef); }, 1500);
  };
  source.addEventListener('stream_error', () => { if (!stopped && snapshot?.task_ref === taskRef) subscribe(taskRef); });
  source.addEventListener('agent_event', (message) => {
    try {
      const event = JSON.parse(message.data);
      if (typeof event.type !== 'string' || (event.id && seen.has(event.id)))
        return;
      if (event.id) seen.add(event.id);
      events.push(event);
      while (events.length > 512) { const old = events.shift(); if (old.id) seen.delete(old.id); }
      renderEvents();
    } catch (error) {
      console.error('Invalid provider event', error);
    }
  });
}
function connectChannel() {
  socket = new WebSocket(
    `${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/api/device/channel`,
  );
  socket.onopen = () => {
    connected = true;
    render();
  };
  socket.onmessage = (message) => {
    try {
      const next = JSON.parse(message.data);
      if (!next.device_id || typeof next.revision !== 'number') return;
      const previousID = snapshot?.task_ref;
      if (next.task_ref !== previousID || (snapshot?.result_url && !next.result_url))
        currentView = 'timeline';
      if (next.result_url && next.result_url !== snapshot?.result_url)
        currentView = 'preview';
      snapshot = next;
      if (next.task_ref !== previousID) subscribe(next.task_ref);
      render();
    } catch (error) {
      console.error('Invalid device snapshot', error);
    }
  };
  socket.onerror = () => socket?.close();
  socket.onclose = () => {
    connected = false;
    render();
    if (!stopped) reconnect = setTimeout(connectChannel, 1500);
  };
}
async function boot() {
  try {
    const config = await request('/api/bootstrap');
    $('app').hidden = false;
    $('login').hidden = true;
    $('mode').textContent =
      config.mode === 'mock' ? '模拟模式' : '云端 AgentAPI';
    const simulator = location.pathname === '/simulator' && config.dev_tools;
    $('simulator').hidden = !simulator;
    $('sim-link').hidden = simulator || !config.dev_tools;
    $('layout').classList.toggle('with-simulator', simulator);
    connectChannel();
    await loadTasks();
  } catch (error) {
    $('login').hidden = false;
    $('login-error').textContent = error.message;
  }
}
$('connect').onsubmit = async (event) => {
  event.preventDefault();
  try {
    await request('/api/connect', { token: $('token').value });
    await boot();
  } catch (error) {
    $('login-error').textContent = error.message;
  }
};
document.querySelectorAll('[data-action],[data-view]').forEach((button) => {
  button.onclick = async () => {
    if (button.dataset.view) {
      if (button.dataset.view === 'preview' && snapshot?.task_ref && !snapshot.result_url) {
        try { const result = await request(`/api/tasks/${encodeURIComponent(snapshot.task_ref)}/preview`, {}); snapshot.result_url = result.result_url; }
        catch (error) { $('receipt').textContent = error.message; return; }
      }
      currentView = button.dataset.view;
      render();
      return;
    }
    const command = {
      action: button.dataset.action,
      prompt: $('instruction').value,
      task_ref: button.dataset.action === 'message' ? $('command-task').value : undefined,
    };
    button.disabled = true;
    try {
      const receipt = await request('/api/commands', command);
      $('receipt').textContent = receipt.message;
      // A creation reference is usable immediately, before it appears in the cloud list.
      for (const id of ['command-task', 'view-task']) {
        if (![...$(id).options].some((o) => o.value === receipt.task_ref)) $(id).add(new Option('新建任务（已受理）', receipt.task_ref));
      }
      $('command-task').value = receipt.task_ref;
    } catch (error) {
      $('receipt').textContent = error.message;
    } finally {
      button.disabled = false;
      render();
    }
  };
});
addEventListener('beforeunload', () => {
  stopped = true;
  clearTimeout(reconnect);
  socket?.close();
  source?.close();
});
void boot();
