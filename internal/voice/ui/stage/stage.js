const $ = id => document.getElementById(id);
const labels = {
  offline: '小灵安静地陪伴你', idle: '小灵在等你说话', listening: '小灵正在聆听',
  thinking: '小灵正在思考', speaking: '小灵正在说话',
};
let currentURL = '', lastUpdate = 0;
let speechTurn = 0;
function speech(text = '', turn = 0) {
  const value = typeof text === 'string' ? text.trim() : '';
  const paragraph = $('speech-text');
  if (paragraph.textContent !== value || speechTurn !== turn) {
    paragraph.textContent = value;
    paragraph.scrollTop = 0;
    speechTurn = turn;
  }
  $('speech').hidden = !value;
}
function mouth(level) {
  const volume = Number.isFinite(level) ? Math.max(0, Math.min(1, level)) : 0;
  const opening = volume > .035 ? 6 + volume * 26 : 0;
  $('mouth').setAttribute('opacity', opening ? '1' : '0');
  // Follow the original painted smile; only the jaw opening varies with playback.
  $('mouth-opening').setAttribute('d', `M330 575 Q350 619 396 601 Q441 619 462 575 Q450 ${611 + opening} 396 ${616 + opening} Q342 ${611 + opening} 330 575Z`);
  $('tongue').setAttribute('d', `M370 ${615 + opening * .6} Q396 ${606 + opening * .6} 422 ${615 + opening * .6} Q396 ${625 + opening * .8} 370 ${615 + opening * .6}Z`);
}
function phase(value) {
  const state = Object.hasOwn(labels, value) ? value : 'offline';
  document.body.dataset.phase = state;
  document.querySelector('.avatar').setAttribute('aria-label', labels[state]);
  return state;
}
function render(s) {
  lastUpdate = performance.now();
  const speaking = phase(s.phase) === 'speaking';
  mouth(speaking ? s.level : 0);
  speech(speaking ? s.text : '', s.turn_id);
  const url = s.view_url || '';
  document.body.classList.toggle('has-content', Boolean(url));
  $('content').hidden = !url;
  if (url !== currentURL) {
    currentURL = url;
    if (url) $('content').src = url;
    else $('content').removeAttribute('src');
  }
}
const events = new EventSource('/api/stage/events');
events.onmessage = ({data}) => { try { render(JSON.parse(data)); } catch { mouth(0); } };
events.onerror = () => { mouth(0); speech(); phase('offline'); };
// A lost playback reporter must never leave the character's mouth open.
setInterval(() => { if (performance.now() - lastUpdate > 1500) mouth(0); }, 400);
