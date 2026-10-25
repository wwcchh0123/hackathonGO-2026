const $ = (id) => document.getElementById(id);
let view = 'attention';
let query = '';
let selectedDevice = '';
let selectedTask = null;
let dashboard = null;

async function request(path, options = {}) {
  const response = await fetch(path, { cache: 'no-store', ...options });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.message || data.error || `HTTP ${response.status}`);
  return data;
}
function showError(error) { $('error').hidden = !error; $('error').textContent = error || ''; }
function fmt(value) { return value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—'; }
function statusLabel(status) { return ({ running: '运行中', idle: '空闲', rescheduling: '排队中', terminated: '已终止' })[status] || status || '未知'; }
function healthLabel(device) { return device.attention?.length ? '需要处理' : device.online ? '在线' : device.last_seen ? '离线' : '未触达'; }
function appendText(parent, tag, text, className = '') { const node = document.createElement(tag); node.textContent = text; if (className) node.className = className; parent.append(node); return node; }
function makeButton(className, children, handler) { const button = document.createElement('button'); button.className = className; children.forEach((child) => button.append(child)); button.onclick = handler; return button; }

function renderStats() {
  const stats = $('stats'); stats.replaceChildren();
  if (!dashboard) return;
  for (const item of [['待处理设备', dashboard.attention_devices, 'attention'], ['在线设备', dashboard.online_count, 'online'], ['运行中任务', dashboard.running_tasks, 'running'], ['待处理任务', dashboard.attention_tasks, 'attention']]) {
    const card = document.createElement('div'); card.className = `stat ${item[2]}`; appendText(card, 'small', item[0]); appendText(card, 'strong', String(item[1])); stats.append(card);
  }
}
function renderNav() {
  document.querySelectorAll('[data-view]').forEach((button) => button.classList.toggle('active', button.dataset.view === view));
  $('status-filter').hidden = view !== 'tasks';
}
function renderAttention() {
  const pane = $('list-pane'); pane.replaceChildren();
  const devices = dashboard?.devices || []; const tasks = dashboard?.tasks || [];
  appendText(pane, 'h2', '待处理', 'pane-title'); appendText(pane, 'p', `${devices.length} 台设备 · ${tasks.length} 个任务`, 'pane-subtitle');
  if (!devices.length && !tasks.length) { appendText(pane, 'p', '当前没有需要处理的设备或任务。', 'empty'); return; }
  if (devices.length) {
    appendText(pane, 'h3', '设备');
    for (const device of devices) pane.append(deviceButton(device));
  }
  if (tasks.length) {
    appendText(pane, 'h3', '任务');
    for (const task of tasks) pane.append(taskButton(task));
  }
}
function deviceButton(device) {
  const title = document.createElement('strong'); title.textContent = device.device_id;
  const status = document.createElement('span'); status.textContent = healthLabel(device); status.className = device.online ? 'online' : 'attention';
  const meta = document.createElement('small'); meta.textContent = `${device.last_error || device.attention?.join(' · ') || '无错误'} · 最近触达 ${fmt(device.last_seen)}`;
  return makeButton(`row ${selectedDevice === device.device_id ? 'selected' : ''}`, [title, status, meta], () => loadDevice(device.device_id));
}
function taskButton(task) {
  const title = document.createElement('strong'); title.textContent = task.title || '未命名任务';
  const status = document.createElement('span'); status.textContent = `${task.device_id} · ${task.runtime || ""} ${task.model?.id || ""} · ${statusLabel(task.status)}`; status.className = task.attention?.length ? 'attention' : 'online';
  const meta = document.createElement('small'); meta.textContent = `${task.attention?.join(' · ') || '无错误'} · 更新于 ${fmt(task.updated_at)}`;
  return makeButton(`row ${selectedTask?.ref === task.task_ref && selectedTask?.device_id === task.device_id ? 'selected' : ''}`, [title, status, meta], () => loadTask(task.device_id, task.task_ref));
}
async function renderDevices() {
  const pane = $('list-pane'); pane.replaceChildren(); appendText(pane, 'h2', '设备', 'pane-title');
  try {
    const result = await request(`/api/admin/devices?limit=50&sort=attention&query=${encodeURIComponent(query)}`);
    appendText(pane, 'p', `${result.data.length}${result.has_more ? '+' : ''} 台 · 关注优先`, 'pane-subtitle');
    for (const device of result.data) pane.append(deviceButton(device));
    if (!result.data.length) appendText(pane, 'p', '没有匹配的设备。', 'empty');
  } catch (error) { showError(error.message); }
}
async function renderTasks() {
  const pane = $('list-pane'); pane.replaceChildren(); appendText(pane, 'h2', '任务', 'pane-title');
  try {
    const status = $('status').value;
    const result = await request(`/api/admin/tasks?limit=50&query=${encodeURIComponent(query)}&status=${encodeURIComponent(status)}`);
    appendText(pane, 'p', `${result.data.length}${result.has_more ? '+' : ''} 条 · 关注优先`, 'pane-subtitle');
    for (const task of result.data) pane.append(taskButton(task));
    if (!result.data.length) appendText(pane, 'p', '没有匹配的任务。', 'empty');
  } catch (error) { showError(error.message); }
}
function render() { renderStats(); renderNav(); if (view === 'attention') renderAttention(); else if (view === 'devices') void renderDevices(); else void renderTasks(); }

async function loadDevice(id) {
  selectedDevice = id; selectedTask = null; view = 'devices'; renderNav();
  try {
    const [deviceResult, tasksResult] = await Promise.all([request(`/api/admin/devices/${encodeURIComponent(id)}`), request(`/api/admin/devices/${encodeURIComponent(id)}/tasks?limit=50&include_archived=true`)]);
    const device = deviceResult.device; const detail = $('detail'); detail.replaceChildren();
    appendText(detail, 'h2', device.device_id); appendText(detail, 'p', `${healthLabel(device)} · 最近触达 ${fmt(device.last_seen)} · ${device.last_seen_source || '启动登记'}`, 'detail-meta');
    appendText(detail, 'h3', '当前快照'); const snapshot = document.createElement('pre'); snapshot.textContent = JSON.stringify(device.snapshot, null, 2); detail.append(snapshot);
    appendText(detail, 'h3', `任务历史 · ${tasksResult.tasks.data.length} 条`); for (const task of tasksResult.tasks.data) detail.append(taskButton({ ...task, device_id: id }));
    showError('');
  } catch (error) { showError(error.message); }
}
async function loadTask(deviceId, ref) {
  selectedDevice = deviceId; selectedTask = { device_id: deviceId, ref }; view = 'tasks'; renderNav();
  const detail = $('detail'); detail.replaceChildren(); appendText(detail, 'p', '正在读取任务…', 'empty');
  try {
    const result = await request(`/api/admin/tasks/${encodeURIComponent(deviceId)}/${encodeURIComponent(ref)}?events_limit=30`);
    if (selectedTask?.ref !== ref || selectedTask?.device_id !== deviceId) return;
    const task = result.task; detail.replaceChildren();
    appendText(detail, 'h2', task.title || '未命名任务'); appendText(detail, 'p', `设备 ${deviceId} · ${statusLabel(task.status)} · ${task.runtime || ''} ${task.model?.id || ''} ${task.model?.effort || ''} · 停止原因：${task.stop_reason || '未知'}`, 'detail-meta');
    if (task.command) appendText(detail, 'p', `命令 ${task.command.command_id} · ${task.command.state}/${task.command.phase}${task.command.error ? ` · ${task.command.error}` : ''}`, 'command');
    const summary = document.createElement('pre'); summary.textContent = task.summary?.text || '暂无摘要'; detail.append(summary); appendText(detail, 'h3', `原始事件 · ${(task.events || []).length} 条`);
    for (const raw of task.events || []) { const event = document.createElement('pre'); try { event.textContent = JSON.stringify(typeof raw === 'string' ? JSON.parse(raw) : raw, null, 2); } catch { event.textContent = String(raw); } detail.append(event); }
    const back = makeButton('link-button', [document.createTextNode('查看所属设备')], () => loadDevice(deviceId)); detail.prepend(back); showError('');
  } catch (error) {
    if (selectedTask?.ref !== ref || selectedTask?.device_id !== deviceId) return;
    detail.replaceChildren(); appendText(detail, 'p', '任务详情读取失败，请重试。', 'empty'); showError(error.message);
  }
}
async function loadDashboard() { try { dashboard = await request(`/api/admin/overview?query=${encodeURIComponent(query)}`); $('queried').textContent = `查询于 ${fmt(dashboard.queried_at)}`; render(); showError(''); } catch (error) { showError(error.message); } }
async function boot() { try { const config = await request('/api/admin/bootstrap'); $('mode').textContent = config.service; $('app').hidden = false; $('login').hidden = true; await loadDashboard(); } catch (error) { $('login-error').textContent = error.message; $('login').hidden = false; } }

$('refresh').onclick = () => loadDashboard();
$('search-form').onsubmit = (event) => { event.preventDefault(); query = $('query').value.trim(); if (view === 'attention') void loadDashboard(); else render(); };
$('status').onchange = () => { if (view === 'tasks') void renderTasks(); };
document.querySelectorAll('[data-view]').forEach((button) => { button.onclick = () => { view = button.dataset.view; selectedTask = null; render(); }; });
$('connect').onsubmit = async (event) => { event.preventDefault(); try { await request('/api/connect', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token: $('token').value }) }); await boot(); } catch (error) { $('login-error').textContent = error.message; } };
void boot();
