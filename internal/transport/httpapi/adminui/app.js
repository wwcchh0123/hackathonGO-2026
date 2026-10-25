const $ = (id) => document.getElementById(id);
let devices = [];
let currentDevice = '';
let currentTask = '';

async function request(path, options = {}) {
  const response = await fetch(path, { cache: 'no-store', ...options });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
  return data;
}
function showError(error) { $('error').hidden = !error; $('error').textContent = error || ''; }
function fmt(value) { return value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—'; }
function statusLabel(status) { return ({ running: '运行中', idle: '空闲', rescheduling: '排队中', terminated: '已终止' })[status] || status || '未知'; }
function renderDevices() {
  const list = $('device-list');
  list.replaceChildren();
  $('device-count').textContent = `${devices.length} 台`;
  for (const device of devices) {
    const button = document.createElement('button');
    button.className = `device-card ${device.device_id === currentDevice ? 'selected' : ''}`;
    const title = document.createElement('strong'); title.textContent = device.device_id;
    const state = document.createElement('span'); state.className = device.online ? 'online' : 'offline'; state.textContent = device.online ? '在线' : '未触达 / 已离线';
    const meta = document.createElement('small'); meta.textContent = `最近触达 ${fmt(device.last_seen)} · ${device.last_seen_source || '启动登记'}`;
    const counts = document.createElement('small'); counts.textContent = device.tasks_error ? `任务查询失败：${device.tasks_error}` : `任务 ${device.task_counts.total}${device.task_counts.has_more ? '+' : ''} · 运行中 ${device.task_counts.running}`;
    button.append(title, state, meta, counts); button.onclick = () => selectDevice(device.device_id); list.append(button);
  }
  if (!devices.length) { const empty = document.createElement('p'); empty.className = 'empty'; empty.textContent = '尚未发现设备。'; list.append(empty); }
}
async function selectDevice(id) {
  currentDevice = id; currentTask = ''; renderDevices(); $('task-title').textContent = `任务 · ${id}`; $('detail').replaceChildren();
  try {
    const result = await request(`/api/admin/devices/${encodeURIComponent(id)}/tasks?limit=50&include_archived=true`);
    renderTasks(result.tasks.data || []);
  } catch (error) { showError(error.message); }
}
function renderTasks(tasks) {
  const list = $('task-list'); list.replaceChildren(); $('task-count').textContent = `${tasks.length} 条`;
  for (const task of tasks) {
    const button = document.createElement('button'); button.className = `task-row ${task.task_ref === currentTask ? 'selected' : ''}`;
    const title = document.createElement('strong'); title.textContent = task.title || '未命名任务';
    const status = document.createElement('span'); status.textContent = statusLabel(task.status);
    const time = document.createElement('small'); time.textContent = fmt(task.updated_at || task.created_at);
    button.append(title, status, time); button.onclick = () => loadDetail(task.task_ref); list.append(button);
  }
  if (!tasks.length) { const empty = document.createElement('p'); empty.className = 'empty'; empty.textContent = '该设备暂无任务。'; list.append(empty); }
}
async function loadDetail(ref) {
  currentTask = ref; renderDevices();
  try {
    const result = await request(`/api/admin/devices/${encodeURIComponent(currentDevice)}/tasks/${encodeURIComponent(ref)}?events_limit=30`);
    const task = result.task; const detail = $('detail'); detail.replaceChildren();
    const h = document.createElement('h3'); h.textContent = task.title || '未命名任务';
    const info = document.createElement('p'); info.className = 'detail-meta'; info.textContent = `${statusLabel(task.status)} · ${task.session_id || '尚未创建云端会话'} · 停止原因：${task.stop_reason || '未知'}`;
    const summary = document.createElement('pre'); summary.textContent = task.summary?.text || '暂无摘要';
    const command = document.createElement('p'); command.className = 'command'; command.textContent = task.command ? `命令 ${task.command.command_id} · ${task.command.state}/${task.command.phase}${task.command.error ? ` · ${task.command.error}` : ''}` : '暂无本地命令记录';
    const events = document.createElement('div'); events.className = 'events';
    for (const raw of task.events || []) { const pre = document.createElement('pre'); try { pre.textContent = JSON.stringify(JSON.parse(raw), null, 2); } catch { pre.textContent = raw; } events.append(pre); }
    detail.append(h, info, command, summary, events);
  } catch (error) { showError(error.message); }
}
async function load() {
  try {
    const result = await request('/api/admin/devices'); devices = result.data || []; $('queried').textContent = `查询于 ${fmt(result.queried_at)}`; renderDevices();
    if (!currentDevice && devices[0]) currentDevice = devices[0].device_id;
    if (currentDevice) await selectDevice(currentDevice);
    showError('');
  } catch (error) { showError(error.message); }
}
async function boot() {
  try { const config = await request('/api/admin/bootstrap'); $('mode').textContent = config.service; $('app').hidden = false; $('login').hidden = true; await load(); }
  catch (error) { $('login-error').textContent = error.message; $('login').hidden = false; }
}
$('refresh').onclick = () => load();
$('connect').onsubmit = async (event) => { event.preventDefault(); try { await request('/api/connect', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token: $('token').value }) }); await boot(); } catch (error) { $('login-error').textContent = error.message; } };
void boot();
