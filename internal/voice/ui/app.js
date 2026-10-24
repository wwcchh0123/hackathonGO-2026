const $ = id => document.getElementById(id);
let ws, audio, mic, source, filter, stream, timer;
let ready = false, recording = false, stopping = false, preparing = false;
let outputRate = 24000, nextPlay = 0, ttsStopped = true, stopResolve;
const playing = new Set();

function error(text = '') { $('error').textContent = text; $('error').hidden = !text; }
function state(text, kind = '') {
  $('status').textContent = text;
  $('indicator').className = `dot ${ready ? 'ready' : ''}`;
  $('orb').className = `orb ${kind}`;
}
function controls() {
  $('record').disabled = !ready || preparing || stopping;
  $('record').textContent = recording ? '说完了，发送' : '开始说话';
  $('interrupt').disabled = !ready || recording || preparing || stopping;
}
function message(label, text, kind = '') {
  $('messages').querySelector('.empty')?.remove();
  const el = document.createElement('p'); el.className = `message ${kind}`;
  const name = document.createElement('b'); name.textContent = label;
  el.append(name, document.createTextNode(text)); $('messages').append(el);
  while ($('messages').children.length > 200) $('messages').firstChild.remove();
  $('messages').scrollTop = $('messages').scrollHeight;
}
function clearPlayback() {
  for (const node of playing) { node.onended = null; try { node.stop(); } catch {} }
  playing.clear(); nextPlay = 0; ttsStopped = true;
}
function releaseMic() {
  clearTimeout(timer);
  stream?.getTracks().forEach(track => track.stop()); stream = null;
  source?.disconnect(); source = null;
  filter?.disconnect(); filter = null;
  mic?.disconnect(); if (mic) mic.port.close(); mic = null;
  recording = false; stopping = false;
}
function send(value) {
  if (ws?.readyState !== WebSocket.OPEN) throw new Error('连接已断开，请重新连接');
  ws.send(JSON.stringify(value));
}
async function ensureAudio() {
  if (!audio || audio.state === 'closed') audio = new AudioContext();
  await audio.resume();
  if (audio.state !== 'running') throw new Error('浏览器未启用音频，请再次点击连接');
}
function play(data) {
  if (!audio || recording || preparing || stopping) return;
  if (data.byteLength % 2) throw new Error('收到无效音频帧');
  if (nextPlay - audio.currentTime > 30) throw new Error('播放积压过多，请重新连接');
  const pcm = new DataView(data);
  const buffer = audio.createBuffer(1, data.byteLength / 2, outputRate);
  const values = buffer.getChannelData(0);
  for (let i = 0; i < values.length; i++) values[i] = pcm.getInt16(i * 2, true) / 32768;
  const node = audio.createBufferSource(); node.buffer = buffer; node.connect(audio.destination);
  nextPlay = Math.max(nextPlay, audio.currentTime + 0.04);
  playing.add(node);
  node.onended = () => { playing.delete(node); if (ttsStopped && !playing.size && !recording) state('已连接 · 等你说话'); };
  node.start(nextPlay); nextPlay += buffer.duration;
}
async function poll() {
  if (ws) return;
  try {
    const result = await fetch('/api/status');
    if (!result.ok) throw new Error('无法读取设备状态');
    const s = await result.json();
    $('device').textContent = s.device_id;
    $('activation').hidden = s.stage !== 'activation'; $('code').textContent = s.code || '';
    $('connect').disabled = s.stage !== 'paired' || !s.audio_available;
    if (s.stage === 'activation') state('等待设备绑定');
    else if (s.stage === 'paired') state('设备已绑定 · 可以连接');
    else if (s.stage === 'error') { state('网关连接失败 · 正在重试'); error(s.error); }
    else state('正在连接设备网关…');
    if (!s.audio_available) error('音频模块未启用，请使用 make voice 启动。');
  } catch (e) { state('本地服务不可用'); $('connect').disabled = true; error(e.message); }
}
async function connect() {
  if (ws) { ws.close(); return; }
  $('connect').disabled = true; error();
  try {
    await ensureAudio();
    const socket = new WebSocket(`ws://${location.host}/api/voice`); ws = socket; socket.binaryType = 'arraybuffer';
    state('正在连接智能体…'); $('connect').textContent = '断开'; $('connect').disabled = false;
    socket.onmessage = ({ data }) => {
      try {
        if (typeof data !== 'string') { play(data); return; }
        const m = JSON.parse(data);
        if (m.type === 'ready') {
          ready = true; outputRate = m.sample_rate; $('activation').hidden = true;
          state('已连接 · 等你说话'); $('hint').textContent = '点击开始说话，说完点击发送。'; controls();
        } else if (m.type === 'activation') {
          $('activation').hidden = false; $('code').textContent = m.code; state('等待设备绑定');
        } else if (m.type === 'stt') message('你', m.text, 'user');
        else if (m.type === 'tts') {
          if (recording || preparing || stopping) return;
          if (m.state === 'start') { ttsStopped = false; state('智能体正在说话', 'speaking'); }
          if (m.state === 'sentence_start' && m.text) message('灵矽', m.text);
          if (m.state === 'stop') {
            ttsStopped = true;
            if (m.is_aborted) clearPlayback();
            if (!playing.size) state('已连接 · 等你说话');
          }
        } else if (m.type === 'notify' && m.event === 'config_updated') {
          error('智能体配置已更新，请断开后重新连接以生效。');
        } else if (m.type === 'ended') { error(); socket.close(); }
        else if (m.type === 'error') { error(m.text); socket.close(); }
      } catch (e) { error(e.message); socket.close(); }
    };
    socket.onerror = () => error('连接失败。请检查本地服务；同一设备只能打开一个语音窗口。');
    socket.onclose = () => {
      if (ws !== socket) return;
      ready = false; ws = null; releaseMic(); clearPlayback(); preparing = false;
      if (stopResolve) { stopResolve(); stopResolve = null; }
      controls(); state('连接已断开'); $('connect').textContent = '重新连接';
      $('hint').textContent = '点击重新连接，继续与智能体对话。'; poll();
    };
  } catch (e) { error(e.message); $('connect').disabled = false; }
}
async function start() {
  if (!ready || preparing || stopping) return;
  preparing = true; controls(); clearPlayback(); error();
  const socket = ws;
  try {
    await ensureAudio();
    const captured = await navigator.mediaDevices.getUserMedia({ audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true }, video: false });
    if (!ready || ws !== socket) { captured.getTracks().forEach(t => t.stop()); return; }
    stream = captured;
    await audio.audioWorklet.addModule('/mic-worklet.js');
    if (!ready || ws !== socket) { releaseMic(); return; }
    source = audio.createMediaStreamSource(stream);
    filter = audio.createBiquadFilter(); filter.type = 'lowpass'; filter.frequency.value = 7200;
    mic = new AudioWorkletNode(audio, 'linx-microphone', { numberOfInputs: 1, numberOfOutputs: 1, outputChannelCount: [1] });
    mic.port.onmessage = ({ data }) => {
      if (data.type === 'stopped') { stopResolve?.(); stopResolve = null; return; }
      if (data.type !== 'pcm' || !recording || ws !== socket || socket.readyState !== WebSocket.OPEN) return;
      if (socket.bufferedAmount > 1920 * 16) { error('网络发送积压，已停止录音，请重新连接。'); socket.close(); releaseMic(); return; }
      socket.send(data.frame);
    };
    mic.onprocessorerror = () => { error('录音处理失败，请重新连接。'); socket.close(); releaseMic(); };
    send({ type: 'start' }); recording = true;
    source.connect(filter); filter.connect(mic); mic.connect(audio.destination);
    mic.port.postMessage('start');
    state('正在录音 · 说完点击发送', 'recording');
    $('hint').textContent = '正在聆听…'; timer = setTimeout(stop, 60000);
  } catch (e) {
    releaseMic();
    const denied = e.name === 'NotAllowedError';
    error(denied ? '麦克风权限未开启。请在浏览器地址栏的网站设置中允许麦克风，再点击开始说话。' : `无法录音：${e.message}`);
    state(ready ? '已连接 · 等你说话' : '连接已断开');
  } finally { preparing = false; controls(); }
}
async function stop() {
  if (!recording || stopping) return;
  stopping = true; controls(); clearTimeout(timer);
  const socket = ws;
  try {
    // Flush the final partial frame before sending listen.stop.
    await new Promise((resolve, reject) => {
      const timeout = setTimeout(() => { stopResolve = null; reject(new Error('录音结束超时，请重新连接')); }, 2000);
      stopResolve = () => { clearTimeout(timeout); resolve(); };
      mic.port.postMessage('stop');
    });
    if (ready && ws === socket) { send({ type: 'stop' }); state('正在理解你的话…'); $('hint').textContent = '等待智能体回复，也可以继续说话。'; }
  } catch (e) { error(e.message); socket?.close(); }
  finally { releaseMic(); controls(); }
}
$('connect').onclick = connect;
$('record').onclick = () => recording ? stop() : start();
$('interrupt').onclick = () => { try { send({ type: 'abort' }); clearPlayback(); state('已停止播报 · 可以继续说话'); } catch (e) { error(e.message); } };
$('clear').onclick = () => { $('messages').replaceChildren(); };
window.addEventListener('pagehide', () => { releaseMic(); ws?.close(); });
document.addEventListener('visibilitychange', () => { if (document.hidden && recording) stop(); });
setInterval(poll, 3000); poll();
