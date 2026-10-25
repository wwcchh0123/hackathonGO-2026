// Dependency-free kiosk renderer for the simulated Linxi device.
//
// The server exposes raw AgentAPI events over SSE. This page deliberately
// performs a presentation pass before rendering them so transport details do
// not become the user interface: status events update the header, thinking
// becomes a compact disclosure row, and tool calls/results become a single
// CodeAgent-style work block.
const $ = (id) => document.getElementById(id);
const query = new URLSearchParams(window.location.search);
const state = {
  taskRef: query.get('task_ref') || '',
  selectedTask: null,
  sessions: new Map(),
  events: [],
  seen: new Set(),
  source: null,
  listBusy: false,
  status: 'ready',
  stopReason: 'unknown',
  total: 0,
  expanded: new Set(),
  lastRenderAt: 0,
  previewURL: '',
  previewBusy: false,
  previewAttempts: 0,
  previewTimer: null,
};

function esc(value = '') {
  return String(value)
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#039;');
}

function short(value, length = 180) {
  return String(value || '').replace(/\s+/g, ' ').trim().slice(0, length);
}

function timeOf(value) {
  if (!value) return '--:--:--';
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? '--:--:--' : date.toLocaleTimeString('zh-CN', { hour12: false });
}

function ageOf(value) {
  if (!value) return 'unknown time';
  const date = new Date(value);
  const seconds = Math.max(0, Math.floor((Date.now() - date.valueOf()) / 1000));
  if (!Number.isFinite(seconds)) return 'unknown time';
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return date.toLocaleDateString('zh-CN', { month: 'short', day: 'numeric' });
}

function textOf(event) {
  if (typeof event?.text === 'string') return event.text.trim();
  if (typeof event?.output === 'string') return event.output.trim();
  if (typeof event?.content === 'string') return event.content.trim();
  if (Array.isArray(event?.content)) {
    return event.content
      .filter((part) => part && typeof part.text === 'string')
      .map((part) => part.text)
      .join('\n')
      .trim();
  }
  if (Array.isArray(event?.message?.content)) {
    return event.message.content
      .filter((part) => part && typeof part.text === 'string')
      .map((part) => part.text)
      .join('\n')
      .trim();
  }
  return '';
}

function textHtml(value) {
  return esc(value)
    .replace(/```([\s\S]*?)```/g, '<pre>$1</pre>')
    .replace(/`([^`\n]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>')
    .replace(/\n\n+/g, '</p><p>')
    .replace(/\n/g, '<br>');
}

function messageHtml(value) {
  const lines = String(value || '').split('\n');
  const blocks = [];
  let paragraph = [];
  let list = [];
  const flushParagraph = () => {
    if (!paragraph.length) return;
    blocks.push(`<p>${textHtml(paragraph.join('\n'))}</p>`);
    paragraph = [];
  };
  const flushList = () => {
    if (!list.length) return;
    blocks.push(`<ul>${list.map((item) => `<li>${textHtml(item)}</li>`).join('')}</ul>`);
    list = [];
  };
  for (const line of lines) {
    const match = line.match(/^\s*[-*]\s+(.+)$/);
    if (match) {
      flushParagraph();
      list.push(match[1]);
    } else if (!line.trim()) {
      flushParagraph();
      flushList();
    } else {
      flushList();
      paragraph.push(line);
    }
  }
  flushParagraph();
  flushList();
  return blocks.join('');
}

function parseInput(event) {
  if (event?.input && typeof event.input === 'object') return event.input;
  if (typeof event?.input_json === 'string') {
    try {
      const input = JSON.parse(event.input_json);
      return input && typeof input === 'object' ? input : {};
    } catch {
      return {};
    }
  }
  return {};
}

function toolName(event) {
  return String(event?.name || event?.tool_name || event?.tool_type || 'tool');
}

function toolTarget(event) {
  const input = parseInput(event);
  return short(input.command || input.file_path || input.path || input.description || input.query || input.url || '', 110);
}

function runtimeLabel(task) {
  const runtime = ({claudecode: 'Claude Code', codex: 'Codex', native: 'Native'})[task?.runtime] || task?.runtime || 'Agent runtime';
  return [runtime, task?.model?.id, task?.model?.effort].filter(Boolean).join(' · ');
}

function taskTitle(task) {
  return short(task?.title, 72) || `Session ${String(task?.task_ref || '').slice(-8)}`;
}

function taskStatus(status) {
  return ({ running: 'RUNNING', rescheduling: 'QUEUED', idle: 'IDLE', terminated: 'STOPPED', creating: 'CREATING' })[String(status || '')] || String(status || 'UNKNOWN').toUpperCase();
}

function taskStatusClass(status) {
  if (status === 'running' || status === 'rescheduling' || status === 'creating') return 'running';
  if (status === 'terminated' || status === 'failed') return 'error';
  return '';
}

function statusLabel(status) {
  if (status === 'running' || status === 'rescheduling') return 'Working';
  if (status === 'creating') return 'Creating';
  if (status === 'idle') return ({ end_turn: 'Turn ended', interrupted: 'Paused', requires_action: 'Needs input', retries_exhausted: 'Retries exhausted', budget_reached: 'Budget reached' })[state.stopReason] || 'Idle';
  if (status === 'terminated' || status === 'failed') return 'Stopped';
  return 'Ready';
}

function setStatus(status, stopReason) {
  state.status = status || 'ready';
  const running = state.status === 'running' || state.status === 'rescheduling';
  if (state.status !== 'idle') state.stopReason = 'unknown';
  else if (stopReason !== undefined) state.stopReason = (typeof stopReason === 'string' ? stopReason : stopReason?.type) || 'unknown';
  const done = state.status === 'idle' && state.stopReason === 'end_turn';
  const failed = state.status === 'idle' && ['retries_exhausted', 'budget_reached'].includes(state.stopReason);
  if (running) clearPreview();
  $('status').textContent = statusLabel(state.status).toUpperCase();
  $('status-pill').className = `status-pill ${running ? 'live' : done ? 'done' : failed ? 'error' : ''}`;
  $('run-summary-label').textContent = statusLabel(state.status);
}

function updateURL(taskRef) {
  const next = new URL(window.location.href);
  if (taskRef) next.searchParams.set('task_ref', taskRef);
  else next.searchParams.delete('task_ref');
  window.history.replaceState({}, '', next);
}

function setPreviewShell(message = 'Checking exported files…') {
  $('preview-pane').hidden = false;
  $('preview-frame').hidden = true;
  $('preview-placeholder').hidden = false;
  $('preview-status').textContent = message;
}

function previewSource(url) {
  try {
    const result = new URL(url, window.location.href);
    // The result is always published by this control service. Loading the
    // same path from the page's current origin avoids a second ngrok
    // interstitial in desktop browsers, while the device still requests the
    // exact same path through its already-bypassed public display page.
    if (!/^\/results\/[0-9a-f]{32}\/(?:.*\.html)?$/i.test(result.pathname)) return '';
    const current = new URL(window.location.href);
    result.protocol = current.protocol;
    result.host = current.host;
    result.searchParams.delete('_linxi_raw');
    result.searchParams.delete('_linxi_channel');
    return result.href;
  } catch {
    return '';
  }
}

function showPreview(url) {
  if (!url || state.status !== 'idle') return;
  const source = previewSource(url);
  if (!source) return;
  state.previewURL = url;
  $('preview-pane').hidden = false;
  $('preview-frame').hidden = false;
  $('preview-placeholder').hidden = true;
  $('preview-status').textContent = 'index.html · ready to explore';
  if ($('preview-frame').src !== source) $('preview-frame').src = source;
}

function clearPreview() {
  if (state.previewTimer) window.clearTimeout(state.previewTimer);
  state.previewTimer = null;
  state.previewURL = '';
  state.previewAttempts = 0;
  state.previewBusy = false;
  $('preview-frame').src = 'about:blank';
  $('preview-frame').hidden = true;
  $('preview-placeholder').hidden = false;
  $('preview-placeholder').querySelector('strong').textContent = 'Preparing preview';
  $('preview-placeholder').querySelector('span').textContent = 'Looking for the latest index.html export.';
  $('preview-pane').hidden = true;
}

async function requestPreview() {
  if (!state.taskRef || state.previewBusy || state.previewTimer || state.previewURL || state.status !== 'idle') return;
  if (state.previewAttempts >= 4) return;
  const taskRef = state.taskRef;
  state.previewBusy = true;
  state.previewAttempts += 1;
  setPreviewShell(state.previewAttempts === 1 ? 'Checking exported files…' : 'Waiting for the latest export…');
  try {
    const response = await fetch('./preview', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ task_ref: state.taskRef }),
      cache: 'no-store',
    });
    const result = await response.json();
    if (!response.ok) throw new Error(result.error || `HTTP ${response.status}`);
    if (state.taskRef !== taskRef || state.status !== 'idle') return;
    if (result.available && result.result_url) {
      showPreview(result.result_url);
      return;
    }
    if (state.previewAttempts < 4) {
      $('preview-status').textContent = 'Export is still settling…';
      state.previewTimer = window.setTimeout(() => { state.previewTimer = null; requestPreview(); }, 3000);
    } else {
      $('preview-status').textContent = 'No index.html preview in this round';
      $('preview-placeholder').querySelector('strong').textContent = 'No preview available';
      $('preview-placeholder').querySelector('span').textContent = 'This task did not publish an HTML result.';
    }
  } catch (error) {
    if (state.taskRef !== taskRef || state.status !== 'idle') return;
    $('preview-status').textContent = `Preview unavailable · ${short(error?.message || 'retry later', 80)}`;
    if (state.previewAttempts < 4) {
      state.previewTimer = window.setTimeout(() => { state.previewTimer = null; requestPreview(); }, 3000);
    }
  } finally {
    state.previewBusy = false;
  }
}

function applySelectedTask() {
  const task = state.selectedTask;
  const title = task ? taskTitle(task) : state.taskRef ? `Session ${state.taskRef.slice(-8)}` : 'Choose a session';
  $('stage-title').textContent = title;
  $('session-id').textContent = state.taskRef ? state.taskRef.slice(-18) : 'waiting';
  $('runtime').textContent = runtimeLabel(task);
  if (task) setStatus(task.status);
  renderSessionList();
  // The session list is polled independently from the event stream. If the
  // SSE connection misses the running -> idle boundary, this still starts the
  // same idle-round artifact lookup without requiring the page to be reopened.
  if (task?.status === 'idle') requestPreview();
}

function resetView() {
  clearPreview();
  state.stopReason = 'unknown';
  state.events = [];
  state.seen.clear();
  state.total = 0;
  state.expanded.clear();
  $('timeline').innerHTML = '<div id="empty" class="empty-state"><div class="empty-icon">✦</div><strong>Listening to the agent</strong><span>Events will appear as this session runs.</span></div>';
  $('event-count').textContent = '0';
  $('run-summary-count').textContent = '0 steps';
  $('time-range').textContent = '—';
  setStatus(state.selectedTask?.status || 'ready');
}

function renderSessionList() {
  const tasks = [...state.sessions.values()];
  const live = tasks.filter((task) => task.status === 'running' || task.status === 'rescheduling').length;
  $('session-list-count').textContent = `${tasks.length}${live ? ` · ${live} live` : ''}`;
  const list = $('session-list');
  if (!tasks.length) {
    list.innerHTML = '<div class="list-empty"><span class="list-empty-mark">◌</span><strong>No sessions yet</strong><span>New tasks will appear here.</span></div>';
    return;
  }
  list.innerHTML = '';
  for (const task of tasks) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = `session-item ${task.task_ref === state.taskRef ? 'selected' : ''}`;
    const statusClass = taskStatusClass(task.status);
    button.innerHTML = `<div class="session-item-head"><strong class="session-item-title">${esc(taskTitle(task))}</strong><span class="session-item-status ${statusClass}"><i></i>${esc(taskStatus(task.status))}</span></div><span class="session-item-summary">${esc(runtimeLabel(task))} · ${esc(ageOf(task.updated_at))}</span><div class="session-item-meta"><code>${esc(task.task_ref)}</code><time>${esc(timeOf(task.created_at))}</time></div>`;
    button.addEventListener('click', () => selectTask(task.task_ref));
    list.appendChild(button);
  }
}

function showListError(error) {
  $('session-sync').textContent = 'index unavailable';
  if (!state.sessions.size) $('session-list').innerHTML = `<div class="list-error">Could not refresh sessions.<br>${esc(short(error?.message || 'control service unavailable', 120))}</div>`;
}

async function refreshSessions() {
  if (state.listBusy) return;
  state.listBusy = true;
  try {
    const response = await fetch('./sessions?limit=20', { cache: 'no-store' });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    const page = await response.json();
    state.sessions = new Map((page.data || []).map((task) => [task.task_ref, task]));
    $('session-sync').textContent = `${page.partial ? "Some runtimes unavailable · " : ""}updated ${timeOf(page.queried_at)}`;
    if (state.taskRef && state.sessions.has(state.taskRef)) {
      state.selectedTask = state.sessions.get(state.taskRef);
    } else if (!state.taskRef && page.data?.length) {
      selectTask(page.data[0].task_ref);
      return;
    }
    applySelectedTask();
  } catch (error) {
    showListError(error);
  } finally {
    state.listBusy = false;
  }
}

function selectTask(taskRef) {
  if (!taskRef) return;
  const changed = state.taskRef !== taskRef;
  state.taskRef = taskRef;
  state.selectedTask = state.sessions.get(taskRef) || null;
  updateURL(taskRef);
  renderSessionList();
  if (!changed) {
    applySelectedTask();
    return;
  }
  state.source?.close();
  state.source = null;
  resetView();
  applySelectedTask();
  connectEvents(taskRef);
}

function makeToolRecord(event) {
  return {
    id: event.tool_use_id || event.id || `tool-${Math.random()}`,
    event,
    result: null,
  };
}

function normalizeSegments() {
  const toolRecords = new Map();
  for (const event of state.events) {
    if (event.type === 'agent.tool_use') toolRecords.set(event.tool_use_id || event.id, makeToolRecord(event));
  }
  const segments = [];
  let current = null;
  const flush = () => { if (current) segments.push(current); current = null; };
  for (const event of state.events) {
    if (event.type === 'session.status_idle' || event.type === 'session.status_running' || event.type === 'session.status_rescheduled' || event.type === 'session.status_terminated' || event.type === 'session.created' || event.type.startsWith('span.')) continue;
    if (event.type === 'agent.tool_result') {
      const record = toolRecords.get(event.tool_use_id);
      if (record) { record.result = event; continue; }
      if (!current || current.type !== 'work') { flush(); current = { type: 'work', items: [] }; }
      current.items.push({ event, result: event });
      continue;
    }
    if (event.type === 'agent.tool_use') {
      if (!current || current.type !== 'work') { flush(); current = { type: 'work', items: [] }; }
      const record = toolRecords.get(event.tool_use_id || event.id) || makeToolRecord(event);
      current.items.push(record);
      continue;
    }
    if (event.type === 'agent.thinking') {
      if (!current || current.type !== 'reasoning') { flush(); current = { type: 'reasoning', events: [] }; }
      current.events.push(event);
      continue;
    }
    if (event.type === 'user.message') { flush(); segments.push({ type: 'user', event }); continue; }
    if (event.type === 'agent.message') { flush(); segments.push({ type: 'message', event }); continue; }
    if (textOf(event)) { flush(); segments.push({ type: 'unknown', event }); }
  }
  flush();
  return segments;
}

function iconForTool(name) {
  const normalized = name.toLowerCase();
  if (normalized.includes('bash') || normalized.includes('command') || normalized.includes('shell')) return '›_';
  if (normalized.includes('read') || normalized.includes('search') || normalized.includes('grep')) return '⌕';
  if (normalized.includes('write') || normalized.includes('edit') || normalized.includes('patch')) return '✎';
  return '✦';
}

function workItemTitle(record) {
  const event = record.event;
  const name = toolName(event);
  const target = toolTarget(event);
  const normalized = name.toLowerCase();
  let verb = 'Used';
  if (normalized.includes('bash') || normalized.includes('command') || normalized.includes('shell')) verb = record.result ? 'Ran' : 'Running';
  else if (normalized.includes('read') || normalized.includes('search') || normalized.includes('grep')) verb = record.result ? 'Read' : 'Reading';
  else if (normalized.includes('write')) verb = record.result ? 'Wrote' : 'Writing';
  else if (normalized.includes('edit') || normalized.includes('patch')) verb = record.result ? 'Edited' : 'Editing';
  else if (!record.result) verb = 'Using';
  return `${verb} ${target || name}`;
}

function workTitle(items) {
  const usable = items.filter((item) => item.event?.type === 'agent.tool_use');
  if (usable.length === 1) return workItemTitle(usable[0]);
  const names = usable.map((item) => toolName(item.event).toLowerCase());
  if (names.length && names.every((name) => name.includes('bash') || name.includes('command') || name.includes('shell'))) return `Ran ${names.length} commands`;
  if (names.length && names.every((name) => name.includes('read') || name.includes('search') || name.includes('grep'))) return 'Loaded context';
  return `Used ${items.length} tools`;
}

function workProgress(items) {
  const finished = items.filter((item) => item.result && !item.result.is_error).length;
  const failed = items.filter((item) => item.result?.is_error).length;
  const running = items.length - finished - failed;
  if (failed) return `${failed} failed`;
  if (running) return `${finished ? `${finished} done · ` : ''}${running} running`;
  return `${finished} done`;
}

function workStatus(items) {
  if (items.some((item) => item.result?.is_error)) return 'error';
  if (items.some((item) => !item.result)) return 'running';
  return 'done';
}

function renderReasoning(segment, index) {
  const id = `reasoning-${index}`;
  const content = segment.events.map(textOf).filter(Boolean).join('\n\n');
  const preview = short(content.split('\n').find(Boolean) || 'about next steps', 90);
  const expanded = state.expanded.has(id);
  return `<article class="timeline-item work-block reasoning-block ${expanded ? 'expanded' : ''}" data-expand-id="${id}"><button class="work-header" type="button" aria-expanded="${expanded}"><span class="work-chevron">›</span><span class="work-icon">✦</span><span class="work-title">Reasoned</span><span class="reasoning-preview">${esc(preview)}</span><span class="work-progress">${segment.events.length} done</span><span class="work-status">✓</span></button><div class="work-detail"><div class="reasoning-detail">${textHtml(content)}</div></div></article>`;
}

function renderWork(segment, index) {
  const id = `work-${index}`;
  const status = workStatus(segment.items);
  const expanded = state.expanded.has(id) || status === 'running' || status === 'error';
  const items = segment.items.map((item) => {
    const result = item.result;
    const output = result ? textOf(result) : '';
    const check = result ? (result.is_error ? '!' : '✓') : '·';
    const checkClass = result?.is_error ? ' error' : '';
    return `<div class="work-item"><span class="work-item-icon">${esc(iconForTool(toolName(item.event)))}</span><span class="work-item-copy"><span class="work-item-title">${esc(workItemTitle(item))}</span>${output ? `<span class="work-item-output">${esc(output)}</span>` : ''}</span><span class="work-item-check${checkClass}">${check}</span></div>`;
  }).join('');
  return `<article class="timeline-item work-block ${expanded ? 'expanded' : ''}" data-expand-id="${id}"><button class="work-header" type="button" aria-expanded="${expanded}"><span class="work-chevron">›</span><span class="work-icon">${esc(iconForTool(toolName(segment.items[0]?.event || {})))}</span><span class="work-title">${esc(workTitle(segment.items))}</span><span class="work-progress">${esc(workProgress(segment.items))}</span><span class="work-status ${status}">${status === 'done' ? '✓' : status === 'error' ? '!' : '…'}</span></button><div class="work-detail">${items}</div></article>`;
}

function renderSegment(segment, index) {
  if (segment.type === 'user') {
    const content = textOf(segment.event);
    return `<article class="timeline-item user-row"><div class="user-message"><div class="message-meta">You · ${esc(timeOf(segment.event.processed_at || segment.event.created_at))}</div>${messageHtml(content)}</div></article>`;
  }
  if (segment.type === 'message') return `<article class="timeline-item agent-message">${messageHtml(textOf(segment.event))}</article>`;
  if (segment.type === 'reasoning') return renderReasoning(segment, index);
  if (segment.type === 'work') return renderWork(segment, index);
  return `<article class="timeline-item unknown-item">${messageHtml(textOf(segment.event))}</article>`;
}

function renderTimeline() {
  const segments = normalizeSegments();
  const container = $('timeline');
  const wasAtBottom = container.scrollHeight - container.scrollTop - container.clientHeight < 80;
  if (!segments.length) {
    container.innerHTML = '<div id="empty" class="empty-state"><div class="empty-icon">✦</div><strong>Listening to the agent</strong><span>Events will appear as this session runs.</span></div>';
  } else {
    container.innerHTML = segments.map(renderSegment).join('');
    container.querySelectorAll('[data-expand-id] .work-header').forEach((button) => {
      button.addEventListener('click', () => {
        const block = button.closest('[data-expand-id]');
        const id = block.dataset.expandId;
        if (state.expanded.has(id)) state.expanded.delete(id); else state.expanded.add(id);
        renderTimeline();
      });
    });
  }
  $('event-count').textContent = String(segments.length);
  $('run-summary-count').textContent = `${segments.length} ${segments.length === 1 ? 'step' : 'steps'}`;
  if (wasAtBottom || state.lastRenderAt === 0) container.scrollTop = container.scrollHeight;
  state.lastRenderAt = Date.now();
}

function applySnapshot(snapshot) {
  if (snapshot.status) {
    if (snapshot.status === 'running' || snapshot.status === 'rescheduling') clearPreview();
    setStatus(snapshot.status, snapshot.stop_reason);
    if (state.selectedTask) state.selectedTask = { ...state.selectedTask, status: snapshot.status };
  }
  if (snapshot.started_at) $('time-range').textContent = `${timeOf(snapshot.started_at)} — ${timeOf(snapshot.ended_at)}`;
  if (snapshot.result_url && state.status === 'idle') showPreview(snapshot.result_url);
  if (snapshot.status === 'idle') requestPreview();
}

function applyMeta(meta) {
  state.total = Number(meta.eventCount || 0);
  $('runtime').textContent = runtimeLabel(meta);
  $('source').textContent = meta.source || 'AgentAPI session events';
  if (meta.startedAt) $('time-range').textContent = `${timeOf(meta.startedAt)} — ${timeOf(meta.endedAt)}`;
  if (meta.taskRef === state.taskRef && meta.title) {
    state.selectedTask = { ...state.selectedTask, task_ref: meta.taskRef, title: meta.title, runtime: meta.runtime, model: meta.model, status: state.status };
    applySelectedTask();
  }
}

function addEvent(event) {
  if (!event || typeof event !== 'object' || (event.id && state.seen.has(event.id))) return;
  if (event.id) state.seen.add(event.id);
  state.events.push(event);
  if (event.type === 'session.status_running' || event.type === 'session.status_rescheduled') {
    clearPreview();
    setStatus('running');
    state.previewAttempts = 0;
  }
  if (event.type === 'user.message') {
    clearPreview();
    state.previewAttempts = 0;
  }
  if (event.type === 'session.status_idle') {
    setStatus('idle', event.stop_reason);
    requestPreview();
  }
  if (event.type === 'session.status_terminated') setStatus('terminated');
  renderTimeline();
}

$('preview-close').addEventListener('click', () => {
  $('preview-pane').hidden = true;
});

function connectEvents(taskRef) {
  if (!taskRef) return;
  const source = new EventSource(`./events?task_ref=${encodeURIComponent(taskRef)}`);
  state.source = source;
  let checkingReference = false;
  source.addEventListener('device-snapshot', (event) => { if (state.taskRef === taskRef) applySnapshot(JSON.parse(event.data)); });
  source.addEventListener('session-meta', (event) => { if (state.taskRef === taskRef) applyMeta(JSON.parse(event.data)); });
  source.addEventListener('session-event', (event) => { if (state.taskRef === taskRef) { try { addEvent(JSON.parse(event.data)); } catch (error) { console.error(error); } } });
  source.onopen = () => {
    if (state.taskRef !== taskRef) return;
    $('connection').textContent = 'SSE connected';
    $('sidebar-connection').textContent = 'connected';
  };
  source.onerror = () => {
    if (state.taskRef !== taskRef) return;
    $('connection').textContent = 'SSE reconnecting';
    $('sidebar-connection').textContent = 'reconnecting';
    if (!checkingReference) {
      checkingReference = true;
      fetch(`./events?task_ref=${encodeURIComponent(taskRef)}`, { method: 'HEAD', cache: 'no-store' })
        .then((response) => {
          if (state.source !== source || state.taskRef !== taskRef) return;
          if ([400, 403, 404].includes(response.status)) {
            source.close();
            $('connection').textContent = 'Task unavailable · select a task again';
            $('sidebar-connection').textContent = 'unavailable';
          }
        }).catch(() => {}).finally(() => { checkingReference = false; });
    }
  };
}

function boot() {
  resetView();
  if (state.taskRef) { applySelectedTask(); connectEvents(state.taskRef); }
  refreshSessions();
  window.setInterval(() => { if (!document.hidden) refreshSessions(); }, 3000);
}

window.setInterval(() => { $('clock').textContent = new Date().toLocaleTimeString('zh-CN', { hour12: false }); }, 1000);
$('clock').textContent = new Date().toLocaleTimeString('zh-CN', { hour12: false });
boot();
